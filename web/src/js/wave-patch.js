/* =====================================================================
   RogerAI - THE PATCH SHEET (Playbox / WAVE MESH)

   Wire a machine to a model. Devices sit at the bottom, models above them,
   a person at the top; escalation only ever travels UP. You patch a channel
   into a model, press RUN, and watch what it asserts - and where its margin
   was too thin to assert at all.

   THE DESIGN CALLS, and why (research brief 2026-08-13):

   CLICK TO CONNECT, not drag. Tap a port, it arms, tap the target. Escape
     cancels. A drag onto a 10px port with your finger covering it is why
     Node-RED's oldest open issue was "Mobile/Tablet Support" for 13 years.
     Click-to-connect is ALSO the keyboard path, so pointer, touch and
     keyboard drive one state machine instead of three. Drag still works as
     an unadvertised fallthrough for people who expect it (150ms/6px, the
     constants ComfyUI ships).

   TIER LANES, not a free canvas. The graph is a hierarchy, so geometry IS
     semantics: up is escalation, left-to-right is execution order. Nothing
     can be dropped somewhere meaningless, wires cannot point backwards, and
     ComfyUI's most-reported confusion - that visual order is not execution
     order - cannot occur here.

   SHAPES, NOT HUES. Filled disc / hollow ring = a channel; filled / hollow
     square = an assertion record. The catalogue has 13 quantity kinds, which
     is a paint chart rather than a colour code, and the brand spends its one
     red on the live glint. NE 107 status ships distinct SYMBOLS as well as
     colours, so shape alone satisfies the standard.

   ONE PACKET, ONE WIRE, ON A RUN. Never a stream. Node-RED refuses to
     animate messages on wires at all because it floods the editor; the one
     contrib that does ships a warning. It also happens to be the honest
     choice: this deck replays a RECORDED run, so a packet may only move
     when a real record moved.

   The rack's faceplates are birth certificates. Fields we have are shown;
   the model digest renders as a visibly empty slot until the artifact is
   exported, because a faceplate that is meant to BE a birth certificate
   cannot ship with an invented hash.
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

  /* ---- geometry -------------------------------------------------------
     n8n's placement invariant: the slot pitch is tile width + gap, so a tile
     added by clicking lands exactly where a tidy-up would have put it. Then
     the sheet can never need tidying, and we never write a layout pass. */
  var TILE_W = 168, TILE_H = 84, GAP = 28;
  var PITCH = TILE_W + GAP;
  var LANE_H = 132;
  var LANES = ["human", "nano", "pico", "device"];   // top to bottom; escalation goes up
  var PAD_X = 24, PAD_Y = 18;

  function laneY(tier) {
    var i = LANES.indexOf(tier);
    return PAD_Y + (i < 0 ? LANES.length - 1 : i) * LANE_H;
  }
  function slotX(slot) { return PAD_X + slot * PITCH; }

  var PATCH = {
    catalog: null, measured: null, scene: null,
    tiles: [], wires: [], seq: 1,
    armed: null,          // {tileId, port} while a connection is in flight
    selected: null,
    template: null,
    ran: false,
  };

  /* =====================================================================
     TEMPLATES - the product, not a shortcut

     The sheet is never empty. A visitor arrives with a patch already wired,
     learns by modifying rather than constructing, and most never wire
     anything at all. Each template names a real asset and a real root cause
     from the catalogue, and a different sensor fault, so the five fault
     kinds get airtime without a five-item dropdown.
     ===================================================================== */
  var TEMPLATES = [
    { id: "pump",    label: "Cavitating pump",     asset: "centrifugal_pump", cause: "cavitation",
      fault: { channel: "vibration", kind: "stuck", severity: 0.9, onset: 0.4 }, floor: 2.0 },
    { id: "gearbox", label: "Gearbox running dry", asset: "gearbox", cause: "lubrication_loss",
      fault: { channel: "oil_temp", kind: "drifting", severity: 0.8, onset: 0.3 }, floor: 2.0 },
    { id: "agv",     label: "AGV battery aging",   asset: "agv", cause: "battery_aging",
      fault: { channel: "battery_volt", kind: "noisy", severity: 0.7, onset: 0.2 }, floor: 2.5 },
    { id: "spindle", label: "CNC spindle chatter", asset: "cnc_spindle", cause: "chatter",
      fault: { channel: "vibration", kind: "railed", severity: 0.85, onset: 0.5 }, floor: 2.0 },
    { id: "motor",   label: "Motor phase imbalance", asset: "induction_motor", cause: "phase_imbalance",
      fault: { channel: "current_a", kind: "dropout", severity: 0.8, onset: 0.35 }, floor: 2.5 },
    { id: "chiller", label: "Chiller losing charge", asset: "chiller", cause: "refrigerant_loss",
      fault: { channel: "suction_press", kind: "stuck", severity: 0.75, onset: 0.45 }, floor: 2.0 },
  ];

  /* =====================================================================
     BUILDING A PATCH
     ===================================================================== */
  function tile(kind, tier, slot, data) {
    return {
      id: "t" + (PATCH.seq++), kind: kind, tier: tier, slot: slot,
      data: data || {}, lamp: "idle", bubble: null,
    };
  }

  function loadTemplate(tpl) {
    PATCH.template = tpl;
    PATCH.tiles = [];
    PATCH.wires = [];
    PATCH.seq = 1;
    PATCH.ran = false;
    PATCH.armed = null;

    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var asset = cat[tpl.asset];
    if (!asset) return;

    // Only the channels this root cause actually MOVES become ports. A pump has
    // eight channels and cavitation touches four of them; drawing all eight would
    // turn the sheet into a wiring harness for no gain. The catalogue already
    // names which, and in which direction.
    var effects = asset.root_causes[tpl.cause] || {};
    var channels = Object.keys(effects).map(function (c) {
      return { name: c, dir: effects[c], unit: (asset.channels[c] || {}).unit || "" };
    });

    var dev = tile("device", "device", 0, {
      asset: tpl.asset, label: labelOf(tpl.asset), cause: tpl.cause,
      channels: channels, fault: tpl.fault, live: asset.source === "live",
    });
    var pico = tile("model", "pico", 0, { label: "Wave Pico", tier: "pico", floor: tpl.floor, frame: "A" });
    var nano = tile("model", "nano", 0, { label: "Wave Nano", tier: "nano", floor: 2.5, frame: "B" });
    var human = tile("human", "human", 0, { label: "Operator" });
    PATCH.tiles = [dev, pico, nano, human];

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
    // Node-RED's load-bearing constraint: a tile has at most ONE assertion input.
    // It makes "what feeds this?" answerable at a glance and splicing unambiguous.
    if (toPort === "in" && kind === "escalate") {
      PATCH.wires = PATCH.wires.filter(function (w) {
        return !(w.toId === toId && w.kind === "escalate");
      });
    }
    PATCH.wires.push({ id: "w" + (PATCH.seq++), fromId: fromId, fromPort: fromPort,
                       toId: toId, toPort: toPort, kind: kind });
  }

  function tileById(id) {
    for (var i = 0; i < PATCH.tiles.length; i++) if (PATCH.tiles[i].id === id) return PATCH.tiles[i];
    return null;
  }

  /* =====================================================================
     RENDER
     ===================================================================== */
  function render() {
    var sheet = $("wpSheet");
    if (!sheet) return;
    var wires = $("wpWires");
    sheet.querySelectorAll(".wp-tile").forEach(function (n) { n.remove(); });
    while (wires.firstChild) wires.removeChild(wires.firstChild);

    // The sheet is exactly as wide as its widest lane needs. No pan, no zoom:
    // at this size the graph always fits, and dropping zoom deletes a whole
    // class of coordinate bugs.
    var maxSlot = 0;
    PATCH.tiles.forEach(function (t) { if (t.slot > maxSlot) maxSlot = t.slot; });
    // The sheet is exactly as big as the patch, and centred. A canvas three
    // times wider than anything on it reads as "you have failed to fill this",
    // which is the opposite of the intended "this is the whole machine".
    var w = PAD_X * 2 + (maxSlot + 1) * PITCH - GAP;
    var h = PAD_Y * 2 + (LANES.length - 1) * LANE_H + TILE_H;
    sheet.style.width = w + "px";
    sheet.style.height = h + "px";
    wires.setAttribute("viewBox", "0 0 " + w + " " + h);
    wires.setAttribute("width", w);
    wires.setAttribute("height", h);

    PATCH.wires.forEach(function (wire) { drawWire(wires, wire); });
    PATCH.tiles.forEach(function (t) { sheet.appendChild(drawTile(t)); });
    renderMirror();
  }

  function portPos(t, port) {
    var x = slotX(t.slot), y = laneY(t.tier);
    if (port === "in") return { x: x + TILE_W / 2, y: y + TILE_H };       // bottom edge
    return { x: x + TILE_W / 2, y: y };                                    // top edge (out / up)
  }

  /* ---- orthogonal wires with quadratic corners -------------------------
     Not bezier. These are industrial people whose native diagram language is
     orthogonal - P&IDs, ladder logic, single-line drawings - and a right
     angle reads as engineered where a curve reads as organic. It also means
     the path has real interior vertices, so marker-mid arrows actually
     render (they draw nothing on a single bezier).

     Because the sheet is tier-layered the path is always up -> across -> up:
     three segments, two corners. getBend clamps each corner radius to half
     the shorter adjacent segment so radii never overlap on a tight zigzag. */
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
    var OFF = 20;
    var a = { x: from.x, y: from.y - OFF };          // leave the port perpendicular
    var d = { x: to.x, y: to.y + OFF };
    var mid = (a.y + d.y) / 2;
    var b = { x: a.x, y: mid }, c = { x: d.x, y: mid };
    return "M" + from.x + " " + from.y + "L" + a.x + " " + a.y +
      bend(a, b, c, 6) + bend(b, c, d, 6) + "L" + d.x + " " + d.y + "L" + to.x + " " + to.y;
  }

  function drawWire(host, wire) {
    var f = tileById(wire.fromId), t = tileById(wire.toId);
    if (!f || !t) return;
    var d = wirePath(portPos(f, wire.fromPort), portPos(t, wire.toPort));
    wire.d = d;

    var g = svg("g", { class: "wp-wire wp-wire--" + wire.kind });
    // An escalation is an exception route, so it is dotted - the visitor can
    // see the exception path before anything fires down it.
    g.appendChild(svg("path", { class: "wp-wire__line", d: d, "marker-mid": "url(#wpArrow)" }));
    // A 20px transparent path is the hit target; 32px on coarse pointers. Zero
    // stroke OPACITY (not stroke:none) so visibleStroke still hit-tests it.
    var hit = svg("path", { class: "wp-wire__hit", d: d });
    hit.addEventListener("click", function (e) { e.stopPropagation(); inspectWire(wire); });
    g.appendChild(hit);
    host.appendChild(g);
    wire.node = g;
  }

  function drawTile(t) {
    var n = el("div", "wp-tile wp-tile--" + t.kind);
    n.style.left = slotX(t.slot) + "px";
    n.style.top = laneY(t.tier) + "px";
    n.style.width = TILE_W + "px";
    n.dataset.tile = t.id;
    n.tabIndex = -1;
    n.setAttribute("role", "group");
    n.setAttribute("aria-label", describe(t));
    if (t.data.live) n.classList.add("is-live");
    if (PATCH.selected === t.id) n.classList.add("is-sel");

    // The engraved plate: on a model tile it is literally the birth certificate.
    var plate = el("div", "wp-tile__plate");
    plate.appendChild(el("b", "wp-tile__name", t.data.label || t.kind));
    if (t.kind === "model") plate.appendChild(el("span", "wp-tile__tier", t.data.tier));
    if (t.data.live) plate.appendChild(el("span", "wp-tile__live", "REAL"));
    n.appendChild(plate);

    if (t.kind === "device") {
      var meta = el("div", "wp-tile__meta");
      meta.appendChild(el("span", null, t.data.cause.replace(/_/g, " ")));
      n.appendChild(meta);
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
      track.appendChild(el("span", "wp-vu__bar"));
      // The floor is a redline TICK, not a colour change. It moves when the
      // frame changes, which is the teaching moment.
      var red = el("span", "wp-vu__red");
      red.style.left = (Math.min(1, t.data.floor / 4) * 100) + "%";
      track.appendChild(red);
      vu.appendChild(track);
      vu.appendChild(el("span", "wp-vu__k", "floor " + t.data.floor.toFixed(1) + " · frame " + t.data.frame));
      n.appendChild(vu);
      n.appendChild(lampFor(t));
    }

    if (t.kind === "human") {
      n.appendChild(el("div", "wp-tile__meta", "the ladder ends with a person"));
    }

    // Input port (bottom edge) for anything that can receive.
    if (t.kind !== "device") {
      var ip = el("button", "wp-port wp-port--in " + (t.kind === "human" ? "wp-port--rec" : "wp-port--chan"));
      ip.type = "button";
      ip.dataset.port = "in";
      ip.setAttribute("aria-label", "input");
      n.appendChild(ip);
    }
    // Escalation output (top edge) for models.
    if (t.kind === "model") {
      var up = el("button", "wp-port wp-port--rec wp-port--up");
      up.type = "button";
      up.dataset.port = "up";
      up.setAttribute("aria-label", "escalation out");
      n.appendChild(up);
    }

    // Make-style run bubble: the result appears ON the thing that produced it.
    if (t.bubble) {
      var b = el("button", "wp-bubble" + (t.bubble.esc ? " wp-bubble--esc" : ""));
      b.type = "button";
      b.textContent = t.bubble.text;
      b.addEventListener("click", function (e) { e.stopPropagation(); inspectTile(t); });
      n.appendChild(b);
    }
    return n;
  }

  var LAMPS = {
    idle:     { sym: "·", label: "idle" },
    ok:       { sym: "●", label: "OK" },
    check:    { sym: "△", label: "function check" },
    escalate: { sym: "↑", label: "escalated" },
    abstain:  { sym: "○", label: "abstained" },
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
    return t.data.label + ", the operator, top of the ladder";
  }

  /* =====================================================================
     CONNECTING - one state machine for pointer, touch and keyboard
     ===================================================================== */
  function armPort(tileId, port, chan) {
    PATCH.armed = { tileId: tileId, port: port, chan: chan };
    document.body.classList.add("wp-arming");
    markCompatible();
    say(port === "out" ? "Channel output armed. Choose a model input, or press Escape."
                       : "Escalation output armed. Choose a parent input, or press Escape.");
  }

  function disarm() {
    PATCH.armed = null;
    document.body.classList.remove("wp-arming");
    var sheet = $("wpSheet");
    if (sheet) sheet.querySelectorAll(".wp-port").forEach(function (p) {
      p.classList.remove("is-ok", "is-no");
    });
  }

  // Compatibility is a perception task, not a memory task: once a wire is armed
  // the ports that can receive it pulse, and the ones that cannot dim.
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
    // Wires only ever go up a tier. Geometry is the rule, so an illegal patch
    // is not something the visitor has to be told about - it is unreachable.
    return LANES.indexOf(to.tier) < LANES.indexOf(from.tier);
  }

  function tryConnect(toTileId) {
    var a = PATCH.armed;
    if (!a) return;
    var from = tileById(a.tileId), to = tileById(toTileId);
    if (!canConnect(from, to)) { say("Those cannot be connected."); return; }
    connect(from.id, a.port, to.id, "in", a.port === "up" ? "escalate" : "channel");
    disarm();
    PATCH.ran = false;
    render();
    say("Connected " + (from.data.label || from.kind) + " to " + (to.data.label || to.kind) + ".");
  }

  function say(msg) {
    var n = $("wpSay");
    if (n) n.textContent = msg;
  }

  /* =====================================================================
     THE RUN - a replay, never a live inference

     Records come from a recorded fleet_sim run. A packet may only travel a
     wire because a real record travelled it; the deck states this
     permanently, and the ids stay on screen so it is checkable.
     ===================================================================== */
  function run() { replay(false); }

  function replay(quiet) {
    var m = PATCH.measured;
    if (!m) return;
    var pico = PATCH.tiles.filter(function (t) { return t.tier === "pico"; })[0];
    var nano = PATCH.tiles.filter(function (t) { return t.tier === "nano"; })[0];
    var floor = pico ? pico.data.floor : 2.0;

    var recs = m.records.slice(0, 40);
    var esc = recs.filter(function (r) { return r.child.margin < floor; });
    var asserted = recs.length - esc.length;

    if (pico) {
      pico.lamp = esc.length ? "escalate" : "ok";
      pico.bubble = { text: asserted + " asserted · " + esc.length + " escalated", esc: !!esc.length };
    }
    if (nano) {
      var fixed = esc.filter(function (r) { return r.parent.prediction === r.truth; }).length;
      nano.lamp = esc.length ? "ok" : "idle";
      nano.bubble = esc.length ? { text: "adjudicated " + esc.length + " · " + fixed + " matched truth", esc: false } : null;
    }
    PATCH.ran = true;
    render();
    if (esc.length && !quiet) flyPacket();
    paintResults();
    if (!quiet) say("Replayed " + recs.length + " recorded records. " + esc.length + " escalated.");
  }

  // One packet, one wire, on a run. CSS offset-path so the compositor can run
  // it off the main thread; the inline style is a STYLE attribute, which the
  // CSP permits, not an inline script.
  function flyPacket() {
    if (REDUCED) return;
    var wire = PATCH.wires.filter(function (w) { return w.kind === "escalate"; })[0];
    if (!wire || !wire.d || !wire.node) return;
    var p = svg("circle", { class: "wp-packet", r: "4", cx: "0", cy: "0" });
    p.style.offsetPath = 'path("' + wire.d + '")';
    wire.node.appendChild(p);
    setTimeout(function () { if (p.parentNode) p.parentNode.removeChild(p); }, 1100);
  }

  /* =====================================================================
     RESULTS - the scope, the curve, the feed
     ===================================================================== */
  function currentConfig() {
    var m = PATCH.measured;
    if (!m) return null;
    var pico = PATCH.tiles.filter(function (t) { return t.tier === "pico"; })[0];
    var hasParent = PATCH.wires.some(function (w) { return w.kind === "escalate"; });
    var name = !pico ? "parent-direct" : (!hasParent ? "child-only" : "child+parent@" + pico.data.floor.toFixed(1));
    return m.escalation.configs.filter(function (c) { return c.config === name; })[0]
        || m.escalation.configs.filter(function (c) { return c.config === "child-only"; })[0];
  }

  function paintResults() {
    var m = PATCH.measured;
    if (!m) return;
    var c = currentConfig();
    if (c) {
      setText("wpMacro", (c.macro_recall * 100).toFixed(1) + "%");
      setText("wpRaw", "raw " + (c.raw * 100).toFixed(1) + "%");
      setText("wpEsc", (c.escalation_rate * 100).toFixed(1) + "%");
      setText("wpCost", (c.pct_of_parent_everywhere * 100).toFixed(0) + "%");
      setText("wpConfig", c.config);
    }
    var prov = $("wpProv");
    if (prov) {
      prov.textContent = "Replay of " + shortName(m.escalation.child) + " under " +
        shortName(m.escalation.parent) + ", " + m.escalation.n + " items on " +
        shortName(m.escalation.bench) + " · " + m._provenance.suite;
    }
    var cost = $("wpCostNote");
    // cost_note is in the data and was never rendered. On a dial labelled
    // "cost" that omission turns a residency proxy into a claim about money.
    if (cost) cost.textContent = m.escalation.cost_note || "";
    drawScope();
    paintFeed();
  }

  function shortName(p) { return String(p || "").split("/").pop(); }
  function setText(id, t) { var n = $(id); if (n) n.textContent = t; }

  // The scope: the real 96 samples, so the flatline at onset is visible as a
  // shape rather than described as a number.
  function drawScope() {
    var host = $("wpScope");
    var sc = PATCH.scene;
    if (!host || !sc || !sc.channel) return;
    while (host.firstChild) host.removeChild(host.firstChild);
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
    var pico = PATCH.tiles.filter(function (t) { return t.tier === "pico"; })[0];
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
      // The faceplate IS the birth certificate. Fields we have are shown; the
      // digest is a visibly empty slot rather than an invented hash.
      var dl = el("dl", "wp-cert");
      [["tier", t.data.tier], ["margin floor", t.data.floor.toFixed(1)],
       ["frame", t.data.frame], ["digest", null]].forEach(function (row) {
        dl.appendChild(el("dt", null, row[0]));
        var dd = el("dd", row[1] == null ? "wp-cert__pending" : null,
                    row[1] == null ? "pending export" : row[1]);
        dl.appendChild(dd);
      });
      box.appendChild(dl);
    } else if (t.kind === "device") {
      box.appendChild(el("p", "wp-note", t.data.cause.replace(/_/g, " ") + " moves " +
        t.data.channels.map(function (c) { return c.name.replace(/_/g, " ") + " " + c.dir; }).join(", ")));
    }
    if (t.bubble) box.appendChild(el("p", "wp-note", t.bubble.text));
  }

  // Clicking a cable shows what is actually travelling down it. Modality is a
  // property of the WIRE, not a tab strip somewhere else on the page: the same
  // truth reads differently in each dialect, and OPC UA's substitute values and
  // Modbus's byte order are where a plant's data lies to you. Only the recorded
  // pump scene has committed renders, so other cables say so rather than
  // showing bytes that were never produced.
  function inspectWire(wire) {
    var box = $("wpInspect");
    if (!box) return;
    box.textContent = "";
    if (wire.kind === "escalate") {
      box.appendChild(el("b", null, "Escalation link"));
      box.appendChild(el("p", "wp-note",
        "Carries a typed record upward when the child's margin falls below its floor. " +
        "These records carry no evidence dict - the recorded export does not include one."));
      return;
    }
    box.appendChild(el("b", null, "Channel link"));
    var sc = PATCH.scene;
    var onScene = sc && PATCH.template && PATCH.template.asset === sc.asset_type;
    if (!onScene || !sc.renders) {
      box.appendChild(el("p", "wp-note",
        "Carries a channel window. Recorded wire formats exist for the cavitating pump " +
        "scene only - load that template to read the actual bytes."));
      return;
    }
    var tabs = el("div", "wp-dialects");
    var pre = el("pre", "wp-wirebytes");
    Object.keys(sc.renders).forEach(function (mo, i) {
      var b = el("button", "wp-dialect", mo);
      b.type = "button";
      b.setAttribute("aria-pressed", i === 0 ? "true" : "false");
      b.addEventListener("click", function (e) {
        e.stopPropagation();
        tabs.querySelectorAll(".wp-dialect").forEach(function (n) { n.setAttribute("aria-pressed", "false"); });
        b.setAttribute("aria-pressed", "true");
        pre.textContent = sc.renders[mo];
      });
      tabs.appendChild(b);
      if (i === 0) pre.textContent = sc.renders[mo];
    });
    box.appendChild(el("p", "wp-note", "The same truth, in every dialect a plant might speak. RECORDED."));
    box.appendChild(tabs);
    box.appendChild(pre);
  }

  /* ---- the list mirror: the same graph, always in the DOM -------------- */
  function renderMirror() {
    var m = $("wpMirror");
    if (!m) return;
    m.textContent = "";
    var order = PATCH.tiles.slice().sort(function (a, b) {
      return LANES.indexOf(b.tier) - LANES.indexOf(a.tier);
    });
    order.forEach(function (t) {
      var li = el("li", null, describe(t));
      var up = PATCH.wires.filter(function (w) { return w.fromId === t.id; })
        .map(function (w) { var d = tileById(w.toId); return d ? (d.data.label || d.kind) : ""; });
      if (up.length) li.textContent += " → " + up.join(", ");
      m.appendChild(li);
    });
  }

  /* =====================================================================
     WIRING UP
     ===================================================================== */
  function onSheetClick(e) {
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
      PATCH.selected = tt.id;
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
    if (e.key.indexOf("Arrow") === 0) {
      e.preventDefault();
      moveFocus(t, e.key);
    }
  }

  // Arrow keys mean something here: left/right walks a tier, up/down walks the
  // escalation ladder. That is only possible because the layout is the hierarchy.
  function moveFocus(from, key) {
    var want;
    if (key === "ArrowLeft" || key === "ArrowRight") {
      var lane = PATCH.tiles.filter(function (t) { return t.tier === from.tier; })
        .sort(function (a, b) { return a.slot - b.slot; });
      var i = lane.indexOf(from) + (key === "ArrowRight" ? 1 : -1);
      want = lane[i];
    } else {
      var li = LANES.indexOf(from.tier) + (key === "ArrowUp" ? -1 : 1);
      want = PATCH.tiles.filter(function (t) { return t.tier === LANES[li]; })[0];
    }
    if (!want) return;
    var n = document.querySelector('.wp-tile[data-tile="' + want.id + '"]');
    if (n) n.focus();
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

  function boot() {
    if (!$("wpSheet")) return;
    Promise.all([
      fetch("data/wave-catalog.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-measured.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-scene-recorded.json").then(function (r) { return r.ok ? r.json() : null; }),
    ]).then(function (res) {
      PATCH.catalog = res[0]; PATCH.measured = res[1]; PATCH.scene = res[2];
      if (!PATCH.catalog || !PATCH.measured) { fail(); return; }
      renderTemplates();
      loadTemplate(TEMPLATES[0]);
      var sheet = $("wpSheet");
      sheet.addEventListener("click", onSheetClick);
      document.addEventListener("keydown", onKey);
      var runBtn = $("wpRun");
      if (runBtn) runBtn.addEventListener("click", run);
    }).catch(fail);
  }

  function fail() {
    var s = $("wpSheet");
    if (s) s.appendChild(el("p", "wp-note", "The patch data did not load. Nothing here is live; reload to try again."));
  }


  /* =====================================================================
     THE MODE SWITCH - console / mesh

     Two decks on one page. This lived in the old mesh module; it moved here
     when that module was retired, because the switch outlives whatever the
     mesh deck happens to be.
     ===================================================================== */
  function showView(mode) {
    var consoleView = $("pgConsoleView"), mesh = $("pgMeshView");
    if (!consoleView || !mesh) return;
    var toMesh = mode === "mesh";
    consoleView.hidden = toMesh;
    mesh.hidden = !toMesh;
    // The hero is the deck's own headline; leaving "Open the console." above the
    // patch sheet would be the page contradicting itself in its largest type.
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
    // #mesh deep-links to the engineering deck, so a patch can be linked to at all.
    var hash = (window.location.hash || "").replace("#", "").toLowerCase();
    if (hash === "mesh" || hash === "console") { showView(hash); return; }
    var saved = "console";
    try { saved = window.localStorage.getItem("pb.mode") || "console"; } catch (e) { /* ignore */ }
    showView(saved === "mesh" ? "mesh" : "console");
  }

  // The mesh deck is behind a tab, so boot when it is first shown - and also
  // now, in case the hash opened straight onto it.
  function maybeBoot() {
    var v = $("pgMeshView");
    if (v && !v.hidden && !PATCH.booted) { PATCH.booted = true; boot(); }
  }
  function start() { wireModeSwitch(); maybeBoot(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
