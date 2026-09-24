// Wildfire mitigation, the ninth market (features/web/healthcare_defense_markets.feature,
// founder-approved 2026-09-24). One test per wildfire scenario; the market-set, count and
// strip scenarios are shared with the other eight and live in wave-family.test.mjs.
//
// THE LINE these tests hold: the models warn and explain. They are never a listed fire
// alarm or sprinkler system, never in its control path, never an evacuation directive,
// never an outcome claim. A valve opens only on the owner's command or an owner-armed
// rule the controller executes with its own abort window, never on the model's word, so
// the page's "never actuates equipment" promise stands with no carve-out.
//
// "Wildfire copy" = the visible text of the wildfire card (photo credit excluded), the
// wildfire boundary paragraph, and the wildfire use case. The boundary's one negating
// sentence is the only place a forbidden term ("listed", "evacuation order") may appear.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ")
  .replace(/&rsquo;/g, "'").replace(/&amp;/g, "&").replace(/&nbsp;/g, " ")
  .replace(/\s+/g, " ").trim();
const css = () => readFileSync(path.join(WEB, "src", "styles", "research.css"), "utf8")
  .replace(/\/\*[\s\S]*?\*\//g, "");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const industry = () => read("research-industry.html").replace(/<!--[\s\S]*?-->/g, "");
const strip = () => industry().match(/<nav class="usecase-strip"[\s\S]*?<\/nav>/)[0];
const stripLink = (slug) => strip().match(new RegExp(`<a href="#market-${slug}">[\\s\\S]*?</a>`))?.[0] || "";
const card = (slug) => industry().match(new RegExp(`<article id="market-${slug}"[\\s\\S]*?</article>`))?.[0] || "";
const title = (slug) => card(slug).match(/<b>([^<]+)<\/b>/)?.[1]?.trim() || "";
const cardCopy = () => visible(card("wildfire").replace(/<figcaption[\s\S]*?<\/figcaption>/g, ""));
// the boundaries block is the device-contract that carries the healthcare boundary
const boundaries = () => industry().match(/<div class="device-contract">(?:(?!<\/div>)[\s\S])*?Healthcare: equipment and facilities[\s\S]*?<\/div>/)?.[0] || "";
const boundaryHtml = () => [...boundaries().matchAll(/<p>[\s\S]*?<\/p>/g)].map((m) => m[0])
  .find((p) => /<b>Wildfire:/.test(p)) || "";
const boundary = () => visible(boundaryHtml());
const useCaseHtml = () => industry().match(/<div class="device-contract">\s*<p><b>Use case: an edge fire watch\.<\/b>[\s\S]*?<\/div>/)?.[0] || "";
const useCase = () => visible(useCaseHtml());
const wildfireCopy = () => [cardCopy(), boundary(), useCase()].join(" ");
// the boundary's one negating sentence is removed before a forbidden-term sweep
const sentences = (s) => s.split(/(?<=[.;])\s+/);
const sweepable = () => [cardCopy(),
  sentences(boundary()).filter((s) => !/not a listed fire alarm/i.test(s)).join(" "),
  useCase()].join(" ");
const sectionHead = () => visible(industry().match(/<section[^>]*id="industry"[\s\S]*?<\/div>\s*<div class="deployment-grid"/)?.[0] || "");

test("wildfire: the card exists with its name and slug", () => {
  const link = stripLink("wildfire");
  assert.ok(link, "the strip links #market-wildfire");
  assert.equal(visible(link), "Wildfire", "the strip label is Wildfire");
  const hrefs = [...strip().matchAll(/href="#(market-[a-z]+)"/g)].map((m) => m[1]);
  assert.equal(hrefs.at(-1), "market-wildfire", "wildfire is the ninth and last strip link");
  assert.ok(card("wildfire"), "the grid carries article#market-wildfire");
  assert.equal(title("wildfire"), "Wildfire mitigation");
  assert.ok(cardCopy().replace(title("wildfire"), "").trim().length > 40, "the card names a concrete workload");
  const alt = card("wildfire").match(/<img[^>]*\balt="([^"]*)"/)?.[1];
  assert.ok(alt, "the card photo has alt text");
  assert.doesNotMatch(alt, /fire|flame|smoke|burning|blaze/i, "the photo shows no fire on a home");
});

