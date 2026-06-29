# EDGE - Cloudflare rules for the RogerAI site

The site is hosted on **DigitalOcean App Platform** (origin) behind a **Cloudflare**
CDN/DNS proxy. It is **not** Cloudflare Pages. You can confirm the shape from the live
response headers:

```sh
curl -sSI https://rogerai.fyi/ | grep -iE 'server|x-do-app-origin|cf-ray'
# server: cloudflare         <- Cloudflare proxy in front
# x-do-app-origin: <uuid>    <- DigitalOcean App Platform origin
# cf-ray: ...
```

Because DO App Platform static sites can't emit custom response headers, the two
Cloudflare-Pages-style files in the repo are **not honored by the host**:

- [`src/_headers`](src/_headers)   - the security headers (CSP, HSTS, X-Frame-Options, ...)
- [`src/_redirects`](src/_redirects) - the `www -> apex` 301

As of this writing **none** of those security headers are on the live response, and
`https://www.rogerai.fyi/` returns `200` instead of a `301`. The fix is to mirror both to
the Cloudflare edge. The repo files stay the **source of truth**; the script below reads them
so the edge never drifts.

---

## Quickest path: the apply script

[`scripts/cf-edge.mjs`](scripts/cf-edge.mjs) reads `src/_headers` + `src/_redirects` and
writes two Cloudflare rulesets via the API. It is **dry-run by default** (prints the exact
payloads, changes nothing) and idempotent + non-destructive (it preserves any other rules you
already have in those phases, replacing only the two it owns by `description`).

1. **Make a token.** Cloudflare dashboard -> My Profile -> API Tokens -> Create Token ->
   *Custom token* with permissions **Zone -> Zone -> Read** and **Zone -> Zone Rulesets ->
   Edit**, scoped to the `rogerai.fyi` zone.

2. **Dry-run** (no token needed to preview the payloads):
   ```sh
   node web/scripts/cf-edge.mjs
   ```

3. **Roll out the CSP in Report-Only first** (it's the one header that can break the site if
   an inline-script hash is stale - Report-Only logs violations without blocking):
   ```sh
   CF_API_TOKEN=*** node web/scripts/cf-edge.mjs --apply --report-only
   ```
   Load the site, open DevTools -> Console, click around (login, dashboard, console). If there
   are **no** CSP violation reports, enforce it:
   ```sh
   CF_API_TOKEN=*** node web/scripts/cf-edge.mjs --apply
   ```

4. **Verify:**
   ```sh
   curl -sSI https://rogerai.fyi/        | grep -iE 'content-security|strict-transport|x-frame|x-content-type|referrer|permissions'
   curl -sSI https://www.rogerai.fyi/    | grep -i location     # -> https://rogerai.fyi/
   ```

---

## Manual path: paste these into the dashboard

If you'd rather click than run the script, both rules live under **Rules -> ...** on the
`rogerai.fyi` zone. Header values must match [`src/_headers`](src/_headers) exactly.

### 1) Security headers - Transform Rule (Modify Response Header)

**Rules -> Transform Rules -> Modify Response Header -> Create rule.**

- **Name:** `rogerai:security-headers`
- **When incoming requests match** (Edit expression):
  ```
  (http.host in {"rogerai.fyi" "www.rogerai.fyi"})
  ```
- **Then... Set static** - one entry per header (copy the values from `src/_headers`):

  | Header | Value |
  | --- | --- |
  | `X-Frame-Options` | `DENY` |
  | `X-Content-Type-Options` | `nosniff` |
  | `Referrer-Policy` | `strict-origin-when-cross-origin` |
  | `Permissions-Policy` | `geolocation=(), microphone=(), camera=(), interest-cohort=()` |
  | `Strict-Transport-Security` | `max-age=63072000; includeSubDomains; preload` |
  | `Cross-Origin-Opener-Policy` | `same-origin-allow-popups` |
  | `Content-Security-Policy` | *(the full CSP line from `src/_headers`)* |

  > **CSP caution.** Start with the header named `Content-Security-Policy-Report-Only`,
  > confirm no console violations, then rename it to `Content-Security-Policy` to enforce. The
  > CSP carries three `sha256` inline-script hashes; if a page's inline `<script>` changes,
  > regenerate the hash in `src/_headers` (and re-apply) or the script will be blocked.

  > **HSTS preload.** The value includes `preload`. To actually join the preload list, submit
  > the apex at <https://hstspreload.org> once enforced. (Cloudflare also has a native HSTS
  > toggle under SSL/TLS -> Edge Certificates if you'd prefer to manage HSTS there.)

### 2) www -> apex - Redirect Rule

**Rules -> Redirect Rules -> Create rule** (Single Redirects).

- **Name:** `rogerai:www-to-apex`
- **When incoming requests match:**
  ```
  (http.host eq "www.rogerai.fyi")
  ```
- **Then... URL redirect**, type **Dynamic**:
  - **Expression:** `concat("https://rogerai.fyi", http.request.uri.path)`
  - **Status:** `301`
  - **Preserve query string:** on

---

## Scope notes

- The header rule is scoped to the site hosts (`rogerai.fyi`, `www.rogerai.fyi`), **not** the
  `broker.rogerai.fyi` API, so the HTML-oriented CSP never lands on JSON API responses. If you
  later want the basic hardening headers (HSTS, `nosniff`, `X-Frame-Options`) on the broker
  too, either widen the expression or set them in the broker's Go handlers (spec-first, per
  `CLAUDE.md`).
- `COOP: same-origin-allow-popups` is set so the "Sign in with Apple" JS **popup** flow can
  talk back to its opener (`window.opener`); plain `same-origin` severs that link and breaks
  the relay. The GitHub OAuth **redirect** flow is unaffected either way.
- `src/_headers` also has per-path `Content-Type` blocks (for `install.sh` etc.); those are
  advisory on this stack too. The installer is served correctly today because `curl ... | sh`
  ignores content type - revisit only if a browser needs the right type for those paths.
