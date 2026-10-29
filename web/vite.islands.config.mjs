// Islands: the interactive parts of otherwise static pages, each an entry in app/islands/,
// bundled by Svelte alone (no SvelteKit client). build.mjs runs this before the page build
// and ships the output as dist/js/islands/<name>.js, cache-busted like every other asset.
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vite";
import { readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const ISLANDS = fileURLToPath(new URL("./app/islands/", import.meta.url));
const input = Object.fromEntries(readdirSync(ISLANDS).filter((f) => f.endsWith(".js"))
  .map((f) => [f.replace(/\.js$/, ""), ISLANDS + f]));

export default defineConfig({
  plugins: [svelte({ compilerOptions: { css: "external" } })],
  resolve: { conditions: ["browser"] },
  build: {
    outDir: fileURLToPath(new URL("./app/.islands", import.meta.url)),
    emptyOutDir: true,
    modulePreload: false,
    rollupOptions: {
      input,
      output: {
        entryFileNames: "[name].js", chunkFileNames: "[name].js", assetFileNames: "[name][extname]",
        // one shared chunk for the Svelte runtime, the broker client and the island mount, so a
        // visitor downloads it once for every island on the site
        manualChunks: (id) => (/node_modules\/svelte\/|\/app\/lib\/(api|island|fmt)\.js$/.test(id) ? "shared" : undefined),
      },
    },
  },
});
