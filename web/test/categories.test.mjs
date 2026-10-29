// Executable lock for features/categories/web_tuner.feature, the @web scenario of
// features/categories/web_sharing_view.feature, and features/categories/docs.feature.
//
// The tuner's rules live in ONE module (js/taxonomy.js, window.RogerTaxonomy), so they are
// RUN here in a node:vm sandbox rather than matched as source text; the page wiring and the
// manual are checked against the built dist.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");

let RT;
before(() => {
  execFileSync("node", ["build.mjs"], { cwd: WEB });
  const win = {};
  vm.runInNewContext(read("js/taxonomy.js"), { window: win });
  RT = win.RogerTaxonomy;
});

// "vision/ocr:declared,vision/ocr:inferred" -> /discover-shaped station taxonomies
const stations = (pairs) =>
  pairs ? pairs.split(",").map((p) => {
    const [pair, src, flag] = p.split(":");
    const [category, subcategory = ""] = pair.split("/");
    return { category, subcategory, taxonomy_source: src, curated: flag === "curated", online: flag !== "offline" };
  }) : [];

const band = (label, tags, grades) => {
  const [cat, sub = ""] = label.split("/");
  return {
    cat, sub, declared: 1,
    tags: tags ? tags.split(",") : [],
    policies: grades ? grades.split(",") : [],
  };
};

// ---------------------------------------------------------------- labels
test("a band's label names its category and marks a guess", () => {
  for (const [pairs, want] of [
    ["vision/ocr:declared,vision/ocr:inferred", "vision/ocr"],
    ["retrieval/embeddings:inferred", "retrieval/embeddings~"],
    ["vision/vlm:declared,vision/vlm:declared,vision/ocr:declared", "vision/vlm"],
    ["vision/vlm:declared,language/chat:declared", "language/chat"],
    ["vision:declared", "vision"],
    ["", ""],
    // the /market rule: online human stations vote, curated only as a fallback, offline last
    ["vision/ocr:declared,language/chat:declared:curated,language/chat:declared:curated", "vision/ocr"],
    ["language/code:declared:curated", "language/code"],
    ["vision/ocr:declared,vision/vlm:declared:offline,vision/vlm:declared:offline", "vision/ocr"],
    ["vision/ocr:declared:offline", "vision/ocr"],
  ]) {
    assert.equal(RT.label(RT.fold(stations(pairs))), want, pairs);
  }
});

test("a /market-only band, whose source is unknown, is marked as a guess", () => {
  const t = RT.fromMarketRow({ category: "vision", subcategory: "ocr", tags: ["ppe"], content_policies: ["aligned"] });
  assert.equal(RT.label(t), "vision/ocr~");
  assert.equal(RT.label(RT.fromMarketRow({})), "");
});

// ---------------------------------------------------------------- filter + search
test("the filter matches a band by category and subcategory", () => {
  const b = band("vision/ocr", "manufacturing,ppe", "aligned");
  for (const [category, sub, shown] of [
    ["vision", "", true], ["language", "", false], ["vision", "ocr", true], ["vision", "vlm", false], ["", "", true],
  ]) {
    assert.equal(RT.matches(b, { category, sub }), shown, `${category}/${sub}`);
  }
});

test("search matches the model name, the category and the tags", () => {
  const b = band("vision/ocr", "manufacturing,ppe", "");
  for (const [q, shown] of [["qwen", true], ["OCR", true], ["manufact", true], ["ppe", true], ["embeddings", false]]) {
    assert.equal(RT.search("qwen3-vl-8b", b, q), shown, q);
  }
  assert.equal(RT.search("qwen3-vl-8b", b, ""), true, "an empty query matches everything");
});

// ---------------------------------------------------------------- grade badge
test("a declared grade other than aligned is badged", () => {
  for (const [grades, want] of [
    ["uncensored", "uncensored"], ["reduced-refusal", "reduced-refusal"],
    ["aligned,uncensored", "uncensored"], ["aligned", ""], ["", ""],
  ]) {
    assert.equal(RT.grade(band("language/chat", "", grades)), want, grades);
  }
});

// ---------------------------------------------------------------- options + deep links
const CATS = {
  version: 1,
  categories: ["language", "vision", "audio", "retrieval", "generation", "signals"].map((name) => ({
    name, providers: name === "vision" ? 3 : 0, curated_providers: 0,
    subcategories: name === "vision" ? [{ name: "ocr" }, { name: "vlm" }] : [{ name: "x" }],
  })),
};

