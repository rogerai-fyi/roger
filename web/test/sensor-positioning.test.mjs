// "Your device, now a sensor": the page scenarios of
// features/web/sensor_positioning.feature (PROPOSED 2026-10-15, awaiting founder approval).
// The data file, the token and the sitewide sweeps live in capabilities.test.mjs.
//
// Every block here is design tense and number-free: no digit in its visible words, no
// "installed / deployed / running", no company, no actuation verb on a valve or sprinkler.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8").replace(/<!--[\s\S]*?-->/g, "");
const css = (f) => readFileSync(path.join(SRC, "styles", f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const data = () => JSON.parse(readFileSync(path.join(SRC, "data", "capabilities.json"), "utf8"));
const visible = (html) => html
  .replace(/<script\b[\s\S]*?<\/script>/g, " ").replace(/<style\b[\s\S]*?<\/style>/g, " ")
  .replace(/<[^>]+>/g, " ").replace(/&rsquo;/g, "'").replace(/&amp;/g, "&").replace(/&nbsp;/g, " ")
  .replace(/&middot;/g, "·").replace(/&rarr;/g, "→").replace(/\s+/g, " ").trim();
const capIds = (html) => [...html.matchAll(/\bdata-cap="([^"]+)"/g)].map((m) => m[1]);

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// the shared honesty rules every new block is held to
function honest(label, html) {
  const t = visible(html);
  assert.ok(t.length > 80, `${label}: has copy`);
  assert.doesNotMatch(t, /\d/, `${label}: no number`);
  assert.doesNotMatch(t, /\b(installed|deployed|running|in service|in operation)\b/i, `${label}: nothing claimed in the field`);
  assert.doesNotMatch(t, /FireDefense|customers?\b|clients?\b|paying|investor/i, `${label}: no company, customer or commercial detail`);
  assert.doesNotMatch(t, /\b(opens?|triggers?|activates?|controls?|actuates?|starts?)\s+(the\s+|a\s+|any\s+)?(valves?|sprinklers?|zones?|alarms?)/i,
    `${label}: no actuation`);
  assert.doesNotMatch(t, /insur|surviv|saves? (a |the )?(homes?|lives)|every fire|never miss|guarantee/i, `${label}: no outcome claim`);
  assert.doesNotMatch(t, /camera|vision|pixels?|subscri|monitoring|we sell|ruggedi/i, `${label}: out of this pass`);
  assert.doesNotMatch(t, /—/, `${label}: no em dash`);
}

const dial = (html) => html.match(/<ol class="spectrum spectrum--dial"[\s\S]*?<\/ol>/)?.[0] || "";

/* ---- homepage ---------------------------------------------------------------- */

const home = () => dist("index.html");
const sensor = () => home().match(/<section\b[^>]*id="sensor"[\s\S]*?<\/section>/)?.[0] || "";

test("homepage: the sensor section sits between the company section and the tune", () => {
  const h = home();
  assert.ok(sensor(), "the homepage carries section#sensor");
  assert.ok(h.indexOf('id="company"') < h.indexOf('id="sensor"'), "after the company section");
  assert.ok(h.indexOf('id="sensor"') < h.indexOf('id="what"'), "before the tune");
  assert.match(sensor(), /<span class="sectionno">§4 \/ ON SITE<\/span>\s*<h2>Your device, now a sensor\.<\/h2>/);
});

test("homepage: three steps on the shared step path", () => {
  const steps = sensor().match(/<ol class="steps"[^>]*>([\s\S]*?)<\/ol>/)?.[1] || "";
  const leads = [...steps.matchAll(/<li><b>([^<]+)<\/b>/g)].map((m) => m[1]);
  assert.deepEqual(leads, ["Install RogerAI.", "Pick a Wave model.", "Pick an adapter."]);
});

test("homepage: what you get says on site, offline, a person, advisory, never a machine", () => {
  const get = visible(sensor().match(/<p class="sensor__get">[\s\S]*?<\/p>/)?.[0] || "");
  assert.match(get, /built to run on site/i);
  assert.match(get, /offline if you want/i);
  assert.match(get, /cannot vouch for[^.]*to a person/i);
  assert.match(get, /advis[a-z]*[^.]*beside the control system/i);
  assert.match(get, /never moves a machine/i);
  assert.match(get, /a person makes every call/i);
});

test("homepage: the adapter dial shows every capability, from the file", () => {
  const d = dial(sensor());
  assert.ok(d, "the section carries the adapter dial");
  assert.match(d, /data-scrub/, "the shared scrubber tunes it");
  assert.deepEqual(capIds(d), data().capabilities.map((c) => c.id));
  assert.match(sensor(), /href="\/research-industry\.html#adapters"/, "it links the industrial shelf");
  assert.match(sensor(), /href="\/research-hardware\.html#devices-row"/, "and the devices row");
});

test("homepage: the section numbers run in order with the new section", () => {
  const nos = [...home().matchAll(/<span class="sectionno">§(\d+) \//g)].map((m) => Number(m[1]));
  assert.deepEqual(nos, nos.map((_, i) => i + 1), "§1 to §n with no gap or repeat");
  assert.equal(nos.length, 10);
});

test("homepage: the sensor section is design tense, number-free and honest", () => {
  honest("homepage sensor section", sensor());
});

/* ---- industrial page ------------------------------------------------------------ */

const industry = () => dist("research-industry.html");
const shelf = () => industry().match(/<div class="adapter-shelf" id="adapters">[\s\S]*?<!-- \/adapter-shelf -->|<div class="adapter-shelf" id="adapters">[\s\S]*?(?=<div class="device-contract">\s*<p><b>Signals\.)/)?.[0] || "";

test("industrial: the adapter shelf sits after the placement figure, before the plant-interface contract", () => {
  const i = industry();
  assert.ok(shelf(), "the page carries div.adapter-shelf#adapters");
  assert.ok(i.indexOf('id="adapters"') > i.indexOf('<figure class="purdue"'), "after the placement figure");
  assert.ok(i.indexOf('id="adapters"') < i.indexOf("<b>Signals.</b>"), "before Signals / Models / Placement");
  assert.match(shelf(), /<h3>Capabilities come as adapters\.<\/h3>/);
});

test("industrial: the shelf shows the same dial, read from the file", () => {
  const d = dial(shelf());
  assert.ok(d);
  assert.deepEqual(capIds(d), data().capabilities.map((c) => c.id));
  assert.equal(d.replace(/\s+/g, " "), dial(sensor()).replace(/\s+/g, " "), "one dial, one partial, on both pages");
});

test("industrial: escalates when unsure, with the hand-off drawn as the good outcome", () => {
  const fig = shelf().match(/<figure class="figure figure--plate gate"[\s\S]*?<\/figure>/)?.[0] || "";
  assert.ok(fig, "the shelf carries the gate figure");
  assert.match(fig, /role="group"/, "a named group, not an image");
  const good = fig.match(/<div class="gate__way gate__way--good">[\s\S]*?<\/div>/)?.[0] || "";
  assert.match(visible(good), /A person decides\./, "the hand-off path is the good one");
  assert.match(visible(good), /the right call/i, "and it reads as the right call, not a fault");
  assert.match(visible(fig.match(/<figcaption[\s\S]*?<\/figcaption>/)?.[0] || ""), /illustrative/i);
  assert.doesNotMatch(visible(fig), /\d|%/, "no number, no threshold value");
  assert.ok(fig.indexOf("gate__way--good") < fig.indexOf('class="gate__way"'), "the person path is drawn first");
});

test("industrial: the shelf is design tense, number-free and honest", () => {
  honest("industrial adapter shelf", shelf());
  assert.doesNotMatch(visible(shelf()), /fire alarm|protection system|suppression/i);
});

/* ---- Wave family ----------------------------------------------------------------- */

test("wave family: one base model, a small add-on per job, status from the file", () => {
  const sec = dist("research-wave-family.html").match(/<section[^>]*id="variations"[\s\S]*?<\/section>/)?.[0] || "";
  const block = sec.match(/<p class="adapter-note">[\s\S]*?<\/p>/)?.[0] || "";
  assert.match(block, /<b>One base model, a small add-on per job\.<\/b>/);
  assert.deepEqual(capIds(block), ["fire-warning", "telemetry-drift"]);
  assert.match(visible(block), /drifting[^.]*built into the base[^.]*not an adapter/i);
  honest("wave family adapter note", block);
});

/* ---- hardware ---------------------------------------------------------------------- */

const hw = () => dist("research-hardware.html");
const row = () => hw().match(/<div class="devices" id="devices-row">[\s\S]*?<!-- \/devices -->/)?.[0] || "";

test("hardware: three devices, one install, each status from the file, the iPad a ghost", () => {
  const h = hw();
  assert.ok(row(), "the page carries the devices row");
  assert.ok(h.indexOf('id="devices-row"') > h.indexOf('id="ladder"') && h.indexOf('id="devices-row"') < h.indexOf('id="boards"'),
    "in §1, above the boards");
  assert.match(row(), /<h3>Devices a site already has\.<\/h3>/);
  assert.match(row(), /class="install__box/, "one install pill");
  assert.deepEqual(capIds(row()), data().devices.map((d) => d.id));
  const ipad = row().match(/<li class="device"[^>]*data-cap="ipad"[^>]*>/)?.[0] || "";
  assert.match(ipad, /data-stage="planned"/);
  assert.match(css("research-hardware.css"), /\.device\[data-stage="planned"\][^{]*\{[^}]*dashed/, "a planned device is drawn as a dashed ghost");
  honest("hardware devices row", row().replace(/<div class="install__box[\s\S]*?<\/div>/, ""));
});

/* ---- Tower ---------------------------------------------------------------------- */

test("tower: the standalone note says it runs offline on your own machines", () => {
  const t = visible(dist("tower.html"));
  assert.match(t, /A site that never dials out can still run it: offline, on its own machines/);
});

/* ---- every new figure ------------------------------------------------------------- */

test("figures: complete without script or motion, mono and one red, labels at 11px or more", () => {
  const sheets = { "components.css": css("components.css"), "research.css": css("research.css"),
    "research-hardware.css": css("research-hardware.css"), "home.css": css("home.css") };
  const NEW = /\.(spectrum--dial|cap-lamp|cap-stage|gate|gate__[a-z-]+|devices?|device__[a-z-]+|sensor__[a-z-]+|adapter-shelf|adapter-note)\b/;
  for (const [name, sheet] of Object.entries(sheets)) {
    for (const [, sel, body] of sheet.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
      if (!NEW.test(sel)) continue;
      assert.doesNotMatch(body, /#[0-9a-f]{3,8}\b|rgba?\(|hsla?\(/i, `${name} ${sel.trim()}: tokens only`);
      assert.doesNotMatch(body, /box-shadow|text-shadow|filter:\s*drop-shadow|--live-glow|--live-wash/, `${name} ${sel.trim()}: no glow`);
      assert.doesNotMatch(body, /position:\s*(sticky|fixed)/, `${name} ${sel.trim()}: nothing pinned`);
      assert.doesNotMatch(body, /background(-image)?:[^;]*(linear|repeating)-gradient/, `${name} ${sel.trim()}: no grid or gradient wash`);
      assert.doesNotMatch(body, /animation|transition/, `${name} ${sel.trim()}: nothing moves`);
      const fs = body.match(/font(?:-size)?:[^;]*?(\d+(?:\.\d+)?)(px|rem)/);
      if (fs) assert.ok((fs[2] === "rem" ? Number(fs[1]) * 16 : Number(fs[1])) >= 11, `${name} ${sel.trim()}: label at 11px or more`);
      assert.doesNotMatch(body, /font-size:\s*var\(--t-(?!micro|xs|sm|body|lead|h[1-3])/, `${name} ${sel.trim()}: a type token`);
    }
  }
  // no-JS: every station, device and gate path is in the HTML
  assert.equal(capIds(dial(sensor())).length, data().capabilities.length);
  assert.equal(capIds(row()).length, data().devices.length);
});
