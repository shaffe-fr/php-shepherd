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
func writeIni(path string, lines []string) {
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", path, err)
		os.Exit(1)
	}
}

// addExtensionToIni adds extension= or zend_extension= to php.ini if not already present.
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

	// Check if already present (active or commented)
	checkRe := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(directive) + `\s*=\s*` + regexp.QuoteMeta(extName))
	if checkRe.MatchString(content) {
		fmt.Printf("  %s=%s already in php.ini\n", directive, extName)
		return nil
	}

	// Append
	content += "\n" + directive + "=" + extName + "\n"
	if err := os.WriteFile(iniPath, []byte(content), 0644); err != nil {
		return err
	}
	fmt.Printf("  ✅ Added %s=%s to php.ini\n", directive, extName)
	return nil
}
