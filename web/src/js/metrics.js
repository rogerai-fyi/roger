// TIME-AWARE, interactive METRICS for the Metrics page (usage.html). One credentialed
// read of the broker's time-series feed drives BOTH a PROVIDER view (what you host +
// earn over time) and a YOUR USAGE view (what you consume + spend over time). No tokens
// ever touch JS - the session cookie carries auth, matching js/account.js.
//
// Endpoint (credentialed; CSP connect-src already allows the broker):
//   GET /metrics/series?days=N
//     -> { period_days, is_consumer, is_provider,
//          daily:  [ { bucket "YYYY-MM-DD", requests, tokens_in, tokens_out,
//                      spend, earned, frontier_est, savings_est,
//                      models:[ { model, requests, tokens_in, tokens_out, spend, earned } ] } ],
//          hourly: [ ... same shape, last 48h, UTC hour buckets ],
//          savings: { baseline_model, spend_usd, frontier_est, savings_est, reference[] } }
//     days clamps to [1, 366]; we ask for 7 | 30 | 90. Buckets arrive oldest-first.
//
// is_consumer / is_provider gate which sections render. A pure consumer sees only
// "Your usage"; a pure provider only "Provider metrics"; a both-sider sees both.
//
// Charts are hand-rolled inline SVG (NO chart library): monochrome + the one red accent,
// tabular-nums readouts, reduced-motion safe, and they reflow for narrow screens. Each
// time-series has a hover/focus crosshair with an exact-bucket readout (keyboard: arrow
// keys move the cursor). Honest empty: real buckets only, never a fabricated trend.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";
  var SVGNS = "http://www.w3.org/2000/svg";

  // ---- tiny DOM helpers (mirrors account.js) ----
  function $(id) { return document.getElementById(id); }
  function show(id) { var el = $(id); if (el) el.hidden = false; }
  function hide(id) { var el = $(id); if (el) el.hidden = true; }
  function text(id, v) { var el = $(id); if (el) el.textContent = v; }

  // Money in dollars (1 credit = $1). Adaptive precision so a real cost never reads as
  // $0.00 (same rule as account.js cr()).
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

  // "2026-06-21" -> "Jun 21" (UTC, no time-of-day, locale month). Falls back to the raw
  // bucket string if it is not a plain day key.
  function dayLabel(bucket) {
    var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(bucket || "");
    if (!m) return bucket || "";
    var d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]));
    return d.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
  }

  // ---- SVG element helper ----
  function svgEl(name, attrs) {
    var el = document.createElementNS(SVGNS, name);
    if (attrs) for (var k in attrs) if (attrs.hasOwnProperty(k)) el.setAttribute(k, attrs[k]);
    return el;
  }

  // ---- per-model snapshot bar (kept from the old view; reused for the breakdown) ----
  // One horizontal bar: label + a single-tone fill scaled to max + a trailing mono value.
  function barRow(label, value, valueText, max) {
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
    var fill = document.createElement("span");
    fill.className = "mx-bars__fill";
    fill.style.width = (pct < 0 ? 0 : pct) + "%";
    track.appendChild(fill);
    li.appendChild(track);

    var val = document.createElement("span");
    val.className = "mx-bars__val";
    val.textContent = valueText;
    li.appendChild(val);
    return li;
  }

  function cell(value, cls) {
    var td = document.createElement("td");
    if (cls) td.className = cls;
    td.textContent = value;
    return td;
  }

  // ============================================================================
  //  Hand-rolled interactive time-series chart.
  //  spec = {
  //    points: [{ bucket, label, series:{ key:value, ... } }],
  //    series: [{ key, cls, label }],   // draw order (later = on top); cls -> stroke/fill
  //    stack: bool,                     // stacked area (tokens in/out) vs single line
  //    fmt: fn(value) -> string,        // value formatter for the readout
  //    onread: fn(html|""),             // called with the readout for the focused bucket
  //  }
  //  Returns the <svg>. Crosshair follows pointer + arrow keys; readout via onread.
  // ============================================================================
  function buildTimeSeries(spec) {
    var pts = spec.points;
    var W = 640, H = 168;                       // viewBox; scales to container width
    var padL = 8, padR = 8, padT = 12, padB = 22;
    var plotW = W - padL - padR, plotH = H - padT - padB;
    var n = pts.length;

    // y-domain: max over the (possibly stacked) total of all series in a bucket.
    var maxY = 0;
    for (var i = 0; i < n; i++) {
      var tot = 0, single = 0;
      for (var s = 0; s < spec.series.length; s++) {
        var v = n0(pts[i].series[spec.series[s].key]);
        tot += v;
        if (v > single) single = v;
      }
      var cand = spec.stack ? tot : single;
      if (cand > maxY) maxY = cand;
    }
    if (maxY <= 0) maxY = 1; // flat-zero range still draws a baseline, not a divide-by-0.

    function xAt(i) { return n <= 1 ? padL + plotW / 2 : padL + (i / (n - 1)) * plotW; }
    function yAt(v) { return padT + plotH - (v / maxY) * plotH; }

    var svg = svgEl("svg", {
      "class": "mx-ts__svg", viewBox: "0 0 " + W + " " + H,
      preserveAspectRatio: "none", role: "img",
      tabindex: "0", "aria-label": spec.aria || "Time series"
    });

    // baseline
    svg.appendChild(svgEl("line", {
      "class": "mx-ts__axis", x1: padL, y1: padT + plotH, x2: padL + plotW, y2: padT + plotH
    }));

    // Build cumulative offsets per bucket for stacked areas.
    function pointSet(key, stackBelowKeys) {
      var out = [];
      for (var i = 0; i < n; i++) {
        var base = 0;
        if (spec.stack && stackBelowKeys) {
          for (var b = 0; b < stackBelowKeys.length; b++) base += n0(pts[i].series[stackBelowKeys[b]]);
        }
        var v = n0(pts[i].series[key]);
        out.push({ x: xAt(i), yTop: yAt(base + v), yBase: yAt(base) });
      }
      return out;
    }

    var belowKeys = [];
    for (var si = 0; si < spec.series.length; si++) {
      var ser = spec.series[si];
      var ps = pointSet(ser.key, belowKeys.slice());

      if (spec.stack) {
        // filled area between this series' running total and the one below it.
        var dArea = "";
        for (var a = 0; a < ps.length; a++) dArea += (a ? "L" : "M") + ps[a].x + " " + ps[a].yTop + " ";
        for (var z = ps.length - 1; z >= 0; z--) dArea += "L" + ps[z].x + " " + ps[z].yBase + " ";
        dArea += "Z";
        svg.appendChild(svgEl("path", { "class": "mx-ts__area mx-ts__area--" + ser.cls, d: dArea }));
        belowKeys.push(ser.key);
      } else {
        // area under a single line for body, plus the line on top.
        var dA = "";
        for (var a2 = 0; a2 < ps.length; a2++) dA += (a2 ? "L" : "M") + ps[a2].x + " " + ps[a2].yTop + " ";
        if (ps.length) dA += "L" + ps[ps.length - 1].x + " " + (padT + plotH) + " L" + ps[0].x + " " + (padT + plotH) + " Z";
        svg.appendChild(svgEl("path", { "class": "mx-ts__area mx-ts__area--" + ser.cls, d: dA }));
        var dL = "";
        for (var l = 0; l < ps.length; l++) dL += (l ? "L" : "M") + ps[l].x + " " + ps[l].yTop + " ";
        svg.appendChild(svgEl("path", { "class": "mx-ts__line mx-ts__line--" + ser.cls, d: dL }));
        // a dot at each bucket so a single-point series is still visible.
        for (var d = 0; d < ps.length; d++) {
          svg.appendChild(svgEl("circle", { "class": "mx-ts__dot mx-ts__dot--" + ser.cls, cx: ps[d].x, cy: ps[d].yTop, r: n <= 14 ? 2.2 : 1.6 }));
        }
      }
    }

    // ---- crosshair + interaction ----
    var cursor = svgEl("line", { "class": "mx-ts__cursor", x1: 0, y1: padT, x2: 0, y2: padT + plotH, visibility: "hidden" });
    var focusDot = svgEl("circle", { "class": "mx-ts__focus", r: 3, visibility: "hidden" });
    svg.appendChild(cursor);
    svg.appendChild(focusDot);

    var active = -1;
    function readoutFor(i) {
      var p = pts[i];
      var rows = "";
      for (var s = 0; s < spec.series.length; s++) {
        var ser = spec.series[s];
        var lbl = ser.label ? ser.label + " " : "";
        rows += "<b class='mx-ts__rv mx-ts__rv--" + ser.cls + "'>" + lbl + spec.fmt(n0(p.series[ser.key])) + "</b>";
      }
      return "<span class='mx-ts__rk'>" + p.label + "</span>" + rows;
    }
    function focus(i) {
      if (i < 0 || i >= n) { blur(); return; }
      active = i;
      var x = xAt(i);
      cursor.setAttribute("x1", x); cursor.setAttribute("x2", x);
      cursor.setAttribute("visibility", "visible");
      // focus dot tracks the topmost series at that bucket.
      var topKey = spec.series[spec.series.length - 1].key;
      var topV = 0;
      if (spec.stack) { for (var s = 0; s < spec.series.length; s++) topV += n0(pts[i].series[spec.series[s].key]); }
      else topV = n0(pts[i].series[topKey]);
      focusDot.setAttribute("cx", x); focusDot.setAttribute("cy", yAt(topV));
      focusDot.setAttribute("visibility", "visible");
      if (spec.onread) spec.onread(readoutFor(i));
    }
    function blur() {
      active = -1;
      cursor.setAttribute("visibility", "hidden");
      focusDot.setAttribute("visibility", "hidden");
      if (spec.onread) spec.onread("");
    }
    function nearestIndex(clientX) {
      var rect = svg.getBoundingClientRect();
      if (!rect.width) return 0;
      var rel = (clientX - rect.left) / rect.width * W; // back into viewBox units
      var best = 0, bestD = Infinity;
      for (var i = 0; i < n; i++) {
        var dd = Math.abs(xAt(i) - rel);
        if (dd < bestD) { bestD = dd; best = i; }
      }
      return best;
    }
    svg.addEventListener("pointermove", function (e) { if (n) focus(nearestIndex(e.clientX)); });
    svg.addEventListener("pointerleave", blur);
    svg.addEventListener("focus", function () { if (n && active < 0) focus(n - 1); });
    svg.addEventListener("blur", blur);
    svg.addEventListener("keydown", function (e) {
      if (!n) return;
      if (e.key === "ArrowLeft") { e.preventDefault(); focus(active <= 0 ? 0 : active - 1); }
      else if (e.key === "ArrowRight") { e.preventDefault(); focus(active < 0 ? 0 : Math.min(n - 1, active + 1)); }
      else if (e.key === "Home") { e.preventDefault(); focus(0); }
      else if (e.key === "End") { e.preventDefault(); focus(n - 1); }
      else if (e.key === "Escape") { blur(); }
    });

    // sparse x-axis tick labels (first + last + a couple between).
    if (n) {
      var ticks = [0];
      if (n > 1) ticks.push(n - 1);
      if (n > 6) ticks.push(Math.round((n - 1) / 2));
      if (n > 12) { ticks.push(Math.round((n - 1) / 4)); ticks.push(Math.round((n - 1) * 3 / 4)); }
      ticks.sort(function (a, b) { return a - b; });
      var seen = {};
      ticks.forEach(function (i) {
        if (seen[i]) return; seen[i] = 1;
        var anchor = i === 0 ? "start" : (i === n - 1 ? "end" : "middle");
        var t = svgEl("text", { "class": "mx-ts__tick", x: xAt(i), y: H - 6, "text-anchor": anchor });
        t.textContent = pts[i].label;
        svg.appendChild(t);
      });
    }
    return svg;
  }

  // Render one time-series figure into plotId, wiring its readout into readId. seriesDef
  // maps the bucket fields to draw. Empty/zero data hides the figure (honest empty).
  function renderTs(figId, plotId, readId, daily, seriesDef, fmt, stack, aria) {
    var plot = $(plotId);
    if (!plot) return;
    plot.textContent = "";
    if (!daily || !daily.length) { hide(figId); return; }

    // is there any nonzero value across the charted series? if not, skip (no fake trend).
    var any = false;
    for (var i = 0; i < daily.length && !any; i++) {
      for (var s = 0; s < seriesDef.length; s++) {
        if (n0(daily[i][seriesDef[s].key]) !== 0) { any = true; break; }
      }
    }
    if (!any) { hide(figId); return; }

    var pts = daily.map(function (b) {
      var series = {};
      for (var s = 0; s < seriesDef.length; s++) series[seriesDef[s].key] = n0(b[seriesDef[s].key]);
      return { bucket: b.bucket, label: dayLabel(b.bucket), series: series };
    });
    var readEl = $(readId);
    var svg = buildTimeSeries({
      points: pts, series: seriesDef, stack: !!stack, fmt: fmt, aria: aria,
      onread: function (html) { if (readEl) readEl.innerHTML = html; }
    });
    plot.appendChild(svg);
    show(figId);
  }

  // ---- per-model breakdown (snapshot rolled up across the whole range) ----
  // Sums each model's bucket slices across daily[] -> one row per model, sorted by the
  // charted metric. valKey is "tokens_out" (provider) or computed "tok" (usage).
  function rollupModels(daily) {
    var by = {};
    (daily || []).forEach(function (b) {
      (b.models || []).forEach(function (m) {
        var k = m.model || "(unknown)";
        var row = by[k] || (by[k] = { model: k, requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 });
        row.requests += n0(m.requests);
        row.tokens_in += n0(m.tokens_in);
        row.tokens_out += n0(m.tokens_out);
        row.spend += n0(m.spend);
        row.earned += n0(m.earned);
      });
    });
    var out = [];
    for (var k in by) if (by.hasOwnProperty(k)) out.push(by[k]);
    return out;
  }

  // ---- totals rolled up across the range (for the headline stats) ----
  function rollupTotals(daily) {
    var t = { requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 };
    (daily || []).forEach(function (b) {
      t.requests += n0(b.requests);
      t.tokens_in += n0(b.tokens_in);
      t.tokens_out += n0(b.tokens_out);
      t.spend += n0(b.spend);
      t.earned += n0(b.earned);
    });
    return t;
  }

  // ---- state machine per section: loading -> (empty | error | data) ----
  function setState(prefix, state) {
    hide(prefix + "Loading"); hide(prefix + "Empty"); hide(prefix + "Error");
    if (state === "loading") { show(prefix + "Loading"); return; }
    if (state === "empty") { show(prefix + "Empty"); return; }
    if (state === "error") { show(prefix + "Error"); return; }
  }
  function clearData(prefix) {
    hide(prefix + "Totals"); hide(prefix + "Chart"); hide(prefix + "TableWrap");
    hide(prefix + "EarnTs"); hide(prefix + "SpendTs"); hide(prefix + "TokTs"); hide(prefix + "ReqTs");
    var rows = $(prefix + "Rows"); if (rows) rows.textContent = "";
    var bars = $(prefix + "Bars"); if (bars) bars.textContent = "";
    // clear any readouts so a stale value never lingers between ranges.
    ["EarnRead", "SpendRead", "TokRead", "ReqRead"].forEach(function (k) {
      var el = $(prefix + k); if (el) el.innerHTML = "";
    });
  }

  // ---- render PROVIDER section from daily[] ----
  function renderProvider(daily) {
    clearData("prov");
    var models = rollupModels(daily);
    var t = rollupTotals(daily);
    if (!daily || !daily.length || t.requests === 0) { setState("prov", "empty"); return; }

    text("provReq", num(t.requests));
    text("provTokOut", num(t.tokens_out));
    text("provEarn", cr(t.earned));
    show("provTotals");

    renderTs("provEarnTs", "provEarnPlot", "provEarnRead", daily,
      [{ key: "earned", cls: "money", label: "" }], cr, false, "Earned per day");
    renderTs("provTokTs", "provTokPlot", "provTokRead", daily,
      [{ key: "tokens_in", cls: "in", label: "in" }, { key: "tokens_out", cls: "out", label: "out" }],
      num, true, "Tokens served per day, in and out stacked");
    renderTs("provReqTs", "provReqPlot", "provReqRead", daily,
      [{ key: "requests", cls: "req", label: "" }], num, false, "Requests per day");

    // per-model breakdown bars by tokens-out, plus snapshot table.
    var sorted = models.slice().sort(function (a, b) { return b.tokens_out - a.tokens_out; });
    var max = sorted.reduce(function (m, r) { return Math.max(m, r.tokens_out); }, 0);
    if (sorted.length) {
      var barsEl = $("provBars");
      sorted.forEach(function (r) {
        barsEl.appendChild(barRow(r.model, r.tokens_out, num(r.tokens_out), max));
      });
      show("provChart");

      var body = $("provRows");
      sorted.forEach(function (r) {
        var tr = document.createElement("tr");
        tr.appendChild(cell(r.model, "mx-model"));
        tr.appendChild(cell(num(r.requests), "num"));
        tr.appendChild(cell(num(r.tokens_out), "num"));
        tr.appendChild(cell(cr(r.earned), "num mx-money"));
        body.appendChild(tr);
      });
      show("provTableWrap");
    }
    setState("prov", "data");
  }

  // ---- render USAGE section from daily[] ----
  function renderUsage(daily) {
    clearData("use");
    var models = rollupModels(daily);
    var t = rollupTotals(daily);
    if (!daily || !daily.length || t.requests === 0) { setState("use", "empty"); return; }

    var totTok = t.tokens_in + t.tokens_out;
    text("useReq", num(t.requests));
    text("useTok", num(totTok));
    text("useSpend", cr(t.spend));
    show("useTotals");

    renderTs("useSpendTs", "useSpendPlot", "useSpendRead", daily,
      [{ key: "spend", cls: "money", label: "" }], cr, false, "Spend per day");
    renderTs("useTokTs", "useTokPlot", "useTokRead", daily,
      [{ key: "tokens_in", cls: "in", label: "in" }, { key: "tokens_out", cls: "out", label: "out" }],
      num, true, "Tokens consumed per day, in and out stacked");
    renderTs("useReqTs", "useReqPlot", "useReqRead", daily,
      [{ key: "requests", cls: "req", label: "" }], num, false, "Requests per day");

    // per-model breakdown by total tokens (in + out).
    var withTok = models.map(function (r) { return { r: r, tok: r.tokens_in + r.tokens_out }; });
    var sorted = withTok.slice().sort(function (a, b) { return b.tok - a.tok; });
    var max = sorted.reduce(function (m, x) { return Math.max(m, x.tok); }, 0);
    if (sorted.length) {
      var barsEl = $("useBars");
      sorted.forEach(function (x) { barsEl.appendChild(barRow(x.r.model, x.tok, num(x.tok), max)); });
      show("useChart");

      var body = $("useRows");
      sorted.forEach(function (x) {
        var r = x.r;
        var tr = document.createElement("tr");
        tr.appendChild(cell(r.model, "mx-model"));
        tr.appendChild(cell(num(r.requests), "num"));
        tr.appendChild(cell(num(x.tok), "num"));
        tr.appendChild(cell(cr(r.spend), "num mx-money"));
        body.appendChild(tr);
      });
      show("useTableWrap");
    }
    setState("use", "data");
  }

  // ---- fetch the time-series feed for a range ----
  // resolves to { ok:true, data } | { ok:false, auth:bool }.
  function fetchSeries(days) {
    var url = BROKER + "/metrics/series?days=" + encodeURIComponent(days);
    return fetch(url, { credentials: "include" }).then(function (r) {
      if (r.status === 401 || r.status === 403) return { ok: false, auth: false };
      if (!r.ok) return { ok: false };
      return r.json().then(function (d) { return { ok: true, data: d }; })
        .catch(function () { return { ok: false }; });
    }).catch(function () { return { ok: false }; });
  }

  // Which sections does this identity get? Honor is_provider / is_consumer; if the
  // broker ever omits both, fall back to showing whichever side has any activity so the
  // page is never blank for a real account.
  function applyRoles(d) {
    var daily = (d && d.daily) || [];
    var hasEarn = false, hasSpend = false;
    daily.forEach(function (b) { if (n0(b.earned) !== 0) hasEarn = true; if (n0(b.requests) !== 0) hasSpend = true; });
    var isProvider = d && typeof d.is_provider === "boolean" ? d.is_provider : hasEarn;
    var isConsumer = d && typeof d.is_consumer === "boolean" ? d.is_consumer : (hasSpend || !isProvider);
    if (isProvider) show("provider"); else hide("provider");
    if (isConsumer) show("usage"); else hide("usage");
    return { isProvider: isProvider, isConsumer: isConsumer };
  }

  // ---- load the view for a given range ----
  function load(days) {
    hide("allError");
    show("allLoading");
    setState("prov", "loading"); clearData("prov");
    setState("use", "loading"); clearData("use");

    fetchSeries(days).then(function (res) {
      hide("allLoading");
      if (!res.ok) {
        if (res.auth === false) { show("gate"); hide("card"); return; }
        // page-level failure: surface one error, keep the chrome.
        hide("provider"); hide("usage"); show("allError");
        return;
      }
      var d = res.data || {};
      var roles = applyRoles(d);
      var daily = d.daily || [];
      if (roles.isProvider) renderProvider(daily);
      if (roles.isConsumer) renderUsage(daily);
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
