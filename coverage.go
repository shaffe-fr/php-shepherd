package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Coverage is a capability, not an extension. Two drivers provide it and
// php-code-coverage picks one on its own, preferring pcov over xdebug — so
// enabling both in php.ini leaves xdebug's coverage mode inert while still
// paying for it. This command resolves the driver per invocation instead,
// which makes the conflict unrepresentable and leaves php.ini untouched.

// coverageDrivers describes which drivers a PHP install can offer.
type coverageDrivers struct {
	// extDir is the extension_dir to hand to php so php.ini's own
	// extension= lines keep resolving under -d.
	extDir string

	// pcovDLL is the path to php_pcov.dll, empty when not installed.
	pcovDLL string

	// pcovInIni reports an active extension=pcov line.
	pcovInIni bool

	// pcovEnabledInIni reports a truthy pcov.enabled, which is what decides
	// whether php-code-coverage considers pcov usable at all.
	pcovEnabledInIni bool

	// xdebugDLL is the path to Herd's xdebug DLL, empty when not shipped.
	xdebugDLL string

	// xdebugInIni reports an active zend_extension line pointing at xdebug.
	xdebugInIni bool
}

// coveragePlan is the resolved decision for one invocation.
type coveragePlan struct {
	driver string
	flags  []string
}

// detectCoverageDrivers inspects the PHP install and its php.ini.
// A missing or unreadable php.ini is not an error: it only means no driver is
// preloaded, which the -d flags then compensate for.
func detectCoverageDrivers(phpDir, version string) coverageDrivers {
	extDir := phpExtDir(phpDir)
	d := coverageDrivers{extDir: extDir}

	pcovDLL := filepath.Join(extDir, "php_pcov.dll")
	if _, err := os.Stat(pcovDLL); err == nil {
		d.pcovDLL = pcovDLL
	}

	xdebugDLL := xdebugDLLPath(version)
	if _, err := os.Stat(xdebugDLL); err == nil {
		d.xdebugDLL = xdebugDLL
	}

	if data, err := os.ReadFile(phpIniPath(phpDir)); err == nil {
		lines := strings.Split(string(data), "\n")
		d.pcovInIni = iniAssignsExtension(lines, "extension", "pcov")
		d.xdebugInIni = iniAssignsExtension(lines, "zend_extension", "xdebug")

		// An absent pcov.enabled means pcov's own default, which is on.
		enabled, found := iniValue(lines, "pcov.enabled")
		d.pcovEnabledInIni = d.pcovInIni && (!found || iniTruthy(enabled))
	}

	return d
}

// iniAssignsExtension reports whether an uncommented directive line loads a
// module whose value mentions name. The value is matched loosely because
// zend_extension is written as an absolute DLL path, not a bare module name.
func iniAssignsExtension(lines []string, directive, name string) bool {
	re := iniKeyRe(directive)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !re.MatchString(trimmed) {
			continue
		}
		_, value, found := strings.Cut(trimmed, "=")
		if found && strings.Contains(strings.ToLower(value), name) {
			return true
		}
	}
	return false
}

// planCoverage picks the fastest available driver and the flags that turn it on.
// pcov wins because php-code-coverage would pick it anyway; obeying that order
// keeps what Shepherd reports and what PHPUnit does in agreement.
func planCoverage(d coverageDrivers) (coveragePlan, error) {
	flags := []string{"-d", "extension_dir=" + d.extDir}

	switch {
	case d.pcovDLL != "":
		if !d.pcovInIni {
			flags = append(flags, "-d", "extension=php_pcov.dll")
		}
		// pcov is installed with pcov.enabled=0 so it costs nothing outside
		// coverage runs; this is the invocation that opts in.
		flags = append(flags, "-d", "pcov.enabled=1")
		return coveragePlan{driver: "pcov", flags: flags}, nil

	case d.xdebugDLL != "":
		if !d.xdebugInIni {
			flags = append(flags, "-d", "zend_extension="+d.xdebugDLL)
		}
		flags = append(flags, "-d", "xdebug.mode=coverage")
		return coveragePlan{driver: "xdebug", flags: flags}, nil

	default:
		return coveragePlan{}, fmt.Errorf("no coverage driver available — install pcov with: shp coverage install")
	}
}

// cmdCoverage manages code coverage drivers for the resolved PHP version.
//
// Usage:
//
//	shp coverage                        Show which driver would be used
//	shp coverage status                 Same, explicitly
//	shp coverage install [--php=all]    Install pcov
//	shp coverage run -- <command...>    Run a command with coverage enabled
func cmdCoverage() {
	sub := "status"
	if len(os.Args) > 2 {
		sub = strings.ToLower(os.Args[2])
	}

	switch sub {
	case "-h", "--help", "help":
		coverageUsage()
	case "status":
		cmdCoverageStatus()
	case "install":
		cmdCoverageInstall()
	case "run":
		cmdCoverageRun()
	default:
		fmt.Fprintf(os.Stderr, "Unknown coverage command: %s\n", sub)
		coverageUsage()
		os.Exit(1)
	}
}

