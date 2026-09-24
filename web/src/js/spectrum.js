/* =====================================================================
   RogerAI - homepage: the Wave Spectrum scrubber (round 10).

   Enhances the static tier ladder (<ol class="home-spectrum">, complete
   without JS) with a range control above it: drag it, or arrow through it,
   and the tier under the needle is tuned in (.is-tuned: its wave turns the
   live red). Pointing at or focusing a tier moves the scrubber to it. It
   starts on Pico, the one trained tier. Every tier stays fully readable;
   tuning only marks one, it never fades the others.
   ===================================================================== */
(function () {
  "use strict";
  var ol = document.querySelector(".home-spectrum");
  if (!ol) return;
  var tiers = ol.querySelectorAll("li");
  if (!tiers.length) return;

  var range = document.createElement("input");
  range.type = "range";
  range.min = "0";
  range.max = String(tiers.length - 1);
  range.step = "1";
  range.value = "0";
  range.className = "spectrum-scrub";
  range.setAttribute("aria-label", ol.getAttribute("aria-label") || "");

  function tune(k) {
    range.value = String(k);
    for (var i = 0; i < tiers.length; i++) tiers[i].classList.toggle("is-tuned", i === k);
    var name = tiers[k].querySelector("b");
    range.setAttribute("aria-valuetext", name ? name.textContent : String(k));
  }

  range.addEventListener("input", function () { tune(Number(range.value)); });
  Array.prototype.forEach.call(tiers, function (li, k) {
    li.addEventListener("pointerenter", function () { tune(k); });
    li.addEventListener("focusin", function () { tune(k); });
  });
  ol.parentNode.insertBefore(range, ol);
  tune(0);
})();
