// The hardware page is the one place on the site where a photograph of real, recognisable
// equipment sits next to a claim about what RogerAI runs on it. That is exactly the shape
// of page that drifts into implying a product exists, so almost everything here is a
// HONESTY guard rather than a layout guard:
//
//   - every tier named here must exist in research-models.html, carrying the SAME stage
//     text, so the catalogue and the hardware page cannot tell a reader two stories;
//   - nothing may be described as running, deployed or shipping on a board when the
//     catalogue says no artifact exists (research-models.html states in its own source
//     that nothing on it "may read as though there were" a library or artifact);
//   - every photograph is somebody else's work under a Creative Commons licence, so the
//     author, the licence and the source page travel with it, and no file may sit in the
//     folder unattributed.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const PAGE = "research-hardware.html";
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const compact = (s) => s.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- it exists and the site knows about it ---------------------------- */

test("hardware: the page builds, is indexable and is in the sitemap", () => {
  assert.doesNotMatch(read(PAGE), /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), new RegExp(`<loc>https://rogerai\\.fm/${PAGE}</loc>`));
});

test("hardware: it is reachable from the Research menu, not orphaned", () => {
  const nav = src("_partials/nav.html");
  const panel = nav.slice(nav.indexOf('id="navResearchPanel"'), nav.indexOf('id="navCompanyPanel"'));
  assert.match(panel, new RegExp(`href="/${PAGE}"`), "the Research panel links the hardware page");
});

test("hardware: it is registered for its own stylesheet", () => {
  assert.match(src("../build.mjs"), new RegExp(`"${PAGE}"`), "the page has a CSS bundle");
  assert.match(read(PAGE), /styles\/research-hardware\.css/, "and links it");
});

/* ---- THE HONESTY LOCK -------------------------------------------------- */

// Each board card declares the tier it is a target for and that tier's stage.
const cards = () =>
  [...read(PAGE).matchAll(/<article[^>]*class="[^"]*hw-card[^"]*"[^>]*>/g)].map((m) => ({
    tag: m[0],
    board: m[0].match(/data-board="([^"]+)"/)?.[1],
    tier: m[0].match(/data-tier="([^"]+)"/)?.[1],
    stage: m[0].match(/data-stage="([^"]+)"/)?.[1],
  }));

test("hardware: every board maps to a tier the model catalogue actually lists", () => {
  const all = cards();
  assert.ok(all.length >= 6, `the page carries board cards (found ${all.length})`);
  const catalogue = compact(src("research-models.html"));
  for (const c of all) {
    assert.ok(c.board && c.tier && c.stage, `a card declares board, tier and stage: ${c.tag}`);
    assert.ok(catalogue.includes(c.tier),
      `"${c.tier}" is not a tier research-models.html lists`);
  }
});

test("hardware: a board never shows a rosier status than the catalogue does", () => {
  // The stage string is copied from research-models.html, so the two pages cannot drift
  // into telling a reader different things about the same tier.
  const catalogue = compact(src("research-models.html")).replace(/&middot;/g, "·");
  // every data-stage on the page, not just the board cards: the experiments section states
  // gates too, and an unchecked stage there is the same lie in a different box.
  const stages = [...read(PAGE).matchAll(/data-stage="([^"]+)"/g)].map((m) => m[1]);
  assert.ok(stages.length >= 8, `stages are declared (found ${stages.length})`);
  for (const raw of stages) {
    const stage = raw.replace(/&middot;/g, "·");
    assert.ok(catalogue.includes(stage),
      `"${stage}" is shown on the page but appears nowhere in the model catalogue`);
  }
});

test("hardware: nothing is described as running on a board that has no artifact", () => {
  // research-models.html says in its own source that nothing there may read as though an
  // artifact existed. A photograph makes that failure much easier to commit, so the ban is
  // aimed where the hazard actually is: the words INSIDE a card, next to the picture.
  const page = read(PAGE);
  const blocks = [...page.matchAll(/<article[^>]*class="[^"]*hw-card[^"]*"[\s\S]*?<\/article>/g)].map((m) => m[0]);
  assert.ok(blocks.length >= 6, `the cards are findable as blocks (found ${blocks.length})`);
  const forbidden = [
    /\brunning (?:Roger Edge|Wave)\b/i,
    /\bruns (?:Roger Edge|Wave)\b/i,
    /\bdeployed\b/i,
    /\bpowered by (?:Roger Edge|Wave)\b/i,
    /\bin production\b/i,
  ];
  for (const b of blocks) {
    for (const re of forbidden) {
      const hit = b.match(re);
      assert.equal(hit, null, `a board card claims a deployment that does not exist: "${hit?.[0]}"`);
    }
  }
});

