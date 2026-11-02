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

// registryReadAttempts and registryReadDelay bound the retry of a registry
// read: Windows can expose Herd's exclusive writer as a sharing violation,
// and Herd itself retries the same read five times.
const (
	registryReadAttempts = 5
	registryReadDelay    = 50 * time.Millisecond
)

// registryReadFile is the file reader behind the registry (a variable so
// tests can simulate a locked file).
var registryReadFile = os.ReadFile

// readRegistryFile reads path, retrying a few times on errors other than
// "does not exist" (a lock held by Herd's writer is transient).
func readRegistryFile(path string) ([]byte, error) {
	var err error
	for attempt := 0; attempt < registryReadAttempts; attempt++ {
		var data []byte
		data, err = registryReadFile(path)
		if err == nil {
			return data, nil
		}
		if os.IsNotExist(err) {
			return nil, err
		}
		if attempt < registryReadAttempts-1 {
			time.Sleep(registryReadDelay)
		}
	}
	return nil, err
}

// herdWorkspacesRegistry reads the workspace registry. A missing, empty or
// malformed file yields no records — Herd 1 has no registry at all. A
// locked file is retried briefly (see readRegistryFile) before giving up.
func herdWorkspacesRegistry() []workspaceRecord {
	data, err := readRegistryFile(herdWorkspacesRegistryPath())
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

// hostLabelRe matches one legal DNS label.
var hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validWorkspaceHost reports whether host is a legal DNS name made of
// dot-separated labels (workspace hosts look like "feature-auth.shop").
// Anything else — separators, "..", drive letters — is rejected, so a host
// can never be turned into a path outside the directory it is joined to.
func validWorkspaceHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !hostLabelRe.MatchString(label) {
			return false
		}
	}
	return true
}

// markerWorkspace walks up from dir looking for the .herd-workspace.json
// marker Herd writes at a workspace checkout root (the same walk
// .phpversion uses, bounded by the first .git entry so a monorepo app
// subdirectory still finds the marker at the checkout root). It returns
// the record and the directory the marker was found in.
//
// A marker is repository content and may be committed by anyone: a
// "site" that is not a legal host name is discarded here. Use
// findAssociatedWorkspaceRecord before anything that writes.
func markerWorkspace(dir string) (workspaceRecord, string, bool) {
	current := resolvePhysicalPath(dir)
	for {
		if data, err := os.ReadFile(filepath.Join(current, herdWorkspaceMarkerName)); err == nil {
			var rec workspaceRecord
			if json.Unmarshal(data, &rec) == nil && (rec.Site != "" || rec.Path != "") &&
				(rec.Site == "" || validWorkspaceHost(rec.Site)) {
				return rec, current, true
			}
		}
		// A .git entry marks the checkout root: if the marker isn't here,
		// this checkout is not a workspace — don't keep walking up.
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return workspaceRecord{}, "", false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return workspaceRecord{}, "", false
		}
		current = parent
	}
}

// markerWorkspaceRecord is markerWorkspace without the marker's directory.
func markerWorkspaceRecord(dir string) (workspaceRecord, bool) {
	rec, _, ok := markerWorkspace(dir)
	return rec, ok
}

// workspaceAssociated reports whether the workspace host really belongs to
// the checkout, the association Herd itself requires before trusting a
// marker: either the registry names this host for this checkout path, or
// the physical Sites link for the host resolves to the checkout.
func workspaceAssociated(host, checkout string) bool {
	if !validWorkspaceHost(host) || checkout == "" {
		return false
	}
	key := pathKey(checkout)
	if key == "" {
		return false
	}
	for _, rec := range herdWorkspacesRegistry() {
		if rec.Path != "" && workspaceHost(rec) == host && pathKey(rec.Path) == key {
			return true
		}
	}
	link := filepath.Join(herdSitesDir(), host)
	if _, err := os.Lstat(link); err == nil && pathKey(link) == key {
		return true
	}
	return false
}

// findAssociatedWorkspaceRecord is findWorkspaceRecord restricted to
// records proven to belong to dir's checkout. Marker-only data (a marker
// no registry entry or Sites link vouches for) is ignored. This is the
// variant to use before any mutation derived from the record.
func findAssociatedWorkspaceRecord(dir string) *workspaceRecord {
	if rec, root, ok := markerWorkspace(dir); ok {
		if workspaceAssociated(workspaceHost(rec), root) {
			return &rec
		}
	}
	return registryWorkspaceRecord(dir)
}

// findWorkspaceRecord returns the workspace record for dir, or nil.
// Sources, in order: the checkout's marker file (walked up from dir) and
// the registry (matched by resolved checkout path, then app path).
func findWorkspaceRecord(dir string) *workspaceRecord {
	if rec, ok := markerWorkspaceRecord(dir); ok {
		return &rec
	}
	return registryWorkspaceRecord(dir)
}

