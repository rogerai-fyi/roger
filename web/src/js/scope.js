// The Wave scope: reveal on approach, and link the plot to the legend both ways.
//
// The contract, deliberately the same one every animated figure on this site should
// use: observe -> flip a class -> let CSS do the animation with a per-item delay ->
// unobserve. JavaScript never tweens anything, so the motion cannot desync from the
// stylesheet and there is no rAF loop to leak.
//
// The final state is the DEFAULT. `is-pending` is added by this script, which means a
// reader with JavaScript disabled, or a script error, gets a fully drawn scope rather
// than four invisible bands. That is the opposite of the usual reveal pattern and it is
// on purpose: the figure carries information, so it must never be hidden by decoration.
//
// The link works the same way. The plot and the legend are one object - a slot raised
// from either side raises on both - but every fact is already in the markup, so with
// scripting off the only thing lost is the emphasis.
(function () {
  var scopes = document.querySelectorAll(".scope");
  if (!scopes.length) return;

  var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)");
  var reduced = !!(still && still.matches);

  // ---- the two-way link ----------------------------------------------------
  Array.prototype.forEach.call(scopes, function (scope) {
    var contacts = scope.querySelectorAll(".scope__contact");
    var rows = scope.querySelectorAll(".scope__row");
    if (!contacts.length || !rows.length) return;

    var pinned = null; // a click SELECTS, so a reader can study one slot hands-free

    function paint(slot) {
      var on = slot || pinned;
      scope.classList.toggle("is-linked", !!on);
      [contacts, rows].forEach(function (set) {
        Array.prototype.forEach.call(set, function (el) {
          el.classList.toggle("is-on", !!on && el.getAttribute("data-slot") === on);
        });
      });
      Array.prototype.forEach.call(rows, function (row) {
        var btn = row.querySelector(".scope__pick");
        if (btn) btn.setAttribute("aria-pressed", String(row.getAttribute("data-slot") === pinned));
      });
    }

    function hover(slot) { return function () { paint(slot); }; }
    function clear() { paint(null); }

    Array.prototype.forEach.call(contacts, function (contact) {
      var slot = contact.getAttribute("data-slot");
      contact.addEventListener("mouseenter", hover(slot));
      contact.addEventListener("mouseleave", clear);
    });

    Array.prototype.forEach.call(rows, function (row) {
      var slot = row.getAttribute("data-slot");
      var btn = row.querySelector(".scope__pick");
      if (!btn) return;
      row.addEventListener("mouseenter", hover(slot));
      row.addEventListener("mouseleave", clear);
      // Focus has to do what hover does, or the link is pointer-only.
      btn.addEventListener("focus", hover(slot));
      btn.addEventListener("blur", clear);
      btn.addEventListener("click", function () {
        pinned = pinned === slot ? null : slot; // toggle off, or move the selection
        paint(pinned);
      });
    });

    scope.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && pinned) { pinned = null; paint(null); }
    });
  });

  // ---- reveal --------------------------------------------------------------
  if (reduced) return;                      // leave the plot drawn and never touch it
  if (!("IntersectionObserver" in window)) return;

  var seen = new IntersectionObserver(
    function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        entry.target.classList.remove("is-pending");
        entry.target.classList.add("is-revealed");
        seen.unobserve(entry.target); // one-shot: it does not replay on scroll-back
      });
    },
    { rootMargin: "0px 0px -18% 0px", threshold: 0.18 }
  );

  Array.prototype.forEach.call(scopes, function (el) {
    el.classList.add("is-pending");
    Array.prototype.forEach.call(el.querySelectorAll(".scope__contact"), function (c, i) {
      c.style.setProperty("--i", String(i));
    });
    seen.observe(el);
  });
})();
