// The site's bundles: the islands (the interactive parts of otherwise static pages, each an
// entry in app/islands/, bundled by Svelte alone, no SvelteKit client) and the site runtime
// (app/runtime.js, the shared page scripts as one module) and each page's own scripts
// (app/pages/<page>.js). build.mjs runs this before the page build and ships the output as
// dist/js/islands/<name>.js, dist/js/runtime.js and dist/js/pages/<page>.js, cache-busted like
// every other asset.
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { defineConfig } from "vite";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const ISLANDS = fileURLToPath(new URL("./app/islands/", import.meta.url));
const PAGES = fileURLToPath(new URL("./app/pages/", import.meta.url));
const input = Object.fromEntries(readdirSync(ISLANDS).filter((f) => f.endsWith(".js"))
  .map((f) => [f.replace(/\.js$/, ""), ISLANDS + f]));
input.runtime = fileURLToPath(new URL("./app/runtime.js", import.meta.url));
for (const f of readdirSync(PAGES).filter((f) => f.endsWith(".js"))) input["pages/" + f.replace(/\.js$/, "")] = PAGES + f;

// The runtime and page bundles carry plain scripts from src/js (IIFEs, no import or export).
// Three of them keep a `module.exports` branch for the node:test guards that load them with
// new Function("module", ...); seen bare, the bundler would read that as CommonJS and wrap
// them in an interop helper chunk (rolldown-runtime.js) the shipped layout cannot resolve.
// Marked as modules, they bundle as the side-effect scripts they are (in a module `module` is
// undefined, so that branch never runs in the browser, as before).
// A page bundle is self-contained: a script two pages share (wave-mark-spectrum.js on both
// research pages) is bundled into each, the way two pages each loaded the file, rather than
// split into a chunk the shipped layout (js/pages/<page>.js beside js/runtime.js) could not
// reach. Each page entry sees its own copy of every src/js script it imports.
const SRC_JS = fileURLToPath(new URL("./src/js/", import.meta.url));
const plainScripts = {
  name: "plain-scripts-are-modules",
  enforce: "pre",   // before Vite's own resolver turns the relative import into one shared file
  resolveId(source, importer) {
    if (!importer || !importer.startsWith(PAGES) || !source.startsWith("../../src/js/")) return null;
    return "\0page:" + importer.slice(PAGES.length).replace(/\.js$/, "") + ":" + SRC_JS + source.slice("../../src/js/".length);
  },
  load(id) {
    if (!id.startsWith("\0page:")) return null;
    return readFileSync(id.slice(id.indexOf(":", 6) + 1), "utf8");
  },
  transform(code, id) {
    const isModule = /^\s*(?:import\s*[\w{*"']|export\s+(?:\{|default|const|let|var|function|class))/m.test(code);   // a statement, not a comment that starts with the word
    if ((/\/src\/js\/[\w-]+\.js$/.test(id) || id.startsWith("\0page:")) && !isModule) return { code: code + "\nexport {};\n", map: null };
  },
};

export default defineConfig({
  plugins: [plainScripts, svelte({ compilerOptions: { css: "external" } })],
  resolve: { conditions: ["browser"] },
  build: {
    outDir: fileURLToPath(new URL("./app/.islands", import.meta.url)),
    emptyOutDir: true,
    modulePreload: false,
    rollupOptions: {
      input,
      output: {
        // an entry keeps its name (the page cache-busts it with ?v=); a chunk an entry imports is
        // named by its content, so a changed chunk is a new url at the edge too
        entryFileNames: "[name].js", chunkFileNames: "[name]-[hash].js", assetFileNames: "[name][extname]",
        // one shared chunk for the Svelte runtime, the broker client and the island mount, so a
        // visitor downloads it once for every island on the site
        manualChunks: (id) => (/node_modules\/svelte\/|\/app\/lib\/(api|island|fmt)\.js$/.test(id) ? "shared" : undefined),
      },
    },
  },
});
