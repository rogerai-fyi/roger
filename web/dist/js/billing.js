// Billing page glue for /billing: wallet balance + Stripe Checkout top-up. Split out
// of the old multi-route account.js so each account page loads only its own logic.
// Thin reads over the broker behind the credentialed session cookie (no tokens ever
// touch JS). Logged-out visitors are routed to /login.
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
  if (path.endsWith("/billing")) {
    get("/billing").then(function (d) {
      if (!d) { location.replace("/login.html"); return; }
      text("balance", cr(d.balance));
      text("derived", cr(d.derived));
      if (d.checkout_ready) {
        text("topupNote", "Top up below, or from the CLI: rogerai topup.");
        show("topupBox");
        wireTopup();
      } else {
        text("topupNote", "Top up from the CLI: rogerai topup.");
        show("topupDisabled");
      }
      fill("topups", "topupsEmpty", d.topups, function (t) {
        return li(when(t.ts) + " - session", cr(t.amount));
      });
      show("card");
      wireLogout();
    });

    // Top-up control: a chosen preset OR a custom amount -> Stripe Checkout.
    function wireTopup() {
      var presets = document.getElementById("topupPresets");
      var custom = document.getElementById("topupCustom");
      var btn = document.getElementById("topup");

      // selecting a preset clears the custom field; typing a custom clears presets.
      if (presets) {
        presets.addEventListener("click", function (ev) {
          var b = ev.target.closest("button[data-usd]");
          if (!b) return;
          var btns = presets.querySelectorAll(".amount");
          for (var i = 0; i < btns.length; i++) btns[i].classList.remove("is-active");
          b.classList.add("is-active");
          if (custom) custom.value = "";
        });
      }
      if (custom) {
        custom.addEventListener("input", function () {
          if (!custom.value) return;
          var btns = presets ? presets.querySelectorAll(".amount") : [];
          for (var i = 0; i < btns.length; i++) btns[i].classList.remove("is-active");
        });
      }

      function chosenUsd() {
        if (custom && custom.value) {
          var v = parseFloat(custom.value);
          return isFinite(v) ? v : NaN;
        }
        var active = presets && presets.querySelector(".amount.is-active");
        return active ? parseFloat(active.getAttribute("data-usd")) : NaN;
      }

      on("topup", "click", function () {
        var usd = chosenUsd();
        if (!isFinite(usd) || usd < 1) { text("topupMsg", " enter an amount of $1 or more"); return; }
        usd = Math.round(usd * 100) / 100;
        if (btn) btn.disabled = true;
        text("topupMsg", " redirecting to Stripe...");
        api("/billing/checkout", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ usd: usd })
        }).then(function (r) {
          if (r && r.url) { window.location = r.url; return; }
          if (btn) btn.disabled = false;
          text("topupMsg", " could not start checkout");
        }).catch(function () {
          if (btn) btn.disabled = false;
          text("topupMsg", " could not start checkout");
        });
      });
    }
  }
})();
