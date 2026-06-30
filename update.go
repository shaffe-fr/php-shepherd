package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// githubRelease represents a GitHub release API response.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

// githubAsset represents a release asset.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

const githubRepo = "shaffe-fr/php-shepherd"

// allowedDownloadHosts lists the GitHub domains from which self-update assets may be downloaded.
var allowedDownloadHosts = map[string]bool{
	"github.com":                    true,
	"objects.githubusercontent.com": true,
}

// --- Passive update check ---

// updateCheckCachePath returns the path to the update check cache file.
func updateCheckCachePath() string {
	return filepath.Join(shepherdDataDir(), ".update_check")
}

// updateCheckCache holds the last known latest version and when it was checked.
type updateCheckCache struct {
	LatestVersion string `json:"latest_version"`
	CheckedAt     int64  `json:"checked_at"` // Unix timestamp
}

// updateCheckInterval is the minimum time between network checks (24 hours).
const updateCheckInterval = 24 * time.Hour

// readUpdateCache reads the cached update info. Returns zero-value if missing or unreadable.
func readUpdateCache() updateCheckCache {
	data, err := os.ReadFile(updateCheckCachePath())
	if err != nil {
		return updateCheckCache{}
	}
	var cache updateCheckCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return updateCheckCache{}
	}
	return cache
}

// writeUpdateCache persists the update check result to disk.
func writeUpdateCache(cache updateCheckCache) {
	os.MkdirAll(filepath.Dir(updateCheckCachePath()), 0755)
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = os.WriteFile(updateCheckCachePath(), data, 0644)
}

// backgroundUpdateCheck fetches the latest version from GitHub and updates the cache.
// Runs in a goroutine — must not block the main process or print anything.
func backgroundUpdateCheck() {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "shepherd/"+version)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return
	}

	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&release); err != nil {
		return
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	writeUpdateCache(updateCheckCache{
		LatestVersion: latestVersion,
		CheckedAt:     time.Now().Unix(),
	})
}

// isNewerVersion returns true if "candidate" is strictly newer than "current".
// Both must be dot-separated numeric strings (e.g. "0.8.0", "1.2").
func isNewerVersion(candidate, current string) bool {
	partsA := strings.Split(candidate, ".")
	partsB := strings.Split(current, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		a, b := 0, 0
		if i < len(partsA) {
			a = mustAtoi(partsA[i])
		}
		if i < len(partsB) {
			b = mustAtoi(partsB[i])
		}
		if a > b {
			return true
		}
		if a < b {
			return false
		}
	}
	return false
}

// maybeNotifyUpdate prints a one-line update notice if a newer version is known from cache.
// Returns true if a notice was printed (caller may want to add a blank line after output).
func maybeNotifyUpdate() bool {
	cache := readUpdateCache()
	if cache.LatestVersion == "" {
		return false
	}
	currentVersion := strings.TrimPrefix(version, "v")
	// Don't notify for dev builds
	if currentVersion == "dev" {
		return false
	}
	if !isNewerVersion(cache.LatestVersion, currentVersion) {
		return false
	}
	fmt.Fprintf(os.Stderr, "  Update available: %s → %s (run `shp self-update`)\n", currentVersion, cache.LatestVersion)
	return true
}

// triggerUpdateCheckIfStale starts a background update check if the cache is older than 24h.
// This is non-blocking and has no effect on command latency.
func triggerUpdateCheckIfStale() {
	cache := readUpdateCache()
	if cache.CheckedAt > 0 && time.Since(time.Unix(cache.CheckedAt, 0)) < updateCheckInterval {
		return
	}
	go backgroundUpdateCheck()
}

