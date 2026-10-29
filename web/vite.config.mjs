// The SvelteKit side of the site build. SvelteKit 3 takes its configuration here (there is
// no svelte.config.js). The app lives in app/ (routes, lib, app.html, hooks, islands) beside
// the legacy pages in src/; build.mjs runs both and writes one dist/. Every route is
// prerendered to a static file; nothing hydrates through SvelteKit's client. Interactive
// parts are islands (app/islands/*.js, built by vite.islands.config.mjs) that mount with
// Svelte alone, so a page ships no inline loader and the edge CSP stays `script-src 'self'`.
import adapter from "@sveltejs/adapter-static";
import { sveltekit } from "@sveltejs/kit/vite";
import { mdsvex } from "mdsvex";
import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";

// mdsvex writes the layout path into the generated import verbatim, so it must be absolute
const POST_LAYOUT = fileURLToPath(new URL("./app/lib/ui/BroadcastPost.svelte", import.meta.url));

// build.mjs names the pages the app owns (app/owned.json + WEB_PREVIEW) as routes; run
// directly (`npx vite build`) the app prerenders every route it has.
const entries = process.env.WEB_KIT_ENTRIES ? JSON.parse(process.env.WEB_KIT_ENTRIES) : ["*"];

export default defineConfig({
  plugins: [
    sveltekit({
      files: { src: "app", assets: "app/static" },
      extensions: [".svelte", ".md"],
      preprocess: [mdsvex({ extensions: [".md"], smartypants: false, layout: { broadcast: POST_LAYOUT } })],
      adapter: adapter({ pages: "app/.build", assets: "app/.build", precompress: false, strict: true }),
      prerender: {
        crawl: false,                 // links point at legacy pages this build does not own
        entries,
        handleHttpError: "warn",
        handleMissingId: "warn",
        handleUnseenRoutes: "ignore", // a route not yet owned is not an error
      },
    }),
  ],
  // vitest: components mount in jsdom, so resolve Svelte's browser build under test
  resolve: process.env.VITEST ? { conditions: ["browser"] } : undefined,
  test: {
    include: ["test/unit/**/*.test.js"],
    environment: "jsdom",
    setupFiles: ["@testing-library/svelte/vitest"],   // unmount between tests
  },
});
