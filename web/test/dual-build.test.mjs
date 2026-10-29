// The dual build (build.mjs): the SvelteKit app in app/ and the legacy pages in src/ write
// one dist/, one owner per page. These rules keep the handover honest while both exist.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, existsSync, statSync } from "node:fs";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { createHash } from "node:crypto";
import { WEB, DIST, SRC, APP, read, allPages } from "./_html.mjs";
import { transform } from "../scripts/gen-fmt.mjs";
import { THEME_INLINE } from "../app/lib/head.js";

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const owned = allPages();   // every route is a page
const routeDir = (page) => path.join(APP, "routes", page === "index.html" ? "" : page.replace(/\.html$/, ""));
const routeFile = (page) => ["+page.svelte", "+page.md"].map((f) => path.join(routeDir(page), f)).find(existsSync);

test("every route is a page: it builds, and nothing is left in web/src for it", () => {
  for (const page of owned) {
    assert.match(page, /^[\w-]+\.html$/, `${page} is a top-level page name`);
    assert.ok(!existsSync(path.join(SRC, page)), `${page}: a page is a route, never a file in web/src`);
    assert.ok(existsSync(path.join(DIST, page)), `${page} was built`);
  }
});

test("the shell's inline scripts are the ones the edge CSP allow-lists, byte for byte", () => {
  // the edge policy (src/_headers, applied by scripts/cf-edge.mjs) names each inline script's
  // sha256; the shell's two and the homepage theme script (app/lib/head.js) must hash to them
  const csp = read(path.join(SRC, "_headers")).match(/Content-Security-Policy:([^\n]*)/)[1];
  const allowed = [...csp.matchAll(/'sha256-([^']+)'/g)].map((m) => m[1]);
  assert.equal(allowed.length, 3, "three inline scripts are allow-listed");
  const shell = read(path.join(APP, "app.html"));
  const inline = [...shell.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
  assert.equal(inline.length, 2, "the shell carries two inline scripts");
  const theme = THEME_INLINE.replace(/^<script>/, "").replace(/<\/script>$/, "");
  for (const body of [...inline, theme]) {
    const h = createHash("sha256").update(body).digest("base64");
    assert.ok(allowed.includes(h), `an inline script is not in the edge allow-list: ${body.slice(0, 50)}...`);
  }
});

test("app/lib/fmt.js is the generated form of src/js/fmt.js (one money renderer, web == CLI)", () => {
  assert.equal(read(path.join(APP, "lib", "fmt.js")), transform(read(path.join(SRC, "js", "fmt.js"))),
    "regenerate with: node web/scripts/gen-fmt.mjs");
});

test("every app-built page went through the finishing passes", () => {
  for (const page of owned) {
    const html = read(path.join(DIST, page));
    assert.equal((html.match(/rel="canonical"/g) || []).length, 1, `${page}: exactly one canonical`);
    assert.doesNotMatch(html, /\{\{|<!--\s*include:/, `${page}: no template leftovers`);
    assert.doesNotMatch(html, /\s(hidden|data-[\w-]+)=""/, `${page}: flags are spelled bare, like the legacy pages`);
    for (const m of html.matchAll(/(?:src|href)="((?:js|styles|assets)\/[^"]+)"/g))
      assert.match(m[1], /\?v=[0-9a-f]{8}$/, `${page}: ${m[1]} is cache-busted`);
    assert.doesNotMatch(html, /<script>(?!document\.documentElement|\(function\(\)\{function r|\s*\(function \(\) \{\s*try \{\s*var saved)/, `${page}: no inline script beyond the shell's two and the homepage theme script (no SvelteKit loader: islands only)`);
    assert.doesNotMatch(html.replace(/<div data-island="[\w-]+">[\s\S]*?<!--\/island-->/g, ""), /<!--(?:\[-?\d*|\]|!|)-->/, `${page}: no hydration comment outside an island`);
    assert.doesNotMatch(html, /_app\/immutable\/[^"]*\.js/, `${page}: no SvelteKit client chunk (a route that forgot csr = false hydrates twice, the island and the client)`);
  }
});

test("every island a page names is built and referenced, and every built island is used", () => {
  const used = new Map();
  for (const page of readdirSync(DIST).filter((f) => f.endsWith(".html"))) {
    const html = read(path.join(DIST, page));
    for (const m of html.matchAll(/data-island="([\w-]+)"/g)) {
      used.set(m[1], page);
      assert.match(html, new RegExp(`<script type="module" src="js/islands/${m[1]}\\.js\\?v=`), `${page}: loads its island ${m[1]}`);
      assert.ok(existsSync(path.join(DIST, "js", "islands", `${m[1]}.js`)), `${page}: island ${m[1]} was built`);
    }
  }
  const built = existsSync(path.join(DIST, "js", "islands")) ? readdirSync(path.join(DIST, "js", "islands")).filter((f) => f.endsWith(".js")) : [];
  const sources = readdirSync(path.join(APP, "islands")).filter((f) => f.endsWith(".js"));
  for (const f of sources) {
    const name = f.replace(/\.js$/, "");
    assert.ok(used.has(name), `app/islands/${f} is mounted by some page`);
  }
  for (const f of built) assert.ok(sources.includes(f) || used.has(f.replace(/\.js$/, "")), `dist/js/islands/${f} comes from an island or a chunk of one`);
});

test("every app route is an owned page (no orphan route)", () => {
  const routes = [];
  const walk = (dir, rel) => { for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory()) walk(path.join(dir, e.name), rel ? `${rel}/${e.name}` : e.name);
    else if (/^\+page\.(svelte|md)$/.test(e.name)) routes.push(rel ? `${rel}.html` : "index.html"); } };
  walk(path.join(APP, "routes"), "");
  assert.deepEqual(routes.sort(), owned, "every +page.* under app/routes is a page the build knows");
});

test("the legacy half still needs nothing installed: build.mjs imports only node: modules and web/scripts", () => {
  const src = read(path.join(WEB, "build.mjs"));
  for (const m of src.matchAll(/^import .* from "([^"]+)";/gm))
    assert.ok(/^node:|^\.\/scripts\//.test(m[1]), `build.mjs imports ${m[1]}: the legacy build stays dependency-free`);
});
