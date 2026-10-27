// Broadcast 016 ("How LLMs actually work, in pictures.") is a generic explainer whose
// figures carry the argument. These tests pin what makes it safe to publish and keep true:
//   - it is wired like every broadcast (index order, sitemap, llms.txt, OG card, FAQ schema);
//   - every figure explains itself to a screen reader, and every diagram is complete without
//     motion (the motion is scroll-driven, opt-in, and starts FROM a state, so the base rules
//     are the finished figure for no-JS, reduced motion, print and older browsers);
//   - its one product claim (the Wave line) says exactly what src/data/wave-status.json says;
//   - it carries no internal detail: a conservative denylist of things that must never ship
//     in a public explainer (internal paths and hosts, training-run settings, customer names).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const PAGE = "broadcasts-how-llms-work.html";
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const visible = (html) => html.replace(/<!--[\s\S]*?-->/g, "");
const mainOf = (html) => (visible(html).match(/<main\b[\s\S]*<\/main>/) || [""])[0];
const text = (html) => html.replace(/<script\b[\s\S]*?<\/script>/g, " ").replace(/<style\b[\s\S]*?<\/style>/g, " ")
  .replace(/<[^>]+>/g, " ").replace(/&nbsp;/g, " ").replace(/&rsquo;/g, "'").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- wired like every other broadcast ---------------------------------- */

test("how-llms: the broadcast builds, is indexable, and is in the sitemap and llms.txt", () => {
  assert.doesNotMatch(read(PAGE), /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), new RegExp(`<loc>https://rogerai\\.fm/${PAGE}</loc>`));
  assert.match(read("llms.txt"), new RegExp(`\\(https://rogerai\\.fm/${PAGE}\\)`));
});

test("how-llms: the transmission log carries it first, as 016, above 015", () => {
  const log = read("broadcasts.html");
  const rows = [...log.matchAll(/<a class="bc-row__link" href="\/(broadcasts-[\w-]+\.html)">/g)].map((m) => m[1]);
  assert.equal(rows[0], PAGE, "newest first");
  assert.equal(rows[1], "broadcasts-jev-vs-wave.html", "016 sits right above 015");
  assert.match(log, /<b class="bc-row__no">BROADCAST 016<\/b>/);
  assert.match(read(PAGE), /BROADCAST 016 &middot; FIELD GUIDE/);
});

test("how-llms: a Quick Answer of 40 to 60 words opens the article", () => {
  const html = read(PAGE);
  const qa = html.match(/<blockquote class="bc-answer"[^>]*>([\s\S]*?)<\/blockquote>/);
  assert.ok(qa, "a .bc-answer");
  assert.match(qa[1], /<b>Quick answer\.<\/b>/);
  const words = text(qa[1]).replace(/Quick answer\./, "").trim().split(" ").length;
  assert.ok(words >= 40 && words <= 60, `the Quick Answer is ${words} words`);
});

test("how-llms: every section heading is a question a reader would type", () => {
  const h2s = [...mainOf(read(PAGE)).matchAll(/<section class="bc-sec"[^>]*>[\s\S]*?<h2>([^<]+)<\/h2>/g)].map((m) => m[1]);
  assert.equal(h2s.length, 12);
  for (const h of h2s.slice(0, 11)) assert.match(h, /\?$/, `"${h}" is a question`);
});

