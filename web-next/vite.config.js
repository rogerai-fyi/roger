// Phase 0 spike config. SvelteKit 3 takes its configuration here (svelte.config.js is gone).
// Everything prerenders to a static tree (adapter-static); no server. CSP: hash mode, so the
// one inline script SvelteKit emits on a hydrated page is hashed and `script-src 'self'`
// stays the policy. Content pages set csr=false and ship no script at all.
import adapter from "@sveltejs/adapter-static";
import { sveltekit } from "@sveltejs/kit/vite";
import { mdsvex } from "mdsvex";
import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";

// mdsvex writes the layout path into the generated import verbatim, so it must be absolute
const POST_LAYOUT = fileURLToPath(new URL("./src/lib/ui/BroadcastPost.svelte", import.meta.url));

export default defineConfig({
  plugins: [
    sveltekit({
      extensions: [".svelte", ".md"],
      preprocess: [mdsvex({ extensions: [".md"], smartypants: false, layout: { broadcast: POST_LAYOUT } })],
      adapter: adapter({ pages: "build", assets: "build", precompress: false, strict: true }),
      // hash mode: Kit hashes the one inline script it emits on a hydrated page. The three
      // sha256 sources are the legacy allow-list (web/src/_headers): the shell's two inline
      // scripts and the homepage theme script. Without them the meta CSP Kit writes into the
      // page would block the shell's own scripts.
      csp: { mode: "hash", directives: { "script-src": ["self",
        "sha256-SnDneqm6BGJa6PkBRyNyh7dC65pFhdmWIclxI/UxeeU=",
        "sha256-WZRJfWvsnNCPcxzZwvyhovnZGqhZaC+8gPGPRbx6wTk=",
        "sha256-dhz791SJGjwR48M9Bg5QhzTI/dKpzDbXm+YjhUNkQHQ="] } },
      prerender: {
        crawl: false,                 // links point at legacy pages this build does not own
        entries: ["/faq", "/console", "/broadcasts-free-chatgpt-alternative"],
        handleHttpError: "warn",
        handleMissingId: "warn",
      },
    }),
  ],
  // vitest: components mount in jsdom, so resolve Svelte's browser build under test
  resolve: process.env.VITEST ? { conditions: ["browser"] } : undefined,
  test: {
    include: ["test/**/*.test.js"],
    environment: "jsdom",
    setupFiles: ["@testing-library/svelte/vitest"],   // unmount between tests
  },
});
