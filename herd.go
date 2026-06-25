package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// requireHerd exits with a clear message if Herd is not installed.
func requireHerd() {
	if !checkHerd() {
		fmt.Fprintf(os.Stderr, "Shepherd requires Laravel Herd for Windows.\n")
		fmt.Fprintf(os.Stderr, "Install it from https://herd.laravel.com\n")
		os.Exit(1)
	}
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
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("herd php:install %s failed: %w", version, err)
	}

	fmt.Printf("  ✓ PHP %s installed\n", version)
	return nil
}
