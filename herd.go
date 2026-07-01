package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// herdHome returns the Herd bin directory.
func herdHome() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "bin")
}

// checkHerd returns true if Herd appears to be installed (bin directory exists).
func checkHerd() bool {
	info, err := os.Stat(herdHome())
	return err == nil && info.IsDir()
}

// errHerdNotInstalled is returned when Herd is not detected.
var errHerdNotInstalled = fmt.Errorf("shepherd requires Laravel Herd for Windows.\nInstall it from https://herd.laravel.com")

// requireHerd returns an error if Herd is not installed.
func requireHerd() error {
	if !checkHerd() {
		return errHerdNotInstalled
	}
	return nil
}

// herdConfigPath returns the path to Herd's global valet config.json.
func herdConfigPath() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "valet", "config.json")
}

// herdParkedPaths reads Herd's config.json and returns the list of parked paths.
func herdParkedPaths() []string {
	data, err := os.ReadFile(herdConfigPath())
	if err != nil {
		return nil
	}
	var config struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	return config.Paths
}

// herdCertsDir returns the Herd certificates directory.
func herdCertsDir() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "valet", "Certificates")
}

// herdTLD reads the TLD from Herd's config.json (defaults to "test").
func herdTLD() string {
	data, err := os.ReadFile(herdConfigPath())
	if err != nil {
		return "test"
	}
	var config struct {
		TLD string `json:"tld"`
	}
	if err := json.Unmarshal(data, &config); err != nil || config.TLD == "" {
		return "test"
	}
	return config.TLD
}

// findProjectDomain resolves the domain name for the current project directory
// by scanning Herd's parked paths for a matching entry.
func findProjectDomain(projectDir string) string {
	physicalDir := strings.ToLower(resolvePhysicalPath(projectDir))

	for _, dir := range herdParkedPaths() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			entryPath := filepath.Join(dir, entry.Name())
			resolved := strings.ToLower(resolvePhysicalPath(entryPath))
			if resolved == physicalDir {
				return entry.Name()
			}
		}
	}
	return ""
}

// herdBasePort reads Herd's base PHP port from config.json (default 9000).
func herdBasePort() int {
	cfgPath := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return 9000
	}
	var cfg struct {
		BasePhpPort int `json:"basePhpPort"`
	}
	if json.Unmarshal(data, &cfg) == nil && cfg.BasePhpPort > 0 {
		return cfg.BasePhpPort
	}
	return 9000
}

// ensurePhpCgiRunning checks that PHP-CGI for the given version (e.g. "8.1")
// is listening on its expected port. If not, it restarts Herd's PHP services
// and waits up to 10 seconds for the port to come up.
// Returns true if the service is (now) running, false if it could not be started.
func ensurePhpCgiRunning(phpVersion string) bool {
	nodot := strings.ReplaceAll(phpVersion, ".", "")
	port := herdBasePort() + mustAtoi(nodot)
	portStr := strconv.Itoa(port)

	// Retry a few times to avoid false negatives from transient TCP failures
	// (antivirus scans, brief backlog under load, etc.)
	for attempts := 0; attempts < 3; attempts++ {
		if checkPort("127.0.0.1", portStr) {
			return true
		}
		if attempts < 2 {
			time.Sleep(250 * time.Millisecond)
		}
	}

	// PHP-CGI is not running — attempt restart via herd.phar
	logVerbose("PHP %s (port %d) is not responding, restarting Herd PHP services...", phpVersion, port)
	if !quiet {
		fmt.Fprintf(os.Stderr, "shp: PHP %s service is not running, restarting Herd...\n", phpVersion)
	}

	bootstrap, err := mostRecentPHP()
	if err != nil {
		return false
	}
	herdPhar := filepath.Join(herdHome(), "herd.phar")
	if _, err := os.Stat(herdPhar); err != nil {
		return false
	}

	cmd := exec.Command(bootstrap, herdPhar, "restart", "php")
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()

	// Wait for the port to come up (poll every 500ms, up to 10s)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if checkPort("127.0.0.1", portStr) {
			if !quiet {
				fmt.Fprintf(os.Stderr, "shp: PHP %s service is now running.\n", phpVersion)
			}
			return true
		}
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "shp: PHP %s service did not start. Restart Herd manually.\n", phpVersion)
	}
	return false
}

// herdInstallPHP installs a PHP version via herd.phar.
// It uses the highest installed PHP as bootstrap to run herd.phar.
func herdInstallPHP(version string) error {
	bootstrap, err := mostRecentPHP()
	if err != nil {
		return fmt.Errorf("no PHP available to bootstrap Herd: %w", err)
	}

	herdPhar := filepath.Join(herdHome(), "herd.phar")
	if _, err := os.Stat(herdPhar); err != nil {
		return fmt.Errorf("herd.phar not found at %s", herdPhar)
	}

	fmt.Printf("  Installing PHP %s via Herd...\n", version)

	cmd := exec.Command(bootstrap, herdPhar, "php:install", version)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("herd php:install %s failed: %w", version, err)
	}

	fmt.Printf("  ✓ PHP %s installed\n", version)
	return nil
}
