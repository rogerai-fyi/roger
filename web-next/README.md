# web-next: the Phase 0 SvelteKit spike

Throwaway by design: Phase 0 of the migration plan (kept in the private docs).
Three pages of the site rebuilt on SvelteKit 3 + Svelte 5, prerendered to a static tree, to
measure parity, CSP, weight and test porting before any real migration work.

- `faq` (content page, `csr = false`): zero JS of its own; semantically identical to the legacy build.
- `console` (account page, hydrated): the console.js state machine as runes, one broker client.
- `broadcasts-free-chatgpt-alternative` (mdsvex): a broadcast as Markdown + one post layout.

```sh
npm ci
node scripts/gen-shell.mjs && node scripts/gen-fmt.mjs && node scripts/gen-faq.mjs   # from web/src
npm run build                      # -> build/
npm test                           # vitest: formatters + the console boot contract
SPIKE_DIST=<overlaid dist> npm run e2e        # playwright, against a legacy dist carrying these pages
node scripts/overlay.mjs <legacy dist>       # lay build/ over a legacy web/dist
```

The go/no-go numbers are in the spike report in the private docs.
