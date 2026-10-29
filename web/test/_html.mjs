// Shared HTML normalizers for the guard suite.
//
// Two renderers emit the site's pages during the migration: the legacy include build and
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

const NAMED = { lt: "<", gt: ">", quot: '"', apos: "'", amp: "&",
  rsquo: "\u2019", lsquo: "\u2018", rdquo: "\u201d", ldquo: "\u201c", middot: "\u00b7", nbsp: "\u00a0",
  hellip: "\u2026", sect: "\u00a7", rarr: "\u2192", larr: "\u2190", darr: "\u2193", uarr: "\u2191",
  copy: "\u00a9", reg: "\u00ae", trade: "\u2122", times: "\u00d7", bull: "\u2022", ndash: "\u2013",
  mdash: "\u2014", ordm: "\u00ba", deg: "\u00b0", micro: "\u00b5", para: "\u00b6", laquo: "\u00ab",
  raquo: "\u00bb", euro: "\u20ac", pound: "\u00a3", yen: "\u00a5", cent: "\u00a2", frac12: "\u00bd",
  frac14: "\u00bc", frac34: "\u00be", plusmn: "\u00b1", ne: "\u2260", le: "\u2264", ge: "\u2265",
  infin: "\u221e", check: "\u2713", star: "\u2606", hearts: "\u2665", shy: "\u00ad", zwj: "\u200d",
  ensp: "\u2002", emsp: "\u2003", thinsp: "\u2009", prime: "\u2032", Prime: "\u2033" };
// the five that make or break markup stay as written in a markup-level decode
const MARKUP_SAFE = new Set(["lt", "gt", "amp", "quot", "apos"]);
const MARKUP_SAFE_CODES = new Set([60, 62, 38, 34, 39]);

function decodeWith(s, keepMarkup) {
  return s
    .replace(/&#x([0-9a-f]+);/gi, (m, h) => { const c = parseInt(h, 16); return keepMarkup && MARKUP_SAFE_CODES.has(c) ? m : String.fromCodePoint(c); })
    .replace(/&#(\d+);/g, (m, d) => { const c = Number(d); return keepMarkup && MARKUP_SAFE_CODES.has(c) ? m : String.fromCodePoint(c); })
    .replace(/&([a-zA-Z0-9]+);/g, (m, n) => (keepMarkup && MARKUP_SAFE.has(n)) ? m : (NAMED[n] ?? m));
}
// Tag-stripped text: every entity decoded (&amp; last, so "&amp;lt;" is "&lt;" not "<").
export const decodeText = (s) => decodeWith(s.replace(/&amp;/g, "\u0000amp;"), false).replace(/\u0000amp;/g, "&");
// Markup: named and numeric entities decoded EXCEPT the ones that would change the parse.
export const decodeMarkup = (h) => decodeWith(h, true);

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
