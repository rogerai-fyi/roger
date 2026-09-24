/* =====================================================================
   RogerAI - homepage scroll stage: motion variants + gentle section snap.

   The homepage's scroll choreography comes in three variants the founder
   can compare in one build (2026-09-23 lab):
     ?motion=a  scroll-driven background + the plain section reveals
     ?motion=b  A + the section choreography in home.css (DEFAULT)
     ?motion=c  B + a gentle snap between major sections (desktop only)
   It also drives the instrument (all variants): the tuning dial under the
   nav, whose needle moves through the sections as you read.
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

  // ---- the instrument: the needle tunes through the sections ----
  // Darkbloom's control, on native scrolling: one reading line (a quarter of
  // the viewport under the dial); the section it falls in is the station on
  // air (aria-current). The needle HOLDS on that station for the first 70% of
  // the section and GLIDES to the next, eased, through the last 30%: a
  // progress window per step, so it settles while you read. Above the first
  // section (the cover) it rests at the start of the scale.
  var navBar = document.querySelector(".nav");
  var dial = document.querySelector(".dial");
  var stations = dial ? dial.querySelectorAll('a[href^="#"]') : [];
  var targets = [];
  for (var s = 0; s < stations.length; s++) {
    targets.push(document.getElementById(stations[s].getAttribute("href").slice(1)));
  }
  var HOLD = 0.7, onAir = -1;
  function ease(t) { return t * t * (3 - 2 * t); }
  function slot(k) { return (k + 0.5) / stations.length; }
  function tune() {
    if (!dial || !stations.length) return;
    var line = dial.getBoundingClientRect().bottom + window.innerHeight * 0.25;
    var k = -1, t = 0;
    for (var i = 0; i < targets.length; i++) {
      if (!targets[i]) continue;
      var r = targets[i].getBoundingClientRect();
      if (r.top <= line) { k = i; t = Math.min(1, (line - r.top) / Math.max(1, r.height)); }
    }
    var at;
    if (k < 0) {
      // the cover: glide from the start of the scale onto station 1 as §1 arrives
      var first = targets[0] ? targets[0].getBoundingClientRect().top : Infinity;
      var h = window.innerHeight;
      at = slot(0) * ease(Math.max(0, Math.min(1, 1 - (first - line) / (h * (1 - HOLD)))));
      at = Math.min(at, slot(0) * 0.999);
    } else {
      var next = k + 1 < stations.length ? slot(k + 1) : slot(k);
      at = slot(k) + (next - slot(k)) * ease(Math.max(0, (t - HOLD) / (1 - HOLD)));
    }
    dial.style.setProperty("--needle", at.toFixed(4));
    if (k !== onAir) {
      if (onAir >= 0) stations[onAir].removeAttribute("aria-current");
      if (k >= 0) stations[k].setAttribute("aria-current", "true");
      onAir = k;
    }
  }
  tune();
  window.addEventListener("scroll", tune, { passive: true });
  window.addEventListener("resize", tune);

  // ---- hold a #fragment while late content loads above it ----
  // The market rows and the reel's power-on grow the page ABOVE a target like
  // #monetize after the browser has already scrolled to it, and the target
  // lands far down the screen. Until the reader takes over (or a few seconds
  // pass), re-align the target whenever the page's size changes.
  var hashTarget = window.location.hash && document.getElementById(window.location.hash.slice(1));
  if (hashTarget && window.ResizeObserver) {
    var hold = new window.ResizeObserver(function () { hashTarget.scrollIntoView({ block: "start" }); });
    var release = function () { hold.disconnect(); };
    hold.observe(document.body);
    ["wheel", "keydown", "touchstart", "pointerdown"].forEach(function (t) {
      window.addEventListener(t, release, { passive: true, once: true });
    });
    window.setTimeout(release, 4000);
  }

  // ---- variant C: settle-then-snap ----
  if (variant !== "c") return;
  var fine = window.matchMedia("(min-width: 1024px) and (pointer: fine)");
  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
  var REACH = 0.18;   // how close (share of the viewport) a section top must be
  var BACK = 0.08;    // how far against your direction it may pull
  var lastY = window.scrollY, dir = 0, snapping = false, timer = null;
  // only a wheel / trackpad scroll may be finished by a snap: a keyboard
  // scroll (PageDown, Space, arrows, find) lands exactly where it was sent
  var wheeled = false;

  function busy() {
    var el = document.activeElement;
    if (el && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return true;
    var sel = window.getSelection && window.getSelection();
    return !!(sel && String(sel).length);
  }

  function settle() {
    if (snapping) { snapping = false; return; }     // this rest is our own snap landing
    var byWheel = wheeled;
    wheeled = false;
    if (!byWheel || !fine.matches || reduced.matches || busy()) return;
    var bar = dial || navBar;                        // sections line up under the dial
    var line = bar ? bar.getBoundingClientRect().bottom : 0;
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
  // any input from the reader means the next rest is theirs, not our landing;
  // only the wheel makes it snappable
  ["wheel", "keydown", "touchstart", "pointerdown", "hashchange"].forEach(function (t) {
    window.addEventListener(t, function () { snapping = false; wheeled = t === "wheel"; }, { passive: true });
  });
})();
