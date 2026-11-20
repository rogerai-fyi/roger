// Shared HTML normalizers for the guard suite.
//
// Every page is an app route now (the legacy include build is gone); the helpers below read a page's
// the Svelte compiler. They spell the same document differently. Svelte writes the decoded
// character where the source wrote an entity (&middot; -> ·, &copy; -> ©, &nbsp; -> a no-break
// space) and strips HTML comments. The copy is the same; a test that compares copy decodes
// BOTH sides with the helpers here, so the comparison is exactly as strict as before and
// indifferent to spelling. (Empty flag attributes, hidden="" for hidden, are normalized in
// build.mjs instead, so the built markup carries the legacy convention.)
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import path from "node:path";

export const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
export const DIST = path.join(WEB, "dist");
export const SRC = path.join(WEB, "src");
export const APP = path.join(WEB, "app");

import { decodeHTML } from "entities";
// Tag-stripped text: every entity decoded (the full HTML5 table, as a browser would).
export const decodeText = (s) => decodeHTML(s);
// Markup: every entity decoded EXCEPT the five that would change the parse.
const KEEP = { "&lt;": "\u0000L", "&gt;": "\u0000G", "&amp;": "\u0000A", "&quot;": "\u0000Q", "&apos;": "\u0000P", "&#39;": "\u0000P", "&#34;": "\u0000Q", "&#60;": "\u0000L", "&#62;": "\u0000G", "&#38;": "\u0000A" };
const RESTORE = Object.fromEntries(Object.entries(KEEP).map(([k, v]) => [v, k]).filter(([v]) => !/#/.test(v)));
export const decodeMarkup = (h) => decodeHTML(h.replace(/&(?:lt|gt|amp|quot|apos|#3[489]|#6[02]);/g, (m) => KEEP[m])).replace(/\u0000[LGAQP]/g, (m) => ({ "\u0000L": "&lt;", "\u0000G": "&gt;", "\u0000A": "&amp;", "\u0000Q": "&quot;", "\u0000P": "&apos;" })[m]);

export const stripComments = (h) => h.replace(/<!--[\s\S]*?-->/g, "");
export const stripTags = (h) => h.replace(/<[^>]+>/g, " ");
// Visible words of a fragment, whitespace removed, entities decoded: the copy comparison key.
export const squashText = (h) => decodeText(stripTags(h)).replace(/\s+/g, "");

// The reader's words of a page: <main> without comments, scripts, styles and the contents
// tuner (research-rollout, wave-claim-sweep). Entities decoded on the way to the hash.
export function mainOf(html) {
  return (html.match(/<main\b[\s\S]*<\/main>/) || [""])[0]
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<script\b[\s\S]*?<\/script>/g, "")
    .replace(/<style\b[\s\S]*?<\/style>/g, "")
    .replace(/<nav class="toc-tuner"[\s\S]*?<\/nav>/g, "");
}
export function wordsOf(html) {
  const text = squashText(mainOf(html).replace(/<span class="install__prompt"[^>]*>\$<\/span>/g, ""));
  return { sha: createHash("sha256").update(text).digest("hex"), length: text.length,
    hrefs: [...mainOf(html).matchAll(/href="([^"]*)"/g)].map((m) => m[1]) };
}

// Every file under a directory with one of the extensions (sorted, absolute paths).
export function walk(dir, exts, skip = () => false) {
  const out = [];
  for (const name of readdirSync(dir).sort()) {
    const p = path.join(dir, name);
    if (skip(p)) continue;
    if (statSync(p).isDirectory()) out.push(...walk(p, exts, skip));
    else if (exts.has(path.extname(name))) out.push(p);
  }
  return out;
}
export const read = (p) => readFileSync(p, "utf8");

// ---- where a page's source lives ----------------------------------------------------------
// A page is written once: as web/src/<page>.html until it is handed to the app, then as
// web/app/routes/<route>/+page.svelte (or +page.md). A test that reads a page's SOURCE (its
// markup as authored, not the built output) reads it through here and keeps working across
// the handover. The route's body markup is the legacy markup; the chrome includes are
// component tags (<Nav .../>, <Footer .../>) and the head is a siteHead() call.
import { existsSync } from "node:fs";
export const routeOf = (page) => (page === "index.html" ? "" : page.replace(/\.html$/, ""));
export function pageSourcePath(page) {
  const legacy = path.join(SRC, page);
  if (existsSync(legacy)) return legacy;
  for (const f of ["+page.svelte", "+page.md"]) {
    const p = path.join(APP, "routes", routeOf(page), f);
    if (existsSync(p)) return p;
  }
  throw new Error(`${page}: no source under web/src or web/app/routes`);
}
// A route that mounts islands (<Island name=... component={X} />) reads with the island
// components' sources appended, and the components those import in turn (an island is a
// tree: the billing card composes its spend-limit panel, its top-up and its help modal): the
// markup a page shows is the markup of all of them. The chrome in lib/ui (nav, footer, the
// account sub-nav) stays what a partial was, read with component().
const resolveSvelte = (spec, from) => (spec.startsWith("#lib/") ? path.join(APP, "lib", spec.slice(5)) : path.resolve(path.dirname(from), spec));
export function pageSource(page) {
  const route = readFileSync(pageSourcePath(page), "utf8");
  const parts = [route];
  const seen = new Set();
  const add = (file) => {
    if (seen.has(file) || file.startsWith(path.join(APP, "lib", "ui") + path.sep)) return;
    seen.add(file);
    const src = readFileSync(file, "utf8");
    parts.push(src);
    for (const [, spec] of src.matchAll(/import \w+ from "([^"]+\.svelte)"/g)) add(resolveSvelte(spec, file));
  };
  for (const [, name] of route.matchAll(/<Island\b[^>]*component=\{(\w+)\}/g)) {
    const imp = route.match(new RegExp(`import ${name} from "#lib/([^"]+)"`));
    if (imp) add(path.join(APP, "lib", imp[1]));
  }
  return parts.join("\n");
}
export const isAppPage = (page) => !existsSync(path.join(SRC, page));
// Every page of the site, legacy and app, sorted: what readdirSync(SRC) used to mean.
export function allPages() {
  const out = [];
  const walk = (dir, rel) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.isDirectory()) walk(path.join(dir, e.name), rel ? `${rel}/${e.name}` : e.name);
      else if (/^\+page\.(svelte|md)$/.test(e.name)) out.push(rel ? `${rel}.html` : "index.html");
    }
  };
  walk(path.join(APP, "routes"), "");
  return out.sort();
}
// A chrome component's source (app/lib/ui/<Name>.svelte): what a partial was.
export const component = (name) => readFileSync(path.join(APP, "lib", "ui", `${name}.svelte`), "utf8");
export const components = () => readdirSync(path.join(APP, "lib", "ui")).filter((f) => f.endsWith(".svelte")).sort();

// A file of the site as authored: a top-level page through pageSource (legacy or app route),
// anything else (js/, styles/, _partials/, data/) from web/src.
export const source = (p) => (/^[\w-]+\.html$/.test(p) ? pageSource(p) : readFileSync(path.join(SRC, p), "utf8"));
export const hasPageSource = (page) => { try { pageSourcePath(page); return true; } catch { return false; } };
// A wave/card/cap fact in a page's source, in either spelling: the legacy {{wave:tier.label}}
// token or the app's facts.wave('tier.label') call.
export const hasFact = (src, kind, ref) => src.includes(`{{${kind}:${ref}}}`) || src.includes(`facts.${{"wave":"wave","wave-stage":"waveStage","wave-band":"waveBand","wave-air":"waveAir","card":"card","cap":"cap"}[kind]}('${ref}')`);
// Svelte writes <circle .../> as <circle ...></circle>; a regex written for the authored
// form reads the built page through this.
export const svgSelfClose = (h) => h.replace(/<(circle|line|path|rect|ellipse|polygon|polyline|use|stop|image)\b([^>]*)><\/\1>/g, "<$1$2/>");
// The whole element that `open` (a regex for its opening tag) starts, nested same-name
// tags counted, so a group inside a group is found by structure and never by indentation.
export function element(html, open, tag) {
  const m = html.match(open);
  if (!m) return null;
  const re = new RegExp(`<${tag}\\b|</${tag}>`, "g");
  re.lastIndex = m.index + m[0].length;
  let depth = 1, x;
  while ((x = re.exec(html))) {
    depth += x[0].startsWith("</") ? -1 : 1;
    if (!depth) return html.slice(m.index, x.index + x[0].length);
  }
  return null;
}

// The site runtime: the scripts app/runtime.js bundles (src/js/<name>.js, in order), and the one
// tag a built page carries for the bundle (js/runtime.js, a module, cache-busted).
export const runtimeScripts = () => [...readFileSync(path.join(APP, "runtime.js"), "utf8").matchAll(/import "\.\.\/src\/js\/([\w-]+)\.js"/g)].map((m) => m[1]);
export const RUNTIME_TAG = /<script type="module" src="js\/runtime\.js\?v=[0-9a-f]+"><\/script>/;
// A page's own bundle: the scripts app/pages/<name>.js carries (src/js/<name>.js, in order), and
// the tag a built page carries for it.
export const pageBundle = (name) => [...readFileSync(path.join(APP, "pages", `${name}.js`), "utf8").matchAll(/import "\.\.\/\.\.\/src\/js\/([\w-]+)\.js"/g)].map((m) => m[1]);
export const pageBundles = () => readdirSync(path.join(APP, "pages")).filter((f) => f.endsWith(".js")).map((f) => f.replace(/\.js$/, "")).sort();
export const PAGE_TAG = (name) => new RegExp(`<script type="module" src="js/pages/${name}\\.js\\?v=[0-9a-f]+"><\\/script>`);
