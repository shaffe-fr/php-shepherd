package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
)

// extSource defines where and how to download an extension.
type extSource struct {
	// urlPattern builds the download URL.
	// Available placeholders: {version}, {phpMajMin}, {ts}, {vs}, {arch}
	// phpMajMin is e.g. "84" for PHP 8.4.
	urlPattern string

	// depsURLPattern is an optional secondary download for support libraries
	// (e.g. ImageMagick DLLs for imagick). Same placeholders available.
	depsURLPattern string
}

// extDefinition describes a supported extension and how to install it.
type extDefinition struct {
	// name is the canonical extension name (used in php.ini).
	name string

	// directive is "extension" or "zend_extension" for php.ini.
	directive string

	// source defines download locations.
	source extSource

	// postInstallMsg is shown after successful installation (e.g. external deps needed).
	postInstallMsg string

	// wingetDeps lists winget package IDs to install as prerequisites.
	wingetDeps []string
}

// extensionRegistry maps extension names to their definitions.
// Only extensions known to have working Windows NTS x64 builds are listed.
var extensionRegistry = map[string]*extDefinition{
	"imagick": {
		name:      "imagick",
		directive: "extension",
		source: extSource{
			urlPattern:     "https://downloads.php.net/~windows/pecl/releases/imagick/{version}/php_imagick-{version}-{phpMajMin}-{ts}-{vs}-{arch}.zip",
			depsURLPattern: "https://windows.php.net/downloads/pecl/deps/ImageMagick-7.1.1-41-{vs}-{arch}.zip",
		},
	},
	"redis": {
		name:      "redis",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/redis/{version}/php_redis-{version}-{phpMajMin}-{ts}-{vs}-{arch}.zip",
		},
	},
	"igbinary": {
		name:      "igbinary",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/igbinary/{version}/php_igbinary-{version}-{phpMajMin}-{ts}-{vs}-{arch}.zip",
		},
	},
	"sqlsrv": {
		name:      "sqlsrv",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/sqlsrv/{version}/php_sqlsrv-{version}-{phpMajMin}-{ts}-{vs}-{arch}.zip",
		},
		wingetDeps: []string{"Microsoft.ODBCDriver.18.ForSQLServer"},
	},
	"pdo_sqlsrv": {
		name:      "pdo_sqlsrv",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/pdo_sqlsrv/{version}/php_pdo_sqlsrv-{version}-{phpMajMin}-{ts}-{vs}-{arch}.zip",
		},
		wingetDeps: []string{"Microsoft.ODBCDriver.18.ForSQLServer"},
	},
	"memcached": {
		name:      "memcached",
		directive: "extension",
		source: extSource{
			urlPattern: "https://github.com/lifenglsf/php_memcached_dll/releases/download/nightly/php_memcached-3.3.0-{phpMajMin}-{ts}-{vs}-{arch}.zip",
		},
	},
}

// buildExtURL constructs the download URL from a pattern and parameters.
func buildExtURL(pattern, version, phpVersion, ts, vs, arch string) string {
	phpMajMin := strings.ReplaceAll(phpVersion, ".", "")
	r := strings.NewReplacer(
		"{version}", version,
		"{phpMajMin}", phpMajMin,
		"{ts}", ts,
		"{vs}", vs,
		"{arch}", arch,
	)
	return r.Replace(pattern)
}

// listSupportedExtensions returns a sorted list of supported extension names.
func listSupportedExtensions() []string {
	names := make([]string, 0, len(extensionRegistry))
	for name := range extensionRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// installWingetDeps installs system-level dependencies via winget.
// Returns nil if all deps are satisfied (already installed or newly installed).
func installWingetDeps(deps []string) error {
	if len(deps) == 0 {
		return nil
	}

	// Verify winget is available
	if _, err := exec.LookPath("winget"); err != nil {
		return fmt.Errorf("winget not found. Install it from the Microsoft Store (App Installer)")
	}

	for _, pkg := range deps {
		// Check if already installed
		checkCmd := exec.Command("winget", "list", "--id", pkg, "--accept-source-agreements")
		checkCmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
		if out, err := checkCmd.Output(); err == nil && strings.Contains(string(out), pkg) {
			fmt.Printf("  ✓ %s already installed\n", pkg)
			continue
		}

		// Install via winget
		fmt.Printf("  Installing %s via winget...\n", pkg)
		installCmd := exec.Command("winget", "install", pkg,
			"--accept-package-agreements", "--accept-source-agreements")
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("failed to install %s: %w", pkg, err)
		}
		fmt.Printf("  ✓ %s installed\n", pkg)
	}
	return nil
}
