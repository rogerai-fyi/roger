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

  // credits as a compact, human number (the wallet/earnings unit is "cr").
  function cr(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    return (Math.round(n * 1e4) / 1e4) + " cr";
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
        .then(function () { location.replace("/login"); });
    });
  }

  var path = location.pathname.replace(/\/$/, "");

  if (path.endsWith("/dashboard")) {
    get("/me").then(function (me) {
      if (!me) { location.replace("/login"); return; }
      text("who", "@" + (me.github_login || "you"));
      text("balance", cr(me.balance));
      text("spend", cr(me.spend));
      renderRecent(me.recent);
      show("card");
      wireLogout();
    });
  } else if (path.endsWith("/console")) {
    get("/account").then(function (a) {
      if (!a) { location.replace("/login"); return; }
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
      if (a) location.replace("/dashboard");
    });
  }
})();
