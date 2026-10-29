#!/usr/bin/env node
// RogerAI site build: ONE static tree (web/dist/) from the SvelteKit app in app/.
//
//   app/routes/<page>/+page.svelte   one route per page, prerendered to dist/<page>.html
//   app/lib/                         the chrome (ui/), the head (head.js), the broker client
//                                    (api.js), the data-file facts (content.js), the islands
//   app/islands/<name>.js            an island's entry, bundled to dist/js/islands/<name>.js
//   app/runtime.js                   the site runtime (src/js, nine scripts) as one module, dist/js/runtime.js
//   src/                             the static tree every page uses: js/ (the page scripts),
//                                    styles/, assets/, data/ (build input), install.sh, roger/
//
// Order: the islands (a page names its island by a fixed name), the app (vite build, every
// route prerendered, nothing hydrated through SvelteKit's client), then each page through
// the finishing passes: hydration comments dropped outside islands, flags spelled bare, one
// self-referential canonical, dash glue in headings, content-hash cache busting. Then the
// static tree, the derived sitemap.xml and llms.txt, and a prune of stale output. Output is
// written atomically. The app rebuilds only when an input changed (a content stamp), so the
// guard suite's sixty rebuilds stay cheap.
//
// Run with   node web/build.mjs   (or `make site`). Needs web/node_modules (installed on
// first use with npm ci).

import { readFileSync, writeFileSync, readdirSync, mkdirSync, rmSync, statSync, copyFileSync, renameSync, existsSync } from "node:fs";
import { join, dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { glueDashes } from "./scripts/glue-dashes.mjs";   // a heading's " - " never starts a line

const ROOT = dirname(fileURLToPath(import.meta.url));   // web/
const SRC = join(ROOT, "src");
const DIST = join(ROOT, "dist");

// ---- the app half ---------------------------------------------------------------------------
const APP = join(ROOT, "app");
const KIT_BUILD = join(APP, ".build");        // adapter-static output (vite.config.js)
const ISLANDS_OUT = join(APP, ".islands");    // island bundles (vite.islands.config.js)
const routeOf = (page) => (page === "index.html" ? "/" : "/" + page.replace(/\.html$/, ""));

// The pages: every +page.svelte / +page.md under app/routes, as dist file names.
function routes(dir = join(APP, "routes"), rel = "") {
  const out = [];
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    if (ent.isDirectory()) out.push(...routes(join(dir, ent.name), rel ? `${rel}/${ent.name}` : ent.name));
    else if (/^\+page\.(svelte|md)$/.test(ent.name)) out.push(rel ? `${rel}.html` : "index.html");
  }
  return out.sort();
}
const PAGES = routes();

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
  walk(join(SRC, "js"));   // the runtime bundle's sources
  for (const f of ["vite.config.mjs", "vite.islands.config.mjs", "package-lock.json"]) h.update(readFileSync(join(ROOT, f)));
  h.update(JSON.stringify(pages));
  return h.digest("hex");
}

// The scripts app/runtime.js bundles (src/js/<name>.js): shipped as dist/js/runtime.js, not
// as the files, so a page never loads them twice.
const RUNTIME_SOURCES = [...readFileSync(join(APP, "runtime.js"), "utf8").matchAll(/import "\.\.\/src\/js\/([\w-]+\.js)"/g)].map((m) => join("js", m[1]));

// The bundles first (a page references its island by a fixed name, the runtime by its one
// name), then the pages.
function buildApp(pages) {
  ensureToolchain();
  const stamp = appStamp(pages), stampFile = join(KIT_BUILD, ".stamp");
  const islandSources = readdirSync(join(APP, "islands")).filter((f) => f.endsWith(".js"));
  const islandsBuilt = [...islandSources, "runtime.js"].every((f) => existsSync(join(ISLANDS_OUT, f)));
  if (existsSync(stampFile) && readFileSync(stampFile, "utf8") === stamp && islandsBuilt && pages.every((p) => existsSync(join(KIT_BUILD, p)))) return;
  rmSync(ISLANDS_OUT, { recursive: true, force: true });
  vite(["build", "--config", "vite.islands.config.mjs", "--logLevel", "warn"]);
  vite(["build", "--logLevel", "warn"], { WEB_KIT_ENTRIES: JSON.stringify(pages.map(routeOf)) });
  for (const page of pages)
    if (!existsSync(join(KIT_BUILD, page))) throw new Error(`the app did not produce ${page} (is there an app/routes${routeOf(page)}/+page.* ?)`);
  writeFileSync(stampFile, stamp);
}

