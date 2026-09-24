/* =====================================================================
   RogerAI - blip-map background (Canvas2D). index / models / voices.
   A network of stations and links behind the page, driven by the reader's
   scroll rather than a timer:
   - it drifts with the scroll at 40% speed (parallax), so it reads as depth
   - the further down the page you have read, the more of the network is on
     air: links draw themselves in as you scroll (a high-water mark, so
     scrolling back up keeps what you have tuned in), and the CTA at the
     bottom sits on the whole network
   - while you scroll, the stations crossing a carrier line at mid-screen
     light up red, as bright as you scroll fast; stop, and it settles
   - behind the text column everything is drawn at a third of its strength;
     the colour lives in the margins
   At rest it is a still picture: no timers, no frame loop. A frame is
   requested only by a scroll event and the loop sleeps once the carrier
   has faded. A hidden tab cancels it. Reduced motion paints the full
   network once and never animates.
   ===================================================================== */
(function () {
  "use strict";

  var canvas = document.getElementById("blipmap");
  if (!canvas) return;
  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  var ctx = canvas.getContext("2d", { alpha: true });
  var DPR = Math.min(window.devicePixelRatio || 1, 2);
  var PARALLAX = 0.4;     // the network moves at 40% of the scroll
  var CELL = 150;         // one station per cell, at most (stratified, never clumped)
  var FILL = 0.35;        // share of cells that hold a station
  var LINK_MAX = 260;     // longest link (css px)
  var GRID = 52;          // background dot grid
  var DIM = 0.3;          // strength behind the text column
  var BASE = 0.3;         // share of links on air before any scrolling
  var BAND = 130;         // half-height of the carrier band (css px)
  var W = 0, H = 0, colL = 0, colR = 0;
  var nodes = [], links = [], pattern = null;
  var raf = null, lastY = 0, energy = 0, reach = 0;

  var COL = { grid: [21, 20, 15], idle: [154, 150, 139], live: [224, 35, 28] };
  function rgba(c, a) { return "rgba(" + c[0] + "," + c[1] + "," + c[2] + "," + Math.round(a * 1000) / 1000 + ")"; }
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
    var tile = document.createElement("canvas");
    tile.width = tile.height = GRID;
    var t = tile.getContext("2d");
    t.fillStyle = rgba(COL.grid, 0.05);
    t.beginPath(); t.arc(GRID / 2, GRID / 2, 0.85, 0, Math.PI * 2); t.fill();
    pattern = ctx.createPattern(tile, "repeat");
  }

  // the text column: the widest main .wrap / hero content box
  function measureColumn() {
    colL = W; colR = 0;
    var cols = document.querySelectorAll("main .wrap, .hero__inner");
    for (var i = 0; i < cols.length; i++) {
      var r = cols[i].getBoundingClientRect(), cs = window.getComputedStyle(cols[i]);
      colL = Math.min(colL, r.left + parseFloat(cs.paddingLeft || 0));
      colR = Math.max(colR, r.right - parseFloat(cs.paddingRight || 0));
    }
    if (colR <= colL) { colL = 0; colR = W; }
  }
  function strength(x) { return x > colL && x < colR ? DIM : 1; }

  // deterministic, so the network is the same map on every visit
  var seed = 7;
  function rand() {
    seed = (seed + 0x6d2b79f5) | 0;
    var t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  }

  function build() {
    W = window.innerWidth; H = window.innerHeight;
    canvas.width = Math.round(W * DPR); canvas.height = Math.round(H * DPR);
    canvas.style.width = W + "px"; canvas.style.height = H + "px";
    ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    measureColumn();
    seed = 7;
    var doc = document.documentElement.scrollHeight || H;
    var plane = H + Math.max(0, doc - H) * PARALLAX * 1.3 + CELL;
    nodes = []; links = [];
    for (var cy = 0; cy < plane; cy += CELL) {
      for (var cx = 0; cx < W; cx += CELL) {
        if (rand() > FILL) continue;
        var x = cx + CELL * (0.15 + rand() * 0.7), y = cy + CELL * (0.15 + rand() * 0.7);
        nodes.push({ x: x, y: y, r: 1.3 + rand() * 1.1, on: false, k: strength(x) });
      }
    }
    // each station links to its two nearest neighbours in reach
    for (var i = 0; i < nodes.length; i++) {
      var best = [];
      for (var j = 0; j < nodes.length; j++) {
        if (j === i) continue;
        var d = Math.abs(nodes[i].x - nodes[j].x) + Math.abs(nodes[i].y - nodes[j].y);
        if (d < LINK_MAX * 1.4) best.push([d, j]);
      }
      best.sort(function (a, b) { return a[0] - b[0]; });
      for (var b = 0; b < Math.min(2, best.length); b++) {
        var j2 = best[b][1];
        if (j2 < i && links.some(function (l) { return l.a === j2 && l.b === i; })) continue;
        var A = nodes[i], B = nodes[j2];
        if (Math.hypot(A.x - B.x, A.y - B.y) > LINK_MAX) continue;
        links.push({ a: i, b: j2, at: rand(), k: strength((A.x + B.x) / 2) });
      }
    }
  }

  function progress() {
    var doc = document.documentElement.scrollHeight || H;
    return Math.min(1, window.scrollY / Math.max(1, doc - H));
  }

  function paint() {
    var off = window.scrollY * PARALLAX;
    var lit = REDUCED ? 1 : BASE + (1 - BASE) * reach;   // share of links on air
    var carrier = off + H * 0.5;
    ctx.clearRect(0, 0, W, H);

    // the grid drifts with the network
    ctx.save();
    ctx.translate(0, -(off % GRID));
    ctx.fillStyle = pattern;
    ctx.fillRect(0, 0, W, H + GRID);
    ctx.restore();

    for (var n = 0; n < nodes.length; n++) nodes[n].on = false;

    // links: settled ones batched per strength; ones drawing in now get a red tip
    var tips = [];
    [1, DIM].forEach(function (k) {
      ctx.strokeStyle = rgba(COL.idle, 0.36 * k);
      ctx.lineWidth = 1;
      ctx.beginPath();
      for (var i = 0; i < links.length; i++) {
        var l = links[i];
        if (l.k !== k) continue;
        var f = Math.min(1, (lit - l.at) / 0.06);       // this link's draw-in
        if (f <= 0) continue;
        var A = nodes[l.a], B = nodes[l.b];
        if (Math.max(A.y, B.y) < off - 20 || Math.min(A.y, B.y) > off + H + 20) continue;
        var ex = A.x + (B.x - A.x) * f, ey = A.y + (B.y - A.y) * f;
        ctx.moveTo(A.x, A.y - off);
        ctx.lineTo(ex, ey - off);
        A.on = true;
        if (f >= 1) B.on = true; else tips.push([ex, ey - off, k]);
      }
      ctx.stroke();
    });

    // stations: on-air ones stronger than the ones not yet reached
    [[true, 1], [true, DIM], [false, 1], [false, DIM]].forEach(function (g) {
      ctx.fillStyle = rgba(COL.idle, (g[0] ? 0.55 : 0.2) * g[1]);
      ctx.beginPath();
      for (var i = 0; i < nodes.length; i++) {
        var s = nodes[i];
        if (s.on !== g[0] || s.k !== g[1]) continue;
        var y = s.y - off;
        if (y < -10 || y > H + 10) continue;
        ctx.moveTo(s.x + s.r, y);
        ctx.arc(s.x, y, s.r, 0, Math.PI * 2);
      }
      ctx.fill();
    });

    if (REDUCED || energy < 0.02) return;

    // the carrier: links and stations near the mid-screen line carry the
    // signal, glowing with the scroll speed
    for (var q = 0; q < links.length; q++) {
      var L = links[q], P = nodes[L.a], Q = nodes[L.b];
      var dl = Math.abs((P.y + Q.y) / 2 - carrier);
      if (dl > BAND || !(P.on && Q.on)) continue;
      ctx.strokeStyle = rgba(COL.live, 0.5 * energy * (1 - dl / BAND) * L.k);
      ctx.beginPath(); ctx.moveTo(P.x, P.y - off); ctx.lineTo(Q.x, Q.y - off); ctx.stroke();
    }
    for (var c = 0; c < nodes.length; c++) {
      var st = nodes[c], d = Math.abs(st.y - carrier);
      if (d > BAND) continue;
      var a = energy * (1 - d / BAND);
      ctx.fillStyle = rgba(COL.live, 0.55 * a * st.k);
      ctx.beginPath(); ctx.arc(st.x, st.y - off, st.r + 0.6, 0, Math.PI * 2); ctx.fill();
      ctx.strokeStyle = rgba(COL.live, 0.22 * a * st.k);
      ctx.beginPath(); ctx.arc(st.x, st.y - off, st.r + 5 + 8 * a, 0, Math.PI * 2); ctx.stroke();
    }
    for (var t = 0; t < tips.length; t++) {
      ctx.fillStyle = rgba(COL.live, 0.6 * energy * tips[t][2]);
      ctx.beginPath(); ctx.arc(tips[t][0], tips[t][1], 1.6, 0, Math.PI * 2); ctx.fill();
    }
  }

  function frame() {
    raf = null;
    var y = window.scrollY, dy = Math.abs(y - lastY);
    lastY = y;
    energy = Math.min(1, energy * 0.88 + dy / 60);
    reach = Math.max(reach, progress());
    paint();
    if (dy > 0 || energy >= 0.02) raf = window.requestAnimationFrame(frame);
    else { energy = 0; paint(); }        // settle on a clean, red-free frame
  }
  function wake() { if (!raf && !document.hidden) raf = window.requestAnimationFrame(frame); }
  function stop() { if (raf) { window.cancelAnimationFrame(raf); raf = null; } energy = 0; }

  syncPalette();
  build();
  lastY = window.scrollY;
  reach = progress();
  paint();

  if (!REDUCED) window.addEventListener("scroll", wake, { passive: true });
  document.addEventListener("visibilitychange", function () { if (document.hidden) stop(); });
  window.addEventListener("themechange", function () { syncPalette(); paint(); });
  var rt;
  function rebuild() { window.clearTimeout(rt); rt = window.setTimeout(function () { stop(); build(); paint(); }, 160); }
  window.addEventListener("resize", rebuild);
  window.addEventListener("load", rebuild);   // late content changes the page height
})();
