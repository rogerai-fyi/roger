// One role, one pattern (web/DESIGN-SYSTEM.md). The DRY guards catch a component defined
// twice; these catch a role drawn two ways across pages - parallel rollouts made different
// choices (the founder spotted /models' lead row next to /company's row of boxed buttons,
// and ruled for the boxes: every row is one solid primary, then outlined boxed buttons).
// Each rule reads the page sources; a page that needs to differ is listed with its reason.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");
const strip = (s) => s.replace(/<!--(?!\s*include)[\s\S]*?-->/g, "");
const STUBS = new Set(["bands.html", "playground.html", "why.html"]);
const PAGES = readdirSync(SRC).filter((f) => f.endsWith(".html") && !STUBS.has(f)).sort();
const classesOf = (tag) => (tag.match(/\bclass="([^"]*)"/)?.[1] || "").split(/\s+/).filter(Boolean);

/* ---- action rows ------------------------------------------------------------------ */

// Every action row is the lead row: ONE primary (the first action), then boxed secondaries
// (the plain .research-button outline). There is no quiet, underlined-link variant.
function actionRows(html) {
  return [...html.matchAll(/<(div|p)\b[^>]*\bclass="[^"]*\bresearch-actions\b[^"]*"[^>]*>([\s\S]*?)<\/\1>/g)]
    .map((m) => ({ open: m[0].slice(0, m[0].indexOf(">") + 1), body: m[2] }));
}
test("every action row is a lead row: the first action primary, the rest boxed secondaries", () => {
  const bad = [];
  for (const page of PAGES) {
    for (const row of actionRows(strip(read(page)))) {
      const where = `${page} ${row.open.slice(0, 70)}`;
      if (!classesOf(row.open).includes("research-actions--lead")) { bad.push(`${where}: not a lead row`); continue; }
      const buttons = [...row.body.matchAll(/<(a|button)\b[^>]*>/g)].map((m) => classesOf(m[0]));
      if (!buttons.length) { bad.push(`${where}: empty`); continue; }
      buttons.forEach((c, i) => {
        if (!c.includes("research-button")) bad.push(`${where}: action ${i + 1} is not a .research-button`);
        else if (i === 0 && !c.includes("research-button--primary")) bad.push(`${where}: the first action is not the primary`);
        else if (i > 0 && c.some((x) => x.startsWith("research-button--") && x !== "research-button--primary")) bad.push(`${where}: action ${i + 1} carries a variant (${c.join(" ")}); a secondary is the plain boxed .research-button`);
        if (i > 0 && c.includes("research-button--primary")) bad.push(`${where}: a second primary`);
      });
    }
  }
  assert.deepEqual(bad, [], `action rows off the pattern:\n  ${bad.join("\n  ")}`);
});

