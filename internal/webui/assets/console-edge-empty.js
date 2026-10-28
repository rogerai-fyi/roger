// console-edge-empty.js - the console's simple empty Edge (features/edge/empty_edge_simple.feature
// §6): the same words the TUI's [3] shows on a machine in no Edge (the server sends them as
// d.empty), the two ways in as buttons, the scan line with its nearby count, and one join field
// that takes an invite or an address through POST /api/edge/joinprompt. Starting an Edge is a
// terminal thing for now, so that button says so. What is typed is sent to the host and kept nowhere.
(function (root) {
  "use strict";

  function text(doc, tag, cls, s) {
    var e = doc.createElement(tag);
    if (cls) e.className = cls;
    e.textContent = s;
    return e;
  }

  function post(actions, body) {
    return actions.fetch("/api/edge/joinprompt", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    }).then(function (r) {
      return r.ok ? r.json() : r.text().then(function (t) { return { state: "failed", says: t.trim() }; });
    });
  }

  // render draws d.empty into host and returns the controls ({openJoin, join, cancel}). With no
  // d.empty (the machine is in an Edge) it hides the host and returns null.
  function render(doc, host, d, actions) {
    var e = d && d.empty;
    if (!e) { host.hidden = true; return null; }
    if (host.replaceChildren) host.replaceChildren();
    host.appendChild(text(doc, "p", "edgeempty__eyebrow", e.brand));
    var head = host.appendChild(doc.createElement("p"));
    head.className = "edgeempty__head";
    head.appendChild(text(doc, "b", "edgeempty__title", e.title));
    head.appendChild(text(doc, "span", "edgeempty__state", " · " + e.state));
    host.appendChild(text(doc, "p", "edgeempty__say", e.say));

    var ways = host.appendChild(doc.createElement("div"));
    ways.className = "edgeempty__ways";
    var startB = ways.appendChild(text(doc, "button", "edgeempty__way", e.start));
    var startIs = ways.appendChild(text(doc, "span", "edgeempty__is", e.start_is));
    var joinB = ways.appendChild(text(doc, "button", "edgeempty__way", e.join));
    ways.appendChild(text(doc, "span", "edgeempty__is", e.join_is));
    var note = host.appendChild(text(doc, "p", "edgeempty__note", ""));
    note.hidden = true;

    // The join field: one line, an invite or an address, with the outcome under it.
    var form = host.appendChild(doc.createElement("div"));
    form.className = "edgeempty__join";
    form.hidden = true;
    var field = form.appendChild(doc.createElement("input"));
    field.className = "edgeempty__field";
    field.setAttribute("type", "text");
    field.setAttribute("placeholder", e.prompt_ask);
    field.setAttribute("autocomplete", "off");
    var go = form.appendChild(text(doc, "button", "edgeempty__go", "join"));
    var says = form.appendChild(text(doc, "p", "edgeempty__says", ""));
    says.hidden = true;
    var ask = form.appendChild(doc.createElement("div"));
    ask.className = "edgeempty__ask";
    ask.hidden = true;
    var phrase = ask.appendChild(text(doc, "b", "edgeempty__phrase", ""));
    ask.appendChild(text(doc, "p", "edgeempty__phrasenote", e.phrase_note));
    var wait = ask.appendChild(text(doc, "p", "edgeempty__wait", ""));
    var cancel = ask.appendChild(text(doc, "button", "edgeempty__cancel", "cancel"));

    var scan = host.appendChild(doc.createElement("p"));
    scan.className = "edgeempty__scan" + (e.off ? " edgeempty__scan--off" : "");
    scan.appendChild(text(doc, "span", "edgeempty__eye", e.eye));
    scan.appendChild(text(doc, "span", "", " " + e.scan));
    host.appendChild(text(doc, "p", "edgeempty__nearby", e.nearby));
    host.hidden = false;

    var ctl = {};
    var timer = null;
    function show(el, on) { el.hidden = !on; }
    function outcome(a) {
      if (!a) return a;
      if (a.state === "waiting") {
        phrase.textContent = a.phrase || "";
        var left = Math.max(0, (a.until || 0) - Math.floor(Date.now() / 1000));
        wait.textContent = "waiting for the owner there to approve · " + Math.floor(left / 60) + ":" + ("0" + (left % 60)).slice(-2) + " left";
        show(says, false); show(ask, true);
        if (!timer && actions.poll !== false) timer = setTimeout(poll, 1000);
        return a;
      }
      if (timer) { clearTimeout(timer); timer = null; }
      show(ask, false);
      if (a.state === "failed") { says.textContent = a.says || "could not join"; show(says, true); }
      if (a.state === "joined") { says.textContent = "joined: this machine is a member now"; show(says, true); if (actions.refresh) actions.refresh(); }
      return a;
    }
    function poll() {
      timer = null;
      actions.fetch("/api/edge/joinprompt", { method: "GET" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(outcome)
        .catch(function () {});
    }
    ctl.openJoin = function () { show(form, true); show(note, false); if (field.focus) field.focus(); };
    ctl.join = function (line) {
      var t = (line !== undefined ? line : field.value || "").trim();
      if (!t) { says.textContent = e.prompt_ask; show(says, true); return Promise.resolve({ state: "failed", says: e.prompt_ask }); }
      field.value = "";
      return post(actions, { text: t }).then(outcome);
    };
    ctl.cancel = function () { return post(actions, { cancel: true }).then(outcome); };
    startB.addEventListener("click", function () { note.textContent = e.setup; show(note, true); show(form, false); });
    joinB.addEventListener("click", ctl.openJoin);
    go.addEventListener("click", function () { ctl.join(); });
    field.addEventListener("keydown", function (ev) { if (ev && ev.key === "Enter") ctl.join(); });
    cancel.addEventListener("click", ctl.cancel);
    if (!e.can_join) { joinB.disabled = true; startIs.textContent = e.start_is; }
    return ctl;
  }

  var api = { render: render };
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  } else {
    root.RogerConsoleEdgeEmpty = api;
  }
})(this);
