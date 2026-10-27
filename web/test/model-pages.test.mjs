// The Wave model pages (Wave Pico, Wave Nano) and the data file they render from.
//
// web/src/data/model-cards.json is the ONE place a model's public facts live: every measured
// number carries its R-number (the lab's results log) and the date it was measured, and a page shows a
// number only through a {{card:...}} token (scripts/model-cards.mjs), so a number appears in
// exactly one place. Adding a model page later (Giga) is: a model entry in the data file, then a
// page built from the tokens; the "every model has a page" checks below then hold it to the
// same rules.
//
// What these tests hold, beyond the data:
//   - no number on a model page that is not in the data file
//   - the outward-copy rules: no Hugging Face, no first/novel/state of the art, no founder names,
//     no hosts, ports, paths or service names, no dataset names or corpus sizes, no place names
//   - fire numbers are simulator-internal every time, in the same block as the number
//   - pre-wet is an owner-armed rule the controller executes; the model never actuates
//   - what is served matches web/src/data/wave-status.json, and nothing more
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { resolveCardTokens, loadModelCards } from "../scripts/model-cards.mjs";
import { resolveWaveTokens, loadWaveStatus } from "../scripts/wave-status.mjs";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const DATA = path.join(SRC, "data", "model-cards.json");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const cards = () => JSON.parse(readFileSync(DATA, "utf8"));
const status = () => JSON.parse(readFileSync(path.join(SRC, "data", "wave-status.json"), "utf8"));
const dist = (p) => readFileSync(path.join(DIST, p), "utf8");
const src = (p) => readFileSync(path.join(SRC, p), "utf8");
const noComments = (s) => s.replace(/<!--[\s\S]*?-->/g, "");
const decode = (s) => s.replace(/&nbsp;/g, " ").replace(/&rsquo;|&lsquo;/g, "'").replace(/&ldquo;|&rdquo;/g, '"')
  .replace(/&ndash;/g, "\u2013").replace(/&middot;/g, "\u00b7").replace(/&plusmn;/g, "\u00b1")
  .replace(/&rarr;/g, "\u2192").replace(/&sect;/g, "\u00a7").replace(/&times;/g, "\u00d7").replace(/&amp;/g, "&");
const visible = (s) => decode(noComments(s).replace(/<script[\s\S]*?<\/script>/g, " ")
  .replace(/<style[\s\S]*?<\/style>/g, " ").replace(/<[^>]+>/g, " ")).replace(/\s+/g, " ").trim();
const main = (p) => noComments(dist(p)).match(/<main[\s\S]*<\/main>/)?.[0] || "";
const sectionHtml = (p, id) => main(p).match(new RegExp(`<section[^>]*id="${id}"[\\s\\S]*?</section>`))?.[0] || "";
const sentences = (s) => s.split(/(?<=[.;:!?])\s+/);

const PAGES = { pico: "research-wave-pico.html", nano: "research-wave-nano.html" };

/* ---- 1. the data file ------------------------------------------------------------- */

test("cards: every model names its page, its status tier and its public name", () => {
  const d = cards();
  assert.deepEqual(d.models.map((m) => m.id), ["pico", "nano"], "Pico and Nano have pages; Giga waits for the lab");
  for (const m of d.models) {
    assert.equal(m.page, PAGES[m.id], `${m.id} page slug`);
    assert.ok(status().tiers.some((t) => t.id === m.statusTier), `${m.id} statusTier is a wave-status tier`);
    for (const k of ["name", "size", "author"]) assert.equal(typeof m[k], "string", `${m.id}.${k}`);
    assert.equal(m.author, "RogerAI Labs");
  }
});

test("cards: every measured item has a label, a value, an R-number and an ISO date", () => {
  for (const m of cards().models) {
    assert.ok(m.measured.length >= 8, `${m.id} has a real measured panel`);
    const ids = new Set();
    for (const it of m.measured) {
      assert.ok(!ids.has(it.id), `${m.id}.${it.id} is unique`); ids.add(it.id);
      assert.match(it.id, /^[a-z0-9-]+$/, `${m.id}.${it.id} is a token-safe id`);
      assert.equal(typeof it.label, "string", `${m.id}.${it.id} label`);
      assert.equal(typeof it.value, "string", `${m.id}.${it.id} value`);
      assert.match(it.ref, /^R\.F?\d+$/, `${m.id}.${it.id} carries an R-number`);
      assert.match(it.date, /^2026-\d\d-\d\d$/, `${m.id}.${it.id} carries the date it was measured`);
      if (it.lo !== undefined || it.hi !== undefined) {
        assert.ok(typeof it.lo === "number" && typeof it.hi === "number" && it.lo <= it.hi, `${m.id}.${it.id} range`);
      }
      if (it.num !== undefined) assert.equal(typeof it.num, "number", `${m.id}.${it.id} num`);
    }
  }
});

