package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testBinary holds the path to the compiled shp binary used by all cmd tests.
var testBinary string

// TestMain compiles the binary once and runs all tests.
func TestMain(m *testing.M) {
	// Build the test binary
	bin := filepath.Join(os.TempDir(), "shp-test.exe")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build test binary: %v\n", err)
		os.Exit(1)
	}
	testBinary = bin

	code := m.Run()

	os.Remove(bin)
	os.Exit(code)
}

// cmdResult holds the output from a subprocess invocation.
type cmdResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runShp executes the test binary with the given args and environment.
// The binary is always invoked as "shp" (Shepherd mode).
func runShp(t *testing.T, args []string, env map[string]string) cmdResult {
	t.Helper()
	return runBinary(t, testBinary, args, env)
}

// runBinary executes the test binary (possibly renamed) with custom env.
func runBinary(t *testing.T, binPath string, args []string, env map[string]string) cmdResult {
	t.Helper()

	cmd := exec.Command(binPath, args...)

	// Start with a minimal environment to isolate from host.
	// Keep SystemRoot and PATH so exec.Command and Windows APIs work.
	cmd.Env = []string{
		"SystemRoot=" + os.Getenv("SystemRoot"),
		"PATH=" + os.Getenv("PATH"),
		"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run binary: %v", err)
		}
	}

	return cmdResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}

// fakeHerd creates a minimal Herd-like directory structure in a temp dir.
// It returns the USERPROFILE path that should be injected via env.
// The structure mirrors what herdHome() and related functions expect:
//
//	<userprofile>/.config/herd/bin/php84/php.exe
//	<userprofile>/.config/herd/bin/php85/php.exe
//	<userprofile>/.config/herd/bin/herd.phar
//	<userprofile>/.config/herd/config/valet/config.json
//	<userprofile>/.config/shepherd/
func fakeHerd(t *testing.T, versions []string) string {
	t.Helper()

	root := t.TempDir()
	herdBin := filepath.Join(root, ".config", "herd", "bin")
	os.MkdirAll(herdBin, 0755)

	// Create fake php.exe for each version
	for _, ver := range versions {
		nodot := strings.ReplaceAll(ver, ".", "")
		phpDir := filepath.Join(herdBin, "php"+nodot)
		os.MkdirAll(phpDir, 0755)
		// Create a tiny executable that just exits 0.
		// On Windows we need a valid PE, so we copy cmd.exe as a stand-in.
		// This is only needed to pass os.Stat checks — we won't actually run PHP.
		phpExe := filepath.Join(phpDir, "php.exe")
		os.WriteFile(phpExe, []byte("fake"), 0755)

		// Create a php.ini alongside it
		iniPath := filepath.Join(phpDir, "php.ini")
		os.WriteFile(iniPath, []byte("[PHP]\n"), 0644)

		// Create ext dir
		os.MkdirAll(filepath.Join(phpDir, "ext"), 0755)
	}

	// Create a fake herd.phar
	os.WriteFile(filepath.Join(herdBin, "herd.phar"), []byte("<?php // fake"), 0644)

	// Create valet config with empty paths
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	os.MkdirAll(valetDir, 0755)
	config := map[string]interface{}{"paths": []string{}, "tld": "test"}
	data, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(valetDir, "config.json"), data, 0644)

	// Create shepherd data dir
	os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	return root
}

// baseEnv returns the standard env map for tests using a fake herd root.
func baseEnv(userProfile string) map[string]string {
	return map[string]string{
		"USERPROFILE": userProfile,
	}
}

// --- Tests ---

