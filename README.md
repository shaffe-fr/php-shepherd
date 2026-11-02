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

- [Laravel Herd](https://herd.laravel.com) for Windows — both Herd 1.x and Herd 2.x are supported automatically (no configuration; see [Herd 2: workspaces & subdomains](#herd-2-workspaces--subdomains))
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
3. Falls back to `herd.phar which-php` when no dotfile is found, or to Herd's global version (`herd\bin\php.bat`, or `activeVersion` in `config\config.json` on Herd 2) when the Herd app is closed
4. Syncs Herd's nginx config so your `.test` domain matches the CLI version (on Herd 2, including workspace subdomains)
5. Execs the real `php.exe` — transparent, no wrapper overhead

## Herd 2: workspaces & subdomains

[Herd 2.0](https://herd.laravel.com) adds **workspaces**: isolated checkouts of a site, each served on its own subdomain (`<branch>.<parent-site>.<tld>`, e.g. `feature-auth.shop.test`). Shepherd supports them without any configuration:

- **Version detection** — Herd 2 is detected on disk from `~/.config/herd/config/herd.json`, the workspace registry, or an `apiPort` in `config/config.json` (none exist on 1.x); any one of them is enough. Everything works offline; the desktop app is only needed to (re)start services, and Shepherd tells you so instead of silently no-op'ing.
- **Workspaces are just sites** — Herd 2 links a workspace as a junction in `~/.config/herd/config/valet/Sites`, registers it in `config/valet/workspaces.json`, and drops a `.herd-workspace.json` marker at the checkout root. Shepherd resolves all three, so `shp use`, `shp which`, `shp status`, nginx syncing and `shp reverb` work inside a workspace exactly as they do for a regular site — on its own subdomain.
- **Unadopted git worktrees** — a plain `git worktree add` checkout of a served site that Herd has not adopted yet is not a Herd site. When you run PHP inside one (with a `.phpversion`), Herd does not serve it until it is linked (its subdomain does not resolve before that, as Windows has no wildcard DNS); Shepherd detects the worktree (from its `.git` pointer, without invoking git) and prints a rate-limited hint:

  ```
  shp: this directory is a git worktree of Herd site "shop" (branch "feature/auth"), not a Herd workspace yet.
    Herd does not serve it until it is linked. Adopt it to serve it as feature-auth.shop.test:
      herd workspace:adopt C:\Users\me\worktrees\feature-auth
  ```

  `shp doctor` also lists such worktrees.
- **Service restarts** — on Herd 2 the phar restarts nginx / PHP-CGI through the app's local HTTP API (`apiPort` from `config\config.json`). With the app closed, Shepherd skips the no-op call and tells you to open Herd (the nginx config itself is already updated on disk).
- **Bootstrap** — to run `herd.phar`, Shepherd picks the newest non-RC build recorded in `config\php.json` on Herd 2, falling back to the highest installed PHP. It deliberately does not run Herd's `bin\find-usable-php.php`, which shells out to `php` and would re-enter Shepherd's own shim.

## Commands

| Command              | Description                                                                |
|----------------------|----------------------------------------------------------------------------|
| `shp use [version]`  | Set the project's PHP version (`latest`, `auto`); lists versions if omitted |
| `shp run <ver> -- …` | Run a command with a specific PHP version                                  |
| `shp which`          | Show resolved PHP path and source                                          |
| `shp current`        | Print the resolved PHP version                                             |
| `shp list`, `ls`     | List available PHP versions                                                |
| `shp status`         | Show configuration overview                                                |
| `shp xdebug <cmd>`   | Manage xdebug (`on`, `off`, `toggle`, `profile`, `trace`, `status`, `run`) |
| `shp ext add <name>` | Install a PHP extension (DLL + deps + ini)                                 |
| `shp ext list`       | List installed extensions (alias `ls`)                                     |
| `shp ext remove <n>` | Remove an installed extension (alias `rm`)                                 |
| `shp coverage <cmd>` | Manage the code coverage driver (`status`, `install`, `run`)               |
| `shp reverb [cmd]`   | Show Laravel Reverb status (`status`, `env`)                               |
| `shp doctor`         | Diagnose common setup issues                                               |
| `shp self-update`    | Update to the latest release (SHA256-verified)                             |
| `shp install`        | Install shims and configure PATH                                           |
| `shp guard <cmd>`    | Manage the optional PATH Guard (`enable`, `disable`, `status`)             |
| `shp uninstall`      | Remove shims and restore PATH                                              |
| `shp version`        | Show current version (`--version`, `-v`)                                   |
| `shp help`           | Show built-in help (`-h`, `--help`)                                        |

### Global flags

| Flag               | Description                        |
|--------------------|------------------------------------|
| `--verbose`        | Extra diagnostic output            |
| `--quiet`, `-q`    | Suppress non-essential output      |
| `--json`           | Machine-readable JSON output       |
| `--no-interactive` | Skip prompts (auto-detected in CI) |

### Command flags

| Flag                     | Applies to | Description                                                     |
|--------------------------|------------|-----------------------------------------------------------------|
| `--force`, `-f`          | `install`  | Reinstall, killing running shim processes first                 |
| `--enable-path-guard`    | `install`  | Enable the PATH Guard without prompting                         |
| `--php=X.Y`, `--php=all` | `ext *`    | Target a specific PHP version, or all of them                   |
| `--ext-version=V`        | `ext add`  | Pin the extension version (default: latest PECL)                |
| `--ts`                   | `ext add`  | Use the Thread Safe build (default: NTS)                        |
| `--vs=vsNN`              | `ext add`  | Visual Studio build tag (default: derived from the PHP version) |
| `--port=PORT`            | `reverb`   | Reverb listen port (default: 8443)                              |

## Xdebug management

Toggle xdebug without editing `php.ini` manually:

```powershell
shp xdebug on          # enable (mode=debug)
shp xdebug coverage    # switch to coverage mode
shp xdebug profile     # enable profiling (sets output_dir to .)
shp xdebug trace       # enable function tracing (sets output_dir to .)
shp xdebug off         # disable
shp xdebug toggle      # quick on/off
```

Works on the PHP version resolved for the current project.

### Stateless xdebug

Profile or trace a single command without touching your config — one-off, no side effects:

```powershell
shp xdebug run trace -- php artisan migrate
shp xdebug run profile -- php artisan test
shp xdebug run debug -- php script.php
```

Xdebug is injected for that invocation only, then gone.

## Code coverage

Coverage has two drivers, and `php-code-coverage` picks one on its own — preferring
pcov, which is several times faster than xdebug. `shp coverage` resolves the driver
per command instead of leaving both enabled in `php.ini`:

```powershell
shp coverage install                                    # add pcov (all versions: --php=all)
shp coverage status                                      # which driver would be used
shp coverage run -- php artisan test --coverage
shp coverage run -- vendor/bin/phpunit --coverage-text
```

pcov is installed with `pcov.enabled=0`, so it costs nothing on normal runs;
`coverage run` enables it for that invocation only. Nothing is written to `php.ini`.

pcov reports line coverage only. For branch or path coverage, use `shp xdebug coverage`.

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

Supported: `brotli`, `igbinary`, `imagick`, `memcached`, `pcov`, `pdo_sqlsrv`, `redis`, `sqlsrv`.

Handles PECL lookup, DLL download, system deps (winget), ini registration, and verification.

List what's installed and remove what you don't need:

```powershell
shp ext list               # show extensions for the current PHP version
shp ext list --php=8.3     # or for a specific one
shp ext remove redis       # remove DLL and php.ini directive
shp ext remove redis --php=all
```

Pin a build when the defaults don't fit:

```powershell
shp ext add redis --ext-version=6.0.2   # instead of the latest PECL release
shp ext add redis --ts                  # Thread Safe build (default: NTS)
shp ext add redis --vs=vs16             # older Visual Studio build tag
```

## Laravel Reverb

Reverb serves WebSockets directly over TLS using Herd's certificates, so no nginx reverse proxy is needed.

```powershell
shp reverb              # connectivity status for the current project
shp reverb env          # the .env variables to copy
shp reverb --port=9000  # non-default listen port (default: 8443)
```

## Self-update

```powershell
shp self-update
```

Downloads the latest release, verifies SHA256, and replaces all shims. Releases are signed with [cosign](https://docs.sigstore.dev) — see [SECURITY.md](SECURITY.md).

## PATH Guard (opt-in)

Herd updates can silently reorder your User PATH, putting its own `bin` directory back before Shepherd. The PATH Guard is a lightweight background process that watches `HKCU\Environment\Path` and restores the correct order automatically.

```powershell
shp guard enable    # create a per-user scheduled task (runs at login)
shp guard status    # check if the guard is active
shp guard disable   # remove the task
```

The guard:
- Runs with standard user privileges, no admin required
- Uses no network access
- Is proposed during `shp install` (default: No)
- Can be enabled non-interactively: `shp install --enable-path-guard`
- Is automatically stopped and removed by `shp uninstall`
- Is refreshed when you run `shp self-update`

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
- **Antivirus flags `shp.exe`** → expected on an unsigned binary that copies itself and edits your PATH; see [SECURITY.md](SECURITY.md#antivirus-false-positives)

## Build from source

```powershell
go build -ldflags="-s -w -X main.version=dev" -o shp.exe .
.\shp.exe install
```

## License

[MIT](LICENSE)
