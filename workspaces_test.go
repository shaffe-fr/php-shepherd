package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitRepo builds a bare-bones git repository layout at repo:
// a .git directory with a worktrees roster. It does not run git; the
// on-disk structures are all Shepherd (and Herd) read:
//
//	repo/.git/worktrees/<id>/gitdir   -> the checkout's .git file (path)
//	repo/.git/worktrees/<id>/HEAD     -> "ref: refs/heads/<branch>" (or a SHA)
//	<checkout>/.git                   -> "gitdir: <repo>/.git/worktrees/<id>"
func fakeGitRepo(t *testing.T, repo, checkout, wtID, branch string, detached bool) {
	t.Helper()

	roster := filepath.Join(repo, ".git", "worktrees", wtID)
	if err := os.MkdirAll(roster, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkout, 0755); err != nil {
		t.Fatal(err)
	}

	rosterPointer := "gitdir: " + filepath.ToSlash(filepath.Join(repo, ".git", "worktrees", wtID))
	if err := os.WriteFile(filepath.Join(roster, "gitdir"), []byte(filepath.ToSlash(filepath.Join(checkout, ".git"))+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git"), []byte(rosterPointer+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	head := "ref: refs/heads/" + branch
	if detached {
		head = "1234567890abcdef1234567890abcdef12345678"
	}
	if err := os.WriteFile(filepath.Join(roster, "HEAD"), []byte(head+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// fakeWorkspace builds a Herd 2 workspace checkout: marker file, registry
// entry and Sites junction, under a fake USERPROFILE root.
func fakeWorkspace(t *testing.T, root, checkout, site, parentSite, branch string) {
	t.Helper()

	if err := os.MkdirAll(checkout, 0755); err != nil {
		t.Fatal(err)
	}

	rec := workspaceRecord{
		Kind:       "worktree",
		Name:       branch,
		Site:       site,
		URL:        "http://" + site + "." + "test",
		Path:       checkout,
		ParentSite: parentSite,
		ParentPath: filepath.Join(root, "Sites", parentSite),
		Branch:     branch,
		PhpVersion: "8.4",
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, herdWorkspaceMarkerName), data, 0644); err != nil {
		t.Fatal(err)
	}

	registry := map[string]interface{}{"workspaces": []workspaceRecord{rec}}
	regData, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	if err := os.MkdirAll(filepath.Join(valetDir, "Sites"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(valetDir, "workspaces.json"), regData, 0644); err != nil {
		t.Fatal(err)
	}
	// The Sites junction Herd would create (a directory stand-in in tests).
	if err := os.MkdirAll(filepath.Join(valetDir, "Sites", site), 0755); err != nil {
		t.Fatal(err)
	}

	config := map[string]interface{}{
		"paths": []string{filepath.ToSlash(filepath.Join(root, "Sites"))},
		"tld":   "test",
	}
	configData, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(valetDir, "config.json"), configData, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRegistry(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	if err := os.MkdirAll(valetDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("missing file yields no records", func(t *testing.T) {
		if recs := herdWorkspacesRegistry(); len(recs) != 0 {
			t.Errorf("expected 0 records, got %d", len(recs))
		}
	})

	t.Run("parses the workspaces array", func(t *testing.T) {
		reg := `{"workspaces":[{"kind":"worktree","name":"fix-bug","site":"fix-bug.shop","path":"C:\\wt\\fix-bug","branch":"fix/bug","php_version":"8.4"}]}`
		if err := os.WriteFile(filepath.Join(valetDir, "workspaces.json"), []byte(reg), 0644); err != nil {
			t.Fatal(err)
		}
		recs := herdWorkspacesRegistry()
		if len(recs) != 1 {
			t.Fatalf("expected 1 record, got %d", len(recs))
		}
		if recs[0].Site != "fix-bug.shop" || recs[0].Branch != "fix/bug" || recs[0].PhpVersion != "8.4" {
			t.Errorf("unexpected record: %+v", recs[0])
		}
	})

	t.Run("malformed JSON yields no records", func(t *testing.T) {
		_ = os.WriteFile(filepath.Join(valetDir, "workspaces.json"), []byte("not json"), 0644)
		if recs := herdWorkspacesRegistry(); len(recs) != 0 {
			t.Errorf("expected 0 records, got %d", len(recs))
		}
	})
}

func TestFindWorkspaceRecord(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	checkout := filepath.Join(root, "worktrees", "feature-auth")
	parentSite := filepath.Join(root, "Sites", "shop")
	if err := os.MkdirAll(parentSite, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkout, 0755); err != nil {
		t.Fatal(err)
	}
	fakeWorkspace(t, root, checkout, "feature-auth.shop", "shop", "feature/auth")

	t.Run("finds the record from the checkout root", func(t *testing.T) {
		rec := findWorkspaceRecord(checkout)
		if rec == nil {
			t.Fatal("expected a workspace record")
		}
		if rec.Site != "feature-auth.shop" || rec.Branch != "feature/auth" {
			t.Errorf("unexpected record: %+v", rec)
		}
	})

	t.Run("finds the record from a subdirectory (marker walk-up)", func(t *testing.T) {
		sub := filepath.Join(checkout, "app", "Http")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		rec := findWorkspaceRecord(sub)
		if rec == nil {
			t.Fatal("expected a workspace record from a subdirectory")
		}
		if rec.Site != "feature-auth.shop" {
			t.Errorf("unexpected record: %+v", rec)
		}
	})

	t.Run("registry match without marker", func(t *testing.T) {
		other := filepath.Join(root, "worktrees", "refactor")
		if err := os.MkdirAll(other, 0755); err != nil {
			t.Fatal(err)
		}
		// Registry only (no marker, no .git): Herd's registry still names it.
		recs := herdWorkspacesRegistry()
		recs = append(recs, workspaceRecord{Kind: "clone", Name: "refactor", Site: "refactor.shop", Path: other, ParentSite: "shop", Branch: "refactor"})
		reg := map[string]interface{}{"workspaces": recs}
		data, _ := json.Marshal(reg)
		_ = os.WriteFile(filepath.Join(root, ".config", "herd", "config", "valet", "workspaces.json"), data, 0644)

		rec := findWorkspaceRecord(other)
		if rec == nil || rec.Site != "refactor.shop" {
			t.Fatalf("expected the registry record, got %+v", rec)
		}
	})

	t.Run("nil for an unrelated directory", func(t *testing.T) {
		other := filepath.Join(root, "unrelated")
		if err := os.MkdirAll(other, 0755); err != nil {
			t.Fatal(err)
		}
		if rec := findWorkspaceRecord(other); rec != nil {
			t.Errorf("expected nil, got %+v", rec)
		}
	})
}

func TestWorkspaceHost(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir()) // no config → herdTLD() defaults to "test"

	rec := workspaceRecord{Site: "feature-auth.shop"}
	if got := workspaceHost(rec); got != "feature-auth.shop" {
		t.Errorf("workspaceHost() = %q, want feature-auth.shop", got)
	}

	// No site recorded: derive from branch + parent.
	rec = workspaceRecord{Branch: "fix/bug", ParentSite: "shop"}
	if got := workspaceHost(rec); got != "fix-bug.shop" {
		t.Errorf("workspaceHost() = %q, want fix-bug.shop", got)
	}
}

func TestDeriveWorkspaceHost(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir()) // no registry, no parked paths

	tests := []struct {
		branch, parent, want string
	}{
		{"feature/auth", "shop", "feature-auth.shop"},
		{"Fix BUG#42?!", "shop", "fix-bug-42.shop"},
		{"", "shop", "workspace.shop"},
		{"main", "My-Shop", "main.my-shop"},
		{"a/b-c/d", "shop", "a-b-c-d.shop"},
	}
	for _, tc := range tests {
		if got := deriveWorkspaceHost(tc.branch, tc.parent, "test"); got != tc.want {
			t.Errorf("deriveWorkspaceHost(%q, %q) = %q, want %q", tc.branch, tc.parent, got, tc.want)
		}
	}
}

func TestDeriveWorkspaceHostBudget(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())

	// A long branch is clamped so "<label>.<parent>.<tld>" stays <= 60 chars.
	long := ""
	for i := 0; i < 80; i++ {
		long += "b"
	}
	host := deriveWorkspaceHost(long, "shop", "test")
	if len(host+".test") > 60 {
		t.Errorf("host length %d exceeds budget: %q", len(host)+len(".test"), host)
	}
	if strings.Contains(host, "-") {
		t.Errorf("an all-letter label must not be truncated with a dash: %q", host)
	}
}

func TestDeriveWorkspaceHostCollision(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	if err := os.MkdirAll(valetDir, 0755); err != nil {
		t.Fatal(err)
	}
	// The base host is already taken by a Sites link → the suffix applies.
	if err := os.MkdirAll(filepath.Join(valetDir, "Sites", "main.shop"), 0755); err != nil {
		t.Fatal(err)
	}
	config := `{"paths":[], "tld":"test"}`
	_ = os.WriteFile(filepath.Join(valetDir, "config.json"), []byte(config), 0644)

	if got := deriveWorkspaceHost("main", "shop", "test"); got != "main-2.shop" {
		t.Errorf("deriveWorkspaceHost() = %q, want main-2.shop", got)
	}
}

func TestGitWorktreeParent(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "shop")
	checkout := filepath.Join(root, "wt", "feature-x")
	fakeGitRepo(t, repo, checkout, "feature-x", "feature/x", false)

	t.Run("absolute pointer", func(t *testing.T) {
		gotRepo, wtID, ok := gitWorktreeParent(checkout)
		if !ok {
			t.Fatal("expected a worktree")
		}
		if pathKey(gotRepo) != pathKey(repo) || wtID != "feature-x" {
			t.Errorf("got (%q, %q), want (%q, feature-x)", gotRepo, wtID, repo)
		}
	})

	t.Run("main checkout is not a worktree", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := gitWorktreeParent(repo); ok {
			t.Error("expected a main checkout (.git dir) to not be a worktree")
		}
	})

	t.Run("non-git directory", func(t *testing.T) {
		plain := filepath.Join(root, "plain")
		_ = os.MkdirAll(plain, 0755)
		if _, _, ok := gitWorktreeParent(plain); ok {
			t.Error("expected a plain directory to not be a worktree")
		}
	})
}

func TestWorktreeBranch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "shop")
	checkout := filepath.Join(root, "wt", "feature-x")
	fakeGitRepo(t, repo, checkout, "feature-x", "feature/x", false)
	fakeGitRepo(t, repo, filepath.Join(root, "wt", "detached"), "detached", "", true)

	if branch, head := worktreeBranch(repo, "feature-x"); branch != "feature/x" || head != "" {
		t.Errorf("got (%q, %q), want (feature/x, \"\")", branch, head)
	}
	if branch, head := worktreeBranch(repo, "detached"); branch != "" || head == "" {
		t.Errorf("detached: got (%q, %q), want (\"\", sha)", branch, head)
	}
}

