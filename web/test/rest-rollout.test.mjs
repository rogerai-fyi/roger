// The design-system rollout to the remaining pages (2026-09): the marketing pages (app,
// manual, 404, confidential, why) take the homepage look, and the product surfaces (login,
// account, billing, keys, console, dashboard, device, stations, payouts, usage, private, r)
// move onto the shared system. A PRESENTATION-only pass, so these tests pin its promises:
//   (a) the words did not change: each page's <main> text and its links are the pre-rollout
//       copy, captured from the build into fixtures/rest-pages-text.json (whitespace free).
//       A deliberate COPY change must update that fixture in the same commit.
//   (b) the long pages get the shared TOC tuner (never pinned), built from their own labels.
//   (c) the product surfaces load the shared runtime the same way every other page does,
//       deferred, in the same order, so behaviour is unchanged.
//   (d) no token fallback hides a token that does not exist.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const stripCss = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const fixture = JSON.parse(readFileSync(path.join(WEB, "test/fixtures/rest-pages-text.json"), "utf8"));

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// the tuners these pages GAIN repeat the page's own section labels; app.html had no
// contents before, so its tuner is checked on its own below (every name is a label
// already on the page) and left out of the words comparison
const NEW_TUNER = new Set(["app.html"]);
function mains(page) {
  let html = dist(page).replace(/<!--[\s\S]*?-->/g, "");
  const m = [...html.matchAll(/<main\b[\s\S]*?<\/main>/g)].map((x) => x[0]).join("\n") || html;
  let out = m.replace(/<script\b[\s\S]*?<\/script>/g, "");
  if (NEW_TUNER.has(page)) out = out.replace(/<nav\b[^>]*data-tuner[\s\S]*?<\/nav>/g, "");
  return out;
}
const squash = (h) => h.replace(/<[^>]+>/g, " ").replace(/\s+/g, "");

