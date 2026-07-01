/* =====================================================================
   RogerAI - Ping concierge (the homepage mascot chatbot).

   Two parts, both deliberately tiny + dependency-free (no runtime deps,
   CSS/SVG only; one shared rAF; full prefers-reduced-motion fallback):

   1) ALWAYS-ON MASCOT - the mascot is always present with a scrolling
      LED banner to its RIGHT (#pingBanner). While the chat is closed the
      banner rolls FLAVOR phrases (a station idle loop) and the mascot
      gets gentle life (a small carrier-stepped drift). When Ping replies,
      the reply SCROLLS across this same banner, like a now-playing
      display. Reduced motion => static (no drift, no marquee; the banner
      just shows the latest text).

   2) CHAT POPUP - clicking the mascot OR the banner opens a small,
      draggable record-player / tape-deck surface (markup in index.html,
      styled in site.css) for TYPING. It POSTs to broker /concierge (NO
      credentials), shows the reply as a log line AND scrolling on the
      always-on banner, and degrades to an "off air, tune in via the CLI"
      line if /concierge is unreachable - never a broken state. The popup
      is draggable by its header (kept on-screen, position remembered in
      localStorage), and the X reliably closes it. All chat wiring is LAZY
      (first open only).

   Status ownership: #pingTag is the SINGLE status label, owned by
   teaser.js on the homepage (it writes "on air"). This file NEVER touches
   #pingTag and the banner carries NO status words - only flavor +
   responses. The popup header shows just "PING / CONCIERGE" with a small
   live dot (no "ON AIR" text), so there is exactly one status anywhere.
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var POS_KEY = "rogerai.ping.pos";

  var dock = document.getElementById("pingDock");
  var trigger = document.getElementById("pingTrigger");
  var banner = document.getElementById("pingBanner");
  var bannerText = document.getElementById("pingBannerText");
  var deck = document.getElementById("pingDeck");
  if (!dock || !trigger || !deck) return;

  /* ---------------------------------------------------------------
     1) ALWAYS-ON MASCOT - flavor banner + gentle drift (shared rAF)
     FLAVOR phrases only: NO status words ("ON AIR"/"STAND BY") - the
     single status lives on #pingTag (owned by teaser.js).
     --------------------------------------------------------------- */
  var PHRASES = [
    // lead phrase (kept first; the rest are shuffled below)
    "tune in / share / earn",
    // --- the originals ---
    "now playing: your GPU",
    "70/30 split, live",
    "signed receipts only",
    "two-way radio for GPUs",
    "taking requests",
    "find a station, pay per token",
    "operators going on air worldwide",
    "your hardware, your channel",
    "clear signal, fair split",
    // --- ham-radio idiom ---
    "roger that",
    "reading you five by five",
    "CQ CQ — any station on the band?",
    "73 and good tokens",
    "QSL: receipt confirmed",
    "the bands are open tonight",
    "working the GPU bands",
    "low latency, clean carrier",
    "over to you",
    "come back on this frequency",
    "ragchew on the coder band",
    "spot the strongest station",
    "antenna up, tokens out",
    "patch me through to a home GPU",
    "DX is calling — a model far away",
    "field day, every day",
    "break, break — incoming request",
    "squelch open, ready to copy",
    "low SWR, clean signal",
    "ears on the band",
    "mic check on the GPU band",
    "hand-keyed, co-signed, settled",
    "elmer-approved billing",
    "coming in loud and clear",
    "signal check — receipts pass",
    "roger, roger — token settled",
    "wind the dial, find a model",
    // --- community / free-form radio ---
    "community-run, token-funded",
    "listener-supported, operator-owned",
    "free-form on the GPU dial",
    "the request line is open",
    "dedications to your bots",
    "your local frequency for LLMs",
    "no playlist — just your prompts",
    "the airwaves belong to operators",
    "tune the dial, not the corporation",
    "an open frequency for everyone",
    "radio for machines, run by people",
    "small station, big signal",
    "operators welcome — lurkers too",
    "keep the channel open",
    // --- the marketplace, in radio idiom ---
    "ride the airwaves, pay per token",
    "your GPU is paying rent",
    "home GPUs, on the dial",
    "dial in a band, read the signal",
    "the band sets the price",
    "more stations, stronger signal",
    "borrow a model, lend your own",
    "lineage on every token",
    "co-signed, both ways",
    "no static on the billing",
    "clear channel, fair share",
    "pay per token, settle per receipt",
    "price drifts with the band",
    "strong signal, fair margins",
    "clean carrier, clear conscience",
    "your prompt, their silicon, your receipt",
    "the band remembers every token",
    "call-signs, not hostnames",
    "lend a watt, earn a token",
    "the frequency is yours",
    "catch a station before it drifts",
    "a marketplace on the dial",
    "honest band, honest bill",
    "read the band before you tune in",
    "the strongest signal takes your prompt",
    "broadcasting spare GPU, worldwide",
    "tune in from the CLI",
    "one dial, both sides of the radio",
    "find your frequency",
    "swap models like swapping bands",
    "amateur radio, professional uptime",
    "your call-sign, your channel",
    "patching you through to a GPU",
    "the band fills, the price falls",
    "lend your rig, keep 70%",
    "see you on the band",
    "the dial is live, the band is real",
    "every watt earns its keep",
    "signal verified, lineage intact",
    "your spare GPU, someone's fast token",
    "tune past the static to a clean station",
    "the band is busy tonight",
    "point your bot at the band",
    "open band, open market",
    "real stations, real signal",
    "broadcast your spare cycles",
    "a home GPU near you is on air",
    "drop-in endpoint, ham-radio heart",
    "every token accounted for",
    "the dial never sleeps",
    "somewhere, a station just went live",
  ];
  // Keep the lead phrase first, then shuffle the rest (Fisher-Yates over indices 1..n) so
  // the idle reel feels fresh and varies its order on each visit instead of a fixed loop.
  for (var shi = PHRASES.length - 1; shi > 0; shi--) {
    var shj = 1 + Math.floor(Math.random() * shi);
    var sht = PHRASES[shi]; PHRASES[shi] = PHRASES[shj]; PHRASES[shj] = sht;
  }
  // Carrier beat in ms, read from the design token (fallback 2200ms).
  var CARRIER = (function () {
    var v = getComputedStyle(document.documentElement).getPropertyValue("--carrier");
    var n = parseFloat(v);
    if (!n) return 2200;
    return /ms\s*$/.test(v) ? n : n * 1000;
  })();

  var open = false;
  var pi = 0;
  var replyShowing = false; // when true, the banner is showing a reply (pause idle rotation)

  // Set a flavor/idle phrase on the banner and (re)start the marquee roll.
  function setBanner(text, isReply) {
    if (!bannerText) return;
    bannerText.textContent = text;
    replyShowing = !!isReply;
    if (REDUCED || !banner) return;
    banner.classList.remove("is-rolling");
    void banner.offsetWidth; // reflow so the keyframe restarts
    banner.classList.add("is-rolling");
  }
  if (bannerText) setBanner(PHRASES[0], false);

  if (!REDUCED) {
    // One shared rAF: gentle mascot drift + carrier-stepped flavor rotation
    // on the banner (only while a reply is NOT being shown).
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
      if (document.hidden) return;
      if (!lastBeat) { lastBeat = t; nextDriftAt = t + CARRIER * 2; nextPhraseAt = t + CARRIER * 3; }

      if (t >= nextDriftAt) { pickDrift(); nextDriftAt = t + CARRIER * 2; }
      // rotate flavor phrases on the always-on banner, but never while a reply
      // is on display (that would clobber the now-playing reply).
      if (t >= nextPhraseAt) {
        if (!replyShowing) {
          pi = (pi + 1) % PHRASES.length;
          setBanner(PHRASES[pi], false);
        }
        nextPhraseAt = t + CARRIER * 3;
      }
      // gentle mascot drift only while closed (the popup wants a steady anchor)
      if (!open) {
        drift.x += (driftTarget.x - drift.x) * 0.04;
        drift.y += (driftTarget.y - drift.y) * 0.04;
        trigger.style.transform = "translate(" + drift.x.toFixed(2) + "px," + drift.y.toFixed(2) + "px)";
      }
    }
    raf = requestAnimationFrame(loop);
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && !raf) raf = requestAnimationFrame(loop);
    });
  }

  /* ---------------------------------------------------------------
     2) CHAT POPUP - lazy: nothing below runs until the first open
     --------------------------------------------------------------- */
  var wired = false;
  var log, form, input, sendBtn, closeBtn, head, deckLight;
  var sending = false;

  function el(id) { return document.getElementById(id); }

  function wire() {
    if (wired) return;
    wired = true;
    log = el("pingLog");
    form = el("pingForm");
    input = el("pingInput");
    sendBtn = el("pingSend");
    closeBtn = el("pingClose");
    head = el("pingHead");
    deckLight = el("pingDeckLight");

    // greeting once, in-theme
    addLine("ping", "You're tuned in. I'm Ping - ask me about going on air, sharing a GPU, or finding a station.");

    form.addEventListener("submit", onSubmit);
    if (closeBtn) closeBtn.addEventListener("click", function (e) {
      e.preventDefault();
      e.stopPropagation();
      closeDeck();
      trigger.focus();
    });
    deck.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { closeDeck(); trigger.focus(); }
    });

    // Click anywhere in the deck body (outside the form controls) focuses the
    // input, so a plain mouse click lands on typing even if it misses the field.
    deck.addEventListener("click", function (e) {
      if (sending) return;
      if (e.target.closest("button") || e.target.closest("a")) return;
      if (e.target === input) return;
      input && input.focus();
    });
    // Defensive: clicking the input always focuses it (some browsers swallow
    // the first click if a sibling stole pointer events before this pass).
    if (input) input.addEventListener("mousedown", function () { input.focus(); });

    makeDraggable();
    restorePos();
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

  function setState(s) {
    // s: "standby" | "transmit" | "onair" | "offair"
    deck.setAttribute("data-ping-state", s);
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
    // While waiting, roll a descriptive radio-themed status instead of a bare "..." - the
    // broker may scan for a free on-air station before the DJ answers (up to ~60s when
    // nothing's on air), so the wait should read as "searching", not "stuck". Cycles until
    // the reply lands (cleared in the settle .then below). Static first line under reduced-motion.
    var WAIT_MSGS = [
      "Searching for an available free station...",
      "Scanning the band for a clear signal...",
      "Hailing on-air operators...",
      "Still tuning you in - hang tight...",
      "Patching you through to the DJ...",
    ];
    setBanner(WAIT_MSGS[0], true);
    var thinking = addLine("ping", WAIT_MSGS[0]);
    var wi = 0;
    var waitTimer = REDUCED ? 0 : setInterval(function () {
      wi = (wi + 1) % WAIT_MSGS.length;
      thinking.textContent = WAIT_MSGS[wi];
      setBanner(WAIT_MSGS[wi], true);
    }, 4000);
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
        // the reply scrolls across the ALWAYS-ON banner (now-playing display)
        setBanner(reply.length > 120 ? reply.slice(0, 120) + " ..." : reply, true);
        setState(data.via === "offair" ? "offair" : "onair");
        log.scrollTop = log.scrollHeight;
      })
      .catch(function () {
        // Never a broken state: Ping is off air, point to the CLI.
        thinking.textContent = "I'm off air right now - tune in straight from your terminal: curl -fsSL https://rogerai.fyi/install.sh | sh";
        setBanner("off air - tune in via the CLI", true);
        setState("offair");
        log.scrollTop = log.scrollHeight;
      })
      .then(function () {
        if (waitTimer) clearInterval(waitTimer); // stop the "searching..." roll
        sending = false;
        sendBtn.disabled = false;
        deck.classList.remove("is-transmitting");
      });
  }

  /* ---- draggable popup (grab the header), kept on-screen, remembered ---- */
  function clamp(v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }

  function applyPos(left, top) {
    var w = deck.offsetWidth || 340;
    var h = deck.offsetHeight || 240;
    left = clamp(left, 6, Math.max(6, window.innerWidth - w - 6));
    top = clamp(top, 6, Math.max(6, window.innerHeight - h - 6));
    deck.style.left = left + "px";
    deck.style.top = top + "px";
    deck.style.right = "auto";
    deck.style.bottom = "auto";
    deck.style.transform = "none"; // override the centered default
    deck.classList.add("is-dragged");
    return { left: left, top: top };
  }

  function savePos(p) {
    try { localStorage.setItem(POS_KEY, JSON.stringify(p)); } catch (e) {}
  }

  function restorePos() {
    var raw;
    try { raw = localStorage.getItem(POS_KEY); } catch (e) { return; }
    if (!raw) return;
    try {
      var p = JSON.parse(raw);
      if (p && typeof p.left === "number" && typeof p.top === "number") {
        // defer so offsetWidth/Height are real (deck just un-hidden)
        requestAnimationFrame(function () { applyPos(p.left, p.top); });
      }
    } catch (e) {}
  }

  function makeDraggable() {
    if (!head) return;
    var dragging = false, sx = 0, sy = 0, baseLeft = 0, baseTop = 0;

    head.addEventListener("pointerdown", function (e) {
      // never start a drag from the close button
      if (e.target.closest(".pingdeck__close")) return;
      dragging = true;
      var r = deck.getBoundingClientRect();
      baseLeft = r.left; baseTop = r.top;
      sx = e.clientX; sy = e.clientY;
      head.classList.add("is-grabbing");
      try { head.setPointerCapture(e.pointerId); } catch (err) {}
      e.preventDefault();
    });
    head.addEventListener("pointermove", function (e) {
      if (!dragging) return;
      applyPos(baseLeft + (e.clientX - sx), baseTop + (e.clientY - sy));
    });
    function endDrag(e) {
      if (!dragging) return;
      dragging = false;
      head.classList.remove("is-grabbing");
      try { head.releasePointerCapture(e.pointerId); } catch (err) {}
      var r = deck.getBoundingClientRect();
      savePos({ left: r.left, top: r.top });
    }
    head.addEventListener("pointerup", endDrag);
    head.addEventListener("pointercancel", endDrag);
  }

  function openDeck() {
    if (open) return;
    open = true;
    wire();
    deck.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
    if (banner) banner.setAttribute("aria-expanded", "true");
    dock.classList.add("is-open");
    if (!REDUCED) trigger.style.transform = ""; // settle the idle drift
    setState("standby");
    restorePos();
    // focus the input on the next frame (after the reveal)
    requestAnimationFrame(function () { input && input.focus(); });
  }

  function closeDeck() {
    if (!open) return;
    open = false;
    deck.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    if (banner) banner.setAttribute("aria-expanded", "false");
    dock.classList.remove("is-open");
  }

  function toggle() { open ? closeDeck() : openDeck(); }

  trigger.addEventListener("click", toggle);
  if (banner) banner.addEventListener("click", function () { if (!open) openDeck(); });
})();
