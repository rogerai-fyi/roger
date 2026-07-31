// Regression lock for the DECLUTTERED top nav + the pre-cut "App" slot (the IA reorg).
//
// The ONE shared nav (_partials/nav.html) renders TWO variants: "marketing" (section
// links + burger) and "lean" (account pages: brand + theme + account only). The reorg:
//   - top-bar sections: Models · Voices · App   (the same-page anchors Spec/Operating/
//     Monetize were REMOVED from the bar; they still live in the footer + scroll)
//   - utility cluster: "API keys" REMOVED from the bar (it's a signed-in action; it
//     moved to the footer Account group)
//   - an App Store CTA SLOT is RESERVED (a comment, cut but not live) after Log in,
//     before the theme toggle - no listing exists yet, so no badge/link is fabricated
//   - App -> /app.html, a real "tuning up" placeholder page (so the link 200s, not dead)
//   - the footer keeps the FULL map: everything pulled from the bar stays reachable there
//
// We build the site once (node:fs only, no install) and assert over the RENDERED dist
// so the gating ({{#if variant}}) is exercised for real, plus a few source-partial facts.
// Run: node --test test/nav.test.mjs   (picked up by `npm test`).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const readDist = (p) => readFileSync(path.join(DIST, p), "utf8");
const readSrc = (p) => readFileSync(path.join(SRC, p), "utf8");

