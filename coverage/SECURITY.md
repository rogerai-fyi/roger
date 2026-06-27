# Security Policy

## Reporting a vulnerability

Please report security issues **privately** - do not open a public issue or PR.

- Preferred: GitHub **private vulnerability reporting** (this repo → *Security* → *Report a vulnerability*).
- Or email **security@rogerai.fyi**.

We aim to acknowledge within 72 hours. Include reproduction steps and impact; we'll
coordinate a fix and a disclosure timeline with you.

## Scope

The broker (`cmd/rogerai-broker`), the CLI + node agent (`cmd/rogerai`, `internal/`),
and the install script (`web/install.sh`). Vulnerabilities in third-party
dependencies should also be reported upstream.

## Secrets

Never commit secrets. All runtime config - database URL, Stripe keys, the broker
signing key - is supplied via environment variables / a gitignored `.env`
(see `.env.example`). The broker stores only token counts and signed receipts,
never prompt or response content (see `PRIVACY.md`).
