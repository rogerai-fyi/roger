// Per-model METRICS for the Metrics page (usage.html): a PROVIDER view (what you
// host + earn) and a YOUR USAGE view (what you consume + spend). Thin, credentialed
// reads over the broker behind the session cookie - no tokens ever touch JS, matching
// js/account.js. Charts are hand-rolled CSS bars (no chart library), on the design
// system (mono numerals, hairlines, the one red accent for the paid portion).
//
// Endpoints (credentialed; CSP connect-src already allows the broker):
//   GET /metrics/provider?days=N -> { period_days, totals, models:[ { model, node_id,
//        requests, tokens_in, tokens_out, free_requests, paid_requests,
//        free_tokens, paid_tokens, earnings_usd } ] }
//   GET /metrics/usage?days=N    -> { period_days, totals, models:[ { model, requests,
//        tokens_in, tokens_out, free_requests, paid_requests, spend_usd } ] }
// days = 7 | 30 | "all". Degrades gracefully if an endpoint is not up yet (404/500):
// the section shows its error state, the rest of the page still works.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";

  // ---- tiny DOM helpers (mirrors account.js) ----
  function $(id) { return document.getElementById(id); }
  function show(id) { var el = $(id); if (el) el.hidden = false; }
  function hide(id) { var el = $(id); if (el) el.hidden = true; }
  function text(id, v) { var el = $(id); if (el) el.textContent = v; }

  // Money in dollars (1 credit = $1). Adaptive precision so a real cost never
  // reads as $0.00 (same rule as account.js cr()).
  function cr(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    if (n === 0) return "$0.00";
    var s = n < 0 ? "-" : "";
    var a = Math.abs(n);
    if (a >= 0.01) return s + "$" + a.toFixed(2);
    var p = a.toPrecision(3);
    if (/e/i.test(p)) p = a.toFixed(20).replace(/0+$/, "");
    else p = p.replace(/0+$/, "").replace(/\.$/, "");
    return s + "$" + p;
  }
  // Compact integer with thousands separators (tabular-nums in CSS keeps columns).
  function num(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    return Math.round(n).toLocaleString("en-US");
  }
  function n0(v) { return typeof v === "number" && isFinite(v) ? v : 0; }

  // Build one horizontal bar row: label + a stacked paid/free track scaled to max,
  // plus a trailing mono value. portion = { paid, free } in the charted unit.
  function barRow(label, value, valueText, max, paid, free) {
    var li = document.createElement("li");
    li.className = "mx-bars__row";

    var lab = document.createElement("span");
    lab.className = "mx-bars__label";
    lab.textContent = label;
    lab.title = label;
    li.appendChild(lab);

    var track = document.createElement("span");
    track.className = "mx-bars__track";
    var pct = max > 0 ? (value / max) * 100 : 0;
    // width of the whole bar relative to the biggest model
    var fill = document.createElement("span");
    fill.className = "mx-bars__fill";
    fill.style.width = (pct < 0 ? 0 : pct) + "%";
    // within the bar, shade the paid portion (red) vs free (muted)
    var tot = paid + free;
    var paidPct = tot > 0 ? (paid / tot) * 100 : 0;
    var segPaid = document.createElement("span");
    segPaid.className = "mx-bars__seg mx-bars__seg--paid";
    segPaid.style.width = paidPct + "%";
    var segFree = document.createElement("span");
    segFree.className = "mx-bars__seg mx-bars__seg--free";
    segFree.style.width = (100 - paidPct) + "%";
    fill.appendChild(segPaid);
    fill.appendChild(segFree);
    track.appendChild(fill);
    li.appendChild(track);

    var val = document.createElement("span");
    val.className = "mx-bars__val";
    val.textContent = valueText;
    li.appendChild(val);
    return li;
  }

  // Free/paid as a compact "F / P" cell.
  function fpCell(free, paid) {
    var td = document.createElement("td");
    td.className = "num mx-fp";
    var f = document.createElement("span");
    f.className = "mx-fp__free";
    f.textContent = num(free);
    var sep = document.createElement("span");
    sep.className = "mx-fp__sep";
    sep.textContent = " / ";
    var p = document.createElement("span");
    p.className = "mx-fp__paid";
    p.textContent = num(paid);
    td.appendChild(f); td.appendChild(sep); td.appendChild(p);
    return td;
  }
  function cell(value, cls) {
    var td = document.createElement("td");
    if (cls) td.className = cls;
    td.textContent = value;
    return td;
  }

  // ---- state machine per section: loading -> (empty | error | data) ----
  function setState(prefix, state) {
    // prefix is "prov" or "use"
    hide(prefix + "Loading"); hide(prefix + "Empty"); hide(prefix + "Error");
    if (state === "loading") { show(prefix + "Loading"); return; }
    if (state === "empty") { show(prefix + "Empty"); return; }
    if (state === "error") { show(prefix + "Error"); return; }
  }
  function clearData(prefix) {
    hide(prefix + "Totals"); hide(prefix + "Chart"); hide(prefix + "TableWrap");
    var rows = $(prefix + "Rows"); if (rows) rows.textContent = "";
    var bars = $(prefix + "Bars"); if (bars) bars.textContent = "";
  }

  // ---- fetch one metrics endpoint ----
  // resolves to { ok:true, data } | { ok:false } (network/404/500 -> not ok).
  function fetchMetrics(kind, days) {
    var url = BROKER + "/metrics/" + kind + "?days=" + encodeURIComponent(days);
    return fetch(url, { credentials: "include" }).then(function (r) {
      if (r.status === 401 || r.status === 403) return { ok: false, auth: false };
      if (!r.ok) return { ok: false };
      return r.json().then(function (d) { return { ok: true, data: d }; })
        .catch(function () { return { ok: false }; });
    }).catch(function () { return { ok: false }; });
  }

  // ---- render PROVIDER section ----
  function renderProvider(d) {
    var models = (d && d.models) || [];
    var t = (d && d.totals) || {};
    clearData("prov");
    if (!models.length) { setState("prov", "empty"); return; }

    text("provReq", num(n0(t.requests)));
    text("provTokOut", num(n0(t.tokens_out)));
    text("provEarn", cr(n0(t.earnings_usd)));
    show("provTotals");

    // chart: tokens_out per model, free/paid by token split.
    var sorted = models.slice().sort(function (a, b) { return n0(b.tokens_out) - n0(a.tokens_out); });
    var max = sorted.reduce(function (m, r) { return Math.max(m, n0(r.tokens_out)); }, 0);
    var barsEl = $("provBars");
    sorted.forEach(function (r) {
      barsEl.appendChild(barRow(
        r.model || "(unknown)", n0(r.tokens_out), num(n0(r.tokens_out)),
        max, n0(r.paid_tokens), n0(r.free_tokens)
      ));
    });
    show("provChart");

    // table
    var body = $("provRows");
    sorted.forEach(function (r) {
      var tr = document.createElement("tr");
      tr.appendChild(cell(r.model || "(unknown)", "mx-model"));
      tr.appendChild(cell(num(n0(r.requests)), "num"));
      tr.appendChild(cell(num(n0(r.tokens_out)), "num"));
      tr.appendChild(fpCell(n0(r.free_requests), n0(r.paid_requests)));
      tr.appendChild(cell(cr(n0(r.earnings_usd)), "num mx-money"));
      body.appendChild(tr);
    });
    show("provTableWrap");
    setState("prov", "data");
  }

  // ---- render USAGE section ----
  function renderUsage(d) {
    var models = (d && d.models) || [];
    var t = (d && d.totals) || {};
    clearData("use");
    if (!models.length) { setState("use", "empty"); return; }

    var totTok = n0(t.tokens_in) + n0(t.tokens_out);
    text("useReq", num(n0(t.requests)));
    text("useTok", num(totTok));
    text("useSpend", cr(n0(t.spend_usd)));
    show("useTotals");

    // tokens per model = in + out; free/paid split is by request count (usage
    // shape has no per-token free/paid), used to shade the bar proportionally.
    var withTok = models.map(function (r) {
      return { r: r, tok: n0(r.tokens_in) + n0(r.tokens_out) };
    });
    var sorted = withTok.slice().sort(function (a, b) { return b.tok - a.tok; });
    var max = sorted.reduce(function (m, x) { return Math.max(m, x.tok); }, 0);
    var barsEl = $("useBars");
    sorted.forEach(function (x) {
      barsEl.appendChild(barRow(
        x.r.model || "(unknown)", x.tok, num(x.tok),
        max, n0(x.r.paid_requests), n0(x.r.free_requests)
      ));
    });
    show("useChart");

    var body = $("useRows");
    sorted.forEach(function (x) {
      var r = x.r;
      var tr = document.createElement("tr");
      tr.appendChild(cell(r.model || "(unknown)", "mx-model"));
      tr.appendChild(cell(num(n0(r.requests)), "num"));
      tr.appendChild(cell(num(x.tok), "num"));
      tr.appendChild(fpCell(n0(r.free_requests), n0(r.paid_requests)));
      tr.appendChild(cell(cr(n0(r.spend_usd)), "num mx-money"));
      body.appendChild(tr);
    });
    show("useTableWrap");
    setState("use", "data");
  }

  // ---- load both views for a given range ----
  function load(days) {
    setState("prov", "loading"); clearData("prov");
    setState("use", "loading"); clearData("use");

    fetchMetrics("provider", days).then(function (res) {
      if (res.ok) renderProvider(res.data);
      else setState("prov", "error");
    });
    fetchMetrics("usage", days).then(function (res) {
      if (res.ok) renderUsage(res.data);
      else setState("use", "error");
    });
  }

  function wireRange() {
    var opts = document.querySelectorAll(".mx-range__opt");
    for (var i = 0; i < opts.length; i++) {
      opts[i].addEventListener("click", function (ev) {
        var btn = ev.currentTarget;
        for (var j = 0; j < opts.length; j++) opts[j].classList.remove("is-active");
        btn.classList.add("is-active");
        load(btn.getAttribute("data-days") || "30");
      });
    }
  }

  function wireLogout() {
    var el = $("logout");
    if (!el) return;
    el.addEventListener("click", function () {
      fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
        .then(function () { location.replace("/login.html"); })
        .catch(function () { location.replace("/login.html"); });
    });
  }

  // ---- boot: confirm session, then load. Logged-out -> the gate card. ----
  fetch(BROKER + "/account", { credentials: "include" })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (acct) {
      if (!acct || !(acct.github_login || acct.github_id)) {
        show("gate");
        return;
      }
      show("card");
      wireRange();
      wireLogout();
      load("30");
    })
    .catch(function () {
      // offline / broker down: show the logged-out gate rather than a blank page.
      show("gate");
    });
})();
