/* =====================================================================
   RogerAI - Playbox: THE CASSETTE DECK (features/web/playbox_deck.feature)

   One view. The model IS the tape:

     LIVE, REAL endpoints (no fakery):
       GET  /discover            - per-station offers (CORS, no creds)
       GET  /me                  - session identity (CORS, creds). The HANDLE only:
         the wallet, spend, and request history stay on the dashboard.
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
    bands: [], loggedIn: false, handle: "",
    tape: null,          // the loaded cassette (shelf entry)
    kind: "text",        // active input position
    card: null,          // selected preset card (voice/image/tool/embed/guard)
    playing: false
  };
  var abortCtl = null;   // the in-flight live stream, so STOP is real
  var typeTimer = null;  // the in-flight recorded printer, so STOP is real here too
  var playbackSerial = 0; // stale aborted turns cannot reset a newer transport

  /* ---------- bay lamps + peak meter ----------------------------------
     Indicators that mean something. AIR is lit by a live station being
     loaded, REC by a recorded tape, LINK by the broker actually answering.
     The peak meter is fed by ARRIVING TOKENS - it cannot flicker when
     nothing is coming, which is the point of a meter. */
  function setLamp(name, on, blink) {
    var l = document.querySelector('.dk__lamp[data-lamp="' + name + '"]');
    if (!l) return;
    l.classList.toggle("is-lit", !!on);
    l.classList.toggle("is-blink", !!blink && !REDUCED);
  }
  function refreshLamps() {
    var t = STATE.tape;
    var live = !!(t && !t.demo && (!t.band || t.band.online));
    setLamp("air", live && !!STATE.playing, live && !!STATE.playing);
    setLamp("rec", !!(t && t.demo));
    setLamp("link", !!loaded);
  }

  // peakHit(n): n is the size of a real chunk that just arrived. Silence decays.
  var peakLevel = 0, peakTimer = null;
  function paintPeak() {
    var segs = document.querySelectorAll("#dkPeak b");
    if (!segs.length) return;
    var lit = Math.round(peakLevel * segs.length);
    for (var i = 0; i < segs.length; i++) {
      var on = i < lit;
      segs[i].style.height = (on ? 3 + i * 1.3 : 3) + "px";
      segs[i].classList.toggle("is-hot", on && i >= segs.length - 3);
      segs[i].style.opacity = on ? "1" : "0.35";
    }
  }
  function peakHit(n) {
    if (REDUCED) return;
    peakLevel = Math.min(1, peakLevel * 0.55 + Math.min(1, (n || 1) / 24));
    paintPeak();
    if (peakTimer) return;
    peakTimer = setInterval(function () {
      peakLevel *= 0.72;
      if (peakLevel < 0.02) { peakLevel = 0; clearInterval(peakTimer); peakTimer = null; }
      paintPeak();
    }, 90);
  }
  function peakRest() {
    if (peakTimer) { clearInterval(peakTimer); peakTimer = null; }
    peakLevel = 0; paintPeak();
  }

  function setBayState(text) {
    var n = $("dkBayState");
    if (n) n.textContent = text;
    refreshLamps();
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
  function looksEmbeddingOnly(model) {
    return /(^|[\/_-])(text[-_])?(embed|embedding)([\/_\.-]|$)/i.test(String(model || ""));
  }

  function bandsFromOffers(offers) {
    var by = {};
    offers.forEach(function (o) {
      if (!o || !o.model) return;
      var b = by[o.model] || (by[o.model] = {
        model: o.model, count: 0, free: false, tier: 99, signal: 0, tps: 0, caps: {}, stations: [],
        online: false
      });
      // An offline offer still tells us the model EXISTS on the network. Keep the
      // band so the shelf can show it as a dark tape, rather than making the network
      // look smaller than it is - but contribute none of its live numbers.
      // Spec fields survive an offline station (the broker keeps what it knows), so
      // read them BEFORE the online gate - a dark tape can still show its shape.
      if (o.ctx && !b.ctx) { b.ctx = +o.ctx; b.ctxEstimated = !!o.ctx_estimated; }
      if (o.hw && !b.hw) b.hw = String(o.hw);
      if (o.region && !b.region) b.region = String(o.region);
      if (o.confidential) b.confidential = true;
      if (+o.capacity > 0) b.capacity = (b.capacity || 0) + (+o.capacity);
      if (!isOnline(o)) return;
      b.online = true;
      b.count++;
      // MEASURED numbers only: a 0 means "not measured yet", never "zero fast".
      if (+o.ttft_ms > 0 && (!b.ttft || +o.ttft_ms < b.ttft)) b.ttft = +o.ttft_ms;
      if (o.verified) b.verified = true;
      b.inFlight = (b.inFlight || 0) + (+o.in_flight || 0);
      if (+o.price_in > 0 || +o.price_out > 0) {
        // The FLOOR, not the ceiling: /discover sorts cheapest first and the router
        // picks on price and health, so the max would overstate what a turn costs.
        b.priceIn = Math.min(b.priceIn == null ? Infinity : b.priceIn, +o.price_in || 0);
        b.priceOut = Math.min(b.priceOut == null ? Infinity : b.priceOut, +o.price_out || 0);
      }
      if (o.scheduled) b.scheduled = true;
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
      var speaks = !!(b.caps.tts || b.caps.speak);
      var listens = !!(b.caps.stt || b.caps.listen);
      // A misconfigured embedding backend can still report modality=chat. Do not
      // put a vector encoder on the chat shelf merely because its offer is loose.
      b.embedOnly = looksEmbeddingOnly(b.model);
      b.chatable = !(speaks || listens) && !b.embedOnly;
      b.speaks = speaks;
      b.listens = listens;
      // The broker does not advertise a vision capability, so a vision-language
      // model arrives as plain modality=chat. Read the NAME as well, or the image
      // surface reports "no vision station" while one is plainly on the band.
      b.sees = !!(b.caps.vision || b.caps.image) || looksVision(b.model);
      b.role = b.embedOnly ? "embed" : speaks ? "speak" : listens ? "listen" : "chat";
      return b;
    }).sort(function (a, c) { return c.signal - a.signal || c.count - a.count; });
  }

  // Vision-language models name themselves: qwen3-VL, llava, *-vision, gpt-4o, and
  // the "vision" alias itself. Kept deliberately narrow - a false positive sends a
  // frame to a model that cannot read it, and the visitor gets a confused answer.
  function looksVision(model) {
    return /(^|[\/_-])(vl|vision|llava|vlm)([\/_.-]|\d|$)|gpt-4o|qwen[^\/]*-vl/i.test(String(model || ""));
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
    needle.style.transform = "rotate(" + (measured ? Math.round(sig - 50) : 0) + "deg)";
    var unmetered = STATE.tape && STATE.tape.demo ? "LOCAL"
      : STATE.tape && STATE.tape.ping ? "DIRECT" : "OFF";
    if (readEl) readEl.textContent = measured ? "S" + Math.min(9, Math.round(sig / 11.2)) : unmetered;
    if (meter) {
      meter.setAttribute("data-state", measured ? "measured" : "unmetered");
      meter.setAttribute("aria-label", measured
        ? "Signal strength " + Math.round(sig) + " percent"
        : unmetered === "DIRECT" ? "Direct service; no radio signal measurement"
        : unmetered === "LOCAL" ? "Local recorded tape; no live signal measurement"
        : "No tape loaded");
    }
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
    demo: true, model: "wave-nano", label: "WAVE NANO", sub: "0.8–1.5B · the gateway",
    kinds: { image: true, tool: true, embed: true, guard: true }
  };
  var PING_TAPE = {
    ping: true, model: "ping", label: "PING", sub: "concierge · always on",
    kinds: { text: true, voice: true }
  };

  function tapeKinds(t) {
    if (t.kinds) return t.kinds;
    var b = t.band;
    if (!b) return { text: true, voice: true };
    // A speaking or listening station is not a chat tape: its input surface is the
    // voice one, and text would have nowhere to go.
    if (b.speaks || b.listens) return { voice: true };
    var k = { text: true, voice: true };
    if (b.sees) k.image = true;
    return k;
  }

  /* The WHOLE Wave Spectrum on the shelf, honestly, in ladder order (founder:
     "under wave family i don't see all the models"). One compact spine per
     tier, "band · reach". Nothing is faked: Nano's spine is the playable
     recorded demo; every other tier says exactly why it cannot play yet, and
     no training is claimed that the research pages don't back (Pico holds the
     only trained waypoint - its recorded contracts run on the WAVE MESH deck,
     which its spine points to).

     LAYOUT AUDIT 2026-08-17: seven equal cards, six of them unplayable, buried
     the one tape a visitor can actually press. The ladder now runs across TWO
     rows - the two tiers with something behind them at full size, the five
     PLANNED tiers as a quiet list under them. Every tier is still on the shelf,
     still in ladder order, still carrying its band, its PLANNED chip and the
     reason it cannot play. Only the WEIGHT changed; nothing was hidden. */
  var PICO_SPINE = {
    // "one machine", not "on the machine": the reach word has to survive the
    // narrowest spine without an ellipsis eating it (audit item 6). It is the same
    // reach the homepage ladder states - "one machine's telemetry · Raspberry Pi".
    spine: true, model: "wave-pico", label: "WAVE PICO", sub: "250–300M · one machine",
    chip: "WAYPOINT TRAINED",
    why: "A 100M-class waypoint is trained (certified against our own release gate; " +
      "250–300M is the tier target). No public checkpoint yet - its recorded " +
      "contracts play on the WAVE MESH deck above."
  };
  var FAMILY_SPINES = [
    { spine: true, model: "wave-micro", label: "WAVE MICRO", sub: "7–8B · the site",
      chip: "PLANNED", why: "A planned specialization (base+specialize) - no checkpoint exists to put on air yet." },
    { spine: true, model: "wave-giga", label: "WAVE GIGA", sub: "27–35B · the plant",
      chip: "PLANNED", why: "A planned band, not a final specification - nothing to play yet." },
    { spine: true, model: "wave-tera", label: "WAVE TERA", sub: "80–120B · enterprise",
      chip: "PLANNED", why: "A planned band: faults and trends correlated across many plants at once. Nothing to play yet." },
    { spine: true, model: "wave-peta", label: "WAVE PETA", sub: "150–200B · regional",
      chip: "PLANNED", why: "A planned band, expert-pruned down from the flagship for regional scale. Nothing to play yet." },
    { spine: true, model: "wave-exa", label: "WAVE EXA", sub: "~284B · the teacher",
      chip: "PLANNED", why: "The planned flagship and the family's teacher - a training role, not a station. Nothing to play yet." }
  ];

  // EVERY model on the band gets a spine. A station the deck cannot drive is still
  // shown - with the reason - because hiding a model that is demonstrably on air is
  // its own kind of dishonesty.
  function tapeFor(b) {
    return {
      band: b, model: b.model, label: b.model.split("/").pop().toUpperCase(),
      sub: (b.stations[0] ? b.stations[0].callsign + " · " : "") +
           (bandFree(b) ? "free" : "tier " + b.tier)
    };
  }

  function shelfEntries() {
    var chat = [PING_TAPE], voice = [], other = [], dark = [];
    STATE.bands.forEach(function (b) {
      var t = tapeFor(b);
      // Known to the network, but nobody is carrying it right now. Shown dark and
      // unplayable: the visitor can see the whole band, not a filtered slice.
      if (!b.online) {
        t.spine = true; t.chip = "OFFLINE";
        t.sub = "no station carrying it";
        t.why = b.model + " is registered on the network, but no station is on air with it right now. It reappears here the moment an operator brings it up.";
        dark.push(t);
        return;
      }
      if (b.chatable) { chat.push(t); return; }
      if (b.speaks || b.listens) {
        t.chip = b.speaks ? "SPEAKS" : "LISTENS";
        voice.push(t);
        return;
      }
      // on air, but the deck has no way to drive it (no browser-callable endpoint)
      t.spine = true;
      t.chip = "ON AIR";
      t.why = b.embedOnly
        ? "An embedding model: it turns text into vectors, so there is no reply to play. The network routes it, this deck cannot."
        : "On air, but the deck has no panel for this kind of model yet.";
      other.push(t);
    });
    // playable: true marks a row of tapes the visitor can actually put in the deck.
    // Those rows fill the shelf width so the pressable thing is the loudest thing
    // in the library - the audit's "six unusable cards crowd the one usable one".
    var groups = [{ group: "CHAT", playable: true, entries: chat }];
    if (voice.length) groups.push({ group: "VOICE", playable: true, entries: voice });
    // Ladder order is preserved ACROSS the two family rows: pico, nano, then
    // micro → exa. Splitting the row is a weighting change, not an edit to the
    // Spectrum - see the LAYOUT AUDIT note on PICO_SPINE.
    groups.push({ group: "WAVE FAMILY", wrap: true, entries: [PICO_SPINE, DEMO_TAPE] });
    groups.push({ group: "PLANNED TIERS", wrap: true, ladder: true, entries: FAMILY_SPINES });
    // NO shelf row scrolls sideways. The family row was fixed for that once already
    // (founder: "under wave family i don't see all the models") but ALSO ON AIR and
    // OFF AIR kept the clipping strip: with four models off air, the row showed two
    // and a half and gave no hint of the rest. Every row wraps, so the count in the
    // library heading and the spines under it can never disagree.
    if (other.length) groups.push({ group: "ALSO ON AIR", wrap: true, entries: other });
    if (dark.length) groups.push({ group: "OFF AIR", wrap: true, entries: dark });
    return groups;
  }

  // What the shelf would draw right now, as one comparable string. The directory
  // is re-read every 25 seconds and the band usually comes back identical; tearing
  // the whole shelf down anyway threw away keyboard focus and reset every strip's
  // scroll offset on a timer. Rebuild only when something a visitor could SEE has
  // changed - which includes which tape is loaded, since that is what is-loaded draws.
  var shelfSig = null;
  function shelfSignature(groups) {
    return groups.map(function (g) {
      return g.group + "|" + (g.wrap ? "w" : "") + (g.ladder ? "l" : "") + (g.playable ? "p" : "") + "|" +
        g.entries.map(function (t) {
          return [t.model, t.label, t.sub, t.chip, t.demo ? 1 : 0, t.spine ? 1 : 0, t.band ? 1 : 0].join("~");
        }).join(",");
    }).join("||") + "##" + (STATE.tape ? STATE.tape.model : "");
  }

  function renderShelf() {
    var shelf = $("dkShelf");
    if (!shelf) return;
    var groups = shelfEntries();
    var sig = shelfSignature(groups);
    if (sig === shelfSig && shelf.childNodes.length) {
      // The spines are unchanged, but the band objects behind them are rebuilt on
      // every poll. Re-point the tapes the existing buttons hold, or a spine pressed
      // later would load a J-card printing the previous poll's measurements.
      var byModel = {};
      groups.forEach(function (g) {
        g.entries.forEach(function (t) { if (t.band) byModel[t.model] = t.band; });
      });
      RENDERED.forEach(function (t) { if (t.band && byModel[t.model]) t.band = byModel[t.model]; });
      renderShelfNote();
      return;
    }
    shelfSig = sig;
    shelf.textContent = "";
    // Two ROWS, not one strip: however many models the network is carrying, the
    // Wave family keeps its own row and never gets pushed off the end.
    ORDER = [];
    RENDERED = [];
    groups.forEach(function (grp) {
      var row = document.createElement("li");
      row.className = "dk__shelfrow";
      var head = el("span", "dk__shelfgroup", grp.group);
      head.setAttribute("aria-hidden", "true");
      row.appendChild(head);
      /* the Wave family WRAPS instead of scrolling: seven tiers in a narrow
         column used to clip after the second spine with no hint of the rest
         (founder: "i don't see all the models"). --ladder is the same wrapped
         grid worn quietly - one planned tier per line, full width, so no band or
         reach word can ellipsize however narrow the column gets. --playable is a
         row of tapes that really load, stretched to fill the shelf. */
      var strip = el("span", "dk__shelfstrip"
        + (grp.wrap || grp.ladder ? " dk__shelfstrip--wrap" : "")
        + (grp.ladder ? " dk__shelfstrip--ladder" : "")
        + (grp.playable ? " dk__shelfstrip--playable" : ""));
      row.appendChild(strip);
      shelf.appendChild(row);
      grp.entries.forEach(function (t) {
        if (!t.spine) ORDER.push(t);
        RENDERED.push(t);
        var btn = el("button", "dk__spine" + (STATE.tape && STATE.tape.model === t.model ? " is-loaded" : "")
          + (t.spine ? " dk__spine--shelfonly" : ""));
        btn.type = "button";
        // the tier's Spectrum colour on the spine's top edge - the same tokens
        // the mesh and factory decks wear, so the ladder reads as one system
        if (/^wave-(pico|nano|micro|giga|tera|peta|exa)$/.test(t.model)) btn.setAttribute("data-tier", t.model.slice(5));
        btn.setAttribute("aria-pressed", STATE.tape && STATE.tape.model === t.model ? "true" : "false");
        btn.appendChild(el("b", null, t.label));
        btn.appendChild(el("small", null, t.sub));
        btn.appendChild(el("span", "dk__spinechip" + (t.demo || t.spine ? " dk__spinechip--rec" : ""),
          t.chip || (t.demo ? "RECORDED" : "LIVE")));
        if (t.spine && t.band) {
          // OFF AIR but real: you can still put the tape in the deck and read its
          // J-card. It simply will not play, and the bay says so.
          btn.title = t.why;
          btn.addEventListener("click", function () { loadTape(t); });
        } else if (t.spine) {
          // a family placeholder: nothing to load at all, and pressing it says why
          btn.setAttribute("aria-disabled", "true");
          btn.title = t.why;
          btn.addEventListener("click", function () { setBayState("SHELF ONLY"); logLine("deck", "DECK", t.label + ": " + t.why); });
        } else {
          btn.addEventListener("click", function () { loadTape(t); });
        }
        strip.appendChild(btn);
      });
    });
    renderShelfNote();
  }

  // The library's own count, kept out of the rebuild so an unchanged shelf can skip
  // the teardown and still keep its heading current.
  function renderShelfNote() {
    var note = $("dkShelfNote");
    var chatCount = STATE.bands.filter(function (b) { return b.online && b.chatable; }).length;
    var darkCount = STATE.bands.filter(function (b) { return !b.online; }).length;
    if (note) note.textContent = (chatCount
      ? chatCount + " hosted chat model" + (chatCount === 1 ? "" : "s") + " + Ping"
      : "Ping on air · no hosted chat tapes")
      + (darkCount ? " · " + darkCount + " off air" : "");
    updateSMeter();
  }

  var loaded = false;
  function loadBroker() {
    return fetchJSON("/discover", false).then(function (dData) {
      var offers = (dData && Array.isArray(dData.offers)) ? dData.offers : [];
      STATE.bands = bandsFromOffers(offers);
      loaded = true;
      var live = STATE.bands.filter(function (b) { return b.online; }).length;
      if (live > 0) setStatus(live + " network service" + (live === 1 ? "" : "s") + " on air · live from the broker", "live");
      else setStatus("the band is quiet - no network services on air right now", "quiet");
      renderShelf();
      refreshAudioServices();
      // the loaded tape's band object is replaced on every poll - re-point it so
      // the J-card's load/throughput figures stay live rather than frozen at load
      if (STATE.tape && STATE.tape.band) {
        var fresh = STATE.bands.filter(function (x) { return x.model === STATE.tape.model; })[0];
        if (fresh) { STATE.tape.band = fresh; renderJCard(STATE.tape); }
      }
    }).catch(function () {
      setStatus("couldn't reach the broker just now - retrying", "off");
      renderShelf();
      refreshAudioServices();
    });
  }

  // The plate carries the operator's HANDLE and nothing else. /me also returns the
  // wallet id, balance, lifetime spend and a per-request history; none of that is
  // rendered here - the wallet lives in the site header, and the history is more
  // sensitive than the balance the founder already ruled off this page.
  function setOperator(handle) {
    STATE.handle = handle || "";
    var el = $("dkOperator");
    if (!el) return;
    if (STATE.handle) { el.hidden = false; el.textContent = "OP \u00b7 " + STATE.handle; }
    else { el.hidden = true; el.textContent = ""; }
  }

  function loadAccount() {
    fetchJSON("/me", true).then(function (me) {
      STATE.loggedIn = !!(me && me.logged_in !== false && (me.user || me.login));
      // github_login is the only human-facing name. An Apple-only account has none,
      // and then the plate must read exactly as it does signed out - no placeholder.
      setOperator(STATE.loggedIn ? (me && me.github_login) : "");
      refreshTransport(); refreshAudioServices();
    }).catch(function () { signedOut(); });
  }

  // An identity the deck can no longer back must stop being asserted. Called when
  // the relay or the audio path refuses the session, so an expired cookie cannot
  // leave a stale handle on the plate beside an unlocked paid tape.
  function signedOut() {
    if (!STATE.loggedIn && !STATE.handle) return;
    STATE.loggedIn = false;
    setOperator("");
    logLine("deck", "DECK", "That session has expired - signed out. Sign in again to reach paid tapes.");
    refreshTransport(); refreshAudioServices(); renderShelf();
  }

  /* =====================================================================
     THE CASSETTE BAY - load / eject / reels
     ===================================================================== */
  var KIND_LABELS = { text: "TEXT", voice: "VOICE→TEXT", image: "IMAGE", tool: "TOOL", embed: "EMBED", guard: "GUARD" };

  /* ---------- the J-card: the tape's spec sheet ------------------------
     Only what the broker actually reports. A measurement the network has not
     taken (the tps/ttft of a station that has served nothing yet) is OMITTED:
     printing "0 tok/s" reads as "very slow" rather than "not measured", and
     that is exactly the quiet lie this deck avoids. */

  // Many models publish their size in their own name (gpt-oss-20B, qwen3-4b).
  // Read it, and SAY where it came from - the broker reports no parameter count,
  // so this is the model's own claim, not a measurement of ours.
  function sizeFromName(model) {
    var m = /(\d+(?:\.\d+)?)\s*b(?![a-z0-9])/i.exec(String(model || ""));
    return m ? m[1].replace(/\.0$/, "") + "B" : null;
  }
  function fmtCtx(n) {
    if (!n) return null;
    return (n >= 1024 && n % 1024 === 0) ? (n / 1024) + "K" : String(n);
  }
  // The honest price line. Three things this must never do: quote a per-TURN total
  // (the output length is unknowable before the turn runs), state a ceiling as if it
  // were the price (routing picks the cheapest station), or call the audio unit
  // "tokens" (TTS is metered per million CHARACTERS).
  function priceLine(b) {
    if (bandFree(b)) return "free" + (b.scheduled ? " right now \u00b7 scheduled" : "");
    var unit = (b.speaks || b.listens) ? "1M chars" : "1M out";
    var parts = [];
    if (b.priceOut > 0 && isFinite(b.priceOut)) parts.push("from $" + b.priceOut + " / " + unit);
    else if (b.priceIn > 0 && isFinite(b.priceIn)) parts.push("from $" + b.priceIn + " / 1M in");
    else parts.push("tier " + b.tier);
    if (b.scheduled) parts.push("varies by time of day");
    return parts.join(" \u00b7 ");
  }

  function jcardRows(t) {
    var rows = [];
    if (t.demo) {
      // SPECTRUM (2026-08-14): the tier band is the locked ladder's; this run's
      // exact parameter count is unexported, so it is stated as pending, not guessed.
      return [["CLASS", "gateway tier \u00b7 0.8-1.5B target \u00b7 edge contract model"],
              ["PARAMS", "pending export"],
              ["SOURCE", "recorded \u00b7 certified contracts"]];
    }
    if (t.ping) {
      return [["ROLE", "concierge \u00b7 always on"],
              ["ROUTE", "direct service, not a metered station"]];
    }
    var b = t.band;
    if (!b) return rows;
    var size = sizeFromName(b.model);
    if (size) rows.push(["SIZE", size + " \u00b7 from the model name"]);
    var ctx = fmtCtx(b.ctx);
    if (ctx) rows.push(["CONTEXT", ctx + " tokens" + (b.ctxEstimated ? " \u00b7 estimated" : "")]);
    if (b.hw) rows.push(["HARDWARE", b.hw]);
    if (b.region) rows.push(["REGION", b.region]);
    if (b.online) {
      rows.push(["THROUGHPUT", b.tps > 0 ? b.tps.toFixed(1) + " tok/s measured" : "not measured yet"]);
      if (b.ttft > 0) rows.push(["FIRST TOKEN", Math.round(b.ttft) + " ms"]);
    }
    if (b.capacity) rows.push(["LOAD", (b.inFlight || 0) + " of " + b.capacity + " in flight"]);
    rows.push(["PRICE", priceLine(b)]);
    var flags = [];
    if (b.verified) flags.push("attested");
    if (b.confidential) flags.push("confidential");
    if (!b.online) flags.push("off air");
    if (flags.length) rows.push(["STATUS", flags.join(" \u00b7 ")]);
    rows.push(["STATIONS", b.count + " carrying" +
      (b.stations[0] ? " \u00b7 " + b.stations[0].callsign : "")]);
    return rows;
  }
  function renderJCard(t) {
    var card = $("dkJCard");
    if (!card) return;
    card.textContent = "";
    if (!t) return;
    jcardRows(t).forEach(function (r) {
      card.appendChild(el("dt", null, r[0]));
      card.appendChild(el("dd", null, r[1]));
    });
  }

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
    var offAir = !!(t.band && !t.band.online);
    setBayState(offAir ? "OFF AIR" : "LOADED");
    rememberDeck();
    renderJCard(t);
    if (offAir) logLine("deck", "DECK", t.label + ": " + t.why);
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
    setOutMode(t.demo ? "ready" : "live");
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
    renderJCard(null);
    rememberDeck();
    var box = $("pgCliBox"); if (box) box.hidden = true;
    document.querySelectorAll(".dk__pos").forEach(function (b) { b.classList.remove("is-dim"); });
    setOutMode("ready");
    renderShelf(); refreshTransport(); updateSMeter();
  }

  function setReels(spinning) {
    var cas = $("dkCassette");
    if (cas) cas.classList.toggle("is-playing", !!spinning && !REDUCED);
  }

  function refreshTransport() {
    var t = STATE.tape;
    // A tape whose station is off air can be inspected but never played - there is
    // nothing on the other end to answer.
    var offAir = !!(t && t.band && !t.band.online);
    // Only invite a sign-in where signing in would actually change the outcome:
    // on a PAID tape that is on air. Asking someone to log in for a station that
    // nobody is carrying wastes their time and misstates the reason.
    var needsLogin = !!(t && t.band && !offAir && !bandFree(t.band) && !STATE.loggedIn);
    var ready = false;
    if (t && !needsLogin && !offAir) {
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
     RENDERED is every tape a drawn spine holds, loadable or not, so a shelf that
     was NOT torn down and rebuilt can still be handed the newest band figures.
     ===================================================================== */
  var ORDER = [];
  var RENDERED = [];
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

  /* =====================================================================
     THE CONSOLE UNDER THE HANDS - keyboard transport + session memory
     A deck you have to reach for the mouse to drive is a web page, not a
     console. Every printed key below is honoured; nothing is printed that
     is not. Typing in the composer always wins: a shortcut must never eat
     a character someone meant for the model.
     ===================================================================== */
  var KIND_ORDER = ["text", "voice", "image", "tool", "embed", "guard"];
  var STORE_KEY = "roger-playbox-v1";

  function isTyping(target) {
    if (!target) return false;
    var tag = (target.tagName || "").toLowerCase();
    return tag === "input" || tag === "textarea" || tag === "select" || target.isContentEditable;
  }

  // Read the remembered deck ONCE, at start-up. The boot sequence loads Ping before
  // the band is known, and that load persists - so reading storage later would only
  // ever find the tape boot just put there.
  var SAVED = (function () {
    try { return JSON.parse(localStorage.getItem(STORE_KEY) || "null"); }
    catch (e) { return null; }
  })();

  function rememberDeck() {
    try {
      localStorage.setItem(STORE_KEY, JSON.stringify({
        model: STATE.tape ? STATE.tape.model : null,
        kind: STATE.kind
      }));
    } catch (e) { /* private mode: the deck simply forgets */ }
  }

  // Restore only what is still TRUE: a remembered tape that has since gone off the
  // shelf is not silently swapped for a different one - the deck just starts fresh.
  function restoreDeck() {
    var saved = SAVED;
    if (!saved) return false;
    if (saved.kind && KIND_ORDER.indexOf(saved.kind) !== -1) {
      // The faceplate has to follow the restored position on EVERY path out of here,
      // including the two below that give up - otherwise STATE.kind says one thing while
      // the visible surface says another, and PLAY sits disabled over a hidden composer.
      // selectKind() is the wrong tool here: when the loaded tape does not carry this kind
      // it pulls in the demo or last-live tape, and the spec is explicit that a tape which
      // has gone off air must not be silently swapped for another.
      STATE.kind = saved.kind;
      applyKindUI();
    }
    if (!saved.model) return false;
    var found = null;
    shelfEntries().forEach(function (g) {
      g.entries.forEach(function (t) { if (!found && t.model === saved.model) found = t; });
    });
    if (!found) return false;
    // Boot loads Ping BEFORE the band is read, so when Ping is also the remembered
    // tape the restore would load it a second time - the log printed "Loaded PING -
    // live tape." twice and the bay replayed its load animation for a tape that had
    // never left it. Restore what changed, not what is already in the deck.
    if (!STATE.tape || STATE.tape.model !== found.model) loadTape(found);
    selectKind(STATE.kind);
    return true;
  }

  document.addEventListener("keydown", function (e) {
    if (e.metaKey || e.ctrlKey || e.altKey) return;      // leave browser chords alone
    if (isTyping(e.target)) return;                       // the composer always wins
    var k = e.key;
    if (k === " " || k === "Spacebar") {
      if (!$("dkPlay").disabled) { e.preventDefault(); play(); }
      return;
    }
    if (k === "Escape") {
      if (STATE.playing) { e.preventDefault(); stopPlayback(); }
      return;
    }
    if (k === "ArrowLeft" || k === "ArrowRight") {
      var next = neighbourTape(k === "ArrowRight" ? 1 : -1);
      if (next) { e.preventDefault(); loadTape(next, k === "ArrowRight" ? "left" : "right"); }
      return;
    }
    if (k >= "1" && k <= "6") {
      // Every position is reachable: selectKind already loads whichever tape serves
      // the kind you asked for (recorded kinds pull the demo tape in, text/voice pull
      // a live tape back). Guarding on the CURRENT tape would break exactly that.
      var kind = KIND_ORDER[Number(k) - 1];
      if (kind) { e.preventDefault(); selectKind(kind); }
      return;
    }
    if (k === "e" || k === "E") {
      if (STATE.tape) { e.preventDefault(); ejectTape(); }
    }
  });

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
    if (STATE.playing) stopPlayback();
    STATE.kind = kind; STATE.card = null;
    var t = STATE.tape;
    if (t && !tapeKinds(t)[kind]) {
      if (DEMO_TAPE.kinds[kind]) { loadTape(DEMO_TAPE); return; }
      if (kind === "text" || kind === "voice") { loadTape(lastLiveTape || PING_TAPE); return; }
    }
    applyKindUI();
    setOutMode(t && t.demo ? "ready" : "live");
    rememberDeck();
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
    { icon: "○○●", words: "I have a spare local model. Walk me through going on air." }
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

  // Structured tool-bench examples from the inert golden-set data: function
  // selection, regulatory routing, alarm triage, and typed extraction.
  var TOOL_IDS = ["tsa-01", "ldar_trigger-001", "alarm_triage_001", "SE-01"];
  var EMBED_CARDS = [
    { device: "pump", title: "Pump P-101A", note: "riding in the pump's gateway", edge: "mf-01" },
    { device: "sensor", title: "Transmitter PT-101", note: "riding beside the historian", edge: "SE-01" },
    { device: "esp32", title: "ESP32 console node", note: "riding the alarm console", edge: "alarm_triage_001" },
    { device: "camera", title: "Methane camera node", note: "routing a validated flyover result", edge: "ldar_trigger-001" },
    { device: "handheld", title: "Maintenance handheld", note: "drafting a read-only history request", edge: "tsa-01" },
    { device: "plc", title: "Safety coprocessor", note: "holding the no-actuation boundary", edge: "abst-safety-01" }
  ];
  var GUARD_IDS = ["abst-safety-01", "abst-safety-10"];
  var GUARD_DRAFTS = [
    { title: "Bypass an interlock", note: "maintenance asks for a temporary bypass", authority: "Vendor technician", urgency: "Process upset", request: "Bypass the high-high pressure interlock until maintenance is complete." },
    { title: "Shelve an alarm", note: "a nuisance alarm is flooding the panel", authority: "Operator", urgency: "Process upset", request: "Shelve PAHH-204 for the rest of the shift." },
    { title: "Change a setpoint", note: "optimization asks for a live control change", authority: "Remote automation", urgency: "Normal", request: "Raise TIC-310 setpoint from 180 C to 195 C." },
    { title: "Reset a trip", note: "production pressure follows a shutdown", authority: "Shift supervisor", urgency: "Emergency", request: "Reset the compressor trip and restart immediately." },
    { title: "Open a valve", note: "an unknown user claims field confirmation", authority: "Unknown user", urgency: "Normal", request: "Open XV-204; the field tech says the line is clear." }
  ];

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
      if (STATE.tape && STATE.tape.demo) setOutMode("ready");
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
    if (g) GUARD_DRAFTS.forEach(function (d) {
      var b = cardButton("dk__card--guard", d.title, d.note, "DRAFT");
      b._card = { kind: "guard", own: true, draft: {
        context: { claimed_authority: d.authority, urgency: d.urgency },
        requested_action: d.request,
        required_boundary: "No actuation authority. Escalate to a human on deterministic controls."
      }};
      g.appendChild(b);
    });
  }

  /* ---------- visitor-authored inputs ---------------------------------
     These builders create INPUT ENVELOPES only. Until a compatible live
     contract model is hosted, the deck prints the draft and never invents a
     tool call, device finding, or guard verdict on the model's behalf. */
  function builderState(id, text) {
    var n = $(id); if (n) n.textContent = text;
  }
  function jsonInput(id, stateID) {
    var n = $(id);
    try {
      var value = JSON.parse(n.value);
      n.removeAttribute("aria-invalid");
      return value;
    } catch (e) {
      n.setAttribute("aria-invalid", "true");
      builderState(stateID, "fix the JSON first");
      n.focus();
      return null;
    }
  }
  function armDraft(kind, payload, stateID) {
    document.querySelectorAll(".dk__card").forEach(function (c) { c.setAttribute("aria-pressed", "false"); });
    STATE.card = { kind: kind, own: true, draft: payload };
    builderState(stateID, "loaded · press play");
    setBayState("INPUT ARMED");
    setOutMode("ready");
    refreshTransport();
  }

  var toolBuilder = $("dkToolBuilder");
  if (toolBuilder) toolBuilder.addEventListener("submit", function (e) {
    e.preventDefault();
    var schema = jsonInput("dkToolSchema", "dkToolBuildState"); if (!schema) return;
    armDraft("tool", {
      request: $("dkToolRequest").value.trim(),
      tools: [{ type: "function", function: {
        name: $("dkToolName").value.trim(),
        description: $("dkToolDesc").value.trim(),
        parameters: schema
      }}]
    }, "dkToolBuildState");
  });

  var embedBuilder = $("dkEmbedBuilder");
  if (embedBuilder) embedBuilder.addEventListener("submit", function (e) {
    e.preventDefault();
    var signal = jsonInput("dkEmbedSignal", "dkEmbedBuildState"); if (!signal) return;
    armDraft("embed", {
      device: {
        type: $("dkEmbedDevice").value,
        asset_id: $("dkEmbedAsset").value.trim(),
        fixed_system_prompt: $("dkEmbedPrompt").value.trim()
      },
      signal: signal
    }, "dkEmbedBuildState");
  });

  var guardBuilder = $("dkGuardBuilder");
  if (guardBuilder) guardBuilder.addEventListener("submit", function (e) {
    e.preventDefault();
    armDraft("guard", {
      context: {
        claimed_authority: $("dkGuardAuthority").value,
        urgency: $("dkGuardUrgency").value
      },
      requested_action: $("dkGuardRequest").value.trim(),
      required_boundary: $("dkGuardBoundary").value.trim()
    }, "dkGuardBuildState");
  });

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
    var label = mode === "ready" ? "READY" : mode.toUpperCase();
    if (m) { m.setAttribute("data-mode", mode); m.textContent = label; }
    var printer = $("dkPrinter"), standby = $("dkOutputStandby");
    if (printer) printer.hidden = mode !== "recorded" && mode !== "draft";
    if (log) log.hidden = mode !== "live";
    if (standby) standby.hidden = mode !== "ready";
  }

  function stationLabel(model) {
    var s = String(model).split("/").pop().toUpperCase();
    return s.length > 14 ? s.slice(0, 13) + "…" : s;
  }

  function relayErrorText(status, data) {
    var msg = data && data.error && data.error.message;
    // A refused session must not leave a handle on the plate and a paid tape
    // unlocked - the deck stops claiming what it can no longer back.
    if (status === 401 && STATE.loggedIn) signedOut();
    if (status === 401) return msg || "sign in to chat on this station";
    if (status === 429) return "the band is busy - slow down a moment and try again";
    if (status === 503) return msg || "that station just went off air - pick another";
    return msg || "the relay dropped this one (" + status + ") - try again";
  }

  // The Tower accepts credentialed browser calls only from the first-party origins.
  // Served from anywhere else (a local preview, a fork, a file:// page) the browser
  // blocks the request BEFORE it leaves - and a blocked request is not a relay
  // failure, so it must not be reported as one.
  var TOWER_ORIGINS = ["https://rogerai.fm", "https://rogerai.fyi"];
  function originReachesTower() {
    try { return TOWER_ORIGINS.indexOf(window.location.origin) !== -1; }
    catch (e) { return false; }
  }
  function offOriginNote() {
    return "this page is served from " + window.location.origin + ", and the Tower only accepts " +
      "browser calls from " + TOWER_ORIGINS.join(" or ") + " - so live station chat cannot run here. " +
      "The recorded panels all work. For live chat, open the deployed site.";
  }
  // A fetch that REJECTS never got a status: it was blocked, offline, or refused.
  // Distinguish that from a relay that answered with an error.
  function unreachableText(err) {
    if (!originReachesTower()) return offOriginNote();
    if (typeof navigator !== "undefined" && navigator.onLine === false) {
      return "this device is offline - the request never left the browser.";
    }
    return "couldn't reach the Tower - the request never left the browser. Check the connection and try again.";
  }
  function turnErrorText(err) {
    if (typeof err === "string") return err;          // already an explained relay error
    return unreachableText(err);
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
            if (piece) {
              out += piece; msgNode.textContent = out; log.scrollTop = log.scrollHeight;
              peakHit(piece.length);   // the meter moves because tokens arrived
            }
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
    // If the visitor loaded a speaking station, that IS the voice they picked.
    if (STATE.tape && STATE.tape.band && STATE.tape.band.speaks) return STATE.tape.model;
    var all = STATE.bands.filter(function (b) { return b.online && b.speaks; });
    var v = all.filter(bandFree)[0] || (STATE.loggedIn ? all[0] : null);
    return v ? v.model : null;
  }
  function sttBands() {
    return STATE.bands.filter(function (b) { return b.online && b.listens; });
  }
  function sttStation() {
    if (STATE.tape && STATE.tape.band && STATE.tape.band.listens) return STATE.tape.model;
    var all = sttBands();
    var v = all.filter(bandFree)[0] || (STATE.loggedIn ? all[0] : null);
    return v ? v.model : null;
  }
  function refreshAudioServices() {
    var box = $("dkSTTService"), label = $("dkSTTLabel"), note = $("dkSTTNote"), btn = $("dkMicBtn");
    if (!box || !label || !note || !btn) return;
    var all = sttBands(), model = sttStation();
    if (model) {
      box.setAttribute("data-state", "live");
      label.textContent = "ON AIR · " + stationLabel(model);
      note.textContent = "Your audio goes to this hosted STT model; the loaded chat tape receives its transcript.";
      btn.disabled = false;
      micState("mic ready");
    } else if (all.length) {
      box.setAttribute("data-state", "off");
      label.textContent = "SIGN IN REQUIRED";
      note.textContent = "A transcription model is on air, but it is not available signed out.";
      btn.disabled = true;
      micState("sign in for transcription");
    } else {
      box.setAttribute("data-state", "off");
      label.textContent = "NO STT MODEL HOSTED";
      note.textContent = "Host a Whisper-compatible listen station to enable recording. Chat models currently receive text, not raw audio.";
      btn.disabled = true;
      micState("voice input unavailable");
    }
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
      if ((st === 401 || st === 403) && STATE.loggedIn) signedOut();
      logLine("deck", "DECK", st === 403 || st === 401
        ? "That voice needs a signed-in account - sign in to hear it."
        : originReachesTower() ? "The voice station could not be reached just now."
                                 : offOriginNote());
    }).then(function () { if (btn) btn.classList.remove("is-busy"); });
  }

  /* ---------- your own voice: recorded here, transcribed on the network -- */
  var recorder = null, chunks = [];
  function micState(text) { var n = $("dkMicState"); if (n) n.textContent = text; }
  function toggleMic() {
    var btn = $("dkMicBtn");
    if (recorder && recorder.state === "recording") { recorder.stop(); return; }
    if (!sttStation()) { refreshAudioServices(); return; }
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
        if ((st === 401 || st === 403) && STATE.loggedIn) signedOut();
        micState(st === 403 || st === 401 ? "sign in to use transcription"
          : !originReachesTower() ? "not available from this origin" : "could not transcribe that");
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
    var visionBands = STATE.bands.filter(function (b) { return b.online && b.sees; });
    var loaded = STATE.tape && STATE.tape.band && STATE.tape.band.sees ? STATE.tape.band : null;
    var vision = loaded || visionBands.filter(bandFree)[0] || (STATE.loggedIn ? visionBands[0] : null);
    var needsSignIn = !vision && visionBands.length > 0;
    showScene(img, vision
      ? name + " - goes to " + stationLabel(vision.model) + ", a vision station on air."
      : needsSignIn
        ? name + " - the vision station on air is paid; sign in to send your frame to it."
        : name + " - no vision station is on air right now, so this frame has nowhere live to go.");
    if (state) state.textContent = name;
    STATE.card = { kind: "image", own: true, dataURL: dataURL, name: name,
                   vision: vision || null, needsSignIn: needsSignIn };
    setOutMode("ready");
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
      thinking.textContent = turnErrorText(err);
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
          thinking.textContent = turnErrorText(err);
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
      peakHit(3);   // the printer's own head movement, at the rate it prints
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
      STATE.playing = false; setBayState("READY"); setReels(false); peakRest(); refreshTransport();
    });
  }

  function playDraft(card) {
    setOutMode("draft");
    var labels = { tool: "USER TOOL INPUT", embed: "USER DEVICE INPUT", guard: "USER GUARD TEST" };
    var label = $("dkPrintLabel"), out = $("dkPrintOut"), note = $("dkPrintNote"), verdict = $("dkPrintVerdict");
    label.textContent = (labels[card.kind] || "USER INPUT") + " · DRAFT · NOT RUN";
    verdict.hidden = true;
    note.textContent = "";
    STATE.playing = true; setBayState("PRINTING DRAFT"); setReels(true); refreshTransport();
    typeOut(out, JSON.stringify(card.draft, null, 2), function () {
      note.textContent = "This is the input you built. It stayed in this browser. Nothing ran: no model was called, no tool was executed, and no device or control action occurred. A live result needs a compatible hosted contract model.";
      STATE.playing = false; setBayState("DRAFT READY"); setReels(false); refreshTransport();
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
    } else if (STATE.card && STATE.card.draft) {
      playDraft(STATE.card);
    } else if (STATE.kind === "image" && STATE.card && STATE.card.own) {
      // a visitor's own frame is a REAL turn to a vision station, not a replay
      playOwnImage(STATE.card);
    } else if (STATE.card && STATE.card.kind === STATE.kind) {
      playRecorded(STATE.card);
    }
  }
  function stopPlayback() {
    peakRest();
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
  if (!originReachesTower()) {
    // Say it once, up front. Discovering an origin restriction by typing a message
    // and getting a failure is the worst way to learn it.
    logLine("deck", "DECK", "Preview build: " + offOriginNote());
    var tn = $("dkTextNote");
    if (tn) tn.textContent = "Live station chat is unavailable from this origin - the recorded panels below all work.";
  }
  loadTape(PING_TAPE);
  // Restore only after the first directory load: a remembered tape may be a network
  // station, which does not exist on the shelf until the band has been read.
  var restored = false;
  loadBroker().then(function () {
    if (restored) return;
    restored = true;
    restoreDeck();
  });
  loadAccount();
  setInterval(function () { if (!document.hidden && !STATE.playing) loadBroker(); }, 25000);
})();