test("cards: one R-number, one date, wherever it is cited", () => {
  const seen = new Map();
  for (const m of cards().models) for (const it of m.measured) {
    if (seen.has(it.ref)) assert.equal(it.date, seen.get(it.ref), `${it.ref} is dated once`);
    seen.set(it.ref, it.date);
  }
});

test("cards: the data file is build input only and never ships", () => {
  assert.ok(!existsSync(path.join(DIST, "data", "model-cards.json")));
});

test("cards: every model has a page that exists and is linked where model content lives", () => {
  const nav = noComments(dist("index.html")).match(/<div class="nav__panel" id="navResearchPanel"[\s\S]*?<\/div>/)?.[0] || "";
  for (const m of cards().models) {
    assert.ok(existsSync(path.join(SRC, m.page)), `${m.page} exists`);
    assert.match(nav, new RegExp(`href="/${m.page}"`), `${m.page} is in the Research menu`);
    assert.match(main("research-models.html"), new RegExp(`href="/${m.page}"`), `Models and status links ${m.page}`);
    assert.match(main("research-wave-family.html"), new RegExp(`href="/${m.page}"`), `the Wave family page links ${m.page}`);
  }
  // In the Research menu the model pages sit right after Models and status.
  const order = [...nav.matchAll(/href="([^"]+)"/g)].map((x) => x[1]);
  const i = order.indexOf("/research-models.html");
  assert.deepEqual(order.slice(i + 1, i + 3), ["/research-wave-pico.html", "/research-wave-nano.html"]);
});

/* ---- 2. the resolver -------------------------------------------------------------- */

const FIXTURE = {
  models: [{
    id: "m", name: "Wave M", size: "1M",
    measured: [
      { id: "a", label: "Alpha", value: "72.2", num: 72.2, ref: "R.1", date: "2026-10-18" },
      { id: "b", label: "Beta", value: "0.930 \u00b1 0.009", lo: 0.921, hi: 0.939, ref: "R.F2", date: "2026-10-20" },
      { id: "c", label: "Gamma", value: "99.4", ref: "R.1", date: "2026-10-18", fire: true },
    ],
    limits: [{ text: "One limit." }, { text: "Two.", ref: "R.1" }],
  }],
};

test("resolver: a card token renders the value, a num token the bare number", () => {
  assert.equal(resolveCardTokens("<b>{{card:m.a}}</b>", "x.html", FIXTURE), "<b>72.2</b>");
  assert.equal(resolveCardTokens('style="--v:{{card-num:m.a}}"', "x.html", FIXTURE), 'style="--v:72.2"');
  assert.equal(resolveCardTokens("{{card-lo:m.b}}-{{card-hi:m.b}}", "x.html", FIXTURE), "0.921-0.939");
  assert.equal(resolveCardTokens("{{card:m.name}} {{card:m.size}}", "x.html", FIXTURE), "Wave M 1M");
});

test("resolver: a fire number is marked, so the page can be held to simulator-internal", () => {
  assert.equal(resolveCardTokens("{{card:m.c}}", "x.html", FIXTURE), '<span data-fire="">99.4</span>');
});

test("resolver: a ref token names the R-number and the date", () => {
  assert.equal(resolveCardTokens("{{card-ref:m.b}}", "x.html", FIXTURE), "R.F2, 2026-10-20");
});

test("resolver: the notes token lists each R-number once, with its date and what it measured", () => {
  const out = resolveCardTokens("{{card-notes:m}}", "x.html", FIXTURE);
  assert.equal((out.match(/R\.1\b/g) || []).length, 1, "R.1 listed once");
  assert.match(out, /<li[^>]*><b>R\.1<\/b> 2026-10-18: Alpha, Gamma\.<\/li>/);
  assert.match(out, /<li[^>]*><b>R\.F2<\/b> 2026-10-20: Beta\.<\/li>/);
});

test("resolver: a list token renders one item per entry, with its R-number when it has one", () => {
  const out = resolveCardTokens("<ul>{{card-list:m.limits}}</ul>", "x.html", FIXTURE);
  assert.equal(out, '<ul><li>One limit.</li><li>Two. <span class="wm-ref">R.1</span></li></ul>');
});

