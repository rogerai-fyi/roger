# v5.7.1 — dependency safety and an auditable broker build

This patch repairs two release-audit failures found after v5.7.0.

## Security

- Upgrade `golang.org/x/text` to v0.39.0, fixing GO-2026-5970 / CVE-2026-56852:
  malformed UTF-8 could trap Unicode normalization in an infinite loop on reachable
  RogerAI paths.
- Move capsule HKDF to Go 1.25's standard library and remove RogerAI's direct use of
  `golang.org/x/crypto`. The module remains an indirect SEV-attestation dependency;
  `govulncheck` confirms RogerAI imports neither its unmaintained OpenPGP package nor
  any other vulnerable package or symbol.
- Run `govulncheck` against every package on both pushes to `main` and tagged releases.
  A release can no longer publish merely because tests pass while a known reachable Go
  vulnerability remains.

## Operations

- `GET /version` now reports the broker semantic version and, in production, the exact
  full source commit supplied by the deployment platform.
- Version responses use `Cache-Control: no-store`, so a rolling deployment cannot be
  hidden behind stale intermediary state.
- Invalid or absent commit metadata is omitted instead of being presented as trusted.
  The same exact commit is included in the admin live-health block when available.

No API client or database migration is required.

## Still deliberately gated

Winget publication remains off until `RogerAI.Roger` has its one-time initial manifest
accepted upstream. macOS signing/notarization is already wired but requires the Apple
Developer credentials documented in `packaging/README.md`; Windows Authenticode signing
still requires a certificate or managed signing identity. The release continues to publish
checksum-verified binaries while those identity credentials are unavailable.
