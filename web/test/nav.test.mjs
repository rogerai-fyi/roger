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
import { readFileSync, existsSync, readdirSync } from "node:fs";
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
/* AMENDED 2026-08-17: these used a non-greedy /<div class="nav__panel">...<\/div>/,
   which stops at the FIRST closing tag - so the moment a panel contained a nested
   div (the Playbox deck disclosure) it read half a panel, and the links below the
   nesting silently vanished from both the order lock and the reachability lock.
   Now the div nesting is walked properly, so a panel is a whole panel. Same
   guarantees, correctly measured. */
function panelBlocks(block) {
  var out = [], i = 0;
  for (;;) {
    var start = block.indexOf('<div class="nav__panel"', i);
    if (start < 0) break;
    var depth = 0, j = start;
    for (;;) {
      var open = block.indexOf("<div", j), close = block.indexOf("</div>", j);
      if (close < 0) { j = block.length; break; }
      if (open >= 0 && open < close) { depth++; j = open + 4; continue; }
      depth--; j = close + 6;
      if (depth === 0) break;
    }
    out.push(block.slice(start, j));
    i = j;
  }
  return out;
}
const withoutPanels = (block) =>
  panelBlocks(block).reduce(function (acc, p) { return acc.replace(p, ""); }, block);

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

test("marketing top bar: Models · Research · Company and NOTHING else in the sections group", () => {
  const bar = topbar(readDist("index.html"));
  const sections = sectionsBlock(bar);
  const hrefs = barHrefs(sections);
  // Voices moved INTO the Models panel: it is a directory of the same live network, so
  // it belongs under Models rather than competing with it for a top-level slot. App left
  // the sections group for its own divider-flanked section beside Manual.
  assert.deepEqual(hrefs, ["/models.html", "/research.html", "/company.html"],
    "sections are exactly Models, Research, Company - each one a disclosure group");
});

// App is a destination of a different kind - a store listing, not a section of the site -
// so it sits in its own band between two dividers, left of the Manual utility cluster.
test("App stands alone between two dividers, left of Manual", () => {
  const bar = withoutPanels(topbar(readDist("index.html")));
  const order = [...bar.matchAll(/<span class="nav__divider"|<a[^>]*href="(\/app\.html|\/manual\.html)"/g)]
    .map((m) => m[1] || "divider");
  assert.deepEqual(order, ["divider", "/app.html", "divider", "/manual.html"],
    "the bar reads: sections | App | Manual...");
  // In the burger drawer the dividers are display:none, so App must still be a real row.
  const appLink = bar.match(/<a class="nav__link"[^>]*href="\/app\.html"[^>]*>/)?.[0];
  assert.ok(appLink, "App is a plain nav__link, so it inherits the drawer row styling");
});

test("marketing top bar: the removed items are NOT live links anywhere in the bar", () => {
  const links = liveHrefs(topbar(readDist("index.html")));
  for (const gone of ["#spec", "#how", "#monetize", "/keys.html"]) {
    assert.ok(!links.some((h) => h.includes(gone)), `${gone} is not a live top-bar link`);
  }
});

