package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

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

// verifyExtension runs php -m and checks if the extension is loaded.
func verifyExtension(phpExe, extDir, extName string) bool {
	cmd := exec.Command(phpExe, "-d", "extension_dir="+extDir, "-m")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false
	}
	// Check stderr for warnings about our extension failing to load.
	errOut := strings.ToLower(stderr.String())
	if strings.Contains(errOut, "unable to load") && strings.Contains(errOut, strings.ToLower(extName)) {
		return false
	}
	return strings.Contains(strings.ToLower(stdout.String()), strings.ToLower(extName))
}
