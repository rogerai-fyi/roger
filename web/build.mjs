#!/usr/bin/env node
// RogerAI site build: resolve partial includes and copy assets.
//
// One source of chrome. Pages live in web/src/*.html and pull shared chrome from
// web/src/_partials/{head,brand,nav,footer}.html via `<!-- include: X -->` markers.
// Output is web/dist/, a plain static tree (the same files the host serves).
//
// Dependency-light on purpose: Node ESM + node:fs only, no npm install. Run with
//   node web/build.mjs        (or `make site`)
//
// Include syntax (resolved recursively, depth-guarded):
//   <!-- include: nav.html -->
//   <!-- include: nav.html variant=marketing -->     // pass args to the partial
// Inside a partial, args substitute as {{name}} and gate blocks:
//   {{#if variant=marketing}} ... {{/if}}
//   {{#unless variant=marketing}} ... {{/unless}}
// Unknown {{name}} resolve to "" so stray markers never ship literally.

import { readFileSync, writeFileSync, readdirSync, mkdirSync, rmSync, statSync, copyFileSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";

const ROOT = dirname(fileURLToPath(import.meta.url));   // web/
const SRC = join(ROOT, "src");
const PARTIALS = join(SRC, "_partials");
const DIST = join(ROOT, "dist");

const INCLUDE_RE = /<!--\s*include:\s*([^\s]+)\s*(.*?)\s*-->/g;
const MAX_DEPTH = 12;

// Per-page stylesheet manifest. Each page links exactly these CSS modules, in
// order, so editing one page's styles never touches a file another page needs.
// tokens.css (design tokens) + base.css (shared chrome) lead EVERY page; account
// pages then load account-base.css before their own account module. The order
// matches the original site.css / auth.css source cascade, so the rendered
// cascade is byte-for-byte unchanged from the old monolithic files. The
// head.html `<!-- css-bundle -->` marker is expanded into one <link> per entry
// below (see emitCssBundle). Keep this in sync when adding a page.
const CSS_MARKETING = ["tokens.css", "base.css"];                   // shared lead, marketing
const CSS_ACCOUNT = ["tokens.css", "base.css", "account-base.css"]; // shared lead, account
const CSS_BUNDLES = {
  // marketing pages
  "index.html":     [...CSS_MARKETING, "home.css"],
  "manual.html":    [...CSS_MARKETING, "manual.css"],
  "models.html":    [...CSS_MARKETING, "models.css"],
  "bands.html":     [...CSS_MARKETING],                  // redirect shell: shared chrome only
  "404.html":       [...CSS_MARKETING, "notfound.css"],
  // account (chrome) pages
  "account.html":   [...CSS_ACCOUNT, "account.css"],
  "billing.html":   [...CSS_ACCOUNT, "billing.css"],
  "payouts.html":   [...CSS_ACCOUNT, "payouts.css"],
  "usage.html":     [...CSS_ACCOUNT, "metrics.css"],
  "dashboard.html": [...CSS_ACCOUNT, "dashboard.css"],
  "console.html":   [...CSS_ACCOUNT, "console.css"],
  // admin.html (founder super-admin ops portal) moved to the PRIVATE rogerai-fyi/roger-admin repo.
  "login.html":     [...CSS_ACCOUNT],                    // shared account plates only
  "keys.html":      [...CSS_ACCOUNT, "metrics.css", "keys.css"], // reuses the mx-table ledger
  "privacy.html":   [...CSS_ACCOUNT],                    // legal plate: shared chrome only
  "security.html":  [...CSS_ACCOUNT],                    // legal plate: shared chrome only
  "confidential.html": [...CSS_ACCOUNT],                 // gated TEE-tier info: shared chrome only
  "tos.html":       [...CSS_ACCOUNT],                    // legal plate: shared chrome only
};

const CSS_MARKER_RE = /^([ \t]*)<!--\s*css-bundle\s*-->[ \t]*$/m;

// Expand head.html's `<!-- css-bundle -->` marker into this page's <link> set.
function emitCssBundle(html, page) {
  const bundle = CSS_BUNDLES[page];
  if (!bundle) throw new Error(`no CSS bundle for page ${page} (add it to CSS_BUNDLES in build.mjs)`);
  return html.replace(CSS_MARKER_RE, (_, indent) =>
    bundle.map((f) => `${indent}<link rel="stylesheet" href="styles/${f}" />`).join("\n"));
}

// "k=v k2=v2" -> { k: "v", k2: "v2" }
function parseArgs(s) {
  const out = {};
  const re = /([\w-]+)=("([^"]*)"|'([^']*)'|(\S+))/g;
  let m;
  while ((m = re.exec(s))) out[m[1]] = m[3] ?? m[4] ?? m[5] ?? "";
  return out;
}