test("how-llms: the FAQPage JSON-LD parses and mirrors the visible FAQ", () => {
  const html = read(PAGE);
  const m = html.match(/<script type="application\/ld\+json">(\{"@context":"https:\/\/schema\.org","@type":"FAQPage".*?)<\/script>/s);
  assert.ok(m, "a FAQPage block exists");
  const faq = JSON.parse(m[1]);
  assert.ok(faq.mainEntity.length >= 6, `at least 6 questions (got ${faq.mainEntity.length})`);
  const dts = [...html.matchAll(/<dt><b>([^<]*)<\/b><\/dt>\s*<dd>([\s\S]*?)<\/dd>/g)];
  assert.equal(dts.length, faq.mainEntity.length, "schema and visible FAQ have the same count");
  faq.mainEntity.forEach((q, i) => {
    assert.equal(q.name, dts[i][1], "the questions match, in order");
    assert.equal(q.acceptedAnswer.text, text(dts[i][2]).trim(), `the answer to "${q.name}" matches word for word`);
  });
});

test("how-llms: the hero, plates and OG card are real shipped assets", () => {
  const html = read(PAGE);
  for (const name of ["llm-works-hero", "llm-works-dials", "llm-works-library", "llm-works-plant"]) {
    assert.match(html, new RegExp(`src="assets/broadcasts/${name}\\.webp(\\?v=[0-9a-f]+)?"`), `${name} is shown as WebP`);
    for (const f of [`${name}.webp`, `${name}-800.webp`, `${name}.png`]) readFileSync(path.join(WEB, "dist", "assets", "broadcasts", f));
  }
  assert.match(html, /property="og:image" content="https:\/\/rogerai\.fm\/assets\/broadcasts\/llm-works-og\.png"/);
  const og = readFileSync(path.join(WEB, "src", "assets", "broadcasts", "llm-works-og.png"));
  assert.deepEqual([og.readUInt32BE(16), og.readUInt32BE(20)], [1200, 630], "the social card is 1200x630");
});

/* ---- every figure explains itself ---------------------------------------- */

test("how-llms: every figure has a caption and a text alternative", () => {
  const figs = [...mainOf(read(PAGE)).matchAll(/<figure\b[\s\S]*?<\/figure>/g)].map((m) => m[0]);
  assert.equal(figs.length, 18, "eighteen figures, FIG. 1 to FIG. 18");
  figs.forEach((fig, i) => {
    assert.match(fig, new RegExp(`<figcaption class="fig mono">FIG\\. ${i + 1} &middot; `), `FIG. ${i + 1} is captioned, in order`);
    const img = fig.match(/<img\b[^>]*>/);
    if (img) {
      const alt = img[0].match(/\salt="([^"]*)"/);
      assert.ok(alt && alt[1].length > 40, `FIG. ${i + 1}: the image says what it shows`);
      return;
    }
    // a scrubbed figure names its stops; a stage beside the stops is described in words
    if (/data-scrub/.test(fig)) assert.match(fig, /data-scrub(?: data-scrub-start="\d")? aria-label="[^"]{20,}"/, `FIG. ${i + 1}: the scrubber is named by its list`);
    if (/role="(img|group)"/.test(fig) || !/data-scrub/.test(fig)) assert.match(fig, /role="(img|group)" aria-label="[^"]{60,}"/, `FIG. ${i + 1}: the diagram is described in words`);
  });
});

test("how-llms: the diagrams carry no SVG text (their words are page text, so they reflow on a phone)", () => {
  const figs = (mainOf(read(PAGE)).match(/<figure\b[\s\S]*?<\/figure>/g) || []).join("");
  const svgs = figs.match(/<svg\b[\s\S]*?<\/svg>/g) || [];
  assert.equal(svgs.length, 8, "the arcs, five training waves, the error curves and the trace");
  for (const svg of svgs) {
    assert.doesNotMatch(svg, /<text\b/);
    assert.match(svg, /aria-hidden="true"/, "every drawing is decoration under a described figure");
  }
});

test("how-llms: the training scrubber is the shared component, five checkpoints, each named", () => {
  const list = mainOf(read(PAGE)).match(/<ol class="llm-train llm-stops" data-scrub data-scrub-start="4"[\s\S]*?<\/ol>/)[0];
  const items = [...list.matchAll(/<li>\s*<b>([^<]+)<\/b>/g)].map((m) => m[1]);
  assert.deepEqual(items, ["Step 0", "Early", "Later", "Much later", "Trained"]);
});

/* ---- motion is opt-in, and the drawing is complete without it ------------------- */

