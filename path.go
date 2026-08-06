package main

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// shimDir returns the directory where php.exe and composer.exe shims are installed.
func shimDir() string {
	return filepath.Join(os.Getenv("USERPROFILE"), ".config", "shepherd", "bin")
}

// getUserPath reads the User PATH from the registry.
func getUserPath() (string, uint32, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return "", 0, err
	}
	defer key.Close() //nolint:errcheck // registry close is best-effort
	val, valType, err := key.GetStringValue("Path")
	if err != nil {
		return "", 0, err
	}
	return val, valType, nil
}

// setUserPath writes the User PATH to the registry preserving the original type (REG_EXPAND_SZ or REG_SZ).
func setUserPath(path string, valType uint32) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close() //nolint:errcheck // registry close is best-effort

	// Preserve the registry value type (REG_EXPAND_SZ if it was already, for %USERPROFILE% etc.)
	if valType == registry.EXPAND_SZ {
		return key.SetExpandStringValue("Path", path)
	}
	return key.SetStringValue("Path", path)
}

// samePathEntry compares PATH entries without case, surrounding whitespace,
// or a trailing directory separator. Windows path comparisons are case-insensitive.
func samePathEntry(left, right string) bool {
	clean := func(entry string) string {
		return strings.TrimRight(strings.TrimSpace(entry), `\/`)
	}
	return strings.EqualFold(clean(left), clean(right))
}

// movePathEntryFirst returns PATH with entry as its sole first occurrence.
// Empty entries are removed because they are ambiguous in a Windows PATH.
func movePathEntryFirst(path, entry string) (string, bool) {
	filtered := make([]string, 0)
	for _, current := range strings.Split(path, ";") {
		if strings.TrimSpace(current) == "" || samePathEntry(current, entry) {
			continue
		}
		filtered = append(filtered, current)
	}

	newPath := strings.Join(append([]string{entry}, filtered...), ";")
	return newPath, newPath != path
}

// removePathEntry removes all occurrences of entry from PATH.
func removePathEntry(path, entry string) (string, bool) {
	filtered := make([]string, 0)
	for _, current := range strings.Split(path, ";") {
		if strings.TrimSpace(current) == "" || samePathEntry(current, entry) {
			continue
		}
		filtered = append(filtered, current)
	}

	newPath := strings.Join(filtered, ";")
	return newPath, newPath != path
}

// ensureShepherdFirstInUserPath makes the Shepherd shim directory the first
// User PATH entry. It only writes the registry when a change is required.
func ensureShepherdFirstInUserPath() (changed bool, err error) {
	userPath, valType, err := getUserPath()
	if err != nil {
		return false, err
	}

	newPath, changed := movePathEntryFirst(userPath, shimDir())
	if !changed {
		return false, nil
	}
	if err := setUserPath(newPath, valType); err != nil {
		return false, err
	}
	broadcastSettingChange()
	return true, nil
}

// removeShepherdFromUserPath removes the Shepherd shim directory from User PATH.
func removeShepherdFromUserPath() (changed bool, err error) {
	userPath, valType, err := getUserPath()
	if err != nil {
		return false, err
	}

	newPath, changed := removePathEntry(userPath, shimDir())
	if !changed {
		return false, nil
	}
	if err := setUserPath(newPath, valType); err != nil {
		return false, err
	}
	broadcastSettingChange()
	return true, nil
}

// broadcastSettingChange sends WM_SETTINGCHANGE to all top-level windows
// so that other processes pick up the updated PATH immediately.
//
// Uses windows.NewLazySystemDLL which restricts DLL loading to the System32
// directory (unlike syscall.NewLazyDLL which searches the full DLL search path),
// reducing both security risk and AV heuristic noise.
func broadcastSettingChange() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")
	env, _ := windows.UTF16PtrFromString("Environment")
	// HWND_BROADCAST=0xFFFF, WM_SETTINGCHANGE=0x001A, SMTO_ABORTIFHUNG=0x0002, timeout=5000ms
	//nolint:errcheck // syscall errno is meaningless here
	sendMessageTimeout.Call(0xFFFF, 0x001A, 0,
		uintptr(unsafe.Pointer(env)), 0x0002, 5000, 0)
}
