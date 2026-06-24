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
        .then(function () { location.replace("/login"); });
    });
  }

  var path = location.pathname.replace(/\/$/, "");
  var qs = new URLSearchParams(location.search);

  if (path.endsWith("/account")) {
    get("/account").then(function (a) {
      if (!a) { location.replace("/login"); return; }
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
          if (r.ok) { location.replace("/login"); return; }
          r.json().then(function (e) {
            text("delMsg", " " + ((e && e.error && e.error.message) || "could not delete"));
          });
        });
      });
    });
  } else if (path.endsWith("/billing")) {
    get("/billing").then(function (d) {
      if (!d) { location.replace("/login"); return; }
      text("balance", cr(d.balance));
      text("derived", cr(d.derived));
      text("topupNote", d.checkout_ready ? "Top up from the CLI: rogerai topup, or the in-app button (coming soon)."
        : "Stripe is not configured on this broker yet.");
      fill("topups", "topupsEmpty", d.topups, function (t) {
        return li(when(t.ts) + " - session", cr(t.amount));
      });
      show("card");
      wireLogout();
    });
  } else if (path.endsWith("/usage")) {
    var group = qs.get("group") === "day" ? "day" : "model";
    get("/usage?group=" + group).then(function (d) {
      if (!d) { location.replace("/login"); return; }
      text("spend", cr(d.spend));
      fill("buckets", "bucketsEmpty", d.buckets, function (bkt) {
        return li(bkt.key + " (" + bkt.count + ")", cr(bkt.cost));
      });
      fill("recent", "recentEmpty", d.recent, function (e) {
        return li(e.model || e.node || "request", cr(e.cost));
      });
      show("card");
      wireLogout();
    });
  } else if (path.endsWith("/payouts")) {
    get("/account").then(function (a) {
      if (!a) { location.replace("/login"); return; }
      var e = a.earnings || {};
      text("held", cr(e.held || 0));
      text("reserved", cr(e.reserved || 0));
      text("payable", cr(e.payable || 0));
      text("paid", cr(e.paid || 0));
      if (e.next_release) text("releaseNote", "Next release: " + when(e.next_release));
      show("card");
      wireLogout();
      refreshConnect();
      loadPayouts();
    });

    function refreshConnect() {
      get("/connect/status").then(function (s) {
        var status = (s && s.status) || "none";
        text("connect", status);
        if (status === "active") { hide("onboard"); show("request"); }
        else { show("onboard"); hide("request"); }
      });
    }
    function loadPayouts() {
      get("/payouts/history").then(function (h) {
        if (!h) return;
        fill("payouts", "payoutsEmpty", h.payouts, function (p) {
          return li(when(p.created_at) + " - " + p.state, cr(p.amount));
        });
      });
    }
    on("onboard", "click", function () {
      api("/connect/onboard", { method: "POST" }).then(function (r) {
        if (r && r.url) { location.href = r.url; } else { text("payMsg", "could not start onboarding"); }
      });
    });
    on("request", "click", function () {
      text("payMsg", "requesting...");
      fetch(BROKER + "/payouts/request", { method: "POST", credentials: "include" }).then(function (r) {
        return r.json().then(function (body) {
          if (r.ok) { text("payMsg", "payout requested"); location.reload(); }
          else { text("payMsg", (body && body.error && body.error.message) || "payout failed"); }
        });
      }).catch(function () { text("payMsg", "payout failed"); });
    });
  }
})();
