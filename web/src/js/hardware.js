// Hardware page: ties the board grid and the spectrum to each other.
//
// Everything here is progressive - with JS off the page is complete, the needle simply
// never sweeps and the stops are ordinary drawing. Three jobs:
//   1. Sweep the tuning needle ONCE, when the illustration is first actually seen. Running
//      it on load means it has usually finished before a reader scrolls to it.
//   2. Hovering or focusing a board card lights that board's stop on the spectrum.
//   3. The reverse, which is the one that earns its keep: a stop is a control, so clicking
//      "Wave Giga" on the band takes you to the machine that carries it. Eight stops and ten
//      cards is more than a reader should have to match up by eye.
(function () {
  "use strict";
  var ladder = document.querySelector(".hw-ladder");
  var cards = document.querySelectorAll(".hw-card[data-stop]");
  if (!ladder && !cards.length) return;

  var reduced = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* ---- 1. sweep once, on first sight ---------------------------------- */
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

  /* ---- 2. card -> stop ------------------------------------------------- */
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

  /* ---- 3. stop -> card ------------------------------------------------- */
  // first card per stop, so "Roger Edge" (three boards) lands on the first of them
  var firstCard = {};
  Array.prototype.forEach.call(cards, function (card) {
    var name = card.getAttribute("data-stop");
    if (name && !firstCard[name]) firstCard[name] = card;
  });

  function go(name) {
    var card = firstCard[name];
    if (!card) return;
    card.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "center" });
    // a brief mark so the eye lands on the right card after the scroll settles
    card.classList.add("is-found");
    window.setTimeout(function () { card.classList.remove("is-found"); }, 1600);
  }

  Object.keys(stops).forEach(function (name) {
    var g = stops[name];
    if (!firstCard[name]) return; // a stop with no card is not a control
    var label = (g.querySelector(".hw-stop__tier") || {}).textContent || name;
    g.setAttribute("role", "button");
    g.setAttribute("tabindex", "0");

    g.setAttribute("aria-label", "Jump to the hardware for " + label.trim());
    g.classList.add("is-linked");
    g.addEventListener("click", function () { go(name); });
    g.addEventListener("keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " " && e.key !== "Spacebar") return;
      e.preventDefault();
      go(name);
    });
    g.addEventListener("focus", function () { light(name); });
    g.addEventListener("blur", function () { light(null); });
  });
})();
