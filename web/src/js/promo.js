/* =====================================================================
   RogerAI - dismissible $1-starter promo strip. Small, no deps, CSP-safe
   (external, script-src 'self').

   The strip ships hidden (.promo[hidden]). On load we reveal it ONLY when
   the visitor has not dismissed it, so a returning visitor who closed it
   never sees a flash. The x persists the choice in localStorage.

   To auto-hide once the broker's 1000 starter seeds are used up, gate the
   reveal below on a cheap broker seed-remaining signal (see the note in
   promo.html). For now the promo is live, so it always shows (until x).
   ===================================================================== */
(function () {
  "use strict";

  var STORE_KEY = "roger-promo-dismissed-v1";
  var bar = document.getElementById("promoBar");
  if (!bar) return;

  var dismissed = false;
  try { dismissed = localStorage.getItem(STORE_KEY) === "1"; } catch (e) {}

  // reveal only if still live + not dismissed
  if (!dismissed) bar.hidden = false;

  var close = document.getElementById("promoClose");
  if (close) {
    close.addEventListener("click", function () {
      bar.hidden = true;
      try { localStorage.setItem(STORE_KEY, "1"); } catch (e) {}
    });
  }
})();
