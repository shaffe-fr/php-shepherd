package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// xdebugDLLPath returns the expected xdebug DLL path for a given PHP version.
func xdebugDLLPath(version string) string {
	return filepath.Join(
		os.Getenv("PROGRAMFILES"),
		"Herd", "resources", "app.asar.unpacked", "resources", "bin", "xdebug",
		"xdebug-"+version+".dll",
	)
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
		if strings.Contains(strings.ToLower(arg), xdebugDirLower) ||
			(strings.Contains(strings.ToLower(arg), "xdebug") && strings.Contains(arg, "zend_extension")) {
			for _, dll := range dlls {
				dllName := filepath.Base(dll)
				arg = strings.ReplaceAll(arg, dllName, "xdebug-"+version+".dll")
			}
		}
		result = append(result, arg)
	}
	return result
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
		fmt.Println("  run <mode> -- <command...>")
		fmt.Println("                  Stateless: run a single command with xdebug (one-off)")
		return
	}

	// Dispatch "run" subcommand before resolving PHP for ini-based modes
	if mode == "run" {
		cmdXdebugRun()
		return
	}

	// Resolve PHP version from cwd
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	phpPath, version, err := resolveCurrentPHP(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	phpDir := filepath.Dir(phpPath)

	if version == "" {
		fmt.Fprintf(os.Stderr, "Error: could not determine PHP version\n")
		os.Exit(1)
	}

	iniPath := phpIniPath(phpDir)
	if !jsonOutput {
		fmt.Printf("PHP %s — %s\n", version, iniPath)
	}

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
	// Handles leading spaces before semicolons (e.g. " ; zend_extension=...")
	zendIdx := -1
	zendEnabled := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(trimmed), "xdebug") &&
			(strings.HasPrefix(trimmed, "zend_extension") || strings.HasPrefix(trimmed, ";")) {
			// Confirm it's actually a zend_extension directive (commented or not)
			uncommented := strings.TrimLeft(trimmed, "; ")
			if strings.HasPrefix(uncommented, "zend_extension") {
				zendIdx = i
				zendEnabled = strings.HasPrefix(trimmed, "zend_extension")
				break
			}
		}
	}

	// Handle "toggle" subcommand: if enabled → off, if off → enable with debug
	if mode == "toggle" {
		if zendIdx != -1 && zendEnabled {
			// Currently on → turn off
			lines[zendIdx] = ";" + lines[zendIdx]
			if err := writeIni(iniPath, lines); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			xdebugRestartNginx()
			xdebugActionResult(version, false, "off")
		} else if zendIdx != -1 {
			// Currently off → turn on (strip leading semicolons and spaces)
			trimmed := strings.TrimSpace(lines[zendIdx])
			lines[zendIdx] = strings.TrimLeft(trimmed, "; \t")
			lines = ensureIniValue(lines, zendIdx, "xdebug.mode", "debug")
			lines = ensureIniValue(lines, zendIdx, "xdebug.discover_client_host", "true")
			lines = ensureIniValue(lines, zendIdx, "xdebug.start_with_request", "yes")
			if err := writeIni(iniPath, lines); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			xdebugRestartNginx()
			xdebugActionResult(version, true, "debug")
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
			if err := writeIni(iniPath, lines); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			xdebugRestartNginx()
			xdebugActionResult(version, true, "debug")
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
			if jsonOutput {
				xdebugActionResult(version, false, "off")
			} else {
				fmt.Println("  xdebug is not configured — nothing to disable")
			}
			return
		}
		if !zendEnabled {
			if jsonOutput {
				xdebugActionResult(version, false, "off")
			} else {
				fmt.Println("  xdebug is already disabled")
			}
			return
		}
		lines[zendIdx] = ";" + lines[zendIdx]
		if err := writeIni(iniPath, lines); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		xdebugRestartNginx()
		xdebugActionResult(version, false, "off")
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
		if xdebugNeedsOutputDir(mode) {
			lines = append(lines, "xdebug.output_dir=.")
		}
		if err := writeIni(iniPath, lines); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		xdebugRestartNginx()
		xdebugActionResult(version, true, mode)
		return
	}

	if !zendEnabled {
		// Uncomment (strip leading semicolons and spaces)
		trimmed := strings.TrimSpace(lines[zendIdx])
		lines[zendIdx] = strings.TrimLeft(trimmed, "; \t")
	}

	// Ensure xdebug.mode is set correctly
	lines = ensureIniValue(lines, zendIdx, "xdebug.mode", mode)
	lines = ensureIniValue(lines, zendIdx, "xdebug.discover_client_host", "true")
	lines = ensureIniValue(lines, zendIdx, "xdebug.start_with_request", "yes")
	if xdebugNeedsOutputDir(mode) {
		lines = ensureIniValue(lines, zendIdx, "xdebug.output_dir", ".")
	}

	if err := writeIni(iniPath, lines); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	xdebugRestartNginx()
	xdebugActionResult(version, true, mode)
}

// xdebugNeedsOutputDir returns true when the xdebug mode produces output files
// (trace or profile) and we should set xdebug.output_dir to the current directory.
func xdebugNeedsOutputDir(mode string) bool {
	return mode == "trace" || mode == "profile"
}

