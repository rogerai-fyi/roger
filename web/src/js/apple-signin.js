// "Sign in with Apple" on the web — the Sign in with Apple JS (SiwA) sibling of the GitHub
// login link. Same-origin, external (CSP script-src 'self'): no inline.
//
// This is the WEB half of the flow. The broker's /auth/apple bind is owned by the
// Apple-backend and may land in a separate change; per that contract the broker accepts the
// web Services ID as a token audience and exposes the bind as POST /auth/apple ONLY (no
// callback route), so the browser must obtain Apple's identity token itself and POST it. We
// use Apple's SiwA JS SDK in POPUP mode (the only way to get the id_token back into JS on a
// static site), then POST {identity_token, raw_nonce} to the broker and, on success, land on
// the dashboard the same way the GitHub web login does after its callback.
//
// PUBLIC client config (NOT a secret — like an OAuth client_id; it ships to the browser by
// design): the Services ID and its registered Return URL. The founder sets them in
// login.html's <meta> tags (or window.ROGER_APPLE for staging). Until a Services ID is set,
// the button stays hidden — we never ship a half-wired Apple button.
//
// NONCE CONTRACT (the broker's Apple bind, same as the iOS native bind): make a random
// rawNonce, hand Apple HEX(sha256(rawNonce)) as the nonce — Apple echoes it VERBATIM into
// id_token.nonce — and POST the raw pre-image as raw_nonce. The broker recomputes
// hex(sha256(raw_nonce)) and constant-time-compares it to the token's nonce claim. HEX (not
// base64url) is deliberate: it is the encoding the broker compares against.
(function () {
  "use strict";

  var BROKER = "https://broker.rogerai.fyi";
  var SDK_SRC = "https://appleid.cdn-apple.com/appleauth/static/jsapi/appleid/1/en_US/appleid.auth.js";

  function metaContent(name) {
    var m = document.querySelector('meta[name="' + name + '"]');
    return (m && m.getAttribute("content")) || "";
  }

  // window.ROGER_APPLE override (staging/dev/tests) wins; else the page's <meta> config.
  function config() {
    var o = window.ROGER_APPLE || {};
    return {
      clientId: o.clientId || metaContent("appleid-signin-client-id"),
      redirectURI: o.redirectURI || metaContent("appleid-signin-redirect-uri"),
      scope: o.scope || metaContent("appleid-signin-scope") || "name email",
    };
  }

  // hex(sha256(s)) via Web Crypto — the exact encoding the broker compares against.
  function sha256Hex(s) {
    var bytes = new TextEncoder().encode(s);
    return crypto.subtle.digest("SHA-256", bytes).then(function (buf) {
      var b = new Uint8Array(buf), hex = "";
      for (var i = 0; i < b.length; i++) hex += b[i].toString(16).padStart(2, "0");
      return hex;
    });
  }

  // a random, single-use nonce (128 bits, hex). Its sha256 is what we hand Apple.
  function randomNonce() {
    var a = new Uint8Array(16);
    crypto.getRandomValues(a);
    var hex = "";
    for (var i = 0; i < a.length; i++) hex += a[i].toString(16).padStart(2, "0");
    return hex;
  }

  // Load Apple's SiwA JS SDK once, on demand (no request unless the user clicks Apple).
  // Inject the tag at most once, then POLL for the window.AppleID global rather than rely on a
  // one-shot load event — an already-finished script tag won't re-fire load/error, so polling
  // is what stops the promise hanging if the SDK was loaded before this runs. A timeout maps a
  // failed/blocked load to a reject (-> fail()/retry) instead of an indefinite wait.
  function loadSDK() {
    if (window.AppleID) return Promise.resolve();
    if (!document.querySelector('script[src*="appleid.auth.js"]')) {
      var s = document.createElement("script");
      s.src = SDK_SRC;
      s.async = true;
      document.head.appendChild(s);
    }
    return new Promise(function (resolve, reject) {
      var waited = 0;
      (function poll() {
        if (window.AppleID) return resolve();
        if (waited >= 8000) return reject(new Error("apple sdk load timeout"));
        waited += 100;
        setTimeout(poll, 100);
      })();
    });
  }

  function fail(btn) {
    btn.disabled = false;
    var msg = document.getElementById("appleErr");
    if (msg) msg.textContent = "Apple sign in didn't complete. Please try again.";
  }

  function start(cfg, btn) {
    btn.disabled = true;
    var rawNonce = randomNonce();
    loadSDK()
      .then(function () { return sha256Hex(rawNonce); })
      .then(function (hashedNonce) {
        window.AppleID.auth.init({
          clientId: cfg.clientId,
          scope: cfg.scope,
          redirectURI: cfg.redirectURI,
          nonce: hashedNonce, // Apple echoes this verbatim into id_token.nonce
          usePopup: true,     // id_token comes back to JS (static site: no callback route)
        });
        return window.AppleID.auth.signIn();
      })
      .then(function (res) {
        var auth = (res && res.authorization) || {};
        // name is present on first auth only; Apple never puts it in the token, so the broker
        // treats it as non-authoritative (fill-if-empty) — mirror that and pass it through.
        var nm = res && res.user && res.user.name;
        var name = nm ? [nm.firstName, nm.lastName].filter(Boolean).join(" ") : "";
        return fetch(BROKER + "/auth/apple", {
          method: "POST",
          credentials: "include", // so the broker can set the web session cookie
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            identity_token: auth.id_token,
            raw_nonce: rawNonce,
            authorization_code: auth.code || "",
            name: name,
          }),
        });
      })
      .then(function (r) {
        if (r && r.ok) {
          // The broker set the web session cookie; land on the dashboard (which loads /me)
          // exactly as the GitHub web login does after its callback.
          location.replace("/dashboard.html");
          return;
        }
        fail(btn);
      })
      .catch(function () { fail(btn); });
  }

  var btn = document.querySelector("[data-apple-signin]");
  if (!btn) return; // page has no Apple button: inert
  var cfg = config();
  if (!cfg.clientId) { // not configured yet: keep the dead button hidden, load nothing
    btn.hidden = true;
    return;
  }
  btn.hidden = false; // configured: reveal (it ships hidden so it never flashes half-wired)
  btn.addEventListener("click", function (e) {
    e.preventDefault();
    start(cfg, btn);
  });
})();
