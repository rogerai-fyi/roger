/* =====================================================================
   RogerAI - Ping concierge (the homepage mascot chatbot).

   Two parts, both deliberately tiny + dependency-free (no runtime deps,
   CSS/SVG only; one shared rAF; full prefers-reduced-motion fallback):

   1) IDLE LIFE - while the chat is closed, Ping gets gentle life: a small
      random reposition within its dock area + a rotating radio-host phrase,
      both stepped on the page's shared --carrier beat. Reduced motion =>
      static (no drift, phrase shown once, no rotation).

   2) CHAT - clicking Ping opens a small, collapsible record-player / tape-
      deck surface (markup already in index.html, styled in site.css). It
      POSTs to broker /concierge (NO credentials), shows the reply scrolling
      through the "now playing" ticker + as a log line, and degrades to an
      "off air, tune in via the CLI" line if /concierge is unreachable -
      never a broken state. All chat wiring is LAZY (first open only).

   Note: #pingTag is owned by dial.js (it writes "on air"/"standing by" from
   live market state); this file never touches #pingTag. The rotating idle
   phrase lives on a separate #pingPhrase element so the two never fight.
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";

  var dock = document.getElementById("pingDock");
  var trigger = document.getElementById("pingTrigger");
  var deck = document.getElementById("pingDeck");
  if (!dock || !trigger || !deck) return;

  /* ---------------------------------------------------------------
     1) IDLE LIFE - shared-rAF drift + carrier-stepped phrase rotation
     --------------------------------------------------------------- */
  var phraseEl = document.getElementById("pingPhrase");
  var PHRASES = [
    "ON AIR", "STAND BY", "TUNING IN", "READING THE BAND",
    "CARRIER LOCKED", "NOW PLAYING: YOUR GPU", "70/30 SPLIT, LIVE",
    "SIGNED RECEIPTS ONLY", "CLEAR SIGNAL", "TAKING REQUESTS",
  ];
  // Carrier beat in ms, read from the design token (fallback 2200ms).
  var CARRIER = (function () {
    var v = getComputedStyle(document.documentElement).getPropertyValue("--carrier");
    var n = parseFloat(v);
    if (!n) return 2200;
    return /ms\s*$/.test(v) ? n : n * 1000;
  })();

  var open = false;
  var pi = 0;
  if (phraseEl) phraseEl.textContent = PHRASES[0];

  if (!REDUCED) {
    // One shared rAF for both the phrase swap (every ~3 carrier beats) and the
    // gentle drift (eased toward a fresh random target every ~2 carrier beats).
    var lastBeat = 0;
    var driftTarget = { x: 0, y: 0 };
    var drift = { x: 0, y: 0 };
    var nextDriftAt = 0, nextPhraseAt = 0;
    var DRIFT_R = 6; // px - small, stays "within its area"

    function pickDrift() {
      driftTarget.x = (Math.random() * 2 - 1) * DRIFT_R;
      driftTarget.y = (Math.random() * 2 - 1) * DRIFT_R;
    }
    pickDrift();

    var raf = null;
    function loop(t) {
      raf = requestAnimationFrame(loop);
      if (open || document.hidden) return; // idle life only while closed + visible
      if (!lastBeat) { lastBeat = t; nextDriftAt = t + CARRIER * 2; nextPhraseAt = t + CARRIER * 3; }

      if (t >= nextDriftAt) { pickDrift(); nextDriftAt = t + CARRIER * 2; }
      if (t >= nextPhraseAt && phraseEl) {
        pi = (pi + 1) % PHRASES.length;
        phraseEl.textContent = PHRASES[pi];
        nextPhraseAt = t + CARRIER * 3;
      }
      // ease toward the target (compositor-only transform on the trigger)
      drift.x += (driftTarget.x - drift.x) * 0.04;
      drift.y += (driftTarget.y - drift.y) * 0.04;
      trigger.style.transform = "translate(" + drift.x.toFixed(2) + "px," + drift.y.toFixed(2) + "px)";
    }
    raf = requestAnimationFrame(loop);
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && !raf) raf = requestAnimationFrame(loop);
    });
  }

  /* ---------------------------------------------------------------
     2) CHAT - lazy: nothing below runs until the first open
     --------------------------------------------------------------- */
  var wired = false;
  var log, ticker, tickerText, form, input, sendBtn, closeBtn, deckLight;
  var sending = false;

  function el(id) { return document.getElementById(id); }

  function wire() {
    if (wired) return;
    wired = true;
    log = el("pingLog");
    ticker = el("pingTicker");
    tickerText = el("pingTickerText");
    form = el("pingForm");
    input = el("pingInput");
    sendBtn = el("pingSend");
    closeBtn = el("pingClose");
    deckLight = el("pingDeckLight");

    // greeting once, in-theme
    addLine("ping", "You're tuned in. I'm Ping - ask me about going on air, sharing a GPU, or finding a station.");

    form.addEventListener("submit", onSubmit);
    if (closeBtn) closeBtn.addEventListener("click", closeDeck);
    deck.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { closeDeck(); trigger.focus(); }
    });
  }

  function addLine(who, text) {
    if (!log) return;
    var row = document.createElement("div");
    row.className = "pingdeck__line pingdeck__line--" + who;
    var tag = document.createElement("span");
    tag.className = "pingdeck__who mono";
    tag.textContent = who === "you" ? "YOU" : "PING";
    var body = document.createElement("span");
    body.className = "pingdeck__msg";
    body.textContent = text;
    row.appendChild(tag);
    row.appendChild(body);
    log.appendChild(row);
    log.scrollTop = log.scrollHeight;
    return body;
  }

  // Scroll a reply through the "now playing" ticker (one short pass), then leave
  // it as a calm label. Reduced motion: set it directly, no animation.
  function setTicker(text) {
    if (!tickerText) return;
    tickerText.textContent = text;
    if (REDUCED || !ticker) return;
    ticker.classList.remove("is-rolling");
    // force reflow so re-adding the class restarts the keyframe
    void ticker.offsetWidth;
    ticker.classList.add("is-rolling");
  }

  function setState(s) {
    // s: "standby" | "transmit" | "onair" | "offair"
    deck.setAttribute("data-ping-state", s);
    if (deckLight && deckLight.lastChild) {
      deckLight.lastChild.textContent =
        s === "transmit" ? "TRANSMITTING" : s === "offair" ? "OFF AIR" : "ON AIR";
    }
  }

  function onSubmit(e) {
    e.preventDefault();
    if (sending) return;
    var text = (input.value || "").trim();
    if (!text) return;
    input.value = "";
    addLine("you", text);
    send(text);
  }

  // keep a short rolling transcript for the broker (last few turns)
  var history = [];

  function send(text) {
    sending = true;
    sendBtn.disabled = true;
    deck.classList.add("is-transmitting");
    setState("transmit");
    setTicker("transmitting...");
    var thinking = addLine("ping", "...");
    history.push({ role: "user", content: text });

    fetch(BROKER + "/concierge", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // NO credentials: this surface holds no session/wallet.
      credentials: "omit",
      cache: "no-store",
      body: JSON.stringify({ messages: history.slice(-8) }),
    })
      .then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
      .then(function (data) {
        var reply = (data && data.reply) ? String(data.reply) : "";
        if (!reply) throw 0;
        thinking.textContent = reply;
        history.push({ role: "assistant", content: reply });
        setTicker(reply.length > 90 ? reply.slice(0, 90) + " ..." : reply);
        setState(data.via === "offair" ? "offair" : "onair");
        log.scrollTop = log.scrollHeight;
      })
      .catch(function () {
        // Never a broken state: Ping is off air, point to the CLI.
        thinking.textContent = "I'm off air right now - tune in straight from your terminal: curl -fsSL https://rogerai.fyi/install.sh | sh";
        setTicker("off air - tune in via the CLI");
        setState("offair");
        log.scrollTop = log.scrollHeight;
      })
      .then(function () {
        sending = false;
        sendBtn.disabled = false;
        deck.classList.remove("is-transmitting");
      });
  }

  function openDeck() {
    if (open) return;
    open = true;
    wire();
    deck.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
    dock.classList.add("is-open");
    if (!REDUCED) trigger.style.transform = ""; // settle the idle drift
    setState("standby");
    // focus the input on the next frame (after the reveal)
    requestAnimationFrame(function () { input && input.focus(); });
  }

  function closeDeck() {
    if (!open) return;
    open = false;
    deck.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    dock.classList.remove("is-open");
  }

  trigger.addEventListener("click", function () {
    open ? closeDeck() : openDeck();
  });
})();
