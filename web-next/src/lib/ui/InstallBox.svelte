<script>
  let { id, cmd, size = "", oslock = "", label = "" } = $props();
</script>
<button class="install__box{size === 'lg' ? ' install__box--lg' : ''}" {id} type="button" data-os-lock={oslock || undefined}
                aria-label={label || "Copy install command to clipboard"}>
          <span class="install__prompt" aria-hidden="true">$</span>
          <code class="install__code">{@html cmd}</code>
          <span class="install__copy" aria-hidden="true">
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
              <rect x="9" y="9" width="11" height="11" rx="2"></rect>
              <path d="M5 15V5a2 2 0 0 1 2-2h10"></path>
            </svg>
          </span>
        </button>
