/* =====================================================================
   RogerAI - homepage: hold a #fragment while late content loads above it.

   The market rows and the Roger Edge reel's power-on grow the page ABOVE a
   target like #monetize after the browser has already scrolled to it, and
   the target lands far down the screen. Until the reader takes over (or a
   few seconds pass), re-align the target whenever the page's size changes.
   (Rounds 3-9 also carried motion variants, a snap, a sticky dial and a
   pinned stage here; round 10 retired them - nothing on the page is pinned
   but the site nav.)
   ===================================================================== */
(function () {
  "use strict";
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
