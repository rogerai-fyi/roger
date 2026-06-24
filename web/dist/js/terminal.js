/* =====================================================================
   RogerAI - terminal replay (hand-built, asciinema-style).
   Two sequences, played in a loop:

   SEQUENCE 1 - TUNE IN
     rogerai search qwen3-coder-30b
       -> band header (N stations, live $/M range)
       -> station rows with INLINE SIGNAL BARS:
          ◉ ◆ @callsign REGION  ▁▂▃▄▅▆▇  NN t/s  0.NN $/M   (+ one ○ over-margin)
     rogerai use qwen3-coder-30b --max-price 0.30 --min-tps 40
       -> ◉ scanning stations … ok
       -> ◉ locking strongest @nightowl … ok
       -> ◉ lineage handshake ◆ weights·shard·token ok
       -> ◉ CHANNEL OPEN … ◆ verified
       -> BASE URL / API KEY / MODEL block
       -> "drop-in, OpenAI-compatible - point any OpenAI tool here. roger that."

   SEQUENCE 2 - GO ON AIR (sharing)
     rogerai share qwen3-coder-30b --rate 0.30
       -> detect / your station -> ◉ ON AIR
       -> a served request + an earnings tick

   Lightweight: one timer chain writing pre-escaped HTML into a <pre>.
   Respects prefers-reduced-motion (renders the final state, no typing),
   only autoplays once it scrolls into view.
   ===================================================================== */
