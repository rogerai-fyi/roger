<script module>
  // GET /categories is read once per page: every card's editor shares the list (legacy: the
  // module-level catList in console.js). Only a good list is cached; a failed read is retried
  // by the next editor. forgetCategories() is for tests, which mount many pages in one module.
  let cached = null;
  export function forgetCategories() { cached = null; }
</script>
<script>
  // The category editor (features/categories/web_card_editor.feature): a front-end on
  // POST /provider/meta. The broker validates, so a refusal shows the server's reason.
  import { untrack } from "svelte";
  import { BROKER, postJSON } from "#lib/api.js";
  let { m, onsaved, hidden = false } = $props();   // hidden between opens: the edits survive a close

  let catList = $state(cached);
  async function loadCatList() {
    if (cached) { catList = cached; return cached; }
    try {
      const r = await fetch(BROKER + "/categories", { cache: "no-store" });
      const d = r.ok ? await r.json() : null;
      if (d && d.categories) cached = catList = d.categories;
      return (d && d.categories) || [];
    } catch { return []; }
  }

  let category = $state(untrack(() => m.category || ""));
  let subcategory = $state(untrack(() => m.subcategory || ""));
  let tags = $state(untrack(() => (m.tags || []).join(", ")));
  let tuning = $state(untrack(() => m.tuning || ""));
  let policy = $state(untrack(() => m.content_policy || ""));
  let license = $state(untrack(() => m.license || ""));
  let baseModel = $state(untrack(() => m.base_model || ""));
  // The details (features/details/declaration.feature): shown to people browsing the market.
  let summary = $state(untrack(() => m.summary || ""));
  let bestAt = $state(untrack(() => m.best_at || ""));
  let notFor = $state(untrack(() => m.not_for || ""));
  let busy = $state(false);
  let ready = $state(false);
  let msg = $state({ cls: "", text: "" });
  const subs = $derived([""].concat(...(catList || []).filter((c) => c.name === category).map((c) => (c.subcategories || []).map((s) => s.name))));

  $effect(() => {
    loadCatList().then((list) => {
      ready = list.length > 0;
      if (!list.length) msg = { cls: "md-state--err", text: "Could not load the category list - try again shortly." };
    });
  });

  async function send(payload) {
    busy = true; msg = { cls: "", text: "Saving..." };
    try {
      const r = await postJSON("/provider/meta", payload);
      const j = await r.json();
      busy = false;
      if (!r.ok || !j || !j.ok) { msg = { cls: "md-state--err", text: (j && j.error && j.error.message) || "Could not save." }; return; }
      msg = { cls: "md-state--ok", text: j.note || "Saved." };
      onsaved?.();
    } catch { busy = false; msg = { cls: "md-state--err", text: "Could not reach the broker." }; }
  }
  const save = () => send({
    node: m.node, model: m.model,
    category: subcategory ? category + "/" + subcategory : category,
    tags: tags.split(",").map((s) => s.trim()).filter(Boolean),
    tuning, content_policy: policy, license: license.trim(), base_model: baseModel.trim(),
    summary: summary.trim(), best_at: bestAt.trim(), not_for: notFor.trim(),
  });
  const clear = () => send({ node: m.node, model: m.model, clear: true });
</script>
<div class="md-catbox" {hidden}>
  <label class="md-catrow"><span class="md-lbl">Category</span>
    <select class="md-catsel" bind:value={category} onchange={() => (subcategory = "")}>
      <option value="">(none)</option>
      {#each catList || [] as c}<option value={c.name}>{c.name}</option>{/each}
    </select></label>
  <label class="md-catrow"><span class="md-lbl">Subcategory</span>
    <select class="md-catsel" bind:value={subcategory}>
      {#each subs as s}<option value={s}>{s || "(none)"}</option>{/each}
    </select></label>
  <label class="md-catrow"><span class="md-lbl">Tags</span><input class="md-cattext" type="text" placeholder="tags, comma-separated" bind:value={tags} /></label>
  <label class="md-catrow"><span class="md-lbl">Tuning</span>
    <select class="md-catsel" bind:value={tuning}>
      {#each ["", "base", "instruct", "finetune", "merge", "quantized", "adapter"] as v}<option value={v}>{v || "(none)"}</option>{/each}
    </select></label>
  <label class="md-catrow"><span class="md-lbl">Content policy</span>
    <select class="md-catsel" bind:value={policy}>
      {#each ["", "aligned", "reduced-refusal", "uncensored"] as v}<option value={v}>{v || "(none)"}</option>{/each}
    </select></label>
  <label class="md-catrow"><span class="md-lbl">License</span><input class="md-cattext" type="text" placeholder="license (e.g. Apache-2.0)" bind:value={license} /></label>
  <label class="md-catrow"><span class="md-lbl">Base model</span><input class="md-cattext" type="text" placeholder="base model" bind:value={baseModel} /></label>
  <label class="md-catrow"><span class="md-lbl">Summary</span><input class="md-cattext" type="text" maxlength="140" placeholder="one line: what the model is" bind:value={summary} /></label>
  <label class="md-catrow"><span class="md-lbl">Best at</span><textarea class="md-cattext" maxlength="200" rows="2" placeholder="what it does well" bind:value={bestAt}></textarea></label>
  <label class="md-catrow"><span class="md-lbl">Not for</span><textarea class="md-cattext" maxlength="200" rows="2" placeholder="what it should not be used for" bind:value={notFor}></textarea></label>
  {#if m.curated_note}<p class="fine">RogerAI describes this model; your text shows only where it does not.</p>{/if}
  <p class="fine">Your own description, shown as declared. A content-policy grade describes the weights; RogerAI still screens every request.</p>
  <div class="md-foot">
    <button class="primary" type="button" disabled={busy || !ready} onclick={save}>Save category</button>
    <button class="ghost" type="button" disabled={busy} onclick={clear}>Clear</button>
    <span class="md-state {msg.cls}">{msg.text}</span>
  </div>
</div>
