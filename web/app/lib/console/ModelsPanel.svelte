<script>
  // Models & pricing (owner only): per-model base price + time-of-use windows.
  // GET /provider/models -> { models:[row], ceiling_in, ceiling_out }
  import { onMount } from "svelte";
  import { api } from "#lib/api.js";
  import { priceStr } from "./format.js";
  import ModelCard from "./ModelCard.svelte";
  let status = $state("loading");   // loading | error | empty | ready
  let rows = $state([]);
  let gen = $state(0);              // one generation per load: the cards are rebuilt from the fresh rows
  let ceil = $state({ in: 50, out: 100 });
  export async function load() {
    status = "loading";
    gen += 1;
    try {
      const r = await api("/provider/models");
      if (!r.ok) throw new Error("http " + r.status);
      const d = await r.json();
      if (d && typeof d.ceiling_in === "number") ceil.in = d.ceiling_in;
      if (d && typeof d.ceiling_out === "number") ceil.out = d.ceiling_out;
      rows = (d && d.models) || [];
      status = rows.length ? "ready" : "empty";
    } catch { status = "error"; }
  }
  onMount(load);   // once, on mount (an effect would re-run on the generation it bumps)
</script>
<section class="panel" id="modelsWrap">
  <h2>Models &amp; pricing</h2>
  <p>Set what each model you serve charges per million tokens, and add time-of-use windows (a cheaper night rate, a free daily window). Changes publish immediately and survive the node reconnecting - they only ever set future prices, never what was already billed.</p>
  {#if status === "loading"}<p class="cn-state" id="mdLoading">Reading your models...</p>{/if}
  {#if status === "error"}<p class="cn-state" id="mdError">Could not load your models. Try again shortly.</p>{/if}
  {#if status === "empty"}<p class="cn-state" id="mdEmpty">No models on air yet. Run <code>roger share</code> on a box and the models it serves show up here to price.</p>{/if}
  <p class="fine" id="mdCatNote">Each card shows the category your station registered (a guess is marked <code>~</code>). Change it here with <b>Edit category</b> (it then wins over the node's own), or from the node with <code>roger share --category</code>, the TUI SHARE tab (<kbd>g</kbd>), or the local console's Category button.</p>
  {#if status === "ready"}
    <svelte:boundary onerror={(e) => console.error("console: could not render the models", e)}>
    <div class="md-list" id="mdList">
      {#each rows as m, i (gen + ":" + i)}<ModelCard {m} {ceil} onreload={load} />{/each}
    </div>
    <p class="fine" id="mdCeil">Price ceiling: ${priceStr(ceil.in)} in / ${priceStr(ceil.out)} out per 1M tokens. It applies to every band, public or private - a private band hides a station from the market, it is not a price bypass.</p>
    {#snippet failed()}<p class="cn-state" id="mdError">Could not load your models. Try again shortly.</p>{/snippet}
    </svelte:boundary>
  {/if}
</section>
