/* =====================================================================
   RogerAI - the demo console: a tape-deck / station-preset player.

   Four switchable demos, each an animated terminal replay, selected from a
   radio-preset bar ( [ roger ] [ tune in ] [ agent ] [ share ] ) with the
   current preset lit red. The four tapes mirror the real TUI preset bank you
   get from a bare `roger` ( [0] AGENT  [1] TUNE IN  [2] SHARE ). Transport
   controls (play / pause / replay) and a tuning-bar progress readout, all
   radio/tape-deck styled.

     roger    - boot the dial: type `roger`, acquire the carrier (an animated
                sweep), reveal the preset bank + brand lockup, then read the
                live band (stations fade in, signal bars fill).
     tune in  - the [1] TUNE IN consumer walk: lock the strongest station ->
                CHANNEL OPEN + the drop-in endpoint plate (BASE URL / API KEY /
                MODEL), then one chat turn with a co-signed receipt.
     agent    - the [0] AGENT harness (dj.md persona): one tool turn over the
                open channel - prompt -> routing -> tool call box -> result ->
                answer + co-signed receipt.
     share    - the [2] SHARE provider walk: scan local backends, the detected-
                models table, go (( ON AIR )), an incoming request + earnings tick.

   Engine: each demo compiles to a flat list of "frames" (a screen of lines +
   a hold duration); typing a command expands to one frame per character. The
   player walks the list on a single timer chain; a shared rAF advances ONLY
   the tuning-bar fill (transform only). Auto-plays the first demo, gently
   loops, pauses on hover + offscreen + tab-hidden. prefers-reduced-motion
   renders the static FINAL frame of the selected demo (no typing, no loop,
   no rAF).

   Lightweight: pre-escaped HTML written into a <pre>; no deps.
   ===================================================================== */
