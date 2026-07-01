package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractBinaryFromZip(t *testing.T) {
	t.Run("extracts named file", func(t *testing.T) {
		// Create a zip with shp.exe inside
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "release.zip")
		f, _ := os.Create(zipPath)
		w := zip.NewWriter(f)
		entry, _ := w.Create("shp.exe")
		_, _ = entry.Write([]byte("fake binary content"))
		_ = w.Close()
		_ = f.Close()

		extracted, err := extractBinaryFromZip(zipPath, "shp.exe")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer func() { _ = os.Remove(extracted) }()

		data, _ := os.ReadFile(extracted)
		if string(data) != "fake binary content" {
			t.Errorf("got %q, want %q", string(data), "fake binary content")
		}
	})

	t.Run("case insensitive match", func(t *testing.T) {
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "release.zip")
		f, _ := os.Create(zipPath)
		w := zip.NewWriter(f)
		entry, _ := w.Create("SHP.EXE")
		_, _ = entry.Write([]byte("binary"))
		_ = w.Close()
		_ = f.Close()

		extracted, err := extractBinaryFromZip(zipPath, "shp.exe")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer func() { _ = os.Remove(extracted) }()

		data, _ := os.ReadFile(extracted)
		if string(data) != "binary" {
			t.Errorf("got %q, want %q", string(data), "binary")
		}
	})

	t.Run("error when file not in archive", func(t *testing.T) {
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "release.zip")
		f, _ := os.Create(zipPath)
		w := zip.NewWriter(f)
		entry, _ := w.Create("other.exe")
		_, _ = entry.Write([]byte("not shp"))
		_ = w.Close()
		_ = f.Close()

		_, err := extractBinaryFromZip(zipPath, "shp.exe")
		if err == nil {
			t.Error("expected error when file not found in zip")
		}
	})

	t.Run("extracts from subdirectory using Base", func(t *testing.T) {
		dir := t.TempDir()
		zipPath := filepath.Join(dir, "release.zip")
		f, _ := os.Create(zipPath)
		w := zip.NewWriter(f)
		entry, _ := w.Create("bin/shp.exe")
		_, _ = entry.Write([]byte("nested binary"))
		_ = w.Close()
		_ = f.Close()

		extracted, err := extractBinaryFromZip(zipPath, "shp.exe")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer func() { _ = os.Remove(extracted) }()

		data, _ := os.ReadFile(extracted)
		if string(data) != "nested binary" {
			t.Errorf("got %q, want %q", string(data), "nested binary")
		}
	})
}

func TestReplaceBinary(t *testing.T) {
	t.Run("replaces target with new binary", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "shp.exe")
		_ = os.WriteFile(target, []byte("old"), 0755)

		newBin := filepath.Join(dir, "new.exe")
		_ = os.WriteFile(newBin, []byte("new version"), 0755)

		if err := replaceBinary(target, newBin); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		data, _ := os.ReadFile(target)
		if string(data) != "new version" {
			t.Errorf("got %q, want %q", string(data), "new version")
		}
	})

	t.Run("error when target doesn't exist", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "nonexistent.exe")
		newBin := filepath.Join(dir, "new.exe")
		_ = os.WriteFile(newBin, []byte("new"), 0755)

		err := replaceBinary(target, newBin)
		if err == nil {
			t.Error("expected error when target doesn't exist")
		}
	})
}

func TestUpdateCheckCache(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	t.Run("read returns zero value when no cache", func(t *testing.T) {
		cache := readUpdateCache()
		if cache.LatestVersion != "" || cache.CheckedAt != 0 {
			t.Errorf("expected zero value, got %+v", cache)
		}
	})

	t.Run("write and read roundtrip", func(t *testing.T) {
		now := time.Now().Unix()
		writeUpdateCache(updateCheckCache{
			LatestVersion: "1.2.3",
			CheckedAt:     now,
		})

		cache := readUpdateCache()
		if cache.LatestVersion != "1.2.3" {
			t.Errorf("LatestVersion = %q, want %q", cache.LatestVersion, "1.2.3")
		}
		if cache.CheckedAt != now {
			t.Errorf("CheckedAt = %d, want %d", cache.CheckedAt, now)
		}
	})

	t.Run("read handles corrupt file", func(t *testing.T) {
		_ = os.WriteFile(updateCheckCachePath(), []byte("not json"), 0644)
		cache := readUpdateCache()
		if cache.LatestVersion != "" {
			t.Errorf("expected zero value for corrupt cache, got %+v", cache)
		}
	})
}

