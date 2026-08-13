/* =====================================================================
   RogerAI - THE PATCH SHEET (Playbox / WAVE MESH)

   Wire a machine to a model. Signal flows LEFT TO RIGHT: devices on the
   left, Wave models in the middle columns, the operator on the right.
   Escalation travels rightward up the ladder. You patch a channel into a
   model, press RUN, and watch what it asserts - and where its margin was
   too thin to assert at all.

   THE DESIGN CALLS, and why:

   LEFT-TO-RIGHT COLUMNS (founder direction 2026-08-13): reads like every
     signal chain an engineer already knows - source on the left, sink on
     the right. Columns are typed by tier, so geometry is semantics:
     nothing can be dropped somewhere meaningless and a wire can never
     point backwards.

   DRAG WITH SNAP, plus click-to-connect for wiring. Modules are dragged
     from the rail and SNAP into a column slot (slot pitch = tile + gap,
     n8n's invariant, so a dropped tile lands exactly where a tidy-up
     would put it and the sheet can never need tidying). Wiring stays
     tap-tap: tap a port, it arms, compatible ports pulse, tap the
     target. Escape cancels. One state machine serves pointer, touch and
     keyboard; clicking a rail chip (no drag) adds to the next free slot,
     which IS the keyboard path.

   SHAPES, NOT HUES for ports; the palette spends its one red on the
     glint (armed port, escalation, focus, RUN). NE 107 status ships
     distinct symbols, so shape alone satisfies the standard.

   ONE PACKET, ONE WIRE, ON A RUN - never a stream. This deck replays a
     RECORDED run; a packet may only move because a real record moved.
     Extra tiles the visitor adds beyond the recorded chain say plainly
     that they were not part of the recorded run, instead of inventing
     numbers for them.

   The rack's faceplates are birth certificates. Fields we have are
   shown; the model digest renders as a visibly empty slot until the
   artifact is exported - never an invented hash.
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
  var TILE_W = 176, TILE_H = 96, GAP_X = 84, GAP_Y = 22;
  var COLS = ["device", "pico", "nano", "human"]; // left -> right; signal flows right
  var COL_PITCH = TILE_W + GAP_X;
  var SLOT_PITCH = TILE_H + GAP_Y;
  var PAD_X = 30, PAD_Y = 34;
  var MAX_SLOTS = 4;

  function colX(tier) {
    var i = COLS.indexOf(tier);
    return PAD_X + (i < 0 ? 0 : i) * COL_PITCH;
  }
  function slotY(slot) { return PAD_Y + slot * SLOT_PITCH; }

  var PATCH = {
    catalog: null, measured: null, scene: null,
    tiles: [], wires: [], seq: 1,
    armed: null, selected: null, focusId: null, refocus: false,
    template: null, dragMoved: false, booted: false,
  };

  /* =====================================================================
     TEMPLATES - the sheet is never empty
     ===================================================================== */
  var TEMPLATES = [
    { id: "pump",    label: "Cavitating pump",     asset: "centrifugal_pump", cause: "cavitation",
      fault: { channel: "vibration", kind: "stuck", severity: 0.9, onset: 0.4 }, floor: 2.0 },
    { id: "gearbox", label: "Gearbox running dry", asset: "gearbox", cause: "lubrication_loss",
      fault: { channel: "oil_temp", kind: "drifting", severity: 0.8, onset: 0.3 }, floor: 2.0 },
    { id: "agv",     label: "AGV battery aging",   asset: "agv", cause: "battery_aging",
      fault: { channel: "battery_volt", kind: "noisy", severity: 0.7, onset: 0.2 }, floor: 2.0 },
    { id: "spindle", label: "CNC spindle chatter", asset: "cnc_spindle", cause: "chatter",
      fault: { channel: "vibration", kind: "railed", severity: 0.85, onset: 0.5 }, floor: 2.0 },
    { id: "motor",   label: "Motor phase imbalance", asset: "induction_motor", cause: "phase_imbalance",
      fault: { channel: "current_a", kind: "dropout", severity: 0.8, onset: 0.35 }, floor: 2.0 },
    { id: "chiller", label: "Chiller losing charge", asset: "chiller", cause: "refrigerant_loss",
      fault: { channel: "suction_press", kind: "stuck", severity: 0.75, onset: 0.45 }, floor: 2.0 },
  ];

  /* ---- the module rail: what can be added to the sheet ----------------- */
  var MODULES = [
    { kind: "model", tier: "pico", label: "Wave Pico", floor: 2.0, frame: "A",
      blurb: "channel reader · 98M" },
    { kind: "model", tier: "nano", label: "Wave Nano", floor: 2.5, frame: "B",
      blurb: "adjudicates escalations" },
  ];

  /* =====================================================================
     BUILDING A PATCH
     ===================================================================== */
  function tile(kind, tier, slot, data) {
    return { id: "t" + (PATCH.seq++), kind: kind, tier: tier, slot: slot,
             data: data || {}, lamp: "idle", bubble: null, margin: null, inRun: false };
  }

  function loadTemplate(tpl) {
    PATCH.template = tpl;
    PATCH.tiles = [];
    PATCH.wires = [];
    PATCH.seq = 1;
    PATCH.armed = null;
    PATCH.selected = null;

    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var asset = cat[tpl.asset];
    if (!asset) return;

    // Only the channels the root cause MOVES become ports - cavitation touches
    // four of a pump's eight, and drawing all eight is a wiring harness.
    var effects = asset.root_causes[tpl.cause] || {};
    var channels = Object.keys(effects).map(function (c) {
      return { name: c, dir: effects[c], unit: (asset.channels[c] || {}).unit || "" };
    });

    var dev = tile("device", "device", 1, {
      asset: tpl.asset, label: labelOf(tpl.asset), cause: tpl.cause,
      channels: channels, fault: tpl.fault, live: asset.source === "live",
    });
    var pico = tile("model", "pico", 1, { label: "Wave Pico", tier: "pico", floor: tpl.floor, frame: "A" });
    var nano = tile("model", "nano", 1, { label: "Wave Nano", tier: "nano", floor: 2.5, frame: "B" });
    var human = tile("human", "human", 1, { label: "Operator" });
    // The intake: a permanent slot in the source column for the visitor's OWN
    // bytes. The truthful statement about a Wave model is that it has no other
    // input path - human input enters the graph the same way a device does.
    var intake = tile("intake", "device", 2, { label: "Your Data", channels: [] });
    PATCH.tiles = [dev, pico, nano, human, intake];

    connect(dev.id, "out", pico.id, "in", "channel");
    connect(pico.id, "up", nano.id, "in", "escalate");
    connect(nano.id, "up", human.id, "in", "escalate");

    render();
    replay(true);
  }

  function labelOf(name) {
    var EXACT = { roggentoo: "RogGentoo" };
    var ACR = { agv: "AGV", cnc: "CNC", vfd: "VFD", hpu: "HPU" };
    if (EXACT[name]) return EXACT[name];
    return name.split("_").map(function (w) {
      return ACR[w] || w.charAt(0).toUpperCase() + w.slice(1);
    }).join(" ");
  }

  function connect(fromId, fromPort, toId, toPort, kind) {
    // One assertion input per parent (Node-RED's load-bearing constraint):
    // connecting a second escalation source replaces the first, so "what
    // feeds this?" stays answerable at a glance.
    if (toPort === "in" && kind === "escalate") {
      PATCH.wires = PATCH.wires.filter(function (w) {
        return !(w.toId === toId && w.kind === "escalate");
      });
    }
    PATCH.wires.push({ id: "w" + (PATCH.seq++), fromId: fromId, fromPort: fromPort,
                       toId: toId, toPort: toPort, kind: kind });
  }

  function disconnect(wireId) {
    PATCH.wires = PATCH.wires.filter(function (w) { return w.id !== wireId; });
    render();
    replay(true);
    say("Disconnected.");
  }

  function tileById(id) {
    for (var i = 0; i < PATCH.tiles.length; i++) if (PATCH.tiles[i].id === id) return PATCH.tiles[i];
    return null;
  }

  function freeSlot(tier) {
    var used = {};
    PATCH.tiles.forEach(function (t) { if (t.tier === tier) used[t.slot] = true; });
    for (var s = 0; s < MAX_SLOTS; s++) if (!used[s]) return s;
    return -1;
  }

  function addModule(mod, slot) {
    var s = (slot != null && slot >= 0) ? slot : freeSlot(mod.tier);
    if (s < 0) { say("That column is full."); return null; }
    var t = tile(mod.kind, mod.tier, s,
      { label: mod.label, tier: mod.tier, floor: mod.floor, frame: mod.frame });
    PATCH.tiles.push(t);
    PATCH.selected = t.id;
    PATCH.focusId = t.id;
    render();
    replay(true);
    say(mod.label + " added. Tap a filled port, then its input, to wire it in.");
    return t;
  }

  function removeTile(id) {
    var t = tileById(id);
    if (!t) return;
    // The Operator can never be deleted - the ladder honestly ends with a person.
    if (t.kind === "human") { say("The operator stays - every chain ends with a person."); return; }
    PATCH.tiles = PATCH.tiles.filter(function (x) { return x.id !== id; });
    PATCH.wires = PATCH.wires.filter(function (w) { return w.fromId !== id && w.toId !== id; });
    PATCH.selected = null;
    render();
    replay(true);
    say((t.data.label || t.kind) + " removed.");
  }

  /* =====================================================================
     RENDER
     ===================================================================== */
  function sheetSize() {
    var maxSlot = 1;
    PATCH.tiles.forEach(function (t) { if (t.slot > maxSlot) maxSlot = t.slot; });
    return {
      w: PAD_X * 2 + COLS.length * COL_PITCH - GAP_X,
      h: PAD_Y * 2 + (maxSlot + 1) * SLOT_PITCH - GAP_Y,
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
    wires.setAttribute("viewBox", "0 0 " + dim.w + " " + dim.h);
    wires.setAttribute("width", dim.w);
    wires.setAttribute("height", dim.h);

    // Column headings engraved into the sheet: the ladder is legible before a
    // single tile is read.
    COLS.forEach(function (tier) {
      var label = tier === "device" ? "SIGNAL" : tier === "human" ? "OPERATOR" : "WAVE " + tier.toUpperCase();
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

  function portPos(t, port) {
    var x = colX(t.tier), y = slotY(t.slot);
    if (port === "in") return { x: x, y: y + TILE_H / 2 };
    return { x: x + TILE_W, y: y + TILE_H / 2 };   // out / up: right edge
  }

  /* ---- orthogonal wires, rounded corners -------------------------------
     Not bezier: these are industrial people whose native diagrams are
     orthogonal, and real interior vertices are what make marker-mid arrows
     render at all. The path is always right -> across -> right. */
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
    var d = wirePath(portPos(f, wire.fromPort), portPos(t, wire.toPort));
    wire.d = d;

    var g = svg("g", { class: "wp-wire wp-wire--" + wire.kind });
    // A wide, low-opacity under-stroke gives the cable body without a filter -
    // zero feGaussianBlur cost, reads as depth on both themes.
    g.appendChild(svg("path", { class: "wp-wire__under", d: d }));
    g.appendChild(svg("path", { class: "wp-wire__line", d: d, "marker-mid": "url(#wpArrow)" }));
    var hit = svg("path", { class: "wp-wire__hit", d: d });
    hit.addEventListener("click", function (e) { e.stopPropagation(); inspectWire(wire); });
    g.appendChild(hit);
    host.appendChild(g);
    wire.node = g;
  }

  function drawTile(t) {
    var n = el("div", "wp-tile wp-tile--" + t.kind);
    n.style.left = colX(t.tier) + "px";
    n.style.top = slotY(t.slot) + "px";
    n.style.width = TILE_W + "px";
    n.style.height = TILE_H + "px";
    n.dataset.tile = t.id;
    // Roving tabindex: one tab stop for the sheet, arrows walk it.
    n.tabIndex = (PATCH.focusId ? t.id === PATCH.focusId : (t.kind === "device")) ? 0 : -1;
    n.setAttribute("role", "group");
    n.setAttribute("aria-label", describe(t));
    if (t.data.live) n.classList.add("is-live");
    if (PATCH.selected === t.id) n.classList.add("is-sel");

    var plate = el("div", "wp-tile__plate");
    plate.appendChild(el("b", "wp-tile__name", t.data.label || t.kind));
    if (t.kind === "model") plate.appendChild(el("span", "wp-tile__tier", t.data.tier));
    if (t.data.live) plate.appendChild(el("span", "wp-tile__live", "REAL"));
    n.appendChild(plate);

    if (t.kind === "device") {
      n.appendChild(el("div", "wp-tile__meta", t.data.cause.replace(/_/g, " ")));
      var ports = el("div", "wp-tile__ports");
      t.data.channels.forEach(function (c) {
        var p = el("button", "wp-port wp-port--chan wp-port--out");
        p.type = "button";
        p.dataset.port = "out";
        p.dataset.chan = c.name;
        p.title = c.name.replace(/_/g, " ") + " " + c.dir + (c.unit ? " · " + c.unit : "");
        p.setAttribute("aria-label", "channel out: " + c.name.replace(/_/g, " ") + " " + c.dir);
        ports.appendChild(p);
      });
      n.appendChild(ports);
    }

    if (t.kind === "model") {
      var vu = el("div", "wp-vu");
      var track = el("span", "wp-vu__track");
      var bar = el("span", "wp-vu__bar");
      // The needle reads the median margin the model actually produced in the
      // replay - a meter pegged at zero on an instrumentation deck would be
      // self-refuting. Scale 0-4 (observed margins ~0.4-2.6, floors 2.0-2.5).
      if (t.margin != null) bar.style.width = Math.min(100, (t.margin / 4) * 100) + "%";
      track.appendChild(bar);
      var red = el("span", "wp-vu__red");
      red.style.left = (Math.min(1, t.data.floor / 4) * 100) + "%";
      track.appendChild(red);
      vu.appendChild(track);
      vu.appendChild(el("span", "wp-vu__k",
        (t.margin != null ? "margin " + t.margin.toFixed(2) + " · " : "") +
        "floor " + t.data.floor.toFixed(1)));
      n.appendChild(vu);
      n.appendChild(lampFor(t));

      var inp = el("button", "wp-port wp-port--chan wp-port--in");
      inp.type = "button";
      inp.dataset.port = "in";
      inp.setAttribute("aria-label", "input");
      n.appendChild(inp);
      var up = el("button", "wp-port wp-port--rec wp-port--up");
      up.type = "button";
      up.dataset.port = "up";
      up.setAttribute("aria-label", "escalation out");
      n.appendChild(up);
    }

    if (t.kind === "intake") {
      if (t.data.channels.length) {
        n.appendChild(el("div", "wp-tile__meta", "PASTED · " + (t.data.modality || "").toUpperCase()));
        n.appendChild(el("div", "wp-tile__badge", "NOT SIMULATED · YOUR BYTES"));
        var uports = el("div", "wp-tile__ports");
        t.data.channels.forEach(function (c) {
          var up2 = el("button", "wp-port wp-port--chan wp-port--out");
          up2.type = "button";
          up2.dataset.port = "out";
          up2.dataset.chan = c.name;
          up2.title = c.name + (c.unit ? " · " + c.unit : " · unit not stated in the wire");
          up2.setAttribute("aria-label", "channel out: " + c.name);
          uports.appendChild(up2);
        });
        n.appendChild(uports);
      } else {
        n.appendChild(el("div", "wp-tile__meta", "▤ paste / drop"));
        n.appendChild(el("div", "wp-tile__badge", "your plant's bytes, read by the shim"));
      }
    }

    if (t.kind === "human") {
      n.appendChild(el("div", "wp-tile__meta", "the ladder ends with a person"));
      var hin = el("button", "wp-port wp-port--rec wp-port--in");
      hin.type = "button";
      hin.dataset.port = "in";
      hin.setAttribute("aria-label", "input");
      n.appendChild(hin);
    }

    // Make-style run bubble: the result appears ON the thing that produced it.
    if (t.bubble) {
      var b = el("button", "wp-bubble" + (t.bubble.esc ? " wp-bubble--esc" : ""));
      b.type = "button";
      b.textContent = t.bubble.text;
      b.addEventListener("click", function (e) { e.stopPropagation(); inspectTile(t); });
      n.appendChild(b);
    }

    if (t.kind === "model") wireTileDrag(n, t);
    return n;
  }

  var LAMPS = {
    idle:     { sym: "·", label: "idle" },
    ok:       { sym: "●", label: "OK" },
    escalate: { sym: "↑", label: "escalated" },
    abstain:  { sym: "○", label: "abstained" },
    cold:     { sym: "·", label: "not in the recorded run" },
  };
  function lampFor(t) {
    var l = LAMPS[t.lamp] || LAMPS.idle;
    var n = el("span", "wp-lamp", l.sym + " " + l.label);
    n.dataset.state = t.lamp;
    return n;
  }

  function describe(t) {
    if (t.kind === "device") {
      return t.data.label + ", device, " + t.data.cause.replace(/_/g, " ") + ", " +
        t.data.channels.length + " channels out";
    }
    if (t.kind === "model") {
      return t.data.label + ", tier " + t.data.tier + ", margin floor " + t.data.floor +
        ", status " + (LAMPS[t.lamp] || LAMPS.idle).label;
    }
    if (t.kind === "intake") {
      return t.data.channels.length
        ? "Your data, pasted, " + t.data.channels.length + " channel(s) out"
        : "Your data - activate to paste what your plant emits";
    }
    return t.data.label + ", the operator, end of the ladder";
  }

  /* =====================================================================
     DRAG WITH SNAP - modules from the rail, tiles between slots

     Pointer Events only (one path for mouse/pen/touch); 6px of drift before
     a press becomes a drag (ComfyUI's shipped constant); cleanup on
     pointercancel as well as pointerup, which is where stuck-drag bugs
     live. The snap preview only ever appears in the module's OWN column -
     a pico cannot be dropped in the nano column, because the column IS the
     type. Clicking a rail chip without dragging adds to the next free
     slot, which is also the keyboard path.
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
      if (e.target.closest(".wp-port") || e.target.closest(".wp-bubble")) return;
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
      var slot = Math.round((ev.clientY - r.top - PAD_Y) / SLOT_PITCH);
      slot = Math.max(0, Math.min(MAX_SLOTS - 1, slot));
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
      if (!commit || !moved) return;
      if (slot < 0) { say("Dropped outside a free slot - nothing changed."); return; }
      if (what.mod) {
        addModule(what.mod, slot);
      } else {
        var t = tileById(what.tileId);
        if (t) { t.slot = slot; PATCH.refocus = true; render(); }
      }
    }
    function onUp(ev) { if (ev.pointerId === pid) finish(true); }
    function onCancel(ev) { if (ev.pointerId === pid) finish(false); }

    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
    document.addEventListener("pointercancel", onCancel);
  }

  /* =====================================================================
     CONNECTING - tap a port, tap the target
     ===================================================================== */
  function armPort(tileId, port, chan) {
    PATCH.armed = { tileId: tileId, port: port, chan: chan };
    document.body.classList.add("wp-arming");
    markCompatible();
    say(port === "out" ? "Channel output armed. Tap a model's input, or press Escape."
                       : "Escalation output armed. Tap the next tier's input, or press Escape.");
  }

  function disarm() {
    PATCH.armed = null;
    document.body.classList.remove("wp-arming");
    var sheet = $("wpSheet");
    if (sheet) sheet.querySelectorAll(".wp-port").forEach(function (p) {
      p.classList.remove("is-ok", "is-no");
    });
  }

  // Compatibility is a perception task, not a memory task: once a wire is
  // armed, the ports that can receive it pulse and the ones that cannot dim.
  function markCompatible() {
    var a = PATCH.armed;
    var sheet = $("wpSheet");
    if (!a || !sheet) return;
    sheet.querySelectorAll(".wp-port").forEach(function (p) {
      var host = p.closest(".wp-tile");
      var t = host && tileById(host.dataset.tile);
      var ok = t && p.dataset.port === "in" && t.id !== a.tileId && canConnect(tileById(a.tileId), t);
      p.classList.toggle("is-ok", !!ok);
      p.classList.toggle("is-no", !ok && p.dataset.port !== a.port);
    });
  }

  function canConnect(from, to) {
    if (!from || !to) return false;
    // Signal flows right. The rule is geometric, so an illegal patch is
    // unreachable rather than explained.
    return COLS.indexOf(to.tier) > COLS.indexOf(from.tier);
  }

  function tryConnect(toTileId) {
    var a = PATCH.armed;
    if (!a) return;
    var from = tileById(a.tileId), to = tileById(toTileId);
    if (!canConnect(from, to)) { say("Those cannot be connected - signal flows left to right."); return; }
    connect(from.id, a.port, to.id, "in", a.port === "up" ? "escalate" : "channel");
    disarm();
    render();
    replay(true);
    say("Connected " + (from.data.label || from.kind) + " to " + (to.data.label || to.kind) + ".");
  }

  // Announced AND visible: the status line is rendered under the sheet, so a
  // sighted visitor who tries an illegal patch is told too, not only a
  // screen-reader user.
  function say(msg) {
    var n = $("wpSay");
    if (n) n.textContent = msg;
  }

  /* =====================================================================
     THE RUN - a replay of a recorded run, never live inference

     The recorded run has ONE child under ONE parent. The first wired
     device->pico->nano chain replays it; any extra tiles the visitor adds
     say they were not part of the recorded run rather than inventing
     numbers for them.
     ===================================================================== */
  function chainOf() {
    var devWire = null, escWire = null;
    for (var i = 0; i < PATCH.wires.length; i++) {
      var w = PATCH.wires[i];
      var f = tileById(w.fromId), t = tileById(w.toId);
      if (!devWire && w.kind === "channel" && f && (f.kind === "device" || f.kind === "intake") &&
          t && t.tier === "pico") devWire = w;
    }
    if (devWire) {
      for (var j = 0; j < PATCH.wires.length; j++) {
        var w2 = PATCH.wires[j];
        if (w2.kind === "escalate" && w2.fromId === devWire.toId) { escWire = w2; break; }
      }
    }
    var src = devWire ? tileById(devWire.fromId) : null;
    return { devWire: devWire, escWire: escWire, userSource: !!(src && src.kind === "intake") };
  }

  function run() { replay(false); }

  function replay(quiet) {
    var m = PATCH.measured;
    if (!m) return;
    PATCH.tiles.forEach(function (t) {
      if (t.kind === "model") { t.lamp = "idle"; t.bubble = null; t.margin = null; t.inRun = false; }
    });

    var chain = chainOf();
    var pico = chain.devWire ? tileById(chain.devWire.toId) : null;
    var parent = chain.escWire ? tileById(chain.escWire.toId) : null;
    var nano = parent && parent.tier === "nano" ? parent : null;
    var floor = pico ? pico.data.floor : 2.0;

    var recs = m.records.slice(0, 40);
    var esc = recs.filter(function (r) { return r.child.margin < floor; });
    var asserted = recs.length - esc.length;

    if (pico && chain.userSource) {
      // The visitor's own bytes: no model runs in a browser, and a margin is a
      // logprob difference nothing here can compute. What the paste earns is
      // the request envelope, and the bubble says exactly that.
      pico.inRun = true;
      pico.lamp = "idle";
      pico.bubble = { text: "DRAFT · NOT RUN - open the envelope", esc: false };
      pico.draft = true;
    } else if (pico) {
      pico.inRun = true;
      pico.lamp = esc.length ? "escalate" : "ok";
      pico.bubble = { text: asserted + " asserted · " + esc.length + " escalated", esc: !!esc.length };
      pico.margin = median(recs.map(function (r) { return r.child.margin; }));
      pico.draft = false;
    }
    if (nano && chain.userSource) {
      nano.inRun = true;
      nano.lamp = "idle";
      nano.bubble = null;
    } else if (nano) {
      nano.inRun = true;
      var fixed = esc.filter(function (r) { return r.parent.prediction === r.truth; }).length;
      nano.lamp = esc.length ? "ok" : "idle";
      nano.margin = median(esc.map(function (r) { return r.parent.margin; }));
      nano.bubble = esc.length ? { text: "adjudicated " + esc.length + " · " + fixed + " matched truth", esc: false } : null;
    }
    // Tiles outside the replayed chain say so - numbers are never invented.
    PATCH.tiles.forEach(function (t) {
      if (t.kind === "model" && !t.inRun) {
        t.lamp = "cold";
        t.bubble = { text: "not in the recorded run", esc: false };
      }
    });

    render();
    if (esc.length && !quiet && chain.escWire && !chain.userSource) flyPacket(chain.escWire);
    paintResults();
    if (!quiet) say("Replayed " + recs.length + " recorded records. " + esc.length + " escalated.");
  }

  function median(xs) {
    if (!xs.length) return null;
    var a = xs.slice().sort(function (x, y) { return x - y; });
    return a[Math.floor(a.length / 2)];
  }

  // One packet, one wire, on a run. A packet may only move because a real
  // record moved; nothing here animates continuously.
  function flyPacket(wire) {
    if (REDUCED || !wire || !wire.d || !wire.node) return;
    var p = svg("circle", { class: "wp-packet", r: "4", cx: "0", cy: "0" });
    p.style.offsetPath = 'path("' + wire.d + '")';
    wire.node.appendChild(p);
    setTimeout(function () { if (p.parentNode) p.parentNode.removeChild(p); }, 1100);
  }

  /* =====================================================================
     RESULTS
     ===================================================================== */
  function currentConfig() {
    var m = PATCH.measured;
    if (!m) return null;
    var chain = chainOf();
    var pico = chain.devWire ? tileById(chain.devWire.toId) : null;
    var hasParent = !!chain.escWire;
    if (chain.userSource) return null; // your bytes were never in the measured sweep
    var name = !pico ? "parent-direct" : (!hasParent ? "child-only" : "child+parent@" + pico.data.floor.toFixed(1));
    // NO silent fallback: an unmapped topology says so, because quietly showing
    // child-only's numbers would have the gauges contradict the tiles.
    return m.escalation.configs.filter(function (c) { return c.config === name; })[0] || null;
  }

  function paintResults() {
    var m = PATCH.measured;
    if (!m) return;
    var c = currentConfig();
    var un = $("wpUnmeasured");
    if (c) {
      setText("wpMacro", (c.macro_recall * 100).toFixed(1) + "%");
      setText("wpRaw", "raw " + (c.raw * 100).toFixed(1) + "%");
      setText("wpEsc", (c.escalation_rate * 100).toFixed(1) + "%");
      setText("wpCost", (c.pct_of_parent_everywhere * 100).toFixed(0) + "%");
      setText("wpConfig", c.config);
      if (un) un.textContent = "";
    } else {
      ["wpMacro", "wpEsc", "wpCost"].forEach(function (id) { setText(id, "—"); });
      setText("wpRaw", ""); setText("wpConfig", "");
      if (un) un.textContent = "This patch is not one of the measured configurations - " +
        "the sweep covers floors 0.5 to 2.0. The tiles above still show the recorded run.";
    }
    var prov = $("wpProv");
    if (prov) {
      prov.textContent = "Replay of " + shortName(m.escalation.child) + " under " +
        shortName(m.escalation.parent) + ", " + m.escalation.n + " items on " +
        shortName(m.escalation.bench) + " · " + m._provenance.suite;
    }
    var cost = $("wpCostNote");
    if (cost) cost.textContent = m.escalation.cost_note || "";
    drawScope();
    paintFeed();
  }

  function shortName(p) { return String(p || "").split("/").pop(); }
  function setText(id, t) { var n = $(id); if (n) n.textContent = t; }

  function drawScope() {
    var host = $("wpScope");
    var sc = PATCH.scene;
    var note = $("wpScopeNote");
    if (!host || !sc || !sc.channel) return;
    while (host.firstChild) host.removeChild(host.firstChild);
    // Only the pump scene has a committed recording. Drawing its vibration
    // trace under an AGV template would label one device's data as another's -
    // the same refusal the wire inspector keeps.
    var onScene = PATCH.template && PATCH.template.asset === sc.asset_type;
    if (!onScene) {
      if (note) note.textContent = "No recorded signal for this device yet - the trace exists " +
        "for the cavitating pump scene only. Load that template to see it.";
      return;
    }
    if (note) note.textContent = "";
    var s = sc.channel.samples || [];
    if (!s.length) return;
    var W = 520, H = 96, lo = Math.min.apply(null, s), hi = Math.max.apply(null, s);
    var span = (hi - lo) || 1;
    host.setAttribute("viewBox", "0 0 " + W + " " + H);
    var d = s.map(function (v, i) {
      return (i ? "L" : "M") + (i / (s.length - 1) * W).toFixed(1) + " " +
        (H - 8 - ((v - lo) / span) * (H - 16)).toFixed(1);
    }).join("");
    host.appendChild(svg("path", { class: "wp-scope__line", d: d }));
    var onset = (PATCH.template && PATCH.template.fault && PATCH.template.fault.onset) || 0;
    if (onset > 0) {
      host.appendChild(svg("line", { class: "wp-scope__onset",
        x1: (onset * W).toFixed(1), x2: (onset * W).toFixed(1), y1: 4, y2: H - 4 }));
    }
  }

  function paintFeed() {
    var list = $("wpFeed");
    var m = PATCH.measured;
    if (!list || !m) return;
    var chain = chainOf();
    var pico = chain.devWire ? tileById(chain.devWire.toId) : null;
    var floor = pico ? pico.data.floor : 2.0;
    list.textContent = "";
    m.records.slice(0, 14).forEach(function (r) {
      var esc = r.child.margin < floor;
      var li = el("li", "wp-rec" + (esc ? " wp-rec--esc" : ""));
      li.appendChild(el("span", "wp-rec__kind", esc ? "escalate" : "assert"));
      li.appendChild(el("code", "wp-rec__id", r.node_id));
      li.appendChild(el("span", "wp-rec__pred", r.child.prediction));
      li.appendChild(el("span", "wp-rec__m", r.child.margin.toFixed(2)));
      if (esc) li.appendChild(el("span", "wp-rec__adj", "→ " + r.parent.prediction));
      list.appendChild(li);
    });
  }

  function inspectTile(t) {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    box.appendChild(el("b", null, t.data.label || t.kind));
    if (t.kind === "model") {
      // The faceplate IS the birth certificate: fields we have are shown, and
      // the digest is a visibly empty slot rather than an invented hash.
      var dl = el("dl", "wp-cert");
      [["tier", t.data.tier], ["margin floor", t.data.floor.toFixed(1)],
       ["frame", t.data.frame], ["digest", null]].forEach(function (row) {
        dl.appendChild(el("dt", null, row[0]));
        dl.appendChild(el("dd", row[1] == null ? "wp-cert__pending" : null,
                          row[1] == null ? "pending export" : row[1]));
      });
      box.appendChild(dl);
      // The envelope appears whenever the visitor's bytes are wired into THIS
      // tile - even alongside a simulated device. The recorded replay still
      // owns the bubble (it IS the recorded run); the draft owns the envelope.
      var userWire = PATCH.wires.filter(function (w) {
        var f = tileById(w.fromId);
        return w.toId === t.id && f && f.kind === "intake" && f.data.body;
      })[0];
      if (t.draft || userWire) {
        var src = userWire ? tileById(userWire.fromId) : (function () {
          var c2 = chainOf();
          return c2.devWire ? tileById(c2.devWire.fromId) : null;
        })();
        if (src && src.kind === "intake" && src.data.body) {
          box.appendChild(el("p", "wp-tag wp-tag--draft", "DRAFT · NOT RUN"));
          box.appendChild(el("p", "wp-note",
            "No model was called - this stayed in your browser. This is the exact request " +
            "your bytes become on a stock llama-server; run it yourself, or wait for the " +
            "in-browser tier (Q4, ~65MB, certified)."));
          var pre2 = el("pre", "wp-wirebytes", envelopeFor(t, src.data.body));
          box.appendChild(pre2);
        }
      }
      if (PATCH.tiles.indexOf(t) > 3) {
        var rm = el("button", "wp-remove", "remove this module");
        rm.type = "button";
        rm.addEventListener("click", function () { removeTile(t.id); box.textContent = ""; });
        box.appendChild(rm);
      }
    } else if (t.kind === "device") {
      box.appendChild(el("p", "wp-note", t.data.cause.replace(/_/g, " ") + " moves " +
        t.data.channels.map(function (c) { return c.name.replace(/_/g, " ") + " " + c.dir; }).join(", ")));
    }
    if (t.bubble) box.appendChild(el("p", "wp-note", t.bubble.text));
  }

  // Clicking a cable shows what is actually travelling down it. Modality is a
  // property of the WIRE. Only the recorded pump scene has committed renders,
  // so other cables say so rather than showing bytes that were never produced.
  function inspectWire(wire) {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    if (wire.kind === "escalate") {
      box.appendChild(el("b", null, "Escalation link"));
      box.appendChild(el("p", "wp-note",
        "Carries a typed record rightward when the child's margin falls below its floor. " +
        "These records carry no evidence dict - the recorded export does not include one."));
    } else {
      box.appendChild(el("b", null, "Channel link"));
      var sc = PATCH.scene;
      var onScene = sc && PATCH.template && PATCH.template.asset === sc.asset_type;
      if (onScene && sc.renders) {
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
        box.appendChild(el("p", "wp-note", "The same truth, in every dialect a plant might speak. RECORDED."));
        box.appendChild(tabs);
        box.appendChild(pre);
      } else {
        box.appendChild(el("p", "wp-note",
          "Carries a channel window. Recorded wire formats exist for the cavitating pump " +
          "scene only - load that template to read the actual bytes."));
      }
    }
    var un = el("button", "wp-remove", "disconnect");
    un.type = "button";
    un.addEventListener("click", function () { disconnect(wire.id); box.textContent = ""; });
    box.appendChild(un);
  }

  /* ---- the list mirror: the same graph, always in the DOM -------------- */
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
        .map(function (w) { var d = tileById(w.toId); return d ? (d.data.label || d.kind) : ""; });
      if (out.length) li.textContent += " → " + out.join(", ");
      m.appendChild(li);
    });
  }

  /* =====================================================================
     EVENTS
     ===================================================================== */
  function onSheetClick(e) {
    if (PATCH.dragMoved) { PATCH.dragMoved = false; return; }
    var port = e.target.closest && e.target.closest(".wp-port");
    var host = e.target.closest && e.target.closest(".wp-tile");
    if (port && host) {
      e.preventDefault();
      var t = tileById(host.dataset.tile);
      if (!t) return;
      if (PATCH.armed) {
        if (port.dataset.port === "in") tryConnect(t.id);
        else disarm();
        return;
      }
      if (port.dataset.port !== "in") armPort(t.id, port.dataset.port, port.dataset.chan);
      return;
    }
    if (host) {
      var tt = tileById(host.dataset.tile);
      if (PATCH.armed) { tryConnect(tt.id); return; }
      if (tt.kind === "intake") { openIntake(); return; }
      PATCH.selected = tt.id;
      PATCH.focusId = tt.id;
      PATCH.refocus = true;
      inspectTile(tt);
      render();
      return;
    }
    if (PATCH.armed) disarm();
  }

  function onKey(e) {
    if (e.key === "Escape" && PATCH.armed) { disarm(); say("Cancelled."); return; }
    var host = document.activeElement && document.activeElement.closest
      && document.activeElement.closest(".wp-tile");
    if (!host) return;
    var t = tileById(host.dataset.tile);
    if (!t) return;
    if (e.key === "c" || e.key === "C") {
      e.preventDefault();
      if (t.kind === "device") armPort(t.id, "out", t.data.channels[0] && t.data.channels[0].name);
      else if (t.kind === "model") armPort(t.id, "up");
      return;
    }
    if (e.key === "Enter" && PATCH.armed) { e.preventDefault(); tryConnect(t.id); return; }
    if ((e.key === "Delete" || e.key === "Backspace") && t.kind === "model" && PATCH.tiles.indexOf(t) > 3) {
      e.preventDefault();
      removeTile(t.id);
      return;
    }
    if (e.key.indexOf("Arrow") === 0) {
      e.preventDefault();
      moveFocus(t, e.key);
    }
  }

  // Arrows mean something: left/right walks the ladder, up/down walks a column.
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

  function renderTemplates() {
    var shelf = $("wpShelf");
    if (!shelf) return;
    shelf.textContent = "";
    TEMPLATES.forEach(function (tpl) {
      var b = el("button", "wp-card");
      b.type = "button";
      b.appendChild(el("b", null, tpl.label));
      b.appendChild(el("span", null, tpl.fault.kind + " " + tpl.fault.channel.replace(/_/g, " ")));
      b.addEventListener("click", function () {
        shelf.querySelectorAll(".wp-card").forEach(function (n) { n.setAttribute("aria-pressed", "false"); });
        b.setAttribute("aria-pressed", "true");
        loadTemplate(tpl);
        say("Loaded " + tpl.label + ".");
      });
      shelf.appendChild(b);
    });
    var first = shelf.querySelector(".wp-card");
    if (first) first.setAttribute("aria-pressed", "true");
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

  function classify(text) {
    var t = text.trim();
    if (!t) return null;

    // wire blob: count fingerprint hits per modality
    var scores = FINGERPRINTS.map(function (f) {
      var hits = f.probes.filter(function (pr) { return t.indexOf(pr) >= 0; });
      return { mod: f.mod, hits: hits.length, evidence: hits };
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

    // raw numbers: mostly numeric tokens
    var tokens = t.split(/[\s,;]+/).filter(Boolean);
    var nums = tokens.filter(function (x) { return /^-?\d+(\.\d+)?$/.test(x); });
    if (nums.length >= 8 && nums.length / tokens.length >= 0.6) {
      return { kind: "numbers", n: nums.length, samples: nums.slice(0, 96).map(Number) };
    }

    // scenario in words: a catalogue token lookup, not NLU
    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var low = t.toLowerCase();
    for (var i = 0; i < TEMPLATES.length; i++) {
      var tpl = TEMPLATES[i];
      // Stems, not whole words: the founder's own phrasing is "cavitating pump",
      // and "cavitating" does not contain "cavitation" - but both contain
      // "cavita". Six characters of stem is enough to be specific in this
      // catalogue, and the fault vocabulary counts too ("stuck vibration").
      var words = tpl.asset.split("_")
        .concat(tpl.cause.split("_"))
        .concat([tpl.fault.kind, tpl.fault.channel.split("_")[0]]);
      var hit = words.filter(function (w) {
        if (w.length <= 3) return false;
        var stem = w.length > 6 ? w.slice(0, 6) : w;
        return low.indexOf(stem) >= 0;
      });
      if (hit.length >= 2) return { kind: "scenario", template: tpl, evidence: hit };
    }
    for (var name in cat) {
      var words = name.split("_").filter(function (w) { return w.length > 3; });
      if (words.length && words.every(function (w) { return low.indexOf(w) >= 0; })) {
        return { kind: "scenario-asset", asset: name, evidence: words };
      }
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
      mod.textContent = "raw numbers  ↳ " + v.n + " numeric samples";
      shape.textContent = "one channel · unit NOT STATED IN THE WIRE - a defaulted unit would be an invented fact";
      frame.textContent = "T01 sensor-health · frame text — pending export —";
      send.disabled = false;
    } else if (v.kind === "scenario" || v.kind === "scenario-asset") {
      mod.textContent = "a scenario, in words  ↳ matched " + v.evidence.join(", ");
      shape.textContent = "words are never sent to a Wave model";
      frame.textContent = "—";
      if (v.kind === "scenario") {
        note.textContent = "This maps to the \"" + v.template.label + "\" template.";
        send.textContent = "LOAD " + v.template.label.toUpperCase() + " →";
        send.disabled = false;
      } else {
        note.textContent = "That device is in the catalogue (" + labelOf(v.asset) +
          ") but no template carries it yet.";
      }
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
    if (v.kind === "scenario") {
      var shelf = $("wpShelf");
      var cards = shelf ? shelf.querySelectorAll(".wp-card") : [];
      for (var i = 0; i < cards.length; i++) {
        if (cards[i].textContent.indexOf(v.template.label) >= 0) { cards[i].click(); break; }
      }
      closeIntake();
      return;
    }
    var t = PATCH.tiles.filter(function (x) { return x.kind === "intake"; })[0];
    if (!t) return;
    if (v.kind === "blob") {
      t.data.modality = v.mod;
      t.data.channels = [{ name: v.tag || "your channel", unit: "" }];
      t.data.body = INTAKE.text;
    } else if (v.kind === "numbers") {
      t.data.modality = "raw numbers";
      t.data.channels = [{ name: "your channel", unit: "" }];
      t.data.body = INTAKE.text;
    } else {
      return;
    }
    closeIntake();
    render();
    say("Your data is on the sheet. Tap its filled port, then Wave Pico's input, to wire it in.");
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
  function envelopeFor(t, body) {
    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var anyAsset = cat[Object.keys(cat)[0]] || {};
    var candidates = (anyAsset.sensor_faults && anyAsset.sensor_faults.length)
      ? anyAsset.sensor_faults : ["ok", "stuck", "dropout", "noisy", "drifting", "railed"];
    var lines = [
      "# one request PER CANDIDATE - grammar locks the reply to that candidate;",
      "# margin = best logprob sum - runner-up (EOG token excluded, leading space)",
      "POST ${LLAMA_SERVER}/completion",
      "{",
      '  "prompt": <task frame — pending export — + your input body below>,',
      '  "grammar": "root ::= \\" ' + candidates[1] + '\\"",   # and one per candidate:',
      "  # " + candidates.map(function (c) { return '"root ::= \\" ' + c + '\\""'; }).join(", "),
      '  "n_predict": 16,',
      '  "n_probs": 1,',
      '  "cache_prompt": true   # candidates share one prompt eval',
      "}",
      "",
      "# input body - your bytes, verbatim (" + body.length + " chars):",
    ];
    return lines.join("\n") + "\n" + body;
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
    // #mesh deep-links to the engineering deck, so a patch can be linked to.
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
      renderTemplates();
      renderRail();
      loadTemplate(TEMPLATES[0]);
      var sheet = $("wpSheet");
      sheet.addEventListener("click", onSheetClick);
      document.addEventListener("keydown", onKey);
      var runBtn = $("wpRun");
      if (runBtn) runBtn.addEventListener("click", run);
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

  function start() { wireModeSwitch(); maybeBoot(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