test("hardware: the page never claims a shipped deployment anywhere", () => {
  // These read as a shipped product wherever they appear, so they are banned page-wide
  // rather than card-wide - including any caption, lead or footnote.
  const page = compact(read(PAGE));
  for (const re of [/\bships with (?:Roger Edge|Wave)\b/i, /\bin production on\b/i,
                    /\balready running (?:Roger Edge|Wave)\b/i, /\bcustomers? (?:run|deploy)\b/i]) {
    const hit = page.match(re);
    assert.equal(hit, null, `the page implies a deployment that does not exist: "${hit?.[0]}"`);
  }
});

test("hardware: the page says plainly that the targets are targets", () => {
  const page = compact(read(PAGE)).toLowerCase();
  assert.match(page, /target|intended|not yet|no checkpoint|prototype/,
    "the page states that the mapping is intent rather than a shipped result");
});

test("hardware: the one downloadable artifact is the only thing shown as available now", () => {
  const page = read(PAGE);
  assert.match(page, /DeepSeek-V4-Flash-MTP-GGUF|DeepSeek-V4-Flash MTP/,
    "the one real artifact is named");
  assert.match(page, /huggingface\.co\/rogerai-fyi/, "and linked where it can be fetched");
});

/* ---- the photographs are other people's work --------------------------- */

const SHOTS = path.join(WEB, "src", "assets", "hardware");

test("hardware: every photograph carries its author, licence and source", () => {
  const page = read(PAGE);
  const files = readdirSync(SHOTS).filter((f) => f.endsWith(".webp"));
  assert.ok(files.length >= 6, `the folder holds the board photographs (found ${files.length})`);

  for (const f of files) {
    assert.ok(page.includes(`assets/hardware/${f}`), `${f} is used on the page (no orphan assets)`);
    // the credit for this shot: a Commons source link and a licence, in its own figure
    const fig = page.match(new RegExp(`<figure[^>]*>[\\s\\S]{0,1200}?assets/hardware/${f.replace(".", "\\.")}[\\s\\S]{0,1200}?</figure>`));
    assert.ok(fig, `${f} sits in a figure that can carry a credit`);
    assert.match(fig[0], /commons\.wikimedia\.org/, `${f} credits its source page`);
    assert.match(fig[0], /CC0|CC BY/, `${f} names its licence`);
  }
});

test("hardware: every photograph has real alt text and intrinsic dimensions", () => {
  for (const m of read(PAGE).matchAll(/<img[^>]*assets\/hardware\/[^>]*>/g)) {
    const alt = m[0].match(/alt="([^"]*)"/)?.[1] ?? "";
    assert.ok(alt.trim().length >= 15, `a board photo needs describing, got alt="${alt}"`);
    assert.match(m[0], /width="\d+"/, "width is declared so the grid does not jump");
    assert.match(m[0], /height="\d+"/, "height is declared so the grid does not jump");
    assert.match(m[0], /loading="lazy"/, "below-the-fold photographs load lazily");
  }
});

test("hardware: the photographs stay inside their weight budget", () => {
  for (const f of readdirSync(SHOTS).filter((f) => f.endsWith(".webp"))) {
    const kb = statSync(path.join(SHOTS, f)).size / 1024;
    assert.ok(kb < 130, `${f} is ${Math.round(kb)}KB; past ~130KB a board photo is not worth its bytes`);
  }
});

/* ---- the illustration -------------------------------------------------- */

test("hardware: the ladder diagram is described and does not move for a reader who asked it not to", () => {
  const fig = read(PAGE).match(/<svg[^>]*class="[^"]*hw-ladder[^"]*"[\s\S]*?<\/svg>/)?.[0];
  assert.ok(fig, "the size-ladder illustration is inline SVG");
  assert.match(fig, /role="img"/, "announced as an image");
  assert.match(fig, /<title[\s>]/, "with a name");
  const desc = fig.match(/<desc[^>]*>([\s\S]*?)<\/desc>/)?.[1] ?? "";
  assert.ok(compact(desc).length >= 120, "and prose a screen reader can use instead of the picture");
  assert.match(src("styles/research-hardware.css"), /prefers-reduced-motion/,
    "the animation yields to prefers-reduced-motion");
});

