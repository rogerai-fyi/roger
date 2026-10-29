<script>
  // Billing, /billing.html (legacy: web/src/js/billing.js + billing-help.js): the value-first
  // wallet view and the Stripe Checkout top-up. Thin, credentialed reads over the broker
  // behind the session cookie (no token ever touches JS); logged-out visitors go to /login.
  // Charts are hand-rolled SVG/CSS on the design system (mono numerals, hairlines, the one
  // red accent), no chart library.
  //
  // Feeds (all credentialed):
  //   GET /billing            -> { balance, derived, credit_usd, checkout_ready, topups[] }
  //   GET /metrics/series     -> { daily[], savings{ spend_usd, frontier_est, savings_est,
  //                               baseline_model, reference[] } }        (savings + velocity)
  //   GET /console            -> { role, events[]{model,node,cost,ts}, counters{spend_today} }
  //   GET/PATCH /account/limit (SpendLimit), POST /billing/checkout (TopUp)
  // Every secondary panel is best-effort and honest-empty (hidden, or "-") when its feed is
  // missing, so the balance and the top-up flow never block on them.
  import { onMount } from "svelte";
  import { getJSON, logout } from "#lib/api.js";
  import { bootGet } from "#lib/account/boot.js";
  import { n0, cr, when } from "#lib/format.js";
  import { seriesOK } from "#lib/rollup.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";
  import SpendLimit from "./SpendLimit.svelte";
  import TopUp from "./TopUp.svelte";
  import LedgerHelp from "./LedgerHelp.svelte";
  import { spark } from "./spark.js";

  let status = $state("loading");   // loading | fault | ready
  let d = $state(null);             // /billing
  let series = $state(null);        // /metrics/series, once it answered
  let con = $state(null);           // /console, once it answered ("failed" when refused)
  let limit;

  const ready = $derived(status === "ready");
  const topups = $derived((d && d.topups) || []);

  // ---- SAVINGS-VS-FRONTIER (value front and center): surfaced only with a real frontier
  // estimate to compare against (honest empty: a brand-new wallet shows nothing) ----
  const barWidth = (value, max) => { const pct = max > 0 ? (value / max) * 100 : 0; return (pct < 1 ? 1 : pct) + "%"; };
  const savings = $derived.by(() => {
    const sav = series && series.savings;
    if (!sav) return null;
    const spend = n0(sav.spend_usd), frontier = n0(sav.frontier_est), saved = n0(sav.savings_est);
    if (frontier <= 0) return null;   // nothing consumed yet -> no honest claim to make
    const max = Math.max(frontier, spend, 1e-9);
    const pct = Math.round((saved / frontier) * 100);
    const note = sav.reference_note || "Estimate only: published list prices, not a live or contractual quote.";
    const baseline = sav.baseline_model || "frontier";
    return { saved, spend, frontier,
      // two stacked bars: what you paid (red receipt) vs the frontier estimate (muted),
      // scaled to the larger of the two so the gap is the message
      bars: [{ label: "RogerAI", value: spend, kind: "paid", width: barWidth(spend, max) }, { label: baseline, value: frontier, kind: "frontier", width: barWidth(frontier, max) }],
      note: pct > 0 ? "About " + pct + "% less than the " + baseline + " list price. " + note : note };
  });
  const vel = $derived(series ? spark(series.daily) : null);

  // ---- SPENT TODAY + RECENT CHARGES from the console feed: a failed feed reads "-",
  // unavailable, never a real-looking $0.00; only rows with a real cost are receipts ----
  const conOk = $derived(!!con && con !== "failed");
  const spendToday = $derived(!conOk ? "-" : con.counters && typeof con.counters.spend_today === "number" ? cr(con.counters.spend_today) : cr(0));
  const charges = $derived(conOk ? (con.events || []).filter((e) => n0(e.cost) > 0).slice(0, 8) : []);
  const chargeLabel = (e) => when(e.ts) + " - " + (e.model || "request") + (e.node ? " - " + e.node : "");

  onMount(async () => {
    const res = await bootGet("/billing");
    if (res.status === 401) { location.replace("/login.html"); return; }
    if (!res.data) { status = "fault"; return; }
    d = res.data;
    status = "ready";
    getJSON("/metrics/series?days=30").then((s) => { if (seriesOK(s)) series = s; });   // a shape the charts cannot read is no feed
    limit.load();
    getJSON("/console?limit=40").then((c) => { con = c || "failed"; });
  });
