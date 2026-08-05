// Regression locks for the Playbox CASSETTE DECK (features/web/playbox_deck.feature):
// one view - input console / cassette bay / output monitor - with live chat through
// the Tower's browser-session relay (features/relay/browser_session.feature) and
// labelled RECORDED contract replays. Static-content assertions over web/src, like
// console-tapes.test.mjs. Carries the rename, honesty, and audit locks forward.
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
const css = read("styles/playbox.css");
const nav = read("_partials/nav.html");
// Prose wraps across lines; a copy assertion must not break on where it wrapped.
const flat = (s) => s.replace(/\s+/g, " ");
const htmlFlat = flat(html);
const footer = read("_partials/footer.html");

// ---------- the rename (carried) ---------------------------------------------

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

// ---------- one view (deck spec) ---------------------------------------------

test("deck: input, cassette, and output sit side by side with no tab navigation", () => {
  for (const col of ["dk__inputcol", "dk__baycol", "dk__outcol"]) {
    assert.ok(html.includes(col), `the ${col} column must exist`);
  }
  assert.ok(!html.includes('role="tabpanel"'), "no tab panels - the deck is one view");
  assert.ok(html.includes("pg-bench__plate"), "the maker's plate survives on the deck");
});

test("deck: the rotary selector offers the six input kinds, one active at a time", () => {
  for (const kind of ["text", "voice", "image", "tool", "embed", "guard"]) {
    assert.ok(html.includes(`data-kind="${kind}"`), `the ${kind} position must exist`);
  }
  assert.equal((html.match(/class="dk__pos"/g) || []).length, 6, "exactly six positions");
  assert.ok(html.includes('role="radiogroup"'), "the rotary selector uses one-choice semantics");
  assert.equal((html.match(/class="dk__pos"[^>]*aria-checked="true"/g) || []).length, 1,
    "exactly one position starts active");
  assert.ok(js.includes("function selectKind"), "the selector drives one surface at a time");
  assert.ok(js.includes('e.key !== "ArrowRight"') && js.includes('btn.setAttribute("tabindex"'),
    "the rotary positions use roving focus and arrow-key control");
});

test("deck: the cassette bay has reels, a label, capability lamps, and transport", () => {
  for (const id of ["dkCassette", "dkReelL", "dkReelR", "dkTapeName", "dkTapeCaps",
    "dkPlay", "dkStop", "dkEject", "dkShelf"]) {
    assert.ok(html.includes(`id="${id}"`), `${id} must exist`);
  }
  assert.ok(css.includes("dk-spin"), "the reels spin while playing");
  assert.ok(css.includes("dk-load"), "loading a tape animates into the bay");
  assert.ok(js.includes('cas.setAttribute("data-state", "loading")'), "loadTape runs the load animation");
});

test("deck: the shelf presents the network and the Wave family as honest groups", () => {
  assert.ok(js.includes('{ group: "ON AIR", entries: onAir }'), "the live network is its own group");
  assert.ok(js.includes('"WAVE FAMILY"'), "the Wave family is its own group");
  assert.ok(js.includes("FAMILY_SPINES"), "the family placeholders exist");
  assert.ok(js.includes("TRAINED · OFF AIR"), "Micro is trained but off air, said plainly");
  assert.ok(js.includes('chip: "PLANNED"'), "Core is a planned band, said plainly");
  assert.ok(js.includes('aria-disabled'), "placeholders cannot load and say why");
  assert.ok(!js.includes('"AVAILABLE"'), "no placeholder ever claims availability");
});

test("deck: the shelf marks live stations LIVE and the demo tape RECORDED", () => {
  assert.ok(js.includes('t.demo ? "RECORDED" : "LIVE"'), "every spine carries its honesty chip");
  assert.ok(js.includes("the band is quiet - demo tapes only"),
    "a quiet band leaves the demo tapes honestly labelled");
  assert.ok(js.includes("DEMO_TAPE"), "the Wave demo tape is on the shelf");
});

test("deck: selecting a kind the tape lacks loads the tape that carries it", () => {
  assert.ok(js.includes("recorded kinds pull the demo tape in"),
    "the auto-load rule is implemented");
  assert.ok(js.includes("lastLiveTape || PING_TAPE"), "text/voice pull a live tape back");
});

// ---------- live playback (carried relay locks) -------------------------------

