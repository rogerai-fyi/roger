/* ============================================================================
   Roger node console — console.js
   Dependency-free vanilla JS. Module-pattern IIFE. Sections:
     1. Auth / token        6. SHARE actions (onair/private/price/rename/detect)
     2. tiny DOM + fmt      7. ACCOUNT
     3. API + token plumbing 8. BROWSE
     4. Toasts / modals     9. Boot
     5. SSE + SHARE render
   Every /api call carries the per-run token (header or query); the SSE stream
   carries it in the query (EventSource cannot set headers).
   ========================================================================== */
(function () {
  "use strict";

  /* 1. AUTH / TOKEN -------------------------------------------------------- */
  var TOKEN = new URLSearchParams(location.search).get("t") || "";

  /* 2. TINY DOM + FORMAT HELPERS ------------------------------------------ */
  function $(id) { return document.getElementById(id); }
  function el(tag, cls, txt) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (txt != null) e.textContent = txt;
    return e;
  }
  function show(node, on) { if (node) node.hidden = !on; }
  function fmtInt(n) { return (Number(n) || 0).toLocaleString("en-US"); }
  function fmtUSD(n) { return "$" + (Number(n) || 0).toFixed(2); }
  function clamp(n, lo, hi) { return Math.max(lo, Math.min(hi, n)); }

  // Signal meter from block glyphs ▁▂▃▄▅▆▇█ — a small rising equalizer.
  function signalBars(sig) {
    var ramp = "▁▂▃▄▅▆▇█";
    var n = clamp(Number(sig) || 0, 0, 100);
    var out = "";
    for (var seg = 0; seg < 5; seg++) {
      var local = clamp((n - seg * 20) / 20, 0, 1); // 0..1 within this segment
      out += ramp.charAt(Math.round(local * (ramp.length - 1)));
    }
    return out;
  }
  function signalClass(sig) {
    var n = Number(sig) || 0;
    return n >= 66 ? "s-high" : n >= 33 ? "s-mid" : "s-low";
  }

  /* 3. API + TOKEN PLUMBING ----------------------------------------------- */
  // ApiError carries the HTTP status so callers can special-case 503 ("broker
  // not configured") versus real failures.
  function ApiError(status, message) { this.status = status; this.message = message; }
  ApiError.prototype = Object.create(Error.prototype);

  function withToken(path) {
    return path + (path.indexOf("?") === -1 ? "?" : "&") + "t=" + encodeURIComponent(TOKEN);
  }

  // api(method, path, body) -> Promise<parsedJSON|null>. Always sends the token
  // both as a header and in the query, so it works regardless of how the server
  // reads it.
  function api(method, path, body) {
    var opts = {
      method: method,
      headers: { "X-Roger-Token": TOKEN }
    };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return fetch(withToken(path), opts).then(function (res) {
      var ct = res.headers.get("content-type") || "";
      var parse = ct.indexOf("application/json") !== -1
        ? res.json().catch(function () { return null; })
        : res.text().catch(function () { return ""; });
      return parse.then(function (data) {
        if (!res.ok) {
          var msg = (data && data.message) || (typeof data === "string" && data) || res.statusText;
          throw new ApiError(res.status, msg);
        }
        return data;
      });
    });
  }
  function apiGet(path) { return api("GET", path); }
  function apiPost(path, body) { return api("POST", path, body || {}); }

  /* 4. TOASTS / MODALS ----------------------------------------------------- */
  function toast(msg, kind) {
    if (!msg) return;
    var t = el("div", "toast" + (kind ? " " + kind : ""), msg);
    $("toasts").appendChild(t);
    setTimeout(function () {
      t.style.transition = "opacity .3s";
      t.style.opacity = "0";
      setTimeout(function () { t.remove(); }, 320);
    }, kind === "err" ? 6000 : 4000);
  }
  function toastErr(e) {
    var m = (e && e.message) || "something went wrong";
    if (e && e.status === 503) m = "broker not configured: " + m;
    toast(m, "err");
  }

  var openModal = null;
  function modalOpen(id) {
    closeModal();
    openModal = $(id);
    show($("modal-backdrop"), true);
    show(openModal, true);
    var first = openModal.querySelector("input, button");
    if (first) try { first.focus(); } catch (_) {}
  }
  function closeModal() {
    if (openModal) show(openModal, false);
    show($("modal-backdrop"), false);
    openModal = null;
  }
  function copyText(text, btn) {
    var done = function () { if (btn) { var o = btn.textContent; btn.textContent = "copied"; setTimeout(function () { btn.textContent = o; }, 1200); } };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () { toast("copy failed", "warn"); });
    } else {
      try {
        var ta = el("textarea"); ta.value = text; document.body.appendChild(ta);
        ta.select(); document.execCommand("copy"); ta.remove(); done();
      } catch (_) { toast("copy failed", "warn"); }
    }
  }

  /* 5. SSE + SHARE RENDER -------------------------------------------------- */
  var shareRowEls = {};       // model -> <tr>, reused across frames to avoid flicker
  var lastSnapshot = null;

  function connectSSE() {
    var conn = $("conn-status");
    var es = new EventSource(withToken("/api/events"));
    es.onopen = function () { conn.className = "conn live"; $("conn-text").textContent = "live"; };
    es.onmessage = function (e) {
      try { renderSnapshot(JSON.parse(e.data)); } catch (_) {}
    };
    es.onerror = function () {
      conn.className = "conn down";
      $("conn-text").textContent = "reconnecting";
      // EventSource auto-reconnects; nothing else to do.
    };
  }

  function renderSnapshot(s) {
    lastSnapshot = s;
    // top bar + share header
    $("top-callsign").textContent = s.station || "—";
    $("share-callsign").textContent = s.station || "—";
    var slots = (s.on_air || 0) + "/" + (s.max_on_air || 0);
    $("top-slots-text").textContent = slots;
    $("share-slots-text").textContent = slots;

    var t = s.totals || {};
    var totals = $("share-totals");
    totals.innerHTML = "";
    totals.appendChild(el("span", null, fmtInt(t.requests) + " requests"));
    totals.appendChild(document.createTextNode(" · "));
    totals.appendChild(el("span", null, fmtInt(t.out_tokens) + " out tok"));
    totals.appendChild(document.createTextNode(" · "));
    totals.appendChild(el("span", null, fmtUSD(t.earnings)));
    totals.appendChild(document.createTextNode(" · "));
    totals.appendChild(el("span", "muted", "settles on the broker"));

    show($("share-login-warn"), !s.logged_in);
    renderShareRows(s.rows || []);
  }

  function renderShareRows(rows) {
    var tbody = $("share-rows");
    // drop the placeholder empty row on first real data
    var empty = tbody.querySelector(".empty-row");
    if (rows.length && empty) empty.remove();

    var seen = {};
    rows.forEach(function (row) {
      seen[row.model] = true;
      var tr = shareRowEls[row.model];
      if (!tr) { tr = buildShareRow(row.model); shareRowEls[row.model] = tr; tbody.appendChild(tr); }
      updateShareRow(tr, row);
    });
    // remove rows for models that vanished
    Object.keys(shareRowEls).forEach(function (m) {
      if (!seen[m]) { shareRowEls[m].remove(); delete shareRowEls[m]; }
    });
    if (!tbody.children.length) {
      var er = el("tr", "empty-row");
      var td = el("td", null, "No models detected yet. Try re-detect.");
      td.colSpan = 7; er.appendChild(td); tbody.appendChild(er);
    }
  }

  // buildShareRow makes the stable skeleton once; updateShareRow fills it each
  // frame. Button clicks go through tbody delegation (see wiring), so rebuilding
  // text never drops handlers.
  function buildShareRow(model) {
    var tr = el("tr");
    tr.setAttribute("data-model", model);
    tr.appendChild(el("td", "cell-model"));               // 0 model
    tr.appendChild(el("td", "status-cell"));              // 1 status
    tr.appendChild(el("td", "price-cell"));               // 2 price
    tr.appendChild(el("td", "num served"));               // 3 served
    tr.appendChild(el("td", "num outtok"));               // 4 out tok
    tr.appendChild(el("td", "num earnings"));             // 5 earnings
    tr.appendChild(el("td", "col-actions"));              // 6 actions
    return tr;
  }

  function updateShareRow(tr, row) {
    var tds = tr.children;
    var onAir = !!row.on_air, priv = !!row.private, link = row.link || "off";
    var connecting = onAir && link !== "on-air"; // truthful link state

    // --- model cell: dot + name + ctx ---
    var dotCls = priv ? "off" : connecting ? "connecting" : onAir ? "on" : "off";
    var dotCh = connecting ? "◌" : onAir ? "◉" : "○";
    var ctx = (row.ctx ? (Math.round(row.ctx / 1024) + "k") + (row.ctx_estimated ? "≈" : "") : "");
    tds[0].innerHTML = "";
    tds[0].appendChild(el("span", "status-dot " + dotCls, dotCh));
    tds[0].appendChild(el("span", "model-name", row.model));
    if (ctx) tds[0].appendChild(el("span", "ctx", " " + ctx + " ctx"));

    // --- status label ---
    var lblCls, lblTxt;
    if (priv) { lblCls = "private"; lblTxt = "PRIVATE"; }
    else if (connecting) { lblCls = "connecting"; lblTxt = link === "reconnecting" ? "RECONNECTING" : "CONNECTING"; }
    else if (onAir) { lblCls = "on"; lblTxt = "ON-AIR"; }
    else { lblCls = "off"; lblTxt = "OFF-AIR"; }
    tds[1].innerHTML = "";
    tds[1].appendChild(el("span", "status-label " + lblCls, lblTxt));

    // --- price ---
    tds[2].innerHTML = "";
    if ((Number(row.price_out) || 0) === 0) {
      tds[2].appendChild(el("span", "free", "FREE"));
    } else {
      tds[2].appendChild(document.createTextNode("$" + row.price_out + "/1M out"));
    }
    if (row.scheduled) tds[2].appendChild(el("span", "sched", " · sched"));

    // --- counters ---
    tds[3].textContent = fmtInt(row.served);
    tds[4].textContent = fmtInt(row.out_tokens);
    tds[5].textContent = fmtUSD(row.earnings);

    // --- actions ---
    tds[6].innerHTML = "";
    var actions = el("div", "row-actions");
    var onairBtn = el("button", "btn small", onAir ? "Take off air" : "Put on air");
    onairBtn.setAttribute("data-act", "onair");
    onairBtn.setAttribute("data-model", row.model);
    var privBtn = el("button", "btn small ghost", priv ? "Make public" : "Make private");
    privBtn.setAttribute("data-act", "private");
    privBtn.setAttribute("data-model", row.model);
    var priceBtn = el("button", "btn small ghost", "Price");
    priceBtn.setAttribute("data-act", "price");
    priceBtn.setAttribute("data-model", row.model);
    actions.appendChild(onairBtn);
    actions.appendChild(privBtn);
    actions.appendChild(priceBtn);
    tds[6].appendChild(actions);
  }

  /* 6. SHARE ACTIONS ------------------------------------------------------- */
  function findRow(model) {
    if (!lastSnapshot || !lastSnapshot.rows) return null;
    for (var i = 0; i < lastSnapshot.rows.length; i++) {
      if (lastSnapshot.rows[i].model === model) return lastSnapshot.rows[i];
    }
    return null;
  }

  function actOnAir(model) {
    apiPost("/api/share/onair", { model: model }).then(function (r) {
      r = r || {};
      if (r.login_needed) { toast(r.message || "Log in first to put a model on air.", "warn"); setTab("account"); }
      else if (r.at_limit) { toast(r.message || "All on-air slots are full.", "warn"); }
      else if (r.message) { toast(r.message, "ok"); }
      // SSE reflects the real result either way.
    }).catch(toastErr);
  }

  function actPrivate(model) {
    apiPost("/api/share/private", { model: model }).then(function (r) {
      r = r || {};
      if (r.code) showFreqCode(r.code, r.band_display);
      if (r.message) toast(r.message, "ok");
    }).catch(toastErr);
  }

  function showFreqCode(code, band) {
    $("code-value").textContent = code;
    $("code-band").textContent = band ? ("band: " + band) : "";
    show($("code-card"), true);
    setTab("share");
  }

  // --- price modal ---
  var priceModel = null;
  function openPriceModal(model) {
    priceModel = model;
    var row = findRow(model) || {};
    $("price-model").textContent = model;
    $("price-in").value = row.price_in != null ? row.price_in : "";
    $("price-out").value = row.price_out != null ? row.price_out : "";
    $("price-windows").innerHTML = "";
    modalOpen("modal-price");
  }
  function addWindowRow(w) {
    w = w || {};
    var wrap = el("div", "window-row");
    function mk(ph, val, type) { var i = el("input", "inp"); i.type = type || "text"; i.placeholder = ph; if (val != null) i.value = val; return i; }
    var start = mk("HH:MM", w.start); start.setAttribute("data-f", "start");
    var end = mk("HH:MM", w.end); end.setAttribute("data-f", "end");
    var inp = mk("in", w.in, "number"); inp.setAttribute("data-f", "in"); inp.min = "0"; inp.step = "0.01";
    var outp = mk("out", w.out, "number"); outp.setAttribute("data-f", "out"); outp.min = "0"; outp.step = "0.01";
    var freeLbl = el("label", "chk");
    var free = el("input"); free.type = "checkbox"; free.setAttribute("data-f", "free"); free.checked = !!w.free;
    freeLbl.appendChild(free); freeLbl.appendChild(document.createTextNode("free"));
    var rm = el("button", "x-btn", "×"); rm.type = "button";
    rm.onclick = function () { wrap.remove(); };
    wrap.appendChild(start); wrap.appendChild(end); wrap.appendChild(inp); wrap.appendChild(outp); wrap.appendChild(freeLbl); wrap.appendChild(rm);
    $("price-windows").appendChild(wrap);
  }
  function collectWindows() {
    var rows = $("price-windows").querySelectorAll(".window-row");
    var out = [];
    Array.prototype.forEach.call(rows, function (r) {
      function v(f) { return r.querySelector('[data-f="' + f + '"]'); }
      var start = v("start").value.trim(), end = v("end").value.trim();
      if (!start && !end) return;
      out.push({
        start: start, end: end,
        in: parseFloat(v("in").value) || 0,
        out: parseFloat(v("out").value) || 0,
        free: v("free").checked
      });
    });
    return out;
  }
  function savePrice() {
    if (!priceModel) return;
    var body = {
      model: priceModel,
      in: parseFloat($("price-in").value) || 0,
      out: parseFloat($("price-out").value) || 0,
      windows: collectWindows()
    };
    apiPost("/api/share/price", body).then(function (r) {
      closeModal();
      toast((r && r.message) || "Pricing saved.", "ok");
    }).catch(toastErr);
  }

  // --- rename + detect ---
  function saveRename() {
    var name = $("rename-input").value.trim();
    if (!name) { toast("Enter a callsign.", "warn"); return; }
    apiPost("/api/share/rename", { station: name }).then(function (r) {
      closeModal();
      toast((r && r.message) || "Station renamed.", "ok");
    }).catch(toastErr);
  }
  function runDetect() {
    var url = $("detect-url").value.trim();
    var key = $("detect-key").value.trim();
    var body = {};
    if (url) body.url = url;
    if (key) body.key = key;
    apiPost("/api/share/detect", body).then(function (r) {
      closeModal();
      toast((r && r.message) || "Detection complete.", "ok");
    }).catch(toastErr);
  }

  /* 7. ACCOUNT ------------------------------------------------------------- */
  var accountDisabled = false;

  function disableAccount() {
    accountDisabled = true;
    show($("account-body"), false);
    show($("account-disabled"), true);
  }

  function loadAccount() {
    apiGet("/api/account").then(function (a) {
      a = a || {};
      accountDisabled = false;
      show($("account-body"), true);
      show($("account-disabled"), false);

      $("acct-balance").textContent = fmtUSD(a.balance);
      var cap = Number(a.monthly_cap) || 0, spend = Number(a.monthly_spend) || 0;
      var pct = cap > 0 ? clamp(spend / cap * 100, 0, 100) : 0;
      var fill = $("acct-spend-fill");
      fill.style.width = pct + "%";
      fill.className = "meter-fill" + (cap > 0 && spend >= cap ? " over" : "");
      $("acct-spend-text").textContent = fmtUSD(spend) + " of " + (cap > 0 ? fmtUSD(cap) : "no cap") + " this month";
      if ($("limit-cap").value === "") $("limit-cap").value = cap || "";

      var inLogged = !!a.logged_in;
      show($("logged-in"), inLogged);
      show($("logged-out"), !inLogged);
      if (inLogged) {
        show($("login-flow"), false);
        $("acct-login-state").textContent = "signed in" + (a.user_id ? " · " + a.user_id : "");
      }
    }).catch(function (e) {
      if (e.status === 503) disableAccount();
      else toastErr(e);
    });

    if (accountDisabled) return;
    loadPayout();
    loadGrants();
  }

  function loadPayout() {
    apiGet("/api/payout").then(function (p) {
      p = p || {};
      $("payout-status").textContent = p.status || (p.kyc || "not set up");
      $("payout-payable").textContent = fmtUSD(p.payable);
    }).catch(function (e) { if (e.status !== 503) {/* keep quiet */} });
  }
  function payoutHistory() {
    apiGet("/api/payout/history").then(function (h) {
      var box = $("payout-history");
      show(box, true);
      if (!h || (h.length === 0)) { box.textContent = "no payouts yet"; return; }
      var lines = (Array.isArray(h) ? h : (h.items || [])).map(function (x) {
        return (x.date || x.created_at || "") + "  " + fmtUSD(x.amount) + "  " + (x.status || "");
      });
      box.textContent = lines.join("\n") || "no payouts yet";
    }).catch(toastErr);
  }

  function loadGrants() {
    apiGet("/api/grants").then(function (g) {
      var list = $("grant-list");
      list.innerHTML = "";
      var items = Array.isArray(g) ? g : (g && g.items) || [];
      if (!items.length) { list.appendChild(el("li", "muted", "none")); return; }
      items.forEach(function (it) {
        var name = it.name || it.id || "grant";
        var meta = it.free ? " · free" : (it.balance != null ? " · " + fmtUSD(it.balance) : "");
        list.appendChild(el("li", null, name + meta));
      });
    }).catch(function (e) { if (e.status !== 503) {/* quiet */} });
  }
  function createGrant() {
    var name = $("grant-name").value.trim();
    if (!name) { toast("Name the grant.", "warn"); return; }
    apiPost("/api/grants", { name: name, free: $("grant-free").checked }).then(function (r) {
      r = r || {};
      $("grant-name").value = ""; $("grant-free").checked = false;
      if (r.secret) revealGrantSecret(name, r.secret);
      toast((r.message) || "Grant created.", "ok");
      loadGrants();
    }).catch(toastErr);
  }
  function revealGrantSecret(name, secret) {
    var list = $("grant-list");
    var li = el("li", null);
    li.style.borderLeft = "3px solid var(--accent)";
    li.style.paddingLeft = "8px";
    li.appendChild(el("div", "small", name + " — save this secret, shown once:"));
    var row = el("div", "code-row");
    var code = el("code", "code-value", secret);
    var copy = el("button", "btn small", "copy");
    copy.onclick = function () { copyText(secret, copy); };
    row.appendChild(code); row.appendChild(copy);
    li.appendChild(row);
    list.insertBefore(li, list.firstChild);
  }

  // login device flow
  function loginBegin() {
    var btn = $("btn-login"); btn.disabled = true;
    apiPost("/api/account/login/begin", {}).then(function (r) {
      r = r || {};
      show($("login-flow"), true);
      var a = $("login-uri");
      a.href = r.verification_uri || "#";
      a.textContent = r.verification_uri || "the GitHub device page";
      $("login-code").textContent = r.user_code || "—";
      // poll blocks server-side until authorized
      return apiPost("/api/account/login/poll", {});
    }).then(function () {
      toast("Signed in.", "ok");
      show($("login-flow"), false);
      loadAccount();
    }).catch(function (e) {
      toastErr(e);
      show($("login-flow"), false);
    }).then(function () { btn.disabled = false; });
  }
  function logout() {
    apiPost("/api/account/logout", {}).then(function () { toast("Signed out.", "ok"); loadAccount(); }).catch(toastErr);
  }
  function topup() {
    var usd = parseFloat($("topup-amount").value);
    if (!usd || usd <= 0) { toast("Enter an amount.", "warn"); return; }
    apiPost("/api/account/topup", { usd: usd }).then(function (r) {
      if (r && r.url) { window.open(r.url, "_blank", "noopener"); toast("Opening checkout…", "ok"); }
      else toast((r && r.message) || "Top-up requested.", "ok");
    }).catch(toastErr);
  }
  function setLimit() {
    var cap = parseFloat($("limit-cap").value);
    if (isNaN(cap) || cap < 0) { toast("Enter a cap (0 = no cap).", "warn"); return; }
    apiPost("/api/account/limit", { cap: cap }).then(function (r) {
      toast((r && r.message) || "Spend limit updated.", "ok"); loadAccount();
    }).catch(toastErr);
  }
  function payoutOnboard() {
    apiPost("/api/payout/onboard", {}).then(function (r) {
      if (r && r.url) { window.open(r.url, "_blank", "noopener"); toast("Opening payout setup…", "ok"); }
      else toast((r && r.message) || "Payout onboarding started.", "ok");
    }).catch(toastErr);
  }
  function payoutRequest() {
    apiPost("/api/payout/request", {}).then(function (r) {
      toast((r && r.message) || "Payout requested.", "ok"); loadPayout();
    }).catch(toastErr);
  }

  /* 8. BROWSE -------------------------------------------------------------- */
  function loadBrowse() {
    var tbody = $("browse-rows");
    apiGet("/api/browse").then(function (offers) {
      tbody.innerHTML = "";
      offers = Array.isArray(offers) ? offers : [];
      if (!offers.length) {
        var er = el("tr", "empty-row");
        var td = el("td", null, "No models on the market right now."); td.colSpan = 8;
        er.appendChild(td); tbody.appendChild(er); return;
      }
      offers.forEach(function (o) { tbody.appendChild(buildBrowseRow(o)); });
    }).catch(function (e) {
      tbody.innerHTML = "";
      var er = el("tr", "empty-row");
      var td = el("td", null, e.status === 503 ? "Browse needs a configured broker." : "Could not load the market.");
      td.colSpan = 8; er.appendChild(td); tbody.appendChild(er);
    });
  }
  function buildBrowseRow(o) {
    var tr = el("tr");
    // model + verified
    var m = el("td", "cell-model");
    var dot = el("span", "online-dot " + (o.online ? "on" : "off"), o.online ? "◉" : "○");
    m.appendChild(dot);
    m.appendChild(el("span", "model-name", o.model || "—"));
    if (o.verified) { var v = el("span", "verified", " ◆"); v.title = "verified lineage"; m.appendChild(v); }
    if (o.confidential) m.appendChild(el("span", "ctx", " · conf"));
    tr.appendChild(m);
    // node
    tr.appendChild(el("td", "mono", o.node_id || "—"));
    // price
    var price = el("td", "price-cell");
    if (o.free_now || (Number(o.price_out) || 0) === 0) price.appendChild(el("span", "free", "FREE"));
    else price.appendChild(document.createTextNode("$" + o.price_out + "/1M out"));
    tr.appendChild(price);
    // signal
    var sig = el("td");
    var bars = el("span", "signal " + signalClass(o.signal), signalBars(o.signal));
    bars.title = "signal " + (Number(o.signal) || 0);
    sig.appendChild(bars);
    tr.appendChild(sig);
    // tps / ttft / ctx / region
    tr.appendChild(el("td", "num", o.tps != null ? Math.round(o.tps) : "—"));
    tr.appendChild(el("td", "num", o.ttft_ms != null ? Math.round(o.ttft_ms) + "ms" : "—"));
    tr.appendChild(el("td", "num", o.ctx ? Math.round(o.ctx / 1024) + "k" : "—"));
    tr.appendChild(el("td", null, o.region || "—"));
    return tr;
  }

  /* TABS ------------------------------------------------------------------- */
  function setTab(name) {
    ["share", "account", "browse"].forEach(function (n) {
      show($("panel-" + n), n === name);
    });
    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (b) {
      b.setAttribute("aria-selected", b.getAttribute("data-tab") === name ? "true" : "false");
    });
    if (name === "account") loadAccount();
    if (name === "browse") loadBrowse();
  }

  /* 9. BOOT / WIRING ------------------------------------------------------- */
  function wire() {
    // tabs + any [data-goto] / [data-tab] click
    document.addEventListener("click", function (e) {
      var tabBtn = e.target.closest("[data-tab]");
      if (tabBtn) { setTab(tabBtn.getAttribute("data-tab")); return; }
      var gotoBtn = e.target.closest("[data-goto]");
      if (gotoBtn) { setTab(gotoBtn.getAttribute("data-goto")); return; }
      var closeBtn = e.target.closest("[data-close]");
      if (closeBtn) { closeModal(); return; }
    });

    // share table action delegation
    $("share-rows").addEventListener("click", function (e) {
      var btn = e.target.closest("button[data-act]");
      if (!btn) return;
      var model = btn.getAttribute("data-model");
      var act = btn.getAttribute("data-act");
      if (act === "onair") actOnAir(model);
      else if (act === "private") actPrivate(model);
      else if (act === "price") openPriceModal(model);
    });

    // share header
    $("btn-rename").onclick = function () {
      $("rename-input").value = (lastSnapshot && lastSnapshot.station) || "";
      modalOpen("modal-rename");
    };
    $("rename-save").onclick = saveRename;
    $("btn-detect").onclick = function () {
      $("detect-url").value = ""; $("detect-key").value = "";
      modalOpen("modal-detect");
    };
    $("detect-run").onclick = runDetect;

    // price modal
    $("price-add-window").onclick = function () { addWindowRow(); };
    $("price-save").onclick = savePrice;

    // freq code card
    $("code-copy").onclick = function () { copyText($("code-value").textContent, $("code-copy")); };
    $("code-dismiss").onclick = function () { show($("code-card"), false); };

    // backdrop click closes
    $("modal-backdrop").addEventListener("click", function (e) {
      if (e.target === $("modal-backdrop")) closeModal();
    });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape") closeModal(); });

    // account
    $("btn-login").onclick = loginBegin;
    $("login-code-copy").onclick = function () { copyText($("login-code").textContent, $("login-code-copy")); };
    $("btn-logout").onclick = logout;
    $("btn-topup").onclick = topup;
    $("btn-limit").onclick = setLimit;
    $("btn-payout-onboard").onclick = payoutOnboard;
    $("btn-payout-request").onclick = payoutRequest;
    $("btn-payout-history").onclick = payoutHistory;
    $("btn-grant-create").onclick = createGrant;

    // browse
    $("btn-browse-refresh").onclick = loadBrowse;
  }

  function boot() {
    if (!TOKEN) { show($("auth-gate"), true); return; }
    show($("app"), true);
    wire();
    setTab("share");
    // one-shot state for an instant paint, then live stream
    apiGet("/api/state").then(renderSnapshot).catch(function (e) {
      if (e.status === 403) { show($("app"), false); show($("auth-gate"), true); }
    });
    connectSSE();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