</script>
<div class="authwrap tint-panel">
<main class="card{status === 'fault' ? ' is-fault' : ''}" id="card" hidden={status === "loading"}>
  <p class="fine" id="pageError" role="alert" hidden={status !== "fault"}>Could not load this page. Try again shortly.</p>
  <h1>Billing</h1>
  <p class="sub">Add money, watch it go further. Your wallet, your spend, and what RogerAI saves you against the name-brand labs.</p>

  <!-- VALUE FIRST: the savings-vs-frontier headline, the one place the page leads with the
       why. Honest empty: hidden until /metrics/series returns real spend. -->
  <section class="panel bx-save" id="saveBox" hidden={!savings}>
    <h2>Saved vs frontier labs</h2>
    <p class="bx-save__hero">
      <span class="bx-save__amt" id="saveAmt">{savings ? cr(savings.saved) : "-"}</span>
      <span class="bx-save__cap">saved so far</span>
    </p>
    <div class="bx-save__bars" id="saveBars" aria-hidden="true">
      {#each savings ? savings.bars : [] as b}
        <div class="bx-save__row"><span class="bx-save__lab">{b.label}</span><span class="bx-save__track"><span class="bx-save__fill bx-save__fill--{b.kind}" style:width={b.width}></span></span><span class="bx-save__val">{cr(b.value)}</span></div>
      {/each}
    </div>
    <dl class="bx-save__legend">
      <div><dt>You paid</dt><dd id="saveSpend">{savings ? cr(savings.spend) : "-"}</dd></div>
      <div><dt>Frontier estimate</dt><dd id="saveFrontier">{savings ? cr(savings.frontier) : "-"}</dd></div>
    </dl>
    <p class="fine" id="saveNote">{savings ? savings.note : "-"}</p>
  </section>

  <!-- WALLET: current balance + today's spend, the two numbers a person checks before
       topping up. -->
  <section class="panel">
    <h2>Wallet<button type="button" class="bx-help-btn" id="walletHelp" data-help-open
        aria-haspopup="dialog" aria-controls="ledgerModal" aria-expanded="false"
        aria-label="How RogerAI tracks your money, end to end">?</button></h2>
    <dl class="stats">
      <div><dt>Balance</dt><dd id="balance">{ready ? cr(d.balance) : "-"}</dd></div>
      <div><dt>Spent today</dt><dd id="spendToday">{spendToday}</dd></div>
      <div><dt>Verified</dt><dd id="derived">{ready ? cr(d.derived) : "-"}</dd></div>
    </dl>
    <p class="fine">"Verified" re-derives your balance from the append-only ledger - it should always match.
      <button type="button" class="bx-help-link" data-help-open
        aria-haspopup="dialog" aria-controls="ledgerModal" aria-expanded="false">How the money works</button></p>
  </section>

  <!-- MONTHLY SPEND LIMIT: the account-wide guardrail, one ceiling on ALL of the account's
       paid usage this calendar month (per-key token caps are the first guardrail). -->
  <SpendLimit bind:this={limit} />

  <!-- SPEND VELOCITY: a hand-rolled SVG sparkline of recent daily spend. Honest empty. -->
  <section class="panel bx-vel" id="velBox" hidden={!vel}>
    <h2>Spend velocity</h2>
    <div class="bx-vel__chart" id="velChart">
      {#if vel}
        <svg viewBox={vel.viewBox} preserveAspectRatio="none" class="bx-spark" role="img" aria-label="Recent daily spend">
          <path d={vel.area} class="bx-spark__area"></path>
          <path d={vel.line} class="bx-spark__line"></path>
          <circle cx={vel.dot.cx} cy={vel.dot.cy} r="1.8" class="bx-spark__dot"></circle>
        </svg>
      {/if}
    </div>
    <div class="bx-vel__foot">
      <span class="bx-vel__span" id="velSpan">{vel ? vel.n + (vel.n === 1 ? " day" : " days") + " of activity" : "-"}</span>
      <span class="bx-vel__peak" id="velPeak">{vel ? "peak " + cr(vel.peak) + "/day" : ""}</span>
    </div>
  </section>

  <!-- TOP UP: the Stripe flow, presets, custom amount, secure-payment reassurance. -->
  <section class="panel">
    <h2>Add money</h2>
    <p>Top up your wallet with a card. Stripe handles the payment end to end; your card details never reach our servers.</p>

    <TopUp ready={ready && !!d.checkout_ready} />

    <p class="fine" id="topupDisabled" hidden={!(ready && !d.checkout_ready)}>Top-ups are not enabled on this broker yet.</p>
    <p class="fine" id="topupNote">{ready ? (d.checkout_ready ? "Top up below, or from the CLI: roger topup." : "Top up from the CLI: roger topup.") : "-"}</p>
  </section>

  <!-- RECEIPTS: recent top-ups (the red receipt accent) plus recent charges from the
       lineage console, so money in and money out sit side by side. -->
  <section class="panel">
    <h2>Recent top-ups</h2>
    <ul class="recent" id="topups">
      {#each topups as t}<li><span class="r-model">{when(t.ts)} - top-up</span><span class="r-cost">{cr(t.amount)}</span></li>{/each}
    </ul>
    <p class="fine" id="topupsEmpty" hidden={!ready || topups.length > 0}>No top-ups yet.</p>
  </section>

  <section class="panel">
    <h2>Recent charges</h2>
    <ul class="recent" id="charges">
      {#each charges as e}<li><span class="r-model">{chargeLabel(e)}</span><span class="r-cost">{cr(n0(e.cost))}</span></li>{/each}
    </ul>
    <p class="fine" id="chargesEmpty" hidden={!conOk || charges.length > 0}>No charges yet. Free models cost nothing.</p>
  </section>

  <AccountNav current='billing' />
  <button id="logout" class="ghost" onclick={logout}>Sign out</button>
</main>
</div>

<!-- MONEY-FLOW HELP MODAL: sits OUTSIDE main.card (a top-level fixed overlay) so it is not
     gated by the card's hidden state. -->
<LedgerHelp />
