// Regression locks for the Playbox (features/web/playbox.feature) - the renamed
// Playground with LIVE per-station chat through the Tower's browser-session relay
// (features/relay/browser_session.feature). Static-content assertions over web/src,
// like console-tapes.test.mjs. Carries forward the four 2026-08-01 audit locks.
// Run: node --test test/playbox.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");

const js = read("js/playbox.js");
const html = read("playbox.html");
const nav = read("_partials/nav.html");
const footer = read("_partials/footer.html");

// ---------- the rename -------------------------------------------------------

test("playbox: the visitor-facing name is Playbox everywhere, never Playground", () => {
  for (const [name, s] of [["playbox.html", html], ["nav", nav], ["footer", footer],
    ["models.html", read("models.html")], ["manual.html", read("manual.html")]]) {
    assert.ok(!/Playground/.test(s.replace(/<!--[\s\S]*?-->/g, "")),
      `${name} still says "Playground" in visitor-facing content`);
  }
  assert.ok(nav.includes('href="/playbox.html"'), "nav must point at /playbox.html");
  assert.ok(footer.includes('href="/playbox.html"'), "footer must point at /playbox.html");
});

test("playbox: the old playground address keeps working via a redirect stub", () => {
  const stub = read("playground.html");
  assert.ok(stub.includes("url=/playbox.html"), "the stub must refresh to /playbox.html");
  assert.ok(stub.includes('rel="canonical"') && stub.includes("/playbox.html"),
    "the stub must hand its search identity to the Playbox");
  assert.ok(stub.includes("noindex"), "the legacy address must not be indexed");
  assert.ok(stub.includes('href="/playbox.html"'), "the stub needs a real link, not only the refresh");
});

test("playbox: the social card and metadata use the Playbox name and address", () => {
  assert.ok(html.includes('title="Playbox'), "page title must say Playbox");
  assert.ok(html.includes('ogurl="https://rogerai.fm/playbox.html"'), "og:url must be the playbox address");
  assert.ok(!/ogtitle="[^"]*Playground/.test(html), "the social card must not say Playground");
});

// ---------- live station chat ------------------------------------------------

test("playbox: station chat goes through the Tower relay, credentialed and streamed", () => {
  assert.ok(js.includes('"/v1/chat/completions"'), "station chat must call the relay endpoint");
  const relayCall = js.slice(js.indexOf('"/v1/chat/completions"'), js.indexOf('"/v1/chat/completions"') + 400);
  assert.ok(relayCall.includes('credentials: "include"'),
    "the relay call must send the session cookie (credentialed)");
  assert.ok(js.includes("stream: true"), "the reply must be requested as a stream");
  assert.ok(js.includes("getReader"), "the reply must be written as it arrives");
});

test("playbox: the transcript labels a station reply with the model, not PING", () => {
  assert.ok(js.includes("stationLabel"), "a station label distinct from PING is required");
  assert.ok(js.includes('model ? stationLabel(model) : "PING"'),
    "the wait/reply line must be labeled by the tuned model");
});

test("playbox: a paid station signed out yields the sign-in invitation, composer hidden", () => {
  assert.ok(html.includes('id="pgSignInInvite"'), "the invite block must exist in the page");
  assert.ok(js.includes("pgSignInInvite"), "selectModel must toggle the invitation");
  assert.ok(js.includes("form.hidden = !!needsLogin"), "the composer must yield when sign-in is needed");
});

test("playbox: Ping remains the default operator when nothing is tuned", () => {
  assert.ok(js.includes('"/concierge"'), "the concierge path must remain");
  assert.ok(js.includes(": pingSend("), "an untuned send must route to Ping");
});

test("playbox: the honest-limit CLI-handoff era is over, but the CLI path survives", () => {
  assert.ok(!html.includes("isn&rsquo;t live yet"), "the page must not claim station chat is unavailable");
  assert.ok(js.includes("roger chat --model"), "the copyable terminal command stays offered");
});

