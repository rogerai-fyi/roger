/* =====================================================================
   RogerAI - THE SIGNAL BENCH (Playbox / WAVE MESH)

   One sentence, left to right:

     [SENSOR SELECTOR] -> [MODEL CHAIN] -> [THE MONITOR]

   Pick a sensor type - the types are derived from the recorded tags, so
   the selector cannot offer a sensor nothing measured. Its pads dial a
   RECORDED condition; type + condition select one deterministic record.
   Then daisy-chain models into the rail: each stage transforms the
   output, and THE MONITOR - the hero of the deck - shows the output at
   every stage as a readability cascade: the raw wire, then the Pico's
   protocol line, then the Nano's human-readable verdict. Adding a model
   visibly adds a more-readable stage. That is the whole product.

   HONESTY, unchanged: no model executes in a browser. The raw stage is
   the byte-for-byte recorded window; the Pico and Nano stages print
   recorded fields only; the verdict paragraph is a fixed template over
   recorded fields plus a STATIC fault-kind glossary (labelled as
   glossary - documentation, not measurement). Unrecorded family members
   chain in but their stage says "no recorded transcript - output
   unchanged". A margin is a logprob difference nothing here computes.
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var SVGNS = "http://www.w3.org/2000/svg";

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }
  function svg(tag, attrs) {
    var n = document.createElementNS(SVGNS, tag);
    for (var k in attrs) if (attrs.hasOwnProperty(k)) n.setAttribute(k, attrs[k]);
    return n;
  }

  // The four measured floors are the knob's detents - the knob cannot be set
  // to a floor nothing measured.
  var DETENTS = [0.5, 1.0, 1.5, 2.0];
  var TOP = DETENTS[DETENTS.length - 1];

  /* ---- the model family: sizes, statuses, runs-on from the family page.
     `does` is each slot's transform, in plain words, for its chain card. */
  var FAMILY = [
    { id: "edge", label: "Roger Edge", size: "KB-10M", status: "in design",
      runs: "ESP32 · Cortex-M", icon: "pocket", px: 26,
      does: "wake and sensing tier",
      blurb: "wake and sensing tier - in design" },
    { id: "pico", label: "Wave Pico", size: "270M", status: "recorded",
      runs: "gateway class", icon: "pocket", px: 38,
      does: "asserts or escalates",
      blurb: "the recorded reader on this bench" },
    { id: "nano", label: "Wave Nano", size: "~350M", status: "recorded",
      runs: "phone · Pi · gateway", icon: "reader", px: 46,
      does: "adjudicates doubts",
      blurb: "the recorded senior - adjudicates doubtful reads" },
    { id: "micro", label: "Wave Micro", size: "1-8B", status: "trained",
      runs: "laptop · edge computer", icon: "reader", px: 58,
      does: "local reasoning tier",
      blurb: "trained, but has no recorded run on this bench" },
    { id: "core", label: "Wave Core", size: "8-30B", status: "planned",
      runs: "single GPU · control room", icon: "senior", px: 52,
      does: "control-room synthesis tier",
      blurb: "planned slot" },
    { id: "station", label: "Wave Station", size: "30-70B", status: "planned",
      runs: "rack · plant server", icon: "senior", px: 62,
      does: "plant-server tier",
      blurb: "planned slot" },
    { id: "satellite", label: "Wave Satellite", size: "~70B+", status: "planned",
      runs: "plant server room", icon: "senior", px: 74,
      does: "plant-wide tier",
      blurb: "planned slot" },
  ];
  function familyById(id) {
    for (var i = 0; i < FAMILY.length; i++) if (FAMILY[i].id === id) return FAMILY[i];
    return null;
  }

  /* ---- the fault-kind GLOSSARY. STATIC DOCUMENTATION, not measurement:
     these are the standard sensor-health terms the suite labels with. The
     monitor marks every use with a "glossary" microlabel so a fixed
     dictionary can never read as a recorded fact. */
  var GLOSSARY = {
    none: "the channel reads as a healthy instrument should",
    stuck: "the reading freezes at one value and stops responding",
    dropout: "the reading intermittently vanishes",
    noisy: "excess random variation swamps the signal",
    drifting: "the reading slides steadily away from the truth",
    railed: "the reading pins at the top or bottom of its range",
  };

  /* ---- sensor TYPES: derived from the recorded tags. A suffix like
     _DISCHARGE_TEMP groups its records into a type; the AI_xxxx tags -
     Sparkplug aliases with no name - group into UNNAMED CHANNEL, which is
     kept on the selector deliberately: real plants are full of them. */
  var TYPE_DEFS = [
    { key: "temp", label: "DISCHARGE TEMP", icon: "thermo", suffixes: ["DISCHARGE_TEMP"] },
    { key: "press", label: "PRESSURE", icon: "gauge", suffixes: ["DISCHARGE_PRESS", "SUCTION_PRESS"] },
    { key: "vib", label: "VIBRATION", icon: "vibro", suffixes: ["VIBRATION"] },
    { key: "amp", label: "MOTOR CURRENT", icon: "ammeter", suffixes: ["MOTOR_CURRENT"] },
    { key: "oil", label: "OIL TEMP", icon: "oilcan", suffixes: ["OIL_TEMP"] },
    { key: "unnamed", label: "UNNAMED CHANNEL", icon: "junction", suffixes: [] },
  ];

  var UNIT_WORD = { Cel: "°C", kPa: "kPa", "mm/s": "mm/s", A: "A" };
  var CONDW = { none: "OK" };

  var PATCH = {
    catalog: null, measured: null, scene: null,
    types: [],            // built from the records at boot
    typeKey: null,        // the selected sensor type
    cond: "none",         // the dialed condition (a recorded truth)
    windows: null,        // wave-windows.json: the real 96-sample series per record
    tab: "all",           // the monitor's channel button: all | raw | pico | nano
    reply: null,          // the prompt's last response (classified, DRAFT-only)
    chain: [],            // family ids, in daisy-chain order
    floor: 1.5,           // the Pico's margin floor (measured detents)
    operator: false,      // the lever by the monitor footer
    authority: false,     // UNATTENDED AUTHORITY: a POLICY the visitor sets -
                          // the senior may act with no operator on shift, and
                          // its decisions queue for human review. Never a
                          // capability claim; a policy simulation over the
                          // same recorded outcomes.
    whyOpen: null,        // which why-panel is expanded (one at a time)
    verdict: null,
    menuFor: null,        // chain slot index whose attach menu is open
    booted: false,
    step: 0,
  };

  function buildTypes() {
    var m = PATCH.measured;
    if (!m) return;
    var bySuffix = {};
    m.records.forEach(function (r, i) {
      var tag = (r.window && r.window.tag) || "";
      var key = /^AI_\d+$/.test(tag) ? "unnamed" : tag.replace(/^[A-Z]+\d+_/, "");
      bySuffix[key] = bySuffix[key] || [];
      bySuffix[key].push(i);
    });
    PATCH.types = [];
    TYPE_DEFS.forEach(function (def) {
      var idxs = def.key === "unnamed" ? (bySuffix.unnamed || [])
        : def.suffixes.reduce(function (acc, s) { return acc.concat(bySuffix[s] || []); }, []);
      if (!idxs.length) return; // a type exists only if the records do
      idxs.sort(function (a, b) { return a - b; });
      var recIdx = {}, pickWhy = {};
      var byTruth = {};
      idxs.forEach(function (i) {
        var t = m.records[i].truth;
        (byTruth[t] = byTruth[t] || []).push(i);
      });
      Object.keys(byTruth).forEach(function (t) {
        var pick = pickRecord(byTruth[t], t, m.records);
        recIdx[t] = pick.idx;
        pickWhy[t] = pick.why;
      });
      var conds = Object.keys(recIdx).sort(function (a, b) {
        if (a === "none") return -1;
        if (b === "none") return 1;
        return a < b ? -1 : 1;
      });
      PATCH.types.push({ key: def.key, label: def.label, icon: def.icon,
                         count: idxs.length, conds: conds, recIdx: recIdx, pickWhy: pickWhy });
    });
    if (!PATCH.typeKey && PATCH.types[0]) PATCH.typeKey = PATCH.types[0].key;
  }

  /* ---- the representative pick: per (type, condition), the RECORDED window
     that shows the condition most clearly - chosen by a documented feature
     criterion, deterministically, from that type's records only. The samples
     are never touched; only WHICH record a pad replays is chosen.

     NOTE on stuck: in this export every stuck window has longest_run = 1 -
     the suite's "stuck" means the sensor stops TRACKING the process while
     quantization jitter keeps neighbouring samples unequal. The honest
     "clearest stuck" is therefore the FLATTEST window: minimum sd relative
     to the window's own magnitude (tiebreak: max repeat_frac). */
  var PICK = {
    none:     { why: "calmest healthy window", score: function (w) { return -w.hf_energy; } },
    stuck:    { why: "steadiest window", score: function (w) {
                  return -(w.sd / Math.max(Math.abs(w.mean), 1e-9)) + (w.repeat_frac || 0) * 1e-6; } },
    dropout:  { why: "most resets", score: function (w) { return w.n_resets + (w.max_drop || 0) * 1e-6; } },
    noisy:    { why: "most high-frequency energy", score: function (w) { return w.hf_energy; } },
    drifting: { why: "steepest trend", score: function (w) {
                  return Math.abs(w.slope_per_min) + (w.monotonic_frac || 0) * 1e-6; } },
    railed:   { why: "longest at the rail", score: function (w) { return w.at_max_frac; } },
  };

  function pickRecord(idxs, truth, records) {
    var rule = PICK[truth] || PICK.none;
    var best = idxs[0], bestScore = -Infinity;
    idxs.forEach(function (i) {
      var w = records[i].window || {};
      var s = rule.score(w);
      if (s > bestScore) { bestScore = s; best = i; }
    });
    var w2 = records[best].window || {};
    var detail = truth === "stuck" ? "sd " + w2.sd + " over " + w2.n + " samples"
      : truth === "dropout" ? w2.n_resets + " resets, max drop " + w2.max_drop
      : truth === "noisy" ? "hf_energy " + w2.hf_energy
      : truth === "drifting" ? "slope " + w2.slope_per_min + "/min"
      : truth === "railed" ? "at the rail " + Math.round((w2.at_max_frac || 0) * 100) + "% of the window"
      : "hf_energy " + w2.hf_energy;
    return { idx: best,
             why: "chosen as the clearest recorded " + (truth === "none" ? "OK" : truth.toUpperCase()) +
                  ": " + rule.why + " (" + detail + ")" };
  }

  function typeOf(key) {
    for (var i = 0; i < PATCH.types.length; i++) if (PATCH.types[i].key === key) return PATCH.types[i];
    return null;
  }
  function currentType() { return typeOf(PATCH.typeKey); }

  // The selected RECORD: type + condition, deterministically.
  function currentRecord() {
    var t = currentType();
    if (!t || !PATCH.measured) return null;
    var i = t.recIdx[PATCH.cond] != null ? t.recIdx[PATCH.cond] : t.recIdx[t.conds[0]];
    return PATCH.measured.records[i];
  }
  function unitWordOf(r) {
    var u = r && r.window && r.window.unit;
    if (!u) return null;
    return UNIT_WORD[u] || u;
  }

  /* ---- the chain, read for meaning ---------------------------------------
     The FIRST recorded reader in the chain reads the wire (Pico asserts or
     escalates at the floor; Nano reads direct). A Nano placed after a Pico
     is the senior. Everything unrecorded is a pass-through: honestly
     silent, output unchanged. */
  function chainInfo() {
    var picoAt = -1, nanoAt = -1;
    PATCH.chain.forEach(function (id, i) {
      if (id === "pico" && picoAt < 0) picoAt = i;
      if (id === "nano" && nanoAt < 0) nanoAt = i;
    });
    return {
      picoAt: picoAt, nanoAt: nanoAt,
      senior: picoAt >= 0 && nanoAt > picoAt,
      reader: picoAt >= 0 ? "pico" : (nanoAt >= 0 ? "nano" : null),
    };
  }

  // A prompt reply cites the selection (readings) and the chain (the DRAFT's
  // addressee). When either moves, the card on screen would be describing a
  // bench that no longer exists - so context moves dismiss it. (v11: caught
  // live - a TEMP reading card survived a switch to VIBRATION.)
  function contextMoved() {
    PATCH.reply = null;
  }

  function selectType(key, cond) {
    var t = typeOf(key);
    if (!t) return;
    contextMoved();
    PATCH.typeKey = key;
    PATCH.cond = (cond != null && t.recIdx[cond] != null) ? cond
      : (t.recIdx[PATCH.cond] != null ? PATCH.cond : t.conds[0]);
  }

  /* =====================================================================
     THE CASCADE - the output at each stage, recorded fields only
     ===================================================================== */
  function stages() {
    var out = [];
    var r = currentRecord();
    if (!r) return out;
    var w = r.window || {};
    out.push({ kind: "raw", who: "RAW WIRE", body: w.body ||
      "(this record's window was not exported - the log is absent, not invented)",
      tag: w.tag, unit: unitWordOf(r), r: r });

    var info = chainInfo();
    var escalated = false, answered = false;
    PATCH.chain.forEach(function (id, i) {
      var fam = familyById(id);
      if (!fam) return;
      if (fam.status !== "recorded") {
        out.push({ kind: "silent", who: fam.label.toUpperCase(), fam: fam });
        return;
      }
      if (id === "pico" && i === info.picoAt) {
        var esc = r.child.margin < PATCH.floor;
        escalated = esc;
        if (!esc) answered = true;
        out.push({ kind: "pico", who: "WAVE PICO", esc: esc,
                   said: r.child.prediction, margin: r.child.margin,
                   floor: PATCH.floor, ok: r.child.prediction === r.truth, r: r });
        return;
      }
      if (id === "nano") {
        if (i === info.nanoAt && info.senior && !escalated) {
          // the senior only speaks when a read reaches it
          out.push({ kind: "quietSenior", who: "WAVE NANO",
                     note: "the Pico asserted below - nothing escalated to the senior on this record" });
          return;
        }
        answered = true;
        out.push(nanoStage(r, w));
        return;
      }
      // a second pico, or a nano before a pico: pass-through, stated
      out.push({ kind: "silent", who: fam.label.toUpperCase(), fam: fam });
    });
    if (escalated && !answered) {
      out.push({ kind: "deadend", who: "NOBODY",
                 note: "the Pico escalated and no senior is in the chain - the doubt goes unheard" });
    }
    return out;
  }

  // The Nano's stage: the human-readable verdict. A fixed TEMPLATE over
  // recorded fields, plus the static glossary - never generated prose.
  function nanoStage(r, w) {
    var pred = r.parent.prediction;
    var isFault = r.truth !== "none";
    var predRight = pred === r.truth;
    var outcome = predRight
      ? (isFault ? "caught: the recorded truth is " + r.truth
                 : "quiet: the recorded truth is none")
      : (isFault ? "MISSED: the recorded truth is " + r.truth
                 : "false alarm: the recorded truth is none");
    var para = "On " + (w.tag || r.node_id) + ": " +
      (w.n ? w.n + " samples, " : "") +
      (w.lo != null ? w.lo + " … " + w.hi + (unitWordOf(r) ? " " + unitWordOf(r) : "") +
        ", mean " + w.mean + ". " : "") +
      'The senior read the window and said " ' + pred + '" (margin ' +
      r.parent.margin.toFixed(2) + ") - " + outcome + ".";
    return { kind: "nano", who: "WAVE NANO", verdict: pred,
             gloss: GLOSSARY[pred] || null, para: para,
             ok: predRight, isFault: isFault, r: r };
  }

  /* =====================================================================
     THE DERIVATION - one record through one chain, recounted
     ===================================================================== */
  function derive() {
    if (!PATCH.measured) return;
    var st = stages();
    var state, why, label = null, sym = null;

    var r = currentRecord();
    var info = chainInfo();
    var isFault = r && r.truth !== "none";
    var hasRecorded = PATCH.chain.some(function (id) {
      var f = familyById(id); return f && f.status === "recorded";
    });

    if (!PATCH.chain.length) {
      state = "off"; why = "The sensor is emitting raw wire. Tap [+] on the rail to chain a model.";
    } else if (!hasRecorded || !info.reader) {
      state = "off";
      why = "Nothing in the chain has a recorded run on this bench - the lamp only glows " +
        "for recounts of recorded records. Chain Wave Pico or Wave Nano.";
    } else {
      // walk the record through the chain
      var missed = false, fixable = false, deadEnd = false, caught = false, falseAlarm = false;
      if (info.reader === "nano" && info.picoAt < 0) {
        var okN = r.parent.prediction === r.truth;
        if (okN) { if (isFault) caught = true; }
        else if (isFault) missed = true;
        else falseAlarm = true;
      } else {
        var esc = r.child.margin < PATCH.floor;
        if (!esc) {
          var okC = r.child.prediction === r.truth;
          if (okC) { if (isFault) caught = true; }
          else if (isFault) {
            missed = true;
            if (r.parent.prediction === r.truth && r.child.margin < TOP) fixable = true;
          } else falseAlarm = true;
        } else if (info.senior) {
          var okP = r.parent.prediction === r.truth;
          if (okP) { if (isFault) caught = true; }
          else if (isFault) missed = true;
          else falseAlarm = true;
        } else {
          deadEnd = true;
          if (isFault) missed = true;
        }
      }

      if (deadEnd && missed) {
        state = "red";
        why = "The dialed fault went unheard - the Pico doubted, and there is no senior in the chain. Chain Wave Nano after it.";
      } else if (fixable) {
        state = "red";
        why = "The dialed fault was missed - and a higher floor would have escalated it to a senior who, in the recorded run, had the right answer. Raise the FLOOR knob.";
      } else if (deadEnd) {
        state = "yellow";
        why = "The Pico doubts this read and has nobody to ask - chain Wave Nano after it.";
      } else if (!PATCH.operator) {
        // UNATTENDED AUTHORITY: with the senior aboard and the policy granted,
        // the no-operator state is not DEGRADED but PROVISIONAL - the chain
        // acts, and its decisions queue for a person to review. This is a
        // POLICY the visitor sets, not a measurement; the recounted outcomes
        // are the same recorded records either way.
        var seniorAboard = PATCH.chain.indexOf("nano") >= 0;
        if (PATCH.authority && seniorAboard) {
          state = "green"; label = "PROVISIONAL"; sym = "◐";
          why = "The senior is acting unattended - this record's decision is queued for human " +
            "review. Whether a model is big enough to take a shift is a POLICY you set, " +
            "not a measurement; the ladder should still end with a person.";
        } else {
          state = "yellow";
          why = "The chain works, but no operator is on shift - flip the lever by the monitor; " +
            "the ladder should end with a person." +
            (seniorAboard ? " (Or grant UNATTENDED AUTHORITY and let the senior act provisionally.)" : "");
        }
      } else if (!missed) {
        state = "green";
        why = caught ? "Fault caught, operator on shift. Complete chain."
          : falseAlarm ? "A false alarm - the chain cried fault on a healthy channel. The operator will read it."
          : "All quiet: the channel is dialed OK and the chain agrees.";
        if (falseAlarm) { state = "yellow"; }
      } else {
        state = "green"; label = "AT CEILING";
        why = "This fault was missed by the recorded senior itself - no knob setting changes that. " +
          "The chain is at its measured ceiling on this record.";
      }
    }
    PATCH.verdict = { state: state, why: why, label: label, sym: sym, stages: st };
    paintMonitor();
  }

  /* ---- the announcer + one-line ticker ------------------------------------ */
  function react(msg) {
    var strip = $("wpStrip");
    if (strip) {
      PATCH.step++;
      var li = el("li", "wp-strip__line");
      li.appendChild(el("span", "wp-strip__t", String(PATCH.step).padStart(3, "0")));
      li.appendChild(el("span", null, msg));
      strip.insertBefore(li, strip.firstChild);
      while (strip.childNodes.length > 2) strip.removeChild(strip.lastChild);
    }
    var say = $("wpSay");
    if (say) say.textContent = msg;
  }

  /* =====================================================================
     THE DIAL - tap to step, drag for detents, arrows for the keyboard
     ===================================================================== */
  function drawDial(opts) {
    var size = opts.size || 56;
    var wrap = el("div", "wp-knob");
    wrap.tabIndex = 0;
    wrap.setAttribute("role", "slider");
    wrap.setAttribute("aria-label", opts.name);
    wrap.setAttribute("aria-valuemin", "0");
    wrap.setAttribute("aria-valuemax", String(opts.values.length - 1));

    function angle(i) {
      if (opts.values.length === 1) return 0;
      return -135 + (i / (opts.values.length - 1)) * 270;
    }
    function paint() {
      wrap.textContent = "";
      wrap.setAttribute("aria-valuenow", String(opts.index));
      wrap.setAttribute("aria-valuetext", opts.labels[opts.index]);
      wrap.title = opts.tip ? opts.tip(opts.index) : "";
      var s = svg("svg", { viewBox: "0 0 64 64", class: "wp-knob__svg", "aria-hidden": "true",
                           width: size, height: size });
      s.appendChild(svg("circle", { class: "wp-knob__ring", cx: 32, cy: 32, r: 26 }));
      opts.values.forEach(function (_, i) {
        var a = (angle(i) - 90) * Math.PI / 180;
        s.appendChild(svg("line", {
          class: "wp-knob__tick" + (i === opts.index ? " is-on" : ""),
          x1: 32 + Math.cos(a) * 21, y1: 32 + Math.sin(a) * 21,
          x2: 32 + Math.cos(a) * 26, y2: 32 + Math.sin(a) * 26,
        }));
      });
      var g = svg("g", { class: "wp-knob__cap", transform: "rotate(" + angle(opts.index) + " 32 32)" });
      g.appendChild(svg("circle", { cx: 32, cy: 32, r: 17 }));
      g.appendChild(svg("line", { class: "wp-knob__needle", x1: 32, y1: 32, x2: 32, y2: 17 }));
      s.appendChild(g);
      wrap.appendChild(s);
      wrap.appendChild(el("span", "wp-knob__k", opts.labels[opts.index]));
    }
    function setIndex(i, fromTap) {
      i = Math.max(0, Math.min(opts.values.length - 1, i));
      if (i === opts.index && !fromTap) return;
      opts.index = i;
      paint();
      opts.onset(i);
    }

    var moved = false;
    wrap.addEventListener("pointerdown", function (e) {
      if (e.button != null && e.button !== 0) return;
      e.preventDefault();
      moved = false;
      var startY = e.clientY, startIdx = opts.index, pid = e.pointerId;
      wrap.setPointerCapture && wrap.setPointerCapture(pid);
      function mv(ev) {
        if (ev.pointerId !== pid) return;
        var step = Math.round((startY - ev.clientY) / 24);
        if (step) moved = true;
        var i = Math.max(0, Math.min(opts.values.length - 1, startIdx + step));
        if (i !== opts.index) setIndex(i);
      }
      function up(ev) {
        if (ev.pointerId !== pid) return;
        document.removeEventListener("pointermove", mv);
        document.removeEventListener("pointerup", up);
        document.removeEventListener("pointercancel", up);
      }
      document.addEventListener("pointermove", mv);
      document.addEventListener("pointerup", up);
      document.addEventListener("pointercancel", up);
    });
    // The tablet path: a plain tap steps the dial one position, wrapping,
    // like clicking a rotary switch through its stations.
    wrap.addEventListener("click", function (e) {
      e.stopPropagation();
      if (moved) { moved = false; return; }
      setIndex((opts.index + 1) % opts.values.length, true);
    });
    wrap.addEventListener("keydown", function (e) {
      if (e.key === "ArrowUp" || e.key === "ArrowRight") { e.preventDefault(); setIndex(opts.index + 1); }
      if (e.key === "ArrowDown" || e.key === "ArrowLeft") { e.preventDefault(); setIndex(opts.index - 1); }
    });
    paint();
    return wrap;
  }

  /* =====================================================================
     RENDER
     ===================================================================== */
  function render() {
    renderSelector();
    renderPads();
    renderChain();
    renderWhys();
    renderTabs();
    paintMonitor();
    paintWire();
    renderOp();
    renderMirror();
  }

  function modelIcon(fam) {
    var box = el("span", "ws-icon ws-icon--" + fam.icon);
    box.style.width = fam.px + "px";
    box.style.height = Math.round(fam.px * (fam.icon === "senior" ? 1.25 : fam.icon === "pocket" ? 1 : 0.66)) + "px";
    box.setAttribute("aria-hidden", "true");
    box.appendChild(el("span", "wb-plate__ink"));
    return box;
  }

  /* ---- the sensor selector: one button per RECORDED type ----------------- */
  function renderSelector() {
    var host = $("wsTypes");
    if (!host) return;
    host.textContent = "";
    host.setAttribute("role", "radiogroup");
    host.setAttribute("aria-label", "Sensor type - derived from the recorded tags");
    PATCH.types.forEach(function (t) {
      var sel = PATCH.typeKey === t.key;
      var b = el("button", "sn-type" + (sel ? " is-sel" : ""));
      b.type = "button";
      b.setAttribute("role", "radio");
      b.setAttribute("aria-checked", sel ? "true" : "false");
      var art = el("span", "sn-type__art sn-type__art--" + t.icon);
      art.setAttribute("aria-hidden", "true");
      art.appendChild(el("span", "wb-plate__ink"));
      b.appendChild(art);
      var txt = el("span", "sn-type__txt");
      txt.appendChild(el("b", null, t.label));
      txt.appendChild(el("span", null, t.count + " recorded"));
      b.appendChild(txt);
      b.title = t.key === "unnamed"
        ? "Sparkplug aliases with no name - the wire never said what these measure. Real plants are full of them."
        : "grouped from the recorded tags ending _" + t.label.replace(/ /g, "_");
      b.addEventListener("click", function () {
        selectType(t.key);
        derive(); render();
        var r = currentRecord();
        react(t.label + " selected - emitting " + ((r.window && r.window.tag) || "its wire") + ".");
        var again = document.querySelector(".sn-type.is-sel");
        if (again) again.focus({ preventScroll: true });
      });
      host.appendChild(b);
    });
  }

  /* ---- the pads: one backlit pad per RECORDED condition of the type ------ */
  // The first-run nudge: one tiny dismissible hint pointing at the pads.
  // localStorage-gated (like pb.mode); gone forever after dismissal or the
  // first pad press. Static styling - any pulse is CSS, reduced-motion-gated.
  function nudgeWanted() {
    try { return !window.localStorage.getItem("pb.meshNudge"); }
    catch (e) { return false; }
  }
  function dismissNudge(silent) {
    if (!nudgeWanted()) return;
    try { window.localStorage.setItem("pb.meshNudge", "seen"); } catch (e) { /* private mode */ }
    var n = document.querySelector(".sn-nudge");
    if (n && n.parentNode) n.parentNode.removeChild(n);
    if (!silent) {
      var ta = $("wpPrompt");
      if (ta) ta.focus({ preventScroll: true });
    }
  }

  function renderPads() {
    var host = $("wsPads");
    if (!host) return;
    host.textContent = "";
    var t = currentType();
    if (!t) return;
    var head = el("div", "syn-pads__head");
    head.appendChild(el("b", null, "CONDITION"));
    head.appendChild(el("span", "sn-sub",
      "one pad per recorded condition - unrecorded conditions have no pad"));
    host.appendChild(head);
    var row = el("div", "syn-pads__row");
    row.setAttribute("role", "radiogroup");
    row.setAttribute("aria-label", "Condition - each pad replays one recorded instance");
    t.conds.forEach(function (c) {
      var pad = el("button", "syn-pad" + (PATCH.cond === c ? " is-lit" : "") +
        (c === "none" ? " syn-pad--ok" : ""));
      pad.type = "button";
      pad.setAttribute("role", "radio");
      pad.setAttribute("aria-checked", PATCH.cond === c ? "true" : "false");
      var rr = PATCH.measured.records[t.recIdx[c]];
      pad.title = (t.pickWhy && t.pickWhy[c] ? t.pickWhy[c] + " · " : "") +
        "replays recorded record " + rr.node_id + " (scene " + rr.scene_id +
        ") - a pad is a recorded instance, selected, not simulated";
      var cap = el("span", "syn-pad__cap");
      var sp = sparkline(rr);
      if (sp) cap.appendChild(sp);
      pad.appendChild(cap);
      pad.appendChild(el("span", "syn-pad__k", CONDW[c] || c.toUpperCase()));
      pad.addEventListener("click", function () {
        contextMoved();
        PATCH.cond = c;
        dismissNudge(true);
        derive(); render();
        var r = currentRecord();
        react("Condition " + (CONDW[c] || c.toUpperCase()) + " - replaying " +
          ((r.window && r.window.tag) || r.node_id) + ".");
        var again = document.querySelector(".syn-pad.is-lit");
        if (again) again.focus({ preventScroll: true });
      });
      row.appendChild(pad);
    });
    host.appendChild(row);

    if (nudgeWanted()) {
      var nd = el("div", "sn-nudge");
      nd.appendChild(el("span", "sn-nudge__arrow", "↑"));
      nd.appendChild(el("span", null, "try: press DROPOUT - watch the trace and the chain react"));
      var nx = el("button", "sn-nudge__x");
      nx.type = "button";
      nx.setAttribute("aria-label", "Dismiss this hint");
      nx.textContent = "×";
      nx.addEventListener("click", function (e) { e.stopPropagation(); dismissNudge(); });
      nd.appendChild(nx);
      host.appendChild(nd);
    }

    // the selected record's identity, honest to the wire
    var r = currentRecord();
    if (r) {
      var w = r.window || {};
      var id = el("p", "sn-now");
      id.appendChild(el("b", null, w.tag || r.node_id));
      id.appendChild(el("span", null, " · " + (unitWordOf(r) || "unit not stated") +
        (w.n ? " · " + w.n + " samples" : "")));
      if (!unitWordOf(r)) id.title = "the wire did not state a unit - a defaulted unit would be an invented fact";
      host.appendChild(id);
      var vu = drawVU(w, unitWordOf(r));
      if (vu) {
        var vwrap = el("div", "sn-vuwell");
        vwrap.appendChild(vu);
        host.appendChild(vwrap);
      }
    }
  }

  /* ---- the chain rail: sensor -> slots -> monitor ------------------------- */
  function renderChain() {
    var host = $("wsChain");
    if (!host) return;
    host.textContent = "";

    // the sensor end of the rail
    var t = currentType();
    var sens = el("div", "sn-slot sn-slot--sensor");
    var art = el("span", "sn-type__art sn-type__art--" + (t ? t.icon : "gauge"));
    art.setAttribute("aria-hidden", "true");
    art.appendChild(el("span", "wb-plate__ink"));
    sens.appendChild(art);
    sens.appendChild(el("b", null, t ? t.label : ""));
    sens.appendChild(el("span", "sn-sub", "emitting"));
    host.appendChild(sens);

    PATCH.chain.forEach(function (id, i) {
      host.appendChild(railArrow());
      host.appendChild(drawChainCard(id, i));
    });

    // the next empty slot: one big [+]
    host.appendChild(railArrow());
    var plus = el("button", "syn-plus syn-plus--slot");
    plus.type = "button";
    plus.setAttribute("aria-expanded", PATCH.menuFor === PATCH.chain.length ? "true" : "false");
    plus.setAttribute("aria-label", "Add a model to the chain");
    plus.textContent = "+";
    plus.addEventListener("click", function (e) {
      e.stopPropagation();
      PATCH.menuFor = PATCH.menuFor === PATCH.chain.length ? null : PATCH.chain.length;
      render();
      var m = document.querySelector(".ws-menu button");
      if (m) m.focus();
    });
    host.appendChild(plus);

    var badgeHost = $("wsChainBadge");
    if (badgeHost) {
      badgeHost.textContent = "";
      if (PATCH.chain.length === 2 && PATCH.chain[0] === "pico" && PATCH.chain[1] === "nano") {
        var badge = el("span", "sn-reco", "RECOMMENDED · PICO + NANO");
        badge.title = "the measured deployment pattern: a small reader asserting at its floor, " +
          "a senior adjudicating the doubtful reads";
        badgeHost.appendChild(badge);
      }
    }

    host.appendChild(railArrow());
    var mon = el("div", "sn-slot sn-slot--monitor");
    mon.appendChild(el("b", null, "MONITOR"));
    mon.title = "the chain ends at the monitor - the output, below";
    host.appendChild(mon);

    if (PATCH.menuFor != null) {
      var wrap = $("wsChainMenu");
      if (wrap) { wrap.textContent = ""; wrap.appendChild(drawMenu(PATCH.menuFor)); }
    } else {
      var wrap2 = $("wsChainMenu");
      if (wrap2) wrap2.textContent = "";
    }
  }

  function railArrow() {
    // a short patch cable with a little sag - geometry as texture
    var a = el("span", "sn-rail");
    a.setAttribute("aria-hidden", "true");
    var s = svg("svg", { viewBox: "0 0 22 16", width: 22, height: 16, class: "sn-rail__svg" });
    s.appendChild(svg("path", { class: "sn-rail__cable", d: "M2 5 C 8 13, 14 13, 20 5" }));
    s.appendChild(svg("circle", { class: "sn-rail__plug", cx: 2, cy: 5, r: 2.2 }));
    s.appendChild(svg("circle", { class: "sn-rail__plug", cx: 20, cy: 5, r: 2.2 }));
    a.appendChild(s);
    return a;
  }

  // The bounds meter as a VU instrument: the recorded window's range is the
  // scale, the mean is the needle. Recorded numbers only - an instrument
  // face around them, never a number they did not bring.
  function drawVU(w, unitWord) {
    if (!w || w.lo == null || w.hi == null) return null;
    var W = 150, H = 84, cx = W / 2, cy = H - 10, R = 58;
    var host = svg("svg", { class: "sn-vu", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "recorded window: " + w.lo + " to " + w.hi + (unitWord ? " " + unitWord : "") +
        ", mean " + w.mean });
    function pt(frac, r) {
      var a = (-140 + frac * 100) * Math.PI / 180; // -140deg .. -40deg
      return [cx + Math.cos(a) * r, cy + Math.sin(a) * r];
    }
    var arc0 = pt(0, R), arc1 = pt(1, R);
    host.appendChild(svg("path", { class: "sn-vu__arc",
      d: "M" + arc0[0].toFixed(1) + " " + arc0[1].toFixed(1) +
         " A" + R + " " + R + " 0 0 1 " + arc1[0].toFixed(1) + " " + arc1[1].toFixed(1) }));
    for (var i = 0; i <= 10; i++) {
      var frac = i / 10, len = (i % 5 === 0) ? 8 : 4;
      var o = pt(frac, R), ii = pt(frac, R - len);
      host.appendChild(svg("line", { class: "sn-vu__tick" + (i % 5 === 0 ? " is-major" : ""),
        x1: o[0].toFixed(1), y1: o[1].toFixed(1), x2: ii[0].toFixed(1), y2: ii[1].toFixed(1) }));
    }
    var span = (w.hi - w.lo) || 1;
    var nf = Math.max(0, Math.min(1, (w.mean - w.lo) / span));
    var np = pt(nf, R - 6);
    host.appendChild(svg("line", { class: "sn-vu__needle",
      x1: cx, y1: cy, x2: np[0].toFixed(1), y2: np[1].toFixed(1) }));
    host.appendChild(svg("circle", { class: "sn-vu__hub", cx: cx, cy: cy, r: 3.4 }));
    var t1 = svg("text", { class: "sn-vu__t", x: 6, y: H - 2, "text-anchor": "start" });
    t1.textContent = String(w.lo);
    var t2 = svg("text", { class: "sn-vu__t", x: W - 6, y: H - 2, "text-anchor": "end" });
    t2.textContent = String(w.hi);
    var t3 = svg("text", { class: "sn-vu__t sn-vu__t--mean", x: cx, y: 12, "text-anchor": "middle" });
    t3.textContent = "mean " + w.mean + (unitWord ? " " + unitWord : "");
    host.appendChild(t1); host.appendChild(t2); host.appendChild(t3);
    host.setAttribute("title", "recorded window bounds, with the mean as the needle");
    return host;
  }

  function drawChainCard(id, i) {
    var fam = familyById(id);
    var card = el("div", "sn-slot sn-slot--model" +
      (fam.status === "recorded" ? "" : " sn-slot--quiet"));
    card.title = fam.blurb + (fam.status === "recorded"
      ? " - its stage replays recorded fields only"
      : " - it chains in honestly silent: no recorded transcript");
    card.appendChild(modelIcon(fam));
    var txt = el("span", "sn-slot__txt");
    txt.appendChild(el("b", null, fam.label));
    txt.appendChild(el("span", "sn-sub", fam.does));
    txt.appendChild(el("span", "sn-sub sn-sub--dim", fam.size + " · " + fam.status));
    card.appendChild(txt);
    var x = el("button", "ws-resp__x");
    x.type = "button";
    x.setAttribute("aria-label", "Remove " + fam.label + " from the chain");
    x.textContent = "×";
    x.addEventListener("click", function () {
      contextMoved();
      PATCH.chain.splice(i, 1);
      PATCH.menuFor = null;
      derive(); render();
      react(fam.label + " removed from the chain.");
    });
    card.appendChild(x);

    if (id === "pico") {
      card.appendChild(drawDial({
        values: DETENTS.slice(),
        labels: DETENTS.map(function (d) { return "FLOOR " + d.toFixed(1); }),
        index: Math.max(0, DETENTS.indexOf(PATCH.floor)),
        name: "margin floor",
        size: 38,
        tip: function (k) { return knobTip(DETENTS[k]); },
        onset: function (k) {
          PATCH.floor = DETENTS[k];
          derive(); render();
          react("Floor set to " + PATCH.floor.toFixed(1) + " - margins below it now escalate.");
          var again = document.querySelector(".sn-slot--model .wp-knob");
          if (again) again.focus({ preventScroll: true });
        },
      }));
    }
    // the model whys moved to the standing row below the rail: an open pop
    // inside the rail's scroll container was CLIPPED (founder v12: "when the
    // tooltip is open i have to scroll and it hides parts of the ui") - all
    // whys now expand in place, full width, one at a time.
    return card;
  }

  // The attach menu: the whole family, sized so size is VISIBLE.
  function drawMenu(slotIdx) {
    var menu = el("div", "ws-menu");
    menu.setAttribute("role", "menu");
    menu.setAttribute("aria-label", "Add a model to the chain");
    FAMILY.forEach(function (fam) {
      var b = el("button", "ws-menu__item" +
        (fam.status === "recorded" ? "" : " ws-menu__item--quiet"));
      b.type = "button";
      b.setAttribute("role", "menuitem");
      b.appendChild(modelIcon(fam));
      var txt = el("span", "ws-menu__txt");
      txt.appendChild(el("b", null, fam.label));
      txt.appendChild(el("span", null, fam.size + " · runs on " + fam.runs));
      txt.appendChild(el("span", "ws-menu__status",
        fam.status === "recorded" ? "recorded on this bench" : fam.status + " · will attach silent"));
      b.appendChild(txt);
      b.title = fam.blurb;
      b.addEventListener("click", function (e) {
        e.stopPropagation();
        chainAdd(fam.id, slotIdx);
      });
      menu.appendChild(b);
    });
    var close = el("button", "wp-remove");
    close.type = "button";
    close.textContent = "close";
    close.addEventListener("click", function (e) {
      e.stopPropagation();
      PATCH.menuFor = null;
      render();
    });
    menu.appendChild(close);
    return menu;
  }

  // THE LADDER RUNS UP. A senior does not hand work down: the reader (Pico,
  // or Nano reading direct) comes first, the senior after it, observers
  // behind. Attach order is the visitor's gesture; CHAIN order is physics -
  // normalize makes a backwards chain unconstructible rather than merely
  // discouraged (founder v12: a Pico was reading the Nano's output).
  function normalizeChain() {
    var rest = PATCH.chain.filter(function (x) { return x !== "pico" && x !== "nano"; });
    var out = [];
    if (PATCH.chain.indexOf("pico") >= 0) out.push("pico");
    if (PATCH.chain.indexOf("nano") >= 0) out.push("nano");
    out = out.concat(rest);
    var moved = out.join(",") !== PATCH.chain.join(",");
    PATCH.chain = out;
    return moved;
  }

  function chainAdd(id, at) {
    var fam = familyById(id);
    if (PATCH.chain.indexOf(id) >= 0) {
      // one of EACH - a duplicate silent model adds rail, not meaning
      // (v11: a fidgety tap chained four Micros)
      react("One " + fam.label + " is the chain's shape here.");
      PATCH.menuFor = null;
      render();
      return;
    }
    contextMoved();
    if (at == null || at > PATCH.chain.length) at = PATCH.chain.length;
    PATCH.chain.splice(at, 0, id);
    var rearranged = normalizeChain();
    PATCH.menuFor = null;
    derive(); render();
    if (rearranged && id === "pico") {
      react("The ladder runs up: Wave Pico took the wire and Wave Nano moved to senior - " +
        "a senior does not hand work down to a reader.");
    } else if (rearranged) {
      react(fam.label + " in the chain - slotted where the ladder runs up: reader first, senior after.");
    } else if (fam.status === "recorded") {
      var info = chainInfo();
      react(fam.label + " in the chain - " + (id === "pico"
        ? "it classifies each read: assert, or escalate at its floor."
        : (info.senior ? "the senior now adjudicates every doubtful read, and the monitor gains a human-readable stage."
                       : "it reads the wire directly, and the monitor gains a human-readable stage.")));
    } else {
      react(fam.label + " (" + fam.size + ") in the chain - but it is " + fam.status +
        ", with no recorded run on this bench. Its stage stays honestly silent.");
    }
  }

  /* =====================================================================
     THE WHY LAYER - the product story, one hover/tap away.
     Surface stays minimal; every NUMBER in a pop is rendered from the
     measured bundle with its citation. Qualitative lines are deployment
     facts (runs-on) or the shim's own doctrine - never invented figures.
     ===================================================================== */
  function whyPop(label, cls) {
    var det = el("details", "sn-why" + (cls ? " " + cls : ""));
    var sum = el("summary", null, null);
    sum.appendChild(el("span", "sn-why__i", "ⓘ"));
    sum.appendChild(el("span", "sn-why__k", label));
    det.appendChild(sum);
    return det;
  }

  // A standing why: expands IN PLACE below the rail (full-width accordion
  // row - never a floating pop inside a scroll container), one open at a
  // time, and the open one survives re-renders via PATCH.whyOpen.
  function whyChip(key, label) {
    var det = whyPop(label);
    det.dataset.why = key;
    if (PATCH.whyOpen === key) det.open = true;
    det.addEventListener("toggle", function () {
      if (det.open) {
        PATCH.whyOpen = key;
        var others = document.querySelectorAll('.sn-whys .sn-why[open]');
        Array.prototype.forEach.call(others, function (o) {
          if (o !== det) o.open = false;
        });
      } else if (PATCH.whyOpen === key) {
        PATCH.whyOpen = null;
      }
    });
    return det;
  }

  // WHY NOT ONE BIG MODEL? The measured sweep drawn as a picture: macro
  // recall vs % of parent-everywhere compute, every point a real config.
  function econChart() {
    var m = PATCH.measured;
    if (!m) return null;
    var cfgs = m.escalation.configs.filter(function (c) {
      return c.pct_of_parent_everywhere != null && c.macro_recall != null;
    });
    var W = 300, H = 150, padL = 34, padB = 26, padT = 12, padR = 12;
    var host = svg("svg", { class: "sn-econ", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "measured sweep: macro recall against percent of parent-everywhere compute, one point per config" });
    var x = function (p) { return padL + p * (W - padL - padR); };
    var y = function (rec) { return H - padB - rec * (H - padB - padT); };
    host.appendChild(svg("line", { class: "sn-econ__ax", x1: padL, y1: H - padB, x2: W - padR, y2: H - padB }));
    host.appendChild(svg("line", { class: "sn-econ__ax", x1: padL, y1: padT, x2: padL, y2: H - padB }));
    var tx = svg("text", { class: "sn-econ__t", x: (padL + W - padR) / 2, y: H - 4, "text-anchor": "middle" });
    tx.textContent = "% of parent-everywhere compute";
    var ty = svg("text", { class: "sn-econ__t", x: 8, y: (padT + H - padB) / 2,
      transform: "rotate(-90 8 " + ((padT + H - padB) / 2) + ")", "text-anchor": "middle" });
    ty.textContent = "macro recall";
    host.appendChild(tx); host.appendChild(ty);
    // the mesh points joined, so the trade reads as a curve
    var mesh = cfgs.filter(function (c) { return /child\+parent@/.test(c.config); });
    if (mesh.length > 1) {
      var d = mesh.map(function (c, i) {
        return (i ? "L" : "M") + x(c.pct_of_parent_everywhere).toFixed(1) + " " + y(c.macro_recall).toFixed(1);
      }).join("");
      host.appendChild(svg("path", { class: "sn-econ__curve", d: d }));
    }
    cfgs.forEach(function (c) {
      var isMesh = /child\+parent@/.test(c.config);
      var cx = x(c.pct_of_parent_everywhere), cy = y(c.macro_recall);
      host.appendChild(svg("circle", { class: "sn-econ__pt" + (isMesh ? " is-mesh" : ""),
        cx: cx.toFixed(1), cy: cy.toFixed(1), r: isMesh ? 3.4 : 2.6 }));
      var flip = cx > W - 70; // keep the rightmost labels inside the frame
      var lab = svg("text", { class: "sn-econ__pl", x: (flip ? cx - 4 : cx + 4).toFixed(1),
        y: (cy - 4).toFixed(1), "text-anchor": flip ? "end" : "start" });
      lab.textContent = c.config.replace("child+parent@", "@").replace("parent-adjudicate-all", "adjudicate-all");
      host.appendChild(lab);
    });
    return host;
  }

  function browserTierQuant() {
    var m = PATCH.measured;
    if (!m || !m.quants) return null;
    for (var i = 0; i < m.quants.length; i++) {
      if (/browser/.test(m.quants[i].role || "")) return m.quants[i];
    }
    return null;
  }

  // The standing questions, parked under the chain rail - ALL the whys live
  // here now (the model-card pops clipped inside the rail's scroller).
  function renderWhys() {
    var host = $("wsWhys");
    if (!host || !PATCH.measured) return;
    host.textContent = "";
    var m = PATCH.measured;

    var tn = whyChip("tasknative", "why task-native?");
    tn.appendChild(el("p", "sn-why__p",
      "A chat model free-sampled on these bytes dreams a Modbus register table. " +
      "A Wave model decodes a LOCKED ENUM with a MARGIN - the margin is the model " +
      "saying how sure it is, and that calibrated doubt is what the escalation " +
      "contract is built on. No prose, no dreaming: one token of meaning, scored."));
    host.appendChild(tn);

    var nano = familyById("nano");
    var sr = whyChip("senior", "why a senior?");
    sr.appendChild(el("p", "sn-why__p",
      "The senior only pays attention when a reader is unsure - that is the whole " +
      "economics of the mesh (open WHY NOT ONE BIG MODEL? for the measured " +
      "sweep). It runs on " + nano.runs + ", so the doubtful reads stay on-prem too."));
    host.appendChild(sr);

    var econ = whyChip("econ", "why not one big model?");
    var chart = econChart();
    if (chart) econ.appendChild(chart);
    var best = m.escalation.configs.filter(function (c) { return c.config === "child+parent@1.5"; })[0];
    if (best) {
      econ.appendChild(el("p", "sn-why__p",
        "The measured trade: at floor 1.5 the mesh reaches " + (best.macro_recall * 100).toFixed(1) +
        " macro recall for " + (best.pct_of_parent_everywhere * 100).toFixed(0) +
        "% of the compute of asking the senior about everything. Escalation buys most of the " +
        "senior's judgment for a fraction of its residency."));
    }
    econ.appendChild(el("p", "sn-why__cite",
      "measured: " + m.escalation.configs.map(function (c) { return c.config; }).join(" · ") +
      " (" + m._provenance.suite + ")"));
    host.appendChild(econ);

    var tiny = whyChip("tiny", "why so small?");
    var ladder = el("span", "sn-ladder");
    ladder.setAttribute("aria-hidden", "true");
    ladder.appendChild(el("span", "wb-plate__ink"));
    tiny.appendChild(ladder);
    tiny.appendChild(el("p", "sn-why__p",
      "The reading happens where the wire is. A chat model needs a datacenter and a " +
      "round-trip; a Wave slot runs on the hardware the plant already owns - " +
      FAMILY.filter(function (f) { return f.status !== "planned"; })
        .map(function (f) { return f.label + " on " + f.runs; }).join(", ") +
      " - so the bytes never leave the fence."));
    var q = browserTierQuant();
    if (q) {
      tiny.appendChild(el("p", "sn-why__p",
        "Small also survives quantization: the " + q.quant + " build is " + q.size_mb +
        "MB at " + (q.fault_id_macro * 100).toFixed(1) + " fault-ID macro - small enough " +
        "for a browser tab (" + q.source + ")."));
    }
    host.appendChild(tiny);

    var person = whyChip("person", "why a person at the end?");
    person.appendChild(el("p", "sn-why__p",
      "The ladder should end with someone accountable - the operator lever is that " +
      "doctrine. UNATTENDED AUTHORITY is the exception you can grant: the senior acts " +
      "with nobody on shift and every decision queues for human review, so the lamp " +
      "reads PROVISIONAL, not ALL CLEAR. Whether a model is big enough to take a " +
      "shift is a POLICY you set, not a measurement - this bench never claims " +
      "measured autonomous performance."));
    host.appendChild(person);
  }

  /* ---- the measured figures live on the knob's tooltip ------------------- */
  function knobTip(floor) {
    var m = PATCH.measured;
    if (!m) return "";
    var c = m.escalation.configs.filter(function (x) {
      return x.config === "child+parent@" + floor.toFixed(1);
    })[0];
    if (!c) return "floor " + floor.toFixed(1);
    return "measured at this floor: " + (c.macro_recall * 100).toFixed(1) + " macro recall · " +
      (c.escalation_rate * 100).toFixed(1) + "% escalate · " +
      (c.pct_of_parent_everywhere * 100).toFixed(0) + "% of parent-everywhere compute, a residency proxy (" +
      m._provenance.suite + ")";
  }

  /* ---- the MARGIN-vs-FLOOR meter: the knob's effect, visible every turn.
     The bar is the RECORDED margin; the tick is the floor, and it slides as
     the knob turns - so a detent that does not flip the verdict still
     visibly moves the threshold against the same recorded bar. ------------- */
  function floorMeter(margin, floor) {
    var W = 230, H = 30, max = Math.max(TOP + 0.6, margin + 0.4);
    var x = function (v) { return 8 + (v / max) * (W - 16); };
    var host = svg("svg", { class: "sn-fm", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "recorded margin " + margin.toFixed(2) + " against floor " + floor.toFixed(1) +
        (margin < floor ? " - below the floor, so it escalates" : " - at or above the floor, so it asserts") });
    host.appendChild(svg("line", { class: "sn-fm__track", x1: 8, y1: 19, x2: W - 8, y2: 19 }));
    // every measured detent is a notch, so the knob's stations are visible
    DETENTS.forEach(function (d) {
      host.appendChild(svg("line", { class: "sn-fm__detent",
        x1: x(d).toFixed(1), y1: 16, x2: x(d).toFixed(1), y2: 22 }));
    });
    var esc = margin < floor;
    host.appendChild(svg("line", { class: "sn-fm__bar" + (esc ? " is-esc" : ""),
      x1: 8, y1: 19, x2: x(margin).toFixed(1), y2: 19 }));
    host.appendChild(svg("circle", { class: "sn-fm__tip" + (esc ? " is-esc" : ""),
      cx: x(margin).toFixed(1), cy: 19, r: 2.6 }));
    host.appendChild(svg("line", { class: "sn-fm__floor",
      x1: x(floor).toFixed(1), y1: 8, x2: x(floor).toFixed(1), y2: 26 }));
    var fl = svg("text", { class: "sn-fm__t", x: x(floor).toFixed(1), y: 6, "text-anchor": "middle" });
    fl.textContent = "floor " + floor.toFixed(1);
    var ml = svg("text", { class: "sn-fm__t sn-fm__t--m",
      x: Math.min(W - 8, Math.max(24, x(margin))).toFixed(1), y: 29, "text-anchor": "middle" });
    ml.textContent = "margin " + margin.toFixed(2);
    host.appendChild(fl); host.appendChild(ml);
    host.setAttribute("title",
      "the floor is the doubt threshold - margins below it escalate; the measured sweep numbers ride the knob");
    return host;
  }

  /* ---- the semantic tint + change flash: colour rides only on verdict
     words that already carry their meaning in text and shape; the flash
     fires exactly when a stage's verdict CONTENT changes (the same
     signature discipline as the v11 power-on gate), and reduced motion
     keeps the steady tint but skips the flash. ---------------------------- */
  function verdictTint(esc, ok, isFault) {
    if (esc) return "sn-live--esc";
    if (ok) return "sn-live--ok";
    return isFault ? "sn-live--bad" : "sn-live--esc"; // false alarm reads amber
  }
  function flashIfChanged(node, key, sig) {
    PATCH._vSig = PATCH._vSig || {};
    if (PATCH._vSig[key] !== undefined && PATCH._vSig[key] !== sig && !REDUCED) {
      node.classList.add("is-flash");
    }
    PATCH._vSig[key] = sig;
  }

  /* =====================================================================
     THE MONITOR - the hero. The output at each stage, top to bottom.
     ===================================================================== */
  /* ---- the real recorded series, drawn live ------------------------------
     Every path below is plotted from wave-windows.json - the raw 96-sample
     series behind the very window the model read, verified by the exporter
     against the window body. The MOTION is chrome (a loop, labelled); the
     SHAPE is recorded fact: a STUCK window genuinely flatlines, a DROPOUT
     genuinely gaps, because the samples are real. */
  function seriesOf(r) {
    return (PATCH.windows && r && PATCH.windows[r.node_id]) || null;
  }

  /* HONEST SCALING, ANCHORED TO THE INSTRUMENT'S OWN HEALTHY WINDOW.
     Autoscaling every window to its own min/max would blow a steady window's
     quantization jitter up to full height - STUCK would look noisy, and
     every condition would look alike (founder v12: "stuck moves more like
     OK"). The display span is therefore anchored to the selected TYPE's OK
     window, in RELATIVE terms (the conditions come from different recorded
     machines, so absolute ranges cannot be shared - but the same SENSITIVITY
     can): span >= the OK window's padded span scaled to this record's own
     magnitude. OK then breathes across its band, STUCK renders near-flat at
     the same zoom, a drift ramps out of band, a rail clips. The samples are
     untouched, and the strip prints the DISPLAYED scale so the scale itself
     is honest. Fallback when a type has no OK record: the v8 floor,
     3% of the signal's own magnitude (every current type has an OK pick). */
  function okAnchorRel() {
    var t = currentType();
    if (!t || t.recIdx.none == null || !PATCH.measured) return null;
    var okS = seriesOf(PATCH.measured.records[t.recIdx.none]);
    if (!okS) return null;
    var lo = Math.min.apply(null, okS.samples), hi = Math.max.apply(null, okS.samples);
    var mean = 0;
    for (var i = 0; i < okS.samples.length; i++) mean += okS.samples[i];
    mean /= okS.samples.length || 1;
    if (!isFinite(mean) || Math.abs(mean) < 1e-9) return null;
    return ((hi - lo) * 1.2) / Math.abs(mean); // the OK span, padded, as a fraction of reading
  }

  function scaleOf(samples, anchorRel) {
    var lo = Math.min.apply(null, samples), hi = Math.max.apply(null, samples);
    var mean = 0;
    for (var i = 0; i < samples.length; i++) mean += samples[i];
    mean /= samples.length || 1;
    var floor = anchorRel
      ? anchorRel * Math.abs(mean)                 // the OK window's sensitivity
      : 0.03 * Math.abs(mean);                     // v8 fallback floor
    var span = Math.max(hi - lo, floor, 1e-9);
    var mid = (hi + lo) / 2;
    return { lo: mid - span / 2, hi: mid + span / 2, span: span,
             dataLo: lo, dataHi: hi, anchored: !!anchorRel };
  }

  function fmtN(x) {
    var a = Math.abs(x);
    return a >= 100 ? x.toFixed(1) : a >= 1 ? x.toFixed(2) : x.toFixed(4);
  }

  function seriesPath(samples, W, H, pad, dup, scale) {
    var sc = scale || scaleOf(samples);
    var n = samples.length, reps = dup ? 2 : 1, d = "";
    for (var k = 0; k < reps; k++) {
      for (var i = 0; i < n; i++) {
        var x = ((k * n + i) / (reps * n - 1)) * W;
        var y = pad + (1 - (samples[i] - sc.lo) / sc.span) * (H - pad * 2);
        d += (k === 0 && i === 0 ? "M" : "L") + x.toFixed(1) + " " + y.toFixed(1);
      }
    }
    return d;
  }

  /* ---- the PHOSPHOR renderer: the same real samples, drawn with light.
     A 2D-canvas oscilloscope beam sweeps the recorded window; a low-alpha
     destination-out wash each frame gives the trail its decay. RENDERING
     math only - the data path is seriesOf() and nothing else, and the
     SVG strip below remains the reduced-motion / no-canvas fallback with
     identical labels. */
  var TRACE = { gen: 0 };
  function drawStripCanvas(wrap, s, sc, unitWord) {
    var cv = document.createElement("canvas");
    var ctx = cv.getContext && cv.getContext("2d");
    if (!ctx) return false;
    cv.className = "sn-strip__cv";
    cv.setAttribute("role", "img");
    cv.setAttribute("aria-label", "the recorded sample series for this record, replayed in a loop");
    wrap.appendChild(cv);
    var gen = ++TRACE.gen;
    var W = 560, H = 96, dpr = Math.min(2, window.devicePixelRatio || 1);
    cv.width = W * dpr; cv.height = H * dpr;
    var samples = s.samples, n = samples.length;
    var SWEEP_MS = 9000; // presentation speed, as labelled - not the recorded rate
    var stroke = null, blur = 2.5, frame = 0, t0 = null, li = 0, roNodes = null;
    function yOf(v) { return 8 + (1 - (v - sc.lo) / sc.span) * (H - 16); }
    function xOf(i) { return (i / (n - 1)) * W; }
    function seg(a, b) {
      ctx.beginPath();
      ctx.moveTo(xOf(a), yOf(samples[Math.round(a)] != null ? samples[Math.round(a)] : samples[0]));
      for (var i = Math.floor(a) + 1; i <= Math.floor(b); i++) ctx.lineTo(xOf(i), yOf(samples[i]));
      ctx.lineTo(xOf(b), yOf(samples[Math.min(n - 1, Math.round(b))]));
      ctx.stroke();
    }
    function step(ts) {
      if (gen !== TRACE.gen || !cv.isConnected) return; // superseded or torn down
      // idle while the mesh view is hidden (mode switch) - keep the ticker
      // alive but do no canvas work; the sweep restarts clean on return.
      // (Backgrounded tabs already stop: the browser suspends rAF itself.)
      if (frame % 15 === 0 && cv.offsetParent === null) {
        t0 = null; li = 0; frame = 0; // stays 0 -> re-check every idle tick
        requestAnimationFrame(step);
        return;
      }
      if (t0 == null) t0 = ts;
      if (frame % 45 === 0) { // theme can flip mid-loop: re-read color + glow
        var cs = getComputedStyle(cv);
        stroke = cs.color;
        blur = parseFloat(cs.getPropertyValue("--beam-blur")) || 2.5;
      }
      frame++;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      // phosphor decay: fade what is already lit instead of clearing
      ctx.globalCompositeOperation = "destination-out";
      ctx.fillStyle = "rgba(0,0,0,0.045)";
      ctx.fillRect(0, 0, W, H);
      ctx.globalCompositeOperation = "source-over";
      ctx.strokeStyle = stroke; ctx.fillStyle = stroke;
      ctx.lineWidth = 1.5; ctx.lineJoin = "round"; ctx.lineCap = "round";
      ctx.shadowColor = stroke; ctx.shadowBlur = blur;
      var head = (((ts - t0) / SWEEP_MS) % 1) * (n - 1);
      if (head < li) { seg(li, n - 1); li = 0; } // wrap: finish the window, restart the sweep
      seg(li, head);
      li = head;
      // the beam head
      ctx.beginPath();
      ctx.arc(xOf(head), yOf(samples[Math.round(head)]), 1.8, 0, Math.PI * 2);
      ctx.fill();
      // the text side moves with the graph: the READOUT is the recorded
      // sample under the beam - real data at presentation cadence
      if (frame % 6 === 0) {
        if (frame % 30 === 0 || !roNodes) roNodes = document.querySelectorAll(".sn-beamro");
        var hi0 = Math.round(head);
        var roTxt = "▸ sample " + (hi0 + 1) + "/" + n + " · " + fmtN(samples[hi0]) +
          (unitWord ? " " + unitWord : "");
        for (var ri = 0; ri < roNodes.length; ri++) roNodes[ri].textContent = roTxt;
      }
      requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
    return true;
  }

  function drawStrip(r) {
    var s = seriesOf(r);
    if (!s) return null;
    var wrap = el("div", "sn-strip");
    var head = el("div", "sn-stage__head");
    head.appendChild(el("b", null, "LIVE TRACE"));
    var rec = el("span", "ws-rec" + (REDUCED ? "" : " is-live"));
    rec.appendChild(el("span", "ws-rec__dot"));
    rec.appendChild(el("span", null, "REPLAY · RECORDED LOOP"));
    rec.title = "the record's real " + s.samples.length + " samples, replayed in a loop - " +
      "the motion is presentation; the shape is recorded fact";
    head.appendChild(rec);
    wrap.appendChild(head);
    var W = 560, H = 96;
    var sc = scaleOf(s.samples, okAnchorRel());
    // the phosphor beam when motion is welcome and canvas exists; the SVG
    // strip is the reduced-motion / no-canvas fallback
    if (!REDUCED) {
      var ro = el("span", "sn-beamro");
      ro.setAttribute("aria-hidden", "true"); // the legend carries the accessible numbers
      head.appendChild(ro);
    }
    var painted = !REDUCED && drawStripCanvas(wrap, s, sc, unitWordOf(r));
    if (!painted) {
      var host = svg("svg", { class: "sn-strip__svg", viewBox: "0 0 " + W + " " + H,
        preserveAspectRatio: "none", role: "img",
        "aria-label": "the recorded sample series for this record, replayed in a loop" });
      if (REDUCED) {
        // no motion: the full recorded window, static
        host.appendChild(svg("path", { class: "sn-strip__line",
          d: seriesPath(s.samples, W, H, 8, false, sc) }));
      } else {
        var g = svg("g", { class: "sn-strip__scroll" });
        g.appendChild(svg("path", { class: "sn-strip__line",
          d: seriesPath(s.samples, W * 2, H, 8, true, sc) }));
        host.appendChild(g);
      }
      wrap.appendChild(host);
    }
    var w = r.window || {};
    var legend = el("p", "sn-sub sn-strip__legend",
      "display scale " + fmtN(sc.lo) + " … " + fmtN(sc.hi) +
      (unitWordOf(r) ? " " + unitWordOf(r) : "") +
      (sc.anchored ? " · matched to this sensor's OK window" : "") +
      " · data " + fmtN(sc.dataLo) + " … " + fmtN(sc.dataHi));
    legend.title = "every condition of this sensor draws at the SAME sensitivity - the scale is " +
      "anchored to its OK window, so a steady window looks steady and a wild one looks wild. " +
      "The samples are untouched, and this line prints the scale actually drawn";
    wrap.appendChild(legend);
    wrap.appendChild(el("p", "sn-sub",
      (w.tag || r.node_id) + " · " + s.samples.length + " recorded samples · period " +
      s.period_s + "s · replay speed is presentation, not the recorded rate"));
    return wrap;
  }

  // A pad's preview: the tiny REAL series of the record that pad replays.
  function sparkline(r) {
    var s = seriesOf(r);
    if (!s) return null;
    var host = svg("svg", { class: "sn-spark", viewBox: "0 0 40 14", "aria-hidden": "true" });
    host.appendChild(svg("path", { class: "sn-spark__line",
      d: seriesPath(s.samples, 40, 14, 2, false, scaleOf(s.samples, okAnchorRel())) }));
    return host;
  }

  /* ---- the channel buttons: which stage the screen shows ------------------ */
  function renderTabs() {
    var host = $("wsTabs");
    if (!host) return;
    host.textContent = "";
    host.setAttribute("role", "tablist");
    host.setAttribute("aria-label", "Which stage's output the monitor shows");
    var sts = PATCH.verdict ? PATCH.verdict.stages : [];
    var tabs = [{ id: "all", label: "ALL" }], seen = {};
    sts.forEach(function (st) {
      var id = st.kind === "raw" ? "raw"
        : st.kind === "pico" ? "pico"
        : (st.kind === "nano" || st.kind === "quietSenior") ? "nano" : null;
      if (!id || seen[id]) return;
      seen[id] = true;
      tabs.push({ id: id, label: id === "raw" ? "RAW WIRE" : id === "pico" ? "WAVE PICO" : "WAVE NANO" });
    });
    if (!tabs.some(function (t) { return t.id === PATCH.tab; })) PATCH.tab = "all";
    tabs.forEach(function (t) {
      var b = el("button", "sn-tab" + (PATCH.tab === t.id ? " is-on" : ""));
      b.type = "button";
      b.setAttribute("role", "tab");
      b.setAttribute("aria-selected", PATCH.tab === t.id ? "true" : "false");
      b.textContent = t.label;
      b.title = t.id === "all" ? "the whole cascade, every stage in order"
        : "show only this stage's output, large";
      b.addEventListener("click", function () {
        PATCH.tab = t.id;
        paintMonitor();
        renderTabs();
      });
      host.appendChild(b);
    });
  }

  function paintMonitor() {
    var host = $("wpMonitor");
    if (!host) return;
    host.textContent = "";
    // the power-on sweep fires only when what the glass SHOWS changes -
    // record, tab, chain or reply - not on every repaint (v11: operator
    // toggles were strobing the screen). Chrome, reduced-motion-guarded.
    var r0 = currentRecord();
    var monSig = [r0 && r0.node_id, PATCH.cond, PATCH.tab,
                  PATCH.chain.join(","), PATCH.reply ? PATCH.reply.kind : ""].join("|");
    if (monSig !== PATCH._monSig) {
      PATCH._monSig = monSig;
      host.classList.remove("is-flip");
      void host.offsetWidth;
      host.classList.add("is-flip");
    }

    var v = PATCH.verdict;
    var sts = v ? v.stages : stages();
    var r = currentRecord();

    if (r && (PATCH.tab === "all" || PATCH.tab === "raw")) {
      var strip = drawStrip(r);
      if (strip) host.appendChild(strip);
    }

    sts.forEach(function (st) {
      if (PATCH.tab !== "all") {
        if (PATCH.tab === "raw" && st.kind !== "raw") return;
        if (PATCH.tab === "pico" && st.kind !== "pico") return;
        if (PATCH.tab === "nano" && st.kind !== "nano" && st.kind !== "quietSenior") return;
      }
      var solo = PATCH.tab !== "all";
      var box = el("section", "sn-stage sn-stage--" + st.kind + (solo ? " sn-stage--solo" : ""));
      var head = el("div", "sn-stage__head");
      head.appendChild(el("b", null, st.who));
      if (st.kind === "raw") {
        var rec = el("span", "ws-rec");
        rec.appendChild(el("span", "ws-rec__dot"));
        rec.appendChild(el("span", null, "MONITORING · REPLAY"));
        rec.title = "the recorded window, replayed - nothing here is live";
        head.appendChild(rec);
        if (!REDUCED) {
          var echo = el("span", "sn-beamro sn-beamro--echo");
          echo.setAttribute("aria-hidden", "true");
          echo.title = "the recorded sample under the beam, as it sweeps";
          head.appendChild(echo);
        }
      }
      box.appendChild(head);

      if (st.kind === "raw") {
        var log = el("pre", "ws-log", st.body);
        log.title = "byte-for-byte, the window the model read in the recorded run";
        box.appendChild(log);
      } else if (st.kind === "pico") {
        var isFaultP = st.r.truth !== "none";
        var line = el("p", "sn-proto");
        // the verdict word carries a semantic tint (word + shape still carry
        // the meaning without colour) and flashes when it CHANGES
        var vb = el("b", "sn-vword " + (st.esc ? "sn-proto__esc sn-live--esc" : verdictTint(false, st.ok, isFaultP)),
          st.esc ? "ESCALATE ↑" : "ASSERT " + '" ' + st.said + '"');
        flashIfChanged(vb, "pico", (st.esc ? "esc" : "as|" + st.said) + "|" + st.margin);
        line.appendChild(vb);
        // margin vs floor, ALWAYS both numbers - the knob's effect is
        // visible on every detent, not only when the verdict flips
        line.appendChild(el("span", null, st.esc
          ? " · margin " + st.margin.toFixed(2) + " < floor " + st.floor.toFixed(1) + " - too doubtful to assert"
          : " · margin " + st.margin.toFixed(2) + " ≥ floor " + st.floor.toFixed(1) + " - sure enough to assert"));
        box.appendChild(line);
        box.appendChild(floorMeter(st.margin, st.floor));
        box.appendChild(el("p", "sn-sub", st.esc
          ? "the wire protocol line - the Pico hands this read up. The margin is the model " +
            "saying 'I am not sure' - that honesty is the feature."
          : "the wire protocol line - machine-facing, one token of meaning"));
      } else if (st.kind === "nano") {
        var vw = el("p", "sn-verdict");
        var nb = el("b", "sn-vword " + verdictTint(false, st.ok, st.isFault),
          st.verdict.toUpperCase());
        flashIfChanged(nb, "nano", st.verdict + "|" + (st.ok ? "ok" : "bad"));
        vw.appendChild(nb);
        if (st.gloss) {
          var g = el("span", "sn-gloss");
          g.appendChild(el("span", null, " - " + st.gloss + " "));
          g.appendChild(el("i", "sn-gloss__k", "glossary"));
          vw.appendChild(g);
        }
        box.appendChild(vw);
        box.appendChild(el("p", "sn-para", st.para));
      } else if (st.kind === "quietSenior") {
        box.appendChild(el("p", "sn-sub", st.note));
      } else if (st.kind === "deadend") {
        var dn = el("p", "sn-para sn-para--warn sn-live--esc", st.note);
        flashIfChanged(dn, "deadend", "deadend");
        box.appendChild(dn);
      } else if (st.kind === "silent") {
        box.appendChild(el("p", "sn-sub",
          st.fam.blurb + " - no recorded transcript, output unchanged."));
      }
      host.appendChild(box);
    });

    // a stage that left the cascade forgets its signature, so its RETURN
    // flashes too (an escalate that comes back is news)
    if (PATCH._vSig) {
      var present = {};
      sts.forEach(function (st) { present[st.kind] = true; });
      Object.keys(PATCH._vSig).forEach(function (k) {
        if (!present[k]) delete PATCH._vSig[k];
      });
    }

    if (PATCH.reply) host.appendChild(drawReply(PATCH.reply));

    // the glass tells you it scrolls: a sticky fade hugs the bottom edge
    // whenever the cascade runs past it (v8 screenshots clipped the Nano
    // paragraph invisibly)
    var fade = el("div", "sn-mon__fade");
    fade.setAttribute("aria-hidden", "true");
    host.appendChild(fade);

    var lampBig = $("wpLamp2"), why = $("wpWhy");
    if (lampBig && v) {
      lampBig.dataset.state = v.state;
      lampBig.title = "derived from recorded records only: red = a miss a higher floor would have " +
        "caught, yellow = an incomplete chain, green = complete (AT CEILING when the recorded " +
        "senior itself missed)";
      var f2 = LAMP_FACE[v.state] || LAMP_FACE.off;
      lampBig.textContent = "";
      lampBig.appendChild(el("span", "wp-lampwin__sym", v.sym || f2.sym));
      lampBig.appendChild(el("span", "wp-lampwin__label", v.label || f2.label));
    }
    if (why && v) why.textContent = v.why;

    var certHost = $("wpCertHost");
    var certWasOpen = false;
    if (certHost) {
      var prevCert = certHost.querySelector(".sn-cert");
      certWasOpen = !!(prevCert && prevCert.open);
      certHost.textContent = "";
    }
    if (r && certHost) {
      var det = el("details", "sn-cert");
      det.open = certWasOpen; // a pad press must not slam it shut (v11)
      var sum = el("summary", null, "record certificate");
      det.appendChild(sum);
      var w = r.window || {};
      var dl = el("dl", "wp-cert");
      [["record", r.node_id + " (scene " + r.scene_id + ")"],
       ["unit", unitWordOf(r) || "not stated in the wire"],
       ["window", w.lo != null ? w.lo + " … " + w.hi + " · mean " + w.mean : "—"],
       ["truth", r.truth],
       ["child said", r.child.prediction + " · margin " + r.child.margin.toFixed(2)],
       ["senior said", r.parent.prediction + " · margin " + r.parent.margin.toFixed(2)],
       ["digest", null],
      ].forEach(function (row) {
        dl.appendChild(el("dt", null, row[0]));
        if (row[1] == null) dl.appendChild(el("dd", "wp-cert__pending", "— pending export —"));
        else dl.appendChild(el("dd", null, row[1]));
      });
      det.appendChild(dl);
      det.appendChild(el("p", "wp-note",
        "The raw stage above is byte-for-byte the window the model read in the recorded " +
        "run; the live trace is its real sample series from the committed windows bundle. " +
        "Every stage prints recorded fields; the margins are recorded logprob " +
        "differences - nothing in this browser computes one."));
      // the TRUST chip: the deck's one public mistake, worn as provenance.
      var ret = PATCH.measured && PATCH.measured._provenance && PATCH.measured._provenance.retracted;
      if (ret) {
        var trust = whyPop("why trust these numbers?", "sn-why--trust");
        trust.appendChild(el("p", "sn-why__p",
          "This deck once published a wrong one: “" + ret.claim + "” - " + ret.status +
          ". The impossible part (two quantizations returning identical aggregates) is what " +
          "caught it, and the exporter now REFUSES that signature outright. Every figure here " +
          "carries its run and suite so you can re-check us the same way."));
        det.appendChild(trust);
      }
      if (PATCH.scene && r.scene_id === PATCH.scene.scene_id) drawScopeInto(det);
      certHost.appendChild(det);
    }
  }

  var LAMP_FACE = {
    off:    { sym: "·", label: "STANDING BY" },
    green:  { sym: "●", label: "ALL CLEAR" },
    yellow: { sym: "△", label: "DEGRADED" },
    red:    { sym: "⊗", label: "FAULTS MISSED" },
  };

  /* ---- the operator lever, by the monitor's foot -------------------------- */
  function renderOp() {
    var host = $("wbOp");
    if (!host) return;
    host.textContent = "";
    var sw = el("button", "wb-lever wb-lever--op" + (PATCH.operator ? " is-on" : ""));
    sw.type = "button";
    sw.setAttribute("role", "switch");
    sw.setAttribute("aria-checked", PATCH.operator ? "true" : "false");
    sw.setAttribute("aria-label", "Operator on shift");
    sw.appendChild(el("span", "wb-lever__k", PATCH.operator ? "OPERATOR ON SHIFT" : "OPERATOR OFF SHIFT"));
    sw.appendChild(el("span", "wb-lever__pip"));
    sw.addEventListener("click", function () {
      PATCH.operator = !PATCH.operator;
      derive(); render();
      react(PATCH.operator
        ? "The operator is on shift, reading the rollups. The ladder ends with a person."
        : "The operator went off shift.");
    });
    host.appendChild(sw);

    // the policy lever: UNATTENDED AUTHORITY. A policy the visitor sets -
    // never a capability claim - and the lamp answers with PROVISIONAL,
    // not ALL CLEAR, when it is exercised.
    var au = el("button", "wb-lever wb-lever--auth" + (PATCH.authority ? " is-on" : ""));
    au.type = "button";
    au.setAttribute("role", "switch");
    au.setAttribute("aria-checked", PATCH.authority ? "true" : "false");
    au.setAttribute("aria-label", "Unattended authority - the senior may act with no operator on shift");
    au.title = "let the senior act with no operator on shift - decisions queue for human review. " +
      "Whether a model is big enough to take a shift is a POLICY you set, not a measurement.";
    au.appendChild(el("span", "wb-lever__k", "UNATTENDED AUTHORITY"));
    au.appendChild(el("span", "wb-lever__pip"));
    au.addEventListener("click", function () {
      PATCH.authority = !PATCH.authority;
      derive(); render();
      react(PATCH.authority
        ? "Unattended authority granted - with a senior aboard and nobody on shift, decisions queue for human review."
        : "Unattended authority revoked - with nobody on shift the chain reads DEGRADED again.");
    });
    host.appendChild(au);
  }

  /* ---- the recorded pump trace, where it belongs ------------------------- */
  function drawScopeInto(box) {
    var sc = PATCH.scene;
    if (!sc || !sc.steps || !sc.steps.length) return;
    var W = 520, H = 96;
    var host = svg("svg", { class: "wp-scope", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "The recorded channel trace, with the fault onset marked" });
    var vals = sc.steps.map(function (st) { return st.v; });
    var lo = Math.min.apply(null, vals), hi = Math.max.apply(null, vals);
    var span = (hi - lo) || 1;
    var d = vals.map(function (v, i) {
      var x = (i / (vals.length - 1)) * (W - 8) + 4;
      var y = H - 8 - ((v - lo) / span) * (H - 16);
      return (i ? "L" : "M") + x.toFixed(1) + " " + y.toFixed(1);
    }).join(" ");
    host.appendChild(svg("path", { class: "wp-scope__line", d: d }));
    host.appendChild(svg("line", { class: "wp-scope__onset", x1: (0.4 * W).toFixed(1), x2: (0.4 * W).toFixed(1), y1: 4, y2: H - 4 }));
    box.appendChild(host);
    box.appendChild(el("p", "wp-note",
      "The committed seed-42 pump scene: " + sc.steps.length + " recorded steps, fault onset at the dashed line. REPLAY, never live."));
  }

  function shortName(p) { return String(p || "").split("/").pop(); }

  /* ---- the TV's parallax: bezel and glass on separate depths -------------
     Pure chrome. No listeners are even attached under reduced motion. */
  function wireTilt() {
    if (REDUCED) return;
    var tv = document.querySelector(".sn-tv");
    if (!tv) return;
    tv.addEventListener("pointermove", function (e) {
      var b = tv.getBoundingClientRect();
      if (!b.width || !b.height) return;
      var dx = (e.clientX - b.left) / b.width - 0.5;
      var dy = (e.clientY - b.top) / b.height - 0.5;
      tv.style.setProperty("--tiltx", (dy * -1.5).toFixed(2) + "deg");
      tv.style.setProperty("--tilty", (dx * 1.5).toFixed(2) + "deg");
    });
    tv.addEventListener("pointerleave", function () {
      tv.style.setProperty("--tiltx", "0deg");
      tv.style.setProperty("--tilty", "0deg");
    });
  }

  /* ---- the a11y mirror: the same bench as a list ------------------------- */
  function renderMirror() {
    var m = $("wpMirror");
    if (!m) return;
    m.textContent = "";
    var t = currentType();
    var r = currentRecord();
    m.appendChild(el("li", null, "Sensor: " +
      (t ? t.label + ", condition " + (CONDW[PATCH.cond] || PATCH.cond) +
        (r ? ", replaying " + ((r.window && r.window.tag) || r.node_id) : "") : "none")));
    if (!PATCH.chain.length) {
      m.appendChild(el("li", null, "Chain: empty - the sensor writes raw wire"));
    } else {
      PATCH.chain.forEach(function (id, i) {
        var fam = familyById(id);
        m.appendChild(el("li", null, "Chain " + (i + 1) + ": " + fam.label +
          (id === "pico" ? " at floor " + PATCH.floor.toFixed(1) : "") +
          (fam.status === "recorded" ? "" : " (no recorded run - silent)")));
      });
    }
    m.appendChild(el("li", null, "Operator: " + (PATCH.operator ? "on shift" : "off shift")));
  }

  /* =====================================================================
     INTAKE - THE TRANSLATION SHIM (handoff 3)

     A task-native base model given "hi" dreams a Modbus register table -
     free-sampling a Wave child produces corpus dreams, not conversation,
     BY DESIGN. So every human input is classified and wrapped:

       wire blob    -> pass through verbatim: this is the model's native food
       raw numbers  -> a channel dict; the UNIT IS NOT DEFAULTED - a defaulted
                       unit is an invented fact, and units are precisely what
                       the OPC UA substitute-value trap is about
       scenario     -> mapped to a template, never sent as words to a model
       small talk   -> answered by the interface from the faceplate; it
                       never reaches a model

     The classifier SHOWS ITS EVIDENCE per row, and refuses to guess when
     the top two candidates are within one fingerprint of each other - a
     shim that asks is a better demo than a shim that is always right.

     What a paste earns is the REQUEST ENVELOPE: the exact request that
     would go to a stock llama-server (the R.45/R.55 enum+margin protocol),
     marked DRAFT · NOT RUN. Never a result - a margin is a logprob
     difference, and nothing in a browser can compute one.
     ===================================================================== */
  var INTAKE = { text: "", verdict: null };

  function labelOf(name) {
    var EXACT = { roggentoo: "RogGentoo" };
    var ACR = { agv: "AGV", cnc: "CNC", vfd: "VFD", hpu: "HPU" };
    if (EXACT[name]) return EXACT[name];
    return String(name).split("_").map(function (w) {
      return ACR[w] || w.charAt(0).toUpperCase() + w.slice(1);
    }).join(" ");
  }

  // Fingerprints are derived from the committed renderer outputs, so detection
  // can never drift from what the wire bench actually displays.
  var FINGERPRINTS = [
    { mod: "modbus",     probes: ["MODBUS holding", "function 03", "scale factor"] },
    { mod: "opcua",      probes: ["ns=2;s=", "MonitoredItem", "status=Good"] },
    { mod: "prometheus", probes: ["# TYPE", "# HELP", "gauge"] },
    { mod: "sparkplug",  probes: ["spBv1.0/", "\"metrics\"", "DDATA"] },
    { mod: "syslog",     probes: ["<134>1 ", "RFC5424", "ioscan"] },
    { mod: "influx",     probes: ["line protocol", "INFLUXDB"] },
    { mod: "datadog",    probes: ["\"series\"", "\"metric\""] },
    { mod: "signal",     probes: ["channel=", "range=[", "slope_per_min"] },
  ];

  // Body-shape probes: real plant bytes do not carry our renderer's decorative
  // headers, so each modality is also recognisable by the SHAPE of its lines -
  // an Influx line-protocol row, an OPC UA value/status pair, a Modbus register
  // row. A header probe and a shape probe each count one hit.
  var SHAPES = {
    modbus:     /\b4[0-9]{4}\b\s+0x[0-9A-Fa-f]{4}/,
    opcua:      /value=\S+\s+status=\w+/,
    prometheus: /^[a-z_][a-z0-9_]*(\{[^}]*\})? [0-9.eE+-]+$/m,
    sparkplug:  /"(alias|timestamp)"\s*:\s*\d{10,}/,
    syslog:     /^<\d{2,3}>1 \d{4}-\d{2}-\d{2}T/m,
    influx:     /^\w[\w-]*,\S+=\S+ \S+=\S+ \d{15,19}$/m,
    datadog:    /"points"|"resources"\s*:/,
    signal:     /\b(longest_run|sd_tail|repeat_frac)=/,
  };

  function classify(text) {
    var t = text.trim();
    if (!t) return null;

    // wire blob: header fingerprints plus line-shape probes per modality
    var scores = FINGERPRINTS.map(function (f) {
      var hits = f.probes.filter(function (pr) { return t.indexOf(pr) >= 0; });
      var ev = hits.slice();
      if (SHAPES[f.mod] && SHAPES[f.mod].test(t)) {
        ev.push("line shape");
        hits = ev;
      }
      return { mod: f.mod, hits: ev.length, evidence: ev };
    }).sort(function (a, b) { return b.hits - a.hits; });
    var best = scores[0], second = scores[1];

    if (best.hits >= 2) {
      // thin evidence: if the runner-up is within one hit, the shim asks.
      if (second && second.hits >= best.hits - 1 && second.hits >= 2) {
        return { kind: "ambiguous", a: best, b: second };
      }
      var tag = (t.match(/[A-Z][A-Z0-9]*_[A-Z0-9_]+/) || [])[0] || null;
      var recognised = null;
      if (PATCH.scene && PATCH.scene.renders) {
        var norm = t.replace(/\s+/g, " ");
        for (var mo in PATCH.scene.renders) {
          if (PATCH.scene.renders[mo].replace(/\s+/g, " ") === norm) { recognised = mo; break; }
        }
      }
      return { kind: "blob", mod: best.mod, evidence: best.evidence, tag: tag, recognised: recognised };
    }

    // raw numbers: mostly numeric tokens. A REPEATED trailing unit token is
    // stripped before the ratio - "71.2 mm/s, 71.3 mm/s" must not be punished
    // for volunteering the unit this deck moralises about.
    var tokens = t.split(/[\s,;]+/).filter(Boolean);
    var unitGuess = null;
    var nonNum = tokens.filter(function (x) { return !/^-?\d+(\.\d+)?$/.test(x); });
    if (nonNum.length >= 2 && nonNum.every(function (x) { return x === nonNum[0]; })) {
      unitGuess = nonNum[0];
      tokens = tokens.filter(function (x) { return x !== unitGuess; });
    }
    var nums = tokens.filter(function (x) { return /^-?\d+(\.\d+)?$/.test(x); });
    if (nums.length >= 8 && nums.length / tokens.length >= 0.6) {
      return { kind: "numbers", n: nums.length, unit: unitGuess,
               samples: nums.slice(0, 96).map(Number) };
    }
    if (nums.length >= 3 && nums.length / Math.max(1, tokens.length) >= 0.6) {
      return { kind: "few-numbers", n: nums.length };
    }

    // a READING QUESTION: "what does the temperature read now?" - detected
    // by documented stems (a question word + a reading word), never NLU. It
    // is answered by the bench from the recorded window - see benchReading.
    var QWORD = /(\bwhat\b|\bhow (much|high|low|hot|is)\b|right now|\blatest\b|\?)/i;
    var RWORD = /(\bread(ing|s)?\b|\bvalue\b|\bmean\b|\bmeasur|temperature|\btemp\b|pressure|vibration|current draw|\bamps?\b|\blevel\b|\bnow\b)/i;
    if (QWORD.test(t) && RWORD.test(t)) {
      return { kind: "question", text: t };
    }

    // scenario in words: a catalogue token lookup, not NLU. A partial asset
    // name ("pump" for centrifugal_pump) counts only when a FAULT word rides
    // along - "cavitating pump" is a scenario, a lone "pump" in chat is not.
    // Stems, not words: "cavitating" and "cavitation" share "cavitat".
    var FAULT_STEMS = ["cavitat", "misalign", "imbalanc", "bearing", "stuck",
      "drift", "leak", "wear", "running dry", "overheat", "vibrat", "fault",
      "fail", "broke", "trip", "alarm", "seiz"];
    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var low = t.toLowerCase();
    var faulty = FAULT_STEMS.filter(function (s) { return low.indexOf(s) >= 0; });
    var bestAsset = null, bestWords = [];
    for (var name in cat) {
      var nameWords = name.split("_").filter(function (w) { return w.length > 3; });
      var hit = nameWords.filter(function (w) { return low.indexOf(w) >= 0; });
      if (!hit.length) continue;
      var full = hit.length === nameWords.length;
      if ((full || faulty.length) && hit.length > bestWords.length) {
        bestAsset = name; bestWords = hit;
      }
    }
    if (bestAsset) {
      return { kind: "scenario-asset", asset: bestAsset, evidence: bestWords.concat(faulty) };
    }

    // Machine-shaped but unrecognised is NOT conversation: an engineer who
    // pasted real telemetry we failed to read must never be lectured about
    // corpus dreams. Heuristic: several similar-length lines, digit-heavy.
    var lines = t.split("\n").filter(function (l) { return l.trim(); });
    var digits = (t.match(/[0-9]/g) || []).length;
    if (lines.length >= 3 && digits / t.length > 0.15) {
      return { kind: "machine-unknown", lines: lines.length };
    }

    // anything else is conversation, and it never reaches a model
    return { kind: "talk" };
  }

  /* =====================================================================
     THE PROMPT - a terminal line wired to the LAST model in the chain.
     Typing feeds the TRANSLATION SHIM: every input is classified, and the
     honest ceiling is stated on the line itself - drafts only, no model
     runs in a browser. What a wire-blob or a number series earns is the
     DRAFT request envelope; small talk is answered by the interface, from
     the faceplate - it never reaches a model.
     ===================================================================== */
  function lastModel() {
    for (var i = PATCH.chain.length - 1; i >= 0; i--) {
      var f = familyById(PATCH.chain[i]);
      if (f) return f;
    }
    return null;
  }

  /* THE LIVE SEAM - the deck's first live surface, and the path to the rest.
     TODAY: chat and reading questions go to PING - the broker's concierge, a
     REAL model answering over the Tower relay (the same POST /concierge the
     CONSOLE deck's pingSend makes). Ping is the concierge, NOT a Wave model,
     and its card says so; the message hands it the recorded bench context
     (the [WAVE MESH BENCH] prefix the broker persona recognises) and the
     reply is displayed VERBATIM, declines included - a real reply is a real
     reply. The bench's recorded reading always rides beneath a question's
     answer, so the visitor gets the numbers whatever Ping says.
     NEXT (when the hosted wave band lands - asked in
     QUESTION-FOR-MODELS-AGENT-mesh-live-prompt.md):
       - enums (fault calls): the R.45/R.55 one-request-per-candidate grammar
         protocol via the relay, margins from the shim's logprob sums;
       - readings: a single request to the wave band, replacing Ping as the
         voice - the answerer-chain shape below is that seam.
     PROTOCOL kinds (wire blobs, number series) never go to chat: they earn
     the DRAFT envelope, and no model runs in a browser. */
  var PING_URL = "https://broker.rogerai.fm/concierge"; // the ONE cross-origin call this deck makes

  function benchContext() {
    var r = currentRecord();
    if (!r) return "no sensor selected.";
    var w = r.window || {};
    var bits = [];
    stages().forEach(function (st) {
      if (st.kind === "pico") {
        bits.push("Wave Pico " + (st.esc
          ? "escalated (margin " + st.margin.toFixed(2) + " below floor " + st.floor.toFixed(1) + ")"
          : 'asserted " ' + st.said + '" (margin ' + st.margin.toFixed(2) + " at floor " + st.floor.toFixed(1) + ")"));
      }
      if (st.kind === "nano") bits.push('Wave Nano said " ' + st.verdict + '" (margin ' + st.r.parent.margin.toFixed(2) + ")");
      if (st.kind === "deadend") bits.push("the escalation went unheard (no senior in the chain)");
    });
    return "recorded window: tag=" + (w.tag || r.node_id) +
      ", unit=" + (unitWordOf(r) || "not stated") + ", n=" + w.n +
      ", range=" + w.lo + "\u2013" + w.hi + ", mean=" + w.mean +
      ", trend=" + w.slope_per_min + "/min. chain: " +
      (bits.join("; ") || "no model chained") + ".";
  }

  function liveAnswerer(v, ctx, text) {
    // protocol kinds never go to chat - they earn the DRAFT envelope instead
    if (!v || (v.kind !== "talk" && v.kind !== "question")) return null;
    var msg = "[WAVE MESH BENCH] " + benchContext() + " visitor asks: " + text;
    return fetch(PING_URL, {
      method: "POST", headers: { "Content-Type": "application/json" },
      credentials: "omit", cache: "no-store",
      body: JSON.stringify({ messages: [{ role: "user", content: msg }] }),
    }).then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
      .then(function (d) {
        var rep = d && d.reply ? String(d.reply) : "";
        if (!rep) throw 0;
        return rep;
      });
  }

  function liveCtx() {
    return { record: currentRecord(), chain: PATCH.chain.slice(), floor: PATCH.floor };
  }

  // The bench's answer to a reading question: a template over the recorded
  // window of the live selection. Signed THE BENCH - a model never said this.
  function benchReading() {
    var r = currentRecord();
    if (!r) return { kind: "note", wired: "the bench", text: "No sensor is selected." };
    var w = r.window || {};
    var u = unitWordOf(r);
    var chainLine;
    var fam = lastModel();
    var hasReader = PATCH.chain.some(function (id) {
      var f = familyById(id); return f && f.status === "recorded";
    });
    if (!hasReader) {
      chainLine = "Nobody is watching this channel - chain Wave Pico or Wave Nano to have it read.";
    } else {
      var sts = stages();
      var nano = null, pico = null;
      sts.forEach(function (st) { if (st.kind === "nano") nano = st; if (st.kind === "pico") pico = st; });
      var isFault = r.truth !== "none";
      if (nano) {
        chainLine = 'Dialed condition: ' + (CONDW[PATCH.cond] || PATCH.cond.toUpperCase()) +
          ' - the chain says " ' + nano.verdict + '" (margin ' + r.parent.margin.toFixed(2) + ").";
      } else if (pico) {
        chainLine = 'Dialed condition: ' + (CONDW[PATCH.cond] || PATCH.cond.toUpperCase()) +
          (pico.esc ? " - the Pico doubts this window (margin " + pico.margin.toFixed(2) + ")."
                    : ' - the chain says " ' + pico.said + '" (margin ' + pico.margin.toFixed(2) + ").");
      } else {
        chainLine = "";
      }
    }
    return {
      kind: "reading",
      who: "THE BENCH · from the recorded window",
      offAir: "a recount of the recorded window - a hosted Wave Nano will take this over; not yet on air",
      tag: w.tag || r.node_id,
      meanLine: "reads mean " + fmtN(w.mean) + (u ? " " + u : "") + " over the recorded window",
      detail: w.n + " samples · " + fmtN(w.lo) + " … " + fmtN(w.hi) + (u ? " " + u : "") +
        " · trend " + w.slope_per_min + "/min",
      chainLine: chainLine,
    };
  }

  function buildReply(text, v) {
    if (!v) v = classify(text);
    if (v.kind === "question") return benchReading();
    var target = lastModel();
    var wired = target ? target.label : "the chain";
    if (v.kind === "blob" || v.kind === "numbers") {
      var src = v.kind === "blob"
        ? { modality: v.mod, recognised: v.recognised || null,
            channels: [{ name: v.tag || "your channel", unit: "" }], body: text }
        : { modality: "raw numbers", recognised: null, unit: v.unit || null,
            channels: [{ name: "your channel", unit: v.unit || "" }], body: text };
      return { kind: "draft", wired: wired, v: v,
               unitNote: v.kind === "numbers" && !v.unit
                 ? "unit NOT STATED IN THE WIRE - a defaulted unit would be an invented fact"
                 : (v.unit ? "unit stated: " + v.unit + " (you stated it - it was not in the wire)" : null),
               recognised: v.recognised || null,
               envelope: envelopeFor(src) };
    }
    if (v.kind === "talk") {
      // the fallback voice when Ping is off air. Chat goes to Ping - a live
      // concierge, labelled as such; it never reaches a Wave model, whose
      // free-sampled answer would be a corpus dream, not a reply.
      return { kind: "talk", wired: wired,
               text: "Answered by this interface, from the faceplate - conversation never " +
                 "reaches a Wave model. Free-sampled, this input would produce a corpus " +
                 "dream, not an answer. Paste what your plant emits, or press a sample chip." };
    }
    if (v.kind === "scenario-asset") {
      return { kind: "scenario", wired: wired,
               text: "That reads like a scenario about " + labelOf(v.asset) + " (matched: " +
                 v.evidence.join(", ") + "). Words are never sent to a Wave model - this bench " +
                 "replays the recorded fleet; describing your own scene returns when the scene " +
                 "exporter grows." };
    }
    if (v.kind === "ambiguous") {
      return { kind: "note", wired: wired,
               text: "Unclear: " + v.a.mod + " (" + v.a.hits + " marks) vs " + v.b.mod + " (" +
                 v.b.hits + ") - the shim refuses to guess on thin evidence. That is the real " +
                 "system's behaviour too. Paste more of the payload." };
    }
    if (v.kind === "few-numbers") {
      return { kind: "note", wired: wired,
               text: "Looks numeric - " + v.n + " sample" + (v.n === 1 ? "" : "s") + ". A window " +
                 "needs at least 8 samples to say anything about a signal; paste more of the series." };
    }
    return { kind: "note", wired: wired,
             text: "Machine-shaped, but not a dialect the shim recognises - it reads eight " +
               "dialects and their line shapes, and this matched none well enough to wrap " +
               "honestly. Try including the header lines of the dump." };
  }

  var REPLY_SEQ = 0;
  function promptSend(text) {
    text = String(text || "").trim();
    if (!text) return null;
    var v = classify(text);
    var bench = buildReply(text, v); // the recorded recount, always built
    // the answerer chain: live first (Ping today, a wave band next), the
    // bench beneath or behind it
    var live = liveAnswerer(v, liveCtx(), text);
    if (live) {
      var token = ++REPLY_SEQ;
      PATCH.reply = { kind: "pingwait", token: token, bench: bench,
                      question: v.kind === "question" };
      live.then(function (rep) {
        // context may have moved while Ping typed - a stale card never lands
        if (!PATCH.reply || PATCH.reply.token !== token) return;
        PATCH.reply = { kind: "ping", token: token, text: rep, bench: bench,
                        question: v.kind === "question" };
        paintMonitor();
        react("Ping answered - live over the Tower relay.");
      }).catch(function () {
        if (!PATCH.reply || PATCH.reply.token !== token) return;
        bench.offAirNote = "Ping is off air - the bench answered from the recorded window.";
        PATCH.reply = bench;
        paintMonitor();
        react("Ping is off air - the bench answered from the recorded window.");
      });
    } else {
      PATCH.reply = bench;
    }
    var ta = $("wpPrompt");
    if (ta) ta.value = ""; // sent - the reply card carries what it earned
    paintMonitor();
    react(PATCH.reply.kind === "draft"
      ? "Draft envelope built for " + PATCH.reply.wired + " - NOT RUN; no model runs in a browser."
      : PATCH.reply.kind === "pingwait"
      ? "Asking Ping - a live answer over the Tower relay…"
      : PATCH.reply.kind === "reading"
      ? "The bench answered from the recorded window."
      : "The shim answered from the faceplate.");
    return PATCH.reply;
  }

  function drawReply(rep) {
    // the live voice + the recorded recount travel as one stack
    if (rep.kind === "ping" || rep.kind === "pingwait") {
      var stack = el("section", "sn-stage sn-stage--reply sn-replystack");
      var phead = el("div", "sn-stage__head");
      var dot = el("span", "sn-livedot" + (rep.kind === "pingwait" ? " is-wait" : ""));
      dot.setAttribute("aria-hidden", "true");
      phead.appendChild(dot);
      phead.appendChild(el("b", null, "PING · LIVE over the Tower relay"));
      var px = el("button", "ws-resp__x");
      px.type = "button";
      px.setAttribute("aria-label", "Dismiss the prompt response");
      px.textContent = "×";
      px.addEventListener("click", function () { PATCH.reply = null; paintMonitor(); });
      phead.appendChild(px);
      stack.appendChild(phead);
      stack.appendChild(el("p", "sn-para sn-ping__text" + (rep.kind === "pingwait" ? " is-wait" : ""),
        rep.kind === "pingwait" ? "…asking Ping" : rep.text));
      var sub = el("p", "sn-sub sn-ping__sub",
        "Ping is the concierge answering live - not a Wave model; a hosted Wave Nano is the goal.");
      sub.title = "a real reply from a real endpoint, shown verbatim - declines included";
      stack.appendChild(sub);
      // a question always carries the recorded numbers beneath the live voice
      if (rep.question && rep.bench && rep.bench.kind === "reading") {
        stack.appendChild(drawReply(rep.bench));
      }
      return stack;
    }
    var box = el("section", "sn-stage sn-stage--reply");
    var head = el("div", "sn-stage__head");
    head.appendChild(el("b", null, rep.kind === "reading"
      ? rep.who : "YOUR PROMPT → " + rep.wired.toUpperCase()));
    if (rep.kind === "draft") head.appendChild(el("span", "wp-tag wp-tag--draft", "DRAFT · NOT RUN"));
    var x = el("button", "ws-resp__x");
    x.type = "button";
    x.setAttribute("aria-label", "Dismiss the prompt response");
    x.textContent = "×";
    x.addEventListener("click", function () { PATCH.reply = null; paintMonitor(); });
    head.appendChild(x);
    box.appendChild(head);
    if (rep.kind === "reading") {
      var rd = el("p", "sn-read");
      rd.appendChild(el("b", null, rep.tag));
      rd.appendChild(el("span", null, " " + rep.meanLine));
      box.appendChild(rd);
      box.appendChild(el("p", "sn-para", rep.detail));
      if (rep.chainLine) box.appendChild(el("p", "sn-para", rep.chainLine));
      if (rep.offAirNote) box.appendChild(el("p", "sn-sub sn-read__note", rep.offAirNote));
      var off = el("p", "sn-sub sn-read__offair", rep.offAir);
      off.title = "the answer above is a recount of the recorded window - no model produced this sentence";
      box.appendChild(off);
      return box;
    }
    if (rep.kind === "draft") {
      if (rep.recognised) {
        box.appendChild(el("p", "sn-sub", "Recognised: byte-identical to the recorded pump scene's " +
          rep.recognised + " render. These are our recorded bytes - paste your own for the real test."));
      }
      if (rep.unitNote) box.appendChild(el("p", "sn-sub", rep.unitNote));
      box.appendChild(el("p", "sn-sub",
        "no model runs in a browser, and a margin is a logprob difference nothing here " +
        "can compute. This is the exact request that would go to a stock llama-server:"));
      box.appendChild(el("pre", "wp-wirebytes", rep.envelope));
    } else {
      box.appendChild(el("p", "sn-para", rep.text));
    }
    return box;
  }

  function renderChips() {
    var host = $("wpChips");
    if (!host || host.childNodes.length) return;
    var sc = PATCH.scene;
    if (!sc || !sc.renders) return;
    host.appendChild(el("span", "wp-rail__k", "samples · recorded"));
    Object.keys(sc.renders).forEach(function (mo) {
      var b = el("button", "wp-dialect", mo);
      b.type = "button";
      b.title = "paste the recorded " + mo + " render of the pump scene into the prompt";
      b.addEventListener("click", function () {
        var ta = $("wpPrompt");
        if (!ta) return;
        ta.value = sc.renders[mo];
        promptSend(ta.value);
      });
      host.appendChild(b);
    });
  }

  function wirePrompt() {
    var form = $("wpPromptForm"), ta = $("wpPrompt");
    if (!form || !ta) return;
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      promptSend(ta.value);
    });
    ta.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        promptSend(ta.value);
      }
    });
    // paste-your-own-bytes works here too: the prompt is the drop target
    ta.addEventListener("dragover", function (e) { e.preventDefault(); });
    ta.addEventListener("drop", function (e) {
      e.preventDefault();
      var txt = e.dataTransfer.getData("text");
      if (txt) { ta.value = txt; promptSend(txt); return; }
      var f2 = e.dataTransfer.files && e.dataTransfer.files[0];
      if (f2 && f2.size < 1 << 20) {
        var rd = new FileReader();
        rd.onload = function () { ta.value = String(rd.result || ""); promptSend(ta.value); };
        rd.readAsText(f2);
      }
    });
    renderChips();
    paintWire();
  }

  function paintWire() {
    var lab = $("wpPromptWire");
    if (!lab) return;
    var target = lastModel();
    lab.textContent = "wired to " + (target ? target.label : "the chain") +
      " · chat answered live by Ping · drafts only - no model runs in a browser";
  }

  /* ---- the request envelope: what a paste earns -------------------------
     The exact request the R.45/R.55 protocol sends to a stock llama-server:
     one request per candidate with a locked grammar, logprob sums, EOG
     excluded, leading space, cache_prompt true. All of that is measured and
     documented; the one thing NOT invented here is the task frame text,
     which has not been exported - so its slot says so, like the digest. */
  var ENV_SHOW = 4000; // display cap for the envelope's body - the request
                       // itself would carry the paste verbatim, and says so
  function envelopeFor(src) {
    var body = src.body || "";
    var fullLen = body.length;
    var clipped = null;
    if (body.length > ENV_SHOW) {
      clipped = body.length - ENV_SHOW;
      body = body.slice(0, ENV_SHOW) +
        "\n… [display truncated - " + clipped.toLocaleString() +
        " more bytes of your paste would be sent verbatim]";
    }
    var isNumbers = src.modality === "raw numbers";
    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var anyAsset = cat[Object.keys(cat)[0]] || {};
    var candidates = (anyAsset.sensor_faults && anyAsset.sensor_faults.length)
      ? anyAsset.sensor_faults : ["ok", "stuck", "dropout", "noisy", "drifting", "railed"];
    // The task frame used to be "pending export"; it is now exported verbatim
    // from the bench file, so the envelope shows the real thing.
    var frame = (PATCH.measured && PATCH.measured.task_frame) || null;
    var req = [
      "{",
      frame
        ? '  "prompt": "<the exported task frame below, then Input:, then your body>",'
        : '  "prompt": "<task frame — pending export — followed by the input body>",',
      '  "grammar": "root ::= \\" ' + candidates[1] + '\\"",',
      '  "n_predict": 16,',
      '  "cache_prompt": true',
      "}",
    ].join("\n");
    var notes = [
      "# POST ${LLAMA_SERVER}/completion - ONE REQUEST PER CANDIDATE, each with its",
      "# grammar locked to that candidate: " + candidates.map(function (c) { return '" ' + c + '"'; }).join(", "),
      "# cache_prompt shares one prompt eval across the candidates.",
      "# Request token logprobs per your llama-server build; the harness sums the",
      "# returned tokens' logprob fields, EXCLUDING the trailing end-of-generation",
      "# token. margin = best sum - runner-up sum.",
      "",
      req,
      "",
    ];
    if (isNumbers) {
      notes.push("# input body: the task frame wraps a FEATURES RENDER of your " +
        (src.channels.length ? "" : "") + "samples (mean, sd, slope," );
      notes.push("# longest_run, ...) - the in-browser features port is pending, so the render");
      notes.push("# is not shown here. Your raw samples, verbatim (" + fullLen + " chars):");
    } else {
      notes.push("# input body - your bytes, verbatim (" + fullLen + " chars):");
    }
    var frameBlock = frame
      ? "# task frame - exported verbatim from the bench file:\n" + frame + "\n\n"
      : "";
    return frameBlock + notes.join("\n") + "\n" + body;
  }




  /* =====================================================================
     THE MODE SWITCH - console / mesh
     ===================================================================== */
  function showView(mode) {
    var consoleView = $("pgConsoleView"), mesh = $("pgMeshView");
    if (!consoleView || !mesh) return;
    var toMesh = mode === "mesh";
    consoleView.hidden = toMesh;
    mesh.hidden = !toMesh;
    var hc = $("pgHeroConsole"), hm = $("pgHeroMesh"), ht = $("pgHeroTitle"), hk = $("pgHeroKicker");
    if (hc) hc.hidden = toMesh;
    if (hm) hm.hidden = !toMesh;
    if (ht) ht.textContent = toMesh ? "Wire a machine to a model." : "Open the console.";
    if (hk) hk.textContent = toMesh ? "the wave mesh" : "open the console";
    [["pgModeConsole", !toMesh], ["pgModeMesh", toMesh]].forEach(function (pair) {
      var b = $(pair[0]);
      if (!b) return;
      b.setAttribute("aria-selected", pair[1] ? "true" : "false");
      b.setAttribute("tabindex", pair[1] ? "0" : "-1");
    });
    if (toMesh) maybeBoot();
    try { window.localStorage.setItem("pb.mode", mode); } catch (e) { /* private mode */ }
  }

  function wireModeSwitch() {
    var c = $("pgModeConsole"), m = $("pgModeMesh");
    if (!c || !m) return;
    c.addEventListener("click", function () { showView("console"); });
    m.addEventListener("click", function () { showView("mesh"); });
    [c, m].forEach(function (btn) {
      btn.addEventListener("keydown", function (e) {
        if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
        e.preventDefault();
        var next = btn === c ? m : c;
        showView(next === m ? "mesh" : "console");
        next.focus();
      });
    });
    var hash = (window.location.hash || "").replace("#", "").toLowerCase();
    if (hash === "mesh" || hash === "console") { showView(hash); return; }
    var saved = "console";
    try { saved = window.localStorage.getItem("pb.mode") || "console"; } catch (e) { /* ignore */ }
    showView(saved === "mesh" ? "mesh" : "console");
  }

  /* =====================================================================
     BOOT
     ===================================================================== */
  function boot() {
    Promise.all([
      fetch("data/wave-catalog.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-measured.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-scene-recorded.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-windows.json").then(function (r) { return r.ok ? r.json() : null; }),
    ]).then(function (res) {
      PATCH.catalog = res[0]; PATCH.measured = res[1]; PATCH.scene = res[2];
      PATCH.windows = res[3] ? res[3].windows : null;
      if (!PATCH.catalog || !PATCH.measured) { fail(); return; }
      var prov = $("wpProv");
      if (prov) {
        prov.textContent = "Recorded fleet: " + PATCH.measured.escalation.n + " items of " +
          shortName(PATCH.measured.escalation.child) + " under " +
          shortName(PATCH.measured.escalation.parent) + " on " +
          shortName(PATCH.measured.escalation.bench) + " · " + PATCH.measured._provenance.suite +
          " · every reading here is a recount of these records";
      }
      buildTypes();
      // the recommended pattern boots pre-built: Pico reading, Nano adjudicating
      PATCH.chain = ["pico", "nano"];
      derive();
      render();
      react("The bench is live with the recommended chain: Pico reads, Nano adjudicates. " +
        "Pick a sensor, press a condition pad, and watch the monitor.");
      wireTilt();
      document.addEventListener("keydown", onKey);
      wirePrompt();
    }).catch(fail);
  }

  function onKey(e) {
    if (e.key !== "Escape") return;
    if (PATCH.menuFor != null) {
      PATCH.menuFor = null;
      render();
      var add = document.querySelector(".syn-plus");
      if (add) add.focus();
      return;
    }
    if (PATCH.whyOpen != null) {
      var openKey = PATCH.whyOpen;
      PATCH.whyOpen = null;
      renderWhys();
      var chip = document.querySelector('.sn-whys .sn-why[data-why="' + openKey + '"] > summary');
      if (chip) chip.focus();
      return;
    }
    if (PATCH.reply) {
      PATCH.reply = null;
      paintMonitor();
      var ta = $("wpPrompt");
      if (ta) ta.focus();
    }
  }

  function fail() {
    var s = $("wpMonitor");
    if (s) s.appendChild(el("p", "wp-note", "The bench data did not load. Nothing here is live; reload to try again."));
  }

  function maybeBoot() {
    var v = $("pgMeshView");
    if (v && !v.hidden && !PATCH.booted && $("wpMonitor")) { PATCH.booted = true; boot(); }
  }

  // Pure-function hook so tests can EXECUTE the classifier and the derivation.
  if (typeof window !== "undefined") {
    window.__wavePatchTest = {
      classify: classify,
      setScene: function (sc, cat) { PATCH.scene = sc; PATCH.catalog = cat; },
      setMeasured: function (m) { PATCH.measured = m; },
      buildTypes: buildTypes,
      selectType: selectType,
      chainAdd: chainAdd,
      stages: stages,
      derive: derive,
      envelopeFor: envelopeFor,
      promptSend: promptSend,
      liveAnswerer: liveAnswerer,
      setWindows: function (w) { PATCH.windows = w; },
      seriesOf: seriesOf,
      scaleOf: scaleOf,
      okAnchorRel: okAnchorRel,
      buildReply: buildReply,
      benchContext: benchContext,
      state: PATCH,
      family: FAMILY,
      detents: DETENTS,
      glossary: GLOSSARY,
    };
  }

  function start() { wireModeSwitch(); maybeBoot(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
