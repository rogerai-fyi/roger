#!/usr/bin/env node
// Port a legacy page to an app route, mechanically.
//
//   node scripts/port-page.mjs <page.html> [--force]
//
// web/src/<page>.html -> web/app/routes/<route>/+page.svelte. The head include becomes a
// siteHead() call, the chrome includes become components, {{wave|card|cap}} tokens become
// content() calls, the page's own scripts move to the head (still external, still deferred,
// in document order), braces are escaped. The body copy is NOT edited. The body's attributes
// go to app/body-attrs.json. Then:
//   node scripts/page-parity.mjs <page.html>    build both, compare text/headings/links/ids/meta
//   node scripts/handover.mjs <page.html>       own it, drop the legacy page and its CSS entry
import { readFileSync, writeFileSync, existsSync, mkdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..");
const SRC = join(WEB, "src");
const APP = join(WEB, "app");
const [page, ...flags] = process.argv.slice(2);
if (!page || !page.endsWith(".html")) throw new Error("usage: port-page.mjs <page.html> [--force]");
const route = page === "index.html" ? "" : page.replace(/\.html$/, "");
const routeDir = join(APP, "routes", route);
const outFile = join(routeDir, "+page.svelte");
if (existsSync(outFile) && !flags.includes("--force")) throw new Error(`${outFile} exists (use --force to overwrite)`);
const html = existsSync(join(SRC, page)) ? readFileSync(join(SRC, page), "utf8")
  : execFileSync("git", ["show", `HEAD:web/src/${page}`], { cwd: WEB, encoding: "utf8" });   // handed over already: the last committed legacy page

// ---- args of an include marker, as build.mjs parses them -----------------------------------
function parseArgs(s) {
  const out = {};
  const re = /([\w-]+)=("([^"]*)"|'([^']*)'|(\S+))/g;
  let m;
  while ((m = re.exec(s))) out[m[1]] = m[3] ?? m[4] ?? m[5] ?? "";
  return out;
}
const includes = (s) => [...s.matchAll(/<!--\s*include:\s*([^\s]+)\s*(.*?)\s*-->/g)].map((m) => ({ raw: m[0], file: m[1], args: parseArgs(m[2]) }));
const jsStr = (s) => "'" + String(s).replace(/\\/g, "\\\\").replace(/'/g, "\\'").replace(/\n/g, "\\n") + "'";
const attr = (name, v) => (v === undefined ? "" : ` ${name}=${jsStr(v)}`);
const tpl = (s) => "`" + s.replace(/\\/g, "\\\\").replace(/`/g, "\\`").replace(/\$\{/g, "\\${").replace(/<\/script>/g, "<\\/script>") + "`";
// a raw block held in the body: markup, so a literal </script> is fine (and the guard
// suite's script-stripping regexes see it); only the component <script> block needs <\/script>
const raw = (s) => "`" + s.replace(/\\/g, "\\\\").replace(/`/g, "\\`").replace(/\$\{/g, "\\${") + "`";

// ---- the document's parts, located with the comments blanked (a doc comment may mention
// <head> or <body>); the top doc comment, before <html>, is kept for the route's readers
const blank = html.replace(/<!--(?!\s*include:)[\s\S]*?-->/g, (m) => " ".repeat(m.length));
const at = (re, from = 0) => { const m = blank.slice(from).match(re); if (!m) throw new Error(`${page}: no ${re}`); return from + m.index; };
const headStart = at(/<head>/), headEnd = at(/<\/head>/, headStart);
const bodyStart = at(/<body[^>]*>/, headEnd), bodyEnd = at(/<\/body>/, bodyStart);
const docComment = (html.slice(0, at(/<html/)).match(/<!--[\s\S]*?-->/g) || []).join("\n");

// ---- head ---------------------------------------------------------------------------------
const headHtml = html.slice(headStart + "<head>".length, headEnd);
const headInc = includes(headHtml).find((i) => i.file === "head.html");
if (!headInc) throw new Error(`${page}: no head.html include`);
const h = headInc.args;
let extra = headHtml.replace(headInc.raw, "").replace(/<!--[\s\S]*?-->/g, "").trim();
// the page's CSS bundle, from the legacy manifest
const manifest = readFileSync(join(WEB, "build.mjs"), "utf8");
const bundle = manifest.match(new RegExp(`"${page.replace(".", "\\.")}":\\s*\\[([^\\]]*)\\]`));
if (!bundle) throw new Error(`${page}: no CSS_BUNDLES entry in build.mjs`);
const css = bundle[1].split(",").map((s) => s.trim().replace(/^"|"$/g, "")).filter(Boolean).map((s) =>
  s === "...CSS_MARKETING" || s === "...CSS_SHARED" ? "...CSS_SHARED" : s === "...CSS_ACCOUNT" ? "...CSS_ACCOUNT" : s);
const headOpts = [`title: ${jsStr(h.title)}`];
if (h.desc) headOpts.push(`desc: ${jsStr(h.desc)}`);
if (h.robots === "noindex") headOpts.push(`robots: "noindex"`);
if (h.theme === "inline") headOpts.push(`theme: "inline"`);
if (h.og === "1") headOpts.push(`og: "home"`);
else if (h.og === "post") headOpts.push(`og: { type: ${jsStr(h.ogtype || "website")}, title: ${jsStr(h.ogtitle)}, desc: ${jsStr(h.ogdesc)}, url: ${jsStr(h.ogurl)}, image: ${jsStr(h.ogimage)} }`);
else headOpts.push(`canonical: ${jsStr(page === "index.html" ? "https://rogerai.fm/" : "https://rogerai.fm/" + page)}`);
headOpts.push(`css: [${css.map((c) => (c.startsWith("...") ? c : jsStr(c))).join(", ")}]`);

// ---- body ---------------------------------------------------------------------------------
const bodyTag = blank.slice(bodyStart).match(/<body([^>]*)>/);
const bodyAttrs = bodyTag[1];
let body = html.slice(bodyStart + bodyTag[0].length, bodyEnd);

// the page's own scripts (external, deferred) and the shared runtime's place (the site-js
// include): to the head, in document order
const scripts = [];
body = body.replace(/^[ \t]*(?:<script\b([^>]*)\bsrc="([^"]+)"([^>]*)><\/script>|<!--\s*include:\s*site-js\.html\s*(.*?)\s*-->)[ \t]*\n?/gm, (m, a, src, b, siteArgs) => {
  if (src) scripts.push({ src, defer: /\bdefer\b/.test(a + b) });
  else scripts.push({ runtime: true, ...(parseArgs(siteArgs).promo === "0" ? { promo: false } : {}) });
  return "";
});
if (/<script\b(?![^>]*type="application\/(?:ld\+)?json")/.test(body)) throw new Error(`${page}: an inline script in the body; port it by hand`);

// placeholders keep raw segments (JSON-LD, expressions) out of the brace escaping below
const keep = [];
const hold = (s) => { keep.push(s); return `\u0001${keep.length - 1}\u0001`; };
// data scripts (JSON-LD, a page's JSON data block) are markup, kept verbatim
body = body.replace(/<script type="application\/(?:ld\+)?json"[^>]*>[\s\S]*?<\/script>/g, (m) => hold(`{@html ${raw(m)}}`));
// a page-scoped <style> block stays where it was, as raw markup (a component <style> would be scoped)
body = body.replace(/<style\b[^>]*>[\s\S]*?<\/style>/g, (m) => hold(`{@html ${raw(m)}}`));

// facts tokens -> content() calls; inside a tag an expression, in text {@html}
let usesFacts = false;
body = body.replace(/\{\{\s*(wave|wave-stage|wave-band|wave-air|card|card-num|card-lo|card-hi|card-ref|card-list|card-notes|cap):([^}]+?)\s*\}\}/g, (m, kind, ref, at) => {
  usesFacts = true;
  const fn = { wave: "wave", "wave-stage": "waveStage", "wave-band": "waveBand", "wave-air": "waveAir", card: "card", "card-num": "cardNum", "card-lo": "cardLo", "card-hi": "cardHi", "card-ref": "cardRef", "card-list": "cardList", "card-notes": "cardNotes", cap: "cap" }[kind];
  const before = body.slice(0, at);
  const inTag = before.lastIndexOf("<") > before.lastIndexOf(">");
  const call = `facts.${fn}(${jsStr(ref)})`;
  return hold(inTag ? `{${call}}` : `{@html ${call}}`);
});
if (/\{\{/.test(body)) throw new Error(`${page}: an unknown {{token}} in the body`);

// chrome includes -> components
const used = new Set();
const component = (name) => { used.add(name); return name; };
// an include arg is a string, or an expression when a facts token rode in it
const argExpr = (v) => {
  if (v === undefined) return undefined;
  if (!/\u0001\d+\u0001/.test(v)) return jsStr(v);
  return "{" + v.split(/(\u0001\d+\u0001)/).filter(Boolean).map((part) => {
    const m = part.match(/^\u0001(\d+)\u0001$/);
    return m ? keep[+m[1]].replace(/^\{@html |^\{/, "").replace(/\}$/, "") : jsStr(part);
  }).join(" + ") + "}";
};
const attrX = (name, v) => (v === undefined ? "" : ` ${name}=${argExpr(v)}`);
// an include arg as children markup: the literal parts raw, a facts token as a text expression
const argMarkup = (v) => v.split(/(\u0001\d+\u0001)/).filter(Boolean).map((part) => {
  const m = part.match(/^\u0001(\d+)\u0001$/);
  return m ? "{" + keep[+m[1]].replace(/^\{@html |^\{/, "").replace(/\}$/, "").replace(/^facts\./, "facts.t.") + "}" : part;
}).join("");
body = body.replace(/<!--\s*include:\s*([^\s]+)\s*(.*?)\s*-->/g, (raw, file, argStr) => {
  const a = parseArgs(argStr);
  switch (file) {
    case "nav.html": {
      const cur = Object.keys(a).find((k) => k.endsWith("current"));
      return hold(`<${component("Nav")} variant=${jsStr(a.variant || "lean")}${attr("home", a.home)}${a.promo === "0" ? " promo={false}" : ""}${cur ? ` current=${jsStr(cur.replace(/current$/, ""))}` : ""} />`);
    }
    case "rail.html": return hold(`<${component("Rail")} head=${argExpr(a.head)} rev=${argExpr(a.rev)}${a.orange === "1" ? " orange" : ""} />`);
    case "footer.html": return hold(`<${component("Footer")}${attr("anchors", a.anchors)} />`);
    case "onair.html": return hold(`<${component("OnAir")} />`);
    case "accountnav.html": return hold(`<${component("AccountNav")} current=${jsStr(a.current)} />`);
    case "install-box.html": return hold(`<${component("InstallBox")} id=${argExpr(a.id)}${attrX("size", a.size)}${attrX("oslock", a.oslock)}${attrX("label", a.label)}>${argMarkup(a.cmd)}</InstallBox>`);
    case "orange3d.html": return hold(`<${component("Orange3d")} id=${jsStr(a.id)} />`);
    case "promo.html": return hold(`<${component("Promo")} />`);
    case "site-js.html": return "";        // handled above: a { runtime: true } scripts entry
    case "lets-talk.html": return "";      // the dialog rides the footer
    case "tube-ping.html": return hold(`<${component("TubePing")} />`);
    case "wave-mark.html": return hold(`<${component("WaveMark")}${a.slim === "yes" ? " slim" : ""}${attr("caption", a.caption)} />`);
    case "wave-mesh.html": return hold(`<${component("WaveMesh")} no=${jsStr(a.no)} fig=${jsStr(a.fig)} parent=${jsStr(a.parent)} />`);
    case "wave-firewatch.html": return hold(`<${component("WaveFirewatch")} no=${jsStr(a.no)} fig=${jsStr(a.fig)} />`);
    case "adapter-dial.html": return hold(`<${component("AdapterDial")} />`);
    default: throw new Error(`${page}: no component for include ${file}`);
  }
});
for (const name of used) if (!existsSync(join(APP, "lib", "ui", `${name}.svelte`))) throw new Error(`${page}: app/lib/ui/${name}.svelte does not exist yet; port the partial first`);

// braces in copy are text, not expressions; then put the raw segments back
body = body.replace(/\{/g, "&#123;").replace(/\}/g, "&#125;");
while (/\u0001\d+\u0001/.test(body)) body = body.replace(/\u0001(\d+)\u0001/g, (_, i) => keep[+i]);
body = body.replace(/^\n+/, "").replace(/\s+$/, "\n");

// ---- write ----------------------------------------------------------------------------------
const imports = [`  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";`];
if (usesFacts) imports.push(`  import { content } from "#lib/content.js";`);
for (const name of [...used].sort()) imports.push(`  import ${name} from "#lib/ui/${name}.svelte";`);
const headCall = `siteHead({\n    ${headOpts.join(",\n    ")}${extra ? `,\n    extra: ${tpl(extra)}` : ""}${scripts.length ? `,\n    scripts: [${scripts.map((s) => s.runtime ? `{ runtime: true${s.promo === false ? ", promo: false" : ""} }` : `{ src: ${jsStr(s.src)}${s.defer ? ", defer: true" : ""} }`).join(", ")}]` : ""},\n  })`;
const out = `<script>
  // ${h.title}
  // Ported from web/src/${page} (${new Date().toISOString().slice(0, 10)}); the copy is the legacy copy.
${imports.join("\n")}
${usesFacts ? `  const facts = content(${jsStr(page)});\n` : ""}  const head = ${headCall};
</script>
<svelte:head>{@html head}</svelte:head>
${docComment ? docComment.replace(/-->\s*$/, "\n     (the page's doc comment, from the legacy file; its CSS bundle is the css: list above) -->") + "\n" : ""}${body}`;
mkdirSync(routeDir, { recursive: true });
writeFileSync(outFile, out);

const attrsFile = join(APP, "body-attrs.json");
const attrs = JSON.parse(readFileSync(attrsFile, "utf8"));
const routeId = "/" + route;
if (bodyAttrs.trim()) attrs[routeId] = bodyAttrs; else delete attrs[routeId];
writeFileSync(attrsFile, JSON.stringify(Object.fromEntries(Object.entries(attrs).sort()), null, 2) + "\n");
console.log(`${page} -> app/routes/${route}/+page.svelte (${scripts.length} script(s), ${used.size} component(s)${usesFacts ? ", facts" : ""})`);
