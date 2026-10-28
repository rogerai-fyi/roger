// Dual build, spike edition: lay this build's pages over a legacy dist tree.
//   node scripts/overlay.mjs <dist-dir>
// For each page in build/*.html: version local js/styles/assets URLs with the legacy content
// hash (so the CDN rule and the weight tooling see the same URL shape), glue heading dashes
// like the legacy build, then write it over the legacy page. _app/ is copied whole. A page
// owned by both builds is the collision Phase 1 forbids; here it is the point.
import { readFileSync, writeFileSync, readdirSync, cpSync, mkdirSync, existsSync } from "node:fs";
import { join, resolve, dirname } from "node:path";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import { glueDashes } from "../../web/scripts/glue-dashes.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const BUILD = join(HERE, "..", "build");
const SRC = join(HERE, "..", "..", "web", "src");
const dist = resolve(process.argv[2] || join(HERE, "..", "..", "web", "dist"));
const hashes = new Map();
const assetHash = (rel) => {
  if (!hashes.has(rel)) {
    try { hashes.set(rel, createHash("sha256").update(readFileSync(join(SRC, rel))).digest("hex").slice(0, 8)); }
    catch { hashes.set(rel, null); }
  }
  return hashes.get(rel);
};
function cacheBust(html) {
  html = html.replace(/((?:src|href|poster)=")((?:js|styles|assets)\/[^"?]+\.(?:js|css|mp4|vtt|gif|png|webp|svg|jpe?g|avif|woff2))"/g,
    (m, pre, rel) => { const h = assetHash(rel); return h ? `${pre}${rel}?v=${h}"` : m; });
  return html.replace(/(srcset=")([^"]+)"/g, (_, pre, list) => pre + list.split(",").map((e) => {
    const m = e.trim().match(/^((?:js|styles|assets)\/[^\s?]+)(\s+\S+)?$/);
    const h = m && assetHash(m[1]);
    return h ? `${m[1]}?v=${h}${m[2] || ""}` : e.trim();
  }).join(", ") + '"');
}
const pages = readdirSync(BUILD).filter((f) => f.endsWith(".html"));
for (const p of pages) {
  let html = readFileSync(join(BUILD, p), "utf8");
  html = cacheBust(glueDashes(html));
  writeFileSync(join(dist, p), html);
}
cpSync(join(BUILD, "_app"), join(dist, "_app"), { recursive: true });
console.log(`overlaid ${pages.length} page(s) + _app/ onto ${dist}: ${pages.join(", ")}`);
