// ONE SOURCE OF TRUTH FOR WAVE STATUS, and a strict ledger of where the pages disagree.
//
// web/src/data/wave-status.json holds, per Wave tier (and the two related lines, Roger Edge
// and Wave Infinite), the canonical stage and every status label a page shows today. Wired
// labels are rendered from it at build time by a `{{wave:<tier>.<label>}}` token
// (scripts/wave-status.mjs); the rest are still hand-typed and this file holds them to the
// data instead. Nothing visible changed when this landed: the labels were copied verbatim.
//
// The CONFLICTS LEDGER below is deliberately strict. Every label text is classified into the
// fixed stage vocabulary HERE, independently of the data file, so the data cannot paper over
// a disagreement. The set of tiers whose labels imply more than one stage must equal exactly
// the set marked `"ruling": "pending"`. New drift fails; when the founder rules and the
// labels are unified, the ledger has to shrink with it.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { resolveWaveTokens, loadWaveStatus } from "../scripts/wave-status.mjs";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const DATA = path.join(SRC, "data", "wave-status.json");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const data = () => JSON.parse(readFileSync(DATA, "utf8"));
const norm = (s) => s.replace(/\s+/g, " ").trim();
const LDJSON_RE = /<script type="application\/ld\+json">[\s\S]*?<\/script>/g;
// Where a label is read: html pages from the BUILT tree (what a visitor gets), js from src
// (it ships as-is). JSON-LD labels are read only inside JSON-LD, every other label only
// outside it, so one copy cannot stand in for the other.
function region(page, labelId) {
  const raw = readFileSync(path.join(page.endsWith(".html") ? DIST : SRC, page), "utf8");
  if (labelId.endsWith(".jsonld")) return norm((raw.match(LDJSON_RE) || []).join("\n"));
  return norm(raw.replace(LDJSON_RE, ""));
}
const allLabels = () => data().tiers.flatMap((t) =>
  Object.entries(t.labels).map(([id, l]) => ({ tier: t, id, ...l })));
const tierById = (id) => data().tiers.find((t) => t.id === id);

const STAGES = ["planned", "base-selected", "training", "gated", "on-air", "released"];
const LADDER = ["pico", "nano", "micro", "giga", "tera", "peta", "exa"];

/* ---- 1. the data file is valid ------------------------------------------ */

test("wave-status: the stage vocabulary is exactly the fixed six, each defined in one line", () => {
  const d = data();
  assert.deepEqual(Object.keys(d.stages), STAGES);
  for (const [k, v] of Object.entries(d.stages)) {
    assert.equal(typeof v, "string", `${k} has a definition`);
    assert.ok(v.length > 10 && !v.includes("\n"), `${k} is a one-line definition`);
  }
});

test("wave-status: every ladder tier is present, in ladder order, plus the two related lines", () => {
  const d = data();
  assert.deepEqual(d.tiers.filter((t) => t.kind === "tier").map((t) => t.id), LADDER);
  assert.deepEqual(d.tiers.filter((t) => t.kind === "line").map((t) => t.id).sort(), ["edge", "infinite"]);
  assert.equal(new Set(d.tiers.map((t) => t.id)).size, d.tiers.length, "tier ids are unique");
});