(function () {
  "use strict";

  var screen = document.getElementById("termScreen");
  var root = document.getElementById("term");
  if (!screen || !root) return;

  var presetBar = document.getElementById("termPresets");
  var btnPlay = document.getElementById("termPlay");
  var btnReplay = document.getElementById("termReplay");
  var barFill = document.getElementById("termBarFill");
  var titleEl = document.getElementById("termTitle");

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* ---- colored spans (text pre-escaped, safe) ----------------------- */
  function esc(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
  function span(cls, s) { return '<span class="' + cls + '">' + esc(s) + "</span>"; }
  function dim(s) { return span("t-dim", s); }
  function ok(s) { return span("t-ok", s); }
  function gold(s) { return span("t-gold", s); }
  function money(s) { return span("t-money", s); }
  function live(s) { return span("t-live", s); }
  function head(s) { return span("t-head", s); }

  var PROMPT = span("t-prompt", "roger> ");
  var CURSOR = '<span class="t-cursor">&nbsp;</span>';
  var RULE = dim("  ──────────────────────────────────────────────────────────────────");

  var BAND = "qwen3-coder-30b";
  var PORT = "8779";

  // Brand lockup + preset bank, matching the real TUI header (`▟█▙ R O G E R · A I`)
  // and the always-visible preset row ( [0] AGENT [1] TUNE IN [2] SHARE [3] CONFIG
  // [L] LOGIN [?] HELP ). `lit` names which preset reads as pressed (red glint).
  var BRAND = head("▟█▙") + head(" R O G E R") + dim(" · A I");
  function presetLine(lit) {
    var b = [["0", "AGENT"], ["1", "TUNE IN"], ["2", "SHARE"], ["3", "CONFIG"], ["L", "LOGIN"], ["?", "HELP"]];
    var out = "  ";
    for (var i = 0; i < b.length; i++) {
      var cell = "[" + b[i][0] + "] " + b[i][1];
      out += (b[i][1] === lit ? span("t-sel", " •" + cell + " ") : dim(" " + cell + " "));
    }
    return out;
  }
  // the resting "dial" deck: brand + carrier line + preset bank + legend.
  function deck(lit) {
    return [
      PROMPT + head("roger"), "",
      "  " + BRAND + "   " + ok("◉ carrier acquired") + dim("   broker.rogerai.fyi"), "",
      presetLine(lit || "TUNE IN"),
      dim("      dial a band · ◉ on  ○ off  ◆ lineage-verified · ") + dim("[m] compact")
    ];
  }
  // an 8-cell carrier-acquire sweep bar (frame-driven, monochrome): the lit cell
  // rides left->right while the acquired cells behind it fill solid.
  function carrier(p) {
    var s = "";
    for (var i = 0; i < 8; i++) s += i < p ? live("▰") : (i === p ? live("▱") : dim("▱"));
    return s;
  }

  function pad(s, n) { s = String(s); while (s.length < n) s += " "; return s; }
  function padL(s, n) { s = String(s); while (s.length < n) s = " " + s; return s; }

  // inline signal-bar glyph for a 0..1 strength (7 cells, "head" highest)
  function sigbar(level) {
    var chars = "▁▂▃▄▅▆▇";
    var n = Math.round(level * 6), s = "";
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

  function stationRow(s) {
    var dot = s.over ? dim("○") : ok("◉");
    var cs = s.cs ? gold("◆") : " ";
    var who = head(pad(s.who, 10));
    var loc = dim(pad(s.loc, 5));
    var bars = s.over ? dim("·······") : live(sigbar(s.sig));
    var tps = dim(padL(s.over ? "-" : s.tps, 3) + " t/s");
    var price = money(padL(s.price, 4) + " $/M");
    var tail = s.over ? dim("  over margin") : "";
    return "    " + dot + " " + cs + " " + who + loc + "  " + bars + "  " + tps + "  " + price + tail;
  }
  function bandRange() {
    var p = [];
    stations.forEach(function (s) { if (!s.over) p.push(parseFloat(s.price)); });
    return Math.min.apply(null, p).toFixed(2) + " ~ " + Math.max.apply(null, p).toFixed(2);
  }
  function nStations() { var n = 0; stations.forEach(function (s) { if (!s.over) n++; }); return n; }

  function endpointPlate(stationWho) {
    return [
      ok("  ◉ CHANNEL OPEN") + "  " + head(BAND) + " " + dim("via " + stationWho) + "   " + gold("◆ verified"),
      "",
      dim("  BASE URL  ") + money("http://127.0.0.1:" + PORT + "/v1"),
      dim("  API KEY   ") + money("roger-local"),
      dim("  MODEL     ") + money(BAND),
      "",
      dim("  drop-in, OpenAI-compatible - point any OpenAI tool here. ") + live("roger that.")
    ];
  }

  /* =====================================================================
     Compile a demo to frames. Each demo builder uses:
       c.show(lines, hold)            - print a screen, hold `hold` ms
       c.type(prefixLines, cmd, hold) - type a command char-by-char, then settle
     ===================================================================== */
  var TYPE_MS = 60, AFTER_TYPE = 700, STEP = 600, STAGE = 1500, END_HOLD = 2800;

  function compile(builder) {
    var frames = [];
    var c = {
      show: function (lines, hold) { frames.push({ lines: lines.slice(), hold: hold == null ? STEP : hold }); },
      type: function (prefixLines, cmd, settleHold) {
        var prefix = prefixLines || [];
        for (var i = 1; i <= cmd.length; i++) {
          frames.push({ lines: prefix.concat([PROMPT + head(cmd.slice(0, i)) + CURSOR]), hold: TYPE_MS });
        }
        frames.push({ lines: prefix.concat([PROMPT + head(cmd)]), hold: settleHold == null ? AFTER_TYPE : settleHold });
      }
    };
    builder(c);
    return frames;
  }

  // bandHeadTUI is the TUI band-browse header (no typed command): the brand
  // lockup + the lit [1] TUNE IN preset + the live band range, then a rule. The
  // stations render under it via stationRow.
  function bandHeadTUI() {
    return [
      "  " + BRAND + "   " + span("t-sel", " •[1] TUNE IN ") + dim("   reading the band"), "",
      dim("  band ") + head(BAND) + dim("   " + nStations() + " stations   ") +
        money(bandRange() + " $/M out") + dim(" (live range)"),
      RULE
    ];
  }

  /* ---- the four demos (mirror the TUI preset bank) ------------------- */
  var DEMOS = {
    // `roger` - boot the dial: type the command, acquire the carrier (animated
    // sweep), reveal the preset bank, then read the live band.
    roger: {
      label: "roger", title: "roger — the dial",
      build: function () {
        return compile(function (c) {
          c.type([], "roger", AFTER_TYPE);
          // carrier-acquire: an 8-cell sweep fills left->right under the brand.
          for (var p = 0; p <= 8; p++) {
            c.show([
              PROMPT + head("roger"), "",
              "  " + BRAND, "",
              dim("  acquiring carrier  ") + carrier(p) +
                (p >= 8 ? "  " + ok("◉ locked") : dim("  broker.rogerai.fyi"))
            ], 130);
          }
          // the resting dial: brand + carrier line + preset bank + legend.
          c.show(deck("TUNE IN"), STAGE);
          // read the live band: stations fade in one by one, signal bars filling.
          for (var i = 1; i <= stations.length; i++) {
            c.show(bandHeadTUI().concat(stations.slice(0, i).map(stationRow)),
              i < stations.length ? STEP : STAGE);
          }
          c.show(bandHeadTUI().concat(stations.map(stationRow), [RULE,
            dim("   ◆ = lineage-verified · tune a BAND + your margins, not one station")
          ]), END_HOLD);
        });
      }
    },

    // `tune in` - the [1] TUNE IN consumer walk: lock the strongest station ->
    // CHANNEL OPEN + the drop-in endpoint plate, then one chat turn + receipt.
    tunein: {
      label: "tune in", title: "roger — tune in",
      build: function () {
        return compile(function (c) {
          var band = bandHeadTUI().concat(stations.map(stationRow));
          c.show(band, STAGE);
          c.show(band.concat([RULE,
            dim("  ▸ ") + span("t-sel", " enter ") + dim("  tuning in to ") + head("@nightowl") + dim("…")
          ]), STEP);
          // staged lock: scan -> lock -> lineage handshake (mirrors `roger use`).
          var base = [
            "  " + BRAND + "   " + span("t-sel", " •[1] TUNE IN ") + dim("   opening a channel"), RULE
          ];
          var steps = [
            ok("  ◉") + " scanning stations  " + dim(nStations() + " on this band") + "   " + ok("ok"),
            ok("  ◉") + " locking strongest  " + head("@nightowl") + dim(" · ") + live("58 t/s") + dim(" · ") + money("0.22 $/M") + "   " + ok("ok"),
            ok("  ◉") + " lineage handshake  " + gold("◆ weights·shard·token") + "   " + ok("ok")
          ];
          for (var i = 0; i < steps.length; i++) c.show(base.concat(steps.slice(0, i + 1)), STEP);
          // CHANNEL OPEN + the BASE URL / API KEY / MODEL plate.
          c.show(base.concat(steps, [""], endpointPlate("@nightowl")), END_HOLD);
          // one chat turn over the open channel.
          var chatHead = [
            ok("  ◉ CHANNEL OPEN") + "  " + head(BAND) + " " + dim("via @nightowl") + "   " + gold("◆ verified"),
            RULE
          ];
          c.type(chatHead, "summarize this repo in one line", AFTER_TYPE);
          c.show(chatHead.concat([
            PROMPT + head("summarize this repo in one line"), "",
            dim("  ((•)) ") + live("◉ receiving") + dim("  @nightowl · 58 t/s") + CURSOR
          ]), STEP);
          c.show(chatHead.concat([
            PROMPT + head("summarize this repo in one line"), "",
            dim("  ((•)) ") + ok("◉") + dim("  @nightowl"), "",
            "  A peer-to-peer marketplace + CLI to rent home-GPU LLMs by the token.", "",
            dim("  ◆ receipt co-signed · ") + money("47 tok · $0.000014") + dim(" · 70% to @nightowl")
          ]), STAGE);
          // a glimpse of compact `m` (windowshade) mode.
          c.show([
            dim("  press ") + span("t-sel", " m ") + dim("  · windowshade -> compact"), "",
            ok("◉") + " " + head(BAND) + dim("  @nightowl ") + live("▆▆▆▆▆▅·") + dim("  58 t/s  ") + money("0.22 $/M") + dim("  ◆"),
            dim("  ▸ on channel · 3 stations on band · calm mode")
          ], END_HOLD);
        });
      }
    },

    // `agent` - the [0] AGENT harness (dj.md persona): one tool turn over the
    // open channel - prompt -> routing -> tool call -> result -> answer + receipt.
    agent: {
      label: "agent", title: "roger — agent",
      build: function () {
        return compile(function (c) {
          var agentHead = [
            "  " + BRAND + "   " + span("t-sel", " •[0] AGENT ") + dim("  harness ") + head("dj.md") +
              dim(" · band ") + head(BAND) + "   " + gold("◆ verified"),
            RULE
          ];
          c.show(agentHead.concat([
            dim("  the embedded agent runs tools over your open channel."), "",
            dim("  ▸ ask it to do something - it routes the turn to the band.")
          ]), STAGE);
          c.type(agentHead, "/agent how many Go files in cmd/ ?", AFTER_TYPE);
          c.show(agentHead.concat([
            PROMPT + head("/agent how many Go files in cmd/ ?"), "",
            dim("  ((•)) ") + live("◉ thinking") + dim("  routing to band…") + CURSOR
          ]), STEP);
          c.show(agentHead.concat([
            PROMPT + head("/agent how many Go files in cmd/ ?"), "",
            dim("  ((•)) ") + ok("◉") + dim("  tool call"), "",
            dim("  ┌ ") + gold("◆ tool") + dim(" ─ run ─────────────────────────────────────────┐"),
            dim("  │ ") + head("$ ") + "find cmd -name '*.go' | wc -l" + dim("                          │"),
            dim("  └────────────────────────────────────────────────────────────┘")
          ]), STAGE);
          c.show(agentHead.concat([
            PROMPT + head("/agent how many Go files in cmd/ ?"), "",
            dim("  ((•)) ") + ok("◉") + dim("  tool result"), "",
            dim("  ┌ ") + gold("◆ tool") + dim(" ─ run ─────────────────────────────────────────┐"),
            dim("  │ ") + head("$ ") + "find cmd -name '*.go' | wc -l" + dim("                          │"),
            dim("  │ ") + ok("9") + dim("                                                            │"),
            dim("  └────────────────────────────────────────────────────────────┘")
          ]), STEP);
          c.show(agentHead.concat([
            PROMPT + head("/agent how many Go files in cmd/ ?"), "",
            dim("  ((•)) ") + ok("◉") + dim("  @nightowl"), "",
            "  9 Go files under cmd/ - one per binary (broker, cli, sidecar).", "",
            dim("  ◆ receipt co-signed · ") + money("1 tool · 88 tok · $0.000026") + dim(" · ") + live("roger that.")
          ]), END_HOLD);
        });
      }
    },

    // `share` - the [2] SHARE provider walk: scan local backends, the detected-
    // models table, go ON AIR, an incoming request + earnings tick.
    share: {
      label: "share", title: "roger — share",
      build: function () {
        return compile(function (c) {
          // from the dial, press [2] SHARE to become a provider.
          c.show(deck("SHARE"), STAGE);
          c.show(deck("SHARE").concat(["",
            dim("  ▸ ") + span("t-sel", " •[2] SHARE ") + dim("  ·  put your own GPU on the air")
          ]), STEP);
          // detection scan: probe the real local backends one at a time.
          // ◉ = backend up + models found, ○ = port answered but empty.
          var scanHead = [
            "  " + BRAND + "   " + span("t-sel", " •[2] SHARE ") + dim("  go on air") + "   " + gold("◆ provider"),
            RULE,
            dim("  scanning the band for local models…"), ""
          ];
          var probes = [
            ok("  ◉") + " ollama      " + dim(":11434") + dim("  ……  ") + ok("2 models"),
            ok("  ◉") + " llama.cpp   " + dim(":8080") + dim("   ……  ") + ok("1 model"),
            dim("  ○") + " lm-studio   " + dim(":1234") + dim("   ……  ") + dim("none"),
            ok("  ◉") + " vLLM        " + dim(":8000") + dim("   ……  ") + ok("1 model")
          ];
          var names = ["ollama      :11434", "llama.cpp   :8080 ", "lm-studio   :1234 ", "vLLM        :8000 "];
          c.show(scanHead.concat([dim("  ○") + " " + dim(names[0]) + dim("  ……  ") + live("probing…") + CURSOR]), STEP);
          for (var pi = 0; pi < probes.length; pi++) {
            var rows = probes.slice(0, pi + 1);
            if (pi + 1 < probes.length) {
              rows = rows.concat([dim("  ○") + " " + dim(names[pi + 1]) + dim("  ……  ") + live("probing…") + CURSOR]);
            }
            c.show(scanHead.concat(rows), STEP);
          }
          c.show(scanHead.concat(probes, ["",
            ok("  ◉") + dim("  3 backends up · ") + head("4 models") + dim(" detected across the box")
          ]), STAGE);
          // the detected-models table; pick qwen3-coder-30b, go ON AIR.
          var shareHead = [
            "  " + BRAND + "   " + span("t-sel", " •[2] SHARE ") + dim("  your models, detected") + "   " + gold("◆ provider"),
            RULE,
            "  " + dim(pad("MODEL", 20)) + dim(pad("BACKEND", 12)) + dim(pad("STATUS", 11)) + dim("YOUR RATE")
          ];
          var locals = [
            { model: "gpt-oss-20b",      back: "ollama",    rate: "0.18" },
            { model: "qwen3-coder-30b",  back: "vLLM",      rate: "0.30" },
            { model: "llama-3.3-70b",    back: "llama.cpp", rate: "0.55" }
          ];
          function localRow(m, on) {
            return "  " + (on ? ok("◉") : dim("○")) + " " + head(pad(m.model, 18)) +
              dim(pad(m.back, 12)) + (on ? live(pad("ON AIR", 11)) : dim(pad("OFF-AIR", 11))) + money(m.rate + " $/M");
          }
          for (var li = 0; li < locals.length; li++) {
            c.show(shareHead.concat(locals.slice(0, li + 1).map(function (m) { return localRow(m, false); })), STEP);
          }
          c.show(shareHead.concat(locals.map(function (m, i) { return localRow(m, i === 1); }), [RULE,
            dim("  ▸ ") + head("qwen3-coder-30b") + dim(" going on air at ") + money("0.30 $/M out") + dim("…")
          ]), STAGE);
          // ON AIR: the single go-live line (mirrors `roger share`'s onAirLine).
          var onair = [
            "  " + BRAND + "   " + span("t-sel", " •[2] SHARE ") + dim("  on air") + "   " + gold("◆ provider"), RULE,
            ok("  ◉ ON AIR") + "  " + head("@you ") + gold("◆") + dim(" · ") + head(BAND) +
              dim(" · ") + money("earning $0.30/1M") + dim(" · view at rogerai.fyi")
          ];
          c.show(onair, STAGE);
          var live1 = onair.concat(["",
            dim("  ┌ live ──────────────────────────────────────────────────────┐"),
            dim("  │ ") + ok("◉ on air ") + gold("◆") + dim(" │ ") + head("@you    ") +
              dim(" │ ") + live("incoming request from @ssh-bot…") + dim("            │"),
            dim("  └────────────────────────────────────────────────────────────┘")
          ]);
          c.show(live1, STAGE);
          c.show(live1.concat(["",
            ok("  ◉") + " served " + head("742 tok out") + dim(" @ ") + money("0.30 $/M") +
              dim("  ·  earned ") + money("+$0.000223") + dim("  (70% keep)"),
            dim("  balance ") + money("$42.18") + dim("  ·  your GPU is paying rent. ") + live("roger that.")
          ]), END_HOLD);
        });
      }
    }
  };

  /* ---- engine -------------------------------------------------------- */
  // playlist order: when a demo finishes we auto-advance to the next preset
  // and play it (roger -> tune in -> agent -> share -> back to roger).
  var ORDER = ["roger", "tunein", "agent", "share"];
  var NEXT_HOLD = 1500;         // ms to show the "NEXT:" indicator before switching
  function nextOf(name) {
    var i = ORDER.indexOf(name);
    return ORDER[(i + 1) % ORDER.length];
  }

  var current = "roger";
  var frames = [];
  var idx = 0;
  var playing = false;
  var hovered = false, visible = false;
  var timer = null;
  var elapsed = 0, total = 0;   // ms, for the tuning bar
  var frameStart = 0;

  function now() { return (window.performance && performance.now) ? performance.now() : Date.now(); }
  function flush(lines) { screen.innerHTML = lines.join("\n"); screen.scrollTop = screen.scrollHeight; }

  function buildFrames(name) {
    frames = DEMOS[name].build();
    total = frames.reduce(function (a, f) { return a + f.hold; }, 0);
  }
  function clearTimer() { if (timer) { clearTimeout(timer); timer = null; } }

  function setBar(t) {
    if (barFill) barFill.style.transform = "scaleX(" + Math.max(0, Math.min(1, t)) + ")";
  }

  function showFrame(i) {
    idx = i;
    flush(frames[i].lines);
    frameStart = now();
  }
  function renderFinal() {
    if (!frames.length) buildFrames(current);
    flush(frames[frames.length - 1].lines);
    idx = frames.length - 1; elapsed = total; setBar(1);
  }

  function advance() {
    if (!playing) return;
    if (idx >= frames.length - 1) {
      // end of demo: show a brief "NEXT:" indicator, then auto-advance to the
      // next preset and play it (a continuous playlist). Pausing halts this.
      elapsed = total; setBar(1);
      var nxt = nextOf(current);
      flush(frames[frames.length - 1].lines.concat([
        "",
        dim("  ▸ ") + live("NEXT") + dim("  ") + head(DEMOS[nxt].label) +
          dim("  ·  " + (nxt === "roger" ? "looping the dial…" : "auto-advancing…"))
      ]));
      timer = setTimeout(function () { if (playing) select(nxt, "auto"); }, NEXT_HOLD);
      return;
    }
    elapsed += frames[idx].hold;
    showFrame(idx + 1);
    timer = setTimeout(advance, frames[idx].hold);
  }

  function start() {
    clearTimer();
    if (!frames.length) buildFrames(current);
    if (REDUCED) { renderFinal(); return; }
    idx = 0; elapsed = 0; playing = true;
    setPlayUI(true);
    showFrame(0);
    timer = setTimeout(advance, frames[0].hold);
    startRAF();
  }
  function pause() {
    if (!playing) return;
    playing = false; clearTimer(); setPlayUI(false); stopRAF();
  }
  function resume() {
    if (REDUCED || playing || !frames.length) return;
    playing = true; setPlayUI(true);
    var f = frames[idx];
    var spent = Math.min(f.hold, now() - frameStart);
    timer = setTimeout(advance, Math.max(60, f.hold - spent));
    startRAF();
  }

  function setPlayUI(on) {
    if (!btnPlay) return;
    btnPlay.textContent = on ? "❚❚" : "▶";
    btnPlay.setAttribute("aria-label", on ? "Pause demo" : "Play demo");
    btnPlay.setAttribute("aria-pressed", on ? "true" : "false");
  }

  function select(name, mode) {
    if (!DEMOS[name]) return;
    // mode: "force" = deliberate preset switch -> reset + play right away;
    //       "auto"  = first scroll-into-view kick (respects hover/visibility);
    //       falsy   = passive load (paused on frame 0).
    current = name;
    buildFrames(name);
    if (titleEl) titleEl.textContent = DEMOS[name].title;
    if (presetBar) {
      var btns = presetBar.querySelectorAll("[data-demo]");
      for (var i = 0; i < btns.length; i++) {
        var on = btns[i].getAttribute("data-demo") === name;
        btns[i].classList.toggle("is-on", on);
        btns[i].setAttribute("aria-pressed", on ? "true" : "false");
      }
    }
    if (REDUCED) { renderFinal(); return; }
    if (mode === "force") {
      // a deliberate switch means "show me this one now" - the cursor is by
      // definition over the panel, so ignore the hover-pause and play at once.
      hovered = false; visible = true;
      start();
    } else if (mode === "auto" && visible && !hovered) {
      start();
    } else { pause(); showFrame(0); setBar(0); }
  }

  /* ---- one shared rAF: advances ONLY the tuning-bar fill ------------- */
  var rafId = null, rafRunning = false;
  function tick() {
    if (!rafRunning) return;
    var f = frames[idx];
    var frac = f ? Math.min(1, (now() - frameStart) / Math.max(1, f.hold)) : 0;
    setBar(total ? (elapsed + (f ? f.hold * frac : 0)) / total : 0);
    rafId = requestAnimationFrame(tick);
  }
  function startRAF() { if (REDUCED || rafRunning) return; rafRunning = true; rafId = requestAnimationFrame(tick); }
  function stopRAF() { rafRunning = false; if (rafId) cancelAnimationFrame(rafId); rafId = null; }

  /* ---- wiring -------------------------------------------------------- */
  if (presetBar) {
    presetBar.addEventListener("click", function (e) {
      var btn = e.target.closest("[data-demo]");
      if (btn) select(btn.getAttribute("data-demo"), "force");
    });
  }
  if (btnPlay) btnPlay.addEventListener("click", function () { if (playing) pause(); else resume(); });
  if (btnReplay) btnReplay.addEventListener("click", function () { if (REDUCED) { renderFinal(); return; } start(); });

  // pause on hover so it can be read; resume on leave
  root.addEventListener("mouseenter", function () { hovered = true; pause(); });
  root.addEventListener("mouseleave", function () { hovered = false; if (visible) resume(); });

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) pause();
    else if (visible && !hovered) resume();
  });

  // autoplay the first demo once it scrolls into view; pause offscreen
  var kicked = false;
  function activate() {
    visible = true;
    if (REDUCED) { renderFinal(); return; }
    if (!kicked) { kicked = true; select(current, "auto"); }
    else if (!hovered) resume();
  }
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) activate();
        else { visible = false; pause(); }
      });
    }, { threshold: 0.3 });
    io.observe(root);
  } else { activate(); }

  // first paint so the panel isn't blank before it scrolls in
  buildFrames(current);
  if (REDUCED) renderFinal();
  else { showFrame(0); setBar(0); }
})();
