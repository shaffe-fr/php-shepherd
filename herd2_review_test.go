package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// herdTestEnv is a fake Herd install under a temp USERPROFILE.
type herdTestEnv struct {
	root      string
	herdDir   string
	binDir    string
	configDir string
	valetDir  string
}

// newHerdTestEnv creates the directory skeleton of a Herd install. All
// state lives under a temp USERPROFILE; nothing ever reaches the real
// Herd app (tests only talk to throwaway local listeners).
func newHerdTestEnv(t *testing.T) *herdTestEnv {
	t.Helper()
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	e := &herdTestEnv{root: root}
	e.herdDir = filepath.Join(root, ".config", "herd")
	e.binDir = filepath.Join(e.herdDir, "bin")
	e.configDir = filepath.Join(e.herdDir, "config")
	e.valetDir = filepath.Join(e.configDir, "valet")
	for _, d := range []string{e.binDir, e.configDir, filepath.Join(e.valetDir, "Sites"), filepath.Join(e.valetDir, "Nginx")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *herdTestEnv) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// markHerd2 makes the install look like Herd 2 and points the API at port.
func (e *herdTestEnv) markHerd2(t *testing.T, port int) {
	t.Helper()
	e.write(t, filepath.Join(e.configDir, "herd.json"), "{}")
	e.write(t, filepath.Join(e.configDir, "config.json"), `{"apiPort": `+strconv.Itoa(port)+`}`)
}

// fakePHP creates bin\phpNN\php.exe (not executable on purpose: any
// attempt to run it fails, which proves nothing was spawned).
func (e *herdTestEnv) fakePHP(t *testing.T, nodot string) string {
	t.Helper()
	p := filepath.Join(e.binDir, "php"+nodot, "php.exe")
	e.write(t, p, "fake")
	return p
}

func (e *herdTestEnv) park(t *testing.T, dirs ...string) {
	t.Helper()
	var slashed []string
	for _, d := range dirs {
		slashed = append(slashed, filepath.ToSlash(d))
	}
	data, _ := json.Marshal(map[string]interface{}{"paths": slashed, "tld": "test"})
	e.write(t, filepath.Join(e.valetDir, "config.json"), string(data))
}

func (e *herdTestEnv) registry(t *testing.T, recs ...workspaceRecord) {
	t.Helper()
	data, _ := json.Marshal(map[string]interface{}{"workspaces": recs})
	e.write(t, filepath.Join(e.valetDir, "workspaces.json"), string(data))
}

// closedPort returns an ephemeral port nothing listens on: it binds one
// and releases it right away.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// fakeHerdAPI starts a stand-in for the Herd app's API on an ephemeral
// port and records every requested path.
func fakeHerdAPI(t *testing.T, status int, version string) (port int, paths func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, version)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ = strconv.Atoi(p)
	return port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// mkJunction links link -> target with a directory symlink, skipping the
// test when the platform refuses (symlinks need Developer Mode or admin
// rights on Windows). NTFS junctions are deliberately not used:
// filepath.EvalSymlinks does not resolve them on current Go versions.
func mkJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
}

// --- bootstrap oracle replacement (issues 1 and 4) ---

func TestHerdStablePHPSkipsReleaseCandidates(t *testing.T) {
	e := newHerdTestEnv(t)
	e.markHerd2(t, closedPort(t))
	e.fakePHP(t, "84")
	e.fakePHP(t, "85")
	e.fakePHP(t, "86")
	e.write(t, filepath.Join(e.configDir, "php.json"), `{
		"installed_8.4": "8.4.27", "installed_internal_8.4": 27,
		"installed_8.5": "8.5.12", "installed_internal_8.5": 16,
		"installed_8.6": "8.6.0.RC3", "installed_internal_8.6": 3}`)

	php, ok := herdStablePHP()
	if !ok || filepath.Base(filepath.Dir(php)) != "php85" {
		t.Fatalf("herdStablePHP() = %q, %v; want php85 (php86 is an RC)", php, ok)
	}
	got, err := herdBootstrapPHP()
	if err != nil || filepath.Base(filepath.Dir(got)) != "php85" {
		t.Fatalf("herdBootstrapPHP() = %q, %v; want php85", got, err)
	}
}

