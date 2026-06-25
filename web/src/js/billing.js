// Billing page glue for /billing: the value-first wallet view + Stripe Checkout
// top-up. Thin, credentialed reads over the broker behind the session cookie (no
// tokens ever touch JS); logged-out visitors are routed to /login. Charts are
// hand-rolled SVG/CSS on the design system (mono numerals, hairlines, the one red
// accent) - no chart library.
//
// Endpoints (all credentialed; CSP connect-src already allows the broker):
//   GET /billing        -> { balance, derived, credit_usd, checkout_ready, topups[] }
//   GET /balance        -> { logged_in, balance, monthly_cap, monthly_spend }   (cap meter)
//   GET /metrics/series -> { daily[], savings{ spend_usd, frontier_est,
//                            savings_est, baseline_model, reference[] } }        (savings + velocity)
//   GET /console        -> { role, events[]{model,node,cost,ts}, counters{spend_today} } (today + charges)
//   POST /billing/checkout { usd } -> { url }   (Stripe Checkout, unchanged)
// Every added panel degrades to honest-empty (hidden) if its feed is missing.
(function () {
  var BROKER = "https://broker.rogerai.fyi";

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = "include";
    return fetch(BROKER + path, opts).then(function (r) {
      return r.ok ? r.json() : null;
    }).catch(function () { return null; });
  }
  function get(path) { return api(path); }

  function text(id, v) { var el = document.getElementById(id); if (el) el.textContent = v; }
  function show(id) { var el = document.getElementById(id); if (el) el.hidden = false; }
  function hide(id) { var el = document.getElementById(id); if (el) el.hidden = true; }
  function on(id, ev, fn) { var el = document.getElementById(id); if (el) el.addEventListener(ev, fn); }

  // Money in dollars (1 credit = $1; display relabel only - ledger math unchanged).
  // Adaptive precision: >= 1c at 2dp; sub-cent costs keep significant digits so a
  // real cost never reads as $0.00.
  function cr(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    if (n === 0) return "$0.00";
    var s = n < 0 ? "-" : "";
    var a = Math.abs(n);
    if (a >= 0.01) return s + "$" + a.toFixed(2);
    // sub-cent: ~3 significant figures, plain decimal, trailing zeros stripped.
    var p = a.toPrecision(3);
    if (/e/i.test(p)) p = a.toFixed(20).replace(/0+$/, "");
    else p = p.replace(/0+$/, "").replace(/\.$/, "");
    return s + "$" + p;
  }
  function n0(v) { return typeof v === "number" && isFinite(v) ? v : 0; }
  function when(ts) {
    if (!ts) return "";
    return new Date(ts * 1000).toLocaleDateString();
  }

  function li(left, right, leftClass) {
    var el = document.createElement("li");
    var a = document.createElement("span");
    a.className = leftClass || "r-model";
    a.textContent = left;
    var b = document.createElement("span");
    b.className = "r-cost";
    b.textContent = right;
    el.appendChild(a);
    el.appendChild(b);
    return el;
  }
  function fill(listId, emptyId, rows, render) {
    var ul = document.getElementById(listId);
    if (!ul) return;
    if (!rows || !rows.length) { if (emptyId) show(emptyId); return; }
    rows.forEach(function (row) { ul.appendChild(render(row)); });
    show(listId);
  }

  function wireLogout() {
    on("logout", "click", function () {
      fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
        .then(function () { location.replace("/login.html"); });
    });
  }

  // ---- SAVINGS-VS-FRONTIER (value front and center) -----------------------
  // savings = { spend_usd, frontier_est, savings_est, baseline_model, reference[] }.
  // We only surface the panel when there is real frontier estimate to compare
  // against (honest empty: a brand-new wallet with no usage shows nothing).
  function renderSavings(sav) {
    if (!sav) return;
    var spend = n0(sav.spend_usd);
    var frontier = n0(sav.frontier_est);
    var saved = n0(sav.savings_est);
    if (frontier <= 0) return; // nothing consumed yet -> no honest claim to make
    text("saveAmt", cr(saved));
    text("saveSpend", cr(spend));
    text("saveFrontier", cr(frontier));
    // two stacked bars: what you paid (red receipt) vs the frontier estimate
    // (muted), scaled to the larger of the two so the gap is the message.
    var max = Math.max(frontier, spend, 1e-9);
    var bars = document.getElementById("saveBars");
    if (bars) {
      bars.textContent = "";
      bars.appendChild(savingsBar("RogerAI", spend, max, "paid"));
      bars.appendChild(savingsBar(sav.baseline_model || "frontier", frontier, max, "frontier"));
    }
    var pct = frontier > 0 ? Math.round((saved / frontier) * 100) : 0;
    var note = sav.reference_note ||
      "Estimate only: published list prices, not a live or contractual quote.";
    if (pct > 0) {
      text("saveNote", "About " + pct + "% less than the " +
        (sav.baseline_model || "frontier") + " list price. " + note);
    } else {
      text("saveNote", note);
    }
    show("saveBox");
  }
  function savingsBar(label, value, max, kind) {
    var row = document.createElement("div");
    row.className = "bx-save__row";
    var lab = document.createElement("span");
    lab.className = "bx-save__lab";
    lab.textContent = label;
    var track = document.createElement("span");
    track.className = "bx-save__track";
    var fillEl = document.createElement("span");
    fillEl.className = "bx-save__fill bx-save__fill--" + kind;
    var pct = max > 0 ? (value / max) * 100 : 0;
    fillEl.style.width = (pct < 1 ? 1 : pct) + "%";
    track.appendChild(fillEl);
    var val = document.createElement("span");
    val.className = "bx-save__val";
    val.textContent = cr(value);
    row.appendChild(lab);
    row.appendChild(track);
    row.appendChild(val);
    return row;
  }

  // ---- MONTHLY SPEND-CAP METER --------------------------------------------
  // cap = 0 means unlimited (no meter). Otherwise a gauge with 80%/100% states.
  function renderCap(cap, spend) {
    cap = n0(cap);
    spend = n0(spend);
    if (cap <= 0) return; // unlimited -> no gauge (honest: nothing to meter)
    var ratio = cap > 0 ? spend / cap : 0;
    var pct = Math.round(ratio * 100);
    text("capMtd", cr(spend));
    text("capLimit", cr(cap) + "/mo");
    text("capPct", pct + "%");
    var fillEl = document.getElementById("capGauge");
    if (fillEl) fillEl.style.width = Math.min(100, Math.max(0, ratio * 100)) + "%";
    var wrap = document.getElementById("capGaugeWrap");
    var state = ratio >= 1 ? "over" : (ratio >= 0.8 ? "near" : "ok");
    if (wrap) {
      wrap.classList.remove("is-near", "is-over");
      if (state === "near") wrap.classList.add("is-near");
      if (state === "over") wrap.classList.add("is-over");
    }
    var pctEl = document.getElementById("capPct");
    if (pctEl) {
      pctEl.classList.remove("is-near", "is-over");
      if (state === "near") pctEl.classList.add("is-near");
      if (state === "over") pctEl.classList.add("is-over");
    }
    var note;
    if (state === "over") {
      note = "Cap reached. Paid requests are paused until next month or until you raise the cap with `rogerai limit`.";
    } else if (state === "near") {
      note = "You are at " + pct + "% of your monthly cap. Raise or clear it any time with `rogerai limit`.";
    } else {
      note = "A budget ceiling on captured spend this calendar month. Change it from the CLI: `rogerai limit`.";
    }
    // keep the inline <code> styling readable: the note is plain text, the code
    // hint lives in the markup default, so only swap text for the alert states.
    if (state !== "ok") text("capNote", note);
    show("capBox");
  }

  // ---- SPEND VELOCITY (hand-rolled SVG sparkline) -------------------------
  // daily[] = [{ bucket:"YYYY-MM-DD", spend, ... }] oldest-first. We chart the
  // recent spend per day. Honest empty: hidden if no day carries spend.
  function renderVelocity(daily) {
    if (!daily || !daily.length) return;
    // take the most recent 30 buckets, oldest-first (series is already sorted).
    var pts = daily.slice(-30).map(function (d) {
      return { day: d.bucket, v: n0(d.spend) };
    });
    var total = pts.reduce(function (s, p) { return s + p.v; }, 0);
    if (total <= 0) return; // nobody spent anything -> nothing to plot
    var max = pts.reduce(function (m, p) { return Math.max(m, p.v); }, 0) || 1;
    var peak = pts.reduce(function (m, p) { return Math.max(m, p.v); }, 0);

    var W = 100, H = 32, n = pts.length; // viewBox units; CSS scales width
    var step = n > 1 ? W / (n - 1) : 0;
    var y = function (v) { return H - (v / max) * (H - 3) - 1.5; };
    var line = "", area = "";
    pts.forEach(function (p, i) {
      var px = (n > 1 ? i * step : W / 2).toFixed(2);
      var py = y(p.v).toFixed(2);
      line += (i === 0 ? "M" : "L") + px + " " + py + " ";
      area += (i === 0 ? "M" : "L") + px + " " + py + " ";
    });
    if (n === 1) {
      // single bucket: draw a flat tick so the chart is not empty.
      line = "M0 " + y(pts[0].v).toFixed(2) + " L" + W + " " + y(pts[0].v).toFixed(2);
    }
    area += "L" + W + " " + H + " L0 " + H + " Z";

    var ns = "http://www.w3.org/2000/svg";
    var svg = document.createElementNS(ns, "svg");
    svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    svg.setAttribute("preserveAspectRatio", "none");
    svg.setAttribute("class", "bx-spark");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", "Recent daily spend");
    var ap = document.createElementNS(ns, "path");
    ap.setAttribute("d", area);
    ap.setAttribute("class", "bx-spark__area");
    var lp = document.createElementNS(ns, "path");
    lp.setAttribute("d", line);
    lp.setAttribute("class", "bx-spark__line");
    svg.appendChild(ap);
    svg.appendChild(lp);
    // a red dot on the last point - the live edge of the trace.
    if (n >= 1) {
      var last = pts[n - 1];
      var dot = document.createElementNS(ns, "circle");
      dot.setAttribute("cx", (n > 1 ? (n - 1) * step : W / 2).toFixed(2));
      dot.setAttribute("cy", y(last.v).toFixed(2));
      dot.setAttribute("r", "1.8");
      dot.setAttribute("class", "bx-spark__dot");
      svg.appendChild(dot);
    }
    var chart = document.getElementById("velChart");
    if (chart) { chart.textContent = ""; chart.appendChild(svg); }
    text("velSpan", n + (n === 1 ? " day" : " days") + " of activity");
    text("velPeak", "peak " + cr(peak) + "/day");
    show("velBox");
  }

  // ---- RECENT CHARGES (money out) -----------------------------------------
  // /console events: a consumer sees cost-per-row. Show only rows with a real
  // (non-zero) cost so free traffic does not crowd out the receipt list.
  function renderCharges(d) {
    var events = (d && d.events) || [];
    var paid = events.filter(function (e) { return n0(e.cost) > 0; }).slice(0, 8);
    fill("charges", "chargesEmpty", paid, function (e) {
      var label = (e.model || "request");
      if (e.node) label += " - " + e.node;
      return li(when(e.ts) + " - " + label, cr(n0(e.cost)));
    });
  }

  // Strip a trailing slash AND a ".html" suffix so the branch matches whether the
  // page is served at the clean path (/billing) or the static file (/billing.html) -
  // the static host serves /billing.html, so matching only "/billing" left it blank.
  var path = location.pathname.replace(/\/$/, "").replace(/\.html$/, "");
  if (path.endsWith("/billing")) {
    get("/billing").then(function (d) {
      if (!d) { location.replace("/login.html"); return; }
      text("balance", cr(d.balance));
      text("derived", cr(d.derived));
      if (d.checkout_ready) {
        text("topupNote", "Top up below, or from the CLI: rogerai topup.");
        show("topupBox");
        wireTopup();
      } else {
        text("topupNote", "Top up from the CLI: rogerai topup.");
        show("topupDisabled");
      }
      fill("topups", "topupsEmpty", d.topups, function (t) {
        return li(when(t.ts) + " - top-up", cr(t.amount));
      });
      show("card");
      wireLogout();

      // Secondary feeds: each is best-effort and self-hides on absence, so the
      // core balance + top-up flow never blocks on them.
      get("/metrics/series?days=30").then(function (s) {
        if (!s) return;
        renderSavings(s.savings);
        renderVelocity(s.daily);
      });
      get("/balance").then(function (bal) {
        if (bal && bal.logged_in) renderCap(bal.monthly_cap, bal.monthly_spend);
      });
      get("/console?limit=40").then(function (c) {
        if (c) {
          if (c.counters && typeof c.counters.spend_today === "number") {
            text("spendToday", cr(c.counters.spend_today));
          } else {
            text("spendToday", cr(0));
          }
          renderCharges(c);
        } else {
          text("spendToday", cr(0));
        }
      });
    });

    // Top-up control: a chosen preset OR a custom amount -> Stripe Checkout.
    function wireTopup() {
      var presets = document.getElementById("topupPresets");
      var custom = document.getElementById("topupCustom");
      var btn = document.getElementById("topup");

      // Reflect the chosen amount on the action button so the commitment is
      // explicit before the redirect ("Add $20" reads safer than "Add money").
      function reflect() {
        var usd = chosenUsd();
        var valEl = document.getElementById("topupValue");
        if (isFinite(usd) && usd >= 1) {
          usd = Math.round(usd * 100) / 100;
          if (btn) btn.textContent = "Add " + cr(usd);
          if (valEl) {
            valEl.textContent = "Adds " + cr(usd) + " to your wallet balance.";
            valEl.hidden = false;
          }
        } else {
          if (btn) btn.textContent = "Add money";
          if (valEl) valEl.hidden = true;
        }
      }

      // selecting a preset clears the custom field; typing a custom clears presets.
      if (presets) {
        presets.addEventListener("click", function (ev) {
          var b = ev.target.closest("button[data-usd]");
          if (!b) return;
          var btns = presets.querySelectorAll(".amount");
          for (var i = 0; i < btns.length; i++) btns[i].classList.remove("is-active");
          b.classList.add("is-active");
          if (custom) custom.value = "";
          reflect();
        });
      }
      if (custom) {
        custom.addEventListener("input", function () {
          if (custom.value) {
            var btns = presets ? presets.querySelectorAll(".amount") : [];
            for (var i = 0; i < btns.length; i++) btns[i].classList.remove("is-active");
          }
          reflect();
        });
      }

      function chosenUsd() {
        if (custom && custom.value) {
          var v = parseFloat(custom.value);
          return isFinite(v) ? v : NaN;
        }
        var active = presets && presets.querySelector(".amount.is-active");
        return active ? parseFloat(active.getAttribute("data-usd")) : NaN;
      }

      reflect(); // seed the button with the default-active preset

      on("topup", "click", function () {
        var usd = chosenUsd();
        if (!isFinite(usd) || usd < 1) { text("topupMsg", " enter an amount of $1 or more"); return; }
        usd = Math.round(usd * 100) / 100;
        if (btn) btn.disabled = true;
        text("topupMsg", " redirecting to Stripe...");
        api("/billing/checkout", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ usd: usd })
        }).then(function (r) {
          if (r && r.url) { window.location = r.url; return; }
          if (btn) btn.disabled = false;
          text("topupMsg", " could not start checkout");
        }).catch(function () {
          if (btn) btn.disabled = false;
          text("topupMsg", " could not start checkout");
        });
      });
    }
  }
})();
