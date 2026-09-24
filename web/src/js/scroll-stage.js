/* =====================================================================
   RogerAI - homepage scroll stage: motion variants + gentle section snap.

   The homepage's scroll choreography comes in three variants the founder
   can compare in one build (2026-09-23 lab):
     ?motion=a  scroll-driven background + the plain section reveals
     ?motion=b  A + the section choreography in home.css (DEFAULT)
     ?motion=c  B + a gentle snap between major sections (desktop only)
   It also runs the adaptive chrome (all variants): the nav, promo and rail
   turn ink over the ink zones.
   No parameter = the default, with no attribute at all, so production pages
   carry nothing extra. ?lab=1 (or any ?motion=) shows a small switcher.

   The snap never fights the reader: it acts only after a scroll has come
   to rest, only when a section's top is already within a short distance
   of the nav line (in the direction you were going), only with a fine
   pointer on a wide screen, never under reduced motion, never while typing
   or selecting text, and any new scroll simply overrides it.
   ===================================================================== */
(function () {
  "use strict";

  var root = document.documentElement;
  var params = new URLSearchParams(window.location.search);
  var asked = (params.get("motion") || "").toLowerCase();
  var VARIANTS = ["a", "b", "c"], DEFAULT = "b";
  if (VARIANTS.indexOf(asked) >= 0) root.setAttribute("data-motion", asked);
  var variant = VARIANTS.indexOf(asked) >= 0 ? asked : DEFAULT;

  // ---- the lab switcher: only when asked for by URL ----
  if (params.has("lab") || params.has("motion")) {
    var lab = document.createElement("nav");
    lab.className = "motion-lab";
    lab.setAttribute("aria-label", "Motion variants");
    lab.appendChild(document.createTextNode("motion"));
    VARIANTS.forEach(function (v) {
      var a = document.createElement("a");
      a.href = "?motion=" + v + "&lab=1";
      a.textContent = v.toUpperCase() + (v === DEFAULT ? "*" : "");
      if (v === variant) a.setAttribute("aria-current", "true");
      lab.appendChild(a);
    });
    document.body.appendChild(lab);
  }

  // ---- adaptive chrome: the nav, promo and rail take the tone under them ----
  // data-chrome="ink" whenever an ink zone sits under the nav's bottom edge
  // (tokens.css re-themes .nav/.promo/.rail from it). The first set holds
  // transitions off (data-chrome-instant) so the cover doesn't fade the nav
  // in on load; later swaps transition. No JS: the default paper chrome.
  var zones = document.querySelectorAll(".tone-zone");
  var navBar = document.querySelector(".nav");
  var ink = null;
  function chrome() {
    if (!navBar || !zones.length) return;
    var line = navBar.getBoundingClientRect().bottom + 1, over = false;
    for (var z = 0; z < zones.length; z++) {
      var zr = zones[z].getBoundingClientRect();
      if (zr.top <= line && zr.bottom > line) { over = true; break; }
    }
    if (over === ink) return;
    var first = ink === null;
    ink = over;
    if (first) {
      root.setAttribute("data-chrome-instant", "");
      window.setTimeout(function () { root.removeAttribute("data-chrome-instant"); }, 60);
    }
    if (over) root.setAttribute("data-chrome", "ink"); else root.removeAttribute("data-chrome");
  }
  chrome();
  window.addEventListener("scroll", chrome, { passive: true });
  window.addEventListener("resize", chrome);

  // ---- variant C: settle-then-snap ----
  if (variant !== "c") return;
  var fine = window.matchMedia("(min-width: 1024px) and (pointer: fine)");
  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
  var REACH = 0.18;   // how close (share of the viewport) a section top must be
  var BACK = 0.08;    // how far against your direction it may pull
  var lastY = window.scrollY, dir = 0, snapping = false, timer = null;

  function busy() {
    var el = document.activeElement;
    if (el && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return true;
    var sel = window.getSelection && window.getSelection();
    return !!(sel && String(sel).length);
  }

  function settle() {
    if (snapping) { snapping = false; return; }     // this rest is our own snap landing
    if (!fine.matches || reduced.matches || busy()) return;
    var nav = document.querySelector(".nav");
    var line = nav ? nav.getBoundingClientRect().bottom : 0;
    var H = window.innerHeight;
    var max = (document.documentElement.scrollHeight || 0) - H;
    var best = null;
    var sections = document.querySelectorAll("main > section, .tone-zone > section");
    for (var i = 0; i < sections.length; i++) {
      var d = sections[i].getBoundingClientRect().top - line;
      var ahead = dir >= 0 ? d : -d;                  // positive = where you were heading
      if (ahead > REACH * H || ahead < -BACK * H) continue;
      if (window.scrollY + d > max) continue;         // the page can't scroll that far
      if (best === null || Math.abs(d) < Math.abs(best)) best = d;
    }
    if (best === null || Math.abs(best) < 4) return;
    snapping = true;
    window.scrollBy({ top: best, behavior: "smooth" });
  }

  window.addEventListener("scroll", function () {
    var y = window.scrollY;
    if (y !== lastY) dir = y > lastY ? 1 : -1;
    lastY = y;
    if (!("onscrollend" in window)) {
      window.clearTimeout(timer);
      timer = window.setTimeout(settle, 140);
    }
  }, { passive: true });
  window.addEventListener("scrollend", settle);
  // a key or wheel from the reader cancels the idea that the next rest is ours
  ["wheel", "keydown", "touchstart", "pointerdown"].forEach(function (t) {
    window.addEventListener(t, function () { snapping = false; }, { passive: true });
  });
})();
