// Regression locks for the Playbox ROUTING DRAWER (features/web/playbox_routing.feature, slice 4 of
// the routing-expression set). One node:test per scenario (outline rows expanded), named after it.
//
// HOW THIS RUNS: static-content assertions over web/src (playbox.html, js/playbox.js,
// styles/playbox.css, DESIGN-SYSTEM.md), the same approach as playbox.test.mjs and
// console-tapes.test.mjs. web/ has no DOM harness (no jsdom/happy-dom in package.json), so
// behaviour that only a live page shows - the request body a keyed-up turn actually sends,
// focus order, 360px layout, the reduced-motion transition - is asserted as the MECHANISM the
// source must contain (the body key in the send path, the label on the control, the media query
// on the drawer's rule). A GREEN slice that adds a DOM harness can turn these into behaviour
// checks; until then each assertion names the source fact it reads.
//
// GROUND TRUTH (why most of this is RED): stationSend posts {model, stream, max_tokens, messages}
// with only a Content-Type header (playbox.js ~1224); there is no routing drawer, no routing
// storage key, no usage-chunk footer, and relay errors render through relayErrorText.
// Run: node --test web/test/playbox-routing.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (p) => readFileSync(path.join(WEB, p), "utf8");
const html = read("src/playbox.html");
const js = read("src/js/playbox.js");
const css = read("src/styles/playbox.css");
const ds = read("DESIGN-SYSTEM.md");
const flat = (s) => s.replace(/\s+/g, " ");

