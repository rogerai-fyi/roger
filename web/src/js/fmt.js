// RogerFmt - the ONE site-wide number / money formatter.
//
// It renders a COMPACT, layout-safe form that fits a box (a k/M/B/T suffix for counts, a
// sub-cent-aware $ for money) and exposes the EXACT value two ways that DON'T shift layout:
// the native `title` tooltip on desktop hover, AND a small overlay popover on tap (so it works
// on touch). A number can keep growing - 1.1B today, 12B later - without ever bleeding its box,
// yet the precise figure is always one hover/tap away.
//
// usd() faithfully mirrors the Go internal/client FormatUSD (3 significant figures sub-cent,
// expanded to a plain decimal), so the web reads the same as the CLI/TUI.
//
//   RogerFmt.count(1111111111)  -> "1.1B"      RogerFmt.exact(1111111111) -> "1,111,111,111"
//   RogerFmt.usd(0.00000036)    -> "$0.00000036"
//   RogerFmt.bind(el, n)        -> sets el's compact text + wires the hover/tap exact reveal
//   RogerFmt.bind(el, n, {usd:true})  -> same, but money
//
// Pure, dependency-free, CSP-safe (external script, no inline). Loaded before a page's own JS.
(function () {
  "use strict";

  function grp(n) { return (Number(n) || 0).toLocaleString("en-US"); }

  // plainDecimal: a number's SHORTEST round-trip decimal, never scientific - so a tiny value
  // (3.6e-7) prints as "0.00000036", not "3.6e-7", with no float noise (it re-lays-out the
  // shortest string's own digits rather than padding via toFixed).
  function plainDecimal(x) {
    var s = String(x);
    var m = s.match(/^(-?)(\d+)(?:\.(\d+))?[eE]([+-]?\d+)$/);
    if (!m) return s; // already plain
    var sign = m[1], digits = m[2] + (m[3] || ""), point = m[2].length + parseInt(m[4], 10);
    if (point <= 0) return sign + "0." + "0".repeat(-point) + digits;
    if (point >= digits.length) return sign + digits + "0".repeat(point - digits.length);
    return sign + digits.slice(0, point) + "." + digits.slice(point);
  }

  var R = {};

  // ---- money (canonical, mirrors Go client.FormatUSD) -------------------------------------
  R.usd = function (v) {
    v = Number(v);
    if (!isFinite(v) || v < 0) return "-";
    if (v === 0) return "$0.00";
    if (v >= 0.01) return "$" + v.toFixed(2);
    // sub-cent: 3 significant figures (Go 'g',3), as a plain decimal - so the smallest real
    // charge always shows a nonzero digit (never "$0.00"/"$0").
    return "$" + plainDecimal(Number(v.toPrecision(3)));
  };
  // SIGNED money: the payouts/adjustment ledger is signed (a payout/chargeback/debit is
  // negative). usd() collapses negatives to "-" (Go parity, where money is always >= 0); this
  // keeps the sign + magnitude ("-$12.34"), so a signed ledger row or a clawed-back balance is
  // never blanked. A non-negative value reads identically to usd().
  R.usdSigned = function (v) {
    v = Number(v);
    if (!isFinite(v)) return "-";
    return v < 0 ? "-" + R.usd(-v) : R.usd(v);
  };
  // The EXACT money value for the reveal: full precision, grouped, never collapsed to $0.00 for
  // a real charge.
  R.usdExact = function (v) {
    v = Number(v);
    if (!isFinite(v) || v < 0) return "-";
    if (v === 0) return "$0.00";
    if (v >= 0.01) return "$" + v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 8 });
    return "$" + plainDecimal(v);
  };

  // ---- counts (compact k/M/B/T; full grouping under 1,000) --------------------------------
  var UNITS = [[1e15, "Q"], [1e12, "T"], [1e9, "B"], [1e6, "M"], [1e3, "k"]];
  R.count = function (n) {
    n = Number(n);
    if (!isFinite(n)) return "-";
    var a = Math.abs(n);
    for (var i = 0; i < UNITS.length; i++) {
      if (a >= UNITS[i][0]) {
        var ui = i, v = n / UNITS[i][0];
        // promote to the next-larger unit when rounding would hit 1000 (avoid a 5-glyph
        // "1000k"/"1000M"): 999,999 -> "1M", not "1000k".
        if (Math.abs(v) >= 999.5 && ui > 0) { ui--; v = n / UNITS[ui][0]; }
        var av = Math.abs(v);
        // one decimal under 10 of a unit (1.1B), none at/above (12B) - max ~4 glyphs.
        return (av < 10 ? v.toFixed(1).replace(/\.0$/, "") : String(Math.round(v))) + UNITS[ui][1];
      }
    }
    return grp(Math.round(n));
  };
  R.exact = function (n) { n = Number(n); return isFinite(n) ? grp(Math.round(n)) : "-"; };

  // ---- the exact-value reveal: native title (hover) + one shared overlay popover (tap) -----
  // The popover is position:absolute over the page, so revealing the exact value never reflows
  // or resizes anything - the compact number stays put. It is aria-hidden (a purely visual aid):
  // screen readers already get the exact value from the bound element's aria-label.
  var pop;
  function ensurePop() {
    if (pop) return pop;
    pop = document.createElement("div");
    pop.className = "rfmt-pop";
    pop.setAttribute("aria-hidden", "true");
    pop.hidden = true;
    document.body.appendChild(pop);
    document.addEventListener("click", function (e) {
      if (!(e.target && e.target.closest && e.target.closest(".rfmt"))) hide();
    });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape") hide(); });
    window.addEventListener("scroll", hide, { passive: true, capture: true });
    window.addEventListener("resize", hide);
    return pop;
  }
  function hide() {
    if (!pop) return;
    pop.hidden = true;
    if (pop.__for) { pop.__for.setAttribute("aria-expanded", "false"); pop.__for = null; }
  }
  function show(el) {
    // re-tapping the number that's already open toggles the popover closed.
    if (pop && !pop.hidden && pop.__for === el) { hide(); return; }
    hide(); // collapse any other open one first
    var p = ensurePop();
    p.__for = el;
    el.setAttribute("aria-expanded", "true");
    p.textContent = el.getAttribute("data-exact") || el.title || el.textContent;
    p.style.left = "-9999px"; p.style.top = "0"; p.hidden = false; // off-screen to measure
    var r = el.getBoundingClientRect();
    var vw = document.documentElement.clientWidth, vh = document.documentElement.clientHeight;
    var pw = p.offsetWidth, ph = p.offsetHeight;
    var x = Math.min(Math.max(8, r.left), Math.max(8, vw - pw - 8));
    // place below; flip above when it would fall off the bottom of the viewport.
    var below = r.bottom + 6, top = (below + ph <= vh) ? below : Math.max(8, r.top - ph - 6);
    p.style.left = Math.round(x + window.scrollX) + "px";
    p.style.top = Math.round(top + window.scrollY) + "px";
  }

  // bind(el, n[, {usd}]): render the compact form on el and wire the exact reveal. Idempotent -
  // safe to call again when a dashboard refreshes (updates the text/title, never double-wires).
  R.bind = function (el, n, opts) {
    if (!el) return el;
    opts = opts || {};
    var v = Number(n);
    // missing / non-numeric -> a plain "-" (no data), and strip any interactive state left by
    // a prior bind, so a "-" cell is never a stale button. Centralized here so every caller
    // gets it for free.
    if (!isFinite(v)) {
      el.textContent = "-";
      el.classList.remove("rfmt");
      ["role", "tabindex", "aria-label", "aria-expanded", "data-exact", "title"].forEach(function (a) { el.removeAttribute(a); });
      el.__rfmt = false; // re-binding back to a finite value re-wires the button affordance
      return el;
    }
    var compact = opts.usd ? R.usd(v) : R.count(v);
    var exact = opts.usd ? R.usdExact(v) : R.exact(v);
    el.textContent = compact;
    el.title = exact;                       // desktop hover: native tooltip
    el.setAttribute("data-exact", exact);
    el.setAttribute("aria-label", exact);   // screen readers get the exact value directly
    el.classList.add("rfmt");
    if (!el.__rfmt) {
      el.__rfmt = true;
      el.setAttribute("role", "button");
      el.setAttribute("aria-expanded", "false");
      if (!el.hasAttribute("tabindex")) el.tabIndex = 0;
      el.addEventListener("click", function (e) { e.stopPropagation(); show(el); });
      el.addEventListener("keydown", function (e) {
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); show(el); }
      });
      el.addEventListener("blur", hide);
    }
    return el;
  };

  if (typeof window !== "undefined") window.RogerFmt = R;
  if (typeof module !== "undefined" && module.exports) module.exports = R; // node test
})();
