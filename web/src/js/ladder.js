// Reveal the Wave ladder when it is actually looked at.
//
// The contract, deliberately the same one every animated figure on this site should
// use: observe -> flip a class -> let CSS do the animation with a per-item delay ->
// unobserve. JavaScript never tweens anything, so the motion cannot desync from the
// stylesheet and there is no rAF loop to leak.
//
// The final state is the DEFAULT. `is-pending` is added by this script, which means a
// reader with JavaScript disabled, or a script error, gets a fully drawn ladder rather
// than four invisible bars. That is the opposite of the usual reveal pattern and it is
// on purpose: the figure carries information, so it must never be hidden by decoration.
(function () {
  var ladders = document.querySelectorAll(".ladder");
  if (!ladders.length) return;

  // Reduced motion: leave the ladder drawn and never touch it again.
  var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)");
  if (still && still.matches) return;

  if (!("IntersectionObserver" in window)) return; // drawn already; nothing to do

  var seen = new IntersectionObserver(
    function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        entry.target.classList.remove("is-pending");
        entry.target.classList.add("is-revealed");
        seen.unobserve(entry.target); // one-shot: it does not replay on scroll-back
      });
    },
    // Fire a little before the figure is fully on screen, so the wipe is already
    // running by the time it is comfortably readable.
    { rootMargin: "0px 0px -18% 0px", threshold: 0.18 }
  );

  ladders.forEach(function (el) {
    el.classList.add("is-pending");
    seen.observe(el);
  });
})();
