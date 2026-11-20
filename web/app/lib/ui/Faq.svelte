<script>
  // A broadcast's FAQ section: the questions a post lists (`faq`), rendered where the post
  // places it and, by the layout, as the FAQPage JSON-LD in the head. One source for both: a
  // question can never be in the schema and not on the page. title: the h2; label: the
  // section number line (its class too, one post sets it mono); an item with schema: false is
  // on the page but not in the schema (a question answered for people, not for bots).
  let { no, items = [], title = "Frequently asked questions", label = "", labelClass = "bc-sec__no" } = $props();
</script>
<section class="bc-sec" id="s{no}" data-reveal>
  <span class={labelClass}>{#if label}{@html label}{:else}&sect;{no} &middot; FAQ{/if}</span>
  <h2>{@html title}</h2>
  <dl class="bc-faq">
    {#each items as q}<dt><b>{@html q.qHtml || q.q}</b></dt><dd>{@html q.aHtml || q.a}</dd>{/each}
  </dl>
</section>
