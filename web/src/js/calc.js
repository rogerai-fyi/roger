/* =====================================================================
   The pricing page's two sums.

   Every number here is one of three things and nothing else: the reader's
   own input, a policy constant of ours, or live market data. There is no
   fourth category, and in particular no competitor's price - naming one
   would put a number we do not control, and cannot keep current, on the
   page whose argument is that we do not do that.

   OPERATOR_SHARE is 1 - the broker's take rate (cmd/rogerai-broker
   main.go -fee, default 0.30). test/calculator.test.mjs reads that flag
   and requires this constant to match it.

   The live rate is READ, never assumed. When the market is quiet or
   unreachable the field is left to the reader with a note saying so,
   because a fabricated "typical" rate is exactly the kind of number this
   page exists to not print.
   ===================================================================== */
(function () {
  "use strict";

  var OPERATOR_SHARE = 0.7; // 1 - the broker's 0.30 take rate
  var DAYS = 30;            // a thirty-day month, stated on the page
  var BROKER = "https://broker.rogerai.fm";

  function $(id) { return document.getElementById(id); }
  // Clamped to the input's OWN min/max. Without this the markup declared a 24-hour day
  // and a 100% ceiling that the arithmetic then ignored, so a typed 500% utilisation
  // computed happily and printed a number nobody could earn.
  function num(id) {
    var el = $(id);
    if (!el) return 0;
    var v = parseFloat(el.value);
    if (!isFinite(v)) return 0;
    var lo = parseFloat(el.min), hi = parseFloat(el.max);
    if (isFinite(lo) && v < lo) v = lo;
    if (isFinite(hi) && v > hi) v = hi;
    return v >= 0 ? v : 0;
  }
  // usd defers to the site's one money renderer when it is loaded, so a figure here
  // reads the same as a figure anywhere else on the site.
  function usd(n) {
    if (window.RogerFmt && window.RogerFmt.usd) return window.RogerFmt.usd(n);
    if (!isFinite(n)) return "$0.00";
    return "$" + n.toFixed(2);
  }
  function millions(n) {
    if (n >= 1000) return (n / 1000).toFixed(1) + "B";
    return n.toFixed(1) + "M";
  }

  /* ---------- operator: capacity x a price you set ------------------ */
  function operator() {
    var rate = num("opRate"), tps = num("opTps"), hours = num("opHours"), busy = num("opBusy") / 100;
    var tokensPerMonth = tps * 3600 * hours * busy * DAYS;   // tokens
    var m = tokensPerMonth / 1e6;                            // millions of tokens
    var gross = m * rate;
    var share = gross * OPERATOR_SHARE;
    if ($("opShare")) $("opShare").textContent = usd(share);
    if ($("opFormula")) {
      $("opFormula").textContent =
        tps + " tok/s x " + hours + " h x " + Math.round(busy * 100) + "% x " + DAYS +
        " days = " + millions(m) + " tokens · x " + usd(rate) + " / 1M = " + usd(gross) +
        " gross · your " + Math.round(OPERATOR_SHARE * 100) + "% = " + usd(share);
    }
  }

  /* ---------- consumer: your own bill, re-priced --------------------- */
  function consumer() {
    var mine = num("cnPrice"), volume = num("cnVolume"), here = num("cnHere");
    var today = volume * mine;
    var band = volume * here;
    var delta = today - band;
    if ($("cnDelta")) {
      $("cnDelta").textContent = (delta > 0 ? "-" : delta < 0 ? "+" : "") + usd(Math.abs(delta));
    }
    if ($("cnFormula")) {
      $("cnFormula").textContent =
        millions(volume) + " x " + usd(mine) + " / 1M = " + usd(today) + " today · " +
        "at " + usd(here) + " / 1M = " + usd(band) + " here";
    }
  }

  function recalc() { operator(); consumer(); }

  /* ---------- the band rate: read, or left to the reader ------------- */
  function loadBandRate() {
    var note = $("cnHereNote"), field = $("cnHere");
    if (!note || !field) return;
    fetch(BROKER + "/market", { credentials: "omit" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        // min_out, NEVER min_price: min_price is the cheapest INPUT price (market.go),
        // and every figure on this page is an OUT price. Reading the wrong one multiplied
        // an output volume by an input rate and overstated the saving. A broker too old
        // to send min_out simply leaves the field to the reader - falling back to
        // min_price would be the original bug wearing a fallback.
        var rows = (d && d.market) || [];
        var priced = [], free = 0, sawOut = false;
        for (var i = 0; i < rows.length; i++) {
          var p = rows[i].min_out;
          if (typeof p !== "number") continue;
          sawOut = true;
          if (p > 0) priced.push(p); else free++;
        }
        if (!sawOut && rows.length) {
          note.textContent = "this broker does not report an out-price - put in a rate yourself";
          return;
        }
        if (priced.length) {
          var cheapest = Math.min.apply(null, priced);
          field.value = String(cheapest);
          note.textContent = "cheapest on air right now";
        } else if (free) {
          field.value = "0";
          note.textContent = "every band on air is free right now";
        } else {
          note.textContent = "the band is quiet - put in a rate yourself";
        }
        recalc();
      })
      .catch(function () {
        // No invented rate. The reader's own number is the only honest fallback.
        note.textContent = "could not read the market - put in a rate yourself";
      });
  }

  function init() {
    var ids = ["opRate", "opTps", "opHours", "opBusy", "cnPrice", "cnVolume", "cnHere"];
    var any = false;
    for (var i = 0; i < ids.length; i++) {
      var el = $(ids[i]);
      if (!el) continue;
      any = true;
      el.addEventListener("input", recalc);
    }
    if (!any) return; // not the pricing page
    recalc();
    loadBandRate();
  }

  if (document.readyState !== "loading") init();
  else document.addEventListener("DOMContentLoaded", init);
})();
