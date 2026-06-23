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

## Supply chain protections

- All downloads (self-update, PECL extensions) enforce HTTPS only.
- Self-update download URLs are validated against a domain allowlist (github.com, objects.githubusercontent.com).
- Download sizes are capped to prevent resource exhaustion.
- Zip extraction includes zip-slip protection (path traversal prevention).
- Extension names are validated against a strict regex before being used in URLs.
- The `checksums.txt` must be present in the release — self-update refuses to proceed without it.
