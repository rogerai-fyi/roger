<script>
  // The account hub, /account.html (legacy: web/src/js/account.js): who you are signed in as,
  // the wallet, the contact email, adding an email sign-in to a GitHub/Apple account
  // (features/auth/email_link.feature), export, delete, sign out. Thin reads over the broker
  // behind the credentialed session cookie; no token ever touches the page.
  import { onMount } from "svelte";
  import { api, postJSON, logout } from "#lib/api.js";
  import { bootGet, reason } from "./boot.js";
  import RogerFmt from "#lib/fmt.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";

  let status = $state("loading");   // loading | fault | ready
  let a = $state(null);
  let email = $state("");
  let emailLocked = $state(false);   // a verified address is a sign-in credential: shown, never edited here
  let saveMsg = $state("");
  let delMsg = $state("");
  // adding an email sign-in
  let linkEmail = $state("");
  let linkCode = $state("");
  let linkStep2 = $state(false);
  let linkMsg = $state("");
  let linkCurrent = $state("");
  let token = "", addr = "";

  const cr = (n) => (typeof n === "number" && isFinite(n) ? RogerFmt.usdSigned(n) : "-");
  const when = (ts) => (ts ? new Date(ts * 1000).toLocaleDateString() : "");
  const showLink = $derived(!!a && (a.provider === "github" || a.provider === "apple"));
  const linkNeedsLogin = $derived(showLink && a.operator === false);
  const signin = $derived(a ? ({ github: "GitHub", apple: "Apple", email: "Email" }[a.provider] || (a.github_id ? "GitHub" : "-")) : "-");
  const machines = $derived(a && typeof a.operator === "boolean" ? (a.operator ? "Linked" : "None linked to this sign-in") : "-");

  onMount(async () => {
    const res = await bootGet("/account");
    if (res.status === 401) { location.replace("/login.html"); return; }
    if (!res.data) { status = "fault"; return; }
    a = res.data;
    email = a.email || "";
    if (a.email_verified && a.email) linkCurrent = "Verified: " + a.email + ". Adding another replaces it.";
    emailLocked = a.provider === "email" || !!a.email_verified;
    status = "ready";
  });

  async function saveEmail() {
    // the refusal can say WHY (the broker's own message), not a bare failure
    try {
      const r = await api("/account", { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email }) });
      saveMsg = r.ok ? " saved" : " " + (await reason(r, "could not save"));
    } catch { saveMsg = " could not reach RogerAI"; }
  }
  async function linkSend() {
    addr = linkEmail.trim();
    if (!addr) return;
    linkMsg = "Sending...";
    try {
      const r = await postJSON("/auth/email/link/start", { email: addr });
      if (!r.ok) { linkMsg = await reason(r, "that did not work - try again"); return; }
      token = (await r.json()).token || "";
      linkStep2 = true;
      linkMsg = "If that address can receive mail, a code is on its way.";   // never says whether the address is known
    } catch { linkMsg = "Could not reach RogerAI. Check your connection and try again."; }
  }
  async function linkVerify() {
    const code = linkCode.trim();
    if (!code) return;
    try {
      const r = await postJSON("/auth/email/link/verify", { email: addr, code, token });
      if (!r.ok) { linkMsg = await reason(r, "that did not work - try again"); return; }
      const j = await r.json();
      const done = (j && j.email) || addr;
      linkStep2 = false;
      // the address is now a sign-in credential: lock the contact field without a reload
      email = done; emailLocked = true;
      linkMsg = done + " is added and verified. Signing in with an emailed code now reaches this account." + (j && j.merge_note ? " " + j.merge_note : "");
      linkCurrent = "Verified: " + done;
    } catch { linkMsg = "Could not reach RogerAI. Check your connection and try again."; }
  }
  async function exportData() {
    const r = await api("/account/export", { method: "POST" });
    if (!r.ok) return;
    const url = URL.createObjectURL(await r.blob());
    const a2 = document.createElement("a");
    a2.href = url; a2.download = "rogerai-export.json"; a2.click();
    URL.revokeObjectURL(url);
  }
  async function del() {
    if (!window.confirm("Delete your account? Identity is anonymized; financial records are retained de-identified.")) return;
    const r = await api("/account/delete", { method: "POST" });
    if (r.ok) { location.replace("/login.html"); return; }
    delMsg = " " + (await reason(r, "could not delete"));
  }