// xdebugActionResult outputs the result of an xdebug action (enable/disable/mode change).
// In JSON mode it emits structured output; in human mode it prints the familiar status line.
func xdebugActionResult(phpVersion string, enabled bool, mode string) {
	if jsonOutput {
		result := map[string]interface{}{
			"phpVersion": phpVersion,
			"enabled":    enabled,
			"mode":       mode,
		}
		if xdebugNeedsOutputDir(mode) {
			result["outputDir"] = "."
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	if enabled {
		fmt.Printf("  ✅ xdebug enabled (mode: %s)\n", mode)
		if xdebugNeedsOutputDir(mode) {
			fmt.Println("  📂 output_dir set to current directory (.)")
		}
	} else {
		fmt.Println("  ⏸️  xdebug disabled")
	}
}

// xdebugRestartNginx restarts nginx so that xdebug config changes take effect on served sites.
func xdebugRestartNginx() {
	if err := restartNginx(); err != nil {
		logVerbose("nginx restart failed: %v", err)
		return
	}
	if !jsonOutput {
		fmt.Println("  ↻ nginx restarted")
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

	phpPath, version, err := resolveCurrentPHP(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	phpDir := filepath.Dir(phpPath)

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

	if !jsonOutput {
		fmt.Printf("PHP %s — %s\n", version, iniPath)
	}
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

	if jsonOutput {
		status := map[string]interface{}{
			"phpVersion": version,
			"enabled":    enabled,
			"mode":       mode,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(status)
		return
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

// cmdXdebugRun executes a PHP command with xdebug enabled via -d flags.
// Stateless: xdebug is injected for that invocation only, config is never touched.
//
// Usage: shp xdebug run <mode> -- <command...>
//
// Examples:
//
//	shp xdebug run trace -- php artisan migrate
//	shp xdebug run profile -- php artisan test
//	shp xdebug run debug -- php script.php
func cmdXdebugRun() {
	// shp xdebug run <mode> -- <command...>
	// os.Args[0]=shp [1]=xdebug [2]=run [3]=mode ... [sep]=-- [cmd...]
	if len(os.Args) < 4 || os.Args[3] == "-h" || os.Args[3] == "--help" {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]interface{}{
				"command":     "xdebug run",
				"usage":       "shp xdebug run <mode> -- <command...>",
				"description": "Stateless: run a command with xdebug (one-off, no config change)",
			})
			return
		}
		fmt.Println("Usage: shp xdebug run <mode> -- <command...>")
		fmt.Println()
		fmt.Println("Stateless: run a command with xdebug enabled (one-off, no config change).")
		fmt.Println()
		fmt.Println("Modes: debug, coverage, profile, trace")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shp xdebug run trace -- php artisan migrate")
		fmt.Println("  shp xdebug run profile -- php artisan test")
		fmt.Println("  shp xdebug run debug -- php script.php")
		return
	}

	mode := strings.ToLower(os.Args[3])

	// Validate mode (off and toggle don't make sense here)
	validRunModes := map[string]bool{
		"debug":          true,
		"coverage":       true,
		"debug,coverage": true,
		"coverage,debug": true,
		"profile":        true,
		"trace":          true,
	}
	if !validRunModes[mode] {
		fmt.Fprintf(os.Stderr, "Error: invalid mode %q for xdebug run\n", mode)
		fmt.Fprintf(os.Stderr, "Valid modes: debug, coverage, debug,coverage, profile, trace\n")
		os.Exit(1)
	}

	// Find the "--" separator
	sepIdx := -1
	for i := 4; i < len(os.Args); i++ {
		if os.Args[i] == "--" {
			sepIdx = i
			break
		}
	}
	if sepIdx == -1 || sepIdx >= len(os.Args)-1 {
		fmt.Fprintf(os.Stderr, "Error: missing command after '--'\n")
		fmt.Fprintf(os.Stderr, "Usage: shp xdebug run <mode> -- <command...>\n")
		os.Exit(1)
	}

	cmdArgs := os.Args[sepIdx+1:]

	// Resolve PHP
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	phpPath, version, err := resolveCurrentPHP(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if version == "" {
		fmt.Fprintf(os.Stderr, "Error: could not determine PHP version\n")
		os.Exit(1)
	}

	// Verify xdebug DLL exists
	dllPath := xdebugDLLPath(version)
	if _, err := os.Stat(dllPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: xdebug DLL not found at %s\n", dllPath)
		os.Exit(1)
	}

	// Build xdebug -d flags
	xdebugFlags := []string{
		"-d", "zend_extension=" + dllPath,
		"-d", "xdebug.mode=" + mode,
		"-d", "xdebug.start_with_request=yes",
	}
	if xdebugNeedsOutputDir(mode) {
		xdebugFlags = append(xdebugFlags, "-d", "xdebug.output_dir=.")
	}

	// Determine how to run the command
	cmdName := strings.ToLower(cmdArgs[0])
	extDir := filepath.Join(filepath.Dir(phpPath), "ext")

	var execArgs []string
	switch cmdName {
	case "php", "php.exe":
		// Direct PHP invocation: inject xdebug flags before user args
		execArgs = append(xdebugFlags, "-d", "extension_dir="+extDir)
		execArgs = append(execArgs, cmdArgs[1:]...)
	case "composer", "composer.exe":
		// Composer: run via PHP with xdebug flags
		composerPhar := filepath.Join(herdHome(), "composer.phar")
		execArgs = append(xdebugFlags, "-d", "extension_dir="+extDir)
		execArgs = append(execArgs, composerPhar)
		execArgs = append(execArgs, cmdArgs[1:]...)
	default:
		// Other commands: cannot inject -d flags, run with PATH override
		// and xdebug env vars as a best-effort fallback
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		phpBinDir := filepath.Dir(phpPath)
		env := os.Environ()
		env = append(env, "PATH="+phpBinDir+";"+os.Getenv("PATH"))
		env = append(env, "XDEBUG_MODE="+mode)
		cmd.Env = env
		execCmdResult(cmd, map[string]interface{}{"phpVersion": version, "mode": mode})
		return
	}

	cmd := exec.Command(phpPath, execArgs...)
	execCmdResult(cmd, map[string]interface{}{"phpVersion": version, "mode": mode})
}
