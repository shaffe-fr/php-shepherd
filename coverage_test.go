package main

import (
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