test("how-llms: every animation is scroll-driven, inside reduced-motion and support guards, and never loops", () => {
  const css = src("styles/broadcast-how-llms-work.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const guard = css.indexOf("@media (prefers-reduced-motion: no-preference)");
  assert.ok(guard > 0, "motion lives under prefers-reduced-motion: no-preference");
  const block = css.slice(guard);
  assert.match(block, /^@media \(prefers-reduced-motion: no-preference\) \{\s*@supports \(animation-timeline: view\(\)\)/);
  const before = css.slice(0, guard);
  assert.doesNotMatch(before, /animation(-name)?\s*:/, "no animation outside the guard");
  assert.doesNotMatch(css, /infinite/, "nothing loops on its own");
  assert.match(block, /animation-timeline: --llm-fig/, "the reader's scroll drives it");
  for (const [, name, body] of css.matchAll(/@keyframes ([\w-]+) \{([\s\S]*?)\}\s*\}/g)) {
    assert.match(body, /^\s*from \{/, `${name} animates FROM a state, so the base rule is the finished frame`);
    assert.doesNotMatch(body, /\bto \{|100%/, `${name} has no end frame of its own`);
  }
});

/* ---- house style ------------------------------------------------------------------ */

test("how-llms: no em dashes, in the source or the build", () => {
  assert.doesNotMatch(src(PAGE), /—|&mdash;/);
  assert.doesNotMatch(read(PAGE), /—/);
  assert.doesNotMatch(src("styles/broadcast-how-llms-work.css"), /—/);
});

test("how-llms: the illustrative numbers are labelled as illustrative", () => {
  const caps = [...read(PAGE).matchAll(/<figcaption class="fig mono">([^<]*)<\/figcaption>/g)].map((m) => m[1]);
  for (const n of [3, 6, 10, 14, 15]) assert.match(caps[n - 1], /ILLUSTRATIVE/, `FIG. ${n} shows made-up numbers and says so`);
  assert.match(caps[7], /A SKETCH/, "FIG. 8 says it is a sketch");
});

/* ---- the one product claim matches the ledger ---------------------------------------- */

test("how-llms: every Wave claim says what wave-status.json says, and no more", () => {
  const ledger = JSON.parse(src("data/wave-status.json"));
  const tier = (id) => ledger.tiers.find((t) => t.id === id);
  const html = read(PAGE);
  const body = text(mainOf(html));
  const pico = tier("pico"), nano = tier("nano");
  assert.equal(pico.stage, "on-air"); assert.equal(nano.stage, "training");
  assert.match(body, /Wave Pico is on air, which means/, "the page states Pico's stage");
  assert.ok(body.includes(ledger.stages["on-air"]), "and quotes the ledger's definition of that stage, verbatim");
  assert.match(body, /Wave Nano is in training/);
  assert.ok(html.includes(`<span>${pico.onAirBand}</span>`), "the sign-off names the ledger's band");
  assert.ok(ledger.tiers.every((t) => !t.publicCheckpoint), "nothing is released, so the page claims no download");
  assert.doesNotMatch(body, /open weights|download (the )?weights|weights are (out|available|published)/i);
  // the size ladder: one row per ladder tier, in ledger order, the ledger's size and stage word for word
  const rows = [...html.matchAll(/<li class="llm-ladder__row[^"]*"><span class="llm-ladder__name">Wave (\w+)<\/span>[\s\S]*?<span class="llm-ladder__size">([^<]*)<\/span><span class="llm-ladder__fit">[^<]*<em>([^<]*)<\/em>/g)]
    .map((m) => ({ name: m[1].toLowerCase(), size: m[2].replace(/&ndash;/g, "\u2013").replace(/&gt;/g, ">"), stage: m[3].replace(/ /g, "-") }));
  const ladder = ledger.tiers.filter((t) => t.kind === "tier");
  assert.deepEqual(rows.map((r) => r.name), ladder.map((t) => t.id), "every ladder tier, in order");
  rows.forEach((r, i) => {
    assert.equal(r.size, ladder[i].size, `${r.name}: the ledger's size`);
    assert.equal(r.stage, ladder[i].stage, `${r.name}: the ledger's stage`);
  });
  assert.match(html, /Microcontroller<\/span>[\s\S]{0,120}no model/, "no Wave tier runs on a microcontroller: it reads the sensor");
});

test("how-llms: the glossary defines the words the page uses, and the vendor list is five quoted questions", () => {
  const html = read(PAGE);
  const gloss = html.match(/<dl class="tint-panel llm-gloss">[\s\S]*?<\/dl>/)[0];
  const terms = [...gloss.matchAll(/<dt>([^<]+)<\/dt><dd>([^<]+)<\/dd>/g)].map((m) => ({ term: m[1], line: m[2] }));
  assert.ok(terms.length >= 10 && terms.length <= 12, `${terms.length} terms`);
  const prose = text(mainOf(html).replace(/<dl class="tint-panel llm-gloss">[\s\S]*?<\/dl>/, "")).toLowerCase();
  for (const { term, line } of terms) {
    assert.ok(prose.includes(term.toLowerCase().replace(/s$/, "")), `"${term}" is used on the page`);
    assert.ok(line.split(" ").length <= 16, `"${term}": one line`);
  }
  const ask = [...html.match(/<ol class="llm-ask">[\s\S]*?<\/ol>/)[0].matchAll(/<li>([\s\S]*?)<\/li>/g)].map((m) => m[1]);
  assert.equal(ask.length, 5);
  for (const q of ask) assert.match(q, /^&ldquo;[^?]+\?&rdquo;/, "a quoted question");
});

test("how-llms: every scrubbed stage draws its last stop by default and reads the tuned stop through :has()", () => {
  const css = src("styles/broadcast-how-llms-work.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const html = read(PAGE);
  for (const [stage, def] of [["llm-mapstage", null], ["llm-attn", "llm-attn__set--5"], ["llm-loss", "llm-loss__mark--5"], ["llm-fault", "llm-fault__cur--3"]]) {
    assert.match(css, new RegExp(`\\.${stage}:has\\(\\.llm-stops > li:nth-child\\(1\\)\\.is-tuned\\)`), `${stage} reads its stops`);
    if (def) assert.match(css, new RegExp(`\\.${def} \\{ display: (block|grid); \\}`), `${stage}: a drawn default without JS`);
  }
  // the stops lists are laid out whole (a wrapping row), so data-scrub-start applies everywhere
  for (const m of html.matchAll(/<ol class="([^"]*llm-stops[^"]*)" data-scrub( data-scrub-start="(\d)")? aria-label="([^"]+)"/g)) {
    if (!/llm-attn__row/.test(m[1])) assert.ok(m[2], `${m[4]}: starts on its payoff stop`);
  }
  assert.match(css, /\.llm-stops \{[^}]*flex-wrap: wrap/, "the stops wrap instead of scrolling");
});

test("how-llms: the map's words are keyboard-reachable buttons in a named group, and a group lights on hover or focus", () => {
  const map = read(PAGE).match(/<div class="llm-map" role="group" aria-label="[^"]{60,}">[\s\S]*?<\/div>/)[0];
  const words = [...map.matchAll(/<button type="button" class="llm-map__w[^"]*" data-g="([abc])" aria-label="[^"]+">/g)];
  assert.equal(words.length, 12);
  const css = src("styles/broadcast-how-llms-work.css");
  for (const g of ["a", "b", "c"]) assert.match(css, new RegExp(`\\.llm-map:has\\(\\.llm-map__w\\[data-g="${g}"\\]:is\\(:hover, :focus-visible\\)\\) \\.llm-map__w\\[data-g="${g}"\\]`));
  assert.match(css, /\.llm-map__w:focus-visible \{ outline: 2px solid var\(--live\)/, "a visible focus ring");
});

/* ---- nothing internal ships ------------------------------------------------------------ */

// Conservative on purpose: a public explainer stays at textbook level. Internal paths, hosts
// and repo names; training-run settings and tooling; customer and site names. A match here is
// a leak, not a style nit.
const NEVER = [
  /\/mnt\//, /\/home\//, /localhost|127\.0\.0\.1/, /\.internal\b/, /docs-internal|internal-docs/i,
  /roggentoo|rogerai-house|hermes/i, /wave-(train|packed|ckpt)/i, /pretrain_|nano-1p5b|_ddp\b/i,
  /adamw|learning rate|\blr\s*=|batch size|hidden[ -]size|warmup steps|grad(ient)? checkpoint/i,
  /\b\d+\s*(attention heads|kv heads|layers deep)\b/i, /tokens? of (our|proprietary)/i,
  /firedefense|southern california/i,
];
test("how-llms: no internal detail, run setting or customer name appears anywhere in the shipped page, comments included", () => {
  const page = read(PAGE);
  for (const re of NEVER) assert.doesNotMatch(page, re);
});

test("how-llms: the page has a first-load weight budget", () => {
  const budget = JSON.parse(readFileSync(path.join(WEB, "test", "fixtures", "page-weight-budget.json"), "utf8"));
  assert.ok(budget.pages[PAGE] > 0, "budgeted in test/fixtures/page-weight-budget.json");
});

test("how-llms: the shipped source comment is a neutral one-liner", () => {
  const first = read(PAGE).match(/<!DOCTYPE html>\s*<!--([\s\S]*?)-->/)[1];
  assert.ok(!first.includes("\n"), "one line");
  assert.doesNotMatch(first, /founder|reddit|brief/i);
});

test("how-llms: vocabulary size is stated as it is today, in the body, the FAQ and the schema alike", () => {
  const html = read(PAGE);
  const n = (html.match(/tens of\s+thousands to a few hundred thousand/g) || []).length;
  assert.equal(n, 3, "body, visible FAQ and JSON-LD");
  assert.doesNotMatch(html.replace(/tens of\s+thousands to a few hundred thousand/g, ""), /tens of\s+thousands/);
});

test("how-llms: the Quick Answer does not claim the model learns only from wrong guesses", () => {
  const qa = read(PAGE).match(/<blockquote class="bc-answer"[^>]*>([\s\S]*?)<\/blockquote>/)[1];
  assert.doesNotMatch(qa, /wrong guess/);
  assert.match(qa, /after each round of guesses/);
});

test("how-llms: data-scrub-start tunes the start item on a wide list, but a sideways gallery starts on its first item so the needle and the view agree without a scroll", () => {
  const on = (el) => { el.listeners = {}; el.addEventListener = (t, f) => { (el.listeners[t] ||= []).push(f); }; return el; };
  const names = ["Step 0", "Early", "Later", "Much later", "Trained"];
  const mk = () => {
    const lis = names.map((n, k) => on({ cls: new Set(), offsetLeft: k * 200, offsetWidth: 180, querySelector: (q) => (q === "b" ? { textContent: n } : null) }));
    lis.forEach((li) => { li.classList = { toggle: (c, v) => (v ? li.cls.add(c) : li.cls.delete(c)) }; });
    return lis;
  };
  const run = (start, gallery) => {
    const lis = mk(); let range = null, scrolled = 0;
    const ol = on({ getAttribute: (k) => (k === "data-scrub-start" ? start : "Training"), querySelectorAll: () => lis,
      scrollWidth: gallery ? 1000 : 300, clientWidth: 300, scrollLeft: 0, scrollTo() { scrolled++; }, parentNode: { insertBefore: (el) => { range = el; } } });
    const win = { document: { querySelectorAll: (q) => (q === "[data-scrub]" ? [ol] : []),
      createElement: (tag) => on({ tagName: tag, value: "", attrs: {}, setAttribute(k, v) { this.attrs[k] = String(v); }, style: { setProperty() {} } }) },
      matchMedia: () => ({ matches: false }), setTimeout: (f) => f(), clearTimeout() {} };
    win.window = win; vm.createContext(win); vm.runInContext(src("js/scrub.js"), win);
    return { tuned: lis.findIndex((l) => l.cls.has("is-tuned")), value: range.value, text: range.attrs["aria-valuetext"], scrolled, left: ol.scrollLeft };
  };
  // a wide list (the desktop row): the start item is tuned, named and marked
  assert.deepEqual(run("4", false), { tuned: 4, value: "4", text: "Trained", scrolled: 0, left: 0 });
  assert.equal(run(null, false).tuned, 0, "no attribute: the first item, as before");
  assert.equal(run("9", false).tuned, 0, "out of range: the first item");
  // a sideways gallery (the phone): it shows its first item on load, so the needle starts there too,
  // and nothing scrolls it (the homepage rule: loading never scrolls a gallery)
  assert.deepEqual(run("4", true), { tuned: 0, value: "0", text: "Step 0", scrolled: 0, left: 0 });
});

test("how-llms: the flow strip's arrows share one type rule, so the phone's third arrow is set like the others", () => {
  const css = src("styles/broadcast-how-llms-work.css").replace(/\/\*[\s\S]*?\*\//g, "");
  const esc = (t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const rule = (sel) => [...css.matchAll(new RegExp("(?:^|[}\\n])\\s*" + esc(sel) + "\\s*\\{([^}]*)\\}", "g"))].map((m) => m[1]);
  const shared = rule(".llm-flow__steps > li:not(:last-child)::after");
  const typed = shared.filter((b) => /font-family:/.test(b) && /font-size:/.test(b) && /color:/.test(b));
  assert.equal(typed.length, 1, "one rule, covering every card but the last, sets the arrow's face, size and ink");
  assert.doesNotMatch(typed[0], /content:/, "it carries no direction: the layout rules set the arrow glyph and place it");
  for (const b of [...rule(".llm-flow__steps > li:not(:last-child, :nth-child(3))::after"), ...shared.filter((b) => b !== typed[0])]) {
    assert.doesNotMatch(b, /font-family:|font-size:|(?:^|[^-])color:/, `a layout rule sets no type: ${b.trim().slice(0, 60)}`);
    assert.match(b, /content:/, "a layout rule sets the glyph");
  }
});

/* ---- the second review's findings, pinned ----------------------------------------- */

// FIG. 5: every word has three hand-placed positions (trained, halfway, scattered), and at
// every stop no two chips overlap. Static geometry: a chip is its text in the mono face at
// 14px (0.6em per glyph) plus 22px of padding and border, 28px tall; the map is 608x380 on
// a desktop (the 640px article column less the plate's padding) and 326x407 on a 390 phone;
// the sizes are the rendered ones (a chip 28px tall, a label 13px), checked in a browser.
const PHONE_BLOCK = /@media \(max-width: 640px\) \{([\s\S]*?)\n\}/g;
function mapGeometry(css, phone) {
  // the phone block overrides the desktop rules, so each layout reads only its own
  const block = phone ? (css.match(PHONE_BLOCK) || []).join("\n") : css.replace(PHONE_BLOCK, "");
  const words = ["antenna", "dial", "signal", "over", "roger", "pump", "valve", "bearing", "motor", "bread", "soup", "oven"];
  const vars = (k) => {
    const out = {};
    for (const m of block.matchAll(new RegExp(`\\.llm-w${k}\\s*\\{([^}]*)\\}`, "g")))
      for (const [, name, v] of m[1].matchAll(/--(\w+):\s*([\d.]+)%/g)) out[name] = Number(v);
    return out;
  };
  return words.map((w, i) => {
    const base = vars(i + 1), over = phone ? { ...mapGeometry(css, false)[i].vars, ...base } : base;
    return { word: w, vars: over, w: w.length * 0.6 * 14 + 22, h: 28 };
  });
}
function overlaps(items, W, H, stop) {
  const box = (it) => {
    const x = it.vars[stop === "trained" ? "x" : stop === "halfway" ? "mx" : "rx"], y = it.vars[stop === "trained" ? "y" : stop === "halfway" ? "my" : "ry"];
    assert.ok(x !== undefined && y !== undefined, `${it.word} has a ${stop} position`);
    return { l: (x / 100) * W - it.w / 2, r: (x / 100) * W + it.w / 2, t: (y / 100) * H - it.h / 2, b: (y / 100) * H + it.h / 2 };
  };
  const pairs = [];
  for (let a = 0; a < items.length; a++) for (let b = a + 1; b < items.length; b++) {
    const A = box(items[a]), B = box(items[b]);
    if (A.l < B.r && B.l < A.r && A.t < B.b && B.t < A.b) pairs.push(`${items[a].word}/${items[b].word}`);
  }
  return pairs;
}
test("how-llms: FIG. 5 places every word by hand at all three stops, with no two chips overlapping, desktop or phone", () => {
  const css = src("styles/broadcast-how-llms-work.css").replace(/\/\*[\s\S]*?\*\//g, "");
  assert.match(css, /\.llm-mapstage:has\(\.llm-stops > li:nth-child\(2\)\.is-tuned\) \.llm-map__w \{ left: var\(--mx\); top: var\(--my\); \}/, "halfway is its own position, not a midpoint");
  for (const [items, W, H, where, phone] of [[mapGeometry(css, false), 608, 380, "desktop", false], [mapGeometry(css, true), 326, 407, "phone", true]]) {
    for (const stop of ["trained", "halfway", "scattered"]) assert.deepEqual(overlaps(items, W, H, stop), [], `${where}, ${stop}`);
    // the group labels (RADIO, MACHINES, KITCHEN: mono 11px, anchored top-left) show at the
    // trained and halfway stops, and no chip may sit over one
    const block = phone ? (css.match(PHONE_BLOCK) || []).join("\n") : css.replace(PHONE_BLOCK, "");
    const labels = [["RADIO", "a"], ["MACHINES", "b"], ["KITCHEN", "c"]].map(([name, k]) => {
      const m = block.match(new RegExp(`\\.llm-map__name--${k} \\{ --x: ([\\d.]+)%; --y: ([\\d.]+)%; \\}`));
      assert.ok(m, `${where}: ${name} label position`);
      return { name, l: (Number(m[1]) / 100) * W, t: (Number(m[2]) / 100) * H, w: name.length * 0.6 * 11 + name.length * 1.5, h: 13 };
    });
    for (const stop of ["trained", "halfway"]) {
      const covered = [];
      for (const it of items) {
        const x = it.vars[stop === "trained" ? "x" : "mx"], y = it.vars[stop === "trained" ? "y" : "my"];
        const c = { l: (x / 100) * W - it.w / 2, r: (x / 100) * W + it.w / 2, t: (y / 100) * H - it.h / 2, b: (y / 100) * H + it.h / 2 };
        for (const L of labels) if (c.l < L.l + L.w && L.l < c.r && c.t < L.t + L.h && L.t < c.b) covered.push(`${it.word} over ${L.name}`);
      }
      assert.deepEqual(covered, [], `${where}, ${stop}`);
    }
  }
});

// FIG. 3: scrubbing must not move the page. Every step carries the same parts (a line, four
// bars, a note), and nothing a step shows is hidden on another, so every stop has one height.
test("how-llms: FIG. 3's five steps carry the same parts, and the note is never display:none", () => {
  const steps = [...read(PAGE).match(/<ol class="llm-gen"[\s\S]*?<\/ol>/)[0].matchAll(/<li>([\s\S]*?)<\/li>/g)].map((m) => m[1]);
  assert.equal(steps.length, 5);
  for (const s of steps) {
    assert.equal((s.match(/class="llm-bars__row/g) || []).length, 4, "four bars");
    assert.equal((s.match(/class="llm-gen__note/g) || []).length, 1, "one note");
  }
  const css = src("styles/broadcast-how-llms-work.css").replace(/\/\*[\s\S]*?\*\//g, "");
  for (const [sel, body] of css.matchAll(/([^{}]*llm-gen__note[^{}]*)\{([^}]*)\}/g)) assert.doesNotMatch(body, /display:\s*none/, `${sel.trim()} hides the note`);
});

// No-JS and print: the scrubbers do not exist there, so the "Drag the needle" sentences go too.
test("how-llms: every scrubber instruction is marked .llm-js and hidden without scripts and in print", () => {
  const html = read(PAGE);
  const spans = [...html.matchAll(/<span class="llm-js">([^<]*)<\/span>/g)].map((m) => m[1].trim());
  assert.equal(spans.length, 5);
  for (const s of spans) assert.match(s, /^Drag /, s);
  assert.doesNotMatch(html.replace(/<span class="llm-js">[^<]*<\/span>/g, ""), /Drag (the needle|from)/, "no instruction escapes the span");
  const css = src("styles/broadcast-how-llms-work.css");
  assert.match(css, /html:not\(\.js\) \.llm-js \{ display: none; \}/);
  assert.match(css, /@media print \{[^}]*\.llm-js \{ display: none; \}/);
});

test("how-llms: FIG. 2 numbers its stages once each, and FIG. 15's bins are uniform", () => {
  const html = read(PAGE);
  const nos = [...html.match(/<ol class="llm-flow__steps"[\s\S]*?<\/ol>/)[0].matchAll(/<span class="llm-flow__no">([^<]*)<\/span>/g)].map((m) => m[1]);
  assert.equal(new Set(nos).size, nos.length, `no repeated stage label: ${nos.join(" ")}`);
  const axis = [...html.match(/<ol class="llm-dist__axis"[\s\S]*?<\/ol>/)[0].matchAll(/<li>([^<]*)<\/li>/g)].map((m) => Number(m[1]));
  const steps = axis.slice(1).map((v, i) => +(v - axis[i]).toFixed(2));
  assert.ok(steps.every((s) => s === steps[0]), `uniform bins: ${axis.join(", ")}`);
});
