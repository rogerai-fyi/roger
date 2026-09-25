/* =====================================================================
   RogerAI - anchor hold: keep a #fragment in place while late content
   loads above it.

   Opt-in by markup: a page whose content grows after load ABOVE its
   anchors (the homepage's market rows and reel power-on) marks an element
   [data-anchor-hold]. The browser scrolls to #target once; this re-aligns
   the target whenever the page's size changes, until the reader takes
   over (wheel, key, touch, pointer) or four seconds pass. It never
   scrolls on its own after that, and it never pins anything.
   Markup contract: web/DESIGN-SYSTEM.md.
   ===================================================================== */
(function () {
  "use strict";
  if (!document.querySelector("[data-anchor-hold]")) return;
  var hashTarget = window.location.hash && document.getElementById(window.location.hash.slice(1));
  if (!hashTarget || !window.ResizeObserver) return;
  var hold = new window.ResizeObserver(function () { hashTarget.scrollIntoView({ block: "start" }); });
  var release = function () { hold.disconnect(); };
  hold.observe(document.body);
  ["wheel", "keydown", "touchstart", "pointerdown"].forEach(function (t) {
    window.addEventListener(t, release, { passive: true, once: true });
  });
  window.setTimeout(release, 4000);
})();
