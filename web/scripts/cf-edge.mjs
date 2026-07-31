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
    expression: `(http.host eq "www.${zone}")`,
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
    expression: `(http.host in {"${legacy}" "www.${legacy}"})`,
    description: DESC_LEGACY,
    enabled: true,
  };
}

// `go get rogerai.fm/roger` fetches the module path with NO trailing slash, and this
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
        target_url: { expression: `concat("https://${zone}", "/roger/")` },
        preserve_query_string: true,
      },
    },
    expression: `(http.host eq "${zone}" and http.request.uri.path eq "/roger")`,
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
  const live = await fetch(`https://${APEX}/`, { method: "HEAD", redirect: "manual" });
  for (const h of headers) {
    const got = live.headers.get(h.name);
    if (got === null) drift.push(`missing on the live edge: ${h.name}`);
    else if (got.trim() !== h.value) drift.push(`value drift: ${h.name}\n  repo: ${h.value}\n  live: ${got.trim()}`);
  }
  const www = await fetch(`https://${WWW}/`, { method: "HEAD", redirect: "manual" });
  const loc = www.headers.get("location") || "";
  if (www.status !== 301 || !loc.startsWith(`https://${APEX}`)) {
    drift.push(`www redirect drift: expected 301 -> https://${APEX}/..., got ${www.status} -> ${loc || "(none)"}`);
  }
  if (drift.length) {
    console.error(`EDGE DRIFT (${drift.length}): the live Cloudflare edge does not match web/src/_headers|_redirects.\n`);
    for (const d of drift) console.error("  - " + d);
    console.error("\nFix: CF_API_TOKEN=... node web/scripts/cf-edge.mjs --apply");
    process.exit(1);
  }
  console.log(`edge in sync: ${headers.length} header(s) + www->apex 301 all match the repo.`);
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