// Build once so the assertions run against the real rendered chrome (both nav variants).
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// Slice out just the <header class="nav"> ... </header> block (the top bar) of a page.
const topbar = (html) => {
  const m = html.match(/<header class="nav[\s\S]*?<\/header>/);
  assert.ok(m, "page has a <header class=\"nav\"> block");
  return m[0];
};
// Only LIVE anchors - strip HTML comments first (the reorg leaves explanatory comments
// that legitimately mention "API keys" / "#spec" as prose, not as live links).
const liveHrefs = (block) =>
  [...block.replace(/<!--[\s\S]*?-->/g, "").matchAll(/<a\b[^>]*href="([^"]*)"/g)].map((m) => m[1]);

// The BAR items only - links inside a disclosure panel carry no .nav__link class.
// Asserting over every anchor would conflate "what the bar shows" with "what the
// panels reveal", which are different questions with different right answers.
// Drop disclosure panels before asking "what does the BAR link to, in order".
const withoutPanels = (block) => block.replace(/<div class="nav__panel"[\s\S]*?<\/div>/g, "");

const barHrefs = (block) =>
  [...block.replace(/<!--[\s\S]*?-->/g, "").matchAll(/<a\b[^>]*class="nav__link[^"]*"[^>]*href="([^"]*)"/g)]
    .map((m) => m[1]);

// A balanced slice. The sections container now nests a <div class="nav__panel">, so a
// non-greedy match to the first </div> stops inside the panel and silently under-reads
// the very thing being asserted.
const sectionsBlock = (bar) => {
  const start = bar.indexOf('<div class="nav__sections">');
  assert.ok(start >= 0, "nav has a sections group");
  let depth = 0, i = start;
  const tag = /<(\/?)div\b/g;
  tag.lastIndex = start;
  for (let m; (m = tag.exec(bar)); ) {
    depth += m[1] ? -1 : 1;
    if (depth === 0) { i = m.index + m[0].length; break; }
  }
  return bar.slice(start, bar.indexOf(">", i) + 1);
};

test("marketing top bar: Models · Research · Voices · App · Company and NOTHING else in the sections group", () => {
  const bar = topbar(readDist("index.html"));
  const sections = sectionsBlock(bar);
  const hrefs = barHrefs(sections);
  assert.deepEqual(hrefs, ["/models.html", "/research.html", "/voices.html", "/app.html", "/company.html"],
    "sections are exactly Models, Research, Voices, App, Company - the #spec/#how/#monetize anchors are gone");
});

test("marketing top bar: the removed items are NOT live links anywhere in the bar", () => {
  const links = liveHrefs(topbar(readDist("index.html")));
  for (const gone of ["#spec", "#how", "#monetize", "/keys.html"]) {
    assert.ok(!links.some((h) => h.includes(gone)), `${gone} is not a live top-bar link`);
  }
});

test("marketing top bar: order is Models·Research·Voices·App·Company | Manual·Source·Log in (then the reserved slot + toggle)", () => {
  const links = liveHrefs(withoutPanels(topbar(readDist("index.html"))));
  assert.deepEqual(links, [
    "#top",                                   // brand
    "/models.html", "/research.html", "/voices.html", "/app.html", "/company.html",
    "/manual.html",
    "https://github.com/rogerai-fyi/roger",   // Source (ghost)
    "/login.html",
  ], "the decluttered marketing bar renders the exact target link set, in order");
});

// The disclosure panels exist so the sub-pages are not discoverable only by landing
// on a hub and scrolling. Pin what they reveal, and pin the accessibility contract:
// the APG is explicit that site navigation must NOT claim role="menu", because that
// role promises arrow-key and typeahead behaviour this does not implement.
test("the nav panels reveal every page that is otherwise hub-only", () => {
  const bar = topbar(readDist("index.html"));
  const panelHrefs = [...bar.matchAll(/<div class="nav__panel"[\s\S]*?<\/div>/g)]
    .flatMap((m) => liveHrefs(m[0]));
  for (const must of [
    "/research-models.html",
    "/research-wave-family.html",
    "/research-industry.html",
    "/broadcasts.html",
    "/careers.html",
  ]) {
    assert.ok(panelHrefs.includes(must), `${must} is reachable from the nav, not just from a hub`);
  }
});

test("the nav disclosure follows the APG contract, not the menu role", () => {
  const bar = topbar(readDist("index.html"));
  const buttons = [...bar.matchAll(/<button class="nav__more"[^>]*>/g)].map((m) => m[0]);
  assert.ok(buttons.length >= 2, `expected a disclosure per group, found ${buttons.length}`);
  for (const b of buttons) {
    assert.match(b, /aria-expanded="false"/, "starts collapsed");
    assert.match(b, /aria-controls="[^"]+"/, "names the panel it controls");
    assert.match(b, /aria-label="[^"]+"/, "a caret glyph needs an accessible name");
    assert.match(b, /type="button"/, "never submits");
  }
  // Every aria-controls must resolve to a real id on this page.
  for (const b of buttons) {
    const id = b.match(/aria-controls="([^"]+)"/)[1];
    assert.ok(bar.includes(`id="${id}"`), `aria-controls="${id}" points at a real element`);
  }
  // Strip comments: the note explaining why we avoid role="menu" is not a role.
  assert.doesNotMatch(bar.replace(/<!--[\s\S]*?-->/g, ""), /role="menu(item)?"/,
    "site navigation does not claim the menu role");
  // Panels start hidden, so nothing depends on CSS to conceal them.
  assert.equal((bar.match(/<div class="nav__panel"[^>]*hidden/g) || []).length, buttons.length);
});

// With JavaScript off the panels never open, so each top-level item must still be a
// real link to a hub that lists the same destinations. The panel is an accelerator.
test("every disclosure group keeps a working link beside its caret", () => {
  const bar = topbar(readDist("index.html"));
  const groups = [...bar.matchAll(/<span class="nav__group">[\s\S]*?<\/span>/g)].map((m) => m[0]);
  assert.ok(groups.length >= 2, `expected disclosure groups, found ${groups.length}`);
  for (const g of groups) {
    assert.match(g, /<a class="nav__link"[^>]*href="\/[a-z-]+\.html"/, "the group heading is a real link");
  }
});

test("App Store CTA slot is RESERVED (a comment), not a fabricated live link/badge", () => {
  const bar = topbar(readDist("index.html"));
  assert.match(bar, /RESERVED: App Store badge SLOT/, "the reserved-slot comment marks the position");
  // no live App Store anchor yet (no listing exists) - the slot lives only inside a comment.
  const links = liveHrefs(bar);
  assert.ok(!links.some((h) => /app.?store|apps\.apple\.com|itunes\.apple\.com/i.test(h)),
    "no App Store link is fabricated");
  assert.doesNotMatch(bar.replace(/<!--[\s\S]*?-->/g, ""), /nav__appstore/,
    "no live nav__appstore element outside the comment");
});

test("lean nav (account pages): brand + Models/Voices/Manual/Log in + toggle, no marketing extras", () => {
  const bar = topbar(readDist("dashboard.html"));
  const links = liveHrefs(bar);
  assert.deepEqual(links, [
    "/",                                      // brand -> home
    "/models.html", "/voices.html", "/manual.html", "/login.html",
  ], "the lean variant stays minimal - no App/Source/anchors/API-keys in the bar");
});

test("footer keeps the FULL map: everything pulled from the bar is still reachable there", () => {
  const links = liveHrefs(readDist("index.html").match(/<footer[\s\S]*?<\/nav>/)[0]);
  for (const must of [
    "/app.html",        // the new destination
    "#spec", "#monetize",  // the anchors removed from the bar (same-page on the homepage)
    "/keys.html",       // API keys, removed from the bar -> Account group
  ]) {
    assert.ok(links.includes(must), `footer carries ${must} so the decluttered bar loses nothing`);
  }
});

// The panels need JavaScript. The footer does not, and neither does a crawler, so
// every destination a panel reveals must also be in the footer map.
test("the footer carries every destination the nav panels reveal", () => {
  const home = readDist("index.html");
  const bar = topbar(home);
  const panelHrefs = [...bar.matchAll(/<div class="nav__panel"[\s\S]*?<\/div>/g)]
    .flatMap((m) => liveHrefs(m[0]));
  const footer = liveHrefs(home.match(/<footer[\s\S]*?<\/nav>/)[0]);
  for (const h of new Set(panelHrefs)) {
    assert.ok(footer.includes(h), `${h} is in the footer too, so it survives with JS off`);
  }
});

test("the App link resolves: /app.html is the live App Store launch page", () => {
  assert.ok(existsSync(path.join(DIST, "app.html")), "app.html builds to dist (the link 200s)");
  const app = readDist("app.html");
  // the operative phrase may carry the red-underline span (the index H1 motif)
  assert.match(app, /the band, (?:<span[^>]*>)?in&nbsp;your&nbsp;pocket/i, "the launch page keeps the pocket-band headline");
  // the app SHIPPED 2026-07-09: the page is indexed now (in the sitemap), no placeholder leftovers
  assert.doesNotMatch(app, /name="robots" content="noindex"/, "launch page is indexed");
  assert.doesNotMatch(app, /tuning up/i, "no 'tuning up' placeholder copy survives");
  assert.match(app, /apps\.apple\.com\/us\/app\/rogerai-fyi\/id6785743752/, "links the real listing");
  assert.doesNotMatch(app, /<!--\s*include:|<!--\s*css-bundle\s*-->/, "all partial/css includes resolved");
});

test("homepage anchors survive: the sections the bar used to jump to still exist", () => {
  const home = readDist("index.html");
  // "how" (§6 Operating Notes) was retired on founder direction; its footer link
  // went with it, so there is no dangling anchor left to keep alive.
  for (const id of ["spec", "monetize"]) {
    assert.match(home, new RegExp(`id="${id}"`), `#${id} section still on the homepage (reachable by footer/scroll)`);
  }
  assert.doesNotMatch(home, /href="[^"]*#how"/, "no footer link survives the retired section");
});

test("source partial: no stray {{APP_STORE_URL}} marker (would ship literally)", () => {
  // build.mjs resolves unknown {{name}} to "" - the reserved-slot example URL must be a
  // plain token, not a {{...}} marker that would silently blank inside the shipped comment.
  assert.doesNotMatch(readSrc("_partials/nav.html"), /\{\{\s*APP_STORE_URL\s*\}\}/);
});

test("each first-class destination marks exactly one matching current nav link", () => {
  const destinations = new Map([
    ["models.html", "/models.html"],
    ["research.html", "/research.html"],
    ["voices.html", "/voices.html"],
    ["app.html", "/app.html"],
    ["company.html", "/company.html"],
    ["manual.html", "/manual.html"],
  ]);
  for (const [page, href] of destinations) {
    const html = readFileSync(path.join(DIST, page), "utf8");
    const current = [...html.matchAll(/<a[^>]+href="([^"]+)"[^>]+aria-current="page"[^>]*>/g)];
    assert.equal(current.length, 1, `${page} has exactly one current nav link`);
    assert.equal(current[0][1], href, `${page} marks its matching destination`);
  }
  const home = readFileSync(path.join(DIST, "index.html"), "utf8");
  assert.doesNotMatch(home, /aria-current="page"/);
});

test("current navigation uses a stable red underline with reduced-motion safety", () => {
  const css = readFileSync(path.join(SRC, "styles", "base.css"), "utf8");
  assert.match(css, /\.nav__link\[aria-current="page"\]/);
  assert.match(css, /aria-current="page"[\s\S]*?color:\s*var\(--ink-900\)/);
  const currentRule = css.match(/\.nav__link\[aria-current="page"\]::after\s*\{([^}]*)\}/)?.[1] || "";
  assert.match(currentRule, /height:\s*2px/);
  assert.match(currentRule, /background:\s*var\(--live\)/);
  assert.match(currentRule, /transform:\s*scaleX\(1\)/);
  const reducedMotion = css.match(/@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{([\s\S]*?)\n\}/g) || [];
  assert.ok(reducedMotion.some((block) =>
    /\.nav__link:not\(\.nav__link--ghost\)::after\s*\{\s*transition:\s*none;\s*\}/.test(block)
  ), "reduced motion disables the nav underline transition");
});
