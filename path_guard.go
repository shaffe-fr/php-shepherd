package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	pathGuardTaskNamePrefix   = "Shepherd PATH Guard"
	pathGuardInternalCommand  = "__path-guard"
	pathGuardMutexName        = `Local\ShepherdPathGuard`
	pathGuardStopEventName    = `Local\ShepherdPathGuardStop`
	pathGuardRunningEventName = `Local\ShepherdPathGuardRunning`
	pathGuardRetryDelay       = time.Second
	pathGuardStopTimeout      = 5 * time.Second
)

// pathGuardTaskName identifies this Windows user's task. The SID prevents a
// second local user from replacing a task that belongs to another account.
func pathGuardTaskName() string {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tokenUser == nil || tokenUser.User.Sid == nil {
		return pathGuardTaskNamePrefix
	}
	sid := tokenUser.User.Sid.String()
	if sid == "" {
		return pathGuardTaskNamePrefix
	}
	return fmt.Sprintf("%s (%s)", pathGuardTaskNamePrefix, sid)
}

// runScheduledTask executes schtasks.exe from System32. It is replaceable in
// tests so task registration can be verified without touching the host system.
var runScheduledTask = func(args ...string) error {
	systemRoot := os.Getenv("SystemRoot")
	schtasks := "schtasks.exe"
	if systemRoot != "" {
		schtasks = filepath.Join(systemRoot, "System32", "schtasks.exe")
	}

	cmd := exec.Command(schtasks, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(output)); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

// pathGuardExecutable returns the dedicated shim used by the scheduled task.
// Keeping the long-running guard separate prevents it from locking shp.exe while
// Shepherd updates the normal command shims.
func pathGuardExecutable() string {
	return filepath.Join(shimDir(), "shp-guard.exe")
}

// ensurePathGuardExecutable creates the dedicated guard shim on first opt-in.
// It intentionally leaves an existing guard binary untouched: that process may
// be running, while shp.exe must remain replaceable by self-update.
func ensurePathGuardExecutable() (string, error) {
	source := filepath.Join(shimDir(), "shp.exe")
	if _, err := os.Stat(source); err != nil {
		return "", fmt.Errorf("shepherd shim not found at %s: %w", source, err)
	}

	destination := pathGuardExecutable()
	if _, err := os.Stat(destination); err == nil {
		return destination, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("could not inspect PATH Guard shim at %s: %w", destination, err)
	}

	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("could not read Shepherd shim: %w", err)
	}
	if err := os.WriteFile(destination, data, 0755); err != nil {
		return "", fmt.Errorf("could not create PATH Guard shim: %w", err)
	}
	return destination, nil
}

// refreshPathGuardExecutable updates the dedicated shim after Shepherd itself
// is updated. The guard is stopped first so Windows releases the executable.
func refreshPathGuardExecutable() error {
	if !pathGuardEnabled() {
		return nil
	}
	if err := stopPathGuard(); err != nil {
		return err
	}

	source := filepath.Join(shimDir(), "shp.exe")
	if err := replaceBinary(pathGuardExecutable(), source); err != nil {
		return fmt.Errorf("could not refresh PATH Guard shim: %w", err)
	}
	if err := resetPathGuardStopSignal(); err != nil {
		return err
	}
	if err := runScheduledTask("/Run", "/TN", pathGuardTaskName()); err != nil {
		return fmt.Errorf("PATH Guard was refreshed but could not be restarted: %w", err)
	}
	return nil
}

// pathGuardTaskAction returns the command line stored in the scheduled task.
func pathGuardTaskAction(executable string) string {
	return `"` + executable + `" ` + pathGuardInternalCommand
}

func pathGuardTaskCreateArgs(executable string) []string {
	return []string{
		"/Create",
		"/TN", pathGuardTaskName(),
		"/TR", pathGuardTaskAction(executable),
		"/SC", "ONLOGON",
		"/IT",
		"/RL", "LIMITED",
		"/F",
	}
}

// pathGuardEnabled reports whether the current user's scheduled task exists.
func pathGuardEnabled() bool {
	return runScheduledTask("/Query", "/TN", pathGuardTaskName()) == nil
}

// createNamedEvent opens or creates a manual-reset event. ERROR_ALREADY_EXISTS
// still returns a valid handle, so it is not an error for callers.
func createNamedEvent(name string, initialState bool) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}

	var state uint32
	if initialState {
		state = windows.CREATE_EVENT_INITIAL_SET
	}
	handle, err := windows.CreateEvent(nil, windows.CREATE_EVENT_MANUAL_RESET, state, namePtr)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return 0, err
	}
	return handle, nil
}

func resetPathGuardStopSignal() error {
	event, err := createNamedEvent(pathGuardStopEventName, false)
	if err != nil {
		return fmt.Errorf("could not reset PATH Guard stop signal: %w", err)
	}
	defer func() { _ = windows.CloseHandle(event) }()
	if err := windows.ResetEvent(event); err != nil {
		return fmt.Errorf("could not reset PATH Guard stop signal: %w", err)
	}
	return nil
}

