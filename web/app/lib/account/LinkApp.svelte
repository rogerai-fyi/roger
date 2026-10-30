<script>
  // /r.html, the phone/device link handler (legacy: web/src/js/r.js). A session's one-time
  // link code arrives in the URL FRAGMENT (#<code>) so it never reaches the broker's server
  // logs. Flow: read the code, exchange it for a per-device attach token (POST /rc/attach,
  // same account required: the code alone is never enough), stash the token, open Base
  // Station on that session. A logged-out visitor is told to log in and reopen the link (the
  // fragment survives the round trip; no auto-redirect, the OAuth flow threads no return path).
  import { onMount } from "svelte";
  import { postJSON } from "#lib/api.js";

  let msg = $state("Checking the link…");
  let needsLogin = $state(false);
  let hint = $state(false);

  onMount(async () => {
    const code = (location.hash || "").replace(/^#/, "").trim();
    hint = true;
    if (!code) { msg = "No link code in the URL. Ask the host to run /remote-control and share the link again."; return; }
    let r;
    try { r = await postJSON("/rc/attach", { code }); }
    catch { msg = "Could not reach the broker. Try again shortly."; return; }
    if (r.status === 401) { msg = "Log in to your account on this device, then reopen this link. "; needsLogin = true; return; }
    if (r.status === 404) { msg = "This link is invalid, expired, or for a different account."; return; }
    if (!r.ok) { msg = "Could not link this device. Try again shortly."; return; }
    let res;
    try { res = await r.json(); } catch { msg = "Could not reach the broker. Try again shortly."; return; }
    if (!res) return;
    try { sessionStorage.setItem("rc_open", JSON.stringify({ id: res.session_id, name: res.name, attach: res.attach_token })); } catch {}
    msg = "Linked. Opening the session...";
    location.href = "/private.html";
  });
</script>
<div class="authwrap tint-panel">
<main class="card" id="card">
  <h1>Link a session</h1>
  <p class="sub" id="msg">{msg}{#if needsLogin}<a href="/login.html">Log in</a>{/if}</p>
  <p class="fine" id="hint" hidden={!hint}>This links THIS device to a remote agent session on your
    account. Private - account-locked, and carried over TLS to the broker that relays it - encrypted in transit, not end-to-end.</p>
</main>
</div>
