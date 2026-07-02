// /r.html - the phone/device link handler. A session's one-time link code arrives in the URL
// FRAGMENT (#<code>) so it never reaches the broker's server logs. Flow: read the code, ensure
// the visitor is logged into the SAME account (the code alone is not enough), exchange it for a
// per-device attach token (POST /rc/attach), stash the token, and open Base Station on that
// session. A logged-out visitor is sent to log in first (the fragment survives the round-trip).
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";
  function api(path, opts) { opts = opts || {}; opts.credentials = "include"; return fetch(BROKER + path, opts); }
  function msg(t) { var el = document.getElementById("msg"); if (el) el.textContent = t; }

  document.addEventListener("DOMContentLoaded", function () {
    var code = (location.hash || "").replace(/^#/, "").trim();
    var hint = document.getElementById("hint"); if (hint) hint.hidden = false;
    if (!code) { msg("No link code in the URL. Ask the host to run /remote-control and share the link again."); return; }

    // Same-account is required: attach exchanges the code, but only for the owner. A 404 means
    // wrong account / expired / already used - the uniform, non-enumerable answer.
    api("/rc/attach", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: code }),
    }).then(function (r) {
      if (r.status === 401) {
        // Not logged in on this device. The code alone is never enough — same-account is
        // required — so ask the visitor to log in, then reopen the link (the fragment carries
        // the code). We deliberately do NOT auto-redirect: the code must survive the round trip
        // and the OAuth flow does not thread a return path.
        var m = document.getElementById("msg");
        if (m) {
          m.textContent = "Log in to your account on this device, then reopen this link. ";
          var a = document.createElement("a"); a.href = "/login.html"; a.textContent = "Log in";
          m.appendChild(a);
        }
        return null;
      }
      if (r.status === 404) { msg("This link is invalid, expired, or for a different account."); return null; }
      if (!r.ok) { msg("Could not link this device. Try again shortly."); return null; }
      return r.json();
    }).then(function (res) {
      if (!res) return;
      try {
        sessionStorage.setItem("rc_open", JSON.stringify({
          id: res.session_id, name: res.name, attach: res.attach_token,
        }));
      } catch (e) {}
      msg("Linked. Opening the session...");
      location.href = "/private.html";
    }).catch(function () { msg("Could not reach the broker. Try again shortly."); });
  });
})();
