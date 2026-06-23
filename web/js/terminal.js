/* =====================================================================
   RogerAI - terminal replay (hand-built, asciinema-style).
   Drives the radio TUI flow on a fixed schedule of "frames":
     run `rogerai` -> browse stations (signal bars) -> tune in ->
     receive the local OpenAI-compatible endpoint + key.
   Lightweight: one timer chain writing pre-escaped HTML lines into a
   <pre>. Respects prefers-reduced-motion (renders the final state, no
   typing), and only autoplays once it scrolls into view.
   ===================================================================== */
(function () {
  "use strict";

  var screen = document.getElementById("termScreen");
  var replayBtn = document.getElementById("termReplay");
  if (!screen) return;

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

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

  // ---- the script: an array of "steps" --------------------------------
  // Each step pushes lines and/or types into the prompt, then waits.
  // We rebuild the whole buffer each frame so it stays simple + jitter-free.
  var buffer = [];
  function flush() { screen.innerHTML = buffer.join("\n"); screen.scrollTop = screen.scrollHeight; }
  function line(html) { buffer.push(html); }
  function clear() { buffer = []; }

  // station table data
  var stations = [
    { sel: false, st: ok("◉"), cs: gold("◆"), model: "qwen3-coder-30b", who: "@nightowl",  loc: "DE",   price: "0.22", sig: 0.78, tps: "58" },
    { sel: false, st: ok("◉"), cs: gold("◆"), model: "qwen3-72b",       who: "@forge",     loc: "US-W", price: "0.38", sig: 0.92, tps: "71" },
    { sel: true,  st: ok("◉"), cs: " ",       model: "llama3.3-70b",     who: "@basement",  loc: "US-E", price: "0.31", sig: 0.62, tps: "44" },
    { sel: false, st: dim("○"), cs: " ",      model: "mistral-large",    who: "@afkmachine",loc: "NL",   price: "0.49", sig: 0.0,  tps: "idle" },
  ];

  function pad(s, n) { s = String(s); while (s.length < n) s += " "; return s; }
  function padL(s, n) { s = String(s); while (s.length < n) s = " " + s; return s; }

  function stationRow(s) {
    var caret = s.sel ? volt("▸ ") : "  ";
    var mark = s.st + " " + s.cs + " ";
    var name = pad(s.model, 17);
    var who = dim(pad(s.who + " · " + s.loc, 16));
    var price = money(padL(s.price, 4) + " $/M");
    var sigbar = s.sig > 0 ? live(bar(s.sig)) : dim("·······");
    var tps = dim(padL(s.tps, 4)) + (s.tps === "idle" ? dim("    ") : dim(" t/s"));
    return caret + mark + head(name) + who + "  " + sigbar + "  " + tps + "  " + price;
  }

  // ---- frame timeline -------------------------------------------------
  var timers = [];
  function at(ms, fn) { timers.push(setTimeout(fn, ms)); }
  function reset() { timers.forEach(clearTimeout); timers = []; }

  function typeInto(prefixLines, text, doneCb, speed) {
    speed = speed || 55;
    var i = 0;
    function tick() {
      clear();
      prefixLines.forEach(line);
      line(PROMPT + head(text.slice(0, i)) + (i < text.length ? CURSOR : ""));
      flush();
      if (i < text.length) { i++; at(speed, tick); }
      else if (doneCb) at(360, doneCb);
    }
    tick();
  }

  function renderFinal() {
    clear();
    line(span("t-prompt", "roger> ") + head("rogerai"));
    line("");
    line(dim("  the band - 122 stations on air                            sort: tok/s ▸"));
    line(dim("  ──────────────────────────────────────────────────────────────────────"));
    stations.forEach(function (s) { line(stationRow(s)); });
    line(dim("  ──────────────────────────────────────────────────────────────────────"));
    line("");
    line(ok("  ◉ CHANNEL OPEN") + "  " + head("llama3.3-70b") + " " + dim("@basement") + "   " + live("44 t/s"));
    line("");
    line("  " + dim("BASE URL   ") + money("http://127.0.0.1:8779/v1"));
    line("  " + dim("API KEY    ") + money("rog-sk-live-9f3c…a71d"));
    line("  " + dim("MODEL      ") + money("llama3.3-70b"));
    line("");
    line(dim("  drop-in, OpenAI-compatible - point any OpenAI tool here. roger that."));
    flush();
  }

  // The animated sequence (returns when scheduled).
  function play() {
    reset();
    if (REDUCED) { renderFinal(); return; }

    // 1) type `rogerai`
    typeInto([], "rogerai", function () {
      // 2) reveal the band, one station row at a time
      var prefix = [
        span("t-prompt", "roger> ") + head("rogerai"),
        "",
        dim("  the band - 122 stations on air                            sort: tok/s ▸"),
        dim("  ──────────────────────────────────────────────────────────────────────"),
      ];
      clear(); prefix.forEach(line); flush();
      var d = 220;
      stations.forEach(function (s, idx) {
        at(d * (idx + 1), function () { line(stationRow(s)); flush(); });
      });
      at(d * (stations.length + 1), function () {
        line(dim("  ──────────────────────────────────────────────────────────────────────"));
        line(dim("   ↑↓ move   ⏎ tune in   space preview   / refine   q quit"));
        flush();
      });

      // 3) move selection down to the verified top station, then "tune in"
      at(d * stations.length + 1100, function () {
        stations[2].sel = false; stations[0].sel = true; // hop to @nightowl ◆
        rebuildBand();
      });
      at(d * stations.length + 2000, function () {
        rebuildBand(span("t-prompt", "roger> ") + head("connect @nightowl") + CURSOR);
      });

      // 4) connecting handshake
      at(d * stations.length + 2700, function () {
        clear();
        line(volt("  Opening a channel to ") + head("@nightowl · qwen3-coder-30b"));
        line("");
        flush();
        var steps = [
          ok("  ◉") + " reaching station " + dim("……………………") + "  " + ok("ok"),
          ok("  ◉") + " negotiating price " + money("0.22 $/M") + dim(" …") + "  " + ok("ok"),
          ok("  ◉") + " lineage handshake " + gold("◆ weights·shard·token") + "  " + ok("ok"),
          ok("  ◉") + " warming endpoint  " + volt("▰▰▰▰▰▰▰▰▱▱") + "  " + head("84%"),
        ];
        steps.forEach(function (s, i) { at(420 * (i + 1), function () { line(s); flush(); }); });
      });

      // 5) the endpoint + key panel (the money moment)
      at(d * stations.length + 5400, function () {
        clear();
        line(ok("  ◉ CHANNEL OPEN") + "  " + head("qwen3-coder-30b") + " " + dim("@nightowl") + "   " + gold("◆ verified") + "   " + live("58 t/s"));
        line("");
        line(dim("  ╭──────────── point your bots here ─────────────╮"));
        line(dim("  │  ") + dim("BASE URL ") + money("http://127.0.0.1:8779/v1") + dim("           │"));
        line(dim("  │  ") + dim("API KEY  ") + money("rog-sk-live-9f3c…a71d") + dim("              │"));
        line(dim("  │  ") + dim("MODEL    ") + money("qwen3-coder-30b") + dim("                    │"));
        line(dim("  ╰─────────────────────────────────────────────────────────╯"));
        line("");
        line(dim("  drop-in, OpenAI-compatible. ") + live("roger that.") + dim("  the endpoint stays live."));
        line("");
        line("  " + dim("┌ live ──────────────────────────────────────────────────────┐"));
        line("  " + dim("│ ") + ok("◉ open ") + gold("◆") + dim(" │ ") + live("58 t/s ▆▆▇▆▅▆▇") + dim(" │ session ") + money("$0.004") + dim(" │ bal ") + money("$42.17") + dim(" │"));
        line("  " + dim("└────────────────────────────────────────────────────────────┘"));
        flush();
      });

      // 6) hold, then loop
      at(d * stations.length + 11000, function () { play(); });
    });
  }

  function rebuildBand(promptLine) {
    clear();
    line(span("t-prompt", "roger> ") + head("rogerai"));
    line("");
    line(dim("  the band - 122 stations on air                            sort: tok/s ▸"));
    line(dim("  ──────────────────────────────────────────────────────────────────────"));
    stations.forEach(function (s) { line(stationRow(s)); });
    line(dim("  ──────────────────────────────────────────────────────────────────────"));
    line(promptLine || dim("   ↑↓ move   ⏎ tune in   space preview   / refine   q quit"));
    flush();
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
      // restore selection state and replay
      stations.forEach(function (s, i) { s.sel = (i === 2); });
      started = true; play();
    });
  }

  // first paint so the panel isn't blank before it scrolls in
  renderFinal();
})();
