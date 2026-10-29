// The dual build (build.mjs): the SvelteKit app in app/ and the legacy pages in src/ write
// one dist/, one owner per page. These rules keep the handover honest while both exist.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, existsSync, statSync } from "node:fs";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { WEB, DIST, SRC, APP, read } from "./_html.mjs";
import { transform } from "../scripts/gen-fmt.mjs";

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const owned = JSON.parse(read(path.join(APP, "owned.json")));
const routeDir = (page) => path.join(APP, "routes", page === "index.html" ? "" : page.replace(/\.html$/, ""));
const routeFile = (page) => ["+page.svelte", "+page.md"].map((f) => path.join(routeDir(page), f)).find(existsSync);

test("app/owned.json is a sorted list of page names, each with a route and no legacy twin", () => {
  assert.deepEqual(owned, [...owned].sort(), "owned.json is sorted");
  assert.deepEqual(owned, [...new Set(owned)], "owned.json has no duplicate");
  for (const page of owned) {
    assert.match(page, /^[\w-]+\.html$/, `${page} is a top-level page name`);
    assert.ok(routeFile(page), `${page} has app/routes${page === "index.html" ? "" : "/" + page.replace(/\.html$/, "")}/+page.svelte or +page.md`);
    assert.ok(!existsSync(path.join(SRC, page)), `${page} is owned by the app, so web/src/${page} must be gone (one owner per page)`);
    assert.ok(existsSync(path.join(DIST, page)), `${page} was built`);
  }
});

test("the app shell carries the legacy head's two CSP-hashed inline scripts, byte for byte", () => {
  // the edge CSP allow-lists their sha256 (web/scripts/cf-edge.mjs); the bytes must not drift
  const head = read(path.join(SRC, "_partials", "head.html"));
  const shell = read(path.join(APP, "app.html"));
  const inline = (h) => [...h.matchAll(/^\s*(<script>(?!\s*\(function \(\) \{\s*try)[^\n]*<\/script>)\s*$/gm)].map((m) => m[1]);
  assert.equal(inline(head).length, 2, "head.html has the two hashed inline scripts");
  assert.deepEqual(inline(shell), inline(head), "app/app.html carries the same two, unchanged");
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
    assert.doesNotMatch(html, /<script>(?!document\.documentElement|\(function\(\)\{function r)/, `${page}: no inline script beyond the shell's two (no SvelteKit loader: islands only)`);
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
    assert.ok(used.has(name) || !owned.length, `app/islands/${f} is mounted by some owned page`);
  }
  if (owned.length) for (const f of built) assert.ok(sources.includes(f) || used.has(f.replace(/\.js$/, "")), `dist/js/islands/${f} comes from an island or a chunk of one`);
});

test("the legacy half still needs nothing installed: build.mjs imports only node: modules and web/scripts", () => {
  const src = read(path.join(WEB, "build.mjs"));
  for (const m of src.matchAll(/^import .* from "([^"]+)";/gm))
    assert.ok(/^node:|^\.\/scripts\//.test(m[1]), `build.mjs imports ${m[1]}: the legacy build stays dependency-free`);
});
