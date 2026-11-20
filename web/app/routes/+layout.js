// Every page is a static file: prerendered, and never hydrated through SvelteKit's client.
// Interactive parts are islands (lib/ui/Island.svelte + app/islands/*.js) mounted by Svelte
// alone, so a page ships no inline loader and the edge CSP stays `script-src 'self'`.
export const prerender = true;
export const csr = false;
