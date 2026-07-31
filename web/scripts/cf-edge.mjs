#!/usr/bin/env node
// cf-edge.mjs - mirror the site's security headers + www->apex redirect to the Cloudflare
// edge, because the host (DigitalOcean App Platform) does not read web/src/_headers or
// web/src/_redirects (those are Cloudflare-Pages/Netlify conventions). See web/EDGE.md.
//
// It reads the VALUES from web/src/_headers and web/src/_redirects (single source of truth,
// so the edge never drifts from the repo) and writes two Cloudflare rulesets via the API:
//   - http_response_headers_transform  : one rule that sets the security headers on the site
//   - http_request_dynamic_redirect    : one rule that 301s www -> apex
//
// Idempotent + non-destructive: it preserves any of your other rules in those phases and only
// replaces the two it owns (matched by description). DRY-RUN BY DEFAULT - prints the exact
// payloads and changes nothing until you pass --apply.
//
//   CF_API_TOKEN=...  node web/scripts/cf-edge.mjs                 # dry-run (no network writes)
//   CF_API_TOKEN=...  node web/scripts/cf-edge.mjs --apply         # write the rules
//   CF_API_TOKEN=...  node web/scripts/cf-edge.mjs --apply --report-only   # CSP as Report-Only
//   CF_API_TOKEN=...  node web/scripts/cf-edge.mjs --legacy-redirect --apply  # legacy -> canonical
//
// Env: CF_API_TOKEN (or CLOUDFLARE_API_TOKEN) - required for --apply; a token with Zone:Read +
//      Zone Transform Rules:Edit + Dynamic URL Redirects:Edit on the rogerai.fm zone.
//      CF_ZONE overrides the zone name (default rogerai.fm).
//      CF_LEGACY_ZONE overrides the legacy zone (default rogerai.fyi) for --legacy-redirect.
//
// Dependency-free: Node >=18 (global fetch). No npm install.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SRC = path.join(HERE, "..", "src");
const ZONE = process.env.CF_ZONE || "rogerai.fm";
const LEGACY_ZONE = process.env.CF_LEGACY_ZONE || "rogerai.fyi";
const APEX = ZONE;
const WWW = "www." + ZONE;
const API = "https://api.cloudflare.com/client/v4";

const apply = process.argv.includes("--apply");
const reportOnly = process.argv.includes("--report-only");
const check = process.argv.includes("--check");
const legacyRedirect = process.argv.includes("--legacy-redirect");

// stable markers so re-runs update our rules instead of stacking duplicates.
const DESC_HEADERS = "rogerai:security-headers";
const DESC_REDIRECT = "rogerai:www-to-apex";
const DESC_LEGACY = "rogerai:legacy-to-canonical";
const DESC_VANITY = "rogerai:go-vanity-import";

// ---- parse the `/*` block of web/src/_headers into an ordered {name,value} list ----------
export function parseHeaders() {
  const lines = readFileSync(path.join(SRC, "_headers"), "utf8").split("\n");
  const out = [];
  let inStar = false;
  for (const raw of lines) {
    const trimmed = raw.trim();
    if (!inStar) { if (trimmed === "/*") inStar = true; continue; }
    if (trimmed === "" || trimmed.startsWith("#")) break;   // blank / comment ends the block
    if (!/^\s/.test(raw)) break;                            // a non-indented line = next path
    const i = trimmed.indexOf(":");
    if (i < 0) continue;
    out.push({ name: trimmed.slice(0, i).trim(), value: trimmed.slice(i + 1).trim() });
  }
  if (!out.length) throw new Error("no headers parsed from src/_headers /* block");
  return out;
}

// ---- parse the single www->apex line of web/src/_redirects (sanity-check only) -----------
function parseRedirect() {
  const line = readFileSync(path.join(SRC, "_redirects"), "utf8")
    .split("\n").map((l) => l.trim()).find((l) => l && !l.startsWith("#"));
  if (!line || !/www\./.test(line)) throw new Error("no www redirect line found in src/_redirects");
  return line; // informational; the rule below encodes the same intent declaratively
}

// ---- build the rule objects ---------------------------------------------------------------
// Every expression below is an EXACT host test (`in {...}` / `eq`), never a suffix or wildcard
// match. That is load-bearing: broker.* carries API, SSE and WebSocket traffic that must never
// be redirected or rewritten, and the compatibility contract keeps the legacy broker live
// indefinitely for already-installed clients.
export function headerRule(headers, opts = {}) {
  const zone = opts.zone || ZONE;
  const asReportOnly = opts.reportOnly ?? reportOnly;
  const map = {};
  for (const { name, value } of headers) {
    const key = asReportOnly && name.toLowerCase() === "content-security-policy"
      ? "Content-Security-Policy-Report-Only" : name;
    map[key] = { operation: "set", value };
  }
  return {
    action: "rewrite",
    action_parameters: { headers: map },
    expression: `(http.host in {"${zone}" "www.${zone}"})`,
    description: DESC_HEADERS,
    enabled: true,
  };
}

