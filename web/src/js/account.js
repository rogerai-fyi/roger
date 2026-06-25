// Account page glue for /account. Split out of the old multi-route account.js so
// each account page loads only its own logic (billing.js + payouts.js are siblings).
// Thin reads over the broker behind the credentialed session cookie (no tokens ever
// touch JS), matching js/auth.js. Logged-out visitors are routed to /login.
// Account-hub glue for /account, /billing, /usage and /payouts. Thin reads over the
// broker behind the credentialed session cookie (no tokens ever touch JS), matching
// the pattern in js/auth.js. Logged-out visitors are routed to /login.
(function () {
  var BROKER = "https://broker.rogerai.fyi";

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = "include";
    return fetch(BROKER + path, opts).then(function (r) {
      return r.ok ? r.json() : null;
    }).catch(function () { return null; });
  }
  function get(path) { return api(path); }

  function text(id, v) { var el = document.getElementById(id); if (el) el.textContent = v; }
  function show(id) { var el = document.getElementById(id); if (el) el.hidden = false; }
  function hide(id) { var el = document.getElementById(id); if (el) el.hidden = true; }
  function on(id, ev, fn) { var el = document.getElementById(id); if (el) el.addEventListener(ev, fn); }

  // Money in dollars (1 credit = $1; display relabel only - ledger math unchanged).
  // Adaptive precision: >= 1c at 2dp; sub-cent costs keep significant digits so a
  // real cost never reads as $0.00.
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
  function when(ts) {
    if (!ts) return "";
    return new Date(ts * 1000).toLocaleDateString();
  }

  function li(left, right, leftClass) {
    var el = document.createElement("li");
    var a = document.createElement("span");
    a.className = leftClass || "r-model";
    a.textContent = left;
    var b = document.createElement("span");
    b.className = "r-cost";
    b.textContent = right;
    el.appendChild(a);
    el.appendChild(b);
    return el;
  }
  function fill(listId, emptyId, rows, render) {
    var ul = document.getElementById(listId);
    if (!ul) return;
    if (!rows || !rows.length) { if (emptyId) show(emptyId); return; }
    rows.forEach(function (row) { ul.appendChild(render(row)); });
    show(listId);
  }

  function wireLogout() {
    on("logout", "click", function () {
      fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
        .then(function () { location.replace("/login.html"); });
    });
  }

  // Strip a trailing slash AND a ".html" suffix so the branch matches whether the
  // page is served at the clean path (/billing) or the static file (/billing.html) -
  // the static host serves /billing.html, so matching only "/billing" left it blank.
  var path = location.pathname.replace(/\/$/, "").replace(/\.html$/, "");
  var qs = new URLSearchParams(location.search);
  if (path.endsWith("/account")) {
    get("/account").then(function (a) {
      if (!a) { location.replace("/login.html"); return; }
      text("who", "@" + (a.github_login || "you"));
      text("handle", "@" + (a.github_login || "you"));
      text("balance", cr(a.balance));
      text("ghid", a.github_id || "-");
      text("connect", (a.connect && a.connect.status) || "none");
      var em = document.getElementById("email");
      if (em && a.email) em.value = a.email;
      show("card");
      wireLogout();
      on("saveEmail", "click", function () {
        var email = (document.getElementById("email") || {}).value || "";
        api("/account", { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email: email }) })
          .then(function (r) { text("saveMsg", r ? " saved" : " could not save"); });
      });
      on("export", "click", function () {
        // POST then download the JSON the browser receives.
        fetch(BROKER + "/account/export", { method: "POST", credentials: "include" })
          .then(function (r) { return r.ok ? r.blob() : null; })
          .then(function (blob) {
            if (!blob) return;
            var url = URL.createObjectURL(blob);
            var a2 = document.createElement("a");
            a2.href = url; a2.download = "rogerai-export.json"; a2.click();
            URL.revokeObjectURL(url);
          });
      });
      on("del", "click", function () {
        if (!confirm("Delete your account? Identity is anonymized; financial records are retained de-identified.")) return;
        fetch(BROKER + "/account/delete", { method: "POST", credentials: "include" }).then(function (r) {
          if (r.ok) { location.replace("/login.html"); return; }
          r.json().then(function (e) {
            text("delMsg", " " + ((e && e.error && e.error.message) || "could not delete"));
          });
        });
      });
    });
  }
})();