// ---- copy every non-page asset from src/ to dist/, recursively ----
function copyAssets(dir) {
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    const abs = join(dir, ent.name);
    if (ent.isDirectory()) { copyAssets(abs); continue; }
    const rel = relative(SRC, abs);
    if (rel === join("data", "wave-status.json") || rel === join("data", "capabilities.json")) continue;   // build input, rendered into pages
    if (rel === join("data", "model-cards.json")) continue;   // build input, rendered into the model pages
    if (RUNTIME_SOURCES.includes(rel)) continue;   // bundled into js/runtime.js
    if (!rel.includes("/") && ent.name.endsWith(".html")) throw new Error(`web/src/${rel}: a page is an app route (app/routes), not a file in src/`);
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
      : rel === "js/runtime.js" ? readFileSync(join(ISLANDS_OUT, "runtime.js"))
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

// Svelte's hydration comments (<!---->, <!--[-->, <!--]-->, <!--[!-->, <!--[3-->) carry
// nothing on a static page: no client walks them. An island hydrates, so it keeps its own
// (from its <div data-island> to its <!--/island--> sentinel, lib/ui/Island.svelte);
// everywhere else they are dropped, and the page reads like the hand-written one it replaces.
// (SvelteKit's own head anchor, <!--1bexm30-->, a random id, goes the same way.)
const MARKER_RE = /<!--(?:\[-?\d*|\]|!||[0-9a-z]{5,10})-->/g;
function stripMarkers(html) {
  const parts = html.split(/(<div data-island="[\w-]+">[\s\S]*?<!--\/island-->)/);
  return parts.map((part, i) => (i % 2 ? part : part.replace(MARKER_RE, ""))).join("");
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

// sitemap.xml of the indexable pages (lastmod = the route file's mtime).
function lastmod(page) {
  const route = page === "index.html" ? "" : page.replace(/\.html$/, "");
  const f = ["+page.svelte", "+page.md"].map((n) => join(APP, "routes", route, n)).find(existsSync);
  if (!f) throw new Error(`no route file for ${page}`);
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

  buildApp(PAGES);
  const indexable = [];
  const htmlByPage = new Map();
  for (const page of PAGES) {
    let out = readFileSync(join(KIT_BUILD, page), "utf8");
    if (/\{\{(wave|card|cap):/.test(out)) throw new Error(`${page}: a page reads wave/card/capability facts through app/lib/content, never {{tokens}}`);
    out = stripMarkers(out);            // no hydration comments outside an island
    out = bareFlags(out);               // hidden="" -> hidden: the legacy spelling of a flag
    out = ensureCanonical(out, page);   // exactly one self-referential canonical per page
    out = glueDashes(out);             // a heading's " - " never starts a line
    out = cacheBust(out);               // content-version js/css urls so the CDN can't serve stale
    writeOut(join(DIST, page), out);
    htmlByPage.set(page, out);
    if (!isNoindex(out)) indexable.push(page);   // sitemap tracks the ACTUAL robots directive
  }

  copyAssets(SRC);
  // the app's own output beside the pages: component stylesheets under _app/ (if any page
  // has them) and the island bundles under js/islands/
  if (existsSync(join(KIT_BUILD, "_app"))) copyTree(join(KIT_BUILD, "_app"), join(DIST, "_app"));
  for (const f of existsSync(ISLANDS_OUT) ? readdirSync(ISLANDS_OUT) : []) {
    copyOut(join(ISLANDS_OUT, f), f === "runtime.js" ? join(DIST, "js", f) : join(DIST, "js", "islands", f));
  }
  const n = writeSitemap(indexable);
  const l = writeLlms(indexable, htmlByPage);

  // last, so a page removed from app/routes stops being served
  pruneStale(DIST);

  console.log(`built ${PAGES.length} page(s) + sitemap.xml (${n} urls) + llms.txt (${l} entries) -> ${relative(ROOT, DIST)}/`);
}

build();
