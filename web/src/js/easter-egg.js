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

  // The iOS app isn't live yet (bundle fyi.rogerai.app; Sign-in-with-Apple still a submission
  // blocker), so there is no real numeric App Store id. PLACEHOLDER - swap in the real id at launch;
  // the bubble says "coming soon" so this is never presented as a live listing.
  var APP_STORE_URL = "https://apps.apple.com/app/rogerai/id000000000"; // TODO: real App Store ID once the app is live
  // pure: the argument tuple for a safe new-tab open - opener severed so the store tab can't touch us.
  function openArgs(url) { return [url, "_blank", "noopener,noreferrer"]; }

  if (typeof document === "undefined") { // node test path: export the pure bits, skip the DOM
    if (typeof module !== "undefined" && module.exports) {
      module.exports = { makeMultiClick: makeMultiClick, easeInOutCubic: easeInOutCubic,
        openArgs: openArgs, APP_STORE_URL: APP_STORE_URL };
    }
    return;
  }

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var SIZE = 58;           // px - small + subtle
  var WALK_IN = 3400;      // ms - stroll in from the left to centre stage
  var HOLD = 3400;         // ms - pause at centre with the App Store bubble up (clickable)
  var WALK_OUT = 3400;     // ms - amble off to the right
  var REDUCED_MS = 4600;   // ms - reduced motion: fade in, hold (clickable), fade out - no travel
  var playing = false;

  // the App Store speech bubble: calm + on-brand (paper, hairline, one red arrow), sits above Ping.
  // Built from nodes with inline styles (CSP-safe, no innerHTML); token vars carry literal fallbacks
  // so a missing stylesheet can't leave it unstyled. Starts hidden + click-through until Ping arrives.
  function makeBubble() {
    var b = document.createElement("div");
    b.style.cssText =
      "position:absolute;left:50%;bottom:calc(100% + 12px);transform:translateX(-50%);box-sizing:border-box;" +
      "max-width:min(240px,76vw);padding:8px 11px;text-align:center;white-space:normal;" +
      "font-family:var(--font-mono,ui-monospace,monospace);font-size:12px;line-height:1.35;letter-spacing:.01em;" +
      "color:var(--ink-900,#15140f);background:var(--paper,#fbfbfa);border:1px solid var(--hairline-2,#d8d7d2);" +
      "border-radius:10px;box-shadow:0 12px 28px rgba(0,0,0,.22),0 0 0 1px rgba(224,35,28,.10);" +
      "opacity:0;pointer-events:none;cursor:pointer;will-change:transform,opacity;";
    var line = document.createElement("span");
    line.textContent = "Check out RogerAI on the App Store ";
    var arrow = document.createElement("b");
    arrow.textContent = "→";
    arrow.style.cssText = "color:#e0231c;font-weight:700;";
    line.appendChild(arrow);
    var soon = document.createElement("div"); // honest: the listing isn't live yet (placeholder id)
    soon.textContent = "coming soon";
    soon.style.cssText = "margin-top:2px;font-size:10px;letter-spacing:.06em;text-transform:uppercase;color:var(--ink-400,#9a968b);";
    var tail = document.createElement("div"); // little downward beak toward Ping
    tail.style.cssText = "position:absolute;left:50%;bottom:-5px;width:10px;height:10px;" +
      "transform:translateX(-50%) rotate(45deg);background:var(--paper,#fbfbfa);" +
      "border-right:1px solid var(--hairline-2,#d8d7d2);border-bottom:1px solid var(--hairline-2,#d8d7d2);";
    b.appendChild(line); b.appendChild(soon); b.appendChild(tail);
    return b;
  }

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
    // Ping is the ONE clickable island on the click-through stage (pointer-events:auto on the dancer).
    var dancer = document.createElement("div");
    dancer.style.cssText = "position:absolute;left:0;bottom:8px;width:" + SIZE + "px;height:" + SIZE + "px;" +
      "opacity:0;cursor:pointer;pointer-events:auto;will-change:transform,opacity;";
    var img = document.createElement("img");
    img.src = "ping.svg"; img.alt = "";
    img.style.cssText = "width:100%;height:100%;display:block;border-radius:13px;pointer-events:none;" +
      "box-shadow:0 8px 18px rgba(0,0,0,.28),0 0 0 1px rgba(150,150,150,.16);";
    dancer.appendChild(img);
    var bubble = makeBubble();
    dancer.appendChild(bubble); // rides with Ping; steady while he's paused at centre
    stage.appendChild(halo); stage.appendChild(dancer);
    document.body.appendChild(stage);

    var haloDx = (SIZE - 132) / 2;          // centre the bloom behind Ping
    var centerX = (W - SIZE) / 2;
    var fromX = -SIZE - 24, toX = W + 24;   // walk in from the left, off to the right
    var rafId = 0, done = false, dismissing = false, dismissAt = null;
    var lastX = centerX, lastBob = 0, curDOp = 0, curBOp = 0, dOp0 = 0, bOp0 = 0;

    function place(x, bob, waddle, dop) {
      lastX = x; lastBob = bob; curDOp = dop;
      dancer.style.opacity = dop;
      dancer.style.transform = "translate(" + x.toFixed(1) + "px," + bob.toFixed(1) + "px) rotate(" + waddle.toFixed(2) + "deg)";
      halo.style.opacity = (dop * 0.5).toFixed(3);
      halo.style.transform = "translate(" + (x + haloDx).toFixed(1) + "px," + (bob + 6).toFixed(1) + "px)";
    }
    function bubbleOp(op) {
      curBOp = op;
      bubble.style.opacity = op.toFixed(3);
      bubble.style.pointerEvents = op > 0.05 ? "auto" : "none"; // only hot while actually visible
    }
    function finish() {
      if (done) return;
      done = true; playing = false;
      if (rafId) cancelAnimationFrame(rafId);
      stage.remove();
    }
    function dismissFrame(ts) {                 // graceful quick fade from wherever we were, then gone
      if (dismissAt === null) dismissAt = ts;
      var k = 1 - clamp((ts - dismissAt) / 460, 0, 1);
      place(lastX, lastBob * k, 0, dOp0 * k);
      bubble.style.opacity = (bOp0 * k).toFixed(3);
      if (k <= 0) { finish(); return; }
      rafId = requestAnimationFrame(dismissFrame);
    }
    // click Ping (any time) or his bubble -> open the store in a severed new tab, then bow out. One-shot.
    dancer.addEventListener("click", function (ev) {
      ev.preventDefault(); ev.stopPropagation();
      if (done || dismissing) return;
      window.open.apply(window, openArgs(APP_STORE_URL));
      dismissing = true; dismissAt = null; dOp0 = curDOp; bOp0 = curBOp;
      bubble.style.pointerEvents = "none";
      if (rafId) cancelAnimationFrame(rafId);
      rafId = requestAnimationFrame(dismissFrame);
    });

    if (REDUCED) { // reduced motion: fade in at bottom-centre, hold (clickable), fade out - no travel.
      var rStart = null;
      rafId = requestAnimationFrame(function reducedFrame(ts) {
        if (rStart === null) rStart = ts;
        var p = clamp((ts - rStart) / REDUCED_MS, 0, 1);
        var out = 1 - clamp((p - 0.80) / 0.20, 0, 1);
        place(centerX, 0, 0, Math.min(1, p * 5) * out);
        bubbleOp(clamp((ts - rStart) / 500, 0, 1) * out); // reachable through the calm hold
        if (p >= 1) { finish(); return; }
        rafId = requestAnimationFrame(reducedFrame);
      });
      return;
    }

    var wStart = null;
    rafId = requestAnimationFrame(function walkFrame(ts) {
      if (wStart === null) wStart = ts;
      var e = ts - wStart;
      if (e < WALK_IN) {                                   // 1) stroll in to centre
        var p = e / WALK_IN;
        place(fromX + (centerX - fromX) * easeInOutCubic(p),
          -Math.abs(Math.sin(e / 250)) * 3, Math.sin(e / 250) * 2, clamp(p / 0.08, 0, 1));
        bubbleOp(0);
      } else if (e < WALK_IN + HOLD) {                     // 2) pause, settle the hops, raise the bubble
        var he = e - WALK_IN, hop = 1 - clamp(he / 260, 0, 1);
        place(centerX, -Math.abs(Math.sin(e / 250)) * 3 * hop, Math.sin(e / 250) * 2 * hop, 1);
        bubbleOp(clamp(he / 420, 0, 1) * (1 - clamp((he - (HOLD - 520)) / 520, 0, 1)));
      } else {                                             // 3) amble off to the right
        var oe = e - WALK_IN - HOLD, p2 = oe / WALK_OUT;
        if (p2 >= 1) { finish(); return; }
        place(centerX + (toX - centerX) * easeInOutCubic(p2),
          -Math.abs(Math.sin(oe / 250)) * 3, Math.sin(oe / 250) * 2, clamp((1 - p2) / 0.12, 0, 1));
        bubbleOp(0);
      }
      rafId = requestAnimationFrame(walkFrame);
    });
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
