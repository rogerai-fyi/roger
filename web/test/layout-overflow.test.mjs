// Layout guards: no page may scroll sideways, and no figure may light up the dark theme.
//
// The real check is a browser sweep (scripts/overflow-sweep.py: every built page at 390
// and 1440 wide, documentElement.scrollWidth <= clientWidth). This web tree runs
// dependency-free under `node --test` - no browser, no jsdom - so this file is the static
// twin of that sweep: it pins the specific CSS and markup that made each live overflow
// go away, so a later edit cannot quietly bring one back.
//
// Each block names the bug it locks down:
//   1. /models.html on a phone: the model-name column collapsed to one character a line.
//   2. Broadcast figures had no shared style, so an <img> rendered at its natural 1600px.
//   3. Article figures were styled inline with a hard-coded paper color: a bright box on
//      the dark theme.
//   4. Article <pre> had no overflow rule; the Tower broadcast ran ~930px wide on a phone.
//   5. The closing install command broke mid-token ("vast-/onstart.sh", "--/model").
//   6. The Let's-talk scrim sat under the left rail and the nav, so both stayed bright.
//   7. Pages the sitewide sweep found overflowing on a phone (manual, integrations,
//      homepage hero, hardware page cards).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const css = (name) => readFileSync(path.join(SRC, "styles", name), "utf8");
const page = (name) => readFileSync(path.join(DIST, name), "utf8");
const BROADCASTS = readdirSync(SRC).filter((f) => /^broadcasts-.+\.html$/.test(f)).sort();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// ---- a tiny CSS reader ---------------------------------------------------------------
// Flattens a stylesheet into { media, selector, decls } rows. `media` is the enclosing
// @media prelude ("" at top level). Enough for assertions about which rules exist where;
// not a general CSS parser.
function rules(text) {
  const src = text.replace(/\/\*[\s\S]*?\*\//g, "");
  const out = [];
  (function walk(s, media) {
    let i = 0;
    while (i < s.length) {
      const open = s.indexOf("{", i);
      if (open < 0) return;
      const prelude = s.slice(i, open).trim();
      let depth = 1, j = open + 1;
      while (j < s.length && depth) { if (s[j] === "{") depth++; else if (s[j] === "}") depth--; j++; }
      const body = s.slice(open + 1, j - 1);
      if (prelude.startsWith("@media")) walk(body, prelude);
      else if (!prelude.startsWith("@")) {
        const decls = {};
        for (const d of body.split(";")) {
          const k = d.indexOf(":");
          if (k > 0) decls[d.slice(0, k).trim().toLowerCase()] = d.slice(k + 1).trim();
        }
        for (const sel of prelude.split(",")) out.push({ media, selector: sel.trim().replace(/\s+/g, " "), decls });
      }
      i = j;
    }
  })(src, "");
  return out;
}
// All declarations for `selector` (merged in source order), optionally only inside media
// preludes the predicate accepts.
function decl(text, selector, mediaOk = (m) => m === "") {
  const merged = {};
  for (const r of rules(text)) if (r.selector === selector && mediaOk(r.media)) Object.assign(merged, r.decls);
  return merged;
}
const maxWidth = (m) => Number((m.match(/max-width:\s*(\d+)px/) || [])[1] || Infinity);
const phone = (m) => maxWidth(m) <= 560;

// ---- 1. models directory on a phone ----------------------------------------------------
test("models: the directory stacks on a phone - the name gets a whole row", () => {
  const c = css("models.css");
  const row = decl(c, ".band-row", phone);
  assert.ok(row["grid-template-areas"], "a phone-width rule re-flows .band-row into named areas");
  const firstRow = row["grid-template-areas"].match(/"([^"]+)"/)[1].trim().split(/\s+/);
  assert.ok(firstRow.length >= 2 && firstRow.every((a) => a === "name"),
    `the name spans the whole first row (got "${firstRow.join(" ")}")`);
  assert.match(row["grid-template-areas"], /"[^"]*price[^"]*"/, "price moves to its own row below the name");
  assert.equal(decl(c, ".band-row--head", phone)["grid-template-areas"], row["grid-template-areas"],
    "the column heads follow the same stacked areas");
});

test("models: a model name never collapses to its narrowest possible width", () => {
  // word-break:break-word (and overflow-wrap:anywhere) shrink an element's MIN-CONTENT to
  // one glyph, so the grid/flex math starved the name column: "wa/ve/-/pi/co".
  const name = decl(css("models.css"), ".band-name");
  assert.notEqual(name["word-break"], "break-word", ".band-name must not use word-break:break-word");
  assert.notEqual(name["overflow-wrap"], "anywhere", ".band-name must not use overflow-wrap:anywhere");
  assert.equal(name["min-width"], "0", "the name shrinks beside the dot and wraps inside itself");
  assert.equal(decl(css("models.css"), ".band-tag")["white-space"], "nowrap",
    "a tag like \"✓ verified\" never splits across lines");
});