test("deck: station chat goes through the Tower relay, credentialed and streamed", () => {
  assert.ok(js.includes('"/v1/chat/completions"'), "station chat must call the relay endpoint");
  const relayCall = js.slice(js.indexOf('"/v1/chat/completions"'), js.indexOf('"/v1/chat/completions"') + 500);
  assert.ok(relayCall.includes('credentials: "include"'),
    "the relay call must send the session cookie (credentialed)");
  assert.ok(js.includes("stream: true"), "the reply must be requested as a stream");
  assert.ok(js.includes("getReader"), "the reply must be written as it arrives");
  assert.ok(js.includes('"/concierge"'), "Ping remains the always-on tape");
  assert.ok(js.includes("roger chat --model"), "the copyable terminal command stays offered");
});

test("deck: stop is real - the in-flight stream is aborted", () => {
  assert.ok(js.includes("abortCtl.abort()"), "STOP must abort the live stream");
  assert.ok(js.includes('signal: abortCtl ? abortCtl.signal : undefined'),
    "both live paths must carry the abort signal");
  assert.ok(js.includes("clearTimeout(typeTimer)"), "STOP must halt the recorded printer too");
  assert.ok(js.includes("playbackSerial"), "a stale aborted turn must not reset a newer transport");
});

test("deck: play only arms when the active input is ready", () => {
  assert.ok(js.includes('$("dkTextInput")') && js.includes("STATE.card.kind === STATE.kind"),
    "text needs content and preset modes need a selected card");
  assert.ok(js.includes('$("dkPlay").disabled = !ready || STATE.playing'),
    "transport readiness, not merely a loaded tape, controls PLAY");
});

test("deck: a paid tape signed out yields the sign-in invitation and play disables", () => {
  assert.ok(html.includes('id="pgSignInInvite"'), "the invite block must exist");
  assert.ok(js.includes("needsLogin"), "refreshTransport must gate play on sign-in");
  assert.ok(css.includes(".pg-invite[hidden] { display: none; }"),
    "the invite's display must never defeat its hidden attribute");
});

test("deck: voice input is preset utterances whose words are the input", () => {
  assert.ok(js.includes("VOICE_CARDS"), "the voice presets exist");
  assert.ok(js.includes("playLive(STATE.card.words, true)"), "playing a card sends its transcript live");
  assert.ok(js.includes('spoken ? "MIC" : "YOU"'), "the log labels spoken input");
});

// ---------- recorded playback -------------------------------------------------

test("deck: image, tool, embed, and guard replay labelled certified contracts", () => {
  assert.ok(js.includes("IMAGE_CARDS"), "preselected scenes by category exist");
  assert.ok(js.includes("EMBED_CARDS"), "the devices the model rides in exist");
  for (const dev of ["pump", "sensor", "esp32"]) {
    assert.ok(js.includes(`"${dev}"`), `the ${dev} device is represented`);
  }
  assert.ok(js.includes("RECORDED"), "recorded output is labelled RECORDED");
  assert.ok(js.includes("Recorded demonstration"), "image readings are labelled demonstrations");
  assert.ok(html.includes('id="dkPrinter"'), "the printer renders the contract replays");
  assert.ok(css.includes(".dk__printer[hidden], .pg-log[hidden] { display: none; }"),
    "printer/log visibility must never defeat the hidden attribute");
});

test("deck: every recorded device carries its real fixed device prompt", () => {
  assert.ok(js.includes("DEVICE_PROMPTS"), "the prompts ship in the page code");
  assert.ok(js.includes("offline alarm-management analyzer"),
    "the excerpt must be the real production framing, not paraphrase");
  assert.ok(js.includes("NO authority to actuate"),
    "the safety device's framing must carry the no-actuation clause");
  assert.ok(js.includes("Device prompt excerpt (ships with the device)"),
    "the printer accurately labels the real prompt excerpt shown with the output");
  assert.ok(htmlFlat.includes("ship as one unit"), "the model+prompt unit principle is stated");
});

test("deck: ESCALATE renders as a first-class good outcome", () => {
  assert.ok(js.includes('"escalate · right call"'), "ESCALATE must read as the right call");
  assert.ok(js.includes('"pg-verdict pg-verdict--ok"'), "the verdict chip uses the positive style");
  assert.ok(!js.includes('? "warn" : "ok"'), "no branch demotes ESCALATE to a warning");
});

// ---------- real media: see it, hear it, bring your own -----------------------

test("media: the image viewer shows the actual frame the model is shown", () => {
  assert.ok(html.includes('id="dkImageStage"'), "the viewer stage must exist");
  assert.ok(js.includes("function sceneSVG"), "scenes are drawn, not glyph placeholders");
  for (const scene of ["gauge", "plate", "thermal"]) {
    assert.ok(js.includes(`${scene}:`), `the ${scene} scene must be drawn`);
  }
  assert.ok(js.includes("function showScene"), "picking a scene renders it into the viewer");
  assert.ok(css.includes(".dk__scenesvg"), "the drawn scene has its own styling");
});

