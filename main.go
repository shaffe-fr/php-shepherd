package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// version is set at build time via ldflags.
var version = "dev"

// verbose controls whether extra diagnostic output is printed.
var verbose bool

// quiet suppresses non-essential output.
var quiet bool

// httpClient is the shared HTTP client with a sensible timeout.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// phpDirRe matches a Herd PHP install dir name like "php84" or "php810".
var phpDirRe = regexp.MustCompile(`^php(\d)(\d+)$`)

// phpDirVersion returns a comparable integer for a Herd PHP dir
// (e.g. "php84" -> 8004, "php810" -> 8010). Returns -1 if it doesn't match.
func phpDirVersion(dir string) int {
	m := phpDirRe.FindStringSubmatch(filepath.Base(dir))
	if len(m) != 3 {
		return -1
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major*1000 + minor
}

// versionRe validates that a .phpversion file contains a proper version string.
var versionRe = regexp.MustCompile(`^\d+\.\d+$`)

// herdHome returns the Herd bin directory.
func herdHome() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "bin")
}

// checkHerd returns true if Herd appears to be installed (bin directory exists).
func checkHerd() bool {
	info, err := os.Stat(herdHome())
	return err == nil && info.IsDir()
}

// requireHerd exits with a clear message if Herd is not installed.
func requireHerd() {
	if !checkHerd() {
		fmt.Fprintf(os.Stderr, "Shepherd requires Laravel Herd for Windows.\n")
		fmt.Fprintf(os.Stderr, "Install it from https://herd.laravel.com\n")
		os.Exit(1)
	}
}

// findPHPVersion walks up from dir looking for a .phpversion file.
// Returns the version string (e.g. "8.5") or empty if not found.
func findPHPVersion(dir string) string {
	for {
		candidate := filepath.Join(dir, ".phpversion")
		data, err := os.ReadFile(candidate)
		if err == nil {
			ver := strings.TrimSpace(string(data))
			if versionRe.MatchString(ver) {
				return ver
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// resolveFromVersion returns the php.exe path for a given version like "8.5".
func resolveFromVersion(ver string) (string, error) {
	if !versionRe.MatchString(ver) {
		return "", fmt.Errorf("invalid PHP version format: %q", ver)
	}
	nodot := strings.ReplaceAll(ver, ".", "")
	php := filepath.Join(herdHome(), "php"+nodot, "php.exe")
	if _, err := os.Stat(php); err != nil {
		return "", fmt.Errorf("php %s not found at %s", ver, php)
	}
	return php, nil
}

// mostRecentPHP finds the highest-versioned php.exe under Herd.
func mostRecentPHP() (string, error) {
	pattern := filepath.Join(herdHome(), "php*")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("no PHP installations found in %s", herdHome())
	}
	// Sort by numeric PHP version (descending) so php810 (8.10) ranks above php84 (8.4).
	sort.Slice(matches, func(i, j int) bool {
		return phpDirVersion(matches[i]) > phpDirVersion(matches[j])
	})
	for _, m := range matches {
		php := filepath.Join(m, "php.exe")
		if _, err := os.Stat(php); err == nil {
			return php, nil
		}
	}
	return "", fmt.Errorf("no php.exe found in %s", herdHome())
}

// whichPHP resolves the PHP executable via herd.phar which-php.
func whichPHP(bootstrapPHP, dir string) (string, error) {
	herdPhar := filepath.Join(herdHome(), "herd.phar")
	cmd := exec.Command(bootstrapPHP, herdPhar, "which-php", dir)
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("which-php failed: %w", err)
	}
	php := strings.TrimSpace(string(out))
	if php == "" {
		return "", fmt.Errorf("which-php returned empty")
	}
	// Validate that the returned path is under herdHome.
	// Resolve symlinks/junctions to prevent path traversal via symlink tricks.
	absPhp, err := filepath.Abs(php)
	if err != nil {
		return "", fmt.Errorf("invalid path from which-php: %w", err)
	}
	// EvalSymlinks resolves symlinks AND cleans the path; if the target doesn't exist
	// it fails, which is fine (we want an existing php.exe).
	realPhp, err := filepath.EvalSymlinks(absPhp)
	if err != nil {
		return "", fmt.Errorf("cannot resolve which-php path: %w", err)
	}
	absHerd, _ := filepath.Abs(herdHome())
	realHerd, _ := filepath.EvalSymlinks(absHerd)
	realPhpLower := strings.ToLower(realPhp)
	realHerdLower := strings.ToLower(realHerd)
	sep := string(os.PathSeparator)
	// Ensure the returned path is the Herd dir itself or strictly inside it.
	// A plain HasPrefix check would wrongly accept siblings like "...\bin-evil".
	if realPhpLower != realHerdLower && !strings.HasPrefix(realPhpLower, realHerdLower+sep) {
		return "", fmt.Errorf("which-php returned path outside Herd directory: %s", php)
	}
	return realPhp, nil
}

// extractVersion extracts the PHP version from a path like .../php84/php.exe -> "8.4"
func extractVersion(phpPath string) string {
	dir := filepath.Base(filepath.Dir(phpPath))
	m := phpDirRe.FindStringSubmatch(dir)
	if len(m) == 3 {
		return m[1] + "." + m[2]
	}
	return ""
}

// syncNginx updates the Herd nginx config if needed, then restarts nginx.
// It modifies the conf file inline and triggers a non-blocking restart.
func syncNginx(projectDir, version string) {
	folderName := filepath.Base(projectDir)
	site := folderName + ".test"
	confPath := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "valet", "Nginx", site+".conf")

	// Bail if no conf file
	data, err := os.ReadFile(confPath)
	if err != nil {
		return
	}
	content := string(data)

	// Check if already up-to-date
	if strings.Contains(content, "ISOLATED_PHP_VERSION="+version) {
		return
	}

	// Bail if conf is empty (don't overwrite with regex on empty content)
	if len(strings.TrimSpace(content)) == 0 {
		return
	}

	// Update ISOLATED_PHP_VERSION comment
	reIsolated := regexp.MustCompile(`(?m)^# ISOLATED_PHP_VERSION=.*$`)
	content = reIsolated.ReplaceAllString(content, "# ISOLATED_PHP_VERSION="+version)

	// Update herd_sock references (with or without version suffix)
	nodot := strings.ReplaceAll(version, ".", "")
	reSock := regexp.MustCompile(`\$herd_sock(?:_\d+)?`)
	content = reSock.ReplaceAllString(content, "$herd_sock_"+nodot)

	// Repair empty fastcgi_pass directives (left behind by previous buggy rewrites)
	reEmptyPass := regexp.MustCompile(`(?m)(fastcgi_pass)\s*;`)
	content = reEmptyPass.ReplaceAllString(content, "fastcgi_pass $herd_sock_"+nodot+";")

	// Write back
	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		return
	}

	// Restart nginx via herd.phar (detached process, non-blocking).
	bootstrap, err := mostRecentPHP()
	if err != nil {
		return
	}
	herdPhar := filepath.Join(herdHome(), "herd.phar")
	cmd := exec.Command(bootstrap, herdPhar, "restart", "nginx")
	cmd.Stdout = nil
	cmd.Stderr = nil
	// CREATE_NO_WINDOW | DETACHED_PROCESS: the child process runs independently
	// and won't be affected if the parent exits immediately after.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000008}
	if err := cmd.Start(); err == nil {
		// Release the process handle so it can outlive us without leaking handles.
		_ = cmd.Process.Release()
	}
}

// rewriteXdebugArgs rewrites xdebug DLL paths and strips -n flag.
// Only rewrites arguments that reference the xdebug directory or contain
// a zend_extension directive pointing to an xdebug DLL.
func rewriteXdebugArgs(args []string, version string) []string {
	// Without a known version we can't build a valid DLL name; only strip -n.
	if version == "" {
		var result []string
		for _, arg := range args {
			if arg == "-n" {
				continue
			}
			result = append(result, arg)
		}
		return result
	}
	xdebugDir := filepath.Join(os.Getenv("PROGRAMFILES"), "Herd", "resources", "app.asar.unpacked", "resources", "bin", "xdebug")
	xdebugDirLower := strings.ToLower(xdebugDir)
	dlls, _ := filepath.Glob(filepath.Join(xdebugDir, "xdebug-*.dll"))

	var result []string
	for _, arg := range args {
		if arg == "-n" {
			continue
		}
		// Only rewrite args that actually reference the xdebug directory
		// (e.g. -d zend_extension=C:\...\xdebug\xdebug-8.3.dll)
		if strings.Contains(strings.ToLower(arg), xdebugDirLower) || strings.Contains(strings.ToLower(arg), "xdebug") && strings.Contains(arg, "zend_extension") {
			for _, dll := range dlls {
				dllName := filepath.Base(dll)
				arg = strings.ReplaceAll(arg, dllName, "xdebug-"+version+".dll")
			}
		}
		result = append(result, arg)
	}
	return result
}