func coverageUsage() {
	if emitHelpJSON("coverage", "shp coverage <command>",
		"Manage the code coverage driver for the resolved PHP version") {
		return
	}
	fmt.Println("Usage: shp coverage <command>")
	fmt.Println()
	fmt.Println("Manage the code coverage driver for the resolved PHP version.")
	fmt.Println("pcov is preferred over xdebug: it is several times faster, which is")
	fmt.Println("what php-code-coverage picks anyway when both are loaded.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  status                Show which driver is available and would be used")
	fmt.Println("  install [--php=all]   Install pcov (disabled by default, enabled per run)")
	fmt.Println("  run -- <command...>   Run a command with coverage enabled (no config change)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  shp coverage install --php=all")
	fmt.Println("  shp coverage run -- php artisan test --coverage")
	fmt.Println("  shp coverage run -- vendor/bin/phpunit --coverage-html build/coverage")
	fmt.Println()
	fmt.Println("Note: pcov reports line coverage only. Branch and path coverage need")
	fmt.Println("      xdebug — use `shp xdebug coverage` for those.")
}

// cmdCoverageInstall installs pcov through the existing extension pipeline.
func cmdCoverageInstall() {
	for _, arg := range os.Args[3:] {
		if arg == "-h" || arg == "--help" {
			if emitHelpJSON("coverage install", "shp coverage install [--php=X.Y] [--php=all]",
				"Install pcov for the resolved PHP version") {
				return
			}
			fmt.Println("Usage: shp coverage install [--php=X.Y] [--php=all]")
			fmt.Println()
			fmt.Println("Install pcov. It is added to php.ini with pcov.enabled=0 so it stays")
			fmt.Println("inert until `shp coverage run` enables it for a single command.")
			return
		}
	}

	// cmdExtInstall reads its arguments from os.Args, so the delegation is
	// expressed as the equivalent `ext add` invocation.
	args := append([]string{os.Args[0], "ext", "add", "pcov"}, os.Args[3:]...)
	os.Args = args
	cmdExtInstall()
}

