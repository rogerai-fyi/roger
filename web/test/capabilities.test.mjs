// ONE SOURCE OF TRUTH FOR ADAPTER, SKILL AND DEVICE STATUS
// (features/web/sensor_positioning.feature, PROPOSED 2026-10-15).
//
// web/src/data/capabilities.json holds every adapter, base skill and device row with its
// status from a fixed vocabulary. Pages render it through a {{cap:<id>.<field>}} token
// (scripts/wave-status.mjs, beside the Wave tier tokens), so no page can tell a reader a
// different story from the file. The sweeps below walk EVERY built page, the way
// wave-claim-sweep.test.mjs does: a new page inherits the guard by existing.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { resolveCapTokens, loadCapabilities } from "../scripts/wave-status.mjs";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const DATA = path.join(SRC, "data", "capabilities.json");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const data = () => JSON.parse(readFileSync(DATA, "utf8"));
const wave = () => JSON.parse(readFileSync(path.join(SRC, "data", "wave-status.json"), "utf8"));
const entries = () => [...data().capabilities, ...data().devices];
const byId = (id) => entries().find((e) => e.id === id);
const STAGES = ["planned", "lab", "release-candidate", "available"];
const pages = () => readdirSync(DIST).filter((f) => f.endsWith(".html"));
const dist = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (html) => html
  .replace(/<script\b[\s\S]*?<\/script>/g, " ").replace(/<style\b[\s\S]*?<\/style>/g, " ")
  .replace(/<!--[\s\S]*?-->/g, " ").replace(/<[^>]+>/g, " ")
  .replace(/&rsquo;/g, "'").replace(/&amp;/g, "&").replace(/&nbsp;/g, " ").replace(/&middot;/g, "·")
  .replace(/\s+/g, " ").trim();
const FIRE_DETECTION = /fire[\s-]*detect|detects?\s+(a\s+|the\s+)?(wild)?fires?\b|detecting\s+(wild)?fires?/i;

/* ---- 1. the file is valid ------------------------------------------------ */

test("capabilities: the stage vocabulary is exactly the fixed four, in order, each defined in one line", () => {
  const d = data();
  assert.deepEqual(Object.keys(d.stages), STAGES);
  for (const [k, v] of Object.entries(d.stages)) {
    assert.equal(typeof v, "string", `${k} has a definition`);
    assert.ok(v.length > 10 && !v.includes("\n"), `${k} is a one-line definition`);
  }
  assert.deepEqual(Object.keys(d.stageLabels), STAGES, "every stage has a public label");
  for (const [k, v] of Object.entries(d.stageLabels)) {
    assert.match(v, /^[a-z][a-z -]{2,30}$/, `${k}: a short lowercase public label`);
  }
});

test("capabilities: every capability carries the required fields with the right types", () => {
  const tiers = new Set(wave().tiers.map((t) => t.id));
  assert.ok(data().capabilities.length >= 3);
  for (const c of data().capabilities) {
    const at = `capability ${c.id}`;
    assert.match(c.id, /^[a-z][a-z-]*$/, `${at}: id`);
    assert.equal(typeof c.name, "string", `${at}: name`);
    assert.ok(["adapter", "base-skill"].includes(c.kind), `${at}: kind "${c.kind}"`);
    assert.ok(tiers.has(c.base), `${at}: base tier "${c.base}" exists in wave-status.json`);
    assert.ok(STAGES.includes(c.stage), `${at}: stage "${c.stage}" is in the vocabulary`);
    assert.equal(typeof c.description, "string", `${at}: description`);
    assert.ok(c.description.length > 20 && !c.description.includes("\n"), `${at}: a one-line description`);
  }
});

test("capabilities: every device row carries the required fields with the right types", () => {
  assert.deepEqual(data().devices.map((d) => d.id), ["laptop", "industrial-pc", "ipad"]);
  for (const d of data().devices) {
    const at = `device ${d.id}`;
    assert.equal(typeof d.name, "string", `${at}: name`);
    assert.equal(typeof d.installs, "boolean", `${at}: installs`);
    assert.ok(STAGES.includes(d.stage), `${at}: stage "${d.stage}" is in the vocabulary`);
    assert.equal(typeof d.description, "string", `${at}: description`);
    assert.ok(d.description.length > 20 && !d.description.includes("\n"), `${at}: a one-line description`);
    assert.equal(typeof d.ghost, "boolean", `${at}: ghost`);
    assert.equal(d.ghost, d.stage === "planned", `${at}: drawn as a ghost exactly when planned`);
    assert.ok(!("speed" in d) && !/\d+\s*(tok|t\/s|ms|fps)/i.test(d.description), `${at}: no speed, none is measured`);
  }
});

