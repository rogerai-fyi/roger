/* =====================================================================
   RogerAI - Ping's eye follows the pointer, a little (round 11).

   Every Ping mark on the page turns its on-air eye (and its glow) toward
   the pointer by at most 1.6 viewBox units, via CSS custom properties that
   base.css feeds to the `translate` property (separate from the blink and
   pulse animations' `transform`). A click on Ping blinks. One rAF per
   pointer burst. Reduced motion: the eye stays put.
   ===================================================================== */
(function () {
  "use strict";
  if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  var marks = document.querySelectorAll(".tube-ping__mark");
  if (!marks.length) return;
  var MAX = 1.6, px = 0, py = 0, raf = false;

  function look() {
    raf = false;
    for (var i = 0; i < marks.length; i++) {
      var r = marks[i].getBoundingClientRect();
      // the eye sits at (32, 17) of a 64 x 80 viewBox that starts at y = -8
      var ex = r.left + r.width / 2, ey = r.top + r.height * (25 / 80);
      var dx = px - ex, dy = py - ey, d = Math.sqrt(dx * dx + dy * dy) || 1;
      var k = Math.min(1, d / 240) * MAX / d;
      marks[i].style.setProperty("--eye-x", (dx * k).toFixed(2) + "px");
      marks[i].style.setProperty("--eye-y", (dy * k).toFixed(2) + "px");
    }
  }
  window.addEventListener("pointermove", function (e) {
    px = e.clientX; py = e.clientY;
    if (!raf) { raf = true; window.requestAnimationFrame(look); }
  }, { passive: true });
  window.addEventListener("click", function (e) {
    var hit = e.target && e.target.closest && e.target.closest(".tube-ping");
    if (!hit) return;
    var mark = hit.querySelector(".tube-ping__mark");
    if (!mark) return;
    mark.classList.add("is-blink");
    window.setTimeout(function () { mark.classList.remove("is-blink"); }, 180);
  });
})();
