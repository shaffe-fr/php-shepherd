package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultVSTag(t *testing.T) {
	tests := []struct {
		phpVersion string
		want       string
	}{
		{"8.5", "vs17"},
		{"8.4", "vs17"},
		{"8.3", "vs16"},
		{"8.0", "vs16"},
		{"7.4", "vc15"},
		{"8.10", "vs17"},
	}

	for _, tt := range tests {
		if got := defaultVSTag(tt.phpVersion); got != tt.want {
			t.Errorf("defaultVSTag(%q) = %q, want %q", tt.phpVersion, got, tt.want)
		}
	}
}

func TestResolveVSTagPrefersExplicit(t *testing.T) {
	if got := resolveVSTag("vc15", "8.4"); got != "vc15" {
		t.Errorf("explicit tag not honoured: got %q", got)
	}
	if got := resolveVSTag("", "8.3"); got != "vs16" {
		t.Errorf("derived tag = %q, want vs16", got)
	}
}

// PHP 8.3 downloads are published under vs16, so the pinned vs17 default made
// `ext add --php=all` 404 on every branch older than 8.4.
func TestBuildExtURLForOlderBranch(t *testing.T) {
	def := extensionRegistry["pcov"]
	url := buildExtURL(def.source.urlPattern, "1.0.12", "8.3", "nts", resolveVSTag("", "8.3"), "x64")

	want := "php_pcov-1.0.12-8.3-nts-vs16-x64.zip"
	if !strings.HasSuffix(url, want) {
		t.Errorf("url = %q, want suffix %q", url, want)
	}
}

func TestIniTruthy(t *testing.T) {
	falsy := []string{"", "0", "off", "Off", "false", "no", "none", `"0"`}
	for _, v := range falsy {
		if iniTruthy(v) {
			t.Errorf("iniTruthy(%q) = true, want false", v)
		}
	}
	truthy := []string{"1", "on", "On", "true", "yes"}
	for _, v := range truthy {
		if !iniTruthy(v) {
			t.Errorf("iniTruthy(%q) = false, want true", v)
		}
	}
}

func TestIniValueLastAssignmentWins(t *testing.T) {
	lines := []string{"pcov.enabled=1", ";pcov.enabled=0", "pcov.enabled = 0"}
	value, found := iniValue(lines, "pcov.enabled")
	if !found {
		t.Fatal("pcov.enabled not found")
	}
	if value != "0" {
		t.Errorf("value = %q, want %q", value, "0")
	}
}

func TestIniValueIgnoresCommentedAndPrefixedKeys(t *testing.T) {
	lines := []string{";pcov.enabled=1", "pcov.enabled_extra=1"}
	if _, found := iniValue(lines, "pcov.enabled"); found {
		t.Error("pcov.enabled reported as set by a comment or a longer key")
	}
}

func TestEnsureIniValueDoesNotMatchLongerKey(t *testing.T) {
	lines := []string{"xdebug.mode_extra=1"}
	got := ensureIniValue(lines, 0, "xdebug.mode", "coverage")

	if got[0] != "xdebug.mode_extra=1" {
		t.Errorf("unrelated key was overwritten: %q", got[0])
	}
	if len(got) != 2 || got[1] != "xdebug.mode=coverage" {
		t.Errorf("directive not inserted: %#v", got)
	}
}

func TestApplyIniDefaultsInsertsBelowExtension(t *testing.T) {
	iniPath := filepath.Join(t.TempDir(), "php.ini")
	content := "[PHP]\nmemory_limit=512M\nextension=pcov\n"
	if err := os.WriteFile(iniPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := applyIniDefaults(iniPath, "pcov", [][2]string{{"pcov.enabled", "0"}}); err != nil {
		t.Fatalf("applyIniDefaults: %v", err)
	}

	data, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "extension=pcov\npcov.enabled=0") {
		t.Errorf("default not written below the extension line:\n%s", data)
	}
}

func TestApplyIniDefaultsKeepsExistingValue(t *testing.T) {
	iniPath := filepath.Join(t.TempDir(), "php.ini")
	content := "extension=pcov\npcov.enabled=1\n"
	if err := os.WriteFile(iniPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := applyIniDefaults(iniPath, "pcov", [][2]string{{"pcov.enabled", "0"}}); err != nil {
		t.Fatalf("applyIniDefaults: %v", err)
	}

	data, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("a deliberate value was overwritten:\n%s", data)
	}
}

