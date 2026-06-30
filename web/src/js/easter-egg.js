// easter-egg.js - "Ping takes a walk": click any footer orange (.orange3d) 5 times fast and the
// Ping mascot strolls once across the BOTTOM edge of the screen, then off. Deliberately small and
// calm - nothing crazy.
//
// IMPORTANT: every layout-critical style is set INLINE here (not via a CSS class). That makes the
// effect self-contained and cache-proof - a stale/old base.css can never leave these nodes
// unstyled in the page flow (which previously dropped them below the footer and jumped the
// scroll). The element is a fixed, click-through, overflow-clipped overlay, so it can never grow
// the document or move the scroll position.
//
// Dependency-free, CSP-safe (external 'self' script; ping.svg is 'self'; styles are inline, which
// style-src 'unsafe-inline' permits). Honours prefers-reduced-motion with a still fade.
//
//   - 5 clicks on .orange3d within 2.5s  ->  walk
//   - or any page with #73 in the URL    ->  walk (a quiet bonus + preview hook)
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
  function easeInOutCubic(t) { return t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2; }

  if (typeof document === "undefined") { // node test path: export the pure bits, skip the DOM
    if (typeof module !== "undefined" && module.exports) {
      module.exports = { makeMultiClick: makeMultiClick, easeInOutCubic: easeInOutCubic };
    }
    return;
  }

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var SIZE = 58;          // px - small + subtle
  var DURATION = 8600;    // ms - one calm stroll across
  var playing = false;

  function launch() {
    if (playing) return;
    playing = true;
    var W = window.innerWidth;

    // fixed, click-through, overflow-clipped overlay - all inline so no CSS file is required.
    var stage = document.createElement("div");
    stage.setAttribute("aria-hidden", "true");
    stage.style.cssText = "position:fixed;left:0;top:0;right:0;bottom:0;margin:0;padding:0;" +
      "overflow:hidden;pointer-events:none;z-index:2147483000;";
    // a soft red bloom (literal colour, not a CSS var) so Ping reads on the dark theme too.
    var halo = document.createElement("div");
    halo.style.cssText = "position:absolute;left:0;bottom:8px;width:132px;height:132px;" +
      "border-radius:50%;background:radial-gradient(circle,rgba(224,35,28,.20),rgba(224,35,28,0) 66%);" +
      "opacity:0;will-change:transform,opacity;";
    var dancer = document.createElement("div");
    dancer.style.cssText = "position:absolute;left:0;bottom:8px;width:" + SIZE + "px;height:" + SIZE + "px;" +
      "opacity:0;will-change:transform,opacity;";
    var img = document.createElement("img");
    img.src = "ping.svg"; img.alt = "";
    img.style.cssText = "width:100%;height:100%;display:block;border-radius:13px;" +
      "box-shadow:0 8px 18px rgba(0,0,0,.28),0 0 0 1px rgba(150,150,150,.16);";
    dancer.appendChild(img);
    stage.appendChild(halo); stage.appendChild(dancer);
    document.body.appendChild(stage);

    if (REDUCED) { reducedFade(stage, dancer, halo, (W - SIZE) / 2); return; }

    var fromX = -SIZE - 24, toX = W + 24;   // walk in from the left, off to the right
    var haloDx = (SIZE - 132) / 2;          // centre the bloom behind Ping
    var start = null;
    function frame(ts) {
      if (start === null) start = ts;
      var ms = ts - start, t = ms / DURATION;
      if (t >= 1) { stage.remove(); playing = false; return; }
      var x = fromX + (toX - fromX) * easeInOutCubic(t);   // gentle start + stop
      var bob = -Math.abs(Math.sin(ms / 250)) * 3;          // little walking hops
      var waddle = Math.sin(ms / 250) * 2;                  // slight weight shift
      var op = clamp(t / 0.06, 0, 1) * clamp((1 - t) / 0.06, 0, 1);  // fade in/out at the edges
      dancer.style.opacity = op;
      dancer.style.transform = "translate(" + x.toFixed(1) + "px," + bob.toFixed(1) + "px) rotate(" + waddle.toFixed(2) + "deg)";
      halo.style.opacity = (op * 0.5).toFixed(3);
      halo.style.transform = "translate(" + (x + haloDx).toFixed(1) + "px," + (bob + 6).toFixed(1) + "px)";
      requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
  }

  // reduced motion: no walk - Ping just fades in at the bottom centre, holds, fades out.
  function reducedFade(stage, dancer, halo, cx) {
    dancer.style.transform = "translateX(" + cx + "px)";
    halo.style.transform = "translateX(" + (cx + (SIZE - 132) / 2) + "px)";
    var start = null, T = 2600;
    function frame(ts) {
      if (start === null) start = ts;
      var p = clamp((ts - start) / T, 0, 1);
      var op = Math.min(1, p * 5) * (1 - clamp((p - 0.78) / 0.22, 0, 1));
      dancer.style.opacity = op; halo.style.opacity = (op * 0.5).toFixed(3);
      if (p < 1) requestAnimationFrame(frame); else { stage.remove(); playing = false; }
    }
    requestAnimationFrame(frame);
  }

  function wire() {
    var oranges = document.querySelectorAll(".orange3d");
    if (oranges.length) {
      var fire = makeMultiClick(5, 2500, launch);   // one shared rolling counter across every orange
      oranges.forEach(function (orange) {
        orange.addEventListener("click", function () {
          // tiny per-click "feel it building" pulse via WAAPI (no CSS dependency).
          if (orange.animate) orange.animate(
            [{ transform: "scale(1)" }, { transform: "scale(1.18)" }, { transform: "scale(1)" }],
            { duration: 360, easing: "ease", composite: "add" }); // add (not replace) so a rotated orange (the rail) keeps its rotation during the pulse
          fire(Date.now());
        });
      });
    }
    if (/^#73$/.test(location.hash)) setTimeout(launch, 350);
  }
  if (document.readyState !== "loading") wire();
  else document.addEventListener("DOMContentLoaded", wire);

  if (typeof window !== "undefined") window.RogerEgg = { launch: launch, makeMultiClick: makeMultiClick };
})();
