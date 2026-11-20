<script>
  // One model card: base price + time-of-use schedule + save/clear, and the category line.
  import { untrack } from "svelte";
  import { postJSON } from "#lib/api.js";
  import { priceStr, fnum, daysStr, parseDays } from "./format.js";
  import WindowRow from "./WindowRow.svelte";
  import CatEditor from "./CatEditor.svelte";
  let { m, ceil, onreload } = $props();

  let priceIn = $state(untrack(() => priceStr(m.price_in)));
  let priceOut = $state(untrack(() => priceStr(m.price_out)));
  let free = $state(untrack(() => !!m.free));
  let windows = $state(untrack(() => (m.schedule || []).map(toRow)));
  let busy = $state(false);
  let msg = $state({ cls: "", text: "" });
  let catOpen = $state(false);
  let catBuilt = $state(false);     // the editor exists from the first open on, hidden in between

  function toRow(w) {
    w = w || { days: [], start: "00:00", end: "00:00", price_in: 0, price_out: 0, free: false };
    return { days: daysStr(w.days), start: w.start || "00:00", end: w.end || "00:00",
             price_in: priceStr(w.price_in), price_out: priceStr(w.price_out), free: !!w.free };
  }
  const readRow = (r) => ({ days: parseDays(r.days), start: r.start || "00:00", end: r.end || "00:00",
                            price_in: fnum(r.price_in), price_out: fnum(r.price_out), free: !!r.free });

  let catText = $derived.by(() => {
    let t = m.taxonomy_source ? "no category" : "none declared";
    if (m.category) t = (m.subcategory ? m.category + "/" + m.subcategory : m.category) + (m.taxonomy_source === "declared" ? "" : "~");
    if (m.tags && m.tags.length) t += " · " + m.tags.join(", ");
    if (m.content_policy) t += " · " + m.content_policy;
    if (m.meta_overridden) t += " · set on the web";
    return t;
  });

  async function save() {
    busy = true; msg = { cls: "", text: "Saving..." };
    try {
      const r = await postJSON("/provider/models", {
        node: m.node, model: m.model,
        price_in: free ? 0 : fnum(priceIn), price_out: free ? 0 : fnum(priceOut),
        schedule: windows.map(readRow),
      });
      const j = await r.json();
      busy = false;
      if (!r.ok || !j || !j.ok) { msg = { cls: "md-state--err", text: (j && j.error && j.error.message) || "Could not save." }; return; }
      msg = { cls: "md-state--ok", text: "Saved" };
      setTimeout(onreload, 700);
    } catch { busy = false; msg = { cls: "md-state--err", text: "Network error. Try again." }; }
  }
  async function clear() {
    if (!window.confirm(`Reset "${m.model}" to the price the node itself sets? Your custom price is removed on its next reconnect.`)) return;
    busy = true; msg = { cls: "", text: "Clearing..." };
    try {
      const r = await postJSON("/provider/models", { node: m.node, model: m.model, clear: true });
      busy = false;
      if (!r.ok) { msg = { cls: "md-state--err", text: "Could not clear." }; return; }
      msg = { cls: "md-state--ok", text: "Cleared" };
      setTimeout(onreload, 700);
    } catch { busy = false; msg = { cls: "md-state--err", text: "Network error. Try again." }; }
  }
</script>
<div class="md-card">
  <div class="md-head">
    <span class="md-dot {m.online ? 'md-dot--on' : 'md-dot--off'}" title={m.online ? "on air" : "off air"}>{m.online ? "◉" : "○"}</span>
    <span class="md-name">{m.model || "(model)"}</span>
    <span class="md-node">{m.node || ""}</span>
    {#if m.overridden}<span class="md-tag">custom</span>{/if}
  </div>
  <div class="md-base md-cat">
    <span class="md-lbl">Category</span>
    <span class="md-catv">{catText}</span>
    <button class="ghost md-catedit" type="button" onclick={() => { catOpen = !catOpen; catBuilt = true; }}>Edit category</button>
  </div>
  {#if catBuilt}<CatEditor {m} onsaved={onreload} hidden={!catOpen} />{/if}
  <div class="md-base">
    <span class="md-lbl">Base $/1M</span>
    <span class="md-io">in</span><input class="md-in" type="number" min="0" step="0.01" max={ceil.in || null} placeholder="in" bind:value={priceIn} disabled={free} />
    <span class="md-io">out</span><input class="md-in" type="number" min="0" step="0.01" max={ceil.out || null} placeholder="out" bind:value={priceOut} disabled={free} />
    <label class="md-free"><input type="checkbox" bind:checked={free} /><span>free</span></label>
  </div>
  <div class="md-sched">
    <div class="md-shead">
      <span class="md-lbl">Schedule</span>
      <button class="md-add" type="button" onclick={() => windows.push(toRow(null))}>+ window</button>
    </div>
    <div class="md-wins">
      {#each windows as win, i (win)}
        <WindowRow bind:win={windows[i]} {ceil} onremove={() => windows.splice(i, 1)} />
      {/each}
    </div>
  </div>
  <div class="md-foot">
    <button class="primary md-save" type="button" disabled={busy} onclick={save}>Save</button>
    {#if m.overridden}<button class="ghost md-clear" type="button" disabled={busy} onclick={clear}>Reset to node price</button>{/if}
    <span class="md-state {msg.cls}">{msg.text}</span>
  </div>
</div>
