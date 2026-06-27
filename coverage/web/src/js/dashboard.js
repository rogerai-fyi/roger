// Dashboard - the consumer/provider hero view. Driven entirely by the broker's
// time-series feed:
//
//   GET /metrics/series?days=30 -> {
//     period_days, is_consumer, is_provider,
//     daily:  [ { bucket:"2026-06-01", requests, tokens_in, tokens_out,
//                 spend, earned, frontier_est, savings_est, models:[...] } ],
//     hourly: [ same shape, "2026-06-01T15" buckets, last 48h ],
//     savings:{ baseline_model, spend_usd, frontier_est, savings_est,
//               reference:[ { model, in_per_1m, out_per_1m } ], reference_note }
//   }
//
// Login gate matches the other account pages: /account confirms the session, a
// 401 from /metrics/series sends us to login. No tokens touch JS; the broker
// holds the session cookie (credentialed CORS). Charts are hand-rolled inline
// SVG + CSS bars (no chart library), on the design system: mono numerals,
// hairlines, one red glint (savings / earned). Honest-empty everywhere.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";
  var DAYS = 30;

  // ---- tiny DOM helpers (mirror metrics.js / account.js) ----
  function $(id) { return document.getElementById(id); }
  function show(id) { var el = $(id); if (el) el.hidden = false; }
  function hide(id) { var el = $(id); if (el) el.hidden = true; }
  function text(id, v) { var el = $(id); if (el) el.textContent = v; }
  function n0(v) { return typeof v === "number" && isFinite(v) ? v : 0; }

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
  function num(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    return Math.round(n).toLocaleString("en-US");
  }
  // Compact token count: 12.3k / 4.5M, for tight chart labels.
  function kfmt(n) {
    n = n0(n);
    if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1).replace(/\.0$/, "") + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1).replace(/\.0$/, "") + "k";
    return String(Math.round(n));
  }
  // "2026-06-01" -> "Jun 1".
  function dayLabel(b) {
    var m = /^(\d{4})-(\d{2})-(\d{2})/.exec(b || "");
    if (!m) return b || "";
    var d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]));
    return d.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
  }

  var SVGNS = "http://www.w3.org/2000/svg";
  function svgEl(name, attrs) {
    var el = document.createElementNS(SVGNS, name);
    if (attrs) for (var k in attrs) if (attrs.hasOwnProperty(k)) el.setAttribute(k, attrs[k]);
    return el;
  }

  // ---------------------------------------------------------------------------
  // SVG line/area chart. Plots one or two series over the daily buckets on a
  // shared x-axis. series = [{ key, color, fill }]; reads point[key] per day.
  // Pure hand-rolled paths; viewBox scales responsively, no library, and it is
  // static (no animation) so it is reduced-motion + narrow safe.
  // ---------------------------------------------------------------------------
  function lineChart(daily, series) {
    var W = 720, H = 200, padL = 8, padR = 8, padT = 14, padB = 8;
    var iw = W - padL - padR, ih = H - padT - padB;
    var n = daily.length;
    var max = 0;
    daily.forEach(function (d) {
      series.forEach(function (s) { max = Math.max(max, n0(d[s.key])); });
    });
    if (max <= 0) max = 1;
    var x = function (i) { return n <= 1 ? padL + iw / 2 : padL + (i / (n - 1)) * iw; };
    var y = function (v) { return padT + ih - (n0(v) / max) * ih; };

    var svg = svgEl("svg", {
      viewBox: "0 0 " + W + " " + H, class: "ds-line",
      preserveAspectRatio: "none", role: "img"
    });

    // baseline + two faint gridlines
    [0, 0.5, 1].forEach(function (f) {
      var yy = padT + ih - f * ih;
      svg.appendChild(svgEl("line", {
        x1: padL, x2: W - padR, y1: yy, y2: yy,
        class: f === 0 ? "ds-axis" : "ds-grid"
      }));
    });

    series.forEach(function (s) {
      var dPath = "";
      daily.forEach(function (d, i) {
        dPath += (i === 0 ? "M" : "L") + x(i).toFixed(1) + " " + y(d[s.key]).toFixed(1) + " ";
      });
      if (s.fill && n > 1) {
        var aPath = dPath + "L" + x(n - 1).toFixed(1) + " " + (padT + ih) +
          " L" + x(0).toFixed(1) + " " + (padT + ih) + " Z";
        svg.appendChild(svgEl("path", { d: aPath, class: "ds-area", fill: s.fill }));
      }
      svg.appendChild(svgEl("path", { d: dPath, class: "ds-stroke", stroke: s.color }));
      svg.appendChild(svgEl("circle", {
        cx: x(n - 1).toFixed(1), cy: y(daily[n - 1][s.key]).toFixed(1),
        r: 2.5, class: "ds-dot", fill: s.color
      }));
    });
    return svg;
  }

  // x-axis labels under a chart: first / middle / last bucket (readable at 30
  // points). HTML, not SVG, so they stay crisp (not stretched by the viewBox).
  function axisLabels(daily) {
    var wrap = document.createElement("div");
    wrap.className = "ds-xaxis";
    var n = daily.length;
    var picks = n <= 1 ? [0] : [0, Math.floor((n - 1) / 2), n - 1];
    var seen = {};
    picks.forEach(function (i) {
      if (seen[i]) return; seen[i] = 1;
      var s = document.createElement("span");
      s.textContent = dayLabel(daily[i].bucket);
      wrap.appendChild(s);
    });
    return wrap;
  }

  // ---------------------------------------------------------------------------
  // Per-day savings column chart (red = savings, the one glint). Bar heights
  // scale to the max savings day; hover title carries the exact figure.
  // ---------------------------------------------------------------------------
  function savingsBars(daily) {
    var max = daily.reduce(function (m, d) { return Math.max(m, n0(d.savings_est)); }, 0);
    var wrap = document.createElement("div");
    wrap.className = "ds-cols";
    daily.forEach(function (d) {
      var col = document.createElement("span");
      col.className = "ds-cols__col";
      col.title = dayLabel(d.bucket) + ": " + cr(n0(d.savings_est)) + " saved";
      var bar = document.createElement("span");
      bar.className = "ds-cols__bar";
      var h = max > 0 ? (n0(d.savings_est) / max) * 100 : 0;
      bar.style.height = (h < 0 ? 0 : h) + "%";
      col.appendChild(bar);
      wrap.appendChild(col);
    });
    return wrap;
  }

  // ---------------------------------------------------------------------------
  // Per-model breakdown: roll the daily models[] up over the window, render
  // horizontal token bars + a per-model spend/earned value.
  // ---------------------------------------------------------------------------
  function rollupModels(daily) {
    var by = {};
    daily.forEach(function (d) {
      (d.models || []).forEach(function (m) {
        var k = m.model || "unknown";
        var r = by[k] || (by[k] = { model: k, requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 });
        r.requests += n0(m.requests);
        r.tokens_in += n0(m.tokens_in);
        r.tokens_out += n0(m.tokens_out);
        r.spend += n0(m.spend);
        r.earned += n0(m.earned);
      });
    });
    var arr = [];
    for (var k in by) if (by.hasOwnProperty(k)) {
      by[k].tokens = by[k].tokens_in + by[k].tokens_out;
      arr.push(by[k]);
    }
    arr.sort(function (a, b) { return b.tokens - a.tokens; });
    return arr;
  }

  function modelChart(models, isProvider) {
    var max = models.reduce(function (m, r) { return Math.max(m, r.tokens); }, 0);
    var ul = document.createElement("ul");
    ul.className = "ds-mbars";
    models.forEach(function (r) {
      var li = document.createElement("li");
      li.className = "ds-mbars__row";
      var lab = document.createElement("span");
      lab.className = "ds-mbars__label";
      lab.textContent = r.model;
      lab.title = r.model;
      var track = document.createElement("span");
      track.className = "ds-mbars__track";
      var fill = document.createElement("span");
      fill.className = "ds-mbars__fill";
      fill.style.width = (max > 0 ? (r.tokens / max) * 100 : 0) + "%";
      track.appendChild(fill);
      var val = document.createElement("span");
      val.className = "ds-mbars__val";
      val.textContent = kfmt(r.tokens) + " tok / " + cr(isProvider ? r.earned : r.spend);
      li.appendChild(lab); li.appendChild(track); li.appendChild(val);
      ul.appendChild(li);
    });
    return ul;
  }

  // ---------------------------------------------------------------------------
  // render the whole dashboard from one /metrics/series payload.
  // ---------------------------------------------------------------------------
  function render(d) {
    var daily = (d && d.daily) || [];
    var sv = (d && d.savings) || {};
    var isConsumer = !!(d && d.is_consumer);
    var isProvider = !!(d && d.is_provider);

    var tot = { requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 };
    daily.forEach(function (p) {
      tot.requests += n0(p.requests);
      tot.tokens_in += n0(p.tokens_in);
      tot.tokens_out += n0(p.tokens_out);
      tot.spend += n0(p.spend);
      tot.earned += n0(p.earned);
    });
    var totalTokens = tot.tokens_in + tot.tokens_out;
    var hasData = daily.length > 0 && (totalTokens > 0 || tot.requests > 0);

    if (!hasData) { show("dashEmpty"); return; }

    // ---- HEADLINE: savings vs frontier (consumer); else earned banner. ----
    if (isConsumer && n0(sv.savings_est) > 0) {
      text("heroAmt", cr(n0(sv.savings_est)));
      text("heroBase", sv.baseline_model || "gpt-4o");
      text("heroSpend", cr(n0(sv.spend_usd)));
      text("heroFrontier", cr(n0(sv.frontier_est)));
      show("hero");
      $("savingsChart").appendChild(savingsBars(daily));
      $("savingsChart").appendChild(axisLabels(daily));
      if (sv.reference_note) text("savingsNote", sv.reference_note);
      show("savingsWrap");
    } else if (isProvider && tot.earned > 0) {
      text("heroAmtEarn", cr(tot.earned));
      show("heroEarn");
    }

    // ---- STAT TILES (period totals). ----
    text("statReq", num(tot.requests));
    text("statTok", num(totalTokens));
    if (isConsumer) { text("statSpend", cr(tot.spend)); show("tileSpend"); }
    if (isProvider) { text("statEarn", cr(tot.earned)); show("tileEarn"); }
    show("stats");

    // ---- TIME-SERIES CHARTS ----
    $("tokChart").appendChild(lineChart(daily, [
      { key: "tokens_out", color: "var(--ink-900)", fill: "var(--paper-3)" }
    ]));
    $("tokChart").appendChild(axisLabels(daily));
    show("tokWrap");

    $("reqChart").appendChild(lineChart(daily, [
      { key: "requests", color: "var(--ink-500)", fill: "var(--paper-2)" }
    ]));
    $("reqChart").appendChild(axisLabels(daily));
    show("reqWrap");

    if (isConsumer || isProvider) {
      var moneySeries = [];
      var legend = "";
      if (isConsumer) {
        moneySeries.push({ key: "spend", color: "var(--ink-900)", fill: "var(--paper-3)" });
        legend += '<span class="ds-legend__sw ds-legend__sw--ink"></span>spend';
      }
      if (isProvider) {
        moneySeries.push({ key: "earned", color: "var(--live)" });
        legend += '<span class="ds-legend__sw ds-legend__sw--live"></span>earned';
      }
      var mc = $("moneyChart");
      mc.appendChild(lineChart(daily, moneySeries));
      mc.appendChild(axisLabels(daily));
      var lg = $("moneyLegend");
      if (lg) lg.innerHTML = legend;
      show("moneyWrap");
    }

    // ---- PER-MODEL BREAKDOWN ----
    var models = rollupModels(daily);
    if (models.length) {
      $("modelBars").appendChild(modelChart(models, isProvider));
      show("modelWrap");
    }

    // provider CTA only when they are not yet earning anything.
    if (!isProvider) show("earnCta");
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

  // ---- boot: confirm session via /account, then load the series feed. ----
  fetch(BROKER + "/account", { credentials: "include" })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (acct) {
      if (!acct || !(acct.github_login || acct.github_id)) {
        location.replace("/login.html");
        return;
      }
      text("who", "@" + (acct.github_login || "you"));
      show("card");
      wireLogout();
      return fetch(BROKER + "/metrics/series?days=" + DAYS, { credentials: "include" })
        .then(function (r) {
          if (r.status === 401 || r.status === 403) { location.replace("/login.html"); return null; }
          if (!r.ok) { hide("dashLoading"); show("dashError"); return null; }
          return r.json();
        })
        .then(function (d) {
          if (!d) return;
          hide("dashLoading");
          render(d);
        })
        .catch(function () { hide("dashLoading"); show("dashError"); });
    })
    .catch(function () { location.replace("/login.html"); });
})();
