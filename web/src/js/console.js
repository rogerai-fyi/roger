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

  function $(id) { return document.getElementById(id); }
  function show(id) { var el = $(id); if (el) el.hidden = false; }
  function hide(id) { var el = $(id); if (el) el.hidden = true; }
  function text(id, v) { var el = $(id); if (el) el.textContent = v; }
  function n0(v) { return typeof v === "number" && isFinite(v) ? v : 0; }

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
  function kfmt(n) {
    n = n0(n);
    if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1).replace(/\.0$/, "") + "M";
    if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1).replace(/\.0$/, "") + "k";
    return String(Math.round(n));
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

    // ---- stat tiles ----
    text("ctrReq", num(n0(c.requests_today)));
    if (isOwner) {
      text("ctrMoney", cr(n0(c.earned_today)));
      text("ctrMoneyK", "Earned today");
      text("ctrNodes", num(n0(c.active_nodes)));
      text("ctrBands", num(n0(c.active_bands)));
      show("tileNodes");
      show("tileBands");
      // owner header column = earned
      text("colMoney", "Earned");
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
