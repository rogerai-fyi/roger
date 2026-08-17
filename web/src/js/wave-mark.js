/* THE WAVE MARK, ALIVE
   -------------------------------------------------------------------------
   The static mark was two S-curves crossing at a live beacon. This keeps that
   exact geometry as the resting frame and lets it breathe with meaning:

   - THE FAMILY IS A SPECTRUM. Behind the wide grey wave (one wavelength across
     the mark) sit fainter harmonics at 2x, 3x, 4x the frequency - the ladder
     as wavelengths, short waves at the edge, long waves at the flagship. Every
     harmonic of the same fundamental has a NODE at the centre, so all of them
     cross exactly under the beacon, always. The crossing is the point of the
     mark, and the animation cannot move it.
   - STANDING WAVES, BREATHING. Each wave is a standing wave whose amplitude
     breathes on its own slow period (never to zero - the mark never collapses
     flat), with a slow travelling shimmer riding along the live wave so it
     reads as carrying signal rather than merely oscillating.
   - THE BEACON IS ON AIR: a ripple ring leaves it every few seconds (CSS).

   Cheap: five paths, 72 points each, recomputed per frame only while the
   mark is on screen. Reduced-motion users get the resting frame, unmoving,
   which is the original mark to the pixel. */
(function () {
  "use strict";
  var svg = document.querySelector(".wave-mark__svg[data-animate]");
  if (!svg) return;
  var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (reduce) return;

  var X0 = 0, X1 = 348, CX = 174, CY = 78, N = 72;
  var TWO_PI = Math.PI * 2;

  /* amplitudes chosen so mid-breath reproduces the static mark's two curves
     (a cubic with control y=18 peaks ~45 off centre; control 42 peaks ~27) */
  var WAVES = [
    { sel: ".wave-mark__wave--h4",   k: 4, amp: 9,  breath: 7.1, phase: 2.1, floor: 0.45 },
    { sel: ".wave-mark__wave--h3",   k: 3, amp: 14, breath: 6.3, phase: 1.3, floor: 0.45 },
    { sel: ".wave-mark__wave--h2",   k: 2, amp: 22, breath: 5.4, phase: 0.6, floor: 0.5 },
    { sel: ".wave-mark__wave--wide", k: 1, amp: 52, breath: 8.6, phase: 0.0, floor: 0.62 },
    { sel: ".wave-mark__wave--live", k: 1, amp: 32, breath: 4.7, phase: 0.9, floor: 0.72, live: true }
  ].map(function (w) { w.el = svg.querySelector(w.sel); return w; }).filter(function (w) { return w.el; });

  function pathFor(w, t) {
    /* standing wave: y = A(t) * sin(k * 2pi * (x - CX) / (X1 - X0)) - a node at CX
       for every k. The static curves are cosine-shaped crest-left, so the
       fundamental starts a quarter period back: sin(k*2pi*(x-CX)/L) with the
       wide wave's crest at x=87 - matches the original bezier's crest. */
    var L = X1 - X0;
    var breathe = w.floor + (1 - w.floor) * (0.5 + 0.5 * Math.sin(TWO_PI * t / w.breath + w.phase));
    var A = w.amp * breathe;
    var d = "";
    for (var i = 0; i <= N; i++) {
      var x = X0 + (L * i) / N;
      var u = (x - CX) / L;
      /* + not -: the mark crests UPWARD left of the node (the static bezier's
         crest sits at x~87, y~33), and SVG y grows downward */
      var y = CY + A * Math.sin(w.k * TWO_PI * u);
      if (w.live) {
        /* the shimmer: a slow travelling bump of extra amplitude riding the
           live wave, so it carries signal. It is a multiplier that is 1 at
           the node, so the crossing stays exact. */
        var s = 1 + 0.10 * Math.sin(TWO_PI * (u * 1.5 - t / 3.9));
        y = CY + A * s * Math.sin(w.k * TWO_PI * u);
      }
      d += (i === 0 ? "M" : "L") + x.toFixed(2) + " " + y.toFixed(2);
    }
    return d;
  }

  var running = false, raf = 0, t0 = 0;
  function frame(now) {
    if (!running) return;
    var t = (now - t0) / 1000;
    for (var i = 0; i < WAVES.length; i++) WAVES[i].el.setAttribute("d", pathFor(WAVES[i], t));
    raf = window.requestAnimationFrame(frame);
  }
  function start() { if (running) return; running = true; t0 = performance.now() - (t0 ? 0 : 0); raf = window.requestAnimationFrame(frame); }
  function stop() { running = false; if (raf) window.cancelAnimationFrame(raf); raf = 0; }

  /* only spend frames while the mark is on screen and the tab is visible */
  if ("IntersectionObserver" in window) {
    new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { if (e.isIntersecting) start(); else stop(); });
    }, { threshold: 0.05 }).observe(svg);
  } else start();
  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stop(); else if (svg.getBoundingClientRect().bottom > 0) start();
  });
  svg.setAttribute("data-live", "1");
})();