// ---------- honest states ----------------------------------------------------

test("playbox: quiet band, unreachable broker, and relay errors stay distinct and honest", () => {
  assert.ok(js.includes("the band is quiet"), "the quiet band has its own state");
  assert.ok(js.includes("couldn't reach the broker"), "the unreachable broker has its own state");
  assert.ok(js.includes("relayErrorText"), "relay errors are surfaced as themselves");
  assert.ok(js.includes("slow down"), "a 429 tells the visitor to slow down");
});

// ---------- carried-forward audit locks (2026-08-01) --------------------------

test("playbox: no /market fetch - /discover is the one directory source", () => {
  assert.ok(!js.includes('"/market"'), "playbox.js still fetches /market");
  assert.ok(js.includes('fetchJSON("/discover"'), "the /discover fetch is gone");
});

test("playbox: broker-unreachable branch is reachable (no null-swallow on /discover)", () => {
  assert.ok(!/\/discover"[^\n]*catch\(function \(\) \{ return null; \}\)/.test(js),
    "the /discover fetch swallows errors into null, making the off-state unreachable");
});

test("playbox: the edge canvas never calls draw() outside the rAF loop", () => {
  assert.ok(!js.includes("draw(performance.now())"),
    "a direct draw(performance.now()) call stacks extra rAF loops beside the beacon loop");
  assert.ok(js.includes("function startDraw()"), "startDraw() (the cancel-then-schedule entry) is gone");
});

// ---------- the bench (features/web/playbox_bench.feature) --------------------

const css = read("styles/playbox.css");

test("bench: the deck is one faceplate with plate and MODE selector", () => {
  assert.ok(html.includes('class="pg-deck pg-bench"'), "the deck must carry the bench faceplate class");
  assert.ok(html.includes("pg-bench__plate"), "the maker's plate must exist");
  assert.ok(html.includes('class="pg-tabs__mode"'), "the tablist needs its MODE label");
  assert.ok(html.includes('role="tablist"'), "tab semantics must be unchanged");
});

test("bench: the S-meter needle is driven by the directory's own signal numbers", () => {
  assert.ok(html.includes('id="pgSMeterNeedle"'), "the needle element must exist");
  assert.ok(js.includes("function meterSignal()"), "the meter reads from STATE.bands");
  assert.ok(js.includes("b.signal"), "the meter uses the same signal field the rows draw");
  assert.ok(js.includes("updateSMeter()"), "renderDirectory/selectModel must refresh the meter");
  assert.ok(!js.includes("Math.random"), "the meter must never invent a value");
});

test("bench: the transcript is a ruled logbook with UTC times", () => {
  assert.ok(css.includes("repeating-linear-gradient"), "the logbook rules must be drawn");
  assert.ok(js.includes("pg-line__ts"), "each entry must carry its timestamp");
  assert.ok(js.includes("getUTCHours"), "logbook time is UTC, the operator's convention");
});

test("bench: the send control is the key, and still a real submit button", () => {
  assert.ok(html.includes("pg-send--key"), "the key styling must be applied");
  assert.ok(html.includes(">KEY</span>"), "the KEY cap marking must exist");
  assert.ok(/pg-send--key[^>]*type="submit"|type="submit"[^>]*>\s*<span class="pg-send__cap"/.test(html.replace(/\n\s*/g, " ")),
    "the key must remain a type=submit button");
});

test("bench: reduced motion freezes the needle at its true value", () => {
  const rm = css.slice(css.indexOf("prefers-reduced-motion"));
  assert.ok(rm.includes(".pg-smeter__needle { transition: none; }"),
    "the reduced-motion block must stop the needle animation");
});

test("playbox: the unreferenced nano-samples file does not ship", () => {
  assert.ok(!existsSync(path.join(SRC, "data/playground-nano-samples.jsonl")),
    "playground-nano-samples.jsonl is back but nothing loads it");
  assert.ok(html.includes('id="pgEdgeData"'), "the inline Edge dataset is gone");
});
