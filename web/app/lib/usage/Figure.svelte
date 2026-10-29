<script>
  // A time-series figure on the Metrics page: the title, an optional in/out legend, the
  // readout for the focused bucket, and the chart. Honest empty: hidden unless some bucket
  // carries a nonzero value (legacy: renderTs in web/src/js/metrics.js).
  import TimeSeries from "./TimeSeries.svelte";
  import { points, anyValue } from "./series.js";
  import { n0 } from "#lib/format.js";
  let { id, title, daily, series, fmt, stack = false, aria, legend = false } = $props();
  const shown = $derived(!!(daily && daily.length) && anyValue(daily, series));
  const pts = $derived(shown ? points(daily, series) : []);
  let active = $state(-1);
  $effect(() => { pts; active = -1; });   // a new range: no stale readout lingers
</script>
<figure class="mx-ts" id="{id}Ts" hidden={!shown}>
  <figcaption class="mx-ts__head">
    <span class="mx-ts__title">{title}</span>
    {#if legend}
    <span class="mx-legend" aria-hidden="true">
      <span class="mx-legend__sw mx-legend__sw--out"></span>out
      <span class="mx-legend__sw mx-legend__sw--in"></span>in
    </span>
    {/if}
    <span class="mx-ts__read" id="{id}Read" aria-live="polite">{#if active >= 0 && pts[active]}<span class="mx-ts__rk">{pts[active].label}</span>{#each series as s}<b class="mx-ts__rv mx-ts__rv--{s.cls}">{s.label ? s.label + " " : ""}{fmt(n0(pts[active].series[s.key]))}</b>{/each}{/if}</span>
  </figcaption>
  <div class="mx-ts__plot" id="{id}Plot">{#if shown}<TimeSeries points={pts} {series} {stack} {aria} bind:active />{/if}</div>
</figure>
