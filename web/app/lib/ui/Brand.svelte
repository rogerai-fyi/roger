<script>
  // The RogerAI brand mark, defined ONCE (legacy: _partials/brand.html).
  let { as = "nav", href = "/" } = $props();
</script>
{#if as === "footer"}
<div class="footer__brand">
  <svg class="brand__logo" viewBox="0 0 32 32" fill="currentColor" aria-hidden="true" focusable="false">
    <path d="M4 6 h9 v4.5 H8.5 v11 H13 V26 H4 Z" />
    <path d="M28 6 h-9 v4.5 H23.5 v11 H19 V26 h9 Z" />
    <circle class="brand__pulse" cx="16" cy="16" r="5" />
  </svg>
  <span class="brand__name">Roger<b>AI</b></span>
</div>
{:else}
<a class="brand" {href} aria-label="RogerAI home">
  <svg class="brand__logo" viewBox="0 0 32 32" fill="currentColor" aria-hidden="true" focusable="false">
    <path d="M4 6 h9 v4.5 H8.5 v11 H13 V26 H4 Z" />
    <path d="M28 6 h-9 v4.5 H23.5 v11 H19 V26 h9 Z" />
    <circle class="brand__pulse" cx="16" cy="16" r="5" />
  </svg>
  <span class="brand__name">Roger<b>AI</b></span>
</a>
{/if}