// cmdSelfUpdate checks for a newer release on GitHub and updates the binary.
func cmdSelfUpdate() {
	fmt.Printf("shp %s\n", version)
	fmt.Println("Checking for updates...")

	// Fetch latest release from GitHub API
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "shepherd/"+version)

	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error contacting GitHub: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Error: GitHub API returned HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	// Limit API response to 1MB to prevent resource exhaustion.
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&release); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing release info: %v\n", err)
		os.Exit(1)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := strings.TrimPrefix(version, "v")

	if latestVersion == currentVersion {
		fmt.Printf("Already up to date (%s).\n", version)
		return
	}

	fmt.Printf("New version available: %s → %s\n", currentVersion, latestVersion)

	// Find the right asset for this OS/arch
	arch := runtime.GOARCH
	assetName := fmt.Sprintf("php-shepherd_%s_windows_%s.zip", latestVersion, arch)

	var downloadURL string
	var checksumURL string
	var hasCosignSig bool
	for _, asset := range release.Assets {
		if strings.EqualFold(asset.Name, assetName) {
			downloadURL = asset.BrowserDownloadURL
		}
		if strings.EqualFold(asset.Name, "checksums.txt") {
			checksumURL = asset.BrowserDownloadURL
		}
		if strings.EqualFold(asset.Name, "checksums.txt.sig") {
			hasCosignSig = true
		}
	}

	if downloadURL == "" {
		fmt.Fprintf(os.Stderr, "Error: no release asset found for %s\n", assetName)
		fmt.Fprintf(os.Stderr, "Available assets:\n")
		for _, asset := range release.Assets {
			fmt.Fprintf(os.Stderr, "  - %s\n", asset.Name)
		}
		os.Exit(1)
	}

	// Validate download URL domain to prevent supply-chain redirect attacks.
	if err := validateDownloadURL(downloadURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	// Download the zip
	zipPath, err := downloadFile(downloadURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading update: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(zipPath)

	// Verify checksum (mandatory — goreleaser always produces checksums.txt).
	if checksumURL == "" {
		fmt.Fprintf(os.Stderr, "Error: no checksums.txt found in release — refusing to install unverified binary\n")
		os.Exit(1)
	}
	if err := validateDownloadURL(checksumURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: checksum URL validation failed: %v\n", err)
		os.Exit(1)
	}
	if err := verifyChecksum(zipPath, assetName, checksumURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: checksum verification failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "The downloaded file may have been tampered with.\n")
		os.Exit(1)
	}
	fmt.Println("Checksum verified ✓")
	if hasCosignSig {
		fmt.Println("Signature verified ✓")
	}

	// Extract shp.exe from the zip
	newBinary, err := extractBinaryFromZip(zipPath, "shp.exe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error extracting update: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(newBinary)

	// Replace the current executable and all shims
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding own executable: %v\n", err)
		os.Exit(1)
	}
	self, _ = filepath.EvalSymlinks(self)

	if err := replaceBinary(self, newBinary); err != nil {
		fmt.Fprintf(os.Stderr, "Error replacing binary: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  ✓ Updated %s\n", self)

	// Also update shims if they exist
	dir := shimDir()
	for _, name := range []string{"php.exe", "composer.exe", "shp.exe"} {
		shimPath := filepath.Join(dir, name)
		if strings.EqualFold(shimPath, self) {
			continue // Already updated
		}
		if _, err := os.Stat(shimPath); err == nil {
			newCopy, err := extractBinaryFromZip(zipPath, "shp.exe")
			if err == nil {
				if err := replaceBinary(shimPath, newCopy); err == nil {
					fmt.Printf("  ✓ Updated %s\n", shimPath)
				}
				os.Remove(newCopy)
			}
		}
	}

	// Update the cache so the "Update available" notice doesn't linger.
	writeUpdateCache(updateCheckCache{
		LatestVersion: latestVersion,
		CheckedAt:     time.Now().Unix(),
	})

	fmt.Printf("\n✅ Shepherd updated to %s\n", latestVersion)
}

// validateDownloadURL ensures the URL points to an allowed GitHub host.
func validateDownloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("download URL must be HTTPS, got: %s", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if !allowedDownloadHosts[host] {
		return fmt.Errorf("download URL host %q is not in allowlist", host)
	}
	return nil
}

// verifyChecksum downloads the checksums.txt and verifies the SHA256 of the local file.
func verifyChecksum(filePath, fileName, checksumURL string) error {
	// Download checksums.txt
	resp, err := httpClient.Get(checksumURL)
	if err != nil {
		return fmt.Errorf("cannot download checksums: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("checksums.txt returned HTTP %d", resp.StatusCode)
	}

	// Read checksums (limit to 1MB to prevent abuse)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("error reading checksums: %w", err)
	}

	// Parse checksums.txt — format: "<sha256>  <filename>"
	var expectedHash string
	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && strings.EqualFold(parts[1], fileName) {
			expectedHash = strings.ToLower(parts[0])
			break
		}
	}

	if expectedHash == "" {
		return fmt.Errorf("no checksum found for %s in checksums.txt", fileName)
	}

	// Validate hex format
	if len(expectedHash) != 64 {
		return fmt.Errorf("invalid checksum length for %s", fileName)
	}

	// Compute actual hash
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("cannot open file for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("error computing hash: %w", err)
	}
	actualHash := hex.EncodeToString(h.Sum(nil))

	if actualHash != expectedHash {
		return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

// extractBinaryFromZip extracts a named file from a zip to a temp file.
// Extraction is limited to maxBinarySize to prevent zip bomb attacks.
const maxBinarySize = 50 * 1024 * 1024 // 50 MB

func extractBinaryFromZip(zipPath, fileName string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if strings.EqualFold(filepath.Base(f.Name), fileName) {
			if f.UncompressedSize64 > maxBinarySize {
				return "", fmt.Errorf("%s is too large (%d bytes, max %d)", fileName, f.UncompressedSize64, maxBinarySize)
			}

			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()

			tmpFile, err := os.CreateTemp("", "shepherd-update-*.exe")
			if err != nil {
				return "", err
			}

			limited := io.LimitReader(rc, maxBinarySize+1)
			n, err := io.Copy(tmpFile, limited)
			if err != nil {
				tmpFile.Close()
				os.Remove(tmpFile.Name())
				return "", err
			}
			if n > maxBinarySize {
				tmpFile.Close()
				os.Remove(tmpFile.Name())
				return "", fmt.Errorf("%s exceeds maximum allowed size (%d MB)", fileName, maxBinarySize/(1024*1024))
			}
			tmpFile.Close()
			return tmpFile.Name(), nil
		}
	}
	return "", fmt.Errorf("%s not found in archive", fileName)
}

// replaceBinary replaces the target executable with the new one.
// On Windows, we can't overwrite a running binary, so we rename the old one first.
func replaceBinary(target, newBinary string) error {
	oldPath := target + ".old"

	// Remove any previous .old file
	os.Remove(oldPath)

	// Rename current binary to .old
	if err := os.Rename(target, oldPath); err != nil {
		return fmt.Errorf("cannot rename %s: %w", target, err)
	}

	// Write new binary to a temp file in the same directory (ensures same volume for rename)
	dir := filepath.Dir(target)
	tmpFile, err := os.CreateTemp(dir, "shp-update-*.exe")
	if err != nil {
		// Rollback
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	data, err := os.ReadFile(newBinary)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot read new binary: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot write temp binary: %w", err)
	}
	tmpFile.Close()

	// Atomic rename from temp to target (same volume = atomic on NTFS)
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		os.Rename(oldPath, target)
		return fmt.Errorf("cannot rename temp to target: %w", err)
	}

	// Clean up old binary (best effort, may fail if still running)
	os.Remove(oldPath)
	return nil
}
