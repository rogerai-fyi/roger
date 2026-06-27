// API keys page (keys.html): mint + manage grant keys over the broker's owner-auth
// /grants endpoints. Credentialed CORS (the session cookie IS the owner auth; the web
// origin is echoed in Access-Control-Allow-Origin). No tokens ever touch JS - the only
// secret on this page is the freshly-minted grant secret, shown once on create and never
// re-fetched (the broker stores only its hash).
//
// Endpoints (all credentialed; CSP connect-src already allows the broker):
//   GET    /account            -> gate (logged-in?) - reuses the account hub shape
//   POST   /grants  {name, free, price_in, price_out, daily_cap, monthly_cap,
//                    expires_at(unix,0=never), nodes:[], models:[], self}
//            -> { ok, grant, secret, openai_api_base, openai_api_key }   (secret ONCE)
//   GET    /grants             -> { grants: [ grantView ] }   (with usage rollup)
//   DELETE /grants/{id}        -> revoke
//   PATCH  /grants/{id}        {daily_cap?, monthly_cap?, expires_at?, revoked?}
//
// grantView fields used: id, name, price, free, self, daily_cap, monthly_cap,
//   expires_at, status, usage.{day_tokens, month_tokens}.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";

  // ---- tiny DOM helpers (mirrors account.js / metrics.js) ----
  function $(id) { return document.getElementById(id); }
  function show(id) { var el = $(id); if (el) el.hidden = false; }
  function hide(id) { var el = $(id); if (el) el.hidden = true; }
  function on(id, ev, fn) { var el = $(id); if (el) el.addEventListener(ev, fn); }

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = "include";
    return fetch(BROKER + path, opts);
  }

  // Compact integer with thousands separators (tabular-nums in CSS keeps columns).
  function num(n) {
    if (typeof n !== "number" || !isFinite(n)) return "-";
    return Math.round(n).toLocaleString("en-US");
  }
  function capLabel(v) { return !v || v <= 0 ? "unlimited" : num(v); }
  function expiryLabel(unix) {
    if (!unix || unix <= 0) return "never";
    var d = new Date(unix * 1000);
    return d.toISOString().slice(0, 10);
  }
  function priceLabel(g) {
    if (g.self) return "self";
    if (g.free) return "free";
    return g.price || "priced";
  }

  // ---- form: segmented "Billing" + "Node scope" toggles --------------------
  var billMode = "free"; // free | self | priced
  var scopeMode = "all"; // all | pick  (node scope)
  var modelScope = "all"; // all | pick (model scope)

  function pickGroup(selector, attr, onChange) {
    var opts = document.querySelectorAll(selector + " .kf__opt");
    for (var i = 0; i < opts.length; i++) {
      opts[i].addEventListener("click", function (e) {
        var btn = e.currentTarget;
        for (var j = 0; j < opts.length; j++) {
          opts[j].classList.remove("is-active");
          opts[j].setAttribute("aria-pressed", "false");
        }
        btn.classList.add("is-active");
        btn.setAttribute("aria-pressed", "true");
        onChange(btn.getAttribute(attr));
      });
    }
  }

  // Reflect the current billMode onto the Billing segmented control (keeps the
  // simple "Free key" toggle and the Advanced radiogroup in sync).
  function syncBillButtons() {
    var opts = document.querySelectorAll('[role="radiogroup"][aria-label="Billing"] .kf__opt');
    for (var i = 0; i < opts.length; i++) {
      var on = opts[i].getAttribute("data-bill") === billMode;
      opts[i].classList.toggle("is-active", on);
      opts[i].setAttribute("aria-pressed", on ? "true" : "false");
    }
    if (billMode === "priced") { show("priceField"); } else { hide("priceField"); }
  }

  // Activate the option matching `value` in a segmented radiogroup, clearing the rest.
  function setSegment(selector, attr, value) {
    var opts = document.querySelectorAll(selector + " .kf__opt");
    for (var i = 0; i < opts.length; i++) {
      var on = opts[i].getAttribute(attr) === value;
      opts[i].classList.toggle("is-active", on);
      opts[i].setAttribute("aria-pressed", on ? "true" : "false");
    }
  }

  // Reset the Node + Model scope controls to their "all/any" default after a mint
  // (.reset() clears inputs but not the JS-driven segmented buttons / hints).
  function resetScopeButtons() {
    setSegment('[role="radiogroup"][aria-label="Node scope"]', "data-scope", "all");
    hide("kNodes");
    var sh = $("scopeHint"); if (sh) sh.textContent = "The key works on every node you put on air.";
    setSegment('[role="radiogroup"][aria-label="Model scope"]', "data-mscope", "all");
    hide("kModels");
    var mh = $("mscopeHint"); if (mh) mh.textContent = "The key can call any model your in-scope nodes serve.";
  }

  function initForm() {
    pickGroup('[role="radiogroup"][aria-label="Billing"]', "data-bill", function (m) {
      billMode = m;
      if (m === "priced") { show("priceField"); } else { hide("priceField"); }
      // keep the simple toggle honest: free <-> the Free key checkbox
      var fc = $("kFree");
      if (fc) fc.checked = (m === "free");
      var hint = $("billHint");
      if (hint) {
        hint.textContent = m === "free"
          ? "Free - costs nobody. Your nodes serve it at $0."
          : m === "self"
            ? "Self - a $0 key for your OWN headless boxes and agents."
            : "Sponsored - usage bills to your own wallet at the price below.";
      }
    });
    // The common-path "Free key" toggle: on => free; off => open Advanced so the
    // user can pick Self/Sponsored/caps (defaults to Self, still a working $0 key).
    on("kFree", "change", function (e) {
      if (e.target.checked) {
        billMode = "free";
      } else {
        if (billMode === "free") billMode = "self";
        var adv = $("advBlock"); if (adv) adv.open = true;
      }
      syncBillButtons();
    });
    pickGroup('[role="radiogroup"][aria-label="Node scope"]', "data-scope", function (m) {
      scopeMode = m;
      var inp = $("kNodes"), hint = $("scopeHint");
      if (m === "pick") { if (inp) inp.hidden = false; if (hint) hint.textContent = "This key works only on these nodes - others reject it. Comma-separated node ids."; }
      else { if (inp) inp.hidden = true; if (hint) hint.textContent = "The key works on every node you put on air."; }
    });
    pickGroup('[role="radiogroup"][aria-label="Model scope"]', "data-mscope", function (m) {
      modelScope = m;
      var inp = $("kModels"), hint = $("mscopeHint");
      if (m === "pick") { if (inp) inp.hidden = false; if (hint) hint.textContent = "This key can only call these models - any other model is rejected. Comma-separated model names."; }
      else { if (inp) inp.hidden = true; if (hint) hint.textContent = "The key can call any model your in-scope nodes serve."; }
    });
    on("kExpiry", "change", function (e) {
      var d = $("kExpiryDate");
      if (d) d.hidden = e.target.value !== "date";
    });
    on("createForm", "submit", onCreate);
    on("copySecret", "click", onCopySecret);
    on("logout", "click", function () {
      api("/auth/logout", { method: "POST" }).then(function () { location.reload(); })
        .catch(function () { location.reload(); });
    });
  }

  // Resolve the expiry control to a unix expires_at (0 = never).
  function expiresAtUnix() {
    var sel = $("kExpiry");
    var v = sel ? sel.value : "never";
    if (v === "never") return 0;
    if (v === "30d") return Math.floor(Date.now() / 1000) + 30 * 86400;
    if (v === "90d") return Math.floor(Date.now() / 1000) + 90 * 86400;
    if (v === "date") {
      var d = $("kExpiryDate");
      if (d && d.value) {
        var t = Math.floor(new Date(d.value + "T00:00:00Z").getTime() / 1000);
        if (isFinite(t) && t > 0) return t;
      }
    }
    return 0;
  }

  function onCreate(e) {
    e.preventDefault();
    var nameEl = $("kName");
    var name = (nameEl && nameEl.value || "").trim();
    var err = $("createErr");
    if (err) { err.hidden = true; err.textContent = ""; }
    if (!name) { if (err) { err.textContent = "Name is required."; err.hidden = false; } return; }

    var priceOut = 0;
    if (billMode === "priced") {
      priceOut = parseFloat(($("kPriceOut") || {}).value) || 0;
    }
    var nodes = [];
    if (scopeMode === "pick") {
      var raw = (($("kNodes") || {}).value || "").split(",");
      for (var i = 0; i < raw.length; i++) { var t = raw[i].trim(); if (t) nodes.push(t); }
    }
    var models = [];
    if (modelScope === "pick") {
      var rawm = (($("kModels") || {}).value || "").split(",");
      for (var k = 0; k < rawm.length; k++) { var mt = rawm[k].trim(); if (mt) models.push(mt); }
    }
    var payload = {
      name: name,
      free: billMode !== "priced",          // self + free are both $0; only "priced" is paid
      self: billMode === "self",
      price_out: priceOut,
      daily_cap: parseInt(($("kDaily") || {}).value, 10) || 0,
      monthly_cap: parseInt(($("kMonthly") || {}).value, 10) || 0,
      expires_at: expiresAtUnix(),
      nodes: nodes,
      models: models                         // empty = any model the in-scope nodes serve
    };

    var btn = $("createBtn");
    if (btn) { btn.disabled = true; btn.textContent = "Minting..."; }
    api("/grants", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    }).then(function (r) {
      return r.json().then(function (j) { return { ok: r.ok, j: j }; });
    }).then(function (res) {
      if (btn) { btn.disabled = false; btn.textContent = "Mint key"; }
      if (!res.ok || !res.j || !res.j.ok || !res.j.secret) {
        var m = res.j && res.j.error && res.j.error.message ? res.j.error.message : "Could not create the key.";
        if (err) { err.textContent = m; err.hidden = false; }
        return;
      }
      revealSecret(res.j);
      var f = $("createForm"); if (f) f.reset();
      // restore defaults the .reset() does not (JS state, hidden fields, toggle)
      billMode = "free"; scopeMode = "all"; modelScope = "all";
      syncBillButtons();
      resetScopeButtons();
      hide("priceField");
      var adv = $("advBlock"); if (adv) adv.open = false;
      loadKeys();
    }).catch(function () {
      if (btn) { btn.disabled = false; btn.textContent = "Mint key"; }
      if (err) { err.textContent = "Network error. Try again."; err.hidden = false; }
    });
  }

  function revealSecret(j) {
    var secret = j.secret;
    var sv = $("secretVal");
    if (sv) sv.textContent = secret;
    var copy = $("copySecret");
    if (copy) { copy.setAttribute("data-copy", secret); copy.textContent = "Copy"; }
    var base = j.openai_api_base || (BROKER + "/v1");
    var env = $("envLines");
    if (env) env.textContent = "OPENAI_API_BASE=" + base + "\nOPENAI_API_KEY=" + secret;
    show("reveal");
  }

  function onCopySecret(e) {
    var btn = e.currentTarget;
    var secret = btn.getAttribute("data-copy") || "";
    if (!secret) return;
    function done() { btn.textContent = "Copied"; setTimeout(function () { btn.textContent = "Copy"; }, 1600); }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(secret).then(done).catch(function () { legacyCopy(secret, done); });
    } else {
      legacyCopy(secret, done);
    }
  }
  function legacyCopy(text, done) {
    try {
      var ta = document.createElement("textarea");
      ta.value = text; ta.setAttribute("readonly", "");
      ta.style.position = "absolute"; ta.style.left = "-9999px";
      document.body.appendChild(ta); ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
      done();
    } catch (err) { /* leave the secret visible for manual copy */ }
  }

  // ---- list + per-row actions ----------------------------------------------
  function loadKeys() {
    show("keysLoading"); hide("keysError"); hide("keysEmpty");
    api("/grants").then(function (r) {
      if (r.status === 403) { throw new Error("forbidden"); }
      return r.json();
    }).then(function (j) {
      hide("keysLoading");
      var rows = (j && j.grants) || [];
      var tbody = $("keysRows");
      if (tbody) tbody.innerHTML = "";
      if (!rows.length) { hide("keysWrap"); show("keysEmpty"); return; }
      // active first, then by name
      rows.sort(function (a, b) {
        var ar = a.status === "active" ? 0 : 1, br = b.status === "active" ? 0 : 1;
        if (ar !== br) return ar - br;
        return (a.name || "").localeCompare(b.name || "");
      });
      for (var i = 0; i < rows.length; i++) tbody.appendChild(rowFor(rows[i]));
      show("keysWrap");
    }).catch(function () {
      hide("keysLoading"); hide("keysWrap"); show("keysError");
    });
  }

  function cell(text, cls) {
    var td = document.createElement("td");
    if (cls) td.className = cls;
    td.textContent = text;
    return td;
  }

  function rowFor(g) {
    var tr = document.createElement("tr");
    if (g.status !== "active") tr.className = "kf__row--off";

    tr.appendChild(cell(g.name || "-", "mx-model"));
    tr.appendChild(cell(priceLabel(g)));
    tr.appendChild(cell(expiryLabel(g.expires_at)));
    tr.appendChild(cell(capLabel(g.daily_cap), "num"));

    var u = g.usage || {};
    tr.appendChild(cell(num(u.day_tokens || 0) + " / " + num(u.month_tokens || 0), "num"));

    var st = document.createElement("td");
    var pill = document.createElement("span");
    pill.className = "kf__status kf__status--" + (g.status || "active");
    pill.textContent = g.status || "active";
    st.appendChild(pill);
    tr.appendChild(st);

    // actions: edit caps + revoke (active only)
    var act = document.createElement("td");
    act.className = "kf__act";
    if (g.status === "active") {
      var edit = document.createElement("button");
      edit.type = "button"; edit.className = "kf__rowbtn";
      edit.textContent = "Caps"; edit.title = "Edit caps";
      edit.addEventListener("click", function () { editCaps(g, tr); });
      act.appendChild(edit);

      var rev = document.createElement("button");
      rev.type = "button"; rev.className = "kf__rowbtn kf__rowbtn--danger";
      rev.textContent = "Revoke";
      rev.addEventListener("click", function () { revoke(g); });
      act.appendChild(rev);
    } else {
      act.textContent = "-";
    }
    tr.appendChild(act);
    return tr;
  }

  // Inline cap edit: a small prompt-driven PATCH (daily + monthly tokens). Kept
  // minimal and dependency-free; the PATCH supports daily_cap/monthly_cap/expires_at.
  function editCaps(g, tr) {
    var cur = (g.daily_cap || 0);
    var nd = window.prompt("Daily token cap for \"" + g.name + "\" (0 = unlimited):", String(cur));
    if (nd === null) return;
    var nm = window.prompt("Monthly token cap (0 = unlimited):", String(g.monthly_cap || 0));
    if (nm === null) return;
    var dv = parseInt(nd, 10), mv = parseInt(nm, 10);
    if (!isFinite(dv) || dv < 0) dv = 0;
    if (!isFinite(mv) || mv < 0) mv = 0;
    api("/grants/" + encodeURIComponent(g.id), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ daily_cap: dv, monthly_cap: mv })
    }).then(function (r) {
      if (!r.ok) throw new Error("patch failed");
      loadKeys();
    }).catch(function () { window.alert("Could not update caps. Try again."); });
  }

  function revoke(g) {
    if (!window.confirm("Revoke \"" + g.name + "\"? The next request with its key is rejected. This cannot be undone.")) return;
    api("/grants/" + encodeURIComponent(g.id), { method: "DELETE" }).then(function (r) {
      if (!r.ok) throw new Error("revoke failed");
      loadKeys();
    }).catch(function () { window.alert("Could not revoke the key. Try again."); });
  }

  // ---- gate: logged in? -----------------------------------------------------
  api("/account").then(function (r) {
    return r.ok ? r.json() : null;
  }).then(function (acct) {
    if (acct && (acct.github_login || acct.github_id)) {
      show("card");
      initForm();
      loadKeys();
    } else {
      show("gate");
    }
  }).catch(function () {
    show("gate"); // offline / logged-out
  });
})();