test("wave-status: every tier carries the required fields with the right types", () => {
  for (const t of data().tiers) {
    const at = `tier ${t.id}`;
    assert.match(t.slot, /^[a-z-]+$/, `${at}: slot id`);
    assert.equal(typeof t.name, "string", `${at}: display name`);
    if (t.kind === "tier") assert.equal(typeof t.size, "string", `${at}: size band`);
    else assert.equal(t.size, null, `${at}: a line has no size band`);
    assert.ok(STAGES.includes(t.stage), `${at}: stage "${t.stage}" is in the vocabulary`);
    assert.equal(typeof t.publicCheckpoint, "boolean", `${at}: publicCheckpoint`);
    assert.ok(t.onAirBand === null || /^[a-z0-9-]+$/.test(t.onAirBand), `${at}: onAirBand`);
    assert.equal(t.onAirBand !== null, t.stage === "on-air" || t.stage === "released",
      `${at}: an on-air band exactly when the stage is on air or later`);
    assert.equal(t.publicCheckpoint, t.stage === "released", `${at}: a public checkpoint only when released`);
    if ("ruling" in t) {
      assert.equal(t.ruling, "pending", `${at}: the only ruling value is "pending"`);
      assert.ok(typeof t.conflict === "string" && t.conflict.length > 20, `${at}: a pending ruling says what disagrees`);
    } else assert.ok(!("conflict" in t), `${at}: a conflict note only rides on a pending ruling`);
    assert.ok(Object.keys(t.labels).length > 0, `${at}: at least one label`);
    for (const [id, l] of Object.entries(t.labels)) {
      assert.match(id, /^[a-z-]+\.[a-z-]+$/, `${at}: label id ${id} is page-key.slot`);
      assert.equal(typeof l.page, "string", `${at}.${id}: page`);
      assert.ok(existsSync(path.join(SRC, l.page)), `${at}.${id}: page ${l.page} exists`);
      assert.equal(typeof l.text, "string", `${at}.${id}: text`);
      assert.equal(typeof l.wired, "boolean", `${at}.${id}: wired`);
      assert.ok(!/\u2014/.test(l.text), `${at}.${id}: no em dash`);
    }
  }
});

test("wave-status: the data file is build input only and never ships", () => {
  assert.ok(!existsSync(path.join(DIST, "data", "wave-status.json")),
    "pending rulings and stage definitions are not published yet");
});

/* ---- 2. the token resolver ---------------------------------------------- */

const FIX = {
  tiers: [{ id: "pico", labels: {
    "index.plate": { page: "index.html", text: "A &middot; B", wired: true },
    "research.scope": { page: "research.html", text: "C", wired: true } } }],
};

test("resolver: a token renders its label text verbatim, attributes included", () => {
  assert.equal(resolveWaveTokens('<b data-x="{{wave:pico.index.plate}}">{{wave:pico.index.plate}}</b>', "index.html", FIX),
    '<b data-x="A &middot; B">A &middot; B</b>');
});

test("resolver: a page with no token is returned untouched", () => {
  const html = "<p>{{other}} stays for someone else</p>";
  assert.equal(resolveWaveTokens(html, "index.html", FIX), html);
});

test("resolver: an unknown tier or label fails the build instead of shipping a blank", () => {
  assert.throws(() => resolveWaveTokens("{{wave:zeta.index.plate}}", "index.html", FIX), /zeta/);
  assert.throws(() => resolveWaveTokens("{{wave:pico.index.nope}}", "index.html", FIX), /index\.nope/);
});

test("resolver: a label renders only on the page it belongs to", () => {
  assert.throws(() => resolveWaveTokens("{{wave:pico.research.scope}}", "index.html", FIX), /research\.html/);
});

test("resolver: a malformed token fails rather than shipping literally", () => {
  assert.throws(() => resolveWaveTokens("{{wave:pico}}", "index.html", FIX), /unresolved/);
});

test("resolver: loadWaveStatus reads the real data file", () => {
  assert.deepEqual(loadWaveStatus(), data());
});

/* ---- 3. every inventoried page shows exactly the label the data says ----- */

test("pages: a wired label comes from its token, and ships as the data text", () => {
  for (const l of allLabels().filter((x) => x.wired)) {
    const src = readFileSync(path.join(SRC, l.page), "utf8");
    assert.ok(src.includes(`{{wave:${l.tier.id}.${l.id}}}`), `${l.page} renders ${l.tier.id}.${l.id} from the data`);
    assert.ok(region(l.page, l.id).includes(norm(l.text)), `${l.page} shows "${l.text}"`);
  }
});

test("pages: a hand-typed label still reads exactly what the data says", () => {
  for (const l of allLabels().filter((x) => !x.wired)) {
    const src = readFileSync(path.join(SRC, l.page), "utf8");
    assert.ok(!src.includes(`{{wave:${l.tier.id}.${l.id}}}`), `${l.tier.id}.${l.id} is marked hand-typed but is wired`);
    if (l.page === "js/playbox.js") continue;   // checked structurally below
    assert.ok(region(l.page, l.id).includes(norm(l.text)),
      `${l.page} no longer shows "${l.text}" for ${l.tier.name} (${l.id}): change the data file too`);
  }
});

