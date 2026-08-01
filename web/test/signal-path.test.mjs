// The signal path. It was FIG.3 on the homepage and now opens /tower.html - the page
// that exists to explain routing, metering and receipts, where the figure only has to
// carry the tower rather than the whole company.
//
// The figure makes three factual claims about how RogerAI works - the endpoint is
// local, the broker meters and signs, and the station dials out rather than opening
// a port. Those are the reasons the diagram exists, so they are asserted rather than
// left to survive a copy edit.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const figure = () => read("tower.html").match(/<figure class="signal"[\s\S]*?<\/figure>/)?.[0] || "";

test("the signal path shows the three hops a request actually takes", () => {
  const fig = figure();
  assert.ok(fig, "the signal path is on the tower page");
  const copy = visible(fig);
  // The middle node is labelled TOWER now, with "the broker" beneath it - the figure
  // reconciles the two names in place rather than making a reader carry the mapping.
  for (const node of [/YOUR APP/, /TOWER/, /STATION/]) assert.match(copy, node);
  assert.match(copy, /the broker/, "the older name rides along as subtext");
  // It must not be left behind on the homepage as well: one figure, one home.
  assert.doesNotMatch(read("index.html"), /<figure class="signal"/, "the homepage no longer carries it");
  // Word-boundary: "signal__nodes" is the container, not a hop.
  assert.equal((fig.match(/class="signal__node["\s]/g) || []).length, 3, "exactly three hops");
});

test("the figure states the claims it exists to make", () => {
  const copy = visible(figure());
  assert.match(copy, /127\.0\.0\.1/, "the endpoint is local");
  assert.match(copy, /OpenAI-compatible/);
  assert.match(copy, /meters/i);
  assert.match(copy, /signs|signed receipt/i);
  assert.match(copy, /no inbound port/i, "the station dials out");
});

// The bug this figure actually shipped with. offset-path is undefined across subpaths:
// a packet given "M175 118 H285 M440 118 H545" rendered somewhere else entirely. One
// packet per leg, one subpath each.
test("every packet rides a single-subpath offset-path", () => {
  const css = src("styles/tower.css");
  const paths = [...css.matchAll(/offset-path:\s*path\("([^"]+)"\)/g)].map((m) => m[1]);
  assert.ok(paths.length >= 3, `expected a path per leg, found ${paths.length}`);
  for (const d of paths) {
    const moves = (d.match(/M/gi) || []).length;
    assert.equal(moves, 1, `offset-path has one subpath, got ${moves}: ${d}`);
  }
});

// The diagram must survive frozen. If it only reads while animating, the animation is
// carrying meaning that belongs in the markup.
test("the figure is legible with motion disabled", () => {
  const css = src("styles/tower.css");
  const reduced = css.slice(css.indexOf(".signal"));
  assert.match(reduced, /prefers-reduced-motion[\s\S]*signal__pkt[\s\S]*animation:\s*none/i);
  // Nothing that carries meaning may live only in an animation.
  const fig = figure();
  assert.match(fig, /request/i);
  assert.match(fig, /routed/i);
  assert.match(fig, /tokens \+ signed receipt/i);
});

test("the figure is described for screen readers, not just drawn", () => {
  const fig = figure();
  assert.match(fig, /aria-labelledby="signal-caption"/);
  assert.match(fig, /id="signal-caption"/);
  assert.match(visible(fig), /\S{200,}|.{200,}/, "the caption explains the path in prose");
  // Decorative layers must not be announced.
  for (const layer of ["signal__wire", "signal__packets", "signal__labels"]) {
    const g = fig.match(new RegExp(`<g class="${layer}"[^>]*>`))?.[0] || "";
    assert.match(g, /aria-hidden="true"/, `${layer} is decorative`);
  }
});