// ---- 2 + 3. article figures ------------------------------------------------------------
test("broadcasts: one shared figure style keeps every figure inside the column", () => {
  const c = css("broadcasts.css");
  assert.equal(decl(c, ".bc-figure")["max-width"], "100%", ".bc-figure is capped at the column");
  for (const media of ["img", "video"]) {
    const d = decl(c, `.bc-figure ${media}`);
    assert.equal(d["max-width"], "100%", `.bc-figure ${media} never exceeds the column`);
    assert.equal(d["height"], "auto", `.bc-figure ${media} keeps its aspect ratio`);
  }
  assert.ok(Object.keys(decl(c, ".bc-figure figcaption")).length, "figcaption has a shared style");
});

test("broadcasts: the figure plate is themed, never a hard-coded paper color", () => {
  const plate = decl(css("broadcasts.css"), ".bc-figure--plate");
  assert.match(plate.background || "", /var\(--paper-2\)/, "the plate reads the theme's paper token");
  assert.match(plate.border || "", /var\(--hairline/, "and a theme hairline");
  // the page-scoped hero plates had the same hard-coded paper
  for (const f of ["broadcast-routing.css", "broadcast-agent-governance.css", "broadcast-gpu-isolation.css"]) {
    for (const r of rules(css(f))) {
      assert.doesNotMatch(r.decls.background || "", /#f3f1ea/i, `${f} ${r.selector} uses a theme token, not #f3f1ea`);
    }
  }
});

test("broadcasts: no figure, figure image or caption is styled inline", () => {
  for (const f of BROADCASTS) {
    const html = page(f);
    assert.doesNotMatch(html, /#F3F1EA/i, `${f}: no hard-coded paper color`);
    for (const fig of html.match(/<figure\b[\s\S]*?<\/figure>/g) || []) {
      assert.doesNotMatch(fig.match(/<figure\b[^>]*>/)[0], /\sstyle=/, `${f}: <figure> has no inline style`);
      for (const tag of fig.match(/<(?:img|video|figcaption)\b[^>]*>/g) || []) {
        assert.doesNotMatch(tag, /\sstyle=/, `${f}: ${tag.slice(0, 60)}... has no inline style`);
      }
    }
  }
});

test("broadcasts: a wide table inside a figure scrolls in its own box", () => {
  for (const f of BROADCASTS) {
    for (const fig of page(f).match(/<figure\b[\s\S]*?<\/figure>/g) || []) {
      if (!/<table\b/.test(fig)) continue;
      assert.match(fig, /<div class="bc-scroll">\s*<table\b/, `${f}: a figure's table sits in .bc-scroll`);
    }
  }
  assert.equal(decl(css("broadcasts.css"), ".bc-scroll")["overflow-x"], "auto");
});

// ---- 4. code blocks ------------------------------------------------------------------
test("broadcasts: article code blocks scroll inside their own box", () => {
  const d = decl(css("broadcasts.css"), ":where(.bc-post__body) pre");
  assert.equal(d["overflow-x"], "auto", "a long line scrolls inside the <pre>");
  assert.equal(d["max-width"], "100%", "and the <pre> never widens the column");
});

test("hardware: the ladder scrolls inside its box, the run cards never widen a phone", () => {
  const c = css("research-hardware.css");
  assert.equal(decl(c, ".hw-figure__scroll")["overflow-x"], "auto", "the ladder band scrolls in its own box");
  assert.match(page("research-hardware.html"), /<div class="hw-figure__scroll">\s*<svg class="hw-ladder"/);
  assert.equal(decl(c, ".hw-exps")["grid-template-columns"], "minmax(0, 1fr)",
    "a bare 1fr column is minmax(auto,1fr): the bench table's nowrap cells forced it wide");
  const cell = decl(c, ".hw-bench td", phone);
  assert.equal(cell["white-space"], "normal", "on a phone the bench cells may wrap");
});

// ---- 5. commands break only at spaces ------------------------------------------------
// Browsers break after a hyphen or slash, so a bare <code> command wraps as
// "vast-/onstart.sh". A .cmd-words command carries each word in its own nowrap span:
// the only break opportunities left are the spaces between them.
const words = (code) => code.replace(/<[^>]+>/g, "").replace(/&[a-z]+;/g, "x").trim().split(/\s+/);
function assertCmdWords(where, code) {
  assert.match(code, /^<code class="cmd-words">/, `${where}: ${code.slice(0, 70)} is a .cmd-words command`);
  const spans = [...code.matchAll(/<span>([^<]*(?:<(?!\/span>)[^<]*)*)<\/span>/g)].map((m) => m[1]);
  assert.deepEqual(spans.map((s) => s.replace(/&[a-z]+;/g, "x")), words(code), `${where}: one span per word`);
  assert.ok(spans.every((s) => !/\s/.test(s)), `${where}: no span holds a space`);
}

test("commands: every multi-word command in a sign-off note breaks only at spaces", () => {
  let n = 0;
  for (const f of BROADCASTS) {
    for (const note of page(f).match(/<div class="man-note[^"]*">[\s\S]*?<\/div>/g) || []) {
      for (const code of note.match(/<code\b[\s\S]*?<\/code>/g) || []) {
        if (words(code).length < 2) continue;
        assertCmdWords(f, code); n++;
      }
    }
  }
  assert.ok(n >= 12, `found ${n} sign-off commands`);
  assert.equal(decl(css("components.css"), ".cmd-words > span")["white-space"], "nowrap");
});

test("commands: the rent-a-box bootstrap breaks only at spaces too", () => {
  for (const f of ["research-hardware.html", "broadcasts-what-a-million-tokens-costs.html"]) {
    const code = page(f).match(/<code\b[^>]*>(?:(?!<\/code>)[\s\S])*vast-onstart\.sh(?:(?!<\/code>)[\s\S])*<\/code>/)[0];
    assertCmdWords(f, code);
  }
  assert.notEqual(decl(css("research-hardware.css"), ".hw-rent code")["white-space"], "nowrap",
    "the whole command no longer refuses to wrap; its words do");
});

// ---- 6. the Let's-talk scrim covers the whole viewport --------------------------------
test("lets-talk: the scrim and dialog paint above the rail and the nav", () => {
  const tokens = decl(css("tokens.css"), ":root");
  const resolve = (v) => {
    const s = String(v).replace(/var\((--[\w-]+)\)/g, (_, k) => tokens[k]);
    return Function(`return (${s.replace(/^calc/, "")})`)();
  };
  const base = css("base.css");
  const scrim = resolve(decl(base, ".lt-scrim")["z-index"]);
  const modal = resolve(decl(base, ".lt-modal")["z-index"]);
  const rail = resolve(decl(base, ".rail")["z-index"]);
  const nav = resolve(decl(base, ".nav")["z-index"]);
  assert.ok(Number.isFinite(rail) && Number.isFinite(nav), "resolved the rail + nav layers");
  assert.ok(scrim > rail && scrim > nav, `scrim z ${scrim} above rail ${rail} and nav ${nav}`);
  assert.ok(modal > scrim, "the dialog sits above its own scrim");
  assert.equal(decl(base, ".lt-scrim").inset, "0", "and the scrim spans the full viewport");
});

// ---- 7. other pages the sweep caught ---------------------------------------------------
test("manual: every command block sits inside a styled manual section", () => {
  const html = page("manual.html");
  const body = html.slice(html.indexOf('class="man-doc__body"'));
  let depth = 0, pos = 0;
  for (const m of body.matchAll(/<section\b|<\/section>|<pre\b/g)) {
    if (m[0] === "<section") depth++;
    else if (m[0] === "</section>") { depth--; if (depth < 0) break; }
    else assert.ok(depth > 0, `a <pre> at offset ${m.index} is outside every .man-sec, so nothing scrolls it`);
    pos = m.index;
  }
  assert.ok(pos > 0);
});

test("integrations: the wide table scrolls in its own box", () => {
  assert.match(page("integrations.html"), /<div class="scroll">\s*<table class="intg">/);
  assert.equal(decl(css("integrations.css"), ".scroll")["overflow-x"], "auto");
});

test("integrations: the two-ways column on a phone lets its code block scroll", () => {
  // the single phone column was a bare 1fr, so the widest line of a .ways__code block
  // set the column width and the page scrolled instead of the block
  const ways = decl(css("integrations.css"), ".ways", (m) => maxWidth(m) <= 760);
  assert.equal(ways["grid-template-columns"], "minmax(0, 1fr)");
  assert.equal(decl(css("integrations.css"), ".ways__code")["overflow-x"], "auto");
});

test("home: the hero column may shrink below its longest word", () => {
  const c = css("home.css");
  assert.equal(decl(c, ".hero__grid")["grid-template-columns"], "minmax(0, 1fr)",
    "a bare 1fr column is forced as wide as its widest unbreakable line");
  // the headline is sized to its own box (container units, with a vw fallback), so
  // on a phone the nowrap underlined phrase always fits the column
  const title = decl(c, ".hero__title");
  assert.match(title["font-size"] || "", /cqi|vw/,
    "on a phone the headline scales with its column so the nowrap underlined phrase fits");
});
