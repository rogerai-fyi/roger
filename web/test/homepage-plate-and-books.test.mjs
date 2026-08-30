// Executable spec for two homepage changes:
//
//  1. §5 SPECIFICATIONS shrinks from eleven full-width rows to one dense data plate.
//     The point is real estate, not content - so this asserts that every single
//     specification survived the compression. A "shorter" section that quietly dropped
//     three rows would be a regression wearing a redesign.
//
//  2. §6 THE INDUSTRIAL SERIES: the o'ailly books. The honesty rule here is the same one
//     the App Store slot follows - name only what a stranger can actually open today.
//     Two of the three industrial titles are live on oailly.com; the third is in draft and
//     must not appear until it is not.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const home = () => readFileSync(path.join(DIST, "index.html"), "utf8");
const section = (id) => home().match(new RegExp(`<section[^>]*id="${id}"[\\s\\S]*?</section>`))?.[0] || "";

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- §5, compressed ------------------------------------------------ */

const SPECS = [
  "Protocol", "Transport", "Endpoint", "Pricing", "Settlement", "Lineage",
  "Split", "Backends", "Interfaces", "Platforms", "Reach",
];

test("spec plate: every specification survived the compression", () => {
  const spec = section("spec");
  assert.ok(spec, "the spec section is still there");
  for (const label of SPECS) {
    assert.match(spec, new RegExp(`<dt>${label}</dt>`), `the ${label} row`);
  }
  const cells = spec.match(/<dt>/g) || [];
  assert.equal(cells.length, SPECS.length, "no row was dropped or duplicated");
});

test("spec plate: the values are still the committed ones", () => {
  const spec = section("spec").replace(/\s+/g, " ");
  assert.match(spec, /OpenAI-compatible/);
  assert.match(spec, /70 \/ 30/, "the split");
  assert.match(spec, /127\.0\.0\.1/, "the local endpoint");
  assert.match(spec, /no shell, no files, no context/, "the reach limit");
});

test("spec plate: it is a dense plate, not eleven full-width rows", () => {
  const spec = section("spec");
  assert.match(spec, /class="plate"/, "the section renders the data plate");
  assert.doesNotMatch(spec, /class="spec__row"/, "the tall row layout is gone");
  // The section head collapsed into the plate's own etched label, so the standing
  // heading-plus-lead-paragraph block should no longer be there.
  assert.doesNotMatch(spec, /<p>/, "no lead paragraph left to pad the section out");
  assert.doesNotMatch(spec, /section__head/, "no standalone section head");
});

/* ---- §6, the books ------------------------------------------------- */

const LIVE_BOOKS = [
  { slug: "local-llms-for-manufacturing", title: "Local LLMs for Manufacturing", cover: "cover-manufacturing.svg" },
  { slug: "inference-on-the-edge", title: "Inference on the Edge", cover: "cover-inference.svg" },
];

test("books: the section names the press and both published titles", () => {
  const books = section("books");
  assert.ok(books, "the homepage carries a books section");
  assert.match(books, /o&rsquo;ailly|o'ailly/i, "it names the press");
  for (const b of LIVE_BOOKS) {
    assert.ok(books.includes(b.title), `it names ${b.title}`);
    assert.ok(books.includes(`https://oailly.com/read/rogerai-labs--${b.slug}/`),
      `it links straight to ${b.slug}`);
  }
});

test("books: a title appears only once a stranger can open it", () => {
  // LLMs for Machines is Nº 3 and still a draft. Same rule as the reserved App Store
  // slot: the site does not announce an artifact nobody can reach.
  assert.doesNotMatch(home(), /LLMs for Machines/i,
    "an unpublished title is named on the homepage");
});

test("books: the covers are real local assets with real alt text", () => {
  const books = section("books");
  for (const b of LIVE_BOOKS) {
    const img = books.match(new RegExp(`<img[^>]*assets/books/${b.cover}[^>]*>`))?.[0];
    assert.ok(img, `${b.cover} is shown`);
    assert.match(img, /alt="[^"]{15,}"/, `${b.cover} has descriptive alt text`);
    assert.match(img, /loading="lazy"/, "covers are below the fold, so they load lazily");
    assert.match(img, /width="\d+" height="\d+"/, "intrinsic size, so nothing reflows");
    assert.ok(existsSync(path.join(DIST, "assets", "books", b.cover)), `${b.cover} ships`);
  }
});

test("books: a cover carries its illustration, it does not link to one", () => {
  // These SVGs are loaded through <img src>, which runs them in secure static mode:
  // an external reference is never fetched, so the press's sibling comfyui/*.png
  // href rendered an empty frame where the insect should be. Nothing in a rendered
  // page catches that, because no test renders an SVG - so the rule is structural.
  for (const b of LIVE_BOOKS) {
    const svg = readFileSync(path.join(DIST, "assets", "books", b.cover), "utf8");
    const refs = [...svg.matchAll(/(?:xlink:)?href="([^"]+)"/g)].map((m) => m[1]);
    for (const ref of refs) {
      assert.ok(/^data:/.test(ref), `${b.cover} references ${ref.slice(0, 40)} instead of inlining it`);
    }
  }
});

test("books: the nudge to the press is a real link, not just a mention", () => {
  const books = section("books");
  assert.match(books, /href="https:\/\/oailly\.com\/?"/, "oailly.com itself is linked");
  assert.match(books, /rel="[^"]*noopener/, "external links carry rel=noopener");
});

test("books: the section is honest about who wrote them", () => {
  const books = section("books").replace(/\s+/g, " ");
  // The whole premise of the press is that the books are machine-written and then
  // reviewed. Presenting them as our own prose would be the one dishonest way to run
  // this section.
  assert.match(books, /written by (a )?machine|machine-written|written by machines/i,
    "it says the books were written by machines");
  assert.match(books, /verif|review/i, "and that a human stands behind them");
});
