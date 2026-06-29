package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// cmdDoctor diagnoses common issues that prevent Shepherd from working correctly.
func cmdDoctor() {
	type checkResult struct {
		Name   string `json:"name"`
		Status string `json:"status"` // "ok", "warning", "error"
		Detail string `json:"detail,omitempty"`
		Fix    string `json:"fix,omitempty"`
	}
	var checks []checkResult

	addCheck := func(name, status, detail, fix string) {
		checks = append(checks, checkResult{Name: name, Status: status, Detail: detail, Fix: fix})
	}

	if !jsonOutput {
		fmt.Println("shp doctor")
		fmt.Println()
	}

	issues := 0

	// 0. Check Herd is installed
	if !checkHerd() {
		if !jsonOutput {
			fmt.Printf("  ✗ Laravel Herd is not installed (expected %s)\n", herdHome())
			fmt.Printf("    → Install from https://herd.laravel.com\n")
		}
		addCheck("herd", "error", "not installed", "Install from https://herd.laravel.com")
		issues++
	} else {
		if !jsonOutput {
			fmt.Printf("  ✓ Laravel Herd found\n")
		}
		addCheck("herd", "ok", "", "")
	}

	// 1. Check .phpversion in cwd
	cwd, err := os.Getwd()
	if err != nil {
		if !jsonOutput {
			fmt.Printf("  ✗ Cannot get working directory: %v\n", err)
		}
		addCheck("phpversion", "error", err.Error(), "")
		issues++
	} else {
		ver := findPHPVersion(cwd)
		if ver != "" {
			// Validate that the PHP binary exists
			_, resolveErr := resolveFromVersion(ver)
			if resolveErr != nil {
				if !jsonOutput {
					fmt.Printf("  ✗ .phpversion requests PHP %s, but it is not installed\n", ver)
					fmt.Printf("    → Install PHP %s via Herd, or change .phpversion\n", ver)
				}
				addCheck("phpversion", "error", "PHP "+ver+" not installed", "Install PHP "+ver+" via Herd, or change .phpversion")
				issues++
			} else {
				if !jsonOutput {
					fmt.Printf("  ✓ .phpversion: %s (installed)\n", ver)
				}
				addCheck("phpversion", "ok", ver, "")
			}
		} else {
			if !jsonOutput {
				fmt.Printf("  • No .phpversion found (will use Herd global)\n")
			}
			addCheck("phpversion", "ok", "none (using Herd global)", "")
		}
	}

	// 2. Check shims exist
	dir := shimDir()
	phpShim := filepath.Join(dir, "php.exe")
	composerShim := filepath.Join(dir, "composer.exe")

	if _, err := os.Stat(phpShim); err != nil {
		if !jsonOutput {
			fmt.Printf("  ✗ php.exe shim not found at %s\n", phpShim)
			fmt.Printf("    → Run: shp install\n")
		}
		addCheck("phpShim", "error", "not found", "shp install")
		issues++
	} else {
		if !jsonOutput {
			fmt.Printf("  ✓ php.exe shim installed\n")
		}
		addCheck("phpShim", "ok", "", "")
	}

	if _, err := os.Stat(composerShim); err != nil {
		if !jsonOutput {
			fmt.Printf("  ✗ composer.exe shim not found at %s\n", composerShim)
			fmt.Printf("    → Run: shp install\n")
		}
		addCheck("composerShim", "error", "not found", "shp install")
		issues++
	} else {
		if !jsonOutput {
			fmt.Printf("  ✓ composer.exe shim installed\n")
		}
		addCheck("composerShim", "ok", "", "")
	}

	// 3. Check PATH order (User PATH from registry)
	userPath, _, pathErr := getUserPath()
	shimIndex := -1
	herdIndex := -1
	if pathErr != nil {
		if !jsonOutput {
			fmt.Printf("  ✗ Cannot read User PATH: %v\n", pathErr)
		}
		addCheck("pathOrder", "error", pathErr.Error(), "")
		issues++
	} else {
		entries := strings.Split(userPath, ";")
		herdBin := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "bin")

		for i, e := range entries {
			clean := strings.TrimRight(e, `\`)
			if strings.EqualFold(clean, strings.TrimRight(dir, `\`)) {
				shimIndex = i
			}
			if strings.EqualFold(clean, herdBin) {
				herdIndex = i
			}
		}

		if shimIndex == -1 {
			if !jsonOutput {
				fmt.Printf("  ✗ Shepherd shim directory is NOT in User PATH\n")
				fmt.Printf("    → Run: shp install\n")
			}
			addCheck("pathOrder", "error", "shim directory not in PATH", "shp install")
			issues++
		} else if herdIndex != -1 && shimIndex > herdIndex {
			if !jsonOutput {
				fmt.Printf("  ✗ Shepherd is AFTER Herd in PATH (position %d vs %d)\n", shimIndex+1, herdIndex+1)
				fmt.Printf("    → Run: shp install\n")
			}
			addCheck("pathOrder", "error", "Shepherd is after Herd in PATH", "shp install")
			issues++
		} else {
			if !jsonOutput {
				fmt.Printf("  ✓ PATH order (registry): Shepherd is before Herd\n")
			}
			addCheck("pathOrder", "ok", "", "")
		}
	}

	// 4. Check for shell aliases that override Shepherd
	aliasIssues := doctorCheckAliases()
	if jsonOutput {
		if aliasIssues > 0 {
			addCheck("shellAliases", "error", fmt.Sprintf("%d conflicting alias(es)", aliasIssues), "Remove aliases or guard them with shp check")
		} else {
			addCheck("shellAliases", "ok", "", "")
		}
	}
	issues += aliasIssues

	// 5. Check that where.exe php/composer resolve to Shepherd shims first (session PATH)
	for _, shimCheck := range []struct {
		name string
		exe  string
		shim string
	}{
		{"wherePhp", "php", phpShim},
		{"whereComposer", "composer", composerShim},
	} {
		whereCmd := exec.Command("where.exe", shimCheck.exe)
		whereOut, err := whereCmd.Output()
		if err == nil {
			whereLines := strings.Split(strings.TrimSpace(string(whereOut)), "\r\n")
			if len(whereLines) > 0 {
				first := strings.TrimSpace(whereLines[0])
				if strings.EqualFold(first, shimCheck.shim) {
					if !jsonOutput {
						fmt.Printf("  ✓ where.exe %s → Shepherd shim\n", shimCheck.exe)
					}
					addCheck(shimCheck.name, "ok", first, "")
				} else {
					// Registry says Shepherd is first, but session disagrees → profile issue
					hint := "Check your PATH or System PATH."
					if pathErr == nil && shimIndex != -1 && (herdIndex == -1 || shimIndex < herdIndex) {
						hint = "Registry PATH is correct, but this session has a different order. A PowerShell profile or shell startup script is reordering PATH."
					}
					if !jsonOutput {
						fmt.Printf("  ✗ where.exe %s resolves to: %s\n", shimCheck.exe, first)
						if strings.HasSuffix(strings.ToLower(first), ".bat") {
							fmt.Printf("    → A .bat file takes priority over Shepherd.\n")
						} else {
							fmt.Printf("    → Expected: %s\n", shimCheck.shim)
						}
						fmt.Printf("    → %s\n", hint)
						// Check if a PowerShell profile is the culprit
						if profileOverridesPath() {
							fmt.Printf("    → Detected: PowerShell profile reorders PATH. Run: shp install\n")
						}
					}
					addCheck(shimCheck.name, "error", first, hint)
					issues++
				}
			}
		}
	}

	// 6. Check Windows Developer Mode (needed for symlinks without admin)
	devModeEnabled := checkWindowsDevMode()
	if devModeEnabled {
		if !jsonOutput {
			fmt.Printf("  ✓ Windows Developer Mode is enabled\n")
		}
		addCheck("devMode", "ok", "", "")
	} else {
		if !jsonOutput {
			fmt.Printf("  ⚠ Windows Developer Mode is disabled\n")
			fmt.Printf("    → Commands like 'php artisan storage:link' will fail without Admin privileges\n")
			fmt.Printf("    → Enable it: Settings → System → For developers → Developer Mode\n")
		}
		addCheck("devMode", "warning", "disabled", "Settings → System → For developers → Developer Mode")
		issues++
	}

	// 7. Check Composer global vendor/bin is in PATH
	composerGlobalBin := filepath.Join(os.Getenv("APPDATA"), "Composer", "vendor", "bin")
	if userPath != "" {
		composerInPath := false
		for _, e := range strings.Split(userPath, ";") {
			if strings.EqualFold(strings.TrimRight(e, `\`), strings.TrimRight(composerGlobalBin, `\`)) {
				composerInPath = true
				break
			}
		}
		if composerInPath {
			if !jsonOutput {
				fmt.Printf("  ✓ Composer global bin is in PATH\n")
			}
			addCheck("composerGlobalBin", "ok", "", "")
		} else {
			if !jsonOutput {
				fmt.Printf("  ⚠ %s is not in PATH\n", composerGlobalBin)
				fmt.Printf("    → Global Composer tools (laravel, phpstan, etc.) won't be found\n")
				fmt.Printf("    → Add it to your User PATH or run: setx PATH \"%%PATH%%;%s\"\n", composerGlobalBin)
			}
			addCheck("composerGlobalBin", "warning", "not in PATH", "Add "+composerGlobalBin+" to User PATH")
			issues++
		}
	}

	// 8. Check CA certificate bundle
	pemPath := cacertPath()
	if _, err := os.Stat(pemPath); err == nil {
		if !jsonOutput {
			fmt.Printf("  ✓ CA certificate bundle found at %s\n", pemPath)
		}
		addCheck("cacert", "ok", pemPath, "")
	} else {
		if !jsonOutput {
			fmt.Printf("  ⚠ No CA certificate bundle found\n")
			fmt.Printf("    → HTTPS requests from PHP CLI (Http::get, composer) may fail with cURL error 60\n")
			fmt.Printf("    → Run any php command to auto-configure, or reinstall Herd\n")
		}
		addCheck("cacert", "warning", "not found", "Run any php command to auto-configure, or reinstall Herd")
		issues++
	}

	// 9. Check nginx config validity
	if checkHerd() {
		nginxBin := filepath.Join(os.Getenv("PROGRAMFILES"), "Herd", "resources", "app.asar.unpacked", "resources", "bin", "nginx", "nginx.exe")
		nginxConf := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "nginx", "nginx.conf")
		if _, err := os.Stat(nginxBin); err == nil {
			nginxPrefix := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "nginx")
			nginxTestCmd := exec.Command(nginxBin, "-t", "-c", nginxConf, "-p", nginxPrefix)
			var stderr bytes.Buffer
			nginxTestCmd.Stderr = &stderr
			nginxTestCmd.Stdout = nil
			testErr := nginxTestCmd.Run()
			output := stderr.String()
			if testErr == nil || strings.Contains(output, "syntax is ok") || strings.Contains(output, "test is successful") {
				if !jsonOutput {
					fmt.Printf("  ✓ nginx config is valid\n")
				}
				addCheck("nginxConfig", "ok", "", "")
			} else {
				errDetail := strings.TrimSpace(output)
				if errDetail == "" {
					errDetail = testErr.Error()
				}
				if !jsonOutput {
					fmt.Printf("  ✗ nginx config has errors\n")
					fmt.Printf("    → %s\n", errDetail)
					fmt.Printf("    → Check files in %s\n", nginxConfDir())
				}
				addCheck("nginxConfig", "error", errDetail, "Check files in "+nginxConfDir())
				issues++
			}
		}
	}

	// 10. Reverb check (only if running inside a Laravel project that uses Reverb)
	if cwd != "" && isLaravelProject(cwd) && requiresReverb(cwd) {
		domain := findProjectDomain(cwd)
		if domain != "" {
			tld := herdTLD()
			fqdn := domain + "." + tld
			reverbPort := 8443
			certFile := filepath.Join(herdCertsDir(), fqdn+".crt")
			hasCert := false
			if _, err := os.Stat(certFile); err == nil {
				hasCert = true
			}
			listening := checkPort(fqdn, strconv.Itoa(reverbPort))

			if !hasCert {
				if !jsonOutput {
					fmt.Printf("  ⚠ Reverb: no SSL certificate for %s\n", fqdn)
					fmt.Printf("    → Run: herd secure %s\n", domain)
				}
				addCheck("reverb", "warning", "no certificate for "+fqdn, "herd secure "+domain)
				issues++
			} else if !listening {
				if !jsonOutput {
					fmt.Printf("  ⚠ Reverb: not listening on wss://%s:%d\n", fqdn, reverbPort)
					fmt.Printf("    → Start it: php artisan reverb:start --host=0.0.0.0 --port=%d\n", reverbPort)
				}
				addCheck("reverb", "warning", fmt.Sprintf("not listening on port %d", reverbPort), "php artisan reverb:start --host=0.0.0.0 --port=8443")
				issues++
			} else {
				if !jsonOutput {
					fmt.Printf("  ✓ Reverb: wss://%s:%d is reachable\n", fqdn, reverbPort)
				}
				addCheck("reverb", "ok", fmt.Sprintf("wss://%s:%d", fqdn, reverbPort), "")
			}
		}
	}

	// 11. Check that PHP-CGI processes are listening for all isolated versions
	if checkHerd() {
		// Read Herd's base PHP port from config.json (default 9000)
		basePort := 9000
		herdCfgPath := filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "config.json")
		if cfgData, err := os.ReadFile(herdCfgPath); err == nil {
			var herdCfg struct {
				BasePhpPort int `json:"basePhpPort"`
			}
			if json.Unmarshal(cfgData, &herdCfg) == nil && herdCfg.BasePhpPort > 0 {
				basePort = herdCfg.BasePhpPort
			}
		}

		// Scan nginx confs for unique $herd_sock_XX references
		reSock := regexp.MustCompile(`\$herd_sock_(\d+)`)
		confDir := nginxConfDir()
		entries, _ := os.ReadDir(confDir)
		sockVersions := map[string][]string{} // "85" → ["germineo.test"]
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(confDir, e.Name()))
			if err != nil {
				continue
			}
			matches := reSock.FindAllStringSubmatch(string(data), -1)
			for _, m := range matches {
				ver := m[1]
				domain := strings.TrimSuffix(e.Name(), ".conf")
				if !slices.Contains(sockVersions[ver], domain) {
					sockVersions[ver] = append(sockVersions[ver], domain)
				}
			}
		}

		// For each isolated version, check the port is listening
		deadVersions := 0
		for ver, domains := range sockVersions {
			port := basePort + mustAtoi(ver)
			listening := checkPort("127.0.0.1", strconv.Itoa(port))
			prettyVer := ver
			if len(ver) == 2 {
				prettyVer = string(ver[0]) + "." + ver[1:]
			}
			if listening {
				if !jsonOutput {
					fmt.Printf("  ✓ PHP %s (port %d) is running\n", prettyVer, port)
				}
				addCheck("phpCgi_"+ver, "ok", fmt.Sprintf("port %d listening", port), "")
			} else {
				siteList := strings.Join(domains, ", ")
				if !jsonOutput {
					fmt.Printf("  ✗ PHP %s (port %d) is NOT running — affects: %s\n", prettyVer, port, siteList)
					fmt.Printf("    → Restart Herd completely (quit and relaunch) to start the PHP %s service\n", prettyVer)
				}
				addCheck("phpCgi_"+ver, "error",
					fmt.Sprintf("port %d not listening, affects: %s", port, siteList),
					"Restart Herd completely to start PHP "+prettyVer)
				deadVersions++
			}
		}
		issues += deadVersions
	}

	// Output
	if jsonOutput {
		result := map[string]interface{}{
			"healthy": issues == 0,
			"issues":  issues,
			"checks":  checks,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	// Summary
	fmt.Println()
	if issues == 0 {
		fmt.Println("  No issues found. Shepherd should be working correctly.")
	} else {
		// Count errors vs warnings from the collected checks
		errors := 0
		warnings := 0
		for _, c := range checks {
			switch c.Status {
			case "error":
				errors++
			case "warning":
				warnings++
			}
		}
		if errors > 0 && warnings > 0 {
			fmt.Printf("  Found %d error(s) and %d warning(s). Fix them and run 'shp doctor' again.\n", errors, warnings)
		} else if errors > 0 {
			fmt.Printf("  Found %d error(s). Fix them and run 'shp doctor' again.\n", errors)
		} else {
			fmt.Printf("  Found %d warning(s). Fix them and run 'shp doctor' again.\n", warnings)
		}
	}
}

// profileOverridesPath checks whether a PowerShell profile exists that reorders
// PATH entries (e.g. putting Herd before Shepherd). This is a common source of
// "registry says X but session says Y" discrepancies.
func profileOverridesPath() bool {
	home := os.Getenv("USERPROFILE")
	profiles := []string{
		filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"),
		filepath.Join(home, "Documents", "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1"),
	}
	for _, p := range profiles {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		content := strings.ToLower(string(data))
		// If the profile mentions herd and manipulates $env:PATH, it's likely reordering
		if strings.Contains(content, "herd") && strings.Contains(content, "$env:path") {
			// Check if it already includes shepherd
			if !strings.Contains(content, "shepherd") {
				return true
			}
		}
	}
	return false
}

// shepherdProfilePath returns the path to Shepherd's own PowerShell profile snippet.
func shepherdProfilePath() string {
	return filepath.Join(shepherdDataDir(), "profile.ps1")
}

// shepherdProfileContent returns the PowerShell snippet that ensures Shepherd
// is first in PATH for every session, regardless of what other tools do.
func shepherdProfileContent() string {
	return `# Managed by Shepherd — ensures shp shims take priority over Herd.
# Do not edit manually; this file is regenerated by 'shp install'.
$shepherdBin = "$env:USERPROFILE\.config\shepherd\bin"
if ($env:PATH -split ';' | Where-Object { $_ -eq $shepherdBin }) {
    $parts = $env:PATH -split ';' | Where-Object { $_ -ne '' -and $_ -ne $shepherdBin }
    $env:PATH = (@($shepherdBin) + $parts) -join ';'
}
`
}

// profileSourceLine is the one-liner injected into the user's PowerShell profile.
const profileSourceLine = `. "$env:USERPROFILE\.config\shepherd\profile.ps1"`

// patchPowerShellProfile writes Shepherd's own profile.ps1 and adds a source
// line to the user's PowerShell profile(s). Returns true if anything was written.
func patchPowerShellProfile(shepherdDir string) bool {
	// 1. Write our own profile snippet
	snippetPath := shepherdProfilePath()
	os.MkdirAll(filepath.Dir(snippetPath), 0755)
	os.WriteFile(snippetPath, []byte(shepherdProfileContent()), 0644)

	// 2. Add source line to user profiles (if not already present)
	home := os.Getenv("USERPROFILE")
	profiles := []string{
		filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"),
		filepath.Join(home, "Documents", "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1"),
	}

	patched := false
	for _, p := range profiles {
		data, err := os.ReadFile(p)
		if err != nil {
			continue // Profile doesn't exist — don't create one
		}
		content := string(data)

		if strings.Contains(content, "shepherd/profile.ps1") || strings.Contains(content, `shepherd\profile.ps1`) {
			continue
		}

		// Append source line
		addition := "\n# Shepherd PATH priority\n" + profileSourceLine + "\n"
		if err := os.WriteFile(p, []byte(content+addition), 0644); err != nil {
			continue
		}
		patched = true
	}
	return patched
}

// unpatchPowerShellProfile removes the Shepherd source line from user profiles
// and deletes the Shepherd profile snippet.
func unpatchPowerShellProfile() {
	home := os.Getenv("USERPROFILE")
	profiles := []string{
		filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"),
		filepath.Join(home, "Documents", "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1"),
	}

	for _, p := range profiles {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		content := string(data)
		if !strings.Contains(content, "shepherd") {
			continue
		}
		// Remove the source line and its comment
		content = strings.ReplaceAll(content, "\n# Shepherd PATH priority\n"+profileSourceLine+"\n", "")
		content = strings.ReplaceAll(content, "# Shepherd PATH priority\n"+profileSourceLine+"\n", "")
		os.WriteFile(p, []byte(content), 0644)
	}

	// Remove our snippet file
	os.Remove(shepherdProfilePath())
}

// checkWindowsDevMode reads the registry to determine if Developer Mode is enabled.
// When disabled, symlink creation (e.g. php artisan storage:link) requires admin.
func checkWindowsDevMode() bool {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return false
	}
	defer key.Close()

	val, _, err := key.GetIntegerValue("AllowDevelopmentWithoutDevLicense")
	if err != nil {
		return false
	}
	return val == 1
}

// shellConfigFiles returns the list of shell configuration files to scan for alias conflicts.
func shellConfigFiles() []string {
	home := os.Getenv("USERPROFILE")
	return []string{
		// Bash
		filepath.Join(home, ".bash_aliases"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
		// Zsh
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".zshenv"),
		filepath.Join(home, ".zsh", "aliases.zsh"),
		// PowerShell
		filepath.Join(home, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"),
		filepath.Join(home, "Documents", "WindowsPowerShell", "Microsoft.PowerShell_profile.ps1"),
		// Git Bash (MSYS2/MinGW)
		filepath.Join(home, ".config", "git", "bash_profile"),
		// Cmder / ConEmu
		filepath.Join(home, ".config", "cmder", "user_aliases.cmd"),
		// Clink
		filepath.Join(home, ".config", "clink", "clink_start.cmd"),
		// Nushell
		filepath.Join(home, "AppData", "Roaming", "nushell", "config.nu"),
		filepath.Join(home, "AppData", "Roaming", "nushell", "env.nu"),
	}
}

// Alias detection patterns (compiled once, reused across calls).
var (
	// Bash/Zsh: alias php=... or alias composer=...
	aliasRe = regexp.MustCompile(`(?m)^\s*(?:export\s+)?alias\s+(php|composer)\s*=`)
	// PowerShell: Set-Alias php ... / New-Alias composer ... / sal / nal
	psAliasRe = regexp.MustCompile(`(?mi)^\s*(Set-Alias|New-Alias|sal|nal)\s+(php|composer)\b`)
	// Guard pattern: file already checks for shp before aliasing — considered safe.
	guardRe = regexp.MustCompile(`(?mi)command\s+-v\s+shp|which\s+shp|type\s+shp|hash\s+shp|\$\+commands\[shp\]|Get-Command\s+shp|\bshepherd[\\/]bin[\\/]shp|shp:ignore`)
)

// findUnguardedAliases returns the list of conflicting command names (php/composer)
// found in the given file content, or nil if the file is guarded or has no aliases.
func findUnguardedAliases(content string) []string {
	if guardRe.MatchString(content) {
		return nil
	}

	seen := map[string]bool{}
	// Bash/Zsh aliases
	for _, m := range aliasRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			seen[m[1]] = true
		}
	}
	// PowerShell aliases
	for _, m := range psAliasRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 2 {
			seen[m[2]] = true
		}
	}

	if len(seen) == 0 {
		return nil
	}

	var cmds []string
	for cmd := range seen {
		cmds = append(cmds, cmd)
	}
	sort.Strings(cmds)
	return cmds
}

// doctorCheckAliases scans common shell config files for aliases that override php/composer.
func doctorCheckAliases() int {
	home := os.Getenv("USERPROFILE")
	issues := 0

	for _, configFile := range shellConfigFiles() {
		data, err := os.ReadFile(configFile)
		if err != nil {
			continue
		}

		cmds := findUnguardedAliases(string(data))
		if len(cmds) == 0 {
			continue
		}

		relPath := strings.TrimPrefix(configFile, home+string(os.PathSeparator))
		issues += len(cmds)

		if !jsonOutput {
			fmt.Printf("  ✗ Shell alias found: %s aliased in ~\\%s\n", strings.Join(cmds, ", "), relPath)
			fmt.Printf("    → This overrides Shepherd. Remove the alias or guard it.\n")
		}
	}

	if issues == 0 && !jsonOutput {
		fmt.Printf("  ✓ No conflicting shell aliases found\n")
	}

	return issues
}
