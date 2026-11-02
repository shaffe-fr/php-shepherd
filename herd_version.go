package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// herdVersionConfigPath returns the path of config\herd.json, a file that
// only exists on Herd 2.x (created by the desktop app).
func herdVersionConfigPath() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "herd.json")
}

// isHerd2 reports whether the installed Herd is 2.x or newer. Detection is
// purely on-disk so it works with the app closed: the marker above does
// not exist on Herd 1.x. When it is absent we assume 1.x, which is safe —
// every Herd 1 layout assumption (bin\phpNN, config\valet\*, php.bat)
// still holds on 2.x.
func isHerd2() bool {
	_, err := os.Stat(herdVersionConfigPath())
	return err == nil
}

// herdAppConfigPath returns the path of the app's own config\config.json
// (basePhpPort, apiPort, activeVersion, ...).
func herdAppConfigPath() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "config.json")
}

// herdApiPort returns the port of the Herd app's local HTTP API. On
// Herd 2 the app exposes the API that herd.phar delegates to for
// restart/use/status; the port comes from config\config.json ("apiPort"),
// defaulting to the HerdApi::DEFAULT_PORT value of 2304.
func herdApiPort() int {
	data, err := os.ReadFile(herdAppConfigPath())
	if err != nil {
		return 2304
	}
	var cfg struct {
		ApiPort int `json:"apiPort"`
	}
	if json.Unmarshal(data, &cfg) == nil && cfg.ApiPort > 0 {
		return cfg.ApiPort
	}
	return 2304
}

// herdAppRunning reports whether the Herd desktop app is running, detected
// by dialing its local API port. Best effort, 1 second timeout: when
// nothing listens, the app is closed (or was never started).
func herdAppRunning() bool {
	return checkPort("127.0.0.1", strconv.Itoa(herdApiPort()))
}

// herdVersionString returns a human-readable Herd version: "Herd 2.0.4"
// (exact, from the app's /version endpoint while it is running), "Herd 2.x"
// (marker present, app closed) or "Herd 1.x".
func herdVersionString() string {
	if !isHerd2() {
		return "Herd 1.x"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(herdApiPort()) + "/version")
	if err != nil {
		return "Herd 2.x"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if v := strings.TrimSpace(string(body)); v != "" {
		return "Herd " + v
	}
	return "Herd 2.x"
}

// herdGlobalVersion returns the global PHP version the Herd app has
// configured (config\config.json "activeVersion", e.g. "8.4") — a second
// offline source on Herd 2, besides bin\php.bat. "" when unavailable.
func herdGlobalVersion() string {
	data, err := os.ReadFile(herdAppConfigPath())
	if err != nil {
		return ""
	}
	var cfg struct {
		ActiveVersion string `json:"activeVersion"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.ActiveVersion)
}

// herdBootstrapPHP picks the PHP interpreter used to run herd.phar.
// On Herd 2 it prefers Herd's own find-usable-php.php oracle, which skips
// broken or release-candidate builds, and falls back to the highest
// installed PHP. On Herd 1 it keeps the historical behavior (highest
// installed PHP).
func herdBootstrapPHP() (string, error) {
	if isHerd2() {
		if php, ok := herdUsablePHP(); ok {
			return php, nil
		}
	}
	return mostRecentPHP()
}

// herdUsablePHP runs Herd's bin\find-usable-php.php — a plain script with
// no framework dependencies — and returns the PHP it reports, the one
// Herd itself considers safe for running its tooling. The script is tried
// with the installed interpreters, highest first: a broken or RC build
// simply fails to run it. ("", false) when the script is absent or
// yields nothing usable; callers fall back to mostRecentPHP.
func herdUsablePHP() (string, bool) {
	script := filepath.Join(herdHome(), "find-usable-php.php")
	if _, err := os.Stat(script); err != nil {
		return "", false
	}
	// The script locates Herd's bin through HOME (or HOMEDRIVE+HOMEPATH);
	// make sure HOME is set whatever shell we were invoked from.
	env := append(os.Environ(), "HOME="+os.Getenv("USERPROFILE"))

	for _, runner := range installedPHPPaths() {
		cmd := exec.Command(runner, script)
		cmd.Env = env
		cmd.Stderr = nil
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		php := strings.TrimSpace(string(out))
		if php == "" {
			continue
		}
		// The script can report a build that is not installed (its last
		// resort is an unconditional path): reject it and fall back.
		valid, verr := validateHerdPHPPath(php)
		if verr != nil {
			break
		}
		return valid, true
	}
	return "", false
}
