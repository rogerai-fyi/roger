// Polish round 5 (2026-10): the last of the "left" list. Static checks on the markup and
// the sheets; the rendered checks (the tuners at 390 and 1440, the App screenshots on the
// dark site, the Playbox offline header at 390) were run with Playwright by hand.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const css = (f) => src(`styles/${f}`).replace(/\/\*[\s\S]*?\*\//g, "");

/* ---- the article heroes read in one palette ---------------------------------------- */

// Every broadcast hero is drawn in greys (with at most the one red); the DeepSeek hero was
// drawn on a yellow ground. The shared figure modifier renders a still in greyscale, so the
// PNG master stays as drawn (and stays the social card).
test("figure--mono: the shared figure modifier renders its still in greyscale", () => {
  assert.match(css("components.css"), /\.figure--mono > :is\(img, video\) \{\s*filter: grayscale\(1\);\s*\}/);
});

test("deepseek: the warm hero takes the greyscale treatment; the og:image stays the PNG master", () => {
  const html = src("broadcasts-deepseek-mtp-gguf.html");
  const lead = html.match(/<figure class="([^"]*)"[^>]*>\s*<img src="assets\/broadcasts\/mtp-hero\.webp"/);
  assert.ok(lead, "the hero figure");
  assert.match(lead[1], /\bfigure--plate\b/);
  assert.match(lead[1], /\bfigure--mono\b/, "the hero carries .figure--mono");
  assert.match(html, /ogimage="https:\/\/rogerai\.fm\/assets\/broadcasts\/mtp-hero\.png"/);
});
