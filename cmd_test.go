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

	_ = os.Remove(bin)
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
	if _, ok := env[userPathFileEnv]; !ok {
		cmd.Env = append(cmd.Env, userPathFileEnv+"="+filepath.Join(t.TempDir(), "user-path.txt"))
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
	if err := os.MkdirAll(herdBin, 0755); err != nil {
		t.Fatal(err)
	}

	// Create fake php.exe for each version
	for _, ver := range versions {
		nodot := strings.ReplaceAll(ver, ".", "")
		phpDir := filepath.Join(herdBin, "php"+nodot)
		if err := os.MkdirAll(phpDir, 0755); err != nil {
			t.Fatal(err)
		}
		// Create a tiny executable that just exits 0.
		// On Windows we need a valid PE, so we copy cmd.exe as a stand-in.
		// This is only needed to pass os.Stat checks — we won't actually run PHP.
		phpExe := filepath.Join(phpDir, "php.exe")
		if err := os.WriteFile(phpExe, []byte("fake"), 0755); err != nil {
			t.Fatal(err)
		}

		// Create a php.ini alongside it
		iniPath := filepath.Join(phpDir, "php.ini")
		if err := os.WriteFile(iniPath, []byte("[PHP]\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Create ext dir
		if err := os.MkdirAll(filepath.Join(phpDir, "ext"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	// Create a fake herd.phar
	if err := os.WriteFile(filepath.Join(herdBin, "herd.phar"), []byte("<?php // fake"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create valet config with empty paths
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	if err := os.MkdirAll(valetDir, 0755); err != nil {
		t.Fatal(err)
	}
	config := map[string]interface{}{"paths": []string{}, "tld": "test"}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(valetDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Create shepherd data dir
	if err := os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755); err != nil {
		t.Fatal(err)
	}

	return root
}

// baseEnv returns the standard env map for tests using a fake herd root.
func baseEnv(userProfile string) map[string]string {
	return map[string]string{
		"USERPROFILE":   userProfile,
		userPathFileEnv: filepath.Join(userProfile, "user-path.txt"),
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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.5\n"), 0644)

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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.5\n"), 0644)

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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		// Write a php.ini with xdebug enabled
		phpDir := filepath.Join(profile, ".config", "herd", "bin", "php84")
		iniContent := "[PHP]\nzend_extension=xdebug.dll\nxdebug.mode=coverage\n"
		_ = os.WriteFile(filepath.Join(phpDir, "php.ini"), []byte(iniContent), 0644)

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
		_ = os.WriteFile(filepath.Join(workDir, ".phpversion"), []byte("8.4\n"), 0644)

		// Ensure php.ini is clean for this test
		phpDir := filepath.Join(profile, ".config", "herd", "bin", "php84")
		_ = os.WriteFile(filepath.Join(phpDir, "php.ini"), []byte("[PHP]\n"), 0644)

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
	_ = os.MkdirAll(shimBin, 0755)
	for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
		_ = os.WriteFile(filepath.Join(shimBin, name), []byte("fake"), 0755)
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

// TestUnknownCommandDoesNotInstall covers the dispatch fall-through: an
// unrecognized command used to reach the first-run install branch, letting its
// flags be parsed as installation flags.
func TestUnknownCommandDoesNotInstall(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)
	shimBin := filepath.Join(profile, ".config", "shepherd", "bin")

	t.Run("unknown command is rejected", func(t *testing.T) {
		res := runShp(t, []string{"bogus-command"}, env)
		if res.ExitCode == 0 {
			t.Fatalf("expected non-zero exit, stdout: %s", res.Stdout)
		}
		if !strings.Contains(res.Stderr, "unknown command") {
			t.Errorf("expected 'unknown command' in stderr, got: %s", res.Stderr)
		}
	})

	t.Run("install flags on an unknown command install nothing", func(t *testing.T) {
		res := runShp(t, []string{"bogus-command", "--force", "--enable-path-guard"}, env)
		if res.ExitCode == 0 {
			t.Fatalf("expected non-zero exit, stdout: %s", res.Stdout)
		}
		for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
			if _, err := os.Stat(filepath.Join(shimBin, name)); err == nil {
				t.Errorf("%s was installed by an unknown command", name)
			}
		}
	})

	t.Run("explicit help works when not installed", func(t *testing.T) {
		res := runShp(t, []string{"--help"}, env)
		if res.ExitCode != 0 {
			t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stdout, "Commands:") {
			t.Errorf("expected help text, got: %s", res.Stdout)
		}
	})
}

// TestExtRemoveRejectsPathTraversal covers a real deletion primitive: extName was
// concatenated into "php_<name>.dll" and joined with the ext directory, so a name
// made of parent segments resolved to an arbitrary .dll outside of it.
func TestExtRemoveRejectsPathTraversal(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)

	victim := filepath.Join(profile, "victim.dll")
	if err := os.WriteFile(victim, []byte("sensitive"), 0644); err != nil {
		t.Fatal(err)
	}

	// ext/ -> php84 -> bin -> herd -> .config -> profile, plus one segment absorbed
	// by the "php_" prefix.
	traversal := filepath.Join("..", "..", "..", "..", "..", "..", "..", "victim")

	res := runShp(t, []string{"ext", "remove", traversal, "--php=8.4"}, env)
	if res.ExitCode == 0 {
		t.Errorf("expected non-zero exit for a traversing extension name, stdout: %s", res.Stdout)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("file outside the ext directory was deleted: %v", err)
	}
}

// TestExtAddRejectsMalformedURLValues keeps --ext-version and --vs from reaching
// the download URL unchecked.
func TestExtAddRejectsMalformedURLValues(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)

	cases := []struct {
		name string
		args []string
	}{
		{"traversing ext-version", []string{"ext", "add", "redis", "--php=8.4", "--ext-version=../../evil"}},
		{"traversing vs", []string{"ext", "add", "redis", "--php=8.4", "--vs=../../evil"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runShp(t, tc.args, env)
			if res.ExitCode == 0 {
				t.Errorf("expected non-zero exit, stdout: %s", res.Stdout)
			}
			if !strings.Contains(res.Stderr, "invalid") {
				t.Errorf("expected an 'invalid' error, got: %s", res.Stderr)
			}
		})
	}
}

// TestExtVersionPatterns pins the accepted shapes, including PECL prereleases.
func TestExtVersionPatterns(t *testing.T) {
	valid := []string{"3.8.1", "1.0.0", "3.1.6RC2", "2.2.0beta1"}
	invalid := []string{"", "3.8", "../../evil", "3.8.1/../x", "3.8.1 ", "%2e%2e"}

	for _, v := range valid {
		if !extVersionRe.MatchString(v) {
			t.Errorf("extVersionRe rejected valid version %q", v)
		}
	}
	for _, v := range invalid {
		if extVersionRe.MatchString(v) {
			t.Errorf("extVersionRe accepted invalid version %q", v)
		}
	}

	for _, v := range []string{"vs17", "vs16"} {
		if !vsVersionRe.MatchString(v) {
			t.Errorf("vsVersionRe rejected valid value %q", v)
		}
	}
	for _, v := range []string{"", "vs", "17", "../vs17", "vs17x"} {
		if vsVersionRe.MatchString(v) {
			t.Errorf("vsVersionRe accepted invalid value %q", v)
		}
	}
}

func TestWhichFallsBackToHerdGlobalWhenHerdIsClosed(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4", "8.5"})
	env := baseEnv(profile)
	writeHerdPhpBat(t, profile, filepath.Join(profile, ".config", "herd", "bin", "php84", "php.exe"))

	res := runShp(t, []string{"--json", "which"}, env)
	if res.ExitCode != 0 {
		t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, res.Stdout)
	}
	if out["version"] != "8.4" {
		t.Errorf("version = %v, want 8.4", out["version"])
	}
}

// TestInstallStaysInSandbox guards against tests rewriting the developer's
// real User PATH or PATH Guard task, which install used to do.
func TestInstallStaysInSandbox(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)
	pathFile := env[userPathFileEnv]
	if err := os.WriteFile(pathFile, []byte(`C:\Existing`), 0600); err != nil {
		t.Fatal(err)
	}
	realBefore, _, err := userPathFromRegistry()
	if err != nil {
		t.Fatal(err)
	}

	res := runShp(t, []string{"install", "--no-interactive", "--enable-path-guard"}, env)
	if res.ExitCode != 0 {
		t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
	}

	data, err := os.ReadFile(pathFile)
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := filepath.Join(profile, ".config", "shepherd", "bin")
	if !strings.HasPrefix(string(data), wantFirst+";") {
		t.Errorf("sandbox PATH = %q, want it to start with %s", data, wantFirst)
	}
	if !strings.Contains(res.Stderr, "test sandbox") {
		t.Errorf("expected the PATH Guard to be refused in the sandbox, stderr: %s", res.Stderr)
	}

	realAfter, _, err := userPathFromRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if realAfter != realBefore {
		t.Error("install modified the real HKCU User PATH")
	}
}

// TestHelpFlagNeverRunsTheCommand covers commands without their own help
// handler: `uninstall --help` used to uninstall and `install --help` to install.
func TestHelpFlagNeverRunsTheCommand(t *testing.T) {
	profile := fakeHerd(t, []string{"8.4"})
	env := baseEnv(profile)
	shimBin := filepath.Join(profile, ".config", "shepherd", "bin")

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"install", "--help"}, "Usage: shp install"},
		{[]string{"install", "--force", "-h"}, "--enable-path-guard"},
		{[]string{"uninstall", "--help"}, "Usage: shp uninstall"},
		{[]string{"self-update", "--help"}, "Usage: shp self-update"},
		{[]string{"use", "--help"}, "latest"},
		{[]string{"doctor", "--help"}, "Usage: shp doctor"},
		{[]string{"status", "--help"}, "Usage: shp status"},
		{[]string{"which", "--help"}, "Usage: shp which"},
		{[]string{"current", "--help"}, "Usage: shp current"},
		{[]string{"ls", "--help"}, "Usage: shp list"},
		{[]string{"ext", "add", "--help"}, "Usage: shp ext"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			res := runShp(t, c.args, env)
			if res.ExitCode != 0 {
				t.Fatalf("exit code %d, stderr: %s", res.ExitCode, res.Stderr)
			}
			if !strings.Contains(res.Stdout, c.want) {
				t.Errorf("expected %q in help, got: %s", c.want, res.Stdout)
			}
			if _, err := os.Stat(shimBin); err == nil {
				entries, _ := os.ReadDir(shimBin)
				if len(entries) > 0 {
					t.Errorf("help request created files in %s", shimBin)
				}
			}
		})
	}

	t.Run("json help", func(t *testing.T) {
		res := runShp(t, []string{"--json", "uninstall", "--help"}, env)
		var out map[string]string
		if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, res.Stdout)
		}
		if out["command"] != "uninstall" {
			t.Errorf("command = %q, want uninstall", out["command"])
		}
	})
}
