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
