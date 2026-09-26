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
   - drag and tap to tune: the scale strip is the drag zone (touch-action:
     none). A press there moves the needle to it at once and a drag carries it,
     whatever the angle, naming the station under it in the readout. On touch
     the first tap or drag only selects (the needle rests there, the hint "Tap
     again to tune in" fades in, the page stays put); a second tap on that
     station, on the readout or on the hint follows its link. A mouse click,
     a mouse drag's release, the keyboard and assistive tech go there in one
     step. A cancelled gesture changes nothing; a gesture that starts on the
     rest of the band scrolls the page. The long-document list form (no
     needle) is plain tappable rows. No new semantics: the links and
     aria-current are the whole story.
   Markup contract: web/DESIGN-SYSTEM.md.
   ===================================================================== */
(function () {
  "use strict";
  var SLOP = 8;          // px a pointer travels before a press becomes a drag (a tap stays a tap)
  var FORGET = 6000;     // ms an untouched selection lasts
  var HINT = "Tap again to tune in";
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

  // ---- drag and tap to tune -------------------------------------------------
  // The scale strip is the drag zone (touch-action: none in the stylesheet): a touch that
  // starts there always tunes, whatever its angle; the rest of the band scrolls the page.
  // TOUCH is two steps: the first tap or drag only selects (the needle lands, the readout
  // names the station, a hint fades in, the page stays put); a second tap on that station,
  // on the readout or on the hint tunes in. Scrolling, a tap outside the tuner or 6s of
  // nothing clears the selection. A mouse, a pen, the keyboard and assistive tech are one
  // step, as before: only pointerType "touch" selects first.
  function drag(nav, links, current) {
    var band = nav.querySelector && nav.querySelector(".toc-tuner__band");
    var scale = nav.querySelector && nav.querySelector(".toc-tuner__scale");
    var needle = nav.querySelector && nav.querySelector(".toc-tuner__needle");
    var readout = nav.querySelector && nav.querySelector(".toc-tuner__readout");
    if (!band || !scale || !needle || !scale.addEventListener) return;
    var n = links.length;
    var mq = function (q) { return !!(window.matchMedia && window.matchMedia(q).matches); };
    var reduced = mq("(prefers-reduced-motion: reduce)");
    var g = null;        // the gesture in progress: { id, x0, y0, from, k, dragging, touch }
    var sel = -1;        // the touch selection waiting for its second tap
    var selY = 0;        // where the page was when it was made
    var timer = 0;
    var swallow = false; // the browser's own click at the end of a gesture is not a second tap
    var hint = null;
    // on any device: a touch laptop's finger selects even when its primary pointer is fine
    if (document.createElement && band.appendChild) {
      hint = document.createElement("span");
      hint.className = "toc-tuner__hint";
      hint.setAttribute("aria-hidden", "true");
      hint.textContent = HINT;
      band.appendChild(hint);
    }
    Array.prototype.forEach.call(links, function (a) { if (a.setAttribute) a.setAttribute("draggable", "false"); });

    function stationAt(x) {
      var r = scale.getBoundingClientRect(), f = (x - r.left) / r.width * n;
      return { k: Math.max(0, Math.min(n - 1, Math.floor(f))), at: Math.max(0, Math.min(n - 1, f - 0.5)) };
    }
    function tune(k) {
      for (var i = 0; i < n; i++) links[i].classList.toggle("is-tuning", i === k);
    }
    function show(x) {
      var s = stationAt(x);
      nav.style.setProperty("--at", String(reduced ? s.k : s.at));
      tune(s.k);
      return s.k;
    }
    function listForm() {
      return window.getComputedStyle && window.getComputedStyle(needle).display === "none";
    }
    function stopTimer() { if (timer && window.clearTimeout) window.clearTimeout(timer); timer = 0; }
    function clear() {           // back to the section in view
      stopTimer();
      sel = -1;
      nav.classList.remove("is-dragging");
      nav.classList.remove("is-selected");
      nav.style.removeProperty("--at");
      tune(-1);
    }
    function select(k) {
      sel = k;
      selY = window.pageYOffset || 0;
      nav.classList.remove("is-dragging");
      nav.classList.add("is-selected");
      nav.style.setProperty("--at", String(k));
      tune(k);
      stopTimer();
      timer = window.setTimeout(function () { if (sel >= 0) clear(); }, FORGET);
    }
    function go(k) {             // follow the station's link, as a click on it would
      clear();
      var held = swallow;
      swallow = false;                   // let this click through...
      links[k].click();
      swallow = held;                    // ...but not the gesture's own trailing one
      current(k);
    }
    function hold() {            // the gesture's own trailing click is not a second tap
      swallow = true;
      window.setTimeout(function () { swallow = false; }, 400);
    }

    scale.addEventListener("pointerdown", function (e) {
      swallow = false;
      if (!e.isPrimary || (e.pointerType === "mouse" && e.button !== 0) || e.ctrlKey || e.metaKey || e.shiftKey || listForm()) return;
      stopTimer();
      nav.classList.add("is-dragging");
      var k = show(e.clientX);
      g = { id: e.pointerId, x0: e.clientX, y0: e.clientY, from: k, k: k, dragging: false, touch: e.pointerType === "touch" };
    });
    scale.addEventListener("pointermove", function (e) {
      if (!g || e.pointerId !== g.id) return;
      if (!g.dragging) {
        if (Math.abs(e.clientX - g.x0) < SLOP && Math.abs(e.clientY - g.y0) < SLOP) return;
        g.dragging = true;               // any direction: inside the scale there is no page scroll to yield to
        if (scale.setPointerCapture) scale.setPointerCapture(e.pointerId);
      }
      if (e.preventDefault) e.preventDefault();
      var k = show(e.clientX);
      if (k !== g.k && g.touch && !reduced && window.navigator && window.navigator.vibrate) window.navigator.vibrate(8);
      g.k = k;
    });
    scale.addEventListener("pointerup", function (e) {
      if (!g || e.pointerId !== g.id) return;
      var was = g, k = stationAt(e.clientX).k;
      g = null;
      if (was.touch) {                   // two steps: select, then a second tap tunes in
        hold();
        if (!was.dragging && k === sel) go(k); else select(k);
        return;
      }
      clear();                           // a mouse or a pen: one step, as before
      if (!was.dragging) return;         // a click: the link's own navigation follows
      hold();
      if (k !== was.from) go(k);
    });
    function abandon() { if (g) { g = null; if (sel >= 0) select(sel); else clear(); } }
    scale.addEventListener("pointercancel", abandon);
    // a mouse or pen press that leaves the scale before it drags (no capture yet) and is let
    // go elsewhere never reaches the scale's pointerup: the window hears it, and it ends
    if (window.addEventListener) window.addEventListener("pointerup", function (e) { if (g && e.pointerId === g.id) abandon(); });
    nav.addEventListener("click", function (e) {
      var t = e.target;
      if (sel >= 0 && ((readout && readout.contains && readout.contains(t)) || (hint && (t === hint)))) {
        e.preventDefault();
        go(sel);
        return;
      }
      if (!swallow || !(band.contains && band.contains(t))) {
        // a link click that goes through (the keyboard, assistive tech, a late click) while a
        // touch selection waits: it is going somewhere, so the selection and hint go
        if (sel >= 0) for (var i = 0; i < n; i++) {
          if (links[i] === t || (links[i].contains && links[i].contains(t))) { clear(); current(i); break; }
        }
        return;
      }
      e.preventDefault();
      if (e.stopPropagation) e.stopPropagation();
      swallow = false;
    }, true);
    // ignoring the selection clears it: the page scrolls, or a tap lands outside the tuner
    // (a reader's scroll, not a few pixels of layout settling above the tuner)
    if (window.addEventListener) window.addEventListener("scroll", function () {
      if (sel >= 0 && !g && Math.abs((window.pageYOffset || 0) - selY) > 24) clear();
    }, { passive: true });
    if (document.addEventListener) document.addEventListener("pointerdown", function (e) {
      if (sel >= 0 && !(nav.contains && nav.contains(e.target))) clear();
    }, true);
  }
})();
