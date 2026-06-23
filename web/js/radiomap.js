/* =====================================================================
   RogerAI - blip-map background (Canvas2D).
   A faint dotted grid. Stations sit on it; when one "goes on air" it
   breathes, blinks and emits a slow ripple, and now and then fires a
   quiet token-beam to the volt receiver near the hero. Light, very low
   opacity, slow - premium ambient, never noisy.

   - requestAnimationFrame, capped DPR
   - respects prefers-reduced-motion (renders nothing; CSS shows a
     static gradient fallback instead)
   - throttled to ~40fps and pauses when the tab/section is offscreen
     (visibilitychange + IntersectionObserver)
   - a soft fade-in on first paint so it arrives quietly
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var canvas = document.getElementById("blipmap");
  if (!canvas || REDUCED) return;

  var ctx = canvas.getContext("2d", { alpha: true });
  var DPR = Math.min(window.devicePixelRatio || 1, 2);
  var W = 0, H = 0;
  var raf = null, running = false;
  var nodes = [], ripples = [], beams = [];
  var recv = { x: 0, y: 0 };
  var GRID = 52;          // dot grid spacing (css px) - sparser = calmer
  var last = 0, acc = 0;
  var FRAME = 1000 / 40;  // throttle the loop to ~40fps; plenty for ambient
  var fade = 0;           // global opacity ramp on first start

  // Palette is pulled from the live CSS variables so light/dark themes
  // (and any token tweak) drive the canvas with no duplicated hex here.
  var COL = {
    grid:  [11, 13, 18],
    volt:  [91, 91, 255],
    live:  [0, 199, 129],
    ember: [255, 138, 61],
    idle:  [174, 179, 192],
  };
  function rgba(c, a) { return "rgba(" + c[0] + "," + c[1] + "," + c[2] + "," + a + ")"; }

  // parse "#rgb"/"#rrggbb"/"rgb(...)" into [r,g,b]; null if unparseable
  function toRGB(str) {
    if (!str) return null;
    str = str.trim();
    var m;
    if (str[0] === "#") {
      var h = str.slice(1);
      if (h.length === 3) h = h[0] + h[0] + h[1] + h[1] + h[2] + h[2];
      if (h.length >= 6) {
        return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
      }
      return null;
    }
    m = str.match(/rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/i);
    if (m) return [Math.round(+m[1]), Math.round(+m[2]), Math.round(+m[3])];
    return null;
  }

  function syncPalette() {
    var cs = getComputedStyle(document.documentElement);
    // grid = the page ink (so dots invert sensibly between themes)
    var grid = toRGB(cs.getPropertyValue("--ink-900"));
    var volt = toRGB(cs.getPropertyValue("--volt"));
    var live = toRGB(cs.getPropertyValue("--live"));
    var ember = toRGB(cs.getPropertyValue("--ember"));
    var idle = toRGB(cs.getPropertyValue("--ink-300"));
    if (grid)  COL.grid = grid;
    if (volt)  COL.volt = volt;
    if (live)  COL.live = live;
    if (ember) COL.ember = ember;
    if (idle)  COL.idle = idle;
  }
  syncPalette();

  function resize() {
    W = window.innerWidth;
    H = window.innerHeight;
    canvas.width = Math.round(W * DPR);
    canvas.height = Math.round(H * DPR);
    canvas.style.width = W + "px";
    canvas.style.height = H + "px";
    ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    // receiver ("you") sits centred under the hero copy
    recv.x = W * 0.5;
    recv.y = H * 0.30;
    build();
  }

  function build() {
    nodes = [];
    var area = W * H;
    // sparser than before - fewer, quieter stations read as elegant
    var count = Math.max(8, Math.min(24, Math.round(area / 74000)));
    for (var i = 0; i < count; i++) {
      var x = (0.06 + Math.random() * 0.88) * W;
      var y = (0.08 + Math.random() * 0.84) * H;
      // snap loosely to the grid so blips feel like map cells
      x = Math.round(x / GRID) * GRID;
      y = Math.round(y / GRID) * GRID;
      // keep stations a little clear of the receiver halo
      if (Math.abs(x - recv.x) < GRID && Math.abs(y - recv.y) < GRID) y += GRID * 2;
      var hot = Math.random() > 0.78;
      nodes.push({
        x: x, y: y, r: 1.4 + Math.random() * 1.1,
        online: Math.random() > 0.28,
        hot: hot,
        phase: Math.random() * Math.PI * 2,
        speed: 0.0008 + Math.random() * 0.0007,   // slow breathing
        nextFire: performance.now() + 1400 + Math.random() * 7000,
        blink: 0,
      });
    }
  }

  function colorOf(n) { return n.online ? (n.hot ? COL.ember : COL.live) : COL.idle; }

  function fire(n, now) {
    ripples.push({ x: n.x, y: n.y, t: 0, c: colorOf(n), max: 70 + Math.random() * 46 });
    // ~38% of fires also send a beam to the receiver
    if (Math.random() < 0.38) {
      beams.push({ from: n, t: 0, speed: 0.00055 + Math.random() * 0.0006, c: n.hot ? COL.ember : COL.volt });
    }
    n.blink = now;
  }

  // smootherstep for soft, premium easing of rings/beams
  function smooth(t) { return t * t * t * (t * (t * 6 - 15) + 10); }

  function render(now) {
    ctx.clearRect(0, 0, W, H);
    var dt = FRAME;

    // ease the whole field in on first frames
    if (fade < 1) fade = Math.min(1, fade + 0.012);
    ctx.globalAlpha = fade;

    // ---- faint dotted grid (very quiet) ----
    for (var gx = GRID; gx < W; gx += GRID) {
      for (var gy = GRID; gy < H; gy += GRID) {
        // gentle radial falloff so the grid melts toward the edges
        var d = Math.hypot(gx - recv.x, gy - recv.y) / (Math.max(W, H) * 0.7);
        var ga = 0.045 * (1 - Math.min(d, 1) * 0.55);
        ctx.fillStyle = rgba(COL.grid, ga);
        ctx.beginPath();
        ctx.arc(gx, gy, 0.85, 0, Math.PI * 2);
        ctx.fill();
      }
    }

    // ---- ripples (on-air rings) ----
    ripples = ripples.filter(function (r) { return r.t < 1; });
    ripples.forEach(function (r) {
      r.t += dt * 0.00042;                // slower expansion
      var e = smooth(Math.min(r.t, 1));
      var rad = 4 + e * r.max;
      ctx.beginPath();
      ctx.strokeStyle = rgba(r.c, (1 - r.t) * 0.16);
      ctx.lineWidth = 1;
      ctx.arc(r.x, r.y, rad, 0, Math.PI * 2);
      ctx.stroke();
    });

    // ---- beams to receiver (quiet token comets along a gentle bow) ----
    beams = beams.filter(function (b) { return b.t < 1; });
    beams.forEach(function (b) {
      b.t += dt * b.speed;
      var n = b.from;
      var mx = (n.x + recv.x) / 2 + (recv.y - n.y) * 0.10;
      var my = (n.y + recv.y) / 2 + (n.x - recv.x) * 0.10;
      var tt = smooth(Math.min(b.t, 1)), u = 1 - tt;
      var x = u * u * n.x + 2 * u * tt * mx + tt * tt * recv.x;
      var y = u * u * n.y + 2 * u * tt * my + tt * tt * recv.y;
      // fade in/out at the ends so it never pops
      var ends = Math.sin(Math.min(b.t, 1) * Math.PI);
      var halo = ctx.createRadialGradient(x, y, 0, x, y, 6);
      halo.addColorStop(0, rgba(b.c, 0.38 * ends));
      halo.addColorStop(1, rgba(b.c, 0));
      ctx.fillStyle = halo;
      ctx.beginPath(); ctx.arc(x, y, 6, 0, Math.PI * 2); ctx.fill();
      ctx.fillStyle = rgba(b.c, 0.7 * ends);
      ctx.beginPath(); ctx.arc(x, y, 1.3, 0, Math.PI * 2); ctx.fill();
    });

    // ---- station blips ----
    nodes.forEach(function (n) {
      n.phase += dt * n.speed;
      if (n.online && now > n.nextFire) {
        fire(n, now);
        n.nextFire = now + 4200 + Math.random() * 7000;
      }
      var c = colorOf(n);
      var breathe = 0.5 + 0.5 * Math.sin(n.phase);
      var blinkBoost = n.blink && now - n.blink < 700 ? (1 - (now - n.blink) / 700) : 0;

      if (n.online) {
        ctx.beginPath();
        ctx.fillStyle = rgba(c, 0.035 + breathe * 0.03 + blinkBoost * 0.10);
        ctx.arc(n.x, n.y, n.r + 4 + breathe * 1.5 + blinkBoost * 3, 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.beginPath();
      ctx.fillStyle = rgba(c, n.online ? 0.40 + breathe * 0.10 + blinkBoost * 0.4 : 0.22);
      ctx.arc(n.x, n.y, n.r, 0, Math.PI * 2);
      ctx.fill();
    });

    // ---- the receiver ("you") ----
    var beat = 0.5 + 0.5 * Math.sin(now * 0.0018);
    ctx.beginPath();
    ctx.strokeStyle = rgba(COL.volt, 0.10 + beat * 0.05);
    ctx.lineWidth = 1.1;
    ctx.arc(recv.x, recv.y, 15 + beat * 6, 0, Math.PI * 2);
    ctx.stroke();
    var rh = ctx.createRadialGradient(recv.x, recv.y, 0, recv.x, recv.y, 20);
    rh.addColorStop(0, rgba(COL.volt, 0.14));
    rh.addColorStop(1, rgba(COL.volt, 0));
    ctx.fillStyle = rh;
    ctx.beginPath(); ctx.arc(recv.x, recv.y, 20, 0, Math.PI * 2); ctx.fill();
    ctx.fillStyle = rgba(COL.volt, 0.6);
    ctx.beginPath(); ctx.arc(recv.x, recv.y, 3, 0, Math.PI * 2); ctx.fill();

    ctx.globalAlpha = 1;
  }

  function loop(now) {
    var delta = now - last; last = now;
    acc += delta;
    if (acc >= FRAME) {
      acc = acc % FRAME;          // catch up without drift; cap implicit via throttle
      render(now);
    }
    raf = requestAnimationFrame(loop);
  }

  function start() {
    if (running) return;
    running = true; last = performance.now(); acc = 0;
    raf = requestAnimationFrame(loop);
  }
  function stop() {
    running = false;
    if (raf) { cancelAnimationFrame(raf); raf = null; }
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stop(); else start();
  });

  // re-read theme tokens when the user flips light/dark; repaint once if paused
  window.addEventListener("themechange", function () {
    syncPalette();
    if (!running) render(performance.now());
  });

  // pause when the canvas is fully scrolled out of view
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      if (entries[0].isIntersecting && !document.hidden) start();
      else stop();
    }, { threshold: 0 });
    io.observe(canvas);
  }

  var rt;
  window.addEventListener("resize", function () {
    clearTimeout(rt); rt = setTimeout(resize, 160);
  });

  resize();
  start();
})();
