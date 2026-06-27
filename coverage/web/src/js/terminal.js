/* =====================================================================
   RogerAI - the demo console: a tape-deck / station-preset player.

   Four switchable demos, each an animated terminal replay, selected from a
   radio-preset bar ( [ rogerai ] [ search ] [ use ] [ share ] ) with the
   current preset lit red. Transport controls (play / pause / replay) and a
   tuning-bar progress readout, all radio/tape-deck styled.

     rogerai  - a fuller walk of the bare interactive TUI: boot + the preset
                bar, then [2] SHARE -> auto-detecting local models across
                backends (the provider-onboarding moment), then the consumer
                walk: tuning in + browsing the band, opening a channel +
                chatting, a glimpse of compact `m` mode, and a short agent-mode
                beat (a dj.md harness tool turn: tool call + result).
     search   - the band listing for a model, with inline signal bars.
     use      - staged scanning -> locking -> handshake -> CHANNEL OPEN +
                endpoint plate.
     share    - going (( ON AIR )) and an earnings tick.

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
  var MAX_PRICE = "0.30";
  var MIN_TPS = "40";

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

  function bandHead(cmd) {
    return [
      PROMPT + head(cmd), "",
      dim("  band ") + head(BAND) + dim("   " + nStations() + " stations    ") +
        money(bandRange() + " $/M out") + dim(" (live range)"),
      RULE
    ];
  }
  function endpointPlate(stationWho) {
    return [
      ok("  ◉ CHANNEL OPEN") + "  " + head(BAND) + " " + dim("via " + stationWho) + "   " + gold("◆ verified"),
      "",
      dim("  BASE URL  ") + money("http://127.0.0.1:8779/v1"),
      dim("  API KEY   ") + money("rog-sk-live-9f3c…a71d"),
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

  /* ---- the four demos ------------------------------------------------ */
  var DEMOS = {
    // bare `rogerai` - a fuller walk of the interactive TUI: boot + preset
    // bar (incl [0] AGENT), tune the band, open a channel + chat, a glimpse of
    // compact `m` mode, then a short agent-mode beat (a dj.md tool turn).
    rogerai: {
      label: "rogerai", title: "rogerai - the dial",
      build: function () {
        return compile(function (c) {
          // --- OPEN ON ONBOARDING: launch the bare TUI, then [2] SHARE ->
          // auto-detect local models across backends. This is the DISCOVERY
          // moment (find what you can put on air), NOT a station broadcasting.
          // The consumer walk (tune in -> chat -> [0] AGENT) follows after.
          c.type([], "rogerai", AFTER_TYPE);
          c.show([
            PROMPT + head("rogerai"), "",
            dim("  ((•)) RogerAI   the two-way radio for GPUs   ") + dim("[ tuning in… ]")
          ], STEP);
          var deck = [
            PROMPT + head("rogerai"), "",
            dim("  ((•)) RogerAI   ") + ok("◉ carrier acquired") + dim("   broker.rogerai.fyi"), "",
            dim("  presets   ") + head("[0] AGENT") + dim("  [1] TUNE IN  ") + head("[2] SHARE") + dim("  [3] CONFIG  [4] balance"),
            dim("            ") + dim("dial a band · ◉ on  ○ off  ◆ lineage-verified")
          ];
          c.show(deck, STAGE);

          // --- press [2] SHARE: become a provider, scan for local models ---
          var pressShare = deck.concat(["",
            dim("  ▸ ") + head("[2] SHARE") + dim("  ·  put your own GPU on the air")
          ]);
          c.show(pressShare, STEP);

          // detection scan: probe the real local backends one at a time.
          // ◉ = backend up + models found, ○ = port answered but empty.
          var scanHead = [
            head("  [2] SHARE") + dim("  ·  go on air") + "   " + gold("◆ provider"),
            RULE,
            dim("  scanning the band for local models…"), ""
          ];
          var probes = [
            ok("  ◉") + " ollama      " + dim(":11434") + dim("  ……  ") + ok("2 models"),
            ok("  ◉") + " llama.cpp   " + dim(":8080") + dim("   ……  ") + ok("1 model"),
            dim("  ○") + " lm-studio   " + dim(":1234") + dim("   ……  ") + dim("none"),
            ok("  ◉") + " vLLM        " + dim(":8000") + dim("   ……  ") + ok("1 model")
          ];
          // probing line, then each backend resolves in turn
          c.show(scanHead.concat([dim("  ○") + " ollama      " + dim(":11434") + dim("  ……  ") + live("probing…") + CURSOR]), STEP);
          for (var pi = 0; pi < probes.length; pi++) {
            var rows = probes.slice(0, pi + 1);
            if (pi + 1 < probes.length) {
              var nextName = ["ollama      :11434", "llama.cpp   :8080 ", "lm-studio   :1234 ", "vLLM        :8000 "][pi + 1];
              rows = rows.concat([dim("  ○") + " " + dim(nextName) + dim("  ……  ") + live("probing…") + CURSOR]);
            }
            c.show(scanHead.concat(rows), STEP);
          }
          c.show(scanHead.concat(probes, ["",
            ok("  ◉") + dim("  3 backends up · ") + head("4 models") + dim(" detected across the box")
          ]), STAGE);

          // --- the SHARE table: detected local models, ready to go on air ---
          var shareHead = [
            head("  [2] SHARE") + dim("  ·  your models, detected") + "   " + gold("◆ provider"),
            RULE,
            "  " + dim(pad("MODEL", 20)) + dim(pad("BACKEND", 12)) + dim(pad("STATUS", 11)) + dim("YOUR RATE")
          ];
          var locals = [
            { model: "gpt-oss-20b",      back: "ollama",    rate: "0.18" },
            { model: "qwen3-coder-30b",  back: "vLLM",      rate: "0.30" },
            { model: "llama-3.3-70b",    back: "llama.cpp", rate: "0.55" }
          ];
          function localRow(m) {
            return "  " + dim("○") + " " + head(pad(m.model, 18)) +
              dim(pad(m.back, 12)) + dim(pad("OFF-AIR", 11)) + money(m.rate + " $/M");
          }
          for (var li = 0; li < locals.length; li++) {
            c.show(shareHead.concat(locals.slice(0, li + 1).map(localRow)), STEP);
          }
          c.show(shareHead.concat(locals.map(localRow), [RULE,
            dim("  your models, ready to go on air - pick one to monetize.")
          ]), STAGE);
          c.show(shareHead.concat(locals.map(localRow), [RULE,
            dim("  pick a model, set a rate, go live - GPU pays rent. ") + live("roger that.")
          ]), END_HOLD);

          // --- now the consumer side: tune in, browse the band ---
          c.type([], "rogerai", AFTER_TYPE);
          c.show(deck, STAGE);

          // --- tune in: browse the band, signal bars fill in ---
          c.show(bandHead("browse the band").concat(stations.slice(0, 2).map(stationRow)), STEP);
          c.show(bandHead("browse the band").concat(stations.slice(0, 3).map(stationRow)), STEP);
          c.show(bandHead("browse the band").concat(stations.map(stationRow)).concat([RULE]), STAGE);

          // --- open a channel on the strongest station ---
          c.show([
            dim("  band ") + head(BAND) + dim("   tuning in to ") + head("@nightowl") + dim("…"), ""
          ].concat(endpointPlate("@nightowl")), STAGE);

          // --- chat over the open channel ---
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

          // --- glimpse of compact `m` windowshade mode ---
          c.show([
            dim("  press ") + head("m") + dim("  · windowshade -> compact"), "",
            ok("◉") + " " + head(BAND) + dim("  @nightowl ") + live("▆▆▆▆▆▅·") + dim("  58 t/s  ") + money("0.22 $/M") + dim("  ◆"),
            dim("  ▸ ") + money("$42.18") + dim("  ·  3 stations on band  ·  calm mode")
          ], STAGE);

          // --- agent-mode beat: [0] AGENT, a dj.md tool turn ---
          var agentHead = [
            head("  [0] AGENT") + dim("  ·  harness ") + head("dj.md") + dim("  ·  band ") + head(BAND) + "   " + gold("◆ verified"),
            RULE
          ];
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

    // `roger search` - the band listing with signal bars
    search: {
      label: "search", title: "roger search",
      build: function () {
        return compile(function (c) {
          c.type([], "roger search " + BAND, AFTER_TYPE);
          var hdr = bandHead("roger search " + BAND);
          c.show(hdr, STEP);
          for (var i = 0; i < stations.length; i++) {
            c.show(hdr.concat(stations.slice(0, i + 1).map(stationRow)), 380);
          }
          c.show(hdr.concat(stations.map(stationRow)).concat([
            RULE,
            dim("   ◆ = lineage-verified   tune the BAND + your margins, not one station")
          ]), END_HOLD);
        });
      }
    },

    // `roger use` - scanning -> locking -> handshake -> CHANNEL OPEN + plate
    use: {
      label: "use", title: "roger use",
      build: function () {
        return compile(function (c) {
          var cmd = "roger use " + BAND + " --max-price " + MAX_PRICE + " --min-tps " + MIN_TPS;
          c.type([], cmd, AFTER_TYPE);
          var base = [PROMPT + head(cmd), ""];
          var steps = [
            ok("  ◉") + " scanning stations  " + dim(nStations() + " on this band") + "  " + ok("ok"),
            ok("  ◉") + " locking strongest  " + head("@nightowl") + dim(" · ") + live("58 t/s") + dim(" · ") + money("0.22 $/M") + "  " + ok("ok"),
            ok("  ◉") + " lineage handshake  " + gold("◆ weights·shard·token") + "  " + ok("ok")
          ];
          for (var i = 0; i < steps.length; i++) c.show(base.concat(steps.slice(0, i + 1)), STEP);
          c.show(base.concat(steps, [""], endpointPlate("@nightowl")), END_HOLD);
        });
      }
    },

    // `roger share` - going ON AIR + an earnings tick
    share: {
      label: "share", title: "roger share",
      build: function () {
        return compile(function (c) {
          var cmd = "roger share " + BAND + " --rate " + MAX_PRICE;
          c.type([], cmd, AFTER_TYPE);
          var base = [PROMPT + head(cmd), ""];
          var steps = [
            ok("  ◉") + " detecting backend  " + dim("scanning local ports") + "  " + ok("ok"),
            ok("  ◉") + " backend locked     " + head("vLLM") + dim(" · 127.0.0.1:8000 · ") + head(BAND) + "  " + ok("ok"),
            ok("  ◉") + " call-sign assigned " + head("@you") + " " + gold("◆") + dim(" · rate ") + money(MAX_PRICE + " $/M out") + "  " + ok("ok"),
            ok("  ◉") + " lineage co-sign    " + gold("◆ weights·shard·token") + "  " + ok("ok")
          ];
          for (var i = 0; i < steps.length; i++) c.show(base.concat(steps.slice(0, i + 1)), STEP);
          var onair = base.concat(steps, ["",
            ok("  ◉ ON AIR") + "  " + head("@you ") + gold("◆") + dim(" · ") + head(BAND) +
              dim(" · ") + live("station live") + dim(" · appears in the band in ~10s")
          ]);
          c.show(onair, STAGE);
          var live1 = onair.concat(["",
            dim("  ┌ live ──────────────────────────────────────────────────────┐"),
            dim("  │ ") + ok("◉ on air ") + gold("◆") + dim(" │ ") + head("@you    ") +
              dim(" │ ") + live("incoming request from @ssh-bot…") + dim("            │"),
            dim("  └────────────────────────────────────────────────────────────┘")
          ]);
          c.show(live1, STAGE);
          c.show(live1.concat(["",
            ok("  ◉") + " served " + head("742 tok out") + dim(" @ ") + money(MAX_PRICE + " $/M") +
              dim("  ·  earned ") + money("+$0.000223") + dim("  (70% keep)"),
            dim("  balance ") + money("$42.18") + dim("  ·  your GPU is paying rent. ") + live("roger that.")
          ]), END_HOLD);
        });
      }
    }
  };

  /* ---- engine -------------------------------------------------------- */
  // playlist order: when a demo finishes we auto-advance to the next preset
  // and play it (rogerai -> search -> use -> share -> back to rogerai).
  var ORDER = ["rogerai", "search", "use", "share"];
  var NEXT_HOLD = 1500;         // ms to show the "NEXT:" indicator before switching
  function nextOf(name) {
    var i = ORDER.indexOf(name);
    return ORDER[(i + 1) % ORDER.length];
  }

  var current = "rogerai";
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
          dim("  ·  " + (nxt === "rogerai" ? "looping the dial…" : "auto-advancing…"))
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
