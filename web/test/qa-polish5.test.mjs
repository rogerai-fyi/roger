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

/* ---- the App screenshots on the dark site: adapt, don't invert --------------------- */

// The iPhone and Mac captures are light UI. On the dark site they glared even at 84%
// brightness. Founder ruling: adapt, don't invert - keep the light subject light (its
// polarity), dim it further, and seat it on a warm dark-grey mat. The contrast step rises
// with the dim so the screenshots' own text keeps its contrast (white at .76 then 1.12
// lands near 200, the 142 grey of secondary text near 104: about 3.2:1, as the undimmed
// capture's 3.3:1).
test("app screenshots on the dark site: dimmed, never inverted, on a warm dark-grey mat", () => {
  const c = css("app.css");
  const rule = c.match(/\[data-theme="dark"\] \.app-shot img \{([^}]*)\}/)?.[1] || "";
  const filter = rule.match(/filter:\s*([^;]*)/)?.[1] || "";
  assert.doesNotMatch(filter, /invert|hue-rotate/, "polarity kept");
  const b = Number(filter.match(/brightness\(([\d.]+)\)/)?.[1]);
  const k = Number(filter.match(/contrast\(([\d.]+)\)/)?.[1]);
  assert.ok(b >= 0.7 && b <= 0.8, `dimmed to 70-80% (was 84%, still glared): ${b}`);
  assert.ok(k >= 1.08, `the contrast step rises with the dim: ${k}`);
  // the capture's own text: white and the iOS secondary grey (142) through the filter
  const through = (v) => Math.min(1, Math.max(0, (v * b - 0.5) * k + 0.5));
  const lin = (v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  const ratio = (x, y) => (lin(through(x)) + 0.05) / (lin(through(y)) + 0.05);
  assert.ok(ratio(1, 142 / 255) >= 3.1, `secondary text in a shot keeps its contrast: ${ratio(1, 142 / 255).toFixed(2)}`);
  assert.match(rule, /padding:\s*\d+px/, "a mat round the capture");
  assert.match(rule, /background:\s*var\(--paper-3\)/, "the mat is the warm dark-grey fill");
  // the terminal and browser plates are dark captures and stay out of it
  assert.doesNotMatch(c, /\[data-theme="dark"\] \.app-tile img \{[^}]*filter/);
});
