package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNginxSyncAllowed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	// Ensure shepherd data dir exists
	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	t.Run("allowed when no lockfile", func(t *testing.T) {
		if !nginxSyncAllowed("fresh-domain") {
			t.Error("expected true when lockfile doesn't exist")
		}
	})

	t.Run("not allowed when recently touched", func(t *testing.T) {
		nginxSyncTouch("recent-domain")
		if nginxSyncAllowed("recent-domain") {
			t.Error("expected false immediately after touch")
		}
	})

	t.Run("allowed after cooldown expires", func(t *testing.T) {
		domain := "old-domain"
		lockPath := nginxSyncLockPath(domain)
		_ = os.MkdirAll(filepath.Dir(lockPath), 0755)
		// Create lockfile with old mod time
		f, _ := os.Create(lockPath)
		_ = f.Close()
		oldTime := time.Now().Add(-5 * time.Second)
		_ = os.Chtimes(lockPath, oldTime, oldTime)

		if !nginxSyncAllowed(domain) {
			t.Error("expected true after cooldown")
		}
	})
}

func TestNginxSyncTouch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	domain := "test-domain"
	nginxSyncTouch(domain)

	lockPath := nginxSyncLockPath(domain)
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("expected lockfile to exist: %v", err)
	}
	if time.Since(info.ModTime()) > 2*time.Second {
		t.Error("lockfile modtime is too old")
	}
}

func TestFindNginxConfsForProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	// Setup parked path with a project
	sitesDir := filepath.Join(root, "Sites")
	projectDir := filepath.Join(sitesDir, "my-app")
	_ = os.MkdirAll(projectDir, 0755)

	// Create valet config
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	_ = os.MkdirAll(valetDir, 0755)
	config := `{"paths": ["` + filepath.ToSlash(sitesDir) + `"], "tld": "test"}`
	_ = os.WriteFile(filepath.Join(valetDir, "config.json"), []byte(config), 0644)

	// Create nginx conf dir with a matching conf
	nginxDir := filepath.Join(valetDir, "Nginx")
	_ = os.MkdirAll(nginxDir, 0755)
	_ = os.WriteFile(filepath.Join(nginxDir, "my-app.test.conf"), []byte("server {}"), 0644)
	_ = os.WriteFile(filepath.Join(nginxDir, "other.test.conf"), []byte("server {}"), 0644)

	t.Run("finds matching conf", func(t *testing.T) {
		confs := findNginxConfsForProject(projectDir)
		if len(confs) != 1 {
			t.Fatalf("expected 1 conf, got %d: %v", len(confs), confs)
		}
		if filepath.Base(confs[0]) != "my-app.test.conf" {
			t.Errorf("expected my-app.test.conf, got %s", filepath.Base(confs[0]))
		}
	})

	t.Run("returns empty for unknown project", func(t *testing.T) {
		unknown := filepath.Join(root, "other-dir")
		_ = os.MkdirAll(unknown, 0755)
		confs := findNginxConfsForProject(unknown)
		if len(confs) != 0 {
			t.Errorf("expected empty, got %v", confs)
		}
	})
}

func TestSyncNginx(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	// Setup parked path with a project
	sitesDir := filepath.Join(root, "Sites")
	projectDir := filepath.Join(sitesDir, "my-app")
	_ = os.MkdirAll(projectDir, 0755)

	// Create valet config
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	_ = os.MkdirAll(valetDir, 0755)
	config := `{"paths": ["` + filepath.ToSlash(sitesDir) + `"], "tld": "test"}`
	_ = os.WriteFile(filepath.Join(valetDir, "config.json"), []byte(config), 0644)

	// Create nginx conf
	nginxDir := filepath.Join(valetDir, "Nginx")
	_ = os.MkdirAll(nginxDir, 0755)
	confContent := "# ISOLATED_PHP_VERSION=8.3\nfastcgi_pass \"$herd_sock_83\";\n"
	confPath := filepath.Join(nginxDir, "my-app.test.conf")
	_ = os.WriteFile(confPath, []byte(confContent), 0644)

	// Create herd bin dir (needed for syncNginx to find bootstrap php)
	herdBin := filepath.Join(root, ".config", "herd", "bin")
	_ = os.MkdirAll(filepath.Join(herdBin, "php84"), 0755)
	_ = os.WriteFile(filepath.Join(herdBin, "php84", "php.exe"), []byte("fake"), 0755)
	_ = os.WriteFile(filepath.Join(herdBin, "herd.phar"), []byte("<?php"), 0644)

	// Create shepherd data dir
	_ = os.MkdirAll(filepath.Join(root, ".config", "shepherd"), 0755)

	t.Run("updates nginx conf for project", func(t *testing.T) {
		changed, found := syncNginx(projectDir, "8.4")
		if !found {
			t.Fatal("expected the project to be found")
		}
		if !changed {
			t.Error("expected a change (8.3 → 8.4)")
		}

		data, _ := os.ReadFile(confPath)
		content := string(data)
		if !strings.Contains(content, "ISOLATED_PHP_VERSION=8.4") {
			t.Errorf("expected version 8.4 in conf, got:\n%s", content)
		}
		if !strings.Contains(content, "$herd_sock_84") {
			t.Errorf("expected $herd_sock_84 in conf, got:\n%s", content)
		}
	})

	t.Run("no-op when project not in parked paths", func(t *testing.T) {
		unknownDir := filepath.Join(root, "unknown-project")
		_ = os.MkdirAll(unknownDir, 0755)
		// Should not panic
		changed, found := syncNginx(unknownDir, "8.5")
		if changed || found {
			t.Errorf("expected (false, false), got (%v, %v)", changed, found)
		}
	})
}

