/* =====================================================================
   RogerAI - the scrubber component (components.css .scrub).

   Auto-initializes every list marked [data-scrub] (an <ol>/<ul>, complete
   and readable without JS): it inserts a native range control above the
   list; drag it, or arrow through it, and the item under the needle is
   tuned (.is-tuned - the page styles what tuned looks like). Pointing at an
   item moves the scrubber to it. It starts on the first item. Every item
   stays fully readable; tuning only marks one, it never fades the others.
   The control's accessible name is the list's aria-label; each position
   reads out the item's <b> text.
   When a page lays the list out as a sideways gallery (it overflows its own
   width, e.g. the homepage spectrum on phones), the two stay in step:
   scrubbing brings the tuned item to the middle, and swiping the gallery
   tunes the item that comes to rest in the middle.
   Markup contract: web/DESIGN-SYSTEM.md.
   ===================================================================== */
(function () {
  "use strict";
  Array.prototype.forEach.call(document.querySelectorAll("[data-scrub]"), init);

  function init(list) {
    var items = list.querySelectorAll("li");
    if (!items.length) return;

    var range = document.createElement("input");
    range.type = "range";
    range.min = "0";
    range.max = String(items.length - 1);
    range.step = "1";
    range.value = "0";
    range.className = "scrub";
    range.style.setProperty("--scrub-n", String(items.length));
    range.setAttribute("aria-label", list.getAttribute("aria-label") || "");

    function tune(k) {
      range.value = String(k);
      for (var i = 0; i < items.length; i++) items[i].classList.toggle("is-tuned", i === k);
      var name = items[k].querySelector("b");
      range.setAttribute("aria-valuetext", name ? name.textContent : String(k));
    }

    // the sideways gallery: only when the list overflows its own width
    function overflows() {
      return typeof list.scrollTo === "function" && list.scrollWidth > list.clientWidth;
    }
    function center(k) {
      if (!overflows()) return;
      var li = items[k];
      var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      list.scrollTo({ left: li.offsetLeft - (list.clientWidth - li.offsetWidth) / 2, behavior: still ? "auto" : "smooth" });
    }
    function nearest() {
      var mid = list.scrollLeft + list.clientWidth / 2, best = 0, gap = Infinity;
      for (var i = 0; i < items.length; i++) {
        var d = Math.abs(items[i].offsetLeft + items[i].offsetWidth / 2 - mid);
        if (d < gap) { gap = d; best = i; }
      }
      return best;
    }

    range.addEventListener("input", function () { tune(Number(range.value)); center(Number(range.value)); });
    Array.prototype.forEach.call(items, function (li, k) {
      li.addEventListener("pointerenter", function () { tune(k); center(k); });
    });
    // a swipe tunes where it comes to rest (after the scroll settles, so a
    // smooth scroll the scrubber started never re-tunes on its way past)
    if (typeof list.addEventListener === "function") {
      var settle = null;
      list.addEventListener("scroll", function () {
        if (!overflows()) return;
        clearTimeout(settle);
        settle = setTimeout(function () { tune(nearest()); }, 120);
      }, { passive: true });
    }
    list.parentNode.insertBefore(range, list);
    tune(0);
  }
})();