// cmdCoverageStatus reports the driver that `shp coverage run` would select.
func cmdCoverageStatus() {
	phpDir, version := requireCoveragePHP()
	d := detectCoverageDrivers(phpDir, version)
	plan, err := planCoverage(d)

	driver := plan.driver
	if err != nil {
		driver = ""
	}

	if jsonOutput {
		result := map[string]interface{}{
			"phpVersion": version,
			"driver":     nilIfEmpty(driver),
			"pcov": map[string]interface{}{
				"installed": d.pcovDLL != "",
				"inPhpIni":  d.pcovInIni,
			},
			"xdebug": map[string]interface{}{
				"available": d.xdebugDLL != "",
				"inPhpIni":  d.xdebugInIni,
			},
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		if err != nil {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("PHP %s — %s\n", version, phpIniPath(phpDir))
	printCoverageDriverLine("pcov", d.pcovDLL != "", d.pcovInIni)
	printCoverageDriverLine("xdebug", d.xdebugDLL != "", d.xdebugInIni)

	if err != nil {
		fmt.Println()
		fmt.Println("  ⏸️  no coverage driver available")
		fmt.Println("  Install the fast one with: shp coverage install")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("  ✅ coverage runs would use %s\n", plan.driver)
	if plan.driver == "xdebug" && d.pcovDLL == "" {
		fmt.Println("  Tip: `shp coverage install` adds pcov, which is several times faster")
	}
	if d.pcovEnabledInIni && d.xdebugInIni {
		fmt.Println("  ⚠️  pcov.enabled=1 in php.ini, so it shadows xdebug for coverage")
		fmt.Println("      and instruments every other run too. Set pcov.enabled=0 and let")
		fmt.Println("      `shp coverage run` enable it per command.")
	}
}

// warnPcovShadowsXdebug reports that xdebug will not be the coverage driver,
// because php-code-coverage prefers pcov whenever pcov.enabled is truthy.
// Selecting an xdebug mode that includes coverage is then a silent no-op.
func warnPcovShadowsXdebug(mode string, iniLines []string) {
	if jsonOutput || quiet || !strings.Contains(mode, "coverage") {
		return
	}
	if !iniAssignsExtension(iniLines, "extension", "pcov") {
		return
	}
	enabled, found := iniValue(iniLines, "pcov.enabled")
	if found && !iniTruthy(enabled) {
		return
	}

	fmt.Fprintln(os.Stderr, "  ⚠️  pcov is loaded with pcov.enabled on, and php-code-coverage")
	fmt.Fprintln(os.Stderr, "      prefers pcov, so xdebug will not be the coverage driver.")
	fmt.Fprintln(os.Stderr, "      Set pcov.enabled=0 in php.ini, or use `shp coverage run`.")
}

// printCoverageDriverLine renders one driver's availability.
func printCoverageDriverLine(name string, available, inIni bool) {
	if !available {
		fmt.Printf("  ○ %s not installed\n", name)
		return
	}
	if inIni {
		fmt.Printf("  ✓ %s available (loaded in php.ini)\n", name)
		return
	}
	fmt.Printf("  ✓ %s available\n", name)
}

// cmdCoverageRun executes a command with a coverage driver injected via -d.
// Stateless: php.ini is never modified, so there is no state to forget to undo.
func cmdCoverageRun() {
	if len(os.Args) > 3 && (os.Args[3] == "-h" || os.Args[3] == "--help") {
		if emitHelpJSON("coverage run", "shp coverage run -- <command...>",
			"Run a command with code coverage enabled (one-off, no config change)") {
			return
		}
		fmt.Println("Usage: shp coverage run -- <command...>")
		fmt.Println()
		fmt.Println("Run a command with code coverage enabled (one-off, no config change).")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shp coverage run -- php artisan test --coverage")
		fmt.Println("  shp coverage run -- vendor/bin/phpunit --coverage-text")
		return
	}

	sepIdx := -1
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--" {
			sepIdx = i
			break
		}
	}
	if sepIdx == -1 || sepIdx >= len(os.Args)-1 {
		fmt.Fprintf(os.Stderr, "Error: missing command after '--'\n")
		fmt.Fprintf(os.Stderr, "Usage: shp coverage run -- <command...>\n")
		os.Exit(1)
	}
	cmdArgs := os.Args[sepIdx+1:]

	phpDir, version := requireCoveragePHP()
	plan, err := planCoverage(detectCoverageDrivers(phpDir, version))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !jsonOutput && !quiet {
		fmt.Fprintf(os.Stderr, "PHP %s — coverage driver: %s\n", version, plan.driver)
	}

	phpExe := filepath.Join(phpDir, "php.exe")
	extraFields := map[string]interface{}{"phpVersion": version, "driver": plan.driver}

	if script, args, ok := resolvePHPTarget(cmdArgs); ok {
		execArgs := append([]string{}, plan.flags...)
		if script != "" {
			execArgs = append(execArgs, script)
		}
		execArgs = append(execArgs, args...)
		execCmdResult(exec.Command(phpExe, execArgs...), extraFields)
		return
	}

	// The target is a native executable, so -d flags cannot reach the engine
	// it spawns. Without them the driver stays off and PHPUnit would only
	// report that no driver is available, so say why here instead.
	fmt.Fprintf(os.Stderr, "Error: cannot enable coverage for %q — it is not a PHP entry point\n", cmdArgs[0])
	fmt.Fprintf(os.Stderr, "  Invoke the PHP script directly, e.g.:\n")
	fmt.Fprintf(os.Stderr, "    shp coverage run -- php artisan test --coverage\n")
	fmt.Fprintf(os.Stderr, "    shp coverage run -- vendor/bin/phpunit --coverage-text\n")
	os.Exit(1)
}

// resolvePHPTarget maps a command line onto a php.exe invocation, returning the
// script to run (empty when the user already said "php") and the remaining
// arguments. ok is false when nothing PHP-executable was found.
func resolvePHPTarget(cmdArgs []string) (script string, args []string, ok bool) {
	switch strings.ToLower(cmdArgs[0]) {
	case "php", "php.exe":
		return "", cmdArgs[1:], true
	case "composer", "composer.exe":
		return filepath.Join(herdHome(), "composer.phar"), cmdArgs[1:], true
	}

	// Composer bin-dir entries ship as a .bat wrapper next to the extensionless
	// PHP script; vendor/bin/phpunit and friends are reached that way.
	target := strings.TrimSuffix(strings.TrimSuffix(cmdArgs[0], ".bat"), ".cmd")
	for _, candidate := range []string{target, target + ".php", target + ".phar"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, cmdArgs[1:], true
		}
	}

	return "", nil, false
}

// requireCoveragePHP resolves the PHP install for the current directory,
// exiting with a message when it cannot be determined.
func requireCoveragePHP() (phpDir, version string) {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	phpPath, version, err := resolveCurrentPHP(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if version == "" {
		fmt.Fprintf(os.Stderr, "Error: could not determine PHP version\n")
		os.Exit(1)
	}
	return filepath.Dir(phpPath), version
}