</script>
<div class="authwrap tint-panel">
<main class="card{status === 'fault' ? ' is-fault' : ''}" id="card" hidden={status === "loading"}>
  <p class="fine" id="pageError" role="alert" hidden={status !== "fault"}>Could not load this page. Try again shortly.</p>
  <h1>Account</h1>
  <p class="sub">Signed in as <b id="who">{a ? RogerFmt.who(a) : "-"}</b></p>

  <!-- WALLET: the available balance, front and center - the one number a person opens this
       page to check. Set big in mono ink (money is weight, not hue). -->
  <section class="ac-wallet">
    <p class="ac-wallet__eyebrow">Wallet balance</p>
    <p class="ac-wallet__amt" id="balance">{a ? cr(a.balance) : "-"}</p>
    <p class="ac-wallet__sub">Available to spend across every model and grant key.
      <a href="/billing.html">Add money or read the ledger</a>.</p>
  </section>

  <section class="panel">
    <h2>Profile</h2>
    <dl class="stats">
      <div><dt>Handle</dt><dd id="handle">{a ? RogerFmt.who(a) : "-"}</dd></div>
    </dl>
    <dl class="ac-meta">
      <div><dt>Sign-in</dt><dd id="signin">{signin}</dd></div>
      <div><dt>Machines</dt><dd id="machines">{machines}</dd></div>
      <div><dt>Payout status</dt><dd id="connect">{(a && a.connect && a.connect.status) || (a ? "none" : "-")}</dd></div>
      <div><dt>Member since</dt><dd id="since">{a ? (a.created_at ? when(a.created_at) : "-") : "-"}</dd></div>
    </dl>

    <p class="fine" id="identityHelp">Your machines, earnings and API keys belong to the sign-in that ran
      <code>roger login</code>. If you signed in another way and see none of them here, sign in with that one
      instead: <a href="/login.html">switch sign-in</a>.</p>

    <div class="ac-field">
      <label class="ac-field__k" for="email">Contact email <span class="ac-field__opt">optional</span></label>
      <input id="email" class="ac-field__in" type="email" placeholder="you@example.com" autocomplete="email" bind:value={email} readonly={emailLocked} />
      <span class="ac-field__hint">Used only for payout and account notices. Never shared.</span>
      <div class="ac-field__act">
        <button id="saveEmail" class="ghost" hidden={emailLocked} onclick={saveEmail}>Save email</button>
        <span class="fine" id="saveMsg">{saveMsg}</span>
      </div>
    </div>
  </section>

  <section class="panel" id="linkPanel" hidden={!showLink}>
    <h2>Sign in with email too</h2>
    <p>Add an email address to this account. Afterwards, signing in with an emailed code reaches these same
      machines, earnings and keys. Nothing is merged: balances stay where they are.</p>
    <p class="fine" id="linkCurrent">{linkCurrent}</p>
    <p class="fine" id="linkNeedsLogin" hidden={!linkNeedsLogin}>This sign-in has no machines linked yet. Run <code>roger login</code>
      on a machine with this account first, then come back.</p>
    <div id="linkForm" hidden={linkNeedsLogin}>
      <div class="ac-field" id="linkStep1">
        <label class="ac-field__k" for="linkEmail">Email address</label>
        <input id="linkEmail" class="ac-field__in" type="email" placeholder="you@example.com" autocomplete="email" bind:value={linkEmail} />
        <div class="ac-field__act"><button id="linkSend" class="ghost" type="button" onclick={linkSend}>Send code</button></div>
      </div>
      <div class="ac-field" id="linkStep2" hidden={!linkStep2}>
        <label class="ac-field__k" for="linkCode">Code from the email</label>
        <input id="linkCode" class="ac-field__in" type="text" inputmode="numeric" autocomplete="one-time-code" maxlength="6" bind:value={linkCode} />
        <div class="ac-field__act"><button id="linkVerify" class="ghost" type="button" onclick={linkVerify}>Add address</button></div>
      </div>
      <p class="fine" id="linkMsg" role="status">{linkMsg}</p>
    </div>
  </section>

  <section class="panel">
    <h2>API keys</h2>
    <p>Grant keys (<code>rog-grant_...</code>) are the home for programmatic access -
      labeled bearer keys your bots, scripts and headless boxes use, with their own caps.</p>
    <p class="ac-keys">
      <a href="/keys.html" class="primary ac-keys__btn">Manage API keys</a>
    </p>
  </section>

  <section class="panel">
    <h2>Your stations</h2>
    <p>Every machine you have put on air, with its status, what it has served and earned,
      and any evidence recorded against your account.</p>
    <p class="ac-keys">
      <a href="/stations.html" class="primary ac-keys__btn">View your stations</a>
    </p>
  </section>

  <section class="panel">
    <h2>Your data</h2>
    <p>Download a portable copy of your profile, ledger and receipts as JSON, any time.</p>
    <button id="export" class="ghost" onclick={exportData}>Export my data</button>
  </section>

  <section class="panel">
    <h2>Delete account</h2>
    <p>This clears every identifier we hold for you - your email, your GitHub link, your
      Apple link, and your name - and revokes your sessions.
      Financial records are retained, de-identified, for the legal retention window.
      Deletion is blocked while you hold a balance, unpaid earnings, or open disputes.</p>
    <button id="del" class="ghost danger" onclick={del}>Delete my account</button>
    <span class="fine" id="delMsg">{delMsg}</span>
  </section>

  <AccountNav current="account" />
  <button id="logout" class="ghost" onclick={logout}>Sign out</button>
</main>
</div>