func main() {
	// Parse global flags (--verbose, --quiet) before anything else.
	// These are stripped from os.Args so subcommands don't see them.
	var cleanedArgs []string
	for _, arg := range os.Args {
		switch arg {
		case "--verbose":
			verbose = true
		case "--quiet", "-q":
			quiet = true
		default:
			cleanedArgs = append(cleanedArgs, arg)
		}
	}
	os.Args = cleanedArgs

	// Detect if called as "composer" (multicall binary)
	exe := strings.ToLower(filepath.Base(os.Args[0]))
	isComposer := strings.HasPrefix(exe, "composer")
	isShepherd := strings.HasPrefix(exe, "shp")

	// Handle subcommands (only when invoked as shp)
	if isShepherd {
		if len(os.Args) > 1 {
			switch os.Args[1] {
			case "use":
				cmdUse()
				return
			case "install":
				cmdInstall()
				return
			case "uninstall":
				cmdUninstall()
				return
			case "status":
				cmdStatus()
				return
			case "xdebug":
				cmdXdebug()
				return
			case "ext":
				cmdExt()
				return
			case "self-update":
				cmdSelfUpdate()
				return
			case "doctor":
				cmdDoctor()
				return
			case "list", "ls":
				cmdList()
				return
			case "version", "--version", "-v":
				fmt.Printf("shp %s\n", version)
				return
			}
		}
		fmt.Println("Shepherd - Per-project PHP on Windows, done right.")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  use         Set the PHP version for the current project (.phpversion)")
		fmt.Println("  list        List available PHP versions")
		fmt.Println("  status      Show current PHP version and configuration")
		fmt.Println("  xdebug      Manage xdebug for the current PHP version")
		fmt.Println("  ext         Add PHP extensions from PECL")
		fmt.Println("  install     Install php.exe and composer.exe shims and configure PATH")
		fmt.Println("  uninstall   Remove shims and restore PATH")
		fmt.Println("  doctor      Diagnose common issues with Shepherd setup")
		fmt.Println("  self-update Update Shepherd to the latest version")
		fmt.Println("  version     Show current Shepherd version")
		fmt.Println()
		fmt.Println("Global flags:")
		fmt.Println("  --verbose   Show extra diagnostic output")
		fmt.Println("  --quiet     Suppress non-essential output")
		fmt.Println()
		fmt.Println("When invoked as php.exe or composer.exe, acts as a transparent PHP version switcher.")
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "shp: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	requireHerd()

	var targetPHP string
	var version string
	fromDotfile := false

	// 1. Try .phpversion file (walk up directory tree)
	version = findPHPVersion(cwd)
	if version != "" {
		fromDotfile = true
		targetPHP, err = resolveFromVersion(version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "shp: %v\n", err)
			os.Exit(1)
		}
	} else {
		// 2. Fallback: ask herd.phar which-php
		bootstrap, berr := mostRecentPHP()
		if berr != nil {
			fmt.Fprintf(os.Stderr, "shp: %v\n", berr)
			os.Exit(1)
		}
		targetPHP, err = whichPHP(bootstrap, cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "shp: %v\n", err)
			os.Exit(1)
		}
		version = extractVersion(targetPHP)
	}

	// Resolve extension_dir
	extDir := filepath.Join(filepath.Dir(targetPHP), "ext")

	// Build args
	var cmdArgs []string

	if isComposer {
		// composer mode: php.exe composer.phar <user args>
		composerPhar := filepath.Join(herdHome(), "composer.phar")
		cmdArgs = []string{composerPhar}
		cmdArgs = append(cmdArgs, os.Args[1:]...)
	} else {
		// php mode: php.exe -d extension_dir=... <rewritten user args>
		userArgs := rewriteXdebugArgs(os.Args[1:], version)
		cmdArgs = []string{"-d", "extension_dir=" + extDir}
		cmdArgs = append(cmdArgs, userArgs...)
	}

	// Sync nginx config in background (if .phpversion exists)
	if fromDotfile {
		syncNginx(cwd, version)
	}

	// Exec PHP
	cmd := exec.Command(targetPHP, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

// shimDir returns the directory where php.exe and composer.exe shims are installed.
func shimDir() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "shepherd", "bin")
}

// getUserPath reads the User PATH from the registry.
func getUserPath() (string, uint32, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return "", 0, err
	}
	defer key.Close()
	val, valType, err := key.GetStringValue("Path")
	if err != nil {
		return "", 0, err
	}
	return val, valType, nil
}

// setUserPath writes the User PATH to the registry preserving the original type (REG_EXPAND_SZ or REG_SZ).
func setUserPath(path string, valType uint32) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	// Preserve the registry value type (REG_EXPAND_SZ if it was already, for %USERPROFILE% etc.)
	if valType == registry.EXPAND_SZ {
		return key.SetExpandStringValue("Path", path)
	}
	return key.SetStringValue("Path", path)
}

// broadcastSettingChange sends WM_SETTINGCHANGE to all top-level windows.
func broadcastSettingChange() {
	user32 := syscall.NewLazyDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")
	env, _ := syscall.UTF16PtrFromString("Environment")
	// HWND_BROADCAST=0xFFFF, WM_SETTINGCHANGE=0x001A, SMTO_ABORTIFHUNG=0x0002
	sendMessageTimeout.Call(0xFFFF, 0x001A, 0, uintptr(unsafe.Pointer(env)), 0x0002, 5000, 0)
}

// cmdList lists available PHP versions (dedicated command for discoverability).
func cmdList() {
	requireHerd()
	pattern := filepath.Join(herdHome(), "php*")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "No PHP versions found in %s\n", herdHome())
		os.Exit(1)
	}

	sort.Slice(matches, func(i, j int) bool {
		return phpDirVersion(matches[i]) < phpDirVersion(matches[j])
	})

	cwd, _ := os.Getwd()
	currentVersion := ""
	if cwd != "" {
		currentVersion = findPHPVersion(cwd)
	}

	fmt.Println("Available PHP versions:")
	fmt.Println()
	for _, m := range matches {
		dirName := filepath.Base(m)
		v := phpDirVersion(m)
		if v < 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(m, "php.exe")); err != nil {
			continue
		}
		dm := phpDirRe.FindStringSubmatch(dirName)
		if len(dm) != 3 {
			continue
		}
		ver := dm[1] + "." + dm[2]
		if ver == currentVersion {
			fmt.Printf("  → %s (active)\n", ver)
		} else {
			fmt.Printf("    %s\n", ver)
		}
	}
}

// cmdUse sets or displays the PHP version for the current project.
// Without arguments, lists available PHP versions.
// With a version argument, writes it to .phpversion in the current directory.
func cmdUse() {
	if len(os.Args) < 3 {
		// List available PHP versions
		pattern := filepath.Join(herdHome(), "php*")
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			fmt.Fprintf(os.Stderr, "No PHP versions found in %s\n", herdHome())
			os.Exit(1)
		}

		// Sort by version ascending
		sort.Slice(matches, func(i, j int) bool {
			return phpDirVersion(matches[i]) < phpDirVersion(matches[j])
		})

		// Determine current version for highlighting
		cwd, _ := os.Getwd()
		currentVersion := ""
		if cwd != "" {
			currentVersion = findPHPVersion(cwd)
		}

		fmt.Println("Available PHP versions:")
		fmt.Println()
		for _, m := range matches {
			dirName := filepath.Base(m)
			v := phpDirVersion(m)
			if v < 0 {
				continue
			}
			// Check php.exe exists
			if _, err := os.Stat(filepath.Join(m, "php.exe")); err != nil {
				continue
			}
			dm := phpDirRe.FindStringSubmatch(dirName)
			if len(dm) != 3 {
				continue
			}
			ver := dm[1] + "." + dm[2]
			if ver == currentVersion {
				fmt.Printf("  → %s (active)\n", ver)
			} else {
				fmt.Printf("    %s\n", ver)
			}
		}
		return
	}

	ver := os.Args[2]

	// Normalize: allow "84" or "810" as shorthand for "8.4" or "8.10"
	// Only accept 2-3 digit shorthands (major is always single digit for PHP).
	if !strings.Contains(ver, ".") && len(ver) >= 2 && len(ver) <= 3 {
		ver = ver[:1] + "." + ver[1:]
	}

	if !versionRe.MatchString(ver) {
		fmt.Fprintf(os.Stderr, "Error: invalid version format %q (expected X.Y, e.g. 8.4)\n", os.Args[2])
		os.Exit(1)
	}

	// Validate that this version is installed
	if _, err := resolveFromVersion(ver); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Write .phpversion in current directory
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	target := filepath.Join(cwd, ".phpversion")
	if err := os.WriteFile(target, []byte(ver+"\n"), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", target, err)
		os.Exit(1)
	}

	fmt.Printf("  ✓ .phpversion set to %s\n", ver)
}

