package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// extNameRe constrains extension names to characters that can neither escape a
// directory nor a URL path. Removal deliberately accepts names absent from
// extensionRegistry (xdebug, opcache), so this format check is what keeps the
// resolved DLL path inside the ext directory.
var extNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// extVersionRe matches PECL version strings, including prerelease suffixes such
// as 3.1.6RC2. Values reach a download URL, so anything outside this shape is
// refused rather than interpolated.
var extVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+[A-Za-z0-9]*$`)

// vsVersionRe matches the Visual Studio build tag used in PECL filenames.
// Both spellings occur: vc15 for PHP 7.x, vs16/vs17 for PHP 8.x.
var vsVersionRe = regexp.MustCompile(`^(vs|vc)\d+$`)

// resolveVSTag picks the build tag for a download, honouring an explicit --vs
// over the tag derived from the PHP version.
func resolveVSTag(explicit, phpVersion string) string {
	if explicit != "" {
		return explicit
	}
	return defaultVSTag(phpVersion)
}

// defaultVSTag returns the Visual Studio build tag PECL uses for a PHP version.
// PECL publishes one toolchain per PHP branch, so a fixed tag cannot address
// more than one branch: pinning vs17 makes every PHP 8.3 or older download 404.
func defaultVSTag(phpVersion string) string {
	switch {
	case comparePHPVersion(phpVersion, "8.4") >= 0:
		return "vs17"
	case comparePHPVersion(phpVersion, "8.0") >= 0:
		return "vs16"
	default:
		return "vc15"
	}
}

// comparePHPVersion orders two "X.Y" version strings, returning a negative
// number, zero, or a positive number as a is less than, equal to, or greater
// than b. Unparseable input sorts as 0.0, which defaultVSTag treats as legacy.
func comparePHPVersion(a, b string) int {
	aMaj, aMin := splitPHPVersion(a)
	bMaj, bMin := splitPHPVersion(b)
	if aMaj != bMaj {
		return aMaj - bMaj
	}
	return aMin - bMin
}

// splitPHPVersion splits "8.4" into its major and minor components.
func splitPHPVersion(v string) (major, minor int) {
	majStr, minStr, found := strings.Cut(v, ".")
	if !found {
		return 0, 0
	}
	major, _ = strconv.Atoi(majStr)
	minor, _ = strconv.Atoi(minStr)
	return major, minor
}

// extSource defines where and how to download an extension.
type extSource struct {
	// urlPattern builds the download URL.
	// Available placeholders: {version}, {phpDotVer}, {phpMajMin}, {ts}, {vs}, {arch}
	// phpDotVer is e.g. "8.4" for PHP 8.4 (used by PECL filenames).
	// phpMajMin is e.g. "84" for PHP 8.4 (digits only, no dot).
	urlPattern string
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
	// imagick needs no separate ImageMagick download: the PECL release zip
	// ships the full CORE_RL_*/IM_MOD_RL_* set alongside php_imagick.dll
	// (verified on 3.7.0 and 3.8.1). Pulling the standalone ImageMagick
	// archive only duplicated those DLLs, added the CLI tools to the PHP
	// directory, and forced a hardcoded library version that went stale.
	"imagick": {
		name:      "imagick",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/imagick/{version}/php_imagick-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
		},
	},
	"brotli": {
		name:      "brotli",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/brotli/{version}/php_brotli-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
		},
	},
	"redis": {
		name:      "redis",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/redis/{version}/php_redis-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
		},
	},
	"igbinary": {
		name:      "igbinary",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/igbinary/{version}/php_igbinary-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
		},
	},
	"sqlsrv": {
		name:      "sqlsrv",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/sqlsrv/{version}/php_sqlsrv-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
		},
		wingetDeps: []string{"Microsoft.ODBCDriver.18.ForSQLServer"},
	},
	"pdo_sqlsrv": {
		name:      "pdo_sqlsrv",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/pdo_sqlsrv/{version}/php_pdo_sqlsrv-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
		},
		wingetDeps: []string{"Microsoft.ODBCDriver.18.ForSQLServer"},
	},
	"memcached": {
		name:      "memcached",
		directive: "extension",
		source: extSource{
			urlPattern: "https://downloads.php.net/~windows/pecl/releases/memcached/{version}/php_memcached-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
		},
	},
}

// buildExtURL constructs the download URL from a pattern and parameters.
func buildExtURL(pattern, version, phpVersion, ts, vs, arch string) string {
	phpMajMin := strings.ReplaceAll(phpVersion, ".", "")
	r := strings.NewReplacer(
		"{version}", version,
		"{phpMajMin}", phpMajMin,
		"{phpDotVer}", phpVersion,
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
		// Check if already installed using --exact + exit code (locale-independent)
		checkCmd := exec.Command("winget", "list", "--exact", "--id", pkg, "--accept-source-agreements")
		if err := checkCmd.Run(); err == nil {
			fmt.Printf("  ✓ %s already installed\n", pkg)
			continue
		}

		// Install via winget — no CREATE_NO_WINDOW so UAC prompts display correctly
		fmt.Printf("  Installing %s via winget...\n", pkg)
		fmt.Printf("    ⚠ This may require UAC elevation (Administrator prompt)\n")
		installCmd := exec.Command("winget", "install", pkg,
			"--accept-package-agreements", "--accept-source-agreements", "--silent")
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("failed to install %s (you may need to run as Administrator): %w", pkg, err)
		}
		fmt.Printf("  ✓ %s installed\n", pkg)
	}
	return nil
}
