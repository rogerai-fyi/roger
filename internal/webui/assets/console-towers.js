// console-towers.js - the owner's side of joining in the console (features/join/approve.feature,
// invite.feature, surfaces.feature, tower_identity.feature): the Towers this user serves with
// their fingerprints, waiting join requests with Approve and Refuse, a one-paste invite with its
// QR that says who joined, and the Towers this machine joined. Every sentence comes from the
// server (the same internal/ownerjoin sentences the TUI shows); this file only lays them out.
// Pure functions over a document and the /api/towers view, so a test can run them in node.
(function (root) {
  "use strict";

  function el(doc, tag, cls, txt) {
    var n = doc.createElement(tag);
    if (cls) n.className = cls;
    if (txt !== undefined && txt !== null) n.textContent = txt;
    return n;
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  // render draws view into the #edge-towers block. on.invite(tower), on.decide(req, approve) are
  // what the buttons do.
  function render(doc, view, on) {
    var box = doc.getElementById("edge-towers");
    if (!box) return;
    clear(box);
    view = view || {};
    var towers = view.towers || [], reqs = view.requests || [], pinned = view.pinned || [];
    box.hidden = towers.length === 0 && reqs.length === 0 && pinned.length === 0;
    reqs.forEach(function (r) {
      var card = el(doc, "div", "join-request");
      card.appendChild(el(doc, "p", "mono join-ask", r.ask));
      card.appendChild(el(doc, "p", "muted small", r.hint));
      var yes = el(doc, "button", "btn", "Approve");
      yes.addEventListener("click", function () { on.decide(r, true); });
      var no = el(doc, "button", "btn btn--quiet", "Refuse");
      no.addEventListener("click", function () { on.decide(r, false); });
      card.appendChild(yes);
      card.appendChild(no);
      box.appendChild(card);
    });
    if (towers.length) {
      box.appendChild(el(doc, "h3", null, "YOUR TOWERS"));
      towers.forEach(function (t) {
        var row = el(doc, "div", "tower-row");
        row.appendChild(el(doc, "span", "mono", t.line));
        var inv = el(doc, "button", "btn", "Invite a device");
        inv.addEventListener("click", function () { on.invite(t); });
        row.appendChild(inv);
        box.appendChild(row);
      });
    }
    if (pinned.length) {
      box.appendChild(el(doc, "h3", null, "TOWERS YOU JOINED"));
      pinned.forEach(function (p) { box.appendChild(el(doc, "div", "mono", p.line)); });
    }
  }

  // renderInvite draws an invite into #edge-invite: the TUI's heading and next step, the join
  // line with a copy button, the QR and the minutes left; once used, who joined, in place.
  function renderInvite(doc, inv, outcome, on) {
    var box = doc.getElementById("edge-invite");
    if (!box) return;
    clear(box);
    box.hidden = !inv;
    if (!inv) return;
    box.appendChild(el(doc, "p", null, inv.heading));
    box.appendChild(el(doc, "p", "muted", inv.next));
    box.appendChild(el(doc, "code", "join-line", inv.line));
    var copy = el(doc, "button", "btn", "Copy");
    copy.addEventListener("click", function () { on.copy(inv.line); });
    box.appendChild(copy);
    if (outcome && outcome.over) {
      box.appendChild(el(doc, "p", "join-outcome", outcome.line));
      return;
    }
    box.appendChild(el(doc, "p", "muted small", (outcome && outcome.left) || inv.left));
    box.appendChild(el(doc, "pre", "join-qr", inv.qr));
  }

  var api = { render: render, renderInvite: renderInvite };
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  } else {
    root.RogerConsoleTowers = api;
  }
})(this);