test("resolver: an unknown model or item, or a num token on an item without a number, fails the build", () => {
  assert.throws(() => resolveCardTokens("{{card:z.a}}", "x.html", FIXTURE), /unknown model/);
  assert.throws(() => resolveCardTokens("{{card:m.zz}}", "x.html", FIXTURE), /no item/);
  assert.throws(() => resolveCardTokens("{{card-num:m.b}}", "x.html", FIXTURE), /no number/);
  assert.throws(() => resolveCardTokens("{{card-list:m.zz}}", "x.html", FIXTURE), /no list/);
  assert.throws(() => resolveCardTokens("{{card:m.}}", "x.html", FIXTURE), /unresolved card token/);
  assert.throws(() => resolveCardTokens("{{ card:m.a }}", "x.html", FIXTURE), /unresolved card token/);
});

test("resolver: loadModelCards reads the real data file", () => {
  assert.deepEqual(loadModelCards().models.map((m) => m.id), ["pico", "nano"]);
});

test("wave tokens: the band token renders the on-air band, and refuses a tier with none", () => {
  const d = loadWaveStatus();
  const pico = d.tiers.find((t) => t.id === "pico");
  assert.equal(resolveWaveTokens("{{wave-band:pico}}", "x.html", d), pico.onAirBand);
  const off = d.tiers.find((t) => t.onAirBand === null);
  assert.throws(() => resolveWaveTokens(`{{wave-band:${off.id}}}`, "x.html", d), /not on air/);
  assert.equal(resolveWaveTokens("{{wave-air:pico}}", "x.html", d), pico.onAirBand ? "on" : "off");
  assert.equal(resolveWaveTokens(`{{wave-air:${off.id}}}`, "x.html", d), "off");
  assert.throws(() => resolveWaveTokens("{{wave-air:nope}}", "x.html", d), /unknown wave tier/);
});

/* ---- 3. the pages ----------------------------------------------------------------- */

test("pages: both model pages build as research pages with their own title, description and canonical", () => {
  for (const [id, p] of Object.entries(PAGES)) {
    const html = dist(p);
    assert.match(html, /<title>[^<]*Wave (Pico|Nano)/, `${p} title`);
    assert.match(html, /<meta name="description" content="[^"]{60,}"/, `${p} description`);
    assert.match(html, new RegExp(`rel="canonical" href="https://rogerai\\.fm/${p}"`));
    assert.match(main(p), /<h1>/);
    assert.match(main(p), /class="toc-tuner"/, `${p} has the contents tuner`);
    assert.ok(dist("sitemap.xml").includes(`https://rogerai.fm/${p}`), `${p} is in the sitemap`);
    assert.ok(dist("llms.txt").includes(`https://rogerai.fm/${p}`), `${p} is in llms.txt`);
    assert.match(visible(main(p)), /RogerAI Labs/, `${id}: the author is RogerAI Labs`);
  }
});

test("pages: the mesh explainer and the wildfire watch are shared partials, written once", () => {
  for (const p of Object.values(PAGES)) {
    assert.match(src(p), /<!-- include: wave-mesh\.html/, `${p} includes the mesh explainer`);
    assert.match(src(p), /<!-- include: wave-firewatch\.html/, `${p} includes the wildfire watch`);
    assert.doesNotMatch(noComments(src(p)), /T13/, `${p} does not carry its own copy of the mesh questions`);
    assert.match(sectionHtml(p, "mesh"), /T13/, `${p} renders the mesh questions`);
  }
});

test("pages: every number a reader sees on a model page is in the data file", () => {
  const json = readFileSync(DATA, "utf8");
  for (const p of Object.values(PAGES)) {
    const text = visible(main(p)).replace(/\u00a7\s?\d+/g, "").replace(/FIG\.\s?\d+/g, "");
    const missing = [...new Set(text.match(/\d+(?:[.,]\d+)*/g) || [])].filter((n) => !json.includes(n));
    assert.deepEqual(missing, [], `${p}: numbers not in model-cards.json: ${missing.join(", ")}`);
  }
});

test("pages: the measured footnote lists every R-number the page cites, with its date", () => {
  for (const m of cards().models) {
    const notes = main(m.page).match(/<ol class="wm-notes"[\s\S]*?<\/ol>/)?.[0] || "";
    assert.ok(notes, `${m.page} has the measured footnote`);
    for (const it of m.measured) {
      assert.match(notes, new RegExp(`<b>${it.ref.replace(".", "\\.")}</b> ${it.date}`), `${m.page}: ${it.ref}`);
    }
  }
});