// requestPathGuardStop returns a handle that must stay open until the caller has
// removed the scheduled task. A guard that starts during that window observes
// the signalled event and exits before it can change PATH.
func requestPathGuardStop() (windows.Handle, error) {
	event, err := createNamedEvent(pathGuardStopEventName, false)
	if err != nil {
		return 0, fmt.Errorf("could not signal PATH Guard to stop: %w", err)
	}
	if err := windows.SetEvent(event); err != nil {
		_ = windows.CloseHandle(event)
		return 0, fmt.Errorf("could not signal PATH Guard to stop: %w", err)
	}
	return event, nil
}

func pathGuardRunning() bool {
	namePtr, err := windows.UTF16PtrFromString(pathGuardRunningEventName)
	if err != nil {
		return false
	}
	event, err := windows.OpenEvent(windows.SYNCHRONIZE, false, namePtr)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(event) }()

	result, err := windows.WaitForSingleObject(event, 0)
	return err == nil && result == windows.WAIT_OBJECT_0
}

func waitForPathGuardStop(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for pathGuardRunning() {
		if time.Now().After(deadline) {
			return fmt.Errorf("PATH Guard did not stop within %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// stopPathGuard requests a graceful stop and waits until the background process
// has released its running event. schtasks /End remains a fallback for a guard
// from a previous Shepherd version that does not understand the stop signal.
func stopPathGuard() error {
	stopEvent, err := requestPathGuardStop()
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(stopEvent) }()

	_ = runScheduledTask("/End", "/TN", pathGuardTaskName())
	return waitForPathGuardStop(pathGuardStopTimeout)
}

// enablePathGuard registers the task and starts it for the current session.
func enablePathGuard() error {
	if err := stopPathGuard(); err != nil {
		return err
	}
	if err := resetPathGuardStopSignal(); err != nil {
		return err
	}

	executable, err := ensurePathGuardExecutable()
	if err != nil {
		return fmt.Errorf("PATH Guard requires an installed Shepherd shim: %w", err)
	}
	if err := runScheduledTask(pathGuardTaskCreateArgs(executable)...); err != nil {
		return fmt.Errorf("could not create PATH Guard task: %w", err)
	}
	if err := runScheduledTask("/Run", "/TN", pathGuardTaskName()); err != nil {
		return fmt.Errorf("PATH Guard was scheduled but could not be started: %w", err)
	}
	return nil
}

// disablePathGuard stops and removes the scheduled task. It is idempotent.
func disablePathGuard() (removed bool, err error) {
	stopEvent, err := requestPathGuardStop()
	if err != nil {
		return false, err
	}
	defer func() { _ = windows.CloseHandle(stopEvent) }()

	_ = runScheduledTask("/End", "/TN", pathGuardTaskName())
	if err := waitForPathGuardStop(pathGuardStopTimeout); err != nil {
		return false, err
	}
	if !pathGuardEnabled() {
		return false, nil
	}
	if err := runScheduledTask("/Delete", "/TN", pathGuardTaskName(), "/F"); err != nil {
		return false, fmt.Errorf("could not remove PATH Guard task: %w", err)
	}
	return true, nil
}

// userPathChangeWatcher receives a single change notification for values under
// HKCU\Environment. It is armed before reconciliation to avoid a timing gap.
type userPathChangeWatcher struct {
	key   registry.Key
	event windows.Handle
}

func armUserPathChangeWatcher() (*userPathChangeWatcher, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.NOTIFY)
	if err != nil {
		return nil, err
	}

	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = key.Close()
		return nil, err
	}

	if err := windows.RegNotifyChangeKeyValue(
		windows.Handle(key),
		false,
		windows.REG_NOTIFY_CHANGE_LAST_SET,
		event,
		true,
	); err != nil {
		_ = windows.CloseHandle(event)
		_ = key.Close()
		return nil, err
	}

	return &userPathChangeWatcher{key: key, event: event}, nil
}

func (watcher *userPathChangeWatcher) close() {
	_ = windows.CloseHandle(watcher.event)
	_ = watcher.key.Close()
}

func (watcher *userPathChangeWatcher) wait(stopEvent windows.Handle) (pathChanged, stopped bool, err error) {
	result, err := windows.WaitForMultipleObjects(
		[]windows.Handle{stopEvent, watcher.event},
		false,
		windows.INFINITE,
	)
	if err != nil {
		return false, false, err
	}
	switch result {
	case windows.WAIT_OBJECT_0:
		return false, true, nil
	case windows.WAIT_OBJECT_0 + 1:
		return true, false, nil
	default:
		return false, false, fmt.Errorf("unexpected wait result %d", result)
	}
}

func waitForStopSignal(stopEvent windows.Handle, timeout time.Duration) bool {
	result, err := windows.WaitForSingleObject(stopEvent, uint32(timeout/time.Millisecond))
	return err == nil && result == windows.WAIT_OBJECT_0
}

// runPathGuard continuously watches User PATH changes and restores Shepherd as
// the first entry. It only ever modifies HKCU\Environment\Path.
func runPathGuard() error {
	mutexName, err := windows.UTF16PtrFromString(pathGuardMutexName)
	if err != nil {
		return fmt.Errorf("invalid PATH Guard mutex name: %w", err)
	}
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if mutex != 0 {
		defer func() { _ = windows.CloseHandle(mutex) }()
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil // Another task or manual invocation already owns the guard.
	}
	if err != nil {
		return fmt.Errorf("could not create PATH Guard mutex: %w", err)
	}

	stopEvent, err := createNamedEvent(pathGuardStopEventName, false)
	if err != nil {
		return fmt.Errorf("could not create PATH Guard stop event: %w", err)
	}
	defer func() { _ = windows.CloseHandle(stopEvent) }()
	if waitForStopSignal(stopEvent, 0) {
		return nil
	}

	runningEvent, err := createNamedEvent(pathGuardRunningEventName, false)
	if err != nil {
		return fmt.Errorf("could not create PATH Guard running event: %w", err)
	}
	defer func() {
		_ = windows.ResetEvent(runningEvent)
		_ = windows.CloseHandle(runningEvent)
	}()
	if err := windows.SetEvent(runningEvent); err != nil {
		return fmt.Errorf("could not set PATH Guard running event: %w", err)
	}

	for {
		// Arm the registry notification first, then reconcile. This ensures a
		// concurrent Herd update cannot be lost between the check and the wait.
		watcher, err := armUserPathChangeWatcher()
		if err != nil {
			if waitForStopSignal(stopEvent, pathGuardRetryDelay) {
				return nil
			}
			continue
		}

		if _, err := ensureShepherdFirstInUserPath(); err != nil {
			watcher.close()
			if waitForStopSignal(stopEvent, pathGuardRetryDelay) {
				return nil
			}
			continue
		}

		changed, stopped, err := watcher.wait(stopEvent)
		watcher.close()
		if stopped {
			return nil
		}
		if err != nil {
			if waitForStopSignal(stopEvent, pathGuardRetryDelay) {
				return nil
			}
			continue
		}
		if changed && waitForStopSignal(stopEvent, 250*time.Millisecond) {
			return nil
		}
	}
}

// confirmPathGuard explains the persistent, opt-in behavior before enabling it.
func confirmPathGuard() bool {
	fmt.Println()
	fmt.Println("Keep Shepherd ahead of Herd after future Herd updates?")
	fmt.Println()
	fmt.Printf("This creates a per-user scheduled task named %q.\n", pathGuardTaskName())
	fmt.Println("It starts at login and watches only your User PATH.")
	fmt.Println("If another program changes the order, it restores Shepherd before Herd.")
	fmt.Println("No admin rights or network access are used.")
	fmt.Print("Enable PATH Guard? [y/N] ")

	var answer string
	_, _ = fmt.Scanln(&answer)
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

// cmdPathGuard manages the optional PATH Guard task.
func cmdPathGuard() {
	usage := func() {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]string{
				"command":     "guard",
				"usage":       "shp guard <enable|disable|status>",
				"description": "Manage the optional PATH Guard that keeps Shepherd ahead of Herd",
			})
			return
		}
		fmt.Println("Usage: shp guard <enable|disable|status>")
		fmt.Println()
		fmt.Println("Manage the optional PATH Guard that keeps Shepherd ahead of Herd.")
	}

	if len(os.Args) < 3 || os.Args[2] == "-h" || os.Args[2] == "--help" {
		usage()
		return
	}

	switch os.Args[2] {
	case "enable":
		if !isInstalled() {
			fmt.Fprintln(os.Stderr, "Error: Shepherd is not installed. Run `shp install` first.")
			os.Exit(1)
		}
		if err := enablePathGuard(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]interface{}{
				"enabled":  true,
				"running":  pathGuardRunning(),
				"taskName": pathGuardTaskName(),
			})
			return
		}
		fmt.Printf("  ✓ PATH Guard enabled (%s)\n", pathGuardTaskName())
	case "disable":
		removed, err := disablePathGuard()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]interface{}{
				"enabled": false,
				"removed": removed,
			})
			return
		}
		if removed {
			fmt.Println("  ✓ PATH Guard disabled")
		} else {
			fmt.Println("  • PATH Guard is already disabled")
		}
	case "status":
		enabled := pathGuardEnabled()
		running := pathGuardRunning()
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]interface{}{
				"enabled":  enabled,
				"running":  running,
				"taskName": pathGuardTaskName(),
			})
			return
		}
		if enabled {
			state := "waiting for the next sign-in"
			if running {
				state = "running"
			}
			fmt.Printf("  ✓ PATH Guard is enabled (%s; %s)\n", pathGuardTaskName(), state)
		} else {
			fmt.Println("  • PATH Guard is disabled (enable it with `shp guard enable`)")
		}
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown guard action %q\n", os.Args[2])
		usage()
		os.Exit(1)
	}
}
