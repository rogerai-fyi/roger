#!/usr/bin/env node
// Hand a ported page to the app: list it in app/owned.json, remove the legacy page and its
// CSS_BUNDLES entry, rebuild. Run page-parity.mjs first; `git rm` the legacy page yourself.
//   node scripts/handover.mjs <page.html>...
import { readFileSync, writeFileSync, existsSync, unlinkSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..");
const owned = JSON.parse(readFileSync(join(WEB, "app", "owned.json"), "utf8"));
let manifest = readFileSync(join(WEB, "build.mjs"), "utf8");
for (const page of process.argv.slice(2)) {
  const route = page === "index.html" ? "" : page.replace(/\.html$/, "");
  if (!["+page.svelte", "+page.md"].some((f) => existsSync(join(WEB, "app", "routes", route, f)))) throw new Error(`${page}: no route; port it first`);
  if (!owned.includes(page)) owned.push(page);
  if (existsSync(join(WEB, "src", page))) unlinkSync(join(WEB, "src", page));
  const before = manifest;
  manifest = manifest.replace(new RegExp(`^[ \\t]*"${page.replace(".", "\\.")}":\\s*\\[[^\\]]*\\],?[^\\n]*\\n`, "m"), "");
  if (manifest === before) console.warn(`${page}: no CSS_BUNDLES entry to drop`);
  console.log(`${page}: owned by the app`);
}
writeFileSync(join(WEB, "app", "owned.json"), JSON.stringify(owned.sort(), null, 2) + "\n");
writeFileSync(join(WEB, "build.mjs"), manifest);
execFileSync("node", ["build.mjs"], { cwd: WEB, stdio: "inherit" });
