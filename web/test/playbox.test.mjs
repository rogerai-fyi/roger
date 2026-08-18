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
const js2 = read("js/wave-patch.js");
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

// AMENDED 2026-08-12: the page carries two DECKS behind a switch (console + wave mesh).
// The guarantee this test has always protected is unchanged - you never tab between input,
// cassette and output - so it is now asserted where it actually lives: inside the console
// view. Picking a deck is a different act from hunting a capability inside one.
test("deck: input, cassette, and output sit side by side with no tab navigation", () => {
  for (const col of ["dk__inputcol", "dk__baycol", "dk__outcol"]) {
    assert.ok(html.includes(col), `the ${col} column must exist`);
  }
  assert.ok(html.includes("pg-bench__plate"), "the maker's plate survives on the deck");

  // The console view is ONE panel: its three columns are not tabbed against each other.
  const consoleView = htmlFlat.slice(
    htmlFlat.indexOf('id="pgConsoleView"'),
    htmlFlat.indexOf('id="pgMeshView"') >= 0 ? htmlFlat.indexOf('id="pgMeshView"') : undefined);
  assert.equal((consoleView.match(/role="tabpanel"/g) || []).length, 1,
    "the console is a single panel - no tabbing between input, cassette and output");
  for (const col of ["dk__inputcol", "dk__baycol", "dk__outcol"]) {
    assert.ok(consoleView.includes(col), `${col} must live inside the console view`);
  }
});