test("wildfire: nine markets lay out without a lone orphan", () => {
  const c = css();
  assert.match(c, /\.usecase-strip\s*\{[^}]*grid-template-columns:\s*repeat\(9,/, "nine across at full width");
  for (const bp of [880, 480]) {
    const m = c.match(new RegExp(`@media \\(max-width: ${bp}px\\) \\{ \\.usecase-strip \\{ grid-template-columns: repeat\\((\\d+),`));
    assert.equal(m?.[1], "3", `the strip is three across at ${bp}px, so nine close 3x3`);
  }
  // Nine cards fall back to the base three across (3x3): nothing may force four across.
  assert.doesNotMatch(c, /\.deployment-grid:has\(> article:nth-child\(8\)\)/,
    "the eight-card four-across rule would close nine as 4+4+1");
  assert.match(c, /\.deployment-grid\s*\{[^}]*grid-template-columns:\s*repeat\(3,/);
  // at the two-column breakpoint the ninth card takes the whole row
  const two = c.match(/@media \(max-width: 820px\) \{[\s\S]*?\n\}/)?.[0] || "";
  assert.match(two, /\.deployment-grid\s*[,{][^}]*grid-template-columns:\s*1fr 1fr/, "two columns at 820px");
  assert.match(two, /\.deployment-grid > article:nth-child\(9\):last-child\s*\{[^}]*grid-column:\s*1 \/ -1/,
    "the ninth card spans the row at two columns");
  const one = c.match(/@media \(max-width: 560px\) \{[\s\S]*?\n\}/)?.[0] || "";
  assert.match(one, /\.deployment-grid\s*[,{][^}]*grid-template-columns:\s*1fr[;\s}]/, "one column stacks");
});

test("wildfire: warning and mitigation, not fire protection", () => {
  assert.ok(boundaryHtml(), "the wildfire boundary sits with the healthcare and defense boundaries");
  assert.equal([...boundaries().matchAll(/<p>/g)].length, 3, "three boundaries: healthcare, defense, wildfire");
  const b = boundary();
  assert.match(b, /^Wildfire: warning and mitigation, not fire protection\./);
  assert.match(b, /not a listed fire alarm or sprinkler system/i);
  assert.match(b, /never in the control path of one/i);
  assert.match(b, /does not replace code-required detection or suppression, an evacuation order, or the fire service/i);
  const copy = sweepable();
  assert.doesNotMatch(copy, /\b(is|as|a|an) (fire alarm|fire protection system|suppression (system|control))/i,
    "never presented as an alarm, a protection system, or a suppression control");
});

test("wildfire: a valve opens only on the owner's command or an owner-armed rule", () => {
  const b = boundary();
  assert.match(b, /raises the alert/i);
  assert.match(b, /makes the threat call/i);
  assert.match(b, /valve opens only on the owner's command/i);
  assert.match(b, /under a rule the owner armed in advance/i);
  assert.match(b, /the controller[^.]*not the model[^.]*executes[^.]*its own abort window/i);
  assert.match(b, /never opens a valve by itself/i);
  // no copy has the model act on the equipment; only its own negation ("never opens") may
  for (const m of wildfireCopy().matchAll(/\b(opens?|triggers?|activates?|controls?|actuates?|starts?)\s+(the\s+|a\s+|any\s+|every\s+)?(valves?|sprinklers?|zones?)/gi)) {
    const before = wildfireCopy().slice(Math.max(0, m.index - 12), m.index);
    assert.match(before, /never\s*$/i, `the model acts on equipment: "${m[0]}"`);
  }
  // the universal promise stands, with no exception attached
  const head = sectionHead();
  const promise = sentences(head).find((s) => /never actuates equipment/i.test(s));
  assert.equal(promise, "In every deployment the model advises a person and stays outside the control loop: it never actuates equipment, and it is never part of a protection or safety path.");
  assert.doesNotMatch(head, /except|exception|carve-out|unless/i, "no carve-out");
  assert.match(visible(industry().match(/<figcaption id="purdue-caption">[\s\S]*?<\/figcaption>/)[0]),
    /wired to nothing in the control system/);
});

// the sweeps below prove nothing over an empty page, so each first checks it has copy
const hasCopy = () => {
  assert.ok(cardCopy() && boundary() && useCase(), "the wildfire card, boundary and use case all exist");
};

test("wildfire: no outcome, insurance, or life-safety claim", () => {
  hasCopy();
  const copy = sweepable();
  for (const claim of [
    /surviv|saves? (a |the )?(homes?|houses?|lives)|lives saved|prevents? (a |the )?loss|loss prevent/i,
    /insur|premium/i,
    /\blisted\b|approv|certif|code[- ]compliant|compliance|fire[- ]rat/i,
    /\d+\s?%|percent|detection rate|every fire|all fires|never miss/i,
    /evacuat|\bstay (put|home|inside)\b|shelter/i,
    /\b\d+\s+(installations?|installs|homes|properties|sites)\b|several installations|hundreds of|dozens of/i,
    /Orange County|Los Angeles|Inland Empire|Malibu|\b[A-Z][a-z]+ (Street|Road|Avenue|Drive|Lane|Canyon)\b/,
  ]) assert.doesNotMatch(copy, claim, `wildfire copy makes a claim it must not (${claim})`);
});

test("wildfire: the use case is honest about what runs today", () => {
  const uc = useCase();
  const film = sentences(uc).find((s) => /shown in the FireDefense film/i.test(s)) || "";
  assert.ok(film, "the mast is described as shown in the film");
  for (const part of [/mast/i, /camera/i, /gas/i, /models on board|on-board models/i]) {
    assert.match(film, part, `the film sentence names ${part}`);
  }
  assert.doesNotMatch(wildfireCopy(), /\b(installed|deployed|running|in service|in operation)\b/i,
    "the mast and the watch are not claimed to be in the field");
  // the tier status comes from the one source of truth, so a stage change breaks this
  const status = JSON.parse(readFileSync(path.join(WEB, "src", "data", "wave-status.json"), "utf8"));
  const tier = (id) => status.tiers.find((t) => t.id === id);
  const PHRASE = { "on-air": "on air", training: "in training", gated: "gated", planned: "planned",
                   "base-selected": "base selected", released: "released" };
  const pico = tier("pico"), nano = tier("nano");
  assert.match(uc, new RegExp(`Wave Pico[^.]*\\b${PHRASE[pico.stage]}\\b[^.]*${pico.onAirBand ?? ""}`),
    `Wave Pico is named with its status-file stage (${pico.stage})`);
  assert.match(uc, new RegExp(`Nano[^.]*\\b${PHRASE[nano.stage]}\\b`),
    `Wave Nano is named with its status-file stage (${nano.stage})`);
  assert.doesNotMatch(wildfireCopy(), /287M|0\.8B/i, "the film's model sizes stay out");
  assert.doesNotMatch(wildfireCopy(), /(\bapp\b|Safety Box)[^.]*(available|shipped|installed|live|App Store)/i,
    "the app and the Safety Box are in development, never available");
  assert.doesNotMatch(wildfireCopy(), /valve[- ]health|self-test|exercis/i,
    "no valve-health claim: no such code exists (founder ruling 2026-09-24)");
});

test("wildfire: FireDefense Systems is named as a separate company, with its film", () => {
  const uc = useCase();
  assert.match(uc, /Built with FireDefense Systems, a separate company that designs and installs exterior wildfire sprinkler systems for homes/);
  assert.doesNotMatch(wildfireCopy(), /\b(RogerAI|our)(&rsquo;s|'s)? (product|sprinklers?|app|fire map)|FireDefense[^.]*\bis a RogerAI\b/i,
    "FireDefense and its products are never RogerAI products");
  const href = useCaseHtml().match(/<a [^>]*href="([^"]*hero-fd[^"]*)"[^>]*>([\s\S]*?)<\/a>/);
  assert.ok(href, "the use case links the FireDefense film");
  assert.equal(href[1], "/assets/edge/hero-fd.mp4", "the film itself, not the random reel");
  assert.ok(existsSync(path.join(DIST, "assets", "edge", "hero-fd.mp4")), "and the film ships");
  // the name appears only in the use case and the naming sentence
  const page = visible(industry().match(/<main[\s\S]*<\/main>/)[0])
    .replace(useCase(), "")
    .replace(/The one company this page names, FireDefense Systems, is named with its agreement\./, "");
  assert.doesNotMatch(page, /FireDefense/i, "no other place on the industrial page names FireDefense");
  for (const p of ["research.html", "company.html"]) {
    assert.doesNotMatch(visible(read(p).match(/<main[\s\S]*<\/main>/)[0]), /FireDefense/i, `${p} does not name FireDefense`);
  }
});

test("wildfire: the market is not mistaken for defense", () => {
  hasCopy();
  assert.ok(stripLink("wildfire"), "the wildfire strip entry exists");
  assert.doesNotMatch(visible(stripLink("wildfire")), /defense/i);
  assert.doesNotMatch(title("wildfire"), /defense/i);
  assert.equal(title("defense"), "Defense sustainment");
  assert.equal(visible(stripLink("defense")), "Defense");
  assert.doesNotMatch(wildfireCopy(), /\bdefense\b/i, "no standalone 'defense' in wildfire copy");
  const glyph = (l) => l.match(/<svg[\s\S]*?<\/svg>/)?.[0].replace(/\s+/g, " ");
  assert.notEqual(glyph(stripLink("wildfire")), glyph(stripLink("defense")), "a different glyph from the shield");
});

test("wildfire: the customer-naming promise is reconciled, not broken", () => {
  const head = sectionHead();
  assert.match(head, /The industrial engagements are under NDA and not named here\./);
  assert.match(head, /The one company this page names, FireDefense Systems, is named with its agreement\./);
  const page = visible(industry().replace(/<figcaption class="photo-credit"[\s\S]*?<\/figcaption>/g, "")
    .match(/<main[\s\S]*<\/main>/)[0]);
  assert.doesNotMatch(page, /\b(customers?|clients?|partners?)\b[^.]*\b(include|such as|like|named)\b/i,
    "no other company is named as a customer or partner");
  assert.doesNotMatch(page, /customers (it|we) do(es)? not have|no customers|none yet/i);
});
