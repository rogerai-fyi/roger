<script>
  // An island: the interactive part of an otherwise static page. The page prerenders the
  // component's markup here, and app/islands/<name>.js hydrates it in the browser with
  // Svelte alone (no SvelteKit client, no inline loader, so the edge CSP stays
  // `script-src 'self'`). The island's own script is loaded as a module, cache-busted by
  // build.mjs like every other asset. Nothing else in the page runs through Svelte.
  //
  // The component is rendered with svelte/server's render() so the markup carries exactly
  // the hydration envelope hydrate() looks for (an {@render} of the component as children
  // did not, and Svelte quietly threw the prerendered markup away: an audit finding).
  import { render } from "svelte/server";
  let { name, component, props = {} } = $props();
  const { body, head } = render(component, { props });
</script>
<svelte:head>{@html head}{@html '<script type="module" src="js/islands/' + name + '.js"><\/script>'}</svelte:head>
<div data-island={name}>{@html body}{@html "<!--/island-->"}</div>
