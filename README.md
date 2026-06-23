# Shepherd

Per-project PHP on Windows, done right.

Drop a `.phpversion` file in your project root, and Shepherd ensures both the CLI and Herd's nginx use the correct PHP version — automatically.

[![CI](https://github.com/shaffe-fr/php-shepherd/actions/workflows/ci.yml/badge.svg)](https://github.com/shaffe-fr/php-shepherd/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## The problem

Herd installs multiple PHP versions side by side but doesn't natively support per-project CLI switching via a simple dotfile. The usual workaround is batch scripts that wrap `php.exe`, but these are fragile — they spawn subshells, suffer from PATH recursion, and break in subtle ways across different terminal emulators.

## How it works

`shp` is a single compiled binary (~2 MB) that replaces both `php` and `composer` in your PATH:

1. **Reads `.phpversion`** from the current directory, walking up the tree (like `.nvmrc` for Node)
2. **Resolves the matching `php.exe`** from Herd's installations (`~/.config/herd/bin/phpXX/`)
3. **Falls back to `herd.phar which-php`** if no `.phpversion` is found
4. **Syncs Herd's nginx config** — updates `ISOLATED_PHP_VERSION` and `fastcgi_pass` in the site's `.conf`, then restarts nginx in the background
5. **Rewrites xdebug paths** to match the resolved PHP version
6. **Execs the real `php.exe`** with the correct `extension_dir`

No batch scripts. No subshells. No recursion. No race conditions.

## Requirements

- [Laravel Herd](https://herd.laravel.com) for Windows
- Windows 10/11 (amd64 or arm64)

## Installation

### 1. Download

Grab the latest `php-shepherd_<version>_windows_<arch>.zip` from the [releases page](https://github.com/shaffe-fr/php-shepherd/releases) and extract `shp.exe` somewhere convenient.

### 2. Run the installer (recommended)

The binary ships with a built-in installer. From the folder where you extracted it:

```powershell
.\shp.exe install
```

This will:

- Copy the binary as `php.exe`, `composer.exe`, and `shp.exe` into
  `%USERPROFILE%\.config\shepherd\bin`
- Prepend that directory to your **User** `PATH` (so it takes precedence over Herd's own `php`)
- Broadcast the environment change to running apps

Use `--force` (or `-f`) to kill running shim processes before overwriting — useful when a previous `php.exe` or `composer.exe` shim is still in use by another process:

```powershell
.\shp.exe install --force
```

Then **restart your terminal** for the `PATH` change to take effect.

Verify it worked:

```powershell
shp status
```

To remove everything (shims + PATH entry):

```powershell
shp uninstall
```

<details>
<summary>Manual installation (alternative)</summary>

If you prefer to manage your own `PATH`, copy the binary under both names into a
directory that comes **before** Herd's `~/.config/herd/bin` in your `PATH`:

```powershell
$dest = "$env:USERPROFILE\.bin"
New-Item -ItemType Directory -Force -Path $dest | Out-Null
Copy-Item shp.exe "$dest\php.exe"
Copy-Item shp.exe "$dest\composer.exe"
```

</details>

### From source

```powershell
go build -ldflags="-s -w -X main.version=dev" -o shp.exe .
.\shp.exe install
```

## Usage

Create a `.phpversion` file in your project root:

```
8.4
```

Then use `php` and `composer` as usual — the correct version is resolved automatically:

```powershell
cd my-project
php -v             # → PHP 8.4.x
php artisan        # → uses PHP 8.4
composer install   # → uses PHP 8.4

cd ../other-project  # has .phpversion containing "8.5"
php -v             # → PHP 8.5.x
```

If no `.phpversion` is found while walking up the tree, Shepherd falls back to asking Herd directly via `herd.phar which-php`.

When the version in `.phpversion` differs from what's configured in Herd's nginx, Shepherd updates the config and restarts nginx in the background — so your local `.test` domain always matches.

## Commands

| Command | Description |
|---------|-------------|
| `shp install` | Install the `php`/`composer` shims and prepend them to the User PATH |
| `shp uninstall` | Remove the shims and clean up the PATH |
| `shp status` | Show whether the shims are installed and ordered correctly relative to Herd |
| `shp doctor` | Diagnose common issues with Shepherd setup |
| `shp xdebug [mode]` | Toggle xdebug on/off for the resolved PHP version |
| `shp ext install <name>` | Install a PHP extension from PECL |
| `shp ext list` | List installed extensions for the resolved PHP |
| `shp self-update` | Update Shepherd to the latest GitHub release (with SHA256 verification) |
| `shp version` | Show the current shp version |
| `shp` (no args) | Print usage help |

When invoked as `php`/`php.exe` or `composer`/`composer.exe`, it acts as a transparent PHP version switcher (see [Multicall binary](#multicall-binary)).

## Self-update

Update Shepherd to the latest version with a single command:

```powershell
shp self-update
```

This will:

1. Check the latest release on GitHub
2. Download the matching archive for your architecture
3. **Verify the SHA256 checksum** against `checksums.txt` from the release (mandatory — update is refused if verification fails)
4. Replace the current binary and all installed shims

Downloads are restricted to HTTPS on known GitHub domains only. Releases are signed with [cosign](https://docs.sigstore.dev) (Sigstore keyless) — see [SECURITY.md](SECURITY.md) for manual verification instructions.

```powershell
shp version       # show current version
shp self-update   # update to latest
```

## Xdebug management

Toggle xdebug on or off without manually editing `php.ini`:

```powershell
shp xdebug              # enable with mode=debug (default)
shp xdebug coverage     # enable with mode=coverage
shp xdebug debug,coverage  # both
shp xdebug profile      # profiling mode
shp xdebug off          # disable xdebug
shp xdebug status       # show current state
```

The command resolves the PHP version the same way as the `php` shim (`.phpversion` → Herd fallback), then edits the matching `php.ini` in place:

- Enables/disables the `zend_extension=...xdebug` line (commenting/uncommenting)
- Sets `xdebug.mode` to the requested value
- Ensures `xdebug.discover_client_host=true` and `xdebug.start_with_request=yes` are present
- If no xdebug line exists, adds one pointing to the correct DLL from Herd's bundled xdebug directory

### Typical workflows

```powershell
# Run tests with coverage
shp xdebug coverage
php artisan test --coverage
shp xdebug off

# Debug a request (e.g. with PhpStorm)
shp xdebug debug
# ... trigger your request ...
shp xdebug off

# Check current state
shp xdebug status
#  ✅ xdebug is enabled (mode: coverage)
```

Xdebug adds overhead, so toggling it off when you don't need it keeps things fast.

## Extension management

Install PHP extensions from PECL directly into the resolved Herd PHP version:

```powershell
shp ext install imagick
shp ext install redis --php=8.4
shp ext install mongodb --ext-version=1.19.0
shp ext list
```

### Options

| Flag | Description |
|------|-------------|
| `--php=X.Y` | Target PHP version (default: resolved from `.phpversion`) |
| `--ext-version=V` | Extension version (default: latest from PECL) |
| `--ts` | Use Thread Safe build (default: NTS) |
| `--vs=vsXX` | Visual Studio version (default: vs17) |

### What it does

1. Resolves the PHP version (from `.phpversion` or `--php`)
2. Detects the latest extension version on [pecl.php.net](https://pecl.php.net) (or uses `--ext-version`)
3. Downloads the pre-compiled NTS x64 zip from `downloads.php.net`
4. Extracts the extension DLL (`php_<name>.dll`) into the `ext/` directory
5. Places support DLLs (e.g. ImageMagick's `CORE_*.dll`) next to `php.exe`
6. Adds `extension=<name>` (or `zend_extension=` for xdebug/opcache) to `php.ini`
7. Verifies the extension loads via `php -m`

## Multicall binary

The binary detects how it was invoked via its filename:

| Invoked as | Behavior |
|-----------|----------|
| `php` or `php.exe` | Runs PHP with your arguments |
| `composer` or `composer.exe` | Runs `composer.phar` via the resolved PHP |
| `shp.exe` | Management commands (`install`/`uninstall`/`status`/`xdebug`/`ext`/`self-update`) |

This means you only need one binary — the `install` command sets up all three names for you.

## How nginx sync works

Herd stores the isolated PHP version for each site in its nginx config:

```nginx
# ISOLATED_PHP_VERSION=8.4
...
fastcgi_pass $herd_sock_84;
```

When you switch versions via `.phpversion`, Shepherd:

1. Checks if the config already matches — if so, does nothing (fast path)
2. Updates the `ISOLATED_PHP_VERSION` comment and `$herd_sock_XX` references
3. Restarts nginx via `herd.phar restart nginx` in the background (non-blocking)

This happens transparently on every `php` invocation with no perceptible delay.

## PATH configuration

The shim directory must come **before** Herd's `~/.config/herd/bin` in your `PATH`,
otherwise Herd's own `php.exe` wins. The `install` command handles this for you, and
`status` will warn you if the ordering is wrong:

```
%USERPROFILE%\.config\shepherd\bin       ← shepherd (must be first)
%USERPROFILE%\.config\herd\bin        ← Herd's default PHP
```

## Troubleshooting

- **`php -v` still shows the wrong version** — run `shp status`. If the shim
  is listed *after* Herd in PATH, re-run `install` and restart your terminal.
- **Changes don't apply** — the `PATH` is only re-read when a new terminal/session starts.
  Open a fresh terminal.
- **`php X.Y not found`** — the version in `.phpversion` isn't installed in Herd. Install it
  from the Herd UI, or pick an installed version.

## Comparison with batch scripts

| | Batch scripts | Shepherd |
|---|---|---|
| Spawn subshells | Yes (cmd.exe chains) | No |
| PATH recursion risk | High | Impossible |
| CR/LF handling | Manual, error-prone | Automatic |
| Startup overhead | ~50-100ms (multiple cmd.exe) | <1ms |
| Cross-shell compat | Needs .bat + bash variants | Single .exe works everywhere |
| nginx sync | Separate script, blocking | Built-in, non-blocking |

## License

[MIT](LICENSE)