// registryWorkspaceRecord matches dir against the registry, by resolved
// checkout path then app path.
func registryWorkspaceRecord(dir string) *workspaceRecord {
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
// host), otherwise the base host derived from branch + parent site. This
// resolves an existing record, so no collision avoidance applies: the
// workspace's own link or registry entry would make its host look taken.
func workspaceHost(rec workspaceRecord) string {
	if rec.Site != "" {
		return rec.Site
	}
	host, _ := workspaceBaseHost(rec.Branch, rec.ParentSite, herdTLD())
	return host
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

// workspaceBaseHost computes the host Herd derives from a branch and a
// parent site before any collision handling: "<sanitized-branch>.<parent>"
// with the label clamped so that "<label>.<parent>.<tld>" stays within
// Herd's domain budget. It also returns the clamped label and budget used
// by the suffix ladder.
func workspaceBaseHost(branch, parentSite, tld string) (host string, ladder func(suffix int) string) {
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

	return join(label), func(suffix int) string {
		tail := "-" + strconv.Itoa(suffix)
		// PHP's substr() is a no-op when the length exceeds the string; cap
		// the Go slice the same way.
		keep := budget - len(tail)
		if keep > len(label) {
			keep = len(label)
		}
		if keep < 1 {
			keep = 1
		}
		return join(strings.TrimRight(label[:keep], "-") + tail)
	}
}

// maxHostAttempts bounds the suffix ladder of deriveWorkspaceHost (a
// variable so tests can exhaust it cheaply). Herd loops until a host is
// free; the bound only protects against a pathological filesystem.
var maxHostAttempts = 10000

// deriveWorkspaceHost replicates Workspace::deriveHost for allocation: the
// base host, then "-2", "-3", ... until the host is not taken. It returns
// "" when no free host was found within maxHostAttempts, so callers never
// present a name that belongs to another site.
func deriveWorkspaceHost(branch, parentSite, tld string) string {
	candidate, ladder := workspaceBaseHost(branch, parentSite, tld)
	suffix := 2
	for i := 0; workspaceHostTaken(candidate); i++ {
		if i >= maxHostAttempts {
			return ""
		}
		candidate = ladder(suffix)
		suffix++
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
	// main checkout. When several names serve it, the first by name wins,
	// like Herd's sitePaths().
	siteName := ""
	repoKey := pathKey(repoRoot)
	for _, dir := range herdParkedPaths() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if siteName != "" && e.Name() >= siteName {
				continue
			}
			if pathKey(rootOf(filepath.Join(dir, e.Name()))) == repoKey {
				siteName = e.Name()
			}
		}
	}
	if siteName == "" {
		return nil
	}

	domain := deriveWorkspaceHost(label, siteName, herdTLD())
	if domain == "" {
		return nil
	}

	return &worktreeInfo{
		Checkout:   root,
		ParentPath: repoRoot,
		ParentSite: siteName,
		Branch:     branch,
		Head:       head,
		Domain:     domain,
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

	// A repository may be served under several names; like Herd, the
	// first by name is its parent site. Resolve one entry per repository.
	type repoSite struct {
		root string
		name string
	}
	repos := map[string]*repoSite{}
	var repoOrder []string

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
		rk := pathKey(repoRoot)
		if existing, ok := repos[rk]; ok {
			if s.name < existing.name {
				existing.name = s.name
			}
			continue
		}
		repos[rk] = &repoSite{root: repoRoot, name: s.name}
		repoOrder = append(repoOrder, rk)
	}

	for _, rk := range repoOrder {
		repoRoot := repos[rk].root
		siteName := repos[rk].name
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
			domain := deriveWorkspaceHost(label, siteName, herdTLD())
			if domain == "" {
				continue
			}
			seen[ck] = true
			out = append(out, worktreeInfo{
				Checkout:   checkout,
				ParentPath: repoRoot,
				ParentSite: siteName,
				Branch:     branch,
				Head:       head,
				Domain:     domain,
			})
		}
	}
	return out
}

// worktreeHintCooldown limits how often the same checkout gets the
// unadopted-worktree hint. The lock is touched whether or not the checkout
// turns out to be an unserved worktree, so a negative result is cached too
// and a non-worktree directory is only rescanned once per interval.
const worktreeHintCooldown = 10 * time.Minute

// maybeHintUnservedWorktree prints a one-off hint when a .phpversion
// version switch found no nginx config to update on Herd 2 and the
// directory is a plain `git worktree add` checkout of a served site.
// Herd 2 does not serve such a worktree until it is linked, and its
// subdomain does not resolve before that (Windows has no wildcard DNS), so
// the hint leads with the adopt command and shows the derived host only as
// the name the workspace will get.
func maybeHintUnservedWorktree(dir string) {
	if quiet {
		return
	}

	// Key the cooldown on the checkout, so sibling subdirectories share it.
	key := rootOf(dir)
	if key == "" {
		key = dir
	}
	lockName := ".worktree_hint_" + strconv.FormatUint(fnvHash(pathKey(key)), 16)
	lockPath := filepath.Join(shepherdDataDir(), lockName)
	info, err := os.Stat(lockPath)
	if err == nil && time.Since(info.ModTime()) < worktreeHintCooldown {
		return
	}

	_ = os.MkdirAll(shepherdDataDir(), 0755)
	if f, err := os.Create(lockPath); err == nil {
		_ = f.Close()
	}

	wt := findUnservedWorktree(dir)
	if wt == nil {
		return
	}

	fmt.Fprintf(os.Stderr,
		"shp: this directory is a git worktree of Herd site %q (branch %q), not a Herd workspace yet.\n"+
			"  Herd does not serve it until it is linked. Adopt it to serve it as %s:\n"+
			"    herd workspace:adopt %s\n",
		wt.ParentSite, firstNonEmpty(wt.Branch, "detached "+wt.Head), wt.Domain+"."+herdTLD(), wt.Checkout)
}

// fnvHash returns a stable FNV-1a hash of s, for short lockfile names.
func fnvHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