test("media: the drawn gauge agrees with the reading its output claims", () => {
  // 0 lower-left, 25 lower-right: 12.5 barg is straight up. A frame that disagrees
  // with its own certified output would be a quiet lie.
  assert.ok(js.includes('"reading":12.5'), "the gauge output claims 12.5");
  assert.ok(js.includes('x1="160" y1="100" x2="160" y2="42"'),
    "the needle must point at mid-scale for a mid-scale reading");
});

test("media: voice is spoken by a real station, never a browser synthesizer", () => {
  assert.ok(js.includes('"/v1/audio/speech"'), "hearing a card calls the network TTS relay");
  assert.ok(!js.includes("speechSynthesis"), "the browser's own synthesizer must never stand in");
  assert.ok(js.includes("No voice station is on air"), "with no voice station the deck says so");
  assert.ok(js.includes("function voiceStation"), "the voice comes from a station on the band");
});

test("media: your own recording is transcribed on the network before the model hears it", () => {
  assert.ok(js.includes("MediaRecorder"), "the mic records in the page");
  assert.ok(js.includes("/v1/audio/transcriptions?model="), "the recording goes to the STT relay");
  assert.ok(js.includes("no transcription station on air"), "with no STT station the deck says so");
  assert.ok(html.includes('id="dkMicBtn"'), "the recorder control exists");
});

// The 2026-08-01 push audit caught three media features that were provably dead in
// production while substring tests passed. These pin the REQUEST SHAPE and the
// browser permissions the code actually depends on - the facts that were wrong.

test("contract: STT routes on ?model= and posts the raw blob, never multipart", () => {
  // transcribeRelay reads the model from the QUERY STRING only and meters the raw
  // body bytes (cmd/rogerai-broker/audio.go). Multipart would route to "" -> 503,
  // and the node forces Content-Type: application/json upstream anyway.
  assert.ok(/\/v1\/audio\/transcriptions\?model=" \+ encodeURIComponent/.test(js),
    "the model must travel in the query string");
  assert.ok(!js.includes("FormData"), "a multipart body cannot route or reach the node");
  assert.ok(/body: blob/.test(js), "the raw audio blob is the body the broker meters");
});

test("contract: the security headers grant exactly what the deck needs", () => {
  const headers = read("_headers");
  // station TTS arrives as a blob: URL and is played with new Audio(...)
  if (js.includes("URL.createObjectURL")) {
    assert.match(headers, /media-src[^;]*blob:/,
      "playing station audio needs media-src blob: - default-src 'self' blocks it");
  }
  // the mic is used for your-own-voice transcription
  if (js.includes("getUserMedia")) {
    assert.match(headers, /Permissions-Policy:[^\n]*microphone=\(self\)/,
      "recording needs microphone=(self); microphone=() disables it for the origin");
  }
  // the relay host must stay reachable
  assert.match(headers, /connect-src[^;]*broker\.rogerai\.fm/, "the relay host must stay in connect-src");
});

test("contract: a visitor's own image is re-encoded under the relay's body limit", () => {
  // the relay reads at most 4 MiB; a raw phone photo would be truncated into an
  // invalid turn and 400.
  assert.ok(js.includes("function downscale"), "own images must be re-encoded before they travel");
  assert.ok(js.includes('toDataURL("image/jpeg"'), "re-encode to bounded JPEG, not a raw data URL");
  assert.ok(js.includes("downscale(String(rd.result)"), "the upload path runs through the downscaler");
});

test("contract: a paid vision station is a sign-in prompt, not an opaque failure", () => {
  assert.ok(js.includes("visionBands.filter(bandFree)"), "a free vision station is preferred");
  assert.ok(js.includes("needsSignIn"), "a paid-only vision station asks the visitor to sign in");
});

test("media: your own image (or a video frame) goes to a real vision station", () => {
  assert.ok(html.includes('id="dkImageFile"'), "the file control exists");
  assert.ok(html.includes('accept="image/*,video/*"'), "images and video are both accepted");
  assert.ok(js.includes("first frame"), "a video contributes one frame");
  assert.ok(js.includes("image_url"), "the frame travels as a real vision turn");
  assert.ok(js.includes("no vision station is on air"), "with no vision station the deck says so");
  assert.ok(js.includes("function playOwnImage"), "an own image is a live turn, not a replay");
});

test("media: the deck states plainly that everything on it is real", () => {
  assert.ok(htmlFlat.includes("Every model on this deck is real"), "the one honest line must be present");
  assert.ok(/LIVE<\/b>[\s\S]{0,240}RECORDED/.test(htmlFlat),
    "the note must keep the live-vs-recorded distinction, not claim everything is live");
  assert.ok(htmlFlat.includes("real GPUs"), "it names the hardware");
  assert.ok(!html.includes("How it routes"), "the old routing essay is gone");
  assert.ok(htmlFlat.includes("real RogerAI model"), "the hero says these are real models");
});

