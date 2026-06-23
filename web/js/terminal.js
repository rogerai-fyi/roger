/* =====================================================================
   RogerAI - terminal replay (hand-built, asciinema-style).
   Drives the radio TUI flow on a fixed schedule of "frames":
     search a BAND (the model you want) -> see the STATIONS serving it
     (home GPUs with call-signs) -> `use` it with MARGINS (max $/1M,
     min t/s) -> the radio LOCKS the strongest station and hands you a
     local OpenAI-compatible endpoint + key -> when a station FADES it
     AUTO RE-TUNES to another station still inside your margins, no
     dropped session.

   Truthful to the CLI: `rogerai search`, `rogerai use <model>` + criteria
   (max price, min tps, confidential); failover is across STATIONS serving
   the SAME band, within the operator's margins (client relayWithFailover
   over Criteria = model + max price + min tps + confidential).

   A "band" = the MODEL you want (e.g. qwen3-coder-30b).
   A "station" = a provider / home GPU with a call-sign (@nightowl) that
   serves that band. You tune the BAND + your margins, not one station.

   Lightweight: one timer chain writing pre-escaped HTML lines into a
   <pre>. Respects prefers-reduced-motion (renders the final state, no
   typing), and only autoplays once it scrolls into view. Pacing is
   deliberately gentle so a first-time viewer can read each beat.
   ===================================================================== */
