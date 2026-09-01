// Hardware page: ties the board grid back to the spectrum above it.
//
// Two jobs, both progressive - with JS off the page is complete, the needle simply never
// sweeps and the cards are ordinary cards.
//   1. Sweep the tuning needle ONCE, when the illustration is first actually seen. Running
//      it on load means it has usually finished before a reader scrolls to it.
//   2. Hovering or focusing a board card lights that board's stop on the spectrum, which is
//      the only thing connecting "this photograph" to "this point on the ladder".
(function () {
  "use strict";
  var ladder = document.querySelector(".hw-ladder");
  var cards = document.querySelectorAll(".hw-card[data-stop]");
  if (!ladder && !cards.length) return;

  var reduced = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // 1. sweep once, on first sight
  if (ladder && !reduced) {
    var sweep = function () { ladder.classList.add("is-sweeping"); };
    if ("IntersectionObserver" in window) {
      var io = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          if (!e.isIntersecting) return;
          sweep();
          io.disconnect();
        });
      }, { threshold: 0.35 });
      io.observe(ladder);
    } else {
      sweep();
    }
  }

  // 2. card -> stop
  var stops = {};
  Array.prototype.forEach.call(document.querySelectorAll(".hw-stop[data-stop]"), function (g) {
    stops[g.getAttribute("data-stop")] = g;
  });
  var lit = null;
  function light(name) {
    if (lit === name) return;
    if (lit && stops[lit]) stops[lit].classList.remove("is-lit");
    lit = name && stops[name] ? name : null;
    if (lit) stops[lit].classList.add("is-lit");
  }
  Array.prototype.forEach.call(cards, function (card) {
    var name = card.getAttribute("data-stop");
    ["mouseenter", "focusin"].forEach(function (ev) {
      card.addEventListener(ev, function () { light(name); });
    });
    ["mouseleave", "focusout"].forEach(function (ev) {
      card.addEventListener(ev, function () { light(null); });
    });
  });
})();