// ---------- shelf rows + the draggable bay ------------------------------------

test("shelf: the network and the Wave family get their own rows", () => {
  assert.ok(js.includes("dk__shelfrow"), "each group is a row");
  assert.ok(css.includes(".dk__shelfrow"), "rows are styled");
  assert.ok(js.includes("Wave family keeps its own row"), "the reason is recorded in code");
  assert.ok(css.includes(".dk__shelfstrip"), "each row scrolls independently");
});

test("bay: the cassette can be thrown sideways to change tapes, and by keyboard too", () => {
  assert.ok(js.includes("pointerdown"), "the bay is draggable");
  assert.ok(js.includes("function neighbourTape"), "a throw moves along the shelf order");
  assert.ok(js.includes("ArrowLeft"), "arrow keys are the keyboard equivalent");
  assert.ok(css.includes("dk-swap-left") && css.includes("dk-swap-right"),
    "the new tape flies in from the side thrown toward");
  assert.ok(js.includes("a nudge is not a throw"), "a small drag must not change the tape");
});

// ---------- honest states + naming (carried) ----------------------------------

test("deck: quiet band, unreachable broker, and relay errors stay distinct and honest", () => {
  assert.ok(js.includes("the band is quiet"), "the quiet band has its own state");
  assert.ok(js.includes("couldn't reach the broker"), "the unreachable broker has its own state");
  assert.ok(js.includes("relayErrorText"), "relay errors are surfaced as themselves");
  assert.ok(js.includes("slow down"), "a 429 tells the visitor to slow down");
});

test("deck: the brain is named honestly and no benchmark figures leak", () => {
  assert.ok(!html.includes("in-development slot"), "the stale Nano framing must be gone");
  assert.ok(html.includes("(350M)"), "Wave Nano's real size must be stated");
  assert.ok(htmlFlat.includes("MCU-class classifier line"), "Roger Edge must be named as the MCU line");
  assert.ok(htmlFlat.includes("no trained artifact yet"), "the untrained truth must be stated");
  for (const leak of ["24/25", "4/133", "46%", "84–90%"]) {
    assert.ok(!html.includes(leak) && !js.includes(leak),
      `internal benchmark figure ${leak} must not appear on the public page`);
  }
});

test("deck: Wave contract models are never offered as raw free chat", () => {
  assert.ok(js.includes("never offered as raw free chat"),
    "the framing rule from the models agent is stated in code");
  assert.ok(!js.includes("text: true, voice: true, image: true, tool: true"),
    "the demo tape must not claim chat kinds");
});

// ---------- bench survivors + audit locks -------------------------------------

test("deck: the S-meter needle is driven by the directory's own signal numbers", () => {
  assert.ok(html.includes('id="pgSMeterNeedle"'), "the needle element must exist");
  assert.ok(js.includes("function meterSignal()"), "the meter reads from STATE.bands");
  assert.ok(js.includes("b.signal"), "the meter uses the same signal field the shelf draws");
  assert.ok(!js.includes("Math.random"), "the meter must never invent a value");
  assert.ok(js.includes('readEl.textContent = measured ? "S"') && js.includes(': "N/A"'),
    "tapes without a measured station signal must say N/A instead of borrowing one");
});

test("deck: the transcript is a ruled logbook with UTC times", () => {
  assert.ok(css.includes("repeating-linear-gradient"), "the logbook rules must be drawn");
  assert.ok(js.includes("pg-line__ts"), "each entry must carry its timestamp");
  assert.ok(js.includes("getUTCHours"), "logbook time is UTC, the operator's convention");
});

test("deck: reduced motion freezes reels, load, and needle legible", () => {
  const rm = css.slice(css.indexOf("prefers-reduced-motion"));
  for (const frozen of ['.dk__cassette[data-state="loading"] { animation: none; }',
    ".pg-smeter__needle { transition: none; }"]) {
    assert.ok(rm.includes(frozen), `reduced motion must include: ${frozen}`);
  }
});

test("deck: no /market fetch and no orphaned sample data", () => {
  assert.ok(!js.includes('"/market"'), "playbox.js must not fetch /market");
  assert.ok(js.includes('fetchJSON("/discover"'), "/discover is the one directory source");
  assert.ok(!existsSync(path.join(SRC, "data/playground-nano-samples.jsonl")),
    "the unreferenced samples file must not return");
  assert.ok(html.includes('id="pgEdgeData"'), "the inline golden-set dataset is the replay source");
});
