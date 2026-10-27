/* =====================================================================
   RogerAI - the cycle word: a word that tunes to the next station.

   Opt-in by markup. A [data-cycle="a|b|c"] word wearing the carrier
   sweep (.carrier) swaps to the next word every second loop of that
   sweep, so it is paced by the gradient, not a lone timer. The markup
   ships the first word, so JS off, crawlers and print read it as-is.
   Used by the nav's App / TUI / WebUI item and the homepage headline.

     data-cycle-hrefs="h1|h2|h3"  the enclosing link's href follows the word
     data-cycle-store="key"       each page load starts on the next word
                                  (localStorage; first visit = first word)
     data-cycle-fit=".selector"   the closest such ancestor gets
                                  --cycle-slack (em): the slot's spare width,
                                  so the words after it close up by transform
     data-cycle-reserve="word"    (CSS only) the slot holds this word's width

   Reduced motion: the load's word, never cycled. Hover or focus on the
   enclosing link pauses it, so a word cannot change under a click.
   Unless the word sits in an element named by aria-label, a visually
   hidden twin carries the first word and the moving word is aria-hidden,
   so assistive tech reads one stable phrase.
   Markup contract: web/DESIGN-SYSTEM.md ("Cycle word").
   ===================================================================== */
(function () {
  "use strict";
  var words = document.querySelectorAll("[data-cycle]");
  if (!words.length) return;

  var SWAP_EVERY = 2;    // carrier loops (3.2s each) between words
  var SWAP_MS = 260;     // the outgoing word's fade (components.css .cycle-word)
  var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)");
  var still = !!(reduce && reduce.matches);
  // DEV-ONLY founder preview, remove before ship: ?hero=refresh shows one word per load
  // and never cycles in the page, to compare with the default (cycles in the page).
  var refreshOnly = /[?&]hero=refresh(?:&|$)/.test(window.location.search || "");

  function loadIndex(key, n) {
    try {
      var k = "rogerai.cycle." + key;
      var i = parseInt(window.localStorage.getItem(k), 10);
      if (!(i >= 0 && i < n)) i = 0;
      window.localStorage.setItem(k, String((i + 1) % n));
      return i;
    } catch (e) { return 0; }
  }

  Array.prototype.forEach.call(words, function (word) {
    var list = word.getAttribute("data-cycle").split("|");
    if (list.length < 2) return;
    var hrefs = (word.getAttribute("data-cycle-hrefs") || "").split("|");
    var link = word.closest("a");
    var store = word.getAttribute("data-cycle-store");
    var fitSel = word.getAttribute("data-cycle-fit");
    var host = fitSel && word.closest(fitSel);

    if (!word.closest("[aria-label]")) {
      var twin = document.createElement("span");
      twin.classList.add("cycle-word__sr");
      twin.textContent = word.textContent;
      word.parentNode.insertBefore(twin, word);
      word.setAttribute("aria-hidden", "true");
    }

    function fit() {
      if (!host) return;
      var r = document.createRange();
      r.selectNodeContents(word);
      var px = parseFloat(window.getComputedStyle(word).fontSize) || 1;
      var slack = Math.max(0, word.getBoundingClientRect().width - r.getBoundingClientRect().width) / px;
      host.style.setProperty("--cycle-slack", slack.toFixed(3) + "em");
    }

    function show(i) {
      word.textContent = list[i];
      if (link && hrefs[i]) link.setAttribute("href", hrefs[i]);
      fit();
    }

    var i = store ? loadIndex(store, list.length) : 0;
    if (i) show(i);
    if (host) {
      host.classList.add("cycle-fit");
      if (document.fonts && document.fonts.ready) document.fonts.ready.then(fit);
      else fit();
    }

    if (still || (store && refreshOnly)) return;

    var loops = 0, paused = false;
    var pauser = link || word;
    pauser.addEventListener("mouseenter", function () { paused = true; });
    pauser.addEventListener("mouseleave", function () { paused = false; });
    pauser.addEventListener("focusin", function () { paused = true; });
    pauser.addEventListener("focusout", function () { paused = false; });

    function advance() {
      i = (i + 1) % list.length;
      word.classList.add("is-out");               // lift + fade the outgoing word
      window.setTimeout(function () {
        show(i);
        word.classList.remove("is-out");
        word.classList.add("is-in");              // place the incoming word low, no transition
        void word.offsetWidth;                    // reflow, so dropping is-in animates up
        word.classList.remove("is-in");
      }, SWAP_MS);
    }

    word.addEventListener("animationiteration", function (e) {
      if (e.target !== word || e.animationName !== "carrier-sweep" || paused) return;
      loops += 1;
      if (loops >= SWAP_EVERY) { loops = 0; advance(); }
    });
  });
})();