test("capabilities: ids are unique across capabilities and devices, and no text carries an em dash", () => {
  const ids = entries().map((e) => e.id);
  assert.equal(new Set(ids).size, ids.length);
  assert.doesNotMatch(readFileSync(DATA, "utf8"), /—/);
});

test("capabilities: the data file is build input only and never ships", () => {
  assert.ok(!existsSync(path.join(DIST, "data", "capabilities.json")));
});

/* ---- 2. the first entries say what is true today -------------------------- */

test("capabilities: fire early warning is an adapter on Wave Pico, in the lab", () => {
  const c = byId("fire-warning");
  assert.equal(c?.name, "Fire early warning");
  assert.equal(c.kind, "adapter");
  assert.equal(c.base, "pico");
  assert.equal(c.stage, "lab");
});

test("capabilities: telemetry drift is a base skill, your own is planned, vibration is no adapter", () => {
  assert.equal(byId("telemetry-drift")?.kind, "base-skill");
  assert.equal(byId("telemetry-drift").base, "pico");
  assert.equal(byId("custom")?.name, "Your own");
  assert.equal(byId("custom").kind, "adapter");
  assert.equal(byId("custom").stage, "planned");
  for (const c of data().capabilities.filter((x) => x.kind === "adapter")) {
    assert.doesNotMatch(`${c.id} ${c.name}`, /vibration|bearing|predictive/i, `${c.id}: vibration is not an adapter`);
  }
  assert.doesNotMatch(readFileSync(DATA, "utf8"), FIRE_DETECTION, "the approved term is early warning");
});

test("capabilities: the iPad is planned and ghosted; nothing is offered for sale", () => {
  const ipad = byId("ipad");
  assert.equal(ipad.stage, "planned", "the iPad runs no Wave model or adapter today");
  assert.equal(ipad.ghost, true);
  assert.deepEqual(data().offers, { hardware: false, monitoring: false, subscription: false });
});

/* ---- 3. the token resolver ------------------------------------------------- */

const FIX = {
  stageLabels: { planned: "planned", lab: "in the lab", "release-candidate": "release candidate", available: "available" },
  capabilities: [{ id: "fire-warning", name: "Fire early warning", stage: "lab", description: "A &middot; B" }],
  devices: [{ id: "ipad", name: "iPad", stage: "planned", description: "C" }],
};

test("resolver: a cap token renders name, stage, label and description verbatim", () => {
  assert.equal(resolveCapTokens('<li data-stage="{{cap:fire-warning.stage}}">{{cap:fire-warning.name}} {{cap:fire-warning.label}} {{cap:fire-warning.description}}</li>', "x.html", FIX),
    '<li data-stage="lab">Fire early warning in the lab A &middot; B</li>');
  assert.equal(resolveCapTokens("{{cap:ipad.label}}", "x.html", FIX), "planned");
});

test("resolver: a page with no cap token is returned untouched", () => {
  const html = "<p>{{wave:pico.index.chip}} stays for the wave resolver</p>";
  assert.equal(resolveCapTokens(html, "x.html", FIX), html);
});

test("resolver: an unknown id or field fails the build", () => {
  assert.throws(() => resolveCapTokens("{{cap:vibration.name}}", "x.html", FIX), /vibration/);
  assert.throws(() => resolveCapTokens("{{cap:ipad.speed}}", "x.html", FIX), /speed/);
});

test("resolver: a malformed cap token fails rather than shipping literally", () => {
  assert.throws(() => resolveCapTokens("{{cap:ipad}}", "x.html", FIX), /unresolved/);
});

test("resolver: loadCapabilities reads the real data file", () => {
  assert.deepEqual(loadCapabilities(), data());
});

