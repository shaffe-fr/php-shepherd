package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXdebugDLLPath(t *testing.T) {
	// Just verify it returns a non-empty path with the version in it
	got := xdebugDLLPath("8.4")
	if got == "" {
		t.Error("expected non-empty path")
	}
	if !strings.Contains(got, "xdebug-8.4.dll") {
		t.Errorf("expected path to contain 'xdebug-8.4.dll', got %q", got)
	}
}

func TestXdebugStatus_Enabled(t *testing.T) {
	lines := []string{
		"[PHP]",
		"zend_extension=C:\\xdebug\\xdebug-8.4.dll",
		"xdebug.mode=coverage",
		"xdebug.start_with_request=yes",
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	oldJsonOutput := jsonOutput
	jsonOutput = true
	xdebugStatus(lines, "8.4")
	jsonOutput = oldJsonOutput

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\noutput: %s", err, buf.String())
	}
	if result["enabled"] != true {
		t.Error("expected enabled=true")
	}
	if result["mode"] != "coverage" {
		t.Errorf("expected mode=coverage, got %v", result["mode"])
	}
	if result["phpVersion"] != "8.4" {
		t.Errorf("expected phpVersion=8.4, got %v", result["phpVersion"])
	}
}

func TestXdebugStatus_Disabled(t *testing.T) {
	lines := []string{
		"[PHP]",
		";zend_extension=C:\\xdebug\\xdebug-8.4.dll",
		"xdebug.mode=debug",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	oldJsonOutput := jsonOutput
	jsonOutput = true
	xdebugStatus(lines, "8.4")
	jsonOutput = oldJsonOutput

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\noutput: %s", err, buf.String())
	}
	if result["enabled"] != false {
		t.Error("expected enabled=false when zend_extension is commented")
	}
}

func TestXdebugStatus_NoXdebugLines(t *testing.T) {
	lines := []string{
		"[PHP]",
		"memory_limit=256M",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	oldJsonOutput := jsonOutput
	jsonOutput = true
	xdebugStatus(lines, "8.4")
	jsonOutput = oldJsonOutput

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	var result map[string]interface{}
	_ = json.Unmarshal(buf.Bytes(), &result)
	if result["enabled"] != false {
		t.Error("expected enabled=false when no xdebug lines")
	}
	if result["mode"] != "" {
		t.Errorf("expected empty mode, got %v", result["mode"])
	}
}

func TestXdebugNeedsOutputDir(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{"trace", true},
		{"profile", true},
		{"debug", false},
		{"coverage", false},
		{"off", false},
		{"debug,coverage", false},
	}
	for _, tt := range tests {
		if got := xdebugNeedsOutputDir(tt.mode); got != tt.want {
			t.Errorf("xdebugNeedsOutputDir(%q) = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestXdebugRunHelp(t *testing.T) {
	result := runShp(t, []string{"xdebug", "run", "--help"}, nil)
	if result.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "shp xdebug run <mode> -- <command...>") {
		t.Errorf("expected usage line in help output, got: %s", result.Stdout)
	}
}

func TestXdebugRunHelp_JSON(t *testing.T) {
	result := runShp(t, []string{"--json", "xdebug", "run", "--help"}, nil)
	if result.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", result.ExitCode, result.Stderr)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(result.Stdout), &obj); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, result.Stdout)
	}
	if obj["command"] != "xdebug run" {
		t.Errorf("expected command='xdebug run', got %v", obj["command"])
	}
}

func TestXdebugRunInvalidMode(t *testing.T) {
	result := runShp(t, []string{"xdebug", "run", "invalid", "--", "php", "-v"}, nil)
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit for invalid mode")
	}
	if !strings.Contains(result.Stderr, "invalid mode") {
		t.Errorf("expected 'invalid mode' in stderr, got: %s", result.Stderr)
	}
}

func TestXdebugRunMissingSeparator(t *testing.T) {
	result := runShp(t, []string{"xdebug", "run", "trace", "php", "-v"}, nil)
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit when -- is missing")
	}
	if !strings.Contains(result.Stderr, "missing command after '--'") {
		t.Errorf("expected separator error in stderr, got: %s", result.Stderr)
	}
}

func TestXdebugRunMissingCommand(t *testing.T) {
	result := runShp(t, []string{"xdebug", "run", "trace", "--"}, nil)
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit when command is missing after --")
	}
	if !strings.Contains(result.Stderr, "missing command after '--'") {
		t.Errorf("expected missing command error in stderr, got: %s", result.Stderr)
	}
}

func TestXdebugRunOffModeRejected(t *testing.T) {
	result := runShp(t, []string{"xdebug", "run", "off", "--", "php", "-v"}, nil)
	if result.ExitCode == 0 {
		t.Error("expected non-zero exit for 'off' mode in run")
	}
	if !strings.Contains(result.Stderr, "invalid mode") {
		t.Errorf("expected invalid mode error for 'off', got: %s", result.Stderr)
	}
}

func TestXdebugActionResult_JSONIncludesOutputDir(t *testing.T) {
	tests := []struct {
		mode          string
		expectKey     bool
		expectedValue string
	}{
		{"trace", true, "."},
		{"profile", true, "."},
		{"debug", false, ""},
		{"coverage", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			oldJsonOutput := jsonOutput
			jsonOutput = true
			xdebugActionResult("8.4", true, tt.mode)
			jsonOutput = oldJsonOutput

			_ = w.Close()
			os.Stdout = oldStdout

			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r)

			var result map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
				t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
			}

			val, exists := result["outputDir"]
			if tt.expectKey && !exists {
				t.Errorf("expected outputDir key in JSON for mode %q", tt.mode)
			}
			if !tt.expectKey && exists {
				t.Errorf("unexpected outputDir key in JSON for mode %q", tt.mode)
			}
			if tt.expectKey && val != tt.expectedValue {
				t.Errorf("outputDir = %v, want %q", val, tt.expectedValue)
			}
		})
	}
}

func TestXdebugActionResult_HumanOutputDir(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	oldJsonOutput := jsonOutput
	jsonOutput = false
	xdebugActionResult("8.4", true, "trace")
	jsonOutput = oldJsonOutput

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "output_dir") {
		t.Error("expected human output to mention output_dir for trace mode")
	}
}

func TestDoctorCheckAliases(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	t.Run("returns 0 when no config files exist", func(t *testing.T) {
		issues := doctorCheckAliases()
		if issues != 0 {
			t.Errorf("expected 0 issues, got %d", issues)
		}
	})

	t.Run("detects alias in bashrc", func(t *testing.T) {
		bashrc := filepath.Join(root, ".bashrc")
		_ = os.WriteFile(bashrc, []byte("alias php='/usr/bin/php'\n"), 0644)

		issues := doctorCheckAliases()
		if issues != 1 {
			t.Errorf("expected 1 issue, got %d", issues)
		}

		_ = os.Remove(bashrc)
	})

	t.Run("detects alias in PowerShell profile", func(t *testing.T) {
		profileDir := filepath.Join(root, "Documents", "PowerShell")
		_ = os.MkdirAll(profileDir, 0755)
		profile := filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1")
		_ = os.WriteFile(profile, []byte("Set-Alias composer C:\\composer.bat\n"), 0644)

		issues := doctorCheckAliases()
		if issues < 1 {
			t.Errorf("expected at least 1 issue, got %d", issues)
		}
	})
}