for (const page of Object.keys(fixture)) {
  test(`(a) ${page}: the words and links are the pre-rollout copy`, () => {
    const m = mains(page);
    assert.equal(squash(m), fixture[page].text, `${page}: visible text changed; the rollout moves words, never edits them`);
    assert.deepEqual([...m.matchAll(/href="([^"]*)"/g)].map((x) => x[1]), fixture[page].hrefs, `${page}: links changed`);
  });
}

/* ---- (b) the tuners ---------------------------------------------------------------- */

const tunerOf = (html) => html.match(/<nav class="toc-tuner[^"]*" data-tuner[^>]*>[\s\S]*?<\/nav>/)?.[0] || "";
const stations = (tuner) => [...tuner.matchAll(/<a class="toc-tuner__st" href="#([^"]+)"><b>([^<]*)<\/b><span class="toc-tuner__name">([^<]*)<\/span><\/a>/g)]
  .map((m) => ({ id: m[1], no: m[2], name: m[3] }));

test("(b) the manual's contents are a tuner of every section, in order, and nothing is pinned", () => {
  const html = src("manual.html");
  const t = tunerOf(html);
  assert.ok(t, "manual.html has a <nav class=\"toc-tuner\" data-tuner>");
  assert.match(t, /aria-label="Manual contents"/, "the contents keep their accessible name");
  const st = stations(t);
  const ids = [...html.matchAll(/<section class="man-sec" id="([^"]+)"/g)].map((m) => m[1]);
  assert.equal(ids.length, 19, "the manual has its 19 sections");
  assert.deepEqual(st.map((s) => s.id), ids, "one station per section, in page order");
  assert.doesNotMatch(html, /class="man-toc/, "the old sticky rail is gone");
  assert.doesNotMatch(stripCss(src("styles/manual.css")), /position:\s*sticky/, "nothing on the manual is sticky");
});

test("(b) the app page tunes across its numbered sections with the shared tuner, not a page-local dial", () => {
  const html = src("app.html");
  const st = stations(tunerOf(html));
  const secs = [...html.matchAll(/<section class="section[^"]*" id="([^"]+)"[\s\S]*?class="sectionno">(§\d+) \/ ([^<]*)</g)]
    .map((m) => ({ id: m[1], no: m[2], label: m[3] }));
  assert.ok(secs.length >= 11, `the page keeps its numbered sections (got ${secs.length})`);
  assert.deepEqual(st.map((s) => s.id), secs.map((s) => s.id), "one station per numbered section, in order");
  assert.deepEqual(st.map((s) => s.no), secs.map((s) => s.no), "each station carries its section's number");
  for (const [i, s] of st.entries()) {
    assert.ok(secs[i].label.startsWith(s.name), `station ${s.no} is named from its own label: "${s.name}" vs "${secs[i].label}"`);
  }
  assert.doesNotMatch(html, /app-dialrule/, "the per-section dial rules gave way to the tuner");
  assert.doesNotMatch(src("styles/app.css"), /app-dialrule/, "and their styles went with them");
});

test("(b) the tuner component takes up to 20 stations and turns into a list on phones past 12", () => {
  const css = stripCss(src("styles/components.css"));
  for (let n = 13; n <= 20; n++) {
    assert.match(css, new RegExp(`\\.toc-tuner:has\\(li:nth-child\\(${n}\\):last-child\\)\\s*\\{\\s*--tuner-n:\\s*${n};`), `counts ${n} stations`);
    assert.match(css, new RegExp(`\\.toc-tuner:has\\(li:nth-child\\(${n}\\) a:is\\(:hover, :focus-visible\\)\\)\\s*\\{\\s*--at:\\s*${n - 1};`), `points at station ${n}`);
  }
  assert.match(css, /\.toc-tuner:has\(li:nth-child\(13\)\)/, "a many-station tuner has a list form");
});

/* ---- panels and scroll ------------------------------------------------------------- */

test("the rolled-out marketing pages drop the page-local dot texture and use shared panels", () => {
  assert.doesNotMatch(src("styles/app.css"), /--app-static|data:image\/svg/, "no background dot field on the app page");
  assert.match(src("app.html"), /<main id="top" data-lift>/, "the app page opts in to the scroll lift");
  assert.match(src("app.html"), /class="tone-zone" data-tone="ink"/, "the app page uses the ink panel");
  for (const page of ["manual.html", "confidential.html", "404.html"]) {
    assert.match(src(page), /class="[^"]*\btint-panel\b/, `${page} uses the shared tinted panel`);
  }
});

/* ---- (c) the product surfaces ------------------------------------------------------ */

const PRODUCT = ["login.html", "account.html", "billing.html", "keys.html", "console.html", "dashboard.html",
  "device.html", "stations.html", "payouts.html", "usage.html", "private.html", "r.html", "confidential.html"];

test("(c) every product surface ships the shared runtime, every script deferred", () => {
  for (const page of PRODUCT) {
    const html = dist(page);
    const body = html.slice(html.indexOf("<body"));
    const scripts = [...body.matchAll(/<script\b[^>]*\bsrc="([^"]+)"[^>]*>/g)];
    assert.ok(scripts.length, `${page} has scripts`);
    for (const [tag] of scripts) assert.match(tag, /\sdefer\b/, `${page}: ${tag} is deferred like every other page's`);
    for (const js of ["site", "session", "tuner", "scrub", "anchor-hold", "range-twin"]) {
      assert.match(body, new RegExp(`<script src="/?js/${js}\\.js\\?v=`), `${page} ships js/${js}.js`);
    }
  }
});

test("(c) a page's own scripts keep their order relative to each other and to the runtime", () => {
  // the account pages ran their page script BEFORE site.js (sync); with everything
  // deferred the order is the document order, so the source order must be unchanged
  const order = (page) => [...dist(page).matchAll(/<script\b[^>]*\bsrc="\/?js\/([\w-]+)\.js/g)].map((m) => m[1])
    .filter((n) => !["easter-egg", "lets-talk", "promo", "tuner", "scrub", "anchor-hold", "range-twin", "session"].includes(n));
  assert.deepEqual(order("login.html"), ["theme-init", "fmt", "auth", "site", "login-next", "email-login"]);
  assert.deepEqual(order("account.html"), ["theme-init", "fmt", "account", "site"]);
  assert.deepEqual(order("billing.html"), ["theme-init", "fmt", "billing", "billing-help", "site"]);
  assert.deepEqual(order("keys.html"), ["theme-init", "keys", "site"]);
  assert.deepEqual(order("private.html"), ["theme-init", "site", "private"]);
  assert.deepEqual(order("r.html"), ["theme-init", "site", "r"]);
  assert.deepEqual(order("device.html"), ["theme-init", "auth", "device", "site"]);
  assert.deepEqual(order("stations.html"), ["theme-init", "fmt", "auth", "stations", "site"]);
});

test("(c) the page runtime partial has one form: deferred", () => {
  const partial = src("_partials/site-js.html");
  assert.doesNotMatch(partial, /sync=1/, "no blocking variant");
  for (const m of partial.matchAll(/<script\b[^>]*>/g)) assert.match(m[0], /\sdefer\b/, m[0]);
});

/* ---- (d) tokens ---------------------------------------------------------------------- */

test("(d) the product sheets name only tokens that exist (no fallback papering over a typo)", () => {
  const all = ["tokens.css", "base.css", "components.css", "account-base.css"].map((f) => stripCss(src(`styles/${f}`))).join("\n");
  const defined = new Set([...all.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]));
  // a custom property a page sets inline (style="--i:1") or a script sets is a hook, not a token
  for (const f of readdirSync(path.join(WEB, "src")).filter((f) => f.endsWith(".html"))) {
    for (const m of src(f).matchAll(/style="[^"]*?(--[\w-]+)\s*:/g)) defined.add(m[1]);
  }
  for (const f of readdirSync(path.join(WEB, "src/js"))) {
    for (const m of src(`js/${f}`).matchAll(/setProperty\(\s*["'](--[\w-]+)/g)) defined.add(m[1]);
  }
  for (const sheet of ["device.css", "stations.css", "payouts.css", "private.css", "account-base.css", "account.css",
    "billing.css", "keys.css", "console.css", "dashboard.css", "metrics.css", "manual.css", "app.css", "notfound.css"]) {
    const css = stripCss(src(`styles/${sheet}`));
    const own = new Set([...css.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]));
    for (const m of css.matchAll(/var\((--[\w-]+)/g)) {
      assert.ok(defined.has(m[1]) || own.has(m[1]), `${sheet}: var(${m[1]}) is not a token`);
    }
  }
});

/* ---- (e) contrast on the signed-in plate --------------------------------------------- */

// The plate (.card) is a raised --white face in a tinted panel now, with command blocks
// (.cmd) on --paper-3 and wells on --paper/--paper-2. Every
// ink used for text on it must hold AA (4.5:1) on both grounds, light and dark, including
// the tertiary --ink-400 its labels and fine print use (2.7:1 on the tint as a bare token).
function tokenSets() {
  const css = stripCss(src("styles/tokens.css"));
  const block = (sel) => {
    const i = css.indexOf(sel + " {") >= 0 ? css.indexOf(sel + " {") : css.indexOf(sel + "{");
    if (i < 0) return {};
    const body = css.slice(css.indexOf("{", i) + 1, css.indexOf("}", i));
    return Object.fromEntries([...body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));
  };
  const light = block(":root");
  const darkCss = css.match(/:root\[data-theme="dark"\],\s*\.tone-zone\s*\{([^}]*)\}/)[1];
  const dark = { ...light, ...Object.fromEntries([...darkCss.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()])) };
  const plate = block(".card");
  return { light, dark, plate };
}
const resolve = (set, v) => { for (let i = 0; i < 5 && /^var\(/.test(v); i++) v = set[v.match(/var\((--[\w-]+)\)/)[1]]; return v; };
const lum = (hex) => {
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((x) => (x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
};
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };

test("(e) every text ink on the signed-in plate holds AA on its grounds, light and dark", () => {
  const { light, dark, plate } = tokenSets();
  assert.ok(Object.keys(plate).length, "tokens.css scopes the plate's inks (.card { ... })");
  for (const [theme, base] of [["light", light], ["dark", dark]]) {
    const set = { ...base, ...plate };
    for (const ink of ["--ink-400", "--ink-500", "--ink-700", "--ink-900"]) {
      for (const ground of ["--white", "--paper-2", "--paper-3", "--paper"]) {
        const fg = resolve(set, set[ink]), bg = resolve(set, set[ground]);
        const r = ratio(fg, bg);
        assert.ok(r >= 4.5, `${theme}: ${ink} ${fg} on ${ground} ${bg} is ${r.toFixed(2)}:1`);
      }
    }
  }
});
