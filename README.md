# Shepherd

**Per-project PHP version switching on Windows — automatic, instant, zero-config.**

Drop a `.phpversion` file in your project, and `php` / `composer` use the right version. No manual switching, no batch scripts, no broken PATH.

[![CI](https://github.com/shaffe-fr/php-shepherd/actions/workflows/ci.yml/badge.svg)](https://github.com/shaffe-fr/php-shepherd/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

```bash
# Inside project A
~/projects/api $ php -v
PHP 8.3.12 (cli) ...

# Just change directory — Shepherd handles the rest automatically
~/projects/api $ cd ../frontend
~/projects/frontend $ php -v
PHP 8.5.7 (cli) ...
```

## Why

Laravel Herd installs multiple PHP versions side by side, but doesn't let you pin a version per project from the CLI. The usual workaround is batch scripts wrapping `php.exe` — fragile, slow, and broken across terminals.

Shepherd replaces that with a single compiled binary (~2 MB) that acts as a transparent shim for `php` and `composer`. It reads `.phpversion`, resolves the right `php.exe` from Herd's installations, syncs nginx, and gets out of the way.

Shepherd does not alter your Herd installation or replace its binaries. It sits in your user profile PATH, layers on top of Herd, and falls back to Herd's default behavior when no project configuration is found.

No subshells. No recursion. No race conditions.

## Requirements

- [Laravel Herd](https://herd.laravel.com) for Windows
- Windows 10/11 (amd64 or arm64)

## Quick start

1. Download the latest release from the [releases page](https://github.com/shaffe-fr/php-shepherd/releases)
2. Run `shp.exe` — it detects it's not installed and offers to set everything up:

```
Shepherd is not installed yet. Install now? [Y/n]
```

3. Restart your terminal, then:

```powershell
shp use 8.4      # writes .phpversion
php -v           # → PHP 8.4.x
composer install # → uses PHP 8.4
```

That's it. The installer places shims (`php.exe`, `composer.exe`, `shp.exe`) in `%USERPROFILE%\.config\shepherd\bin`, prepends it to your PATH, and broadcasts the change.

> **CI / non-interactive:** Use `shp install` in scripts. Interactive prompts are auto-skipped when stdin is not a terminal.

## How it works

1. Reads `.phpversion` from the current directory, walking up the tree (like `.nvmrc`)
2. Resolves the matching `php.exe` from Herd's installs (`~/.config/herd/bin/phpXX/`)
3. Falls back to `herd.phar which-php` when no dotfile is found
4. Syncs Herd's nginx config so your `.test` domain matches the CLI version
5. Execs the real `php.exe` — transparent, no wrapper overhead

## Commands

| Command              | Description                                                          |
|----------------------|----------------------------------------------------------------------|
| `shp use [version]`  | Set the PHP version for the current project (`latest`, `auto`)       |
| `shp run <ver> -- …` | Run a command with a specific PHP version                            |
| `shp which`          | Show resolved PHP path and source                                    |
| `shp current`        | Print the resolved PHP version                                       |
| `shp list`           | List available PHP versions                                          |
| `shp status`         | Show configuration overview                                          |
| `shp xdebug <cmd>`   | Toggle/configure xdebug (`on`, `off`, `debug`, `coverage`, `toggle`) |
| `shp ext add <name>` | Install a PHP extension (DLL + deps + ini)                           |
| `shp ext list`       | List installed extensions                                            |
| `shp ext remove <n>` | Remove an installed extension                                        |
| `shp reverb`         | Show Laravel Reverb status and .env config                           |
| `shp doctor`         | Diagnose common setup issues                                         |
| `shp self-update`    | Update to the latest release (SHA256-verified)                       |
| `shp install`        | Install shims and configure PATH                                     |
| `shp uninstall`      | Remove shims and restore PATH                                        |
| `shp version`        | Show current version                                                 |

### Global flags

| Flag               | Description                        |
|--------------------|------------------------------------|
| `--verbose`        | Extra diagnostic output            |
| `--quiet`          | Suppress non-essential output      |
| `--json`           | Machine-readable JSON output       |
| `--no-interactive` | Skip prompts (auto-detected in CI) |

## Xdebug management

Toggle xdebug without editing `php.ini` manually:

```powershell
shp xdebug on          # enable (mode=debug)
shp xdebug coverage    # switch to coverage mode
shp xdebug off         # disable
shp xdebug toggle      # quick on/off
```

Works on the PHP version resolved for the current project.

## Running with a different version

Test compatibility with another PHP version without modifying `.phpversion`:

```powershell
shp run 8.3 -- php artisan test
shp run 8.3 -- composer install
shp run latest -- php -v
```

The resolved version applies only to that command. Your `.phpversion` stays untouched.

With `--json`, both `shp run` and `shp xdebug run` capture and return structured output:

```powershell
shp --json run 8.3 -- php -v
# {"exitCode":0,"phpVersion":"8.3","stdout":"PHP 8.3.12 ...","stderr":""}
```

## Auto-detection from composer.json

Don't know which version to pin? Let Shepherd figure it out:

```powershell
shp use auto
```

Reads `require.php` from `composer.json`, picks the highest installed version that satisfies the constraint, and writes `.phpversion`. Useful for onboarding onto an existing project.

## Extension management

Install extensions not bundled with Herd — no manual DLL download:

```powershell
shp ext add redis
shp ext add imagick
shp ext add sqlsrv --php=all   # all installed versions
```

Supported: `igbinary`, `imagick`, `memcached`, `pdo_sqlsrv`, `redis`, `sqlsrv`.

Handles PECL lookup, DLL download, system deps (winget), ini registration, and verification.

List what's installed and remove what you don't need:

```powershell
shp ext list               # show extensions for the current PHP version
shp ext remove redis       # remove DLL and php.ini directive
shp ext remove redis --php=all
```

## Self-update

```powershell
shp self-update
```

Downloads the latest release, verifies SHA256, and replaces all shims. Releases are signed with [cosign](https://docs.sigstore.dev) — see [SECURITY.md](SECURITY.md).

## Multicall binary

The binary detects how it was invoked:

| Invoked as                  | Behavior                              |
|-----------------------------|---------------------------------------|
| `php` / `php.exe`           | Transparent PHP shim                  |
| `composer` / `composer.exe` | Runs `composer.phar` via resolved PHP |
| `shp.exe`                   | Management commands                   |

One binary, three names — `shp install` sets them all up.

## IDE integration

Point your IDE's PHP interpreter to the Shepherd shim so static analysis matches your terminal:

**PhpStorm:**
Settings → PHP → CLI Interpreter → `...` → Add Local → path:

```
%USERPROFILE%\.config\shepherd\bin\php.exe
```

**VS Code (Intelephense / PHP Intelephense):**
In `.vscode/settings.json`:

```json
{
  "php.validate.executablePath": "${env:USERPROFILE}/.config/shepherd/bin/php.exe"
}
```

The shim resolves the correct PHP version per project, so the IDE always uses the same binary as your terminal.

## Troubleshooting

```powershell
shp doctor
```

Checks: Herd presence, `.phpversion` validity, shim installation, PATH order, shell aliases, Developer Mode, CA certificate, nginx config, PHP-CGI ports.

Common fixes:
- **Wrong PHP version** → `shp status` to check PATH order, then `shp install` + restart terminal
- **Version not found** → install it from the Herd UI
- **nginx errors** → `shp doctor` will report the file and line

## Build from source

```powershell
go build -ldflags="-s -w -X main.version=dev" -o shp.exe .
.\shp.exe install
```

## License

[MIT](LICENSE)
