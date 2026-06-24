// Minimal auth glue for /login, /dashboard and /console.
//
// Auto-route per AUTH-DESIGN: consuming needs no login; monetizing needs it.
//   - /dashboard: no valid session -> /login; else show identity + wallet (/me).
//   - /console:   no valid session -> /login; else show accrued earnings (/earnings).
//   - /login:     already logged in -> /dashboard.
//
// The broker holds the session (signed http-only cookie); these pages just ask it
// over credentialed CORS. No tokens ever touch JS.
(function () {
  var BROKER = "https://broker.rogerai.fyi";

  function get(path) {
    return fetch(BROKER + path, { credentials: "include" }).then(function (r) {
      return r.ok ? r.json() : null;
    }).catch(function () { return null; });
  }

  function text(id, v) { var el = document.getElementById(id); if (el) el.textContent = v; }
  function show(id) { var el = document.getElementById(id); if (el) el.hidden = false; }
  function hide(id) { var el = document.getElementById(id); if (el) el.hidden = true; }

  // Money in dollars (1 credit = $1; a display relabel only - the broker ledger
  // math is unchanged). Groq-style adaptive precision: >= 1c shows 2dp ($12.34),
  // tiny per-token/per-reply costs keep enough significant digits to never read
  // as $0.00 (e.g. $0.000123).
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

  // render a short recent list; each entry has model + cost + a timestamp.
  function renderRecent(list) {
    if (!list || !list.length) { show("recentEmpty"); return; }
    var ul = document.getElementById("recent");
    if (!ul) return;
    list.slice(0, 8).forEach(function (e) {
      var li = document.createElement("li");
      var model = document.createElement("span");
      model.className = "r-model";
      model.textContent = e.model || e.node || "request";
      var cost = document.createElement("span");
      cost.className = "r-cost";
      cost.textContent = cr(e.cost);
      li.appendChild(model);
      li.appendChild(cost);
      ul.appendChild(li);
    });
    show("recent");
  }

  function wireLogout() {
    var out = document.getElementById("logout");
    if (!out) return;
    out.addEventListener("click", function () {
      fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
        .then(function () { location.replace("/login.html"); });
    });
  }

  // Strip a trailing slash AND a ".html" suffix, so this works whether the page is
  // served at the clean path (/dashboard) or the static file (/dashboard.html) - the
  // static host serves /dashboard.html, so matching only "/dashboard" left it blank.
  var path = location.pathname.replace(/\/$/, "").replace(/\.html$/, "");

  if (path.endsWith("/dashboard")) {
    get("/me").then(function (me) {
      if (!me) { location.replace("/login.html"); return; }
      text("who", "@" + (me.github_login || "you"));
      text("balance", cr(me.balance));
      text("spend", cr(me.spend));
      renderRecent(me.recent);
      show("card");
      wireLogout();
    });
  } else if (path.endsWith("/console")) {
    get("/account").then(function (a) {
      if (!a) { location.replace("/login.html"); return; }
      text("who", "@" + (a.github_login || "you"));
      show("card");
      wireLogout();
      var node = new URLSearchParams(location.search).get("node");
      if (!node) return; // no node id yet: show the identity + the how-to note
      get("/earnings?node=" + encodeURIComponent(node)).then(function (e) {
        if (!e) return;
        text("earnings", cr(e.earnings));
        text("status", e.online ? "on air" : "offline");
        text("note", "Node " + node);
        renderRecent(e.recent);
      });
    });
  } else if (path.endsWith("/login")) {
    get("/account").then(function (a) {
      if (a) location.replace("/dashboard.html");
    });
  }
})();
