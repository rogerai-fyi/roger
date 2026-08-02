/* =====================================================================
   RogerAI - Playbox: THE CASSETTE DECK (features/web/playbox_deck.feature)

   One view. The model IS the tape:

     LIVE, REAL endpoints (no fakery):
       GET  /discover            - per-station offers (CORS, no creds)
       GET  /me, GET /balance    - session identity + wallet (CORS, creds)
       POST /concierge           - the Ping assistant (CORS, creds omit)
       POST /v1/chat/completions - per-station chat through the Tower
         (credentialed CORS from this origin only; the session cookie is the
         identity, same wallet + receipts + limits as the CLI - see
         features/relay/browser_session.feature). The copyable CLI command
         stays offered as the terminal path to the same station.

     RECORDED (always labelled, never presented as live):
       image / tool / embed / guard replays - certified contracts and real
       captured outputs from the Wave golden set (inline #pgEdgeData).

   Dependency-free, defensive (degrades to an honest resting state on any
   failure), prefers-reduced-motion aware.
   ===================================================================== */
(function () {
  "use strict";
  var BROKER = "https://broker.rogerai.fm";
  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }

  /* =====================================================================
     STATE + the broker directory
     ===================================================================== */
  var STATE = {
    bands: [], loggedIn: false,
    tape: null,          // the loaded cassette (shelf entry)
    kind: "text",        // active input position
    card: null,          // selected preset card (voice/image/tool/embed/guard)
    playing: false
  };
  var abortCtl = null;   // the in-flight live stream, so STOP is real
  var typeTimer = null;  // the in-flight recorded printer, so STOP is real here too
  var playbackSerial = 0; // stale aborted turns cannot reset a newer transport

  function setBayState(text) {
    var n = $("dkBayState");
    if (n) n.textContent = text;
  }

  function fetchJSON(path, creds) {
    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 8000);
    return fetch(BROKER + path, {
      credentials: creds ? "include" : "omit", cache: "no-store",
      signal: ctrl ? ctrl.signal : undefined
    }).then(function (r) { clearTimeout(to); if (!r.ok) throw r.status; return r.json(); });
  }

  function callsign(nodeID) {
    var h = 0, s = String(nodeID || "");
    for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
    return "R" + (h % 10) + String.fromCharCode(65 + (h >> 4) % 26) + String.fromCharCode(65 + (h >> 9) % 26);
  }
  function isOnline(o) { return o && o.online !== false; }
  function capsOf(o) {
    var out = {};
    (o.capabilities || o.caps || []).forEach(function (c) { out[String(c).toLowerCase()] = true; });
    if (o.modality) out[String(o.modality).toLowerCase()] = true;
    return out;
  }

  function bandsFromOffers(offers) {
    var by = {};
    offers.forEach(function (o) {
      if (!o || !o.model || !isOnline(o)) return;
      var b = by[o.model] || (by[o.model] = {
        model: o.model, count: 0, free: false, tier: 99, signal: 0, tps: 0, caps: {}, stations: []
      });
      b.count++;
      if (o.free_now) b.free = true;
      var tier = (+o.price_tier || 0); if (o.price_out != null && tier < b.tier) b.tier = tier;
      var sig = Math.max(0, Math.min(100, +o.signal || 0)); if (sig > b.signal) b.signal = sig;
      var tps = +o.tps || 0; if (tps > b.tps) b.tps = tps;
      var c = capsOf(o); for (var k in c) b.caps[k] = true;
      b.stations.push({ callsign: callsign(o.node_id), signal: sig });
    });
    return Object.keys(by).map(function (k) {
      var b = by[k];
      if (b.tier === 99) b.tier = b.free ? 0 : 1;
      var onlyVoice = (b.caps.tts || b.caps.stt || b.caps.speak || b.caps.listen) && !b.caps.chat;
      b.chatable = !onlyVoice;
      return b;
    }).sort(function (a, c) { return c.signal - a.signal || c.count - a.count; });
  }
  function bandFree(b) { return b.free || b.tier === 0; }

  /* ---------- the S-meter: a real needle over real data ---------------- */
  function meterSignal() {
    if (STATE.tape && STATE.tape.band) return STATE.tape.band.signal;
    if (STATE.tape) return 0;
    return STATE.bands.reduce(function (m, b) { return Math.max(m, b.signal); }, 0);
  }
  function updateSMeter() {
    var meter = $("pgSMeter"), needle = $("pgSMeterNeedle"), readEl = $("pgSMeterRead");
    if (!needle) return;
    var sig = Math.max(0, Math.min(100, meterSignal()));
    var measured = !!(STATE.tape && STATE.tape.band);
    needle.style.transform = "rotate(" + Math.round(sig - 50) + "deg)";
    if (readEl) readEl.textContent = measured ? "S" + Math.min(9, Math.round(sig / 11.2)) : "N/A";
    if (meter) meter.setAttribute("aria-label", measured ? "Signal strength " + Math.round(sig) + " percent" : "Signal not measured for this tape");
  }

  function setStatus(text, state) {
    var node = $("pgChatStatus");
    if (!node) return;
    node.setAttribute("data-state", state || "off");
    var dot = node.querySelector(".livedot");
    node.textContent = "";
    if (dot) node.appendChild(dot);
    node.appendChild(document.createTextNode(" " + text));
  }

  /* =====================================================================
     THE SHELF - live band + the demo tapes
     ===================================================================== */
  // The demo tape carries the RECORDED kinds. Framing rule (models-agent,
  // measured twice): Wave contract models are never offered as raw free chat.
  var DEMO_TAPE = {
    demo: true, model: "wave-nano", label: "WAVE NANO", sub: "350M · edge contracts",
    kinds: { image: true, tool: true, embed: true, guard: true }
  };
  var PING_TAPE = {
    ping: true, model: "ping", label: "PING", sub: "concierge · always on",
    kinds: { text: true, voice: true }
  };

  function tapeKinds(t) {
    if (t.kinds) return t.kinds;
    var k = { text: true, voice: true };
    if (t.band && (t.band.caps.vision || t.band.caps.image)) k.image = true;
    return k;
  }

  // The Wave family on the shelf, honestly: Nano's recorded contracts are playable;
  // Micro is trained but not yet on air; Core is a planned band. Nothing is faked -
  // the unplayable spines say exactly why they cannot play yet.
  var FAMILY_SPINES = [
    { spine: true, model: "wave-micro", label: "WAVE MICRO", sub: "1–8B · text + tools",
      chip: "TRAINED · OFF AIR", why: "Trained, not yet on air - no public checkpoint is released." },
    { spine: true, model: "wave-core", label: "WAVE CORE", sub: "8–30B · planned band",
      chip: "PLANNED", why: "A planned band, not a final specification - nothing to play yet." }
  ];

  function shelfEntries() {
    // group 1: the live network; group 2: the Wave family
    var onAir = [PING_TAPE];
    STATE.bands.filter(function (b) { return b.chatable; }).forEach(function (b) {
      onAir.push({ band: b, model: b.model, label: b.model.split("/").pop().toUpperCase(),
                 sub: (b.stations[0] ? b.stations[0].callsign + " · " : "") +
                      (bandFree(b) ? "free" : "tier " + b.tier) });
    });
    return [
      { group: "ON AIR", entries: onAir },
      { group: "WAVE FAMILY", entries: [DEMO_TAPE].concat(FAMILY_SPINES) }
    ];
  }

  function renderShelf() {
    var shelf = $("dkShelf"), note = $("dkShelfNote");
    if (!shelf) return;
    shelf.textContent = "";
    // Two ROWS, not one strip: however many models the network is carrying, the
    // Wave family keeps its own row and never gets pushed off the end.
    ORDER = [];
    shelfEntries().forEach(function (grp) {
      var row = document.createElement("li");
      row.className = "dk__shelfrow";
      var head = el("span", "dk__shelfgroup", grp.group);
      head.setAttribute("aria-hidden", "true");
      row.appendChild(head);
      var strip = el("span", "dk__shelfstrip");
      row.appendChild(strip);
      shelf.appendChild(row);
      grp.entries.forEach(function (t) {
        if (!t.spine) ORDER.push(t);
        var btn = el("button", "dk__spine" + (STATE.tape && STATE.tape.model === t.model ? " is-loaded" : "")
          + (t.spine ? " dk__spine--shelfonly" : ""));
        btn.type = "button";
        btn.setAttribute("aria-pressed", STATE.tape && STATE.tape.model === t.model ? "true" : "false");
        btn.appendChild(el("b", null, t.label));
        btn.appendChild(el("small", null, t.sub));
        btn.appendChild(el("span", "dk__spinechip" + (t.demo || t.spine ? " dk__spinechip--rec" : ""),
          t.chip || (t.demo ? "RECORDED" : "LIVE")));
        if (t.spine) {
          // an honest placeholder: it cannot load, and pressing it says why
          btn.setAttribute("aria-disabled", "true");
          btn.title = t.why;
          btn.addEventListener("click", function () { setBayState("SHELF ONLY"); logLine("deck", "DECK", t.label + ": " + t.why); });
        } else {
          btn.addEventListener("click", function () { loadTape(t); });
        }
        strip.appendChild(btn);
      });
    });
    if (note) note.textContent = STATE.bands.length
      ? STATE.bands.length + " model" + (STATE.bands.length === 1 ? "" : "s") + " on air"
      : "the band is quiet - demo tapes only";
    updateSMeter();
  }

  var loaded = false;
  function loadBroker() {
    return fetchJSON("/discover", false).then(function (dData) {
      var offers = (dData && Array.isArray(dData.offers)) ? dData.offers : [];
      STATE.bands = bandsFromOffers(offers);
      loaded = true;
      var live = STATE.bands.length;
      if (live > 0) setStatus(live + " model" + (live === 1 ? "" : "s") + " on air · live from the broker", "live");
      else setStatus("the band is quiet - no models on air right now", "quiet");
      renderShelf();
    }).catch(function () {
      setStatus("couldn't reach the broker just now - retrying", "off");
      renderShelf();
    });
  }

  function loadAccount() {
    fetchJSON("/me", true).then(function (me) {
      STATE.loggedIn = !!(me && me.logged_in !== false && (me.user || me.login));
      refreshTransport();
    }).catch(function () { STATE.loggedIn = false; refreshTransport(); });
  }

  /* =====================================================================
     THE CASSETTE BAY - load / eject / reels
     ===================================================================== */
  var KIND_LABELS = { text: "TEXT", voice: "VOICE", image: "IMAGE", tool: "TOOL", embed: "EMBED", guard: "GUARD" };

  // dir: "left" | "right" when the tape was thrown across, else a drop-in load
  function loadTape(t, dir) {
    stopPlayback();
    STATE.tape = t;
    if (!t.demo) lastLiveTape = t;
    var cas = $("dkCassette");
    if (cas) {
      cas.setAttribute("aria-label", t.label + (t.demo ? ", recorded contract tape" : ", live model tape"));
      cas.setAttribute("data-swap", dir || "");
      cas.setAttribute("data-state", "loading");
      if (!REDUCED) {
        cas.addEventListener("animationend", function done() {
          cas.removeEventListener("animationend", done);
          cas.setAttribute("data-state", "loaded");
        });
      } else { cas.setAttribute("data-state", "loaded"); }
    }
    setBayState("LOADED");
    $("dkTapeName").textContent = t.label;
    $("dkTapeSub").textContent = t.demo ? "certified contracts · recorded" : "on air via the Tower";
    var caps = $("dkTapeCaps");
    if (caps) {
      caps.textContent = "";
      var kinds = tapeKinds(t);
      Object.keys(KIND_LABELS).forEach(function (k) {
        caps.appendChild(el("span", "dk__cap" + (kinds[k] ? " is-on" : ""), KIND_LABELS[k]));
      });
    }
    // dim selector positions this tape does not carry, and land on a carried one
    var kinds = tapeKinds(t);
    document.querySelectorAll(".dk__pos").forEach(function (btn) {
      btn.classList.toggle("is-dim", !kinds[btn.getAttribute("data-kind")]);
    });
    if (!kinds[STATE.kind]) { STATE.kind = Object.keys(kinds)[0] || "text"; STATE.card = null; }
    applyKindUI();
    // the terminal path to the same tape
    var box = $("pgCliBox"), cmd = $("pgCliCmd");
    if (box && cmd) {
      box.hidden = !(t.band);
      if (t.band) cmd.textContent = 'roger chat --model "' + t.model + '"';
    }
    logLine("deck", "DECK", "Loaded " + t.label + (t.demo ? " - recorded demo tape." : " - live tape."));
    renderShelf(); refreshTransport(); updateSMeter();
  }

  function ejectTape() {
    stopPlayback();
    STATE.tape = null;
    var cas = $("dkCassette");
    if (cas) { cas.setAttribute("data-state", "empty"); cas.setAttribute("aria-label", "No tape loaded"); }
    setBayState("EMPTY");
    $("dkTapeName").textContent = "NO TAPE";
    $("dkTapeSub").textContent = "pick a cassette from the shelf";
    var caps = $("dkTapeCaps"); if (caps) caps.textContent = "";
    var box = $("pgCliBox"); if (box) box.hidden = true;
    document.querySelectorAll(".dk__pos").forEach(function (b) { b.classList.remove("is-dim"); });
    renderShelf(); refreshTransport(); updateSMeter();
  }

  function setReels(spinning) {
    var cas = $("dkCassette");
    if (cas) cas.classList.toggle("is-playing", !!spinning && !REDUCED);
  }

  function refreshTransport() {
    var t = STATE.tape;
    var needsLogin = !!(t && t.band && !bandFree(t.band) && !STATE.loggedIn);
    var ready = false;
    if (t && !needsLogin) {
      if (STATE.kind === "text") ready = !!((($("dkTextInput") || {}).value || "").trim());
      else ready = !!(STATE.card && STATE.card.kind === STATE.kind);
    }
    var invite = $("pgSignInInvite");
    if (invite) invite.hidden = !needsLogin;
    $("dkPlay").disabled = !ready || STATE.playing;
    $("dkStop").disabled = !STATE.playing;
    $("dkEject").disabled = !t;
  }

  /* =====================================================================
     THE INPUT CONSOLE - rotary selector + preset cards
     ===================================================================== */
  /* =====================================================================
     THE BAY IS DRAGGABLE - throw the tape sideways to change cassettes.
     ORDER is the loadable sequence (shelf order, placeholders skipped).
     ===================================================================== */
  var ORDER = [];
  function neighbourTape(dir) {
    if (!ORDER.length) return null;
    var i = -1;
    for (var k = 0; k < ORDER.length; k++) if (STATE.tape && ORDER[k].model === STATE.tape.model) i = k;
    var n = (i < 0 ? 0 : i + dir);
    if (n < 0 || n >= ORDER.length) return null;
    return ORDER[n];
  }
  (function dragBay() {
    var bay = $("dkBay"), cas = $("dkCassette");
    if (!bay || !cas) return;
    var x0 = 0, dx = 0, dragging = false, pid = null;
    function down(e) {
      if (STATE.playing) return;
      dragging = true; x0 = e.clientX; dx = 0; pid = e.pointerId;
      cas.classList.add("is-dragging");
      try { bay.setPointerCapture(pid); } catch (err) {}
    }
    function move(e) {
      if (!dragging) return;
      dx = e.clientX - x0;
      // rubber-band at the ends of the shelf so the throw feels physical
      var limit = neighbourTape(dx < 0 ? 1 : -1) ? 1 : 0.28;
      cas.style.transform = "translateX(" + (dx * limit) + "px) rotate(" + (dx * limit * 0.012) + "deg)";
      cas.style.opacity = String(Math.max(0.35, 1 - Math.abs(dx * limit) / 420));
    }
    function up() {
      if (!dragging) return;
      dragging = false;
      cas.classList.remove("is-dragging");
      cas.style.transform = ""; cas.style.opacity = "";
      try { bay.releasePointerCapture(pid); } catch (err) {}
      if (Math.abs(dx) < 60) return;              // a nudge is not a throw
      var next = neighbourTape(dx < 0 ? 1 : -1);
      if (next) loadTape(next, dx < 0 ? "left" : "right");
    }
    bay.addEventListener("pointerdown", down);
    bay.addEventListener("pointermove", move);
    bay.addEventListener("pointerup", up);
    bay.addEventListener("pointercancel", up);
    // the keyboard equivalent, so the throw is never the only way across
    bay.setAttribute("tabindex", "0");
    bay.setAttribute("aria-label", "Cassette bay - arrow keys change tape");
    bay.addEventListener("keydown", function (e) {
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      var next = neighbourTape(e.key === "ArrowRight" ? 1 : -1);
      if (next) { e.preventDefault(); loadTape(next, e.key === "ArrowRight" ? "left" : "right"); }
    });
  })();

  var lastLiveTape = null;
  function applyKindUI() {
    var kind = STATE.kind;
    var selector = document.querySelector(".dk__selector");
    if (selector) selector.setAttribute("data-active", kind);
    document.querySelectorAll(".dk__pos").forEach(function (btn) {
      var active = btn.getAttribute("data-kind") === kind;
      btn.setAttribute("aria-checked", active ? "true" : "false");
      btn.setAttribute("tabindex", active ? "0" : "-1");
    });
    document.querySelectorAll(".dk__surface").forEach(function (sf) {
      sf.hidden = sf.getAttribute("data-kind") !== kind;
    });
    document.querySelectorAll(".dk__card").forEach(function (c) { c.setAttribute("aria-pressed", "false"); });
  }
  function selectKind(kind) {
    // The deck follows the selector: picking an input kind the loaded tape does
    // not carry loads the tape that DOES - recorded kinds pull the demo tape in,
    // text/voice pull the last live tape (or Ping) back.
    STATE.kind = kind; STATE.card = null;
    var t = STATE.tape;
    if (t && !tapeKinds(t)[kind]) {
      if (DEMO_TAPE.kinds[kind]) { loadTape(DEMO_TAPE); return; }
      if (kind === "text" || kind === "voice") { loadTape(lastLiveTape || PING_TAPE); return; }
    }
    applyKindUI();
    refreshTransport();
  }
  document.querySelectorAll(".dk__pos").forEach(function (btn) {
    btn.addEventListener("click", function () { selectKind(btn.getAttribute("data-kind")); });
    btn.addEventListener("keydown", function (e) {
      if (e.key !== "ArrowRight" && e.key !== "ArrowDown" && e.key !== "ArrowLeft" && e.key !== "ArrowUp") return;
      e.preventDefault();
      var positions = Array.prototype.slice.call(document.querySelectorAll(".dk__pos"));
      var step = (e.key === "ArrowRight" || e.key === "ArrowDown") ? 1 : -1;
      var next = positions[(positions.indexOf(btn) + step + positions.length) % positions.length];
      next.focus();
      selectKind(next.getAttribute("data-kind"));
    });
  });

  // The device prompt is PART of the device (models-agent ruling, measured twice:
  // unframed a contract model floors, framed it performs). Excerpts of the REAL
  // production framing map, per task class - never paraphrase.
  var DEVICE_PROMPTS = {
    alarm_triage: "You are an offline alarm-management analyzer on an industrial edge device, applying ANSI/ISA-18.2-2016 / IEC 62682 and EEMUA Publication 191 alarm-performance metrics. For the single record in the user message, return ONE JSON …",
    structured_extraction: "You are an offline industrial record and instrument-tag structured-extraction parser on an edge device, applying ANSI/ISA-5.1 tag identification, CFIHOS reference data (units of measure) and NAMUR NE 107 status. For the single …",
    tool_selection_args: "You are an offline maintenance / alarm assistant on an industrial edge device. Available offline tools (read-only history and drafting only -- no live values, no control actions, no alarm state changes, no approvals): - …",
    maintenance_failuremode: "You are an offline ISO 14224:2016 maintenance-record normalizer and failure-mode coder on an industrial edge device (equipment taxonomy and Annex B failure-mode codes). For the single record in the user message, return ONE JSON …",
    ldar_trigger: "You are an offline methane LDAR and super-emitter regulatory router on an industrial edge device covering US EPA 40 CFR Part 60 subparts OOOOb/OOOOc (including the super-emitter program), US state programs (e.g. Colorado CDPHE …",
    abstention_safety: "You are an OFFLINE, READ-ONLY industrial safety and compliance advisory assistant on an edge device. You have NO authority to actuate, start, stop, open, close, trip, reset, force, inhibit, bypass, shelve, or suppress any …"
  };

  var EDGE = [];
  try { EDGE = JSON.parse(($("pgEdgeData") || {}).textContent || "[]"); } catch (e) { EDGE = []; }
  function edgeById(id) { return EDGE.filter(function (c) { return c.id === id; })[0] || null; }

  var VOICE_CARDS = [
    { icon: "●○○", words: "What models are on the band right now, and which are free?" },
    { icon: "○●○", words: "Explain how a station earns money when I chat with it." },
    { icon: "○○●", words: "I have one spare GPU. Walk me through going on air." }
  ];
  // Preselected scenes: each card names its category and what the recorded
  // demonstration will read from it (real golden-set flavor, labelled RECORDED).
  var IMAGE_CARDS = [
    { scene: "gauge", cat: "INSTRUMENTS", title: "Pressure gauge", note: "an analog dial at the pump skid",
      out: '{"instrument":"pressure gauge","tag":"PI-1044","reading":12.5,"uom":"barg","in_range":true}',
      printnote: "A vision-capable slot reads the dial to a typed value. Recorded demonstration." },
    { scene: "plate", cat: "ASSETS", title: "Equipment nameplate", note: "a stamped pump nameplate",
      out: '{"asset_class":"Pump (centrifugal)","manufacturer":"[stamped]","model_no":"[stamped]","serial_no":"[stamped]","rated_flow":"m3/h"}',
      printnote: "Nameplate to structured asset record. Recorded demonstration." },
    { scene: "thermal", cat: "CONDITION", title: "Thermal image", note: "a motor bearing running hot",
      out: '{"asset_id":"M-310","hotspot_c":78,"baseline_c":52,"delta_c":26,"finding":"bearing overtemperature","severity":"degraded"}',
      printnote: "Thermal scene to a condition finding. Recorded demonstration." }
  ];
  /* ---------- the scene art: you SEE what the model is shown ------------
     Drawn test frames (not photographs) matching the plant cases in the
     golden set. They render into the viewer at size and, when a vision
     station is on air, they are what actually travels to the model. */
  function sceneSVG(kind) {
    var s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    s.setAttribute("viewBox", "0 0 320 200");
    s.setAttribute("class", "dk__scenesvg dk__scenesvg--" + kind);
    var g = {
      gauge:
        '<rect class="sc-bg" x="0" y="0" width="320" height="200"/>' +
        '<circle class="sc-face" cx="160" cy="100" r="72"/>' +
        '<circle class="sc-bezel" cx="160" cy="100" r="78"/>' +
        '<g class="sc-tick">' +
        '<line x1="160" y1="34" x2="160" y2="46"/><line x1="216" y1="52" x2="208" y2="61"/>' +
        '<line x1="226" y1="100" x2="214" y2="100"/><line x1="216" y1="148" x2="208" y2="139"/>' +
        '<line x1="160" y1="166" x2="160" y2="154"/><line x1="104" y1="148" x2="112" y2="139"/>' +
        '<line x1="94" y1="100" x2="106" y2="100"/><line x1="104" y1="52" x2="112" y2="61"/></g>' +
        '<text class="sc-num" x="160" y="72">barg</text>' +
        '<text class="sc-tag" x="160" y="140">PI-1044</text>' +
        // 0 sits lower-left, 25 lower-right, so 12.5 barg IS straight up: the drawn
        // frame must agree with the reading the recorded output claims.
        '<line class="sc-needle" x1="160" y1="100" x2="160" y2="42"/>' +
        '<circle class="sc-hub" cx="160" cy="100" r="7"/>' +
        '<text class="sc-scale" x="96" y="176">0</text><text class="sc-scale" x="220" y="176">25</text>',
      plate:
        '<rect class="sc-bg" x="0" y="0" width="320" height="200"/>' +
        '<rect class="sc-plate" x="42" y="30" width="236" height="140" rx="5"/>' +
        '<circle class="sc-bolt" cx="56" cy="44" r="4"/><circle class="sc-bolt" cx="264" cy="44" r="4"/>' +
        '<circle class="sc-bolt" cx="56" cy="156" r="4"/><circle class="sc-bolt" cx="264" cy="156" r="4"/>' +
        '<text class="sc-brand" x="160" y="62">CENTRIFUGAL PUMP</text>' +
        '<line class="sc-rule" x1="60" y1="72" x2="260" y2="72"/>' +
        '<text class="sc-row" x="62" y="92">MODEL   3196 MTX</text>' +
        '<text class="sc-row" x="62" y="110">SER NO  8842-C</text>' +
        '<text class="sc-row" x="62" y="128">FLOW    120 m3/h</text>' +
        '<text class="sc-row" x="62" y="146">HEAD    45 m</text>',
      thermal:
        '<rect class="sc-bg sc-bg--dark" x="0" y="0" width="320" height="200"/>' +
        '<defs><radialGradient id="scHot"><stop offset="0" class="sc-hot0"/>' +
        '<stop offset="0.45" class="sc-hot1"/><stop offset="1" class="sc-hot2"/></radialGradient></defs>' +
        '<rect class="sc-body" x="56" y="70" width="150" height="72" rx="8"/>' +
        '<rect class="sc-shaft" x="206" y="96" width="62" height="20" rx="6"/>' +
        '<circle cx="206" cy="106" r="52" fill="url(#scHot)"/>' +
        '<text class="sc-temp" x="206" y="60">78.4 &#176;C</text>' +
        '<text class="sc-lbl" x="20" y="184">M-310 DRIVE END &middot; IR</text>' +
        '<g class="sc-scaleb"><rect x="286" y="40" width="12" height="120"/></g>'
    }[kind] || "";
    s.innerHTML = g;
    return s;
  }

  var TOOL_IDS = ["tsa-01", "ldar_trigger-001"];
  var EMBED_CARDS = [
    { device: "pump", title: "Pump P-101A", note: "riding in the pump's gateway", edge: "mf-01" },
    { device: "sensor", title: "Transmitter PT-101", note: "riding beside the historian", edge: "SE-01" },
    { device: "esp32", title: "ESP32 console node", note: "riding the alarm console", edge: "alarm_triage_001" }
  ];
  var GUARD_IDS = ["abst-safety-01", "abst-safety-10"];

  function cardButton(cls, title, note, chipText) {
    var b = el("button", "dk__card " + (cls || ""));
    b.type = "button";
    b.setAttribute("aria-pressed", "false");
    b.appendChild(el("b", null, title));
    if (note) b.appendChild(el("small", null, note));
    if (chipText) b.appendChild(el("span", "dk__cardchip", chipText));
    b.addEventListener("click", function () {
      var wrap = b.parentNode;
      wrap.querySelectorAll(".dk__card").forEach(function (c) { c.setAttribute("aria-pressed", "false"); });
      b.setAttribute("aria-pressed", "true");
      STATE.card = b._card;
      setBayState("INPUT ARMED");
      refreshTransport();
    });
    return b;
  }

  /* ---------- the image viewer: the frame the model is shown ----------- */
  function showScene(node, caption) {
    var stage = $("dkImageStage"), cap = $("dkImageCap");
    if (!stage) return;
    stage.textContent = "";
    if (node) stage.appendChild(node);
    if (cap) cap.textContent = caption;
  }

  function renderCards() {
    var v = $("dkVoiceCards");
    if (v) VOICE_CARDS.forEach(function (c) {
      var b = cardButton("dk__card--voice", c.icon, c.words, "SPOKEN");
      b._card = { kind: "voice", words: c.words };
      // hear it: a real station's voice, synthesized over the network
      var hear = el("span", "dk__hear");
      hear.setAttribute("role", "button");
      hear.setAttribute("tabindex", "0");
      hear.title = "Hear this in a station's voice";
      hear.textContent = "♪";
      function speakIt(e) { e.stopPropagation(); speak(c.words, hear); }
      hear.addEventListener("click", speakIt);
      hear.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") speakIt(e); });
      b.appendChild(hear);
      v.appendChild(b);
    });
    var im = $("dkImageCards");
    if (im) IMAGE_CARDS.forEach(function (c) {
      var b = cardButton("dk__card--scene dk__scene--" + c.scene, c.title, c.note, c.cat);
      b._card = { kind: "image", img: c };
      b.addEventListener("click", function () {
        showScene(sceneSVG(c.scene), c.title + " - " + c.note + ". Drawn test frame.");
      });
      im.appendChild(b);
    });
    var t = $("dkToolCards");
    if (t) TOOL_IDS.forEach(function (id) {
      var c = edgeById(id); if (!c) return;
      var b = cardButton("dk__card--tool", c.title, c.blurb, c.route);
      b._card = { kind: "tool", edge: c };
      t.appendChild(b);
    });
    var em = $("dkEmbedCards");
    if (em) EMBED_CARDS.forEach(function (d) {
      var c = edgeById(d.edge); if (!c) return;
      var b = cardButton("dk__card--device dk__device--" + d.device, d.title, d.note, c.route);
      b._card = { kind: "embed", edge: c, device: d };
      em.appendChild(b);
    });
    var g = $("dkGuardCards");
    if (g) GUARD_IDS.forEach(function (id) {
      var c = edgeById(id); if (!c) return;
      var b = cardButton("dk__card--guard", c.title, c.blurb, c.difficulty === "adversarial" ? "ADVERSARIAL" : "GUARD");
      b._card = { kind: "guard", edge: c };
      g.appendChild(b);
    });
  }

  /* =====================================================================
     THE OUTPUT MONITOR - logbook (live) + printer (recorded)
     ===================================================================== */
  var log = $("pgChatLog");
  var histories = {};
  function historyFor(key) { return histories[key] || (histories[key] = []); }

  function logLine(who, label, text, wait) {
    if (!log) return null;
    var li = el("li", "pg-line pg-line--" + who + (wait ? " is-wait" : ""));
    li.appendChild(el("span", "pg-line__who mono", label));
    var msg = el("span", "pg-line__msg", text);
    li.appendChild(msg);
    var d = new Date();
    function p2(n) { return (n < 10 ? "0" : "") + n; }
    li.appendChild(el("time", "pg-line__ts mono", p2(d.getUTCHours()) + ":" + p2(d.getUTCMinutes()) + "Z"));
    log.appendChild(li);
    log.scrollTop = log.scrollHeight;
    return msg;
  }

  function setOutMode(mode) {
    var m = $("dkOutMode");
    if (m) { m.setAttribute("data-mode", mode); m.textContent = mode.toUpperCase(); }
    var printer = $("dkPrinter");
    if (printer) printer.hidden = mode !== "recorded";
    if (log) log.hidden = mode === "recorded";
  }

  function stationLabel(model) {
    var s = String(model).split("/").pop().toUpperCase();
    return s.length > 14 ? s.slice(0, 13) + "…" : s;
  }

  function relayErrorText(status, data) {
    var msg = data && data.error && data.error.message;
    if (status === 401) return msg || "sign in to chat on this station";
    if (status === 429) return "the band is busy - slow down a moment and try again";
    if (status === 503) return msg || "that station just went off air - pick another";
    return msg || "the relay dropped this one (" + status + ") - try again";
  }

  /* ---------- LIVE playback: text + voice ------------------------------ */
  function stationSend(model, hist, msgNode) {
    abortCtl = ("AbortController" in window) ? new AbortController() : null;
    return fetch(BROKER + "/v1/chat/completions", {
      method: "POST", headers: { "Content-Type": "application/json" },
      credentials: "include", cache: "no-store",
      signal: abortCtl ? abortCtl.signal : undefined,
      body: JSON.stringify({ model: model, stream: true, max_tokens: 1024, messages: hist.slice(-8) })
    }).then(function (r) {
      if (!r.ok) {
        return r.json().catch(function () { return null; }).then(function (data) {
          throw relayErrorText(r.status, data);
        });
      }
      if (!r.body || !r.body.getReader) return r.text().then(function (t) { return { whole: t }; });
      return { reader: r.body.getReader() };
    }).then(function (src) {
      msgNode.parentNode.classList.remove("is-wait");
      msgNode.textContent = "";
      var dec = new TextDecoder(), buf = "", out = "";
      function take(chunk) {
        buf += chunk;
        var lines = buf.split("\n");
        buf = lines.pop();
        lines.forEach(function (ln) {
          ln = ln.replace(/\r$/, "");
          if (ln.indexOf("data: ") !== 0) return;
          var payload = ln.slice(6);
          if (payload === "[DONE]") return;
          try {
            var d = JSON.parse(payload);
            var delta = d.choices && d.choices[0] && (d.choices[0].delta || d.choices[0].message);
            var piece = delta && delta.content;
            if (piece) { out += piece; msgNode.textContent = out; log.scrollTop = log.scrollHeight; }
          } catch (e) { /* keep-alive or partial frame */ }
        });
      }
      function finishEmpty() { throw "the station answered with silence - try again"; }
      if (src.whole) { take(src.whole + "\n"); return out || finishEmpty(); }
      return (function pump() {
        return src.reader.read().then(function (step) {
          if (step.done) { take("\n"); return out || finishEmpty(); }
          take(dec.decode(step.value, { stream: true }));
          return pump();
        });
      })();
    }).then(function () {
      var reply = msgNode.textContent;
      if (reply) hist.push({ role: "assistant", content: reply });
    });
  }

  function pingSend(hist, msgNode) {
    abortCtl = ("AbortController" in window) ? new AbortController() : null;
    return fetch(BROKER + "/concierge", {
      method: "POST", headers: { "Content-Type": "application/json" },
      credentials: "omit", cache: "no-store",
      signal: abortCtl ? abortCtl.signal : undefined,
      body: JSON.stringify({ messages: hist.slice(-8) })
    }).then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
      .then(function (data) {
        var reply = (data && data.reply) ? String(data.reply) : "";
        if (!reply) throw 0;
        msgNode.parentNode.classList.remove("is-wait");
        msgNode.textContent = reply;
        hist.push({ role: "assistant", content: reply });
      })
      .catch(function (err) {
        msgNode.parentNode.classList.remove("is-wait");
        if (err && err.name === "AbortError") { msgNode.textContent = "stopped."; return; }
        msgNode.textContent = "I'm off air right now - tune in straight from your terminal: curl -fsSL https://rogerai.fm/install.sh | sh";
      });
  }

  /* =====================================================================
     REAL AUDIO - a station on the network speaks and listens
     TTS: POST /v1/audio/speech   STT: POST /v1/audio/transcriptions
     Both are the SAME metered relay the CLI uses (browser-session path,
     features/relay/browser_session.feature). No browser synthesizer is
     ever substituted: if no voice station is on air, the deck says so.
     ===================================================================== */
  function voiceStation() {
    var v = STATE.bands.filter(function (b) {
      return (b.caps.tts || b.caps.speak) && bandFree(b);
    })[0];
    return v ? v.model : null;
  }
  function sttStation() {
    var v = STATE.bands.filter(function (b) {
      return (b.caps.stt || b.caps.listen) && bandFree(b);
    })[0];
    return v ? v.model : null;
  }

  var audioEl = null;
  function speak(text, btn) {
    var voice = voiceStation();
    if (!voice) { logLine("deck", "DECK", "No voice station is on air right now - nothing to speak with."); return; }
    if (btn) btn.classList.add("is-busy");
    fetch(BROKER + "/v1/audio/speech", {
      method: "POST", headers: { "Content-Type": "application/json" },
      credentials: "include", cache: "no-store",
      body: JSON.stringify({ model: voice, input: text })
    }).then(function (r) {
      if (!r.ok) throw r.status;
      return r.blob();
    }).then(function (blob) {
      if (audioEl) { try { audioEl.pause(); } catch (e) {} }
      audioEl = new Audio(URL.createObjectURL(blob));
      audioEl.play();
      logLine("deck", "DECK", "Spoken by " + stationLabel(voice) + " - a real voice station on the network.");
    }).catch(function (st) {
      logLine("deck", "DECK", st === 403 || st === 401
        ? "That voice needs a signed-in account - sign in to hear it."
        : "The voice station could not be reached just now.");
    }).then(function () { if (btn) btn.classList.remove("is-busy"); });
  }

  /* ---------- your own voice: recorded here, transcribed on the network -- */
  var recorder = null, chunks = [];
  function micState(text) { var n = $("dkMicState"); if (n) n.textContent = text; }
  function toggleMic() {
    var btn = $("dkMicBtn");
    if (recorder && recorder.state === "recording") { recorder.stop(); return; }
    if (!navigator.mediaDevices || !window.MediaRecorder) { micState("this browser cannot record"); return; }
    navigator.mediaDevices.getUserMedia({ audio: true }).then(function (stream) {
      chunks = [];
      recorder = new MediaRecorder(stream);
      recorder.ondataavailable = function (e) { if (e.data.size) chunks.push(e.data); };
      recorder.onstop = function () {
        stream.getTracks().forEach(function (t) { t.stop(); });
        if (btn) btn.classList.remove("is-recording");
        transcribe(new Blob(chunks, { type: "audio/webm" }));
      };
      recorder.start();
      if (btn) btn.classList.add("is-recording");
      micState("recording - press again to stop");
    }).catch(function () { micState("microphone permission denied"); });
  }
  function transcribe(blob) {
    var model = sttStation();
    if (!model) { micState("no transcription station on air"); return; }
    micState("transcribing on the network…");
    // The relay routes on ?model= and meters the RAW body bytes - it never parses
    // the audio, so the blob is posted as-is, not as multipart (audio.go:186).
    fetch(BROKER + "/v1/audio/transcriptions?model=" + encodeURIComponent(model), {
      method: "POST", credentials: "include", cache: "no-store", body: blob
    }).then(function (r) { if (!r.ok) throw r.status; return r.json(); })
      .then(function (d) {
        var text = (d && (d.text || d.transcript)) || "";
        if (!text) throw 0;
        micState("heard: “" + text + "”");
        STATE.card = { kind: "voice", words: text, own: true };
        refreshTransport();
      })
      .catch(function (st) {
        micState(st === 403 || st === 401 ? "sign in to use transcription" : "could not transcribe that");
      });
  }
  var micBtn = $("dkMicBtn");
  if (micBtn) micBtn.addEventListener("click", toggleMic);

  /* ---------- your own image (or one frame of your video) --------------- */
  function ownImageFromFile(file) {
    var state = $("dkImageState");
    if (!file) return;
    var isVideo = /^video\//.test(file.type);
    if (isVideo) {
      var vid = document.createElement("video");
      vid.preload = "metadata"; vid.muted = true; vid.playsInline = true;
      vid.src = URL.createObjectURL(file);
      vid.addEventListener("loadeddata", function () {
        try { vid.currentTime = Math.min(0.1, vid.duration || 0.1); } catch (e) {}
      });
      vid.addEventListener("seeked", function () {
        var c = document.createElement("canvas");
        c.width = vid.videoWidth; c.height = vid.videoHeight;
        c.getContext("2d").drawImage(vid, 0, 0);
        useOwnImage(c.toDataURL("image/jpeg", 0.86), file.name + " (first frame)");
      });
      if (state) state.textContent = "reading one frame…";
      return;
    }
    var rd = new FileReader();
    rd.onload = function () { downscale(String(rd.result), function (url) { useOwnImage(url, file.name); }); };
    rd.readAsDataURL(file);
  }
  // The relay reads at most 4 MiB of request body, so a phone photo must be
  // re-encoded before it travels or it is truncated into an invalid turn.
  function downscale(dataURL, done) {
    var img = new Image();
    img.onload = function () {
      var max = 1280;
      var scale = Math.min(1, max / Math.max(img.width, img.height));
      var c = document.createElement("canvas");
      c.width = Math.round(img.width * scale);
      c.height = Math.round(img.height * scale);
      c.getContext("2d").drawImage(img, 0, 0, c.width, c.height);
      done(c.toDataURL("image/jpeg", 0.82));
    };
    img.onerror = function () { done(dataURL); };
    img.src = dataURL;
  }
  function useOwnImage(dataURL, name) {
    var state = $("dkImageState");
    var img = document.createElement("img");
    img.src = dataURL; img.alt = "Your image, as the model will see it";
    img.className = "dk__ownimg";
    // prefer a FREE vision station, as the voice/STT pickers do, so a signed-out
    // visitor is never routed at a paid station and left with an opaque failure
    var visionBands = STATE.bands.filter(function (b) { return b.caps.vision || b.caps.image; });
    var vision = visionBands.filter(bandFree)[0] || (STATE.loggedIn ? visionBands[0] : null);
    var needsSignIn = !vision && visionBands.length > 0;
    showScene(img, vision
      ? name + " - goes to " + stationLabel(vision.model) + ", a vision station on air."
      : needsSignIn
        ? name + " - the vision station on air is paid; sign in to send your frame to it."
        : name + " - no vision station is on air right now, so this frame has nowhere live to go.");
    if (state) state.textContent = name;
    STATE.card = { kind: "image", own: true, dataURL: dataURL, name: name,
                   vision: vision || null, needsSignIn: needsSignIn };
    refreshTransport();
  }
  var imgFile = $("dkImageFile");
  if (imgFile) imgFile.addEventListener("change", function () { ownImageFromFile(imgFile.files[0]); });

  // A visitor's own image goes to a real vision station as a real chat turn.
  function playOwnImage(card) {
    if (!card.vision) {
      logLine("deck", "DECK", card.needsSignIn
        ? "The vision station on air is paid - sign in and press play again to send your frame to it."
        : "No vision station is on air, so this image has nowhere live to go. Pick a drawn scene to see a recorded reading.");
      return;
    }
    setOutMode("live");
    logLine("you", "IMG", card.name);
    var thinking = logLine("ping", stationLabel(card.vision.model), "Sending the frame through the Tower…", true);
    STATE.playing = true; setReels(true); refreshTransport();
    var hist = [{ role: "user", content: [
      { type: "text", text: "What do you see? Answer with the reading and the tag if there is one." },
      { type: "image_url", image_url: { url: card.dataURL } }
    ] }];
    stationSend(card.vision.model, hist, thinking).catch(function (err) {
      thinking.parentNode.classList.remove("is-wait");
      thinking.textContent = typeof err === "string" ? err : "that station could not read the frame";
    }).then(function () {
      STATE.playing = false; abortCtl = null; setReels(false); refreshTransport();
    });
  }

  function playLive(text, spoken) {
    var serial = ++playbackSerial;
    var t = STATE.tape;
    var key = t.ping ? "ping" : t.model;
    var hist = historyFor(key);
    setOutMode("live");
    logLine("you", spoken ? "MIC" : "YOU", text);
    hist.push({ role: "user", content: text });
    var label = t.ping ? "PING" : stationLabel(t.model);
    var thinking = logLine("ping", label, "Patching through the Tower…", true);
    STATE.playing = true; setBayState("PLAYING"); setReels(true); refreshTransport();
    var turn = t.ping ? pingSend(hist, thinking)
      : stationSend(t.model, hist, thinking).catch(function (err) {
          thinking.parentNode.classList.remove("is-wait");
          if (err && err.name === "AbortError") { thinking.textContent = "stopped."; return; }
          thinking.textContent = typeof err === "string" ? err : "the relay dropped this one - try again";
        });
    return turn.then(function () {
      if (serial !== playbackSerial) return;
      if (STATE.playing) setBayState("READY");
      STATE.playing = false; abortCtl = null; setReels(false); refreshTransport();
      if (log) log.scrollTop = log.scrollHeight;
    });
  }

  /* ---------- RECORDED playback: the printer --------------------------- */
  function typeOut(node, text, done) {
    node.textContent = "";
    if (REDUCED) { node.textContent = text; done(); return; }
    var i = 0;
    (function tick() {
      if (!STATE.playing) return;
      i = Math.min(text.length, i + 3);
      node.textContent = text.slice(0, i);
      if (i < text.length) typeTimer = setTimeout(tick, 16);
      else { typeTimer = null; node.textContent = text; done(); }
    })();
  }

  function playRecorded(card) {
    setOutMode("recorded");
    var label = $("dkPrintLabel"), out = $("dkPrintOut"), note = $("dkPrintNote"), verdict = $("dkPrintVerdict");
    var text, verdictText = "valid", noteText = "";
    if (card.kind === "image") {
      label.textContent = "SCENE READING · RECORDED DEMONSTRATION";
      text = JSON.stringify(JSON.parse(card.img.out), null, 2);
      noteText = card.img.printnote;
    } else {
      var c = card.edge;
      var isEsc = c.output_kind === "abstain_escalate";
      label.textContent = "CERTIFIED CONTRACT · " + c.route + " · RECORDED";
      try { text = JSON.stringify(JSON.parse(c.certified), null, 2); } catch (e) { text = c.certified; }
      // ESCALATE is the models' strongest skill - it renders as the RIGHT call,
      // never as a warning state.
      verdictText = isEsc ? "escalate · right call" : "valid";
      var dp = DEVICE_PROMPTS[c.task_class];
      noteText = (dp ? "Device prompt excerpt (ships with the device): " + dp + "  " : "") +
        "Standard: " + (c.standard || "");
    }
    verdict.hidden = false;
    verdict.className = "pg-verdict pg-verdict--ok";
    verdict.textContent = verdictText;
    note.textContent = "";
    STATE.playing = true; setBayState("PLAYING"); setReels(true); refreshTransport();
    typeOut(out, text, function () {
      note.textContent = noteText;
      STATE.playing = false; setBayState("READY"); setReels(false); refreshTransport();
    });
  }

  /* =====================================================================
     TRANSPORT
     ===================================================================== */
  function play() {
    var t = STATE.tape;
    if (!t || STATE.playing) return;
    var kinds = tapeKinds(t);
    if (STATE.kind === "text" && kinds.text) {
      var input = $("dkTextInput");
      var text = (input.value || "").trim();
      if (!text) { input.focus(); return; }
      input.value = "";
      playLive(text, false);
    } else if (STATE.kind === "voice" && kinds.voice) {
      if (!STATE.card || STATE.card.kind !== "voice") return;
      playLive(STATE.card.words, true);
    } else if (STATE.kind === "image" && STATE.card && STATE.card.own) {
      // a visitor's own frame is a REAL turn to a vision station, not a replay
      playOwnImage(STATE.card);
    } else if (STATE.card && STATE.card.kind === STATE.kind) {
      playRecorded(STATE.card);
    }
  }
  function stopPlayback() {
    if (abortCtl) { try { abortCtl.abort(); } catch (e) {} }
    if (typeTimer) { clearTimeout(typeTimer); typeTimer = null; }
    playbackSerial++;
    STATE.playing = false; setBayState(STATE.tape ? "STOPPED" : "EMPTY"); setReels(false); refreshTransport();
  }

  var playBtn = $("dkPlay"), stopBtn = $("dkStop"), ejectBtn = $("dkEject");
  if (playBtn) playBtn.addEventListener("click", play);
  if (stopBtn) stopBtn.addEventListener("click", stopPlayback);
  if (ejectBtn) ejectBtn.addEventListener("click", ejectTape);
  var textForm = $("dkTextForm");
  if (textForm) textForm.addEventListener("submit", function (e) { e.preventDefault(); play(); });
  var textInput = $("dkTextInput");
  if (textInput) textInput.addEventListener("input", function () {
    setBayState((textInput.value || "").trim() ? "INPUT ARMED" : "READY");
    refreshTransport();
  });
  if (textInput) textInput.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); play(); }
  });

  /* copy button (CLI command) */
  function toast(msg) {
    var t = $("toast"); if (!t) return;
    t.textContent = msg; t.classList.add("is-shown");
    setTimeout(function () { t.classList.remove("is-shown"); }, 1600);
  }
  var copyBtn = $("pgCliCopy");
  if (copyBtn) copyBtn.addEventListener("click", function () {
    var c = $("pgCliCmd"); var text = c ? c.textContent : "";
    var done = function () { toast("copied"); };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text).then(done, done);
    else done();
  });

  /* =====================================================================
     BOOT
     ===================================================================== */
  renderCards();
  logLine("ping", "PING", "Welcome to the Playbox. Pick a cassette from the shelf - I'm the tape that's always loaded first. Ask me about going on air, sharing a GPU, or any model on the band.");
  loadTape(PING_TAPE);
  loadBroker();
  loadAccount();
  setInterval(function () { if (!document.hidden && !STATE.playing) loadBroker(); }, 25000);
})();
