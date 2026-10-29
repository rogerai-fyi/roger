<script>
  // Metrics, /usage.html (legacy: web/src/js/metrics.js): time-aware, interactive metrics. One
  // credentialed read of the broker's time-series feed drives BOTH a PROVIDER view (what you
  // host and earn over time) and a YOUR USAGE view (what you consume and spend over time). No
  // token ever touches JS: the session cookie carries auth.
  //
  //   GET /metrics/series?days=N -> { period_days, is_consumer, is_provider,
  //       daily: [{ bucket "YYYY-MM-DD", requests, tokens_in, tokens_out, spend, earned,
  //                 frontier_est, savings_est, models: [{ model, requests, tokens_in, tokens_out, spend, earned }] }],
  //       savings: {...} }   days clamps to [1, 366]; the page asks for 7 | 30 | 90, buckets oldest first
  //   GET /usage -> { recent: [...] }   the consumer's private routing history
  //
  // is_consumer / is_provider gate which sections render: a pure consumer sees only "Your
  // usage", a pure provider only "Provider metrics", a both-sider both. Charts are hand-rolled
  // inline SVG (no chart library): monochrome + the one red accent, tabular-nums readouts,
  // reduced-motion safe, reflowing for narrow screens, each with a hover/focus crosshair and an
  // exact-bucket readout (arrow keys move the cursor). Honest empty: real buckets only.
  import { onMount } from "svelte";
  import { api, getJSON, logout } from "#lib/api.js";
  import { n0, cr, num, kfmt, rfmt } from "#lib/format.js";
  import AccountNav from "#lib/ui/AccountNav.svelte";
  import { rollupModels, rollupTotals, seriesOK } from "#lib/rollup.js";
  import Figure from "./Figure.svelte";

  let auth = $state("loading");   // loading | gate | in
  let days = $state("30");
  let view = $state("loading");   // loading | error | ready
  let d = $state(null);           // the series response for the range
  let recent = $state([]);        // the consumer's recent requests
  let seq = 0;

  const daily = $derived((d && d.daily) || []);
  // Which sections does this identity get? Honor is_provider / is_consumer; if the broker ever
  // omits both, fall back to whichever side has any activity so the page is never blank. Kept
  // from the last answer while a range reloads, so the sections stay up with their loading lines.
  let roles = $state({ isProvider: false, isConsumer: false });
  function applyRoles(data) {
    const rows = (data && data.daily) || [];
    let hasEarn = false, hasSpend = false;
    for (const b of rows) { if (n0(b.earned) !== 0) hasEarn = true; if (n0(b.requests) !== 0) hasSpend = true; }
    const isProvider = data && typeof data.is_provider === "boolean" ? data.is_provider : hasEarn;
    const isConsumer = data && typeof data.is_consumer === "boolean" ? data.is_consumer : hasSpend || !isProvider;
    roles = { isProvider, isConsumer };
  }
  const totals = $derived(rollupTotals(daily));
  const models = $derived(rollupModels(daily, "(unknown)", false));   // first seen first: a tie keeps the page's order
  const section = $derived(view === "loading" ? "loading" : view !== "ready" ? "none" : daily.length && totals.requests !== 0 ? "data" : "empty");
  const provModels = $derived([...models].sort((a, b) => b.tokens_out - a.tokens_out));
  const provMax = $derived(provModels.reduce((m, r) => Math.max(m, r.tokens_out), 0));
  const useModels = $derived([...models].sort((a, b) => b.tokens - a.tokens));
  const useMax = $derived(useModels.reduce((m, r) => Math.max(m, r.tokens), 0));
  const width = (v, max) => { const pct = max > 0 ? (v / max) * 100 : 0; return (pct < 0 ? 0 : pct) + "%"; };
  const SERIES = {
    earned: [{ key: "earned", cls: "money", label: "" }],
    spend: [{ key: "spend", cls: "money", label: "" }],
    tokens: [{ key: "tokens_in", cls: "in", label: "in" }, { key: "tokens_out", cls: "out", label: "out" }],
    requests: [{ key: "requests", cls: "req", label: "" }],
  };

  // A curated row names the provider it was routed through and the SPLIT: the upstream
  // pass-through (the server derives the list; owner_share stopped equaling it when operators
  // gained half the fee) vs the routing fee, so the fee is never a hidden margin. A human row
  // shows the band and the cost; per-station operator economics are not the consumer's surface.
  function routing(e) {
    if (e.curated_provider) {
      const list = +e.curated_list || 0;
      const fee = Math.max(0, (+e.cost || 0) - list);
      return "» " + e.curated_provider + " curated · " + cr(list) + " upstream + " + cr(fee) + " routing";
    }
    return "station " + String(e.node || "").slice(0, 10);
  }

  // 401 = the session is really gone (the gate). A 403 for a person /account just confirmed is
  // a fault to show, never "you are signed out".
  async function fetchSeries(range) {
    try {
      const r = await api("/metrics/series?days=" + encodeURIComponent(range));
      if (r.status === 401) return { ok: false, auth: false };
      if (!r.ok) return { ok: false };
      try { const data = await r.json(); return seriesOK(data) ? { ok: true, data } : { ok: false }; } catch { return { ok: false }; }
    } catch { return { ok: false }; }
  }
  async function load(range) {
    const my = ++seq;
    view = "loading"; d = null;
    const res = await fetchSeries(range);
    if (my !== seq) return;   // a newer range answered first
    if (!res.ok) { if (res.auth === false) auth = "gate"; else view = "error"; return; }
    d = res.data || {};
    applyRoles(d);
    view = "ready";
    if (roles.isConsumer) loadRecent(my);
  }
  async function loadRecent(my) {
    const r = await getJSON("/usage");   // the aggregate view stands alone when this fails
    if (my !== seq) return;
    const rows = (r && r.recent) || [];
    if (rows.length) recent = rows.slice(0, 50);   // an empty or refused refetch keeps what is on screen
  }
  function pick(v) { days = v; load(v); }

  onMount(async () => {
    // confirm the session, then load; logged out, or the broker down, is the gate, never a blank page
    let acct = null;
    try { const r = await api("/account"); acct = r.ok ? await r.json() : null; } catch { acct = null; }
    if (!acct || !(acct.github_login || acct.github_id)) { auth = "gate"; return; }
    auth = "in";
    load(days);
  });