test("hardware: the band scrolls on a narrow screen instead of shrinking to nothing", () => {
  // The ladder is ~1180 user units wide. Scaled into a phone it renders its labels at about
  // four pixels - not a small illustration, an unreadable one. It scrolls instead, and only
  // the band scrolls: overflow on the <figure> would carry the caption off with it.
  const page = read(PAGE);
  const css = src("styles/research-hardware.css");
  assert.match(page, /<div class="hw-figure__scroll">[\s\S]*?<svg[^>]*hw-ladder/,
    "the band sits in its own scroll container");
  assert.match(css, /\.hw-figure__scroll\s*\{[^}]*overflow-x:\s*auto/,
    "that container scrolls horizontally");
  assert.match(css, /\.hw-ladder\s*\{[^}]*min-width:\s*\d+px/,
    "and the band keeps a legible minimum width rather than scaling down");
  assert.doesNotMatch(css, /\.hw-figure\s*\{[^}]*overflow-x/,
    "the figure itself must not scroll, or the caption leaves with the band");
});

/* ---- the credits list, and a picture that is not what it says ---------- */

test("hardware: the page indicates that the photographs were altered", () => {
  // The credits SECTION is gone - every picture carries its own author, licence and source.
  // What a list is still needed for is the CC BY requirement to say changes were made, and
  // the Blackwell/Ada substitution. Both live in one notice under the grid.
  const page = compact(read(PAGE));
  assert.match(page, /resized, cropped and colour-graded/i,
    "the page says the photographs were altered, as CC BY asks");
  assert.match(page, /Blackwell has no freely licensed photograph/i,
    "and admits which cards are illustrated with a different generation");
});

test("hardware: a photograph of a different generation says so on the picture", () => {
  // Two cards name the RTX PRO 6000 Blackwell, which has no freely licensed photograph
  // yet, and are illustrated with the previous Ada generation. That gap is exactly the
  // kind a marketing page closes silently, so the picture has to admit it.
  const page = read(PAGE);
  for (const m of page.matchAll(/<article[^>]*class="[^"]*hw-card[^"]*"[\s\S]*?<\/article>/g)) {
    const card = m[0];
    if (!/Blackwell/.test(card)) continue;
    const fig = card.match(/<figure[\s\S]*?<\/figure>/)?.[0] ?? "";
    assert.match(fig, /Ada pictured/,
      "a card naming Blackwell must say on the picture that an Ada-generation card is shown");
  }
});

test("hardware: the bench figures are the ones the broadcast actually published", () => {
  // This is the only MEASURED result on the page, so it is the one most worth pinning: the
  // rig, the model and both throughput numbers have to be findable in the write-up that
  // produced them. A figure that drifts from its own source is worse than no figure.
  const card = read(PAGE).match(/<article[^>]*data-exp="concurrency"[\s\S]*?<\/article>/)?.[0];
  assert.ok(card, "the concurrency run has a card");
  const attr = (n) => card.match(new RegExp(`data-${n}="([^"]+)"`))?.[1];
  const source = compact(read("broadcasts-one-gpu-many-users.html")).replace(/&times;/g, "x");
  const shown = compact(card); // what a reader sees, with every attribute stripped away

  const rig = attr("rig"), model = attr("model");
  assert.ok(rig && model, "the card names the rig and the model it was measured on");
  assert.ok(source.replace(/\s/g, "").includes(rig.replace(/\s/g, "")),
    `the bench rig "${rig}" is not the one the broadcast reports`);
  assert.ok(source.includes(model), `the model "${model}" is not the one the broadcast reports`);

  for (const k of ["single", "batched", "users"]) {
    const v = attr(k);
    assert.ok(v && /^\d+$/.test(v), `${k} is a number`);
    assert.ok(source.includes(v), `the figure ${v} (${k}) appears nowhere in the broadcast`);
    // against the VISIBLE text, not the markup: card.includes(v) is satisfied by the
    // data-* attribute the figure came from, so it would pass with the cell blanked out.
    assert.ok(shown.includes(v), `the figure ${v} (${k}) is declared but never shown to a reader`);
  }
  assert.ok(Number(attr("batched")) > Number(attr("single")),
    "the whole point is that aggregate throughput rises under load");
});
