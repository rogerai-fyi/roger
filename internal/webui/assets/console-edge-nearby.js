// console-edge-nearby.js - the console's NEARBY list (features/edge/empty_edge_simple.feature §7):
// the devices pinging that are not on the Edge, in the words the TUI's [3] uses (the server sends
// them as d.nearby), each with Invite and Approve. Invite shows the join line and the QR of an
// invite only that device can use; Approve grants a phone's claim, or waits for a roger device to
// ask and then shows the phrase, answered with the Towers card's decide. Drawn on the empty state
// and the enrolled view alike.
(function (root) {
  "use strict";

  function text(doc, tag, cls, s) {
    var e = doc.createElement(tag);
    if (cls) e.className = cls;
    e.textContent = s;
    return e;
  }

  function call(actions, path, method, body) {
    var opts = { method: method };
    if (body) { opts.headers = { "Content-Type": "application/json" }; opts.body = JSON.stringify(body); }
    return actions.fetch(path, opts).then(function (r) {
      return r.ok ? r.json() : r.text().then(function (t) { return { state: "failed", say: t.trim() }; });
    });
  }

  // render draws d.nearby into host and returns the controls ({invite, approve, check, decide, busy}),
  // or hides the host and returns null when nothing pings.
  function render(doc, host, d, actions) {
    var rows = (d && d.nearby) || [];
    var head = (d && d.nearby_head) || {};
    if (host.replaceChildren) host.replaceChildren();
    if (!rows.length) { host.hidden = true; return null; }
    var top = host.appendChild(doc.createElement("p"));
    top.className = "edgenearby__head";
    top.appendChild(text(doc, "span", "edgenearby__eyebrow", head.eyebrow || "NEARBY"));
    top.appendChild(text(doc, "span", "edgenearby__actions", "  " + (head.actions || "")));

    var panel = null; // the one open invite or approve, under its row
    var ctl = {};
    var byID = {};
    rows.forEach(function (n) {
      var row = host.appendChild(doc.createElement("div"));
      row.className = "edgenearby__row" + (n.stale ? " edgenearby__row--stale" : "");
      row.appendChild(text(doc, "b", "edgenearby__name", n.name));
      var facts = [n.kind, n.seen, n.key_label + " " + n.key].filter(Boolean).join(" · ");
      row.appendChild(text(doc, "span", "edgenearby__facts", " " + facts));
      var inv = row.appendChild(text(doc, "button", "btn edgenearby__invite", "Invite"));
      inv.setAttribute("type", "button");
      if (!head.invite) inv.disabled = true;
      var app = row.appendChild(text(doc, "button", "btn edgenearby__approve", "Approve"));
      app.setAttribute("type", "button");
      if (!head.approve) app.disabled = true;
      var under = host.appendChild(doc.createElement("div"));
      under.className = "edgenearby__panel";
      under.hidden = true;
      byID[n.id] = { row: n, under: under };
      inv.addEventListener("click", function () { ctl.invite(n.id); });
      app.addEventListener("click", function () { ctl.approve(n.id); });
    });
    host.hidden = false;

    function open(id) {
      if (panel && panel !== byID[id].under) { panel.hidden = true; if (panel.replaceChildren) panel.replaceChildren(); }
      panel = byID[id].under;
      if (panel.replaceChildren) panel.replaceChildren();
      panel.hidden = false;
      return panel;
    }
    function failed(p, a) { p.appendChild(text(doc, "p", "edgenearby__says", a.say || "could not do that")); return a; }

    ctl.invite = function (id) {
      if (!byID[id]) return Promise.resolve({ state: "failed", say: "not nearby" });
      return call(actions, "/api/edge/nearby/invite", "POST", { id: id }).then(function (a) {
        var p = open(id);
        if (a.state === "failed") return failed(p, a);
        p.appendChild(text(doc, "b", "edgenearby__invitehead", a.head));
        p.appendChild(text(doc, "p", "edgenearby__next", a.next));
        p.appendChild(text(doc, "code", "edgenearby__line", a.line));
        if (a.qr) p.appendChild(text(doc, "pre", "join-qr edgenearby__qr", a.qr));
        p.appendChild(text(doc, "p", "edgenearby__left", "works once · " + a.left));
        return a;
      });
    };
    function asked(id, a) {
      var p = open(id);
      if (a.state === "failed") return failed(p, a);
      p.appendChild(text(doc, "p", "edgenearby__say", a.say));
      if (a.state === "waiting" && a.run) p.appendChild(text(doc, "code", "edgenearby__run", a.run));
      if (a.state === "asked") {
        p.appendChild(text(doc, "b", "edgenearby__phrase", a.phrase));
        if (a.hint) p.appendChild(text(doc, "p", "edgenearby__hint", a.hint));
        var yes = p.appendChild(text(doc, "button", "btn primary edgenearby__yes", "Approve"));
        var no = p.appendChild(text(doc, "button", "btn edgenearby__no", "Refuse"));
        yes.addEventListener("click", function () { ctl.decide(a, true); });
        no.addEventListener("click", function () { ctl.decide(a, false); });
      }
      if (a.state === "waiting" && actions.poll !== false) {
        setTimeout(function () { ctl.check(id); }, 2000);
      }
      return a;
    }
    ctl.approve = function (id) {
      if (!byID[id]) return Promise.resolve({ state: "failed", say: "not nearby" });
      return call(actions, "/api/edge/nearby/approve", "POST", { id: id }).then(function (a) {
        a = asked(id, a);
        if (a.state === "granted" && actions.refresh) actions.refresh();
        return a;
      });
    };
    ctl.check = function (id) {
      if (!byID[id] || panel !== byID[id].under || panel.hidden) return Promise.resolve(null);
      return call(actions, "/api/edge/nearby/asked?id=" + encodeURIComponent(id), "GET").then(function (a) { return asked(id, a); });
    };
    ctl.busy = function () { return !!panel && !panel.hidden; };
    ctl.decide = function (a, approve) {
      return call(actions, "/api/towers/decide", "POST", { dir: a.dir, id: a.request, approve: approve }).then(function (r) {
        if (panel) panel.hidden = true;
        if (actions.refresh) actions.refresh();
        return r;
      });
    };
    return ctl;
  }

  var api = { render: render };
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  } else {
    root.RogerConsoleEdgeNearby = api;
  }
})(this);
