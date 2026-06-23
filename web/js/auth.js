// Minimal auth glue for /login and /dashboard.
//
// Auto-route per AUTH-DESIGN: consuming needs no login; monetizing needs it.
//   - /dashboard: if no valid broker session, bounce to /login; else show the
//     logged-in GitHub identity. Sign out clears the session cookie.
//   - /login: if already logged in, skip straight to /dashboard.
//
// The broker holds the session (signed http-only cookie); these pages just ask it
// "who am I?" over credentialed CORS. No tokens ever touch JS.
(function () {
  var BROKER = "https://broker.rogerai.fyi";

  function account() {
    return fetch(BROKER + "/account", { credentials: "include" }).then(function (r) {
      return r.ok ? r.json() : null;
    }).catch(function () { return null; });
  }

  var path = location.pathname.replace(/\/$/, "");

  if (path.endsWith("/dashboard")) {
    account().then(function (a) {
      if (!a) { location.replace("/login"); return; }
      var who = document.getElementById("who");
      if (who) who.textContent = "@" + a.github_login;
      var card = document.getElementById("card");
      if (card) card.hidden = false;
      var out = document.getElementById("logout");
      if (out) out.addEventListener("click", function () {
        fetch(BROKER + "/auth/logout", { method: "POST", credentials: "include" })
          .then(function () { location.replace("/login"); });
      });
    });
  } else if (path.endsWith("/login")) {
    account().then(function (a) {
      if (a) location.replace("/dashboard");
    });
  }
})();