// Certificate issuance and renewal fetch http://<host>/.well-known/acme-challenge/<token>.
// A redirect that matches that path hands the certificate authority a 301 instead of the
// token, so the hostname never gets a certificate. That is not theoretical: it is exactly
// why www.rogerai.fm sat at DomainCertPendingValidation while the apex, which has no
// redirect, validated immediately. Every redirect below must carry this exclusion, and it
// matters at renewal just as much as at first issuance.
const ACME_EXCLUSION =
  'not starts_with(http.request.uri.path, "/.well-known/acme-challenge/")';

export function redirectRule(opts = {}) {
  const zone = opts.zone || ZONE;
  return {
    action: "redirect",
    action_parameters: {
      from_value: {
        status_code: 301,
        target_url: { expression: `concat("https://${zone}", http.request.uri.path)` },
        preserve_query_string: true,
      },
    },
    expression: `(http.host eq "www.${zone}" and ${ACME_EXCLUSION})`,
    description: DESC_REDIRECT,
    enabled: true,
  };
}

// One permanent, path-preserving hop from the legacy site to the canonical one. Lives in the
// LEGACY zone. Matches the legacy apex and www only - broker and control keep serving.
export function legacyRedirectRule(opts = {}) {
  const legacy = opts.legacyZone || LEGACY_ZONE;
  const canonical = opts.canonicalZone || ZONE;
  return {
    action: "redirect",
    action_parameters: {
      from_value: {
        status_code: 301,
        target_url: { expression: `concat("https://${canonical}", http.request.uri.path)` },
        preserve_query_string: true,
      },
    },
    expression: `(http.host in {"${legacy}" "www.${legacy}"} and ${ACME_EXCLUSION})`,
    description: DESC_LEGACY,
    enabled: true,
  };
}

// The live zone still carries the single-path 2026-07-30 rule (/roger only). The two-path,
// request-derived rule below has NOT been applied yet, so `--check` reports drift for
// /roger/v5 until someone re-runs `--apply` against the zone. That is the intended order:
// the site must ship /roger/v5/ first, or the redirect would point at a 404.
//
// `go get rogerai.fm/roger/v5` fetches the module path with NO trailing slash, and this
// host does no extensionless resolution - measured, not assumed:
//     https://rogerai.fm/manual       404
//     https://rogerai.fm/manual.html  200
// so /roger 404s while /roger/ serves the go-import page. Without this hop the module
// is go-gettable by no path at all, including for third parties importing the
// Apache-2.0 protocol carve-out. preserve_query_string is load-bearing: Go appends
// ?go-get=1 and drops the request if the redirect eats it.
export function vanityImportRule(opts = {}) {
  const zone = opts.zone || ZONE;
  return {
    action: "redirect",
    action_parameters: {
      from_value: {
        status_code: 301,
        // Trailing slash derived from the request, so /roger/v5 lands on /roger/v5/ rather
        // than on /roger/ - which would answer with a tag for the wrong module path.
        target_url: { expression: `concat("https://${zone}", http.request.uri.path, "/")` },
        preserve_query_string: true,
      },
    },
    // Exact set membership, never a prefix: starts_with(..., "/roger") would also swallow
    // /roger-ios and every future page whose name begins with "roger". Both the bare path
    // and the major-version path are listed because Go fetches the FULL module path.
    // The ACME exclusion is redundant against an exact-path test and carried anyway: "every
    // redirect carries this" is only a guarantee if it holds without a case-by-case argument.
    expression: `(http.host eq "${zone}" and http.request.uri.path in {"/roger" "/roger/v5"} and ${ACME_EXCLUSION})`,
    description: DESC_VANITY,
    enabled: true,
  };
}

// The legacy redirect is only safe once the canonical site actually answers over TLS -
// enabling it earlier would send every visitor and every search result into a dead host.
export async function canonicalSiteReady(host, fetchImpl = fetch) {
  try {
    const res = await fetchImpl(`https://${host}/`, { method: "GET", redirect: "manual" });
    if (res.status !== 200) {
      return { ready: false, reason: `https://${host}/ returned ${res.status}, expected 200` };
    }
    return { ready: true, reason: `https://${host}/ answered 200 over a valid certificate` };
  } catch (e) {
    return {
      ready: false,
      reason: `https://${host}/ is unreachable or its certificate is not yet valid: ${e.message}`,
    };
  }
}

