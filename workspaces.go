package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Herd 2.0 workspace support.
//
// Herd 2 can serve "workspaces": isolated checkouts of a site, each under
// its own subdomain (<branch-label>.<parent-site>.<tld>). There are two
// kinds:
//
//   - "clone"    — a full copy Herd creates with `herd workspace [name]`
//   - "worktree" — an existing `git worktree add` checkout that Herd
//     adopted (via `herd workspace:adopt`, or on demand when its URL is
//     visited for the first time)
//
// Every registered workspace is (a) linked as a junction in
// config\valet\Sites, (b) recorded in config\valet\workspaces.json, and
// (c) marked by a .herd-workspace.json file at the checkout root
// (git-excluded). Plain worktrees nobody adopted yet are listed by
// `herd workspaces` and served on demand on Herd 2; Shepherd detects
// them to point users at the right domain.

const herdWorkspaceMarkerName = ".herd-workspace.json"

// workspaceRecord mirrors one entry of Herd's workspaces.json registry.
// The .herd-workspace.json marker file holds the same record. Only the
// fields Shepherd needs are decoded; unknown fields are ignored.
type workspaceRecord struct {
	Kind           string `json:"kind"` // "clone" or "worktree"
	Name           string `json:"name"`
	Site           string `json:"site"` // host without TLD, e.g. "feature-auth.shop"
	URL            string `json:"url"`
	Path           string `json:"path"` // checkout root
	AppDir         string `json:"app_dir"`
	AppPath        string `json:"app_path"`
	ParentSite     string `json:"parent_site"`
	ParentPath     string `json:"parent_path"`
	Branch         string `json:"branch"`
	BaseBranch     string `json:"base_branch"`
	BaseCommit     string `json:"base_commit"`
	Secured        bool   `json:"secured"`
	PhpVersion     string `json:"php_version"`
	DatabaseMode   string `json:"database_mode"`
	DatabaseName   string `json:"database_name"`
	DatabaseDriver string `json:"database_driver"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      string `json:"created_at"`
}

// herdWorkspacesRegistryPath returns the path of Herd 2's workspace registry.
func herdWorkspacesRegistryPath() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "herd", "config", "valet", "workspaces.json")
}

// herdWorkspacesRegistry reads the workspace registry. A missing, empty or
// malformed file yields no records — Herd 1 has no registry at all, and a
// locked file (Herd writes it atomically) must not be a hard failure.
func herdWorkspacesRegistry() []workspaceRecord {
	data, err := os.ReadFile(herdWorkspacesRegistryPath())
	if err != nil {
		return nil
	}
	var reg struct {
		Workspaces []workspaceRecord `json:"workspaces"`
	}
	if json.Unmarshal(data, &reg) != nil {
		return nil
	}
	return reg.Workspaces
}

// pathKey normalizes a path for case-insensitive comparison, the way Herd
// compares paths (backslashes to separators, symlinks/junctions resolved,
// lowercased, cleaned).
func pathKey(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.FromSlash(p)
	if resolved := resolvePhysicalPath(p); resolved != "" {
		p = resolved
	}
	return strings.ToLower(filepath.Clean(p))
}

// rootOf returns the nearest ancestor of dir (or dir itself) that contains
// a .git entry (directory or pointer file) — the checkout root — or ""
// when dir is not inside a git checkout. Mirrors Herd's GitWorktrees::rootOf.
func rootOf(dir string) string {
	current := resolvePhysicalPath(dir)
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// markerWorkspaceRecord walks up from dir looking for the
// .herd-workspace.json marker Herd writes at a workspace checkout root
// (the same walk .phpversion uses, bounded by the first .git entry so a
// monorepo app subdirectory still finds the marker at the checkout root).
func markerWorkspaceRecord(dir string) (workspaceRecord, bool) {
	current := resolvePhysicalPath(dir)
	for {
		if data, err := os.ReadFile(filepath.Join(current, herdWorkspaceMarkerName)); err == nil {
			var rec workspaceRecord
			if json.Unmarshal(data, &rec) == nil && (rec.Site != "" || rec.Path != "") {
				return rec, true
			}
		}
		// A .git entry marks the checkout root: if the marker isn't here,
		// this checkout is not a workspace — don't keep walking up.
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return workspaceRecord{}, false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return workspaceRecord{}, false
		}
		current = parent
	}
}

// findWorkspaceRecord returns the workspace record for dir, or nil.
// Sources, in order: the checkout's marker file (walked up from dir) and
// the registry (matched by resolved checkout path, then app path).
func findWorkspaceRecord(dir string) *workspaceRecord {
	if rec, ok := markerWorkspaceRecord(dir); ok {
		return &rec
	}

	records := herdWorkspacesRegistry()
	if len(records) == 0 {
		return nil
	}
	target := pathKey(dir)
	root := pathKey(rootOf(dir))
	if target == "" {
		return nil
	}
	for i := range records {
		rec := &records[i]
		if rec.Path != "" && pathKey(rec.Path) == target {
			return rec
		}
		if root != "" && rec.Path != "" && pathKey(rec.Path) == root {
			return rec
		}
		if rec.AppPath != "" && pathKey(rec.AppPath) == target {
			return rec
		}
	}
	return nil
}

// workspaceHost returns the hostname (without TLD) a workspace is served
// under: the record's own "site" when present (Herd writes the exact
// host), otherwise the host derived from branch + parent site like Herd.
func workspaceHost(rec workspaceRecord) string {
	if rec.Site != "" {
		return rec.Site
	}
	return deriveWorkspaceHost(rec.Branch, rec.ParentSite, herdTLD())
}

// maxDomainLength mirrors Workspace::MAX_DOMAIN_LENGTH — "host.tld" must
// stay under X.509 CN (64) and DNS label (63) limits.
const maxDomainLength = 60

var (
	branchLabelRe = regexp.MustCompile(`[^a-z0-9-]+`)
	siteLabelRe   = regexp.MustCompile(`[^a-z0-9.-]+`)
	dashRunRe     = regexp.MustCompile(`-{2,}`)
)

// sanitizeBranchLabel replicates Workspace::sanitizeLabel: lowercase,
// anything that is not a-z0-9- becomes a dash, dash runs collapse,
// leading/trailing dashes trimmed.
func sanitizeBranchLabel(v string) string {
	v = strings.ToLower(v)
	v = branchLabelRe.ReplaceAllString(v, "-")
	return strings.Trim(dashRunRe.ReplaceAllString(v, "-"), "-")
}

// sanitizeSiteLabel sanitizes a site name like Herd's deriveHost does for
// the parent part (dots are kept: site names can be full hosts).
func sanitizeSiteLabel(v string) string {
	v = strings.ToLower(v)
	v = siteLabelRe.ReplaceAllString(v, "-")
	return strings.Trim(dashRunRe.ReplaceAllString(v, "-"), "-.")
}

// workspaceHostTaken mirrors Workspace::hostIsTaken: a host is taken when
// the registry already names it, a Sites link exists for it, or a parked
// directory carries it.
func workspaceHostTaken(host string) bool {
	for _, rec := range herdWorkspacesRegistry() {
		if rec.Site == host {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(herdSitesDir(), host)); err == nil {
		return true
	}
	for _, dir := range herdParkedPaths() {
		if info, err := os.Stat(filepath.Join(dir, host)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// deriveWorkspaceHost replicates Workspace::deriveHost:
// "<sanitized-branch>.<parent>" with the label clamped so that
// "<label>.<parent>.<tld>" stays within Herd's domain budget. A numeric
// suffix resolves collisions with hosts that are already taken.
func deriveWorkspaceHost(branch, parentSite, tld string) string {
	if tld == "" {
		tld = "test"
	}
	parent := sanitizeSiteLabel(parentSite)
	budget := maxDomainLength - len(tld) - 1 - len(parent) - 1

	label := sanitizeBranchLabel(branch)
	if label == "" {
		label = "workspace"
	}
	if budget < 8 {
		budget = 8
	}
	if len(label) > budget {
		label = strings.TrimRight(label[:budget], "-")
	}

	join := func(label string) string {
		if parent == "" {
			return label
		}
		return label + "." + parent
	}

	candidate := join(label)
	suffix := 2
	for i := 0; i < 32 && workspaceHostTaken(candidate); i++ {
		tail := "-" + strconv.Itoa(suffix)
		suffix++
		// PHP's substr() is a no-op when the length exceeds the string; cap
		// the Go slice the same way.
		keep := budget - len(tail)
		if keep > len(label) {
			keep = len(label)
		}
		if keep < 1 {
			keep = 1
		}
		candidate = join(strings.TrimRight(label[:keep], "-") + tail)
	}
	return candidate
}

// worktreeInfo describes a plain `git worktree add` checkout of a Herd
// site that is not registered as a Herd workspace yet.
type worktreeInfo struct {
	Checkout   string // worktree checkout root
	ParentPath string // main checkout (repository root)
	ParentSite string // Herd site name of the parent
	Branch     string // current branch ("" when detached)
	Head       string // commit SHA when detached
	Domain     string // host Herd serves it under, without TLD
}

var gitdirPointerRe = regexp.MustCompile(`(?m)^gitdir:\s*(.+?)\s*$`)
var worktreeGitdirRe = regexp.MustCompile(`^(.+)/\.git/worktrees/([^/]+)/?$`)

// isAbsPath reports whether p is an absolute path (Unix-style or a
// Windows drive/UNC path, forward or back slashes).
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || strings.HasPrefix(p, "//") {
		return true
	}
	return len(p) >= 2 && p[1] == ':'
}

// gitWorktreeParent parses a worktree checkout's .git pointer file —
// "gitdir: <repo>/.git/worktrees/<name>" — like Herd's
// GitWorktrees::parentOf. Returns the main checkout (repository root) and
// the worktree's id in the repository's roster. ok is false for a main
// checkout (.git is a directory), a plain directory, or a pointer that is
// not a worktree's (e.g. a submodule).
func gitWorktreeParent(checkout string) (repoRoot, worktreeID string, ok bool) {
	pointer := filepath.Join(checkout, ".git")
	info, err := os.Stat(pointer)
	if err != nil || info.IsDir() {
		return "", "", false
	}
	data, err := os.ReadFile(pointer)
	if err != nil {
		return "", "", false
	}
	m := gitdirPointerRe.FindSubmatch(data)
	if m == nil {
		return "", "", false
	}
	raw := strings.TrimSpace(string(m[1]))
	// Pointers use forward slashes in git's format; match in that space,
	// then convert back to the platform separator.
	slash := strings.ReplaceAll(raw, "\\", "/")
	m2 := worktreeGitdirRe.FindStringSubmatch(slash)
	if m2 == nil {
		return "", "", false
	}
	repoRoot = filepath.FromSlash(m2[1])
	worktreeID = m2[2]
	if !isAbsPath(repoRoot) {
		// worktree.useRelativePaths: git writes the pointer relative to
		// the checkout root.
		resolved, err := filepath.Abs(filepath.Join(checkout, repoRoot))
		if err != nil {
			return "", "", false
		}
		repoRoot = resolved
	}
	return strings.TrimRight(filepath.Clean(repoRoot), string(os.PathSeparator)), worktreeID, true
}

// worktreeBranch reads the worktree's HEAD from the main repository's
// roster: <repoRoot>/.git/worktrees/<id>/HEAD holds either
// "ref: refs/heads/<branch>" or a bare SHA (detached).
func worktreeBranch(repoRoot, worktreeID string) (branch, head string) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".git", "worktrees", worktreeID, "HEAD"))
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(string(data))
	if b, found := strings.CutPrefix(line, "ref: refs/heads/"); found {
		return b, ""
	}
	return "", line
}

// worktreeCheckoutFromRoster resolves one entry of a repository's worktree
// roster back to its checkout root, from <repoRoot>/.git/worktrees/<id>/gitdir.
// That file points at the checkout's .git file (absolute, or relative to
// the roster entry when git was told to use relative paths). Stale entries
// (checkout deleted, pointer mismatch, submodules) yield "".
func worktreeCheckoutFromRoster(repoRoot, worktreeID string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".git", "worktrees", worktreeID, "gitdir"))
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(data))
	if !isAbsPath(p) {
		p = filepath.Join(repoRoot, ".git", "worktrees", worktreeID, p)
	}
	p = filepath.FromSlash(p)
	if filepath.Base(p) != ".git" {
		return "", false
	}
	checkout := filepath.Dir(p)
	info, err := os.Stat(checkout)
	if err != nil || !info.IsDir() {
		return "", false
	}
	// Round trip: the checkout's .git file must point back at this roster
	// entry (this also rejects submodules, whose .git files point into
	// .git/modules).
	data2, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return "", false
	}
	m := gitdirPointerRe.FindSubmatch(data2)
	if m == nil {
		return "", false
	}
	back := string(m[1])
	if !isAbsPath(back) {
		back = filepath.Join(checkout, back)
	}
	want := filepath.FromSlash(filepath.Join(repoRoot, ".git", "worktrees", worktreeID))
	if pathKey(back) != pathKey(want) {
		return "", false
	}
	return checkout, true
}

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isServedSitePath reports whether the path key names a directory that
// Herd serves on its own (a direct entry of any parked path — the Sites
// link directory included, as it is itself a parked path).
func isServedSitePath(key string) bool {
	if key == "" {
		return false
	}
	for _, dir := range herdParkedPaths() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if pathKey(filepath.Join(dir, e.Name())) == key {
				return true
			}
		}
	}
	return false
}

// findUnservedWorktree returns worktreeInfo for dir when dir is inside a
// plain `git worktree add` checkout of a Herd-served site that is not
// registered as a workspace yet. Nil in every other case: main checkouts,
// registered workspaces, checkouts Herd serves on their own, or
// non-worktree directories.
func findUnservedWorktree(dir string) *worktreeInfo {
	root := rootOf(dir)
	if root == "" {
		return nil
	}
	// Already a registered workspace: it is served, nothing to hint.
	if findWorkspaceRecord(root) != nil {
		return nil
	}
	repoRoot, worktreeID, ok := gitWorktreeParent(root)
	if !ok {
		return nil
	}
	// A worktree that is itself a served site is served under its own name.
	if isServedSitePath(pathKey(root)) {
		return nil
	}

	branch, head := worktreeBranch(repoRoot, worktreeID)
	label := firstNonEmpty(branch, head)
	if label == "" {
		return nil
	}

	// Find the served site whose repository this worktree belongs to: the
	// site is a parked entry (or Sites link) whose checkout root is the
	// main checkout.
	siteName := ""
	for _, dir := range herdParkedPaths() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if pathKey(rootOf(filepath.Join(dir, e.Name()))) == pathKey(repoRoot) {
				siteName = e.Name()
				break
			}
		}
		if siteName != "" {
			break
		}
	}
	if siteName == "" {
		return nil
	}

	return &worktreeInfo{
		Checkout:   root,
		ParentPath: repoRoot,
		ParentSite: siteName,
		Branch:     branch,
		Head:       head,
		Domain:     deriveWorkspaceHost(label, siteName, herdTLD()),
	}
}

// listUnservedWorktrees returns every `git worktree add` checkout of a
// Herd-served site that is not registered as a workspace yet, shaped like
// what `herd workspaces` lists as "git worktree (not served)". Only
// repositories that actually have linked worktrees cost a roster read.
func listUnservedWorktrees() []worktreeInfo {
	type siteEntry struct {
		name string
		path string
	}
	var sites []siteEntry
	for _, dir := range herdParkedPaths() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
				continue
			}
			sites = append(sites, siteEntry{name: e.Name(), path: filepath.Join(dir, e.Name())})
		}
	}
	if len(sites) == 0 {
		return nil
	}

	registered := map[string]bool{}
	for _, rec := range herdWorkspacesRegistry() {
		if k := pathKey(rec.Path); k != "" {
			registered[k] = true
		}
	}

	seen := map[string]bool{}
	var out []worktreeInfo
	sep := string(os.PathSeparator)

	for _, s := range sites {
		key := pathKey(s.path)

		// Workspaces are linked sites too, but they are never the parent
		// of another workspace: skip them as parents (Herd does the same).
		skip := false
		for rk := range registered {
			if key == rk || strings.HasPrefix(key, rk+sep) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		repoRoot := rootOf(s.path)
		if repoRoot == "" {
			continue
		}
		// Cheap pre-check before touching the roster: most sites have no
		// linked worktrees at all.
		if _, err := os.Stat(filepath.Join(repoRoot, ".git", "worktrees")); err != nil {
			continue
		}

		ids, err := os.ReadDir(filepath.Join(repoRoot, ".git", "worktrees"))
		if err != nil {
			continue
		}
		for _, id := range ids {
			if !id.IsDir() {
				continue
			}
			checkout, ok := worktreeCheckoutFromRoster(repoRoot, id.Name())
			if !ok {
				continue
			}
			ck := pathKey(checkout)
			if seen[ck] || registered[ck] || isServedSitePath(ck) {
				continue
			}
			branch, head := worktreeBranch(repoRoot, id.Name())
			label := firstNonEmpty(branch, head)
			if label == "" {
				continue
			}
			seen[ck] = true
			out = append(out, worktreeInfo{
				Checkout:   checkout,
				ParentPath: repoRoot,
				ParentSite: s.name,
				Branch:     branch,
				Head:       head,
				Domain:     deriveWorkspaceHost(label, s.name, herdTLD()),
			})
		}
	}
	return out
}

// worktreeHintCooldown limits how often the same checkout gets the
// unadopted-worktree hint (positive or negative: the lock is touched
// either way, so a non-worktree directory is only rescaned once per
// interval).
const worktreeHintCooldown = 10 * time.Minute

// maybeHintUnservedWorktree prints a one-off hint when a .phpversion
// version switch found no nginx config to update on Herd 2 and the
// directory is a plain `git worktree add` checkout of a served site:
// Herd 2 serves such worktrees on demand under a branch subdomain, and
// visiting it (or `herd workspace:adopt`) registers them.
func maybeHintUnservedWorktree(dir string) {
	if quiet {
		return
	}

	lockName := ".worktree_hint_" + strconv.FormatUint(fnvHash(dir), 16)
	lockPath := filepath.Join(shepherdDataDir(), lockName)
	info, err := os.Stat(lockPath)
	if err == nil && time.Since(info.ModTime()) < worktreeHintCooldown {
		return
	}

	wt := findUnservedWorktree(dir)
	if wt == nil {
		return
	}

	_ = os.MkdirAll(shepherdDataDir(), 0755)
	if f, err := os.Create(lockPath); err == nil {
		_ = f.Close()
	}

	fmt.Fprintf(os.Stderr,
		"shp: this directory is a git worktree of Herd site %q (branch %q), not a Herd workspace yet.\n"+
			"  Herd 2 serves it on demand at %s — or adopt it explicitly:\n"+
			"    herd workspace:adopt %s\n",
		wt.ParentSite, firstNonEmpty(wt.Branch, "detached "+wt.Head), wt.Domain+"."+herdTLD(), wt.Checkout)
}

// fnvHash returns a stable FNV-1a hash of s, for short lockfile names.
func fnvHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
