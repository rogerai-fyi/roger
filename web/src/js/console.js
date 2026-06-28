// Console - the live operational view. The recent lineage of requests plus the
// day's running counters, driven by the broker's console feed:
//
//   GET /console -> {
//     role: "owner" | "consumer",
//     events: [ { request_id, ts, model, node, tokens_in, tokens_out,
//                 cost, earned, success } ],   // newest first
//     counters: {
//       requests_today,
//       // owner:   earned_today, active_nodes, active_bands
//       // consumer: spend_today
//     }
//   }
//
// An OWNER (a bound operator account) sees what their nodes served: earned per
// row, active nodes/bands. A CONSUMER sees their consumption: cost per row,
// spend today. Login gate matches the account pages (/account confirms session;
// a 401 from /console -> login). Honest-empty: real receipts only, no fabricated
// rows. Mono numerals, hairlines, one red glint (the on-air dot / fail mark).
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";

  // credentialed fetch: the session cookie IS the owner auth (CORS echoes the origin).
  function api(path, opts) {
    opts = opts || {};
    opts.credentials = "include";
    return fetch(BROKER + path, opts);
  }

  function $(id) { return document.getElementById(id); }
  function show(id) { var el = $(id); if (el) el.hidden = false; }
  function hide(id) { var el = $(id); if (el) el.hidden = true; }
  function text(id, v) { var el = $(id); if (el) el.textContent = v; }
  function n0(v) { return typeof v === "number" && isFinite(v) ? v : 0; }

  function cr(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    if (window.RogerFmt) return RogerFmt.usdSigned(n); // canonical money renderer (web == CLI/TUI)
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
  // compact count for tight table cells (k/M/B/T via the shared formatter); local fallback.
  function kfmt(n) {
    n = n0(n);
    if (window.RogerFmt) return RogerFmt.count(n);
    if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1).replace(/\.0$/, "") + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1).replace(/\.0$/, "") + "k";
    return String(Math.round(n));
  }
  // A growing stat tile: compact form + exact value on hover/tap; fallback to num().
  function bindNum(id, v) {
    var el = $(id); if (!el) return;
    if (window.RogerFmt) RogerFmt.bind(el, v);
    else el.textContent = num(v);
  }
  // relative time for the log ("12s", "4m", "3h", "2d"); falls back to a date.
  function ago(ts) {
    var s = Math.max(0, Math.floor(Date.now() / 1000 - n0(ts)));
    if (s < 60) return s + "s";
    if (s < 3600) return Math.floor(s / 60) + "m";
    if (s < 86400) return Math.floor(s / 3600) + "h";
    if (s < 7 * 86400) return Math.floor(s / 86400) + "d";
    return new Date(n0(ts) * 1000).toLocaleDateString("en-US", { month: "short", day: "numeric" });
  }
  // short receipt id (the chain id can be long; show a head fragment, full on hover).
  function shortId(id) {
    if (!id) return "-";
    return id.length > 12 ? id.slice(0, 12) : id;
  }

  function cell(cls, txt, title) {
    var td = document.createElement("td");
    if (cls) td.className = cls;
    td.textContent = txt;
    if (title) td.title = title;
    return td;
  }

  // ---- one lineage row: time · model · node · tokens · cost/earned · id · ok/fail
  function eventRow(e, isOwner) {
    var tr = document.createElement("tr");
    tr.className = "cn-row";

    // status pill (◉ ok / ◯ fail) - the one red glint lives on a failure.
    var st = document.createElement("td");
    st.className = "cn-st";
    var dot = document.createElement("span");
    dot.className = "cn-dot " + (e.success ? "cn-dot--ok" : "cn-dot--fail");
    dot.textContent = e.success ? "◉" : "○";
    dot.title = e.success ? "ok" : "failed";
    st.appendChild(dot);
    tr.appendChild(st);

    tr.appendChild(cell("cn-model", e.model || "(unknown)", e.model || ""));
    tr.appendChild(cell("cn-node", e.node || "-", e.node || ""));
    tr.appendChild(cell("num cn-tok",
      kfmt(e.tokens_in) + " / " + kfmt(e.tokens_out),
      n0(e.tokens_in) + " in / " + n0(e.tokens_out) + " out tokens"));
    tr.appendChild(cell("num cn-money",
      isOwner ? cr(n0(e.earned)) : cr(n0(e.cost))));
    tr.appendChild(cell("cn-id", shortId(e.request_id), e.request_id || ""));
    tr.appendChild(cell("num cn-time", ago(e.ts),
      new Date(n0(e.ts) * 1000).toLocaleString()));
    return tr;
  }

  function render(d) {
    var role = (d && d.role) || "consumer";
    var isOwner = role === "owner";
    var events = (d && d.events) || [];
    var c = (d && d.counters) || {};

    // role line
    text("role", isOwner ? "Operator console" : "Consumer console");

    // ---- stat tiles ---- (requests can grow large -> compact + exact reveal; nodes/bands
    // stay plain, they're small counts)
    bindNum("ctrReq", n0(c.requests_today));
    if (isOwner) {
      text("ctrMoney", cr(n0(c.earned_today)));
      text("ctrMoneyK", "Earned today");
      text("ctrNodes", num(n0(c.active_nodes)));
      text("ctrBands", num(n0(c.active_bands)));
      show("tileNodes");
      show("tileBands");
      // owner header column = earned
      text("colMoney", "Earned");
      // owner-only: the per-model price + schedule manager.
      loadModels();
    } else {
      text("ctrMoney", cr(n0(c.spend_today)));
      text("ctrMoneyK", "Spend today");
      text("colMoney", "Cost");
    }
    show("counters");

    // ---- lineage log ----
    if (!events.length) {
      show(isOwner ? "logEmptyOwner" : "logEmptyConsumer");
      return;
    }
    var body = $("logRows");
    events.forEach(function (e) { body.appendChild(eventRow(e, isOwner)); });
    show("logWrap");
  }

  // =====================================================================
  // Models & pricing (owner only): per-model base price + time-of-use windows.
  // GET  /provider/models -> { models:[ row ], ceiling_in, ceiling_out }
  // POST /provider/models { node, model, price_in, price_out, schedule:[win] }
  // POST /provider/models { node, model, clear:true }
  // row: { node, model, online, ctx, price_in, price_out, free, schedule,
  //        overridden, active_in, active_out, active_free }
  // win: { days:[int], start:"HH:MM", end:"HH:MM", price_in, price_out, free }
  // =====================================================================
  var CEIL = { in: 50, out: 100 };

  function priceStr(n) {
    n = n0(n);
    if (n === 0) return "0";
    var s = n.toFixed(6).replace(/0+$/, "").replace(/\.$/, "");
    return s;
  }
  function fnum(v) { var n = parseFloat(v); return isFinite(n) && n >= 0 ? n : 0; }
  function daysStr(arr) { return (arr && arr.length) ? arr.join(",") : ""; }
  function parseDays(s) {
    var out = [];
    (s || "").split(",").forEach(function (t) {
      t = t.trim(); if (t === "") return;
      var d = parseInt(t, 10);
      if (isFinite(d) && d >= 0 && d <= 6 && out.indexOf(d) < 0) out.push(d);
    });
    return out;
  }
  function el(tag, cls, txt) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (txt != null) e.textContent = txt;
    return e;
  }
  function priceInput(val, max, ph) {
    var i = el("input", "md-in");
    i.type = "number"; i.min = "0"; i.step = "0.01";
    if (max) i.max = String(max);
    i.placeholder = ph || "0";
    i.value = priceStr(val);
    return i;
  }

  // ---- one time-of-use window row (in the schedule editor) ----
  function windowRow(win) {
    var row = el("div", "md-win");
    win = win || { days: [], start: "00:00", end: "00:00", price_in: 0, price_out: 0, free: false };

    var days = el("input", "md-days"); days.type = "text";
    days.placeholder = "every day"; days.value = daysStr(win.days);
    days.title = "Weekdays 0-6 (Sun-Sat), comma-separated. Blank = every day.";

    var start = el("input", "md-time"); start.type = "time"; start.value = win.start || "00:00";
    var dash = el("span", "md-dash", "–");
    var end = el("input", "md-time"); end.type = "time"; end.value = win.end || "00:00";

    var pin = priceInput(win.price_in, CEIL.in, "in");
    var pout = priceInput(win.price_out, CEIL.out, "out");

    var freeWrap = el("label", "md-free");
    var free = el("input"); free.type = "checkbox"; free.checked = !!win.free;
    freeWrap.appendChild(free); freeWrap.appendChild(el("span", null, "free"));
    function syncFree() { pin.disabled = pout.disabled = free.checked; }
    free.addEventListener("change", syncFree); syncFree();

    var rm = el("button", "md-x", "×"); rm.type = "button"; rm.title = "Remove window";
    rm.addEventListener("click", function () { row.parentNode && row.parentNode.removeChild(row); });

    [days, start, dash, end, pin, pout, freeWrap, rm].forEach(function (n) { row.appendChild(n); });
    row._read = function () {
      return { days: parseDays(days.value), start: start.value || "00:00", end: end.value || "00:00",
               price_in: fnum(pin.value), price_out: fnum(pout.value), free: free.checked };
    };
    return row;
  }

  // ---- one model card (base price + schedule + save/clear) ----
  function modelCard(m) {
    var card = el("div", "md-card");

    var head = el("div", "md-head");
    var dot = el("span", "md-dot " + (m.online ? "md-dot--on" : "md-dot--off"), m.online ? "◉" : "○");
    dot.title = m.online ? "on air" : "off air";
    head.appendChild(dot);
    head.appendChild(el("span", "md-name", m.model || "(model)"));
    head.appendChild(el("span", "md-node", m.node || ""));
    if (m.overridden) head.appendChild(el("span", "md-tag", "custom"));
    card.appendChild(head);

    // base price line
    var base = el("div", "md-base");
    base.appendChild(el("span", "md-lbl", "Base $/1M"));
    var bin = priceInput(m.price_in, CEIL.in, "in");
    var bout = priceInput(m.price_out, CEIL.out, "out");
    var freeWrap = el("label", "md-free");
    var bfree = el("input"); bfree.type = "checkbox"; bfree.checked = !!m.free;
    freeWrap.appendChild(bfree); freeWrap.appendChild(el("span", null, "free"));
    function syncBase() { bin.disabled = bout.disabled = bfree.checked; }
    bfree.addEventListener("change", syncBase); syncBase();
    base.appendChild(el("span", "md-io", "in")); base.appendChild(bin);
    base.appendChild(el("span", "md-io", "out")); base.appendChild(bout);
    base.appendChild(freeWrap);
    card.appendChild(base);

    // schedule editor
    var sched = el("div", "md-sched");
    var shead = el("div", "md-shead");
    shead.appendChild(el("span", "md-lbl", "Schedule"));
    var add = el("button", "md-add", "+ window"); add.type = "button";
    shead.appendChild(add);
    sched.appendChild(shead);
    var wins = el("div", "md-wins");
    (m.schedule || []).forEach(function (win) { wins.appendChild(windowRow(win)); });
    sched.appendChild(wins);
    add.addEventListener("click", function () { wins.appendChild(windowRow(null)); });
    card.appendChild(sched);

    // actions + state
    var foot = el("div", "md-foot");
    var save = el("button", "primary md-save", "Save"); save.type = "button";
    foot.appendChild(save);
    if (m.overridden) {
      var clr = el("button", "ghost md-clear", "Reset to node price"); clr.type = "button";
      foot.appendChild(clr);
      clr.addEventListener("click", function () { clearModel(m, save, state); });
    }
    var state = el("span", "md-state");
    foot.appendChild(state);
    card.appendChild(foot);

    save.addEventListener("click", function () {
      var schedule = [];
      var rows = wins.querySelectorAll(".md-win");
      for (var i = 0; i < rows.length; i++) schedule.push(rows[i]._read());
      var payload = {
        node: m.node, model: m.model,
        price_in: bfree.checked ? 0 : fnum(bin.value),
        price_out: bfree.checked ? 0 : fnum(bout.value),
        schedule: schedule
      };
      saveModel(payload, save, state);
    });
    return card;
  }

  function setState(node, cls, txt) { node.className = "md-state " + (cls || ""); node.textContent = txt; }

  function saveModel(payload, btn, state) {
    btn.disabled = true; setState(state, "", "Saving...");
    api("/provider/models", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    }).then(function (r) {
      return r.json().then(function (j) { return { ok: r.ok, j: j }; });
    }).then(function (res) {
      btn.disabled = false;
      if (!res.ok || !res.j || !res.j.ok) {
        var msg = (res.j && res.j.error && res.j.error.message) || "Could not save.";
        setState(state, "md-state--err", msg);
        return;
      }
      setState(state, "md-state--ok", "Saved");
      setTimeout(loadModels, 700);
    }).catch(function () {
      btn.disabled = false; setState(state, "md-state--err", "Network error. Try again.");
    });
  }

  function clearModel(m, btn, state) {
    if (!window.confirm("Reset \"" + m.model + "\" to the price the node itself sets? Your custom price is removed on its next reconnect.")) return;
    btn.disabled = true; setState(state, "", "Clearing...");
    api("/provider/models", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ node: m.node, model: m.model, clear: true })
    }).then(function (r) {
      btn.disabled = false;
      if (!r.ok) { setState(state, "md-state--err", "Could not clear."); return; }
      setState(state, "md-state--ok", "Cleared");
      setTimeout(loadModels, 700);
    }).catch(function () {
      btn.disabled = false; setState(state, "md-state--err", "Network error. Try again.");
    });
  }

  function loadModels() {
    show("modelsWrap");
    show("mdLoading"); hide("mdError"); hide("mdEmpty"); hide("mdList");
    api("/provider/models").then(function (r) {
      if (!r.ok) throw new Error("http " + r.status);
      return r.json();
    }).then(function (d) {
      hide("mdLoading");
      if (d && typeof d.ceiling_in === "number") CEIL.in = d.ceiling_in;
      if (d && typeof d.ceiling_out === "number") CEIL.out = d.ceiling_out;
      var rows = (d && d.models) || [];
      var list = $("mdList");
      if (list) list.innerHTML = "";
      if (!rows.length) { show("mdEmpty"); return; }
      rows.forEach(function (m) { if (list) list.appendChild(modelCard(m)); });
      var ceil = $("mdCeil");
      if (ceil) { ceil.textContent = "Public ceiling: $" + priceStr(CEIL.in) + " in / $" + priceStr(CEIL.out) + " out per 1M tokens. Need to charge more? Share on a private band instead."; show("mdCeil"); }
      show("mdList");
    }).catch(function () {
      hide("mdLoading"); show("mdError");
    });
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

  // ---- boot: confirm session, then load /console. ----
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
      return fetch(BROKER + "/console", { credentials: "include" })
        .then(function (r) {
          if (r.status === 401 || r.status === 403) { location.replace("/login.html"); return null; }
          if (!r.ok) { hide("cnLoading"); show("cnError"); return null; }
          return r.json();
        })
        .then(function (d) {
          if (!d) return;
          hide("cnLoading");
          render(d);
        })
        .catch(function () { hide("cnLoading"); show("cnError"); });
    })
    .catch(function () { location.replace("/login.html"); });
})();
