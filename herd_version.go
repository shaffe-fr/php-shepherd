package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
// purely on-disk so it works with the app closed. Several independent
// Herd 2 signals are accepted, so that a future Herd moving or renaming
// one of them does not silently turn every service restart into a no-op:
// config\herd.json, the workspace registry, or an apiPort in the app
// config. When none is present we assume 1.x, which is safe — every
// Herd 1 layout assumption (bin\phpNN, config\valet\*, php.bat) still
// holds on 2.x.
func isHerd2() bool {
	if _, err := os.Stat(herdVersionConfigPath()); err == nil {
		return true
	}
	if _, err := os.Stat(herdWorkspacesRegistryPath()); err == nil {
		return true
	}
	return herdConfiguredApiPort() > 0
}

// herdAppConfigPath returns the path of the app's own config\config.json
// (basePhpPort, apiPort, activeVersion, ...).
func herdAppConfigPath() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "config.json")
}

// herdConfiguredApiPort returns the "apiPort" of config\config.json, or 0
// when the file or the key is absent.
func herdConfiguredApiPort() int {
	data, err := os.ReadFile(herdAppConfigPath())
	if err != nil {
		return 0
	}
	var cfg struct {
		ApiPort int `json:"apiPort"`
	}
	if json.Unmarshal(data, &cfg) == nil && cfg.ApiPort > 0 {
		return cfg.ApiPort
	}
	return 0
}

// herdApiPort returns the port of the Herd app's local HTTP API. On
// Herd 2 the app exposes the API that herd.phar delegates to for
// restart/use/status; the port comes from config\config.json ("apiPort"),
// defaulting to the HerdApi::DEFAULT_PORT value of 2304.
func herdApiPort() int {
	if p := herdConfiguredApiPort(); p > 0 {
		return p
	}
	return 2304
}

// herdAppRunning reports whether the Herd desktop app is running. Like
// Herd's own isRunning(), it issues an HTTP GET on /cli/mail-config and
// requires a 200: a bare TCP dial would take any unrelated dev server
// sitting on the API port for the app. Read-only route, 1 second timeout.
func herdAppRunning() bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(herdApiPort()) + "/cli/mail-config")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
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
	defer func() { _ = resp.Body.Close() }()
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
// On Herd 2 it reads Herd's own record of installed builds
// (config\php.json), skipping release candidates, and falls back to the
// highest installed PHP. On Herd 1 it keeps the historical behavior
// (highest installed PHP).
//
// No subprocess is spawned on purpose: Herd's bin\find-usable-php.php
// oracle runs shell_exec('php -r ...'), and that `php` resolves through
// the PATH to Shepherd's own shim, which resolves the global PHP by
// calling this function again — an unbounded re-entrancy loop. Reading
// php.json yields the same answer (the newest stable build) without it,
// and with no interpreter to hang on, no deadline is needed either.
func herdBootstrapPHP() (string, error) {
	if isHerd2() {
		if php, ok := herdStablePHP(); ok {
			return php, nil
		}
	}
	return mostRecentPHP()
}

// herdPHPConfigPath returns the path of Herd's installed-builds record.
func herdPHPConfigPath() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "php.json")
}

// herdStablePHP returns the highest installed PHP build recorded in
// config\php.json ("installed_<major.minor>": "<full version>") that is
// not a pre-release (RC, alpha, beta) and whose php.exe exists.
// ("", false) when the file is missing, malformed or lists nothing usable.
func herdStablePHP() (string, bool) {
	data, err := os.ReadFile(herdPHPConfigPath())
	if err != nil {
		return "", false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return "", false
	}
	var versions []string
	for key, val := range raw {
		ver, ok := strings.CutPrefix(key, "installed_")
		if !ok || strings.HasPrefix(ver, "internal_") {
			continue
		}
		var full string
		if json.Unmarshal(val, &full) != nil {
			continue
		}
		upper := strings.ToUpper(full)
		if strings.Contains(upper, "RC") || strings.Contains(upper, "ALPHA") || strings.Contains(upper, "BETA") {
			continue
		}
		versions = append(versions, ver)
	}
	sort.Slice(versions, func(i, j int) bool {
		return phpDirVersion("php"+strings.ReplaceAll(versions[i], ".", "")) >
			phpDirVersion("php"+strings.ReplaceAll(versions[j], ".", ""))
	})
	for _, ver := range versions {
		php := filepath.Join(herdHome(), "php"+strings.ReplaceAll(ver, ".", ""), "php.exe")
		if _, err := os.Stat(php); err != nil {
			continue
		}
		if valid, err := validateHerdPHPPath(php); err == nil {
			return valid, true
		}
	}
	return "", false
}
