package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHerdParkedPaths(t *testing.T) {
	// Set up a fake USERPROFILE with a valet config.json
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	os.MkdirAll(valetDir, 0755)

	t.Run("returns paths from config", func(t *testing.T) {
		config := map[string]interface{}{
			"paths": []string{"C:\\Sites", "D:\\Projects"},
			"tld":   "test",
		}
		data, _ := json.Marshal(config)
		os.WriteFile(filepath.Join(valetDir, "config.json"), data, 0644)

		paths := herdParkedPaths()
		if len(paths) != 2 {
			t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
		}
		if paths[0] != "C:\\Sites" || paths[1] != "D:\\Projects" {
			t.Errorf("unexpected paths: %v", paths)
		}
	})

	t.Run("returns nil on missing config", func(t *testing.T) {
		os.Remove(filepath.Join(valetDir, "config.json"))
		paths := herdParkedPaths()
		if paths != nil {
			t.Errorf("expected nil, got %v", paths)
		}
	})

	t.Run("returns nil on invalid JSON", func(t *testing.T) {
		os.WriteFile(filepath.Join(valetDir, "config.json"), []byte("not json"), 0644)
		paths := herdParkedPaths()
		if paths != nil {
			t.Errorf("expected nil, got %v", paths)
		}
	})

	t.Run("returns nil on empty paths", func(t *testing.T) {
		config := map[string]interface{}{
			"paths": []string{},
		}
		data, _ := json.Marshal(config)
		os.WriteFile(filepath.Join(valetDir, "config.json"), data, 0644)

		paths := herdParkedPaths()
		if len(paths) != 0 {
			t.Errorf("expected empty slice, got %v", paths)
		}
	})
}

func TestHerdTLD(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	os.MkdirAll(valetDir, 0755)

	t.Run("reads TLD from config", func(t *testing.T) {
		config := map[string]interface{}{"tld": "local", "paths": []string{}}
		data, _ := json.Marshal(config)
		os.WriteFile(filepath.Join(valetDir, "config.json"), data, 0644)

		if got := herdTLD(); got != "local" {
			t.Errorf("herdTLD() = %q, want %q", got, "local")
		}
	})

	t.Run("defaults to test on missing config", func(t *testing.T) {
		os.Remove(filepath.Join(valetDir, "config.json"))
		if got := herdTLD(); got != "test" {
			t.Errorf("herdTLD() = %q, want %q", got, "test")
		}
	})

	t.Run("defaults to test on empty TLD", func(t *testing.T) {
		config := map[string]interface{}{"tld": "", "paths": []string{}}
		data, _ := json.Marshal(config)
		os.WriteFile(filepath.Join(valetDir, "config.json"), data, 0644)

		if got := herdTLD(); got != "test" {
			t.Errorf("herdTLD() = %q, want %q", got, "test")
		}
	})
}

func TestHerdBasePort(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	configDir := filepath.Join(root, ".config", "herd", "config")
	os.MkdirAll(configDir, 0755)

	t.Run("reads custom port", func(t *testing.T) {
		config := map[string]interface{}{"basePhpPort": 9100}
		data, _ := json.Marshal(config)
		os.WriteFile(filepath.Join(configDir, "config.json"), data, 0644)

		if got := herdBasePort(); got != 9100 {
			t.Errorf("herdBasePort() = %d, want %d", got, 9100)
		}
	})

	t.Run("defaults to 9000 on missing config", func(t *testing.T) {
		os.Remove(filepath.Join(configDir, "config.json"))
		if got := herdBasePort(); got != 9000 {
			t.Errorf("herdBasePort() = %d, want %d", got, 9000)
		}
	})

	t.Run("defaults to 9000 on zero port", func(t *testing.T) {
		config := map[string]interface{}{"basePhpPort": 0}
		data, _ := json.Marshal(config)
		os.WriteFile(filepath.Join(configDir, "config.json"), data, 0644)

		if got := herdBasePort(); got != 9000 {
			t.Errorf("herdBasePort() = %d, want %d", got, 9000)
		}
	})
}

func TestCheckHerd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	t.Run("false when bin dir missing", func(t *testing.T) {
		if checkHerd() {
			t.Error("expected false when Herd bin dir doesn't exist")
		}
	})

	t.Run("true when bin dir exists", func(t *testing.T) {
		os.MkdirAll(filepath.Join(root, ".config", "herd", "bin"), 0755)
		if !checkHerd() {
			t.Error("expected true when Herd bin dir exists")
		}
	})
}

