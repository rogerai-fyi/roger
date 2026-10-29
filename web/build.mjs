#!/usr/bin/env node
// RogerAI site build: ONE static tree (web/dist/) from two sources, during the migration.
//
//   app/   the SvelteKit app: routes (one per page), lib (chrome, the broker client, the
//          design system), islands (the interactive parts, mounted by Svelte alone). A page
//          listed in app/owned.json is built here. Prerendered: no server, no client router.
//   src/   the legacy pages: web/src/*.html pulling shared chrome from web/src/_partials
//          via `<!-- include: X -->` markers, plus the assets every page uses.
//
// One owner per page: a page in app/owned.json must not exist as web/src/<page>.html, and
// the build fails if it does. As pages move, owned.json grows and src/ shrinks; when src/ is
// empty the legacy half of this file goes.
//
// Both halves get the same finishing passes: wave status tokens, a self-referential
// canonical, dash glue in headings, content-hash cache busting, and the derived sitemap.xml
// and llms.txt. Output is written atomically and stale files are pruned at the end.
//
// Run with   node web/build.mjs   (or `make site`). The legacy half needs nothing installed;
// the app half needs the toolchain in web/node_modules (installed on first use with npm ci).
//   WEB_PREVIEW=console.html,faq.html   builds those app routes too, for a local look
//                                        before a page is handed over in owned.json.
//
// Include syntax of the legacy half (resolved recursively, depth-guarded):
//   <!-- include: nav.html -->
//   <!-- include: nav.html variant=marketing -->     // pass args to the partial
// Inside a partial, args substitute as {{name}} and gate blocks (which may nest):
//   {{#if variant=marketing}} ... {{/if}}
//   {{#unless variant=marketing}} ... {{/unless}}
// Unknown {{name}} resolve to "" so stray markers never ship literally.
//
// Wave status labels in a page render from src/data/wave-status.json:
//   {{wave:<tier>.<label>}}      (scripts/wave-status.mjs; an unknown token fails the build)