const BANNED = [
  [/hugging\s?face/i, "Hugging Face"],
  [/\b(first|novel|unprecedented)\b|state[- ]of[- ]the[- ]art/i, "first / novel / state of the art"],
  [/Gulnaz|Galieva|Miguel|Quijada|\bLuis\b/i, "a founder's name"],
  [/:\d{4}\b|localhost|127\.0\.0\.1|systemd|\.service\b|\/mnt\/|~\/|hermes|hf_serve|serve-wave|workstation|computer-scientist|RogGentoo/i, "a host, port, path or service name"],
  [/FIgLib|HPWREN|pyro-?sdis|PyroNear|SKAB|\bNAB\b|ALERTCalifornia|SigLIP|Idefics|SmolLM/i, "a dataset or component name"],
  [/\bcorpus\b|billion tokens|\b90B\b|\btokens of\b|dataset/i, "a data source or training detail"],
  [/San Clemente/i, "the live camera stream's location"],
  [/\bClaude\b|AI assistant/i, "an assistant credit"],
  [/revenue/i, "a revenue claim"],
];

test("pages: no outward-copy rule is broken on a model page", () => {
  for (const p of Object.values(PAGES)) {
    const head = noComments(dist(p)).match(/<title>[\s\S]*?<\/title>|<meta name="description"[^>]*>|<meta property="og:[^>]*>/g).join(" ");
    const text = visible(main(p)) + " " + head;
    for (const [re, what] of BANNED) assert.doesNotMatch(text, re, `${p}: ${what}`);
    assert.doesNotMatch(main(p), /huggingface\.co/i, `${p}: no Hugging Face link`);
  }
});

// The innermost block (p, li, figcaption, td, dd) around a position in the html.
function blockAt(html, at) {
  let best = null;
  for (const tag of ["p", "li", "figcaption", "td", "dd"]) {
    const open = html.lastIndexOf(`<${tag}`, at);
    if (open < 0) continue;
    const close = html.indexOf(`</${tag}>`, at);
    if (close < 0) continue;
    if (!best || open > best.open) best = { open, close: close + tag.length + 3 };
  }
  return best ? html.slice(best.open, best.close) : "";
}

test("fire: every fire number sits in a block that says simulator-internal", () => {
  for (const p of Object.values(PAGES)) {
    const html = main(p);
    const at = [...html.matchAll(/data-fire=""/g)].map((m) => m.index);
    assert.ok(at.length >= 2, `${p} shows its fire adapter numbers`);
    for (const i of at) assert.match(visible(blockAt(html, i)), /simulator-internal/i, `${p}: a fire number without simulator-internal`);
  }
  for (const m of cards().models) {
    for (const it of m.measured.filter((x) => /fire|T17/i.test(x.label))) assert.equal(it.fire, true, `${m.id}.${it.id} is marked fire`);
  }
});

test("fire: pre-wet is an owner-armed rule the controller runs; the model never actuates", () => {
  for (const p of Object.values(PAGES)) {
    const fire = visible(sectionHtml(p, "wildfire"));
    assert.ok(fire.length > 400, `${p} has the wildfire watch section`);
    assert.match(fire, /owner-armed/i);
    assert.match(fire, /never actuates/i);
    assert.match(fire, /warning and mitigation, never fire protection/i);
    for (const s of sentences(fire).filter((x) => /pre-wet/i.test(x))) {
      assert.match(s, /owner|eligible|controller/i, `pre-wet without its owner or its eligibility: "${s}"`);
    }
    const all = visible(main(p));
    for (const m of all.matchAll(/\b(opens?|triggers?|activates?|controls?|actuates?|starts?|runs?)\s+(the\s+|a\s+|any\s+)?(valves?|sprinklers?|zones?|pre-wet)/gi)) {
      const before = all.slice(Math.max(0, m.index - 60), m.index);
      assert.match(before, /\b(never|controller|owner)\b[^.]*$/i, `${p}: something other than the controller or the owner acts: "${m[0]}"`);
    }
    assert.doesNotMatch(all, /\b(model|Pico|Nano|node)\s+(pre-wets|opens|actuates|triggers)/i);
  }
});

test("fire: design tense, no site, no place, no company, no protection claim", () => {
  for (const p of Object.values(PAGES)) {
    const fire = visible(sectionHtml(p, "wildfire"));
    assert.doesNotMatch(fire, /\b(installed|deployed|running|in service|in operation)\b/i, `${p}: the watch is a design`);
    assert.doesNotMatch(fire, /Orange County|California|FireDefense|\bdefense\b/i, `${p}: no place and no company in the fire copy`);
    assert.doesNotMatch(fire, /surviv|saves?|insur|evacuat|listed|certif|code[- ]compliant/i, `${p}: no outcome or listing claim`);
    assert.match(fire, /camera/i, "the camera detector is a component of the design");
    // (its own section label and figure label are furniture, not numbers: the page-wide
    // number test strips them the same way)
    assert.doesNotMatch(fire.replace(/simulator-internal/gi, "").replace(/\u00a7\s?\d+|FIG\.\s?\d+/g, ""), /\d/,
      `${p}: the design section carries no numbers`);
  }
});

test("served: what a page says is served is exactly what the status file says", () => {
  const tier = (id) => status().tiers.find((t) => t.id === id);
  const pico = tier("pico"), nano = tier("nano");
  const picoText = visible(main(PAGES.pico));
  assert.ok(pico.onAirBand, "Pico is on air in the status file");
  assert.match(picoText, new RegExp(`roger use ${pico.onAirBand}`), "Pico's run command names its band");
  const nanoText = visible(main(PAGES.nano));
  if (nano.onAirBand) {
    assert.match(nanoText, new RegExp(`roger use ${nano.onAirBand}`));
  } else {
    assert.match(nanoText, /not on the band yet/i, "Nano says it is not on the band");
    assert.doesNotMatch(nanoText, /roger use wave-nano/i, "and offers no command for a band that is not on air");
  }
  // No version number is claimed as served.
  for (const p of Object.values(PAGES)) assert.doesNotMatch(visible(main(p)), /\bv\d+[a-z]?\b/, `${p}: no build version named`);
});

test("nano: the vision module is a section of the Nano page, single frames, with R.F65", () => {
  const vl = visible(sectionHtml(PAGES.nano, "vision"));
  assert.match(vl, /Nano-VL/);
  assert.match(vl, /single frames/i);
  assert.match(vl, /persistence/i);
  assert.match(vl, /per-camera baseline/i);
  assert.match(vl, /R\.F65/);
  assert.doesNotMatch(vl, /\b(installed|deployed|running)\b/i);
});

test("nano: the conversation skill is on the Nano page", () => {
  assert.match(visible(sectionHtml(PAGES.nano, "conversation")), /### User:/);
});

test("figures: the escalation gate, the mesh, the readouts, the ladder and the runs-on row", () => {
  for (const p of Object.values(PAGES)) {
    const html = main(p);
    assert.match(html, /class="[^"]*\bwm-gate\b/, `${p} draws the escalation gate`);
    assert.match(html, /class="[^"]*\bwm-mesh\b/, `${p} draws the mesh`);
    assert.match(html, /class="wm-readouts"/, `${p} draws the measured readouts`);
    assert.match(html, /<ol class="steps wm-ladder"/, `${p} draws the ladder on the shared step path`);
    assert.match(html, /class="wm-runs"/, `${p} draws the runs-on row`);
    const ladder = visible(html.match(/<ol class="steps wm-ladder"[\s\S]*?<\/ol>/)[0]);
    const order = ["Notify", "Escalate", "Human-ack pending", "Pre-wet eligible", "Handed to a person"];
    let cur = -1;
    for (const step of order) { const i = ladder.indexOf(step); assert.ok(i > cur, `${p}: ladder step ${step} in order`); cur = i; }
    assert.match(ladder, /owner-armed/i);
    const gate = visible(html.match(/<figure[^>]*\bwm-gate\b[\s\S]*?<\/figure>/)[0]);
    assert.match(gate, /escalate/i);
    assert.match(gate, /R\.\d+/, "the gate figure labels its numbers with their R-number");
  }
});

test("models and status: the two model pages are one click from the hero, and no weights link survives", () => {
  const html = main("research-models.html");
  const hero = html.match(/<section class="research-hero"[\s\S]*?<\/section>/)[0];
  assert.match(hero, /href="\/research-wave-pico\.html"/);
  assert.match(hero, /href="\/research-wave-nano\.html"/);
  assert.doesNotMatch(hero, /huggingface/i, "the hero no longer points at a weights page (not on Hugging Face)");
  // The status lamp reads the status file: lit only for a tier that is on air.
  for (const t of status().tiers.filter((x) => x.kind === "tier")) {
    const row = [...html.matchAll(/<article class="model-row"[^>]*>([\s\S]*?)<\/article>/g)].map((m) => m[1])
      .find((r) => new RegExp(`<h3>${t.name}</h3>`).test(r));
    assert.ok(row, `${t.name} row`);
    assert.match(row, new RegExp(`class="model-lamp" data-air="${t.onAirBand ? "on" : "off"}"`), `${t.name} lamp`);
  }
});
