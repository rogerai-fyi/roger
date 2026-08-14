/* =====================================================================
   RogerAI - THE SIGNAL BENCH (Playbox / WAVE MESH)

   A rack of live recorded instruments. Each bay is a REAL sensor from
   the measured fleet run - its tag, its stated unit, its window bounds,
   and the literal log the model read. With no model attached, a sensor
   does what sensors do: it writes to its log. Tap the [+] at the end of
   a bay to attach a model - the whole Wave family is on the menu, sized
   so you can SEE how big each one is - and the bay shows what the model
   READS (the window, its bounds) and what it SAYS (the recorded
   prediction, its margin, assert or escalate).

   THE CONCEPT (founder direction 2026-08-13, fourth revision):

   SENSORS THAT MAKE SENSE. Bays carry the recorded tags
     (AIR003_DISCHARGE_TEMP, not c00), the stated unit - or "unit not
     stated", because a defaulted unit is an invented fact - and a
     bounds meter drawn from the recorded window. The CONDITION dial's
     positions are exactly the conditions recorded for that channel;
     each position replays one deterministic recorded instance.

   MODELS YOU CAN SIZE AT A GLANCE. The attach menu draws every family
     slot as an engraved radio scaled to its parameter count - a Pico is
     a pocket set, a Nano a tabletop receiver, a Satellite a rack - with
     what it runs on, in plain words. Only the two slots with a recorded
     run on this bench can speak; the rest attach honestly as silent.

   HONESTY, unchanged: no model executes in a browser. Every word in a
     bay's response panel is a FIELD of a recorded record; the log
     window is the byte-for-byte input body the model actually read;
     the task frame is now EXPORTED, verbatim, from the bench file.
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

  /* ---- the model family: the whole shelf, honestly labelled -------------
     Sizes, statuses and runs-on are the family page's own. Only two slots
     have a recorded run on THIS bench: the reader the records call `child`
     (deck name Wave Pico) and the senior they call `parent` (Wave Nano).
     The icon scale is the size argument made visible: a pocket set, a
     tabletop receiver, a rack. */
  var FAMILY = [
    { id: "edge", label: "Roger Edge", size: "KB-10M", status: "in design",
      runs: "ESP32 · Cortex-M", icon: "pocket", px: 26,
      blurb: "wake and sensing tier - in design" },
    { id: "pico", label: "Wave Pico", size: "270M", status: "recorded",
      runs: "gateway class", icon: "pocket", px: 38,
      blurb: "the recorded reader on this bench" },
    { id: "nano", label: "Wave Nano", size: "~350M", status: "recorded",
      runs: "phone · Pi · gateway", icon: "reader", px: 46,
      blurb: "the recorded senior - adjudicates doubtful reads" },
    { id: "micro", label: "Wave Micro", size: "1-8B", status: "trained",
      runs: "laptop · edge computer", icon: "reader", px: 58,
      blurb: "trained, but has no recorded run on this bench" },
    { id: "core", label: "Wave Core", size: "8-30B", status: "planned",
      runs: "single GPU · control room", icon: "senior", px: 52,
      blurb: "planned slot" },
    { id: "station", label: "Wave Station", size: "30-70B", status: "planned",
      runs: "rack · plant server", icon: "senior", px: 62,
      blurb: "planned slot" },
    { id: "satellite", label: "Wave Satellite", size: "~70B+", status: "planned",
      runs: "plant server room", icon: "senior", px: 74,
      blurb: "planned slot" },
  ];
  function familyById(id) {
    for (var i = 0; i < FAMILY.length; i++) if (FAMILY[i].id === id) return FAMILY[i];
    return null;
  }

  var UNIT_WORD = { Cel: "°C", kPa: "kPa", "mm/s": "mm/s", A: "A" };

  var PATCH = {
    catalog: null, measured: null, scene: null,
    sensors: [],          // the bays, built from the records at boot
    senior: false,        // Wave Nano seated in the senior rack
    operator: false,      // the console's ON SHIFT switch
    yourData: null,       // the intake's pasted channel, if any
    verdict: null,
    menuFor: null,        // bay ch whose attach menu is open
    sel: null,            // the selected bay (its pads + terminal are shown)
    booted: false,
    step: 0,              // strip line counter
  };

  /* ---- bays: built FROM the records, never beside them -------------------
     Each bay is one recorded channel slot (c00..c05). Its dial positions
     are the truths that actually occur for that slot in the 120-record
     sample, each mapped to the FIRST record carrying it - deterministic,
     so the same dial position always replays the same recorded instance.
     Dialing changes which recorded machine's instrument sits in the bay,
     so the bay RELABELS itself with the record's own tag. */
  function buildSensors() {
    var m = PATCH.measured;
    if (!m) return;
    var byCh = {};
    m.records.forEach(function (r, i) {
      var ch = r.node_id.slice(-3);
      byCh[ch] = byCh[ch] || {};
      if (!(r.truth in byCh[ch])) byCh[ch][r.truth] = i;
    });
    PATCH.sensors = Object.keys(byCh).sort().map(function (ch, i) {
      var conds = Object.keys(byCh[ch]).sort(function (a, b) {
        if (a === "none") return -1;
        if (b === "none") return 1;
        return a < b ? -1 : 1;
      });
      return { ch: ch, n: i + 1, conds: conds, recIdx: byCh[ch],
               on: i < 3, cond: "none", model: null, floor: 1.5 };
    });
  }
  function liveSensors() {
    return PATCH.sensors.filter(function (s) { return s.on; });
  }
  function recordOf(s) {
    return PATCH.measured.records[s.recIdx[s.cond]];
  }
  var CONDW = { none: "OK" };

  function tagOf(s) {
    var w = recordOf(s).window;
    return (w && w.tag) ? w.tag : ("CHANNEL " + s.ch);
  }
  function unitOf(s) {
    var w = recordOf(s).window;
    if (!w || !w.unit) return null;
    return UNIT_WORD[w.unit] || w.unit;
  }

  /* =====================================================================
     THE DERIVATION - one recorded record per live bay, recounted
     ===================================================================== */
  function readOf(s) {
    var r = recordOf(s);
    var fam = familyById(s.model);
    if (!fam) return { s: s, r: r, nodata: true, silent: "logging only" };
    if (fam.status !== "recorded") {
      return { s: s, r: r, nodata: true,
               silent: fam.label + " has no recorded run on this bench" };
    }
    if (fam.id === "nano") {
      // parent-direct: the measured config where the senior reads directly
      return { s: s, r: r, said: r.parent.prediction, margin: r.parent.margin,
               via: "reads direct", esc: false, ok: r.parent.prediction === r.truth };
    }
    // pico: assert or escalate at THIS bay's floor
    if (r.child.margin >= s.floor) {
      return { s: s, r: r, said: r.child.prediction, margin: r.child.margin,
               via: "asserts", esc: false, ok: r.child.prediction === r.truth };
    }
    if (PATCH.senior) {
      return { s: s, r: r, said: r.parent.prediction, margin: r.child.margin,
               via: "doubts, senior says", esc: true, ok: r.parent.prediction === r.truth };
    }
    return { s: s, r: r, said: null, margin: r.child.margin,
             via: "doubts - nobody to ask", esc: true, deadEnd: true, ok: false };
  }

  function derive() {
    if (!PATCH.measured) return;
    var live = liveSensors();
    var reads = live.map(readOf);
    var modeled = reads.filter(function (rd) { return !rd.nodata; });
    var anyRecorded = PATCH.sensors.some(function (s) {
      var f = familyById(s.model); return f && f.status === "recorded";
    });

    var t = { live: live.length, modeled: modeled.length, faults: 0, caught: 0,
              missed: 0, fixable: 0, deadEnd: 0, falseAlarms: 0, escalated: 0 };
    modeled.forEach(function (rd) {
      var isFault = rd.r.truth !== "none";
      if (isFault) t.faults++;
      if (rd.esc) t.escalated++;
      if (rd.deadEnd) { if (isFault) t.missed++; t.deadEnd++; return; }
      if (rd.ok) { if (isFault) t.caught++; return; }
      if (isFault) {
        t.missed++;
        // FIXABLE: a higher detent on that bay's knob would have escalated
        // the read, and the recorded senior had the right answer. Everything
        // else missed is the ladder's measured ceiling on this dialed set.
        if (!rd.esc && rd.r.parent.prediction === rd.r.truth && rd.r.child.margin < TOP) {
          t.fixable++;
        }
      } else {
        t.falseAlarms++;
      }
    });

    var state, why, label = null;
    if (!anyRecorded) {
      state = "off";
      why = PATCH.sensors.some(function (s) { return s.model; })
        ? "The attached models have no recorded run on this bench - the lamp only glows " +
          "for recounts of recorded records. Attach Wave Pico or Wave Nano."
        : "Sensors are logging, nobody is reading. Tap the [+] at the end of a bay to attach a model.";
    } else if (!t.modeled) {
      state = "off"; why = "Every bay with a model is switched off. Flip a lever.";
    } else if (t.deadEnd > 0 && t.missed > 0) {
      state = "red";
      why = t.missed + " dialed fault" + (t.missed === 1 ? "" : "s") + " missed and " +
        t.deadEnd + " doubtful read" + (t.deadEnd === 1 ? "" : "s") + " with nobody to ask - " +
        "seat Wave Nano in the senior rack.";
    } else if (t.fixable > 0) {
      state = "red";
      why = t.fixable + " dialed fault" + (t.fixable === 1 ? "" : "s") + " missed that a higher " +
        "floor would have escalated - and the recorded senior had the right answer. " +
        "Raise the FLOOR knob on that bay.";
    } else if (t.deadEnd > 0) {
      state = "yellow";
      why = t.deadEnd + " doubtful read" + (t.deadEnd === 1 ? "" : "s") + " with nobody to ask - " +
        "seat Wave Nano in the senior rack.";
    } else if (!PATCH.operator) {
      state = "yellow";
      why = "The chain works, but no operator is on shift - flip the console switch; " +
        "the ladder should end with a person.";
    } else if (t.missed === 0) {
      state = "green";
      why = t.faults
        ? "Complete chain: every dialed fault caught, operator on shift."
        : "All quiet: every read bay dialed OK, and the models agree.";
    } else {
      state = "green"; label = "AT CEILING";
      why = "Complete chain at its measured ceiling: " + t.caught + " of " + t.faults +
        " dialed faults caught. The remaining " + t.missed + " were missed by the recorded " +
        "senior itself - no knob setting changes that.";
    }
    PATCH.verdict = { state: state, why: why, label: label, totals: t, reads: reads };
    paintConsole();
  }

  /* ---- the status console: lamp, why, reads, strip ----------------------- */
  var LAMP_FACE = {
    off:    { sym: "·", label: "STANDING BY" },
    green:  { sym: "●", label: "ALL CLEAR" },
    yellow: { sym: "△", label: "DEGRADED" },
    red:    { sym: "⊗", label: "FAULTS MISSED" },
  };

  function paintConsole() {
    var v = PATCH.verdict;
    if (!v) return;
    var lamp = $("wpLamp2");
    if (lamp) {
      lamp.dataset.state = v.state;
      var f = LAMP_FACE[v.state] || LAMP_FACE.off;
      lamp.textContent = "";
      lamp.appendChild(el("span", "wp-lampwin__sym", f.sym));
      lamp.appendChild(el("span", "wp-lampwin__label", v.label || f.label));
    }
    var why = $("wpWhy");
    if (why) why.textContent = v.why;
    paintReads();
  }

  // WHAT IT SAYS: one printed line per read bay - the recorded prediction,
  // its margin, and the outcome. A replay, never a run.
  function paintReads() {
    var host = $("wpReads");
    if (!host) return;
    host.textContent = "";
    var v = PATCH.verdict;
    if (!v) return;
    var any = false;
    v.reads.forEach(function (rd) {
      if (rd.nodata) return;
      any = true;
      var li = el("li", "wp-read" + (rd.esc ? " wp-read--esc" : ""));
      li.appendChild(el("b", null, "S" + rd.s.n));
      li.appendChild(el("span", "wp-read__cond", CONDW[rd.s.cond] || rd.s.cond.toUpperCase()));
      li.appendChild(el("span", "wp-read__said",
        rd.via + " " + (rd.deadEnd ? '" ?"' : '" ' + rd.said + '"')));
      li.appendChild(el("span", "wp-read__m", rd.margin.toFixed(2)));
      var isFault = rd.r.truth !== "none";
      var mark = rd.deadEnd ? "unheard"
        : rd.ok ? (isFault ? "caught" : "quiet")
        : (isFault ? "MISSED" : "false alarm");
      li.appendChild(el("span", "wp-read__mark wp-read__mark--" + (rd.deadEnd ? "dead" : rd.ok ? "ok" : "bad"), mark));
      li.title = "recorded record " + rd.r.node_id + " (scene " + rd.r.scene_id + ") · truth " +
        rd.r.truth + " · child said " + rd.r.child.prediction + " (margin " +
        rd.r.child.margin.toFixed(2) + ") · senior said " + rd.r.parent.prediction +
        " (margin " + rd.r.parent.margin.toFixed(2) + ")";
      host.appendChild(li);
    });
    if (!any) {
      host.appendChild(el("li", "wp-read wp-read--quiet",
        "— sensors are logging; attach a model to hear a reading —"));
    }
    if (PATCH.yourData) {
      var li2 = el("li", "wp-read wp-read--quiet");
      li2.appendChild(el("b", null, "YOU"));
      li2.appendChild(el("span", "wp-read__said",
        "your channel · DRAFT - nothing ran; open YOUR DATA's bay for the request envelope"));
      host.appendChild(li2);
    }
  }

  // The strip is a chart recorder: one printed line per reaction, newest on
  // top, capped. It narrates cause and effect as the switches flip.
  function react(msg) {
    var strip = $("wpStrip");
    if (strip) {
      PATCH.step++;
      var li = el("li", "wp-strip__line");
      li.appendChild(el("span", "wp-strip__t", String(PATCH.step).padStart(3, "0")));
      li.appendChild(el("span", null, msg));
      strip.insertBefore(li, strip.firstChild);
      while (strip.childNodes.length > 6) strip.removeChild(strip.lastChild);
    }
    var say = $("wpSay");
    if (say) say.textContent = msg;
  }

  function reactStats() {
    var v = PATCH.verdict;
    if (!v || !v.totals.modeled) return;
    var t = v.totals;
    var bits = [t.modeled + " bay" + (t.modeled === 1 ? "" : "s") + " read"];
    if (t.faults) bits.push(t.faults + " dialed fault" + (t.faults === 1 ? "" : "s"));
    if (t.caught) bits.push(t.caught + " caught");
    if (t.missed) bits.push(t.missed + " missed");
    if (t.deadEnd) bits.push(t.deadEnd + " unheard");
    if (t.falseAlarms) bits.push(t.falseAlarms + " false alarm" + (t.falseAlarms === 1 ? "" : "s"));
    react(bits.join(" · "));
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
     RENDER - the node canvas, the channel strip, the pads, the terminal

     The deck is one dark INSTRUMENT: a bezel with its own scoped palette
     (--syn-*), sitting on the cream page like a piece of rack gear on a
     workbench. Inside: an n8n-style node canvas (sensors -> models ->
     senior -> lamp, cables drawn from the same coordinates the nodes are
     placed by, so they cannot drift), a channel strip with the verdict
     lamp, and an MPC-style pad row that dials the SELECTED sensor's
     condition - one backlit pad per RECORDED condition, nothing else.
     ===================================================================== */
  var ROW_H = 92, NODE_W = 190, NODE_H = 76;
  var COL_X = { sensor: 14, model: 262, senior: 512, lamp: 716 };
  var CANVAS_W = 866;

  function render() {
    renderCanvas();
    renderSenior();
    renderOp();
    renderPads();
    renderTerm();
    renderMirror();
  }

  function selected() {
    var s = PATCH.sensors.filter(function (x) { return x.ch === PATCH.sel; })[0];
    return s || PATCH.sensors[0] || null;
  }

  function modelIcon(fam) {
    // The size argument, drawn: an engraved set scaled to the slot's params,
    // re-inked phosphor-light on the dark panel (the plates are alpha masks).
    var box = el("span", "ws-icon ws-icon--" + fam.icon);
    box.style.width = fam.px + "px";
    box.style.height = Math.round(fam.px * (fam.icon === "senior" ? 1.25 : fam.icon === "pocket" ? 1 : 0.66)) + "px";
    box.setAttribute("aria-hidden", "true");
    box.appendChild(el("span", "wb-plate__ink"));
    return box;
  }

  /* ---- the canvas: nodes at computed coordinates, cables from the same
     numbers - geometry is the single source of truth ---------------------- */
  function nodeY(i) { return 18 + i * ROW_H; }

  function renderCanvas() {
    var host = $("wsRack");
    if (!host) return;
    host.textContent = "";
    var rows = PATCH.sensors.length + 1; // + YOUR DATA
    var h = 18 + rows * ROW_H + 8;
    host.style.height = h + "px";
    host.style.minWidth = CANVAS_W + "px";

    PATCH.sensors.forEach(function (s, i) {
      host.appendChild(drawSensorNode(s, i));
      if (s.model) host.appendChild(drawModelNode(s, i));
    });
    host.appendChild(drawIntakeNode(PATCH.sensors.length));

    // the senior rack node appears on the canvas when seated
    if (PATCH.senior) {
      var mid = nodeY(Math.floor((PATCH.sensors.length - 1) / 2));
      var sn = el("div", "syn-node syn-node--senior");
      sn.style.left = COL_X.senior + "px";
      sn.style.top = mid + "px";
      var ic = modelIcon(familyById("nano"));
      sn.appendChild(ic);
      var t = el("div", "syn-node__body");
      t.appendChild(el("b", null, "WAVE NANO"));
      t.appendChild(el("span", "syn-node__sub", "senior · adjudicates doubtful reads"));
      sn.appendChild(t);
      sn.appendChild(el("span", "syn-node__port syn-node__port--in"));
      sn.appendChild(el("span", "syn-node__port syn-node__port--out"));
      host.appendChild(sn);
    }

    // the output lamp node: where every chain ends
    var v = PATCH.verdict || { state: "off" };
    var ln = el("div", "syn-node syn-node--lamp");
    ln.style.left = COL_X.lamp + "px";
    ln.style.top = nodeY(Math.floor((PATCH.sensors.length - 1) / 2)) + "px";
    var lw = el("span", "wp-lampwin wp-lampwin--node");
    lw.dataset.state = v.state;
    var f = LAMP_FACE[v.state] || LAMP_FACE.off;
    lw.appendChild(el("span", "wp-lampwin__sym", f.sym));
    lw.appendChild(el("span", "wp-lampwin__label", v.label || f.label));
    ln.appendChild(lw);
    ln.appendChild(el("span", "syn-node__sub", "OUTPUT"));
    ln.appendChild(el("span", "syn-node__port syn-node__port--in"));
    host.appendChild(ln);

    drawCables();
  }

  function drawSensorNode(s, i) {
    var r = recordOf(s);
    var w = r.window || {};
    var n = el("div", "syn-node syn-node--sensor" + (s.on ? " is-on" : "") +
      (PATCH.sel === s.ch ? " is-sel" : ""));
    n.style.left = COL_X.sensor + "px";
    n.style.top = nodeY(i) + "px";
    n.dataset.sensor = s.ch;
    n.tabIndex = 0;
    n.setAttribute("role", "button");
    n.setAttribute("aria-label", "Sensor bay " + s.n + ", " + tagOf(s) + ", " +
      (s.on ? "on line, dialed " + (CONDW[s.cond] || s.cond) : "off") +
      ". Activate to select; its pads and log appear below.");

    // the power button: a real switch, top-right, like gear
    var pwr = el("button", "syn-node__pwr" + (s.on ? " is-on" : ""));
    pwr.type = "button";
    pwr.setAttribute("role", "switch");
    pwr.setAttribute("aria-checked", s.on ? "true" : "false");
    pwr.setAttribute("aria-label", "Bay " + s.n + " on line");
    pwr.title = s.on ? "on line - tap to switch off" : "off - tap to bring on line";
    pwr.addEventListener("click", function (e) {
      e.stopPropagation();
      s.on = !s.on;
      derive(); render();
      react("Bay " + s.n + (s.on ? " on line - " + tagOf(s) + "." : " off line."));
      reactStats();
      refocusBay(s, ".syn-node__pwr");
    });
    n.appendChild(pwr);

    var body = el("div", "syn-node__body");
    var tagB = el("b", null, tagOf(s));
    tagB.title = "the recorded tag of the instrument this bay is dialed to (scene " + r.scene_id + ")";
    body.appendChild(tagB);
    var u = unitOf(s);
    var sub = el("span", "syn-node__sub",
      (u ? u : "unit not stated") + " · " + (CONDW[s.cond] || s.cond.toUpperCase()));
    if (!u) sub.title = "the wire did not state a unit - a defaulted unit would be an invented fact";
    body.appendChild(sub);
    var meter = drawMeter(w);
    if (meter && s.on) body.appendChild(meter);
    n.appendChild(body);

    var led = el("span", "syn-node__led");
    led.dataset.state = ledOf(s);
    led.setAttribute("aria-hidden", "true");
    n.appendChild(led);

    n.appendChild(el("span", "syn-node__port syn-node__port--out"));

    // the n8n plus-stub: an unconnected output invites a model
    if (!s.model) {
      var plus = el("button", "syn-plus");
      plus.type = "button";
      plus.style.left = (COL_X.sensor + NODE_W + 34) + "px";
      plus.style.top = (nodeY(i) + NODE_H / 2 - 14) + "px";
      plus.setAttribute("aria-expanded", PATCH.menuFor === s.ch ? "true" : "false");
      plus.setAttribute("aria-label", "Attach a model to bay " + s.n);
      plus.textContent = "+";
      plus.addEventListener("click", function (e) {
        e.stopPropagation();
        PATCH.sel = s.ch;
        PATCH.menuFor = PATCH.menuFor === s.ch ? null : s.ch;
        render();
        var menu = document.querySelector(".ws-menu button");
        if (menu) menu.focus();
      });
      // the stub is a sibling of the node so the cable can reach it
      var wrap = el("div");
      wrap.appendChild(n);
      wrap.appendChild(plus);
      n.addEventListener("click", selectHandler(s));
      n.addEventListener("keydown", keySelect(s));
      return wrap;
    }

    n.addEventListener("click", selectHandler(s));
    n.addEventListener("keydown", keySelect(s));
    return n;
  }

  function selectHandler(s) {
    return function (e) {
      if (e.target.closest(".syn-node__pwr, .syn-plus, .ws-resp__x")) return;
      PATCH.sel = s.ch;
      PATCH.menuFor = null;
      render();
    };
  }
  function keySelect(s) {
    return function (e) {
      if (e.key === "Enter" || e.key === " ") {
        if (e.target !== e.currentTarget) return;
        e.preventDefault();
        PATCH.sel = s.ch;
        render();
      }
    };
  }

  function ledOf(s) {
    if (!s.on) return "off";
    var fam = familyById(s.model);
    if (!fam) return "log";
    if (fam.status !== "recorded") return "silent";
    var rd = readOf(s);
    if (rd.deadEnd) return "dead";
    if (!rd.ok && rd.r.truth !== "none") return "missed";
    return "ok";
  }

  function drawModelNode(s, i) {
    var fam = familyById(s.model);
    var n = el("div", "syn-node syn-node--model" +
      (fam.status === "recorded" ? "" : " syn-node--quiet") +
      (PATCH.sel === s.ch ? " is-sel" : ""));
    n.style.left = COL_X.model + "px";
    n.style.top = nodeY(i) + "px";
    n.dataset.model = s.ch;
    n.tabIndex = 0;
    n.setAttribute("role", "button");
    n.setAttribute("aria-label", fam.label + " on bay " + s.n + ". Activate to select.");

    n.appendChild(modelIcon(fam));
    var body = el("div", "syn-node__body");
    body.appendChild(el("b", null, fam.label));
    body.appendChild(el("span", "syn-node__sub", fam.size + " · " + fam.status +
      (fam.id === "pico" ? " · floor " + s.floor.toFixed(1) : "")));
    n.appendChild(body);

    var x = el("button", "ws-resp__x");
    x.type = "button";
    x.setAttribute("aria-label", "Detach " + fam.label + " from bay " + s.n);
    x.textContent = "×";
    x.addEventListener("click", function (e) {
      e.stopPropagation();
      s.model = null;
      derive(); render();
      react(fam.label + " detached from bay " + s.n + " - the sensor writes to its log again.");
    });
    n.appendChild(x);

    n.appendChild(el("span", "syn-node__port syn-node__port--in"));
    n.appendChild(el("span", "syn-node__port syn-node__port--out"));
    n.addEventListener("click", selectHandler(s));
    n.addEventListener("keydown", keySelect(s));
    return n;
  }

  function drawIntakeNode(i) {
    var n = el("div", "syn-node syn-node--intake");
    n.style.left = COL_X.sensor + "px";
    n.style.top = nodeY(i) + "px";
    n.tabIndex = 0;
    n.setAttribute("role", "button");
    n.setAttribute("aria-label", "Your data - paste what your plant emits");
    var body = el("div", "syn-node__body");
    body.appendChild(el("b", null, "YOUR DATA"));
    body.appendChild(el("span", "syn-node__sub", PATCH.yourData
      ? "PASTED · " + (PATCH.yourData.modality || "").toUpperCase() + " · DRAFT"
      : "paste / drop - read by the shim"));
    n.appendChild(body);
    n.appendChild(el("span", "syn-node__port syn-node__port--out is-dark"));
    n.addEventListener("click", function () {
      if (PATCH.yourData) { inspectYourData(); } else { openIntake(); }
    });
    n.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openIntake(); }
    });
    n.addEventListener("dragover", function (e) { e.preventDefault(); });
    n.addEventListener("drop", function (e) {
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
    return n;
  }

  // The bounds meter: the recorded window drawn as an instrument - the
  // range is the scale, the mean is the pointer. Recorded numbers only.
  function drawMeter(w) {
    if (!w || w.lo == null || w.hi == null) return null;
    var W = 168, H = 18;
    var host = svg("svg", { class: "ws-meter", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "recorded window: " + w.lo + " to " + w.hi + (w.unit ? " " + w.unit : "") });
    host.appendChild(svg("line", { class: "ws-meter__rail", x1: 4, x2: W - 4, y1: H - 5, y2: H - 5 }));
    [4, W - 4].forEach(function (x) {
      host.appendChild(svg("line", { class: "ws-meter__stop", x1: x, x2: x, y1: H - 10, y2: H } ));
    });
    var span = (w.hi - w.lo) || 1;
    var mx = 4 + ((w.mean - w.lo) / span) * (W - 8);
    host.appendChild(svg("path", { class: "ws-meter__needle",
      d: "M" + mx + " " + (H - 5) + " L" + (mx - 3.4) + " " + (H - 13) + " L" + (mx + 3.4) + " " + (H - 13) + " z" }));
    var t1 = svg("text", { class: "ws-meter__t", x: 4, y: 7, "text-anchor": "start" });
    t1.textContent = String(w.lo);
    var t2 = svg("text", { class: "ws-meter__t", x: W - 4, y: 7, "text-anchor": "end" });
    t2.textContent = String(w.hi);
    host.appendChild(t1); host.appendChild(t2);
    host.setAttribute("title", "recorded window bounds, with the mean as the pointer");
    return host;
  }

  /* ---- cables: computed from the SAME coordinates the nodes are placed
     by. A cable exists only where a connection exists. --------------------- */
  function cablePath(x1, y1, x2, y2) {
    var mid = (x1 + x2) / 2;
    return "M" + x1 + " " + y1 + " C" + mid + " " + y1 + ", " + mid + " " + y2 + ", " + x2 + " " + y2;
  }

  function drawCables() {
    var cables = $("wbCables"), host = $("wsRack");
    if (!cables || !host) return;
    while (cables.lastChild) cables.removeChild(cables.lastChild);
    var H = parseInt(host.style.height, 10) || 600;
    cables.setAttribute("width", CANVAS_W);
    cables.setAttribute("height", H);
    cables.setAttribute("viewBox", "0 0 " + CANVAS_W + " " + H);

    var midRow = nodeY(Math.floor((PATCH.sensors.length - 1) / 2));
    var seniorIn = { x: COL_X.senior, y: midRow + NODE_H / 2 };
    var seniorOut = { x: COL_X.senior + NODE_W, y: midRow + NODE_H / 2 };
    var lampIn = { x: COL_X.lamp, y: midRow + NODE_H / 2 };
    var anyToLamp = false;

    PATCH.sensors.forEach(function (s, i) {
      var oy = nodeY(i) + NODE_H / 2;
      var out = { x: COL_X.sensor + NODE_W, y: oy };
      if (!s.model) {
        // sensor -> its plus-stub: the n8n invitation
        cables.appendChild(svg("path", { class: "wb-cable wb-cable--stub",
          d: cablePath(out.x, out.y, COL_X.sensor + NODE_W + 34, oy) }));
        return;
      }
      if (!s.on) return;
      cables.appendChild(svg("path", { class: "wb-cable",
        d: cablePath(out.x, out.y, COL_X.model, oy) }));
      var fam = familyById(s.model);
      if (!fam || fam.status !== "recorded") return;
      var mOut = { x: COL_X.model + NODE_W, y: oy };
      var rd = readOf(s);
      if (rd.esc && PATCH.senior) {
        // the doubtful read rides the dashed escalation cable to the senior
        cables.appendChild(svg("path", { class: "wb-cable wb-cable--esc",
          d: cablePath(mOut.x, mOut.y, seniorIn.x, seniorIn.y) }));
        anyToLamp = true;
      } else if (!rd.deadEnd) {
        cables.appendChild(svg("path", { class: "wb-cable wb-cable--thin",
          d: cablePath(mOut.x, mOut.y, lampIn.x, lampIn.y) }));
        anyToLamp = true;
      }
    });
    if (PATCH.senior && anyToLamp) {
      cables.appendChild(svg("path", { class: "wb-cable",
        d: cablePath(seniorOut.x, seniorOut.y, lampIn.x, lampIn.y) }));
    }
  }

  /* ---- the pads: MPC-style, one backlit pad per RECORDED condition ------- */
  function renderPads() {
    var host = $("wsPads");
    if (!host) return;
    host.textContent = "";
    var s = selected();
    if (!s) return;

    var head = el("div", "syn-pads__head");
    head.appendChild(el("b", null, "CONDITION PADS"));
    head.appendChild(el("span", "syn-node__sub", "bay " + s.n + " · " + tagOf(s) +
      " · one pad per recorded condition - unrecorded conditions have no pad"));
    host.appendChild(head);

    var row = el("div", "syn-pads__row");
    row.setAttribute("role", "radiogroup");
    row.setAttribute("aria-label", "Bay " + s.n + " condition - each pad replays one recorded instance");
    s.conds.forEach(function (c) {
      var pad = el("button", "syn-pad" + (s.cond === c ? " is-lit" : "") +
        (c === "none" ? " syn-pad--ok" : ""));
      pad.type = "button";
      pad.setAttribute("role", "radio");
      pad.setAttribute("aria-checked", s.cond === c ? "true" : "false");
      var rr = PATCH.measured.records[s.recIdx[c]];
      pad.title = "replays recorded record " + rr.node_id + " (scene " + rr.scene_id +
        ") - a pad is a recorded instance, selected, not simulated";
      pad.appendChild(el("span", "syn-pad__cap"));
      pad.appendChild(el("span", "syn-pad__k", CONDW[c] || c.toUpperCase()));
      pad.addEventListener("click", function () {
        s.cond = c;
        if (!s.on) s.on = true;
        derive(); render();
        react("Bay " + s.n + " pad " + (CONDW[c] || c.toUpperCase()) +
          " - now replaying " + tagOf(s) + ".");
        reactStats();
        var again = document.querySelector('.syn-pad.is-lit');
        if (again) again.focus({ preventScroll: true });
      });
      row.appendChild(pad);
    });
    host.appendChild(row);

    // the floor knob rides with the pads when the selected bay reads via pico
    if (s.model === "pico") {
      var kwrap = el("div", "syn-pads__knob");
      kwrap.appendChild(drawDial({
        values: DETENTS.slice(),
        labels: DETENTS.map(function (d) { return "FLOOR " + d.toFixed(1); }),
        index: Math.max(0, DETENTS.indexOf(s.floor)),
        name: "Bay " + s.n + " margin floor",
        size: 56,
        tip: function (i) { return knobTip(DETENTS[i]); },
        onset: function (i) {
          s.floor = DETENTS[i];
          derive(); render();
          react("Bay " + s.n + " floor set to " + s.floor.toFixed(1) +
            " - margins below it now escalate.");
          reactStats();
          var again = document.querySelector(".syn-pads__knob .wp-knob");
          if (again) again.focus({ preventScroll: true });
        },
      }));
      host.appendChild(kwrap);
    }

    if (PATCH.menuFor === s.ch) host.appendChild(drawMenu(s));
  }

  // The attach menu: the whole family, sized so size is VISIBLE.
  function drawMenu(s) {
    var menu = el("div", "ws-menu");
    menu.setAttribute("role", "menu");
    menu.setAttribute("aria-label", "Attach a model to bay " + s.n);
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
        attach(s, fam.id);
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

  function attach(s, id) {
    var fam = familyById(id);
    s.model = id;
    if (!s.on) s.on = true;
    PATCH.menuFor = null;
    PATCH.sel = s.ch;
    derive(); render();
    if (fam.status === "recorded") {
      react(fam.label + " attached to " + tagOf(s) + (id === "pico"
        ? " - it reads the window and asserts or escalates at its floor."
        : " - the senior reads this bay directly."));
    } else {
      react(fam.label + " (" + fam.size + ") attached to bay " + s.n + " - but it is " +
        fam.status + ", with no recorded run on this bench. It stays honestly silent.");
    }
    reactStats();
  }

  function refocusBay(s, sel) {
    var n = document.querySelector('.syn-node[data-sensor="' + s.ch + '"] ' + sel) ||
            document.querySelector(sel);
    if (n) n.focus({ preventScroll: true });
  }

  /* ---- the senior rack + operator, on the channel strip ------------------- */
  function renderSenior() {
    var host = $("wbSenior");
    if (!host) return;
    host.textContent = "";
    var card = el("button", "ws-senior" + (PATCH.senior ? " is-seated" : ""));
    card.type = "button";
    card.setAttribute("role", "switch");
    card.setAttribute("aria-checked", PATCH.senior ? "true" : "false");
    card.setAttribute("aria-label", "Wave Nano in the senior rack");
    var art = el("span", "ws-senior__art");
    art.appendChild(el("span", "wb-plate__ink"));
    art.setAttribute("aria-hidden", "true");
    card.appendChild(art);
    var t = el("span", "ws-senior__txt");
    t.appendChild(el("b", null, PATCH.senior ? "WAVE NANO · SENIOR" : "SENIOR RACK · EMPTY"));
    t.appendChild(el("span", null, PATCH.senior
      ? "adjudicates every doubtful read"
      : "tap to seat Wave Nano - doubtful reads need somewhere to go"));
    card.appendChild(t);
    card.addEventListener("click", function () {
      PATCH.senior = !PATCH.senior;
      derive(); render();
      react(PATCH.senior
        ? "Wave Nano seated in the senior rack - every doubtful read now has somewhere to go."
        : "The senior rack is empty - doubtful reads go unheard.");
      reactStats();
    });
    host.appendChild(card);
  }

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
      derive();
      render(); // the canvas lamp node mirrors the verdict - repaint it too
      react(PATCH.operator
        ? "The operator is on shift, reading the rollups. The ladder ends with a person."
        : "The operator went off shift.");
    });
    host.appendChild(sw);
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
     THE TERMINAL - the selected bay, on the phosphor screen
     The terminal IS the inspector (#wpInspect): selecting a node writes
     its record, its log, and its model's response here.
     ===================================================================== */
  function renderTerm() {
    var s = selected();
    if (s) inspectBay(s);
  }

  function inspectBay(s) {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    var r = recordOf(s);
    var w = r.window || {};

    var head = el("div", "syn-term__head");
    head.appendChild(el("b", null, "BAY " + s.n + " · " + tagOf(s)));
    var rec = el("span", "ws-rec" + (REDUCED || !s.on ? "" : " is-live"));
    rec.appendChild(el("span", "ws-rec__dot"));
    rec.appendChild(el("span", null, s.on ? "MONITORING · REPLAY" : "BAY OFF"));
    rec.title = "the recorded window, replayed - nothing here is live";
    head.appendChild(rec);
    box.appendChild(head);

    if (s.on) {
      var log = el("pre", "ws-log", w.body ||
        "(this record's window was not exported - the log is absent, not invented)");
      log.title = "byte-for-byte, the window the model read in the recorded run";
      box.appendChild(log);
    } else {
      box.appendChild(el("pre", "ws-log ws-log--quiet", "— bay switched off —"));
    }

    var fam = familyById(s.model);
    if (fam && s.on) {
      if (fam.status !== "recorded") {
        box.appendChild(el("p", "wp-note",
          fam.label + " is attached, but " + fam.blurb + ". It attaches, but this bench can " +
          "only replay recorded runs - so it stays honestly silent."));
      } else {
        var rd = readOf(s);
        var reads = el("p", "ws-resp__reads");
        reads.appendChild(el("span", "ws-resp__k", "READS "));
        reads.appendChild(el("span", null, (w.n ? w.n + " samples · " : "") +
          (w.lo != null ? "bounds " + w.lo + " … " + w.hi + (unitOf(s) ? " " + unitOf(s) : "") : "the window above")));
        reads.title = "the recorded window is the model's entire input - it knows the bounds because they are IN the window";
        box.appendChild(reads);
        var says = el("p", "ws-resp__says");
        says.appendChild(el("span", "ws-resp__k", "SAYS "));
        says.appendChild(el("b", null, rd.deadEnd ? "…" : '" ' + rd.said + '"'));
        says.appendChild(el("span", "wp-read__m", " " + rd.margin.toFixed(2)));
        box.appendChild(says);
        var isFault = rd.r.truth !== "none";
        var outcome = rd.deadEnd ? "doubts - nobody to ask"
          : rd.esc ? "doubted, senior answered - " + (rd.ok ? "caught" : (isFault ? "missed" : "false alarm"))
          : (rd.ok ? (isFault ? "asserted - caught" : "asserted - quiet")
                   : (isFault ? "asserted - MISSED" : "asserted - false alarm"));
        box.appendChild(el("p", "ws-resp__mark wp-read__mark--" +
          (rd.deadEnd ? "dead" : rd.ok ? "ok" : "bad"), outcome));
      }
    }

    var dl = el("dl", "wp-cert");
    [["dialed", CONDW[s.cond] || s.cond],
     ["record", r.node_id + " (scene " + r.scene_id + ")"],
     ["unit", unitOf(s) || "not stated in the wire"],
     ["window", w.lo != null ? w.lo + " … " + w.hi + " · mean " + w.mean : "—"],
     ["truth", r.truth],
     ["child said", r.child.prediction + " · margin " + r.child.margin.toFixed(2)],
     ["senior said", r.parent.prediction + " · margin " + r.parent.margin.toFixed(2)],
    ].forEach(function (row) {
      dl.appendChild(el("dt", null, row[0]));
      dl.appendChild(el("dd", null, row[1]));
    });
    box.appendChild(dl);
    box.appendChild(el("p", "wp-note",
      "The log above is byte-for-byte the window the model read in the recorded run. " +
      "The pads are the conditions recorded for this channel slot - each pad replays " +
      "one real record. The margins are recorded logprob differences; nothing in this " +
      "browser computes one."));
    if (PATCH.scene && r.scene_id === PATCH.scene.scene_id) {
      drawScopeInto(box);
    }
  }

  function inspectYourData(){
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    var head = el("div", "syn-term__head");
    head.appendChild(el("b", null, "YOUR DATA"));
    head.appendChild(el("span", "wp-tag wp-tag--draft", "DRAFT · NOT RUN"));
    box.appendChild(head);
    box.appendChild(el("p", "wp-note",
      "Your bytes are on the bench but nothing ran - no model executes in a browser, " +
      "and a margin is a logprob difference nothing here can compute. This is the exact " +
      "request that would go to a stock llama-server:"));
    box.appendChild(el("pre", "wp-wirebytes", envelopeFor(PATCH.yourData)));
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
    PATCH.sensors.forEach(function (s) {
      var fam = familyById(s.model);
      m.appendChild(el("li", null, "Bay " + s.n + " (" + tagOf(s) + "): " +
        (s.on ? "on line, dialed " + (CONDW[s.cond] || s.cond) : "off") +
        (fam ? ", reading: " + fam.label + (fam.id === "pico" ? " at floor " + s.floor.toFixed(1) : "")
             : ", writing to log only")));
    });
    m.appendChild(el("li", null, "Senior rack: " + (PATCH.senior ? "Wave Nano seated" : "empty")));
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
    closeIntake();
    derive();
    render();
    react("Your channel is on the rack as a DRAFT - tap its bay for the request envelope.");
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
      buildSensors();
      if (PATCH.sensors[0]) PATCH.sel = PATCH.sensors[0].ch;
      derive();
      render();
      react("The panel is live: " + PATCH.sensors.length + " recorded instruments, writing to " +
        "their logs. Tap a node's [+] stub to attach a model; tap a node to see its log.");
      document.addEventListener("keydown", onKey);
      wireIntake();
    }).catch(fail);
  }

  function onKey(e) {
    if (e.key === "Escape") {
      var drawer = $("wpIntake");
      if (drawer && !drawer.hidden) {
        closeIntake();
        var back = document.querySelector(".syn-node--intake");
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
    var s = $("wsRack");
    if (s) s.appendChild(el("p", "wp-note", "The bench data did not load. Nothing here is live; reload to try again."));
  }

  function maybeBoot() {
    var v = $("pgMeshView");
    if (v && !v.hidden && !PATCH.booted && $("wsRack")) { PATCH.booted = true; boot(); }
  }

  // Pure-function hook so tests can EXECUTE the classifier and the derivation.
  if (typeof window !== "undefined") {
    window.__wavePatchTest = {
      classify: classify,
      setScene: function (sc, cat) { PATCH.scene = sc; PATCH.catalog = cat; },
      setMeasured: function (m) { PATCH.measured = m; },
      buildSensors: buildSensors,
      derive: derive,
      readOf: readOf,
      attach: attach,
      envelopeFor: envelopeFor,
      state: PATCH,
      family: FAMILY,
      detents: DETENTS,
    };
  }

  function start() { wireModeSwitch(); maybeBoot(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