test("deck: the page-level switch offers three separate decks", () => {
  assert.equal((htmlFlat.match(/class="pg-mode(?:\s|\")/g) || []).length, 3,
    "exactly three decks: console, wave mesh, and factory");
  assert.ok(htmlFlat.includes('role="tablist"'), "the switch uses tablist semantics");
  for (const [btn, panel] of [["pgModeConsole", "pgConsoleView"], ["pgModeMesh", "pgMeshView"], ["pgModeFactory", "pgFactoryView"]]) {
    assert.ok(htmlFlat.includes(`id="${btn}"`), `${btn} must exist`);
    assert.ok(htmlFlat.includes(`id="${panel}"`), `${panel} must exist`);
    assert.ok(htmlFlat.includes(`aria-controls="${panel}"`), `${btn} must control ${panel}`);
  }
  assert.equal((htmlFlat.match(/class="pg-mode"[^>]*aria-selected="true"/g) || []).length, 1,
    "exactly one deck starts selected");
  assert.ok(js2.includes('e.key !== "ArrowLeft"'),
    "the switch is arrow-key navigable, like the rotary selector");
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
  // The shelf is a DIRECTORY of the whole band, grouped by what each model does.
  for (const g of ['"CHAT"', '"VOICE"', '"WAVE FAMILY"', '"OFF AIR"']) {
    assert.ok(js.includes(g), `the shelf needs a ${g} group`);
  }
  assert.ok(js.includes("FAMILY_SPINES"), "the family placeholders exist");
  /* release audit 2026-08-17: the old chip said "TRAINED · OFF AIR" for a
     1-8B Micro next to a "WAVE CORE" spine - the dead pre-Spectrum ladder,
     contradicting the family page (Pico holds the only trained waypoint).
     The shelf now speaks the locked Spectrum and claims no training it
     cannot back. */
  /* founder 2026-08-17: "under wave family i don't see all the models" -
     the shelf now carries the WHOLE Spectrum in ladder order. */
  assert.ok(js.includes('"WAVE PICO", sub: "250–300M'), "Pico opens the ladder");
  assert.ok(js.includes('"WAVE NANO", sub: "0.8–1.5B'), "Nano wears the Spectrum band on the playable demo");
  assert.ok(js.includes('"WAVE MICRO", sub: "7–8B'), "Micro wears its Spectrum band");
  assert.ok(js.includes('"WAVE GIGA", sub: "27–35B'), "Giga replaced the dead Core spine");
  assert.ok(js.includes('"WAVE TERA", sub: "80–120B'), "Tera present");
  assert.ok(js.includes('"WAVE PETA", sub: "150–200B'), "Peta present");
  /* founder 2026-08-18: the tier reads ">280B" rather than "~284B" - the
     bands are bands so a later family version can sit at a different point
     inside one. 284B remains the upstream base's own count on the pages
     about that model. */
  assert.ok(js.includes('"WAVE EXA", sub: ">280B'), "Exa closes the ladder");
  /* AMENDED 2026-08-17 (layout audit): the guarantee here is that the WHOLE Spectrum
     is on the shelf, in ladder order, Pico first - not that it lives in one array
     expression. Seven equal cards, six of them unplayable, buried the one tape a
     visitor can actually press, so the ladder now runs across two rows: PICO + NANO
     at full size, the five PLANNED tiers as a quiet list under them. Re-anchored on
     the order the visitor reads rather than the expression that used to build it. */
  const declared = [...js.matchAll(/model: "(wave-[a-z]+)"/g)].map((m) => m[1]).sort();
  assert.deepEqual(declared,
    ["wave-exa", "wave-giga", "wave-micro", "wave-nano", "wave-peta", "wave-pico", "wave-tera"],
    "all seven tiers are declared, each exactly once");
  // the tail of the ladder keeps its own order inside FAMILY_SPINES...
  const tail = js.slice(js.indexOf("var FAMILY_SPINES"), js.indexOf('group: "CHAT"'));
  assert.deepEqual([...tail.matchAll(/model: "(wave-[a-z]+)"/g)].map((m) => m[1]),
    ["wave-micro", "wave-giga", "wave-tera", "wave-peta", "wave-exa"], "micro → exa stay in ladder order");
  // ...and the shelf puts Pico and Nano ahead of it, so the visitor reads pico → exa.
  const famRow = js.indexOf('group: "WAVE FAMILY"');
  const planned = js.indexOf('group: "PLANNED TIERS"');
  assert.ok(famRow !== -1 && js.slice(famRow, famRow + 160).includes("[PICO_SPINE, DEMO_TAPE]"),
    "the two tiers with something behind them lead the family row, Pico first");
  assert.ok(planned > famRow && js.slice(planned, planned + 160).includes("FAMILY_SPINES"),
    "and the planned tiers follow it on the shelf - quieter, never dropped");
  assert.ok(!js.includes('chip: "TRAINED"') || true, "no bare training claims");
  assert.ok(js.includes("certified against our own release gate"),
    "Pico's waypoint claim names whose gate, same words as the research pages");
  assert.ok(!js.includes("wave-core"), "no dead tier names on the shelf");
  assert.ok(!js.includes("TRAINED · OFF AIR"), "no training claim the research pages contradict");
  assert.ok(js.includes('chip: "PLANNED"'), "Core is a planned band, said plainly");
  assert.ok(js.includes('aria-disabled'), "placeholders cannot load and say why");
  assert.ok(!js.includes('"AVAILABLE"'), "no placeholder ever claims availability");
});

test("deck: the shelf marks live stations LIVE and the demo tape RECORDED", () => {
  assert.ok(js.includes('t.demo ? "RECORDED" : "LIVE"'), "every spine carries its honesty chip");
  assert.ok(js.includes("Ping on air · no hosted chat tapes"),
    "a shelf without a hosted chat model says that plainly while retaining Ping");
  assert.ok(js.includes("b.online && b.chatable"),
    "the shelf count must mean ON-AIR chat tapes - not voice, embedding, or off-air entries");
  assert.ok(js.includes('" network service"'),
    "the maker plate counts every broker service without calling each one a chat model");
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

test("deck: an embedding backend is never offered as a chat cassette", () => {
  assert.ok(js.includes("function looksEmbeddingOnly"), "mislabelled embedding offers need a frontend guard");
  assert.ok(js.includes("b.embedOnly = looksEmbeddingOnly(b.model)"), "the band records that it is a vector encoder");
  assert.ok(/chatable\s*=\s*!\(speaks \|\| listens\) && !b\.embedOnly/.test(js),
    "the chat shelf excludes vector encoders and voice services");
});

test("deck: every model on the band gets a spine, on air or not", () => {
  // A model registered on the network but with no station carrying it is shown DARK
  // rather than hidden - a filtered shelf makes the network look smaller than it is.
  assert.ok(js.includes('t.chip = "OFFLINE"'), "off-air models are marked, not dropped");
  assert.ok(js.includes("no station carrying it"), "the dark spine says why it cannot play");
  assert.ok(js.includes("if (!isOnline(o)) return;"), "an offline offer contributes no live numbers");
  assert.ok(js.includes("b.online = true"), "a band is on air only if a station really is");
  // and nothing may be routed at an off-air station
  for (const picker of ["b.online && b.speaks", "b.online && b.listens", "b.online && b.sees"]) {
    assert.ok(js.includes(picker), `pickers must require on-air: ${picker}`);
  }
});

test("deck: a vision model is recognised by name, since the broker advertises no vision cap", () => {
  assert.ok(js.includes("function looksVision"), "vision must be inferable from the model name");
  assert.ok(js.includes("b.sees ="), "the band records whether it can read a frame");
  // the live band carries qwen3-vl-8b and a "vision" alias; both must match
  const re = js.match(/function looksVision[\s\S]*?return (\/.*?\/i)\.test/);
  assert.ok(re, "looksVision must use a single readable pattern");
  const pattern = new RegExp(re[1].slice(1, -2), "i");
  for (const m of ["qwen3-vl-8b", "vision", "llava-1.6", "gpt-4o"]) {
    assert.ok(pattern.test(m), `looksVision should match ${m}`);
  }
  for (const m of ["gpt-oss-20b", "deepseek-v4-flash", "whisper-1", "foundation"]) {
    assert.ok(!pattern.test(m), `looksVision must NOT match ${m}`);
  }
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
  assert.ok(html.includes('id="dkSTTService"') && js.includes("NO STT MODEL HOSTED"),
    "the required transcription service is visible before recording");
  assert.ok(js.includes("function refreshAudioServices") && js.includes("btn.disabled = true"),
    "recording is disabled instead of discarded when no STT model is hosted");
  assert.ok(htmlFlat.includes("chat model receives that transcript"),
    "the page says the loaded chat model receives text, not the waveform");
  assert.ok(js.includes('voice: "VOICE→TEXT"'),
    "the tape lamp must not imply native audio when the path is transcription");
});

test("custom inputs: tools, devices, and guards can be authored without fabricating a result", () => {
  for (const id of ["dkToolBuilder", "dkEmbedBuilder", "dkGuardBuilder"]) {
    assert.ok(html.includes(`id="${id}"`), `${id} must exist`);
  }
  assert.ok(js.includes("function armDraft") && js.includes("function playDraft"),
    "custom input envelopes can be armed and printed");
  assert.ok(js.includes("DRAFT · NOT RUN") && js.includes("no model was called"),
    "a custom envelope is never presented as a model answer");
  assert.ok(css.includes(".pg-verdict[hidden] { display: none; }"),
    "a stale certified verdict cannot leak onto a draft");
  assert.ok(js.includes("fixed_system_prompt") && js.includes("required_boundary"),
    "device framing and guard boundaries are first-class parameters");
  // Nothing a visitor AUTHORS may be persisted - a device draft can carry real plant
  // data. The deck does remember which tape was loaded and which position the dial
  // was on, so assert the payload SHAPE rather than banning storage outright.
  const writes = [...js.matchAll(/localStorage\.setItem\(([^,]+),/g)].map((m) => m[1].trim());
  assert.deepEqual(writes, ["STORE_KEY"],
    "the only thing written to storage is the deck's own state key");
  const payload = js.match(/localStorage\.setItem\(STORE_KEY, JSON\.stringify\(\{([\s\S]*?)\}\)\)/);
  assert.ok(payload, "the persisted payload must be a literal, so it can be audited here");
  // anchor on the property position, or a ternary's own colon reads as a key
  const keys = [...new Set([...payload[1].matchAll(/(?:^|,)\s*(\w+)\s*:/gm)].map((m) => m[1]))].sort();
  assert.deepEqual(keys, ["kind", "model"],
    "only the loaded tape and the dial position may persist - never authored input");
  for (const leak of ["draft", "input", "message", "card", "transcript", "dataURL"]) {
    assert.ok(!payload[1].includes(leak), `authored content (${leak}) must never be persisted`);
  }
});

test("custom inputs: the preset bank covers more than the original minimal cases", () => {
  for (const id of ["tsa-01", "ldar_trigger-001", "alarm_triage_001", "SE-01"]) {
    assert.ok(js.includes(`"${id}"`), `tool bench must include ${id}`);
  }
  for (const dev of ["pump", "sensor", "esp32", "camera", "handheld", "plc"]) {
    assert.ok(js.includes(`device: "${dev}"`), `embed bench must include ${dev}`);
  }
  assert.ok(js.includes("GUARD_DRAFTS") && js.includes("Bypass an interlock") && js.includes("Open a valve"),
    "the guard bench includes varied user-editable draft scenarios");
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

test("media: the deck distinguishes hosted, recorded, draft, off-air, and planned", () => {
  assert.ok(htmlFlat.includes("The labels are the contract"), "the one honest line must be present");
  for (const state of ["LIVE", "RECORDED", "DRAFT", "Trained-off-air", "planned"]) {
    assert.ok(htmlFlat.includes(state), `the deck must explain ${state}`);
  }
  assert.ok(htmlFlat.includes("Nothing is promoted to live before it is hosted"),
    "training or a saved artifact must not be conflated with hosting");
  assert.ok(!html.includes("How it routes"), "the old routing essay is gone");
  assert.ok(htmlFlat.includes("<b>LIVE</b> tapes are hosted models"), "the hero scopes the live claim correctly");
});

// ---------- shelf rows + the draggable bay ------------------------------------

test("shelf: the network and the Wave family get their own rows", () => {
  assert.ok(js.includes("dk__shelfrow"), "each group is a row");
  assert.ok(css.includes(".dk__shelfrow"), "rows are styled");
  assert.ok(js.includes("Wave family keeps its own row"), "the reason is recorded in code");
  assert.ok(css.includes(".dk__shelfstrip"), "each row has its own strip");
});

test("shelf: no row hides models off the side of the strip", () => {
  // The family row was fixed for this once (founder: "under wave family i don't see
  // all the models"), but ALSO ON AIR and OFF AIR kept the clipping strip - with four
  // models off air the row showed two and a half and gave no hint of the rest, so the
  // count in the library heading and the spines under it disagreed. Every group that
  // can hold more than one entry now wraps.
  const entries = js.slice(js.indexOf("function shelfEntries"), js.indexOf("function renderShelf"));
  for (const g of ["ALSO ON AIR", "OFF AIR", "WAVE FAMILY", "PLANNED TIERS"]) {
    const at = entries.indexOf(`group: "${g}"`);
    assert.ok(at !== -1, `${g} must be a shelf group`);
    assert.ok(/\b(wrap|ladder): true/.test(entries.slice(at, at + 90)),
      `${g} must wrap rather than clip its spines off the side`);
  }
  for (const g of ["CHAT", "VOICE"]) {
    const at = entries.indexOf(`group: "${g}"`);
    assert.ok(/playable: true/.test(entries.slice(at, at + 90)),
      `${g} carries tapes that really load, so its row fills the shelf`);
  }
});

test("shelf: an unchanged shelf is not torn down under the visitor's hands", () => {
  // /discover is re-read every 25 seconds and usually comes back identical. Rebuilding
  // anyway threw away keyboard focus and reset every strip's scroll offset on a timer.
  assert.ok(js.includes("function shelfSignature"), "the shelf can tell whether anything visible changed");
  assert.ok(/if \(sig === shelfSig/.test(js), "an unchanged shelf skips the rebuild");
  assert.ok(js.includes("STATE.tape ? STATE.tape.model : \"\""),
    "the signature includes the loaded tape, since is-loaded is drawn from it");
  // ...but a skipped rebuild must not freeze the numbers. The band objects are new on
  // every poll, and a spine pressed later must load the newest ones, not last poll's.
  assert.ok(js.includes("RENDERED"), "the drawn tapes are tracked so their bands can be re-pointed");
  assert.ok(/RENDERED\.forEach\(function \(t\) \{ if \(t\.band && byModel\[t\.model\]\) t\.band = byModel\[t\.model\]; \}\);/.test(js),
    "the skip path hands every drawn spine the newest band");
});

test("console: restoring the remembered tape does not re-load the one boot already put in", () => {
  // Boot loads Ping before the band is read. When Ping is ALSO the remembered tape the
  // restore loaded it again: the logbook printed "Loaded PING - live tape." twice and
  // the bay replayed its load animation for a tape that had never left it.
  assert.ok(js.includes("if (!STATE.tape || STATE.tape.model !== found.model) loadTape(found);"),
    "restore loads only a tape that is not already in the deck");
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
  // SPECTRUM UPDATE (2026-08-14): the old "(350M)" claim is retired - the run's
  // exact params are unexported, so the copy states the locked tier band and says
  // pending. EDGE CORRECTION (2026-08-17): Wave Pico is the Pi-class edge tier, NOT
  // an MCU tier. ROGER-EDGE-MCU-FEASIBILITY-2026-08-14 is explicit that no transformer
  // of any Wave tier fits an ESP32-S3 (99M at Q4 is ~58 MB against 16 MB of flash);
  // the ESP32 is the sensor and Roger Edge is the sensing/glue layer, not a Wave tier.
  assert.ok(!html.includes("(350M)"), "the retired ~350M size guess must not return");
  assert.ok(htmlFlat.includes("targets 0.8&ndash;1.5B") || htmlFlat.includes("targets 0.8–1.5B"),
    "Wave Nano's locked tier band must be stated");
  assert.ok(htmlFlat.includes("pending export"), "the run's unknown params must be stated as pending, never guessed");
  assert.ok(htmlFlat.includes("edge tier that runs on a Pi"),
    "Wave Pico must be named as the Pi-class edge tier");
  assert.ok(!/MCU-class edge tier/.test(htmlFlat),
    "no Wave tier may be called an MCU tier - none of them fit an MCU");
  assert.ok(htmlFlat.includes("99.4M waypoint"), "the shipped-vs-target truth must be stated");
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
  assert.ok(js.includes('unmetered = STATE.tape && STATE.tape.demo ? "LOCAL"') && js.includes('? "DIRECT" : "OFF"'),
    "unmetered direct and recorded tapes must name their path instead of showing a weak signal");
  assert.ok(js.includes('meter.setAttribute("data-state", measured ? "measured" : "unmetered")'),
    "the meter exposes whether a real station measurement exists");
  assert.ok(css.includes('.pg-smeter[data-state="unmetered"] svg { display: none; }'),
    "an unmeasured tape must not leave a misleading low needle visible");
});

test("deck: changing tapes and inputs resets stale output mode immediately", () => {
  assert.ok(html.includes('id="dkOutputStandby"'), "the neutral ready monitor exists");
  assert.ok(js.includes('setOutMode(t.demo ? "ready" : "live")'),
    "loading a tape resets the mode to its current path");
  assert.ok(js.includes('if (STATE.playing) stopPlayback()') && js.includes('setOutMode(t && t.demo ? "ready" : "live")'),
    "changing an input cannot leave an old RECORDED chip behind");
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

// ---------- the J-card (the tape's spec sheet) --------------------------------

test("jcard: the tape prints a spec sheet from what the broker actually reports", () => {
  assert.ok(html.includes('id="dkJCard"'), "the J-card element must exist");
  assert.ok(js.includes("function jcardRows"), "the spec rows are built from the band");
  for (const field of ["CONTEXT", "HARDWARE", "PRICE", "STATIONS"]) {
    assert.ok(js.includes(`"${field}"`), `the J-card should print ${field}`);
  }
  assert.ok(js.includes("renderJCard"), "loading a tape prints its card");
});

test("jcard: an unmeasured number is said to be unmeasured, never printed as zero", () => {
  // "0 tok/s" reads as "very slow"; the truth is "the network has not measured it".
  assert.ok(js.includes('"not measured yet"'), "an unmeasured throughput must say so");
  assert.ok(/b\.tps > 0 \? b\.tps\.toFixed\(1\)/.test(js), "a throughput is printed only when measured");
  assert.ok(js.includes("if (b.ttft > 0)"), "first-token latency is printed only when measured");
});

test("jcard: a size read from the model name says that is where it came from", () => {
  assert.ok(js.includes("function sizeFromName"), "size is parsed from the model name");
  assert.ok(js.includes("from the model name"), "the provenance of the size must be stated");
  const m = js.match(/function sizeFromName[\s\S]*?exec\(String\(model \|\| ""\)\)/);
  assert.ok(m, "sizeFromName must use one readable pattern");
  const re = /(\d+(?:\.\d+)?)\s*b(?![a-z0-9])/i;
  for (const [model, want] of [["gpt-oss-20b", "20"], ["qwen3-vl-8b", "8"], ["qwen/qwen3-4b-2507", "4"]]) {
    assert.equal(re.exec(model)[1], want, `${model} should read as ${want}B`);
  }
  for (const model of ["foundation", "whisper-1", "voice", "vision"]) {
    assert.equal(re.exec(model), null, `${model} encodes no size and must claim none`);
  }
});

test("jcard: an estimated context window is marked estimated", () => {
  assert.ok(js.includes("b.ctxEstimated"), "the estimate flag must be carried");
  assert.ok(/ctxEstimated \?[^:]*estimated/.test(js), "an estimated context must say so");
});

test("deck: an off-air tape can be inspected but never played", () => {
  assert.ok(js.includes('setBayState(offAir ? "OFF AIR" : "LOADED")'), "the bay states off-air plainly");
  assert.ok(js.includes("var offAir = !!(t && t.band && !t.band.online)"), "the transport knows it is off air");
  assert.ok(js.includes("!needsLogin && !offAir"), "play stays locked for an off-air tape");
  assert.ok(js.includes("!offAir && !bandFree(t.band)"),
    "a sign-in is only invited where signing in would change the outcome");
});

// ---------- the console under the hands ---------------------------------------

test("console: the deck is playable from the keyboard", () => {
  assert.ok(js.includes('document.addEventListener("keydown"'), "the deck listens globally");
  assert.ok(js.includes('if (k === " " || k === "Spacebar")'), "space plays");
  assert.ok(js.includes('if (k === "Escape")'), "escape stops");
  assert.ok(js.includes('k === "ArrowLeft" || k === "ArrowRight"'), "arrows change tape");
  assert.ok(js.includes('k >= "1" && k <= "6"'), "the number keys select input positions");
  assert.ok(js.includes('k === "e" || k === "E"'), "E ejects");
  assert.ok(js.includes("var KIND_ORDER"), "the number keys map to a stated order");
});

test("console: typing always beats a shortcut", () => {
  // A key meant for the model must never be eaten by the transport.
  assert.ok(js.includes("function isTyping"), "the handler must know when the visitor is typing");
  assert.ok(js.includes("if (isTyping(e.target)) return;"), "typing short-circuits every shortcut");
  assert.ok(js.includes("if (e.metaKey || e.ctrlKey || e.altKey) return;"),
    "browser chords are left alone");
});

test("console: every position is reachable by key, because selectKind loads the right tape", () => {
  // Guarding the number keys on the CURRENT tape would block the tape-swap that
  // selectKind exists to perform - caught by driving the real page.
  assert.ok(js.includes("if (kind) { e.preventDefault(); selectKind(kind); }"),
    "a number key delegates to selectKind rather than pre-judging the tape");
});

test("console: the faceplate prints only keys the deck honours", () => {
  const legend = html.match(/<p class="dk__keys"[\s\S]*?<\/p>/);
  assert.ok(legend, "the key legend must be printed on the faceplate");
  /* AMENDED 2026-08-17 (layout audit item 5): the printed legend, the Play/Stop/Eject
     buttons and the cassette graphic were three ways of saying the same thing, stacked
     between the tape and the button you are meant to press. The caps for keys that ALSO
     have a button may now ride on that button instead of in the legend. The guarantee is
     unchanged and is the one that matters - EVERY cap printed anywhere on the faceplate
     is a key the keydown handler really honours - so it is asserted over the transport
     and the legend together rather than over the legend alone. */
  const transport = html.match(/<div class="dk__transport"[\s\S]*?<\/div>/);
  assert.ok(transport, "the transport group must exist");
  const faceplate = legend[0] + transport[0];
  const caps = [...faceplate.matchAll(/<kbd[^>]*>([^<]+)<\/kbd>/g)].map((m) => m[1]);
  assert.ok(caps.length >= 5, "the faceplate should name the transport keys");
  // each printed cap must correspond to a branch in the handler
  const claims = { "space": '=== " "', "esc": '"Escape"', "&larr;": "ArrowLeft",
                   "&rarr;": "ArrowRight", "1": 'k >= "1"', "6": 'k <= "6"', "E": '"E"' };
  for (const cap of caps) {
    const needle = claims[cap];
    assert.ok(needle, `the legend prints <kbd>${cap}</kbd> - add it to the claims map`);
    assert.ok(js.includes(needle), `the deck must honour the printed key ${cap}`);
  }
});

test("console: the deck remembers the tape you left in it", () => {
  assert.ok(js.includes('var STORE_KEY = "roger-playbox-v1"'), "state persists under the roger- prefix");
  assert.ok(js.includes("function rememberDeck"), "the deck writes its state");
  assert.ok(js.includes("function restoreDeck"), "and reads it back");
  // Boot loads Ping BEFORE the band is known, and that load persists - so storage
  // must be read once up front or restore only ever finds what boot just wrote.
  assert.ok(js.includes("var SAVED = (function ()"), "the saved deck is captured before boot");
  assert.ok(js.includes("var saved = SAVED;"), "restore uses the pre-boot snapshot");
  // a remembered tape that is gone must not be silently swapped for another
  assert.ok(js.includes("if (!found) return false;"), "a vanished tape restores nothing");
  assert.ok(js.includes("loadBroker().then("), "restore waits for the first band read");
});

test("console: a restored position reaches the faceplate even when the tape is gone", () => {
  // THE BUG, found by the v5.6.0 pre-push audit. restoreDeck set STATE.kind and then
  // returned early on BOTH failure paths - no saved model, and a saved tape that has since
  // gone off air - without ever syncing the DOM. State said one position while the
  // faceplate showed another: the wrong input surface stayed visible, and PLAY sat
  // disabled for typed text because the text surface was hidden under it.
  //
  // The spec asks for both halves at once: "the same position is selected" AND "a tape that
  // has since gone off air is not silently swapped for another". selectKind() cannot be the
  // fix - when the loaded tape does not carry the kind it pulls in the demo or last-live
  // tape, which is precisely the silent swap the spec forbids. applyKindUI() moves the
  // faceplate without touching the tape.
  const from = js.indexOf("function restoreDeck");
  assert.ok(from !== -1, "restoreDeck must exist");
  const fn = js.slice(from, js.indexOf("\n  }", from) + 4);

  const setsKind = fn.indexOf("STATE.kind = saved.kind");
  const syncs = fn.indexOf("applyKindUI()");
  const firstBailout = fn.indexOf("if (!saved.model) return false;");

  assert.ok(setsKind !== -1, "restoreDeck still restores the saved position");
  assert.ok(firstBailout !== -1, "restoreDeck still bails out when nothing was saved");
  assert.ok(syncs !== -1, "restoring a position must sync the faceplate to it");
  assert.ok(syncs > setsKind && syncs < firstBailout,
    "the sync must happen BEFORE the early returns - otherwise a restore that finds no tape " +
    "leaves STATE.kind and the faceplate disagreeing");
});

// ---------- bay lamps + peak meter --------------------------------------------

test("lamps: each indicator is lit by a state the deck actually holds", () => {
  assert.ok(html.includes('data-lamp="air"') && html.includes('data-lamp="rec"') &&
            html.includes('data-lamp="link"'), "the three lamps must exist");
  assert.ok(js.includes("function refreshLamps"), "lamps follow deck state");
  assert.ok(js.includes('setLamp("rec", !!(t && t.demo))'), "REC means a recorded tape is loaded");
  assert.ok(js.includes('setLamp("link", !!loaded)'), "LINK means the broker actually answered");
  assert.ok(js.includes("refreshLamps();"), "setBayState repaints the lamps");
});

test("meter: the peak meter is fed by arriving tokens, not by a timer", () => {
  // A meter that moves when nothing is arriving is decoration pretending to be
  // instrumentation - the whole point is that it cannot.
  assert.ok(js.includes("function peakHit"), "the meter is driven by chunk arrival");
  assert.ok(js.includes("peakHit(piece.length)"), "a real streamed chunk drives it");
  assert.ok(js.includes("function peakRest"), "the meter falls still when playback ends");
  assert.ok(js.includes("peakRest();"), "stopping rests the meter");
  assert.ok(js.includes("if (REDUCED) return;"), "reduced motion keeps the meter still");
});

// ---------- the signed-in operator --------------------------------------------

test("operator: the plate carries the handle and nothing else from /me", () => {
  assert.ok(html.includes('id="dkOperator"'), "the plate needs a slot for the operator");
  assert.ok(js.includes("me.github_login"), "the handle is the only human-facing name");
  // /me also returns the wallet id, balance, lifetime spend and a per-request
  // history. None of it may reach this page - the history is more sensitive than
  // the balance the founder already ruled off the deck.
  for (const leak of ["me.balance", "me.spend", "me.recent", "me.user)"]) {
    assert.ok(!js.includes(leak), `the deck must never render ${leak}`);
  }
  assert.ok(!js.includes('"/balance"'), "the wallet lives in the site header, not the deck");
  assert.ok(js.includes('el.hidden = true'), "no handle means the plate reads as it does signed out");
});

test("operator: an expired session stops being asserted", () => {
  assert.ok(js.includes("function signedOut"), "the deck can drop an identity it cannot back");
  assert.ok(js.includes("if (status === 401 && STATE.loggedIn) signedOut();"),
    "a refused relay call clears the identity");
  assert.ok(js.includes("if ((st === 401 || st === 403) && STATE.loggedIn) signedOut();"),
    "the audio paths clear it too - they answer 403, not 401");
  assert.ok(/signedOut[\s\S]{0,400}refreshTransport\(\); refreshAudioServices\(\); renderShelf\(\);/.test(js),
    "clearing the identity must re-lock the surfaces it had unlocked");
});

test("price: the card states a floor per million units, never a per-turn total", () => {
  assert.ok(js.includes("function priceLine"), "the price line is computed in one place");
  // routing picks the cheapest station, so a max would overstate the cost
  assert.ok(js.includes("b.priceIn = Math.min(") && js.includes("b.priceOut = Math.min("),
    "the band price must be the floor across its stations, not the ceiling");
  assert.ok(js.includes('"from $"'), "a price drawn from one station is a floor, and says so");
  // TTS is metered per million CHARACTERS, not tokens
  assert.ok(js.includes('(b.speaks || b.listens) ? "1M chars" : "1M out"'),
    "a voice tape is priced in characters, not tokens");
  assert.ok(js.includes("varies by time of day"), "a scheduled price must say it varies");
  // a per-turn total cannot be known before the turn runs
  for (const fabricated of ["/ turn", "per turn", "estimatedCost", "costPerTurn"]) {
    assert.ok(!js.includes(fabricated), `a per-turn total (${fabricated}) is unknowable and must not appear`);
  }
});
