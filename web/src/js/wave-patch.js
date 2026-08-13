/* =====================================================================
   RogerAI - THE PATCH SHEET (Playbox / WAVE MESH) - the reactive bench

   Build a mesh like an engineer: start with the plant feed, drag models
   in, and the sheet wires itself - drop a Pico and it reads the feed,
   drop a Nano and every Pico reports to it, drop the Operator and they
   read the Nano's rollups. Turn the floor knob on a Pico, slide the
   channels-online fader on the feed, and the chain's END - a status
   lamp and a chart strip - reacts to every move.

   THE CONCEPT CALLS (founder direction 2026-08-14):

   NO TEMPLATES, NO MANUAL CABLING. The sheet starts as just the signal.
     Wiring is knowledge the sheet already has - a Pico can only read the
     feed, a Nano can only adjudicate Picos, an Operator can only read
     rollups - so making the visitor draw those cables was work without a
     decision in it. Dropping a module IS the decision; the cable follows.

   FAN-IN IS THE MESH. A Nano takes MANY children - that is the whole
     deployment law (picos + parent at the floor). Multiple Picos
     PARTITION the recorded fleet between them, so every number anywhere
     is still arithmetic on recorded records.

   KNOBS, NOT NUMBERS. The margin floor is a rotary knob with DETENTS at
     exactly the four measured floors (0.5 / 1.0 / 1.5 / 2.0) - the knob
     cannot be set to a floor nothing measured. Numbers live in tooltips
     and the inspector; the faceplate carries the instrument.

   THE CHAIN ENDS IN A LAMP AND A STRIP. Green: complete chain, every
     recorded fault caught. Yellow: escalations with nobody to hear them,
     or no operator at the top. Red: faults missed on the recorded
     sample. The strip prints one line per reaction - a chart recorder,
     not a chat. The founder asked for green/yellow/red explicitly; the
     lamp also carries NE 107 shapes so colour is never the only signal.

   HONESTY, unchanged: everything is derived from the 120 recorded
     records (truth, child prediction + margin, parent prediction +
     margin). Missed / caught / escalated are recounts of recorded facts
     at the knob's floor - never a model call, never an invented number.
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

  /* ---- geometry: typed columns, snapped slots -------------------------- */
  var TILE_W = 190, TILE_H = 118, GAP_X = 66, GAP_Y = 24;
  var COLS = ["feed", "pico", "nano", "human"];   // left -> right
  var COL_PITCH = TILE_W + GAP_X;
  var SLOT_PITCH = TILE_H + GAP_Y;
  var PAD_X = 30, PAD_Y = 34;
  var MAX_SLOTS = 3;

  // The four measured floors are the knob's detents - the knob cannot be set
  // to a floor nothing measured.
  var DETENTS = [0.5, 1.0, 1.5, 2.0];

  function colX(tier) {
    var i = COLS.indexOf(tier);
    return PAD_X + (i < 0 ? 0 : i) * COL_PITCH;
  }
  function slotY(slot) { return PAD_Y + slot * SLOT_PITCH; }

  var PATCH = {
    catalog: null, measured: null, scene: null,
    tiles: [], wires: [], seq: 1,
    selected: null, focusId: null, refocus: false,
    dragMoved: false, booted: false,
    online: 30,          // the feed fader: how many recorded channels are live
    step: 0,             // strip line counter
  };

  /* ---- the module rail -------------------------------------------------- */
  var MODULES = [
    { kind: "model", tier: "pico", label: "Wave Pico", floor: 1.5, frame: "A",
      blurb: "channel reader · 98M", max: 3 },
    { kind: "model", tier: "nano", label: "Wave Nano", floor: 2.5, frame: "B",
      blurb: "adjudicates its children", max: 1 },
    { kind: "human", tier: "human", label: "Operator", blurb: "reads the rollups", max: 1 },
  ];

  function tile(kind, tier, slot, data) {
    return { id: "t" + (PATCH.seq++), kind: kind, tier: tier, slot: slot,
             data: data || {}, lamp: "idle", stats: null };
  }

  function tilesOf(tier) {
    return PATCH.tiles.filter(function (t) { return t.tier === tier; })
      .sort(function (a, b) { return a.slot - b.slot; });
  }
  function tileById(id) {
    for (var i = 0; i < PATCH.tiles.length; i++) if (PATCH.tiles[i].id === id) return PATCH.tiles[i];
    return null;
  }

  /* =====================================================================
     THE START STATE - just the signal

     The feed is the recorded fleet itself: many channels from many scenes
     of the measured bench. (The old single-device framing quietly credited
     one pump with records that span a whole fleet - this is more honest as
     well as simpler.) The intake tile for the visitor's own bytes sits
     beneath it, unchanged.
     ===================================================================== */
  function bootSheet() {
    PATCH.tiles = [];
    PATCH.wires = [];
    PATCH.seq = 1;
    PATCH.tiles.push(tile("feed", "feed", 0, { label: "Plant Feed" }));
    PATCH.tiles.push(tile("intake", "feed", 1, { label: "Your Data", channels: [] }));
    render();
    react("The plant feed is live: " + PATCH.online + " recorded channels. Drag a Wave Pico in to read them.");
  }

  /* =====================================================================
     AUTO-WIRING - the sheet knows the topology

     A wire is never drawn by hand. rewire() derives the entire cable set
     from what is on the sheet: feed -> every pico, every pico -> the nano,
     nano -> operator. Removing a module removes its cables the same way.
     ===================================================================== */
  function rewire() {
    PATCH.wires = [];
    var feed = tilesOf("feed").filter(function (t) { return t.kind === "feed"; })[0];
    var intake = PATCH.tiles.filter(function (t) { return t.kind === "intake" && t.data.body; })[0];
    var picos = tilesOf("pico");
    var nano = tilesOf("nano")[0];
    var human = tilesOf("human")[0];

    picos.forEach(function (p) {
      if (feed) PATCH.wires.push({ id: "w" + (PATCH.seq++), fromId: feed.id, toId: p.id, kind: "channel" });
    });
    // The visitor's bytes ride into the FIRST pico as a draft channel.
    if (intake && picos[0]) {
      PATCH.wires.push({ id: "w" + (PATCH.seq++), fromId: intake.id, toId: picos[0].id, kind: "channel", user: true });
    }
    if (nano) {
      picos.forEach(function (p) {
        PATCH.wires.push({ id: "w" + (PATCH.seq++), fromId: p.id, toId: nano.id, kind: "escalate" });
      });
    }
    if (human && nano) {
      PATCH.wires.push({ id: "w" + (PATCH.seq++), fromId: nano.id, toId: human.id, kind: "escalate" });
    }
  }

  function addModule(mod, slot) {
    // The rules a plant engineer would recognise, enforced at the drop:
    if (mod.tier === "human" && !tilesOf("nano").length) {
      react("An operator reads rollups, not raw channels - add a Wave Nano first.");
      return null;
    }
    var have = tilesOf(mod.tier).length;
    if (have >= mod.max) {
      react(mod.max === 1 ? "One " + mod.label + " is the mesh's shape here." : "That column is full.");
      return null;
    }
    var used = {};
    tilesOf(mod.tier).forEach(function (t) { used[t.slot] = true; });
    var s = (slot != null && slot >= 0 && !used[slot]) ? slot : -1;
    if (s < 0) { for (var i = 0; i < MAX_SLOTS; i++) if (!used[i]) { s = i; break; } }
    if (s < 0) { react("That column is full."); return null; }

    var t = tile(mod.kind, mod.tier, s, {
      label: mod.label, tier: mod.tier,
      floor: mod.tier === "pico" ? mod.floor : mod.floor, frame: mod.frame,
    });
    PATCH.tiles.push(t);
    PATCH.focusId = t.id;
    rewire();
    derive();
    render();
    if (mod.tier === "pico") {
      react(tilesOf("pico").length > 1
        ? "Second Pico on line - the feed splits its channels between them."
        : "Wave Pico is reading the feed. Turn its floor knob and watch the lamp.");
    } else if (mod.tier === "nano") {
      react("Wave Nano adjudicates every Pico's escalations now - the dotted cables found it themselves.");
    } else {
      react("The operator is on shift, reading the Nano's rollups. The chain is complete.");
    }
    return t;
  }

  function removeTile(id) {
    var t = tileById(id);
    if (!t || t.kind === "feed" || t.kind === "intake") return;
    PATCH.tiles = PATCH.tiles.filter(function (x) { return x.id !== id; });
    PATCH.selected = null;
    rewire();
    derive();
    render();
    react((t.data.label || t.kind) + " removed" +
      (t.tier === "nano" && tilesOf("human").length ? " - the operator has nothing to read now." : "."));
  }

  /* =====================================================================
     DERIVATION - every reading is a recount of recorded facts

     The recorded run: 120 per-item records, each with truth, the child's
     prediction and margin, and the parent's prediction and margin. The
     feed fader picks how many are live; multiple Picos partition them;
     each Pico's knob decides which of ITS records escalate. Catches,
     misses and dead-end escalations follow arithmetically. Nothing here
     calls a model, and nothing invents a number.
     ===================================================================== */
  function derive() {
    var m = PATCH.measured;
    if (!m) return;
    var recs = m.records.slice(0, PATCH.online);
    var picos = tilesOf("pico");
    var nano = tilesOf("nano")[0];
    var human = tilesOf("human")[0];

    var TOP = DETENTS[DETENTS.length - 1];
    var totals = { read: recs.length, asserted: 0, escalated: 0, caught: 0,
                   missed: 0, fixable: 0, deadEnd: 0, falseAlarms: 0,
                   picos: picos.length, nano: !!nano, human: !!human };

    picos.forEach(function (p, pi) {
      var mine = recs.filter(function (_, i) { return i % picos.length === pi; });
      var st = { read: mine.length, asserted: 0, escalated: 0, missed: 0,
                 fixable: 0, caught: 0 };
      mine.forEach(function (r) {
        var isFault = r.truth !== "none";
        if (r.child.margin < p.data.floor) {
          st.escalated++;
          if (nano) {
            // "caught" counts FAULTS caught - a healthy channel correctly
            // read as none is just quiet, not a catch
            if (r.parent.prediction === r.truth) { if (isFault) { st.caught++; totals.caught++; } }
            else if (isFault) { totals.missed++; st.missed++; }
            else totals.falseAlarms++;
          } else {
            totals.deadEnd++;
          }
          totals.escalated++;
        } else {
          st.asserted++;
          totals.asserted++;
          if (r.child.prediction === r.truth) { if (isFault) { st.caught++; totals.caught++; } }
          else if (isFault) {
            totals.missed++; st.missed++;
            // FIXABLE: a higher detent on THIS knob would have escalated this
            // read, and the recorded parent had the right answer. Everything
            // else missed is the ladder's measured ceiling - the senior model
            // itself was wrong in the recorded run, or the child was
            // confidently wrong past the top measured floor.
            if (r.parent.prediction === r.truth && r.child.margin < TOP) {
              totals.fixable++; st.fixable++;
            }
          }
          else totals.falseAlarms++;
        }
      });
      p.stats = st;
      p.lamp = st.fixable ? "failure" : (st.escalated && !nano ? "check" : (st.missed && !nano ? "failure" : "ok"));
      p.margin = median(mine.map(function (r) { return r.child.margin; }));
    });

    if (nano) {
      var esc = totals.escalated;
      nano.stats = { adjudicated: esc, caught: totals.caught };
      nano.lamp = esc ? "ok" : "idle";
      nano.margin = 2.88; // display only; its floor is fixed at 2.5 (frame B)
    }
    if (human) {
      human.lamp = nano ? "ok" : "check";
    }

    // THE LAMP. Red: this bench is doing WORSE than its own settings allow -
    // faults were missed that a higher knob detent would have escalated to a
    // parent who, in the recorded run, had the right answer (or the chain has
    // no Nano and misses pile up unheard). Yellow: an incomplete chain.
    // Green: the ladder's measured ceiling - which is NOT the same as
    // perfection, and the lamp says so rather than claiming ALL CLEAR when
    // the records show the senior model itself missed faults.
    var state, why, label = null;
    var faults = totals.caught + totals.missed;
    if (!picos.length) {
      state = "off"; why = "No model is reading the feed.";
    } else if (!nano && totals.missed > 0) {
      state = "red";
      why = totals.missed + " recorded fault" + (totals.missed === 1 ? "" : "s") + " missed and " +
        totals.deadEnd + " escalation" + (totals.deadEnd === 1 ? "" : "s") +
        " with nobody to hear " + (totals.deadEnd === 1 ? "it" : "them") + " - add a Wave Nano.";
    } else if (totals.fixable > 0) {
      state = "red";
      why = totals.fixable + " recorded fault" + (totals.fixable === 1 ? "" : "s") +
        " missed that a higher floor would have escalated - and the recorded parent had " +
        (totals.fixable === 1 ? "the right answer" : "the right answers") +
        ". Raise the FLOOR knob so doubtful reads defer up.";
    } else if (totals.deadEnd > 0) {
      state = "yellow";
      why = totals.deadEnd + " escalation" + (totals.deadEnd === 1 ? "" : "s") + " with nobody to hear " +
        (totals.deadEnd === 1 ? "it" : "them") + " - add a Wave Nano.";
    } else if (!human) {
      state = "yellow";
      why = "The knobs are doing all they can, but no operator is on shift - the ladder should end with a person.";
    } else if (totals.missed === 0) {
      state = "green";
      why = "Complete chain: every recorded fault on the live sample caught.";
    } else {
      state = "green"; label = "AT CEILING";
      why = "Complete chain at its measured ceiling: " + totals.caught + " of " + faults +
        " recorded faults caught. The remaining " + totals.missed +
        " were missed by the senior model itself in the recorded run - no knob setting changes that.";
    }
    PATCH.verdict = { state: state, why: why, label: label, totals: totals };
    paintConsole();
    paintFeedList();
  }

  function median(xs) {
    if (!xs.length) return null;
    var a = xs.slice().sort(function (x, y) { return x - y; });
    return a[Math.floor(a.length / 2)];
  }

  /* ---- the status console: the lamp and the strip ----------------------- */
  var LAMP_FACE = {
    off:    { sym: "·", label: "NO READER" },
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
  }

  // The strip is a chart recorder: one printed line per reaction, newest on
  // top, capped. It narrates cause and effect - the thing the founder asked
  // to SEE as knobs move.
  function react(msg) {
    var strip = $("wpStrip");
    if (!strip) return;
    PATCH.step++;
    var li = el("li", "wp-strip__line");
    li.appendChild(el("span", "wp-strip__t", String(PATCH.step).padStart(3, "0")));
    li.appendChild(el("span", null, msg));
    strip.insertBefore(li, strip.firstChild);
    while (strip.childNodes.length > 6) strip.removeChild(strip.lastChild);
    var say = $("wpSay");
    if (say) say.textContent = msg;
  }

  function reactStats() {
    var v = PATCH.verdict;
    if (!v) return;
    var t = v.totals;
    var bits = [t.read + " channels", t.escalated + " escalate"];
    if (t.nano) bits.push(t.caught + " caught");
    if (t.missed) bits.push(t.missed + " missed");
    if (t.deadEnd) bits.push(t.deadEnd + " unheard");
    if (t.falseAlarms) bits.push(t.falseAlarms + " false alarms");
    react(bits.join(" · "));
  }

  /* =====================================================================
     RENDER
     ===================================================================== */
  function sheetSize() {
    var maxSlot = 1;
    PATCH.tiles.forEach(function (t) { if (t.slot > maxSlot) maxSlot = t.slot; });
    var rows = Math.min(MAX_SLOTS, maxSlot + 2);
    return {
      w: PAD_X * 2 + COLS.length * COL_PITCH - GAP_X,
      h: PAD_Y * 2 + rows * SLOT_PITCH - GAP_Y,
    };
  }

  function render() {
    var sheet = $("wpSheet");
    if (!sheet) return;
    var wires = $("wpWires");
    sheet.querySelectorAll(".wp-tile, .wp-colhead").forEach(function (n) { n.remove(); });
    while (wires.lastChild && wires.lastChild.nodeName.toLowerCase() !== "defs") {
      wires.removeChild(wires.lastChild);
    }
    var dim = sheetSize();
    sheet.style.width = dim.w + "px";
    sheet.style.height = dim.h + "px";
    sheet.style.backgroundSize = (COL_PITCH / 2) + "px " + (SLOT_PITCH / 2) + "px";
    wires.setAttribute("viewBox", "0 0 " + dim.w + " " + dim.h);
    wires.setAttribute("width", dim.w);
    wires.setAttribute("height", dim.h);

    COLS.forEach(function (tier) {
      var label = tier === "feed" ? "SIGNAL" : tier === "human" ? "OPERATOR" : "WAVE " + tier.toUpperCase();
      var h = el("span", "wp-colhead", label);
      h.style.left = colX(tier) + "px";
      h.style.width = TILE_W + "px";
      sheet.appendChild(h);
    });

    PATCH.wires.forEach(function (wire) { drawWire(wires, wire); });
    PATCH.tiles.forEach(function (t) { sheet.appendChild(drawTile(t)); });
    renderMirror();

    if (PATCH.focusId && PATCH.refocus) {
      var back = sheet.querySelector('.wp-tile[data-tile="' + PATCH.focusId + '"]');
      if (back) back.focus();
    }
    PATCH.refocus = false;
  }

  function portPos(t, side) {
    var x = colX(t.tier), y = slotY(t.slot);
    if (side === "in") return { x: x, y: y + TILE_H / 2 };
    return { x: x + TILE_W, y: y + TILE_H / 2 };
  }

  function bend(a, b, c, r) {
    var d1 = Math.hypot(b.x - a.x, b.y - a.y), d2 = Math.hypot(c.x - b.x, c.y - b.y);
    var size = Math.min(d1 / 2, d2 / 2, r);
    if ((a.x === b.x && b.x === c.x) || (a.y === b.y && b.y === c.y)) return "L" + b.x + " " + b.y;
    if (a.y === b.y) {
      var xd = a.x < c.x ? -1 : 1, yd = a.y < c.y ? 1 : -1;
      return "L " + (b.x + size * xd) + "," + b.y + "Q " + b.x + "," + b.y + " " + b.x + "," + (b.y + size * yd);
    }
    var xd2 = a.x < c.x ? 1 : -1, yd2 = a.y < c.y ? -1 : 1;
    return "L " + b.x + "," + (b.y + size * yd2) + "Q " + b.x + "," + b.y + " " + (b.x + size * xd2) + "," + b.y;
  }

  function wirePath(from, to) {
    var OFF = 16;
    var a = { x: from.x + OFF, y: from.y };
    var d = { x: to.x - OFF, y: to.y };
    var midX = (a.x + d.x) / 2;
    var b = { x: midX, y: a.y }, c = { x: midX, y: d.y };
    return "M" + from.x + " " + from.y + "L" + a.x + " " + a.y +
      bend(a, b, c, 8) + bend(b, c, d, 8) + "L" + d.x + " " + d.y + "L" + to.x + " " + to.y;
  }

  function drawWire(host, wire) {
    var f = tileById(wire.fromId), t = tileById(wire.toId);
    if (!f || !t) return;
    var d = wirePath(portPos(f, "out"), portPos(t, "in"));
    wire.d = d;
    var g = svg("g", { class: "wp-wire wp-wire--" + wire.kind + (wire.user ? " wp-wire--user" : "") });
    g.appendChild(svg("path", { class: "wp-wire__under", d: d }));
    g.appendChild(svg("path", { class: "wp-wire__line", d: d, "marker-mid": "url(#wpArrow)" }));
    var hit = svg("path", { class: "wp-wire__hit", d: d });
    hit.addEventListener("click", function (e) { e.stopPropagation(); inspectWire(wire); });
    g.appendChild(hit);
    host.appendChild(g);
    wire.node = g;
  }

  /* ---- the rotary floor knob --------------------------------------------
     An SVG instrument: 270-degree arc, tick at each measured detent, needle
     at the current floor. Drag vertically or use arrow keys; it SNAPS to
     detents because the detents are the four floors the sweep measured -
     the knob physically cannot ask for an unmeasured number. */
  function knobAngle(floor) {
    var i = DETENTS.indexOf(floor);
    var f = i < 0 ? 0 : i / (DETENTS.length - 1);
    return -135 + f * 270;
  }

  function drawKnob(t) {
    var wrap = el("div", "wp-knob");
    wrap.tabIndex = 0;
    wrap.setAttribute("role", "slider");
    wrap.setAttribute("aria-label", "margin floor");
    wrap.setAttribute("aria-valuemin", String(DETENTS[0]));
    wrap.setAttribute("aria-valuemax", String(DETENTS[DETENTS.length - 1]));
    wrap.setAttribute("aria-valuenow", String(t.data.floor));
    wrap.title = knobTip(t.data.floor);

    var s = svg("svg", { viewBox: "0 0 64 64", class: "wp-knob__svg", "aria-hidden": "true" });
    s.appendChild(svg("circle", { class: "wp-knob__ring", cx: 32, cy: 32, r: 26 }));
    DETENTS.forEach(function (dv) {
      var a = (knobAngle(dv) - 90) * Math.PI / 180;
      s.appendChild(svg("line", {
        class: "wp-knob__tick" + (dv === t.data.floor ? " is-on" : ""),
        x1: 32 + Math.cos(a) * 21, y1: 32 + Math.sin(a) * 21,
        x2: 32 + Math.cos(a) * 26, y2: 32 + Math.sin(a) * 26,
      }));
    });
    var g = svg("g", { class: "wp-knob__cap", transform: "rotate(" + knobAngle(t.data.floor) + " 32 32)" });
    g.appendChild(svg("circle", { cx: 32, cy: 32, r: 17 }));
    g.appendChild(svg("line", { class: "wp-knob__needle", x1: 32, y1: 32, x2: 32, y2: 17 }));
    s.appendChild(g);
    wrap.appendChild(s);
    wrap.appendChild(el("span", "wp-knob__k", "FLOOR " + t.data.floor.toFixed(1)));

    function setFloor(fl) {
      if (fl === t.data.floor) return;
      t.data.floor = fl;
      derive();
      render();
      flyOnce();
      reactStats();
      // render() rebuilt this tile; hand focus to the NEW knob so a second
      // arrow-press lands on the instrument, not on <body>
      var again = document.querySelector('.wp-tile[data-tile="' + t.id + '"] .wp-knob');
      if (again) again.focus({ preventScroll: true });
    }
    // drag up/down; snaps to the nearest detent
    wrap.addEventListener("pointerdown", function (e) {
      if (e.button != null && e.button !== 0) return;
      e.preventDefault();
      e.stopPropagation();
      var startY = e.clientY, startIdx = Math.max(0, DETENTS.indexOf(t.data.floor));
      var pid = e.pointerId;
      wrap.setPointerCapture && wrap.setPointerCapture(pid);
      function mv(ev) {
        if (ev.pointerId !== pid) return;
        var idx = Math.max(0, Math.min(DETENTS.length - 1,
          startIdx + Math.round((startY - ev.clientY) / 24)));
        if (DETENTS[idx] !== t.data.floor) setFloor(DETENTS[idx]);
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
    wrap.addEventListener("keydown", function (e) {
      var i = Math.max(0, DETENTS.indexOf(t.data.floor));
      if (e.key === "ArrowUp" || e.key === "ArrowRight") { e.preventDefault(); e.stopPropagation(); if (i < DETENTS.length - 1) setFloor(DETENTS[i + 1]); }
      if (e.key === "ArrowDown" || e.key === "ArrowLeft") { e.preventDefault(); e.stopPropagation(); if (i > 0) setFloor(DETENTS[i - 1]); }
    });
    wrap.addEventListener("click", function (e) { e.stopPropagation(); });
    return wrap;
  }

  // The measured numbers live on the knob's TOOLTIP, per the founder: the
  // instrument on the face, the figures on demand.
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

  /* ---- the feed fader ---------------------------------------------------- */
  function drawFader(t) {
    var wrap = el("div", "wp-fader");
    var lab = el("label", "wp-fader__k");
    lab.textContent = "CHANNELS ON LINE";
    var input = document.createElement("input");
    input.type = "range";
    input.min = "10"; input.max = "40"; input.step = "10";
    input.value = String(PATCH.online);
    input.className = "wp-fader__slide";
    input.setAttribute("aria-label", "channels on line");
    input.title = "how many of the 120 recorded channels are live on the bench";
    // 'input' fires per step MID-DRAG - rebuilding the sheet then would
    // destroy the very slider under the pointer. So mid-drag we recount and
    // repaint the console only; the full re-render waits for 'change'.
    input.addEventListener("input", function (e) {
      e.stopPropagation();
      PATCH.online = parseInt(input.value, 10) || 30;
      derive();
      paintConsole();
      var v = wrap.querySelector(".wp-fader__v");
      if (v) v.textContent = PATCH.online + " / 40";
    });
    input.addEventListener("change", function (e) {
      e.stopPropagation();
      render();
      reactStats();
      var again = document.querySelector('.wp-tile[data-tile="' + t.id + '"] .wp-fader__slide');
      if (again) again.focus({ preventScroll: true });
    });
    input.addEventListener("pointerdown", function (e) { e.stopPropagation(); });
    input.addEventListener("click", function (e) { e.stopPropagation(); });
    var id = "wpFader" + t.id;
    input.id = id; lab.htmlFor = id;
    wrap.appendChild(lab);
    wrap.appendChild(input);
    wrap.appendChild(el("span", "wp-fader__v", PATCH.online + " / 40"));
    return wrap;
  }

  var LAMPS = {
    idle:    { sym: "·", label: "idle" },
    ok:      { sym: "●", label: "OK" },
    check:   { sym: "△", label: "check" },
    failure: { sym: "⊗", label: "missed" }, // tile-scale shorthand; the window spells it out
  };

  function drawTile(t) {
    var n = el("div", "wp-tile wp-tile--" + t.kind);
    n.style.left = colX(t.tier) + "px";
    n.style.top = slotY(t.slot) + "px";
    n.style.width = TILE_W + "px";
    n.style.height = TILE_H + "px";
    n.dataset.tile = t.id;
    n.tabIndex = (PATCH.focusId ? t.id === PATCH.focusId : t.kind === "feed") ? 0 : -1;
    n.setAttribute("role", "group");
    n.setAttribute("aria-label", describe(t));
    if (PATCH.selected === t.id) n.classList.add("is-sel");

    var plate = el("div", "wp-tile__plate");
    plate.appendChild(el("b", "wp-tile__name", t.data.label || t.kind));
    if (t.kind === "model") plate.appendChild(el("span", "wp-tile__tier", t.data.tier));
    n.appendChild(plate);

    if (t.kind === "feed") {
      n.appendChild(drawFader(t));
      n.appendChild(el("div", "wp-tile__badge", "RECORDED FLEET · " + shortName(PATCH.measured && PATCH.measured.escalation.bench)));
    }

    if (t.kind === "intake") {
      if (t.data.channels && t.data.channels.length) {
        n.appendChild(el("div", "wp-tile__meta", "PASTED · " + (t.data.modality || "").toUpperCase()));
        n.appendChild(el("div", "wp-tile__badge", t.data.recognised
          ? "RECORDED SAMPLE · OUR BYTES" : "NOT SIMULATED · YOUR BYTES"));
      } else {
        n.appendChild(el("div", "wp-tile__meta", "▤ paste / drop"));
        n.appendChild(el("div", "wp-tile__badge", "your plant's bytes, read by the shim"));
      }
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
    }

    if (t.kind === "model" && t.tier === "pico") {
      var row = el("div", "wp-tile__row");
      row.appendChild(drawKnob(t));
      var side = el("div", "wp-tile__side");
      side.appendChild(lampFor(t));
      if (t.stats) {
        var mini = el("span", "wp-tile__mini",
          t.stats.read + " ch · " + t.stats.escalated + " esc");
        mini.title = t.stats.asserted + " asserted, " + t.stats.escalated + " escalated, " +
          t.stats.missed + " missed on this Pico's share of the recorded sample";
        side.appendChild(mini);
      }
      row.appendChild(side);
      n.appendChild(row);
    }

    if (t.kind === "model" && t.tier === "nano") {
      n.appendChild(el("div", "wp-tile__meta", "floor 2.5 · frame B"));
      n.appendChild(lampFor(t));
      if (t.stats) {
        var mini2 = el("span", "wp-tile__mini",
          t.stats.adjudicated + " adjudicated · " + t.stats.caught + " caught");
        mini2.title = "recounted from the recorded parent predictions";
        n.appendChild(mini2);
      }
    }

    if (t.kind === "human") {
      n.appendChild(el("div", "wp-tile__meta", "the ladder ends with a person"));
      n.appendChild(lampFor(t));
    }

    // Ports are indicators now, not controls - the sheet wires itself.
    if (t.kind !== "human") {
      var po = el("span", "wp-jack wp-jack--out" + (t.kind === "intake" && !(t.data.channels || []).length ? " is-dark" : ""));
      n.appendChild(po);
    }
    if (t.kind !== "feed" && t.kind !== "intake") {
      n.appendChild(el("span", "wp-jack wp-jack--in"));
    }

    if (t.kind === "model" || t.kind === "human") wireTileDrag(n, t);
    return n;
  }

  function lampFor(t) {
    var l = LAMPS[t.lamp] || LAMPS.idle;
    var n = el("span", "wp-lamp", l.sym + " " + l.label);
    n.dataset.state = t.lamp;
    return n;
  }

  function describe(t) {
    if (t.kind === "feed") return "Plant feed, " + PATCH.online + " recorded channels on line";
    if (t.kind === "intake") {
      return (t.data.channels && t.data.channels.length)
        ? "Your data, pasted, one channel out"
        : "Your data - activate to paste what your plant emits";
    }
    if (t.kind === "model") {
      return t.data.label + ", tier " + t.data.tier +
        (t.tier === "pico" ? ", margin floor " + t.data.floor : "") +
        ", status " + (LAMPS[t.lamp] || LAMPS.idle).label;
    }
    return "Operator, end of the ladder, status " + (LAMPS[t.lamp] || LAMPS.idle).label;
  }

  function shortName(p) { return String(p || "").split("/").pop(); }

  // One packet on the first escalation cable, once per interaction.
  function flyOnce() {
    if (REDUCED) return;
    var wire = PATCH.wires.filter(function (w) { return w.kind === "escalate"; })[0];
    if (!wire || !wire.d || !wire.node) return;
    var p = svg("circle", { class: "wp-packet", r: "4", cx: "0", cy: "0" });
    p.style.offsetPath = 'path("' + wire.d + '")';
    wire.node.appendChild(p);
    setTimeout(function () { if (p.parentNode) p.parentNode.removeChild(p); }, 1100);
  }

  /* =====================================================================
     DRAG WITH SNAP - unchanged mechanics, typed columns
     ===================================================================== */
  function wireRailDrag(chip, mod) {
    chip.addEventListener("pointerdown", function (e) {
      if (e.button != null && e.button !== 0) return;
      startDrag(e, { mod: mod });
    });
    chip.addEventListener("click", function () {
      if (PATCH.dragMoved) { PATCH.dragMoved = false; return; }
      addModule(mod);
    });
  }

  function wireTileDrag(node, t) {
    node.addEventListener("pointerdown", function (e) {
      if (e.button != null && e.button !== 0) return;
      if (e.target.closest(".wp-knob") || e.target.closest(".wp-fader")) return;
      startDrag(e, { tileId: t.id });
    });
  }

  function startDrag(e, what) {
    var sx = e.clientX, sy = e.clientY;
    var moved = false;
    var ghost = null, snap = null;
    var sheet = $("wpSheet");
    if (!sheet) return;
    var pid = e.pointerId;

    function tierOf() {
      if (what.mod) return what.mod.tier;
      var t = tileById(what.tileId);
      return t ? t.tier : COLS[1];
    }

    function onMove(ev) {
      if (ev.pointerId !== pid) return;
      if (!moved && Math.hypot(ev.clientX - sx, ev.clientY - sy) < 6) return;
      if (!moved) {
        moved = true;
        PATCH.dragMoved = true;
        document.body.classList.add("wp-dragging");
        ghost = el("div", "wp-ghost");
        ghost.textContent = what.mod ? what.mod.label
          : ((tileById(what.tileId) || {}).data || {}).label || "";
        document.body.appendChild(ghost);
        snap = el("div", "wp-snap");
        sheet.appendChild(snap);
        if (what.tileId) {
          var lifted = sheet.querySelector('.wp-tile[data-tile="' + what.tileId + '"]');
          if (lifted) lifted.classList.add("is-lift");
        }
      }
      ghost.style.left = (ev.clientX + 12) + "px";
      ghost.style.top = (ev.clientY + 12) + "px";

      var r = sheet.getBoundingClientRect();
      var maxSlotNow = 1;
      PATCH.tiles.forEach(function (x) { if (x.slot > maxSlotNow) maxSlotNow = x.slot; });
      var slot = Math.round((ev.clientY - r.top - PAD_Y) / SLOT_PITCH);
      slot = Math.max(0, Math.min(Math.min(MAX_SLOTS - 1, maxSlotNow + 1), slot));
      var over = ev.clientX >= r.left && ev.clientX <= r.right &&
                 ev.clientY >= r.top && ev.clientY <= r.bottom;
      var taken = PATCH.tiles.some(function (x) {
        return x.tier === tierOf() && x.slot === slot && x.id !== what.tileId;
      });
      snap.style.display = over && !taken ? "block" : "none";
      snap.style.left = colX(tierOf()) + "px";
      snap.style.top = slotY(slot) + "px";
      snap.style.width = TILE_W + "px";
      snap.style.height = TILE_H + "px";
      snap.dataset.slot = String(slot);
    }

    function finish(commit) {
      document.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerup", onUp);
      document.removeEventListener("pointercancel", onCancel);
      document.body.classList.remove("wp-dragging");
      var slot = (snap && snap.style.display === "block") ? parseInt(snap.dataset.slot, 10) : -1;
      if (ghost) ghost.remove();
      if (snap) snap.remove();
      sheet.querySelectorAll(".is-lift").forEach(function (x) { x.classList.remove("is-lift"); });
      setTimeout(function () { PATCH.dragMoved = false; }, 0);
      if (!commit || !moved) return;
      if (slot < 0) { react("Dropped outside a free slot - nothing changed."); return; }
      if (what.mod) {
        addModule(what.mod, slot);
      } else {
        var t = tileById(what.tileId);
        if (t) { t.slot = slot; PATCH.refocus = true; rewire(); render(); }
      }
    }
    function onUp(ev) { if (ev.pointerId === pid) finish(true); }
    function onCancel(ev) { if (ev.pointerId === pid) finish(false); }

    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
    document.addEventListener("pointercancel", onCancel);
  }

  /* =====================================================================
     RESULTS - the feed list and the inspector
     ===================================================================== */
  function paintFeedList() {
    var list = $("wpFeed");
    var m = PATCH.measured;
    if (!list || !m) return;
    var picos = tilesOf("pico");
    list.textContent = "";
    if (!picos.length) {
      list.appendChild(el("li", "wp-note", "No model is reading the feed - drag a Wave Pico in."));
      return;
    }
    var recs = m.records.slice(0, Math.min(PATCH.online, 14));
    recs.forEach(function (r, i) {
      var p = picos[i % picos.length];
      var esc = r.child.margin < p.data.floor;
      var li = el("li", "wp-rec" + (esc ? " wp-rec--esc" : ""));
      li.appendChild(el("span", "wp-rec__kind", esc ? "escalate" : "assert"));
      li.appendChild(el("code", "wp-rec__id", r.node_id));
      li.appendChild(el("span", "wp-rec__pred", r.child.prediction));
      li.appendChild(el("span", "wp-rec__m", r.child.margin.toFixed(2)));
      if (esc && tilesOf("nano").length) li.appendChild(el("span", "wp-rec__adj", "→ " + r.parent.prediction));
      list.appendChild(li);
    });
  }

  function inspectTile(t) {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    revealInspector(box);
    box.appendChild(el("b", null, t.data.label || t.kind));
    if (t.kind === "feed") {
      box.appendChild(el("p", "wp-note",
        "The recorded fleet: per-channel records from " + shortName(PATCH.measured.escalation.bench) +
        " (" + PATCH.measured._provenance.suite + "), " + PATCH.online + " on line. " +
        "Every reading on this bench is a recount of these records - no model runs in a browser."));
      drawScopeInto(box);
    }
    if (t.kind === "model") {
      var dl = el("dl", "wp-cert");
      var rows = [["tier", t.data.tier]];
      if (t.tier === "pico") rows.push(["margin floor", t.data.floor.toFixed(1) + " (knob)"]);
      else rows.push(["margin floor", "2.5 (frame B)"]);
      rows.push(["frame", t.data.frame || (t.tier === "pico" ? "A" : "B")]);
      rows.push(["digest", null]);
      rows.forEach(function (row) {
        dl.appendChild(el("dt", null, row[0]));
        dl.appendChild(el("dd", row[1] == null ? "wp-cert__pending" : null,
                          row[1] == null ? "pending export" : row[1]));
      });
      box.appendChild(dl);
      if (t.tier === "pico") {
        box.appendChild(el("p", "wp-note", knobTip(t.data.floor)));
        // the user-bytes envelope, if their channel is wired into this pico
        var userWire = PATCH.wires.filter(function (w) {
          var f = tileById(w.fromId);
          return w.toId === t.id && w.user && f && f.data.body;
        })[0];
        if (userWire) {
          var src = tileById(userWire.fromId);
          box.appendChild(el("p", "wp-tag wp-tag--draft", "DRAFT · NOT RUN"));
          box.appendChild(el("p", "wp-note",
            "Your bytes are patched into this Pico but nothing ran - no model executes in a " +
            "browser. This is the exact request they become on a stock llama-server:"));
          box.appendChild(el("pre", "wp-wirebytes", envelopeFor(t, src)));
        }
      }
      var rm = el("button", "wp-remove", "remove this module");
      rm.type = "button";
      rm.addEventListener("click", function () { removeTile(t.id); box.textContent = ""; });
      box.appendChild(rm);
    }
    if (t.kind === "human") {
      box.appendChild(el("p", "wp-note",
        "Every chain ends with a person. The operator reads the Nano's rollups; " +
        "if the Nano goes, the operator has nothing to read and the lamp says so."));
      var rm2 = el("button", "wp-remove", "off shift");
      rm2.type = "button";
      rm2.addEventListener("click", function () { removeTile(t.id); box.textContent = ""; });
      box.appendChild(rm2);
    }
  }

  function drawScopeInto(box) {
    var sc = PATCH.scene;
    if (!sc || !sc.channel) return;
    box.appendChild(el("p", "wp-note", "One recorded channel from the sample scene (" +
      shortName(sc.asset_type) + ", " + (sc.spec && sc.spec.faults && sc.spec.faults.vibration ? "vibration stuck at 40% onset" : "recorded") + "):"));
    var host = svg("svg", { class: "wp-scope", viewBox: "0 0 520 96", role: "img",
      "aria-label": "recorded channel trace with the fault onset marked" });
    var s = sc.channel.samples || [];
    if (!s.length) return;
    var W = 520, H = 96, lo = Math.min.apply(null, s), hi = Math.max.apply(null, s);
    var span = (hi - lo) || 1;
    var d = s.map(function (v, i) {
      return (i ? "L" : "M") + (i / (s.length - 1) * W).toFixed(1) + " " +
        (H - 8 - ((v - lo) / span) * (H - 16)).toFixed(1);
    }).join("");
    host.appendChild(svg("path", { class: "wp-scope__line", d: d }));
    host.appendChild(svg("line", { class: "wp-scope__onset", x1: (0.4 * W).toFixed(1), x2: (0.4 * W).toFixed(1), y1: 4, y2: H - 4 }));
    box.appendChild(host);
  }

  function inspectWire(wire) {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    revealInspector(box);
    if (wire.kind === "escalate") {
      box.appendChild(el("b", null, "Escalation link"));
      box.appendChild(el("p", "wp-note",
        "Carries a typed record rightward when a child's margin falls below its floor. " +
        "These records carry no evidence dict - the recorded export does not include one. " +
        "Cables draw themselves: remove a module to remove its cables."));
    } else if (wire.user) {
      box.appendChild(el("b", null, "Your channel"));
      box.appendChild(el("p", "wp-note",
        "Your pasted bytes, patched into the first Pico as a draft. Open that Pico for the request envelope."));
    } else {
      box.appendChild(el("b", null, "Channel link"));
      var sc = PATCH.scene;
      if (sc && sc.renders) {
        var tabs = el("div", "wp-dialects");
        var pre = el("pre", "wp-wirebytes");
        Object.keys(sc.renders).forEach(function (mo, i) {
          var b = el("button", "wp-dialect", mo);
          b.type = "button";
          b.setAttribute("aria-pressed", i === 0 ? "true" : "false");
          b.addEventListener("click", function (e) {
            e.stopPropagation();
            tabs.querySelectorAll(".wp-dialect").forEach(function (x) { x.setAttribute("aria-pressed", "false"); });
            b.setAttribute("aria-pressed", "true");
            pre.textContent = sc.renders[mo];
          });
          tabs.appendChild(b);
          if (i === 0) pre.textContent = sc.renders[mo];
        });
        box.appendChild(el("p", "wp-note",
          "What travels this cable, in every dialect a plant might speak - RECORDED, from the sample scene."));
        box.appendChild(tabs);
        box.appendChild(pre);
      }
    }
  }

  function renderMirror() {
    var m = $("wpMirror");
    if (!m) return;
    m.textContent = "";
    var order = PATCH.tiles.slice().sort(function (a, b) {
      return COLS.indexOf(a.tier) - COLS.indexOf(b.tier) || a.slot - b.slot;
    });
    order.forEach(function (t) {
      var li = el("li", null, describe(t));
      var out = PATCH.wires.filter(function (w) { return w.fromId === t.id; })
        .map(function (w) { var d2 = tileById(w.toId); return d2 ? (d2.data.label || d2.kind) : ""; });
      if (out.length) li.textContent += " → " + out.join(", ");
      m.appendChild(li);
    });
  }

  function revealInspector(box) {
    if (box.scrollIntoView) box.scrollIntoView({ block: "nearest", behavior: REDUCED ? "auto" : "smooth" });
  }

  /* =====================================================================
     EVENTS
     ===================================================================== */
  function onSheetClick(e) {
    if (PATCH.dragMoved) { PATCH.dragMoved = false; return; }
    if (e.target.closest(".wp-knob") || e.target.closest(".wp-fader")) return;
    var host = e.target.closest && e.target.closest(".wp-tile");
    if (host) {
      var tt = tileById(host.dataset.tile);
      if (!tt) return;
      if (tt.kind === "intake") { openIntake(); return; }
      PATCH.selected = tt.id;
      PATCH.focusId = tt.id;
      PATCH.refocus = true;
      inspectTile(tt);
      render();
    }
  }

  function onKey(e) {
    if (e.key === "Escape") {
      var drawer = $("wpIntake");
      if (drawer && !drawer.hidden) {
        closeIntake();
        var back = document.querySelector(".wp-tile--intake");
        if (back) back.focus();
        return;
      }
    }
    var host = document.activeElement && document.activeElement.closest
      && document.activeElement.closest(".wp-tile");
    if (!host) return;
    var t = tileById(host.dataset.tile);
    if (!t) return;
    if ((e.key === "Enter" || e.key === " ") && t.kind === "intake") {
      e.preventDefault();
      openIntake();
      return;
    }
    if ((e.key === "Delete" || e.key === "Backspace") && (t.kind === "model" || t.kind === "human")) {
      e.preventDefault();
      removeTile(t.id);
      return;
    }
    if (e.key.indexOf("Arrow") === 0 && !e.target.closest(".wp-knob")) {
      e.preventDefault();
      moveFocus(t, e.key);
    }
  }

  function moveFocus(from, key) {
    var want;
    if (key === "ArrowUp" || key === "ArrowDown") {
      var col = PATCH.tiles.filter(function (t) { return t.tier === from.tier; })
        .sort(function (a, b) { return a.slot - b.slot; });
      var i = col.indexOf(from) + (key === "ArrowDown" ? 1 : -1);
      want = col[i];
    } else {
      var ci = COLS.indexOf(from.tier) + (key === "ArrowRight" ? 1 : -1);
      want = PATCH.tiles.filter(function (t) { return t.tier === COLS[ci]; })
        .sort(function (a, b) {
          return Math.abs(a.slot - from.slot) - Math.abs(b.slot - from.slot);
        })[0];
    }
    if (!want) return;
    PATCH.focusId = want.id;
    var n = document.querySelector('.wp-tile[data-tile="' + want.id + '"]');
    if (n) { n.tabIndex = 0; n.focus(); }
  }

  function renderRail() {
    var rail = $("wpRail");
    if (!rail) return;
    rail.textContent = "";
    MODULES.forEach(function (mod) {
      var chip = el("button", "wp-mod wp-mod--" + mod.tier);
      chip.type = "button";
      chip.appendChild(el("i", "wp-mod__grip", "⠿"));
      var body = el("span", "wp-mod__body");
      body.appendChild(el("b", null, mod.label));
      body.appendChild(el("span", null, mod.blurb));
      chip.appendChild(body);
      chip.setAttribute("aria-label", "Add " + mod.label + " - drag onto the sheet, or press Enter to add");
      wireRailDrag(chip, mod);
      rail.appendChild(chip);
    });
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
    send.textContent = "SEND TO THE SHEET →";
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
    var t = PATCH.tiles.filter(function (x) { return x.kind === "intake"; })[0];
    if (!t) return;
    if (v.kind === "blob") {
      t.data.modality = v.mod;
      t.data.recognised = v.recognised || null;
      t.data.channels = [{ name: v.tag || "your channel", unit: "" }];
      t.data.body = INTAKE.text;
    } else if (v.kind === "numbers") {
      t.data.modality = "raw numbers";
      t.data.recognised = null;
      t.data.unit = v.unit || null;
      t.data.channels = [{ name: "your channel", unit: v.unit || "" }];
      t.data.body = INTAKE.text;
    } else {
      return;
    }
    closeIntake();
    rewire();
    derive();
    render();
    react(tilesOf("pico").length
      ? "Your channel patched itself into the first Pico - as a DRAFT. Open that Pico for the envelope."
      : "Your channel is on the bench. Drag a Wave Pico in and it patches itself.");
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
  function envelopeFor(t, src) {
    var body = src.data.body || "";
    var isNumbers = src.data.modality === "raw numbers";
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
        (src.data.channels.length ? "" : "") + "samples (mean, sd, slope," );
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
      renderRail();
      bootSheet();
      derive();
      var sheet = $("wpSheet");
      sheet.addEventListener("click", onSheetClick);
      document.addEventListener("keydown", onKey);
      wireIntake();
    }).catch(fail);
  }

  function fail() {
    var s = $("wpSheet");
    if (s) s.appendChild(el("p", "wp-note", "The patch data did not load. Nothing here is live; reload to try again."));
  }

  function maybeBoot() {
    var v = $("pgMeshView");
    if (v && !v.hidden && !PATCH.booted && $("wpSheet")) { PATCH.booted = true; boot(); }
  }

  // Pure-function hook so tests can EXECUTE the classifier and the derivation.
  if (typeof window !== "undefined") {
    window.__wavePatchTest = {
      classify: classify,
      setScene: function (sc, cat) { PATCH.scene = sc; PATCH.catalog = cat; },
      setMeasured: function (m) { PATCH.measured = m; },
      derive: derive,
      addModule: addModule,
      bootSheet: bootSheet,
      state: PATCH,
      modules: MODULES,
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
