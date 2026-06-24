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

const ROOT = dirname(fileURLToPath(import.meta.url));   // web/
const SRC = join(ROOT, "src");
const PARTIALS = join(SRC, "_partials");
const DIST = join(ROOT, "dist");

const INCLUDE_RE = /<!--\s*include:\s*([^\s]+)\s*(.*?)\s*-->/g;
const MAX_DEPTH = 12;

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

function build() {
  rmSync(DIST, { recursive: true, force: true });
  mkdirSync(DIST, { recursive: true });

  // pages: top-level *.html in src/
  const pages = readdirSync(SRC).filter((f) => f.endsWith(".html"));
  for (const page of pages) {
    const raw = readFileSync(join(SRC, page), "utf8");
    const out = resolveIncludes(raw, 0);
    if (/<!--\s*include:/.test(out)) throw new Error(`unresolved include in ${page}`);
    writeFileSync(join(DIST, page), out);
  }

  copyAssets(SRC);

  console.log(`built ${pages.length} page(s) -> ${relative(ROOT, DIST)}/`);
}

build();
