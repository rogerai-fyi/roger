<script>
  // The in-app top-up on /billing (legacy: wireTopup in web/src/js/billing.js): a chosen
  // preset OR a custom amount -> Stripe Checkout. Hidden until /billing says checkout_ready
  // (the page shows this or the "not enabled" note). The amount rules live in topup.js, the
  // one place that states the bounds.
  import { api } from "#lib/api.js";
  import { reason } from "#lib/account/boot.js";
  import { cr } from "#lib/format.js";
  import { MIN_USD, MAX_USD, check, label } from "./topup.js";

  let { ready = false } = $props();
  let preset = $state(20);         // the active preset's dollars; null once a custom amount is typed
  let custom = $state("");         // the custom field's text, as typed
  let msg = $state("");
  let busy = $state(false);

  const usd = $derived(custom ? (isFinite(parseFloat(custom)) ? parseFloat(custom) : NaN) : preset === null ? NaN : preset);
  // the button says what it will send; before the feed confirmed checkout it says nothing
  const view = $derived(ready ? label(usd, cr) : { button: "Add money", note: null });

  // selecting a preset clears the custom field; typing a custom clears presets
  function pick(v) { preset = v; custom = ""; }
  function typed(e) { custom = e.currentTarget.value; if (custom) preset = null; }

  async function go() {
    const c = check(usd);
    if (!c.ok) { msg = " " + c.msg; return; }
    busy = true;
    msg = " redirecting to Stripe...";
    try {
      const r = await api("/billing/checkout", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ usd }) });
      if (r.ok) {
        const j = await r.json().catch(() => null);   // a 2xx without a link: checkout did not start
        if (j && j.url) { location.href = j.url; return; }
        msg = " could not start checkout";
      } else {
        msg = " " + (await reason(r, "request failed"));   // the refusal's own words
      }
    } catch { msg = " could not reach the broker"; }
    busy = false;
  }
</script>
<div id="topupBox" hidden={!ready}>
  <div class="amounts" id="topupPresets" role="group" aria-label="Top-up amount">
    {#each [5, 20, 50, 100] as v}
      <button type="button" class="amount{preset === v ? ' is-active' : ''}" data-usd={v} onclick={() => pick(v)}>${v}</button>
    {/each}
  </div>
  <label class="fine" for="topupCustom">Or a custom amount (USD)</label>
  <div class="amount-row">
    <span class="amount-row__pre" aria-hidden="true">$</span>
    <input id="topupCustom" type="number" min={MIN_USD} max={MAX_USD} step="0.01"
      inputmode="decimal" placeholder="20" aria-label="Custom top-up amount in dollars" value={custom} oninput={typed} />
  </div>
  <p class="bx-pay" id="topupValue" hidden={!view.note}>{view.note || ""}</p>
  <button id="topup" class="primary" disabled={busy} onclick={go}>{view.button}</button>
  <span class="fine" id="topupMsg">{msg}</span>
  <p class="bx-secure">
    <span class="bx-secure__lock" aria-hidden="true">
      <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.4">
        <rect x="3" y="7" width="10" height="6.5" rx="1.2"></rect>
        <path d="M5 7V5a3 3 0 0 1 6 0v2"></path>
      </svg>
    </span>
    <span>Secured by Stripe. Card details never touch our servers, and no card is stored after a top-up.</span>
  </p>
</div>
