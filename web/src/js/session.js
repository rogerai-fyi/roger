// Logged-in nav swap. Same-origin, external (CSP script-src 'self'): no inline.
//
// On load, ask the broker who we are over a credentialed CORS request. If a user
// comes back, swap the nav "Log in" link for a compact account control: the user's
// GitHub avatar + @login, opening a small dropdown (Dashboard, Billing, Account,
// Sign out). Logged-out stays exactly as the static markup shipped. No tokens ever
// touch JS - the broker holds the session in a signed http-only cookie.
//
// Loaded on index.html AND the account pages. It only acts where a "Log in" link
// (marked data-session-login, or the homepage .nav__util login link) is present,
// so it is inert and safe on the auth-card pages that have no marketing nav.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";

  // The /account check is a CREDENTIALED request that returns 401 for a logged-out
  // visitor (and CORS-fails entirely off the production origin). We can't read the real
  // session cookie (HttpOnly, on the broker's domain), but the broker ALSO sets a
  // non-secret, readable `roger_signed_in=1` flag at login (cleared at logout) on the web
  // domain - see signedInHint in cmd/rogerai-broker/auth.go. Probe /account ONLY when that
  // flag is present, so a logged-out visitor makes ZERO request (no 401, no CORS noise);
  // on localhost the flag's domain doesn't match so it's absent too. window.ROGER_BROKER_CHECK
  // forces the probe for local-broker dev where the hint domain won't match.
  function signedInHint() {
    return /(?:^|;\s*)roger_signed_in=1(?:;|$)/.test(document.cookie);
  }

  // The "Log in" anchor to replace. Prefer an explicit hook; fall back to the
  // homepage nav's utility login link by href.
  function findLoginLink() {
    var el = document.querySelector("[data-session-login]");
    if (el) return el;
    var utils = document.querySelectorAll(".nav__utils a.nav__util");
    for (var i = 0; i < utils.length; i++) {
      var href = utils[i].getAttribute("href") || "";
      if (/\/login(\.html)?$/.test(href)) return utils[i];
    }
    return null;
  }

  var loginLink = findLoginLink();
  if (!loginLink) return; // nothing to swap on this page

  // No signed-in hint -> logged out (or an origin that can't read it, e.g. localhost):
  // leave the static nav and make NO request. This is what removes the logged-out 401.
  if (!signedInHint() && !window.ROGER_BROKER_CHECK) return;

  fetch(BROKER + "/account", { credentials: "include" })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (acct) {
      if (acct && (acct.github_login || acct.github_id)) mount(acct, loginLink);
    })
    .catch(function () { /* offline / logged-out: leave the static link as-is */ });

  function mount(acct, anchor) {
    var login = acct.github_login || "you";

    // wrapper keeps the same slot in the utility cluster
    var wrap = document.createElement("div");
    wrap.className = "acctmenu";

    // the trigger: a REAL button (accessible), avatar + @handle
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "acctmenu__btn";
    btn.setAttribute("aria-haspopup", "menu");
    btn.setAttribute("aria-expanded", "false");
    btn.setAttribute("aria-label", "Account menu for @" + login);

    if (acct.github_login || acct.github_id) {
      var img = document.createElement("img");
      img.className = "acctmenu__avatar";
      // No crossOrigin: github.com/<login>.png 302-redirects to a CDN with no CORS
      // headers, so a CORS-mode request is blocked. Plain (no-cors) <img> loads fine.
      // Prefer the CDN URL keyed by numeric id (no cookie warning, no redirect); fall
      // back to github.com/<login>.png when github_id is missing.
      img.referrerPolicy = "no-referrer";
      if (acct.github_id) {
        img.src = "https://avatars.githubusercontent.com/u/" + encodeURIComponent(acct.github_id) + "?s=48&v=4";
      } else {
        img.src = "https://github.com/" + encodeURIComponent(acct.github_login) + ".png?size=48";
      }
      img.width = 24; img.height = 24;
      img.alt = "";
      img.setAttribute("aria-hidden", "true");
      img.setAttribute("loading", "lazy");
      btn.appendChild(img);
    }
    var handle = document.createElement("span");
    handle.className = "acctmenu__handle";
    handle.textContent = "@" + login;
    btn.appendChild(handle);

    var caret = document.createElement("span");
    caret.className = "acctmenu__caret";
    caret.setAttribute("aria-hidden", "true");
    btn.appendChild(caret);

    // the dropdown (all links .html - the static host does not serve clean URLs)
    var menu = document.createElement("div");
    menu.className = "acctmenu__panel";
    menu.setAttribute("role", "menu");
    menu.hidden = true;

    // Order MUST match the account-page footer sub-nav (accountnav.html). API keys
    // is intentionally omitted here - it already has its own slot in the top nav.
    [
      { label: "Dashboard", href: "/dashboard.html" },
      { label: "Console", href: "/console.html" },
      { label: "Metrics", href: "/usage.html" },
      { label: "Billing", href: "/billing.html" },
      { label: "Payouts", href: "/payouts.html" },
      { label: "Account", href: "/account.html" }
    ].forEach(function (item) {
      var a = document.createElement("a");
      a.className = "acctmenu__item";
      a.setAttribute("role", "menuitem");
      a.href = item.href;
      a.textContent = item.label;
      menu.appendChild(a);
    });

    var sep = document.createElement("div");
    sep.className = "acctmenu__sep";
    sep.setAttribute("aria-hidden", "true");
    menu.appendChild(sep);

    var signout = document.createElement("button");
    signout.type = "button";
    signout.className = "acctmenu__item acctmenu__item--out";
    signout.setAttribute("role", "menuitem");
    signout.textContent = "Sign out";
    signout.addEventListener("click", function () {
      fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
        .then(function () { location.reload(); })
        .catch(function () { location.reload(); });
    });
    menu.appendChild(signout);

    wrap.appendChild(btn);
    wrap.appendChild(menu);
    anchor.parentNode.replaceChild(wrap, anchor);

    function setOpen(open) {
      menu.hidden = !open;
      btn.setAttribute("aria-expanded", open ? "true" : "false");
      wrap.classList.toggle("is-open", open);
    }
    function close() { setOpen(false); }

    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      setOpen(menu.hidden);
    });
    // close on outside click, Escape, or focus leaving the menu
    document.addEventListener("click", function (e) {
      if (!wrap.contains(e.target)) close();
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !menu.hidden) { close(); btn.focus(); }
    });
    wrap.addEventListener("focusout", function (e) {
      if (!wrap.contains(e.relatedTarget)) close();
    });
  }
})();