func TestCmdVersion(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)

	t.Run("text output", func(t *testing.T) {
		res := runShp(t, []string{"version"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		if !strings.HasPrefix(res.Stdout, "shp ") {
			t.Errorf("expected stdout to start with 'shp ', got: %q", res.Stdout)
		}
	})

	t.Run("json output", func(t *testing.T) {
		res := runShp(t, []string{"--json", "version"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		var out map[string]string
		if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, res.Stdout)
		}
		if _, ok := out["version"]; !ok {
			t.Error("JSON output missing 'version' key")
		}
	})
}

func TestCmdList(t *testing.T) {
	profile := fakeHerd(t, []string{"8.3", "8.4", "8.5"})
	env := baseEnv(profile)

	t.Run("text output lists versions", func(t *testing.T) {
		res := runShp(t, []string{"list"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		for _, ver := range []string{"8.3", "8.4", "8.5"} {
			if !strings.Contains(res.Stdout, ver) {
				t.Errorf("expected %q in output, got: %s", ver, res.Stdout)
			}
		}
	})

	t.Run("ls alias works", func(t *testing.T) {
		res := runShp(t, []string{"ls"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "8.4") {
			t.Errorf("expected '8.4' in output, got: %s", res.Stdout)
		}
	})

	t.Run("json output", func(t *testing.T) {
		res := runShp(t, []string{"--json", "list"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		var versions []map[string]interface{}
		if err := json.Unmarshal([]byte(res.Stdout), &versions); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, res.Stdout)
		}
		if len(versions) != 3 {
			t.Errorf("expected 3 versions, got %d", len(versions))
		}
	})

	t.Run("marks active version", func(t *testing.T) {
		// Create .phpversion in a temp working dir
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		cmd := exec.Command(testBinary, "list")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit code non-zero: %v", err)
		}
		if !strings.Contains(stdout.String(), "→ 8.4") {
			t.Errorf("expected active marker for 8.4, got: %s", stdout.String())
		}
	})
}

func TestCmdUse(t *testing.T) {
	profile := fakeHerd(t, []string{"8.3", "8.4", "8.5"})
	env := baseEnv(profile)

	t.Run("sets phpversion file", func(t *testing.T) {
		workDir := t.TempDir()

		cmd := exec.Command(testBinary, "use", "8.4")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v, stdout: %s", err, stdout.String())
		}

		data, err := os.ReadFile(filepath.Join(workDir, ".phpversion"))
		if err != nil {
			t.Fatal("expected .phpversion to be created")
		}
		if strings.TrimSpace(string(data)) != "8.4" {
			t.Errorf("expected '8.4', got %q", string(data))
		}
	})

	t.Run("accepts shorthand without dot", func(t *testing.T) {
		workDir := t.TempDir()

		cmd := exec.Command(testBinary, "use", "85")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v, stdout: %s", err, stdout.String())
		}

		data, _ := os.ReadFile(filepath.Join(workDir, ".phpversion"))
		if strings.TrimSpace(string(data)) != "8.5" {
			t.Errorf("expected '8.5', got %q", string(data))
		}
	})

	t.Run("rejects invalid version", func(t *testing.T) {
		workDir := t.TempDir()

		cmd := exec.Command(testBinary, "use", "abc")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			t.Fatal("expected non-zero exit code for invalid version")
		}
		if !strings.Contains(stderr.String(), "invalid") {
			t.Errorf("expected 'invalid' in stderr, got: %s", stderr.String())
		}
	})

	t.Run("rejects uninstalled version", func(t *testing.T) {
		workDir := t.TempDir()

		cmd := exec.Command(testBinary, "use", "7.4")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			t.Fatal("expected non-zero exit code for uninstalled version")
		}
		if !strings.Contains(stderr.String(), "not found") {
			t.Errorf("expected 'not found' in stderr, got: %s", stderr.String())
		}
	})

	t.Run("use latest picks highest version", func(t *testing.T) {
		workDir := t.TempDir()

		cmd := exec.Command(testBinary, "use", "latest")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v, stdout: %s", err, stdout.String())
		}

		data, _ := os.ReadFile(filepath.Join(workDir, ".phpversion"))
		if strings.TrimSpace(string(data)) != "8.5" {
			t.Errorf("expected '8.5' (latest), got %q", string(data))
		}
	})

	t.Run("without args lists versions", func(t *testing.T) {
		res := runShp(t, []string{"use"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "8.4") {
			t.Errorf("expected version listing, got: %s", res.Stdout)
		}
	})
}

func TestCmdCurrent(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4", "8.5"})

	t.Run("prints version from phpversion file", func(t *testing.T) {
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		cmd := exec.Command(testBinary, "current")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v", err)
		}
		got := strings.TrimSpace(stdout.String())
		if got != "8.4" {
			t.Errorf("expected '8.4', got %q", got)
		}
	})

	t.Run("json output", func(t *testing.T) {
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.5\n"), 0644)

		cmd := exec.Command(testBinary, "--json", "current")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v", err)
		}
		var out map[string]string
		if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout.String())
		}
		if out["version"] != "8.5" {
			t.Errorf("expected version '8.5', got %q", out["version"])
		}
	})
}

func TestCmdWhich(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4", "8.5"})

	t.Run("shows phpversion source", func(t *testing.T) {
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		cmd := exec.Command(testBinary, "which")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, "8.4") {
			t.Errorf("expected '8.4' in output, got: %s", out)
		}
		if !strings.Contains(out, ".phpversion") {
			t.Errorf("expected '.phpversion' source mentioned, got: %s", out)
		}
	})

	t.Run("json output", func(t *testing.T) {
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.5\n"), 0644)

		cmd := exec.Command(testBinary, "--json", "which")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v", err)
		}
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout.String())
		}
		if out["version"] != "8.5" {
			t.Errorf("expected version '8.5', got %v", out["version"])
		}
		if out["source"] != ".phpversion" {
			t.Errorf("expected source '.phpversion', got %v", out["source"])
		}
		if out["executable"] == nil || out["executable"] == "" {
			t.Error("expected non-empty executable path")
		}
	})
}