(function () {
  "use strict";

  var screen = document.getElementById("termScreen");
  var replayBtn = document.getElementById("termReplay");
  if (!screen) return;

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ---- pacing knobs (ms) ----------------------------------------------
  var TYPE_MS = 64;
  var READ_AFTER_TYPE = 800;
  var STEP_GAP = 560;
  var STAGE_HOLD = 1400;

  // ---- colored spans (text pre-escaped, safe) -------------------------
  function esc(s) { return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
  function span(cls, s) { return '<span class="' + cls + '">' + esc(s) + "</span>"; }
  function dim(s) { return span("t-dim", s); }
  function ok(s) { return span("t-ok", s); }
  function gold(s) { return span("t-gold", s); }
  function money(s) { return span("t-money", s); }
  function live(s) { return span("t-live", s); }
  function head(s) { return span("t-head", s); }

  var PROMPT = span("t-prompt", "roger> ");
  var CURSOR = '<span class="t-cursor">&nbsp;</span>';

  var BAND = "qwen3-coder-30b";
  var MAX_PRICE = "0.30";
  var MIN_TPS = "40";

  // inline signal-bar glyph for a 0..1 strength (7 cells, "head" highest)
  function sigbar(level) {
    var chars = "▁▂▃▄▅▆▇";
    var n = Math.round(level * 6);
    var s = "";
    for (var i = 0; i < 7; i++) s += i <= n ? chars[Math.min(i, 6)] : "·";
    return s;
  }

  // STATIONS serving this band (home GPUs, call-signs). Last is over-margin.
  var stations = [
    { on: true,  cs: true,  who: "@nightowl", loc: "DE",   sig: 0.92, tps: "58", price: "0.22", over: false },
    { on: true,  cs: true,  who: "@glacier",  loc: "US-W", sig: 0.72, tps: "47", price: "0.27", over: false },
    { on: true,  cs: false, who: "@basement", loc: "US-E", sig: 0.55, tps: "41", price: "0.29", over: false },
    { on: false, cs: false, who: "@afkrig",   loc: "NL",   sig: 0,    tps: "-",  price: "0.41", over: true  }
  ];

  function pad(s, n) { s = String(s); while (s.length < n) s += " "; return s; }
  function padL(s, n) { s = String(s); while (s.length < n) s = " " + s; return s; }

  function stationRow(s) {
    var dot = s.over ? dim("○") : ok("◉");
    var cs = s.cs ? gold("◆") : " ";
    var who = head(pad(s.who, 10));
    var loc = dim(pad(s.loc, 5));
    var bars = s.over ? dim("·······") : live(sigbar(s.sig));
    var tps = s.over ? dim(padL("-", 3) + " t/s") : dim(padL(s.tps, 3) + " t/s");
    var price = money(padL(s.price, 4) + " $/M");
    var tail = s.over ? dim("  over margin") : "";
    return "    " + dot + " " + cs + " " + who + loc + "  " + bars + "  " + tps + "  " + price + tail;
  }

  function bandRange() {
    var prices = [];
    stations.forEach(function (s) { if (!s.over) prices.push(parseFloat(s.price)); });
    var lo = Math.min.apply(null, prices), hi = Math.max.apply(null, prices);
    return lo.toFixed(2) + " ~ " + hi.toFixed(2);
  }
  function nStations() { var n = 0; stations.forEach(function (s) { if (!s.over) n++; }); return n; }

  function bandHeadLines(cmd) {
    var rule = dim("  ──────────────────────────────────────────────────────────────────");
    return [
      PROMPT + head(cmd),
      "",
      dim("  band ") + head(BAND) + dim("   " + nStations() + " stations    ") +
        money(bandRange() + " $/M out") + dim(" (live range)"),
      rule
    ];
  }

  function endpointPanel(stationWho, tps) {
    line(ok("  ◉ CHANNEL OPEN") + "  " + head(BAND) + " " + dim("via " + stationWho) +
         "   " + gold("◆ verified"));
    line("");
    line(dim("  BASE URL  ") + money("http://127.0.0.1:8779/v1"));
    line(dim("  API KEY   ") + money("rog-sk-live-9f3c…a71d"));
    line(dim("  MODEL     ") + money(BAND));
    line("");
    line(dim("  drop-in, OpenAI-compatible - point any OpenAI tool here. ") + live("roger that."));
  }

  // ---- timeline + buffer ----------------------------------------------
  var timers = [];
  function at(ms, fn) { timers.push(setTimeout(fn, ms)); }
  function reset() { timers.forEach(clearTimeout); timers = []; }
  var buffer = [];
  function flush() { screen.innerHTML = buffer.join("\n"); screen.scrollTop = screen.scrollHeight; }
  function line(html) { buffer.push(html); }
  function clear() { buffer = []; }

  function typeInto(prefixLines, text, doneCb) {
    var i = 0;
    function tick() {
      clear();
      prefixLines.forEach(line);
      line(PROMPT + head(text.slice(0, i)) + CURSOR);
      flush();
      if (i < text.length) { i++; at(TYPE_MS, tick); }
      else {
        at(READ_AFTER_TYPE, function () {
          clear(); prefixLines.forEach(line);
          line(PROMPT + head(text)); flush();
          if (doneCb) at(STEP_GAP, doneCb);
        });
      }
    }
    tick();
  }

  // ---- static final state (also used under reduced-motion) ------------
  function renderFinal() {
    clear();
    bandHeadLines("rogerai search " + BAND).forEach(line);
    stations.forEach(function (s) { line(stationRow(s)); });
    line(dim("  ──────────────────────────────────────────────────────────────────"));
    line("");
    endpointPanel("@nightowl", "58");
    line("");
    line(PROMPT + head("rogerai share " + BAND + " --rate " + MAX_PRICE));
    line("");
    line(ok("  ◉") + " detected backend  " + dim("vLLM · 127.0.0.1:8000") + "  " + ok("ok"));
    line(ok("  ◉ ON AIR") + "  " + head("@you ") + gold("◆") + dim(" serving ") + head(BAND) +
         dim(" at ") + money(MAX_PRICE + " $/M"));
    line(dim("  served 1 request · +") + money("$0.0001") + dim(" · balance ") + money("$42.18"));
    flush();
  }

  // ====================== SEQUENCE 2: GO ON AIR ========================
  function playShare(loopAfter) {
    var shareCmd = "rogerai share " + BAND + " --rate " + MAX_PRICE;
    typeInto([], shareCmd, function () {
      clear();
      line(PROMPT + head(shareCmd));
      line("");
      flush();
      var steps = [
        ok("  ◉") + " detecting backend  " + dim("scanning local ports") + "  " + ok("ok"),
        ok("  ◉") + " backend locked     " + head("vLLM") + dim(" · 127.0.0.1:8000 · ") + head(BAND) + "  " + ok("ok"),
        ok("  ◉") + " call-sign assigned " + head("@you") + " " + gold("◆") + dim(" · rate ") + money(MAX_PRICE + " $/M out") + "  " + ok("ok"),
        ok("  ◉") + " lineage co-sign    " + gold("◆ weights·shard·token") + "  " + ok("ok")
      ];
      var base = STEP_GAP;
      steps.forEach(function (s, i) { at(base * (i + 1), function () { line(s); flush(); }); });
      var afterSteps = base * (steps.length + 1);

      // your station goes on air
      at(afterSteps, function () {
        line("");
        line(ok("  ◉ ON AIR") + "  " + head("@you ") + gold("◆") + dim(" · ") + head(BAND) +
             dim(" · ") + live("station live") + dim(" · appears in the band in ~10s"));
        flush();
      });

      // a request arrives + earnings tick
      var earn = 42.18;
      at(afterSteps + STAGE_HOLD, function () {
        line("");
        line(dim("  ┌ live ──────────────────────────────────────────────────────┐"));
        line(dim("  │ ") + ok("◉ on air ") + gold("◆") + dim(" │ ") + head("@you    ") +
             dim(" │ ") + live("incoming request from @ssh-bot…") + dim("            │"));
        line(dim("  └────────────────────────────────────────────────────────────┘"));
        flush();
      });
      at(afterSteps + STAGE_HOLD + 1200, function () {
        line("");
        line(ok("  ◉") + " served " + head("742 tok out") + dim(" @ ") + money(MAX_PRICE + " $/M") +
             dim("  ·  earned ") + money("+$0.000223") + dim("  (70% keep)"));
        line(dim("  balance ") + money("$" + (earn + 0.0002).toFixed(4)) + dim("  ·  your GPU is paying rent. ") + live("roger that."));
        flush();
      });

      // hold, then loop back to sequence 1
      at(afterSteps + STAGE_HOLD + 1200 + 6500, function () { if (loopAfter) play(); });
    });
  }

  // ====================== SEQUENCE 1: TUNE IN ==========================
  function play() {
    reset();
    if (REDUCED) { renderFinal(); return; }

    typeInto([], "rogerai search " + BAND, function () {
      clear();
      bandHeadLines("rogerai search " + BAND).forEach(line);
      flush();

      var d = 320;
      stations.forEach(function (s, idx) {
        at(d * (idx + 1), function () { line(stationRow(s)); flush(); });
      });
      var afterRows = d * (stations.length + 1);
      at(afterRows, function () {
        line(dim("  ──────────────────────────────────────────────────────────────────"));
        line(dim("   ◆ = lineage-verified   tune the BAND + your margins, not one station"));
        flush();
      });

      var useCmd = "rogerai use " + BAND + " --max-price " + MAX_PRICE + " --min-tps " + MIN_TPS;
      at(afterRows + STAGE_HOLD, function () {
        var prefix = bandHeadLines("rogerai search " + BAND).concat(
          stations.map(stationRow),
          [dim("  ──────────────────────────────────────────────────────────────────")]
        );
        typeInto(prefix, useCmd, function () {
          clear();
          line(PROMPT + head(useCmd));
          line("");
          flush();
          var steps = [
            ok("  ◉") + " scanning stations  " + dim(nStations() + " on this band") + "  " + ok("ok"),
            ok("  ◉") + " locking strongest  " + head("@nightowl") + dim(" · ") + live("58 t/s") + dim(" · ") + money("0.22 $/M") + "  " + ok("ok"),
            ok("  ◉") + " lineage handshake  " + gold("◆ weights·shard·token") + "  " + ok("ok")
          ];
          var base = STEP_GAP;
          steps.forEach(function (s, i) { at(base * (i + 1), function () { line(s); flush(); }); });
          var afterLock = base * (steps.length + 1);

          at(afterLock, function () {
            line("");
            endpointPanel("@nightowl", "58");
            flush();
          });

          // hand off to sequence 2 (share), then loop
          at(afterLock + STAGE_HOLD + 3200, function () { playShare(true); });
        });
      });
    });
  }

  // ---- autoplay when scrolled into view; replay button ----------------
  var started = false;
  function kick() { if (started) return; started = true; play(); }

  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      if (entries[0].isIntersecting) { kick(); io.disconnect(); }
    }, { threshold: 0.3 });
    io.observe(screen);
  } else { kick(); }

  if (replayBtn) {
    replayBtn.addEventListener("click", function () { started = true; play(); });
  }

  // first paint so the panel isn't blank before it scrolls in
  renderFinal();
})();
