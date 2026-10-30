<script>
  // THE WAVE SPECTRUM MARK, one copy (legacy: _partials/wave-mark.html). These five paths
  // ARE the mark: the resting frame, and what js/wave-mark-spectrum.js animates from. Keep
  // the paths and that script's constants in step.
  let { slim = false, caption = "" } = $props();
</script>
<figure class="wave-mark{slim ? ' wave-mark--slim' : ''}" role="img"
        aria-label="The RogerAI wave mark: a spectrum of waves crossing at one live beacon">
  <svg class="wave-mark__svg" viewBox="-6 20 360 116" aria-hidden="true" focusable="false"
       data-animate data-slim={slim ? "" : undefined}>
    <g fill="none" stroke-linecap="round">
      <path class="wave-mark__wave wave-mark__wave--h4"
            d="M0 78C22 66 44 66 66 78S110 90 132 78S176 66 198 78S242 90 264 78S308 66 330 78L348 72"/>
      <path class="wave-mark__wave wave-mark__wave--h3"
            d="M0 78C29 60 58 60 87 78S145 96 174 78S232 60 261 78S319 96 348 78"/>
      <path class="wave-mark__wave wave-mark__wave--h2"
            d="M0 78C44 50 87 50 131 78S218 106 261 78S305 50 348 78"/>
      <path class="wave-mark__wave wave-mark__wave--wide"
            d="M0 78C58 18 116 18 174 78S290 138 348 78"/>
      <path class="wave-mark__wave wave-mark__wave--live"
            d="M0 78C58 42 116 42 174 78S290 114 348 78"/>
    </g>
    <circle class="wave-mark__ring" cx="174" cy="78" r="14"/>
    <circle class="wave-mark__node" cx="174" cy="78" r="14"/>
  </svg>
  {#if caption}<figcaption>{caption}</figcaption>{/if}
</figure>
