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
      does: "classifies: asserts or escalates",
      blurb: "the recorded reader on this bench" },
    { id: "nano", label: "Wave Nano", size: "~350M", status: "recorded",
      runs: "phone · Pi · gateway", icon: "reader", px: 46,
      does: "adjudicates and makes sense of it",
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
    yours: false,         // YOUR DATA selected instead of a recorded type
    chain: [],            // family ids, in daisy-chain order
    floor: 1.5,           // the Pico's margin floor (measured detents)
    operator: false,      // the lever by the monitor footer
    yourData: null,       // the intake's pasted channel, if any
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
      var recIdx = {};
      idxs.forEach(function (i) {
        var t = m.records[i].truth;
        if (!(t in recIdx)) recIdx[t] = i; // FIRST in file order - deterministic
      });
      var conds = Object.keys(recIdx).sort(function (a, b) {
        if (a === "none") return -1;
        if (b === "none") return 1;
        return a < b ? -1 : 1;
      });
      PATCH.types.push({ key: def.key, label: def.label, icon: def.icon,
                         count: idxs.length, conds: conds, recIdx: recIdx });
    });
    if (!PATCH.typeKey && PATCH.types[0]) PATCH.typeKey = PATCH.types[0].key;
  }

  function typeOf(key) {
    for (var i = 0; i < PATCH.types.length; i++) if (PATCH.types[i].key === key) return PATCH.types[i];
    return null;
  }
  function currentType() { return typeOf(PATCH.typeKey); }

  // The selected RECORD: type + condition, deterministically.
  function currentRecord() {
    if (PATCH.yours) return null;
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

  function selectType(key, cond) {
    var t = typeOf(key);
    if (!t) return;
    PATCH.yours = false;
    PATCH.typeKey = key;
    PATCH.cond = (cond != null && t.recIdx[cond] != null) ? cond
      : (t.recIdx[PATCH.cond] != null ? PATCH.cond : t.conds[0]);
  }

  /* =====================================================================
     THE CASCADE - the output at each stage, recorded fields only
     ===================================================================== */
  function stages() {
    var out = [];
    if (PATCH.yours && PATCH.yourData) {
      out.push({ kind: "raw", who: "YOUR BYTES", body: PATCH.yourData.body || "",
                 tag: "your channel", draft: true });
      if (PATCH.chain.length) {
        out.push({ kind: "draft", who: "THE CHAIN",
                   envelope: envelopeFor(PATCH.yourData) });
      }
      return out;
    }
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
    var state, why, label = null;

    if (PATCH.yours) {
      state = "off";
      why = PATCH.chain.length
        ? "Your bytes are a DRAFT - nothing ran, so the lamp has nothing honest to show. The monitor holds the request envelope."
        : "Your bytes are on the bench. Chain a model to see the envelope it would earn.";
      PATCH.verdict = { state: state, why: why, label: null, stages: st };
      paintMonitor();
      return;
    }

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
        state = "yellow";
        why = "The chain works, but no operator is on shift - flip the lever by the monitor; the ladder should end with a person.";
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
    PATCH.verdict = { state: state, why: why, label: label, stages: st };
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
    paintMonitor();
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
      var sel = !PATCH.yours && PATCH.typeKey === t.key;
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
  function renderPads() {
    var host = $("wsPads");
    if (!host) return;
    host.textContent = "";
    if (PATCH.yours) {
      host.appendChild(el("p", "wp-note",
        "Your pasted bytes are the sensor now. Pick a type above to go back to the recorded fleet."));
      return;
    }
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
      pad.title = "replays recorded record " + rr.node_id + " (scene " + rr.scene_id +
        ") - a pad is a recorded instance, selected, not simulated";
      pad.appendChild(el("span", "syn-pad__cap"));
      pad.appendChild(el("span", "syn-pad__k", CONDW[c] || c.toUpperCase()));
      pad.addEventListener("click", function () {
        PATCH.cond = c;
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
    var art = el("span", "sn-type__art sn-type__art--" + (PATCH.yours ? "junction" : (t ? t.icon : "gauge")));
    art.setAttribute("aria-hidden", "true");
    art.appendChild(el("span", "wb-plate__ink"));
    sens.appendChild(art);
    sens.appendChild(el("b", null, PATCH.yours ? "YOUR BYTES" : (t ? t.label : "")));
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

    host.appendChild(railArrow());
    var mon = el("div", "sn-slot sn-slot--monitor");
    mon.appendChild(el("b", null, "MONITOR"));
    mon.appendChild(el("span", "sn-sub", "the output, below"));
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
    var s = svg("svg", { viewBox: "0 0 30 16", width: 30, height: 16, class: "sn-rail__svg" });
    s.appendChild(svg("path", { class: "sn-rail__cable", d: "M2 5 C 10 13, 20 13, 28 5" }));
    s.appendChild(svg("circle", { class: "sn-rail__plug", cx: 2, cy: 5, r: 2.2 }));
    s.appendChild(svg("circle", { class: "sn-rail__plug", cx: 28, cy: 5, r: 2.2 }));
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
        size: 44,
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

  function chainAdd(id, at) {
    var fam = familyById(id);
    if (PATCH.chain.indexOf(id) >= 0 && (id === "pico" || id === "nano")) {
      react("One " + fam.label + " is the chain's shape here.");
      PATCH.menuFor = null;
      render();
      return;
    }
    if (at == null || at > PATCH.chain.length) at = PATCH.chain.length;
    PATCH.chain.splice(at, 0, id);
    PATCH.menuFor = null;
    derive(); render();
    if (fam.status === "recorded") {
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

  /* =====================================================================
     THE MONITOR - the hero. The output at each stage, top to bottom.
     ===================================================================== */
  function paintMonitor() {
    var host = $("wpMonitor");
    if (!host) return;
    host.textContent = "";
    var v = PATCH.verdict;
    var sts = v ? v.stages : stages();

    sts.forEach(function (st) {
      var box = el("section", "sn-stage sn-stage--" + st.kind);
      var head = el("div", "sn-stage__head");
      head.appendChild(el("b", null, st.who));
      if (st.kind === "raw") {
        var rec = el("span", "ws-rec" + (REDUCED ? "" : " is-live"));
        rec.appendChild(el("span", "ws-rec__dot"));
        rec.appendChild(el("span", null, st.draft ? "DRAFT · NOT RUN" : "MONITORING · REPLAY"));
        rec.title = st.draft ? "your bytes - nothing ran" : "the recorded window, replayed - nothing here is live";
        head.appendChild(rec);
      }
      box.appendChild(head);

      if (st.kind === "raw") {
        var log = el("pre", "ws-log", st.body);
        log.title = st.draft ? "your bytes, verbatim"
          : "byte-for-byte, the window the model read in the recorded run";
        box.appendChild(log);
      } else if (st.kind === "pico") {
        var line = el("p", "sn-proto");
        if (st.esc) {
          line.appendChild(el("b", "sn-proto__esc", "ESCALATE ↑"));
          line.appendChild(el("span", null, " margin " + st.margin.toFixed(2) +
            " · below floor " + st.floor.toFixed(1) + " - too doubtful to assert"));
        } else {
          line.appendChild(el("b", null, "ASSERT " + '" ' + st.said + '"'));
          line.appendChild(el("span", null, " · margin " + st.margin.toFixed(2)));
        }
        box.appendChild(line);
        box.appendChild(el("p", "sn-sub", st.esc
          ? "the wire protocol line - the Pico hands this read up"
          : "the wire protocol line - machine-facing, one token of meaning"));
      } else if (st.kind === "nano") {
        var vw = el("p", "sn-verdict");
        vw.appendChild(el("b", null, st.verdict.toUpperCase()));
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
        box.appendChild(el("p", "sn-para sn-para--warn", st.note));
      } else if (st.kind === "draft") {
        box.appendChild(el("p", "wp-tag wp-tag--draft", "DRAFT · NOT RUN"));
        box.appendChild(el("p", "sn-sub",
          "no model executes in a browser, and a margin is a logprob difference nothing " +
          "here can compute. This is the exact request that would go to a stock llama-server:"));
        box.appendChild(el("pre", "wp-wirebytes", st.envelope));
      } else if (st.kind === "silent") {
        box.appendChild(el("p", "sn-sub",
          st.fam.blurb + " - no recorded transcript, output unchanged."));
      }
      host.appendChild(box);
    });

    // the foot below the glass: the verdict lamp + why, always visible
    var lampBig = $("wpLamp2"), why = $("wpWhy");
    if (lampBig && v) {
      lampBig.dataset.state = v.state;
      var f2 = LAMP_FACE[v.state] || LAMP_FACE.off;
      lampBig.textContent = "";
      lampBig.appendChild(el("span", "wp-lampwin__sym", f2.sym));
      lampBig.appendChild(el("span", "wp-lampwin__label", v.label || f2.label));
    }
    if (why && v) why.textContent = v.why;

    var certHost = $("wpCertHost");
    if (certHost) certHost.textContent = "";
    var r = currentRecord();
    if (r && !PATCH.yours && certHost) {
      var det = el("details", "sn-cert");
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
        "run. Every stage prints recorded fields; the margins are recorded logprob " +
        "differences - nothing in this browser computes one."));
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

  /* ---- the a11y mirror: the same bench as a list ------------------------- */
  function renderMirror() {
    var m = $("wpMirror");
    if (!m) return;
    m.textContent = "";
    var t = currentType();
    var r = currentRecord();
    m.appendChild(el("li", null, "Sensor: " + (PATCH.yours ? "your pasted bytes" :
      (t ? t.label + ", condition " + (CONDW[PATCH.cond] || PATCH.cond) +
        (r ? ", replaying " + ((r.window && r.window.tag) || r.node_id) : "") : "none"))));
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

  function paintDetect() {
    var v = INTAKE.verdict;
    var mod = $("wpDetMod"), shape = $("wpDetShape"), frame = $("wpDetFrame");
    var note = $("wpDetNote"), send = $("wpSend");
    if (!mod) return;
    send.disabled = true;
    send.textContent = "SEND TO THE BENCH →";
    note.textContent = "";

    if (!v) {
      mod.textContent = "paste something - the shim reads as you type";
      shape.textContent = "—"; frame.textContent = "—";
      return;
    }
    if (v.kind === "blob") {
      mod.textContent = v.mod + "  ↳ matched " + v.evidence.map(function (e) {
        return JSON.stringify(e.length > 18 ? e.slice(0, 18) + "…" : e);
      }).join(", ");
      shape.textContent = (v.tag ? "tag " + v.tag + " · " : "") + "pass-through: the blob IS the input body";
      frame.textContent = "T01 sensor-health · frame exported - see the envelope";
      if (v.recognised) {
        note.textContent = "Recognised: byte-identical to the recorded pump scene's " +
          v.recognised + " render. These are our recorded bytes - paste your own for the real test.";
      }
      send.disabled = false;
    } else if (v.kind === "ambiguous") {
      mod.textContent = "unclear: " + v.a.mod + " (" + v.a.hits + " marks) vs " +
        v.b.mod + " (" + v.b.hits + ") - add more of the payload";
      shape.textContent = "—"; frame.textContent = "—";
      note.textContent = "The shim refuses to guess on thin evidence. That is the real system's behaviour too.";
    } else if (v.kind === "numbers") {
      mod.textContent = "raw numbers  ↳ " + v.n + " numeric samples" +
        (v.unit ? " · unit stated: " + v.unit : "");
      shape.textContent = v.unit
        ? "one channel · " + v.unit + " (you stated it - it was not in the wire)"
        : "one channel · unit NOT STATED IN THE WIRE - a defaulted unit would be an invented fact";
      frame.textContent = "T01 sensor-health · frame exported - see the envelope";
      send.disabled = false;
    } else if (v.kind === "few-numbers") {
      mod.textContent = "looks numeric - " + v.n + " sample" + (v.n === 1 ? "" : "s");
      shape.textContent = "a window needs at least 8 samples to say anything about a signal";
      frame.textContent = "—";
      note.textContent = "Paste more of the series and the shim will build the channel.";
    } else if (v.kind === "machine-unknown") {
      mod.textContent = "machine-shaped, but not a dialect the shim recognises";
      shape.textContent = v.lines + " lines, digit-heavy - this looks like telemetry";
      frame.textContent = "—";
      note.textContent = "The shim reads eight dialects and their line shapes; this matched " +
        "none well enough to wrap honestly. Try including the header lines of the dump.";
    } else if (v.kind === "scenario-asset") {
      mod.textContent = "a scenario, in words  ↳ matched " + v.evidence.join(", ");
      shape.textContent = "words are never sent to a Wave model";
      frame.textContent = "—";
      note.textContent = "That device is in the catalogue (" + labelOf(v.asset) +
        "). This bench plays the recorded fleet; describing your own scene returns " +
        "when the scene exporter grows.";
    } else {
      mod.textContent = "conversation";
      shape.textContent = "answered by this interface, from the faceplate - it never reaches a model";
      frame.textContent = "—";
      note.textContent = "Wave models are task-native, not chat models. Free-sampled, this " +
        "input would produce a corpus dream, not an answer. Tier pico · floor 2.0 · " +
        "digest pending export. For conversation, the CONSOLE deck hosts chat models.";
    }
  }

  function intakeSend() {
    var v = INTAKE.verdict;
    if (!v) return;
    if (v.kind === "blob") {
      PATCH.yourData = { modality: v.mod, recognised: v.recognised || null,
        channels: [{ name: v.tag || "your channel", unit: "" }], body: INTAKE.text };
    } else if (v.kind === "numbers") {
      PATCH.yourData = { modality: "raw numbers", recognised: null, unit: v.unit || null,
        channels: [{ name: "your channel", unit: v.unit || "" }], body: INTAKE.text };
    } else {
      return;
    }
    PATCH.yours = true;
    closeIntake();
    derive();
    render();
    react(PATCH.chain.length
      ? "Your bytes are the sensor now - the monitor holds the DRAFT request envelope."
      : "Your bytes are the sensor now. Chain a model to see the envelope it would earn.");
  }

  // Drop or share text straight onto YOUR DATA: open the drawer with it read.
  function intakeWith(text) {
    openIntake();
    var ta = $("wpPaste");
    if (!ta) return;
    ta.value = text;
    INTAKE.text = text;
    INTAKE.verdict = classify(text);
    paintDetect();
  }

  function openIntake() {
    var d = $("wpIntake");
    if (!d) return;
    d.hidden = false;
    renderSamples();
    var ta = $("wpPaste");
    if (ta) ta.focus();
  }
  function closeIntake() {
    var d = $("wpIntake");
    if (d) d.hidden = true;
  }

  function renderSamples() {
    var host = $("wpSamples");
    if (!host || host.childNodes.length) return;
    var sc = PATCH.scene;
    if (!sc || !sc.renders) return;
    host.appendChild(el("span", "wp-rail__k", "samples · recorded"));
    Object.keys(sc.renders).forEach(function (mo) {
      var b = el("button", "wp-dialect", mo);
      b.type = "button";
      b.addEventListener("click", function () {
        var ta = $("wpPaste");
        if (!ta) return;
        ta.value = sc.renders[mo];
        INTAKE.text = ta.value;
        INTAKE.verdict = classify(ta.value);
        paintDetect();
      });
      host.appendChild(b);
    });
  }

  function wireIntake() {
    var ta = $("wpPaste"), send = $("wpSend"), close = $("wpIntakeClose");
    if (!ta) return;
    ta.addEventListener("input", function () {
      INTAKE.text = ta.value;
      INTAKE.verdict = classify(ta.value);
      paintDetect();
    });
    if (send) send.addEventListener("click", intakeSend);
    if (close) close.addEventListener("click", closeIntake);
  }

  /* ---- the request envelope: what a paste earns -------------------------
     The exact request the R.45/R.55 protocol sends to a stock llama-server:
     one request per candidate with a locked grammar, logprob sums, EOG
     excluded, leading space, cache_prompt true. All of that is measured and
     documented; the one thing NOT invented here is the task frame text,
     which has not been exported - so its slot says so, like the digest. */
  function envelopeFor(src) {
    var body = src.body || "";
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
      notes.push("# is not shown here. Your raw samples, verbatim (" + body.length + " chars):");
    } else {
      notes.push("# input body - your bytes, verbatim (" + body.length + " chars):");
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
    ]).then(function (res) {
      PATCH.catalog = res[0]; PATCH.measured = res[1]; PATCH.scene = res[2];
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
      derive();
      render();
      react("The panel is live: " + PATCH.sensors.length + " recorded instruments, writing to " +
        "their logs. Tap a node's [+] stub to attach a model; tap a node to see its log.");
      document.addEventListener("keydown", onKey);
      var pb = $("wsPaste");
      if (pb) {
        pb.addEventListener("click", function () { openIntake(); });
        // dropping text or a small file on the button feeds the shim - the
        // one place HTML5 DnD is welcome: being an external drop TARGET
        pb.addEventListener("dragover", function (e) { e.preventDefault(); });
        pb.addEventListener("drop", function (e) {
          e.preventDefault();
          var txt = e.dataTransfer.getData("text");
          if (txt) { intakeWith(txt); return; }
          var f2 = e.dataTransfer.files && e.dataTransfer.files[0];
          if (f2 && f2.size < 1 << 20) {
            var rd = new FileReader();
            rd.onload = function () { intakeWith(String(rd.result || "")); };
            rd.readAsText(f2);
          }
        });
      }
      wireIntake();
    }).catch(fail);
  }

  function onKey(e) {
    if (e.key === "Escape") {
      var drawer = $("wpIntake");
      if (drawer && !drawer.hidden) {
        closeIntake();
        var back = $("wsPaste") || document.body;
        if (back) back.focus();
        return;
      }
      if (PATCH.menuFor != null) {
        var ch = PATCH.menuFor;
        PATCH.menuFor = null;
        render();
        var add = document.querySelector('.syn-plus');
        if (add) add.focus();
      }
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
