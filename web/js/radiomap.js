/* =====================================================================
   RogerAI - blip-map background (Canvas2D).
   A faint dotted grid. Stations sit on it; when one "goes on air" it
   blinks and emits a slow ripple, and now and then fires a quiet beam
   to the volt receiver near the hero. White/light, low opacity, slow.

   - requestAnimationFrame, capped DPR
   - respects prefers-reduced-motion (renders nothing; CSS shows a
     static gradient fallback instead)
   - pauses when the tab/section is offscreen (visibility + IntersectionObserver)
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
  var GRID = 46;          // dot grid spacing (css px)
  var last = 0;

  var COL = {
    grid:  "rgba(11,13,18,0.05)",
    volt:  [91, 91, 255],
    live:  [0, 199, 129],
    ember: [255, 138, 61],
    idle:  [174, 179, 192],
  };
  function rgba(c, a) { return "rgba(" + c[0] + "," + c[1] + "," + c[2] + "," + a + ")"; }

  function resize() {
    W = window.innerWidth;
    H = window.innerHeight;
    canvas.width = Math.round(W * DPR);
    canvas.height = Math.round(H * DPR);
    canvas.style.width = W + "px";
    canvas.style.height = H + "px";
    ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    // receiver sits up-right, near where the hero install command lives
    recv.x = W * 0.5;
    recv.y = H * 0.30;
    build();
  }

  function build() {
    nodes = [];
    var area = W * H;
    var count = Math.max(10, Math.min(34, Math.round(area / 52000)));
    for (var i = 0; i < count; i++) {
      var x = (0.06 + Math.random() * 0.88) * W;
      var y = (0.08 + Math.random() * 0.84) * H;
      // snap loosely to the grid so blips feel like map cells
      x = Math.round(x / GRID) * GRID;
      y = Math.round(y / GRID) * GRID;
      var hot = Math.random() > 0.7;
      nodes.push({
        x: x, y: y, r: 1.6 + Math.random() * 1.4,
        online: Math.random() > 0.25,
        hot: hot,
        phase: Math.random() * Math.PI * 2,
        nextFire: performance.now() + 600 + Math.random() * 5200,
      });
    }
  }

  function colorOf(n) { return n.online ? (n.hot ? COL.ember : COL.live) : COL.idle; }

  function fire(n, now) {
    ripples.push({ x: n.x, y: n.y, t: 0, c: colorOf(n), max: 60 + Math.random() * 40 });
    // ~45% of fires also send a beam to the receiver
    if (Math.random() < 0.45) {
      beams.push({ from: n, t: 0, speed: 0.0009 + Math.random() * 0.0009, c: n.hot ? COL.ember : COL.volt });
    }
    n.blink = now;
  }

  function draw(now) {
    var dt = Math.min(now - last, 50); last = now;
    ctx.clearRect(0, 0, W, H);

    // ---- faint dotted grid ----
    ctx.fillStyle = COL.grid;
    for (var gx = GRID; gx < W; gx += GRID) {
      for (var gy = GRID; gy < H; gy += GRID) {
        ctx.beginPath();
        ctx.arc(gx, gy, 0.9, 0, Math.PI * 2);
        ctx.fill();
      }
    }

    // ---- ripples (on-air rings) ----
    ripples = ripples.filter(function (r) { return r.t < 1; });
    ripples.forEach(function (r) {
      r.t += dt * 0.00055;
      var rad = 4 + r.t * r.max;
      ctx.beginPath();
      ctx.strokeStyle = rgba(r.c, (1 - r.t) * 0.22);
      ctx.lineWidth = 1.1;
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
      var tt = Math.min(b.t, 1), u = 1 - tt;
      var x = u * u * n.x + 2 * u * tt * mx + tt * tt * recv.x;
      var y = u * u * n.y + 2 * u * tt * my + tt * tt * recv.y;
      var halo = ctx.createRadialGradient(x, y, 0, x, y, 7);
      halo.addColorStop(0, rgba(b.c, 0.5));
      halo.addColorStop(1, rgba(b.c, 0));
      ctx.fillStyle = halo;
      ctx.beginPath(); ctx.arc(x, y, 7, 0, Math.PI * 2); ctx.fill();
      ctx.fillStyle = rgba(b.c, 0.85);
      ctx.beginPath(); ctx.arc(x, y, 1.5, 0, Math.PI * 2); ctx.fill();
    });

    // ---- station blips ----
    nodes.forEach(function (n) {
      n.phase += dt * 0.0014;
      if (n.online && now > n.nextFire) {
        fire(n, now);
        n.nextFire = now + 2600 + Math.random() * 6000;
      }
      var c = colorOf(n);
      var breathe = 0.5 + 0.5 * Math.sin(n.phase);
      var blinkBoost = n.blink && now - n.blink < 600 ? (1 - (now - n.blink) / 600) : 0;

      if (n.online) {
        ctx.beginPath();
        ctx.fillStyle = rgba(c, 0.05 + breathe * 0.04 + blinkBoost * 0.12);
        ctx.arc(n.x, n.y, n.r + 5 + blinkBoost * 3, 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.beginPath();
      ctx.fillStyle = rgba(c, n.online ? 0.55 + blinkBoost * 0.4 : 0.30);
      ctx.arc(n.x, n.y, n.r, 0, Math.PI * 2);
      ctx.fill();
    });

    // ---- the receiver ("you") ----
    var beat = 0.5 + 0.5 * Math.sin(now * 0.0022);
    ctx.beginPath();
    ctx.strokeStyle = rgba(COL.volt, 0.12 + beat * 0.06);
    ctx.lineWidth = 1.2;
    ctx.arc(recv.x, recv.y, 16 + beat * 6, 0, Math.PI * 2);
    ctx.stroke();
    var rh = ctx.createRadialGradient(recv.x, recv.y, 0, recv.x, recv.y, 22);
    rh.addColorStop(0, rgba(COL.volt, 0.18));
    rh.addColorStop(1, rgba(COL.volt, 0));
    ctx.fillStyle = rh;
    ctx.beginPath(); ctx.arc(recv.x, recv.y, 22, 0, Math.PI * 2); ctx.fill();
    ctx.fillStyle = rgba(COL.volt, 0.7);
    ctx.beginPath(); ctx.arc(recv.x, recv.y, 3.2, 0, Math.PI * 2); ctx.fill();

    raf = requestAnimationFrame(draw);
  }

  function start() {
    if (running) return;
    running = true; last = performance.now();
    raf = requestAnimationFrame(draw);
  }
  function stop() {
    running = false;
    if (raf) { cancelAnimationFrame(raf); raf = null; }
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stop(); else start();
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
