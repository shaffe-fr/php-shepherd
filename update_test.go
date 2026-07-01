package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyChecksum_Valid(t *testing.T) {
	// Create a temporary file with known content
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.zip")
	content := []byte("hello shepherd")
	_ = os.WriteFile(filePath, content, 0644)

	// Compute expected SHA256
	h := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(h[:])

	// Serve checksums.txt
	checksumBody := fmt.Sprintf("%s  test.zip\n%s  other.zip\n", expectedHash, "0000000000000000000000000000000000000000000000000000000000000000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumBody))
	}))
	defer srv.Close()

	err := verifyChecksum(filePath, "test.zip", srv.URL+"/checksums.txt")
	if err != nil {
		t.Fatalf("expected valid checksum, got error: %v", err)
	}
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.zip")
	_ = os.WriteFile(filePath, []byte("actual content"), 0644)

	// Serve a checksum that doesn't match
	fakeHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	checksumBody := fmt.Sprintf("%s  test.zip\n", fakeHash)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumBody))
	}))
	defer srv.Close()

	err := verifyChecksum(filePath, "test.zip", srv.URL+"/checksums.txt")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if got := err.Error(); !contains(got, "mismatch") {
		t.Errorf("error should mention 'mismatch', got: %s", got)
	}
}

func TestVerifyChecksum_FileNotInChecksums(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.zip")
	_ = os.WriteFile(filePath, []byte("content"), 0644)

	checksumBody := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  other.zip\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumBody))
	}))
	defer srv.Close()

	err := verifyChecksum(filePath, "test.zip", srv.URL+"/checksums.txt")
	if err == nil {
		t.Fatal("expected error when file not found in checksums")
	}
	if got := err.Error(); !contains(got, "no checksum found") {
		t.Errorf("error should mention 'no checksum found', got: %s", got)
	}
}

func TestVerifyChecksum_ServerError(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.zip")
	_ = os.WriteFile(filePath, []byte("content"), 0644)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	err := verifyChecksum(filePath, "test.zip", srv.URL+"/checksums.txt")
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestVerifyChecksum_CaseInsensitiveFilename(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "Test.zip")
	content := []byte("case test")
	_ = os.WriteFile(filePath, content, 0644)

	h := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(h[:])

	// checksums.txt has lowercase filename
	checksumBody := fmt.Sprintf("%s  test.zip\n", expectedHash)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumBody))
	}))
	defer srv.Close()

	// Should match case-insensitively
	err := verifyChecksum(filePath, "Test.zip", srv.URL+"/checksums.txt")
	if err != nil {
		t.Fatalf("expected case-insensitive filename match, got: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDetectPeclVersion_Valid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a PECL package page with version links
		html := `<html><body>
			<a href="/package/redis/6.1.0">6.1.0</a>
			<a href="/package/redis/6.0.2">6.0.2</a>
		</body></html>`
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	// Override httpClient to use test server
	origClient := httpClient
	httpClient = srv.Client()
	defer func() { httpClient = origClient }()

	// detectPeclVersion hits pecl.php.net — we need to redirect it to our test server.
	// Since the function builds its own URL, we need a different approach:
	// call the server directly and test the parsing logic separately.
	// Instead, let's test via the actual function with a custom transport.

	// Use a RoundTripper that rewrites the host
	httpClient = &http.Client{
		Transport: &rewriteTransport{target: srv.URL},
	}

	ver, err := detectPeclVersion("redis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "6.1.0" {
		t.Errorf("expected '6.1.0', got %q", ver)
	}
}

func TestDetectPeclVersion_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	origClient := httpClient
	httpClient = &http.Client{
		Transport: &rewriteTransport{target: srv.URL},
	}
	defer func() { httpClient = origClient }()

	_, err := detectPeclVersion("nonexistent")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestDetectPeclVersion_NoVersionInPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>no version links here</body></html>"))
	}))
	defer srv.Close()

	origClient := httpClient
	httpClient = &http.Client{
		Transport: &rewriteTransport{target: srv.URL},
	}
	defer func() { httpClient = origClient }()

	_, err := detectPeclVersion("redis")
	if err == nil {
		t.Fatal("expected error when no version found in page")
	}
}

func TestDetectPeclVersion_InvalidName(t *testing.T) {
	_, err := detectPeclVersion("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for invalid extension name")
	}
}

// rewriteTransport rewrites all HTTP requests to point to the test server.
type rewriteTransport struct {
	target string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	// Extract host:port from target
	targetURL := rt.target
	if len(targetURL) > 7 {
		req.URL.Host = targetURL[7:] // strip "http://"
	}
	return http.DefaultTransport.RoundTrip(req)
}