</script>
<div class="authwrap tint-panel">
<main class="card" id="card" hidden={auth !== "in"}>
  <h1>Metrics</h1>
  <p class="sub">An explorable, time-aware view of what you host and earn, and what you consume and spend - over time and per model.</p>

  <!-- Range selector (shared by both views): re-fetches /metrics/series and redraws. -->
  <div class="mx-range" role="group" aria-label="Time range">
    <span class="mx-range__k">Range</span>
    {#each ["7", "30", "90"] as v}<button type="button" class="mx-range__opt{days === v ? ' is-active' : ''}" data-days={v} onclick={() => pick(v)}>{v}d</button>{/each}
  </div>

  <!-- ============ PROVIDER METRICS (what you host / earn) ============ -->
  <section class="panel" id="provider" hidden={view === "error" || !roles.isProvider}>
    <h2>Provider metrics</h2>
    <p class="fine">Tokens you served and earnings accrued across the models on your nodes.</p>

    <dl class="stats mx-totals" id="provTotals" hidden={section !== "data"}>
      <div><dt>Requests</dt><dd id="provReq" use:rfmt={totals.requests}>-</dd></div>
      <div><dt>Tokens out</dt><dd id="provTokOut" use:rfmt={totals.tokens_out}>-</dd></div>
      <div><dt>Earned</dt><dd id="provEarn">{section === "data" ? cr(totals.earned) : "-"}</dd></div>
    </dl>

    <Figure id="provEarn" title="Earned over time" daily={section === "data" ? daily : []} series={SERIES.earned} fmt={cr} aria="Earned per day" />
    <Figure id="provTok" title="Tokens served over time" legend daily={section === "data" ? daily : []} series={SERIES.tokens} fmt={num} stack aria="Tokens served per day, in and out stacked" />
    <Figure id="provReq" title="Requests over time" daily={section === "data" ? daily : []} series={SERIES.requests} fmt={num} aria="Requests per day" />

    <!-- per-model breakdown: bars of tokens-out share by model -->
    <div class="mx-chart" id="provChart" hidden={section !== "data" || !provModels.length}>
      <div class="mx-chart__head">
        <span class="mx-chart__title">Tokens out by model</span>
      </div>
      <ul class="mx-bars" id="provBars">{#each section === "data" ? provModels : [] as r}<li class="mx-bars__row"><span class="mx-bars__label" title={r.model}>{r.model}</span><span class="mx-bars__track"><span class="mx-bars__fill" style:width={width(r.tokens_out, provMax)}></span></span><span class="mx-bars__val">{kfmt(r.tokens_out)}</span></li>{/each}</ul>
    </div>

    <div class="mx-tablewrap" id="provTableWrap" hidden={section !== "data" || !provModels.length}>
      <table class="mx-table">
        <thead>
          <tr>
            <th scope="col">Model</th>
            <th scope="col" class="num">Requests</th>
            <th scope="col" class="num">Tokens out</th>
            <th scope="col" class="num">Earned</th>
          </tr>
        </thead>
        <tbody id="provRows">{#each section === "data" ? provModels : [] as r}<tr><td class="mx-model">{r.model}</td><td class="num">{kfmt(r.requests)}</td><td class="num">{kfmt(r.tokens_out)}</td><td class="num mx-money">{cr(r.earned)}</td></tr>{/each}</tbody>
      </table>
    </div>

    <p class="mx-state" id="provLoading" hidden={section !== "loading"}>Loading provider metrics...</p>
    <p class="mx-state" id="provEmpty" hidden={section !== "empty"}>No activity in this range. Put a GPU on air with <code>roger share</code> to start earning.</p>
    <p class="mx-state" id="provError" hidden>Could not load provider metrics. Try again shortly.</p>
  </section>

  <!-- ============ YOUR USAGE (what you consume / spend) ============ -->
  <section class="panel" id="usage" hidden={view === "error" || !roles.isConsumer}>
    <h2>Your usage</h2>
    <p class="fine">Tokens you consumed and what they cost across the models you tuned in.</p>

    <dl class="stats mx-totals" id="useTotals" hidden={section !== "data"}>
      <div><dt>Requests</dt><dd id="useReq" use:rfmt={totals.requests}>-</dd></div>
      <div><dt>Tokens</dt><dd id="useTok" use:rfmt={totals.tokens_in + totals.tokens_out}>-</dd></div>
      <div><dt>Spend</dt><dd id="useSpend">{section === "data" ? cr(totals.spend) : "-"}</dd></div>
    </dl>

    <Figure id="useSpend" title="Spend over time" daily={section === "data" ? daily : []} series={SERIES.spend} fmt={cr} aria="Spend per day" />
    <Figure id="useTok" title="Tokens consumed over time" legend daily={section === "data" ? daily : []} series={SERIES.tokens} fmt={num} stack aria="Tokens consumed per day, in and out stacked" />
    <Figure id="useReq" title="Requests over time" daily={section === "data" ? daily : []} series={SERIES.requests} fmt={num} aria="Requests per day" />

    <!-- per-model breakdown: bars of total tokens share by model -->
    <div class="mx-chart" id="useChart" hidden={section !== "data" || !useModels.length}>
      <div class="mx-chart__head">
        <span class="mx-chart__title">Tokens by model</span>
      </div>
      <ul class="mx-bars" id="useBars">{#each section === "data" ? useModels : [] as r}<li class="mx-bars__row"><span class="mx-bars__label" title={r.model}>{r.model}</span><span class="mx-bars__track"><span class="mx-bars__fill" style:width={width(r.tokens, useMax)}></span></span><span class="mx-bars__val">{kfmt(r.tokens)}</span></li>{/each}</ul>
    </div>

    <div class="mx-tablewrap" id="useTableWrap" hidden={section !== "data" || !useModels.length}>
      <table class="mx-table">
        <thead>
          <tr>
            <th scope="col">Model</th>
            <th scope="col" class="num">Requests</th>
            <th scope="col" class="num">Tokens</th>
            <th scope="col" class="num">Spend</th>
          </tr>
        </thead>
        <tbody id="useRows">{#each section === "data" ? useModels : [] as r}<tr><td class="mx-model">{r.model}</td><td class="num">{kfmt(r.requests)}</td><td class="num">{kfmt(r.tokens)}</td><td class="num mx-money">{cr(r.spend)}</td></tr>{/each}</tbody>
      </table>
    </div>

    <p class="mx-state" id="useLoading" hidden={section !== "loading"}>Loading your usage...</p>
    <p class="mx-state" id="useEmpty" hidden={section !== "empty"}>No usage in this range. Tune in with <code>roger use &lt;model&gt;</code>.</p>
    <p class="mx-state" id="useError" hidden>Could not load your usage. Try again shortly.</p>

    <!-- Recent requests: the private routing history. Curated rows name the provider and the
         split (upstream pass-through vs routing fee); only your own account can read this,
         the read is credentialed. -->
    <div class="mx-tablewrap" id="useRecentWrap" hidden={!recent.length}>
      <h3>Recent requests</h3>
      <table class="mx-table">
        <thead><tr><th>Band</th><th>Routing</th><th class="num">Tokens</th><th class="num">Cost</th></tr></thead>
        <tbody id="useRecentRows">{#each recent as e}<tr><td class="mx-model">{e.model || "-"}</td><td class="mx-model">{routing(e)}</td><td class="num">{kfmt((+e.prompt_tokens || 0) + (+e.completion_tokens || 0))}</td><td class="num mx-money">{cr(+e.cost || 0)}</td></tr>{/each}</tbody>
      </table>
    </div>
  </section>

  <p class="mx-state" id="allLoading" hidden={view !== "loading"}>Loading metrics...</p>
  <p class="mx-state" id="allError" hidden={view !== "error"}>Could not load metrics. Try again shortly.</p>

  <AccountNav current='metrics' />
  <button id="logout" class="ghost" onclick={logout}>Sign out</button>
</main>

<!-- Logged-out gate: shown when the broker says we are not signed in. -->
<main class="card" id="gate" hidden={auth !== "gate"}>
  <h1>Metrics</h1>
  <p class="sub">Log in to see your provider earnings and usage.</p>
  <section class="acct-state tint-panel">
    <h2>Signed out</h2>
    <p>Sign in to view per-model tokens over time, earnings and spend.</p>
    <p class="research-actions research-actions--lead"><a class="research-button" href="/login.html">Log in</a></p>
  </section>
</main>
</div>
