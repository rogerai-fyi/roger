<script>
  // The dashboard, /dashboard.html (legacy: web/src/js/dashboard.js): the consumer/provider
  // hero view over GET /metrics/series?days=30. Login gate as on every account page: /account
  // confirms the session; a feed that refuses a valid session is an error to show, never a
  // reason to sign out (the login <-> dashboard loop). Honest-empty everywhere; mono numerals,
  // hairlines, one red glint (savings / earned).
  import { onMount } from "svelte";
  import { account, api, logout } from "#lib/api.js";
  import RogerFmt from "#lib/fmt.js";
  import { n0, cr, kfmt, rfmt } from "#lib/format.js";
  import { frontierAt, linePlot, axisLabels, savingsBars, rollupModels } from "./charts.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";

  const DAYS = 30;
  let signedIn = $state(false);
  let acct = $state(null);          // the /account body, kept for the balance / payable tiles
  let status = $state("loading");   // loading | fault | ready
  let d = $state(null);
  let step = $state(0);             // the frontier toggle's step

  const daily = $derived((d && d.daily) || []);
  const sv = $derived((d && d.savings) || {});
  const isConsumer = $derived(!!(d && d.is_consumer));
  const isProvider = $derived(!!(d && d.is_provider));
  const tot = $derived(daily.reduce((t, p) => ({ requests: t.requests + n0(p.requests), tokens_in: t.tokens_in + n0(p.tokens_in),
    tokens_out: t.tokens_out + n0(p.tokens_out), spend: t.spend + n0(p.spend), earned: t.earned + n0(p.earned) }),
    { requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 }));
  const totalTokens = $derived(tot.tokens_in + tot.tokens_out);
  const hasData = $derived(daily.length > 0 && (totalTokens > 0 || tot.requests > 0));
  const showHero = $derived(hasData && isConsumer && n0(sv.savings_est) > 0);
  const showHeroEarn = $derived(hasData && !showHero && isProvider && tot.earned > 0);
  const showEarnCta = $derived(status === "ready" && !isProvider);
  // the frontier toggle: no-op unless two or more reference models carry a numeric estimate
  const ref = $derived((Array.isArray(sv.reference) ? sv.reference : []).filter((r) => r && r.model && typeof r.frontier_est === "number" && isFinite(r.frontier_est)));
  const toggles = $derived(ref.length >= 2);
  const baseIndex = $derived(Math.max(0, ref.findIndex((r) => r.model === (sv.baseline_model || "gpt-4o"))));
  const hero = $derived(toggles ? frontierAt(ref, n0(sv.spend_usd), baseIndex + step)
    : { model: sv.baseline_model || "gpt-4o", frontier: n0(sv.frontier_est), savings: n0(sv.savings_est) });
  const models = $derived(rollupModels(daily));
  const moneySeries = $derived([
    ...(isConsumer ? [{ key: "spend", color: "var(--ink-900)", fill: "var(--paper-3)" }] : []),
    ...(isProvider ? [{ key: "earned", color: "var(--live)" }] : []),
  ]);
  function stepFn() { step += 1; }

  onMount(async () => {
    let a = null;
    try { a = await account(); } catch { a = null; }
    if (!a) { location.replace("/login.html"); return; }
    signedIn = true; acct = a;
    try {
      const r = await api("/metrics/series?days=" + DAYS);
      if (!r.ok) { status = "fault"; return; }
      d = await r.json();
      status = "ready";
    } catch { status = "fault"; }
  });