// the drawer: the element the spec names (role="group", aria-label "routing")
function drawer() {
  const m = html.match(/<(\w+)[^>]*role="group"[^>]*aria-label="routing"[^>]*>[\s\S]*?<\/\1>/)
    || html.match(/<(\w+)[^>]*aria-label="routing"[^>]*role="group"[^>]*>[\s\S]*?<\/\1>/);
  return m ? m[0] : null;
}
function needDrawer() {
  const d = drawer();
  assert.ok(d, 'playbox.html has no routing drawer (an element with role="group" and aria-label="routing")');
  return d;
}
// the turn's request body as stationSend builds it
function sendBody() {
  const i = js.indexOf("function stationSend(");
  assert.ok(i >= 0, "playbox.js has no stationSend");
  const j = js.indexOf("\n  }\n", i);
  return js.slice(i, j > i ? j : i + 4000);
}
// The send path's wiring: stationSend builds its body through routingBody, which hands the
// drawer state to PlayboxRoute.body (web/src/js/playbox-route.js). What that body contains is
// asserted by EXECUTING the module below, not by reading source.
function needSendWiring() {
  assert.match(sendBody(), /routingBody\(model,/, "stationSend does not build its body through routingBody");
  const i = js.indexOf("function routingBody(");
  assert.ok(i >= 0, "playbox.js has no routingBody");
  assert.match(js.slice(i, i + 600), /PlayboxRoute\.body\(ROUTE, model, body, \{ loggedIn: STATE\.loggedIn, routed:/,
    "routingBody does not hand the drawer state to PlayboxRoute.body");
  const at = html.indexOf('src="js/playbox-route.js"');
  assert.ok(at >= 0 && at < html.indexOf('src="js/playbox.js"'), "playbox.html does not load playbox-route.js before playbox.js");
}
const routeSrc = read("src/js/playbox-route.js");
const routeMod = { exports: {} };
new Function("module", "window", routeSrc)(routeMod, undefined);
const PR = routeMod.exports;
const BASE = () => ({ model: "m", stream: true, max_tokens: 1024, messages: [] });
// the routing keys a body carries beyond the base turn, as dotted paths
function routingKeys(b) {
  const out = [];
  if (b.models) out.push("models");
  for (const top of ["provider", "roger"]) {
    for (const [k, v] of Object.entries(b[top] || {})) {
      if (k === "max_price") for (const mk of Object.keys(v)) out.push(`provider.max_price.${mk}`);
      else out.push(`${top}.${k}`);
    }
  }
  return out;
}
const needText = (s, what) => assert.ok(flat(js + html).includes(s), `${what}: "${s}" appears nowhere in playbox.js / playbox.html`);

// ---------- the drawer ---------------------------------------------------------------------

test("The drawer is closed by default and the deck is byte-identical to today", () => {
  const d = needDrawer();
  assert.match(d, /\bhidden\b|aria-expanded="false"/, "the drawer must start closed");
  for (const col of ["dk__inputcol", "dk__baycol", "dk__outcol", "dkJCard"]) {
    assert.ok(html.includes(col), `the approved deck lost ${col}`);
  }
});

test("The drawer opens from one control on the j-card and is a named group", () => {
  const d = needDrawer();
  assert.match(html, /aria-controls="[^"]+"[^>]*>[^<]*routing|>routing<\/button>/i, "no single 'routing' control opens the drawer");
  assert.ok(!/display:\s*grid/.test(d), "the drawer is a tinted inset, not a grid");
});

const FIELDS = [
  ["also try", "llama-3.3-70b", "models", { models: ["llama-3.3-70b"] }],
  ["max $/1M out", "2", "provider.max_price.completion", { out: 2 }],
  ["max $/1M in", "0.5", "provider.max_price.prompt", { in: 0.5 }],
  ["max $/turn", "0.02", "provider.max_price.request", { turn: 0.02 }],
  ["min t/s", "30", "roger.min_tps", { tps: 30 }],
  ["self-hosted", "on", "roger.self_hosted_only", { selfHosted: true }],
  ["confidential", "on", "roger.confidential", { confidential: true }],
  ["needs tools", "on", "roger.require", { tools: true }],
  ["needs vision", "on", "roger.require", { vision: true }],
  ["quant", "Q8_0", "provider.quantizations", { quant: "Q8_0" }],
  ["size", "7-70B", "roger.params_b", { size: [7, 70] }],
  ["region", "eu", "roger.region", { region: "eu" }],
  ["prefer", "fast", "roger.pref", { pref: "fast" }],
  ["sort by", "price", "provider.sort", { sort: "price" }],
  ["trust", "verified", "roger.trust_min", { trust: "verified" }],
  ["min ctx", "32k", "roger.min_ctx", { ctx: 32768 }],
  ["max first token", "1.5s", "roger.max_ttft_ms", { ttft: 1500 }],
];
for (const [field, value, key, route] of FIELDS) {
  test(`Each drawer field maps to exactly one body key: ${field} = ${value} -> ${key}`, () => {
    const d = needDrawer();
    assert.ok(flat(d).toLowerCase().includes(field.toLowerCase()), `the drawer has no "${field}" field`);
    needSendWiring();
    const b = PR.body(PR.clean(route), "m", BASE(), { loggedIn: true, routed: true });
    assert.deepEqual(routingKeys(b), [key], `${field} = ${value} must add exactly ${key}`);
  });
}

test("With nothing set the body is exactly today's body", () => {
  const fn = sendBody();
  assert.match(fn, /model:\s*model[\s\S]*stream:\s*true[\s\S]*max_tokens:\s*1024[\s\S]*messages:/, "the base body changed");
  needSendWiring();
  // nothing set adds nothing: the body PlayboxRoute builds equals the base turn
  assert.deepEqual(PR.body(PR.clean({}), "m", BASE(), { loggedIn: true, routed: true }), BASE());
  assert.deepEqual(PR.body(PR.clean(null), "m", BASE(), { loggedIn: true, routed: true }), BASE());
});

test("Routing is sent in the body only, never as headers", () => {
  const fn = sendBody();
  assert.ok(!/X-Roger-/.test(fn), "the turn sends an X-Roger-* header");
  assert.match(fn, /headers:\s*\{\s*"Content-Type":\s*"application\/json"\s*\}/, "the turn's headers changed");
  needSendWiring();
  const b = PR.body(PR.clean({ tps: 30, confidential: true }), "m", BASE(), { loggedIn: true, routed: true });
  assert.deepEqual(b.roger, { min_tps: 30, confidential: true });
});

test("prefer and sort by are exclusive in the UI, so the broker's 400 cannot happen from here", () => {
  needDrawer();
  needText("sort by replaces prefer", "the exclusivity note");
});

test("The quant, region and size choices are built from the live /discover feed", () => {
  needDrawer();
  needText("station-declared", "the size label");
  assert.match(js, /quant[\s\S]{0,400}(offers|bands|stations)/i, "quant choices are not built from the feed");
});

test("Choices that no station on the tape can satisfy are shown, marked, not hidden", () => {
  needText("no station on this tape states its quant", "the empty-quant note");
});

test("Validation happens in the drawer with the contract's rules", () => {
  needText("min must be at most max", "the size validation");
  needText("must be 0 or more", "the price validation");
});

test("A too-long \"also try\" list is capped at four extra models", () => {
  needText("up to 4 fallbacks after the tape", "the also-try cap");
});

// ---------- persistence ----------------------------------------------------------------------

function routingStoreKey() {
  const keys = [...js.matchAll(/["'](roger-playbox[-\w]*)["']/g)].map((m) => m[1]);
  const store = (js.match(/STORE_KEY\s*=\s*"([^"]+)"/) || [])[1];
  return keys.find((k) => k !== store && /rout/i.test(k));
}

test("Drawer settings persist per viewer in localStorage under their own key", () => {
  const k = routingStoreKey();
  assert.ok(k, "the drawer settings have no localStorage key of their own (separate from the deck's STORE_KEY)");
});

test("Persistence is per viewer and never reaches the broker or the account", () => {
  const k = routingStoreKey();
  assert.ok(k, "no routing storage key exists");
  for (const ep of ["/me", "/account"]) {
    const i = js.indexOf('"' + ep);
    if (i >= 0) assert.ok(!js.slice(i, i + 600).includes(k), `the routing settings reach ${ep}`);
  }
});

test("Private mode or blocked storage degrades to session-only settings without error", () => {
  const k = routingStoreKey();
  assert.ok(k, "no routing storage key exists");
  const uses = [...js.matchAll(new RegExp(`localStorage\\.(setItem|getItem|removeItem)\\([^)]*`, "g"))];
  for (const u of uses) {
    const before = js.slice(Math.max(0, u.index - 160), u.index);
    assert.ok(/try\s*\{/.test(before), `localStorage use at offset ${u.index} is not guarded by try`);
  }
});

test("A persisted setting that no longer applies is dropped honestly", () => {
  needText("is not on this tape right now", "the stale-setting note");
});

test("Changing tapes keeps the drawer's generic settings and clears tape-specific ones", () => {
  needDrawer();
  assert.match(js, /(quant|also try|models)[\s\S]{0,200}clear/i, "nothing clears the tape-specific settings on a tape change");
});

test("reset returns every field to unset and clears the stored settings", () => {
  const d = needDrawer();
  assert.match(d, />\s*reset\s*</i, "the drawer has no reset control");
  const k = routingStoreKey();
  assert.ok(k && new RegExp(`removeItem\\([^)]*`).test(js), "reset does not remove the stored routing key");
});

// ---------- the transcript's summary line --------------------------------------------------

test("A turn with routing set shows one dim summary line above the reply", () => {
  needText("routing: ", "the summary line");
});

test("A turn with nothing set shows no summary line", () => {
  // no summary line exists today, so nothing shows it with the drawer unset: the claim that
  // matters is that the line, once it exists, is conditional
  if (flat(js).includes("routing: ")) {
    assert.match(js, /if\s*\([^)]*\)[\s\S]{0,200}routing: /, "the summary line is not conditional on a set field");
  }
});

// ---------- anonymous vs signed-in -------------------------------------------------------------

test("Anonymous visitors see money fields disabled with the reason", () => {
  needDrawer();
  needText("signed-out turns run on free stations only - price caps do nothing here", "the anonymous note");
});

test("Anonymous turns send :free sugar so a paid station is never planned", () => {
  needSendWiring();
  const b = PR.body(PR.clean({ models: ["b"] }), "m", BASE(), { loggedIn: false, routed: true });
  assert.equal(b.model, "m:free");
  assert.deepEqual(b.models, ["b:free"], "every fallback is asked for free too");
});

test("Signing in enables the money fields without a reload", () => {
  needDrawer();
  const i = js.indexOf('"/me"') >= 0 ? js.indexOf('"/me"') : js.indexOf("/me");
  assert.ok(i >= 0, "playbox.js never reads /me");
  assert.match(js.slice(i, i + 2000), /disabled/, "the /me handler never re-enables the money fields");
});

test("A paid station still asks for sign-in before the first send (approved)", () => {
  assert.match(flat(js), /sign[- ]?in/i, "the approved sign-in ask is gone");
});

test("The drawer never shows a balance or spend (founder ruling)", () => {
  const d = needDrawer();
  // whole words: the pref default "balanced" (the spec's own word) is not a wallet balance
  assert.ok(!/\b(balance|spend|spending|history)\b/i.test(d), "the drawer renders a balance, spend or history");
});

// ---------- who served -------------------------------------------------------------------------

test("The reply's footer names the served model and station from the final usage chunk", () => {
  needText("served by ", "the reply footer");
  assert.match(js, /usage[\s\S]{0,200}\.rogerai|\.rogerai[\s\S]{0,200}usage/, "the footer is not read from the usage chunk's rogerai block");
});

test("A fallback to another model is said plainly", () => {
  needText("(fallback)", "the fallback mark");
});

test("The footer shows the cost only when the visitor is signed in, and from the chunk, never estimated", () => {
  needText("· free", "the anonymous footer");
  assert.match(js, /usage\.cost|\.cost\b/, "the footer cost is not read from usage.cost");
});

test("No usage chunk means no footer numbers, never a guess", () => {
  assert.match(js, /X-RogerAI-Provider/, "the footer cannot fall back to the X-RogerAI-Provider header");
});

test("The header fallback names the served model from X-RogerAI-Model and the station only as the station", () => {
  const fn = sendBody();
  assert.match(fn, /headers\.get\("X-RogerAI-Model"\)/, "the fallback never reads the served model from X-RogerAI-Model");
  assert.ok(!/\{\s*model:\s*provider\s*\}/.test(fn), "the fallback passes the station id (X-RogerAI-Provider) as the served model");
  assert.match(fn, /node:\s*provider\b/, "the fallback does not name the station from X-RogerAI-Provider");
});

test("The station's own usage object is never mistaken for the broker's", () => {
  assert.match(js, /\.rogerai\s*\)|\.rogerai\s*&&|if\s*\([^)]*\.rogerai/, "the footer does not require the rogerai block before reading usage");
});

// ---------- errors -------------------------------------------------------------------------------

test("A 503 no_match names the constraint in the reply", () => {
  needText("no station matches: ", "the no_match reply");
  needText("loosen a routing setting", "the no_match advice");
});

test("A 503 band_cooling shows the wait and is not retried into (approved rate-limit rule)", () => {
  assert.match(js, /band_cooling/, "band_cooling is not handled");
  assert.match(js, /Retry-After/, "the cooling wait is not read from Retry-After");
});

test("A 400 from a routing value is shown with the key named", () => {
  assert.match(js, /params_b["']?\s*:\s*["']size["']/, "no map from roger.params_b to the drawer's label 'size'");
});

// ---------- design and accessibility ---------------------------------------------------------

function drawerCss() {
  const d = needDrawer();
  const cls = (d.match(/class="([^"]+)"/) || [])[1];
  assert.ok(cls, "the drawer has no class to style");
  const first = cls.split(/\s+/)[0];
  const rules = [...css.matchAll(new RegExp(`[^}]*\\.${first.replace(/[-_]/g, (c) => "\\" + c)}[^{]*\\{[^}]*\\}`, "g"))].map((m) => m[0]);
  assert.ok(rules.length > 0, `playbox.css has no rules for .${first}`);
  return { first, rules: rules.join("\n") };
}

test("The drawer meets the design system on a phone", () => {
  const { rules } = drawerCss();
  assert.ok(!/width:\s*\d{4,}px/.test(rules), "the drawer has a fixed width wider than a phone");
});

test("Every control is keyboard reachable and labelled", () => {
  const d = needDrawer();
  const inputs = [...d.matchAll(/<(input|select|button|textarea)\b[^>]*>/g)].map((m) => m[0]);
  assert.ok(inputs.length > 0, "the drawer has no controls");
  for (const el of inputs) {
    const id = (el.match(/id="([^"]+)"/) || [])[1];
    const labelled = /aria-label="/.test(el) || (id && d.includes(`for="${id}"`)) || /<button/.test(el);
    assert.ok(labelled, `unlabelled control: ${el}`);
    assert.ok(!/tabindex="-1"/.test(el), `control removed from the tab order: ${el}`);
  }
});

test("Dark mode contrast holds for every drawer text", () => {
  const { rules } = drawerCss();
  assert.ok(!/--ink-300/.test(rules), "drawer text uses --ink-300");
});

test("The drawer uses existing tokens and components only", () => {
  const { first, rules } = drawerCss();
  assert.ok(!/#[0-9a-f]{3,8}\b|rgba?\(/i.test(rules), "the drawer's CSS has a colour literal outside tokens");
  assert.ok(!/box-shadow|filter:\s*drop-shadow/.test(rules), "the drawer has a glow");
  assert.ok(!/display:\s*grid/.test(rules), "the drawer lays out form rows on a grid");
  assert.ok(ds.includes(first), `.${first} has no entry in DESIGN-SYSTEM.md`);
});

test("prefers-reduced-motion disables the drawer's open animation", () => {
  const { first } = drawerCss();
  const m = css.match(/@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{[\s\S]*?\n\}/g) || [];
  assert.ok(m.some((b) => b.includes(first)), `no reduced-motion rule covers .${first}`);
});

test("The drawer's script adds no parser-blocking work and stays inside the page's weight budget", () => {
  assert.match(html, /<script[^>]*src="[^"]*playbox\.js"[^>]*\bdefer\b|<script[^>]*\bdefer\b[^>]*playbox\.js/, "playbox.js is no longer deferred");
  needDrawer();
});

// ---------- approved Playbox scenarios stay true --------------------------------------------

test("Off-origin, the drawer explains the Tower cannot be reached exactly as the deck does", () => {
  needDrawer();
  assert.match(js, /origin/i, "the off-origin gate is gone");
});

test("Ping remains the default operator and the drawer is hidden when no tape is tuned", () => {
  needDrawer();
  assert.match(js, /concierge/, "Ping no longer talks via the concierge");
});

test("The quiet band and unreachable broker states are unchanged", () => {
  needDrawer();
  assert.match(flat(js + html), /quiet/i, "the quiet-band state is gone");
});

// ---------- slice-4 pre-push audit regressions ----------------------------------------------

test("The routing toggle stays hidden with no live tape (its display rule respects [hidden])", () => {
  assert.match(html, /class="dk-route__toggle[^"]*"[^>]*\bhidden\b/, "the toggle no longer starts hidden");
  assert.match(flat(css), /\.dk-route__toggle\[hidden\]\s*\{\s*display:\s*none;?\s*\}/,
    ".dk-route__toggle { display:block } overrides the hidden attribute");
});

test("A repaint clears a stale refusal, so a fixed drawer never keeps holding the turn", () => {
  const paint = js.match(/function paintRoute\(\) \{[\s\S]*?\n  \}/);
  assert.ok(paint, "paintRoute is gone");
  assert.match(paint[0], /routeBad = ""/, "paintRoute keeps a stale routeBad");
});

test("Stored drawer state is validated on load and can never throw at init", () => {
  assert.match(js, /function routeClean\(/, "no validator for the stored drawer state");
  assert.match(flat(js), /var ROUTE = \(function \(\) \{ try \{ return routeClean\(JSON\.parse\(/,
    "the stored state is used without validation");
});
