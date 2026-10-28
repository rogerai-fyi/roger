// src/app.html is GENERATED from the legacy head partial so the two inline scripts whose
// sha256 the edge CSP allow-lists stay byte-identical. Run before `vite build`.
import { readFileSync, writeFileSync } from "node:fs";
const head = readFileSync(new URL("../../web/src/_partials/head.html", import.meta.url), "utf8");
const inline = [...head.matchAll(/^  <script>(?!\s*\(function \(\) \{\s*try)[^\n]*<\/script>$/gm)].map((m) => m[0]);
if (inline.length !== 2) throw new Error(`expected the 2 hashed inline scripts in head.html, found ${inline.length}`);
const noscript = head.match(/^  <noscript>.*<\/noscript>$/m)[0];
const ld = head.match(/^  <script type="application\/ld\+json">\{"@context":"https:\/\/schema.org","@graph".*<\/script>$/m)[0];
const html = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
${inline.join("\n")}
${noscript}
  %sveltekit.head%
${ld}
  <link rel="icon" href="favicon.svg" type="image/svg+xml" />
  <link rel="mask-icon" href="favicon.svg" color="#E0231C" />
  <link rel="apple-touch-icon" href="ping.svg" />
  <meta name="theme-color" content="#E0231C" />
  <link rel="preload" href="assets/fonts/space-grotesk-latin.woff2" as="font" type="font/woff2" crossorigin />
  <script src="js/easter-egg.js" defer></script>
</head>
<body>
  <div style="display: contents">%sveltekit.body%</div>
  <script src="js/lets-talk.js" defer></script>
  <script src="js/site.js" defer></script>
  <script src="js/session.js" defer></script>
  <script src="js/promo.js" defer></script>
  <script src="js/tuner.js" defer></script>
  <script src="js/scrub.js" defer></script>
  <script src="js/anchor-hold.js" defer></script>
  <script src="js/range-twin.js" defer></script>
  <script src="js/cycle.js" defer></script>
</body>
</html>
`;
writeFileSync(new URL("../src/app.html", import.meta.url), html);
console.log("src/app.html generated from web/src/_partials/head.html");