func TestRequireHerd(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	t.Run("error when not installed", func(t *testing.T) {
		if err := requireHerd(); err == nil {
			t.Error("expected error when Herd is not installed")
		}
	})

	t.Run("nil when installed", func(t *testing.T) {
		os.MkdirAll(filepath.Join(root, ".config", "herd", "bin"), 0755)
		if err := requireHerd(); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
}

func TestFindProjectDomain(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	// Set up valet config with a parked path
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	os.MkdirAll(valetDir, 0755)

	sitesDir := filepath.Join(root, "Sites")
	os.MkdirAll(sitesDir, 0755)

	config := map[string]interface{}{
		"paths": []string{sitesDir},
		"tld":   "test",
	}
	data, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(valetDir, "config.json"), data, 0644)

	t.Run("finds domain for project in parked path", func(t *testing.T) {
		projectDir := filepath.Join(sitesDir, "my-app")
		os.MkdirAll(projectDir, 0755)

		domain := findProjectDomain(projectDir)
		if domain != "my-app" {
			t.Errorf("findProjectDomain() = %q, want %q", domain, "my-app")
		}
	})

	t.Run("returns empty for unknown project", func(t *testing.T) {
		unknownDir := filepath.Join(root, "other", "project")
		os.MkdirAll(unknownDir, 0755)

		domain := findProjectDomain(unknownDir)
		if domain != "" {
			t.Errorf("findProjectDomain() = %q, want empty", domain)
		}
	})
}

func TestMostRecentPHP(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	herdBin := filepath.Join(root, ".config", "herd", "bin")
	os.MkdirAll(herdBin, 0755)

	t.Run("returns highest version", func(t *testing.T) {
		for _, ver := range []string{"php83", "php84", "php85"} {
			dir := filepath.Join(herdBin, ver)
			os.MkdirAll(dir, 0755)
			os.WriteFile(filepath.Join(dir, "php.exe"), []byte("fake"), 0755)
		}

		php, err := mostRecentPHP()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filepath.Base(filepath.Dir(php)) != "php85" {
			t.Errorf("expected php85, got %s", filepath.Dir(php))
		}
	})

	t.Run("error when no PHP installed", func(t *testing.T) {
		emptyRoot := t.TempDir()
		t.Setenv("USERPROFILE", emptyRoot)
		os.MkdirAll(filepath.Join(emptyRoot, ".config", "herd", "bin"), 0755)

		_, err := mostRecentPHP()
		if err == nil {
			t.Error("expected error when no PHP installations found")
		}
	})
}

func TestResolveFromVersion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	herdBin := filepath.Join(root, ".config", "herd", "bin")

	t.Run("resolves valid version", func(t *testing.T) {
		phpDir := filepath.Join(herdBin, "php84")
		os.MkdirAll(phpDir, 0755)
		os.WriteFile(filepath.Join(phpDir, "php.exe"), []byte("fake"), 0755)

		php, err := resolveFromVersion("8.4")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filepath.Base(php) != "php.exe" {
			t.Errorf("expected php.exe, got %s", php)
		}
	})

	t.Run("error for invalid format", func(t *testing.T) {
		_, err := resolveFromVersion("84")
		if err == nil {
			t.Error("expected error for invalid version format")
		}
	})

	t.Run("error for missing version", func(t *testing.T) {
		_, err := resolveFromVersion("7.4")
		if err == nil {
			t.Error("expected error for uninstalled version")
		}
	})
}

func TestInstalledPHPVersions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	herdBin := filepath.Join(root, ".config", "herd", "bin")
	os.MkdirAll(herdBin, 0755)

	// Create fake PHP versions
	for _, ver := range []string{"php83", "php84", "php85"} {
		dir := filepath.Join(herdBin, ver)
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "php.exe"), []byte("fake"), 0755)
	}

	// Create a dir without php.exe (should be skipped)
	os.MkdirAll(filepath.Join(herdBin, "php74"), 0755)

	versions := installedPHPVersions()
	expected := []string{"8.3", "8.4", "8.5"}
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions, got %d: %v", len(versions), versions)
	}
	for i, v := range versions {
		if v != expected[i] {
			t.Errorf("versions[%d] = %q, want %q", i, v, expected[i])
		}
	}
}
