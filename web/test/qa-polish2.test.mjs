// Post-launch polish (2026-09-24): the follow-ups the launch QA report left open.
// Static checks on sources and sheets; the rendered checks (label size, tap targets)
// live in scripts/overflow-sweep.py's companions and were run at 390 by hand.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (f) => readFileSync(path.join(WEB, f), "utf8");
const css = (f) => read(path.join("src/styles", f)).replace(/\/\*[\s\S]*?\*\//g, "");

test("the OS lock is documented for both of its jobs: a one-platform command and a cross-platform non-installer one", () => {
  const js = read("src/js/site.js");
  const lock = js.slice(js.indexOf("[data-os-lock] boxes"), js.indexOf("document.querySelectorAll(\".install__box:not"));
  assert.match(lock, /data-os-lock="linux"/, "site.js comment names the one-platform lock");
  assert.match(lock, /data-os-lock="any"/, "site.js comment names the cross-platform lock");
  assert.match(lock, /roger use/);
  const ds = read("DESIGN-SYSTEM.md");
  assert.match(ds, /oslock=any/);
  assert.match(ds, /roger say/);
});

// Item 2: the inline SVG diagrams that shrank to ~5px labels on a phone. Each sits in the
// shared diagram scroll box, which keeps the drawing at least --diagram-min wide and
// scrolls it sideways (with the edge shade) on a narrower column. The floor is set so the
// figure's smallest label renders at 11px or more: 11 * viewBox width / smallest label
// size in SVG units (measured at 390 with the page's own phone sizes). Where a page sets
// more than one floor (a desktop one and a phone one), the widest is the one a phone gets.
const LABEL_FLOOR = 11;
const DIAGRAMS = [
  // page, sheet, the svg's class hook, viewBox width, smallest label (SVG units), and the
  // narrowest desktop column it sits in (measured, 1024-1920; Industrial from 1280, as its
  // 1024 column is 842px and a legible floor must scroll there)
  { page: "tower.html", sheet: "tower.css", svg: "signal__svg", vbw: 720, smallest: 9, desktop: 889 },
  { page: "tower.html", sheet: "tower.css", svg: "tower__svg", vbw: 640, smallest: 8.5, desktop: 889 },
  { page: "pricing.html", sheet: "pricing.css", svg: "rwire__svg", vbw: 958, smallest: 16, desktop: 922 },
  { page: "research-industry.html", sheet: "research.css", svg: "purdue__svg", vbw: 720, smallest: 9, desktop: 888 },
  { page: "research-wave-family.html", sheet: "wave-family.css", svg: "wf-orbit__svg", vbw: 900, smallest: 16, desktop: 882 },
];

test("the diagram scroll box is one shared component: a floor width and the edge shade", () => {
  const c = css("components.css");
  assert.match(c, /\.scroll-box--diagram > svg \{[^}]*min-width: var\(--diagram-min/);
  assert.match(c, /\.scroll-box--diagram, \.code-block pre \{[^}]*no-repeat local,[^}]*no-repeat scroll/,
    "the diagram box shares the code block's edge shade (one rule, not a copy)");
});

for (const d of DIAGRAMS) {
  test(`${d.page} ${d.svg}: in the diagram scroll box, at a floor that keeps labels >= ${LABEL_FLOOR}px`, () => {
    const html = read(path.join("src", d.page));
    const re = new RegExp(`<div class="scroll-box scroll-box--diagram"[^>]*>\\s*<svg class="${d.svg}"`);
    assert.match(html, re, "the svg is the direct child of the diagram scroll box");
    const hook = d.svg.replace("__svg", "");
    const floors = [...css(d.sheet).matchAll(new RegExp(`\\.${hook}[^{}]*\\{[^}]*--diagram-min: (\\d+)px`, "g"))].map((m) => Number(m[1]));
    assert.ok(floors.length, `${d.sheet} sets --diagram-min in the .${hook} context`);
    const need = Math.ceil((LABEL_FLOOR * d.vbw) / d.smallest);
    assert.ok(Math.max(...floors) >= need, `${d.svg}: floor ${Math.max(...floors)}px < ${need}px`);
    // and a floor set outside a phone query is never wider than a desktop column: past it,
    // a drawing that fits would scroll
    const flat = css(d.sheet).replace(/@media \(max-width: (\d+)px\)[^{]*\{(?:[^{}]*\{[^}]*\})*[^{}]*\}/g, (m, px) => (Number(px) <= 640 ? "" : m));
    for (const m of flat.matchAll(new RegExp(`\\.${hook}[^{}]*\\{[^}]*--diagram-min: (\\d+)px`, "g"))) {
      assert.ok(Number(m[1]) <= d.desktop, `${d.svg}: floor ${m[1]}px > the ${d.desktop}px desktop column`);
    }
  });
}

// Item 3: every standalone control is a 44px target on a touch screen. The sweep (Playwright
// at 390, touch emulation) listed each control under 44x44; each is held by one of three
// rules: the pattern is documented in components.css's touch block, the math is the
// --hit-inset token, and each sheet lists the small controls it owns. Links inside running text (a
// sentence, a callout, a spec plate) are exempt, as WCAG's target-size rule exempts them.
// the bodies of a sheet's @media (pointer: coarse) blocks, braces balanced
const coarseBlocks = (sheet) => {
  const out = [];
  for (const m of sheet.matchAll(/@media \(pointer: coarse\) \{/g)) {
    let i = m.index + m[0].length, depth = 1;
    const start = i;
    while (depth && i < sheet.length) { if (sheet[i] === "{") depth++; else if (sheet[i] === "}") depth--; i++; }
    out.push(sheet.slice(start, i - 1));
  }
  return out.join("\n");
};
const HIT_GROW = [ // keep their drawn size; an invisible ::before grows the tap area
  ".promo__close", ".promo__cta", ".upgrade__toggle", ".nav__burger", ".theme-toggle",
  ".install__alt", ".bc-post__back", ".wf-back", ".dir-sibling > a", ".tlink", ".faq__more",
  ".market__refresh", ".tuner__chip", ".company__primary", ".route-sim__next", ".scope__mode",
  ".model-note", ".copy-code", ".bc-tg__head",
];
// Controls packed too close for a grown area: it would cover a neighbour's drawn box, so a
// tap aimed at the neighbour opens this one (measured by scripts/touch-overlap.py at 390
// and 320). Each is 26px tall or more, inside the 24px target-size minimum with spacing.
const NO_GROW = {
  ".brand": "the lean nav sets its links on a row just under the mark",
  ".term__preset": "the homepage demo's preset chips wrap with an 8px gap",
  ".home-spectrum__foot a": "two links stacked 5px apart at 320",
  ".company__links a": "the card links wrap onto tight rows",
  ".wj__chip": "the Wave jobs filter chips wrap with a small gap, over the slot select",
  ".dk__spine": "the Playbox tape spines stack edge to edge",
  ".pg-mode": "the Playbox deck switch stacks its modes",
  ".reel__mute": "sits on the reel screen, itself a control",
  ".code-block__copy": "beside the code line; a grown area would cover the code text",
};
test("touch: small controls keep their look and grow an invisible 44px hit area", () => {
  assert.match(css("tokens.css"), /--hit: 44px;/);
  assert.match(css("tokens.css"), /--hit-inset: min\(0px, calc\(\(100% - var\(--hit\)\) \/ 2\)\);/);
  const sheets = readdirSync(path.join(WEB, "src/styles")).filter((f) => f.endsWith(".css"));
  const rules = sheets.flatMap((f) => [...coarseBlocks(css(f)).matchAll(/([^{}]+)\{[^}]*content: "";[^}]*position: absolute;[^}]*inset: var\(--hit-inset\)/g)].map((m) => m[1]));
  const grown = rules.flatMap((sel) => sel.split(/,(?![^(]*\))/).flatMap((part) => {
    const m = part.trim().match(/^(?::is\((.*)\)|(.*?))::(before|after)$/);
    return m ? (m[1] || m[2]).split(",").map((x) => x.trim()) : [];
  }));
  for (const sel of HIT_GROW) assert.ok(grown.includes(sel), `${sel} grows a hit area`);
  for (const [sel, why] of Object.entries(NO_GROW)) assert.ok(!grown.includes(sel), `${sel} must not grow a hit area: ${why}`);
  // a static control becomes the pseudo-element's containing block at zero specificity,
  // so a control a page positions (absolute, fixed) keeps its own position
  for (const f of sheets) for (const m of coarseBlocks(css(f)).matchAll(/([^{}]*)\{ position: relative; \}/g)) {
    assert.match(m[1].trim(), /^:where\(/, `${f}: the containing-block rule is zero-specificity`);
  }
});

test("touch: lists of text links get 44px rows, and form fields are 44px tall", () => {
  const c = coarseBlocks(css("components.css"));
  assert.match(c, /:is\(\.page-index__link, \.page-index__head\) \{[^}]*display: block;[^}]*padding-block: calc\(\(var\(--hit\) - 1lh\) \/ 2\)/);
  assert.match(c, /:where\(input:not\(\[type="checkbox"\], \[type="radio"\], \[type="range"\], \[type="hidden"\]\), select, textarea\) \{ min-height: var\(--hit\); \}/);
  // the contents tuner's stations grow downward only (the dial's ticks and readout are laid
  // out against the band, so a station must not become a containing block or move its numeral)
  assert.match(c, /\.toc-tuner__st \{ min-height: var\(--hit\); \}/);
});

