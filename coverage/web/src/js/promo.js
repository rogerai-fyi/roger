/* =====================================================================
   RogerAI - dismissible $1-starter promo strip. Small, no deps, CSP-safe
   (external, script-src 'self').

   The strip ships hidden (.promo[hidden]). On load we reveal it ONLY when
   the visitor has not dismissed it, so a returning visitor who closed it
   never sees a flash. The x persists the choice in localStorage.

   It reveals immediately when not dismissed (no flash for the common live
   case), then asks the broker GET /promo whether the 1000 starter seeds are
   used up and auto-hides if they are. A transient fetch error leaves it shown
   (the promo is live by default; failing open to "shown" is harmless).
   ===================================================================== */
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";
  var STORE_KEY = "roger-promo-dismissed-v1";
  var bar = document.getElementById("promoBar");
  if (!bar) return;

  var dismissed = false;
  try { dismissed = localStorage.getItem(STORE_KEY) === "1"; } catch (e) {}

  // reveal immediately if not dismissed, then auto-hide if the broker reports
  // the starter-seed promo is no longer active (seeds exhausted).
  if (!dismissed) {
    bar.hidden = false;
    try {
      fetch(BROKER + "/promo", { mode: "cors" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) { if (d && d.active === false) bar.hidden = true; })
        .catch(function () {});
    } catch (e) {}
  }

  var close = document.getElementById("promoClose");
  if (close) {
    close.addEventListener("click", function () {
      bar.hidden = true;
      try { localStorage.setItem(STORE_KEY, "1"); } catch (e) {}
    });
  }
})();
