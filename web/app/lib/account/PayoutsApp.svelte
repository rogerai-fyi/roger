<script>
  // Payouts, /payouts.html (legacy: web/src/js/payouts.js): earnings provenance (the held ->
  // payable -> paid split, the hold timeline, the release ladder, the per-model/per-node
  // breakdown, Towers and relay, payout history with its funding receipts, the ledger) plus
  // Stripe Connect status and the payout request. Thin reads over the broker behind the
  // credentialed session cookie (no token ever touches JS). Logged-out visitors go to /login.
  // Honest-empty: every section says plainly when the broker returned nothing, and never
  // invents a number.
  import { onMount } from "svelte";
  import { api, getJSON, logout } from "#lib/api.js";
  import { bootGet, reason } from "./boot.js";
  import { cr, when, rel } from "#lib/format.js";
  import RogerFmt from "#lib/fmt.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";

  let status = $state("loading");   // loading | fault | nooperator | ready
  let split = $state({ held: 0, payable: 0, paid: 0, reserved: 0, next_release: 0 });
  let connect = $state(null);       // /connect/status, once it answered
  let earnings = $state(null);      // /payouts/earnings, once it answered (null: not yet, or refused)
  let earningsDone = $state(false);
  let history = $state(null);       // /payouts/history, once it answered
  let breakdown = $state(null);     // the earning models, once /metrics/provider answered (or failed: [])
  let breakdownWindow = $state("Earnings over the last 30 days, by what you served.");
  let towers = $state(null);        // /tower/status towers[], once it answered (or failed: [])
  let payMsg = $state("");
  let open = $state({});            // payout id -> its drawer is open
  let lots = $state({});            // payout id -> "loading" | the receipts (fetched once, then kept)

  // ---- the held -> payable -> paid meter: segments proportional to the three amounts; a
  // zero total collapses to an empty rail (honest-empty, no fake fill) ----
  const total = $derived(split.held + split.payable + split.paid);
  const pct = (v) => (total > 0 ? (v / total) * 100 : 0);

  // ---- the hold timeline: an earning is always Earned; Held is active while there is held
  // money (with the soonest release date); Payable is active with payable money, done once
  // something was paid; Paid is done when lifetime paid > 0. The policy facts (hold days,
  // minimum, schedule) come from /connect/status, which labels them truthfully. ----
  const holdDays = $derived(connect && typeof connect.hold_days === "number" ? connect.hold_days : null);
  const minNote = $derived(connect && typeof connect.min_payout === "number" ? cr(connect.min_payout) : "$25.00");
  const schedule = $derived((connect && connect.schedule) || "monthly");
  const connectStatus = $derived(connect ? connect.status || "none" : "-");
  const active = $derived(connectStatus === "active");
  const tlHeld = $derived(total > 0 ? (split.held > 0 ? "is-active" : "is-done") : "");
  const tlPayable = $derived(total > 0 && split.payable > 0 ? "is-active" : total > 0 && split.paid > 0 ? "is-done" : "");
  const tlPaid = $derived(total > 0 && split.paid > 0 ? "is-done" : "");
  const heldSub = $derived(holdDays !== null ? "held for " + holdDays + " days" : split.held > 0 ? cr(split.held) + " in the hold window" : "in the hold window");
  const heldWhen = $derived(split.held > 0 && split.next_release ? "next clears " + when(split.next_release) + " (" + rel(split.next_release) + ")" : "");
  const payableSub = $derived(split.payable > 0 ? cr(split.payable) + " cleared, withdrawable now" : "hold cleared, withdrawable");

  // ---- the release LADDER: /payouts/earnings carries releases[], the still-held lots
  // bucketed by clear date, so the page shows every dated release, not only the soonest ----
  const ladder = $derived(((earnings && earnings.releases) || []).filter((r) => (r.amount || 0) > 0));

  // ---- TOWERS: the serving/relay split rides on the same earnings response (tower lots are
  // tagged at settlement) and the fleet comes from /tower/status, scoped to the caller's own
  // Towers. The panel appears once BOTH answered and only with something real to show (relay
  // earnings or an enrolled Tower), so it stays invisible for operators who only serve. ----
  const serving = $derived(earnings ? earnings.serving || 0 : 0);
  const relay = $derived(earnings ? earnings.tower_relay || 0 : 0);
  const towerPanel = $derived(earningsDone && towers !== null && (relay > 0 || towers.length > 0));

  const payouts = $derived((history && history.payouts) || []);
  const ledger = $derived((history && history.ledger) || []);

  const count = (n) => RogerFmt.count(n || 0);
  // Human label for a ledger kind.
  function kindLabel(k) {
    if (k === "payout") return "Payout";
    if (k === "chargeback") return "Clawback (dispute)";
    if (k === "adjustment") return "Adjustment";
    return k || "entry";
  }
  function towerMeta(t) {
    const n = (t.routable || []).length;
    const bits = [t.link_live ? "link live" : "link down", n + (n === 1 ? " station" : " stations") + " routable"];
    if (t.lease_expires) bits.push("lease " + rel(t.lease_expires));
    return bits.join(" · ");
  }

  async function loadConnect() {
    const s = await getJSON("/connect/status");
    if (s) connect = s;
  }
  async function loadEarnings() {
    earnings = await getJSON("/payouts/earnings");
    earningsDone = true;
  }
  async function loadHistory() {
    history = await getJSON("/payouts/history");
    open = {}; lots = {};
  }
  async function loadBreakdown() {
    const m = await getJSON("/metrics/provider?days=30");
    if (m && m.period_days) breakdownWindow = "Earnings over the last " + m.period_days + " days, by what you served.";
    breakdown = ((m && m.models) || []).filter((x) => (x.earnings_usd || 0) > 0).sort((a, b) => (b.earnings_usd || 0) - (a.earnings_usd || 0));
  }
  async function loadTowers() {
    const res = await getJSON("/tower/status");
    towers = (res && res.towers) || [];
  }

  // One expandable payout row: the drawer lazily fetches /payouts/{id}/lots, the exact earning
  // receipts (model, node, gross, request id, date) that funded the transfer. Fetched once.
  async function toggle(p, i) {
    if (open[i]) { open[i] = false; return; }
    open[i] = true;
    if (lots[i] !== undefined) return;
    lots[i] = "loading";
    const res = await getJSON("/payouts/" + encodeURIComponent(p.id) + "/lots");
    lots[i] = (res && res.lots) || [];
  }

  async function onboard() {
    let r = null;
    try { const res = await api("/connect/onboard", { method: "POST" }); r = res.ok ? await res.json() : null; } catch { r = null; }
    if (r && r.url) location.href = r.url;
    else payMsg = "could not start onboarding";
  }
  async function request() {
    payMsg = "requesting...";
    try {
      const r = await api("/payouts/request", { method: "POST" });
      if (r.ok) {
        try { await r.json(); } catch { payMsg = "payout failed"; return; }   // an answer that is not JSON is not a payout
        payMsg = "payout requested";
        location.reload();
      } else payMsg = await reason(r, "payout failed");
    } catch { payMsg = "payout failed"; }
  }

  onMount(async () => {
    const res = await bootGet("/account");
    if (res.status === 401) { location.replace("/login.html"); return; }
    if (!res.data) { status = "fault"; return; }
    const a = res.data;
    // A signed-in person with no operator account (a consumer, or a sign-in whose machines
    // belong to another identity) gets an explanation, and the page makes none of the
    // operator-only requests that would all 403. `operator` absent = an older broker: show all.
    if (a.operator === false) { status = "nooperator"; return; }
    const e = a.earnings || {};
    split = { held: e.held || 0, payable: e.payable || 0, paid: e.paid || 0, reserved: e.reserved || 0, next_release: e.next_release || 0 };
    status = "ready";
    loadConnect();
    loadEarnings();
    loadHistory();
    loadBreakdown();
    loadTowers();
  });
