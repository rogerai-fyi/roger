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

    if (!window.IntersectionObserver) return;
    var byId = {};
    Array.prototype.forEach.call(links, function (a, k) { byId[a.getAttribute("href").slice(1)] = k; });
    function current(k) {
      nav.style.setProperty("--cur", String(k));
      for (var i = 0; i < links.length; i++) {
        links[i].classList.toggle("is-current", i === k);
        if (i === k) links[i].setAttribute("aria-current", "location"); else links[i].setAttribute("aria-current", "false");
      }
    }
    // a section is "in view" when it crosses a band around the upper third
    // back above the first section (the hero), §1 leaves the band downward:
    // rest on §1 again rather than on wherever you last were
    var io = new window.IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (!(e.target.id in byId)) return;
        if (e.isIntersecting) current(byId[e.target.id]);
        else if (byId[e.target.id] === 0 && e.boundingClientRect.top > 0) current(0);
      });
    }, { rootMargin: "-30% 0px -60% 0px" });
    Object.keys(byId).forEach(function (id) {
      var el = document.getElementById(id);
      if (el) io.observe(el);
    });
  }
})();
