package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
)

// downloadFile downloads a URL to a temp file and returns the path.
// Downloads are limited to maxDownloadSize bytes to prevent resource exhaustion.
const maxDownloadSize = 100 * 1024 * 1024 // 100 MB

func downloadFile(rawURL string) (string, error) {
	// Validate URL scheme — only HTTPS is allowed.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("refusing non-HTTPS URL: %s", rawURL)
	}

	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", rawURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close on HTTP body

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
	}

	// Verify final URL after redirects is still HTTPS.
	finalURL := resp.Request.URL
	if finalURL.Scheme != "https" {
		return "", fmt.Errorf("redirect led to non-HTTPS URL: %s", finalURL.String())
	}

	tmpFile, err := os.CreateTemp("", "shepherd-ext-*.zip")
	if err != nil {
		return "", fmt.Errorf("creating temp file for download: %w", err)
	}

	// Limit download size to prevent disk exhaustion.
	limited := io.LimitReader(resp.Body, maxDownloadSize+1)
	n, err := io.Copy(tmpFile, limited)
	_ = tmpFile.Close()
	if err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("writing download to disk: %w", err)
	}
	if n > maxDownloadSize {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("download exceeds maximum size (%d MB)", maxDownloadSize/(1024*1024))
	}
	return tmpFile.Name(), nil
}
