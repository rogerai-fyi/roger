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
    totals.appendChild(el("span", null, fmtUSD(t.earnings) + " at list"));
    totals.appendChild(document.createTextNode(" · "));
    totals.appendChild(el("span", "muted", "settles on the broker"));
    // A PROBE IS NOT TRAFFIC, and the console has to say so in the same words the terminal
    // uses. The requests/tokens above exclude the broker's canaries; reporting them beside
    // rather than not at all is what keeps a busy rig explicable. Shown only when there are
    // any - a printed zero would read as a measurement.
    if (t.probes > 0) {
      totals.appendChild(document.createTextNode(" · "));
      totals.appendChild(el("span", "muted",
        "plus " + fmtInt(t.probes) + " unbilled broker checks"));
    }

    show($("share-login-warn"), !s.logged_in);
    renderShareRows(s.rows || []);
    // The chat picker's LOCAL group IS this table, so it is rebuilt from every snapshot.
    // Detection runs in the background for ~15s after launch, and a picker filled once on
    // tab-open would show the fleet as it was before that landed - which is how a machine
    // serving twenty-seven models looks like a machine serving one.
    chatFillModels();
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
      td.colSpan = 8; er.appendChild(td); tbody.appendChild(er);
    }
  }

  // buildShareRow makes the stable skeleton once; updateShareRow fills it each
  // frame. Button clicks go through tbody delegation (see wiring), so rebuilding
  // text never drops handlers.
  function buildShareRow(model) {
    var tr = el("tr");
    tr.setAttribute("data-model", model);
    tr.appendChild(el("td", "cell-model"));               // 0 model
    tr.appendChild(el("td", "variant-cell"));             // 1 variant
    tr.appendChild(el("td", "status-cell"));              // 2 status
    tr.appendChild(el("td", "price-cell"));               // 3 price
    tr.appendChild(el("td", "num served"));               // 4 served
    tr.appendChild(el("td", "num outtok"));               // 5 out tok
    tr.appendChild(el("td", "num earnings"));             // 6 earnings
    tr.appendChild(el("td", "col-actions"));              // 7 actions
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
    tds[0].title = row.model; // the cell can ellipsis a long id; hover still reads it whole
    if (ctx) tds[0].appendChild(el("span", "ctx", " " + ctx + " ctx"));

    // --- status label ---
    var lblCls, lblTxt;
    if (priv) { lblCls = "private"; lblTxt = "PRIVATE"; }
    else if (connecting) { lblCls = "connecting"; lblTxt = link === "reconnecting" ? "RECONNECTING" : "CONNECTING"; }
    else if (onAir) { lblCls = "on"; lblTxt = "ON-AIR"; }
    else { lblCls = "off"; lblTxt = "OFF-AIR"; }
    // --- variant: what detection read off this machine ---
    // An em dash, not a blank. A blank cell cannot tell "this model published no
    // metadata" apart from "this column failed to render", and only one of those is
    // something the operator should shrug at.
    tds[1].innerHTML = "";
    var vparts = [];
    if (row.quant) vparts.push(row.quant);
    if (row.weights) vparts.push("by " + row.weights);
    if (row.variant) vparts.push(row.variant);
    var vinner = el("span", "variant-inner");
    if (vparts.length) {
      vinner.appendChild(el("span", "variant-q", vparts[0]));
      if (vparts.length > 1) {
        vinner.appendChild(el("span", "variant-rest", " " + vparts.slice(1).join(" · ")));
      }
      // The cell can ellipsis; the full reading stays reachable on hover.
      tds[1].title = vparts.join(" · ");
    } else {
      vinner.appendChild(el("span", "variant-none", "—"));
      tds[1].title = "nothing detected — shares as the plain model id";
    }
    tds[1].appendChild(vinner);

    tds[2].innerHTML = "";
    tds[2].appendChild(el("span", "status-label " + lblCls, lblTxt));

    // --- price ---
    tds[3].innerHTML = "";
    if ((Number(row.price_out) || 0) === 0) {
      tds[3].appendChild(el("span", "free", "FREE"));
    } else {
      tds[3].appendChild(document.createTextNode("$" + row.price_out + "/1M out"));
    }
    if (row.scheduled) tds[3].appendChild(el("span", "sched", " · sched"));

    // --- counters ---
    tds[4].textContent = fmtInt(row.served);
    tds[5].textContent = fmtInt(row.out_tokens);
    tds[6].textContent = fmtUSD(row.earnings);

    // --- actions ---
    tds[7].innerHTML = "";
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
    tds[7].appendChild(actions);
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
  // The market feed, held so the chat picker can be rebuilt from it without re-fetching
  // (the LOCAL half of that picker arrives on the snapshot stream, on its own schedule).
  var lastOffers = [];

  function loadBrowse() {
    var tbody = $("browse-rows");
    apiGet("/api/browse").then(function (offers) {
      tbody.innerHTML = "";
      lastOffers = Array.isArray(offers) ? offers : [];
      if (!lastOffers.length) {
        var er = el("tr", "empty-row");
        var td = el("td", null, "No models on the market right now."); td.colSpan = 8;
        er.appendChild(td); tbody.appendChild(er); chatFillModels(); return;
      }
      lastOffers.forEach(function (o) { tbody.appendChild(buildBrowseRow(o)); });
      // One fetch feeds both surfaces - but the picker filters harder than the table
      // does (see chatFillModels): BROWSE is a market listing, and listing a band that
      // is off air is honest there. Offering it as something to talk to is not.
      chatFillModels();
    }).catch(function (e) {
      // The market half of the picker is fed from this call, so a failed browse has to
      // reach it too. The LOCAL half is unaffected and still fills: a broker that is
      // down is no reason to hide the models running on this very machine.
      lastOffers = [];
      chatFillModels();
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
    //
    // ZERO IS NOT A MEASUREMENT. The wire contract says so in as many words - Offer.TTFTMs
    // is "probe-measured TTFT (ms; 0 = unmeasured)" and CheapTPS likewise - so a station
    // the prober has not reached yet arrives with tps 0 and ttft_ms 0. Rendering those as
    // "0" and "0ms" told the operator this station was measured and is infinitely fast,
    // which is the opposite of the truth and exactly backwards for choosing between
    // stations. The dial has always got this right (tpsCell renders "- t/s"); this table
    // did not. A null check alone does not catch it: 0 is not null.
    tr.appendChild(el("td", "num", o.tps > 0 ? Math.round(o.tps) : "—"));
    tr.appendChild(el("td", "num", o.ttft_ms > 0 ? Math.round(o.ttft_ms) + "ms" : "—"));
    tr.appendChild(el("td", "num", o.ctx ? Math.round(o.ctx / 1024) + "k" : "—"));
    tr.appendChild(el("td", null, o.region || "—"));
    return tr;
  }

  /* CHAT -------------------------------------------------------------------
     The console's chat surface (founder 2026-08-20). The conversation lives HERE,
     in the page, and is posted whole each turn: the console is a live twin of a
     node, not a chat host, and a server-side transcript would be one more place a
     private conversation could sit.

     One turn at a time on purpose. The relay bills per turn and the receipt under
     each answer has to belong to a turn you actually watched happen; letting two
     run at once would put two receipts in flight with nothing tying them to the
     questions that caused them. */
  var chatTurns = [];   // [{role, content}] - exactly what the API takes
  var chatBusy = false;

  function chatFlow() { return $("chat-flow"); }

  function chatScroll() {
    var f = chatFlow();
    if (f) f.scrollTop = f.scrollHeight;
  }

  // chatAppend paints one turn. Text goes in via textContent (never innerHTML) -
  // a model's reply is untrusted input and must never be able to inject markup
  // into the console that holds the operator's key.
  function chatAppend(role, text, cls) {
    show($("chat-empty"), false);
    var turn = el("div", "chat-turn " + (cls || ""));
    turn.appendChild(el("div", "chat-role", role));
    turn.appendChild(el("div", "chat-body", text));
    chatFlow().appendChild(turn);
    chatScroll();
    return turn;
  }

  /* A FAILED TURN GETS TWO LINES: what went wrong, and what to do about it.
     "the station returned status 504 with no reply" was the founder's dead end - it names
     a number, blames "the station", and leaves nowhere to go. The server now maps it to
     the same sentence the TUI shows ("no station is serving grok-4.3 right now (504)" -
     harness.ShortFailure, shared code, so the two surfaces cannot drift) and sends the
     remedy alongside it. The hint is a separate element rather than more prose: it is the
     line the reader acts on, and it should be findable without reading the first one. */
  function chatAppendError(text, hint) {
    var turn = chatAppend("error", text || "the turn failed", "chat-turn--err");
    if (hint) turn.appendChild(el("div", "chat-err-hint", hint));
    chatScroll();
    return turn;
  }

  /* The per-turn RECEIPT is not shown on an agent turn yet: a turn is now many relayed
     calls (one per model step, plus any subagent's), so a single "what this cost" line
     would have to be a rollup the server does not stream yet. Showing one call's numbers
     and calling it the turn's cost would understate it, which is the one direction this
     console must never round. The harness already computes the rollup
     (Loop.TurnReceipt); surfacing it here is the next step. */

  // The working row: the Wave Spectrum carrier, the browser twin of the TUI's
  // sweep. Indeterminate on purpose - a relayed turn has no honest "% done".
  function chatWorking() {
    var w = el("div", "chat-turn chat-working");
    w.appendChild(el("span", "chat-carrier", "∿∿∿∿∿∿∿∿∿∿∿∿"));
    w.appendChild(el("span", "mono", "on air…"));
    chatFlow().appendChild(w);
    chatScroll();
    return w;
  }

  function chatSetBusy(on) {
    chatBusy = on;
    var b = $("chat-send"), i = $("chat-input");
    if (b) b.disabled = on;
    if (i) i.disabled = on;
    if (!on && i) i.focus();
  }

  /* THE PICKER. Two groups, and only what is ONLINE.
     ─────────────────────────────────────────────────────────────────────────
     Founder, 2026-08-22: "it should use the local models or list them in a category as
     local, and in another category showing the open market models, and it should only
     show the ones online, it should maybe show more detail."

     What was here before was one flat list of every band the broker had ever mentioned,
     built from /api/browse alone and ignoring the `online` flag the feed carries. The
     comment above it claimed "the picker can never offer something there is no way to
     send to", which was simply FALSE - it is how chatting with grok-4.3 returned "the
     station returned status 504 with no reply" over and over. The claim is gone and the
     filtering that would have made it true is here instead.

     LOCAL is this machine's own catalog - the same rows the SHARE tab renders, arriving
     on the snapshot stream. A local pick is routed DIRECT at the server that serves it
     (the server resolves the endpoint and its key; see agent.go), never relayed through
     the broker and back to this same box. That is what makes the group offerable at all:
     a category that 504s is the bug, not the feature.

     OPEN MARKET is the broker's discover feed, filtered to bands that are actually on
     air right now.

     Both groups drop VOICE models (tts/stt): they cannot hold a conversation, and
     listing one is an invitation to a turn that can only fail. The TUI's picker draws
     exactly these two groups under exactly these two rules. */
  var CHAT_LOCAL = "local:", CHAT_MARKET = "market:";

  function chatIsVoice(modality) { return modality === "tts" || modality === "stt"; }

  // chatLocalRows are this node's own models that a turn can actually be sent to. A row
  // with no upstream has nothing behind it - offering it would trade a broker 504 for a
  // local one.
  function chatLocalRows() {
    var rows = (lastSnapshot && lastSnapshot.rows) || [];
    return rows.filter(function (r) {
      return r.model && r.upstream && !chatIsVoice(r.modality);
    });
  }

  // chatMarketOffers are the bands the broker says are ON AIR. `online` is the field, and
  // it was there all along - nothing else in the feed answers "will this answer me?".
  function chatMarketOffers() {
    return (lastOffers || []).filter(function (o) {
      return o.model && o.online === true && !chatIsVoice(o.modality);
    });
  }

  /* ABSENCE RENDERS AS ABSENCE.
     Every number below is omitted when it was never measured, because a printed zero
     reads as a measurement: `ttft_ms: 0` means nobody timed it, not that it was instant,
     and a 0 tok/s band is one nobody has clocked, not a stopped one. Where there is a
     column to hold it (the detail line under the composer) absence is the em dash the
     rest of this console uses; where there is not (a one-line <option>) it is left out
     rather than guessed at. `ctx_estimated` gets the ≈ the SHARE table and `roger detect`
     already use - a default window, not a detected one. */
  function chatCtxLabel(ctx, estimated) {
    if (!ctx) return "";
    return Math.round(ctx / 1024) + "k" + (estimated ? "≈" : "");
  }

  function chatOptionLabel(entry) {
    var bits = [];
    if (entry.local) {
      // A local model is deliberately NEVER priced: there is no price, and printing one
      // would be a false claim about money.
      if (entry.ctx) bits.push(chatCtxLabel(entry.ctx, entry.ctxEstimated));
      if (entry.quant) bits.push(entry.quant);
    } else {
      bits.push(entry.free ? "FREE" : "$" + entry.priceOut + "/1M out");
      if (entry.ctx) bits.push(chatCtxLabel(entry.ctx, entry.ctxEstimated));
      if (entry.tps) bits.push(Math.round(entry.tps) + " tok/s");
      if (entry.signal) bits.push(signalBars(entry.signal));
      if (entry.verified) bits.push("◆");
    }
    return entry.model + (bits.length ? "  ·  " + bits.join("  ·  ") : "");
  }

  // chatEntries is the picker's whole content, LOCAL first. Local leads because it is the
  // route with no broker, no meter and no wallet in it - the TUI puts the market first
  // because it opens on the marketplace; the console opens on CHAT.
  function chatEntries() {
    var out = [];
    chatLocalRows().forEach(function (r) {
      out.push({
        local: true, model: r.model, key: CHAT_LOCAL + r.model,
        ctx: r.ctx, ctxEstimated: !!r.ctx_estimated,
        quant: r.quant || "", weights: r.weights || "", variant: r.variant || "",
        upstream: r.upstream, onAir: !!r.on_air, private: !!r.private
      });
    });
    chatMarketOffers().forEach(function (o) {
      out.push({
        local: false, model: o.model, key: CHAT_MARKET + o.model,
        ctx: o.ctx, ctxEstimated: !!o.ctx_estimated,
        priceOut: o.price_out, free: !!o.free_now || (Number(o.price_out) || 0) === 0,
        tps: Number(o.tps) || 0, ttft: Number(o.ttft_ms) || 0,
        signal: Number(o.signal) || 0, verified: !!o.verified,
        node: o.node_id || "", region: o.region || "", hw: o.hw || ""
      });
    });
    return out;
  }

  var chatEntryByKey = {};
  var chatPickerSig = null;

  // chatSig is the picker's content reduced to a string, so a rebuild can be skipped when
  // nothing in it changed. The snapshot stream ticks about once a second and the LOCAL
  // group is built from it; rebuilding the <select> on every tick would collapse the
  // dropdown under an operator who had it open, roughly forever.
  function chatSig(entries) {
    return entries.map(function (e) { return e.key + "|" + chatOptionLabel(e); }).join("\n");
  }

  // chatFillModels rebuilds the picker from the two sources it already holds. Selection
  // survives a rebuild; a pick that has gone away falls back to the first entry.
  function chatFillModels(force) {
    var sel = $("chat-model");
    if (!sel) return;
    var keep = sel.value;
    var entries = chatEntries();
    var sig = chatSig(entries);
    if (!force && sig === chatPickerSig) return;
    chatPickerSig = sig;
    // Cleared by removing children rather than by innerHTML: the chat block bans every
    // HTML-writing sink outright (chat_test.go), because the one place a reply could
    // slip markup into this console is worth more than the convenience of one
    // assignment. A blanket ban is enforceable; "innerHTML but only for clearing" is
    // not, and the next edit is where it stops being for clearing.
    while (sel.firstChild) sel.removeChild(sel.firstChild);
    chatEntryByKey = {};
    var groups = {};
    entries.forEach(function (e) {
      chatEntryByKey[e.key] = e;
      var label = e.local
        ? "LOCAL · this machine · direct, not through the broker"
        : "OPEN MARKET · relayed through the broker";
      if (!groups[label]) {
        groups[label] = el("optgroup");
        groups[label].label = label;
        sel.appendChild(groups[label]);
      }
      var opt = el("option", null, chatOptionLabel(e));
      opt.value = e.key;
      groups[label].appendChild(opt);
    });

    if (!entries.length) {
      // AN EMPTY PICKER EXPLAINS ITSELF. Filtering to "only what is online" can empty the
      // list, and a blank dropdown next to "pick a band first" is an instruction the user
      // cannot follow - which is the whole reason this line exists.
      var none = el("option", null, chatEmptyReason());
      none.value = "";
      sel.appendChild(none);
      sel.disabled = true;
    } else {
      sel.disabled = false;
    }
    if (keep && chatEntryByKey[keep]) sel.value = keep;
    if (!sel.value && sel.options.length) sel.selectedIndex = 0;
    chatBandDetail();
    chatFoot();
  }

  // chatEmptyReason says WHY the list is empty, which is three different situations that
  // a single "no band on the dial" used to blur together.
  function chatEmptyReason() {
    var offers = (lastOffers || []).length;
    var rows = (lastSnapshot && (lastSnapshot.rows || []).length) || 0;
    if (offers) return "every band on the market is off air right now";
    if (rows) return "nothing to chat with - the market is empty and this machine's models are voice-only or unreachable";
    // Detection runs in the background for ~15s after launch, so "empty" this early is
    // very often "not finished". Saying so beats an empty list that reads as a verdict.
    return "nothing online yet - detection is still running; then re-detect on SHARE or check BROWSE";
  }

  function chatSelected() {
    var sel = $("chat-model");
    return (sel && chatEntryByKey[sel.value]) || null;
  }

  /* THE DETAIL LINE, under the composer: everything genuinely known about the band you
     are about to spend a turn on. This is where absence gets room to be shown AS absence
     - an em dash in a labelled slot, the same reading the SHARE table's variant cell and
     `roger detect` give it. The <option> above can only carry one line, so it carries the
     facts that fit and this carries the rest. */
  function chatBandDetail() {
    var line = $("chat-band");
    if (!line) return;
    while (line.firstChild) line.removeChild(line.firstChild);
    var e = chatSelected();
    if (!e) { show(line, false); return; }
    show(line, true);

    function slot(label, value, cls) {
      if (line.firstChild) line.appendChild(document.createTextNode("  ·  "));
      line.appendChild(el("span", "band-k", label + " "));
      line.appendChild(el("span", cls || "band-v", value));
    }
    if (e.local) {
      slot("route", "direct to " + e.upstream.replace(/^https?:\/\//, "").replace(/\/v1\/chat\/completions$/, ""));
      slot("ctx", chatCtxLabel(e.ctx, e.ctxEstimated) || "—");
      var v = [e.quant, e.weights ? "by " + e.weights : "", e.variant].filter(Boolean).join(" · ");
      slot("build", v || "—");
      slot("cost", "nothing - not metered");
      if (e.onAir) slot("also", e.private ? "on air on your private band" : "on air on the open market");
    } else {
      slot("price", e.free ? "FREE" : "$" + e.priceOut + "/1M out");
      slot("ctx", chatCtxLabel(e.ctx, e.ctxEstimated) || "—");
      // A zero here means UNMEASURED, in both fields. The broker sends 0 for a band no
      // probe has clocked yet, and printing "0 tok/s" or "0ms" would read as a reading.
      slot("tok/s", e.tps ? Math.round(e.tps) : "—");
      slot("ttft", e.ttft ? Math.round(e.ttft) + "ms" : "—");
      slot("signal", e.signal ? signalBars(e.signal) : "—", e.signal ? "signal " + signalClass(e.signal) : "band-v");
      slot("station", e.node || "—");
      slot("region", e.region || "—");
      slot("hw", e.hw || "—");
      if (e.verified) slot("lineage", "◆ verified");
    }
  }

  // The foot names the ROUTE, because the two routes differ in the things a user cares
  // about most: one costs money and hides who you are, the other costs nothing and never
  // leaves the machine. Reading the wrong one would misjudge both the speed and the bill.
  function chatFoot(msg) {
    var f = $("chat-foot");
    if (!f) return;
    if (msg) { f.textContent = msg; return; }
    var e = chatSelected();
    f.textContent = !e
      ? "put a model on air on SHARE, or tune in a band on BROWSE, to start a conversation"
      : e.local
        ? "runs on this machine · direct, not through the broker · nothing metered"
        : "relayed through the broker · the station never sees who you are";
  }

  function chatSend() {
    if (chatBusy) return;
    var input = $("chat-input"), sel = $("chat-model");
    var text = chatExpandPastes(input.value || "").trim();
    if (!text) return;
    var picked = chatSelected();
    if (!sel || !sel.value || !picked) { toast("pick a band first", "err"); return; }

    chatAppend("you", text, "chat-turn--you");
    chatTurns.push({ role: "user", content: text });
    input.value = "";
    chatPastes = []; // sent: the held blocks are in the message now
    chatRenderHeld();
    chatAutoGrow();
    chatSetBusy(true);
    var working = chatWorking();

    chatRunAgent(picked, text, working);
  }

  /* A fetch that never reached the server rejects with the BROWSER's wording, and every
     browser words it differently and none of them usefully: Firefox says "NetworkError
     when attempting to fetch resource", Chrome says "Failed to fetch". Neither tells the
     operator the one thing that is almost always true - this page outlived the `roger
     webui` process that served it. Left verbatim it reads like the model or the band
     failed, so the natural next move is to retry a request that cannot succeed.

     A page cannot distinguish "server gone" from "cable unplugged" (the fetch spec
     deliberately hides that), so this does not claim to know which. It names the likely
     cause and the check, and keeps the browser's own words so a real network fault is
     still diagnosable. */
  function chatFetchErrText(err) {
    var raw = (err && err.message) || "";
    var networkish = /networkerror|failed to fetch|load failed|network request failed/i.test(raw);
    if (!networkish) return raw || "the turn did not go through";
    return "could not reach the console server - if you left this tab open, the " +
      "`roger webui` process that served it has probably stopped. Start it again and " +
      "reload. (" + raw + ")";
  }

  /* THE AGENT TURN. Streams newline-delimited JSON off the POST response - EventSource
     is GET-only, and a turn that spends the operator's money must not be reachable by a
     GET, which is the rule every other write on this console follows.

     Tool calls render as a FOLDED box, the same shape the TUI uses: a turn that touched
     eleven files should read as one row of machinery, not eleven. Click to open. */
  function chatRunAgent(entry, text, working) {
    var model = entry.model;
    var box = null;      // the current machinery box, if any
    var answered = false;
    fetch(withToken("/api/agent"), {
      method: "POST",
      headers: { "X-Roger-Token": TOKEN, "Content-Type": "application/json" },
      // `local` says WHICH of the two groups this pick came from. The endpoint is not sent
      // - the server resolves it (and the bearer key it may need) from the node's own
      // catalog, so no credential is ever handed to a page. A model id alone would be
      // ambiguous: the same name can be a market band and a server on this box.
      body: JSON.stringify({ model: model, message: text, local: !!entry.local })
    }).then(function (res) {
      if (!res.ok || !res.body) {
        return res.text().then(function (t) { throw new Error(chatErrText(t, res)); });
      }
      var reader = res.body.getReader(), dec = new TextDecoder(), buf = "";
      return (function pump() {
        return reader.read().then(function (r) {
          if (r.done) { flushLine(buf, true); return; }
          buf += dec.decode(r.value, { stream: true });
          var lines = buf.split("\n");
          buf = lines.pop();            // the tail may be a partial line
          lines.forEach(function (l) { flushLine(l, false); });
          return pump();
        });
      })();
    }).then(function () {
      working.remove();
      if (!answered) chatAppend(model, "(the agent finished with no text)", "chat-turn--err");
    }).catch(function (err) {
      working.remove();
      chatTurns.pop();  // a failed turn leaves history clean, or it is re-sent and re-billed
      chatAppend("error", chatFetchErrText(err), "chat-turn--err");
    }).then(function () { chatSetBusy(false); });

    function flushLine(line, last) {
      line = (line || "").trim();
      if (!line) return;
      var e;
      try { e = JSON.parse(line); } catch (_) { return; }
      // A SUBAGENT's steps stay out of the flow, exactly as in the TUI: a child can make
      // a dozen calls to answer one question, and pouring those in is the noise the fold
      // exists to remove.
      if (e.agent) return;
      switch (e.kind) {
        case "tool_call":
          box = chatToolBox(box);
          chatToolRow(box, e);
          break;
        case "tool_result":
          chatSettleTool(box, e);
          break;
        case "assistant":
          // INTERIM prose, emitted alongside tool calls - the model narrating between
          // steps. It is shown but NOT recorded as the turn's answer: the answer arrives
          // as `final`, which is what EventFinal means.
          box = null;
          if (e.text) chatAppend(model, e.text, "chat-turn--reply");
          break;
        case "notice":
          box = null;
          if (e.text) chatAppend("", e.text, "chat-turn--note");
          break;
        case "final":
          box = null;
          if (e.text) {
            answered = true;
            chatTurns.push({ role: "assistant", content: e.text });
            chatAppend(model, e.text, "chat-turn--reply");
          }
          break;
        case "receipt":
          box = null;
          chatShowReceipt(e);
          break;
        case "error":
          box = null;
          chatAppendError(e.text, e.hint);
          answered = true;
          break;
      }
      if (last) { /* nothing: the tail is handled by the branches above */ }
    }
  }

  /* THE TURN RECEIPT. A turn is many relayed calls now - one per model step, plus any
     subagent's - so this is their SUM, which is the only honest turn total: the root's
     own numbers exclude its children and would understate.

     Zeros are omitted rather than printed. A displayed 0 reads as a measurement ("this
     cost nothing"), and a free local band and a turn whose cost never arrived are not
     the same thing. */
  function chatShowReceipt(e) {
    var parts = [];
    if (e.calls) parts.push(e.calls + (e.calls === 1 ? " call" : " calls"));
    if (e.steps) parts.push(e.steps + (e.steps === 1 ? " step" : " steps"));
    if (e.delegated) parts.push(e.delegated + " delegated");
    if (e.tokens_in || e.tokens_out) parts.push("↑" + fmtInt(e.tokens_in) + " ↓" + fmtInt(e.tokens_out));
    if (e.cost) parts.push("$" + Number(e.cost).toFixed(4));
    if (!parts.length) return;
    var row = el("div", "chat-receipt-row");
    row.appendChild(el("span", null, parts.join("  ·  ")));
    if (e.incomplete) {
      // A partial tree is a LOWER BOUND, and saying so is the difference between a
      // receipt and a guess.
      row.appendChild(el("b", null, "incomplete — a delegated task did not finish"));
    }
    chatFlow().appendChild(row);
    chatScroll();
  }

  function chatErrText(body, res) {
    try { var j = JSON.parse(body); if (j && (j.message || j.error)) return j.message || j.error; }
    catch (_) {}
    return res.statusText || "the turn did not go through";
  }

  // One machinery box per run of tool calls. Starts SHUT: the summary is what a reader
  // scans, and the detail is one click away.
  function chatToolBox(existing) {
    if (existing) return existing;
    var box = el("div", "chat-tools-box");
    var lid = el("button", "chat-tools-lid");
    lid.type = "button";
    lid.setAttribute("aria-expanded", "false");
    var caret = el("span", "chat-tools-caret", "▸");
    var label = el("span", "chat-tools-label", "0 tool calls");
    lid.appendChild(caret);
    lid.appendChild(label);
    var rows = el("div", "chat-tools-rows");
    rows.hidden = true;
    lid.addEventListener("click", function () {
      rows.hidden = !rows.hidden;
      caret.textContent = rows.hidden ? "▸" : "▾";
      lid.setAttribute("aria-expanded", rows.hidden ? "false" : "true");
    });
    box.appendChild(lid);
    box.appendChild(rows);
    box._rows = rows;
    box._label = label;
    box._names = [];
    box._done = 0;
    chatFlow().appendChild(box);
    chatScroll();
    return box;
  }

  function chatToolRow(box, e) {
    var row = el("div", "chat-tool-row");
    row.appendChild(el("span", "chat-tool-status", "◐"));
    row.appendChild(el("span", "chat-tool-name", e.tool || "tool"));
    if (e.arg) row.appendChild(el("span", "chat-tool-arg", e.arg));
    box._rows.appendChild(row);
    box._open = row;
    if (e.tool && box._names.indexOf(e.tool) === -1) box._names.push(e.tool);
    chatToolLabel(box);
  }

  function chatSettleTool(box, e) {
    if (!box || !box._open) return;
    var row = box._open;
    var status = row.firstChild;
    status.textContent = e.is_error || e.denied ? "✕" : "✓";
    row.classList.add(e.is_error || e.denied ? "is-error" : "is-ok");
    if (e.result) {
      var d = el("span", "chat-tool-detail", chatResultHint(e));
      row.appendChild(d);
    }
    box._open = null;
    box._done++;
    chatToolLabel(box);
  }

  // The lid counts SETTLED runs and names the tools, the same summary the TUI's lid
  // carries - the names are what a reader scans for ("did it run a search?").
  function chatToolLabel(box) {
    var n = box._done || box._rows.childNodes.length;
    box._label.textContent = n + (n === 1 ? " tool call" : " tool calls") +
      (box._names.length ? " · " + box._names.slice(0, 4).join(", ") : "");
  }

  // A tool's outcome as the ROW reads it. Mirrors the TUI's shortToolFailure, because
  // the two surfaces must describe the same refusal the same way.
  //
  // "denied" means the OPERATOR said no. A guard refusal is the harness applying a rule
  // and says "refused · <reason>" instead - conflating the two is what made a screen of
  // tool calls read as a permissions problem nobody was ever asked about.
  function chatResultHint(e) {
    if (e.denied) return "denied";
    if (e.is_error) {
      var line = (e.result || "").split("\n")[0];
      if (line.indexOf("refused: ") === 0) {
        // Keep the refusal, drop the paragraph of guidance aimed at the model - it is
        // instruction, not news, and it wraps the row across three lines.
        // First SENTENCE END, not any period: a bare "." sliced URLs in half, so
        // "https://rogerai.fyi/..." became "https://rogerai" - the wrong host, reading
        // like a different refusal (founder screenshot). Mirrors shortToolFailure.
        var rest = line.slice("refused: ".length);
        var stop = rest.indexOf(". ");
        if (stop > 0) rest = rest.slice(0, stop);
        return "refused · " + rest.replace(/\.$/, "").slice(0, 70);
      }
      return line.slice(0, 80);
    }
    return chatHumanSize((e.result || "").length);
  }

  /* The plain one-shot relay (/api/chat) is still served and tested - it is the
     simplest possible way to reach a band and useful for a caller that wants no tools.
     The console itself no longer uses it: the chat tab is agentic now, so keeping a
     second, unreferenced path in the page would just be a second thing to keep working. */

  /* LARGE PASTES, held (the same rule the TUI uses - internal/tui/paste.go).
     A browser textarea has the same problem the composer had: it auto-grows, so a
     300-line paste fills 40% of the viewport and pushes the thing you were typing in
     off the screen. A big paste is held and shown as one chip; the real text goes back
     in at send.

     The THRESHOLDS ARE THE TUI'S ON PURPOSE - four lines or 400 bytes. Two surfaces of
     one product that disagree about what counts as a big paste would be a worse bug
     than either one getting the number wrong. */
  var CHAT_PASTE_MIN_LINES = 4;
  var CHAT_PASTE_MIN_BYTES = 400;
  var chatPastes = [];

  function chatBigPaste(t) {
    return t.split("\n").length >= CHAT_PASTE_MIN_LINES || t.length >= CHAT_PASTE_MIN_BYTES;
  }

  function chatHumanSize(n) {
    if (n >= 1048576) return (n / 1048576).toFixed(1) + " MB";
    if (n >= 1024) return (n / 1024).toFixed(1) + " KB";
    return n + " bytes";
  }

  function chatHoldPaste(text) {
    chatPastes.push(text);
    var n = chatPastes.length;
    // Count CONTENT lines: a paste ending in a newline has a trailing empty line that
    // is not a line of anything.
    var lines = text.replace(/\n+$/, "").split("\n").length;
    return lines >= CHAT_PASTE_MIN_LINES
      ? "[Pasted text #" + n + " +" + lines + " lines]"
      : "[Pasted text #" + n + " " + chatHumanSize(text.length) + "]";
  }

  // Put the held text back before sending. A chip with nothing behind it - typed, or
  // edited to a number that was never held - is left exactly as written, because
  // substituting nothing there would silently delete what the user wrote.
  function chatExpandPastes(s) {
    if (!chatPastes.length) return s;
    return s.replace(/\[Pasted text #(\d+)[^\]]*\]/g, function (ref, num) {
      var i = parseInt(num, 10);
      return (i >= 1 && i <= chatPastes.length) ? chatPastes[i - 1] : ref;
    });
  }

  function chatOnPaste(e) {
    var text = (e.clipboardData || window.clipboardData).getData("text");
    if (!text || !chatBigPaste(text)) return; // small pastes land as themselves
    e.preventDefault();
    var input = $("chat-input");
    var chip = chatHoldPaste(text);
    var at = input.selectionStart, end = input.selectionEnd;
    input.value = input.value.slice(0, at) + chip + input.value.slice(end);
    var pos = at + chip.length;
    input.setSelectionRange(pos, pos);
    chatAutoGrow();
    chatRenderHeld();
  }

  // Show what is being held under the composer. The chip inside the textarea is plain
  // text the user can edit or delete; this line is the console saying what will
  // actually be sent, which is the thing worth being sure about.
  function chatRenderHeld() {
    var host = $("chat-held");
    if (!host) return;
    while (host.firstChild) host.removeChild(host.firstChild);
    if (!chatPastes.length) { show(host, false); return; }
    host.appendChild(el("span", null, chatPastes.length === 1 ? "holding" : "holding"));
    chatPastes.forEach(function (p, i) {
      var lines = p.replace(/\n+$/, "").split("\n").length;
      host.appendChild(el("b", null, "#" + (i + 1) + " · " + lines + " lines · " + chatHumanSize(p.length)));
    });
    show(host, true);
  }

  function chatAutoGrow() {
    var i = $("chat-input");
    if (!i) return;
    i.style.height = "auto";
    i.style.height = Math.min(i.scrollHeight, window.innerHeight * 0.4) + "px";
  }

  function wireChat() {
    var input = $("chat-input"), send = $("chat-send"), model = $("chat-model");
    if (!input) return;
    input.addEventListener("input", chatAutoGrow);
    input.addEventListener("paste", chatOnPaste);
    input.addEventListener("keydown", function (e) {
      // Enter sends, shift+enter is a newline - the convention every chat surface
      // the operator already uses shares.
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); chatSend(); }
    });
    if (send) send.addEventListener("click", chatSend);
    if (model) model.addEventListener("change", function () { chatBandDetail(); chatFoot(); });
  }

  /* TABS ------------------------------------------------------------------- */
  var TABS = ["chat", "share", "account", "browse", "settings"];

  // CHAT IS THE LANDING TAB (founder 2026-08-21: "lets make sure we start the webui on
  // the chat"). The console used to open on SHARE, which is the provider surface - it
  // answered "what am I broadcasting?" to someone who had come to talk to a model. An
  // explicit #hash still wins, so a bookmark to any tab keeps working.
  function tabFromHash() {
    var h = (location.hash || "").replace(/^#/, "").toLowerCase();
    return TABS.indexOf(h) !== -1 ? h : "chat";
  }

  function setTab(name) {
    TABS.forEach(function (n) { show($("panel-" + n), n === name); });
    // CHAT docks its composer to the bottom, so it needs the viewport; the class is
    // what lets the stylesheet give it one without touching any other panel.
    document.body.classList.toggle("is-chat", name === "chat");
    Array.prototype.forEach.call(document.querySelectorAll(".tab"), function (b) {
      b.setAttribute("aria-selected", b.getAttribute("data-tab") === name ? "true" : "false");
    });
    if (name === "account") loadAccount();
    if (name === "browse") loadBrowse();
    if (name === "settings") loadSettings();
    if (name === "chat") {
      // Refresh the market half (the LOCAL half is already live off the snapshot stream),
      // then focus the composer - opening CHAT means you intend to type.
      loadBrowse();
      var i = $("chat-input");
      if (i) i.focus();
    }
  }

  /* 8b. SETTINGS: private bands + per-band spend limits ---------------------
     THE FULLER SURFACE (founder 2026-08-21). Two things lived only in the terminal: the
     PER-BAND spend caps that actually bound what a turn may cost, and private-band
     management, which did not exist here at all - a band minted in the browser became
     unmanageable the moment it existed.

     Both talk to the SAME state the TUI edits (one LimitStore, and the broker for bands),
     so the two windows can never disagree about the operator's money settings. */

  function loadSettings() {
    apiGet("/api/bands").then(function (d) {
      show($("settings-disabled"), d && d.configured === false);
      renderBands((d && d.bands) || []);
    }).catch(function (e) { toast(e.message, "err"); });
    apiGet("/api/limits").then(function (d) {
      renderLimits((d && d.limits) || []);
    }).catch(function (e) { toast(e.message, "err"); });
  }

  function renderBands(rows) {
    var body = $("bands-rows");
    if (!body) return;
    body.innerHTML = "";
    if (!rows.length) {
      var er = el("tr", "empty-row");
      var ec = el("td", null, "No private bands yet \u2014 hide a model on SHARE to mint one.");
      ec.colSpan = 5; er.appendChild(ec); body.appendChild(er);
      return;
    }
    rows.forEach(function (b) {
      var tr = el("tr");
      // The cosmetic DIAL only. Only the hash of the code is stored, so there is nothing
      // else to show and a placeholder would read as the real thing.
      tr.appendChild(el("td", "mono", b.display || b.id));
      tr.appendChild(el("td", null, b.label || "\u2014"));
      // WHERE it lives. "another machine" and "here, server stopped" have completely
      // different remedies, so they must not render the same.
      var where = b.model || (b.here ? b.node_id : "another machine");
      tr.appendChild(el("td", "mono small", where));
      var st = el("td");
      st.appendChild(el("span", b.status === "active" ? "pill live" : "pill", b.status || "\u2014"));
      tr.appendChild(st);

      var act = el("td", "row-actions");
      if (b.status === "active") {
        act.appendChild(bandBtn("name", "name", b.id, "btn small ghost"));
        act.appendChild(bandBtn("rotate", "new code", b.id, "btn small"));
        act.appendChild(bandBtn("revoke", "revoke", b.id, "btn small danger"));
      } else {
        // A revoked row is history. Before `forget` existed nothing could remove it, so
        // dead entries piled up around the live band.
        act.appendChild(bandBtn("forget", "forget", b.id, "btn small ghost"));
      }
      act.setAttribute("data-node", b.node_id || "");
      tr.appendChild(act);
      body.appendChild(tr);
    });
  }

  function bandBtn(act, label, id, cls) {
    var b = el("button", cls, label);
    b.setAttribute("data-band-act", act);
    b.setAttribute("data-band", id);
    return b;
  }

  function renderLimits(rows) {
    var body = $("limits-rows");
    if (!body) return;
    body.innerHTML = "";
    if (!rows.length) {
      var er = el("tr", "empty-row");
      var ec = el("td", null, "No bands yet."); ec.colSpan = 5;
      er.appendChild(ec); body.appendChild(er);
      return;
    }
    rows.forEach(function (r) {
      var tr = el("tr");
      var nameCell = el("td", "mono");
      nameCell.appendChild(document.createTextNode(r.model));
      if (r.on_air) nameCell.appendChild(el("span", "pill live", "on air"));
      tr.appendChild(nameCell);
      tr.appendChild(limitCell(r.model, "max_out", r.max_out));
      tr.appendChild(limitCell(r.model, "min_tps", r.min_tps));
      tr.appendChild(quantCell(r.model, r.quants));
      var act = el("td", "row-actions");
      var save = el("button", "btn small", "save");
      save.setAttribute("data-limit-save", r.model);
      act.appendChild(save);
      tr.appendChild(act);
      body.appendChild(tr);
    });
  }

  // limitCell renders an editable cap. An UNSET cap is an EMPTY box, never 0 - a printed
  // zero reads as "refuse everything", which is the opposite of no cap.
  function limitCell(model, field, v) {
    var td = el("td", "num");
    var i = el("input", "inp tiny");
    i.type = "number"; i.min = "0"; i.step = field === "max_out" ? "0.01" : "1";
    i.placeholder = "no cap";
    i.value = Number(v) > 0 ? String(v) : "";
    i.setAttribute("data-limit", model);
    i.setAttribute("data-field", field);
    td.appendChild(i);
    return td;
  }

  // quantCell edits the STANDING QUANT RULE - the compression labels this band may be
  // routed to. Empty means "any", which is not the same as "none": a band with no rule
  // accepts every quant, and a station that declared none is accepted by any rule.
  //
  // This column exists because the field did not used to be here, and its absence was not
  // cosmetic: the save posted only the two numbers, so editing a price cap in the browser
  // silently destroyed a rule set on the terminal's band card.
  function quantCell(model, quants) {
    var td = el("td");
    var i = el("input", "inp");
    i.type = "text";
    i.placeholder = "any";
    i.value = (quants || []).join(", ");
    i.setAttribute("data-limit", model);
    i.setAttribute("data-field", "quants");
    i.title = "space- or comma-separated, e.g. Q4_K_M IQ4_XS 4bit - empty means any";
    td.appendChild(i);
    return td;
  }

  function saveLimit(model) {
    function val(field) {
      var i = document.querySelector('[data-limit="' + CSS.escape(model) + '"][data-field="' + field + '"]');
      var n = i && i.value !== "" ? Number(i.value) : 0;
      return isFinite(n) && n > 0 ? n : 0;
    }
    // The quant field is ALWAYS sent from this form, because this form can see it. A
    // surface that cannot edit the rule must omit the key entirely so the server leaves
    // it alone; that is what the pointer on the wire is for.
    var qi = document.querySelector('[data-limit="' + CSS.escape(model) + '"][data-field="quants"]');
    // Split on spaces OR commas: the terminal's own band editor joins quant labels with a
    // space (band_config.go), so an operator pasting "Q4_K_M IQ4_XS" from there would
    // otherwise become one bogus label that matches no station, silently.
    var quants = (qi ? qi.value : "").split(/[\s,]+/).map(function (x) { return x.trim(); })
      .filter(function (x) { return x !== ""; });
    apiPost("/api/limits", { model: model, max_out: val("max_out"), min_tps: val("min_tps"), quants: quants })
      .then(function () { toast("saved " + model); loadSettings(); })
      .catch(function (e) { toast(e.message, "err"); });
  }

  function bandAction(act, id, node) {
    if (act === "name") {
      var name = window.prompt("Name this band (empty clears it)", "");
      if (name === null) return;
      apiPost("/api/bands/label", { id: id, label: name })
        .then(function () { toast("named"); loadSettings(); })
        .catch(function (e) { toast(e.message, "err"); });
      return;
    }
    if (act === "rotate") {
      // The cost is the whole difference from a move, so it is the confirm.
      if (!window.confirm("Mint a new code for this band?\n\nThe current code stops working immediately - everyone you gave it to is cut off until you send them the new one. The band keeps its dial, model and slot.")) return;
      apiPost("/api/bands/rotate", { id: id }).then(function (d) {
        // SHOWN ONCE: the broker keeps only the hash, so this is the only moment it
        // exists anywhere.
        $("band-code-value").textContent = (d && d.code) || "";
        $("band-code-note").textContent = "the old code stopped working \u2014 this replaces it";
        show($("band-code-card"), true);
        loadSettings();
      }).catch(function (e) { toast(e.message, "err"); });
      return;
    }
    if (act === "revoke") {
      if (!window.confirm("Revoke this band?\n\nIts code stops working immediately and can never be revived. Everyone tuned in is cut off. To keep the code and change the model instead, move it.")) return;
      apiPost("/api/bands/revoke", { id: id, model: node })
        .then(function () { toast("revoked"); loadSettings(); })
        .catch(function (e) { toast(e.message, "err"); });
      return;
    }
    if (act === "forget") {
      apiPost("/api/bands/forget", { id: id })
        .then(function () { toast("forgotten"); loadSettings(); })
        .catch(function (e) { toast(e.message, "err"); });
    }
  }

  /* 9. BOOT / WIRING ------------------------------------------------------- */
  function wire() {
    wireChat();
    // tabs + any [data-goto] / [data-tab] click
    document.addEventListener("click", function (e) {
      var tabBtn = e.target.closest("[data-tab]");
      if (tabBtn) { setTab(tabBtn.getAttribute("data-tab")); return; }
      var gotoBtn = e.target.closest("[data-goto]");
      if (gotoBtn) { setTab(gotoBtn.getAttribute("data-goto")); return; }
      var closeBtn = e.target.closest("[data-close]");
      if (closeBtn) { closeModal(); return; }
    });

    // settings: band actions, limit saves, the one-time code card
    var bandsRows = $("bands-rows");
    if (bandsRows) bandsRows.addEventListener("click", function (e) {
      var btn = e.target.closest("button[data-band-act]");
      if (!btn) return;
      var node = btn.parentNode ? btn.parentNode.getAttribute("data-node") : "";
      bandAction(btn.getAttribute("data-band-act"), btn.getAttribute("data-band"), node || "");
    });
    var limitRows = $("limits-rows");
    if (limitRows) limitRows.addEventListener("click", function (e) {
      var btn = e.target.closest("button[data-limit-save]");
      if (btn) saveLimit(btn.getAttribute("data-limit-save"));
    });
    var refresh = $("btn-settings-refresh");
    if (refresh) refresh.addEventListener("click", loadSettings);
    var bcc = $("band-code-copy");
    if (bcc) bcc.addEventListener("click", function () {
      var v = $("band-code-value").textContent || "";
      if (navigator.clipboard) navigator.clipboard.writeText(v).then(function () { toast("copied"); });
    });
    var bcd = $("band-code-dismiss");
    if (bcd) bcd.addEventListener("click", function () { show($("band-code-card"), false); });

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
    // Deep-link the tab from the hash (#chat, #browse, …) so a console URL can point
    // at a surface rather than always landing on SHARE. Unknown or absent falls back
    // to SHARE, which is what the operator opened this for most of the time.
    setTab(tabFromHash());
    window.addEventListener("hashchange", function () { setTab(tabFromHash()); });
    // one-shot state for an instant paint, then live stream
    apiGet("/api/state").then(renderSnapshot).catch(function (e) {
      if (e.status === 403) { show($("app"), false); show($("auth-gate"), true); }
    });
    connectSSE();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
