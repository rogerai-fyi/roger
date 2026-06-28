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
//
// Env: CF_API_TOKEN (required for --apply; a token scoped to Zone.Rules + Zone:Read on the
//      rogerai.fyi zone). CF_ZONE overrides the zone name (default rogerai.fyi).
//
// Dependency-free: Node >=18 (global fetch). No npm install.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SRC = path.join(HERE, "..", "src");
const ZONE = process.env.CF_ZONE || "rogerai.fyi";
const APEX = ZONE;
const WWW = "www." + ZONE;
const API = "https://api.cloudflare.com/client/v4";

const apply = process.argv.includes("--apply");
const reportOnly = process.argv.includes("--report-only");

// stable markers so re-runs update our rules instead of stacking duplicates.
const DESC_HEADERS = "rogerai:security-headers";
const DESC_REDIRECT = "rogerai:www-to-apex";

// ---- parse the `/*` block of web/src/_headers into an ordered {name,value} list ----------
function parseHeaders() {
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

// ---- build the two rule objects -----------------------------------------------------------
function headerRule(headers) {
  const map = {};
  for (const { name, value } of headers) {
    const key = reportOnly && name.toLowerCase() === "content-security-policy"
      ? "Content-Security-Policy-Report-Only" : name;
    map[key] = { operation: "set", value };
  }
  return {
    action: "rewrite",
    action_parameters: { headers: map },
    expression: `(http.host in {"${APEX}" "${WWW}"})`,
    description: DESC_HEADERS,
    enabled: true,
  };
}

function redirectRule() {
  return {
    action: "redirect",
    action_parameters: {
      from_value: {
        status_code: 301,
        target_url: { expression: `concat("https://${APEX}", http.request.uri.path)` },
        preserve_query_string: true,
      },
    },
    expression: `(http.host eq "${WWW}")`,
    description: DESC_REDIRECT,
    enabled: true,
  };
}

// ---- Cloudflare API helpers ---------------------------------------------------------------
async function cf(method, urlPath, body) {
  const token = process.env.CF_API_TOKEN;
  if (!token) throw new Error("CF_API_TOKEN is not set");
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

async function zoneId() {
  const { json } = await cf("GET", `/zones?name=${encodeURIComponent(ZONE)}`);
  const id = json.result && json.result[0] && json.result[0].id;
  if (!id) throw new Error(`zone ${ZONE} not found (check the token's zone access)`);
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
const headers = parseHeaders();
const redirectLine = parseRedirect();
const hRule = headerRule(headers);
const rRule = redirectRule();

console.log(`zone:        ${ZONE}`);
console.log(`headers:     ${headers.length} (${headers.map((h) => h.name).join(", ")})`);
console.log(`csp mode:    ${reportOnly ? "Content-Security-Policy-Report-Only (test)" : "Content-Security-Policy (enforce)"}`);
console.log(`redirect:    ${redirectLine}`);
console.log("");

if (!apply) {
  console.log("DRY RUN (no changes). Payloads that --apply would PUT:\n");
  console.log("# http_response_headers_transform / entrypoint  (appended to your existing rules)");
  console.log(JSON.stringify({ rules: [hRule] }, null, 2));
  console.log("\n# http_request_dynamic_redirect / entrypoint  (appended to your existing rules)");
  console.log(JSON.stringify({ rules: [rRule] }, null, 2));
  console.log("\nRe-run with --apply (and CF_API_TOKEN set) to write them.");
  process.exit(0);
}

const zid = await zoneId();
console.log(`zone id:     ${zid}`);
const a = await upsert(zid, "http_response_headers_transform", hRule);
console.log(`headers rule: applied (${a.kept} other rule(s) preserved, ${a.total} total)`);
const b = await upsert(zid, "http_request_dynamic_redirect", rRule);
console.log(`redirect rule: applied (${b.kept} other rule(s) preserved, ${b.total} total)`);
console.log("\nDone. Verify:  curl -sSI https://rogerai.fyi/ | grep -iE 'content-security|strict-transport|x-frame'");
console.log("              curl -sSI https://www.rogerai.fyi/ | grep -i location");
