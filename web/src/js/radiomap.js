/* =====================================================================
   RogerAI - the LED station field inside the homepage's ink zones.

   Each .tone-zone (home.css: the page turns from paper to ink there) gets a
   sticky canvas behind its content: a dot-matrix of station LEDs, the same
   grille as the Ping band. As you scroll through a zone its stations come
   on air, red, in clusters like a map at night: a few at the zone's door,
   most by its end. No lines, no timers.

   - frames only while you scroll, and only for zones on screen; asleep the
     frame after the scroll stops; a hidden tab cancels
   - behind the text column the LEDs burn at a third of their strength
   - a zone marked data-onload (the hero, the first screen) tunes in on
     load: its stations come on air over ~1.4s, then it sleeps like the rest
   - reduced motion: every field painted once, still, half on air
   - no JS: the zones are still ink with their bloom (CSS); just no LEDs
   - the unlit grille is the canvas's CSS background; only lit stations
     are drawn (a canvas-pattern fill stalled software renderers)
   ===================================================================== */
(function () {
  "use strict";

  var zones = document.querySelectorAll(".tone-zone");
  if (!zones.length) return;
  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var DPR = Math.min(window.devicePixelRatio || 1, 2);
  var DIM = 0.3;          // strength behind the text column
  var W = 0, H = 0, PITCH = 32, colL = 0, colR = 0;
  var fields = [], raf = null, lastY = -1;
  var COL = { dot: [243, 241, 234], live: [255, 68, 56] };

  function rgba(c, a) { return "rgba(" + c[0] + "," + c[1] + "," + c[2] + "," + Math.round(a * 1000) / 1000 + ")"; }
  function toRGB(str) {
    str = (str || "").trim();
    if (str[0] !== "#" || str.length < 7) return null;
    return [parseInt(str.slice(1, 3), 16), parseInt(str.slice(3, 5), 16), parseInt(str.slice(5, 7), 16)];
  }

  // smooth value noise: clusters of stations, like towns on a map at night
  function hash(x, y) {
    var h = Math.imul(x | 0, 374761393) + Math.imul(y | 0, 668265263);
    h = Math.imul(h ^ (h >>> 13), 1274126177);
    return ((h ^ (h >>> 16)) >>> 0) / 4294967296;
  }
  function noise(x, y) {
    var xi = Math.floor(x), yi = Math.floor(y), xf = x - xi, yf = y - yi;
    var u = xf * xf * (3 - 2 * xf), v = yf * yf * (3 - 2 * yf);
    var a = hash(xi, yi), b = hash(xi + 1, yi), c = hash(xi, yi + 1), d = hash(xi + 1, yi + 1);
    return a + (b - a) * u + (c - a) * v + (a - b - c + d) * u * v;
  }
  // a station's "call time": the lower, the earlier it comes on air
  function callAt(i, j) { return 0.75 * (1 - noise(i * 0.22, j * 0.22)) + 0.25 * hash(i, j); }

  function measure() {
    W = window.innerWidth; H = window.innerHeight;
    PITCH = W < 600 ? 26 : 32;
    colL = W; colR = 0;
    var cols = document.querySelectorAll("main .wrap, .hero__inner");
    for (var i = 0; i < cols.length; i++) {
      var r = cols[i].getBoundingClientRect(), cs = window.getComputedStyle(cols[i]);
      colL = Math.min(colL, r.left + parseFloat(cs.paddingLeft || 0));
      colR = Math.max(colR, r.right - parseFloat(cs.paddingRight || 0));
    }
    if (colR <= colL) { colL = 0; colR = W; }
  }

  function setup(f) {
    var cs = window.getComputedStyle(f.zone);   // the zone's own (ink) tokens
    COL.dot = toRGB(cs.getPropertyValue("--ink-900")) || COL.dot;
    COL.live = toRGB(cs.getPropertyValue("--live")) || COL.live;
    f.canvas.width = Math.round(W * DPR); f.canvas.height = Math.round(H * DPR);
    f.ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    // the unlit grille is a CSS background on the canvas (static, free);
    // the canvas itself only ever draws the stations on air
    f.canvas.style.backgroundImage = "radial-gradient(circle, " + rgba(COL.dot, 0.09) + " 1.1px, transparent 1.7px)";
    f.canvas.style.backgroundSize = PITCH + "px " + PITCH + "px";
  }

  function draw(f, force) {
    var r = f.zone.getBoundingClientRect();
    if (!force && (r.bottom < 0 || r.top > H)) return;   // off screen: free
    var p = REDUCED ? 0.5 : Math.max(0, Math.min(1, (H - r.top) / (r.height + H)));
    if (f.onload) p = Math.max(p, 0.55);                  // the cover is on air from the start
    var onAir = (0.02 + 0.2 * p) * f.tune;                // share of stations on air
    // the field drifts up at a quarter of the scroll, a slow parallax
    var drift = (window.scrollY || 0) * 0.25;
    var row0 = Math.floor(drift / PITCH), dy = -(drift % PITCH);
    var ctx = f.ctx;
    ctx.clearRect(0, 0, W, H);
    f.canvas.style.backgroundPosition = "0 " + dy.toFixed(1) + "px";
    [1, DIM].forEach(function (k) {
      ctx.fillStyle = rgba(COL.live, 0.9 * k);
      ctx.beginPath();
      for (var j = 0; j * PITCH < H + PITCH; j++) {
        for (var i = 0; i * PITCH < W; i++) {
          var x = i * PITCH + PITCH / 2;
          if ((x > colL && x < colR ? DIM : 1) !== k) continue;
          if (callAt(i, j + row0) > onAir) continue;
          var y = j * PITCH + PITCH / 2 + dy;
          ctx.moveTo(x + 2.4, y);
          ctx.arc(x, y, 2.4, 0, Math.PI * 2);
        }
      }
      ctx.fill();
    });
  }

  function drawAll(force) { for (var i = 0; i < fields.length; i++) draw(fields[i], force); }

  function frame() {
    raf = null;
    var y = window.scrollY;
    if (y === lastY) return;                 // the scroll has stopped: sleep
    lastY = y;
    drawAll(false);
    raf = window.requestAnimationFrame(frame);
  }
  function wake() { if (!raf && !document.hidden) raf = window.requestAnimationFrame(frame); }

  function build() {
    measure();
    for (var i = 0; i < fields.length; i++) setup(fields[i]);
    lastY = window.scrollY;
    drawAll(REDUCED);
  }

  for (var z = 0; z < zones.length; z++) {
    var c = document.createElement("canvas");
    c.className = "tone-field";
    c.setAttribute("aria-hidden", "true");
    zones[z].insertBefore(c, zones[z].firstChild);
    var onload = !!(zones[z].hasAttribute && zones[z].hasAttribute("data-onload"));
    fields.push({ zone: zones[z], canvas: c, ctx: c.getContext("2d", { alpha: true }),
      onload: onload, tune: onload && !REDUCED ? 0 : 1 });
  }
  build();

  // the cover tunes in: stations come on air over TUNE_MS, eased, then sleep
  var TUNE_MS = 1400, tuneRaf = null, t0 = window.performance.now();
  function tuneIn(now) {
    tuneRaf = null;
    var t = Math.min(1, (now - t0) / TUNE_MS), done = true;
    for (var i = 0; i < fields.length; i++) {
      if (!fields[i].onload || fields[i].tune >= 1) continue;
      fields[i].tune = 1 - Math.pow(1 - t, 3);
      draw(fields[i], true);
      if (t < 1) done = false;
    }
    if (!done && !document.hidden) tuneRaf = window.requestAnimationFrame(tuneIn);
  }
  if (!REDUCED && fields.some(function (f) { return f.onload; })) tuneRaf = window.requestAnimationFrame(tuneIn);

  if (!REDUCED) window.addEventListener("scroll", wake, { passive: true });
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) return;
    if (raf) { window.cancelAnimationFrame(raf); raf = null; }
    if (tuneRaf) { window.cancelAnimationFrame(tuneRaf); tuneRaf = null; }
    for (var i = 0; i < fields.length; i++) fields[i].tune = 1;   // back to a tuned-in still
  });
  window.addEventListener("themechange", build);
  var rt;
  window.addEventListener("resize", function () { window.clearTimeout(rt); rt = window.setTimeout(build, 160); });
})();
