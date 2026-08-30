// The size rail's tier labels must not sit on top of each other.
//
// FOUND 2026-08-30. The figure's own note concluded "the closest neighbours (Peta/Exa)
// sit 46px apart on the 860px track - one row, no stagger". The 46px was right, and the
// conclusion did not follow: 46px is the spacing of the DOTS, while a .wf-node LABEL is
// 58px wide. 58 > 46, so the markers cleared and the words overlapped - Peta/Exa by
// 12.4px and Tera/Peta by 6.4px, on desktop as well as mobile (the track is pinned to
// its 860px min-width at both). .wf-node--up existed to drop the middle of three exactly
// here and had been left applied to nothing: a dead rule and a live collision.
//
// So this asserts the ARITHMETIC rather than the one marker that happened to be wrong.
// Any two neighbours closer than a label is wide must be staggered onto different rows,
// which keeps a future re-spacing of the ladder honest.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const html = readFileSync(path.join(WEB, "src/research-wave-family.html"), "utf8");
const css = readFileSync(path.join(WEB, "src/styles/wave-family.css"), "utf8");

// Geometry read from the source, so the test tracks the design rather than a snapshot.
const trackW = Number(css.match(/\.wf-rail__track\s*\{[^}]*min-width:\s*(\d+)px/)[1]);
const labelW = Number(css.match(/\.wf-node\s*\{[^}]*width:\s*(\d+)px/)[1]);

// Every sized marker on the rail, in rail order, with whether it is staggered.
const nodes = [...html.matchAll(/<span class="wf-node([^"]*)" style="--at:([\d.]+)%"><i><\/i><b>([^<]+)<\/b>/g)]
  .map(([, cls, at, name]) => ({
    name,
    at: Number(at),
    x: (Number(at) / 100) * trackW,
    staggered: /wf-node--up/.test(cls),
  }))
  .sort((a, b) => a.x - b.x);

test("the rail's geometry is read, not assumed", () => {
  assert.equal(trackW, 860, "the track's min-width is what pins the spacing at every viewport");
  assert.equal(labelW, 58, "a tier label's box is what actually collides, not its dot");
  assert.equal(nodes.length, 7, "the ladder is the seven sized tiers");
});

test("no two tier labels share a row while overlapping", () => {
  // EVERY pair that shares a row, not just sorted neighbours: staggering the middle of
  // three tightly packed nodes leaves the outer two alone on the top row, and if THOSE
  // two are closer than a label they still collide. Checking neighbours only would call
  // that arrangement clean.
  const bad = [];
  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      const a = nodes[i], b = nodes[j];
      if (a.staggered !== b.staggered) continue;   // different rows, cannot collide
      const gap = Math.abs(b.x - a.x);
      if (gap >= labelW) continue;                 // far enough apart to share a row
      bad.push(`${a.name}/${b.name} are ${gap.toFixed(1)}px apart but a label is ` +
               `${labelW}px wide, and they share a row - they overlap by ` +
               `${(labelW - gap).toFixed(1)}px`);
    }
  }
  assert.deepEqual(bad, [], bad.join("; "));
});

test("the stagger is spent only where it is needed", () => {
  // A staggered marker that is not actually crowded is a row-drop for nothing, and the
  // rail reads as noise. Peta is the middle of the tight top three; nothing else should
  // be carrying the class.
  for (const n of nodes.filter((n) => n.staggered)) {
    const i = nodes.indexOf(n);
    const near = [nodes[i - 1], nodes[i + 1]].filter(Boolean)
      .some((o) => Math.abs(o.x - n.x) < labelW);
    assert.ok(near, `${n.name} is staggered but has no neighbour within ${labelW}px`);
  }
});
