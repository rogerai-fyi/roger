/* =====================================================================
   RogerAI - blip-map background (Canvas2D). index / models / voices.
   A faint dotted grid with a few quiet stations in the side margins. Every
   few seconds ONE station keys up: it flashes the live red and sends a
   single soft ring, then goes back to grey. A live radio network, heard
   from the next room.

   Calm by construction (2026-09-23, founder: "the red blips going back and
   forth ... too distracting"):
   - stations and rings live only in the margins beside the text column
     (measured from main .wrap / .hero__inner, clipped to those bands), so
     nothing moves behind words being read; no margin (phones), no stations
   - at most 10 stations, grey at rest; no comets, no receiver
   - the grid + stations are painted once to a base layer; the frame loop
     runs only while a ring is on air and sleeps between pings
   - hidden tab: every timer and frame is cancelled
   - reduced motion: the base layer is painted once, still, and that's it
   ===================================================================== */
(function () {
  "use strict";

  var canvas = document.getElementById("blipmap");
  if (!canvas) return;
  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  var ctx = canvas.getContext("2d", { alpha: true });
  var layer = document.createElement("canvas");   // grid + stations, painted once
  var lctx = layer.getContext("2d", { alpha: true });
  var DPR = Math.min(window.devicePixelRatio || 1, 2);
  var W = 0, H = 0;
  var GRID = 52;          // dot grid spacing (css px)
  var EDGE = 16;          // keep-out between a band and the column / rail
  var MAX_STATIONS = 10;
  var RING = 26;          // ring radius (css px)
  var RING_MS = 1800;     // one ring's life
  var nodes = [], bands = [], ring = null;
  var raf = null, timer = null;

  var COL = { grid: [21, 20, 15], idle: [154, 150, 139], live: [224, 35, 28] };
  function rgba(c, a) { return "rgba(" + c[0] + "," + c[1] + "," + c[2] + "," + a + ")"; }
  function toRGB(str) {
    str = (str || "").trim();
    if (str[0] === "#") {
      var h = str.slice(1);
      if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
      if (h.length >= 6) return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
      return null;
    }
    var m = str.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/i);
    return m ? [Math.round(+m[1]), Math.round(+m[2]), Math.round(+m[3])] : null;
  }
  // colours come from the live tokens, so light/dark drive the canvas
  function syncPalette() {
    var cs = window.getComputedStyle(document.documentElement);
    COL.grid = toRGB(cs.getPropertyValue("--ink-900")) || COL.grid;
    COL.idle = toRGB(cs.getPropertyValue("--ink-400")) || COL.idle;
    COL.live = toRGB(cs.getPropertyValue("--live")) || COL.live;
  }

  // the margins: left of the widest text column (right of the fixed rail)
  // and right of it. Bands narrower than a station's ring are dropped.
  function measureBands() {
    var L = W, R = 0;
    var cols = document.querySelectorAll("main .wrap, .hero__inner");
    for (var i = 0; i < cols.length; i++) {
      var r = cols[i].getBoundingClientRect(), cs = window.getComputedStyle(cols[i]);
      L = Math.min(L, r.left + parseFloat(cs.paddingLeft || 0));
      R = Math.max(R, r.right - parseFloat(cs.paddingRight || 0));
    }
    if (R <= L) return [];                         // nothing measured: no margins
    var rail = parseFloat(window.getComputedStyle(document.body).paddingLeft || 0) || 0;
    return [[rail + EDGE, L - EDGE], [R + EDGE, W - EDGE]].filter(function (b) { return b[1] - b[0] >= 24; });
  }

  function paintLayer() {
    layer.width = canvas.width; layer.height = canvas.height;
    lctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    lctx.clearRect(0, 0, W, H);
    for (var gx = GRID; gx < W; gx += GRID) {
      for (var gy = GRID; gy < H; gy += GRID) {
        lctx.fillStyle = rgba(COL.grid, 0.04);
        lctx.beginPath(); lctx.arc(gx, gy, 0.85, 0, Math.PI * 2); lctx.fill();
      }
    }
    for (var i = 0; i < nodes.length; i++) {
      lctx.fillStyle = rgba(COL.idle, 0.35);
      lctx.beginPath(); lctx.arc(nodes[i].x, nodes[i].y, nodes[i].r, 0, Math.PI * 2); lctx.fill();
    }
  }

  function build() {
    W = window.innerWidth; H = window.innerHeight;
    canvas.width = Math.round(W * DPR); canvas.height = Math.round(H * DPR);
    canvas.style.width = W + "px"; canvas.style.height = H + "px";
    ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    bands = measureBands();
    nodes = [];
    var area = 0;
    bands.forEach(function (b) { area += (b[1] - b[0]) * H; });
    var count = bands.length ? Math.min(MAX_STATIONS, Math.max(3, Math.round(area / 70000))) : 0;
    for (var i = 0; i < count; i++) {
      var b = bands[i % bands.length];
      nodes.push({
        x: b[0] + (b[1] - b[0]) * (0.2 + Math.random() * 0.6),
        y: H * (0.1 + 0.8 * (i + Math.random()) / count),   // spread down the page
        r: 1.4 + Math.random() * 0.8,
      });
    }
    ring = null;
    paintLayer();
    paint(0);
  }

  function paint(now) {
    ctx.clearRect(0, 0, W, H);
    ctx.drawImage(layer, 0, 0, W, H);
    if (!ring) return;
    var t = Math.min(1, (now - ring.at) / RING_MS);
    var e = 1 - Math.pow(1 - t, 3);                 // ease-out: the ring slows as it fades
    ctx.save();
    ctx.beginPath();
    bands.forEach(function (b) { ctx.rect(b[0], 0, b[1] - b[0], H); });
    ctx.clip();
    ctx.strokeStyle = rgba(COL.live, 0.12 * (1 - t));
    ctx.lineWidth = 1;
    ctx.beginPath(); ctx.arc(ring.n.x, ring.n.y, 3 + e * RING, 0, Math.PI * 2); ctx.stroke();
    ctx.fillStyle = rgba(COL.live, 0.5 * (1 - t));  // the keyed-up station glows red, then greys out
    ctx.beginPath(); ctx.arc(ring.n.x, ring.n.y, ring.n.r + 0.4, 0, Math.PI * 2); ctx.fill();
    ctx.restore();
  }

  // ~30fps is plenty for one slow ring
  var lastPaint = 0;
  function frame(now) {
    raf = null;
    if (now - lastPaint >= 32) { paint(now); lastPaint = now; }
    if (ring && now - ring.at < RING_MS) raf = window.requestAnimationFrame(frame);
    else { ring = null; paint(now); schedule(); }
  }

  function schedule() {
    if (!nodes.length || document.hidden) return;
    timer = window.setTimeout(function () {
      timer = null;
      ring = { n: nodes[Math.floor(Math.random() * nodes.length)], at: window.performance.now() };
      raf = window.requestAnimationFrame(frame);
    }, 3500 + Math.random() * 4500);
  }

  function stop() {
    if (timer) { window.clearTimeout(timer); timer = null; }
    if (raf) { window.cancelAnimationFrame(raf); raf = null; }
    ring = null;
  }

  syncPalette();
  build();
  if (!REDUCED) schedule();

  document.addEventListener("visibilitychange", function () {
    stop();
    if (!document.hidden && !REDUCED) schedule();
  });
  window.addEventListener("themechange", function () { syncPalette(); paintLayer(); paint(0); });
  var rt;
  window.addEventListener("resize", function () {
    window.clearTimeout(rt);
    rt = window.setTimeout(function () { stop(); build(); if (!REDUCED) schedule(); }, 160);
  });
})();
