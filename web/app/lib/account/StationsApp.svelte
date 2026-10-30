<script>
  // Your stations, /stations.html (legacy: web/src/js/stations.js). One credentialed read of
  // GET /stations: the broker scopes the list to the signed-in owner's node bindings, so
  // nothing here sends or trusts a node id from the page. The page tells the truth about WHY
  // it has nothing to show: a 403 (no operator account behind this sign-in) is the normal
  // state for a consumer, never a load failure; only a 5xx or a dropped connection is an
  // error. The chain state is an AUDIT SIGNAL, never a penalty, and the copy says so.
  import { onMount } from "svelte";
  import { api, postJSON } from "#lib/api.js";
  import { bootGet } from "./boot.js";
  import RogerFmt from "#lib/fmt.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";

  let view = $state("loading");     // loading | nooperator | fault | ready
  let who = $state("-");
  let stations = $state([]);
  let strikes = $state([]);
  let appeals = $state([]);
  let appealReason = $state("");
  let appealMsg = $state("");
  let appealBusy = $state(false);   // disabled in flight and after success: one appeal per click

  const money = (n) => (typeof n === "number" && isFinite(n) ? RogerFmt.usdSigned(n) : "-");
  function ago(unix) {
    if (!unix) return "never";
    const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
    if (s < 60) return s + "s ago";
    if (s < 3600) return Math.floor(s / 60) + "m ago";
    if (s < 86400) return Math.floor(s / 3600) + "h ago";
    return Math.floor(s / 86400) + "d ago";
  }
  // chainLabel turns the broker's chain_state into copy an operator can act on
  function chainLabel(st) {
    const ch = st.chain || {};
    switch (st.chain_state) {
      case "continuous": return { cls: "ok", text: "Chain continuous" };
      case "breaks-recorded": return { cls: "warn", text: ch.breaks + (ch.breaks === 1 ? " break recorded" : " breaks recorded") + " - audit signal only" };
      default: return { cls: "muted", text: "No receipts yet" };
    }
  }
  function offerLine(o) {
    const bits = [o.model];
    if (o.modality && o.modality !== "chat") bits.push(o.modality);
    bits.push(money(o.price_in) + " in / " + money(o.price_out) + " out per 1M");
    if (o.ctx) bits.push(o.ctx.toLocaleString() + " ctx");
    return bits.join(" \u00b7 ");
  }
  const onAir = $derived(stations.filter((s) => s.on_air).length);
  const earned = $derived(stations.reduce((n, s) => n + (typeof s.earnings === "number" ? s.earnings : 0), 0));
  const served = $derived(stations.reduce((n, s) => n + (s.recent_served || 0), 0));

  // Appeals already filed, so a reload does not look like nothing was sent. One read, only
  // when the account has marks (the only time the section shows).
  async function loadAppeals() {
    const res = await bootGet("/owner/appeal");
    const list = (res.data && res.data.appeals) || [];
    if (list.length) appeals = list;
  }
  // The account is never sent: the broker takes it from the session, so an appeal can only
  // be filed for the caller.
  async function sendAppeal() {
    if (appealBusy) return;
    const r0 = appealReason.trim();
    if (!r0) { appealMsg = "Write a short reason first."; return; }
    appealBusy = true; appealMsg = "Sending...";
    try {
      const r = await postJSON("/owner/appeal", { reason: r0 });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        // stays disabled: the appeal is filed, and a repeat would file another
        appealMsg = "Appeal " + (j.appeal_id ? "#" + j.appeal_id + " " : "") + "sent. A person will review it with the evidence above.";
        loadAppeals();
      } else {
        appealBusy = false;   // a refusal can be retried
        appealMsg = r.status === 401 ? "Your session expired - sign in again, then send the appeal."
          : (j && j.error && j.error.message) || "That did not go through - try again.";
      }
    } catch { appealBusy = false; appealMsg = "Could not reach RogerAI. Check your connection and try again."; }
  }

  onMount(async () => {
    const res = await bootGet("/stations");
    if (res.status === 401) { location.replace("/login.html?next=" + encodeURIComponent("/stations.html")); return; }   // sign in, then come straight back
    if (res.status === 403) {
      view = "nooperator";
      const a = await bootGet("/account");
      who = a.data && a.data.github_login ? RogerFmt.handle(a.data.github_login) : "your account";
      return;
    }
    if (!res.data) { view = "fault"; return; }
    const d = res.data;
    who = d.github_login ? RogerFmt.handle(d.github_login) : "your account";
    stations = d.stations || [];
    strikes = d.strikes || [];
    view = "ready";
    if (strikes.length) loadAppeals();   // an account with marks and no live station is who most needs the appeal
  });
</script>
<div class="authwrap tint-panel">
<main class="card" id="card" hidden={view === "loading"}>
  <h1>Your stations</h1>
  <p class="sub">Signed in as <b id="who">{who}</b> &middot; every station registered to your account</p>

  <p class="ds-state" id="stLoading" hidden={view !== "loading"}>Loading your stations...</p>
  <p class="ds-state" id="stError" hidden={view !== "fault"}>Could not load your stations. Try again shortly.</p>

  <section class="acct-state tint-panel" id="stNoOperator" hidden={view !== "nooperator"}>
    <h2>You are not sharing from this sign-in</h2>
    <p>Stations belong to the account that put them on air, and nothing is registered to the account you are signed in with.</p>
    <p><b>Already sharing?</b> Sign in with the same account you used for <code>roger login</code> (GitHub or Apple) and your stations will be here.</p>
    <p><b>Not sharing yet?</b> Put a GPU on air from this account:</p>
    <pre class="cmd">roger login
