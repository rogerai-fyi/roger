// Device-approval page for /device.html.
//
// A command line asked to sign in; a human decides. Everything here goes through the
// broker with the session cookie, so this file never holds a token and never sees the
// device code - that stays between the CLI and the broker, because an approver who
// learned it could redeem the login themselves.
//
// The wording is part of the security contract (features/auth/broker_mediated_login.feature):
// inform without alarming. Someone frightened by a routine sign-in learns to click
// through warnings, which is the opposite of what we need.
(function () {
  var BROKER = "https://broker.rogerai.fm";

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = "include";
    return fetch(BROKER + path, opts);
  }
  function el(id) { return document.getElementById(id); }
  function show(id) { var e = el(id); if (e) e.hidden = false; }
  function hide(id) { var e = el(id); if (e) e.hidden = true; }
  function text(id, v) { var e = el(id); if (e) e.textContent = v; }
  function fail(id, msg) { var e = el(id); if (e) { e.textContent = msg; e.hidden = false; } }

  function ago(unix) {
    if (!unix) return "just now";
    var s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
    if (s < 45) return "just now";
    if (s < 3600) return Math.floor(s / 60) + " minutes ago";
    return Math.floor(s / 3600) + " hours ago";
  }

  // A code may arrive pre-filled in the URL, but a page load must never approve
  // anything - it only saves the person retyping it.
  function codeFromURL() {
    var m = /[?&]code=([^&]+)/.exec(location.search);
    return m ? decodeURIComponent(m[1]).toUpperCase() : "";
  }

  function normalize(v) {
    return (v || "").toUpperCase().replace(/[^A-Z0-9]/g, "");
  }

  var current = "";

  function lookup(code) {
    code = normalize(code);
    if (!code) { fail("dvAskError", "Enter the code your terminal printed."); return; }
    api("/auth/device/pending?user_code=" + encodeURIComponent(code)).then(function (r) {
      if (r.status === 401) { showSignedOut(); return null; }
      if (!r.ok) {
        fail("dvAskError", "That code is not valid. Check it against your terminal, or start a new sign-in.");
        return null;
      }
      return r.json();
    }).then(function (info) {
      if (!info) return;
      current = code;
      text("dvShownCode", info.user_code);
      text("dvRequested", ago(info.requested_at));
      hide("dvAsk");
      show("dvConfirm");
    }).catch(function () {
      fail("dvAskError", "Could not reach RogerAI. Try again in a moment.");
    });
  }

  function decide(path, onOK) {
    api("/auth/device/" + path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ user_code: current }),
    }).then(function (r) {
      if (r.status === 401) { showSignedOut(); return null; }
      if (!r.ok) { return r.json().catch(function () { return {}; }).then(function (j) { throw j; }); }
      return r.json();
    }).then(function (out) {
      if (out) onOK(out);
    }).catch(function (j) {
      fail("dvConfirmError", (j && j.error) || "That did not work. The code may have expired - start a new sign-in.");
    });
  }

  function showSignedOut() {
    hide("dvAsk"); hide("dvConfirm");
    // Carry the code back with us: someone sent to sign in should return to the approval
    // they were in the middle of, not be dropped on the dashboard having lost it.
    var typed = normalize(el("dvCode") && el("dvCode").value) || current;
    var back = "/device.html" + (typed ? "?code=" + encodeURIComponent(typed) : "");
    var link = el("dvSignInLink");
    if (link) link.href = "/login.html?next=" + encodeURIComponent(back);
    show("dvSignedOut");
  }

  el("dvLookup").addEventListener("click", function () { lookup(el("dvCode").value); });
  el("dvCode").addEventListener("keydown", function (e) {
    if (e.key === "Enter") lookup(el("dvCode").value);
  });
  el("dvApprove").addEventListener("click", function () {
    decide("approve", function (out) {
      text("dvAccount", out.account || "your account");
      hide("dvConfirm");
      show("dvApproved");
    });
  });
  el("dvDeny").addEventListener("click", function () {
    decide("deny", function () {
      hide("dvConfirm");
      show("dvDenied");
    });
  });

  // Are we signed in? The pending lookup answers it, so ask with the pre-filled code if
  // there is one, else just show the code prompt and let the first lookup tell us.
  var pre = codeFromURL();
  if (pre) {
    el("dvCode").value = pre;
    show("dvAsk");
    lookup(pre);
  } else {
    show("dvAsk");
  }
})();
