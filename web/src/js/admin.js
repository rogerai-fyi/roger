// Admin Ops portal glue for /admin. A THIN reader over the broker's admin-gated
// endpoints behind the credentialed session cookie (no tokens in JS). The browser admin
// path is the GitHub session whose id == ADMIN_GITHUB_ID; the broker-key path (a pasted
// X-Roger-Admin key) is supported for the unhold control. EVERY view is server-gated:
// the page calls /admin/whoami FIRST and shows the deny plate if the caller is not the
// super-admin, so no data ever renders for a non-admin (and the server 403s regardless).
(function () {
  "use strict";
  var BROKER = "https://broker.rogerai.fyi";
  var REFRESH_MS = 20000; // auto-refresh cadence
  var adminKey = ""; // optional pasted broker key for the unhold control (browser session otherwise)

  function headers() {
    var h = {};
    if (adminKey) h["X-Roger-Admin"] = adminKey;
    return h;
  }
  function api(path, opts) {
    opts = opts || {};
    opts.credentials = "include";
    opts.headers = Object.assign(headers(), opts.headers || {});
    return fetch(BROKER + path, opts).then(function (r) {
      return { ok: r.ok, status: r.status, body: r.ok ? r.json() : null };
    }).then(function (res) {
      return res.body ? res.body.then(function (b) { res.json = b; return res; }) : res;
    }).catch(function () { return { ok: false, status: 0, json: null }; });
  }

  // ---- formatters (mono receipt aesthetic; 1 credit = $1) ----
  function cr(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    if (n === 0) return "$0.00";
    var s = n < 0 ? "-" : "", a = Math.abs(n);
    if (a >= 0.01) return s + "$" + a.toFixed(2);
    var p = a.toPrecision(3);
    if (/e/i.test(p)) p = a.toFixed(20).replace(/0+$/, "");
    else p = p.replace(/0+$/, "").replace(/\.$/, "");
    return s + "$" + p;
  }
  function num(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    return n.toLocaleString();
  }
  function dur(secs) {
    if (!secs || secs < 0) return "-";
    var d = Math.floor(secs / 86400), h = Math.floor((secs % 86400) / 3600), m = Math.floor((secs % 3600) / 60);
    if (d > 0) return d + "d " + h + "h";
    if (h > 0) return h + "h " + m + "m";
    return m + "m";
  }
  function whenTs(ts) {
    if (!ts) return "-";
    return new Date(ts * 1000).toLocaleString();
  }
  function shortId(id) {
    if (!id) return "-";
    return id.length > 16 ? id.slice(0, 10) + "…" + id.slice(-4) : id;
  }

  function t(id, v) { var el = document.getElementById(id); if (el) el.textContent = v; }
  function show(id) { var el = document.getElementById(id); if (el) el.hidden = false; }
  function hide(id) { var el = document.getElementById(id); if (el) el.hidden = true; }
  function on(id, ev, fn) { var el = document.getElementById(id); if (el) el.addEventListener(ev, fn); }

  // status dot + label, the one-red palette: ok = ink, degraded/down = the red beacon.
  function statusEl(label, good) {
    return '<span class="ad-dot ' + (good ? "ad-dot--ok" : "ad-dot--bad") + '" aria-hidden="true"></span>' + label;
  }

  // hand-rolled stacked horizontal bar (no chart lib): the earnings ledger split.
  function renderFinBars(fin) {
    var wrap = document.getElementById("finBars");
    if (!wrap) return;
    var parts = [
      { k: "Held", v: fin.held || 0, c: "held" },
      { k: "Payable", v: fin.payable || 0, c: "payable" },
      { k: "Paid", v: fin.paid || 0, c: "paid" },
      { k: "Clawed", v: (fin.clawed || 0) + (fin.platform_loss || 0), c: "claw" },
    ];
    var total = parts.reduce(function (s, p) { return s + p.v; }, 0);
    wrap.innerHTML = "";
    if (total <= 0) { wrap.innerHTML = '<p class="ad-empty">No earnings recorded yet.</p>'; return; }
    var track = document.createElement("div");
    track.className = "ad-bar";
    parts.forEach(function (p) {
      if (p.v <= 0) return;
      var seg = document.createElement("span");
      seg.className = "ad-bar__seg ad-bar__seg--" + p.c;
      seg.style.width = (100 * p.v / total).toFixed(2) + "%";
      seg.title = p.k + " " + cr(p.v);
      track.appendChild(seg);
    });
    wrap.appendChild(track);
    var legend = document.createElement("div");
    legend.className = "ad-legend";
    parts.forEach(function (p) {
      var li = document.createElement("span");
      li.className = "ad-legend__i";
      li.innerHTML = '<i class="ad-sw ad-sw--' + p.c + '"></i>' + p.k + " " + cr(p.v);
      legend.appendChild(li);
    });
    wrap.appendChild(legend);
  }

  function rowsInto(tbodyId, emptyId, rows, render) {
    var tb = document.getElementById(tbodyId);
    if (!tb) return;
    tb.innerHTML = "";
    if (!rows || !rows.length) { show(emptyId); return; }
    hide(emptyId);
    rows.forEach(function (r) { tb.appendChild(render(r)); });
  }
  function tr(cells) {
    var el = document.createElement("tr");
    cells.forEach(function (c) {
      var td = document.createElement("td");
      if (c && c.cls) td.className = c.cls;
      if (c && c.html !== undefined) td.innerHTML = c.html;
      else td.textContent = (c && c.txt !== undefined) ? c.txt : c;
      el.appendChild(td);
    });
    return el;
  }

  // ---- render the overview payload ----
  function paintOverview(d) {
    var h = d.health || {}, m = d.marketplace || {}, f = d.financial || {};
    // HEALTH
    document.getElementById("hReady").innerHTML = statusEl(h.ready ? "ready" : "NOT READY", !!h.ready);
    document.getElementById("hDb").innerHTML = statusEl(h.db || "-", h.db === "ok");
    if (h.shared !== undefined) document.getElementById("hShared").innerHTML = statusEl(h.shared, h.shared === "ok");
    else t("hShared", "off");
    t("hVer", h.version || "-");
    t("hUptime", dur(h.uptime_seconds));
    t("hReqs", num(h.total_requests));
    // MARKETPLACE
    t("mOnAir", num(m.on_air));
    t("mModels", num(m.models_live));
    t("mNodes", num(m.nodes_total));
    t("mPrivate", num(m.private));
    t("mReqWin", num(m.requests_window));
    t("mReqAll", num(m.requests_total));
    t("mTokWin", num((m.tokens_in_window || 0) + (m.tokens_out_window || 0)));
    t("mTokAll", num((m.tokens_in_total || 0) + (m.tokens_out_total || 0)));
    // REVENUE
    t("fFee", cr(f.platform_fee));
    t("fSpend", cr(f.consumer_spend));
    t("fEarned", cr(f.operator_earned));
    t("fTopup", cr(f.topup_volume));
    var mode = f.stripe_mode || "disabled";
    document.getElementById("fStripe").innerHTML = statusEl(mode.toUpperCase(), mode === "live");
    var badge = document.getElementById("modeBadge");
    if (badge) { badge.textContent = "[" + mode.toUpperCase() + "]"; badge.hidden = false; }
    t("fWallets", num(f.wallet_count));
    t("fWalBal", cr(f.wallet_balance));
    var rem = (f.seed_remaining === -1) ? "unlimited" : (num(f.seed_remaining) + " / " + num(f.seed_limit));
    t("fSeed", rem);
    t("fHeld", cr(f.held));
    t("fPayable", cr(f.payable));
    t("fPaid", cr(f.paid));
    t("fClaw", cr((f.clawed || 0) + (f.platform_loss || 0)));
    renderFinBars(f);
  }

  function paintPayouts(d) {
    var q = d.queue || [];
    document.getElementById("pqMeta").textContent = q.length ? "(" + q.length + " accounts)" : "";
    rowsInto("queueRows", "queueEmpty", q, function (r) {
      return tr([
        { cls: "ad-mono", html: shortId(r.account_id) },
        { cls: "num ad-strong", txt: cr(r.payable) },
        { cls: "num", txt: cr(r.held) },
        { cls: "num", txt: cr(r.pending) },
        { cls: "num", txt: cr(r.paid) },
      ]);
    });
    rowsInto("histRows", "histEmpty", d.history || [], function (p) {
      return tr([
        { txt: "#" + p.id },
        { cls: "ad-mono", html: shortId(p.account_id) },
        { cls: "num", txt: cr(p.amount) },
        { html: '<span class="ad-pill ad-pill--' + (p.state || "") + '">' + (p.state || "-") + "</span>" },
        { cls: "ad-mono", txt: p.stripe_transfer_id ? shortId(p.stripe_transfer_id) : "-" },
        { cls: "num", txt: whenTs(p.created_at) },
      ]);
    });
    rowsInto("revRows", "revEmpty", d.open_reversals || [], function (rv) {
      return tr([
        { cls: "ad-mono", txt: shortId(rv.dispute_id) },
        { cls: "ad-mono", html: shortId(rv.account_id) },
        { cls: "num", txt: cr(rv.amount) },
        { cls: "num", txt: num(rv.attempts) },
        { txt: rv.last_error || "-" },
      ]);
    });
  }

  function paintAbuse(d) {
    var a = d.abuse || {};
    t("aBanned", num((a.banned_owners || []).length));
    t("aStruck", num(a.struck_accounts));
    t("aStrikes", num(a.total_strikes));
    t("aHolds", num(a.account_holds));
    var csam = document.getElementById("aCsam");
    csam.textContent = num(a.csam_queued) + " / " + num(a.csam_total);
    csam.className = (a.csam_queued > 0) ? "ad-alarm" : "";
    t("aReports", num(a.report_count));
    t("aBanNodes", num(a.banned_nodes));
    t("aDisputes", num(a.dispute_count));
    rowsInto("banRows", "banEmpty", a.banned_owners || [], function (b) {
      return tr([
        { cls: "ad-mono", html: shortId(b.account_id) },
        { txt: b.reason || "-" },
        { cls: "num", txt: num(b.strikes) },
      ]);
    });
  }

  function paintActivity(d) {
    rowsInto("actRows", "actEmpty", d.events || [], function (e) {
      return tr([
        { cls: "num", txt: e.id },
        { txt: e.side || "-" },
        { html: '<span class="ad-kind">' + (e.kind || "-") + "</span>" },
        { cls: "num " + (e.amount < 0 ? "ad-neg" : ""), txt: cr(e.amount) },
        { cls: "ad-mono", html: shortId(e.holder) },
        { txt: e.state || "-" },
        { cls: "num", txt: whenTs(e.ts) },
      ]);
    });
  }

  function win() { var s = document.getElementById("winSel"); return (s && s.value) || "1"; }

  function loadAll() {
    var d = win();
    Promise.all([
      api("/admin/overview?days=" + d),
      api("/admin/payouts"),
      api("/admin/abuse"),
      api("/admin/activity?limit=120"),
    ]).then(function (res) {
      var anyOk = res.some(function (r) { return r.ok; });
      if (!anyOk) { show("dashError"); return; }
      hide("dashError");
      if (res[0].ok && res[0].json) paintOverview(res[0].json);
      if (res[1].ok && res[1].json) paintPayouts(res[1].json);
      if (res[2].ok && res[2].json) paintAbuse(res[2].json);
      if (res[3].ok && res[3].json) paintActivity(res[3].json);
      t("freshness", "updated " + new Date().toLocaleTimeString());
    });
  }

  function wireUnhold() {
    on("uhSubmit", "click", function () {
      var account = (document.getElementById("uhAccount") || {}).value || "";
      var node = (document.getElementById("uhNode") || {}).value || "";
      var forgive = !!(document.getElementById("uhForgive") || {}).checked;
      if (!account && !node) { t("uhMsg", "account id or node required"); return; }
      t("uhMsg", "working...");
      api("/admin/unhold", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ account_id: account, node: node, forgive: forgive }),
      }).then(function (r) {
        if (r.ok) { t("uhMsg", "released. Lots promote on the next sweep."); loadAll(); }
        else if (r.status === 403) { t("uhMsg", "forbidden - the unhold control needs the broker key (paste below)."); showKeyRow(); }
        else { t("uhMsg", "could not release (status " + r.status + ")"); }
      });
    });
    on("uhKey", "input", function (e) { adminKey = e.target.value || ""; });
  }
  function showKeyRow() { show("uhKeyNote"); show("uhKeyRow"); }

  function wireControls() {
    on("refresh", "click", loadAll);
    on("winSel", "change", loadAll);
    on("logout", "click", function () {
      fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
        .then(function () { location.replace("/login.html"); });
    });
  }

  // ---- gate: verify admin BEFORE rendering any data ----
  function gate() {
    api("/admin/whoami").then(function (r) {
      hide("gateLoading");
      if (!r.ok || !r.json || !r.json.admin) {
        // 403 (not the super-admin) or no session -> deny plate, no data fetched.
        show("gateDeny");
        return;
      }
      hide("gate");
      show("dash");
      t("who", r.json.github_login ? ("@" + r.json.github_login + " (admin)") : "admin");
      if (r.json.via === "session") { /* browser session: unhold may need the key */ showKeyRow(); }
      wireControls();
      wireUnhold();
      loadAll();
      setInterval(loadAll, REFRESH_MS);
    });
  }

  gate();
})();
