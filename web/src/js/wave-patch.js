/* =====================================================================
   RogerAI - THE SIGNAL BENCH (Playbox / WAVE MESH)

   A wall of recorded sensors, a radio, and a console - built so a five
   year old on a tablet could play it, and an engineer could trust it.

   THE CONCEPT (founder direction 2026-08-13, third revision):

   SENSORS ON THE LEFT, WITH SWITCHES. Six sensor cards, each one a real
     recorded channel from the measured fleet run. A big lever turns the
     sensor ON or OFF. A CONDITION dial sets what the sensor is doing -
     and its positions are exactly the conditions that exist in the
     recorded data for that channel (OK, plus the recorded fault kinds).
     The dial physically cannot ask for an unrecorded condition: every
     position IS a recorded instance, selected, not simulated.

   A RADIO IN THE MIDDLE, ANY MODEL IN THE SOCKET. The whole Wave family
     is on the shelf - seat any of them. The two slots with a recorded
     run on this bench (the Pico reader and the Nano senior) replay
     their actual recorded outputs; the rest seat honestly as "no
     recorded run on this bench" and the lamp refuses to glow for them.
     Seat a Pico and its floor knob appears (detents = the measured
     floors); seat the Nano behind it and doubtful reads get a senior.

   A CONSOLE ON THE RIGHT THAT SPEAKS. For every live sensor, one line:
     what the model SAID (the recorded prediction), its margin, and what
     happened - asserted, escalated, caught, missed. Above it, the
     founder-mandated green/yellow/red lamp window; below it, the chart
     strip narrating every move you make.

   HONESTY, unchanged: no model executes in a browser. A margin is a
     logprob difference nothing here can compute. Every word the console
     prints is a FIELD of a recorded record - the dial picks the record,
     the knob picks the branch, arithmetic does the rest.
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

  /* ---- the model shelf: the WHOLE family, honestly labelled -------------
     Sizes and statuses are the family page's own (research-wave-family).
     Only two slots have a recorded run on THIS bench: the reader the
     records call `child` (deck name Wave Pico) and the senior they call
     `parent` (Wave Nano). Everyone else seats, and says why they are
     silent - a shelf that hid the rest of the family would be a lie of
     omission about our own roadmap. */
  var FAMILY = [
    { id: "pico", label: "Wave Pico", size: "270M", status: "recorded",
      role: "reader", blurb: "the recorded reader on this bench" },
    { id: "nano", label: "Wave Nano", size: "~350M", status: "recorded",
      role: "senior", blurb: "the recorded senior - adjudicates doubtful reads" },
    { id: "edge", label: "Roger Edge", size: "KB-10M", status: "in design",
      role: "none", blurb: "wake and sensing tier - in design" },
    { id: "micro", label: "Wave Micro", size: "1-8B", status: "trained",
      role: "none", blurb: "trained, but has no recorded run on this bench" },
    { id: "core", label: "Wave Core", size: "8-30B", status: "planned",
      role: "none", blurb: "planned slot" },
    { id: "station", label: "Wave Station", size: "30-70B", status: "planned",
      role: "none", blurb: "planned slot" },
    { id: "satellite", label: "Wave Satellite", size: "~70B+", status: "planned",
      role: "none", blurb: "planned slot" },
  ];
  function familyById(id) {
    for (var i = 0; i < FAMILY.length; i++) if (FAMILY[i].id === id) return FAMILY[i];
    return null;
  }

  var PATCH = {
    catalog: null, measured: null, scene: null,
    sensors: [],          // built from the records at boot
    reader: null,         // FAMILY id seated in the radio, or null
    senior: false,        // Wave Nano seated behind a Pico reader
    operator: false,      // the console's ON SHIFT switch
    floor: 1.5,           // the Pico's margin floor (knob, measured detents)
    yourData: null,       // the intake's pasted channel, if any
    verdict: null,
    booted: false,
    step: 0,              // strip line counter
  };

  /* ---- sensors: built FROM the records, never beside them ----------------
     Each sensor is one recorded channel (c00..c05). Its dial positions are
     the truths that actually occur for that channel in the 120-record
     sample, each mapped to the FIRST record carrying it - a deterministic
     pick, so the same dial position always replays the same recorded
     instance. */
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
      // OK first, then the recorded fault kinds in a stable order
      var conds = Object.keys(byCh[ch]).sort(function (a, b) {
        if (a === "none") return -1;
        if (b === "none") return 1;
        return a < b ? -1 : 1;
      });
      return { ch: ch, n: i + 1, conds: conds, recIdx: byCh[ch],
               on: i < 2, cond: "none" };
    });
  }
  function liveSensors() {
    return PATCH.sensors.filter(function (s) { return s.on; });
  }
  function recordOf(s) {
    return PATCH.measured.records[s.recIdx[s.cond]];
  }
  var CONDW = { none: "OK" }; // dial word for the healthy position

  /* =====================================================================
     THE DERIVATION - one recorded record per live sensor, recounted
     ===================================================================== */
  function readOf(s) {
    // What the seated chain says about THIS sensor's dialed record. Every
    // field returned here is a field of the record or arithmetic on it.
    var r = recordOf(s);
    var fam = familyById(PATCH.reader);
    if (!fam) return { s: s, r: r, nodata: true, silent: "no model seated" };
    if (fam.status !== "recorded") {
      return { s: s, r: r, nodata: true,
               silent: fam.label + " has no recorded run on this bench" };
    }
    if (fam.id === "nano") {
      // parent-direct: the measured config where the senior reads everything
      var okN = r.parent.prediction === r.truth;
      return { s: s, r: r, said: r.parent.prediction, margin: r.parent.margin,
               via: "reads direct", esc: false, ok: okN };
    }
    // pico: assert or escalate at the knob's floor
    if (r.child.margin >= PATCH.floor) {
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
    var fam = familyById(PATCH.reader);

    var t = { live: live.length, faults: 0, caught: 0, missed: 0, fixable: 0,
              deadEnd: 0, falseAlarms: 0, escalated: 0 };
    reads.forEach(function (rd) {
      if (rd.nodata) return;
      var isFault = rd.r.truth !== "none";
      if (isFault) t.faults++;
      if (rd.esc) t.escalated++;
      if (rd.deadEnd) { if (isFault) { t.missed++; } t.deadEnd++; return; }
      if (rd.ok) { if (isFault) t.caught++; return; }
      if (isFault) {
        t.missed++;
        // FIXABLE: a higher detent would have escalated this read, and the
        // recorded senior had the right answer. Everything else missed is
        // the ladder's measured ceiling on this dialed set.
        if (!rd.esc && rd.r.parent.prediction === rd.r.truth && rd.r.child.margin < TOP) {
          t.fixable++;
        }
      } else {
        t.falseAlarms++;
      }
    });

    var state, why, label = null;
    if (!fam) {
      state = "off"; why = "Seat a model in the radio - the shelf is above the bench.";
    } else if (fam.status !== "recorded") {
      state = "off";
      why = fam.label + " (" + fam.size + ") is " + fam.status + " - it has no recorded run " +
        "on this bench, and this lamp only glows for recounts of recorded records. " +
        "Seat Wave Pico or Wave Nano to hear the recorded fleet.";
    } else if (!t.live) {
      state = "off"; why = "Every sensor is switched off. Flip a lever on the wall.";
    } else if (t.deadEnd > 0 && t.missed > 0) {
      state = "red";
      why = t.missed + " dialed fault" + (t.missed === 1 ? "" : "s") + " missed and " +
        t.deadEnd + " doubtful read" + (t.deadEnd === 1 ? "" : "s") + " with nobody to ask - " +
        "seat the Wave Nano behind the Pico.";
    } else if (t.fixable > 0) {
      state = "red";
      why = t.fixable + " dialed fault" + (t.fixable === 1 ? "" : "s") + " missed that a higher " +
        "floor would have escalated - and the recorded senior had the right answer. " +
        "Raise the FLOOR knob on the radio.";
    } else if (t.deadEnd > 0) {
      state = "yellow";
      why = t.deadEnd + " doubtful read" + (t.deadEnd === 1 ? "" : "s") + " with nobody to ask - " +
        "seat the Wave Nano behind the Pico.";
    } else if (!PATCH.operator) {
      state = "yellow";
      why = "The chain works, but no operator is on shift - flip the console switch; " +
        "the ladder should end with a person.";
    } else if (t.missed === 0) {
      state = "green";
      why = t.faults
        ? "Complete chain: every dialed fault caught, operator on shift."
        : "All quiet: every live sensor dialed OK, and the chain agrees.";
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

  // WHAT IT SAYS: one printed line per live sensor - the recorded
  // prediction, its margin, and the outcome. This is the "preview of what
  // the output would end up saying" - and it is a replay, never a run.
  function paintReads() {
    var host = $("wpReads");
    if (!host) return;
    host.textContent = "";
    var v = PATCH.verdict;
    if (!v) return;
    var fam = familyById(PATCH.reader);
    if (!fam) {
      host.appendChild(el("li", "wp-read wp-read--quiet", "— seat a model to hear the sensors —"));
      return;
    }
    if (fam.status !== "recorded") {
      host.appendChild(el("li", "wp-read wp-read--quiet",
        "— " + fam.label + ": no recorded outputs for this slot on this bench —"));
      return;
    }
    v.reads.forEach(function (rd) {
      var li = el("li", "wp-read" + (rd.esc ? " wp-read--esc" : ""));
      li.appendChild(el("b", null, "S" + rd.s.n));
      li.appendChild(el("span", "wp-read__cond", CONDW[rd.s.cond] || rd.s.cond.toUpperCase()));
      var chain = rd.deadEnd
        ? '" ?"'
        : '" ' + rd.said + '"';
      li.appendChild(el("span", "wp-read__said", rd.via + " " + chain));
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
    if (PATCH.yourData) {
      var li2 = el("li", "wp-read wp-read--quiet");
      li2.appendChild(el("b", null, "YOU"));
      li2.appendChild(el("span", "wp-read__said",
        "your channel · DRAFT - nothing ran; tap YOUR DATA for the request envelope"));
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
    if (!v || !familyById(PATCH.reader) || familyById(PATCH.reader).status !== "recorded") return;
    var t = v.totals;
    var bits = [t.live + " sensor" + (t.live === 1 ? "" : "s") + " live"];
    if (t.faults) bits.push(t.faults + " dialed fault" + (t.faults === 1 ? "" : "s"));
    if (t.caught) bits.push(t.caught + " caught");
    if (t.missed) bits.push(t.missed + " missed");
    if (t.deadEnd) bits.push(t.deadEnd + " unheard");
    if (t.falseAlarms) bits.push(t.falseAlarms + " false alarm" + (t.falseAlarms === 1 ? "" : "s"));
    react(bits.join(" · "));
  }

  /* =====================================================================
     THE DIAL - one rotary control, used for conditions and the floor.
     Tap to step to the next position (the tablet path), drag vertically
     for detents, arrow keys for the keyboard. Positions are DATA - the
     dial renders whatever recorded positions it is given.
     ===================================================================== */
  function drawDial(opts) {
    // opts: { values, labels, index, name, tip(i), onset(i), size }
    var size = opts.size || 64;
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
    // The tablet path: a plain tap steps the dial one position (wrapping),
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
     RENDER - the wall, the radio, the console
     ===================================================================== */
  function render() {
    renderWall();
    renderRadio();
    renderOp();
    renderMirror();
    requestAnimationFrame(drawCables);
  }

  function renderWall() {
    var host = $("wbWall");
    if (!host) return;
    host.textContent = "";
    PATCH.sensors.forEach(function (s) {
      var card = el("div", "wb-sensor" + (s.on ? " is-on" : ""));
      card.dataset.sensor = s.ch;

      var art = el("div", "wb-sensor__art");
      art.appendChild(el("span", "wb-plate__ink"));
      art.setAttribute("aria-hidden", "true");
      card.appendChild(art);

      var head = el("div", "wb-sensor__head");
      head.appendChild(el("b", null, "SENSOR " + s.n));
      head.appendChild(el("span", "wb-sensor__ch", "recorded channel " + s.ch));
      card.appendChild(head);

      // the lever: a big ON/OFF switch
      var sw = el("button", "wb-lever" + (s.on ? " is-on" : ""));
      sw.type = "button";
      sw.setAttribute("role", "switch");
      sw.setAttribute("aria-checked", s.on ? "true" : "false");
      sw.setAttribute("aria-label", "Sensor " + s.n + " on line");
      sw.appendChild(el("span", "wb-lever__k", s.on ? "ON LINE" : "OFF"));
      sw.appendChild(el("span", "wb-lever__pip"));
      sw.addEventListener("click", function (e) {
        e.stopPropagation();
        s.on = !s.on;
        derive();
        render();
        react("Sensor " + s.n + (s.on
          ? " on line - dialed " + (CONDW[s.cond] || s.cond.toUpperCase()) + "."
          : " off line."));
        reactStats();
        refocusSensor(s, ".wb-lever");
      });
      card.appendChild(sw);

      // the condition dial: positions = the recorded conditions, only
      var dial = drawDial({
        values: s.conds,
        labels: s.conds.map(function (c) { return CONDW[c] || c.toUpperCase(); }),
        index: Math.max(0, s.conds.indexOf(s.cond)),
        name: "Sensor " + s.n + " condition",
        tip: function (i) {
          var r = PATCH.measured.records[s.recIdx[s.conds[i]]];
          return "replays recorded record " + r.node_id + " (scene " + r.scene_id +
            ") - every position on this dial is a recorded instance, selected, not simulated";
        },
        onset: function (i) {
          s.cond = s.conds[i];
          if (!s.on) { s.on = true; }
          derive();
          render();
          react("Sensor " + s.n + " dialed " + (CONDW[s.cond] || s.cond.toUpperCase()) + ".");
          reactStats();
          refocusSensor(s, ".wp-knob");
        },
      });
      var dwrap = el("div", "wb-sensor__dial" + (s.on ? "" : " is-dim"));
      dwrap.appendChild(dial);
      card.appendChild(dwrap);

      card.addEventListener("click", function () { inspectSensor(s); });
      host.appendChild(card);
    });

    // YOUR DATA: the intake as the seventh sensor on the wall
    var yd = el("div", "wb-sensor wb-sensor--intake");
    yd.tabIndex = 0;
    yd.setAttribute("role", "button");
    yd.setAttribute("aria-label", "Your data - paste what your plant emits");
    var art2 = el("div", "wb-sensor__art wb-sensor__art--tape");
    art2.appendChild(el("span", "wb-plate__ink"));
    art2.setAttribute("aria-hidden", "true");
    yd.appendChild(art2);
    var h2 = el("div", "wb-sensor__head");
    h2.appendChild(el("b", null, "YOUR DATA"));
    h2.appendChild(el("span", "wb-sensor__ch", PATCH.yourData
      ? "PASTED · " + (PATCH.yourData.modality || "").toUpperCase()
      : "paste / drop - read by the shim"));
    yd.appendChild(h2);
    yd.addEventListener("click", function () { openIntake(); });
    yd.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openIntake(); }
    });
    yd.addEventListener("dragover", function (e) { e.preventDefault(); });
    yd.addEventListener("drop", function (e) {
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
    host.appendChild(yd);
  }

  function refocusSensor(s, sel) {
    var card = document.querySelector('.wb-sensor[data-sensor="' + s.ch + '"] ' + sel);
    if (card) card.focus({ preventScroll: true });
  }

  function renderShelf() {
    var host = $("wpShelf2");
    if (!host) return;
    host.textContent = "";
    FAMILY.forEach(function (f) {
      var seated = PATCH.reader === f.id || (f.id === "nano" && PATCH.senior);
      var chip = el("button", "wb-chip" + (seated ? " is-seated" : ""));
      chip.type = "button";
      chip.setAttribute("aria-pressed", seated ? "true" : "false");
      chip.appendChild(el("b", null, f.label));
      chip.appendChild(el("span", null, f.size + " · " + f.status));
      chip.title = f.blurb;
      chip.addEventListener("click", function () { seat(f); });
      host.appendChild(chip);
    });
  }

  function seat(f) {
    if (f.id === "nano" && PATCH.reader === "pico") {
      PATCH.senior = !PATCH.senior;
      derive(); render(); renderShelf();
      react(PATCH.senior
        ? "Wave Nano seated as the senior - doubtful reads have somewhere to go."
        : "Wave Nano unseated.");
      reactStats();
      return;
    }
    if (PATCH.reader === f.id) {
      PATCH.reader = null; PATCH.senior = false;
      derive(); render(); renderShelf();
      react(f.label + " unseated - the radio is empty.");
      return;
    }
    PATCH.reader = f.id;
    if (f.id !== "pico") PATCH.senior = false;
    derive(); render(); renderShelf();
    if (f.status === "recorded") {
      react(f.label + " seated" + (f.id === "pico"
        ? " - reading the wall. Turn its FLOOR knob, or seat the Nano behind it."
        : " - the senior reads every sensor directly."));
    } else {
      react(f.label + " (" + f.size + ") seated - but it is " + f.status +
        ", with no recorded run on this bench. The console stays honest and quiet.");
    }
    reactStats();
  }

  function renderRadio() {
    var host = $("wbMid");
    if (!host) return;
    host.textContent = "";

    var radio = el("div", "wb-radio" + (PATCH.reader ? " is-seated" : ""));
    var art = el("div", "wb-radio__art");
    art.appendChild(el("span", "wb-plate__ink"));
    art.appendChild(el("span", "wb-plate__spot"));
    art.setAttribute("aria-hidden", "true");
    radio.appendChild(art);

    var face = el("div", "wb-radio__face");
    var fam = familyById(PATCH.reader);
    if (!fam) {
      face.appendChild(el("b", null, "THE RADIO"));
      face.appendChild(el("span", "wp-note", "empty socket - tap a model on the shelf"));
    } else {
      face.appendChild(el("b", null, fam.label.toUpperCase()));
      face.appendChild(el("span", "wb-radio__sub", fam.size + " · " + fam.status +
        (fam.status === "recorded" ? " · digest pending export" : "")));
      if (fam.id === "pico") {
        // the floor knob lives on the radio's faceplate
        var dial = drawDial({
          values: DETENTS.slice(),
          labels: DETENTS.map(function (d) { return "FLOOR " + d.toFixed(1); }),
          index: Math.max(0, DETENTS.indexOf(PATCH.floor)),
          name: "margin floor",
          tip: function (i) { return knobTip(DETENTS[i]); },
          onset: function (i) {
            PATCH.floor = DETENTS[i];
            derive();
            react("Floor set to " + PATCH.floor.toFixed(1) + " - margins below it now escalate.");
            reactStats();
            requestAnimationFrame(drawCables);
          },
        });
        face.appendChild(dial);
      }
    }
    radio.appendChild(face);
    radio.addEventListener("click", function (e) {
      if (e.target.closest(".wp-knob")) return;
      inspectRadio();
    });
    host.appendChild(radio);

    // the senior's rack, behind the reader
    if (PATCH.reader === "pico") {
      var sr = el("div", "wb-senior" + (PATCH.senior ? " is-seated" : ""));
      var art2 = el("div", "wb-senior__art");
      art2.appendChild(el("span", "wb-plate__ink"));
      art2.setAttribute("aria-hidden", "true");
      sr.appendChild(art2);
      sr.appendChild(el("b", null, PATCH.senior ? "WAVE NANO · SENIOR" : "SENIOR RACK · EMPTY"));
      sr.appendChild(el("span", "wp-note", PATCH.senior
        ? "adjudicates every doubtful read"
        : "tap Wave Nano on the shelf to seat the senior"));
      host.appendChild(sr);
    }

    var cables = $("wbCables");
    if (!cables) {
      cables = svg("svg", { id: "wbCables", class: "wb-cables", "aria-hidden": "true" });
      var bench = $("wbBench");
      if (bench) bench.appendChild(cables);
    }
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
      renderOp();
      react(PATCH.operator
        ? "The operator is on shift, reading the rollups. The ladder ends with a person."
        : "The operator went off shift.");
    });
    host.appendChild(sw);
  }

  /* ---- the cables: drawn from live geometry, after layout ----------------
     Cables connect what IS connected - each live sensor to the radio, the
     radio to the senior. They are redrawn from the DOM's real positions, so
     they stay true through resizes and re-renders. */
  function drawCables() {
    var cables = $("wbCables"), bench = $("wbBench");
    if (!cables || !bench) return;
    cables.textContent = "";
    var bb = bench.getBoundingClientRect();
    var radio = document.querySelector(".wb-radio");
    if (!radio) return;
    var rb = radio.getBoundingClientRect();
    var rx = rb.left - bb.left, ry = rb.top - bb.top + rb.height / 2;

    liveSensors().forEach(function (s) {
      var card = document.querySelector('.wb-sensor[data-sensor="' + s.ch + '"]');
      if (!card) return;
      var cb = card.getBoundingClientRect();
      var x1 = cb.right - bb.left, y1 = cb.top - bb.top + cb.height / 2;
      var mid = (x1 + rx) / 2;
      var d = "M" + x1 + " " + y1 + " C" + mid + " " + y1 + ", " + mid + " " + ry + ", " + rx + " " + ry;
      var p = svg("path", { class: "wb-cable", d: d });
      cables.appendChild(p);
    });
    var senior = document.querySelector(".wb-senior.is-seated");
    if (senior) {
      var sb = senior.getBoundingClientRect();
      var x2 = rb.right - bb.left, y2 = ry;
      var x3 = sb.left - bb.left, y3 = sb.top - bb.top + sb.height / 2;
      var mid2 = (x2 + x3) / 2;
      cables.appendChild(svg("path", {
        class: "wb-cable wb-cable--esc",
        d: "M" + x2 + " " + y2 + " C" + mid2 + " " + y2 + ", " + mid2 + " " + y3 + ", " + x3 + " " + y3,
      }));
    }
    cables.setAttribute("width", bb.width);
    cables.setAttribute("height", bb.height);
    cables.setAttribute("viewBox", "0 0 " + bb.width + " " + bb.height);
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
     THE INSPECTOR - certificates, records, the envelope
     ===================================================================== */
  function revealInspector() {
    var panel = $("wpInspect");
    if (panel && panel.scrollIntoView) panel.scrollIntoView({ block: "nearest", behavior: REDUCED ? "auto" : "smooth" });
  }

  function inspectSensor(s) {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    var r = recordOf(s);
    box.appendChild(el("b", null, "Sensor " + s.n + " · recorded channel " + s.ch));
    var dl = el("dl", "wp-cert");
    [["dialed", CONDW[s.cond] || s.cond],
     ["record", r.node_id + " (scene " + r.scene_id + ")"],
     ["truth", r.truth],
     ["child said", r.child.prediction + " · margin " + r.child.margin.toFixed(2)],
     ["senior said", r.parent.prediction + " · margin " + r.parent.margin.toFixed(2)],
    ].forEach(function (row) {
      dl.appendChild(el("dt", null, row[0]));
      dl.appendChild(el("dd", null, row[1]));
    });
    box.appendChild(dl);
    box.appendChild(el("p", "wp-note",
      "The dial's positions are the conditions recorded for this channel in the " +
      "measured sample - each position replays one real record. The margins above " +
      "are the recorded logprob differences; nothing in this browser computes one."));
    if (PATCH.scene && r.scene_id === PATCH.scene.scene_id) {
      drawScopeInto(box);
    }
    revealInspector();
  }

  function inspectRadio() {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    var fam = familyById(PATCH.reader);
    if (!fam) {
      box.appendChild(el("b", null, "The radio"));
      box.appendChild(el("p", "wp-note",
        "An empty socket. The shelf above the bench carries the whole Wave family - " +
        "tap one to seat it. Only slots with a recorded run on this bench can speak."));
      revealInspector();
      return;
    }
    box.appendChild(el("b", null, fam.label));
    var dl = el("dl", "wp-cert");
    var rows = [["tier", fam.id], ["size", fam.size], ["status", fam.status]];
    if (fam.status === "recorded") {
      rows.push(["run", fam.id === "pico"
        ? shortName(PATCH.measured.escalation.child)
        : shortName(PATCH.measured.escalation.parent)]);
      rows.push(["bench", shortName(PATCH.measured.escalation.bench)]);
      rows.push(["suite", PATCH.measured._provenance.suite]);
    }
    rows.forEach(function (row) {
      dl.appendChild(el("dt", null, row[0]));
      dl.appendChild(el("dd", null, row[1]));
    });
    dl.appendChild(el("dt", null, "digest"));
    dl.appendChild(el("dd", "wp-cert__pending", "— pending export —"));
    box.appendChild(dl);
    if (fam.id === "pico") {
      box.appendChild(el("p", "wp-note", knobTip(PATCH.floor)));
    }
    if (fam.status !== "recorded") {
      box.appendChild(el("p", "wp-note",
        fam.blurb + ". Numbers appear here only with a recorded run behind them."));
    }
    if (PATCH.yourData && fam.status === "recorded") {
      box.appendChild(el("p", "wp-tag wp-tag--draft", "DRAFT · NOT RUN"));
      box.appendChild(el("p", "wp-note",
        "Your bytes are patched in but nothing ran - no model executes in a browser, and " +
        "a margin is a logprob difference nothing here can compute. This is the exact " +
        "request that would go to a stock llama-server:"));
      var pre = el("pre", "wp-wirebytes", envelopeFor(PATCH.yourData));
      box.appendChild(pre);
    }
    revealInspector();
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
      "The committed seed-42 pump scene: " + sc.steps.length + " recorded steps, fault onset at the dashed line. " +
      "REPLAY - " + (sc._provenance && sc._provenance.label ? "recorded" : "recorded") + ", never live."));
  }

  function shortName(p) { return String(p || "").split("/").pop(); }

  /* ---- the a11y mirror: the same bench as a list ------------------------- */
  function renderMirror() {
    var m = $("wpMirror");
    if (!m) return;
    m.textContent = "";
    PATCH.sensors.forEach(function (s) {
      m.appendChild(el("li", null, "Sensor " + s.n + " (" + s.ch + "): " +
        (s.on ? "on line, dialed " + (CONDW[s.cond] || s.cond) : "off")));
    });
    var fam = familyById(PATCH.reader);
    m.appendChild(el("li", null, fam
      ? "Radio: " + fam.label + " (" + fam.status + ")" +
        (fam.id === "pico" ? ", floor " + PATCH.floor.toFixed(1) : "")
      : "Radio: empty socket"));
    m.appendChild(el("li", null, "Senior: " + (PATCH.senior ? "Wave Nano seated" : "empty")));
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
      frame.textContent = "T01 sensor-health · frame text — pending export —";
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
      frame.textContent = "T01 sensor-health · frame text — pending export —";
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
    react(familyById(PATCH.reader)
      ? "Your channel is patched in as a DRAFT - tap the radio for the request envelope."
      : "Your channel is on the wall. Seat a model and tap the radio for the envelope.");
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
    var req = [
      "{",
      '  "prompt": "<task frame — pending export — followed by the input body>",',
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
    return notes.join("\n") + "\n" + body;
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
      renderShelf();
      derive();
      render();
      react("The wall is live: " + PATCH.sensors.length + " recorded sensors. " +
        "Flip a lever, spin a dial, seat a model.");
      document.addEventListener("keydown", onKey);
      window.addEventListener("resize", function () { requestAnimationFrame(drawCables); });
      wireIntake();
    }).catch(fail);
  }

  function onKey(e) {
    if (e.key === "Escape") {
      var drawer = $("wpIntake");
      if (drawer && !drawer.hidden) {
        closeIntake();
        var back = document.querySelector(".wb-sensor--intake");
        if (back) back.focus();
      }
    }
  }

  function fail() {
    var s = $("wbWall");
    if (s) s.appendChild(el("p", "wp-note", "The bench data did not load. Nothing here is live; reload to try again."));
  }

  function maybeBoot() {
    var v = $("pgMeshView");
    if (v && !v.hidden && !PATCH.booted && $("wbWall")) { PATCH.booted = true; boot(); }
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
      seat: seat,
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