func TestIniAssignsExtension(t *testing.T) {
	lines := []string{
		"extension=pcov",
		`zend_extension=C:\Program Files\Herd\...\xdebug-8.4.dll`,
		";extension=redis",
	}

	if !iniAssignsExtension(lines, "extension", "pcov") {
		t.Error("extension=pcov not detected")
	}
	if !iniAssignsExtension(lines, "zend_extension", "xdebug") {
		t.Error("xdebug DLL path not detected")
	}
	if iniAssignsExtension(lines, "extension", "redis") {
		t.Error("commented extension reported as active")
	}
}

func TestPlanCoveragePrefersPcov(t *testing.T) {
	plan, err := planCoverage(coverageDrivers{
		extDir:    `C:\php\ext`,
		pcovDLL:   `C:\php\ext\php_pcov.dll`,
		xdebugDLL: `C:\xdebug.dll`,
	})
	if err != nil {
		t.Fatalf("planCoverage: %v", err)
	}
	if plan.driver != "pcov" {
		t.Errorf("driver = %q, want pcov", plan.driver)
	}

	flags := strings.Join(plan.flags, " ")
	if !strings.Contains(flags, "pcov.enabled=1") {
		t.Errorf("pcov not enabled for the run: %q", flags)
	}
	if strings.Contains(flags, "xdebug") {
		t.Errorf("xdebug injected alongside pcov: %q", flags)
	}
}

// Re-declaring a module php.ini already loads makes PHP warn on every run.
func TestPlanCoverageSkipsRedundantLoad(t *testing.T) {
	plan, err := planCoverage(coverageDrivers{
		extDir:    `C:\php\ext`,
		pcovDLL:   `C:\php\ext\php_pcov.dll`,
		pcovInIni: true,
	})
	if err != nil {
		t.Fatalf("planCoverage: %v", err)
	}
	if flags := strings.Join(plan.flags, " "); strings.Contains(flags, "extension=php_pcov.dll") {
		t.Errorf("pcov loaded twice: %q", flags)
	}
}

func TestPlanCoverageFallsBackToXdebug(t *testing.T) {
	plan, err := planCoverage(coverageDrivers{
		extDir:    `C:\php\ext`,
		xdebugDLL: `C:\xdebug-8.4.dll`,
	})
	if err != nil {
		t.Fatalf("planCoverage: %v", err)
	}
	if plan.driver != "xdebug" {
		t.Errorf("driver = %q, want xdebug", plan.driver)
	}

	flags := strings.Join(plan.flags, " ")
	if !strings.Contains(flags, "xdebug.mode=coverage") {
		t.Errorf("coverage mode not set: %q", flags)
	}
	if !strings.Contains(flags, `zend_extension=C:\xdebug-8.4.dll`) {
		t.Errorf("xdebug not loaded: %q", flags)
	}
}

func TestPlanCoverageWithoutDriver(t *testing.T) {
	_, err := planCoverage(coverageDrivers{extDir: `C:\php\ext`})
	if err == nil {
		t.Fatal("expected an error when no driver is installed")
	}
	if !strings.Contains(err.Error(), "shp coverage install") {
		t.Errorf("error does not point at a fix: %v", err)
	}
}

func TestDetectCoverageDriversReadsIni(t *testing.T) {
	phpDir := t.TempDir()
	extDir := phpExtDir(phpDir)
	if err := os.MkdirAll(extDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "php_pcov.dll"), []byte("stub"), 0644); err != nil {
		t.Fatal(err)
	}
	ini := "extension=pcov\npcov.enabled=0\n"
	if err := os.WriteFile(phpIniPath(phpDir), []byte(ini), 0644); err != nil {
		t.Fatal(err)
	}

	// A version with no Herd DLL on disk isolates this from the host install.
	d := detectCoverageDrivers(phpDir, "0.0")

	if d.pcovDLL == "" {
		t.Error("pcov DLL not detected")
	}
	if !d.pcovInIni {
		t.Error("extension=pcov not detected")
	}
	if d.pcovEnabledInIni {
		t.Error("pcov.enabled=0 reported as enabled")
	}
	if d.xdebugDLL != "" {
		t.Errorf("unexpected xdebug DLL: %q", d.xdebugDLL)
	}
}

