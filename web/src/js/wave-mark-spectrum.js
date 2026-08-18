/* THE WAVE SPECTRUM MARK - the ladder, played
   =========================================================================
   The classic mark (js/wave-mark.js, still there behind ?mark=classic) drew
   two curves and three anonymous harmonics. This one draws the FAMILY: seven
   standing waves, one per tier, each in its own colour from the founder's
   Spectrum chart, and it plays them pico -> exa the way the headline reads.

   The physics is the argument, not decoration:
   - SHORT WAVE, SMALL AMPLITUDE = Wave Pico: fine detail, reaches the edge.
     LONG WAVE, BIG AMPLITUDE = Wave Exa: carries the most power. That is the
     page's own sentence - "smaller waves reach the edge; larger waves carry
     more power" - drawn instead of asserted.
   - Every tier is a harmonic of ONE fundamental, so every one has a node at
     the centre. All seven cross exactly under the beacon, always, at any
     amplitude. The family shares a crossing point by construction; the
     animation cannot move it.
   - A charge sweeps up the ladder. As it reaches a tier that wave brightens,
     swells and thickens, the beacon takes its colour and rings, and the tier
     is named. Then it settles and the next one lights. One pass of the sweep
     IS the sentence "one family, pico to exa".

   Cost: seven paths, 84 points, recomputed only while the mark is on screen
   and the tab is visible. Under prefers-reduced-motion nothing runs at all
   and the static markup - the classic resting frame - is what you see.  */
(function () {
  "use strict";
  if (/[?&]mark=classic\b/.test(window.location.search)) return;
  var svg = document.querySelector(".wave-mark__svg[data-animate]");
  if (!svg) return;
  if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

  var X0 = 0, X1 = 348, CX = 174, CY = 78, N = 84, TWO_PI = Math.PI * 2;
  var NS = "http://www.w3.org/2000/svg";

  /* the locked ladder, smallest first. k is the harmonic number: Pico is the
     shortest wave on the plate, Exa the longest and tallest. */
  var TIERS = [
    { id: "pico",  name: "PICO",  k: 7, amp: 13 },
    { id: "nano",  name: "NANO",  k: 6, amp: 19 },
    { id: "micro", name: "MICRO", k: 5, amp: 26 },
    { id: "giga",  name: "GIGA",  k: 4, amp: 34 },
    { id: "tera",  name: "TERA",  k: 3, amp: 42 },
    { id: "peta",  name: "PETA",  k: 2, amp: 50 },
    { id: "exa",   name: "EXA",   k: 1, amp: 58 }
  ];
  var CYCLE = 11.5;            // seconds for one full pico -> exa pass
  var HOLD  = 1.9;             // extra beats at the top before it starts over

  /* Build the spectrum layer. The resting markup underneath is left exactly
     as authored - it is the no-JS mark and what its locks assert - so this
     draws OVER it and hides it only once the first frame is ready. */
  var layer = document.createElementNS(NS, "g");
  layer.setAttribute("class", "wave-mark__spectrum");
  layer.setAttribute("fill", "none");
  layer.setAttribute("stroke-linecap", "round");
  TIERS.forEach(function (t) {
    t.el = document.createElementNS(NS, "path");
    t.el.setAttribute("class", "wave-mark__tier");
    t.el.setAttribute("data-tier", t.id);
    layer.appendChild(t.el);
  });
  var node = svg.querySelector(".wave-mark__node");
  svg.insertBefore(layer, node || null);

  var ring = document.createElementNS(NS, "circle");
  ring.setAttribute("class", "wave-mark__ring");
  ring.setAttribute("cx", CX); ring.setAttribute("cy", CY); ring.setAttribute("r", 14);
  svg.insertBefore(ring, node || null);

  var tag = document.createElementNS(NS, "text");
  tag.setAttribute("class", "wave-mark__tag");
  tag.setAttribute("x", CX); tag.setAttribute("y", CY - 30);
  tag.setAttribute("text-anchor", "middle");
  svg.appendChild(tag);

  function pathFor(t, charge, t0) {
    /* standing wave with a node at CX for every k, so the crossing is exact.
       The charge swells the amplitude; a slow drift keeps an uncharged wave
       breathing rather than frozen. */
    var L = X1 - X0;
    var drift = 0.88 + 0.12 * Math.sin(TWO_PI * t0 / (6 + t.k) + t.k);
    var A = t.amp * drift * (1 + 0.42 * charge);
    var d = "";
    for (var i = 0; i <= N; i++) {
      var x = X0 + (L * i) / N;
      var u = (x - CX) / L;
      var y = CY + A * Math.sin(t.k * TWO_PI * u);
      d += (i === 0 ? "M" : "L") + x.toFixed(2) + " " + y.toFixed(2);
    }
    return d;
  }

  var running = false, raf = 0, start = 0, primed = false;

  function frame(now) {
    if (!running) return;
    var t0 = (now - start) / 1000;
    var span = CYCLE + HOLD;
    var phase = (t0 % span) / CYCLE * TIERS.length;   // 0..7 then a hold
    var lead = null, best = 0;

    for (var i = 0; i < TIERS.length; i++) {
      var t = TIERS[i];
      var d = phase - i;
      var charge = d > -1.6 && d < 1.6 ? Math.exp(-(d * d) * 2.1) : 0;
      t.el.setAttribute("d", pathFor(t, charge, t0));
      t.el.style.opacity = (0.2 + 0.8 * charge).toFixed(3);
      t.el.style.strokeWidth = (1.7 + 4.6 * charge).toFixed(2);
      if (charge > best) { best = charge; lead = t; }
    }

    if (lead) {
      svg.style.setProperty("--mark-lead", "var(--tier-" + lead.id + ")");
      tag.textContent = best > 0.55 ? lead.name : "";
      tag.style.opacity = best > 0.55 ? ((best - 0.55) / 0.45).toFixed(2) : 0;
      /* the beacon rings each time a tier peaks */
      var r = 14 + 26 * Math.max(0, best - 0.35) / 0.65;
      ring.setAttribute("r", r.toFixed(1));
      ring.style.opacity = (Math.max(0, best - 0.35) / 0.65 * 0.55).toFixed(3);
      if (node) node.style.transform = "scale(" + (1 + 0.16 * best).toFixed(3) + ")";
    }

    if (!primed) { primed = true; svg.setAttribute("data-spectrum", "1"); }
    raf = window.requestAnimationFrame(frame);
  }

  function go()  { if (running) return; running = true; start = performance.now(); raf = window.requestAnimationFrame(frame); }
  function halt() { running = false; if (raf) window.cancelAnimationFrame(raf); raf = 0; }

  if ("IntersectionObserver" in window) {
    new IntersectionObserver(function (es) {
      es.forEach(function (e) { if (e.isIntersecting) go(); else halt(); });
    }, { threshold: 0.05 }).observe(svg);
  } else go();
  document.addEventListener("visibilitychange", function () {
    if (document.hidden) halt(); else if (svg.getBoundingClientRect().bottom > 0) go();
  });
})();
