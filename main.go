package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// version is set at build time via ldflags.
var version = "dev"

// verbose controls whether extra diagnostic output is printed.
var verbose bool

// quiet suppresses non-essential output.
var quiet bool

// jsonOutput controls whether commands emit machine-readable JSON instead of human text.
var jsonOutput bool

// noInteractive disables interactive prompts (auto-detected from stdin, or forced via --no-interactive).
var noInteractive bool

// logVerbose prints a message only when --verbose is active.
func logVerbose(format string, args ...interface{}) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[verbose] "+format+"\n", args...)
	}
}

// logInfo prints a message unless --quiet is active.
func logInfo(format string, args ...interface{}) {
	if !quiet {
		fmt.Printf(format, args...)
	}
}

// nilIfEmpty returns nil if s is empty, otherwise returns s.
// Used for JSON output so absent values serialize as null instead of "".
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func mustAtoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

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

// shepherdDataDir returns the Shepherd data directory (for lockfiles, cache, etc.).
func shepherdDataDir() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "shepherd")
}

// resolvePhysicalPath resolves NTFS junctions and symlinks to the real physical path.
func resolvePhysicalPath(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return resolved
}

// cacertPath returns the path to Herd's CA certificate bundle.
func cacertPath() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "php", "cacert.pem")
}

// isInstalled returns true if the shims are already present in the shepherd bin directory.
func isInstalled() bool {
	for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
		if _, err := os.Stat(filepath.Join(shimDir(), name)); err != nil {
			return false
		}
	}
	return true
}

// parentProcessName returns the executable name of the parent process (e.g. "explorer.exe").
func parentProcessName() string {
	ppid := uint32(os.Getppid())

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	err = windows.Process32First(snap, &entry)
	for err == nil {
		if entry.ProcessID == ppid {
			return windows.UTF16ToString(entry.ExeFile[:])
		}
		err = windows.Process32Next(snap, &entry)
	}
	return ""
}

// isLaunchedFromExplorer returns true if the binary was double-clicked from Explorer.
func isLaunchedFromExplorer() bool {
	return strings.EqualFold(parentProcessName(), "explorer.exe")
}

