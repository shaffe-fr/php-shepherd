package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// checkComposerPHPConstraint checks if the resolved .phpversion conflicts with
// the require.php constraint in composer.json. Emits a warning on stderr if
// there is a clear mismatch. Non-blocking, best-effort.
func checkComposerPHPConstraint(projectDir, phpVersion string) {
	composerPath := filepath.Join(projectDir, "composer.json")
	data, err := os.ReadFile(composerPath)
	if err != nil {
		return // No composer.json, nothing to check
	}

	var composer struct {
		Require map[string]string `json:"require"`
	}
	if err := json.Unmarshal(data, &composer); err != nil {
		return
	}

	constraint, ok := composer.Require["php"]
	if !ok || constraint == "" {
		return
	}

	if !phpVersionSatisfies(phpVersion, constraint) {
		fmt.Fprintf(os.Stderr, "Warning: composer.json requires php %q but .phpversion specifies %s\n", constraint, phpVersion)
	}
}

// phpVersionSatisfies checks if a version like "8.2" satisfies a Composer
// constraint. Supports common patterns: ^X.Y, ~X.Y, >=X.Y, >=X.Y <X.Z,
// and exact X.Y. This is best-effort — complex constraints (||, multiple
// ranges with spaces) are skipped to avoid false positives.
func phpVersionSatisfies(version, constraint string) bool {
	major, minor, ok := parseVersion(version)
	if !ok {
		return true // Can't parse, don't warn
	}

	// Handle OR constraints (||) — satisfy if any branch matches
	if strings.Contains(constraint, "||") {
		parts := strings.Split(constraint, "||")
		for _, part := range parts {
			if phpVersionSatisfies(version, strings.TrimSpace(part)) {
				return true
			}
		}
		return false
	}

	// Handle space-separated AND constraints (e.g. ">=8.1 <8.5")
	constraint = strings.TrimSpace(constraint)
	if strings.Contains(constraint, " ") {
		parts := strings.Fields(constraint)
		for _, part := range parts {
			if !phpVersionSatisfies(version, part) {
				return false
			}
		}
		return true
	}

	// Single constraint
	constraint = strings.TrimSpace(constraint)

	switch {
	case strings.HasPrefix(constraint, "^"):
		// ^X.Y means >=X.Y, <(X+1).0
		cMajor, cMinor, ok := parseVersion(strings.TrimPrefix(constraint, "^"))
		if !ok {
			return true
		}
		if major != cMajor {
			return false
		}
		return minor >= cMinor

	case strings.HasPrefix(constraint, "~"):
		// ~X.Y means >=X.Y, <X.(Y+1) — but for PHP major.minor, treat like ^
		cMajor, cMinor, ok := parseVersion(strings.TrimPrefix(constraint, "~"))
		if !ok {
			return true
		}
		if major != cMajor {
			return false
		}
		return minor >= cMinor

	case strings.HasPrefix(constraint, ">="):
		cMajor, cMinor, ok := parseVersion(strings.TrimPrefix(constraint, ">="))
		if !ok {
			return true
		}
		return major > cMajor || (major == cMajor && minor >= cMinor)

	case strings.HasPrefix(constraint, ">"):
		cMajor, cMinor, ok := parseVersion(strings.TrimPrefix(constraint, ">"))
		if !ok {
			return true
		}
		return major > cMajor || (major == cMajor && minor > cMinor)

	case strings.HasPrefix(constraint, "<="):
		cMajor, cMinor, ok := parseVersion(strings.TrimPrefix(constraint, "<="))
		if !ok {
			return true
		}
		return major < cMajor || (major == cMajor && minor <= cMinor)

	case strings.HasPrefix(constraint, "<"):
		cMajor, cMinor, ok := parseVersion(strings.TrimPrefix(constraint, "<"))
		if !ok {
			return true
		}
		return major < cMajor || (major == cMajor && minor < cMinor)

	case strings.HasPrefix(constraint, "!="):
		cMajor, cMinor, ok := parseVersion(strings.TrimPrefix(constraint, "!="))
		if !ok {
			return true
		}
		return major != cMajor || minor != cMinor

	default:
		// Exact version or wildcard — skip complex cases
		cMajor, cMinor, ok := parseVersion(constraint)
		if !ok {
			return true // Can't parse, assume OK
		}
		return major == cMajor && minor == cMinor
	}
}

// parseVersion extracts major.minor from a version string like "8.4", "8.4.0", "8.4.*".
func parseVersion(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	// Strip patch version or wildcard (8.4.0, 8.4.*)
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// resolveAutoVersion reads composer.json in the given directory, extracts the
// require.php constraint, and returns the highest installed PHP version that
// satisfies it. Returns empty string if no constraint is found or no version matches.
func resolveAutoVersion(dir string) string {
	versions := installedPHPVersions()
	if len(versions) == 0 {
		return ""
	}

	composerPath := filepath.Join(dir, "composer.json")
	data, err := os.ReadFile(composerPath)
	if err != nil {
		return ""
	}

	var composer struct {
		Require map[string]string `json:"require"`
	}
	if err := json.Unmarshal(data, &composer); err != nil {
		return ""
	}

	constraint, ok := composer.Require["php"]
	if !ok || constraint == "" {
		return ""
	}

	// Find the highest installed version that satisfies the constraint
	best := ""
	for _, ver := range versions {
		if phpVersionSatisfies(ver, constraint) {
			best = ver
		}
	}
	return best
}