func TestHerdStablePHPUnusable(t *testing.T) {
	e := newHerdTestEnv(t)
	e.fakePHP(t, "84")

	if _, ok := herdStablePHP(); ok {
		t.Error("no php.json: expected ok=false")
	}
	e.write(t, filepath.Join(e.configDir, "php.json"), "not json")
	if _, ok := herdStablePHP(); ok {
		t.Error("malformed php.json: expected ok=false")
	}
	// Listed but not installed on disk.
	e.write(t, filepath.Join(e.configDir, "php.json"), `{"installed_8.3": "8.3.36"}`)
	if _, ok := herdStablePHP(); ok {
		t.Error("php.exe missing: expected ok=false")
	}
}

// Regression for the shim re-entrancy loop: with Shepherd's shim first on
// PATH, resolving the bootstrap PHP on Herd 2 (even with Herd's
// find-usable-php.php present) must not spawn any `php` at all.
func TestHerdBootstrapDoesNotSpawnPHPUnderShimFirstPath(t *testing.T) {
	e := newHerdTestEnv(t)
	e.markHerd2(t, closedPort(t))
	e.fakePHP(t, "84")
	e.write(t, filepath.Join(e.configDir, "php.json"), `{"installed_8.4": "8.4.27"}`)
	e.write(t, filepath.Join(e.binDir, "find-usable-php.php"), `<?php shell_exec('php -r "echo phpversion();"');`)

	shimDir := t.TempDir()
	counter := filepath.Join(shimDir, "invocations.txt")
	e.write(t, filepath.Join(shimDir, "php.bat"), "@echo off\r\necho x>>\""+counter+"\"\r\n")
	e.write(t, filepath.Join(shimDir, "php.cmd"), "@echo off\r\necho x>>\""+counter+"\"\r\n")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := herdBootstrapPHP(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(counter); err == nil {
		t.Fatal("the bootstrap spawned a `php` through the shim-first PATH")
	}
}

// --- shared offline fallback chain (issue 7) ---

func TestResolveHerdDefaultPHPUsesActiveVersion(t *testing.T) {
	e := newHerdTestEnv(t)
	e.write(t, filepath.Join(e.configDir, "herd.json"), "{}")
	e.write(t, filepath.Join(e.configDir, "config.json"), `{"apiPort": `+strconv.Itoa(closedPort(t))+`, "activeVersion": "8.4"}`)
	e.fakePHP(t, "84")
	e.fakePHP(t, "85")
	e.write(t, filepath.Join(e.binDir, "herd.phar"), "<?php")
	// No php.bat: which-php cannot run (fake php.exe) and the batch file is
	// missing, so only activeVersion can answer — as it does for `status`.

	php, err := resolveHerdDefaultPHP(e.root)
	if err != nil {
		t.Fatalf("resolveHerdDefaultPHP() error: %v", err)
	}
	if filepath.Base(filepath.Dir(php)) != "php84" {
		t.Errorf("resolveHerdDefaultPHP() = %q, want php84 (activeVersion)", php)
	}
}

// --- app detection (issues 6 and 14, ephemeral ports) ---

func TestHerdAppRunning(t *testing.T) {
	t.Run("200 on the known route means running", func(t *testing.T) {
		e := newHerdTestEnv(t)
		port, paths := fakeHerdAPI(t, http.StatusOK, "2.0.4")
		e.markHerd2(t, port)
		if !herdAppRunning() {
			t.Error("expected the app to be detected")
		}
		for _, p := range paths() {
			if strings.HasPrefix(p, "/restart-service") {
				t.Fatalf("a restart route was requested: %s", p)
			}
		}
	})

	t.Run("an unrelated listener answering non-200 is not the app", func(t *testing.T) {
		e := newHerdTestEnv(t)
		port, _ := fakeHerdAPI(t, http.StatusNotFound, "")
		e.markHerd2(t, port)
		if herdAppRunning() {
			t.Error("a 404 listener must not count as the Herd app")
		}
	})

	t.Run("a bare TCP listener is not the app", func(t *testing.T) {
		e := newHerdTestEnv(t)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		e.markHerd2(t, l.Addr().(*net.TCPAddr).Port)
		if herdAppRunning() {
			t.Error("a listener that does not speak HTTP must not count as the app")
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		e := newHerdTestEnv(t)
		e.markHerd2(t, closedPort(t))
		if herdAppRunning() {
			t.Error("expected not running")
		}
	})
}

func TestHerdVersionStringFromRunningApp(t *testing.T) {
	e := newHerdTestEnv(t)
	port, _ := fakeHerdAPI(t, http.StatusOK, "2.0.4\n")
	e.markHerd2(t, port)
	if got := herdVersionString(); got != "Herd 2.0.4" {
		t.Errorf("herdVersionString() = %q, want Herd 2.0.4", got)
	}
}

func TestIsHerd2Signals(t *testing.T) {
	t.Run("workspace registry alone", func(t *testing.T) {
		e := newHerdTestEnv(t)
		e.registry(t)
		if !isHerd2() {
			t.Error("expected Herd 2 from the workspace registry")
		}
	})
	t.Run("apiPort alone", func(t *testing.T) {
		e := newHerdTestEnv(t)
		e.write(t, filepath.Join(e.configDir, "config.json"), `{"apiPort": 9001}`)
		if !isHerd2() {
			t.Error("expected Herd 2 from apiPort")
		}
	})
	t.Run("none of them", func(t *testing.T) {
		e := newHerdTestEnv(t)
		e.write(t, filepath.Join(e.configDir, "config.json"), `{"basePhpPort": 9000}`)
		if isHerd2() {
			t.Error("expected Herd 1")
		}
	})
}

// --- hostile and stale markers (issue 2) ---

func writeMarker(t *testing.T, e *herdTestEnv, checkout string, rec workspaceRecord) {
	t.Helper()
	data, _ := json.Marshal(rec)
	e.write(t, filepath.Join(checkout, herdWorkspaceMarkerName), string(data))
}

func TestHostileMarkerCannotRetargetNginxConf(t *testing.T) {
	e := newHerdTestEnv(t)
	victimConf := filepath.Join(e.valetDir, "Nginx", "victim.test.conf")
	e.write(t, victimConf, "server {}")
	// A file just outside the nginx directory that "..\\" would reach.
	outside := filepath.Join(e.valetDir, "escape.test.conf")
	e.write(t, outside, "server {}")

	checkout := filepath.Join(e.root, "cloned", "evil")
	e.write(t, filepath.Join(checkout, ".git"), "gitdir: nowhere")

	t.Run("unassociated marker naming another site", func(t *testing.T) {
		writeMarker(t, e, checkout, workspaceRecord{Site: "victim", Path: checkout})
		if rec := findAssociatedWorkspaceRecord(checkout); rec != nil {
			t.Fatalf("expected no associated record, got %+v", rec)
		}
		if confs := findNginxConfsForProject(checkout); len(confs) != 0 {
			t.Fatalf("a committed marker retargeted confs: %v", confs)
		}
		// Marker-only data still serves non-mutating lookups.
		if rec := findWorkspaceRecord(checkout); rec == nil || rec.Site != "victim" {
			t.Errorf("marker-only record should stay available for read-only use, got %+v", rec)
		}
	})

	t.Run("site that is not a DNS name is dropped", func(t *testing.T) {
		for _, site := range []string{`..\escape`, `../escape`, `a/b`, `escape.`, `.x`, "a b", `C:\x`} {
			writeMarker(t, e, checkout, workspaceRecord{Site: site, Path: checkout})
			if rec := findWorkspaceRecord(checkout); rec != nil {
				t.Errorf("site %q: marker should be rejected, got %+v", site, rec)
			}
			if confs := findNginxConfsForProject(checkout); len(confs) != 0 {
				t.Errorf("site %q: unexpected confs %v", site, confs)
			}
		}
	})

	t.Run("stale marker whose registry entry points elsewhere", func(t *testing.T) {
		other := filepath.Join(e.root, "elsewhere")
		_ = os.MkdirAll(other, 0755)
		e.registry(t, workspaceRecord{Name: "v", Site: "victim", Path: other})
		writeMarker(t, e, checkout, workspaceRecord{Site: "victim", Path: checkout})
		if rec := findAssociatedWorkspaceRecord(checkout); rec != nil {
			t.Fatalf("stale marker accepted: %+v", rec)
		}
	})

	t.Run("registry-vouched marker is accepted", func(t *testing.T) {
		e.registry(t, workspaceRecord{Name: "v", Site: "victim", Path: checkout})
		writeMarker(t, e, checkout, workspaceRecord{Site: "victim", Path: checkout})
		confs := findNginxConfsForProject(checkout)
		if len(confs) != 1 || confs[0] != victimConf {
			t.Fatalf("expected the associated conf, got %v", confs)
		}
	})

	t.Run("physical Sites link vouches for the marker", func(t *testing.T) {
		e.registry(t)
		mkJunction(t, filepath.Join(e.valetDir, "Sites", "victim"), checkout)
		if rec := findAssociatedWorkspaceRecord(checkout); rec == nil {
			t.Fatal("expected the Sites link to vouch for the marker")
		}
	})
}

func TestRegistryHostCannotEscapeNginxDir(t *testing.T) {
	e := newHerdTestEnv(t)
	outside := filepath.Join(e.valetDir, "escape.test.conf")
	e.write(t, outside, "server {}")
	checkout := filepath.Join(e.root, "wt")
	_ = os.MkdirAll(checkout, 0755)
	e.registry(t, workspaceRecord{Name: "x", Site: `..\escape`, Path: checkout})

	if confs := findNginxConfsForProject(checkout); len(confs) != 0 {
		t.Fatalf("conf outside the nginx directory returned: %v", confs)
	}
}

// --- registry read failures (issue 13) ---

func TestRegistryReadRetriesOnLock(t *testing.T) {
	e := newHerdTestEnv(t)
	e.registry(t, workspaceRecord{Name: "a", Site: "a.shop", Path: e.root})

	orig := registryReadFile
	defer func() { registryReadFile = orig }()

	t.Run("transient lock then success", func(t *testing.T) {
		calls := 0
		registryReadFile = func(p string) ([]byte, error) {
			calls++
			if calls < 3 {
				return nil, errors.New("The process cannot access the file because it is being used by another process")
			}
			return os.ReadFile(p)
		}
		recs := herdWorkspacesRegistry()
		if len(recs) != 1 || calls != 3 {
			t.Errorf("records=%d calls=%d, want 1 record after 3 calls", len(recs), calls)
		}
	})

	t.Run("persistent lock gives up after the bound", func(t *testing.T) {
		calls := 0
		registryReadFile = func(string) ([]byte, error) {
			calls++
			return nil, errors.New("sharing violation")
		}
		if recs := herdWorkspacesRegistry(); len(recs) != 0 || calls != registryReadAttempts {
			t.Errorf("records=%d calls=%d, want 0 records after %d calls", len(recs), calls, registryReadAttempts)
		}
	})

	t.Run("a missing file is not retried", func(t *testing.T) {
		calls := 0
		registryReadFile = func(string) ([]byte, error) {
			calls++
			return nil, os.ErrNotExist
		}
		if recs := herdWorkspacesRegistry(); len(recs) != 0 || calls != 1 {
			t.Errorf("records=%d calls=%d, want 0 records after 1 call", len(recs), calls)
		}
	})
}

// --- host derivation (issues 8 and 9) ---

func TestWorkspaceHostIgnoresCollisions(t *testing.T) {
	e := newHerdTestEnv(t)
	// The workspace's own Sites link makes its base host look "taken".
	_ = os.MkdirAll(filepath.Join(e.valetDir, "Sites", "main.shop"), 0755)

	got := workspaceHost(workspaceRecord{Branch: "main", ParentSite: "shop"})
	if got != "main.shop" {
		t.Errorf("workspaceHost() = %q, want main.shop (no suffix when resolving)", got)
	}
	// Allocation still avoids it.
	if got := deriveWorkspaceHost("main", "shop", "test"); got != "main-2.shop" {
		t.Errorf("deriveWorkspaceHost() = %q, want main-2.shop", got)
	}
}

func TestDeriveWorkspaceHostExhaustion(t *testing.T) {
	e := newHerdTestEnv(t)
	for _, h := range []string{"main.shop", "main-2.shop", "main-3.shop", "main-4.shop"} {
		_ = os.MkdirAll(filepath.Join(e.valetDir, "Sites", h), 0755)
	}

	orig := maxHostAttempts
	maxHostAttempts = 2
	defer func() { maxHostAttempts = orig }()

	if got := deriveWorkspaceHost("main", "shop", "test"); got != "" {
		t.Errorf("deriveWorkspaceHost() = %q, want \"\" (no free host found)", got)
	}

	// With enough attempts the ladder goes past 32 to the free host.
	maxHostAttempts = orig
	for i := 5; i <= 40; i++ {
		_ = os.MkdirAll(filepath.Join(e.valetDir, "Sites", "main-"+strconv.Itoa(i)+".shop"), 0755)
	}
	if got := deriveWorkspaceHost("main", "shop", "test"); got != "main-41.shop" {
		t.Errorf("deriveWorkspaceHost() = %q, want main-41.shop", got)
	}
}

// --- worktree hint (issues 5, 10, 17) ---

// unservedWorktreeEnv builds a Herd 2 install with a parked repo "shop"
// and a worktree of it outside any parked path.
func unservedWorktreeEnv(t *testing.T) (e *herdTestEnv, repo, checkout string) {
	t.Helper()
	e = newHerdTestEnv(t)
	e.markHerd2(t, closedPort(t))
	sitesDir := filepath.Join(e.root, "Sites")
	repo = filepath.Join(sitesDir, "shop")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	checkout = filepath.Join(e.root, "worktrees", "feature-auth")
	fakeGitRepo(t, repo, checkout, "feature-auth", "feature/auth", false)
	e.park(t, sitesDir)
	return e, repo, checkout
}

func TestMaybeHintUnservedWorktree(t *testing.T) {
	_, _, checkout := unservedWorktreeEnv(t)
	quiet = false

	// The hint prints the checkout as findUnservedWorktree resolves it
	// (rootOf → resolvePhysicalPath), which is not textually equal to the
	// path we joined: a Windows temp dir resolves through 8.3/symlink
	// normalization. Compare against the resolved form the code emits.
	resolvedCheckout := rootOf(checkout)
	first := captureStderr(t, func() { maybeHintUnservedWorktree(checkout) })
	if !strings.Contains(first, "herd workspace:adopt "+resolvedCheckout) {
		t.Fatalf("hint should lead with the adopt command, got: %q", first)
	}
	if strings.Contains(first, "serves it on demand") {
		t.Errorf("hint must not promise on-demand serving: %q", first)
	}
	if !strings.Contains(first, "feature-auth.shop.test") {
		t.Errorf("hint should name the derived host: %q", first)
	}

	if again := captureStderr(t, func() { maybeHintUnservedWorktree(checkout) }); again != "" {
		t.Errorf("second call within the cooldown should be silent, got %q", again)
	}

	sub := filepath.Join(checkout, "app")
	_ = os.MkdirAll(sub, 0755)
	if shared := captureStderr(t, func() { maybeHintUnservedWorktree(sub) }); shared != "" {
		t.Errorf("a subdirectory of the same checkout shares the cooldown, got %q", shared)
	}

	quiet = true
	defer func() { quiet = false }()
	if q := captureStderr(t, func() { maybeHintUnservedWorktree(checkout) }); q != "" {
		t.Errorf("quiet must silence the hint, got %q", q)
	}
}

func TestMaybeHintCachesNegativeResult(t *testing.T) {
	_, _, _ = unservedWorktreeEnv(t)
	quiet = false

	plain := filepath.Join(t.TempDir(), "plain")
	_ = os.MkdirAll(plain, 0755)

	if out := captureStderr(t, func() { maybeHintUnservedWorktree(plain) }); out != "" {
		t.Fatalf("a plain directory must not be hinted, got %q", out)
	}
	locks, _ := filepath.Glob(filepath.Join(shepherdDataDir(), ".worktree_hint_*"))
	if len(locks) != 1 {
		t.Fatalf("the miss should have been cached with a lock, got %v", locks)
	}
}

func TestParentSiteIsFirstByName(t *testing.T) {
	e := newHerdTestEnv(t)
	e.markHerd2(t, closedPort(t))
	parkA := filepath.Join(e.root, "parkA")
	parkB := filepath.Join(e.root, "parkB")
	repo := filepath.Join(parkA, "zeta")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	_ = os.MkdirAll(parkB, 0755)
	mkJunction(t, filepath.Join(parkB, "alpha"), repo)
	checkout := filepath.Join(e.root, "worktrees", "feat")
	fakeGitRepo(t, repo, checkout, "feat", "feat", false)
	// parkA is listed first, but "alpha" sorts before "zeta".
	e.park(t, parkA, parkB)

	wt := findUnservedWorktree(checkout)
	if wt == nil || wt.ParentSite != "alpha" || wt.Domain != "feat.alpha" {
		t.Fatalf("findUnservedWorktree() = %+v, want parent site alpha", wt)
	}
	list := listUnservedWorktrees()
	if len(list) != 1 || list[0].ParentSite != "alpha" {
		t.Fatalf("listUnservedWorktrees() = %+v, want one entry under alpha", list)
	}
}

// --- doctor (issues 11 and 15) ---

func TestDoctorHerd2Section(t *testing.T) {
	e := newHerdTestEnv(t)
	e.markHerd2(t, closedPort(t))
	e.fakePHP(t, "84")
	e.registry(t, workspaceRecord{Name: "gone-ws", Site: "gone.shop", Path: filepath.Join(e.root, "missing")})

	oldJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = oldJSON }()

	out := captureStdout(t, cmdDoctor)
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON in doctor output: %q", out)
	}
	var res struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Fix    string `json:"fix"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &res); err != nil {
		t.Fatalf("invalid doctor JSON: %v\n%s", err, out)
	}
	status := map[string]string{}
	fix := map[string]string{}
	for _, c := range res.Checks {
		status[c.Name] = c.Status
		fix[c.Name] = c.Fix
	}
	if status["herdApp"] != "info" {
		t.Errorf("herdApp status = %q, want info (app closed is not a problem)", status["herdApp"])
	}
	if status["workspaceOrphans"] != "warning" {
		t.Errorf("workspaceOrphans status = %q, want warning", status["workspaceOrphans"])
	}
	if !strings.Contains(fix["workspaceOrphans"], "herd workspace:delete gone-ws") || strings.Contains(fix["workspaceOrphans"], "herd workspaces") {
		t.Errorf("orphan fix should be the delete command, got %q", fix["workspaceOrphans"])
	}
}
