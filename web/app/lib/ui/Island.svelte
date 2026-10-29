<script>
  // An island: the interactive part of an otherwise static page. The page prerenders the
  // component's markup here, and app/islands/<name>.js hydrates it in the browser with
  // Svelte alone (no SvelteKit client, no inline loader, so the edge CSP stays
  // `script-src 'self'`). The island's own script is loaded as a module, cache-busted by
  // build.mjs like every other asset. Nothing else in the page runs through Svelte.
  let { name, children } = $props();
</script>
<svelte:head>{@html '<script type="module" src="js/islands/' + name + '.js"><\/script>'}</svelte:head>
<div data-island={name}>{@render children()}</div>
