package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// isLaravelProject returns true if the given directory looks like a Laravel project
// (contains artisan or a composer.json requiring laravel/framework).
func isLaravelProject(dir string) bool {
	// Quick check: artisan file exists
	if _, err := os.Stat(filepath.Join(dir, "artisan")); err == nil {
		return true
	}
	// Fallback: composer.json mentions laravel/framework
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "laravel/framework")
}

// requiresReverb returns true if the project's composer.json requires laravel/reverb.
func requiresReverb(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "laravel/reverb")
}

// cmdReverb shows Reverb status and .env configuration for the current project.
//
// Usage:
//
//	shp reverb              Show current Reverb status
//	shp reverb status       Same as above
//	shp reverb env          Print the recommended .env variables
func cmdReverb() {
	if err := requireHerd(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Help
	if len(os.Args) > 2 && (os.Args[2] == "-h" || os.Args[2] == "--help") {
		fmt.Println("Usage: shp reverb [command]")
		fmt.Println()
		fmt.Println("Show Reverb status and configuration for the current project.")
		fmt.Println("Reverb serves WebSockets directly over TLS using Herd's certificates —")
		fmt.Println("no nginx reverse proxy needed.")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  status   Show current Reverb connectivity (default)")
		fmt.Println("  env      Print the recommended .env variables")
		fmt.Println()
		fmt.Println("Flags:")
		fmt.Println("  --port=PORT   Reverb listen port (default: 8443)")
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	// Guard: only run in a Laravel project that actually uses Reverb
	if !isLaravelProject(cwd) {
		fmt.Fprintf(os.Stderr, "Error: not a Laravel project (no artisan or laravel/framework in composer.json).\n")
		os.Exit(1)
	}
	if !requiresReverb(cwd) {
		fmt.Fprintf(os.Stderr, "Error: laravel/reverb is not required in this project's composer.json.\n")
		fmt.Fprintf(os.Stderr, "  Install it first: composer require laravel/reverb\n")
		os.Exit(1)
	}

	// Resolve project domain
	domain := findProjectDomain(cwd)
	if domain == "" {
		fmt.Fprintf(os.Stderr, "Error: could not find this project in Herd's parked paths.\n")
		fmt.Fprintf(os.Stderr, "Make sure the project is in a directory registered with Herd.\n")
		os.Exit(1)
	}

	tld := herdTLD()
	fqdn := domain + "." + tld

	// Parse --port flag
	port := 8443
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--port=") {
			v, perr := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if perr != nil || v < 1 || v > 65535 {
				fmt.Fprintf(os.Stderr, "Error: invalid port value\n")
				os.Exit(1)
			}
			port = v
		}
	}

	subcmd := "status"
	if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
		subcmd = strings.ToLower(os.Args[2])
	}

	switch subcmd {
	case "status":
		cmdReverbStatus(fqdn, port)
	case "env":
		cmdReverbEnv(fqdn, port)
	default:
		fmt.Fprintf(os.Stderr, "Unknown reverb command: %s\n", os.Args[2])
		fmt.Fprintf(os.Stderr, "Run `shp reverb --help` for usage.\n")
		os.Exit(1)
	}
}

// cmdReverbStatus shows whether Reverb is reachable on its expected port.
func cmdReverbStatus(fqdn string, port int) {
	portStr := strconv.Itoa(port)
	listening := checkPort(fqdn, portStr)

	// Check if certs exist (needed for Reverb's auto-TLS)
	certFile := filepath.Join(herdCertsDir(), fqdn+".crt")
	hasCert := false
	if _, err := os.Stat(certFile); err == nil {
		hasCert = true
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]interface{}{
			"domain":    fqdn,
			"port":      port,
			"listening": listening,
			"hasCert":   hasCert,
			"startCmd":  fmt.Sprintf("php artisan reverb:start --host=0.0.0.0 --port=%d", port),
		})
		return
	}

	fmt.Printf("  Reverb: %s:%d\n", fqdn, port)
	fmt.Println()

	if !hasCert {
		domain := strings.TrimSuffix(fqdn, "."+herdTLD())
		fmt.Printf("  ✗ No SSL certificate found for %s\n", fqdn)
		fmt.Printf("    → Run: herd secure %s\n", domain)
		fmt.Printf("    → Reverb needs certs to serve WSS (auto-detected from Herd)\n")
		return
	}
	fmt.Printf("  ✓ SSL certificate found (Reverb will auto-detect it)\n")

	if listening {
		fmt.Printf("  ✓ Listening on wss://%s:%d\n", fqdn, port)
	} else {
		fmt.Printf("  ✗ Not listening on port %d\n", port)
		fmt.Printf("    → Start Reverb: php artisan reverb:start --host=0.0.0.0 --port=%d\n", port)
	}
}

// cmdReverbEnv prints the recommended .env variables for Reverb.
func cmdReverbEnv(fqdn string, port int) {
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]interface{}{
			"REVERB_SERVER_HOST": "0.0.0.0",
			"REVERB_SERVER_PORT": port,
			"REVERB_HOST":        fqdn,
			"REVERB_PORT":        port,
			"REVERB_SCHEME":      "https",
			"VITE_REVERB_HOST":   fqdn,
			"VITE_REVERB_PORT":   port,
			"VITE_REVERB_SCHEME": "https",
		})
		return
	}

	fmt.Println("  Add this to your .env:")
	fmt.Println()
	fmt.Printf("    REVERB_SERVER_HOST=0.0.0.0\n")
	fmt.Printf("    REVERB_SERVER_PORT=%d\n", port)
	fmt.Printf("    REVERB_HOST=%s\n", fqdn)
	fmt.Printf("    REVERB_PORT=%d\n", port)
	fmt.Printf("    REVERB_SCHEME=https\n")
	fmt.Println()
	fmt.Printf("    VITE_REVERB_HOST=%s\n", fqdn)
	fmt.Printf("    VITE_REVERB_PORT=%d\n", port)
	fmt.Printf("    VITE_REVERB_SCHEME=https\n")
}

// checkPort attempts a TCP connection to host:port to see if something is listening.
func checkPort(host, port string) bool {
	if port == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close() //nolint:errcheck // connection test, close is best-effort
	return true
}
