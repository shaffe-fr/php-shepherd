# Security

## Reporting vulnerabilities

If you discover a security vulnerability, please report it privately via [GitHub Security Advisories](https://github.com/shaffe-fr/php-shepherd/security/advisories/new) rather than opening a public issue.

## Release integrity

All release artifacts are built in GitHub Actions and include cryptographic verification:

- **SHA256 checksums** — every release includes a `checksums.txt` containing the SHA256 hash of each archive.
- **Cosign signature** — the `checksums.txt` file is signed using [Sigstore cosign](https://docs.sigstore.dev) with keyless OIDC signing, proving the artifacts were produced by the official CI workflow.

### Self-update verification

`shp self-update` automatically verifies the SHA256 checksum of downloaded archives before installing. If the checksum doesn't match, the update is refused.

### Manual verification

Download the release assets (`checksums.txt`, `checksums.txt.sig`, `checksums.txt.pem`) and the archive you want to verify.

#### 1. Verify the cosign signature

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp "github\.com/shaffe-fr/php-shepherd" \
  checksums.txt
```

This proves the checksums file was produced by the official GitHub Actions workflow for this repository.

#### 2. Verify the archive checksum

```powershell
# PowerShell
(Get-FileHash php-shepherd_*_windows_amd64.zip -Algorithm SHA256).Hash
# Compare with the corresponding line in checksums.txt
```

```bash
# Bash / WSL
sha256sum -c checksums.txt --ignore-missing
```

## Antivirus false positives

Shepherd is not yet Authenticode-signed, so Windows Defender, Avast and similar
engines occasionally flag `shp.exe` on reputation or heuristics alone. Four of its
normal behaviours overlap with the "dropper plus persistence" pattern these engines
look for:

- `shp install` copies the binary into `%USERPROFILE%\.config\shepherd\bin` under
  three names (`php.exe`, `composer.exe`, `shp.exe`), and `shp guard enable` adds a
  fourth (`shp-guard.exe`) — a self-replicating executable.
- Installation prepends that directory to `HKCU\Environment\Path` and broadcasts
  `WM_SETTINGCHANGE` — PATH modification.
- The PATH Guard registers a `schtasks` entry that runs at logon — startup persistence.
- `shp install --force` enumerates processes and terminates the ones running from
  the shim directory.

None of these touch machine-wide state: every write stays inside your user profile
and `HKCU`. Shepherd avoids the patterns that would make this worse — no process
snapshot enumeration, no shelling out to PowerShell for process discovery, and DLL
loading is restricted to `System32` via `NewLazySystemDLL`.

If your scanner quarantines the binary, verify the release before reporting a false
positive: check the SHA256 against `checksums.txt` and the cosign signature as
described above. A matching checksum plus a valid signature proves the binary is the
one GitHub Actions built.

## Supply chain protections

- All downloads (self-update, PECL extensions) enforce HTTPS only.
- Self-update download URLs are validated against a domain allowlist (github.com, objects.githubusercontent.com).
- Download sizes are capped to prevent resource exhaustion.
- Zip extraction includes zip-slip protection (path traversal prevention).
- Extension names are validated against a strict regex before being used in URLs or
  resolved to a file path, for installation and removal alike.
- `--ext-version` and `--vs` are format-checked before being interpolated into a
  download URL.
- The `checksums.txt` must be present in the release — self-update refuses to proceed without it.