</script>
<div class="authwrap tint-panel">
<main class="card{status === 'fault' ? ' is-fault' : ''}" id="card" hidden={status === "loading"}>
  <p class="fine" id="pageError" role="alert" hidden={status !== "fault"}>Could not load this page. Try again shortly.</p>
  <h1>Payouts</h1>
  <p class="sub">Exactly what you earned, when it clears, and where it came from.</p>

  <section class="acct-state tint-panel" id="poNoOperator" hidden={status !== "nooperator"}>
    <h2>Payouts are for people who serve models</h2>
    <p>You earn when your machines serve other people's requests, and this page shows what you earned, when it clears and where it came from. Nothing is registered to the account you are signed in with.</p>
    <p><b>Already sharing?</b> Sign in with the account you used for <code>roger login</code> (GitHub or Apple). <b>Not yet?</b></p>
    <pre class="cmd">roger login
roger share</pre>
    <p class="fine"><a href="/login.html">Switch sign-in</a> &middot; <a href="/account.html">Account</a></p>
  </section>

  <div id="poBody" hidden={status !== "ready"}>
  <!-- ============ THE SPLIT: held -> payable -> paid ============ -->
  <section class="panel">
    <h2>Earnings</h2>

    <div class="po-meter" id="splitMeter" role="img" aria-label="Earnings split">
      <div class="po-meter__track">
        <div class="po-meter__seg po-meter__seg--held" id="segHeld" style:width="{pct(split.held)}%"></div>
        <div class="po-meter__seg po-meter__seg--payable" id="segPayable" style:width="{pct(split.payable)}%"></div>
        <div class="po-meter__seg po-meter__seg--paid" id="segPaid" style:width="{pct(split.paid)}%"></div>
      </div>
    </div>

    <dl class="po-legend">
      <div class="po-legend__row">
        <dt><span class="po-dot po-dot--held"></span>Held</dt>
        <dd id="held">{status === "ready" ? cr(split.held) : "-"}</dd>
        <span class="po-legend__note" id="heldNote">inside the hold window</span>
      </div>
      <div class="po-legend__row">
        <dt><span class="po-dot po-dot--payable"></span>Payable</dt>
        <dd id="payable">{status === "ready" ? cr(split.payable) : "-"}</dd>
        <span class="po-legend__note" id="payableNote">cleared, ready to withdraw</span>
      </div>
      <div class="po-legend__row">
        <dt><span class="po-dot po-dot--paid"></span>Paid</dt>
        <dd id="paid">{status === "ready" ? cr(split.paid) : "-"}</dd>
        <span class="po-legend__note">lifetime, transferred out</span>
      </div>
    </dl>

    <p class="fine" id="reservedNote" hidden={!(split.reserved > 0)}>{split.reserved > 0 ? "Plus " + cr(split.reserved) + " in reserve, released after the hold." : ""}</p>
    <p class="fine">Earnings round to whole cents and need a <b id="minNote">{minNote}</b>
      minimum payable before cashout.</p>
  </section>

  <!-- ============ THE HOLD TIMELINE ============ -->
  <section class="panel">
    <h2>Hold timeline</h2>
    <p class="fine">Each earning's principal is held for <b id="holdNote">{holdDays !== null ? holdDays + " days" : "30 days"}</b> then becomes payable; a 10% reserve slice stays back until day 90 to cover the dispute tail.</p>

    <ol class="po-timeline" id="timeline" hidden={!(total > 0)}>
      <li class="po-timeline__step is-done">
        <span class="po-timeline__mark" aria-hidden="true"></span>
        <span class="po-timeline__body">
          <b>Earned</b>
          <span class="po-timeline__sub">a request you served, split 90/10</span>
        </span>
      </li>
      <li class="po-timeline__step{tlHeld ? ' ' + tlHeld : ''}" id="tlHeld">
        <span class="po-timeline__mark" aria-hidden="true"></span>
        <span class="po-timeline__body">
          <b>Held</b>
          <span class="po-timeline__sub" id="tlHeldSub">{heldSub}</span>
        </span>
        <span class="po-timeline__when" id="tlHeldWhen">{heldWhen}</span>
      </li>
      <li class="po-timeline__step{tlPayable ? ' ' + tlPayable : ''}" id="tlPayable">
        <span class="po-timeline__mark" aria-hidden="true"></span>
        <span class="po-timeline__body">
          <b>Payable</b>
          <span class="po-timeline__sub" id="tlPayableSub">{payableSub}</span>
        </span>
        <span class="po-timeline__when" id="tlPayableWhen"></span>
      </li>
      <li class="po-timeline__step{tlPaid ? ' ' + tlPaid : ''}" id="tlPaid">
        <span class="po-timeline__mark" aria-hidden="true"></span>
        <span class="po-timeline__body">
          <b>Paid</b>
          <span class="po-timeline__sub">transferred to your bank via Stripe</span>
        </span>
      </li>
    </ol>
    <p class="fine" id="timelineEmpty" hidden={status !== "ready" || total > 0}>No earnings yet - the timeline fills in as you serve.</p>

    <!-- the release ladder: each upcoming clear date with the amount + lot count -->
    <div class="po-ladder" id="ladderWrap" hidden={!ladder.length}>
      <p class="po-ladder__title">Upcoming releases</p>
      <ul class="po-ladder__list" id="ladder">
        {#each ladder as r}
          <li class="po-ladder__step">
            <span class="po-ladder__date">{when(r.date)}<span class="po-ladder__meta">{rel(r.date)} - {r.lot_count || 0}{(r.lot_count || 0) === 1 ? " lot" : " lots"}</span></span><span class="po-ladder__amount">{cr(r.amount || 0)}</span>
          </li>
        {/each}
      </ul>
    </div>
  </section>

  <!-- ============ WHERE IT CAME FROM: per-model / per-node ============ -->
  <section class="panel">
    <h2>By model and node</h2>
    <p class="fine" id="breakdownWindow">{breakdownWindow}</p>
    <ul class="recent po-break" id="breakdown">
      {#each breakdown || [] as m}
        <li>
          <span class="r-model po-break__left"><b>{m.model || "model"}</b><span class="po-break__meta">{m.node_id ? m.node_id : "node"} - {count(m.requests)} req</span></span><span class="r-cost">{cr(m.earnings_usd || 0)}</span>
        </li>
      {/each}
    </ul>
    <p class="fine" id="breakdownEmpty" hidden={!breakdown || breakdown.length > 0}>No served traffic in this window yet.</p>
  </section>

  <!-- ============ TOWERS & RELAY: shown only to operators who run Towers ============ -->
  <section class="panel" id="towerPanel" hidden={!towerPanel}>
    <h2>Towers &amp; relay</h2>
    <p class="fine">Your Towers and the sealed work they carry. Every settled request splits
      90% to the serving node, <b>5% to you</b>, 5% to the platform. Relay earnings are
      held, released and <b>cashed out exactly like serving earnings</b> - the totals below
      are already part of your balance above, split out here so you can see which stream
      earned what. <a href="/manual.html#sTower">Run a Tower &rarr;</a></p>

    <dl class="po-legend">
      <div class="po-legend__row">
        <dt><span class="po-dot po-dot--paid"></span>From serving</dt>
        <dd id="servingEarn">{earnings ? cr(serving) : "-"}</dd>
        <span class="po-legend__note">your Stations ran the model</span>
      </div>
      <div class="po-legend__row">
        <dt><span class="po-dot po-dot--payable"></span>From relaying</dt>
        <dd id="relayEarn">{earnings ? cr(relay) : "-"}</dd>
        <span class="po-legend__note">your Towers carried the traffic</span>
      </div>
    </dl>

    <p class="fine po-towers__title" id="towersTitle" hidden={!towerPanel || !towers.length}>Your Towers</p>
    <ul class="recent po-towers" id="towers">
      {#each towers || [] as t}
        <li class="po-tower">
          <span class="r-model po-tower__left"><b>{t.tower_id || "tower"}</b><span class="po-tower__meta">{towerMeta(t)}</span></span><span class="po-tower__state po-tower__state--{t.state || 'unknown'}">{t.may_take_work ? (t.state || "unknown") + " · live" : t.state || "unknown"}</span>
        </li>
      {/each}
    </ul>
    <p class="fine" id="towersEmpty" hidden={!towerPanel || towers.length > 0}>No Towers on the network right now.
      <a href="/manual.html#sTower">Run a Tower</a> on a Linux machine to relay traffic to Stations.</p>
  </section>

  <!-- ============ GET PAID ============ -->
  <section class="panel">
    <h2>Get paid</h2>
    <p class="fine">You must finish Stripe Connect onboarding (KYC) before any withdrawal.
      Payouts are manual and batched <span id="scheduleNote">{schedule}</span>.</p>
    <p>Payout status: <b id="connect">{connectStatus}</b></p>
    <button id="onboard" class="ghost" hidden={active} onclick={onboard}>Set up payouts (Stripe)</button>
    <button id="request" class="primary" hidden={!active} onclick={request}>Request payout</button>
    <p class="fine" id="payMsg">{payMsg}</p>
  </section>

  <!-- ============ LINEAGE: the receipts behind the money ============ -->
  <section class="panel">
    <h2>Payout history</h2>
    <ul class="recent" id="payouts">
      {#each payouts as p, i}
        <li class="po-payout{open[i] ? ' is-open' : ''}">
          <button type="button" class="po-payout__head" aria-expanded={open[i] ? "true" : "false"} title={p.stripe_transfer_id ? "Stripe transfer " + p.stripe_transfer_id : undefined} onclick={() => toggle(p, i)}>
            <span class="r-model"><span class="po-payout__chev" aria-hidden="true">&#8250;</span>{when(p.created_at)} - {p.state}</span><span class="r-cost">{cr(p.amount)}</span>
          </button>
          <div class="po-payout__drawer" hidden={!open[i]}>
            {#if lots[i] === "loading"}
              <p class="fine po-payout__loading">loading receipts...</p>
            {:else if Array.isArray(lots[i]) && !lots[i].length}
              <p class="fine">No funding receipts found for this payout.</p>
            {:else if Array.isArray(lots[i])}
              <ul class="recent po-lots">
                {#each lots[i] as l}
                  <li>
                    <span class="r-model po-lots__left"><b>{l.model || "model"}</b><span class="po-lots__meta">{l.node || "node"} - {l.request_id || "?"} - {when(l.created_at)}</span></span><span class="r-cost">{cr(l.gross || 0)}</span>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
    <p class="fine" id="payoutsEmpty" hidden={!history || payouts.length > 0}>No payouts yet.</p>
  </section>

  <section class="panel">
    <h2>Ledger</h2>
    <p class="fine">The accounting entries behind every payout, clawback and adjustment - your receipts.</p>
    <ul class="recent po-ledger" id="ledger">
      {#each ledger as g}
        <li>
          <span class="r-model">{when(g.ts)} - {kindLabel(g.kind)}{#if g.ref}<span class="po-ledger__ref">{g.ref}</span>{/if}</span><span class="r-cost">{cr(g.amount)}</span>
        </li>
      {/each}
    </ul>
    <p class="fine" id="ledgerEmpty" hidden={!history || ledger.length > 0}>No entries yet.</p>
  </section>

  </div>

  <AccountNav current='payouts' />
  <button id="logout" class="ghost" onclick={logout}>Sign out</button>
</main>
</div>