func TestDetectCoverageDriversDefaultsPcovEnabled(t *testing.T) {
	phpDir := t.TempDir()
	if err := os.MkdirAll(phpExtDir(phpDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(phpIniPath(phpDir), []byte("extension=pcov\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// pcov's own default is enabled, so an absent directive means on.
	if d := detectCoverageDrivers(phpDir, "0.0"); !d.pcovEnabledInIni {
		t.Error("absent pcov.enabled should count as enabled")
	}
}

func TestResolvePHPTarget(t *testing.T) {
	dir := t.TempDir()
	phpunit := filepath.Join(dir, "phpunit")
	if err := os.WriteFile(phpunit, []byte("#!/usr/bin/env php"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		cmdArgs    []string
		wantScript string
		wantArgs   []string
		wantOK     bool
	}{
		{
			name:       "php runs the user args directly",
			cmdArgs:    []string{"php", "artisan", "test"},
			wantScript: "",
			wantArgs:   []string{"artisan", "test"},
			wantOK:     true,
		},
		{
			name:       "bat wrapper resolves to its php script",
			cmdArgs:    []string{phpunit + ".bat", "--coverage-text"},
			wantScript: phpunit,
			wantArgs:   []string{"--coverage-text"},
			wantOK:     true,
		},
		{
			name:    "native executable cannot be instrumented",
			cmdArgs: []string{"npm", "test"},
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, args, ok := resolvePHPTarget(tt.cmdArgs)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if script != tt.wantScript {
				t.Errorf("script = %q, want %q", script, tt.wantScript)
			}
			if strings.Join(args, " ") != strings.Join(tt.wantArgs, " ") {
				t.Errorf("args = %v, want %v", args, tt.wantArgs)
			}
		})
	}
}

func TestPcovRegistryEntry(t *testing.T) {
	def, ok := extensionRegistry["pcov"]
	if !ok {
		t.Fatal("pcov is not in the registry")
	}
	if def.directive != "extension" {
		t.Errorf("directive = %q, want extension", def.directive)
	}

	// Installing pcov enabled would instrument every request on the machine.
	var enabled string
	for _, kv := range def.iniDefaults {
		if kv[0] == "pcov.enabled" {
			enabled = kv[1]
		}
	}
	if enabled != "0" {
		t.Errorf("pcov.enabled default = %q, want 0", enabled)
	}
}

// captureStderr collects what fn writes to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()
	_ = w.Close()

	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestWarnPcovShadowsXdebug(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		lines    []string
		wantWarn bool
	}{
		{
			name:     "pcov enabled shadows a coverage mode",
			mode:     "coverage",
			lines:    []string{"extension=pcov", "pcov.enabled=1"},
			wantWarn: true,
		},
		{
			name:     "absent pcov.enabled means pcov's own default, on",
			mode:     "debug,coverage",
			lines:    []string{"extension=pcov"},
			wantWarn: true,
		},
		{
			name:     "the install default leaves xdebug in charge",
			mode:     "coverage",
			lines:    []string{"extension=pcov", "pcov.enabled=0"},
			wantWarn: false,
		},
		{
			name:     "modes other than coverage are unaffected",
			mode:     "debug",
			lines:    []string{"extension=pcov", "pcov.enabled=1"},
			wantWarn: false,
		},
		{
			name:     "no pcov, no conflict",
			mode:     "coverage",
			lines:    []string{"extension=redis"},
			wantWarn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStderr(t, func() { warnPcovShadowsXdebug(tt.mode, tt.lines) })
			if got := strings.Contains(out, "pcov"); got != tt.wantWarn {
				t.Errorf("warned = %v, want %v (output: %q)", got, tt.wantWarn, out)
			}
		})
	}
}

func TestWarnPcovShadowsXdebugSilentInJSON(t *testing.T) {
	orig := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = orig }()

	out := captureStderr(t, func() {
		warnPcovShadowsXdebug("coverage", []string{"extension=pcov", "pcov.enabled=1"})
	})
	if out != "" {
		t.Errorf("JSON output polluted by a human warning: %q", out)
	}
}
