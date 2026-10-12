// console-welcome.js - the console's one-time welcome card (features/onboarding/welcome_card.feature
// §7): the same six lines as the TUI's card, the same destinations, and the same welcome_seen flag,
// so seeing the card in either front-end counts for both. In a browser "Web console" would point at
// itself, so that line is the terminal app instead; "Log in" is the console's own login.
(function (root) {
  "use strict";

  // The six lines. Keep in step with internal/look WelcomeOptions (a test compares them).
  var OPTIONS = [
    { key: "1", glyph: "◉", label: "Chat with a model", what: "pick a model and talk", dest: "tab:browse" },
    { key: "0", glyph: "▸", label: "Run an agent", what: "let a model read, write, run code", dest: "tab:chat" },
    { key: "2", glyph: "((•))", label: "Share a model", what: "put a model on air (free or paid)", dest: "tab:share" },
    { key: "3", glyph: "▟•▙", label: "Edge network", what: "join your own devices together", dest: "tab:edge" },
    { key: "w", glyph: "◇", label: "Terminal app", what: "run roger in a terminal for the full radio", dest: "terminal" },
    { key: "l", glyph: "◈", label: "Log in", what: "starter credit for new accounts; earn by sharing", dest: "login" },
  ];

  function options() { return OPTIONS.slice(); }

  function text(doc, tag, cls, s) {
    var e = doc.createElement(tag);
    if (cls) e.className = cls;
    e.textContent = s;
    return e;
  }

  // mount reads whether the card was seen and, if not, draws it into #welcome. It resolves to the
  // card's controls ({pick, close}), or null when there is nothing to show.
  function mount(doc, actions) {
    var host = doc.getElementById("welcome");
    if (!host) return Promise.resolve(null);
    return actions.fetch("/api/welcome", { method: "GET" })
      .then(function (r) { return r.ok ? r.json() : { seen: true }; })
      .then(function (st) {
        if (!st || st.seen) { host.hidden = true; return null; }
        return draw(doc, host, actions);
      })
      .catch(function () { host.hidden = true; return null; });
  }

  function draw(doc, host, actions) {
    if (host.replaceChildren) host.replaceChildren();
    host.appendChild(text(doc, "p", "welcome__eyebrow", "FIRST TIME HERE"));
    host.appendChild(text(doc, "p", "welcome__skip", "pick one, or close to skip"));
    var list = host.appendChild(doc.createElement("ul"));
    list.className = "welcome__list";
    var card = {};
    OPTIONS.forEach(function (o) {
      var li = list.appendChild(doc.createElement("li"));
      var b = li.appendChild(doc.createElement("button"));
      b.className = "welcome__opt";
      b.setAttribute("data-welcome", o.key);
      b.appendChild(text(doc, "span", "welcome__key", o.key));
      b.appendChild(text(doc, "span", "welcome__glyph", o.glyph));
      b.appendChild(text(doc, "span", "welcome__label", o.label));
      b.appendChild(text(doc, "span", "welcome__what", o.what));
      b.addEventListener("click", function () { card.pick(o.key); });
    });
    var term = host.appendChild(text(doc, "p", "welcome__term", ""));
    term.hidden = true;
    var close = host.appendChild(text(doc, "button", "welcome__close", "close"));
    close.addEventListener("click", function () { card.close(); });
    host.hidden = false;

    card.close = function () {
      host.hidden = true;
      return actions.fetch("/api/welcome/seen", { method: "POST" }).then(function () {});
    };
    card.pick = function (key) {
      var o = OPTIONS.filter(function (x) { return x.key === key; })[0];
      if (!o) return Promise.resolve();
      if (o.dest === "terminal") {
        // The terminal app is a command, not a page: say it, and keep the card up.
        term.textContent = "in a terminal, run: roger";
        term.hidden = false;
        return Promise.resolve();
      }
      return card.close().then(function () {
        if (o.dest === "login") actions.login();
        else actions.tab(o.dest.slice(4));
      });
    };
    return card;
  }

  var api = { options: options, mount: mount };
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  } else {
    root.RogerConsoleWelcome = api;
  }
})(this);
