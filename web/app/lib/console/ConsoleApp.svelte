<script>
  // The console card: the live operational view (the legacy web/src/js/console.js state
  // machine), mounted as an island by app/islands/console.js.
  // GET /console -> { role, events[], counters{} }. Login gate: /account confirms the session;
  // ONLY a failed /account redirects to login. A /console error with a confirmed session is a
  // fault to show, never a reason to sign out (the login <-> dashboard loop).
  import { onMount } from "svelte";
  import { account, api, logout } from "#lib/api.js";
  import RogerFmt from "#lib/fmt.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";
  import ModelsPanel from "#lib/console/ModelsPanel.svelte";
  import { n0, cr, num, kfmt, ago, shortId, rfmt } from "#lib/console/format.js";

  let signedIn = $state(false);
  let who = $state("-");
  let status = $state("loading");   // loading | fault | ready
  let data = $state(null);
  const isOwner = $derived(!!data && data.role === "owner");
  const events = $derived((data && data.events) || []);
  const c = $derived((data && data.counters) || {});

  onMount(async () => {
    let acct = null;
    try { acct = await account(); } catch { acct = null; }
    if (!acct) { location.replace("/login.html"); return; }
    signedIn = true;
    who = RogerFmt.handle(acct.github_login);
    try {
      const r = await api("/console");
      if (!r.ok) { status = "fault"; return; }
      data = await r.json();
      status = "ready";
    } catch { status = "fault"; }
  });
</script>
<div class="authwrap tint-panel">
<main class="card" id="card" hidden={!signedIn}>
  <h1>Console</h1>
  <p class="sub">Signed in as <b id="who">{who}</b> &middot; <span id="role">{status === "ready" ? (isOwner ? "Operator console" : "Consumer console") : "live activity"}</span></p>

  {#if status === "loading"}<p class="cn-state" id="cnLoading">Tuning in...</p>{/if}
  {#if status === "fault"}<p class="cn-state" id="cnError">Could not reach the console. Try again shortly.</p>{/if}

  {#if status === "ready"}
  <dl class="stats cn-counters" id="counters">
    <div><dt>Requests today</dt><dd id="ctrReq" use:rfmt={n0(c.requests_today)}>{num(n0(c.requests_today))}</dd></div>
    <div><dt id="ctrMoneyK">{isOwner ? "Earned today" : "Spend today"}</dt><dd id="ctrMoney">{cr(n0(isOwner ? c.earned_today : c.spend_today))}</dd></div>
    {#if isOwner}
    <div id="tileNodes"><dt>Active nodes</dt><dd id="ctrNodes">{num(n0(c.active_nodes))}</dd></div>
    <div id="tileBands"><dt>Active bands</dt><dd id="ctrBands">{num(n0(c.active_bands))}</dd></div>
    {/if}
  </dl>

  {#if events.length}
  <section class="panel" id="logWrap">
    <h2>Recent activity</h2>
    <div class="cn-tablewrap">
      <table class="cn-table">
        <thead>
          <tr>
            <th scope="col" class="cn-st" aria-label="Status"></th>
            <th scope="col">Model</th>
            <th scope="col" class="cn-node">Node</th>
            <th scope="col" class="num">Tokens in/out</th>
            <th scope="col" class="num" id="colMoney">{isOwner ? "Earned" : "Cost"}</th>
            <th scope="col">Receipt</th>
            <th scope="col" class="num">When</th>
          </tr>
        </thead>
        <tbody id="logRows">
          {#each events as e}
          <tr class="cn-row">
            <td class="cn-st"><span class="cn-dot {e.success ? 'cn-dot--ok' : 'cn-dot--fail'}" title={e.success ? "ok" : "failed"}>{e.success ? "◉" : "○"}</span></td>
            <td class="cn-model" title={e.model || ""}>{e.model || "(unknown)"}</td>
            <td class="cn-node" title={e.node || ""}>{e.node || "-"}</td>
            <td class="num cn-tok" title="{n0(e.tokens_in)} in / {n0(e.tokens_out)} out tokens">{kfmt(e.tokens_in)} / {kfmt(e.tokens_out)}</td>
            <td class="num cn-money">{cr(n0(isOwner ? e.earned : e.cost))}</td>
            <td class="cn-id" title={e.request_id || ""}>{shortId(e.request_id)}</td>
            <td class="num cn-time" title={new Date(n0(e.ts) * 1000).toLocaleString()}>{ago(e.ts)}</td>
          </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="fine">Each row is a settled receipt: model, node callsign, tokens, the ledger amount, its receipt id and how long ago. Newest first.</p>
  </section>
  {:else if isOwner}
  <p class="cn-state" id="logEmptyOwner">No requests served yet. Put a GPU on air with <code>roger share</code> and the traffic it serves shows here, receipt by receipt.</p>
  {:else}
  <p class="cn-state" id="logEmptyConsumer">No requests yet. Tune in with <code>roger use &lt;model&gt;</code> and your lineage lands here.</p>
  {/if}

  {#if isOwner}<ModelsPanel />{/if}
  {/if}

  <section class="panel">
    <h2>Put a GPU on air</h2>
    <pre class="cmd">curl -fsSL https://rogerai.fm/install.sh | sh
roger login
roger share</pre>
  </section>

  <AccountNav current="console" />
  <button id="logout" class="ghost" onclick={logout}>Sign out</button>
</main>
</div>
