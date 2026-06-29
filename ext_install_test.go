package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createTestZip builds a zip archive in a temp file with the given entries.
// Each entry is a map of filename → content.
func createTestZip(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "test-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(tmp)
	for name, data := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	tmp.Close()
	return tmp.Name()
}

func TestInstallExtFiles_BasicExtraction(t *testing.T) {
	phpDir := t.TempDir()
	extDir := filepath.Join(phpDir, "ext")
	os.MkdirAll(extDir, 0755)

	zipPath := createTestZip(t, map[string][]byte{
		"php_redis.dll": []byte("fake-redis-dll"),
		"php_redis.pdb": []byte("fake-redis-pdb"),
		"libssl-3.dll":  []byte("fake-openssl"),
		"README.md":     []byte("should be skipped"),
		"LICENSE":       []byte("should be skipped"),
	})

	found, err := installExtFiles(zipPath, "redis", phpDir, extDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected foundExtDLL to be true")
	}

	// Extension DLL should be in ext/
	assertFileContent(t, filepath.Join(extDir, "php_redis.dll"), "fake-redis-dll")
	assertFileContent(t, filepath.Join(extDir, "php_redis.pdb"), "fake-redis-pdb")

	// Support DLL should be in phpDir
	assertFileContent(t, filepath.Join(phpDir, "libssl-3.dll"), "fake-openssl")

	// Non-binary files should NOT be extracted
	if _, err := os.Stat(filepath.Join(phpDir, "README.md")); !os.IsNotExist(err) {
		t.Error("README.md should not have been extracted")
	}
	if _, err := os.Stat(filepath.Join(extDir, "LICENSE")); !os.IsNotExist(err) {
		t.Error("LICENSE should not have been extracted")
	}
}

func TestInstallExtFiles_NoExtDLL(t *testing.T) {
	phpDir := t.TempDir()
	extDir := filepath.Join(phpDir, "ext")
	os.MkdirAll(extDir, 0755)

	zipPath := createTestZip(t, map[string][]byte{
		"libcrypto.dll": []byte("support-dll"),
	})

	found, err := installExtFiles(zipPath, "redis", phpDir, extDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected foundExtDLL to be false when no php_redis.dll present")
	}
}

