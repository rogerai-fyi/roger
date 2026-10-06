// RogerAccountKeys - the keys page's "Account keys" section (keys.html): keys that call the API
// AS YOU, with their own spend limits, over the broker's credentialed /account/keys endpoints
// (features/auth/key_guardrails.feature). The session cookie is the auth; every write sends a
// JSON body; a key secret never touches a URL, storage or the log, and is shown once, at mint.
//
//   GET    /account/keys         -> { keys: [ key ] }
//   POST   /account/keys  {name, limit_usd, reset}       -> key + secret (shown ONCE)
//   PATCH  /account/keys/{id}  {limit_usd? | disabled?}  -> key
//   DELETE /account/keys/{id}                            -> 204 (revoked)
//
// The request builder and the renderers are pure and exported for the node tests; mount() is
// the thin DOM wiring the page runs.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fm";
  var WARNING = "Store it now - it is never shown again.";

  // makeApi builds a request function over fetchImpl: credentialed, JSON in and out.
  function makeApi(base, fetchImpl) {
    return function (method, path, body) {
      var opts = { method: method, credentials: "include", headers: {} };
      if (body !== undefined) {
        opts.headers["Content-Type"] = "application/json";
        opts.body = JSON.stringify(body);
      }
      return fetchImpl(base + path, opts).then(function (r) {
        if (r.status === 204) return null;
        return r.json().catch(function () { return null; }).then(function (j) {
          if (!r.ok) {
            var msg = j && j.error && j.error.message ? j.error.message : "the broker answered " + r.status;
            var e = new Error(msg);
            e.status = r.status;
            throw e;
          }
          return j;
        });
      });
    };
  }

  function list(api) { return api("GET", "/account/keys").then(function (j) { return (j && j.keys) || []; }); }
  function mint(api, fields) { return api("POST", "/account/keys", fields); }
  function update(api, id, fields) { return api("PATCH", "/account/keys/" + encodeURIComponent(id), fields); }
  function revoke(api, id) { return api("DELETE", "/account/keys/" + encodeURIComponent(id)); }

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function usd(n) { return "$" + (Number(n) || 0).toFixed(2); }

  // used is the spend in the window the limit counts (reset none: the key's whole life).
  function used(k) {
    var w = { daily: k.usage_daily, weekly: k.usage_weekly, monthly: k.usage_monthly }[k.reset];
    return w == null ? k.usage : w;
  }
  function state(k) { return k.disabled ? "disabled" : k.expired ? "expired" : "active"; }
  var PILL = { active: "kf__status--active", expired: "kf__status--expired", disabled: "kf__status--revoked" };

  function rowsHTML(keys) {
    return keys.map(function (k) {
      var st = state(k), id = esc(k.id);
      return '<tr' + (st === "active" ? "" : ' class="kf__row--off"') + ">" +
        "<td><code>" + id + "</code></td>" +
        '<td class="mx-model">' + esc(k.name || "-") + "</td>" +
        "<td><code>" + esc(k.hint) + "</code></td>" +
        '<td class="num">' + (k.limit_usd > 0 ? usd(k.limit_usd) : "unlimited") + "</td>" +
        '<td class="num">' + usd(used(k)) + "</td>" +
        "<td>" + esc(k.reset) + "</td>" +
        "<td>" + (k.expires_at ? esc(String(k.expires_at).slice(0, 10)) : "never") + "</td>" +
        '<td><span class="kf__status ' + PILL[st] + '">' + st + "</span></td>" +
        '<td class="kf__act">' +
        '<button type="button" class="kf__rowbtn" data-act="limit" data-id="' + id + '">Limit</button>' +
        '<button type="button" class="kf__rowbtn" data-act="toggle" data-id="' + id + '">' + (k.disabled ? "Enable" : "Disable") + "</button>" +
        '<button type="button" class="kf__rowbtn kf__rowbtn--danger" data-act="revoke" data-id="' + id + '">Revoke</button>' +
        "</td></tr>";
    }).join("");
  }

  // revealHTML is the one-time secret block: the secret once, the shared copy control
// (data-copy-target, site.js) pointed at it, and the warning.
  function revealHTML(secret) {
    return '<div class="kf__reveal-head"><span class="kf__reveal-tag">shown once</span>' +
      '<span class="kf__reveal-msg">' + esc(WARNING) + "</span></div>" +
      '<div class="kf__secret"><code class="kf__secret-val" id="akSecret">' + esc(secret) + "</code>" +
      '<button type="button" class="kf__copy" data-copy-target="#akSecret">Copy</button></div>';
  }

  // ---- the page ------------------------------------------------------------------------------
  function mount(doc, api) {
    var $ = function (id) { return doc.getElementById(id); };
    var rows = $("akRows"), wrap = $("akWrap"), empty = $("akEmpty"), err = $("akErr"), reveal = $("akReveal");
    if (!rows) return;
    var keysByID = {};
    function fail(e) { err.textContent = e.message; err.hidden = false; }
    function refresh() {
      return list(api).then(function (keys) {
        keysByID = {};
        keys.forEach(function (k) { keysByID[k.id] = k; });
        rows.innerHTML = rowsHTML(keys);
        wrap.hidden = keys.length === 0;
        empty.hidden = keys.length !== 0;
      }).catch(function (e) { if (e.status !== 401) fail(e); });
    }
    $("akForm").addEventListener("submit", function (ev) {
      ev.preventDefault();
      err.hidden = true;
      var fields = { name: $("akName").value.trim() };
      var limit = parseFloat($("akLimit").value);
      if (limit > 0) fields.limit_usd = limit;
      fields.reset = $("akReset").value;
      mint(api, fields).then(function (k) {
        reveal.innerHTML = revealHTML(k.secret); // its Copy is the shared copy control (site.js)
        reveal.hidden = false;
        $("akForm").reset();
        return refresh();
      }).catch(fail);
    });
    rows.addEventListener("click", function (ev) {
      var b = ev.target.closest("button[data-act]");
      if (!b) return;
      var id = b.getAttribute("data-id"), k = keysByID[id] || {}, act = b.getAttribute("data-act"), p;
      if (act === "revoke") {
        if (!confirm("Revoke " + id + "? Anything using it stops working.")) return;
        p = revoke(api, id);
      } else if (act === "toggle") {
        p = update(api, id, { disabled: !k.disabled });
      } else {
        var v = prompt("Spend limit in $ for " + id + " (0 = unlimited)", k.limit_usd || 0);
        if (v === null) return;
        p = update(api, id, { limit_usd: parseFloat(v) || 0 });
      }
      p.then(refresh).catch(fail);
    });
    refresh();
  }

  var R = { makeApi: makeApi, list: list, mint: mint, update: update, revoke: revoke,
    rowsHTML: rowsHTML, revealHTML: revealHTML, WARNING: WARNING, mount: mount };
  if (typeof window !== "undefined") {
    window.RogerAccountKeys = R;
    document.addEventListener("DOMContentLoaded", function () {
      mount(document, makeApi(BROKER, window.fetch.bind(window)));
    });
  }
  if (typeof module !== "undefined" && module.exports) module.exports = R; // node test
})();
