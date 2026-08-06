package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestSamePathEntry(t *testing.T) {
	tests := []struct {
		left, right string
		want        bool
	}{
		{`C:\Users\Ada\.config\shepherd\bin`, `C:\Users\Ada\.config\shepherd\bin`, true},
		{`C:\Users\Ada\.config\shepherd\bin\`, `C:\Users\Ada\.config\shepherd\bin`, true},
		{`C:\USERS\ADA\.CONFIG\SHEPHERD\BIN`, `C:\Users\Ada\.config\shepherd\bin`, true},
		{`C:\Users\Ada\.config\herd\bin`, `C:\Users\Ada\.config\shepherd\bin`, false},
		{`  C:\Tools\  `, `C:\Tools`, true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.left+"_vs_"+tt.right, func(t *testing.T) {
			if got := samePathEntry(tt.left, tt.right); got != tt.want {
				t.Errorf("samePathEntry(%q, %q) = %v, want %v", tt.left, tt.right, got, tt.want)
			}
		})
	}
}

func TestMovePathEntryFirst(t *testing.T) {
	entry := `C:\Users\Ada\.config\shepherd\bin`

	tests := []struct {
		name    string
		path    string
		want    string
		changed bool
	}{
		{
			name:    "prepends missing entry",
			path:    `C:\Users\Ada\.config\herd\bin;C:\Tools`,
			want:    entry + `;C:\Users\Ada\.config\herd\bin;C:\Tools`,
			changed: true,
		},
		{
			name:    "removes duplicates case-insensitive",
			path:    `C:\Users\Ada\.config\herd\bin;C:\Users\Ada\.config\shepherd\bin\;C:\Tools;C:\USERS\ADA\.CONFIG\SHEPHERD\BIN`,
			want:    entry + `;C:\Users\Ada\.config\herd\bin;C:\Tools`,
			changed: true,
		},
		{
			name:    "already first is unchanged",
			path:    entry + `;C:\Users\Ada\.config\herd\bin`,
			want:    entry + `;C:\Users\Ada\.config\herd\bin`,
			changed: false,
		},
		{
			name:    "removes empty entries",
			path:    `;C:\Tools;;`,
			want:    entry + `;C:\Tools`,
			changed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := movePathEntryFirst(tt.path, entry)
			if got != tt.want {
				t.Errorf("movePathEntryFirst() = %q, want %q", got, tt.want)
			}
			if changed != tt.changed {
				t.Errorf("changed = %v, want %v", changed, tt.changed)
			}
		})
	}
}

func TestRemovePathEntry(t *testing.T) {
	entry := `C:\Users\Ada\.config\shepherd\bin`

	t.Run("removes all occurrences", func(t *testing.T) {
		got, changed := removePathEntry(entry+`;C:\Tools;C:\USERS\ADA\.CONFIG\SHEPHERD\BIN\`, entry)
		if got != `C:\Tools` {
			t.Errorf("removePathEntry() = %q, want %q", got, `C:\Tools`)
		}
		if !changed {
			t.Error("expected changed")
		}
	})

	t.Run("no change when absent", func(t *testing.T) {
		got, changed := removePathEntry(`C:\Tools;C:\Other`, entry)
		if got != `C:\Tools;C:\Other` {
			t.Errorf("removePathEntry() = %q, want %q", got, `C:\Tools;C:\Other`)
		}
		if changed {
			t.Error("expected no change")
		}
	})
}

func TestExtractZipFile(t *testing.T) {
	// Create a temp zip with one file inside.
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	entry, _ := w.Create("hello.txt")
	_, _ = entry.Write([]byte("world"))
	_ = w.Close()
	_ = f.Close()

	// Open the zip and extract
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close() //nolint:errcheck // test cleanup

	dest := filepath.Join(dir, "hello.txt")
	if err := extractZipFile(r.File[0], dest); err != nil {
		t.Fatalf("extractZipFile() error = %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(data) != "world" {
		t.Errorf("got %q, want %q", string(data), "world")
	}
}

func TestValidateDownloadURL_AdditionalCases(t *testing.T) {
	// Supplement the existing TestValidateDownloadURL with edge cases to boost coverage.
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid github releases", "https://github.com/shaffe-fr/php-shepherd/releases/download/v1.0.0/shp.zip", false},
		{"valid objects.githubusercontent.com", "https://objects.githubusercontent.com/some/path/file.zip", false},
		{"github.com subdomain rejected", "https://evil.github.com/file.zip", true},
		{"empty path still valid host", "https://github.com/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDownloadURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDownloadURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}