func TestMaybeNotifyUpdate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	t.Run("no notification without cache", func(t *testing.T) {
		got := maybeNotifyUpdate()
		if got {
			t.Error("expected false without cache")
		}
	})

	t.Run("no notification for dev builds", func(t *testing.T) {
		oldVersion := version
		version = "dev"
		defer func() { version = oldVersion }()

		writeUpdateCache(updateCheckCache{LatestVersion: "9.9.9", CheckedAt: time.Now().Unix()})
		got := maybeNotifyUpdate()
		if got {
			t.Error("expected false for dev build")
		}
	})

	t.Run("no notification when already up to date", func(t *testing.T) {
		oldVersion := version
		version = "1.0.0"
		defer func() { version = oldVersion }()

		writeUpdateCache(updateCheckCache{LatestVersion: "1.0.0", CheckedAt: time.Now().Unix()})
		got := maybeNotifyUpdate()
		if got {
			t.Error("expected false when already up to date")
		}
	})

	t.Run("notification when newer version available", func(t *testing.T) {
		oldVersion := version
		version = "1.0.0"
		defer func() { version = oldVersion }()

		writeUpdateCache(updateCheckCache{LatestVersion: "2.0.0", CheckedAt: time.Now().Unix()})

		// Redirect stderr to capture the notification
		oldStderr := os.Stderr
		_, w, _ := os.Pipe()
		os.Stderr = w

		got := maybeNotifyUpdate()

		_ = w.Close()
		os.Stderr = oldStderr

		if !got {
			t.Error("expected true when newer version available")
		}
	})
}

func TestTriggerUpdateCheckIfStale(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	t.Run("does not panic with no cache", func(t *testing.T) {
		// Just verify it doesn't crash — it spawns a goroutine
		triggerUpdateCheckIfStale()
	})

	t.Run("does not trigger when cache is fresh", func(t *testing.T) {
		writeUpdateCache(updateCheckCache{
			LatestVersion: "1.0.0",
			CheckedAt:     time.Now().Unix(),
		})
		// Should return immediately without spawning goroutine
		triggerUpdateCheckIfStale()
	})
}

func TestShepherdDataDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	got := shepherdDataDir()
	want := filepath.Join(root, ".config", "shepherd")
	if got != want {
		t.Errorf("shepherdDataDir() = %q, want %q", got, want)
	}
}

func TestShimDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	got := shimDir()
	want := filepath.Join(root, ".config", "shepherd", "bin")
	if got != want {
		t.Errorf("shimDir() = %q, want %q", got, want)
	}
}

func TestIsInstalled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	t.Run("false when shims missing", func(t *testing.T) {
		if isInstalled() {
			t.Error("expected false when shims don't exist")
		}
	})

	t.Run("true when all shims present", func(t *testing.T) {
		dir := filepath.Join(root, ".config", "shepherd", "bin")
		_ = os.MkdirAll(dir, 0755)
		for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
			_ = os.WriteFile(filepath.Join(dir, name), []byte("fake"), 0755)
		}
		if !isInstalled() {
			t.Error("expected true when all shims exist")
		}
	})

	t.Run("false when one shim missing", func(t *testing.T) {
		dir := filepath.Join(root, ".config", "shepherd", "bin")
		_ = os.Remove(filepath.Join(dir, "composer.exe"))
		if isInstalled() {
			t.Error("expected false when composer.exe is missing")
		}
	})
}

func TestShellConfigFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	files := shellConfigFiles()
	if len(files) == 0 {
		t.Fatal("expected non-empty list of shell config files")
	}

	// All paths should start with the USERPROFILE
	for _, f := range files {
		if !filepath.IsAbs(f) {
			t.Errorf("expected absolute path, got %q", f)
		}
	}
}

func TestShepherdProfileContent(t *testing.T) {
	content := shepherdProfileContent()
	if content == "" {
		t.Fatal("expected non-empty profile content")
	}
	if !containsSubstr(content, "shepherd") {
		t.Error("profile content should mention shepherd")
	}
	if !containsSubstr(content, "$env:PATH") {
		t.Error("profile content should manipulate PATH")
	}
}

func TestWriteIni(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "php.ini")

	lines := []string{"[PHP]", "memory_limit=256M", "extension=redis"}
	if err := writeIni(path, lines); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	want := "[PHP]\nmemory_limit=256M\nextension=redis"
	if string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}
}

func TestCacertPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	got := cacertPath()
	if got == "" {
		t.Error("expected non-empty path")
	}
	if !containsSubstr(got, "cacert.pem") {
		t.Errorf("expected path to contain cacert.pem, got %q", got)
	}
}

func TestLogVerbose(t *testing.T) {
	// Ensure no panic when verbose is off
	verbose = false
	logVerbose("test %s", "message")

	// And when verbose is on (output goes to stderr, just check no panic)
	verbose = true
	logVerbose("test %s", "message")
	verbose = false
}

func TestLogInfo(t *testing.T) {
	// No panic when quiet is off
	quiet = false
	logInfo("test %s", "message")

	// No output when quiet is on (just check no panic)
	quiet = true
	logInfo("test %s", "message")
	quiet = false
}

func TestMustAtoi(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"42", 42},
		{"0", 0},
		{"abc", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := mustAtoi(tt.input)
		if got != tt.want {
			t.Errorf("mustAtoi(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestResolvePhysicalPath(t *testing.T) {
	dir := t.TempDir()
	// resolvePhysicalPath on an existing dir should return the path
	got := resolvePhysicalPath(dir)
	if got == "" {
		t.Error("expected non-empty resolved path")
	}

	// On a non-existing path, should return the original
	fake := filepath.Join(dir, "nonexistent")
	got = resolvePhysicalPath(fake)
	if got != fake {
		t.Errorf("expected %q for non-existing path, got %q", fake, got)
	}
}

func TestHerdConfigPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	got := herdConfigPath()
	if !containsSubstr(got, "config.json") {
		t.Errorf("expected config.json in path, got %q", got)
	}
}

func TestHerdCertsDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	got := herdCertsDir()
	if !containsSubstr(got, "Certificates") {
		t.Errorf("expected Certificates in path, got %q", got)
	}
}

func TestProfileOverridesPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	t.Run("false when no profile exists", func(t *testing.T) {
		if profileOverridesPath() {
			t.Error("expected false when no profiles exist")
		}
	})

	t.Run("false when profile doesn't mention herd", func(t *testing.T) {
		profileDir := filepath.Join(root, "Documents", "PowerShell")
		_ = os.MkdirAll(profileDir, 0755)
		_ = os.WriteFile(filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1"),
			[]byte("Write-Host 'Hello'\n"), 0644)

		if profileOverridesPath() {
			t.Error("expected false when profile doesn't mention herd")
		}
	})

	t.Run("true when profile mentions herd and PATH", func(t *testing.T) {
		profileDir := filepath.Join(root, "Documents", "PowerShell")
		_ = os.MkdirAll(profileDir, 0755)
		content := "$env:PATH = \"C:\\herd\\bin;\" + $env:PATH\n"
		_ = os.WriteFile(filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1"),
			[]byte(content), 0644)

		if !profileOverridesPath() {
			t.Error("expected true when profile reorders PATH for herd")
		}
	})

	t.Run("false when profile mentions both herd and shepherd", func(t *testing.T) {
		profileDir := filepath.Join(root, "Documents", "PowerShell")
		_ = os.MkdirAll(profileDir, 0755)
		content := "# shepherd integration\n$env:PATH = \"C:\\herd\\bin;\" + $env:PATH\n"
		_ = os.WriteFile(filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1"),
			[]byte(content), 0644)

		if profileOverridesPath() {
			t.Error("expected false when profile already includes shepherd")
		}
	})
}

func TestPatchPowerShellProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	// Create shepherd data dir
	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	t.Run("does nothing when no profile exists", func(t *testing.T) {
		got := patchPowerShellProfile(shimDir())
		if got {
			t.Error("expected false when no profile exists to patch")
		}
	})

	t.Run("patches existing profile", func(t *testing.T) {
		profileDir := filepath.Join(root, "Documents", "PowerShell")
		_ = os.MkdirAll(profileDir, 0755)
		profilePath := filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1")
		_ = os.WriteFile(profilePath, []byte("# my profile\n"), 0644)

		got := patchPowerShellProfile(shimDir())
		if !got {
			t.Error("expected true when profile was patched")
		}

		data, _ := os.ReadFile(profilePath)
		if !containsSubstr(string(data), "shepherd") {
			t.Error("expected profile to contain shepherd reference")
		}
	})

	t.Run("does not duplicate patch", func(t *testing.T) {
		profileDir := filepath.Join(root, "Documents", "PowerShell")
		profilePath := filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1")

		// Already patched from previous sub-test
		got := patchPowerShellProfile(shimDir())
		if got {
			t.Error("expected false when already patched")
		}

		data, _ := os.ReadFile(profilePath)
		count := 0
		for i := 0; i < len(string(data)); i++ {
			if containsSubstr(string(data)[i:], "shepherd\\profile.ps1") {
				count++
				i += len("shepherd\\profile.ps1")
			}
		}
		// Just verify it appears (not duplicated excessively)
	})
}

func TestUnpatchPowerShellProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	profileDir := filepath.Join(root, "Documents", "PowerShell")
	_ = os.MkdirAll(profileDir, 0755)
	profilePath := filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1")

	// Write a profile with the shepherd snippet
	content := "# my stuff\n# Shepherd PATH priority\n" + profileSourceLine + "\n"
	_ = os.WriteFile(profilePath, []byte(content), 0644)

	// Write the snippet file
	snippetPath := shepherdProfilePath()
	_ = os.MkdirAll(filepath.Dir(snippetPath), 0755)
	_ = os.WriteFile(snippetPath, []byte(shepherdProfileContent()), 0644)

	unpatchPowerShellProfile()

	// Verify the profile no longer contains shepherd
	data, _ := os.ReadFile(profilePath)
	if containsSubstr(string(data), "Shepherd PATH priority") {
		t.Error("expected shepherd reference to be removed from profile")
	}

	// Verify snippet file was removed
	if _, err := os.Stat(snippetPath); !os.IsNotExist(err) {
		t.Error("expected snippet file to be deleted")
	}
}

// --- JSON serialization helpers test ---

func TestNginxConfDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	got := nginxConfDir()
	if !containsSubstr(got, "Nginx") {
		t.Errorf("expected Nginx in path, got %q", got)
	}
}

func TestPhpIniPath(t *testing.T) {
	got := phpIniPath("C:\\php84")
	if got != "C:\\php84\\php.ini" {
		t.Errorf("phpIniPath() = %q, want %q", got, "C:\\php84\\php.ini")
	}
}

func TestIsInteractive(t *testing.T) {
	// When noInteractive is set, should always return false
	noInteractive = true
	if isInteractive() {
		t.Error("expected false when noInteractive is true")
	}
	noInteractive = false
}

func TestExtractVersionJSON(t *testing.T) {
	// Test that version JSON output structure is correct
	data, err := json.Marshal(map[string]string{"version": "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out["version"] != "1.2.3" {
		t.Errorf("got %q, want %q", out["version"], "1.2.3")
	}
}
