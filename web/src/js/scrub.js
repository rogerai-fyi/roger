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

    range.addEventListener("input", function () { tune(Number(range.value)); });
    Array.prototype.forEach.call(items, function (li, k) {
      li.addEventListener("pointerenter", function () { tune(k); });
    });
    list.parentNode.insertBefore(range, list);
    tune(0);
  }
})();