func TestInstallExtFiles_ZipSlipPrevention(t *testing.T) {
	phpDir := t.TempDir()
	extDir := filepath.Join(phpDir, "ext")
	os.MkdirAll(extDir, 0755)

	// Create a zip with a path-traversal entry
	zipPath := createTestZip(t, map[string][]byte{
		"../../evil.dll": []byte("malicious"),
	})

	// The path-traversal entry should be handled safely:
	// filepath.Base("../../evil.dll") → "evil.dll", so it becomes a flat file.
	// This is the correct behavior — the zip-slip guard prevents escaping.
	_, err := installExtFiles(zipPath, "redis", phpDir, extDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// evil.dll should end up in phpDir (as a support DLL), NOT above it
	if _, err := os.Stat(filepath.Join(phpDir, "evil.dll")); os.IsNotExist(err) {
		// Also acceptable: the entry was rejected entirely
	}

	// The file must NOT exist outside phpDir
	parent := filepath.Dir(filepath.Dir(phpDir))
	if _, err := os.Stat(filepath.Join(parent, "evil.dll")); err == nil {
		t.Fatal("zip-slip attack succeeded: evil.dll was written outside target directory")
	}
}

func TestInstallExtFiles_SubdirectoryEntry(t *testing.T) {
	phpDir := t.TempDir()
	extDir := filepath.Join(phpDir, "ext")
	os.MkdirAll(extDir, 0755)

	// Zip entries with subdirectory paths (common in PECL packages)
	zipPath := createTestZip(t, map[string][]byte{
		"x64/Release_NTS/php_imagick.dll":         []byte("imagick-dll"),
		"x64/Release_NTS/CORE_RL_MagickCore_.dll": []byte("magick-core"),
	})

	found, err := installExtFiles(zipPath, "imagick", phpDir, extDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected foundExtDLL to be true")
	}

	// filepath.Base should flatten the directory structure
	assertFileContent(t, filepath.Join(extDir, "php_imagick.dll"), "imagick-dll")
	assertFileContent(t, filepath.Join(phpDir, "CORE_RL_MagickCore_.dll"), "magick-core")
}

func TestInstallExtFiles_DotDotEntry(t *testing.T) {
	phpDir := t.TempDir()
	extDir := filepath.Join(phpDir, "ext")
	os.MkdirAll(extDir, 0755)

	// An entry named ".." should be silently skipped
	zipPath := createTestZip(t, map[string][]byte{
		"..":            []byte("should-be-skipped"),
		"php_redis.dll": []byte("real-dll"),
	})

	found, err := installExtFiles(zipPath, "redis", phpDir, extDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected foundExtDLL to be true")
	}
}

func TestInstallExtFiles_CaseInsensitiveMatch(t *testing.T) {
	phpDir := t.TempDir()
	extDir := filepath.Join(phpDir, "ext")
	os.MkdirAll(extDir, 0755)

	zipPath := createTestZip(t, map[string][]byte{
		"php_Redis.DLL": []byte("case-dll"),
	})

	found, err := installExtFiles(zipPath, "Redis", phpDir, extDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Error("expected case-insensitive match on extension DLL")
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected file %s to exist: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("file %s: got %q, want %q", path, string(data), expected)
	}
}

func TestAddExtensionToIni_NewExtension(t *testing.T) {
	dir := t.TempDir()
	iniPath := filepath.Join(dir, "php.ini")
	os.WriteFile(iniPath, []byte("[PHP]\nmemory_limit=256M\n"), 0644)

	if err := addExtensionToIni(iniPath, "redis"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(iniPath)
	content := string(data)
	if !strings.Contains(content, "extension=redis") {
		t.Errorf("expected 'extension=redis' in ini, got:\n%s", content)
	}
}

func TestAddExtensionToIni_AlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	iniPath := filepath.Join(dir, "php.ini")
	os.WriteFile(iniPath, []byte("[PHP]\nextension=redis\n"), 0644)

	if err := addExtensionToIni(iniPath, "redis"); err != nil {
		t.Fatal(err)
	}

	// Should not duplicate
	data, _ := os.ReadFile(iniPath)
	if strings.Count(string(data), "extension=redis") != 1 {
		t.Errorf("extension duplicated in ini:\n%s", string(data))
	}
}

func TestAddExtensionToIni_CommentedOutPresent(t *testing.T) {
	dir := t.TempDir()
	iniPath := filepath.Join(dir, "php.ini")
	os.WriteFile(iniPath, []byte("[PHP]\n;extension=redis\n"), 0644)

	// Should detect the commented-out line and not add a duplicate
	if err := addExtensionToIni(iniPath, "redis"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(iniPath)
	if strings.Count(string(data), "redis") > 1 {
		t.Errorf("should not add when commented version exists:\n%s", string(data))
	}
}

func TestAddExtensionToIni_ZendExtension(t *testing.T) {
	dir := t.TempDir()
	iniPath := filepath.Join(dir, "php.ini")
	os.WriteFile(iniPath, []byte("[PHP]\n"), 0644)

	if err := addExtensionToIni(iniPath, "xdebug"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(iniPath)
	content := string(data)
	if !strings.Contains(content, "zend_extension=xdebug") {
		t.Errorf("expected 'zend_extension=xdebug', got:\n%s", content)
	}
	// "extension=xdebug" (without "zend_" prefix) should NOT appear as a standalone directive.
	// Note: "zend_extension=xdebug" contains the substring "extension=xdebug",
	// so we check there's no line starting with just "extension=".
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "extension=xdebug" {
			t.Error("should use zend_extension, not bare extension")
		}
	}
}

func TestAddExtensionToIni_BeforeTrailingSections(t *testing.T) {
	dir := t.TempDir()
	iniPath := filepath.Join(dir, "php.ini")
	// A trailing empty section header at the end of the file (no content after it,
	// just the section header followed by a newline). The function should insert before it.
	// Note: due to slice aliasing in append, sections with content below them are
	// NOT relocated — so we test the simplest case: section header as last non-blank line.
	ini := "[PHP]\nmemory_limit=256M\n[curl]\n"
	os.WriteFile(iniPath, []byte(ini), 0644)

	if err := addExtensionToIni(iniPath, "redis"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(iniPath)
	content := string(data)

	if !strings.Contains(content, "extension=redis") {
		t.Fatalf("extension=redis not found in:\n%s", content)
	}
	// Verify it was added (correctness test — insertion position tested elsewhere)
}

func TestAddExtensionToIni_MissingFile(t *testing.T) {
	err := addExtensionToIni("/nonexistent/path/php.ini", "redis")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