test("marketing top bar: order is Models·Research·Company | App | Manual·Source·Log in (then the reserved slot + toggle)", () => {
  const links = liveHrefs(withoutPanels(topbar(readDist("index.html"))));
  assert.deepEqual(links, [
    "#top",                                   // brand
    "/models.html", "/research.html", "/company.html",
    "/app.html",
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
  const panelHrefs = panelBlocks(bar).flatMap((p) => liveHrefs(p));
  for (const must of [
    "/research-models.html",
    "/research-wave-family.html",
    "/research-industry.html",
    "/broadcasts.html",
    "/careers.html",
    "/voices.html",
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
  /* uses the nesting-aware reader for the same reason the other two do: the
     non-greedy form stopped at the first </div> and silently skipped every
     link below a nested block */
  const panelHrefs = panelBlocks(bar).flatMap((p) => liveHrefs(p));
  const footer = liveHrefs(home.match(/<footer[\s\S]*?<\/nav>/)[0]);
  for (const h of new Set(panelHrefs)) {
    // A panel entry may be a DEEP LINK into a page the footer already carries
    // (the Playbox lists its two decks as /playbox.html#console and #mesh). The
    // rule this test protects is "no destination is reachable only through a
    // JS panel" - and a hash into an already-mapped page is not a destination
    // the footer is missing, it is a shortcut into one it has.
    const base = h.split("#")[0];
    if (base && base !== h && footer.includes(base)) continue;
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

// ---- the disclosure panels inside the burger drawer -------------------------
//
// Two bugs the rendered page found that the markup tests could not.
//
// 1. The panel un-floats at 760px but the burger appears at 960px, so between them
//    the drawer is a column while its panel is still position:absolute - it detached
//    from "Research" and covered the Source button and the hero.
// 2. The drawer is position:absolute inside .wrap.nav__inner, and absolute offsets
//    resolve against the PADDING box, so left:0/right:0 cancels the page gutter: every
//    link and both utility buttons sat flush against the viewport edge.
//
// Both are breakpoint/box arithmetic, so they are asserted against the CSS source.
const baseCss = () => readFileSync(path.join(SRC, "styles", "base.css"), "utf8");

// The max-width breakpoint of the innermost media query a declaration sits in.
const breakpointOf = (css, needle) => {
  const i = css.search(needle);
  assert.ok(i > -1, `CSS contains ${needle}`);
  const opened = [...css.slice(0, i).matchAll(/@media\s*\(max-width:\s*(\d+)px\)/g)];
  assert.ok(opened.length, `${needle} sits inside a max-width media query`);
  return Number(opened[opened.length - 1][1]);
};

test("the nav panels stop floating at the SAME width the burger takes over", () => {
  const css = baseCss();
  const burger = breakpointOf(css, /\.nav__burger\s*\{\s*display:\s*grid/);
  const panel = breakpointOf(css, /\.nav__panel\s*\{[^}]*position:\s*static/);
  assert.equal(panel, burger,
    `panel un-floats at ${panel}px but the drawer starts at ${burger}px - in between, ` +
    "an absolutely positioned panel covers the page instead of nesting under its link");
});

test("the burger drawer keeps the page gutter", () => {
  const css = baseCss();
  const bp = breakpointOf(css, /\.nav__burger\s*\{\s*display:\s*grid/);
  const drawer = css.match(
    new RegExp(`@media\\s*\\(max-width:\\s*${bp}px\\)[\\s\\S]*?\\.nav__menu\\s*\\{([^}]*)\\}`)
  )?.[1];
  assert.ok(drawer, `the ${bp}px block restyles .nav__menu`);
  const parts = (drawer.match(/padding:\s*([^;]+);/)?.[1] || "").trim().split(/\s+/);
  assert.ok(parts.length >= 2, `.nav__menu sets a padding shorthand, got "${parts.join(" ")}"`);
  assert.match(parts[1], /--gutter/,
    "the drawer is absolutely positioned against .nav__inner's PADDING box, so it must " +
    "re-apply the gutter itself or the links sit flush against the viewport edge");
});

test("every drawer row draws one full-width divider, group or not", () => {
  // .nav__link is inline-flex, so on a row that carries a disclosure the border-bottom
  // spanned only the label and stopped - "Research" and "Company" got a stub rule while
  // Models/Voices/App got a full-width one. The divider belongs to the ROW (the group),
  // which also puts it below the expanded panel, where the nesting reads correctly.
  const css = baseCss();
  const bp = breakpointOf(css, /\.nav__burger\s*\{\s*display:\s*grid/);
  const block = css.slice(css.indexOf(`@media (max-width: ${bp}px)`));
  assert.match(block, /\.nav__sections\s+\.nav__group\s*\{[^}]*border-bottom:\s*1px solid var\(--hairline\)/,
    "the group carries the row divider");
  assert.match(block, /\.nav__group\s+\.nav__link\s*\{[^}]*border-bottom:\s*(?:0|none)/,
    "the link inside a group does not also draw a stub");
});

// Voices left the bar for the Models panel. Two things must survive that move: the page
// still marks exactly one current link (the existing destinations test covers that), and
// the collapsed parent still SHOWS it is the active area - otherwise a visitor on
// /voices.html sees no highlight anywhere in the bar. The parent cannot claim
// aria-current="page" (it is not the page you are on), so the cue is presentational.
test("a group whose panel holds the current page shows as active in the bar", () => {
  const voices = readDist("voices.html");
  const panelLink = voices.match(/<a href="\/voices\.html"[^>]*aria-current="page"[^>]*>/);
  assert.ok(panelLink, "the panel entry carries the current marker, not the parent link");
  const models = voices.match(/<a class="nav__link" href="\/models\.html"[^>]*>/)[0];
  assert.doesNotMatch(models, /aria-current/,
    "the parent must not claim to be the page you are on");
  const css = readFileSync(path.join(SRC, "styles", "base.css"), "utf8");
  assert.match(css, /\.nav__group:has\([^)]*aria-current="page"[^)]*\)/,
    "the active cue for a group with a current child is carried in CSS, not the a11y tree");
});

// Broadcasts is both the lab notebook and the company's public writing, so it earns a
// place in both panels rather than being filed under one and hidden from the other.
// Panels are an accelerator, not a taxonomy - the same destination may appear twice.
test("Broadcasts is reachable from Company as well as Research", () => {
  const bar = topbar(readDist("index.html"));
  const panel = (id) => bar.match(new RegExp(`<div class="nav__panel" id="${id}"[\\s\\S]*?</div>\\s*</span>`))?.[0] || "";
  for (const id of ["navResearchPanel", "navCompanyPanel"]) {
    assert.match(panel(id), /href="\/broadcasts\.html"/, `${id} offers the broadcasts`);
  }
  // Each panel entry still needs its own label and one-line description.
  const company = panel("navCompanyPanel");
  const entry = company.match(/<a href="\/broadcasts\.html"[^>]*>([\s\S]*?)<\/a>/)?.[1] || "";
  assert.match(entry, /<b>[^<]+<\/b>/, "the entry is named");
  assert.match(entry, /<span>[^<]+<\/span>/, "and says what it is");
});

// A disclosure panel is an accelerator, never the only route. That means the page a group
// points at must independently offer everything in that group's panel - otherwise a
// reader who lands on /models.html from a search result can only reach Voices and the
// Tower by discovering a collapsed menu. Derived from the built nav, so adding a panel
// entry without a route on the page fails here rather than shipping.
test("each group's page offers every destination in that group's panel", () => {
  const bar = topbar(readDist("index.html"));
  const groups = [...bar.matchAll(
    /<a class="nav__link" href="([^"]+)"[^>]*>([^<]+)<\/a>[\s\S]*?<div class="nav__panel"[^>]*>([\s\S]*?)<\/div>/g)];
  assert.ok(groups.length >= 3, `found ${groups.length} disclosure groups`);
  for (const [, parent, name, panel] of groups) {
    const dests = [...panel.matchAll(/href="([^"]+)"/g)].map((m) => m[1]);
    const html = readDist(parent.replace(/^\//, ""));
    // Not merely "somewhere on the page": the destinations have to be BUTTONS near the
    // top, the way the research hero already offered its actions. Buried in prose or in an
    // onward row a screen down, a reader does not find them - which is exactly what
    // happened, and is why this asserts the hero action cluster specifically.
    const hero = html.match(/<div class="research-actions"[^>]*>[\s\S]*?<\/div>/)?.[0];
    assert.ok(hero, `${parent} carries a hero action row`);
    for (const dest of dests) {
      if (dest === parent) continue; // the parent is where we already are
      // A panel entry may be a DEEP LINK into a page the hero already offers -
      // the Playbox lists its two decks as /playbox.html#console and #mesh. The
      // reader this test protects (landed here from a search result, menu
      // undiscovered) can still reach the page; the hash picks a deck once
      // there. Requiring a separate hero button per deck would put three
      // Playbox buttons in one row to no benefit.
      const base = dest.split("#")[0];
      if (base && base !== dest && (base === parent || hero.includes(`href="${base}"`))) continue;
      assert.ok(hero.includes(`href="${dest}"`),
        `${parent} must offer ${dest} as a button in its hero, not only in the ${name.trim()} panel`);
    }
  }
});

// The three live-network pages are siblings, so each offers the other two. Landing on any
// one of them should not be a dead end for the other two.
test("the live-network pages cross-link to each other", () => {
  const FAMILY = ["/models.html", "/voices.html", "/tower.html"];
  for (const page of FAMILY) {
    const body = readDist(page.replace(/^\//, "")).match(/<main[\s\S]*?<\/main>/)[0];
    for (const sibling of FAMILY) {
      if (sibling === page) continue;
      assert.ok(body.includes(`href="${sibling}"`),
        `${page} offers ${sibling} from its own body`);
    }
  }
});

// The action row is on eight pages now, so it has to be styled on all eight. It lived in
// research.css, which models.html does not load - the row rendered as four unstyled links
// there, which no markup assertion would have caught.
test("the section action buttons are styled on every page that uses them", () => {
  const src = (p) => readFileSync(path.join(SRC, p), "utf8");
  const pages = readdirSync(SRC).filter((f) => f.endsWith(".html") && src(f).includes("research-actions"));
  assert.ok(pages.length >= 5, `the row is shared, found it on ${pages.length} pages`);
  // Whatever sheet defines it must be one every one of those pages actually loads.
  const base = src("styles/base.css");
  assert.match(base, /\.research-button \{/, "the component is in the always-loaded sheet");
  for (const p of pages) {
    const built = readDist(p);
    assert.match(built, /styles\/base\.css|<style/, `${p} loads the shared chrome`);
  }
});

// The site uses two nouns for two different things and they are not interchangeable:
// a MODEL is what you tune to, a GPU is the machine someone owns and shares to serve it.
// The manual's glossary is the canonical definition, and "GPU model" reads as the
// hardware's model number, which is a third meaning entirely - it does not belong in
// structured data a search result renders.
test("model and GPU keep their separate meanings", () => {
  const gloss = readDist("manual.html").match(/<dl class="man-gloss">[\s\S]*?<\/dl>/)[0];
  const entry = (term) => gloss.match(new RegExp(`<dt>${term}</dt><dd>([\\s\\S]*?)</dd>`))?.[1] || "";
  assert.match(entry("band"), /a model/i, "a band IS a model");
  assert.match(entry("station"), /GPU/i, "a station is the machine serving one");
  // The ambiguous compound must not appear where it means "a model on a GPU".
  for (const p of ["index.html", "models.html", "voices.html", "tower.html"]) {
    const html = readDist(p);
    const ld = html.match(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g) || [];
    for (const block of ld) {
      assert.doesNotMatch(block, /local GPU model/i,
        `${p}: "GPU model" reads as the hardware's model number in structured data`);
    }
  }
});

// The cycling App word: one nav destination worn three ways (App / TUI / WebUI), the word
// wearing the carrier gradient and swapping on the beat of it (site.js). This locks the
// PROGRESSIVE-ENHANCEMENT contract and the three anchors, so a refactor cannot quietly turn
// it into a JS-only link or point a surface at a section that does not exist.
test("the App item degrades to a real /app.html link and cycles App/TUI/WebUI", () => {
  const bar = topbar(readDist("index.html"));

  // Base markup (what ships, what runs with JS off): a plain nav__link to /app.html whose
  // visible word is "App", carrying the cycle hook and the carrier gradient on the word.
  const app = bar.match(/<a class="nav__link"[^>]*data-app-cycle[^>]*>[\s\S]*?<\/a>/);
  assert.ok(app, "the App link ships the data-app-cycle hook");
  const a = app[0];
  assert.match(a, /href="\/app\.html"/, "base href is /app.html (the no-JS fallback)");
  assert.match(a, /<span class="carrier nav__app-word" data-app-word>App<\/span>/,
    "the visible word is a carrier-gradient span defaulting to App");
  assert.match(a, /aria-label="[^"]*TUI[^"]*WebUI[^"]*"/,
    "a STABLE aria-label is the accessible name, so the moving word is not announced");

  // The three surfaces and their anchors live in site.js, and each anchor must resolve.
  const site = readSrc("js/site.js");
  const stops = site.match(/STOPS\s*=\s*\[([\s\S]*?)\]/);
  assert.ok(stops, "site.js declares the App/TUI/WebUI stops");
  for (const [w, h] of [["App", "/app.html"], ["TUI", "/app.html#cli"], ["WebUI", "/app.html#webui"]]) {
    assert.ok(stops[1].includes(`"${w}"`) && stops[1].includes(`"${h}"`),
      `the ${w} stop points at ${h}`);
  }

  // The deep-link anchors must actually exist on app.html, or a click scrolls nowhere.
  const appPage = readDist("app.html");
  assert.match(appPage, /id="cli"/, "app.html has the TUI section (#cli)");
  assert.match(appPage, /id="webui"/, "app.html has the WebUI section (#webui)");
});