// ---- Cloudflare API helpers ---------------------------------------------------------------
async function cf(method, urlPath, body) {
  const token = process.env.CF_API_TOKEN || process.env.CLOUDFLARE_API_TOKEN;
  if (!token) throw new Error("CF_API_TOKEN / CLOUDFLARE_API_TOKEN is not set");
  const res = await fetch(API + urlPath, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const json = await res.json().catch(() => ({}));
  if (!json.success && res.status !== 404) {
    throw new Error(`CF ${method} ${urlPath} -> ${res.status}: ${JSON.stringify(json.errors || json)}`);
  }
  return { status: res.status, json };
}

async function zoneId(name = ZONE) {
  const { json } = await cf("GET", `/zones?name=${encodeURIComponent(name)}`);
  const id = json.result && json.result[0] && json.result[0].id;
  if (!id) throw new Error(`zone ${name} not found (check the token's zone access)`);
  return id;
}

// GET the phase entrypoint, drop our own rule (by description), append the new one, PUT back.
async function upsert(zid, phase, rule) {
  const { json } = await cf("GET", `/zones/${zid}/rulesets/phases/${phase}/entrypoint`);
  const existing = (json.result && json.result.rules) || [];
  const kept = existing.filter((r) => r.description !== rule.description);
  const rules = [...kept.map(stripReadOnly), rule];
  await cf("PUT", `/zones/${zid}/rulesets/phases/${phase}/entrypoint`, { rules });
  return { phase, kept: kept.length, total: rules.length };
}

// the API returns server-managed fields on GET that it rejects on PUT; keep only writable ones.
function stripReadOnly(r) {
  const { action, action_parameters, expression, description, enabled, ref } = r;
  return { action, action_parameters, expression, description, enabled, ...(ref ? { ref } : {}) };
}

// ---- main ---------------------------------------------------------------------------------
// Importable: the CLI only runs when this file is executed directly, so the tests can drive
// the rule builders above without triggering network writes or process.exit.
const isMain = process.argv[1]
  && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);

