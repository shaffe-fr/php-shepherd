package main

import (
	"os"
	"path/filepath"
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
	defer key.Close()
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
	defer key.Close()

	// Preserve the registry value type (REG_EXPAND_SZ if it was already, for %USERPROFILE% etc.)
	if valType == registry.EXPAND_SZ {
		return key.SetExpandStringValue("Path", path)
	}
	return key.SetStringValue("Path", path)
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
	sendMessageTimeout.Call(0xFFFF, 0x001A, 0,
		uintptr(unsafe.Pointer(env)), 0x0002, 5000, 0)
}
