package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPhpDirVersion(t *testing.T) {
	tests := []struct {
		dir  string
		want int
	}{
		{"php84", 8004},
		{"php83", 8003},
		{"php810", 8010},
		{"php74", 7004},
		{"php56", 5006},
		// Edge cases
		{"php", -1},
		{"php8", -1},
		{"notphp84", -1},
		{"PHP84", -1}, // case-sensitive regex
		{"", -1},
		{"phpx4", -1},
	}

	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			got := phpDirVersion(tt.dir)
			if got != tt.want {
				t.Errorf("phpDirVersion(%q) = %d, want %d", tt.dir, got, tt.want)
			}
		})
	}
}

func TestPhpDirVersionSorting(t *testing.T) {
	// Verify that php810 (8.10) sorts higher than php84 (8.4)
	v810 := phpDirVersion("php810")
	v84 := phpDirVersion("php84")
	if v810 <= v84 {
		t.Errorf("php810 (%d) should sort higher than php84 (%d)", v810, v84)
	}
}

func TestExtractVersion(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{filepath.Join("C:", "Users", ".config", "herd", "bin", "php84", "php.exe"), "8.4"},
		{filepath.Join("C:", "Users", ".config", "herd", "bin", "php810", "php.exe"), "8.10"},
		{filepath.Join("C:", "Users", ".config", "herd", "bin", "php74", "php.exe"), "7.4"},
		// Non-matching paths
		{filepath.Join("C:", "some", "other", "path", "php.exe"), ""},
		{filepath.Join("C:", "bin", "php", "php.exe"), ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractVersion(tt.path)
			if got != tt.want {
				t.Errorf("extractVersion(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestRewriteXdebugArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		version string
		want    []string
	}{
		{
			name:    "strips -n flag",
			args:    []string{"-n", "-r", "echo 1;"},
			version: "8.4",
			want:    []string{"-r", "echo 1;"},
		},
		{
			name:    "strips -n with empty version",
			args:    []string{"-n", "test.php"},
			version: "",
			want:    []string{"test.php"},
		},
		{
			name:    "no xdebug args unchanged",
			args:    []string{"-d", "memory_limit=256M", "artisan", "serve"},
			version: "8.4",
			want:    []string{"-d", "memory_limit=256M", "artisan", "serve"},
		},
		{
			name:    "nil args returns nil",
			args:    nil,
			version: "8.4",
			want:    nil,
		},
		{
			name:    "empty args returns nil",
			args:    []string{},
			version: "8.4",
			want:    nil,
		},
		{
			name:    "only -n returns nil",
			args:    []string{"-n"},
			version: "8.4",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteXdebugArgs(tt.args, tt.version)
			if !sliceEqual(got, tt.want) {
				t.Errorf("rewriteXdebugArgs(%v, %q) = %v, want %v", tt.args, tt.version, got, tt.want)
			}
		})
	}
}

func TestFindPHPVersion(t *testing.T) {
	// Create a temp directory structure with a .phpversion file
	root := t.TempDir()
	sub := filepath.Join(root, "project", "src")
	os.MkdirAll(sub, 0755)

	// Write .phpversion at project level
	os.WriteFile(filepath.Join(root, "project", ".phpversion"), []byte("8.4\n"), 0644)

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{
			name: "finds in current dir",
			dir:  filepath.Join(root, "project"),
			want: "8.4",
		},
		{
			name: "walks up to find it",
			dir:  sub,
			want: "8.4",
		},
		{
			name: "not found returns empty",
			dir:  root,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findPHPVersion(tt.dir)
			if got != tt.want {
				t.Errorf("findPHPVersion(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

func TestFindPHPVersionInvalidContent(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"valid version", "8.4\n", "8.4"},
		{"valid with spaces", "  8.10  \n", "8.10"},
		{"invalid no dot", "84\n", ""},
		{"invalid text", "latest\n", ""},
		{"empty file", "", ""},
		{"triple version", "8.4.1\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.WriteFile(filepath.Join(dir, ".phpversion"), []byte(tt.content), 0644)
			got := findPHPVersion(dir)
			if got != tt.want {
				t.Errorf("findPHPVersion with content %q = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestEnsureIniValue(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		anchorIdx int
		key       string
		value     string
		want      []string
	}{
		{
			name:      "updates existing value",
			lines:     []string{"zend_extension=xdebug.dll", "xdebug.mode=coverage", "other=line"},
			anchorIdx: 0,
			key:       "xdebug.mode",
			value:     "debug",
			want:      []string{"zend_extension=xdebug.dll", "xdebug.mode=debug", "other=line"},
		},
		{
			name:      "inserts after anchor when missing",
			lines:     []string{"zend_extension=xdebug.dll", "other=line"},
			anchorIdx: 0,
			key:       "xdebug.mode",
			value:     "debug",
			want:      []string{"zend_extension=xdebug.dll", "xdebug.mode=debug", "other=line"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureIniValue(tt.lines, tt.anchorIdx, tt.key, tt.value)
			if !sliceEqual(got, tt.want) {
				t.Errorf("ensureIniValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildExtURL(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		version    string
		phpVersion string
		ts         string
		vs         string
		arch       string
		want       string
	}{
		{
			name:       "redis extension URL",
			pattern:    "https://downloads.php.net/~windows/pecl/releases/redis/{version}/php_redis-{version}-{phpMajMin}-{ts}-{vs}-{arch}.zip",
			version:    "6.0.2",
			phpVersion: "8.4",
			ts:         "nts",
			vs:         "vs17",
			arch:       "x64",
			want:       "https://downloads.php.net/~windows/pecl/releases/redis/6.0.2/php_redis-6.0.2-84-nts-vs17-x64.zip",
		},
		{
			name:       "PHP 8.10 uses three-digit phpMajMin",
			pattern:    "https://example.com/{phpMajMin}/{version}.zip",
			version:    "1.0.0",
			phpVersion: "8.10",
			ts:         "nts",
			vs:         "vs17",
			arch:       "x64",
			want:       "https://example.com/810/1.0.0.zip",
		},
		{
			name:       "phpDotVer placeholder keeps the dot",
			pattern:    "https://example.com/{version}/php_memcached-{version}-{phpDotVer}-{ts}-{vs}-{arch}.zip",
			version:    "3.4.0",
			phpVersion: "8.4",
			ts:         "nts",
			vs:         "vs17",
			arch:       "x64",
			want:       "https://example.com/3.4.0/php_memcached-3.4.0-8.4-nts-vs17-x64.zip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildExtURL(tt.pattern, tt.version, tt.phpVersion, tt.ts, tt.vs, tt.arch)
			if got != tt.want {
				t.Errorf("buildExtURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUpdateNginxConf(t *testing.T) {
	tests := []struct {
		name    string
		content string
		version string
		want    string
		changed bool
	}{
		{
			name:    "updates ISOLATED_PHP_VERSION and sock",
			content: "# ISOLATED_PHP_VERSION=8.3\nfastcgi_pass \"$herd_sock_83\";\n",
			version: "8.4",
			want:    "# ISOLATED_PHP_VERSION=8.4\nfastcgi_pass \"$herd_sock_84\";\n",
			changed: true,
		},
		{
			name:    "already correct version is not modified",
			content: "# ISOLATED_PHP_VERSION=8.4\nfastcgi_pass \"$herd_sock_84\";\n",
			version: "8.4",
			want:    "# ISOLATED_PHP_VERSION=8.4\nfastcgi_pass \"$herd_sock_84\";\n",
			changed: false,
		},
		{
			name:    "repairs empty fastcgi_pass",
			content: "# ISOLATED_PHP_VERSION=8.4\nfastcgi_pass ;\n",
			version: "8.4",
			want:    "# ISOLATED_PHP_VERSION=8.4\nfastcgi_pass \"$herd_sock_84\";\n",
			changed: true,
		},
		{
			name:    "empty content is not modified",
			content: "   \n  \n",
			version: "8.4",
			want:    "   \n  \n",
			changed: false,
		},
		{
			name:    "handles sock without version suffix",
			content: "# ISOLATED_PHP_VERSION=8.3\nfastcgi_pass \"$herd_sock\";\n",
			version: "8.4",
			want:    "# ISOLATED_PHP_VERSION=8.4\nfastcgi_pass \"$herd_sock_84\";\n",
			changed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write content to a temp file
			dir := t.TempDir()
			confPath := filepath.Join(dir, "test.conf")
			os.WriteFile(confPath, []byte(tt.content), 0644)

			got := updateNginxConf(confPath, tt.version)
			if got != tt.changed {
				t.Errorf("updateNginxConf() returned %v, want %v", got, tt.changed)
			}

			// Read back and compare
			data, _ := os.ReadFile(confPath)
			if string(data) != tt.want {
				t.Errorf("updateNginxConf() content:\n  got:  %q\n  want: %q", string(data), tt.want)
			}
		})
	}
}

func TestListSupportedExtensions(t *testing.T) {
	exts := listSupportedExtensions()

	if len(exts) == 0 {
		t.Fatal("listSupportedExtensions() returned empty list")
	}

	// Verify sorted
	for i := 1; i < len(exts); i++ {
		if exts[i] < exts[i-1] {
			t.Errorf("listSupportedExtensions() not sorted: %q comes after %q", exts[i], exts[i-1])
		}
	}

	// Verify known extensions are present
	known := []string{"redis", "imagick", "igbinary"}
	for _, k := range known {
		found := false
		for _, e := range exts {
			if e == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in listSupportedExtensions(), got %v", k, exts)
		}
	}
}

func TestNilIfEmpty(t *testing.T) {
	tests := []struct {
		input string
		want  interface{}
	}{
		{"", nil},
		{"hello", "hello"},
		{"8.4", "8.4"},
		{" ", " "},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := nilIfEmpty(tt.input)
			if got != tt.want {
				t.Errorf("nilIfEmpty(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateDownloadURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid github.com HTTPS",
			url:     "https://github.com/shaffe-fr/php-shepherd/releases/download/v1.0.0/shp.zip",
			wantErr: false,
		},
		{
			name:    "valid objects.githubusercontent.com",
			url:     "https://objects.githubusercontent.com/some/path/file.zip",
			wantErr: false,
		},
		{
			name:    "rejected HTTP scheme",
			url:     "http://github.com/some/file.zip",
			wantErr: true,
		},
		{
			name:    "rejected unknown host",
			url:     "https://evil.com/malware.zip",
			wantErr: true,
		},
		{
			name:    "rejected FTP scheme",
			url:     "ftp://github.com/file.zip",
			wantErr: true,
		},
		{
			name:    "invalid URL",
			url:     "://not-a-url",
			wantErr: true,
		},
		{
			name:    "empty string",
			url:     "",
			wantErr: true,
		},
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

// sliceEqual compares two string slices, treating nil and empty as equivalent.
func sliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