func TestFindUnservedWorktree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	// Parked layout: C:\...\Sites\shop is the main checkout (a git repo).
	sitesDir := filepath.Join(root, "Sites")
	repo := filepath.Join(sitesDir, "shop")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0755)

	// A worktree of that repo, outside any parked path.
	checkout := filepath.Join(root, "worktrees", "feature-auth")
	fakeGitRepo(t, repo, checkout, "feature-auth", "feature/auth", false)

	// Herd 2 with the site parked.
	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	_ = os.MkdirAll(filepath.Join(valetDir, "Sites"), 0755)
	config := `{"paths":["` + filepath.ToSlash(sitesDir) + `"], "tld":"test"}`
	_ = os.WriteFile(filepath.Join(valetDir, "config.json"), []byte(config), 0644)
	_ = os.WriteFile(filepath.Join(root, ".config", "herd", "config", "herd.json"), []byte("{}"), 0644)

	t.Run("finds the worktree of the served site", func(t *testing.T) {
		wt := findUnservedWorktree(checkout)
		if wt == nil {
			t.Fatal("expected an unserved worktree")
		}
		if wt.ParentSite != "shop" || wt.Branch != "feature/auth" {
			t.Errorf("unexpected worktree: %+v", wt)
		}
		if wt.Domain != "feature-auth.shop" {
			t.Errorf("domain = %q, want feature-auth.shop", wt.Domain)
		}
	})

	t.Run("finds it from a subdirectory too", func(t *testing.T) {
		sub := filepath.Join(checkout, "app")
		_ = os.MkdirAll(sub, 0755)
		wt := findUnservedWorktree(sub)
		if wt == nil || wt.Domain != "feature-auth.shop" {
			t.Fatalf("expected feature-auth.shop from a subdirectory, got %+v", wt)
		}
	})

	t.Run("nil for the main checkout", func(t *testing.T) {
		if wt := findUnservedWorktree(repo); wt != nil {
			t.Errorf("expected nil for the main checkout, got %+v", wt)
		}
	})

	t.Run("nil once the worktree is registered as a workspace", func(t *testing.T) {
		fakeWorkspace(t, root, checkout, "feature-auth.shop", "shop", "feature/auth")
		if wt := findUnservedWorktree(checkout); wt != nil {
			t.Errorf("expected nil once registered, got %+v", wt)
		}
	})
}