(function () {
  "use strict";

  var screen = document.getElementById("termScreen");
  var replayBtn = document.getElementById("termReplay");
  if (!screen) return;

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ---- pacing knobs (ms). Tuned so a first-timer can follow. ----------
  var TYPE_MS = 78;        // per-character type speed (gentle)
  var READ_AFTER_TYPE = 900;   // pause after a command finishes typing, before "enter"
  var STEP_GAP = 620;      // beat between rendered steps
  var STAGE_HOLD = 1500;   // hold on a finished stage before the next move

  // ---- tiny helpers: colored spans (text is pre-escaped, safe) -------
  function esc(s) { return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
  function span(cls, s) { return '<span class="' + cls + '">' + esc(s) + "</span>"; }
  function dim(s) { return span("t-dim", s); }
  function ok(s) { return span("t-ok", s); }
  function gold(s) { return span("t-gold", s); }
  function money(s) { return span("t-money", s); }
  function volt(s) { return span("t-volt", s); }
  function live(s) { return span("t-live", s); }
  function head(s) { return span("t-head", s); }

  var PROMPT = span("t-prompt", "roger> ");
  var CURSOR = '<span class="t-cursor">&nbsp;</span>';

  // signal bar glyphs
  function bar(level) { // 0..1 -> 7-char bar
    var chars = "▁▂▃▄▅▆▇";
    var n = Math.round(level * 6);
    var s = "";
    for (var i = 0; i < 7; i++) s += i <= n ? chars[Math.min(i + 1, 6)] : "·";
    return s;
  }

  // The BAND we tune to, and your MARGINS.
  var BAND = "qwen3-coder-30b";
  var MAX_PRICE = "0.30";   // $/1M out
  var MIN_TPS = "40";       // t/s

  // STATIONS serving this one band (home GPUs, call-signs). All within
  // margins except where noted; the radio picks the strongest.
  var stations = [
    { sel: false, st: ok("◉"), cs: gold("◆"), who: "@nightowl", loc: "DE",   price: "0.22", sig: 0.86, tps: "58" },
    { sel: false, st: ok("◉"), cs: gold("◆"), who: "@glacier",  loc: "US-W", price: "0.27", sig: 0.71, tps: "47" },
    { sel: false, st: ok("◉"), cs: " ",       who: "@basement", loc: "US-E", price: "0.29", sig: 0.55, tps: "41" },
    { sel: false, st: dim("○"),cs: " ",       who: "@afkrig",   loc: "NL",   price: "0.41", sig: 0.0,  tps: "over" },
  ];

  function pad(s, n) { s = String(s); while (s.length < n) s += " "; return s; }
  function padL(s, n) { s = String(s); while (s.length < n) s = " " + s; return s; }

  function stationRow(s) {
    var caret = s.sel ? volt("▸ ") : "  ";
    var mark = s.st + " " + s.cs + " ";
    var who = head(pad(s.who, 11));
    var loc = dim(pad(s.loc, 5));
    var price = money(padL(s.price, 4) + " $/M");
    var sigbar = s.sig > 0 ? live(bar(s.sig)) : dim("·······");
    var over = (s.tps === "over");
    var tpscol = over ? dim(padL("-", 4) + "    ") : dim(padL(s.tps, 4) + " t/s");
    var tail = over ? dim("  over margin") : "";
    return caret + mark + who + loc + "  " + sigbar + "  " + tpscol + "  " + price + tail;
  }

  // ---- frame timeline -------------------------------------------------
  var timers = [];
  function at(ms, fn) { timers.push(setTimeout(fn, ms)); }
  function reset() { timers.forEach(clearTimeout); timers = []; }

  // ---- buffer ---------------------------------------------------------
  var buffer = [];
  function flush() { screen.innerHTML = buffer.join("\n"); screen.scrollTop = screen.scrollHeight; }
  function line(html) { buffer.push(html); }
  function clear() { buffer = []; }

  // Type `text` into the prompt, char by char, on top of prefixLines.
  // After the text is complete, hold READ_AFTER_TYPE (the readable pause
  // before "enter"), then call doneCb.
  function typeInto(prefixLines, text, doneCb) {
    var i = 0;
    function tick() {
      clear();
      prefixLines.forEach(line);
      line(PROMPT + head(text.slice(0, i)) + CURSOR);
      flush();
      if (i < text.length) { i++; at(TYPE_MS, tick); }
      else {
        // command fully typed: hold so it can be read BEFORE enter fires
        at(READ_AFTER_TYPE, function () {
          // brief "enter": drop the cursor to signal execution
          clear();
          prefixLines.forEach(line);
          line(PROMPT + head(text));
          flush();
          if (doneCb) at(STEP_GAP, doneCb);
        });
      }
    }
    tick();
  }

  // ---- band table render helpers --------------------------------------
  function bandHeadLines(cmd) {
    return [
      PROMPT + head(cmd),
      "",
      dim("  band ") + head(BAND) + dim("   4 stations serving it          sort: signal ▸"),
      dim("  ──────────────────────────────────────────────────────────────────────"),
    ];
  }

  function renderBand(prefixCmd, promptLine) {
    clear();
    bandHeadLines(prefixCmd).forEach(line);
    stations.forEach(function (s) { line(stationRow(s)); });
    line(dim("  ──────────────────────────────────────────────────────────────────────"));
    line(promptLine || dim("   set margins, then `use`   ⏎ tune in   space preview   q quit"));
    flush();
  }

  function endpointPanel(stationWho) {
    line(ok("  ◉ CHANNEL OPEN") + "  " + head(BAND) + " " + dim("via " + stationWho) +
         "   " + gold("◆ verified") + "   " + live("58 t/s"));
    line("");
    line(dim("  ╭───────────── point Hermes / your bots here ─────────────╮"));
    line(dim("  │  ") + dim("BASE URL  ") + money("http://127.0.0.1:8779/v1") + dim("      ") + volt("[copy ⌘C]") + dim("  │"));
    line(dim("  │  ") + dim("API KEY   ") + money("rog-sk-live-9f3c…a71d") + dim("    ") + volt("[copy ⌘K]") + dim("  │"));
    line(dim("  │  ") + dim("MODEL     ") + money(BAND) + dim("                       │"));
    line(dim("  ╰─────────────────────────────────────────────────────────╯"));
    line("");
    line(dim("  drop-in, OpenAI-compatible - point any OpenAI tool here. ") + live("roger that."));
  }

  // ---- static final state (also used under reduced-motion) ------------
  function renderFinal() {
    clear();
    bandHeadLines("rogerai search " + BAND).forEach(line);
    stations.forEach(function (s) { line(stationRow(s)); });
    line(dim("  ──────────────────────────────────────────────────────────────────────"));
    line("");
    endpointPanel("@glacier");
    line("");
    line(dim("  @nightowl faded -> re-tuned to ") + head("@glacier") +
         dim("  (<= " + MAX_PRICE + " $/M, >= " + MIN_TPS + " t/s)"));
    flush();
  }

  // ---- the animated sequence ------------------------------------------
  function play() {
    reset();
    if (REDUCED) { renderFinal(); return; }

    // reset selection each loop
    stations.forEach(function (s) { s.sel = false; });

    // 1) search the BAND
    typeInto([], "rogerai search " + BAND, function () {
      // 2) reveal the stations serving this band, one row at a time
      clear();
      bandHeadLines("rogerai search " + BAND).forEach(line);
      flush();

      var d = 360; // gentle reveal cadence per row
      stations.forEach(function (s, idx) {
        at(d * (idx + 1), function () { line(stationRow(s)); flush(); });
      });
      var afterRows = d * (stations.length + 1);
      at(afterRows, function () {
        line(dim("  ──────────────────────────────────────────────────────────────────────"));
        line(dim("   ◆ = lineage-verified   tune the BAND + your margins, not one station"));
        flush();
      });

      // 3) `use` the band WITH MARGINS (the truthful CLI verb)
      var useCmd = "rogerai use " + BAND + " --max-price " + MAX_PRICE + " --min-tps " + MIN_TPS;
      at(afterRows + STAGE_HOLD, function () {
        typeInto(bandHeadLines("rogerai search " + BAND).concat(
          stations.map(stationRow),
          [dim("  ──────────────────────────────────────────────────────────────────────")]
        ).slice(0), useCmd, function () {

          // 4) the radio LOCKS the strongest station within margins
          clear();
          line(volt("  Tuning band ") + head(BAND) + volt("  within margins ") +
               money(MAX_PRICE + " $/M") + dim(" / ") + money(MIN_TPS + " t/s"));
          line("");
          flush();
          var steps = [
            ok("  ◉") + " scanning stations  " + dim("4 on this band") + "  " + ok("ok"),
            ok("  ◉") + " filtering margins  " + dim("3 within limits") + "  " + ok("ok"),
            ok("  ◉") + " locking strongest  " + head("@nightowl") + dim(" · ") + live("58 t/s") + dim(" · ") + money("0.22 $/M") + "  " + ok("ok"),
            ok("  ◉") + " lineage handshake  " + gold("◆ weights·shard·token") + "  " + ok("ok"),
          ];
          var base = STEP_GAP;
          steps.forEach(function (s, i) { at(base * (i + 1), function () { line(s); flush(); }); });
          var afterLock = base * (steps.length + 1);

          // 5) channel open + endpoint panel (the money moment)
          at(afterLock, function () {
            clear();
            endpointPanel("@nightowl");
            line("");
            line("  " + dim("┌ live ──────────────────────────────────────────────────────┐"));
            line("  " + dim("│ ") + ok("◉ open ") + gold("◆") + dim(" │ ") + head("@nightowl") + dim(" │ ") + live("58 t/s ▆▆▇▆▅▆▇") + dim(" │ bal ") + money("$42.17") + dim(" │"));
            line("  " + dim("└────────────────────────────────────────────────────────────┘"));
            flush();
          });

          // 6) RESILIENCE: @nightowl FADES -> auto re-tune to @glacier
          at(afterLock + STAGE_HOLD + 900, function () {
            clear();
            endpointPanel("@nightowl");
            line("");
            line(volt("  ! station ") + head("@nightowl") + volt(" faded") + dim("  (home GPU offline)") + "  re-tuning…");
            flush();
          });
          at(afterLock + STAGE_HOLD + 2100, function () {
            clear();
            endpointPanel("@nightowl");
            line("");
            line(ok("  ◉") + " re-tuned to " + head("@glacier") + dim("  still <= ") + money(MAX_PRICE + " $/M") + dim(", >= ") + money(MIN_TPS + " t/s") + "  " + ok("no dropped session"));
            flush();
          });
          // 7) settle onto @glacier: same channel, new station
          at(afterLock + STAGE_HOLD + 3400, function () {
            clear();
            endpointPanel("@glacier");
            line("");
            line("  " + dim("┌ live ──────────────────────────────────────────────────────┐"));
            line("  " + dim("│ ") + ok("◉ open ") + gold("◆") + dim(" │ ") + head("@glacier ") + dim(" │ ") + live("47 t/s ▆▅▆▅▄▅▆") + dim(" │ bal ") + money("$42.16") + dim(" │"));
            line("  " + dim("└────────────────────────────────────────────────────────────┘"));
            line("");
            line(dim("  same band, same endpoint - the channel never closed. ") + live("roger that."));
            flush();
          });

          // 8) hold, then loop
          at(afterLock + STAGE_HOLD + 3400 + 6500, function () { play(); });
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
    replayBtn.addEventListener("click", function () {
      started = true; play();
    });
  }

  // first paint so the panel isn't blank before it scrolls in
  renderFinal();
})();
