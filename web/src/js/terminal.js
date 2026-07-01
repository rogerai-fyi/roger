/* =====================================================================
   RogerAI - the demo console: a tape-deck / station-preset player.

   Eight demos, selected from a radio-preset bar ( [ roger ] [ tune in ] [ agent ]
   [ share ] [ payouts ] [ using ] [ hosting ] [ ping ] ) with the current preset lit
   red. The first five are animated terminal replays that follow the roger arc - borrow
   -> automate -> lend -> get paid - and mirror the real TUI preset bank you get from a
   bare `roger` ( [0] AGENT  [1] TUNE IN  [2] SHARE ). The last three are MEDIA tapes
   (using, hosting, ping): muted/looping inline <video>s. Transport controls (play /
   pause / replay) and a tuning-bar progress readout, all radio/tape-deck styled. tune
   in and agent are deliberately distinct: tune in hands an ENDPOINT to your tools;
   agent is roger ITSELF running a multi-tool job.

     roger    - boot the dial: type `roger`, acquire the carrier (an animated
                sweep), reveal the preset bank + brand lockup, then read the
                live band (stations fade in, signal bars fill).
     tune in  - BORROW: lock the strongest station -> CHANNEL OPEN + the drop-in
                endpoint plate, then YOUR tool (a curl, the OpenAI SDK, Cursor,
                bots) hits 127.0.0.1; tokens stream, the wallet debits live, and a
                dropped station triggers under-the-hood failover (no retry).
     agent    - AUTOMATE: roger is itself an agent (the [0] AGENT dj.md harness) -
                hand it a JOB and it plans + runs several tools (run/read/grep)
                autonomously, then synthesizes an answer + a multi-tool receipt.
     share    - LEND: scan local backends, the detected-models table, the price
                editor (vs the live median) + a free overnight schedule, go ON AIR,
                the broker canary verify, then a live request log + earnings/slots.
     payouts  - GET PAID: the on-air earnings hint -> `roger payout status` (KYC +
                payable vs held + the 120-day/$25/monthly policy) -> `roger payout
                request` -> sent to the bank via Stripe Connect.

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
  var mediaWrap = document.getElementById("termMedia");

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

  // walletLine renders the consumer's live wallet readout (debited per token) -
  // the spend story that makes TUNE IN about *your tools using a metered endpoint*.
  function walletLine(bal) {
    return dim("  ▸ wallet ") + money("$" + bal) + dim("  · debited live, per token");
  }
  // obox draws a left-border tool box (no right border, so no per-line padding math)
  // for the AGENT tape's tool calls. title rides the top rule; lines are the body.
  function obox(title, lines) {
    var out = [dim("  ┌ ") + gold("◆ " + title) + " " + dim(new Array(Math.max(2, 48 - title.length)).join("─"))];
    lines.forEach(function (l) { out.push(dim("  │ ") + l); });
    out.push(dim("  └" + new Array(56).join("─")));
    return out;
  }

  /* ---- the five tapes: the roger arc (mirror the TUI preset bank) -----
     roger (the lay of the land) -> tune in (BORROW: a drop-in endpoint your own
     tools hit, with failover + live wallet debit) -> agent (roger's BUILT-IN agent
     runs a multi-step, multi-tool job) -> share (LEND: price + schedule + verify +
     live requests) -> payouts (cash out). tune in = "endpoint for your tools";
     agent = "autonomous worker" - deliberately distinct. ------------------- */
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

    // `tune in` - BORROW a model: tune in, get a drop-in OpenAI-compatible endpoint
    // your OWN tools point at (curl / SDK / Cursor / bots), watch tokens stream + the
    // wallet debit live, and see the one-stable-endpoint failover heal a dropped
    // station under the hood. NOT in-TUI chat - that's what makes it != AGENT.
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
          // CHANNEL OPEN + the drop-in BASE URL / API KEY / MODEL plate.
          c.show(base.concat(steps, [""], endpointPlate("@nightowl")), END_HOLD);

          // THE POINT: point any OpenAI client at 127.0.0.1. Shown as a curl, but it is
          // the same line for the OpenAI SDK, Cursor, Cline, your agents/bots.
          var useHead = [
            ok("  ◉ CHANNEL OPEN") + "  " + dim("point any OpenAI tool at ") + money("127.0.0.1:" + PORT) + "   " + gold("◆ verified"),
            RULE
          ];
          c.type(useHead, "curl 127.0.0.1:" + PORT + "/v1/chat/completions \\", AFTER_TYPE);
          var curl = useHead.concat([
            PROMPT + head("curl 127.0.0.1:" + PORT + "/v1/chat/completions \\"),
            dim("       -H ") + "\"Authorization: Bearer " + money("roger-local") + "\" \\",
            dim("       -d ") + "'{\"model\":\"" + head(BAND) + "\", \"messages\": […]}'"
          ]);
          c.show(curl, STAGE);
          // streaming response + the wallet ticking down per token.
          c.show(curl.concat(["",
            dim("  ((•)) ") + live("◉ streaming") + dim("  @nightowl · 58 t/s") + CURSOR,
            walletLine("12.4802")
          ]), STEP);
          c.show(curl.concat(["",
            dim("  ((•)) ") + ok("◉") + dim("  @nightowl"), "",
            "  Refactored the handler into three functions; tests still pass.", "",
            walletLine("12.4799"),
            dim("  ◆ receipt co-signed · ") + money("131 tok · $0.000039") + dim(" · 70% to @nightowl")
          ]), STAGE);
          // failover: ONE stable endpoint - a station drops mid-stream, roger re-routes
          // under the hood, no retry in your code (internal/client/failover.go).
          c.show(curl.concat(["",
            dim("  ((•)) ") + live("◉ @nightowl dropped mid-stream") + dim(" …") + CURSOR
          ]), STEP);
          c.show(curl.concat(["",
            ok("  ◉ failover") + dim("  re-routed ") + head("@nightowl") + dim(" → ") + head("@glacier") +
              dim("  · same URL + key, no retry in your code"), "",
            dim("  ((•)) ") + ok("◉") + dim("  @glacier · stream resumed · 47 t/s"),
            walletLine("12.4796")
          ]), END_HOLD);
        });
      }
    },

    // `agent` - roger is ITSELF an agent (the [0] AGENT dj.md harness): hand it a JOB
    // and it plans, runs SEVERAL tools autonomously over the band, and finishes the
    // task. The multi-step, multi-tool loop is what makes it != TUNE IN (which just
    // hands an endpoint to YOUR tools). Each turn is metered + co-signed.
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
            dim("  roger is itself an agent - hand it a JOB, not a question."), "",
            dim("  ▸ it plans, runs many tools, and finishes the task on its own.")
          ]), STAGE);
          var task = "/agent find the package with the worst test coverage and why";
          c.type(agentHead, task, AFTER_TYPE);
          var P = PROMPT + head(task);
          // plan
          c.show(agentHead.concat([P, "",
            dim("  ((•)) ") + live("◉ planning") + dim("  3 steps · routing to band…") + CURSOR
          ]), STAGE);
          // step 1/3 - run the coverage suite
          c.show(agentHead.concat([P, "",
            dim("  ((•)) ") + ok("◉") + dim("  step 1/3 · run")
          ], obox("tool · run", [
            head("$ ") + "go test ./... -cover | sort -t% -k1 | head -1",
            ok("internal/store") + dim("   ") + live("71.2%") + dim("   ← lowest")
          ])), STAGE);
          // step 2/3 - read the offending file
          c.show(agentHead.concat([P, "",
            dim("  ((•)) ") + ok("◉") + dim("  step 2/3 · read")
          ], obox("tool · read", [
            head("internal/store/ledger.go") + dim("  (412 lines)"),
            dim("  cold: ") + "settleRecount() error branches"
          ])), STAGE);
          // step 3/3 - grep the tests to confirm the gap
          c.show(agentHead.concat([P, "",
            dim("  ((•)) ") + ok("◉") + dim("  step 3/3 · grep")
          ], obox("tool · grep", [
            head("$ ") + "grep -c settleRecount internal/store/*_test.go",
            ok("0") + dim("   no test exercises the recount error path")
          ])), STAGE);
          // synthesized answer + the multi-tool receipt
          c.show(agentHead.concat([P, "",
            dim("  ((•)) ") + ok("◉") + dim("  done · 3 tools"), "",
            "  " + head("internal/store") + " is lowest at " + live("71.2%") + " - settleRecount()'s",
            "  error branches are untested. Add a case where the broker recount",
            "  disagrees with the node's claim and assert the hold is refunded.", "",
            dim("  ◆ receipt co-signed · ") + money("3 tools · 1.24k tok · $0.00031") + dim(" · ") + live("roger that.")
          ]), END_HOLD);
        });
      }
    },

    // `share` - LEND your GPU: scan backends -> detected models -> SET A PRICE (the
    // in-TUI pricing editor vs the live band median) + a free overnight schedule -> go
    // ON AIR -> the broker CANARY verifies you -> live requests stream in -> earnings +
    // the on-air slots. Richer + longer than a single go-live.
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
          // the detected-models table; pick qwen3-coder-30b.
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
            dim("  ▸ ") + head("qwen3-coder-30b") + dim(" · set your rate…")
          ]), STAGE);

          // --- PRICE editor: set the out-price against the live band median ---
          var priceHead = [
            "  " + BRAND + "   " + span("t-sel", " •[2] SHARE ") + dim("  set your rate · ") + head("qwen3-coder-30b") + "   " + gold("◆ provider"),
            RULE
          ];
          function priceScreen(out, note) {
            return priceHead.concat([
              dim("  $/1M in    ") + money("0.20"),
              dim("  $/1M out   ") + span("t-sel", " " + out + " ◂") + dim("   ") + note,
              dim("  schedule   ") + "free 03:00–03:30 UTC " + dim("· earn the rest of the day"), "",
              "  " + span("t-sel", " enter ") + dim(" go on air   ") + span("t-sel", " s ") + dim(" schedule   ") + span("t-sel", " esc ") + dim(" back")
            ]);
          }
          c.show(priceScreen("0.27", dim("band median 0.27 $/M")), STAGE);
          c.show(priceScreen("0.30", live("a touch above median · fair")), STAGE);

          // --- go ON AIR + the broker CANARY verify (now routable + scored) ---
          var onairHead = "  " + BRAND + "   " + span("t-sel", " •[2] SHARE ") + dim("  on air") + "   " + gold("◆ provider");
          var liveOne = ok("  ◉ ON AIR") + "  " + head("@you ") + gold("◆") + dim(" · ") + head(BAND) + dim(" · ") + money("$0.30/1M out");
          c.show([onairHead, RULE,
            ok("  ◉ ON AIR") + "  " + head("@you ") + gold("◆") + dim(" · ") + head(BAND) +
              dim(" · ") + money("earning $0.30/1M") + dim(" · ") + live("station live")
          ], STAGE);
          c.show([onairHead, RULE, liveOne, "",
            ok("  ◉") + dim(" canary probe   broker sent a test request …") + CURSOR
          ], STEP);
          c.show([onairHead, RULE, liveOne, "",
            ok("  ◉") + dim(" canary probe   ") + gold("◆ verified") + dim("  · now routable + scored on the band")
          ], STAGE);

          // --- live requests stream in (left-border log, grows row by row) ---
          var reqs = [
            ok("◉ ") + head(pad("@ssh-bot", 13)) + dim(pad("318 tok", 10)) + money("+$0.000095"),
            ok("◉ ") + head(pad("@cursor-ide", 13)) + dim(pad("742 tok", 10)) + money("+$0.000223"),
            ok("◉ ") + head(pad("@nightly-ci", 13)) + dim(pad("1.2k tok", 10)) + money("+$0.000360")
          ];
          function liveLog(n) {
            var rows = [onairHead, RULE, dim("  ┌ live · incoming ") + dim(new Array(38).join("─"))];
            for (var i = 0; i < n; i++) rows.push(dim("  │ ") + reqs[i]);
            if (n < reqs.length) rows.push(dim("  │ ") + live("◉ serving…") + CURSOR);
            rows.push(dim("  └" + new Array(56).join("─")));
            return rows;
          }
          for (var r = 1; r <= reqs.length; r++) c.show(liveLog(r), STAGE);

          // --- earnings + on-air slots + the payout hint (segue to PAYOUTS) ---
          c.show([onairHead, RULE,
            dim("  served today  ") + head("362 req") + dim("   ·   earned ") + money("+$3.78") + dim("  (70% keep)"),
            dim("  balance       ") + money("$42.18 payable") + dim("   ·   ON AIR ") + head("1/4 slots"), "",
            dim("  ▸ your GPU is paying rent. cash out with ") + span("t-sel", " [$] PAYOUT ")
          ], END_HOLD);
        });
      }
    },

    // `payouts` - MONETIZE: the money-OUT story. The on-air earnings hint -> `roger
    // payout status` (KYC + payable vs held + policy) -> `roger payout request` -> sent
    // to the bank via Stripe Connect. Completes the arc: borrow -> automate -> lend ->
    // get paid. (Grounds: cmd/rogerai payoutStatus output; 120-day hold · $25 · monthly.)
    payouts: {
      label: "payouts", title: "roger — payouts",
      build: function () {
        return compile(function (c) {
          // the on-air earnings surface, balance ticking up, with the payout hint.
          var earnHead = "  " + BRAND + "   " + span("t-sel", " •[2] SHARE ") + dim("  earnings") + "   " + gold("◆ provider");
          var bal = ["38.40", "41.02", "42.18"];
          for (var i = 0; i < bal.length; i++) {
            c.show([earnHead, RULE,
              ok("  ◉ ON AIR") + dim("  @you ◆ · ") + head(BAND) + dim(" · 1/4 slots"), "",
              dim("  served today  ") + head((280 + i * 40) + " req") + dim("   ·   ") + money("$" + bal[i]) + dim(" balance · 70% keep"),
              dim("  ▸ ") + money("$" + bal[i] + " payable") + dim("  ·  run ") + span("t-sel", " roger payout ")
            ], i < bal.length - 1 ? STEP : STAGE);
          }
          // `roger payout status`: KYC + payable vs held + the policy.
          c.type([], "roger payout status", AFTER_TYPE);
          c.show([
            PROMPT + head("roger payout status"), "",
            head("  PAYOUT"),
            dim("    KYC        ") + ok("active") + dim("  (Stripe Connect complete)"),
            dim("    payable    ") + money("$42.18") + dim("   (ready to cash out)"),
            dim("    held       ") + money("$18.40") + dim("   (inside the 120-day hold)"),
            dim("    paid out   ") + money("$306.50") + dim("   (lifetime)"),
            dim("    next due   ") + head("2026-07-15") + dim("   (held earnings become payable)"),
            dim("    policy     120-day hold · $25 min · monthly")
          ], END_HOLD);
          // `roger payout request`: send the payable balance to the bank.
          c.type([], "roger payout request", AFTER_TYPE);
          c.show([
            PROMPT + head("roger payout request"), "",
            ok("  ◉") + dim(" requesting ") + money("$42.18") + dim(" → Stripe Connect …") + CURSOR
          ], STEP);
          c.show([
            PROMPT + head("roger payout request"), "",
            ok("  ◉ PAYOUT SENT") + dim("  ") + money("$42.18") + dim(" → your bank · arrives in ~2 days"), "",
            dim("  held earnings roll to payable on a 120-day basis · paid monthly."), "",
            dim("  ▸ borrow · automate · lend · get paid. ") + live("roger that.")
          ], END_HOLD);
        });
      }
    },

    // `using` - the animated STORY tape (ComfyUI): borrow a friend's qwen-coder-next
    // through opencode, use Hermes from anywhere, serve your local LLM to your bots.
    // A MEDIA tape (muted/looping mp4 + gif), not an ASCII replay - so it carries
    // `media:true` + the <video> id instead of a frame builder.
    using: { label: "using", title: "roger — using it", media: true, el: "termUsing" },

    // `hosting` - the animated STORY tape (ComfyUI): the companion to the `share` tape,
    // told in the house cartoon voice - run your own model locally (Ollama / LM Studio),
    // expose it OpenAI-compatibly (even as a TTS voice), and put it on the air to be
    // discovered, served, metered and earning. Also a media tape.
    hosting: { label: "hosting", title: "roger — hosting", media: true, el: "termHosting" },

    // `ping` - the live Ping World screensaver (the real `roger --ping`), captured
    // straight from internal/tui. Also a media tape.
    ping: { label: "ping", title: "roger — ping", media: true, el: "termPing" }
  };

  /* ---- engine -------------------------------------------------------- */
  // playlist order: when a demo finishes we auto-advance to the next preset and play
  // it - the roger arc: roger -> tune in -> agent -> share -> payouts -> back to roger.
  var ORDER = ["roger", "tunein", "agent", "share", "payouts"];
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
    if (DEMOS[name].media) { frames = []; total = 0; return; } // media tapes have no frames
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
    if (DEMOS[current] && DEMOS[current].media) { setBar(1); return; } // poster shows; no frames
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
    if (titleEl) titleEl.textContent = DEMOS[name].title;
    highlightPreset(name);
    if (DEMOS[name].media) { selectMedia(name, mode); return; } // a video tape
    showAscii();
    buildFrames(name);
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

  function highlightPreset(name) {
    if (!presetBar) return;
    var btns = presetBar.querySelectorAll("[data-demo]");
    for (var i = 0; i < btns.length; i++) {
      var on = btns[i].getAttribute("data-demo") === name;
      btns[i].classList.toggle("is-on", on);
      btns[i].setAttribute("aria-pressed", on ? "true" : "false");
    }
  }

  /* ---- media tapes (using / hosting / ping): a muted, looping inline <video> shown in
     the SAME slot as the ASCII screen. The transport (play/pause/replay), tuning bar
     and hover/visibility pause all work on the video; reduced-motion shows the
     poster (gif) and never autoplays. Media tapes are opt-in (clicked), so they
     stay OUT of the auto-advance ORDER - the CLI demos keep the rotation snappy. */
  var media = null; // the currently-shown <video>, or null while an ASCII tape is up

  function videos() { return mediaWrap ? mediaWrap.querySelectorAll("video") : []; }

  // hydratePosters: promote each tape's deferred data-poster -> poster. The poster gif is
  // 2.6-3.7MB and the browser fetches it the moment a `poster=` is set (even under
  // preload="none"), so the markup ships it as data-poster and we set it only once the
  // console nears the viewport (activate) / a tape opens (selectMedia). First paint pays
  // zero tape bytes; the gif fetches lazily right before it can be seen.
  var postersHydrated = false;
  function hydratePosters() {
    if (postersHydrated) return;
    postersHydrated = true;
    var vs = videos();
    for (var i = 0; i < vs.length; i++) {
      var p = vs[i].getAttribute("data-poster");
      if (p && !vs[i].getAttribute("poster")) vs[i].setAttribute("poster", p);
    }
  }

  function showAscii() {
    var vs = videos();
    for (var i = 0; i < vs.length; i++) { vs[i].pause(); vs[i].hidden = true; }
    media = null;
    if (mediaWrap) mediaWrap.hidden = true;
    if (screen) screen.hidden = false;
  }

  function selectMedia(name, mode) {
    pause();                                   // halt any ASCII frame timer
    hydratePosters();                          // ensure the deferred poster gif is set before showing
    if (screen) screen.hidden = true;
    if (mediaWrap) mediaWrap.hidden = false;
    var target = document.getElementById(DEMOS[name].el);
    var vs = videos();
    for (var i = 0; i < vs.length; i++) {
      var on = vs[i] === target;
      vs[i].hidden = !on;
      if (!on) vs[i].pause();
    }
    media = target;
    setBar(0);
    if (!media) return;
    if (REDUCED) { setPlayUI(false); setBar(1); return; } // poster only, no motion
    if (mode === "force" || (mode === "auto" && visible && !hovered)) playMedia();
    else { try { media.currentTime = 0; } catch (e) {} setPlayUI(false); }
  }

  function playMedia() {
    if (!media || REDUCED) return;
    setPlayUI(true);
    var p = media.play();
    if (p && p.catch) p.catch(function () {}); // ignore autoplay rejection
    startRAF();
  }
  function pauseMedia() {
    if (!media) return;
    media.pause(); setPlayUI(false); stopRAF();
  }

  /* ---- one shared rAF: advances ONLY the tuning-bar fill ------------- */
  var rafId = null, rafRunning = false;
  function tick() {
    if (!rafRunning) return;
    if (media) {
      var d = media.duration || 0;
      setBar(d ? media.currentTime / d : 0); // the bar tracks the video's progress
    } else {
      var f = frames[idx];
      var frac = f ? Math.min(1, (now() - frameStart) / Math.max(1, f.hold)) : 0;
      setBar(total ? (elapsed + (f ? f.hold * frac : 0)) / total : 0);
    }
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
  if (btnPlay) btnPlay.addEventListener("click", function () {
    if (media) { if (media.paused) playMedia(); else pauseMedia(); return; }
    if (playing) pause(); else resume();
  });
  if (btnReplay) btnReplay.addEventListener("click", function () {
    if (media) { try { media.currentTime = 0; } catch (e) {} if (!REDUCED) playMedia(); return; }
    if (REDUCED) { renderFinal(); return; } start();
  });

  // pause on hover so it can be read; resume on leave
  root.addEventListener("mouseenter", function () { hovered = true; if (media) pauseMedia(); else pause(); });
  root.addEventListener("mouseleave", function () { hovered = false; if (!visible) return; if (media) playMedia(); else resume(); });

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) { if (media) pauseMedia(); else pause(); }
    else if (visible && !hovered) { if (media) playMedia(); else resume(); }
  });

  // autoplay the first demo once it scrolls into view; pause offscreen
  var kicked = false;
  function activate() {
    visible = true;
    hydratePosters();  // console is near the viewport now - safe to fetch the tape posters
    if (REDUCED) { if (!media) renderFinal(); return; }
    if (!kicked) { kicked = true; select(current, "auto"); }
    else if (!hovered) { if (media) playMedia(); else resume(); }
  }
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) activate();
        else { visible = false; if (media) pauseMedia(); else pause(); }
      });
    }, { threshold: 0.3 });
    io.observe(root);
  } else { activate(); }

  // first paint so the panel isn't blank before it scrolls in
  buildFrames(current);
  if (REDUCED) renderFinal();
  else { showFrame(0); setBar(0); }
})();