func TestCmdXdebugStatus(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})

	t.Run("shows disabled when not configured", func(t *testing.T) {
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		cmd := exec.Command(testBinary, "xdebug", "status")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v", err)
		}
		if !strings.Contains(stdout.String(), "disabled") {
			t.Errorf("expected 'disabled' in output, got: %s", stdout.String())
		}
	})

	t.Run("shows enabled with mode", func(t *testing.T) {
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		// Write a php.ini with xdebug enabled
		phpDir := filepath.Join(profile, ".config", "herd", "bin", "php84")
		iniContent := "[PHP]\nzend_extension=xdebug.dll\nxdebug.mode=coverage\n"
		os.WriteFile(filepath.Join(phpDir, "php.ini"), []byte(iniContent), 0644)

		cmd := exec.Command(testBinary, "xdebug", "status")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, "enabled") {
			t.Errorf("expected 'enabled' in output, got: %s", out)
		}
		if !strings.Contains(out, "coverage") {
			t.Errorf("expected 'coverage' in output, got: %s", out)
		}
	})

	t.Run("json output", func(t *testing.T) {
		workDir := t.TempDir()
		os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		// Ensure php.ini is clean for this test
		phpDir := filepath.Join(profile, ".config", "herd", "bin", "php84")
		os.WriteFile(filepath.Join(phpDir, "php.ini"), []byte("[PHP]\n"), 0644)

		cmd := exec.Command(testBinary, "--json", "xdebug", "status")
		cmd.Dir = workDir
		cmd.Env = []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"PATH=" + os.Getenv("PATH"),
			"PROGRAMFILES=" + os.Getenv("PROGRAMFILES"),
			"USERPROFILE=" + profile,
		}
		var stdout strings.Builder
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("exit %v", err)
		}
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout.String())
		}
		if out["enabled"] != false {
			t.Errorf("expected enabled=false, got %v", out["enabled"])
		}
		if out["phpVersion"] != "8.4" {
			t.Errorf("expected phpVersion '8.4', got %v", out["phpVersion"])
		}
	})
}

func TestCmdHelp(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)

	// Ensure shims exist so help is shown (not install prompt)
	shimBin := filepath.Join(profile, ".config", "shepherd", "bin")
	os.MkdirAll(shimBin, 0755)
	for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
		os.WriteFile(filepath.Join(shimBin, name), []byte("fake"), 0755)
	}

	t.Run("no args shows help text", func(t *testing.T) {
		res := runShp(t, nil, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "Commands:") {
			t.Errorf("expected help text with 'Commands:', got: %s", res.Stdout)
		}
	})

	t.Run("json help", func(t *testing.T) {
		res := runShp(t, []string{"--json"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, res.Stdout)
		}
		if out["version"] == nil {
			t.Error("JSON help missing 'version' key")
		}
		if out["commands"] == nil {
			t.Error("JSON help missing 'commands' key")
		}
	})
}

func TestCmdNoHerd(t *testing.T) {
	// Use a profile with NO herd directory at all
	root := t.TempDir()
	env := baseEnv(root)

	t.Run("list fails without herd", func(t *testing.T) {
		res := runShp(t, []string{"list"}, env)
		if res.ExitCode == 0 {
			t.Error("expected non-zero exit when herd is missing")
		}
		if !strings.Contains(res.Stderr, "Herd") {
			t.Errorf("expected 'Herd' mention in error, got: %s", res.Stderr)
		}
	})

	t.Run("current fails without herd", func(t *testing.T) {
		res := runShp(t, []string{"current"}, env)
		if res.ExitCode == 0 {
			t.Error("expected non-zero exit when herd is missing")
		}
	})

	t.Run("which fails without herd", func(t *testing.T) {
		res := runShp(t, []string{"which"}, env)
		if res.ExitCode == 0 {
			t.Error("expected non-zero exit when herd is missing")
		}
	})
}

func TestGlobalFlags(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)

	t.Run("quiet suppresses output on list", func(t *testing.T) {
		// quiet doesn't suppress list output (it uses fmt.Println directly),
		// but it should suppress logInfo. We test that --quiet doesn't crash.
		res := runShp(t, []string{"--quiet", "version"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d", res.ExitCode)
		}
	})

	t.Run("verbose doesn't crash", func(t *testing.T) {
		res := runShp(t, []string{"--verbose", "version"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d", res.ExitCode)
		}
	})

	t.Run("no-interactive doesn't crash", func(t *testing.T) {
		res := runShp(t, []string{"--no-interactive", "version"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d", res.ExitCode)
		}
	})
}