// Apply {{#if}} / {{#unless}} gates and {{var}} substitution for one arg set.
function applyArgs(html, args) {
  // {{#if key=val}}...{{/if}}  (also bare {{#if key}} = truthy presence)
  html = html.replace(/\{\{#if\s+([\w-]+)(?:=([^}]*))?\}\}([\s\S]*?)\{\{\/if\}\}/g,
    (_, key, val, body) => {
      const have = args[key];
      const ok = val === undefined ? (have !== undefined && have !== "") : have === val;
      return ok ? body : "";
    });
  html = html.replace(/\{\{#unless\s+([\w-]+)(?:=([^}]*))?\}\}([\s\S]*?)\{\{\/unless\}\}/g,
    (_, key, val, body) => {
      const have = args[key];
      const ok = val === undefined ? (have !== undefined && have !== "") : have === val;
      return ok ? "" : body;
    });
  // {{var}} substitution; unknown -> ""
  html = html.replace(/\{\{\s*([\w-]+)\s*\}\}/g, (_, key) => args[key] ?? "");
  return html;
}

const partialCache = new Map();
function loadPartial(name) {
  if (!partialCache.has(name)) {
    partialCache.set(name, readFileSync(join(PARTIALS, name), "utf8"));
  }
  return partialCache.get(name);
}

function resolveIncludes(html, depth) {
  if (depth > MAX_DEPTH) throw new Error("include depth exceeded (cycle?)");
  return html.replace(INCLUDE_RE, (_, file, argStr) => {
    const args = parseArgs(argStr);
    let body = applyArgs(loadPartial(file), args);
    return resolveIncludes(body, depth + 1);   // nested includes inside partials
  });
}

// ---- copy every non-page asset from src/ to dist/, recursively ----
function copyAssets(dir) {
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    const abs = join(dir, ent.name);
    if (ent.isDirectory()) {
      if (abs === PARTIALS) continue;            // partials are not shipped
      copyAssets(abs);
      continue;
    }
    const rel = relative(SRC, abs);
    if (rel.startsWith("_partials")) continue;
    // top-level *.html in src/ are pages, built separately below
    if (!rel.includes("/") && ent.name.endsWith(".html")) continue;
    const dest = join(DIST, rel);
    mkdirSync(dirname(dest), { recursive: true });
    copyFileSync(abs, dest);
  }
}

// cacheBust appends a short CONTENT hash (?v=<hash>) to every local js/, styles/ + assets/ media
// URL so a changed asset gets a NEW url the CDN (Cloudflare) cannot serve stale - the fix for the
// "edited terminal.js but the edge kept the old one" class of bug (and the same for swapping in a
// re-timed ledger-demo.mp4/.gif). The hash is of the SOURCE file (byte-identical to what copyAssets
// ships), so a url changes ONLY when that file changes; missing/external refs stay unversioned.
const assetHashCache = new Map();
function assetHash(rel) {
  if (assetHashCache.has(rel)) return assetHashCache.get(rel);
  let h = "0";
  try {
    h = createHash("sha256").update(readFileSync(join(SRC, rel))).digest("hex").slice(0, 8);
  } catch { /* missing/external: leave it unversioned rather than break the build */ }
  assetHashCache.set(rel, h);
  return h;
}
function cacheBust(html) {
  return html.replace(/((?:src|href|poster)=")((?:js|styles|assets)\/[^"?]+\.(?:js|css|mp4|gif|png|webp|svg|jpe?g|avif))"/g,
    (_, pre, rel) => `${pre}${rel}?v=${assetHash(rel)}"`);
}

function build() {
  rmSync(DIST, { recursive: true, force: true });
  mkdirSync(DIST, { recursive: true });

  // pages: top-level *.html in src/
  const pages = readdirSync(SRC).filter((f) => f.endsWith(".html"));
  for (const page of pages) {
    const raw = readFileSync(join(SRC, page), "utf8");
    let out = resolveIncludes(raw, 0);
    out = emitCssBundle(out, page);     // expand the per-page stylesheet bundle
    out = cacheBust(out);               // content-version js/css urls so the CDN can't serve stale
    if (/<!--\s*include:/.test(out)) throw new Error(`unresolved include in ${page}`);
    if (/<!--\s*css-bundle\s*-->/.test(out)) throw new Error(`unresolved css-bundle in ${page}`);
    writeFileSync(join(DIST, page), out);
  }

  copyAssets(SRC);

  console.log(`built ${pages.length} page(s) -> ${relative(ROOT, DIST)}/`);
}

build();