test("pages: no wave token is left unresolved anywhere in the built site", () => {
  for (const page of new Set(allLabels().map((l) => l.page).filter((p) => p.endsWith(".html")))) {
    assert.doesNotMatch(readFileSync(path.join(DIST, page), "utf8"), /\{\{\s*wave:/, page);
  }
});

// Structural sweeps: a NEW chip, stage or status cell for a tier in these repeated spots
// must equal the data, not merely coexist with it.
function labelOf(tierId, labelId) {
  const l = tierById(tierId)?.labels[labelId];
  assert.ok(l, `the data has ${tierId}.${labelId}`);
  return norm(l.text);
}
const NAME_TO_ID = { "Wave Pico": "pico", "Wave Nano": "nano", "Wave Micro": "micro", "Wave Giga": "giga",
  "Wave Tera": "tera", "Wave Peta": "peta", "Wave Exa": "exa", "Roger Edge": "edge" };
const dist = (p) => readFileSync(path.join(DIST, p), "utf8");

test("sweep: research-models status chips and stage lines equal the data", () => {
  const html = dist("research-models.html");
  const rows = [...html.matchAll(/<article class="model-row"[^>]*>([\s\S]*?)<\/article>/g)].map((m) => m[1]);
  let seen = 0;
  for (const row of rows) {
    const name = row.match(/<h3>([^<]+)<\/h3>/)?.[1];
    const id = NAME_TO_ID[name];
    if (!id) continue;
    seen++;
    assert.equal(norm(row.match(/<span class="status[^"]*">([^<]+)<\/span>/)[1]), labelOf(id, "research-models.chip"), `${name} chip`);
    assert.equal(norm(row.match(/<span class="model-stage">([^<]+)<\/span>/)[1]), labelOf(id, "research-models.stage"), `${name} stage`);
  }
  assert.equal(seen, 8, "seven tiers and Roger Edge are rows on the models page");
});

test("sweep: the research scope legend status equals the data", () => {
  const rows = [...dist("research.html").matchAll(/<li class="scope__row[^"]*" data-slot="wave-(\w+)">([\s\S]*?)<\/li>/g)];
  assert.equal(rows.length, 4, "the scope legend draws Pico to Giga");
  for (const [, id, body] of rows) {
    assert.equal(norm(body.match(/<span class="scope__status">([^<]+)<\/span>/)[1]), labelOf(id, "research.scope"), id);
  }
});

test("sweep: every hardware card and experiment stage equals the data", () => {
  const html = dist("research-hardware.html");
  const cards = [...html.matchAll(/<article class="hw-card[^"]*" data-board="[^"]+" data-tier="([^"]+)"\s+data-stage="([^"]+)"[\s\S]*?<dd class="hw-stage">([^<]+)<\/dd>/g)];
  assert.equal(cards.length, 10, "three Roger Edge boards and one card per ladder tier");
  for (const [, tier, attr, shown] of cards) {
    const want = labelOf(NAME_TO_ID[tier], "research-hardware.card");
    assert.equal(norm(attr), want, `${tier} data-stage`);
    assert.equal(norm(shown), want, `${tier} shown stage`);
  }
  const exp = html.match(/<article class="hw-exp" data-exp="pico" data-stage="([^"]+)">[\s\S]*?<p class="hw-exp__stage mono">([^<]+)<\/p>/);
  assert.equal(norm(exp[1]), labelOf("pico", "research-hardware.exp"));
  assert.equal(norm(exp[2]), labelOf("pico", "research-hardware.exp"));
});

test("sweep: every Playbox tier chip equals the data", () => {
  const js = readFileSync(path.join(SRC, "js/playbox.js"), "utf8");
  const chips = [...js.matchAll(/model: "wave-(\w+)"[^}]*?chip: "([^"]+)"/g)];
  assert.equal(chips.length, 6, "Pico and the five planned spines carry a chip");
  for (const [, id, chip] of chips) assert.equal(chip, labelOf(id, "playbox.chip"), id);
});

test("sweep: the Wave family ladder recipe cells for the scratch tiers equal the data", () => {
  const html = dist("research-wave-family.html");
  for (const id of ["pico", "nano", "micro"]) {
    const name = `Wave ${id[0].toUpperCase()}${id.slice(1)}`;
    const row = html.match(new RegExp(`<th scope="row">${name}</th>[\\s\\S]*?</tr>`))[0];
    const cells = [...row.matchAll(/<td class="tier-cell">([^<]+)<\/td>/g)].map((m) => m[1]);
    assert.equal(norm(cells.at(-1)), labelOf(id, "wave-family.recipe"), name);
  }
});

/* ---- 4. the CONFLICTS LEDGER -------------------------------------------- */

// Each label text, read into the stage it implies. Classified here, not in the data file.
// A new wording fails until someone decides what it claims.
const IMPLIES = {
  planned: [
    "Next scratch build", "NEXT BUILD", "Scratch build queued behind Pico", "Scratch &middot; next build",
    "PLANNED", "Planned", "Scratch pipeline standing up", "Scratch &middot; planned", "next scratch build",
    "Wave Micro planned",
    // "Base selection" and "base pick" say the base is still being CHOSEN: not yet base-selected.
    "Base selection", "~60 GB &middot; base pick", "Wave Tera about 60 GB at 100B, base being selected",
    "Architecture and data design", "Pruning plan", "Wave Peta about 105 GB and Wave Exa about 170 GB, planned",
  ],
  "base-selected": [
    "Base selected", "FRONTIER BASE",
    // a role sitting in the stage slot; read with its own row's chip, FRONTIER BASE
    "The family teacher",
  ],
  training: [
    "~690 MB &middot; training", "Wave Nano about 690 MB at 1.15B, training",
    // prototypes: something is being built and tested, nothing has passed a release gate
    "PROTOTYPE", "Prototype, actively testing",
    "Prototype &middot; measured foundations, growth loop in development",
    "prototype &middot; foundations measured, growth loop in development",
  ],
  gated: [
    "EDGE CLASS · WAYPOINT TRAINED", "TRAINED", "250–300M · waypoint", "WAYPOINT", "Waypoint trained",
    "WAYPOINT TRAINED", "Wave Pico has a trained waypoint build, certified against our own release gate",
    "Certifying the waypoint &middot; 300M retrain next", "Scratch &middot; waypoint trained",
    "~165 MB &middot; built", "Wave Pico about 165 MB, built",
    "27B, Apache-2.0 base, 16.5 GB on disk", "16.5 GB &middot; built", "Wave Giga 16.5 GB at Q4_K_M, built",
    "built and gated, public checkpoint pending",
    "Wave Giga (27B) has passed its release gate on an Apache-2.0 base",
  ],
  "on-air": [
    "293M, from scratch, on air now",
    "on air as a free band, <code>wave-pico-293m</code>; weights not published yet",
    "Wave Pico (293M) is on air right now as a free band",
  ],
  released: [],
};
const STAGE_OF = new Map(Object.entries(IMPLIES).flatMap(([s, texts]) => texts.map((t) => [norm(t), s])));

function impliedStages(t) {
  return new Set(Object.entries(t.labels).map(([id, l]) => {
    const s = STAGE_OF.get(norm(l.text));
    assert.ok(s, `${t.id}.${id}: "${l.text}" is not classified into a stage; add it to IMPLIES`);
    return s;
  }));
}

test("ledger: the tiers whose pages disagree are exactly the tiers awaiting a ruling", () => {
  const d = data();
  const conflicted = d.tiers.filter((t) => impliedStages(t).size > 1).map((t) => t.id);
  const pending = d.tiers.filter((t) => t.ruling === "pending").map((t) => t.id);
  assert.deepEqual(conflicted, pending,
    "a tier with conflicting labels must be marked pending, and a ruled tier must have unified labels");
});

test("ledger: a tier without a pending ruling states the one stage all its labels imply", () => {
  for (const t of data().tiers.filter((x) => x.ruling !== "pending")) {
    assert.deepEqual([...impliedStages(t)], [t.stage], `${t.id}: labels and canonical stage agree`);
  }
});

test("ledger: nothing claims a public checkpoint while none is released", () => {
  for (const t of data().tiers) {
    if (!t.publicCheckpoint) assert.ok(!impliedStages(t).has("released"), `${t.id} shows a released label`);
  }
});

test("ledger: the pending set today (update this when the founder rules)", () => {
  assert.deepEqual(data().tiers.filter((t) => t.ruling === "pending").map((t) => t.id),
    ["pico", "nano", "micro", "giga", "exa"]);
});
