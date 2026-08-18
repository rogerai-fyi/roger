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
  /* MORE ROOM (founder: "larger, especially taller"). The authored viewBox is
     360x116, but a charged Exa wave swings ~82 units either side of the
     centreline - so the art always drew OUTSIDE its own box (overflow is
     visible, which hid the problem) and the layout only ever reserved the
     short box. Widening the box to the height the art actually uses lets the
     figure occupy the room it was already painting into, and the wider cap
     scales every unit up with it.
     The width cap now lives in wave-family.css; only the viewBox is set
     here, because it is the animation that decides how much room the art
     needs. */
  svg.setAttribute("viewBox", "-6 -16 360 188");
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
  /* THE LOOP CLOSES ON ITSELF (founder: "the way it ends should also be the
     way it starts"). Everything below is a function of ONE normalised loop
     position u in [0,1), and every term is periodic in u, so the last frame
     of a pass is the first frame of the next one and the seam is invisible:
       - the sweep runs on a RING of TOTAL tier-units, so as it leaves Exa it
         is already approaching Pico from behind; Pico ramps up through the
         wrap instead of snapping on at full charge, which is what the old
         (t % span) form did.
       - each wave's idle drift completes a WHOLE number of cycles per loop,
         so the resting shapes match across the seam too. */
  var SPAN  = 16.5;            // seconds for one full pass
  var GAP   = 2.4;             // tier-units of quiet before the pass restarts
  var INF   = 7;               // ring position of the Wave Infinite beat
  var TOTAL = 8 + GAP;         // the ring: seven sizes, the loop, then quiet
  /* WHERE THE PASS BEGINS (founder: "start how it ends"). The ring starts in
     the middle of the QUIET gap, not on a tier - so the mark eases in the way
     it eases out, every wave faint and no name up, and Wave Pico rises into
     view a beat later. Starting at phase 0 opened on Pico at full charge,
     which read as a hard cut the moment the mark scrolled into view. */
  var START = (8 + GAP / 2) / TOTAL;   // 0..1, mid-gap

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

  /* THE STATION PLATE (founder: "lets include RogerAI.fm and Wave in the
     animation or around it"). Two pieces of type, both earning their place:
     the callsign rides the top of the plate like a station ident, with an
     on-air dot that lights in the colour of whichever tier is firing; and
     the firing label names the PRODUCT - WAVE PICO, WAVE NANO - so the
     family name is spoken seven times a pass instead of never. Both carry a
     paper halo (paint-order: stroke) so they stay legible over the waves
     without a box around them. */
  var ident = document.createElementNS(NS, "text");
  ident.setAttribute("class", "wave-mark__ident");
  ident.setAttribute("x", CX); ident.setAttribute("y", 34);
  ident.setAttribute("text-anchor", "middle");
  ident.textContent = "ROGERAI.FM";
  /* styled here rather than in wave-family.css so the mark carries its own
     plate wherever it is dropped, and so a stylesheet edit cannot silently
     un-brand it */
  ident.setAttribute("style",
    "font-family: var(--font-mono); font-size: 11px; letter-spacing: .3em;" +
    "fill: var(--ink-400); stroke: var(--paper); stroke-width: 3px;" +
    "paint-order: stroke; stroke-linejoin: round;");
  svg.appendChild(ident);

  var onair = document.createElementNS(NS, "circle");
  onair.setAttribute("class", "wave-mark__onair");
  onair.setAttribute("cy", 30); onair.setAttribute("r", 3.1);
  onair.setAttribute("style", "fill: var(--mark-lead, var(--live));");
  svg.appendChild(onair);

  /* the firing name sits on a small engraved plate rather than floating on
     the lines - a halo alone left a wave crossing the word like a strike */
  var plate = document.createElementNS(NS, "rect");
  plate.setAttribute("class", "wave-mark__plate");
  plate.setAttribute("rx", 3); plate.setAttribute("height", 20);
  plate.setAttribute("style",
    "fill: var(--paper); stroke: var(--mark-lead, var(--live)); stroke-width: 1; opacity: 0;");
  svg.appendChild(plate);

  var tag = document.createElementNS(NS, "text");
  tag.setAttribute("class", "wave-mark__tag");
  tag.setAttribute("x", CX); tag.setAttribute("y", CY + 44);
  tag.setAttribute("text-anchor", "middle");
  tag.setAttribute("style",
    "font-family: var(--font-mono); font-size: 15px; letter-spacing: .22em;" +
    "fill: var(--mark-lead, var(--live)); opacity: 0;");
  svg.appendChild(tag);

  /* the dot sits just left of the callsign - measured, because the mono
     metrics differ per theme font stack */
  function placeOnAir() {
    var w = 0;
    try { w = ident.getComputedTextLength(); } catch (e) { w = 78; }
    if (!w) w = 78;
    onair.setAttribute("cx", (CX - w / 2 - 9).toFixed(1));
  }
  placeOnAir();
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(placeOnAir);

  function pathFor(t, charge, u) {
    /* standing wave with a node at CX for every k, so the crossing is exact.
       The charge swells the amplitude; a slow drift keeps an uncharged wave
       breathing rather than frozen. */
    var L = X1 - X0;
    /* u, not seconds: a whole number of cycles per loop, so it matches at the seam */
    var drift = 0.88 + 0.12 * Math.sin(TWO_PI * u * (1 + (t.k % 2)) + t.k);
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
    var u = (START + ((now - start) / 1000 % SPAN) / SPAN) % 1;   // one loop, from mid-gap
    var phase = u * TOTAL;
    var lead = null, best = 0;

    /* THE WAVE INFINITE BEAT. Infinite is not an eighth size, so it is not
       another harmonic - it lights the WHOLE family at once, in the page's
       strongest neutral (white on the dark theme), which is the growth loop
       drawn over the family rather than a bigger model added to it. */
    var dInf = phase - INF;
    if (dInf >  TOTAL / 2) dInf -= TOTAL;
    if (dInf < -TOTAL / 2) dInf += TOTAL;
    var inf = dInf > -1.6 && dInf < 1.6 ? Math.exp(-(dInf * dInf) * 2.1) : 0;

    for (var i = 0; i < TIERS.length; i++) {
      var t = TIERS[i];
      /* distance ON THE RING - this is what closes the seam */
      var d = phase - i;
      if (d >  TOTAL / 2) d -= TOTAL;
      if (d < -TOTAL / 2) d += TOTAL;
      var own = d > -1.6 && d < 1.6 ? Math.exp(-(d * d) * 2.1) : 0;
      var charge = Math.max(own, inf);
      t.el.setAttribute("d", pathFor(t, charge, u));
      t.el.style.opacity = (0.2 + 0.8 * charge).toFixed(3);
      t.el.style.strokeWidth = (1.7 + 4.6 * charge).toFixed(2);
      t.el.style.stroke = inf > own ? "var(--ink-900)" : "";
      if (own > best) { best = own; lead = t; }
    }
    if (inf > best) { best = inf; lead = { id: null, name: "INFINITE" }; }

    if (lead) {
      svg.style.setProperty("--mark-lead",
        lead.id ? "var(--tier-" + lead.id + ")" : "var(--ink-900)");
      var showing = best > 0.55;
      var op = showing ? ((best - 0.55) / 0.45) : 0;
      if (showing && tag.textContent !== "WAVE " + lead.name) {
        tag.textContent = "WAVE " + lead.name;
        var w = 0;
        try { w = tag.getComputedTextLength(); } catch (e) { w = 96; }
        if (!w) w = 96;
        plate.setAttribute("x", (CX - w / 2 - 9).toFixed(1));
        plate.setAttribute("width", (w + 18).toFixed(1));
        plate.setAttribute("y", (CY + 44 - 14).toFixed(1));
      }
      if (!showing) tag.textContent = "";
      tag.style.opacity = op.toFixed(2);
      plate.style.opacity = op.toFixed(2);
      /* the beacon rings each time a tier peaks */
      var r = 14 + 26 * Math.max(0, best - 0.35) / 0.65;
      ring.setAttribute("r", r.toFixed(1));
      ring.style.opacity = (Math.max(0, best - 0.35) / 0.65 * 0.55).toFixed(3);
      if (node) node.style.transform = "scale(" + (1 + 0.16 * best).toFixed(3) + ")";
      /* the ident's dot is the on-air lamp: it rides the same charge, so the
         callsign visibly breathes with the ladder rather than sitting dead */
      onair.style.opacity = (0.3 + 0.7 * best).toFixed(3);
      onair.setAttribute("r", (2.6 + 1.1 * best).toFixed(2));
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