// isInteractive returns true if stdin is connected to a terminal and --no-interactive was not passed.
func isInteractive() bool {
	if noInteractive {
		return false
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	// If stdin is a character device (terminal), ModeCharDevice is set.
	return fi.Mode()&os.ModeCharDevice != 0
}

// confirmInstall prompts the user and returns true if they accept.
func confirmInstall() bool {
	fmt.Print("Shepherd is not installed yet. Install now? [Y/n] ")
	var answer string
	fmt.Scanln(&answer)
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "" || answer == "y" || answer == "yes"
}

func main() {
	// Parse global flags (--verbose, --quiet, --json, --no-interactive) before anything else.
	// These are stripped from os.Args so subcommands don't see them.
	var cleanedArgs []string
	for _, arg := range os.Args {
		switch arg {
		case "--verbose":
			verbose = true
		case "--quiet", "-q":
			quiet = true
		case "--json":
			jsonOutput = true
		case "--no-interactive":
			noInteractive = true
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
		// Passive update check: notify if a newer version is known, and refresh
		// the cache in the background if stale. Only in interactive, non-quiet,
		// non-json mode to avoid polluting scripts/CI output.
		if !quiet && !jsonOutput && isInteractive() {
			defer maybeNotifyUpdate()
			triggerUpdateCheckIfStale()
		}

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
			case "reverb":
				cmdReverb()
				return
			case "self-update":
				cmdSelfUpdate()
				return
			case "doctor":
				cmdDoctor()
				return
			case "which":
				cmdWhich()
				return
			case "current":
				cmdCurrent()
				return
			case "list", "ls":
				cmdList()
				return
			case "version", "--version", "-v":
				if jsonOutput {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					_ = enc.Encode(map[string]string{"version": version})
				} else {
					fmt.Printf("shp %s\n", version)
				}
				return
			}
		}
		// If not installed, propose installation instead of showing help
		if !isInstalled() {
			if !isInteractive() {
				// Non-interactive (CI, piped, etc.) — fall through to show help
			} else {
				fromExplorer := isLaunchedFromExplorer()
				if confirmInstall() {
					cmdInstall()
				} else {
					fmt.Println("Skipped. Run `shp install` when you're ready.")
				}
				if fromExplorer {
					fmt.Println("\nPress Enter to close...")
					fmt.Scanln()
				}
				return
			}
		}

		if jsonOutput {
			type cmdInfo struct {
				Name        string   `json:"name"`
				Aliases     []string `json:"aliases,omitempty"`
				Description string   `json:"description"`
			}
			type flagInfo struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			help := struct {
				Version  string     `json:"version"`
				Commands []cmdInfo  `json:"commands"`
				Flags    []flagInfo `json:"flags"`
			}{
				Version: version,
				Commands: []cmdInfo{
					{Name: "use", Description: "Set the PHP version for the current project (.phpversion)"},
					{Name: "which", Description: "Show resolved PHP executable path and source"},
					{Name: "current", Description: "Print the resolved PHP version number"},
					{Name: "list", Aliases: []string{"ls"}, Description: "List available PHP versions"},
					{Name: "status", Description: "Show current PHP version and configuration"},
					{Name: "xdebug", Description: "Manage xdebug for the current PHP version"},
					{Name: "ext", Description: "Download, install, and configure a PHP extension"},
					{Name: "reverb", Description: "Show Reverb status and .env configuration"},
					{Name: "install", Description: "Install php.exe and composer.exe shims and configure PATH"},
					{Name: "uninstall", Description: "Remove shims and restore PATH"},
					{Name: "doctor", Description: "Diagnose common issues with Shepherd setup"},
					{Name: "self-update", Description: "Update Shepherd to the latest version"},
					{Name: "version", Description: "Show current Shepherd version"},
				},
				Flags: []flagInfo{
					{Name: "--verbose", Description: "Show extra diagnostic output"},
					{Name: "--quiet", Description: "Suppress non-essential output"},
					{Name: "--json", Description: "Output machine-readable JSON (for scripts and LLMs)"},
					{Name: "--no-interactive", Description: "Skip interactive prompts (auto-detected when stdin is not a terminal)"},
				},
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(help)
			return
		}

		fmt.Println("Shepherd - Per-project PHP on Windows, done right.")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  use         Set the PHP version for the current project (.phpversion)")
		fmt.Println("  which       Show resolved PHP executable path and source")
		fmt.Println("  current     Print the resolved PHP version number")
		fmt.Println("  list, ls    List available PHP versions")
		fmt.Println("  status      Show current PHP version and configuration")
		fmt.Println("  xdebug      Manage xdebug for the current PHP version")
		fmt.Println("  ext         Download, install, and configure a PHP extension")
		fmt.Println("  reverb      Show Reverb status and .env configuration")
		fmt.Println("  install     Install php.exe and composer.exe shims and configure PATH")
		fmt.Println("  uninstall   Remove shims and restore PATH")
		fmt.Println("  doctor      Diagnose common issues with Shepherd setup")
		fmt.Println("  self-update Update Shepherd to the latest version")
		fmt.Println("  version     Show current Shepherd version")
		fmt.Println()
		fmt.Println("Global flags:")
		fmt.Println("  --verbose         Show extra diagnostic output")
		fmt.Println("  --quiet           Suppress non-essential output")
		fmt.Println("  --json            Output machine-readable JSON (for scripts and LLMs)")
		fmt.Println("  --no-interactive  Skip interactive prompts (auto-detected when stdin is not a terminal)")
		fmt.Println()
		fmt.Println("When invoked as php.exe or composer.exe, acts as a transparent PHP version switcher.")
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "shp: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	logVerbose("cwd: %s", cwd)
	if err := requireHerd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var targetPHP string
	var phpVersion string
	fromDotfile := false

	// 1. Try .phpversion file (walk up directory tree)
	phpVersion = findPHPVersion(cwd)
	if phpVersion != "" {
		fromDotfile = true
		logVerbose("resolved PHP %s from .phpversion", phpVersion)
		targetPHP, err = resolveFromVersion(phpVersion)
		if err != nil {
			if isInteractive() {
				fmt.Fprintf(os.Stderr, "PHP %s is not installed.\n", phpVersion)
				fmt.Fprintf(os.Stderr, "Install it with Herd now? [Y/n] ")
				var answer string
				fmt.Scanln(&answer)
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer == "" || answer == "y" || answer == "yes" {
					if installErr := herdInstallPHP(phpVersion); installErr != nil {
						fmt.Fprintf(os.Stderr, "shp: installation failed: %v\n", installErr)
						os.Exit(1)
					}
					// Retry resolution after install
					targetPHP, err = resolveFromVersion(phpVersion)
					if err != nil {
						fmt.Fprintf(os.Stderr, "shp: %v\n", err)
						os.Exit(1)
					}
				} else {
					os.Exit(1)
				}
			} else {
				fmt.Fprintf(os.Stderr, "shp: %v\n", err)
				os.Exit(1)
			}
		}
	} else {
		// 2. Fallback: ask herd.phar which-php
		logVerbose("no .phpversion found, falling back to herd.phar which-php")
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
		phpVersion = extractVersion(targetPHP)
		logVerbose("herd resolved PHP %s", phpVersion)
	}

	logVerbose("target: %s", targetPHP)

	// Warn if composer.json php constraint conflicts with .phpversion
	if fromDotfile && !quiet {
		checkComposerPHPConstraint(cwd, phpVersion)
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
		logVerbose("composer mode: %s %v", targetPHP, cmdArgs)
	} else {
		// php mode: php.exe -d extension_dir=... <rewritten user args>
		userArgs := rewriteXdebugArgs(os.Args[1:], phpVersion)
		cmdArgs = []string{"-d", "extension_dir=" + extDir}
		cmdArgs = append(cmdArgs, userArgs...)
		logVerbose("php mode: %s %v", targetPHP, cmdArgs)
	}

	// Sync nginx config in background (if .phpversion exists)
	if fromDotfile {
		logVerbose("syncing nginx config for version %s", phpVersion)
		syncNginx(cwd, phpVersion)

		// Ensure PHP-CGI service is running for this version
		ensurePhpCgiRunning(phpVersion)
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

// cmdWhich shows how the current PHP version was resolved and where the executable lives.
func cmdWhich() {
	if err := requireHerd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	var phpPath string
	var phpVersion string
	source := ""
	sourceFile := ""

	// Try .phpversion resolution
	phpVersion = findPHPVersion(cwd)
	if phpVersion != "" {
		phpPath, err = resolveFromVersion(phpVersion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		// Find which .phpversion file was used (walk up)
		dir := cwd
		for {
			candidate := filepath.Join(dir, ".phpversion")
			if _, serr := os.Stat(candidate); serr == nil {
				sourceFile = candidate
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		source = ".phpversion"
	} else {
		// Fallback to herd.phar which-php
		bootstrap, berr := mostRecentPHP()
		if berr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", berr)
			os.Exit(1)
		}
		phpPath, err = whichPHP(bootstrap, cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		phpVersion = extractVersion(phpPath)
		source = "herd (global)"
	}

	if jsonOutput {
		result := map[string]interface{}{
			"version":    phpVersion,
			"executable": phpPath,
			"source":     source,
			"sourceFile": nilIfEmpty(sourceFile),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	fmt.Printf("  PHP version:  %s\n", phpVersion)
	fmt.Printf("  Executable:   %s\n", phpPath)
	if sourceFile != "" {
		fmt.Printf("  Source:       %s (%s)\n", source, sourceFile)
	} else {
		fmt.Printf("  Source:       %s\n", source)
	}
}

// cmdCurrent prints the resolved PHP version number and nothing else.
func cmdCurrent() {
	if err := requireHerd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	phpVersion := findPHPVersion(cwd)
	if phpVersion == "" {
		// Fallback to herd
		bootstrap, berr := mostRecentPHP()
		if berr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", berr)
			os.Exit(1)
		}
		phpPath, err := whichPHP(bootstrap, cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		phpVersion = extractVersion(phpPath)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(map[string]string{"version": phpVersion})
		return
	}

	fmt.Println(phpVersion)
}

// cmdList lists available PHP versions (dedicated command for discoverability).
func cmdList() {
	if err := requireHerd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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

	// Collect versions
	type phpVer struct {
		Version string `json:"version"`
		Active  bool   `json:"active"`
		Path    string `json:"path"`
	}
	var versions []phpVer
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
		versions = append(versions, phpVer{
			Version: ver,
			Active:  ver == currentVersion,
			Path:    m,
		})
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(versions)
		return
	}

	fmt.Println("Available PHP versions:")
	fmt.Println()
	for _, v := range versions {
		if v.Active {
			fmt.Printf("  → %s (active)\n", v.Version)
		} else {
			fmt.Printf("    %s\n", v.Version)
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

		// Determine latest (highest) version
		latestVersion := ""
		for i := len(matches) - 1; i >= 0; i-- {
			m := matches[i]
			if _, err := os.Stat(filepath.Join(m, "php.exe")); err != nil {
				continue
			}
			dm := phpDirRe.FindStringSubmatch(filepath.Base(m))
			if len(dm) == 3 {
				latestVersion = dm[1] + "." + dm[2]
				break
			}
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

			var tags []string
			if ver == currentVersion {
				tags = append(tags, "active")
			}
			if ver == latestVersion {
				tags = append(tags, "latest")
			}

			if len(tags) > 0 {
				fmt.Printf("  → %s (%s)\n", ver, strings.Join(tags, ", "))
			} else {
				fmt.Printf("    %s\n", ver)
			}
		}
		return
	}

	ver := os.Args[2]

	// "latest" writes the highest installed version
	if strings.EqualFold(ver, "latest") {
		latestPHP, err := mostRecentPHP()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		ver = extractVersion(latestPHP)
		if ver == "" {
			fmt.Fprintf(os.Stderr, "Error: could not determine version from %s\n", latestPHP)
			os.Exit(1)
		}
	}

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

// killShimProcesses kills any running shim processes from the given directory.
func killShimProcesses(dir string) {
	// Use tasklist + findstr to locate processes running from the shim directory.
	// WMIC is deprecated and removed in Windows 11 24H2+.
	dir = strings.TrimRight(dir, `\`)
	dirLower := strings.ToLower(dir)

	// Get verbose process list (includes image path)
	taskCmd := exec.Command("tasklist", "/V", "/FO", "CSV", "/NH")
	out, err := taskCmd.Output()
	if err != nil {
		return
	}

	// Also try wmic as fallback for older systems where tasklist /V doesn't show paths
	wmicCmd := exec.Command("wmic", "process", "get", "ProcessId,ExecutablePath", "/format:csv")
	wmicOut, _ := wmicCmd.Output()

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
			psCmd := exec.Command("powershell", "-NoProfile", "-Command",
				fmt.Sprintf("(Get-Process -Id %d -ErrorAction SilentlyContinue).Path", pid))
			psOut, psErr := psCmd.Output()
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

// cmdInstall installs the shims and configures PATH.
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

	type shimResult struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Status string `json:"status"` // "installed", "up_to_date"
	}
	var shims []shimResult

	for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
		dest := filepath.Join(dir, name)

		// Skip if already identical
		existing, err := os.ReadFile(dest)
		if err == nil && bytes.Equal(existing, selfData) {
			logInfo("  • %s is up to date\n", dest)
			shims = append(shims, shimResult{Name: name, Path: dest, Status: "up_to_date"})
			continue
		}

		if err := os.WriteFile(dest, selfData, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", dest, err)
			os.Exit(1)
		}
		logInfo("  ✓ Installed %s\n", dest)
		shims = append(shims, shimResult{Name: name, Path: dest, Status: "installed"})
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

	logInfo("\n")
	logInfo("  ✓ Added %s to the beginning of User PATH\n", dir)

	// Patch PowerShell profile if it reorders PATH without including Shepherd
	profilePatched := patchPowerShellProfile(dir)
	if profilePatched {
		logInfo("  ✓ Patched PowerShell profile to source %s\n", shepherdProfilePath())
		logInfo("    (ensures Shepherd stays first in PATH; reversed by 'shp uninstall')\n")
	}

	if jsonOutput {
		result := map[string]interface{}{
			"shimDir":        dir,
			"shims":          shims,
			"pathUpdated":    true,
			"profilePatched": profilePatched,
			"profileSnippet": shepherdProfilePath(),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	logInfo("\n")
	logInfo("Done! Restart your terminal for the PATH change to take effect.\n")
}

// cmdUninstall removes the shims and restores PATH.
func cmdUninstall() {
	dir := shimDir()

	// Remove shim directory
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing %s: %v\n", dir, err)
	} else {
		logInfo("  ✓ Removed %s\n", dir)
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

	// Remove PowerShell profile integration
	unpatchPowerShellProfile()

	logInfo("  ✓ Removed %s from User PATH\n", dir)
	logInfo("\n")
	logInfo("Done! Restart your terminal for the PATH change to take effect.\n")
}

// cmdStatus shows the current configuration status.
func cmdStatus() {
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
			"phpLocal":              nilIfEmpty(localVersion),
			"phpGlobal":             nilIfEmpty(globalVersion),
			"xdebugEnabled":         xdebugEnabled,
			"xdebugMode":            nilIfEmpty(xdebugMode),
			"phpShimInstalled":      phpShimInstalled,
			"composerShimInstalled": composerShimInstalled,
			"shimDir":               dir,
			"pathConfigured":        pathOK,
			"shepherdVersion":       version,
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
