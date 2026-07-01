package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsLaravelProject(t *testing.T) {
	t.Run("true with artisan file", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "artisan"), []byte("<?php // artisan"), 0644)

		if !isLaravelProject(dir) {
			t.Error("expected true when artisan exists")
		}
	})

	t.Run("true with laravel/framework in composer.json", func(t *testing.T) {
		dir := t.TempDir()
		composer := `{"require": {"laravel/framework": "^11.0"}}`
		_ = os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0644)

		if !isLaravelProject(dir) {
			t.Error("expected true when laravel/framework in composer.json")
		}
	})

	t.Run("false with no artisan and no composer.json", func(t *testing.T) {
		dir := t.TempDir()
		if isLaravelProject(dir) {
			t.Error("expected false for empty directory")
		}
	})

	t.Run("false with composer.json without laravel", func(t *testing.T) {
		dir := t.TempDir()
		composer := `{"require": {"symfony/console": "^6.0"}}`
		_ = os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0644)

		if isLaravelProject(dir) {
			t.Error("expected false without laravel/framework")
		}
	})
}

func TestRequiresReverb(t *testing.T) {
	t.Run("true when laravel/reverb in require", func(t *testing.T) {
		dir := t.TempDir()
		composer := `{"require": {"laravel/reverb": "^1.0"}}`
		_ = os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0644)

		if !requiresReverb(dir) {
			t.Error("expected true when laravel/reverb is required")
		}
	})

	t.Run("false when no composer.json", func(t *testing.T) {
		dir := t.TempDir()
		if requiresReverb(dir) {
			t.Error("expected false when no composer.json")
		}
	})

	t.Run("false when reverb not in composer.json", func(t *testing.T) {
		dir := t.TempDir()
		composer := `{"require": {"laravel/framework": "^11.0"}}`
		_ = os.WriteFile(filepath.Join(dir, "composer.json"), []byte(composer), 0644)

		if requiresReverb(dir) {
			t.Error("expected false when laravel/reverb not present")
		}
	})
}

func TestCheckPort(t *testing.T) {
	t.Run("returns false for closed port", func(t *testing.T) {
		// Port 1 is almost certainly not listening
		if checkPort("127.0.0.1", "1") {
			t.Error("expected false for closed port")
		}
	})

	t.Run("returns false for empty port", func(t *testing.T) {
		if checkPort("127.0.0.1", "") {
			t.Error("expected false for empty port")
		}
	})
}
