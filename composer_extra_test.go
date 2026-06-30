package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckComposerPHPConstraint(t *testing.T) {
	t.Run("no warning when version satisfies constraint", func(t *testing.T) {
		dir := t.TempDir()
		composer := `{"require": {"php": "^8.2"}}`
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0644)

		// Capture stderr
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		checkComposerPHPConstraint(dir, "8.4")

		w.Close()
		os.Stderr = oldStderr

		buf := make([]byte, 1024)
		n, _ := r.Read(buf)
		output := string(buf[:n])
		if strings.Contains(output, "Warning") {
			t.Errorf("expected no warning, got: %s", output)
		}
	})

	t.Run("warning when version violates constraint", func(t *testing.T) {
		dir := t.TempDir()
		composer := `{"require": {"php": "^8.4"}}`
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0644)

		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		checkComposerPHPConstraint(dir, "8.3")

		w.Close()
		os.Stderr = oldStderr

		buf := make([]byte, 1024)
		n, _ := r.Read(buf)
		output := string(buf[:n])
		if !strings.Contains(output, "Warning") {
			t.Error("expected warning for version mismatch")
		}
	})

	t.Run("no crash when composer.json missing", func(t *testing.T) {
		dir := t.TempDir()
		// Should not panic
		checkComposerPHPConstraint(dir, "8.4")
	})

	t.Run("no crash when no php constraint", func(t *testing.T) {
		dir := t.TempDir()
		composer := `{"require": {"laravel/framework": "^11.0"}}`
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0644)
		checkComposerPHPConstraint(dir, "8.4")
	})

	t.Run("no crash on invalid JSON", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "composer.json"), []byte("not json"), 0644)
		checkComposerPHPConstraint(dir, "8.4")
	})
}
