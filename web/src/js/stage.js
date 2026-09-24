/* =====================================================================
   RogerAI - ?stage=pinned: a darkbloom-style pinned instrument stage.

   A lab variant beside the round-8 dial (the default). With ?stage=pinned
   on a wide screen with motion allowed, <html data-stage="pinned">:
   - home.css pins .stage (a big, calm tuning scale with a centred red
     needle) under the nav for the whole §1-§9 run, and lifts each section
     into its own full-width chapter panel with a gap before it. The stage
     shows in the gaps; dense content (the market table, the spec plate,
     the spectrum, the company cards) rides its own panel OVER the stage,
     at full width, never squeezed onto it.
   - this script maps scroll to steps, darkbloom's way: page positions are
     measured on resize (ResizeObserver), one requestAnimationFrame per
     scroll burst reads scrollY only, and progress through each gap runs a
     window: HOLD 0-25%, eased MOVE 25-75%, HOLD 75-100%. The scale pans
     (--station) so the next station arrives under the needle; its numeral
     lights and its title (the section's own label) settles in on a 1.15s
     transition.
   Anything else - no parameter, no JS, reduced motion, under 800px - is
   the normal stacked page with the stage hidden.
   ===================================================================== */
(function () {
  "use strict";

  var params = new URLSearchParams(window.location.search);
  if (params.get("stage") !== "pinned") return;
  if (!window.matchMedia("(min-width: 800px)").matches) return;
  if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  var stage = document.querySelector(".stage");
  if (!stage) return;
  document.documentElement.setAttribute("data-stage", "pinned");

  var links = document.querySelectorAll('.dial a[href^="#"]');
  var targets = [], names = [];
  for (var i = 0; i < links.length; i++) {
    var el = document.getElementById(links[i].getAttribute("href").slice(1));
    targets.push(el);
    var no = el && el.querySelector(".sectionno");
    names.push(no ? no.textContent.trim() : "");
  }
  var numerals = stage.querySelectorAll(".stage__scale b");
  var title = stage.querySelector(".stage__title");
  var tops = [], bottoms = [], raf = null, on = -2;

  function measure() {
    var y = window.scrollY;
    for (var k = 0; k < targets.length; k++) {
      var r = targets[k] ? targets[k].getBoundingClientRect() : { top: 0, bottom: 0 };
      tops[k] = r.top + y; bottoms[k] = r.bottom + y;
    }
  }
  function ease(t) { return t * t * (3 - 2 * t); }

  function frame() {
    raf = null;
    var line = window.scrollY + window.innerHeight / 2;
    var v = -1;
    for (var k = 0; k < targets.length; k++) {
      var gapStart = k ? bottoms[k - 1] : tops[0] - window.innerHeight;
      if (line >= tops[k]) { v = k; continue; }               // inside (or past) chapter k
      if (line > gapStart) {                                  // in the gap before k
        var g = (line - gapStart) / Math.max(1, tops[k] - gapStart);
        v = k - 1 + ease(Math.max(0, Math.min(1, (g - 0.25) / 0.5)));
      }
      break;
    }
    stage.style.setProperty("--station", String(Math.round(v * 1000) / 1000));
    var now = Math.round(v);
    if (now !== on) {
      on = now;
      for (var n = 0; n < numerals.length; n++) numerals[n].classList.toggle("is-on", n === now);
      if (title) {
        title.textContent = now >= 0 ? names[now] : "";
        title.classList.remove("is-in");
        void title.offsetWidth;               // restart the settle transition
        title.classList.add("is-in");
      }
    }
  }
  function wake() { if (!raf) raf = window.requestAnimationFrame(frame); }

  measure();
  frame();
  window.addEventListener("scroll", wake, { passive: true });
  window.addEventListener("resize", function () { measure(); wake(); });
  if (window.ResizeObserver) new window.ResizeObserver(function () { measure(); wake(); }).observe(document.body);
})();
