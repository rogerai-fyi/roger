// easter-egg.js - "Ping pops up": click the footer orange (.orange3d) 5 times fast and the Ping
// mascot quietly walks up from the bottom edge of the screen, ambles in place for a moment, then
// walks back down. Deliberately small + smooth - a slight nudge, not a performance.
//
// One element (no flashy parallax echoes), its starting pose set synchronously so nothing flashes
// in a corner. A soft brand-red halo lifts it off the page (so it reads on the dark theme too).
// Motion is a gentle rise + a subtle walking bob/waddle + a small side sway. 60fps - transform,
// opacity, filter only. Dependency-free, CSP-safe (external 'self' script; ping.svg is 'self').
// Honours prefers-reduced-motion with a still, soft fade.
//
//   - 5 clicks on .orange3d within 2.5s  ->  launch
//   - or any page with #73 in the URL    ->  launch (a quiet bonus + preview hook)
(function () {
  "use strict";

  // --- testable trigger: N hits within windowMs fires onTrigger (rolling, resets on a gap) ----
  function makeMultiClick(threshold, windowMs, onTrigger) {
    var hits = [];
    return function hit(now) {
      now = typeof now === "number" ? now : Date.now();
      hits.push(now);
      while (hits.length && now - hits[0] > windowMs) hits.shift();
      if (hits.length >= threshold) { hits.length = 0; onTrigger(); return true; }
      return false;
    };
  }

  function clamp(v, a, b) { return v < a ? a : v > b ? b : v; }
  function easeOutCubic(t) { return 1 - Math.pow(1 - t, 3); }   // calm rise (no overshoot)
  function easeInCubic(t) { return t * t * t; }                 // calm descent

  if (typeof document === "undefined") { // node test path: export the pure bits, skip the DOM
    if (typeof module !== "undefined" && module.exports) {
      module.exports = { makeMultiClick: makeMultiClick, easeOutCubic: easeOutCubic, easeInCubic: easeInCubic };
    }
    return;
  }

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var RISE = 1100, HOLD = 2600, FALL = 1100;     // ms: walk up, amble, walk down
  var TOTAL = RISE + HOLD + FALL;
  var SIZE = 84;                                  // px, the mascot tile
  var playing = false;

  function el(cls, parent) { var e = document.createElement("div"); e.className = cls; parent.appendChild(e); return e; }

  function launch(centerX) {
    if (playing) return;
    playing = true;
    var W = window.innerWidth, H = window.innerHeight;
    var x = typeof centerX === "number" ? centerX : W * 0.5;
    x = clamp(x, SIZE, W - SIZE);                 // keep fully on-screen horizontally
    var peekY = H - SIZE * 0.72;                   // resting centre when "stood up" near the edge
    var belowY = H + SIZE;                          // fully below the bottom edge (hidden start)

    var stage = el("ping-stage", document.body);
    stage.setAttribute("aria-hidden", "true");
    var halo = el("ping-stage__glow", stage);
    var dancer = el("ping-dancer", stage);
    var img = document.createElement("img");
    img.src = "ping.svg"; img.alt = ""; dancer.setAttribute("aria-hidden", "true");
    dancer.appendChild(img);

    // place it BELOW the screen synchronously, before the first paint -> no corner flash.
    function pose(cx, cy, rotZ, scale, op) {
      dancer.style.opacity = op;
      dancer.style.transform = "translate(" + cx.toFixed(1) + "px," + cy.toFixed(1) + "px)" +
        " translate(-50%,-50%) rotate(" + rotZ.toFixed(2) + "deg) scale(" + scale.toFixed(3) + ")";
      halo.style.opacity = (op * 0.5).toFixed(3);
      halo.style.transform = "translate(" + cx.toFixed(1) + "px," + (cy - 4).toFixed(1) + "px)";
    }
    pose(x, belowY, 0, 0.96, 0);

    if (REDUCED) { reducedFade(stage, dancer, halo, x, peekY); return; }

    var start = null;
    function frame(ts) {
      if (start === null) start = ts;
      var t = ts - start;
      // vertical base: rise -> hold -> fall
      var baseY, op;
      if (t < RISE) { var r = easeOutCubic(t / RISE); baseY = belowY + (peekY - belowY) * r; op = clamp(t / 260, 0, 1); }
      else if (t < RISE + HOLD) { baseY = peekY; op = 1; }
      else { var f = easeInCubic(clamp((t - RISE - HOLD) / FALL, 0, 1)); baseY = peekY + (belowY - peekY) * f; op = 1 - f; }
      // the "walk": a small bob + waddle, plus a gentle side sway - all subtle.
      var hop = -Math.abs(Math.sin(t / 300)) * 5;     // little hops (feet leaving the ground)
      var waddle = Math.sin(t / 300) * 2.4;           // weight shifting side to side
      var sway = Math.sin(t / 1000) * (W * 0.035);    // slow amble across a small range
      var breathe = 1 + Math.sin(t / 760) * 0.015;
      pose(x + sway, baseY + hop, waddle, breathe, op);

      if (t < TOTAL) requestAnimationFrame(frame);
      else { stage.remove(); playing = false; }
    }
    requestAnimationFrame(frame);
  }

  // reduced motion: no walk - Ping just fades in near the bottom edge, holds, fades out.
  function reducedFade(stage, dancer, halo, x, peekY) {
    var start = null, T = 2600;
    function frame(ts) {
      if (start === null) start = ts;
      var p = clamp((ts - start) / T, 0, 1);
      var op = Math.min(1, p * 5) * (1 - clamp((p - 0.78) / 0.22, 0, 1));
      dancer.style.opacity = op;
      dancer.style.transform = "translate(" + x + "px," + peekY + "px) translate(-50%,-50%)";
      halo.style.opacity = (op * 0.5).toFixed(3);
      halo.style.transform = "translate(" + x + "px," + (peekY - 4) + "px)";
      if (p < 1) requestAnimationFrame(frame); else { stage.remove(); playing = false; }
    }
    requestAnimationFrame(frame);
  }

  function wire() {
    var orange = document.querySelector(".orange3d");
    if (orange) {
      var fire = makeMultiClick(5, 2500, function () {
        var r = orange.getBoundingClientRect();
        launch(r.left + r.width / 2);
      });
      orange.addEventListener("click", function () {
        orange.classList.remove("is-pinged"); void orange.offsetWidth; orange.classList.add("is-pinged");
        fire(Date.now());
      });
    }
    if (/^#73$/.test(location.hash)) setTimeout(function () { launch(); }, 350);
  }
  if (document.readyState !== "loading") wire();
  else document.addEventListener("DOMContentLoaded", wire);

  if (typeof window !== "undefined") window.RogerEgg = { launch: launch, makeMultiClick: makeMultiClick };
})();