if (isMain) {
const headers = parseHeaders();
const redirectLine = parseRedirect();
const hRule = headerRule(headers);
const rRule = redirectRule();
const lRule = legacyRedirectRule();

console.log(`zone:        ${ZONE}`);
console.log(`headers:     ${headers.length} (${headers.map((h) => h.name).join(", ")})`);
console.log(`csp mode:    ${reportOnly ? "Content-Security-Policy-Report-Only (test)" : "Content-Security-Policy (enforce)"}`);
console.log(`redirect:    ${redirectLine}`);
console.log("");

if (check) {
  // DRIFT CHECK (no token, no writes): the live site's response headers must match the
  // repo's _headers block, and www must 301 to the apex. This is the end-to-end truth -
  // if someone edits _headers without re-running --apply, this is what catches it (CI
  // runs it on _headers/_redirects changes + a weekly cron).
  const drift = [];
  // Both probes are wrapped: a host that is unreachable or has no valid certificate is
  // a REPORTABLE edge condition, not a reason to die with a stack trace. www currently
  // has no certificate covering it, so an unguarded fetch here aborts the whole check
  // with "TypeError: fetch failed" and tells the reader nothing about what is wrong.
  const probe = async (url) => {
    try {
      return { res: await fetch(url, { method: "HEAD", redirect: "manual" }) };
    } catch (err) {
      const cause = err?.cause?.message || err?.message || String(err);
      return { err: `${url} is unreachable or has no valid certificate: ${cause}` };
    }
  };

  const apex = await probe(`https://${APEX}/`);
  if (apex.err) {
    drift.push(apex.err);
  } else {
    for (const h of headers) {
      const got = apex.res.headers.get(h.name);
      if (got === null) drift.push(`missing on the live edge: ${h.name}`);
      else if (got.trim() !== h.value) drift.push(`value drift: ${h.name}\n  repo: ${h.value}\n  live: ${got.trim()}`);
    }
  }

  const www = await probe(`https://${WWW}/`);
  if (www.err) {
    drift.push(www.err);
  } else {
    const loc = www.res.headers.get("location") || "";
    if (www.res.status !== 301 || !loc.startsWith(`https://${APEX}`)) {
      drift.push(`www redirect drift: expected 301 -> https://${APEX}/..., got ${www.res.status} -> ${loc || "(none)"}`);
    }
  }
  // The vanity-import hop is the ONLY thing that makes `go get rogerai.fm/roger/v5` resolve,
  // and nothing else fails loudly if it disappears: verify-artifacts treats its go-import
  // result as advisory, and a rule deleted in the Cloudflare dashboard leaves no trace in
  // the repo. Check it here, where drift is the whole point of the job.
  // BOTH paths are probed. /roger/v5 is the one Go actually fetches - it filters candidate
  // tags by the module path's major suffix - so checking only /roger would leave the real
  // route unguarded: dropping "/roger/v5" from the rule would keep this check green while
  // `go install` was broken. Each path must land on ITS OWN path plus a slash; asserting a
  // substring like "/roger/" would accept /roger/v5 collapsing onto /roger/, which is
  // precisely the regression the request-derived target expression exists to prevent.
  for (const path of ["/roger", "/roger/v5"]) {
    const vanity = await probe(`https://${APEX}${path}?go-get=1`);
    if (vanity.err) {
      drift.push(vanity.err);
      continue;
    }
    const loc = vanity.res.headers.get("location") || "";
    const want = `https://${APEX}${path}/`;
    if (vanity.res.status !== 301 || !loc.startsWith(want)) {
      drift.push(
        `vanity-import redirect drift for ${path}: expected 301 -> ${want}, ` +
          `got ${vanity.res.status} -> ${loc || "(none)"}; ` +
          "`go install rogerai.fm/roger/v5/cmd/rogerai@latest` is broken while this is wrong",
      );
    } else if (!loc.includes("go-get=1")) {
      // Go drops the response when the redirect eats its query, so a 301 to the right
      // path is still a broken module fetch without this.
      drift.push(`vanity-import redirect for ${path} drops ?go-get=1: ${loc}`);
    }
  }

  if (drift.length) {
    console.error(`EDGE DRIFT (${drift.length}): the live Cloudflare edge does not match web/src/_headers|_redirects.\n`);
    for (const d of drift) console.error("  - " + d);
    console.error("\nFix: CF_API_TOKEN=... node web/scripts/cf-edge.mjs --apply");
    process.exit(1);
  }
  console.log(
    `edge in sync: ${headers.length} header(s) + www->apex 301 + the /roger vanity hop all match the repo.`,
  );
  process.exit(0);
}

if (legacyRedirect) {
  // Sends the legacy site to the canonical one. Gated on the canonical site being live,
  // because the whole point of the gate is that a redirect to a dead host is unrecoverable
  // for visitors and search engines alike.
  console.log(`legacy zone: ${LEGACY_ZONE}  ->  https://${APEX}`);
  const gate = await canonicalSiteReady(APEX);
  console.log(`gate:        ${gate.ready ? "OPEN" : "CLOSED"} - ${gate.reason}`);
  if (!gate.ready) {
    console.error(`\nRefusing to redirect ${LEGACY_ZONE} until https://${APEX}/ serves the site.`);
    process.exit(1);
  }
  if (!apply) {
    console.log("\nDRY RUN (no changes). Payload that --apply would PUT to the legacy zone:\n");
    console.log(JSON.stringify({ rules: [lRule] }, null, 2));
    console.log("\nRe-run with --legacy-redirect --apply (and CF_API_TOKEN set) to write it.");
    process.exit(0);
  }
  const lzid = await zoneId(LEGACY_ZONE);
  const c = await upsert(lzid, "http_request_dynamic_redirect", lRule);
  console.log(`legacy redirect: applied (${c.kept} other rule(s) preserved, ${c.total} total)`);
  console.log(`\nDone. Verify:  curl -sSI https://${LEGACY_ZONE}/broadcasts.html | grep -i location`);
  console.log(`              curl -sSI https://broker.${LEGACY_ZONE}/health   # must stay 200, NOT 301`);
  process.exit(0);
}

if (!apply) {
  console.log("DRY RUN (no changes). Payloads that --apply would PUT:\n");
  console.log("# http_response_headers_transform / entrypoint  (appended to your existing rules)");
  console.log(JSON.stringify({ rules: [hRule] }, null, 2));
  console.log("\n# http_request_dynamic_redirect / entrypoint  (appended to your existing rules)");
  console.log(JSON.stringify({ rules: [rRule, vanityImportRule()] }, null, 2));
  console.log("\nRe-run with --apply (and CF_API_TOKEN set) to write them.");
  process.exit(0);
}

const zid = await zoneId();
console.log(`zone id:     ${zid}`);
const a = await upsert(zid, "http_response_headers_transform", hRule);
console.log(`headers rule: applied (${a.kept} other rule(s) preserved, ${a.total} total)`);
const b = await upsert(zid, "http_request_dynamic_redirect", rRule);
console.log(`redirect rule: applied (${b.kept} other rule(s) preserved, ${b.total} total)`);
const v = await upsert(zid, "http_request_dynamic_redirect", vanityImportRule());
console.log(`vanity rule:  applied (${v.kept} other rule(s) preserved, ${v.total} total)`);
console.log("\nDone. Verify:  curl -sSI https://rogerai.fm/ | grep -iE 'content-security|strict-transport|x-frame'");
console.log("              curl -sSI https://www.rogerai.fm/ | grep -i location");
}
