<script>
  // Device approval, /device.html (legacy: web/src/js/device.js). A command line asked to
  // sign in; a human decides. Everything goes through the broker with the session cookie:
  // this never holds a token and never sees the device code (an approver who learned it
  // could redeem the login). The wording is part of the security contract
  // (features/auth/broker_mediated_login.feature): inform without alarming.
  import { onMount } from "svelte";
  import { api, postJSON } from "#lib/api.js";

  let view = $state("ask");          // ask | confirm | approved | denied | signedout
  let code = $state("");             // the input
  let current = "";                  // the code the broker confirmed
  let askError = $state("");
  let confirmError = $state("");
  let shownCode = $state("-");
  let requested = $state("-");
  let account = $state("-");
  let signInHref = $state("/login.html");

  function ago(unix) {
    if (!unix) return "just now";
    const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
    if (s < 45) return "just now";
    if (s < 3600) return Math.floor(s / 60) + " minutes ago";
    return Math.floor(s / 3600) + " hours ago";
  }
  const normalize = (v) => (v || "").toUpperCase().replace(/[^A-Z0-9]/g, "");
  // A code may arrive pre-filled in the URL, but a page load never approves anything
  const codeFromURL = () => { const m = /[?&]code=([^&]+)/.exec(location.search); return m ? decodeURIComponent(m[1]).toUpperCase() : ""; };

  function showSignedOut() {
    // carry the code back: someone sent to sign in returns to the approval, not the dashboard
    const typed = normalize(code) || current;
    const back = "/device.html" + (typed ? "?code=" + encodeURIComponent(typed) : "");
    signInHref = "/login.html?next=" + encodeURIComponent(back);
    view = "signedout";
  }

  async function lookup() {
    const c = normalize(code);
    askError = "";
    if (!c) { askError = "Enter the code your terminal printed."; return; }
    let r;
    try { r = await api("/auth/device/pending?user_code=" + encodeURIComponent(c)); }
    catch { askError = "Could not reach RogerAI. Try again in a moment."; return; }
    if (r.status === 401) { showSignedOut(); return; }
    if (!r.ok) { askError = "That code is not valid. Check it against your terminal, or start a new sign-in."; return; }
    const info = await r.json();
    current = c;
    shownCode = info.user_code;
    requested = ago(info.requested_at);
    view = "confirm";
  }

  async function decide(path) {
    confirmError = "";
    let r;
    try { r = await postJSON("/auth/device/" + path, { user_code: current }); }
    catch { confirmError = "That did not work. The code may have expired - start a new sign-in."; return; }
    if (r.status === 401) { showSignedOut(); return; }
    if (!r.ok) {
      const j = await r.json().catch(() => ({}));
      confirmError = (j && j.error) || "That did not work. The code may have expired - start a new sign-in.";
      return;
    }
    const out = await r.json();
    if (path === "approve") { account = out.account || "your account"; view = "approved"; }
    else view = "denied";
  }

  onMount(() => {
    const pre = codeFromURL();
    if (pre) { code = pre; lookup(); }
  });
</script>
<div class="authwrap tint-panel">
<main class="card" id="card">
  <h1>Sign in to RogerAI</h1>

  <!-- Signed out: this page can do nothing until we know who is approving. -->
  <section class="panel" id="dvSignedOut" hidden={view !== "signedout"}>
    <p>A command line on this device is asking to sign in to your account.</p>
    <p>Sign in first, and you will come straight back here to approve it.</p>
    <p class="ac-keys">
      <a href={signInHref} class="primary ac-keys__btn" id="dvSignInLink">Sign in</a>
    </p>
    <p class="fine">You can use whichever sign-in you already have.</p>
  </section>

  <!-- Ask for the code. Typing it is the point: it ties the terminal to this screen. -->
  <section class="panel" id="dvAsk" hidden={view !== "ask"}>
    <p>A command line on this device is asking to sign in to your account.</p>
    <label class="dv-label" for="dvCode">Enter the code from your terminal</label>
    <input class="dv-input" id="dvCode" type="text" inputmode="latin" autocomplete="off"
           autocapitalize="characters" spellcheck="false" placeholder="ABCD-2468" bind:value={code}
           onkeydown={(e) => { if (e.key === "Enter") lookup(); }} />
    <p class="ac-keys">
      <button class="primary ac-keys__btn" id="dvLookup" onclick={lookup}>Continue</button>
    </p>
    <p class="ds-state" id="dvAskError" hidden={!askError}>{askError}</p>
  </section>

  <!-- The decision. -->
  <section class="panel" id="dvConfirm" hidden={view !== "confirm"}>
    <p>A command line on this device is asking to sign in to your account.</p>

    <dl class="stats dv-facts">
      <div><dt>Code from your terminal</dt><dd id="dvShownCode">{shownCode}</dd></div>
      <div><dt>Requested</dt><dd id="dvRequested">{requested}</dd></div>
    </dl>

    <p>Check that the code above matches the one your terminal printed.</p>
    <p class="dv-caution">
      Only approve a code you started yourself - a code someone sent you, read to
      you, or asked you to enter would sign <b>their</b> device in to <b>your</b> account.
    </p>

    <p class="ac-keys">
      <button class="primary ac-keys__btn" id="dvApprove" onclick={() => decide("approve")}>Approve</button>
      <button class="ac-keys__btn dv-deny" id="dvDeny" onclick={() => decide("deny")}>Not me - deny</button>
    </p>
    <p class="ds-state" id="dvConfirmError" hidden={!confirmError}>{confirmError}</p>
  </section>

  <!-- Outcomes. -->
  <section class="panel" id="dvApproved" hidden={view !== "approved"}>
    <h2>Signed in</h2>
    <p>Your terminal is signed in as <b id="dvAccount">{account}</b>. You can close this page.</p>
  </section>

  <section class="panel" id="dvDenied" hidden={view !== "denied"}>
    <h2>Denied</h2>
    <p>Nothing was signed in. That code can no longer be used.</p>
    <p class="fine">If you did not start this yourself, no further action is needed -
      whoever asked gained nothing.</p>
  </section>
</main>
</div>