test("category options come from /categories in canonical order, empty aisles counted", () => {
  const texts = RT.options(CATS).map((o) => o.text).join(", ");
  assert.equal(texts, "All categories, language (0), vision (3), audio (0), retrieval (0), generation (0), signals (0)");
  assert.equal(RT.options(CATS)[0].value, "");
});

test("a deep link preselects the filter, and only to real values", () => {
  for (const [q, cat, sub] of [
    ["?category=vision", "vision", ""],
    ["?category=Vision&sub=OCR", "vision", "ocr"],
    ["?category=medical", "", ""],
    ["?category=vision&sub=code", "vision", ""],
    ["?utm=%&category=vision", "vision", ""], // a malformed unrelated pair is skipped, not fatal
    ["", "", ""],
  ]) {
    const f = RT.fromQuery(q, CATS);
    assert.deepEqual([f.category, f.sub], [cat, sub], q);
  }
});

test("subcategory options belong to the chosen category", () => {
  // Array.from: the module runs in a vm realm, so compare values, not array prototypes.
  assert.deepEqual(Array.from(RT.subOptions(CATS, "vision"), (o) => o.value), ["", "ocr", "vlm"]);
  assert.deepEqual(Array.from(RT.subOptions(CATS, ""), (o) => o.value), [""]);
});

test("taxonomy text is escaped when rendered", () => {
  const html = RT.tagsHTML(band("vision/ocr", "<img src=x onerror=alert(1)>", ""));
  assert.doesNotMatch(html, /<img/i, "a station's tag never becomes markup");
  assert.match(html, /&lt;img/, "it is shown as text");
});

// ---------------------------------------------------------------- page wiring
test("the models page loads the rules and offers the category filter", () => {
  const html = read("models.html");
  const t = html.indexOf('<script src="js/taxonomy.js'), b = html.indexOf('<script src="js/bands.js');
  assert.ok(t > 0 && b > 0 && t < b, "taxonomy.js loads before bands.js");
  assert.match(html, /<span class="tuner__k">CATEGORY<\/span>\s*<select id="bandCat"/, "a Category select");
  const js = read("js/bands.js");
  assert.match(js, /BROKER \+ "\/categories"/, "bands.js reads /categories");
  assert.match(js, /RT\.fromMarketRow\(m\)/, "the /market row maps its category fields");
  assert.match(js, /taxonomy_source: o\.taxonomy_source/, "each /discover station keeps its category fields");
});

test("the band detail carries the moderation note for a graded band", () => {
  assert.match(read("js/bands.js"), /RogerAI still screens every request/);
});

// ---------------------------------------------------------------- web_sharing_view (@web)
// The console moved to the app (web/app/lib/console): its category surface is tested as
// components in test/unit/categories.test.js.

// ---------------------------------------------------------------- docs.feature
test("the TUNE IN key table lists G (category) and U (curated)", () => {
  const m = read("manual.html");
  assert.match(m, /<tr><td><kbd>G<\/kbd><\/td><td>cycle the <b>category<\/b> filter/);
  assert.match(m, /<tr><td><kbd>U<\/kbd><\/td><td>[^<]*curated/);
});

test("the SHARE section documents declaring a category", () => {
  const m = read("manual.html");
  for (const flag of ["--category", "--tag", "--tuning", "--policy", "--license", "--base-model"]) {
    assert.ok(m.includes(flag), `the manual documents ${flag}`);
  }
  assert.match(m, /<kbd>g<\/kbd>[^<]*<\/td><td>[^<]*category/i, "the SHARE tab g editor");
  assert.match(m, /guessed[^.]*<code>~<\/code>|<code>~<\/code>[^.]*guess/, "declared versus guessed (~)");
});

test("the manual lists the categories, links the live list, and is honest", () => {
  const m = read("manual.html");
  assert.match(m, /language, vision, audio, retrieval, generation(,)? and signals/);
  assert.match(m, /\/categories/);
  assert.match(m, /not verified/);
  assert.match(m, /never turns screening off/);
  for (const f of ["category", "subcategory", "tag", "policy"]) {
    assert.match(m, new RegExp(`<code>${f}=`), `the ${f} filter is documented`);
  }
});

// ---------------------------------------------------------------- web_card_editor.feature
// The card editor is page wiring over POST /provider/meta (whose behavior the broker suite,
// owner_override_bdd_test.go, runs for real). It lives in the app now; test/unit/categories.test.js
// runs the editor against a fake broker instead of regexing the built script.
