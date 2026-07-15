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

test("marketing top bar: Models · Voices · App and NOTHING else in the sections group", () => {
  const bar = topbar(readDist("index.html"));
  const sections = bar.match(/<div class="nav__sections">[\s\S]*?<\/div>/)[0];
  const hrefs = liveHrefs(sections);
  assert.deepEqual(hrefs, ["/models.html", "/voices.html", "/app.html"],
    "sections are exactly Models, Voices, App - the #spec/#how/#monetize anchors are gone");
});

test("marketing top bar: the removed items are NOT live links anywhere in the bar", () => {
  const links = liveHrefs(topbar(readDist("index.html")));
  for (const gone of ["#spec", "#how", "#monetize", "/keys.html"]) {
    assert.ok(!links.some((h) => h.includes(gone)), `${gone} is not a live top-bar link`);
  }
});

test("marketing top bar: order is Models·Voices·App | Manual·Source·Log in (then the reserved slot + toggle)", () => {
  const links = liveHrefs(topbar(readDist("index.html")));
  assert.deepEqual(links, [
    "#top",                                   // brand
    "/models.html", "/voices.html", "/app.html",
    "/manual.html",
    "https://github.com/rogerai-fyi/roger",   // Source (ghost)
    "/login.html",
  ], "the decluttered marketing bar renders the exact target link set, in order");
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
    "#spec", "#how", "#monetize",  // the anchors removed from the bar (same-page on the homepage)
    "/keys.html",       // API keys, removed from the bar -> Account group
  ]) {
    assert.ok(links.includes(must), `footer carries ${must} so the decluttered bar loses nothing`);
  }
});

test("the App link resolves: /app.html is the live App Store launch page", () => {
  assert.ok(existsSync(path.join(DIST, "app.html")), "app.html builds to dist (the link 200s)");
  const app = readDist("app.html");
  assert.match(app, /the band, in&nbsp;your&nbsp;pocket/i, "the launch page keeps the pocket-band headline");
  // the app SHIPPED 2026-07-09: the page is indexed now (in the sitemap), no placeholder leftovers
  assert.doesNotMatch(app, /name="robots" content="noindex"/, "launch page is indexed");
  assert.doesNotMatch(app, /tuning up/i, "no 'tuning up' placeholder copy survives");
  assert.match(app, /apps\.apple\.com\/us\/app\/rogerai-fyi\/id6785743752/, "links the real listing");
  assert.doesNotMatch(app, /<!--\s*include:|<!--\s*css-bundle\s*-->/, "all partial/css includes resolved");
});

test("homepage anchors survive: the sections the bar used to jump to still exist", () => {
  const home = readDist("index.html");
  for (const id of ["spec", "how", "monetize"]) {
    assert.match(home, new RegExp(`id="${id}"`), `#${id} section still on the homepage (reachable by footer/scroll)`);
  }
});

test("source partial: no stray {{APP_STORE_URL}} marker (would ship literally)", () => {
  // build.mjs resolves unknown {{name}} to "" - the reserved-slot example URL must be a
  // plain token, not a {{...}} marker that would silently blank inside the shipped comment.
  assert.doesNotMatch(readSrc("_partials/nav.html"), /\{\{\s*APP_STORE_URL\s*\}\}/);
});
