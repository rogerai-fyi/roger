<script>
  // The monthly spend limit panel on /billing (legacy: initLimit/paintLimit/saveLimit in
  // web/src/js/billing.js): the account-wide guardrail. GET /account/limit ->
  // { monthly_cap, monthly_spend } (USD; 0 = no limit); PATCH { monthly_cap } sets it (0 =
  // clear to unlimited) and echoes the stored state, so the panel repaints from the response,
  // never from the typed value. The meter's used portion is the one red glint. The page
  // starts the read once its own feed confirmed the session (load()).
  import { api } from "#lib/api.js";
  import { n0, cr } from "#lib/format.js";

  let state = $state("loading");   // loading | error | ready
  let cap = $state(0);
  let spend = $state(0);
  let input = $state("");          // the field's text, reflected from the live cap unless being edited
  let msg = $state("");            // "" = no notice
  let saving = $state(false);
  let field;

  const ratio = $derived(cap > 0 ? spend / cap : 0);
  const pct = $derived(Math.round(ratio * 100));
  const width = $derived(Math.min(100, Math.max(0, ratio * 100)) + "%");

  function paint(d) {
    cap = n0(d.monthly_cap);
    spend = n0(d.monthly_spend);
    // reflect the live cap into the field, but never clobber an active edit
    if (cap > 0 && document.activeElement !== field) input = String(cap);
  }
  export async function load() {
    let d = null;
    try { const r = await api("/account/limit"); d = r.ok ? await r.json() : null; } catch { d = null; }
    if (!d) { state = "error"; return; }
    paint(d);
    state = "ready";
  }
  // clear=true forces 0 (remove); otherwise the field is read and validated (blank or 0 =
  // no limit).
  async function save(clear) {
    let val;
    if (clear) val = 0;
    else {
      const raw = (input || "").trim();
      val = raw === "" ? 0 : parseFloat(raw);
      if (!isFinite(val) || val < 0) { msg = "Enter a dollar amount of $0 or more (0 = no limit)."; return; }
      val = Math.round(val * 100) / 100;
    }
    saving = true;
    msg = "Saving...";
    let r = null;
    try {
      const res = await api("/account/limit", { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ monthly_cap: val }) });
      r = res.ok ? await res.json() : null;
    } catch { r = null; }
    saving = false;
    if (!r) { msg = "Could not save - try again shortly."; return; }
    paint(r);
    msg = n0(r.monthly_cap) > 0 ? "Saved - limit set to " + cr(r.monthly_cap) + "/mo." : "Saved - no monthly limit.";
  }
</script>
<section class="panel bx-limit" id="limitBox">
  <h2>Monthly spend limit</h2>
  <p class="bx-limit__lede">
    <span class="bx-limit__tag">second guardrail</span>
    One account-wide ceiling on everything you pay for this calendar month -
    your own tune-ins to paid models and any sponsored grant keys, together.
    Per-key token caps limit a single key; this bounds your whole account.
    <a href="/keys.html">Manage keys</a>.
  </p>

  <div id="limitBody" hidden={state !== "ready"}>
    <!-- the meter: shown once a cap is set. Month-to-date spend against the cap on the
         payouts hairline rail; the used portion is the red glint. -->
    <div class="bx-limit__gauge" id="limitGauge" hidden={!(cap > 0)}>
      <div class="bx-limit__read">
        <span class="bx-limit__used" id="limitUsed">{cap > 0 ? cr(spend) : "-"}</span>
        <span class="bx-limit__of">of</span>
        <span class="bx-limit__cap" id="limitCap">{cap > 0 ? cr(cap) + "/mo" : "-"}</span>
        <span class="bx-limit__pct{cap > 0 && ratio >= 1 ? ' is-over' : ''}" id="limitPct">{cap > 0 ? pct + "%" : ""}</span>
      </div>
      <div class="bx-limit__track" role="img" id="limitTrack"
           aria-label="Month-to-date spend against your monthly limit">
        <span class="bx-limit__seg" id="limitSeg" style:width={cap > 0 ? width : null}></span>
      </div>
    </div>

    <!-- no cap set: a quiet, honest line. The form below still lets you set one. -->
    <p class="bx-limit__none" id="limitNone" hidden={cap > 0}>No limit set - all paid usage
      (your own and sponsored keys) bills to your wallet without a monthly ceiling.</p>

    <!-- the set / update / clear form. -->
    <div class="bx-limit__field">
      <label class="bx-limit__k" for="limitInput">Set a monthly spend limit (USD)</label>
      <div class="bx-limit__row">
        <span class="bx-limit__pre" aria-hidden="true">$</span>
        <input id="limitInput" class="bx-limit__in" type="number" min="0" step="1"
          inputmode="decimal" placeholder="0.00"
          aria-label="Monthly spend limit in dollars" value={input} oninput={(e) => (input = e.currentTarget.value)} bind:this={field} />
        <span class="bx-limit__suf" aria-hidden="true">/mo</span>
      </div>
      <span class="bx-limit__hint">0 or blank = no limit. At the cap, paid requests
        pause until next month or until you raise it.</span>
      <div class="bx-limit__act">
        <button id="limitSave" class="primary bx-limit__save" disabled={saving} onclick={() => save(false)}>Save limit</button>
        <button id="limitClear" class="ghost" hidden={!(cap > 0)} onclick={() => save(true)}>Remove limit</button>
        <span class="mx-state bx-limit__msg" id="limitMsg" hidden={!msg}>{msg}</span>
      </div>
    </div>
  </div>

  <p class="mx-state" id="limitLoading" hidden={state !== "loading"}>Loading your limit...</p>
  <p class="mx-state" id="limitError" hidden={state !== "error"}>Could not load your monthly limit. Try again shortly.</p>
</section>