// Item 4: the product pages' signed-out and empty states are one pattern, the account state:
// a tinted panel (the shared .tint-panel ground) in the plate, the card's mono section label
// (its h2), the page's own words, and its existing action (a Log in link) as the lead row's
// primary. Words, ids and scripts are unchanged; the rest-rollout text fixture holds the words.
const ACCOUNT_STATES = [
  { page: "keys.html", id: null, within: /<main class="card" id="gate" hidden>[\s\S]*?<\/main>/, action: true },
  { page: "usage.html", id: null, within: /<main class="card" id="gate" hidden>[\s\S]*?<\/main>/, action: true },
  { page: "stations.html", id: "stEmpty" },
  { page: "dashboard.html", id: "dashEmpty" },
  { page: "private.html", id: "gate" },
];
test("account states: one component in the account sheet, on the shared tinted ground", () => {
  const a = css("account-base.css");
  assert.match(a, /\.acct-state \{[^}]*padding:/, "the account state is placed in the plate");
  assert.doesNotMatch(a, /\.acct-state[^{]*\{[^}]*background/, "its ground is the shared .tint-panel rule, not a repaint");
  assert.match(a, /\.acct-state \.research-actions \{[^}]*margin-top:/);
});
for (const s of ACCOUNT_STATES) {
  test(`account states: ${s.page}${s.id ? " #" + s.id : " signed out"} uses the account state`, () => {
    const html = read(path.join("src", s.page));
    const scope = s.within ? html.match(s.within)?.[0] || "" : html;
    const open = s.id
      ? new RegExp(`<(section|div) class="acct-state tint-panel" id="${s.id}" hidden>`)
      : /<section class="acct-state tint-panel">/;
    assert.match(scope, open);
    if (s.action) {
      assert.match(scope, /<p class="research-actions research-actions--lead"><a class="research-button" href="\/login\.html">Log in<\/a><\/p>/);
      assert.doesNotMatch(scope, /style="/, "no inline style");
    }
  });
}

test("account states: the card's link treatment leaves the lead row's buttons alone (it out-specifies them)", () => {
  const a = css("account-base.css");
  for (const m of a.matchAll(/\.card a:not\(\.gh\)[^{]*\{/g)) assert.match(m[0], /:not\(\.research-button\)/, m[0]);
});

// Item 5: one frame for every article figure. Each <figure> in a broadcast is the shared
// plate (.figure.figure--plate): the tinted ground, one radius, one inset, the FIG. caption
// inside it under the media, left-set. A landscape lead (hero) image or loop is cropped to
// one 16:9 shape; a portrait phone lead keeps its shape (.figure--phone).
const ARTICLES = readdirSync(path.join(WEB, "src")).filter((f) => /^broadcasts-.+\.html$/.test(f));
test("articles: every figure is the shared plate", () => {
  const bad = [];
  for (const f of ARTICLES) {
    for (const [tag] of read(path.join("src", f)).matchAll(/<figure\b[^>]*>/g)) {
      const cls = (tag.match(/class="([^"]*)"/)?.[1] || "").split(/\s+/);
      if (!cls.includes("figure") || !cls.includes("figure--plate")) bad.push(`${f}: ${tag}`);
    }
  }
  assert.deepEqual(bad, []);
});
test("articles: the frames the pages drew themselves are gone (the plate is the frame)", () => {
  assert.doesNotMatch(css("broadcast-routing.css"), /\.route-process \{[^}]*(border|background|border-radius):/);
  assert.doesNotMatch(css("broadcast-economics.css"), /\.ec-(chart|formula) \{[^}]*(border|background):/);
});
test("articles: a landscape lead is one 16:9 crop; the video's FIG. label sits under it like every caption", () => {
  const b = css("broadcasts.css");
  assert.match(b, /\.bc-post__wrap > \.figure--plate:not\(\.figure--phone\) > :is\(img, video\) \{[^}]*aspect-ratio: 16 \/ 9;[^}]*object-fit: cover/);
  assert.match(b, /\.bc-video \{[^}]*display: flex;[^}]*flex-direction: column/);
  assert.match(b, /\.bc-video > \.fig \{[^}]*order: 1/);
  assert.match(b, /\.bc-video__cap \{[^}]*order: 2;[^}]*text-align: left/);
  assert.match(b, /\.bc-video__frame \{[^}]*border-radius: var\(--r-lg\)/, "the video frame takes the images' radius");
});

test("share-GPU chart: every body line fits inside its node box with room, at the chart's own 12.5 size", () => {
  const html = read("src/broadcasts-share-gpu-earn.html");
  const fs = Number(html.match(/\.d-b\{[^}]*font-size:([\d.]+)px/)[1]);
  assert.equal(fs, 12.5, "the body size is not shrunk to fit (it fell under the phone floor)");
  // the mono face as rendered advances 0.615em a glyph (measured: 223 units at 12.5px for 29
  // glyphs); each box is 220 wide with its words set 18 units in
  for (const [, x, words] of html.matchAll(/<text x="(\d+)" y="\d+" class="d-b">([^<]*)<\/text>/g)) {
    const box = [30, 295, 560].find((b) => Number(x) >= b && Number(x) < b + 220);
    if (box === undefined) continue;
    const right = Number(x) + words.replace(/&[^;]+;/g, "x").length * 0.615 * fs;
    assert.ok(right <= box + 220 - 6, `"${words}" ends at x=${right.toFixed(1)}, its box border is x=${box + 220}`);
  }
});

test("articles: a lead image's width and height say its real size (share-hero said 1280x560; it is 1280x720)", () => {
  for (const f of ARTICLES) {
    const html = read(path.join("src", f));
    for (const [, src, w, h] of html.matchAll(/<img\s+(?:class="[^"]*"\s+)?src="(assets\/broadcasts\/[^"]+\.png)" width="(\d+)" height="(\d+)"/g)) {
      const png = readFileSync(path.join(WEB, "src", src));
      assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [Number(w), Number(h)], `${f}: ${src}`);
    }
  }
});

test("a diagram box on the figure plate ends its edge shade on the plate's ground (a paper strip showed at its right end)", () => {
  assert.match(css("components.css"), /\.figure--plate \.scroll-box--diagram \{ --edge-ground: var\(--paper-2\); \}/);
});

// Every article chart (an inline SVG with words, in a broadcast) is a .figure--chart whose
// drawing sits in the diagram scroll box; the component's one phone floor keeps each chart's
// smallest label at 11px or more at 390 (on a desktop the 640px article column holds them
// whole, at ~9-10px, accepted). Smallest label per chart, in SVG units, measured.
const CHARTS = {
  "broadcasts-connect-bots-openai-api.html": [{ vbw: 820, smallest: 12 }],
  "broadcasts-jev-vs-wave.html": [{ vbw: 820, smallest: 12 }, { vbw: 820, smallest: 12 }, { vbw: 820, smallest: 11 }],
  "broadcasts-run-a-tower.html": [{ vbw: 820, smallest: 12.5 }, { vbw: 820, smallest: 12 }],
  "broadcasts-share-gpu-earn.html": [{ vbw: 820, smallest: 12.5 }],
  "broadcasts-vram-for-llm.html": [{ vbw: 820, smallest: 12 }],
  "broadcasts-what-a-million-tokens-costs.html": [{ vbw: 720, smallest: 10 }],
};
test("article charts: every one is a .figure--chart in the diagram box", () => {
  for (const f of ARTICLES) {
    const html = read(path.join("src", f));
    const figs = [...html.matchAll(/<figure\b[^>]*>[\s\S]*?<\/figure>/g)].map((m) => m[0]).filter((fig) => /<svg\b[\s\S]*?<text\b/.test(fig));
    assert.equal(figs.length, (CHARTS[f] || []).length, `${f}: the chart table lists every chart`);
    for (const fig of figs) {
      assert.match(fig, /^<figure class="[^"]*\bfigure--chart\b/, `${f}: a chart is a .figure--chart`);
      assert.match(fig, /<div class="scroll-box scroll-box--diagram">\s*<svg\b/, `${f}: its drawing is in the diagram box`);
    }
  }
});
test("article charts: the component's phone floor keeps every chart's smallest label >= 11px at 390", () => {
  const c = css("components.css");
  const floor = Number(c.match(/@media \(max-width: 640px\) \{ \.figure--chart \{ --diagram-min: (\d+)px; \} \}/)?.[1]);
  assert.ok(floor, "one phone floor for article charts, in the component");
  for (const [f, list] of Object.entries(CHARTS)) for (const d of list) {
    const need = Math.ceil((LABEL_FLOOR * d.vbw) / d.smallest);
    assert.ok(floor >= need, `${f}: floor ${floor}px < ${need}px`);
  }
  assert.doesNotMatch(c, /\.figure--chart > svg \{[^}]*min-width: 34rem/, "the old 34rem floor (labels ~8px) is gone");
});

test("no page comment still describes a primary-then-outline action row", () => {
  for (const f of readdirSync(path.join(WEB, "src")).filter((n) => n.endsWith(".html"))) {
    for (const [c] of read(path.join("src", f)).matchAll(/<!--[\s\S]*?-->/g)) {
      assert.doesNotMatch(c, /(filled|solid) primary|outline buttons/i, `${f}: ${c.slice(0, 90)}`);
    }
  }
});