func TestListUnservedWorktrees(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	sitesDir := filepath.Join(root, "Sites")
	repo := filepath.Join(sitesDir, "shop")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0755)

	checkout := filepath.Join(root, "worktrees", "feature-auth")
	fakeGitRepo(t, repo, checkout, "feature-auth", "feature/auth", false)

	valetDir := filepath.Join(root, ".config", "herd", "config", "valet")
	_ = os.MkdirAll(filepath.Join(valetDir, "Sites"), 0755)
	config := `{"paths":["` + filepath.ToSlash(sitesDir) + `"], "tld":"test"}`
	_ = os.WriteFile(filepath.Join(valetDir, "config.json"), []byte(config), 0644)
	_ = os.WriteFile(filepath.Join(root, ".config", "herd", "config", "herd.json"), []byte("{}"), 0644)

	wts := listUnservedWorktrees()
	if len(wts) != 1 {
		t.Fatalf("expected 1 unserved worktree, got %d: %+v", len(wts), wts)
	}
	if wts[0].ParentSite != "shop" || wts[0].Branch != "feature/auth" || wts[0].Domain != "feature-auth.shop" {
		t.Errorf("unexpected worktree: %+v", wts[0])
	}
}

func TestFindProjectDomainWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)

	checkout := filepath.Join(root, "worktrees", "feature-auth")
	parentSite := filepath.Join(root, "Sites", "shop")
	_ = os.MkdirAll(parentSite, 0755)
	fakeWorkspace(t, root, checkout, "feature-auth.shop", "shop", "feature/auth")

	t.Run("the record's host wins", func(t *testing.T) {
		if got := findProjectDomain(checkout); got != "feature-auth.shop" {
			t.Errorf("findProjectDomain() = %q, want feature-auth.shop", got)
		}
	})

	t.Run("from a subdirectory", func(t *testing.T) {
		sub := filepath.Join(checkout, "app")
		_ = os.MkdirAll(sub, 0755)
		if got := findProjectDomain(sub); got != "feature-auth.shop" {
			t.Errorf("findProjectDomain() = %q, want feature-auth.shop", got)
		}
	})

	t.Run("parent site keeps its own name", func(t *testing.T) {
		if got := findProjectDomain(parentSite); got != "shop" {
			t.Errorf("findProjectDomain() = %q, want shop", got)
		}
	})
}
