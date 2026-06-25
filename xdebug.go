package main

import (
	"encoding/json"
	"fmt"
	"os"
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
			if err := writeIni(iniPath, lines); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("  ⏸️  xdebug disabled")
		} else if zendIdx != -1 {
			// Currently off → turn on
			lines[zendIdx] = strings.TrimPrefix(lines[zendIdx], ";")
			lines = ensureIniValue(lines, zendIdx, "xdebug.mode", "debug")
			lines = ensureIniValue(lines, zendIdx, "xdebug.discover_client_host", "true")
			lines = ensureIniValue(lines, zendIdx, "xdebug.start_with_request", "yes")
			if err := writeIni(iniPath, lines); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
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
			if err := writeIni(iniPath, lines); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
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
		if err := writeIni(iniPath, lines); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
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
		if err := writeIni(iniPath, lines); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
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

	if err := writeIni(iniPath, lines); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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
