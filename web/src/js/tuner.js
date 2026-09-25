/* =====================================================================
   RogerAI - the TOC tuner component (components.css .toc-tuner).

   Auto-initializes every <nav data-tuner> on the page: an in-page table of
   contents that is plain links without JS. The needle and the readout are
   CSS; this adds two things:
   - roving focus: the tuner is ONE tab stop; Left/Right move between
     stations (wrapping), Home/End jump to the ends, Enter follows the link
   - the resting station: the section in view marks its station current
     (--cur on the tuner, aria-current="location"), so the needle rests on
     where you are when nothing is pointed at. Observed, never pinned.
   - drag to tune: a finger (or a mouse) dragged along the band moves the
     needle with it and names the station under it in the readout; letting go
     on a station follows that station's link. A tap is still the link's own
     click; a mostly vertical move is the page scrolling (touch-action: pan-y);
     a cancelled gesture, or one that ends on the station it began on, changes
     nothing. The long-document list form (no needle) is tapped, not dragged.
     The drag adds no semantics: the links and aria-current are the whole story.
   Markup contract: web/DESIGN-SYSTEM.md.
   ===================================================================== */
(function () {
  "use strict";
  Array.prototype.forEach.call(document.querySelectorAll("[data-tuner]"), init);

  function init(nav) {
    var links = nav.querySelectorAll("a");
    if (!links.length) return;

    function rove(k) {
      for (var i = 0; i < links.length; i++) links[i].setAttribute("tabindex", i === k ? "0" : "-1");
    }
    rove(0);
    Array.prototype.forEach.call(links, function (a, k) {
      a.addEventListener("keydown", function (e) {
        var n = links.length, to = null;
        if (e.key === "ArrowRight") to = (k + 1) % n;
        else if (e.key === "ArrowLeft") to = (k - 1 + n) % n;
        else if (e.key === "Home") to = 0;
        else if (e.key === "End") to = n - 1;
        if (to === null) return;
        e.preventDefault();
        rove(to);
        links[to].focus();
      });
    });

    function current(k) {
      nav.style.setProperty("--cur", String(k));
      for (var i = 0; i < links.length; i++) {
        links[i].classList.toggle("is-current", i === k);
        if (i === k) links[i].setAttribute("aria-current", "location"); else links[i].setAttribute("aria-current", "false");
      }
    }

    drag(nav, links, current);

    if (!window.IntersectionObserver) return;
    var byId = {};
    Array.prototype.forEach.call(links, function (a, k) { byId[a.getAttribute("href").slice(1)] = k; });
    // a section is "in view" when it crosses a band around the upper third
    // (30% to 40% down the viewport). A section that leaves it DOWNWARD means the
    // reader went back up. A jump (Home, a back-to-top link) can skip every section
    // in between without any of them reporting, so read where the sections are: rest
    // on the last one whose top is above the band's lower edge, or on §1 when none is
    // (back above the first section, in the hero).
    var sections = [];
    function resting() {
      var line = window.innerHeight * 0.4, k = 0; // the band's lower edge
      sections.forEach(function (s) { if (s.el.getBoundingClientRect().top <= line) k = s.k; });
      return k;
    }
    var io = new window.IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (!(e.target.id in byId)) return;
        if (e.isIntersecting) current(byId[e.target.id]);
        else if (e.boundingClientRect.top > 0) current(resting());
      });
    }, { rootMargin: "-30% 0px -60% 0px" });
    Object.keys(byId).forEach(function (id) {
      var el = document.getElementById(id);
      if (el) { io.observe(el); sections.push({ el: el, k: byId[id] }); }
    });
  }

  // ---- drag to tune -------------------------------------------------------
  var SLOP = 8; // px a pointer travels before a press becomes a drag (a tap stays a tap)
  function drag(nav, links, current) {
    var band = nav.querySelector && nav.querySelector(".toc-tuner__band");
    var scale = nav.querySelector && nav.querySelector(".toc-tuner__scale");
    var needle = nav.querySelector && nav.querySelector(".toc-tuner__needle");
    if (!band || !scale || !needle || !band.addEventListener) return;
    var n = links.length;
    var reduced = !!(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
    var g = null;        // the gesture in progress: { id, x0, y0, from, k, dragging, touch }
    var swallow = false; // the browser's own click at the end of a drag is not a tap
    Array.prototype.forEach.call(links, function (a) { if (a.setAttribute) a.setAttribute("draggable", "false"); });

    function stationAt(x) {
      var r = scale.getBoundingClientRect(), f = (x - r.left) / r.width * n;
      return { k: Math.max(0, Math.min(n - 1, Math.floor(f))), at: Math.max(0, Math.min(n - 1, f - 0.5)) };
    }
    function tune(k) {
      for (var i = 0; i < n; i++) links[i].classList.toggle("is-tuning", i === k);
    }
    function end() {
      nav.classList.remove("is-dragging");
      nav.style.removeProperty("--at");
      tune(-1);
      g = null;
    }
    function listForm() {
      return window.getComputedStyle && window.getComputedStyle(needle).display === "none";
    }

    band.addEventListener("pointerdown", function (e) {
      swallow = false;
      if (!e.isPrimary || (e.pointerType === "mouse" && e.button !== 0) || listForm()) return;
      var s = stationAt(e.clientX);
      g = { id: e.pointerId, x0: e.clientX, y0: e.clientY, from: s.k, k: s.k, dragging: false, touch: e.pointerType !== "mouse" };
    });
    band.addEventListener("pointermove", function (e) {
      if (!g || e.pointerId !== g.id) return;
      var dx = e.clientX - g.x0, dy = e.clientY - g.y0;
      if (!g.dragging) {
        if (Math.abs(dx) < SLOP && Math.abs(dy) < SLOP) return;
        if (Math.abs(dy) >= Math.abs(dx)) { g = null; return; } // the page is scrolling
        g.dragging = true;
        nav.classList.add("is-dragging");
        if (band.setPointerCapture) band.setPointerCapture(e.pointerId);
      }
      if (e.preventDefault) e.preventDefault();
      var s = stationAt(e.clientX);
      nav.style.setProperty("--at", String(reduced ? s.k : s.at));
      if (s.k !== g.k && g.touch && !reduced && window.navigator && window.navigator.vibrate) window.navigator.vibrate(8);
      g.k = s.k;
      tune(s.k);
    });
    band.addEventListener("pointerup", function (e) {
      if (!g || e.pointerId !== g.id) return;
      var was = g;
      end();
      if (!was.dragging) return;          // a tap: the link's own click follows
      swallow = true;                     // the click the browser sends after this drag
      var k = stationAt(e.clientX).k;
      if (k !== was.from) {
        swallow = false;
        links[k].click();                 // follow it as its link would
        swallow = true;
        current(k);
      }
      window.setTimeout(function () { swallow = false; }, 400); // no trailing click came
    });
    band.addEventListener("pointercancel", function () { if (g) end(); });
    nav.addEventListener("click", function (e) {
      if (!swallow) return;
      e.preventDefault();
      if (e.stopPropagation) e.stopPropagation();
      swallow = false;
    }, true);
  }
})();
