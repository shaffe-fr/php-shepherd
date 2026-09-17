package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// zendExtensions lists extensions that use zend_extension= instead of extension=.
var zendExtensions = map[string]bool{
	"xdebug":  true,
	"opcache": true,
}

// phpIniPath returns the path to the php.ini for the given php.exe directory.
func phpIniPath(phpDir string) string {
	return filepath.Join(phpDir, "php.ini")
}

// iniKeyRe matches an active (uncommented) assignment of key. The anchor and
// the trailing "=" keep "pcov.enabled" from matching "pcov.enabled_extra", and
// a commented line is deliberately not a match so it stays as documentation.
func iniKeyRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `\s*=`)
}

// hasActiveIniKey reports whether key is assigned by an uncommented line.
func hasActiveIniKey(lines []string, key string) bool {
	_, found := iniValue(lines, key)
	return found
}

// iniValue returns the value of the last uncommented assignment of key. The last
// one wins, matching how PHP itself reads a php.ini.
func iniValue(lines []string, key string) (value string, found bool) {
	re := iniKeyRe(key)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !re.MatchString(trimmed) {
			continue
		}
		_, v, _ := strings.Cut(trimmed, "=")
		value = strings.TrimSpace(v)
		found = true
	}
	return value, found
}

// iniTruthy interprets a php.ini boolean the way PHP does.
func iniTruthy(value string) bool {
	switch strings.ToLower(strings.Trim(value, `"'`)) {
	case "", "0", "off", "false", "no", "none":
		return false
	default:
		return true
	}
}

// ensureIniValue ensures a key=value line exists after the given anchor index.
// If it already exists, updates the value. Otherwise inserts after anchor.
func ensureIniValue(lines []string, anchorIdx int, key, value string) []string {
	re := iniKeyRe(key)
	for i, line := range lines {
		if re.MatchString(strings.TrimSpace(line)) {
			lines[i] = key + "=" + value
			return lines
		}
	}
	// Not found — insert after anchor
	insert := key + "=" + value
	after := anchorIdx + 1
	return slices.Insert(lines, after, insert)
}

// applyIniDefaults writes an extension's default directives below its own
// extension= line. A key already assigned is left alone: these are defaults for
// a fresh install, not values Shepherd keeps ownership of.
func applyIniDefaults(iniPath, extName string, defaults [][2]string) error {
	if len(defaults) == 0 {
		return nil
	}

	data, err := os.ReadFile(iniPath)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", iniPath, err)
	}
	lines := strings.Split(string(data), "\n")

	var missing []string
	for _, kv := range defaults {
		if hasActiveIniKey(lines, kv[0]) {
			continue
		}
		missing = append(missing, kv[0]+"="+kv[1])
	}
	if len(missing) == 0 {
		return nil
	}

	anchorRe := regexp.MustCompile(`(?i)^(zend_)?extension\s*=\s*` + regexp.QuoteMeta(extName) + `\s*$`)
	anchorIdx := -1
	for i, line := range lines {
		if anchorRe.MatchString(strings.TrimSpace(line)) {
			anchorIdx = i
			break
		}
	}

	if anchorIdx == -1 {
		lines = append(lines, missing...)
	} else {
		lines = slices.Insert(lines, anchorIdx+1, missing...)
	}

	if err := writeIni(iniPath, lines); err != nil {
		return err
	}
	for _, entry := range missing {
		fmt.Printf("  ✅ Added %s to php.ini\n", entry)
	}
	return nil
}

// writeIni writes lines back to the php.ini file.
func writeIni(path string, lines []string) error {
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// addExtensionToIni adds extension= or zend_extension= to php.ini if not already present.
// It inserts the directive at the end of the file but before any trailing section headers,
// so it won't accidentally end up under an unrelated [Section].
func addExtensionToIni(iniPath, extName string) error {
	data, err := os.ReadFile(iniPath)
	if err != nil {
		return fmt.Errorf("php.ini not found at %s", iniPath)
	}

	content := string(data)
	directive := "extension"
	if def, ok := extensionRegistry[extName]; ok && def.directive != "" {
		directive = def.directive
	} else if zendExtensions[extName] {
		directive = "zend_extension"
	}

	// Check if already present (active or commented — handle leading spaces before semicolons)
	checkRe := regexp.MustCompile(`(?m)^\s*;?\s*` + regexp.QuoteMeta(directive) + `\s*=\s*` + regexp.QuoteMeta(extName))
	if checkRe.MatchString(content) {
		fmt.Printf("  %s=%s already in php.ini\n", directive, extName)
		return nil
	}

	// Find the last non-section line to insert before any trailing [Section] blocks.
	// This prevents accidentally attaching extensions under [Swoole], [curl], etc.
	lines := strings.Split(content, "\n")
	sectionRe := regexp.MustCompile(`^\s*\[.+\]`)
	insertIdx := len(lines) // default: end of file

	// Walk backwards to find where trailing sections start
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue // skip blank lines
		}
		if sectionRe.MatchString(trimmed) {
			insertIdx = i
		} else {
			break
		}
	}

	// Insert the directive
	newLine := directive + "=" + extName
	// Build new content: lines before insertIdx + our directive + lines from insertIdx onward
	before := lines[:insertIdx]
	after := lines[insertIdx:]
	result := append(before, "")
	result = append(result, newLine)
	result = append(result, after...)

	if err := os.WriteFile(iniPath, []byte(strings.Join(result, "\n")), 0644); err != nil {
		return err
	}
	fmt.Printf("  ✅ Added %s=%s to php.ini\n", directive, extName)
	return nil
}
