// The dual build (build.mjs): the SvelteKit app in app/ and the legacy pages in src/ write
// one dist/, one owner per page. These rules keep the handover honest while both exist.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, existsSync, statSync } from "node:fs";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { createHash } from "node:crypto";
import { WEB, DIST, SRC, APP, read, allPages, pageSourcePath, pageBundle, pageBundles, PAGE_TAG } from "./_html.mjs";
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

test("the site runtime is one bundle: built, loaded once by every page that asks for it, its sources never shipped twice", () => {
  const bundled = [...read(path.join(APP, "runtime.js")).matchAll(/import "\.\.\/src\/js\/([\w-]+\.js)"/g)].map((m) => m[1]);
  assert.ok(bundled.length >= 9, "app/runtime.js imports the runtime scripts");
  for (const f of bundled) {
    assert.ok(existsSync(path.join(SRC, "js", f)), `src/js/${f} is a runtime source`);
    assert.ok(!existsSync(path.join(DIST, "js", f)), `dist/js/${f} is not shipped on its own (it is inside js/runtime.js)`);
  }
  const built = path.join(DIST, "js", "runtime.js");
  assert.ok(existsSync(built), "dist/js/runtime.js is built");
  assert.ok(statSync(built).size > 10_000, "and carries the scripts");
  assert.doesNotMatch(read(built), /^import[\s{"']/m, "the bundle imports nothing: one request");
  for (const page of owned) {
    const html = read(path.join(DIST, page));
    // a broadcast asks for it through its layout (lib/ui/BroadcastPost.svelte)
    const srcPath = pageSourcePath(page), routeSrc = read(srcPath);
    const asks = routeSrc + (srcPath.endsWith(".md") || /<BroadcastPost\b/.test(routeSrc) ? read(path.join(APP, "lib", "ui", "BroadcastPost.svelte")) : "");
    const wants = /\{\s*runtime:\s*true\s*\}/.test(asks);
    assert.equal((html.match(/<script type="module" src="js\/runtime\.js\?v=[0-9a-f]{8}">/g) || []).length, wants ? 1 : 0, `${page}: ${wants ? "loads the runtime once" : "asks for no runtime and gets none"}`);
    for (const f of bundled) assert.doesNotMatch(html, new RegExp(`src="js/${f.replace(".", "\\.")}`), `${page}: never loads js/${f} beside the bundle`);
  }
});

test("a page's own scripts are one bundle: built, loaded once by that page and no other, its sources never shipped twice", () => {
  const names = pageBundles();
  assert.ok(names.length >= 9, "app/pages has the pages that carry their own scripts");
  const page = (name) => (name === "index" ? "index.html" : `${name}.html`);
  for (const name of names) {
    const built = path.join(DIST, "js", "pages", `${name}.js`);
    assert.ok(existsSync(built), `dist/js/pages/${name}.js is built`);
    assert.doesNotMatch(read(built), /^import[\s{"']/m, `${name}: the bundle imports nothing`);
    for (const f of pageBundle(name)) {
      assert.ok(existsSync(path.join(SRC, "js", `${f}.js`)), `src/js/${f}.js is a source of pages/${name}`);
      assert.ok(!existsSync(path.join(DIST, "js", `${f}.js`)), `dist/js/${f}.js is not shipped on its own`);
    }
    const html = read(path.join(DIST, page(name)));
    assert.equal((html.match(PAGE_TAG(name)) || []).length, 1, `${page(name)} loads its bundle once`);
    assert.ok(html.indexOf("js/runtime.js") < html.indexOf(`js/pages/${name}.js`), `${page(name)}: the runtime comes first`);
    for (const other of readdirSync(DIST).filter((f) => f.endsWith(".html") && f !== page(name))) {
      assert.doesNotMatch(read(path.join(DIST, other)), PAGE_TAG(name), `${other} does not load pages/${name}.js`);
    }
  }
  // a redirect stub (bands.html) keeps its one sync script in the head: it must redirect before anything else loads
  for (const p of owned) if (!/http-equiv="refresh"/.test(read(path.join(DIST, p)))) assert.doesNotMatch(read(path.join(DIST, p)), /<script src="js\/(?!theme-init|easter-egg)[\w-]+\.js/, `${p}: no classic page script beside the bundles`);
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
  const islandsDir = path.join(DIST, "js", "islands");
  const built = existsSync(islandsDir) ? readdirSync(islandsDir).filter((f) => f.endsWith(".js")) : [];
  const sources = readdirSync(path.join(APP, "islands")).filter((f) => f.endsWith(".js"));
  for (const f of sources) assert.ok(used.has(f.replace(/\.js$/, "")), `app/islands/${f} is mounted by some page`);
  // a built file is an island entry, or a chunk some entry imports (the shared broker client)
  const imported = new Set(built.flatMap((f) => [...read(path.join(islandsDir, f)).matchAll(/from\s*"\.\/([\w-]+\.js)"|import\s*"\.\/([\w-]+\.js)"/g)].map((m) => m[1] || m[2])));
  for (const f of built) assert.ok(sources.includes(f) || imported.has(f), `dist/js/islands/${f} is neither an island nor a chunk one imports`);
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