roger share</pre>
    <p class="fine"><a href="/login.html">Switch sign-in</a> &middot; <a href="/account.html">Account</a></p>
  </section>

  <section class="acct-state tint-panel" id="stEmpty" hidden={view !== "ready" || stations.length > 0}>
    <h2>No stations yet</h2>
    <p>Nothing is registered to your account. Put a GPU on air and it will appear here with its status, usage and earnings.</p>
    <pre class="cmd">roger share</pre>
    <p class="fine">A station is a machine serving a model through the network. Registration binds it to this account, so only you can see its numbers.</p>
  </section>

  <dl class="stats ds-tiles" id="stTiles" hidden={view !== "ready" || !stations.length}>
    <div><dt>Stations</dt><dd id="tileCount">{stations.length ? stations.length : "-"}</dd></div>
    <div><dt>On air</dt><dd id="tileOnAir">{stations.length ? `${onAir} of ${stations.length}` : "-"}</dd></div>
    <div><dt>Earned</dt><dd id="tileEarned">{stations.length ? money(earned) : "-"}</dd></div>
    <div><dt>Recent requests</dt><dd id="tileServed">{stations.length ? served : "-"}</dd></div>
  </dl>

  <section class="panel" id="stList" hidden={view !== "ready" || !stations.length}>
    <h2>Stations</h2>
    <div id="stRows">
      {#each stations as st}
        {@const chain = chainLabel(st)}
        <article class="st-row">
          <h3>{st.node_id} <span class="badge {st.on_air ? 'on' : 'off'}">{st.on_air ? "ON AIR" : "OFF AIR"}</span>
            {#if st.confidential}<span class="badge">CONFIDENTIAL</span>{/if}
            {#if st.private}<span class="badge">PRIVATE BAND</span>{/if}
            {#if st.curated}<span class="badge">&raquo; CURATED &middot; {st.curated_provider || ""}</span>{/if}</h3>
          <p class="fine">Registered {ago(st.registered_at)} &middot; last seen {ago(st.last_seen)}{st.region ? " \u00b7 " + st.region : ""}{st.hw ? " \u00b7 " + st.hw : ""}</p>
          {#if (st.offers || []).length}
            <ul>{#each st.offers as o}<li class="fine">{offerLine(o)}</li>{/each}</ul>
          {:else}
            <p class="fine">No offers published.</p>
          {/if}
          <!-- A curated station's credits are TWO things and the copy keeps them apart
               (features/curated/curated_web.feature, amended by the 50/50 ruling): the
               reimbursement of the provider's list price - never income - plus the operator's
               half of the routing fee, which is. The human branch says Earned. -->
          <p class="fine">{#if st.curated}Curated credits {st.earnings_unavailable ? "unavailable" : money(st.earnings)} (your provider list reimbursed + your half of the routing fee){:else}Earned {st.earnings_unavailable ? "unavailable" : money(st.earnings)}{/if} &middot; {st.recent_served || 0} recent requests &middot; <span class={chain.cls}>{chain.text}</span></p>
        </article>
      {/each}
    </div>
  </section>

  <section class="panel" id="stEvidence" hidden={view !== "ready" || !strikes.length}>
    <h2>Account evidence</h2>
    <p class="fine">
      Marks recorded against your account, each with the evidence that produced it.
      These are shown to you in full because they can affect payouts - if you think one
      is wrong, you can appeal it.
    </p>
    <div id="stStrikes">
      {#each strikes as k}
        <article class="st-row">
          <h3>{k.kind}</h3>
          <p class="fine">{ago(k.created_at)}</p>
          <pre class="cmd">{k.evidence}</pre>
        </article>
      {/each}
    </div>
    <div id="stAppeal" hidden={!strikes.length}>
      <h3>Appeal</h3>
      <p class="fine">Tell us why a mark is wrong. A person reviews every appeal, with the evidence above.</p>
      <label class="lt-field"><span>Your reason</span>
        <textarea id="appealReason" rows="4" maxlength="4096" placeholder="What happened, and why this is a mistake" bind:value={appealReason}></textarea></label>
      <p><button id="appealSend" class="ghost" type="button" disabled={appealBusy} onclick={sendAppeal}>Send appeal</button></p>
      <p class="fine" id="appealMsg" role="status">{appealMsg}</p>
      <div id="appealHistory" hidden={!appeals.length}>
        {#if appeals.length}<h3>Your appeals</h3>{/if}
        {#each appeals as a}<p class="fine">#{a.id} &middot; {a.state}{a.reason ? " \u00b7 " + String(a.reason).slice(0, 120) : ""}</p>{/each}
      </div>
    </div>
    <p class="fine"><a href="/account.html">Account settings</a> &middot; <a href="/payouts.html">Payouts</a></p>
  </section>

  <section class="panel" id="stChainHelp" hidden={view !== "ready" || !stations.length}>
    <h2>About the receipt chain</h2>
    <p>
      Every request your station serves produces a signed receipt, and each receipt
      links to the one before it. The network records where your station's chain was,
      so a gap is visible rather than silent.
    </p>
    <p class="fine">
      A break is an <b>audit signal, not a penalty</b>. Restarting your station starts
      a new chain, which reads as a break today - that is expected and does not affect
      your earnings or standing.
    </p>
  </section>

  <AccountNav current="stations" />
</main>
</div>
