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
  assert.equal(h2s.length, 9);
  for (const h of h2s.slice(0, 8)) assert.match(h, /\?$/, `"${h}" is a question`);
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
  assert.equal(figs.length, 12, "twelve figures, FIG. 1 to FIG. 12");
  figs.forEach((fig, i) => {
    assert.match(fig, new RegExp(`<figcaption class="fig mono">FIG\\. ${i + 1} &middot; `), `FIG. ${i + 1} is captioned, in order`);
    const img = fig.match(/<img\b[^>]*>/);
    if (img) {
      const alt = img[0].match(/\salt="([^"]*)"/);
      assert.ok(alt && alt[1].length > 40, `FIG. ${i + 1}: the image says what it shows`);
    } else if (/data-scrub/.test(fig)) {
      assert.match(fig, /<ol class="llm-train scroll-box" data-scrub data-scrub-start="4" aria-label="[^"]{20,}"/, "the scrubber is named by its list");
    } else {
      assert.match(fig, /role="img" aria-label="Diagram: [^"]{60,}"/, `FIG. ${i + 1}: the diagram is described in words`);
    }
  });
});

test("how-llms: the diagrams carry no SVG text (their words are page text, so they reflow on a phone)", () => {
  const figs = (mainOf(read(PAGE)).match(/<figure\b[\s\S]*?<\/figure>/g) || []).join("");
  const svgs = figs.match(/<svg\b[\s\S]*?<\/svg>/g) || [];
  assert.equal(svgs.length, 7, "the arcs, five training waves and the trace");
  for (const svg of svgs) {
    assert.doesNotMatch(svg, /<text\b/);
    assert.match(svg, /aria-hidden="true"/, "every drawing is decoration under a described figure");
  }
});

test("how-llms: the training scrubber is the shared component, five checkpoints, each named", () => {
  const list = mainOf(read(PAGE)).match(/<ol class="llm-train scroll-box" data-scrub[\s\S]*?<\/ol>/)[0];
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
  for (const n of [3, 6, 8, 10]) assert.match(caps[n - 1], /ILLUSTRATIVE/, `FIG. ${n} shows made-up numbers and says so`);
});

/* ---- the one product claim matches the ledger ---------------------------------------- */

test("how-llms: the Wave line says what wave-status.json says, and no more", () => {
  const ledger = JSON.parse(src("data/wave-status.json"));
  const tier = (id) => ledger.tiers.find((t) => t.id === id);
  const body = text(mainOf(read(PAGE)));
  const pico = tier("pico"), nano = tier("nano");
  assert.equal(pico.stage, "on-air", "the page says Pico is on air; the ledger must agree");
  assert.ok(body.includes(`Wave Pico, is on air today as the free band ${pico.onAirBand}`), "the band named is the ledger's");
  assert.equal(pico.publicCheckpoint, false);
  assert.match(body, /its weights are not published yet/, "no public checkpoint, said plainly");
  assert.equal(nano.stage, "training", "the page says Nano is training; the ledger must agree");
  assert.match(body, /Wave Nano is in training/);
  assert.ok(ledger.tiers.every((t) => !t.publicCheckpoint), "nothing is released, so the page claims no download");
  assert.doesNotMatch(body, /open weights|download (the )?weights|weights are (out|available|published)/i);
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

test("how-llms: the training scrubber starts tuned on Trained (data-scrub-start), without scrolling the gallery", () => {
  const on = (el) => { el.listeners = {}; el.addEventListener = (t, f) => { (el.listeners[t] ||= []).push(f); }; return el; };
  const names = ["Step 0", "Early", "Later", "Much later", "Trained"];
  const lis = names.map((n) => on({ cls: new Set(), querySelector: (q) => (q === "b" ? { textContent: n } : null) }));
  lis.forEach((li) => { li.classList = { toggle: (c, v) => (v ? li.cls.add(c) : li.cls.delete(c)) }; });
  let range = null, scrolled = 0;
  const make = (start) => on({ getAttribute: (k) => (k === "data-scrub-start" ? start : "Training"), querySelectorAll: () => lis,
    scrollWidth: 1000, clientWidth: 300, scrollLeft: 0, scrollTo() { scrolled++; }, parentNode: { insertBefore: (el) => { range = el; } } });
  const run = (start) => {
    const ol = make(start);
    const win = { document: { querySelectorAll: (q) => (q === "[data-scrub]" ? [ol] : []),
      createElement: (tag) => on({ tagName: tag, value: "", setAttribute() {}, style: { setProperty() {} } }) },
      matchMedia: () => ({ matches: false }), setTimeout: (f) => f(), clearTimeout() {} };
    win.window = win; vm.createContext(win); vm.runInContext(src("js/scrub.js"), win);
    return lis.findIndex((l) => l.cls.has("is-tuned"));
  };
  assert.equal(run("4"), 4); assert.equal(range.value, "4");
  assert.equal(run(null), 0, "no attribute: the first item, as before");
  assert.equal(run("9"), 0, "out of range: the first item");
  assert.equal(scrolled, 0, "loading never scrolls the gallery");
});
