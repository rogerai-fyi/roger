/* =====================================================================
   RogerAI - the range twin component (components.css .range-twin).

   Auto-initializes every <input type="number" data-range-twin>: it inserts a
   native range control right after the number field, with the field's min,
   max and step, and keeps the two in step both ways. Dragging the twin writes
   the field and fires the field's own "input" event, so whatever already
   listens to the field (a calculator) recalculates with no change.
   The typed field stays THE control for keyboards and screen readers: the twin
   is a pointer and touch convenience, out of the tab order and hidden from the
   accessibility tree, so nothing is announced twice. No-JS: the field alone.
   Markup contract: web/DESIGN-SYSTEM.md.
   ===================================================================== */
(function () {
  "use strict";
  Array.prototype.forEach.call(document.querySelectorAll("[data-range-twin]"), init);

  function init(field) {
    var twin = document.createElement("input");
    twin.type = "range";
    twin.className = "range-twin";
    twin.min = field.min || "0";
    twin.max = field.max || "100";
    twin.step = field.step && field.step !== "any" ? field.step : "1";
    twin.tabIndex = -1;
    twin.setAttribute("aria-hidden", "true");

    function show() {
      var lo = Number(twin.min), hi = Number(twin.max), v = Number(field.value);
      if (!isFinite(v)) v = lo;
      v = Math.min(hi, Math.max(lo, v));
      twin.value = String(v);
      twin.style.setProperty("--twin-at", ((v - lo) / (hi - lo || 1)) * 100 + "%");
    }
    twin.addEventListener("input", function () {
      field.value = twin.value;
      show();
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    field.addEventListener("input", show);
    field.parentNode.insertBefore(twin, field.nextSibling);
    show();
  }
})();