func TestFindNginxConfsForProjectCustomTLD(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	sitesDir := filepath.Join(root, "Sites")
	projectDir := filepath.Join(sitesDir, "my-app")
	_ = os.MkdirAll(projectDir, 0755)

	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	_ = os.MkdirAll(filepath.Join(valetDir, "Nginx"), 0755)
	config := `{"paths": ["` + filepath.ToSlash(sitesDir) + `"], "tld": "dev"}`
	_ = os.WriteFile(filepath.Join(valetDir, "config.json"), []byte(config), 0644)

	_ = os.WriteFile(filepath.Join(valetDir, "Nginx", "my-app.dev.conf"), []byte("server {}"), 0644)
	_ = os.WriteFile(filepath.Join(valetDir, "Nginx", "other.test.conf"), []byte("server {}"), 0644)

	confs := findNginxConfsForProject(projectDir)
	if len(confs) != 1 {
		t.Fatalf("expected 1 conf, got %d: %v", len(confs), confs)
	}
	if filepath.Base(confs[0]) != "my-app.dev.conf" {
		t.Errorf("expected my-app.dev.conf, got %s", filepath.Base(confs[0]))
	}
}

func TestFindNginxConfsForProjectWorkspaceRecord(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	// The workspace checkout has NO junction in valet\Sites: the registry
	// record alone must be enough to locate its conf.
	sitesDir := filepath.Join(root, "Sites")
	_ = os.MkdirAll(filepath.Join(sitesDir, "shop"), 0755)
	checkout := filepath.Join(root, "worktrees", "feature-auth")
	_ = os.MkdirAll(checkout, 0755)
	fakeWorkspace(t, root, checkout, "feature-auth.shop", "shop", "feature/auth")

	// The Herd 2 nginx conf for the workspace subdomain.
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	_ = os.MkdirAll(filepath.Join(valetDir, "Nginx"), 0755)
	_ = os.WriteFile(filepath.Join(valetDir, "Nginx", "feature-auth.shop.test.conf"), []byte("server {}"), 0644)

	confs := findNginxConfsForProject(checkout)
	if len(confs) != 1 {
		t.Fatalf("expected 1 conf, got %d: %v", len(confs), confs)
	}
	if filepath.Base(confs[0]) != "feature-auth.shop.test.conf" {
		t.Errorf("expected feature-auth.shop.test.conf, got %s", filepath.Base(confs[0]))
	}
}

func TestRestartNginx(t *testing.T) {
	t.Run("skips the phar call on Herd 2 with the app closed", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("USERPROFILE", root)
		herdBin := filepath.Join(root, ".config", "herd", "bin")
		configDir := filepath.Join(root, ".config", "herd", "config")
		_ = os.MkdirAll(herdBin, 0755)
		_ = os.MkdirAll(configDir, 0755)
		_ = os.WriteFile(filepath.Join(configDir, "herd.json"), []byte("{}"), 0644)
		// The API port is not listening → the app is closed.
		_ = os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"apiPort": 23998}`), 0644)
		_ = os.MkdirAll(filepath.Join(herdBin, "php84"), 0755)
		// If the phar were attempted, this non-executable file would make
		// exec fail and restartNginx would return an error.
		_ = os.WriteFile(filepath.Join(herdBin, "php84", "php.exe"), []byte("fake"), 0755)
		_ = os.WriteFile(filepath.Join(herdBin, "herd.phar"), []byte("<?php"), 0644)

		if err := restartNginx(); err != nil {
			t.Errorf("expected nil (skipped), got %v", err)
		}
	})

	t.Run("attempts the phar call on Herd 1", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("USERPROFILE", root)
		herdBin := filepath.Join(root, ".config", "herd", "bin")
		_ = os.MkdirAll(herdBin, 0755)
		_ = os.MkdirAll(filepath.Join(herdBin, "php84"), 0755)
		_ = os.WriteFile(filepath.Join(herdBin, "php84", "php.exe"), []byte("fake"), 0755)
		_ = os.WriteFile(filepath.Join(herdBin, "herd.phar"), []byte("<?php"), 0644)

		// Herd 1: the phar is attempted; the fake php.exe cannot run, so an
		// error proves the attempt was made.
		if err := restartNginx(); err == nil {
			t.Error("expected an error when the phar cannot run (attempt made)")
		}
	})
}