test("pages: no cap token is left unresolved anywhere in the built site", () => {
  for (const p of pages()) assert.doesNotMatch(dist(p), /\{\{\s*cap:/, p);
});

/* ---- 4. every page's rendered status matches the file ----------------------- */

// each element carrying data-cap, with what follows it up to the next one (its stage words
// sit inside it, often in nested spans, so the body is read forward rather than tag-matched)
export const capElements = (html) => {
  const tags = [...html.matchAll(/<\w+\b([^>]*\bdata-cap="([^"]+)"[^>]*)>/g)];
  return tags.map((m, i) => ({ attrs: m[1], id: m[2],
    body: html.slice(m.index + m[0].length, i + 1 < tags.length ? tags[i + 1].index : m.index + 2000) }));
};

test("pages: every data-cap element shows the stage and label the file gives it", () => {
  const d = data();
  let seen = 0;
  for (const p of pages()) {
    for (const el of capElements(dist(p))) {
      seen++;
      const e = byId(el.id);
      assert.ok(e, `${p}: data-cap="${el.id}" names a known entry`);
      assert.equal(el.attrs.match(/data-stage="([^"]*)"/)?.[1], e.stage, `${p}: ${el.id} data-stage`);
      const shown = el.body.match(/<span class="cap-stage[^"]*">([^<]*)<\/span>/)?.[1];
      assert.equal(shown?.trim(), d.stageLabels[e.stage], `${p}: ${el.id} shows its stage label`);
    }
  }
  assert.ok(seen >= 9, `status lamps are rendered on the pages (found ${seen})`);
});

test("pages: every capability appears on the homepage and the industrial page; every device on the hardware page", () => {
  const ids = (p) => new Set(capElements(dist(p)).map((e) => e.id));
  for (const c of data().capabilities) {
    assert.ok(ids("index.html").has(c.id), `homepage shows ${c.id}`);
    assert.ok(ids("research-industry.html").has(c.id), `industrial page shows ${c.id}`);
  }
  for (const d of data().devices) assert.ok(ids("research-hardware.html").has(d.id), `hardware page shows ${d.id}`);
});

test("pages: a lamp is red only for the available stage", () => {
  const css = readFileSync(path.join(SRC, "styles", "components.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const lampRules = [...css.matchAll(/([^{}]*\.cap-lamp[^{}]*)\{([^}]*)\}/g)];
  assert.ok(lampRules.length > 0, "the lamp is a shared component");
  for (const [, sel, body] of lampRules) {
    if (/var\(--live\)/.test(body)) {
      assert.match(sel, /\[data-stage="available"\]/, `only the available stage paints the lamp red: ${sel.trim()}`);
    }
  }
  assert.ok(lampRules.some(([, sel, body]) => /\[data-stage="available"\]/.test(sel) && /var\(--live\)/.test(body)),
    "the available stage does paint it red");
});

/* ---- 5. the claim sweep ------------------------------------------------------ */

const CLAIM = /\b(available|live|running|installed|deployed|shipping)\b/gi;
const NEGATED = /\b(no|not|never|without|until|only when)\b[^.]{0,60}$/i;
const WINDOW = 160;
// generic names ("Your own") cannot be swept by name; they are held by their data-cap element
const SWEPT = () => entries().filter((e) => e.name.split(" ").length > 1 && e.id !== "custom");

export function offending(text, name, stage) {
  if (stage === "available") return [];
  const hits = [];
  const re = new RegExp(name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "gi");
  for (const m of text.matchAll(re)) {
    const around = text.slice(Math.max(0, m.index - WINDOW), m.index + m[0].length + WINDOW);
    for (const c of around.matchAll(CLAIM)) {
      if (NEGATED.test(around.slice(0, c.index))) continue;
      hits.push(`"${m[0]}" near "${c[0]}": ...${around}...`);
      break;
    }
  }
  return hits;
}

test("sweep: the sweep itself catches a claim and spares a negation", () => {
  assert.equal(offending("Fire early warning is available today.", "Fire early warning", "lab").length, 1);
  assert.equal(offending("Fire early warning is not available yet.", "Fire early warning", "lab").length, 0);
  assert.equal(offending("Fire early warning, now running at a site.", "Fire early warning", "lab").length, 1);
  assert.equal(offending("Fire early warning is available.", "Fire early warning", "available").length, 0);
});

test("sweep: no built page claims an adapter or device row is out ahead of the file", () => {
  const all = [];
  for (const p of pages()) {
    const text = visible(dist(p));
    for (const e of SWEPT()) all.push(...offending(text, e.name, e.stage).map((h) => `${p}: ${h}`));
  }
  assert.deepEqual(all, []);
});

test("sweep: no built page says fire detection; the approved term is early warning", () => {
  for (const p of pages()) assert.doesNotMatch(visible(dist(p)), FIRE_DETECTION, p);
});

test("sweep: no page puts the iPad beside a sensor, adapter or Wave claim without saying planned", () => {
  const label = data().stageLabels[byId("ipad").stage];
  for (const p of pages()) {
    const text = visible(dist(p));
    for (const m of text.matchAll(/\biPad\b/g)) {
      const around = text.slice(Math.max(0, m.index - WINDOW), m.index + WINDOW);
      if (!/\b(sensor|adapter|Wave)\b/i.test(around)) continue;
      if (byId("ipad").stage === "available") continue;
      assert.ok(around.includes(label), `${p}: the iPad beside a sensor claim must say "${label}": ...${around}...`);
    }
  }
});

test("sweep: no page offers hardware for sale while the file offers none", () => {
  if (data().offers.hardware) return;
  for (const p of pages()) {
    assert.doesNotMatch(visible(dist(p)), /\bwe sell\b|ruggedi[sz]ed|\bbuy (our|the|a) (box|device|hardware|unit|sensor)\b/i, p);
  }
});
