/* =====================================================================
   RogerAI - /billing : the money-flow help modal.

   A small accessible modal opened by any [data-help-open] (the Wallet "?"
   button + the fine-print "How the money works" link). It explains, in
   plain language, how money is tracked end to end: metered -> priced ->
   debited -> written to the append-only ledger -> "Verified" re-sums that
   ledger to re-derive the balance (a drift check) -> operator earnings
   accrue + pay out. No network, no state - pure DOM, same shape as
   js/report.js. CSP-safe (external, script-src 'self').

   Accessibility: focus moves into the dialog on open and restores to the
   opener on close; Esc and a backdrop click close it; Tab is trapped
   inside the dialog; every trigger's aria-expanded is kept in sync.
   Reduced-motion-safe: the CSS fade is gated on prefers-reduced-motion, and the
   #ledger-demo explainer video plays only while open + rests paused on its poster
   frame under reduced-motion. The page stays fully usable with this script absent
   (the modal simply never opens; the gif <img> still shows).
   ===================================================================== */
(function () {
  "use strict";

  var modal = document.getElementById("ledgerModal");
  if (!modal) return;
  var scrim    = document.getElementById("ledgerScrim");
  var dialog   = modal.querySelector(".bx-help__dialog");
  var closeBtn = document.getElementById("ledgerClose");
  var doneBtn  = document.getElementById("ledgerDone");
  var lastFocus = null;
  // The animated explainer plays only while the modal is open, and never under
  // reduced-motion (it rests on its poster frame). The gif <img> covers browsers
  // without <video>; querySelector returns null there, so every use is guarded.
  var demoVid = modal.querySelector("#ledger-demo video");
  var reduceMotion = window.matchMedia
    ? window.matchMedia("(prefers-reduced-motion: reduce)") : null;
  // The 3.7MB poster gif is deferred (data-poster) so a closed modal costs zero bytes on
  // page load - a `poster=` gif is fetched on paint even under preload="none". Promote it
  // to a real poster the first time the modal opens, right before the demo can be seen.
  function hydratePoster() {
    if (!demoVid) return;
    var p = demoVid.getAttribute("data-poster");
    if (p && !demoVid.getAttribute("poster")) demoVid.setAttribute("poster", p);
  }
  function playDemo() {
    if (!demoVid) return;
    hydratePoster();
    if (reduceMotion && reduceMotion.matches) {
      try { demoVid.pause(); demoVid.currentTime = 0; } catch (e) {}
      return;
    }
    if (demoVid.play) { var p = demoVid.play(); if (p && p.catch) p.catch(function () {}); }
  }
  function pauseDemo() { if (demoVid && demoVid.pause) { try { demoVid.pause(); } catch (e) {} } }

  function triggers() { return document.querySelectorAll("[data-help-open]"); }
  function setExpanded(open) {
    var t = triggers();
    for (var i = 0; i < t.length; i++) {
      t[i].setAttribute("aria-expanded", open ? "true" : "false");
    }
  }

  function open() {
    lastFocus = document.activeElement;
    modal.hidden = false;
    document.body.classList.add("bx-help-open");
    setExpanded(true);
    playDemo();
    if (closeBtn && closeBtn.focus) closeBtn.focus();
  }

  function close() {
    if (modal.hidden) return;
    modal.hidden = true;
    document.body.classList.remove("bx-help-open");
    setExpanded(false);
    pauseDemo();
    if (lastFocus && lastFocus.focus) { try { lastFocus.focus(); } catch (e) {} }
  }

  // Delegated open: any [data-help-open] anywhere on the page.
  document.addEventListener("click", function (e) {
    var t = e.target.closest && e.target.closest("[data-help-open]");
    if (!t) return;
    e.preventDefault();
    open();
  });

  if (closeBtn) closeBtn.addEventListener("click", close);
  if (doneBtn) doneBtn.addEventListener("click", close);
  if (scrim) scrim.addEventListener("click", close);
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !modal.hidden) close();
  });

  // Keep focus inside the dialog while open (light focus trap).
  if (dialog) dialog.addEventListener("keydown", function (e) {
    if (e.key !== "Tab") return;
    var f = dialog.querySelectorAll(
      'button, a[href], select, textarea, input, [tabindex]:not([tabindex="-1"])');
    if (!f.length) return;
    var first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { last.focus(); e.preventDefault(); }
    else if (!e.shiftKey && document.activeElement === last) { first.focus(); e.preventDefault(); }
  });
})();
