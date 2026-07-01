package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// nginxConfDir returns the Herd nginx config directory.
func nginxConfDir() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "valet", "Nginx")
}

// findNginxConfsForProject finds all .test.conf files that correspond to the
// given physical project directory. It scans all of Herd's registered paths
// (from config.json) for directories, junctions, and symlinks that resolve
// to the same physical path.
func findNginxConfsForProject(physicalDir string) []string {
	confDir := nginxConfDir()
	physicalDirLower := strings.ToLower(physicalDir)

	// Collect all domain names that map to this physical directory
	var domains []string

	for _, dir := range herdParkedPaths() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			// Accept directories and symlinks (which may point to directories)
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			entryPath := filepath.Join(dir, entry.Name())
			resolved := strings.ToLower(resolvePhysicalPath(entryPath))
			if resolved == physicalDirLower {
				domains = append(domains, entry.Name())
			}
		}
	}

	// Deduplicate and resolve to conf file paths
	seen := map[string]bool{}
	var confs []string
	for _, domain := range domains {
		confName := domain + ".test.conf"
		if seen[confName] {
			continue
		}
		seen[confName] = true
		confPath := filepath.Join(confDir, confName)
		if _, err := os.Stat(confPath); err == nil {
			confs = append(confs, confPath)
		}
	}

	return confs
}

// nginxSyncLockPath returns the lockfile path for a given domain name.
func nginxSyncLockPath(domain string) string {
	return filepath.Join(shepherdDataDir(), ".nginx_sync_"+domain)
}

// nginxSyncAllowed checks if enough time has passed since the last sync for this domain.
// Uses a per-domain lockfile with a 3-second TTL based on ModTime.
const nginxSyncCooldown = 3 * time.Second

func nginxSyncAllowed(domain string) bool {
	lockPath := nginxSyncLockPath(domain)
	info, err := os.Stat(lockPath)
	if err != nil {
		// No lockfile → allowed
		return true
	}
	return time.Since(info.ModTime()) >= nginxSyncCooldown
}

// nginxSyncTouch updates (or creates) the lockfile timestamp for this domain.
func nginxSyncTouch(domain string) {
	lockPath := nginxSyncLockPath(domain)
	// Ensure parent directory exists
	os.MkdirAll(filepath.Dir(lockPath), 0755)
	// Create or update modification time
	f, err := os.Create(lockPath)
	if err == nil {
		f.Close()
	}
}

// updateNginxConf rewrites a single nginx conf file for the given PHP version.
// Returns true if the file was actually modified.
func updateNginxConf(confPath, version string) bool {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return false
	}
	content := string(data)

	// Bail if conf is empty (don't overwrite with regex on empty content)
	if len(strings.TrimSpace(content)) == 0 {
		return false
	}

	nodot := strings.ReplaceAll(version, ".", "")
	modified := false

	// Update ISOLATED_PHP_VERSION comment if needed
	// Herd uses inconsistent formats: sometimes "8.4", sometimes "84"
	if !strings.Contains(content, "ISOLATED_PHP_VERSION="+version) &&
		!strings.Contains(content, "ISOLATED_PHP_VERSION="+nodot) {
		reIsolated := regexp.MustCompile(`(?m)^# ISOLATED_PHP_VERSION=.*$`)
		content = reIsolated.ReplaceAllString(content, "# ISOLATED_PHP_VERSION="+version)
		modified = true
	}

	// Update herd_sock references (with or without version suffix, with or without quotes)
	expectedSock := `"$herd_sock_` + nodot + `"`
	if !strings.Contains(content, "$herd_sock_"+nodot) {
		reSock := regexp.MustCompile(`"?\$herd_sock(?:_\d+)?"?`)
		newContent := reSock.ReplaceAllLiteralString(content, expectedSock)
		if newContent != content {
			content = newContent
			modified = true
		}
	}

	// Repair empty fastcgi_pass directives (left behind by previous buggy rewrites)
	// Matches: fastcgi_pass ; OR fastcgi_pass ""; OR fastcgi_pass "";
	reEmptyPass := regexp.MustCompile(`(?m)(fastcgi_pass)\s*"?"?\s*;`)
	if reEmptyPass.MatchString(content) {
		content = reEmptyPass.ReplaceAllLiteralString(content, "fastcgi_pass "+expectedSock+";")
		modified = true
	}

	if !modified {
		return false
	}

	// Write back
	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		return false
	}
	return true
}

// syncNginx updates all Herd nginx configs for the project, then restarts nginx once.
// It resolves symlinks/junctions to find the physical project path, scans for all
// related conf files (including aliases via herd link), applies per-domain rate
// limiting, and triggers a single restart if any conf was modified.
// Returns true if any config was actually changed (indicating a version switch).
func syncNginx(projectDir, version string) bool {
	// Resolve NTFS junctions/symlinks to the real physical path
	physicalDir := resolvePhysicalPath(projectDir)

	// Find all nginx conf files that reference this project
	confs := findNginxConfsForProject(physicalDir)
	if len(confs) == 0 {
		return false
	}

	needNginxRestart := false

	for _, confPath := range confs {
		// Extract domain name from conf filename (e.g. "my-app.test.conf" → "my-app.test")
		domain := strings.TrimSuffix(filepath.Base(confPath), ".conf")

		// Per-domain rate limiting: skip if recently synced
		if !nginxSyncAllowed(domain) {
			continue
		}

		if updateNginxConf(confPath, version) {
			nginxSyncTouch(domain)
			needNginxRestart = true
		}

	}

	// Single nginx restart for all modified confs
	if !needNginxRestart {
		return false
	}

	logVerbose("restarting nginx (configs modified)")
	bootstrap, err := mostRecentPHP()
	if err != nil {
		return true
	}
	herdPhar := filepath.Join(herdHome(), "herd.phar")
	cmd := exec.Command(bootstrap, herdPhar, "restart", "nginx")
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
	return true
}