// cmdInstall installs the shims and configures PATH.
func killShimProcesses(dir string) {
	// Use tasklist + findstr to locate processes running from the shim directory.
	// WMIC is deprecated and removed in Windows 11 24H2+.
	dir = strings.TrimRight(dir, `\`)
	dirLower := strings.ToLower(dir)

	// Get verbose process list (includes image path)
	out, err := exec.Command("tasklist", "/V", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return
	}

	// Also try wmic as fallback for older systems where tasklist /V doesn't show paths
	wmicOut, _ := exec.Command("wmic", "process", "get", "ProcessId,ExecutablePath", "/format:csv").Output()

	// Build PID→path map from wmic output (best effort)
	pidPath := make(map[int]string)
	for _, line := range strings.Split(string(wmicOut), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ",")
		if len(fields) >= 3 {
			path := strings.TrimSpace(fields[1])
			pid, err := strconv.Atoi(strings.TrimSpace(fields[2]))
			if err == nil && path != "" {
				pidPath[pid] = path
			}
		}
	}

	// Parse tasklist CSV output to get PIDs of processes with matching image names
	myPID := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// CSV format: "Image Name","PID",...
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		imageName := strings.Trim(fields[0], `"`)
		imageNameLower := strings.ToLower(imageName)
		// Only consider our shim executables
		if imageNameLower != "php.exe" && imageNameLower != "composer.exe" && imageNameLower != "shp.exe" {
			continue
		}
		pidStr := strings.Trim(fields[1], `"`)
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid == myPID {
			continue
		}
		// Verify the process path is inside our shim dir.
		// If we cannot determine the path (wmic unavailable on Win11 24H2+),
		// skip the process to avoid killing unrelated php.exe instances.
		p, ok := pidPath[pid]
		if !ok {
			// Try PowerShell as fallback to get the process path
			psOut, psErr := exec.Command("powershell", "-NoProfile", "-Command",
				fmt.Sprintf("(Get-Process -Id %d -ErrorAction SilentlyContinue).Path", pid)).Output()
			if psErr == nil {
				psPath := strings.TrimSpace(string(psOut))
				if psPath != "" {
					p = psPath
					ok = true
				}
			}
		}
		if !ok {
			// Cannot determine process path — skip to avoid killing unrelated processes
			continue
		}
		if !strings.HasPrefix(strings.ToLower(p), dirLower+`\`) {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = proc.Kill()
		fmt.Printf("  ✓ Killed shim process (PID %d)\n", pid)
	}
}

func cmdInstall() {
	dir := shimDir()

	// Parse --force flag
	force := false
	for _, arg := range os.Args[2:] {
		if arg == "--force" || arg == "-f" {
			force = true
		}
	}

	// Create shim directory
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %s: %v\n", dir, err)
		os.Exit(1)
	}

	// Kill shim processes if --force is used
	if force {
		killShimProcesses(dir)
	}

	// Copy current binary as php.exe and composer.exe
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding own executable: %v\n", err)
		os.Exit(1)
	}
	self, _ = filepath.EvalSymlinks(self)
	selfData, err := os.ReadFile(self)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading own executable: %v\n", err)
		os.Exit(1)
	}

	for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
		dest := filepath.Join(dir, name)

		// Skip if already identical
		existing, err := os.ReadFile(dest)
		if err == nil && bytes.Equal(existing, selfData) {
			fmt.Printf("  • %s is up to date\n", dest)
			continue
		}

		if err := os.WriteFile(dest, selfData, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", dest, err)
			os.Exit(1)
		}
		fmt.Printf("  ✓ Installed %s\n", dest)
	}

	// Configure User PATH
	userPath, valType, err := getUserPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading User PATH: %v\n", err)
		os.Exit(1)
	}

	// Remove any existing entry for our shim dir, then prepend
	entries := strings.Split(userPath, ";")
	var filtered []string
	for _, e := range entries {
		if !strings.EqualFold(strings.TrimRight(e, `\`), strings.TrimRight(dir, `\`)) && e != "" {
			filtered = append(filtered, e)
		}
	}
	newPath := dir + ";" + strings.Join(filtered, ";")

	// Windows has a practical PATH limit. Warn if we're approaching it.
	const maxPathLen = 2047
	if len(newPath) > maxPathLen {
		fmt.Fprintf(os.Stderr, "Warning: User PATH length (%d chars) exceeds the safe limit (%d).\n", len(newPath), maxPathLen)
		fmt.Fprintf(os.Stderr, "  Some programs may not see the full PATH. Consider removing unused entries.\n")
	}

	if err := setUserPath(newPath, valType); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting User PATH: %v\n", err)
		os.Exit(1)
	}
	broadcastSettingChange()

	fmt.Println()
	fmt.Printf("  ✓ Added %s to the beginning of User PATH\n", dir)
	fmt.Println()
	fmt.Println("Done! Restart your terminal for the PATH change to take effect.")
}

// cmdUninstall removes the shims and restores PATH.
func cmdUninstall() {
	dir := shimDir()

	// Remove shim directory
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing %s: %v\n", dir, err)
	} else {
		fmt.Printf("  ✓ Removed %s\n", dir)
	}

	// Remove from User PATH
	userPath, valType, err := getUserPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading User PATH: %v\n", err)
		os.Exit(1)
	}

	entries := strings.Split(userPath, ";")
	var filtered []string
	for _, e := range entries {
		if !strings.EqualFold(strings.TrimRight(e, `\`), strings.TrimRight(dir, `\`)) && e != "" {
			filtered = append(filtered, e)
		}
	}
	newPath := strings.Join(filtered, ";")

	if err := setUserPath(newPath, valType); err != nil {
		fmt.Fprintf(os.Stderr, "Error setting User PATH: %v\n", err)
		os.Exit(1)
	}
	broadcastSettingChange()

	fmt.Printf("  ✓ Removed %s from User PATH\n", dir)
	fmt.Println()
	fmt.Println("Done! Restart your terminal for the PATH change to take effect.")
}

// cmdStatus shows the current configuration status.
func cmdStatus() {
	// Check for --json flag
	jsonOutput := false
	for _, arg := range os.Args[2:] {
		if arg == "--json" {
			jsonOutput = true
		}
	}

	dir := shimDir()
	phpShim := filepath.Join(dir, "php.exe")
	composerShim := filepath.Join(dir, "composer.exe")

	// Gather status data
	cwd, _ := os.Getwd()
	localVersion := ""
	if cwd != "" {
		localVersion = findPHPVersion(cwd)
	}

	globalPHP, globalErr := mostRecentPHP()
	globalVersion := ""
	if globalErr == nil {
		globalVersion = extractVersion(globalPHP)
	}

	// Xdebug status
	activeVersion := localVersion
	if activeVersion == "" {
		activeVersion = globalVersion
	}
	xdebugEnabled := false
	xdebugMode := ""
	if activeVersion != "" {
		nodot := strings.ReplaceAll(activeVersion, ".", "")
		iniPath := filepath.Join(herdHome(), "php"+nodot, "php.ini")
		if data, err := os.ReadFile(iniPath); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.Contains(strings.ToLower(trimmed), "xdebug") &&
					strings.HasPrefix(trimmed, "zend_extension") {
					xdebugEnabled = true
				}
				if strings.HasPrefix(trimmed, "xdebug.mode") {
					parts := strings.SplitN(trimmed, "=", 2)
					if len(parts) == 2 {
						xdebugMode = strings.TrimSpace(parts[1])
					}
				}
			}
		}
	}
	if xdebugEnabled && xdebugMode == "" {
		xdebugMode = "debug"
	}

	// Shim existence
	phpShimInstalled := false
	if _, err := os.Stat(phpShim); err == nil {
		phpShimInstalled = true
	}
	composerShimInstalled := false
	if _, err := os.Stat(composerShim); err == nil {
		composerShimInstalled = true
	}

	// PATH check
	pathOK := false
	pathError := ""
	userPath, _, err := getUserPath()
	if err != nil {
		pathError = err.Error()
	} else {
		entries := strings.Split(userPath, ";")
		shimIndex := -1
		herdIndex := -1
		herdBin := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "bin")
		for i, e := range entries {
			clean := strings.TrimRight(e, `\`)
			if strings.EqualFold(clean, strings.TrimRight(dir, `\`)) {
				shimIndex = i
			}
			if strings.EqualFold(clean, herdBin) {
				herdIndex = i
			}
		}
		pathOK = shimIndex != -1 && (herdIndex == -1 || shimIndex < herdIndex)
	}

	// JSON output
	if jsonOutput {
		status := map[string]interface{}{
			"phpLocal":          localVersion,
			"phpGlobal":         globalVersion,
			"xdebugEnabled":     xdebugEnabled,
			"xdebugMode":        xdebugMode,
			"phpShimInstalled":  phpShimInstalled,
			"composerShimInstalled": composerShimInstalled,
			"shimDir":           dir,
			"pathConfigured":    pathOK,
			"shepherdVersion":   version,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(status)
		return
	}

	// Human-readable output
	fmt.Println("shp status:")
	fmt.Println()

	if localVersion != "" {
		fmt.Printf("  PHP local:  %s (from .phpversion)\n", localVersion)
	} else {
		fmt.Printf("  PHP local:  (none — no .phpversion found)\n")
	}
	if globalVersion != "" {
		fmt.Printf("  PHP global: %s\n", globalVersion)
	} else {
		fmt.Printf("  PHP global: (not found)\n")
	}

	if xdebugEnabled {
		fmt.Printf("  Xdebug:     ✅ enabled (mode: %s)\n", xdebugMode)
	} else if activeVersion != "" {
		fmt.Printf("  Xdebug:     ⏸️  disabled\n")
	}
	fmt.Println()

	if phpShimInstalled {
		fmt.Printf("  ✓ php.exe shim installed at %s\n", phpShim)
	} else {
		fmt.Printf("  ✗ php.exe shim not found at %s\n", phpShim)
	}
	if composerShimInstalled {
		fmt.Printf("  ✓ composer.exe shim installed at %s\n", composerShim)
	} else {
		fmt.Printf("  ✗ composer.exe shim not found at %s\n", composerShim)
	}

	fmt.Println()
	if pathError != "" {
		fmt.Printf("  ✗ Cannot read User PATH: %s\n", pathError)
	} else if pathOK {
		fmt.Printf("  ✓ PATH is correctly configured\n")
	} else {
		fmt.Printf("  ✗ PATH is not correctly configured — run 'shp install' to fix\n")
	}
}

// validXdebugModes lists accepted xdebug mode values.
var validXdebugModes = map[string]bool{
	"off":            true,
	"debug":          true,
	"coverage":       true,
	"debug,coverage": true,
	"coverage,debug": true,
	"profile":        true,
	"trace":          true,
}

// phpIniPath returns the path to the php.ini for the given php.exe directory.
func phpIniPath(phpDir string) string {
	return filepath.Join(phpDir, "php.ini")
}

// xdebugDLLPath returns the expected xdebug DLL path for a given PHP version.
func xdebugDLLPath(version string) string {
	return filepath.Join(
		os.Getenv("PROGRAMFILES"),
		"Herd", "resources", "app.asar.unpacked", "resources", "bin", "xdebug",
		"xdebug-"+version+".dll",
	)
}

// cmdXdebug manages xdebug in the php.ini for the resolved PHP version.
//
// Usage:
//
//	shp xdebug                Show current status (no active change)
//	shp xdebug toggle         Toggle xdebug on/off
//	shp xdebug <mode>         Enable xdebug with a specific mode
//	shp xdebug off            Disable xdebug
//	shp xdebug status         Show current xdebug state
func cmdXdebug() {
	// Without arguments, show status (non-active — like other commands)
	if len(os.Args) <= 2 {
		cmdXdebugShowStatus()
		return
	}

	mode := strings.ToLower(os.Args[2])

	if mode == "-h" || mode == "--help" {
		fmt.Println("Usage: shp xdebug <command>")
		fmt.Println()
		fmt.Println("Manage xdebug for the resolved PHP version.")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  toggle          Toggle xdebug on/off")
		fmt.Println("  on              Enable with debugging mode (alias for debug)")
		fmt.Println("  debug           Enable with debugging mode")
		fmt.Println("  coverage        Enable with code coverage mode")
		fmt.Println("  debug,coverage  Enable with both")
		fmt.Println("  profile         Enable with profiling mode")
		fmt.Println("  trace           Enable with function trace mode")
		fmt.Println("  off             Disable xdebug")
		fmt.Println("  status          Show current xdebug state")
		return
	}

	// Resolve PHP version from cwd
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	version := findPHPVersion(cwd)
	var phpDir string
	if version != "" {
		phpExe, err := resolveFromVersion(version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		phpDir = filepath.Dir(phpExe)
	} else {
		bootstrap, err := mostRecentPHP()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		resolved, err := whichPHP(bootstrap, cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		version = extractVersion(resolved)
		phpDir = filepath.Dir(resolved)
	}

	if version == "" {
		fmt.Fprintf(os.Stderr, "Error: could not determine PHP version\n")
		os.Exit(1)
	}

	iniPath := phpIniPath(phpDir)
	fmt.Printf("PHP %s — %s\n", version, iniPath)

	// Read php.ini
	data, err := os.ReadFile(iniPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading php.ini: %v\n", err)
		os.Exit(1)
	}
	content := string(data)
	lines := strings.Split(content, "\n")

	// Handle "status" subcommand
	if mode == "status" {
		xdebugStatus(lines, version)
		return
	}

	// Find zend_extension line containing "xdebug"
	zendIdx := -1
	zendEnabled := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(trimmed), "xdebug") &&
			(strings.HasPrefix(trimmed, "zend_extension") || strings.HasPrefix(trimmed, ";zend_extension")) {
			zendIdx = i
			zendEnabled = !strings.HasPrefix(trimmed, ";")
			break
		}
	}

	// Handle "toggle" subcommand: if enabled → off, if off → enable with debug
	if mode == "toggle" {
		if zendIdx != -1 && zendEnabled {
			// Currently on → turn off
			lines[zendIdx] = ";" + lines[zendIdx]
			writeIni(iniPath, lines)
			fmt.Println("  ⏸️  xdebug disabled")
		} else if zendIdx != -1 {
			// Currently off → turn on
			lines[zendIdx] = strings.TrimPrefix(lines[zendIdx], ";")
			lines = ensureIniValue(lines, zendIdx, "xdebug.mode", "debug")
			lines = ensureIniValue(lines, zendIdx, "xdebug.discover_client_host", "true")
			lines = ensureIniValue(lines, zendIdx, "xdebug.start_with_request", "yes")
			writeIni(iniPath, lines)
			fmt.Println("  ✅ xdebug enabled (mode: debug)")
		} else {
			// No xdebug line — add it
			dllPath := xdebugDLLPath(version)
			if _, err := os.Stat(dllPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: xdebug DLL not found at %s\n", dllPath)
				os.Exit(1)
			}
			lines = append(lines, "")
			lines = append(lines, "zend_extension="+dllPath)
			lines = append(lines, "xdebug.mode=debug")
			lines = append(lines, "xdebug.discover_client_host=true")
			lines = append(lines, "xdebug.start_with_request=yes")
			writeIni(iniPath, lines)
			fmt.Println("  ✅ xdebug enabled (mode: debug)")
		}
		return
	}

	// "on" is a shorthand alias for "debug"
	if mode == "on" {
		mode = "debug"
	}

	// Validate mode
	if !validXdebugModes[mode] {
		fmt.Fprintf(os.Stderr, "Error: invalid mode %q\n", mode)
		fmt.Fprintf(os.Stderr, "Valid modes: toggle, on, debug, coverage, debug,coverage, profile, trace, off\n")
		os.Exit(1)
	}

	// If "off" is requested, just disable
	if mode == "off" {
		if zendIdx == -1 {
			fmt.Println("  xdebug is not configured — nothing to disable")
			return
		}
		if !zendEnabled {
			fmt.Println("  xdebug is already disabled")
			return
		}
		lines[zendIdx] = ";" + lines[zendIdx]
		writeIni(iniPath, lines)
		fmt.Println("  ⏸️  xdebug disabled")
		return
	}

	// Toggle ON or ensure correct mode
	if zendIdx == -1 {
		// No xdebug line exists — add it
		dllPath := xdebugDLLPath(version)
		if _, err := os.Stat(dllPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: xdebug DLL not found at %s\n", dllPath)
			os.Exit(1)
		}
		lines = append(lines, "")
		lines = append(lines, "zend_extension="+dllPath)
		lines = append(lines, "xdebug.mode="+mode)
		lines = append(lines, "xdebug.discover_client_host=true")
		lines = append(lines, "xdebug.start_with_request=yes")
		writeIni(iniPath, lines)
		fmt.Printf("  ✅ xdebug enabled (mode: %s)\n", mode)
		return
	}

	if !zendEnabled {
		// Uncomment
		lines[zendIdx] = strings.TrimPrefix(lines[zendIdx], ";")
	}

	// Ensure xdebug.mode is set correctly
	lines = ensureIniValue(lines, zendIdx, "xdebug.mode", mode)
	lines = ensureIniValue(lines, zendIdx, "xdebug.discover_client_host", "true")
	lines = ensureIniValue(lines, zendIdx, "xdebug.start_with_request", "yes")

	writeIni(iniPath, lines)
	if !zendEnabled {
		fmt.Printf("  ✅ xdebug enabled (mode: %s)\n", mode)
	} else {
		fmt.Printf("  ✅ xdebug mode updated to: %s\n", mode)
	}
}

// cmdXdebugShowStatus resolves the PHP version and shows the current xdebug state.
// Called when `shp xdebug` is invoked without arguments.
func cmdXdebugShowStatus() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	version := findPHPVersion(cwd)
	var phpDir string
	if version != "" {
		phpExe, err := resolveFromVersion(version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		phpDir = filepath.Dir(phpExe)
	} else {
		bootstrap, err := mostRecentPHP()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		resolved, err := whichPHP(bootstrap, cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		version = extractVersion(resolved)
		phpDir = filepath.Dir(resolved)
	}

	if version == "" {
		fmt.Fprintf(os.Stderr, "Error: could not determine PHP version\n")
		os.Exit(1)
	}

	iniPath := phpIniPath(phpDir)
	data, err := os.ReadFile(iniPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading php.ini: %v\n", err)
		os.Exit(1)
	}
	lines := strings.Split(string(data), "\n")

	fmt.Printf("PHP %s — %s\n", version, iniPath)
	xdebugStatus(lines, version)
}

// xdebugStatus prints the current xdebug state from php.ini lines.
func xdebugStatus(lines []string, version string) {
	enabled := false
	mode := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(trimmed), "xdebug") &&
			strings.HasPrefix(trimmed, "zend_extension") {
			enabled = true
		}
		if strings.HasPrefix(trimmed, "xdebug.mode") {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				mode = strings.TrimSpace(parts[1])
			}
		}
	}
	if enabled {
		if mode == "" {
			mode = "debug (default)"
		}
		fmt.Printf("  ✅ xdebug is enabled (mode: %s)\n", mode)
	} else {
		fmt.Println("  ⏸️  xdebug is disabled")
	}
}

// ensureIniValue ensures a key=value line exists after the given anchor index.
// If it already exists, updates the value. Otherwise inserts after anchor.
func ensureIniValue(lines []string, anchorIdx int, key, value string) []string {
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				lines[i] = key + "=" + value
				return lines
			}
		}
	}
	// Not found — insert after anchor
	insert := key + "=" + value
	after := anchorIdx + 1
	lines = append(lines, "")
	copy(lines[after+1:], lines[after:])
	lines[after] = insert
	return lines
}

// writeIni writes lines back to the php.ini file.
func writeIni(path string, lines []string) {
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", path, err)
		os.Exit(1)
	}
}

// zendExtensions lists extensions that use zend_extension= instead of extension=.
var zendExtensions = map[string]bool{
	"xdebug":  true,
	"opcache": true,
}

// cmdExt handles the "ext" subcommand for PHP extension management.
//
// Usage:
//
//	shp ext add <name> [--php=8.4] [--ext-version=1.0.0] [--ts]
func cmdExt() {
	if len(os.Args) < 3 {
		extUsage()
		return
	}

	switch os.Args[2] {
	case "add":
		cmdExtInstall()
	case "-h", "--help":
		extUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown ext command: %s\n", os.Args[2])
		extUsage()
		os.Exit(1)
	}
}

func extUsage() {
	fmt.Println("Usage: shp ext add <name> [options]")
	fmt.Println()
	fmt.Println("Add a PHP extension from PECL.")
	fmt.Println()
	fmt.Println("Supported extensions:")
	fmt.Printf("  %s\n", strings.Join(listSupportedExtensions(), ", "))
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --php=X.Y         Target PHP version (default: resolved from .phpversion)")
	fmt.Println("  --php=all         Install for all PHP versions")
	fmt.Println("  --ext-version=V   Extension version (default: latest from PECL)")
	fmt.Println("  --ts              Use Thread Safe build (default: NTS)")
	fmt.Println("  --vs=vsXX         Visual Studio version (default: vs17)")
}

// cmdExtInstall installs a PHP extension from PECL.
func cmdExtInstall() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: extension name required\n")
		fmt.Fprintf(os.Stderr, "Usage: shp ext add <name> [options]\n")
		os.Exit(1)
	}

	extName := os.Args[3]

	// Validate against supported extensions
	extDef, ok := extensionRegistry[strings.ToLower(extName)]
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: '%s' is not a supported extension.\n\n", extName)
		fmt.Fprintf(os.Stderr, "Supported extensions:\n")
		fmt.Fprintf(os.Stderr, "  %s\n", strings.Join(listSupportedExtensions(), ", "))
		fmt.Fprintf(os.Stderr, "\nOpen an issue to request support for a new extension:\n")
		fmt.Fprintf(os.Stderr, "  https://github.com/shaffe-fr/php-shepherd/issues\n")
		os.Exit(1)
	}
	extName = extDef.name
	phpVersion := ""
	extVersion := ""
	vsVersion := "vs17"
	threadSafe := false

	// Parse flags after the extension name
	for _, arg := range os.Args[4:] {
		switch {
		case strings.HasPrefix(arg, "--php="):
			phpVersion = strings.TrimPrefix(arg, "--php=")
		case strings.HasPrefix(arg, "--ext-version="):
			extVersion = strings.TrimPrefix(arg, "--ext-version=")
		case strings.HasPrefix(arg, "--vs="):
			vsVersion = strings.TrimPrefix(arg, "--vs=")
		case arg == "--ts":
			threadSafe = true
		case arg == "-h" || arg == "--help":
			extUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown option: %s\n", arg)
			os.Exit(1)
		}
	}

	// Resolve PHP version(s)
	if phpVersion == "all" {
		versions := installedPHPVersions()
		if len(versions) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no PHP installations found in %s\n", herdHome())
			os.Exit(1)
		}

		// Detect extension version once (shared across all PHP versions)
		if extVersion == "" {
			var err error
			extVersion, err = detectPeclVersion(extName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Install system-level dependencies once
		if len(extDef.wingetDeps) > 0 {
			fmt.Println("Checking system dependencies...")
			if err := installWingetDeps(extDef.wingetDeps); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
				fmt.Fprintf(os.Stderr, "  The extension may not work without this dependency.\n")
			}
			fmt.Println()
		}

		fmt.Printf("Installing %s %s for all PHP versions: %s\n\n", extName, extVersion, strings.Join(versions, ", "))

		var failed []string
		for _, ver := range versions {
			fmt.Printf("── PHP %s ──\n", ver)
			if err := installExtForVersion(extDef, extName, extVersion, ver, vsVersion, threadSafe); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %v\n", err)
				failed = append(failed, ver)
			}
			fmt.Println()
		}

		if len(failed) > 0 {
			fmt.Printf("⚠️  Failed for: %s\n", strings.Join(failed, ", "))
			os.Exit(1)
		}
		fmt.Printf("✅ %s %s installed for all PHP versions\n", extName, extVersion)
		return
	}

	if phpVersion == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
			os.Exit(1)
		}
		phpVersion = findPHPVersion(cwd)
		if phpVersion == "" {
			fmt.Fprintf(os.Stderr, "Error: no .phpversion found and --php not specified\n")
			fmt.Fprintf(os.Stderr, "  Tip: use --php=all to install for all PHP versions\n")
			os.Exit(1)
		}
	}

	if !versionRe.MatchString(phpVersion) {
		fmt.Fprintf(os.Stderr, "Error: invalid PHP version format: %q (expected X.Y or \"all\")\n", phpVersion)
		os.Exit(1)
	}

	nodot := strings.ReplaceAll(phpVersion, ".", "")
	phpDir := filepath.Join(herdHome(), "php"+nodot)
	phpExe := filepath.Join(phpDir, "php.exe")

	if _, err := os.Stat(phpExe); err != nil {
		fmt.Fprintf(os.Stderr, "Error: PHP %s not found at %s\n", phpVersion, phpDir)
		os.Exit(1)
	}

	fmt.Printf("PHP %s — %s\n", phpVersion, phpDir)

	// Detect extension version from PECL if not specified
	if extVersion == "" {
		var err error
		extVersion, err = detectPeclVersion(extName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("Extension: %s %s\n", extName, extVersion)

	// Install system-level dependencies via winget (e.g. ODBC Driver for sqlsrv)
	if len(extDef.wingetDeps) > 0 {
		fmt.Println("Checking system dependencies...")
		if err := installWingetDeps(extDef.wingetDeps); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
			fmt.Fprintf(os.Stderr, "  The extension may not work without this dependency.\n")
		}
		fmt.Println()
	}

	// Build download URL
	ts := "nts"
	if threadSafe {
		ts = "ts"
	}
	arch := "x64"
	downloadURL := buildExtURL(extDef.source.urlPattern, extVersion, phpVersion, ts, vsVersion, arch)

	fmt.Printf("Downloading: %s\n", downloadURL)

	// Download extension zip
	zipPath, err := downloadFile(downloadURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nVerify the extension/version/PHP combination is available at:\n")
		fmt.Fprintf(os.Stderr, "  https://pecl.php.net/package/%s/%s/windows\n", extName, extVersion)
		os.Exit(1)
	}
	defer os.Remove(zipPath)

	fmt.Println("Download OK.")

	// Extract and install
	extDir := filepath.Join(phpDir, "ext")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating ext directory: %v\n", err)
		os.Exit(1)
	}

	installed, err := installExtFiles(zipPath, extName, phpDir, extDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error installing: %v\n", err)
		os.Exit(1)
	}

	if !installed {
		fmt.Fprintf(os.Stderr, "Warning: no php_%s.dll found in archive\n", extName)
	}

	// Download and install dependency libraries (e.g. ImageMagick DLLs for imagick)
	if extDef.source.depsURLPattern != "" {
		depsURL := buildExtURL(extDef.source.depsURLPattern, extVersion, phpVersion, ts, vsVersion, arch)
		fmt.Printf("Downloading dependencies: %s\n", depsURL)

		depsZipPath, err := downloadFile(depsURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to download dependencies: %v\n", err)
			fmt.Fprintf(os.Stderr, "  You may need to install support libraries manually.\n")
		} else {
			defer os.Remove(depsZipPath)
			if _, err := installExtFiles(depsZipPath, extName, phpDir, extDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to install dependencies: %v\n", err)
			} else {
				fmt.Println("Dependencies installed.")
			}
		}
	}

	// Update php.ini
	iniPath := filepath.Join(phpDir, "php.ini")
	if err := addExtensionToIni(iniPath, extName); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		fmt.Fprintf(os.Stderr, "Add manually: %s=%s\n", extDef.directive, extName)
	}

	// Verify
	fmt.Println()
	fmt.Println("Verifying...")
	if verifyExtension(phpExe, extDir, extName) {
		fmt.Printf("✅ %s %s installed for PHP %s\n", extName, extVersion, phpVersion)
	} else {
		fmt.Printf("⚠️  %s may not be loaded correctly. Verify with:\n", extName)
		fmt.Printf("   php -m | findstr %s\n", extName)
	}

	// Show post-install message if any (e.g. external dependencies)
	if extDef.postInstallMsg != "" {
		fmt.Println()
		fmt.Printf("  Note: %s\n", extDef.postInstallMsg)
	}
}

// installedPHPVersions returns a sorted list of all PHP version strings
// (e.g. ["8.3", "8.4", "8.5"]) installed under Herd.
func installedPHPVersions() []string {
	pattern := filepath.Join(herdHome(), "php*")
	matches, _ := filepath.Glob(pattern)

	// Sort by version ascending
	sort.Slice(matches, func(i, j int) bool {
		return phpDirVersion(matches[i]) < phpDirVersion(matches[j])
	})

	var versions []string
	for _, m := range matches {
		dirName := filepath.Base(m)
		dm := phpDirRe.FindStringSubmatch(dirName)
		if len(dm) != 3 {
			continue
		}
		// Verify php.exe exists
		if _, err := os.Stat(filepath.Join(m, "php.exe")); err != nil {
			continue
		}
		versions = append(versions, dm[1]+"."+dm[2])
	}
	return versions
}

// installExtForVersion installs an extension for a single PHP version.
// Returns an error instead of calling os.Exit so the caller can continue with other versions.
func installExtForVersion(extDef *extDefinition, extName, extVersion, phpVersion, vsVersion string, threadSafe bool) error {
	nodot := strings.ReplaceAll(phpVersion, ".", "")
	phpDir := filepath.Join(herdHome(), "php"+nodot)
	phpExe := filepath.Join(phpDir, "php.exe")

	if _, err := os.Stat(phpExe); err != nil {
		return fmt.Errorf("PHP %s not found at %s", phpVersion, phpDir)
	}

	ts := "nts"
	if threadSafe {
		ts = "ts"
	}
	arch := "x64"
	downloadURL := buildExtURL(extDef.source.urlPattern, extVersion, phpVersion, ts, vsVersion, arch)

	fmt.Printf("  Downloading: %s\n", downloadURL)

	zipPath, err := downloadFile(downloadURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer os.Remove(zipPath)

	// Extract and install
	extDir := filepath.Join(phpDir, "ext")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		return fmt.Errorf("cannot create ext directory: %w", err)
	}

	installed, err := installExtFiles(zipPath, extName, phpDir, extDir)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}
	if !installed {
		fmt.Printf("  Warning: no php_%s.dll found in archive\n", extName)
	}

	// Download and install dependency libraries
	if extDef.source.depsURLPattern != "" {
		depsURL := buildExtURL(extDef.source.depsURLPattern, extVersion, phpVersion, ts, vsVersion, arch)
		fmt.Printf("  Downloading dependencies...\n")

		depsZipPath, err := downloadFile(depsURL)
		if err != nil {
			fmt.Printf("  Warning: failed to download dependencies: %v\n", err)
		} else {
			defer os.Remove(depsZipPath)
			if _, err := installExtFiles(depsZipPath, extName, phpDir, extDir); err != nil {
				fmt.Printf("  Warning: failed to install dependencies: %v\n", err)
			}
		}
	}

	// Update php.ini
	iniPath := filepath.Join(phpDir, "php.ini")
	if err := addExtensionToIni(iniPath, extName); err != nil {
		fmt.Printf("  Warning: %v — add manually: %s=%s\n", err, extDef.directive, extName)
	}

	// Verify
	if verifyExtension(phpExe, extDir, extName) {
		fmt.Printf("  ✓ %s %s installed for PHP %s\n", extName, extVersion, phpVersion)
	} else {
		fmt.Printf("  ⚠️  %s may not load correctly for PHP %s\n", extName, phpVersion)
	}

	return nil
}

// detectPeclVersion scrapes pecl.php.net to find the latest stable version.
func detectPeclVersion(extName string) (string, error) {
	// Validate extension name to prevent URL injection.
	extNameRe := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
	if !extNameRe.MatchString(extName) {
		return "", fmt.Errorf("invalid extension name: %q", extName)
	}

	peclURL := "https://pecl.php.net/package/" + extName
	resp, err := httpClient.Get(peclURL)
	if err != nil {
		return "", fmt.Errorf("cannot reach pecl.php.net: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("extension %q not found on pecl.php.net (HTTP %d)", extName, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", fmt.Errorf("error reading pecl page: %w", err)
	}

	// Look for the first version link like /package/extname/1.2.3
	re := regexp.MustCompile(`/package/` + regexp.QuoteMeta(extName) + `/(\d+\.\d+\.\d+)`)
	m := re.FindSubmatch(body)
	if len(m) < 2 {
		return "", fmt.Errorf("could not detect latest version for %q on pecl.php.net", extName)
	}
	return string(m[1]), nil
}

// downloadFile downloads a URL to a temp file and returns the path.
// Downloads are limited to maxDownloadSize bytes to prevent resource exhaustion.
const maxDownloadSize = 100 * 1024 * 1024 // 100 MB

func downloadFile(rawURL string) (string, error) {
	// Validate URL scheme — only HTTPS is allowed.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("refusing non-HTTPS URL: %s", rawURL)
	}

	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
	}

	// Verify final URL after redirects is still HTTPS.
	finalURL := resp.Request.URL
	if finalURL.Scheme != "https" {
		return "", fmt.Errorf("redirect led to non-HTTPS URL: %s", finalURL.String())
	}

	tmpFile, err := os.CreateTemp("", "shepherd-ext-*.zip")
	if err != nil {
		return "", err
	}

	// Limit download size to prevent disk exhaustion.
	limited := io.LimitReader(resp.Body, maxDownloadSize+1)
	n, err := io.Copy(tmpFile, limited)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}
	if n > maxDownloadSize {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download exceeds maximum size (%d MB)", maxDownloadSize/(1024*1024))
	}
	return tmpFile.Name(), nil
}

// installExtFiles extracts the zip and places files in the correct locations.
// Extension DLLs (php_<name>.dll/pdb) go to extDir.
// Support DLLs/EXEs go to phpDir (next to php.exe).
// Returns true if the main extension DLL was found.
func installExtFiles(zipPath, extName, phpDir, extDir string) (bool, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return false, err
	}
	defer r.Close()

	extDLLPattern := regexp.MustCompile(`(?i)^php_` + regexp.QuoteMeta(extName) + `\.(dll|pdb)$`)
	foundExtDLL := false

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}

		// Use filepath.Base to correctly handle both / and \ on Windows,
		// preventing zip-slip attacks with crafted entry names.
		name := filepath.Base(filepath.FromSlash(f.Name))

		// Reject any entry that resolves to a parent traversal or empty name.
		if name == "." || name == ".." || name == "" {
			continue
		}

		lowerName := strings.ToLower(name)

		var destDir string
		if extDLLPattern.MatchString(name) {
			// Main extension DLL/PDB → ext/
			destDir = extDir
			foundExtDLL = true
		} else if strings.HasSuffix(lowerName, ".dll") || strings.HasSuffix(lowerName, ".pdb") || strings.HasSuffix(lowerName, ".exe") {
			// Support files → php dir
			destDir = phpDir
		} else {
			// Skip non-binary files (docs, etc.)
			continue
		}

		destPath := filepath.Join(destDir, name)

		// Final zip-slip guard: ensure destination stays within allowed directory.
		if !strings.HasPrefix(destPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return false, fmt.Errorf("zip entry %q escapes target directory", f.Name)
		}

		if err := extractZipFile(f, destPath); err != nil {
			return false, fmt.Errorf("extracting %s: %w", name, err)
		}
		fmt.Printf("  → %s\n", destPath)
	}

	return foundExtDLL, nil
}

// extractZipFile extracts a single file from a zip archive.
func extractZipFile(f *zip.File, destPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// addExtensionToIni adds extension= or zend_extension= to php.ini if not already present.
func addExtensionToIni(iniPath, extName string) error {
	data, err := os.ReadFile(iniPath)
	if err != nil {
		return fmt.Errorf("php.ini not found at %s", iniPath)
	}

	content := string(data)
	directive := "extension"
	if def, ok := extensionRegistry[extName]; ok && def.directive != "" {
		directive = def.directive
	} else if zendExtensions[extName] {
		directive = "zend_extension"
	}

	// Check if already present (active or commented)
	checkRe := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(directive) + `\s*=\s*` + regexp.QuoteMeta(extName))
	if checkRe.MatchString(content) {
		fmt.Printf("  %s=%s already in php.ini\n", directive, extName)
		return nil
	}

	// Append
	content += "\n" + directive + "=" + extName + "\n"
	if err := os.WriteFile(iniPath, []byte(content), 0644); err != nil {
		return err
	}
	fmt.Printf("  ✅ Added %s=%s to php.ini\n", directive, extName)
	return nil
}

// verifyExtension runs php -m and checks if the extension is loaded.
func verifyExtension(phpExe, extDir, extName string) bool {
	cmd := exec.Command(phpExe, "-d", "extension_dir="+extDir, "-m")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(extName))
}

// githubRelease represents a GitHub release API response.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

// githubAsset represents a release asset.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

const githubRepo = "shaffe-fr/php-shepherd"

// allowedDownloadHosts lists the GitHub domains from which self-update assets may be downloaded.
var allowedDownloadHosts = map[string]bool{
	"github.com":               true,
	"objects.githubusercontent.com": true,
}

// cmdDoctor diagnoses common issues that prevent Shepherd from working correctly.
func cmdDoctor() {
	fmt.Println("shp doctor")
	fmt.Println()

	issues := 0

	// 0. Check Herd is installed
	if !checkHerd() {
		fmt.Printf("  ✗ Laravel Herd is not installed (expected %s)\n", herdHome())
		fmt.Printf("    → Install from https://herd.laravel.com\n")
		issues++
	} else {
		fmt.Printf("  ✓ Laravel Herd found\n")
	}

	// 1. Check .phpversion in cwd
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("  ✗ Cannot get working directory: %v\n", err)
		issues++
	} else {
		ver := findPHPVersion(cwd)
		if ver != "" {
			// Validate that the PHP binary exists
			_, resolveErr := resolveFromVersion(ver)
			if resolveErr != nil {
				fmt.Printf("  ✗ .phpversion requests PHP %s, but it is not installed\n", ver)
				fmt.Printf("    → Install PHP %s via Herd, or change .phpversion\n", ver)
				issues++
			} else {
				fmt.Printf("  ✓ .phpversion: %s (installed)\n", ver)
			}
		} else {
			fmt.Printf("  • No .phpversion found (will use Herd global)\n")
		}
	}

	// 2. Check shims exist
	dir := shimDir()
	phpShim := filepath.Join(dir, "php.exe")
	composerShim := filepath.Join(dir, "composer.exe")

	if _, err := os.Stat(phpShim); err != nil {
		fmt.Printf("  ✗ php.exe shim not found at %s\n", phpShim)
		fmt.Printf("    → Run: shp install\n")
		issues++
	} else {
		fmt.Printf("  ✓ php.exe shim installed\n")
	}

	if _, err := os.Stat(composerShim); err != nil {
		fmt.Printf("  ✗ composer.exe shim not found at %s\n", composerShim)
		fmt.Printf("    → Run: shp install\n")
		issues++
	} else {
		fmt.Printf("  ✓ composer.exe shim installed\n")
	}

	// 3. Check PATH order (User PATH from registry)
	userPath, _, pathErr := getUserPath()
	if pathErr != nil {
		fmt.Printf("  ✗ Cannot read User PATH: %v\n", pathErr)
		issues++
	} else {
		entries := strings.Split(userPath, ";")
		shimIndex := -1
		herdIndex := -1
		herdBin := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "bin")

		for i, e := range entries {
			clean := strings.TrimRight(e, `\`)
			if strings.EqualFold(clean, strings.TrimRight(dir, `\`)) {
				shimIndex = i
			}
			if strings.EqualFold(clean, herdBin) {
				herdIndex = i
			}
		}

		if shimIndex == -1 {
			fmt.Printf("  ✗ Shepherd shim directory is NOT in User PATH\n")
			fmt.Printf("    → Run: shp install\n")
			issues++
		} else if herdIndex != -1 && shimIndex > herdIndex {
			fmt.Printf("  ✗ Shepherd is AFTER Herd in PATH (position %d vs %d)\n", shimIndex+1, herdIndex+1)
			fmt.Printf("    → Run: shp install\n")
			issues++
		} else {
			fmt.Printf("  ✓ PATH order: Shepherd is before Herd\n")
		}
	}

	// 4. Check for shell aliases that override Shepherd
	aliasIssues := doctorCheckAliases()
	issues += aliasIssues

	// 5. Check that where.exe php resolves to Shepherd shim first
	whereOut, err := exec.Command("where.exe", "php").Output()
	if err == nil {
		whereLines := strings.Split(strings.TrimSpace(string(whereOut)), "\r\n")
		if len(whereLines) > 0 {
			first := strings.TrimSpace(whereLines[0])
			if strings.EqualFold(first, phpShim) {
				fmt.Printf("  ✓ where.exe php → Shepherd shim (first result)\n")
			} else {
				fmt.Printf("  ✗ where.exe php resolves to: %s\n", first)
				if strings.HasSuffix(strings.ToLower(first), ".bat") {
					fmt.Printf("    → A .bat file takes priority over Shepherd. Check your PATH or System PATH.\n")
				} else {
					fmt.Printf("    → Expected: %s\n", phpShim)
				}
				issues++
			}
		}
	}

	// Summary
	fmt.Println()
	if issues == 0 {
		fmt.Println("  No issues found. Shepherd should be working correctly.")
	} else {
		fmt.Printf("  Found %d issue(s). Fix them and run 'shp doctor' again.\n", issues)
	}
}

// doctorCheckAliases scans common shell config files for aliases that override php/composer.
func doctorCheckAliases() int {
issues := 0
home := os.Getenv("USERPROFILE")

// Files to scan for alias definitions
configFiles := []string{
// Bash
filepath.Join(home, ".bash_aliases"),
filepath.Join(home, ".bashrc"),
filepath.Join(home, ".bash_profile"),
filepath.Join(home, ".profile"),
// Zsh
filepath.Join(home, ".zshrc"),
filepath.Join(home, ".zprofile"),
filepath.Join(home, ".zshenv"),
filepath.Join(home, ".zsh", "aliases.zsh"),
// PowerShell
filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"),
filepath.Join(home, "Documents", "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1"),
// Git Bash (MSYS2/MinGW)
filepath.Join(home, ".config", "git", "bash_profile"),
// Cmder / ConEmu
filepath.Join(home, ".config", "cmder", "user_aliases.cmd"),
// Clink
filepath.Join(home, ".config", "clink", "clink_start.cmd"),
// Nushell
filepath.Join(home, "AppData", "Roaming", "nushell", "config.nu"),
filepath.Join(home, "AppData", "Roaming", "nushell", "env.nu"),
}

// Patterns that indicate a problematic alias
aliasRe := regexp.MustCompile(`(?m)^\s*alias\s+(php|composer)\s*=`)
// PowerShell-style: Set-Alias php ... or New-Alias php ...
psAliasRe := regexp.MustCompile(`(?mi)^\s*(Set-Alias|New-Alias|sal|nal)\s+(php|composer)\b`)
// Nushell-style: alias php = ... (same syntax as bash but included for clarity)
nuAliasRe := regexp.MustCompile(`(?m)^\s*(?:export\s+)?alias\s+(php|composer)\s*=`)
// Pattern for conditional aliases (guarded by Shepherd check) — these are fine
guardRe := regexp.MustCompile(`(?mi)command\s+-v\s+shp|\$\+commands\[shp\]|Get-Command\s+shp|shp`)

for _, configFile := range configFiles {
data, err := os.ReadFile(configFile)
if err != nil {
continue // File doesn't exist, skip
}

content := string(data)

// Check if there's a Shepherd guard anywhere in the file
hasGuard := guardRe.MatchString(content)

// Collect all alias matches from different patterns
type aliasMatch struct {
cmd string
}
var found []aliasMatch

for _, m := range aliasRe.FindAllStringSubmatch(content, -1) {
if len(m) > 1 {
found = append(found, aliasMatch{cmd: m[1]})
}
}
for _, m := range psAliasRe.FindAllStringSubmatch(content, -1) {
if len(m) > 2 {
found = append(found, aliasMatch{cmd: m[2]})
}
}
for _, m := range nuAliasRe.FindAllStringSubmatch(content, -1) {
if len(m) > 1 {
found = append(found, aliasMatch{cmd: m[1]})
}
}

if len(found) == 0 || hasGuard {
continue
}

// Found unguarded alias(es)
relPath := strings.TrimPrefix(configFile, home)
relPath = strings.TrimPrefix(relPath, string(os.PathSeparator))

seen := map[string]bool{}
for _, f := range found {
if seen[f.cmd] {
continue
}
seen[f.cmd] = true
fmt.Printf("  ✗ Shell alias found: %s is aliased in ~\\%s\n", f.cmd, relPath)
fmt.Printf("    → This overrides Shepherd. Remove the alias or guard it.\n")
issues++
}
}

if issues == 0 {
fmt.Printf("  ✓ No conflicting shell aliases found\n")
}

return issues
}

// cmdSelfUpdate checks for a newer release on GitHub and updates the binary.
func cmdSelfUpdate() {
	fmt.Printf("shp %s\n", version)
	fmt.Println("Checking for updates...")

	// Fetch latest release from GitHub API
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "shepherd/"+version)

	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error contacting GitHub: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Error: GitHub API returned HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	// Limit API response to 1MB to prevent resource exhaustion.
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&release); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing release info: %v\n", err)
		os.Exit(1)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := strings.TrimPrefix(version, "v")

	if latestVersion == currentVersion {
		fmt.Printf("Already up to date (%s).\n", version)
		return
	}

	fmt.Printf("New version available: %s → %s\n", currentVersion, latestVersion)

	// Find the right asset for this OS/arch
	arch := runtime.GOARCH
	assetName := fmt.Sprintf("php-shepherd_%s_windows_%s.zip", latestVersion, arch)

	var downloadURL string
	var checksumURL string
	var hasCosignSig bool
	for _, asset := range release.Assets {
		if strings.EqualFold(asset.Name, assetName) {
			downloadURL = asset.BrowserDownloadURL
		}
		if strings.EqualFold(asset.Name, "checksums.txt") {
			checksumURL = asset.BrowserDownloadURL
		}
		if strings.EqualFold(asset.Name, "checksums.txt.sig") {
			hasCosignSig = true
		}
	}

	if downloadURL == "" {
		fmt.Fprintf(os.Stderr, "Error: no release asset found for %s\n", assetName)
		fmt.Fprintf(os.Stderr, "Available assets:\n")
		for _, asset := range release.Assets {
			fmt.Fprintf(os.Stderr, "  - %s\n", asset.Name)
		}
		os.Exit(1)
	}

	// Validate download URL domain to prevent supply-chain redirect attacks.
	if err := validateDownloadURL(downloadURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	// Download the zip
	zipPath, err := downloadFile(downloadURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading update: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(zipPath)

	// Verify checksum (mandatory — goreleaser always produces checksums.txt).
	if checksumURL == "" {
		fmt.Fprintf(os.Stderr, "Error: no checksums.txt found in release — refusing to install unverified binary\n")
		os.Exit(1)
	}
	if err := validateDownloadURL(checksumURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: checksum URL validation failed: %v\n", err)
		os.Exit(1)
	}
	if err := verifyChecksum(zipPath, assetName, checksumURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: checksum verification failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "The downloaded file may have been tampered with.\n")
		os.Exit(1)
	}
	fmt.Println("Checksum verified ✓")
	if hasCosignSig {
		fmt.Println("Cosign signature present ✓ (verify with: cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig checksums.txt)")
	}

	// Extract shp.exe from the zip
	newBinary, err := extractBinaryFromZip(zipPath, "shp.exe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error extracting update: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(newBinary)

	// Replace the current executable and all shims
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding own executable: %v\n", err)
		os.Exit(1)
	}
	self, _ = filepath.EvalSymlinks(self)

	if err := replaceBinary(self, newBinary); err != nil {
		fmt.Fprintf(os.Stderr, "Error replacing binary: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  ✓ Updated %s\n", self)

	// Also update shims if they exist
	dir := shimDir()
	for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
		shimPath := filepath.Join(dir, name)
		if strings.EqualFold(shimPath, self) {
			continue // Already updated
		}
		if _, err := os.Stat(shimPath); err == nil {
			newCopy, err := extractBinaryFromZip(zipPath, "shp.exe")
			if err == nil {
				if err := replaceBinary(shimPath, newCopy); err == nil {
					fmt.Printf("  ✓ Updated %s\n", shimPath)
				}
				os.Remove(newCopy)
			}
		}
	}

	fmt.Printf("\n✅ Shepherd updated to %s\n", latestVersion)
}

// validateDownloadURL ensures the URL points to an allowed GitHub host.
func validateDownloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("download URL must be HTTPS, got: %s", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if !allowedDownloadHosts[host] {
		return fmt.Errorf("download URL host %q is not in allowlist", host)
	}
	return nil
}

// verifyChecksum downloads the checksums.txt and verifies the SHA256 of the local file.
func verifyChecksum(filePath, fileName, checksumURL string) error {
	// Download checksums.txt
	resp, err := httpClient.Get(checksumURL)
	if err != nil {
		return fmt.Errorf("cannot download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("checksums.txt returned HTTP %d", resp.StatusCode)
	}

	// Read checksums (limit to 1MB to prevent abuse)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("error reading checksums: %w", err)
	}

	// Parse checksums.txt — format: "<sha256>  <filename>"
	var expectedHash string
	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && strings.EqualFold(parts[1], fileName) {
			expectedHash = strings.ToLower(parts[0])
			break
		}
	}

	if expectedHash == "" {
		return fmt.Errorf("no checksum found for %s in checksums.txt", fileName)
	}

	// Validate hex format
	if len(expectedHash) != 64 {
		return fmt.Errorf("invalid checksum length for %s", fileName)
	}

	// Compute actual hash
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("cannot open file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("error computing hash: %w", err)
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	if actualHash != expectedHash {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

// extractBinaryFromZip extracts a named file from a zip to a temp file.
// Extraction is limited to maxBinarySize to prevent zip bomb attacks.
const maxBinarySize = 50 * 1024 * 1024 // 50 MB

func extractBinaryFromZip(zipPath, fileName string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if strings.EqualFold(filepath.Base(f.Name), fileName) {
			if f.UncompressedSize64 > maxBinarySize {
				return "", fmt.Errorf("%s is too large (%d bytes, max %d)", fileName, f.UncompressedSize64, maxBinarySize)
			}

			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			tmpFile, err := os.CreateTemp("", "shepherd-update-*.exe")
			if err != nil {
				return "", err
			}

			limited := io.LimitReader(rc, maxBinarySize+1)
			n, err := io.Copy(tmpFile, limited)
			if err != nil {
				tmpFile.Close()
				os.Remove(tmpFile.Name())
				return "", err
			}
			if n > maxBinarySize {
				tmpFile.Close()
				os.Remove(tmpFile.Name())
				return "", fmt.Errorf("%s exceeds maximum allowed size (%d MB)", fileName, maxBinarySize/(1024*1024))
			}
			tmpFile.Close()
			return tmpFile.Name(), nil
		}
	}
	return "", fmt.Errorf("%s not found in archive", fileName)
}

// replaceBinary replaces the target executable with the new one.
// On Windows, we can't overwrite a running binary, so we rename the old one first.
func replaceBinary(target, newBinary string) error {
	oldPath := target + ".old"

	// Remove any previous .old file
	os.Remove(oldPath)

	// Rename current binary to .old
	if err := os.Rename(target, oldPath); err != nil {
		return fmt.Errorf("cannot rename %s: %w", target, err)
	}

	// Write new binary to a temp file in the same directory (ensures same volume for rename)
	dir := filepath.Dir(target)
	tmpFile, err := os.CreateTemp(dir, "shp-update-*.exe")
	if err != nil {
		// Rollback
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	data, err := os.ReadFile(newBinary)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot read new binary: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot write temp binary: %w", err)
	}
	tmpFile.Close()

	// Atomic rename from temp to target (same volume = atomic on NTFS)
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot rename temp to target: %w", err)
	}

	// Clean up old binary (best effort, may fail if still running)
	os.Remove(oldPath)
	return nil
}