import { readFileSync, writeFileSync, readdirSync, mkdirSync, rmSync, statSync, copyFileSync, renameSync, existsSync } from "node:fs";
import { join, dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { loadWaveStatus, resolveWaveTokens, loadCapabilities, resolveCapTokens } from "./scripts/wave-status.mjs";
import { loadModelCards, resolveCardTokens } from "./scripts/model-cards.mjs";
import { glueDashes } from "./scripts/glue-dashes.mjs";   // a heading's " - " never starts a line

const ROOT = dirname(fileURLToPath(import.meta.url));   // web/
const SRC = join(ROOT, "src");
const PARTIALS = join(SRC, "_partials");
const DIST = join(ROOT, "dist");

// ---- the app half ---------------------------------------------------------------------------
const APP = join(ROOT, "app");
const KIT_BUILD = join(APP, ".build");        // adapter-static output (vite.config.js)
const ISLANDS_OUT = join(APP, ".islands");    // island bundles (vite.islands.config.js)
const OWNED = JSON.parse(readFileSync(join(APP, "owned.json"), "utf8"));
const PREVIEW = (process.env.WEB_PREVIEW || "").split(",").map((s) => s.trim()).filter(Boolean);
const APP_PAGES = [...new Set([...OWNED, ...PREVIEW])].sort();
const routeOf = (page) => (page === "index.html" ? "/" : "/" + page.replace(/\.html$/, ""));

function ensureToolchain() {
  if (existsSync(join(ROOT, "node_modules", "vite", "bin", "vite.js"))) return;
  console.log("[build] installing the web toolchain (npm ci)");
  execFileSync("npm", ["ci", "--no-audit", "--no-fund"], { cwd: ROOT, stdio: "inherit" });
}
const vite = (args, env = {}) =>
  execFileSync(process.execPath, [join(ROOT, "node_modules", "vite", "bin", "vite.js"), ...args],
    { cwd: ROOT, stdio: "inherit", env: { ...process.env, ...env } });

// The app build is slow (SvelteKit resolves every module on every run) and the guard suite
// rebuilds the site in sixty before() hooks, so the app half rebuilds only when an input
// changed: a content hash of app/ (sources, never outputs), src/data (the facts pages read),
// the vite configs, the lockfile and the page list, kept beside the output in .stamp.
function appStamp(pages) {
  const h = createHash("sha256");
  const walk = (dir) => {
    for (const ent of readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const abs = join(dir, ent.name);
      if (ent.isDirectory()) { if (!/^\.(build|islands|svelte-kit)$/.test(ent.name)) walk(abs); }
      else { h.update(relative(ROOT, abs)); h.update(readFileSync(abs)); }
    }
  };
  walk(APP);
  if (existsSync(join(SRC, "data"))) walk(join(SRC, "data"));
  for (const f of ["vite.config.mjs", "vite.islands.config.mjs", "package-lock.json"]) h.update(readFileSync(join(ROOT, f)));
  h.update(JSON.stringify(pages));
  return h.digest("hex");
}

// The islands first (a page references its island by a fixed name), then the pages.
function buildApp(pages) {
  ensureToolchain();
  const stamp = appStamp(pages), stampFile = join(KIT_BUILD, ".stamp");
  if (existsSync(stampFile) && readFileSync(stampFile, "utf8") === stamp && pages.every((p) => existsSync(join(KIT_BUILD, p)))) return;
  rmSync(ISLANDS_OUT, { recursive: true, force: true });
  if (readdirSync(join(APP, "islands")).some((f) => f.endsWith(".js")))
    vite(["build", "--config", "vite.islands.config.mjs", "--logLevel", "warn"]);
  vite(["build", "--logLevel", "warn"], { WEB_KIT_ENTRIES: JSON.stringify(pages.map(routeOf)) });
  for (const page of pages)
    if (!existsSync(join(KIT_BUILD, page))) throw new Error(`the app did not produce ${page} (is there an app/routes${routeOf(page)}/+page.* ?)`);
  writeFileSync(stampFile, stamp);
}

const INCLUDE_RE = /<!--\s*include:\s*([^\s]+)\s*(.*?)\s*-->/g;
const MAX_DEPTH = 12;

// Per-page stylesheet manifest. Each page links exactly these CSS modules, in
// order, so editing one page's styles never touches a file another page needs.
// The shared lead is the design system's three layers, on EVERY page and in this
// order: tokens.css (design tokens) -> base.css (reset, typesetting, site chrome) ->
// components.css (the shared components; see DESIGN-SYSTEM.md). Account pages then
// load account-base.css before their own account module. The head.html
// `<!-- css-bundle -->` marker is expanded into one <link> per entry below (see
// emitCssBundle). Keep this in sync when adding a page.
const CSS_SHARED = ["tokens.css", "base.css", "components.css"];
const CSS_MARKETING = CSS_SHARED;                                   // shared lead, marketing
const CSS_ACCOUNT = [...CSS_SHARED, "account-base.css"];            // shared lead, account
const CSS_BUNDLES = {
  // marketing pages
  "index.html":     [...CSS_MARKETING, "home.css"],
  "manual.html":    [...CSS_MARKETING, "manual.css"],
  "models.html":    [...CSS_MARKETING, "models.css"],
  "playbox.html": [...CSS_MARKETING, "playbox.css", "wave-patch.css", "wave-factory.css"], // console + measured mesh + the separate factory game
  "playground.html": CSS_MARKETING, // legacy address: a redirect stub to the Playbox
  "tower.html":     [...CSS_MARKETING, "research.css", "tower.css"], // the broker, publicly
  "research.html":  [...CSS_MARKETING, "research.css"],
  "research-industry.html": [...CSS_MARKETING, "research.css"], // plant placement + standards, split out of the hub
  "research-hardware.html": [...CSS_MARKETING, "research.css", "research-hardware.css"], // the board -> tier map, the only page carrying third-party photography
  "careers.html":   [...CSS_MARKETING, "research.css", "careers.css", "company.css"], // hiring surface; notebook shell + the Company section sheet
  "company.html":   [...CSS_MARKETING, "research.css", "company.css"], // About; company.css = the Company section sheet
  "pricing.html":   [...CSS_MARKETING, "research.css", "pricing.css"], // reuses the notebook shell; pricing.css is only the plates
  "faq.html":       [...CSS_MARKETING, "research.css", "faq.css", "company.css"], // ditto; faq.css is only the question list
  "why.html":       [...CSS_MARKETING, "research.css"],                // the routing-fee argument; page-scoped styles inline
  "integrations.html": [...CSS_MARKETING, "research.css", "integrations.css"], // ditto; the tables and guest plates
  "research-models.html": [...CSS_MARKETING, "research.css"], // the model catalogue, split out of the hub
  "research-wave-pico.html": [...CSS_MARKETING, "research.css", "wave-model.css"], // a Wave model page: facts from data/model-cards.json
  "research-wave-nano.html": [...CSS_MARKETING, "research.css", "wave-model.css"], // ditto; Nano-VL is a section of it
  "research-wave-family.html": [...CSS_MARKETING, "research.css", "wave-family.css"], // the Wave ladder field guide, split out so research.html keeps its byte budget
  "voices.html":    [...CSS_MARKETING, "voices.css"],
  "broadcasts.html":                 [...CSS_MARKETING, "broadcasts.css"], // the transmission-log blog index
  "broadcasts-independence-v5.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 001 + the reusable post template
  "broadcasts-run-local-llm.html":   [...CSS_MARKETING, "broadcasts.css"], // broadcast 002 · SEO field guide
  "broadcasts-vram-for-llm.html":    [...CSS_MARKETING, "broadcasts.css"], // broadcast 003 · SEO field guide (VRAM)
  "broadcasts-share-gpu-earn.html":  [...CSS_MARKETING, "broadcasts.css"], // broadcast 004 · SEO field guide (share/earn)
  "broadcasts-one-gpu-many-users.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 005 · SEO field guide (concurrency)
  "broadcasts-free-chatgpt-alternative.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 006 · SEO field guide (free ChatGPT alt)
  "broadcasts-connect-bots-openai-api.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 007 · SEO field guide (connect bots / OpenAI API)
  "broadcasts-deepseek-mtp-gguf.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 008 · field guide (DeepSeek MTP GGUF build story)
  "broadcasts-how-rogerai-routes-models.html": [...CSS_MARKETING, "broadcasts.css", "broadcast-routing.css"], // broadcast 009 · capacity-aware smart routing
  "broadcasts-run-a-tower.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 010 · field guide (run a Tower / relay operator)
  "broadcasts-what-a-million-tokens-costs.html": [...CSS_MARKETING, "broadcasts.css", "broadcast-economics.css"], // broadcast 011 · unit economics (break-even per 1M tokens)
  "broadcasts-llm-on-iphone.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 012 · SEO field guide (LLM on iPhone / v1.2 transmitter)
  "broadcasts-agent-governance-identity.html": [...CSS_MARKETING, "broadcasts.css", "broadcast-agent-governance.css"], // broadcast 013 · dispatch (agent misalignment incident + Governance Identity)
  "broadcasts-sharing-your-gpu-is-safe.html": [...CSS_MARKETING, "broadcasts.css", "broadcast-gpu-isolation.css"], // broadcast 014 · field guide (model vs. harness isolation)
  "broadcasts-jev-vs-wave.html": [...CSS_MARKETING, "broadcasts.css"], // broadcast 015 · field guide (Jev vs the Wave family: decision models, the map, the ladder)
  "broadcasts-how-llms-work.html": [...CSS_MARKETING, "broadcasts.css", "broadcast-how-llms-work.css"], // broadcast 016 · field guide (how an LLM works and learns, in pictures)
  "bands.html":     [...CSS_MARKETING],                  // redirect shell: shared chrome only
  "app.html":       [...CSS_MARKETING, "app.css"],       // the App Store launch page (shipped 2026-07-09)
  "404.html":       [...CSS_MARKETING, "notfound.css"],
  // account (chrome) pages
  "account.html":   [...CSS_ACCOUNT, "account.css"],
  "billing.html":   [...CSS_ACCOUNT, "billing.css"],
  "payouts.html":   [...CSS_ACCOUNT, "payouts.css"],
  "usage.html":     [...CSS_ACCOUNT, "metrics.css"],
  "dashboard.html": [...CSS_ACCOUNT, "dashboard.css"],
  "stations.html":  [...CSS_ACCOUNT, "stations.css"],
  "device.html":    [...CSS_ACCOUNT, "device.css"],    // CLI sign-in approval  // operator station roll-up: status, usage, chain evidence
  "console.html":   [...CSS_ACCOUNT, "console.css"],
  "private.html":   [...CSS_ACCOUNT, "console.css", "private.css"], // reuses the cn-table; rc-* is its own
  "r.html":         [...CSS_ACCOUNT],                    // link handler: shared chrome only
  // admin.html (founder super-admin ops portal) moved to the PRIVATE rogerai-fyi/roger-admin repo.
  "login.html":     [...CSS_ACCOUNT],                    // shared account plates only
  "keys.html":      [...CSS_ACCOUNT, "metrics.css", "keys.css"], // reuses the mx-table ledger
  "privacy.html":   [...CSS_MARKETING, "company.css"],   // legal reading column (Company section sheet)
  "security.html":  [...CSS_MARKETING, "company.css"],   // ditto
  "confidential.html": [...CSS_MARKETING, "confidential.css"], // gated TEE-tier info: section head + tinted panels
  "tos.html":       [...CSS_MARKETING, "company.css"],   // ditto
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
// Gates may nest: each pass resolves only the INNERMOST blocks (a body that holds
// no other gate marker), and passes repeat until none is left.
const GATE_RE = /\{\{#(if|unless)\s+([\w-]+)(?:=([^}]*))?\}\}((?:(?!\{\{[#/](?:if|unless)\b)[\s\S])*?)\{\{\/\1\}\}/g;
function applyArgs(html, args) {
  // {{#if key=val}}...{{/if}}  (also bare {{#if key}} = truthy presence); {{#unless}} inverts
  for (let prev = null; prev !== html;) {
    prev = html;
    html = html.replace(GATE_RE, (_, kind, key, val, body) => {
      const have = args[key];
      const ok = val === undefined ? (have !== undefined && have !== "") : have === val;
      return ok === (kind === "if") ? body : "";
    });
  }
  if (/\{\{[#/](if|unless)\b/.test(html)) throw new Error("unbalanced {{#if}}/{{#unless}} in a partial");
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
    if (rel === join("data", "wave-status.json") || rel === join("data", "capabilities.json")) continue;   // build input, rendered into pages
    if (rel === join("data", "model-cards.json")) continue;   // build input, rendered into the model pages
    // top-level *.html in src/ are pages, built separately below
    if (!rel.includes("/") && ent.name.endsWith(".html")) continue;
    const dest = join(DIST, rel);
    mkdirSync(dirname(dest), { recursive: true });
    if (rel.endsWith(".css")) writeOut(dest, cssOut(rel));   // its url()s versioned
    else copyOut(abs, dest);
  }
}

// copy a built tree into dist, tracked like every other output (so pruneStale keeps it)
function copyTree(from, to) {
  for (const ent of readdirSync(from, { withFileTypes: true })) {
    const abs = join(from, ent.name);
    if (ent.isDirectory()) copyTree(abs, join(to, ent.name));
    else copyOut(abs, join(to, ent.name));
  }
}

// cacheBust appends a short CONTENT hash (?v=<hash>) to every local js/, styles/ + assets/ media
// URL so a changed asset gets a NEW url the CDN (Cloudflare) cannot serve stale - the fix for the
// "edited terminal.js but the edge kept the old one" class of bug (and the same for swapping in a
// re-timed ledger-demo.mp4/.gif). The hash is of the SOURCE file (byte-identical to what copyAssets
// ships), so a url changes ONLY when that file changes; missing/external refs stay unversioned.
// A stylesheet is hashed by its BUILT bytes (cssOut), so a re-vendored font or a swapped
// background image also gives the sheet that names it a new url.
const assetHashCache = new Map();
function assetHash(rel) {
  if (assetHashCache.has(rel)) return assetHashCache.get(rel);
  let h = "0";
  try {
    const data = rel.endsWith(".css") ? cssOut(rel)
      : rel.startsWith("js/islands/") ? readFileSync(join(ISLANDS_OUT, rel.slice("js/islands/".length)))
      : readFileSync(join(SRC, rel));
    h = createHash("sha256").update(data).digest("hex").slice(0, 8);
  } catch { /* missing/external: leave it unversioned rather than break the build */ }
  assetHashCache.set(rel, h);
  return h;
}
// The stylesheets' own local url()s (the self-hosted fonts, a few background images) get the
// same ?v=<content hash>, so the edge's long cache for versioned urls covers them too. The
// head.html font preload goes through cacheBust with the same hash: preload and @font-face
// name one url, or the face downloads twice.
const cssOutCache = new Map();
function cssOut(rel) {
  if (!cssOutCache.has(rel)) {
    const css = readFileSync(join(SRC, rel), "utf8").replace(/url\(("?)(\.\.\/[^")?#]+\.(?:woff2|png|webp|svg|jpe?g|gif|avif))\1\)/g,
      (m, q, url) => {
        const target = relative(SRC, resolve(SRC, dirname(rel), url));
        const h = assetHash(target);
        return h === "0" ? m : `url(${q}${url}?v=${h}${q})`;
      });
    cssOutCache.set(rel, css);
  }
  return cssOutCache.get(rel);
}
function cacheBust(html) {
  html = html.replace(/((?:src|href|poster)=")((?:js|styles|assets)\/[^"?]+\.(?:js|css|mp4|vtt|gif|png|webp|svg|jpe?g|avif|woff2))"/g,
    (_, pre, rel) => `${pre}${rel}?v=${assetHash(rel)}"`);
  // srcset carries several URLs with width descriptors; version each local one, so
  // responsive variants get the same immutable-URL treatment as plain src.
  return html.replace(/(srcset=")([^"]+)"/g, (_, pre, list) => pre + list.split(",").map((entry) => {
    const m = entry.trim().match(/^((?:js|styles|assets)\/[^\s?]+)(\s+\S+)?$/);
    return m ? `${m[1]}?v=${assetHash(m[1])}${m[2] || ""}` : entry.trim();
  }).join(", ") + '"');
}

// The Svelte compiler writes a valueless attribute as attr="" (hidden="", data-reveal="");
// the legacy pages, and every regex in the guard suite, spell it bare. Same meaning in HTML.
// Only the boolean attributes and data-* flags are touched; a real empty value (alt="") stays.
const FLAG_ATTR = /\s(hidden|defer|async|disabled|checked|selected|required|readonly|multiple|autofocus|autoplay|muted|loop|playsinline|controls|open|novalidate|nomodule|crossorigin|data-[\w-]+)=""(?=[\s/>])/g;
const bareFlags = (html) => html.replace(FLAG_ATTR, " $1");

// ---- SEO: one canonical per page + a build-time sitemap ------------------------------------
const ORIGIN = "https://rogerai.fm";
const canonicalURL = (page) => (page === "index.html" ? `${ORIGIN}/` : `${ORIGIN}/${page}`);

// A page is excluded from the sitemap iff its RENDERED HTML marks itself noindex. Deriving this
// from the built output (not a hand-maintained list) means the sitemap can never drift from the
// actual robots directive - a stale list shipped noindex pages and triggered GSC "Submitted URL
// marked noindex" warnings.
const isNoindex = (html) => /<meta[^>]+name=["']robots["'][^>]*noindex/i.test(html);

// Inject a self-referential <link rel="canonical"> for any page that doesn't already emit one
// (the og=1 / og=post head blocks emit their own). EXACTLY ONE canonical per page - a duplicate or
// wrong canonical makes Bing treat every page as a copy of the homepage (a real case-study bug).
function ensureCanonical(html, page) {
  if (/rel=["']canonical["']/.test(html)) return html;
  return html.replace(/<\/title>/, `</title>\n  <link rel="canonical" href="${canonicalURL(page)}" />`);
}

// sitemap.xml of the indexable pages (lastmod = the source file's mtime: the legacy page, or
// the app route's +page file).
function lastmod(page) {
  const route = page === "index.html" ? "" : page.replace(/\.html$/, "");
  const candidates = [join(SRC, page), ...["+page.svelte", "+page.md"].map((f) => join(APP, "routes", route, f))];
  const f = candidates.find((c) => existsSync(c));
  if (!f) throw new Error(`no source file for ${page}`);
  return statSync(f).mtime.toISOString().slice(0, 10);
}
function writeSitemap(indexablePages) {
  const idx = [...indexablePages].sort((a, b) =>
    a === "index.html" ? -1 : b === "index.html" ? 1 : a.localeCompare(b));
  const urls = idx
    .map((p) => `  <url><loc>${canonicalURL(p)}</loc><lastmod>${lastmod(p)}</lastmod></url>`)
    .join("\n");
  writeOut(
    join(DIST, "sitemap.xml"),
    `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls}\n</urlset>\n`,
  );
  return idx.length;
}

// ---- llms.txt: the site index, for the crawlers that read prose ------------------------------
//
// Same doctrine as the sitemap: DERIVED from the built output, never hand-listed. A page's line
// carries its own <title> and its own meta description, so a page can no more drift out of
// llms.txt than it can drift out of the sitemap. Convention: llmstxt.org (H1, blockquote, then
// linked sections).
const stripComments = (s) => s.replace(/<!--[\s\S]*?-->/g, "");
const stripTags = (s) => s.replace(/<[^>]*>/g, "").replace(/\s+/g, " ").trim();
// &amp; is decoded LAST: doing it first turns a literal "&amp;lt;" into "<" instead
// of "&lt;". Numeric refs are handled generically so a page whose title uses &#8217;
// or &sect; does not leak the raw entity into llms.txt (a test asserts none do).
const NAMED = { lt: "<", gt: ">", quot: '"', apos: "'", rsquo: "'", lsquo: "'",
  rdquo: '"', ldquo: '"', middot: "-", nbsp: " ", hellip: "...", sect: "\u00a7",
  ordm: "\u00ba", bull: "\u2022", darr: "\u2193", rarr: "\u2192" };
const decode = (s) => s
  .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCodePoint(parseInt(h, 16)))
  .replace(/&#(\d+);/g, (_, d) => String.fromCodePoint(Number(d)))
  .replace(/&([a-z]+);/gi, (m, name) => NAMED[name] ?? NAMED[name.toLowerCase()] ?? m)
  .replace(/&amp;/g, "&");

// "The Tower - RogerAI" / "RogerAI - the operating manual" -> the distinctive half.
function llmsTitle(html, page) {
  // Match inside the comment-free document: head.html's own doc comment documents a
  // `title =` argument, and a naive scan swallowed the whole comment as the title.
  const raw = decode(stripTags(stripComments(html).match(/<title>([\s\S]*?)<\/title>/)?.[1] || page));
  if (page === "index.html") return raw;   // the homepage IS the brand; do not strip it
  const parts = raw.split(/\s+[-\u00b7|]\s+/).filter((p) => p && !/^RogerAI$/i.test(p));
  return (parts.join(" - ") || raw).trim();
}

function llmsDescription(html, page) {
  // The quote character is captured and back-referenced: an apostrophe inside a
  // double-quoted description used to end the match early and truncate the line.
  const doc = stripComments(html);
  const meta = doc.match(/<meta[^>]+name=["']description["'][^>]+content=(["'])([\s\S]*?)\1/i)?.[2]
    || doc.match(/<meta[^>]+property=["']og:description["'][^>]+content=(["'])([\s\S]*?)\1/i)?.[2];
  if (!meta) throw new Error(`${page} has no meta description, so it cannot be listed in llms.txt`);
  return decode(stripTags(meta));
}

// Three groups, so a reader (or a crawler) sees the shape of the site rather than 30 flat lines.
const llmsGroup = (page) =>
  /^broadcasts-/.test(page) ? "Broadcasts (field guides and build notes)"
  : /^research/.test(page) ? "Research"
  : "Pages";
const LLMS_GROUPS = ["Pages", "Research", "Broadcasts (field guides and build notes)"];

function writeLlms(indexablePages, htmlByPage) {
  const lines = [
    "# RogerAI",
    "",
    "> An OpenAI-compatible network for local models: put a model running on your own GPU on",
    "> the air and get paid per token, or tune in to someone else's. One CLI does both sides.",
    "",
    "RogerAI is an American AI research and infrastructure company. It publishes the Wave model",
    "family for constrained hardware, and runs a marketplace where independently owned machines",
    "serve those and other open models over one OpenAI-compatible endpoint. Prices are set by the",
    "operators, not by us, so any figure on this site that looks like a rate is live data read from",
    "the market at https://rogerai.fm/models.html rather than a published price list.",
    "",
  ];
  for (const group of LLMS_GROUPS) {
    const pages = indexablePages.filter((p) => llmsGroup(p) === group).sort();
    if (!pages.length) continue;
    lines.push(`## ${group}`, "");
    for (const page of pages) {
      const html = htmlByPage.get(page);
      lines.push(`- [${llmsTitle(html, page)}](${canonicalURL(page)}): ${llmsDescription(html, page)}`);
    }
    lines.push("");
  }
  writeOut(join(DIST, "llms.txt"), lines.join("\n"));
  return indexablePages.length;
}


// ---------------------------------------------------------------------------
// dist/ is written IN PLACE, not deleted and recreated.
//
// `rmSync(DIST)` at the top of a build meant that anything reading dist/ at that moment saw
// correct paths vanish. That is not theoretical: it failed a push on 2026-09-01 with
// "ENOENT: dist/research-wave-family.html" because the gate's own suite was reading the
// directory a foreground build had just emptied. CLAUDE.md says several sessions run against
// this repo at once, so a build that cannot be run twice safely is a trap.
//
// Instead: every output is written to a temp name and renamed over its destination, which is
// atomic on one filesystem, so a reader gets the old complete file or the new one and never a
// missing or half-written one. Whatever the build did NOT write is pruned at the end, so
// stale output still cannot outlive its source.
const written = new Set();
const TMP_RE = /\.tmp\.\d+$/;          // `<name>.tmp.<pid>`, written by writeOut/copyOut
const ORPHAN_TMP_MS = 60 * 60 * 1000;   // older than this and nobody is coming back for it

function track(dest) {
  written.add(resolve(dest));
  return dest;
}

// Atomic per file: write beside the target, then rename onto it. The pid keeps two concurrent
// builds from sharing a temp name.
function writeOut(dest, data) {
  mkdirSync(dirname(dest), { recursive: true });
  const tmp = `${dest}.tmp.${process.pid}`;
  writeFileSync(tmp, data);
  renameSync(tmp, dest);
  return track(dest);
}

function copyOut(src, dest) {
  mkdirSync(dirname(dest), { recursive: true });
  const tmp = `${dest}.tmp.${process.pid}`;
  copyFileSync(src, tmp);
  renameSync(tmp, dest);
  return track(dest);
}

// Remove anything under dist/ this build did not produce - a page deleted from src/ must stop
// being served. Scoped to DIST and nothing above it.
function pruneStale(dir) {
  if (!existsSync(dir)) return;
  let names;
  try { names = readdirSync(dir); } catch { return; }   // a concurrent build removed it
  for (const name of names) {
    const abs = join(dir, name);
    // Everything below races a concurrent build by construction: between this readdir and
    // the stat, another process may have renamed its temp file into place or finished. A
    // path that has already gone is not stale output, so ENOENT is the expected case and
    // never a reason to fail a build.
    let st;
    try { st = statSync(abs); } catch { continue; }
    if (st.isDirectory()) {
      pruneStale(abs);
      continue;   // empty directories are left alone: removing one a concurrent build is
                  // still filling makes ITS next write fail, and an empty dir harms nothing
    }
    if (TMP_RE.test(name)) {
      if (Date.now() - st.mtimeMs > ORPHAN_TMP_MS) { try { rmSync(abs, { force: true }); } catch {} }
      continue;
    }
    if (!written.has(resolve(abs))) { try { rmSync(abs, { force: true }); } catch {} }
  }
}

function build() {
  written.clear();
  mkdirSync(DIST, { recursive: true });

  // pages: top-level *.html in src/ (legacy) + the app's owned routes. One owner per page.
  const legacyPages = readdirSync(SRC).filter((f) => f.endsWith(".html"));
  for (const page of APP_PAGES)
    if (legacyPages.includes(page))
      throw new Error(`${page} is owned by the app (app/owned.json) but web/src/${page} still exists: one owner per page`);
  if (APP_PAGES.length) buildApp(APP_PAGES);
  const pages = [...legacyPages, ...APP_PAGES];
  const indexable = [];
  const htmlByPage = new Map();
  const wave = loadWaveStatus();
  const cards = loadModelCards();
  const caps = loadCapabilities();
  for (const page of APP_PAGES) {
    let out = readFileSync(join(KIT_BUILD, page), "utf8");
    if (/\{\{(wave|card|cap):/.test(out)) throw new Error(`${page}: the app reads wave/card/capability facts through app/lib/content, never {{tokens}}`);
    out = bareFlags(out);               // hidden="" -> hidden: the legacy spelling of a flag
    out = ensureCanonical(out, page);   // exactly one self-referential canonical per page
    out = glueDashes(out);             // a heading's " - " never starts a line
    out = cacheBust(out);               // content-version js/css urls so the CDN can't serve stale
    writeOut(join(DIST, page), out);
    htmlByPage.set(page, out);
    if (!isNoindex(out)) indexable.push(page);
  }
  for (const page of legacyPages) {
    const raw = readFileSync(join(SRC, page), "utf8");
    let out = resolveIncludes(raw, 0);
    out = resolveWaveTokens(out, page, wave);   // wave status labels from the one data file
    out = resolveCardTokens(out, page, cards);  // model page facts from the one data file
    out = resolveCapTokens(out, page, caps);    // adapter, skill and device status, likewise
    out = emitCssBundle(out, page);     // expand the per-page stylesheet bundle
    out = ensureCanonical(out, page);   // exactly one self-referential canonical per page
    out = glueDashes(out);             // a heading's " - " never starts a line
    out = cacheBust(out);               // content-version js/css urls so the CDN can't serve stale
    if (/<!--\s*include:/.test(out)) throw new Error(`unresolved include in ${page}`);
    if (/<!--\s*css-bundle\s*-->/.test(out)) throw new Error(`unresolved css-bundle in ${page}`);
    writeOut(join(DIST, page), out);
    htmlByPage.set(page, out);
    if (!isNoindex(out)) indexable.push(page);   // sitemap tracks the ACTUAL robots directive
  }

  copyAssets(SRC);
  if (APP_PAGES.length) {
    // the app's own output beside the pages: component stylesheets under _app/ (if any
    // page has them) and the island bundles under js/islands/
    if (existsSync(join(KIT_BUILD, "_app"))) copyTree(join(KIT_BUILD, "_app"), join(DIST, "_app"));
    if (existsSync(ISLANDS_OUT)) copyTree(ISLANDS_OUT, join(DIST, "js", "islands"));
  }
  const n = writeSitemap(indexable);
  const l = writeLlms(indexable, htmlByPage);

  // last, so a page removed from src/ stops being served - the one thing the old
  // delete-everything-first build got for free.
  pruneStale(DIST);

  console.log(`built ${pages.length} page(s) (${APP_PAGES.length} from app/, ${legacyPages.length} legacy) + sitemap.xml (${n} urls) + llms.txt (${l} entries) -> ${relative(ROOT, DIST)}/`);
}

build();
