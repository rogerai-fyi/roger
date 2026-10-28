import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vite";
export default defineConfig({
  root: new URL("..", import.meta.url).pathname,
  plugins: [svelte({ compilerOptions: { css: "external" } })],
  resolve: { conditions: ["browser"] },
  build: {
    outDir: process.env.ISLAND_OUT || new URL("../island-dist", import.meta.url).pathname,
    emptyOutDir: true, minify: true, modulePreload: false,
    rollupOptions: { input: new URL("../src/islands/console.js", import.meta.url).pathname, output: { entryFileNames: "console-island.js" } },
  },
});