</script>
{#snippet chart(plot)}
  <svg viewBox={plot.viewBox} class="ds-line" preserveAspectRatio="none" role="img">
    {#each plot.grid as g}<line x1={g.x1} x2={g.x2} y1={g.y} y2={g.y} class={g.cls} />{/each}
    {#each plot.lines as l}
      {#if l.area}<path d={l.area} class="ds-area" fill={l.fill} />{/if}
      <path d={l.d} class="ds-stroke" stroke={l.color} />
      <circle cx={l.cx} cy={l.cy} r="2.5" class="ds-dot" fill={l.color} />
    {/each}
  </svg>
{/snippet}
{#snippet xaxis(labels)}
  <div class="ds-xaxis">{#each labels as t}<span>{t}</span>{/each}</div>
{/snippet}
<div class="authwrap tint-panel">
<main class="card" id="card" hidden={!signedIn}>
  <h1>Dashboard</h1>
  <p class="sub">Signed in as <b id="who">{acct ? RogerFmt.who(acct) : "-"}</b> &middot; last 30 days</p>

  <!-- loading / empty / error states for the whole feed -->
  <p class="ds-state" id="dashLoading" hidden={status !== "loading"}>Loading your numbers...</p>
  <p class="ds-state" id="dashError" hidden={status !== "fault"}>Could not load your dashboard. Try again shortly.</p>
  <section class="acct-state tint-panel" id="dashEmpty" hidden={!(status === "ready" && !hasData)}>
    <h2>No traffic yet</h2>
    <p>Nothing has moved through your account in the last 30 days. Tune in to a model and your tokens, spend and savings will chart here.</p>
    <p>Balance <b id="emptyBalance">{acct ? cr(acct.balance) : "-"}</b> &middot; <a href="/billing.html">Add credit</a> &middot; <a href="/keys.html">Make an API key</a> &middot; <a href="/stations.html">Your stations</a></p>
    <pre class="cmd">roger use &lt;model&gt;</pre>
    <p class="fine">Or put a GPU on air with <code>roger share</code> to start earning.</p>
  </section>

  <!-- HERO: savings vs frontier (consumer); the baseline name cycles the reference models -->
  <section class="ds-hero" id="hero" hidden={!showHero}>
    <p class="ds-hero__eyebrow">You have saved</p>
    <p class="ds-hero__amt" id="heroAmt">{showHero ? cr(hero.savings) : "-"}</p>
    <p class="ds-hero__sub">vs {#if toggles}<b id="heroBase" class="ds-hero__toggle" role="button" tabindex="0" aria-label="Compare another frontier model" title="click to compare other frontier models"
        onclick={stepFn} onkeydown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); stepFn(); } }}>{hero.model}</b>{:else}<b id="heroBase">{hero.model}</b>{/if} list price &middot; you paid <b id="heroSpend">{showHero ? cr(n0(sv.spend_usd)) : "-"}</b> where a frontier lab would have billed <b id="heroFrontier">{showHero ? cr(hero.frontier) : "-"}</b></p>
  </section>

  <!-- alt hero for a pure provider: lifetime earned over the window -->
  <section class="ds-hero" id="heroEarn" hidden={!showHeroEarn}>
    <p class="ds-hero__eyebrow">You have earned</p>
    <p class="ds-hero__amt"><span id="heroAmtEarn">{showHeroEarn ? cr(tot.earned) : "-"}</span></p>
    <p class="ds-hero__sub">owner share across your nodes, last 30 days</p>
  </section>

  <!-- STAT TILES (period totals); balance and payable come from /account, no extra request -->
  <dl class="stats ds-tiles" id="stats" hidden={!hasData}>
    <div><dt>Requests</dt><dd id="statReq" use:rfmt={tot.requests}>-</dd></div>
    <div><dt>Tokens</dt><dd id="statTok" use:rfmt={totalTokens}>-</dd></div>
    <div id="tileSpend" hidden={!isConsumer}><dt>Spend</dt><dd id="statSpend" use:rfmt={{ value: tot.spend, usd: true }}>-</dd></div>
    <div id="tileEarn" hidden={!isProvider}><dt>Earned</dt><dd id="statEarn" use:rfmt={{ value: tot.earned, usd: true }}>-</dd></div>
    <div id="tileBalance" hidden={!(acct && typeof acct.balance === "number")}><dt><a href="/billing.html">Balance</a></dt><dd id="statBalance" use:rfmt={{ value: acct ? acct.balance : undefined, usd: true }}>-</dd></div>
    <div id="tilePayable" hidden={!(acct && acct.operator && acct.earnings && typeof acct.earnings.payable === "number")}><dt><a href="/payouts.html">Payable now</a></dt><dd id="statPayable" use:rfmt={{ value: acct && acct.earnings ? acct.earnings.payable : undefined, usd: true }}>-</dd></div>
  </dl>

  <!-- SAVINGS PER DAY -->
  <section class="panel" id="savingsWrap" hidden={!showHero}>
    <h2>Savings per day</h2>
    <div class="ds-chart">
      <div class="ds-chart__head">
        <span class="ds-chart__title">Estimated $ saved vs frontier</span>
        <span class="ds-legend" aria-hidden="true"><span class="ds-legend__sw ds-legend__sw--live"></span>saved</span>
      </div>
      <div id="savingsChart">{#if showHero}<div class="ds-cols">{#each savingsBars(daily) as b}<span class="ds-cols__col" title={b.title}><span class="ds-cols__bar" style="height: {b.height}%"></span></span>{/each}</div>{@render xaxis(axisLabels(daily))}{/if}</div>
    </div>
    <p class="fine" id="savingsNote">{(showHero && sv.reference_note) || "Estimate only: published list prices, not a live or contractual quote."}</p>
  </section>

  <!-- TOKENS OVER TIME -->
  <section class="panel" id="tokWrap" hidden={!hasData}>
    <h2>Tokens over time</h2>
    <div class="ds-chart">
      <div class="ds-chart__head"><span class="ds-chart__title">Tokens out per day</span></div>
      <div id="tokChart">{#if hasData}{@render chart(linePlot(daily, [{ key: "tokens_out", color: "var(--ink-900)", fill: "var(--paper-3)" }]))}{@render xaxis(axisLabels(daily))}{/if}</div>
    </div>
  </section>

  <!-- SPEND / EARNED OVER TIME -->
  <section class="panel" id="moneyWrap" hidden={!(hasData && moneySeries.length)}>
    <h2>Money over time</h2>
    <div class="ds-chart">
      <div class="ds-chart__head">
        <span class="ds-chart__title">Per day</span>
        <span class="ds-legend" id="moneyLegend" aria-hidden="true">{#if isConsumer}<span class="ds-legend__sw ds-legend__sw--ink"></span>spend{/if}{#if isProvider}<span class="ds-legend__sw ds-legend__sw--live"></span>earned{/if}</span>
      </div>
      <div id="moneyChart">{#if hasData && moneySeries.length}{@render chart(linePlot(daily, moneySeries))}{@render xaxis(axisLabels(daily))}{/if}</div>
    </div>
  </section>

  <!-- REQUESTS OVER TIME -->
  <section class="panel" id="reqWrap" hidden={!hasData}>
    <h2>Requests over time</h2>
    <div class="ds-chart">
      <div class="ds-chart__head"><span class="ds-chart__title">Requests per day</span></div>
      <div id="reqChart">{#if hasData}{@render chart(linePlot(daily, [{ key: "requests", color: "var(--ink-500)", fill: "var(--paper-2)" }]))}{@render xaxis(axisLabels(daily))}{/if}</div>
    </div>
  </section>

  <!-- PER-MODEL BREAKDOWN -->
  <section class="panel" id="modelWrap" hidden={!(hasData && models.length)}>
    <h2>By model</h2>
    <div class="ds-chart">
      <div class="ds-chart__head"><span class="ds-chart__title">Tokens by model</span></div>
      <div id="modelBars">{#if hasData && models.length}{@const max = models[0].tokens}<ul class="ds-mbars">{#each models as r}<li class="ds-mbars__row"><span class="ds-mbars__label" title={r.model}>{r.model}</span><span class="ds-mbars__track"><span class="ds-mbars__fill" style="width: {max > 0 ? (r.tokens / max) * 100 : 0}%"></span></span><span class="ds-mbars__val">{kfmt(r.tokens)} tok / {cr(isProvider ? r.earned : r.spend)}</span></li>{/each}</ul>{/if}</div>
    </div>
    <p class="fine"><a href="/usage.html">Full per-model metrics</a></p>
  </section>

  <!-- EARN CTA (only when not yet a provider) -->
  <section class="panel" id="earnCta" hidden={!showEarnCta}>
    <h2>Earn</h2>
    <p>Put a GPU on air and earn per token. Install, then link this account on the box:</p>
    <pre class="cmd">curl -fsSL https://rogerai.fm/install.sh | sh
roger login
roger share</pre>
    <p class="fine">Once a node serves under your account, its activity shows on <a href="/console.html">the console</a>.</p>
  </section>

  <AccountNav current="dashboard" />
  <button id="logout" class="ghost" onclick={logout}>Sign out</button>
</main>
</div>