// The boxed secondary is drawn once, in components.css: an outline that reads on paper and
// in an ink panel (theme tokens), a visible hover, focus and press, a 44px tap target on
// touch, and on a phone every button keeps its own width (no full-width stack).
const sheet = (f) => readFileSync(path.join(SRC, "styles", f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
test("the action row's buttons: one solid primary, boxed secondaries, never a quiet link or a full-width stack", () => {
  const all = readdirSync(path.join(SRC, "styles")).filter((f) => f.endsWith(".css"));
  for (const f of all) assert.doesNotMatch(sheet(f), /research-button--quiet/, `${f} still draws the quiet link`);
  const c = sheet("components.css");
  assert.match(c, /\.research-button \{[^}]*border: 1px solid var\(--hairline-2\)[^}]*color: var\(--ink-700\)/, "the secondary is a hairline box with an AA ink label");
  assert.match(c, /\.research-button:focus-visible \{[^}]*outline: 2px solid var\(--live\)/, "keyboard focus shows");
  assert.match(c, /\.research-button:not\(\.research-button--primary\):active \{[^}]*background: var\(--paper-2\)/, "a pressed secondary shows");
  assert.match(c, /@media \(pointer: coarse\) \{[^@]*\.research-button \{[^}]*min-height: 44px/, "a 44px tap target on touch");
  assert.match(c, /\.research-actions--lead \.research-button \{[^}]*width: auto[^}]*flex: 0 0 auto/, "each button keeps its own width");
  assert.doesNotMatch(sheet("company.css"), /\.research-button \{[^}]*flex: 1/, "no page stretches its buttons to fill a row");
  // no phone block stacks an action row full width, however the rule is spelled
  const phoneBlocks = [...sheet("research.css").matchAll(/@media \(max-width: \d+px\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n");
  assert.doesNotMatch(phoneBlocks, /\.research-actions[^{]*\{[^}]*grid-template-columns:\s*1fr/, "the research hero does not stack full width on a phone");
  // the hub hero's row is the plain lead row (a 2x2 grid in the copy column wrapped 5 boxes to 3 rows at 1440)
  assert.doesNotMatch(sheet("research.css"), /\.research-hero__layout \.research-actions \{[^}]*display: grid/, "the hub hero row is not a grid");
  assert.doesNotMatch(c, /\.research-button[^{}]*\{[^}]*box-shadow/, "no glow on the buttons");
});

// A .research-button lives in an action row; a page does not draw its own row of actions.
const OTHER_ACTION_ROWS = {
  "bx-help__actions": "the billing help dialog's form buttons",
  "reportform__actions": "the report-a-station form's submit / cancel",
  "model-row__actions": "per-model links inside the models table",
};
test("buttons live only in action rows, and no page invents its own action row", () => {
  const bad = [];
  for (const page of PAGES) {
    const html = strip(read(page));
    const inRows = actionRows(html).reduce((n, r) => n + (r.body.match(/class="[^"]*\bresearch-button\b/g) || []).length, 0);
    const all = (html.match(/class="[^"]*\bresearch-button\b/g) || []).length;
    if (all !== inRows) bad.push(`${page}: ${all - inRows} .research-button outside an action row`);
    for (const [, c] of html.matchAll(/class="([^"]*(?:__actions|-actions)\b[^"]*)"/g)) {
      const own = c.split(/\s+/).find((x) => /(__|-)actions$/.test(x));
      if (own !== "research-actions" && !OTHER_ACTION_ROWS[own]) bad.push(`${page}: .${own} is its own action row`);
    }
  }
  assert.deepEqual(bad, [], bad.join("\n"));
});

/* ---- heroes ----------------------------------------------------------------------- */

// A page hero is paper. The ink panel marks a page's moment (its instrument, its live
// data), and the homepage's cover is the site's one ink hero.
const HERO = /<(section|header|div)\b[^>]*\bclass="(research-hero|bands-hero|app-hero|bc-hero|hero|bc-post__head|man-cover)\b/;
test("a page hero is paper; only the homepage cover is an ink panel", () => {
  const bad = [];
  for (const page of PAGES) {
    const html = strip(read(page));
    const m = html.match(HERO);
    if (!m) continue;
    const before = html.slice(0, m.index);
    const open = (before.match(/class="tone-zone"/g) || []).length;
    const closed = (before.match(/<!--\s*\/tone-zone\s*-->|<\/div><!-- \/tone-zone -->/g) || []).length;
    const inInk = open > closed;
    if (inInk !== (page === "index.html")) bad.push(`${page}: the hero is ${inInk ? "an ink panel" : "paper"}`);
  }
  assert.deepEqual(bad, [], bad.join("\n"));
});

/* ---- the tuner -------------------------------------------------------------------- */

// A page with four or more numbered sections carries the contents tuner, and it sits in one
// place: right after the hero (and the hero's strip, when the page has one).
test("the tuner sits right after the hero, on every page long enough to need one", () => {
  const NO_TUNER = {
    "models.html": "the directory itself is the page; its sections are the dial and the table",
    "voices.html": "the roster itself is the page",
    "manual.html": "the manual's own table of contents is its cover (the tuner follows it)",
  };
  const bad = [];
  for (const page of PAGES) {
    const html = strip(read(page));
    const sections = (html.match(/class="sectionno"/g) || []).length;
    const tuner = html.lastIndexOf("<", html.indexOf('class="toc-tuner"'));
    if (html.indexOf('class="toc-tuner"') < 0) {
      if (sections >= 4 && !NO_TUNER[page]) bad.push(`${page}: ${sections} numbered sections and no tuner`);
      continue;
    }
    const hero = html.match(HERO);
    if (!hero) continue;
    // between the end of the hero block and the tuner: only closing tags, the ink panel's
    // close, comments, and the hero's own strip (the homepage doors, the distinction strip)
    // or an article's hero figure
    const after = html.slice(hero.index, tuner);
    const heroEnd = after.search(/<\/(section|header)>/);
    const gap = after.slice(heroEnd)
      .replace(/<style>[\s\S]*?<\/style>/g, "")
      .replace(/<figure\b[^>]*class="figure[^"]*"[\s\S]*?<\/figure>/g, "")
      .replace(/<\/(section|header|div)>|<!--[\s\S]*?-->|\s+/g, "");
    const gapOk = gap === "" || /^<(aside|section|div)[^>]*class="[^"]*(institution-strip|research-distinction)/.test(gap);
    if (!gapOk) bad.push(`${page}: something sits between the hero and the tuner: ${gap.slice(0, 80)}`);
  }
  assert.deepEqual(bad, [], bad.join("\n"));
});

/* ---- section heads ---------------------------------------------------------------- */

// Every numbered section opens the same way: the .sectionno label, then the section's h2
// (then its lede), whatever block holds them.
test("every numbered section label heads an h2", () => {
  const bad = [];
  for (const page of PAGES) {
    const html = strip(read(page));
    for (const m of html.matchAll(/<span class="sectionno">[\s\S]{0,200}?<(h2|h3|p)\b/g)) {
      if (m[1] !== "h2") bad.push(`${page}: a .sectionno heads a <${m[1]}>, not an h2`);
    }
  }
  assert.deepEqual(bad, [], bad.join("\n"));
});

/* ---- commands --------------------------------------------------------------------- */

// A command a reader will copy is one of the sanctioned components, by context: the
// install-box partial (the install line), .code-block (a block of commands), .copy-code
// (an inline command in prose). A bare <pre> is not copyable and not themed.
const BARE_PRE = {
  "integrations.html": "the config diff (.intg-diff): a before/after of one line, read, not run",
  "index.html": "the terminal demo's replay screen (#termScreen), not a command",
  "playbox.html": "the deck's printout (#dkPrintOut), generated by the game",
  "keys.html": "the signed-in plate's own command well (account-base .cmd)",
  "stations.html": "the signed-in plate's own command well (account-base .cmd)",
  "dashboard.html": "the signed-in plate's own command well (account-base .cmd)",
  "console.html": "the signed-in plate's own command well (account-base .cmd)",
};
test("every block of commands is a .code-block (the install line the install-box partial)", () => {
  const bad = [];
  for (const page of PAGES) {
    const html = strip(read(page));
    const bare = [...html.matchAll(/<pre\b[^>]*>/g)].filter((m) => {
      const before = html.slice(Math.max(0, m.index - 120), m.index);
      return !/class="code-block"[^<]*>\s*$/.test(before);
    }).length;
    if (bare && !BARE_PRE[page]) bad.push(`${page}: ${bare} bare <pre> block(s)`);
  }
  assert.deepEqual(bad, [], bad.join("\n"));
});

/* ---- hero titles ------------------------------------------------------------------ */

// Two kinds of page, two title sizes: a landing page (a section's front door) sets its h1
// at the display size; a document (an article, the manual, a legal page, the 404, the
// confidential tier) at h1 size. The homepage cover sizes its own to its box, capped at
// the display size.
test("hero titles: landing pages at the display size, documents at h1", () => {
  const sheet = (f) => readFileSync(path.join(SRC, "styles", f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const size = (f, sel) => {
    const m = [...sheet(f).matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((r) => r[1].trim() === sel);
    assert.ok(m, `${f} has a "${sel}" rule`);
    return m[2].match(/font-size:\s*([^;]+)/)?.[1].trim();
  };
  for (const [f, sel] of [["research.css", ".research-hero h1"], ["components.css", ".bands-hero__title"], ["broadcasts.css", ".bc-hero__title"]]) {
    assert.equal(size(f, sel), "var(--t-display)", `landing: ${sel}`);
  }
  for (const [f, sel] of [["broadcasts.css", ".bc-post__title"], ["manual.css", ".man-cover h1"], ["notfound.css", ".lost__title"], ["confidential.css", ".conf__head h1"], ["company.css", ".legal__head h1"]]) {
    assert.equal(size(f, sel), "var(--t-h1)", `document: ${sel}`);
  }
  // and every landing page's h1 is one of the landing heroes
  for (const page of ["app.html", "broadcasts.html", "careers.html", "company.html", "faq.html", "integrations.html", "models.html", "pricing.html", "research.html", "research-hardware.html", "research-industry.html", "research-models.html", "research-wave-family.html", "tower.html", "voices.html"]) {
    const html = read(page);
    const h1 = html.match(/<h1\b[^>]*>/)?.[0] || "";
    const inLanding = /class="bands-hero__title|class="bc-hero__title/.test(h1)
      || /<section class="research-hero"[\s\S]*?<h1\b/.test(html.slice(0, html.indexOf(h1) + h1.length));
    assert.ok(inLanding, `${page}: its h1 is a landing hero title`);
  }
});

/* ---- tables ----------------------------------------------------------------------- */

// Every table of data is the shared .data-table: one header row, one rule weight, no box.
const ACCOUNT = new Set(["account.html", "billing.html", "console.html", "dashboard.html", "device.html",
  "keys.html", "payouts.html", "private.html", "r.html", "stations.html", "usage.html", "login.html"]);
test("every table of data on a public page is a .data-table", () => {
  const bad = [];
  for (const page of PAGES.filter((p) => !ACCOUNT.has(p) && p !== "playbox.html")) {
    for (const m of strip(read(page)).matchAll(/<table\b[^>]*>/g)) {
      if (!classesOf(m[0]).includes("data-table")) bad.push(`${page}: ${m[0]}`);
    }
  }
  assert.deepEqual(bad, [], `tables off the pattern (the signed-in pages and the Playbox keep their own kits):\n  ${bad.join("\n  ")}`);
});
