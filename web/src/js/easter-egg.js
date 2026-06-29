// easter-egg.js - "Ping breaks loose": click the footer orange (.orange3d) 5 times fast and
// Ping the mascot pops off and takes a slow, elegant 3D glide across the screen, then bows out.
//
// "Subtle & premium" brief: smooth > flashy. A flowing Catmull-Rom path, real 3D banking
// (container perspective + rotateY), depth-of-field (scale + blur driven by a depth sine), a
// soft drop-shadow that grows as Ping nears, two faint parallax echoes, and the brand red as the
// ONLY colour (a low radial glow). Mono otherwise. 60fps - transforms/opacity/filter only.
//
// Dependency-free, CSP-safe (external 'self' script, no inline; image is ping.svg = 'self').
// Honours prefers-reduced-motion (a gentle centre fade instead of the big glide).
//
//   - 5 clicks on .orange3d within 2.5s  ->  launch
//   - or visit any page with #73 in the URL (a quiet bonus trigger, also used to preview)
(function () {
  "use strict";

  // --- testable trigger: N hits within windowMs fires onTrigger (rolling, resets on a gap) ----
  // Exposed for the node unit test; pure, no DOM.
  function makeMultiClick(threshold, windowMs, onTrigger) {
    var hits = [];
    return function hit(now) {
      now = typeof now === "number" ? now : Date.now();
      hits.push(now);
      // keep only hits inside the trailing window
      while (hits.length && now - hits[0] > windowMs) hits.shift();
      if (hits.length >= threshold) { hits.length = 0; onTrigger(); return true; }
      return false;
    };
  }

  // ---- small math helpers --------------------------------------------------------------------
  function clamp(v, a, b) { return v < a ? a : v > b ? b : v; }
  function lerp(a, b, t) { return a + (b - a) * t; }
  function easeInOutCubic(t) { return t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2; }
  function smoothstep(a, b, t) { t = clamp((t - a) / (b - a), 0, 1); return t * t * (3 - 2 * t); }
  // Catmull-Rom through screen waypoints -> a flowing, organic path (t in [0,1] across pts).
  function spline(pts, t) {
    var n = pts.length - 1, f = t * n, i = clamp(Math.floor(f), 0, n - 1), u = f - i;
    var p0 = pts[Math.max(0, i - 1)], p1 = pts[i], p2 = pts[i + 1], p3 = pts[Math.min(n, i + 2)];
    var u2 = u * u, u3 = u2 * u;
    var c = function (a, b, c2, d) {
      return 0.5 * (2 * b + (-a + c2) * u + (2 * a - 5 * b + 4 * c2 - d) * u2 + (-a + 3 * b - 3 * c2 + d) * u3);
    };
    return { x: c(p0.x, p1.x, p2.x, p3.x), y: c(p0.y, p1.y, p2.y, p3.y) };
  }

  if (typeof document === "undefined") { // node test: export the trigger + math, skip the DOM
    if (typeof module !== "undefined" && module.exports) {
      module.exports = { makeMultiClick: makeMultiClick, spline: spline, easeInOutCubic: easeInOutCubic };
    }
    return;
  }

  var DURATION = 6800;          // ms - slow and graceful
  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var playing = false;

  function makeEl(cls, parent) { var e = document.createElement("div"); e.className = cls; parent.appendChild(e); return e; }
  function mascotImg() { var i = document.createElement("img"); i.src = "ping.svg"; i.alt = ""; i.setAttribute("aria-hidden", "true"); return i; }

  function launch(origin) {
    if (playing) return;
    playing = true;
    var W = window.innerWidth, H = window.innerHeight;
    var ox = origin ? origin.x : W * 0.5, oy = origin ? origin.y : H * 0.92;

    var stage = makeEl("ping-stage", document.body);
    stage.setAttribute("aria-hidden", "true");
    var glow = makeEl("ping-stage__glow", stage);
    // two faint echoes behind the hero for parallax depth, then Ping on top
    var echoes = [makeEl("ping-dancer ping-echo", stage), makeEl("ping-dancer ping-echo", stage)];
    var hero = makeEl("ping-dancer", stage);
    echoes.concat([hero]).forEach(function (d) { d.appendChild(mascotImg()); });

    if (REDUCED) { reducedGlide(stage, hero, glow, ox, oy, W, H); return; }

    // waypoints: rise from the orange, flow up-and-across in a long S, settle mid-screen-ish.
    var pts = [
      { x: ox, y: oy },
      { x: W * 0.80, y: H * 0.64 },
      { x: W * 0.52, y: H * 0.28 },
      { x: W * 0.20, y: H * 0.46 },
      { x: W * 0.46, y: H * 0.40 },
      { x: W * 0.56, y: H * 0.52 },
    ];

    var start = null, lastX = ox, lastY = oy, bankY = 0, bankZ = 0;
    function place(el, x, y, scale, blur, rotY, rotZ, op, shadow) {
      el.style.opacity = op;
      el.style.filter = "blur(" + blur.toFixed(2) + "px)" +
        (shadow ? " drop-shadow(0 " + (10 + scale * 14).toFixed(0) + "px " + (16 + scale * 16).toFixed(0) + "px rgba(21,20,15,.28))" : "");
      el.style.transform =
        "translate(" + x.toFixed(1) + "px," + y.toFixed(1) + "px) translate(-50%,-50%)" +
        " rotateY(" + rotY.toFixed(1) + "deg) rotateZ(" + rotZ.toFixed(1) + "deg) scale(" + scale.toFixed(3) + ")";
    }

    function frame(ts) {
      if (start === null) start = ts;
      var p = clamp((ts - start) / DURATION, 0, 1);
      var te = easeInOutCubic(p);
      var pos = spline(pts, te);
      var z = Math.sin(te * Math.PI) * 1 - 0.18;        // depth: forward mid-path, recede at ends
      var scale = 0.62 + 0.62 * (z + 0.18) / 1.18;       // closer = bigger
      var dof = clamp(Math.abs((z) - 0.62) * 5.2, 0, 5); // depth-of-field blur around a focal plane
      // bank into the turn from velocity, smoothed
      var vy = pos.y - lastY;
      bankY = lerp(bankY, clamp((pos.x - lastX) * 1.5, -24, 24), 0.12);
      bankZ = lerp(bankZ, clamp(vy * -0.45, -9, 9), 0.12);
      lastX = pos.x; lastY = pos.y;
      var op = smoothstep(0, 0.07, te) * (1 - smoothstep(0.85, 1, te));

      place(hero, pos.x, pos.y, scale, dof, bankY, bankZ, op, true);
      // echoes: same path, lagged + smaller + softer = parallax
      [0.045, 0.09].forEach(function (lag, i) {
        var lt = easeInOutCubic(clamp(p - lag, 0, 1));
        var ep = spline(pts, lt);
        place(echoes[i], ep.x, ep.y, scale * (0.9 - i * 0.08), dof + 2 + i * 1.6, bankY, bankZ, op * (0.22 - i * 0.08), false);
      });
      // brand-red glow trails Ping, brightest when nearest
      glow.style.opacity = (op * (0.10 + 0.16 * (z + 0.18) / 1.18)).toFixed(3);
      glow.style.transform = "translate(" + pos.x.toFixed(1) + "px," + pos.y.toFixed(1) + "px) scale(" + (0.7 + scale).toFixed(2) + ")";

      if (p < 1) requestAnimationFrame(frame);
      else { stage.remove(); playing = false; }
    }
    requestAnimationFrame(frame);
  }

  // reduced-motion: no big travel - Ping simply fades up in the centre, holds, and fades out.
  function reducedGlide(stage, hero, glow, ox, oy, W, H) {
    var cx = W * 0.5, cy = H * 0.42, start = null;
    glow.style.transform = "translate(" + cx + "px," + cy + "px) scale(1.4)";
    function frame(ts) {
      if (start === null) start = ts;
      var p = clamp((ts - start) / 2600, 0, 1);
      var op = smoothstep(0, 0.2, p) * (1 - smoothstep(0.75, 1, p));
      hero.style.opacity = op;
      hero.style.transform = "translate(" + cx + "px," + cy + "px) translate(-50%,-50%) scale(" + (0.9 + 0.1 * op) + ")";
      glow.style.opacity = (op * 0.16).toFixed(3);
      if (p < 1) requestAnimationFrame(frame); else { stage.remove(); playing = false; }
    }
    requestAnimationFrame(frame);
  }

  // ---- wire the footer orange --------------------------------------------------------------
  function wire() {
    var orange = document.querySelector(".orange3d");
    if (orange) {
      var fire = makeMultiClick(5, 2500, function () {
        var r = orange.getBoundingClientRect();
        launch({ x: r.left + r.width / 2, y: r.top + r.height / 2 });
      });
      orange.addEventListener("click", function () {
        // subtle per-click feedback so the curious feel the egg building
        orange.classList.remove("is-pinged"); void orange.offsetWidth; orange.classList.add("is-pinged");
        fire(Date.now());
      });
    }
    // quiet bonus trigger + preview hook
    if (/^#73$/.test(location.hash)) setTimeout(function () { launch(null); }, 400);
  }
  if (document.readyState !== "loading") wire();
  else document.addEventListener("DOMContentLoaded", wire);

  if (typeof window !== "undefined") window.RogerEgg = { launch: launch, makeMultiClick: makeMultiClick };
})();
