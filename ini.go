package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// ensureIniValue ensures a key=value line exists after the given anchor index.
// If it already exists, updates the value. Otherwise inserts after anchor.
func ensureIniValue(lines []string, anchorIdx int, key, value string) []string {
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				lines[i] = key + "=" + value
				return lines
			}
		}
	}
	// Not found — insert after anchor
	insert := key + "=" + value
	after := anchorIdx + 1
	lines = append(lines, "")
	copy(lines[after+1:], lines[after:])
	lines[after] = insert
	return lines
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
