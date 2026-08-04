// First-party sign-in: ask for a code, then type it back HERE.
//
// The code must be entered in the session that requested it. That is the whole reason we
// mail a code rather than a link: a followed link authenticates whoever followed it, in
// whatever browser followed it - including a mail scanner. Typing the code back into this
// tab is what ties the person who asked to the person who arrives.
(function () {
  "use strict";
  var BROKER = "https://broker.rogerai.fm";

  var form = document.getElementById("email-form");
  if (!form) return;

  var emailStep = document.getElementById("email-step");
  var codeStep = document.getElementById("code-step");
  var emailInput = document.getElementById("email-input");
  var codeInput = document.getElementById("code-input");
  var sendBtn = document.getElementById("email-send");
  var verifyBtn = document.getElementById("code-verify");
  var note = document.getElementById("email-note");
  var sentTo = document.getElementById("email-sent-to");
  var address = "";

  function say(msg, bad) {
    note.textContent = msg || "";
    note.className = "fine" + (bad ? " err" : "");
  }

  function post(path, body) {
    return fetch(BROKER + path, {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        return { ok: r.ok, status: r.status, body: j };
      });
    });
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    if (codeStep.hidden) sendCode();
    else verifyCode();
  });

  function sendCode() {
    address = (emailInput.value || "").trim();
    if (!address) return;
    sendBtn.disabled = true;
    say("Sending...");
    post("/auth/email/start", { email: address })
      .then(function (res) {
        sendBtn.disabled = false;
        if (!res.ok) {
          say(res.body.error || "Could not send a code. Try again in a moment.", true);
          return;
        }
        // Deliberately says "if" - the page must not reveal whether the address is known.
        emailStep.hidden = true;
        codeStep.hidden = false;
        sentTo.textContent = address;
        say("");
        codeInput.focus();
      })
      .catch(function () {
        sendBtn.disabled = false;
        say("Could not reach RogerAI. Check your connection and try again.", true);
      });
  }

  function verifyCode() {
    var code = (codeInput.value || "").trim();
    if (!code) return;
    verifyBtn.disabled = true;
    say("Checking...");
    post("/auth/email/verify", { email: address, code: code, next: nextParam() })
      .then(function (res) {
        verifyBtn.disabled = false;
        if (!res.ok) {
          say(res.body.error || "That code is not valid.", true);
          codeInput.select();
          return;
        }
        window.location.href = res.body.next || "/dashboard.html";
      })
      .catch(function () {
        verifyBtn.disabled = false;
        say("Could not reach RogerAI. Check your connection and try again.", true);
      });
  }

  // The destination rides along so a person who was sent here mid-task lands back where
  // they were. The broker re-validates it against the same same-site allowlist the OAuth
  // callbacks use - this value is a hint, never a trusted instruction.
  function nextParam() {
    try {
      return new URLSearchParams(window.location.search).get("next") || "";
    } catch (e) {
      return "";
    }
  }

  var again = document.getElementById("email-again");
  if (again) {
    again.addEventListener("click", function (e) {
      e.preventDefault();
      codeStep.hidden = true;
      emailStep.hidden = false;
      say("");
      emailInput.focus();
    });
  }
})();
