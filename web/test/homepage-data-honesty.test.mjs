// Executable behavior lock for the live-versus-illustrative homepage slice in
// features/web/homepage_company_research_branding.feature.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const home = () => readFileSync(path.join(DIST, "index.html"), "utf8");
const compact = (value) => value.replace(/\s+/g, " ");

test("the current market is explicitly live and keeps a retry plus directory path", () => {
  const section = home().match(/<section class="market"[\s\S]*?<\/section>/)?.[0] || "";
  assert.match(section, /live/i);
  assert.match(section, /id="marketRefresh"/);
  assert.match(section, /href="\/models\.html"/);
  assert.match(section, /aria-live="polite"/);
});

test("without JavaScript the market is an honest unloaded state, not fake activity", () => {
  const section = home().match(/<section class="market"[\s\S]*?<\/section>/)?.[0] || "";
  assert.match(section, /Live data loads when JavaScript is available/i);
  assert.match(section, /No live data loaded yet/i);
  assert.doesNotMatch(section, /class="mkt-skel"/);
  assert.doesNotMatch(section, /tuning in to the broker/i);
  assert.doesNotMatch(section, /debited live/i);
});

test("the hard-coded tuner is a dated historical interface sample, never a live read", () => {
  const hero = home().match(/<section class="hero">[\s\S]*?<\/section>/)?.[0] || "";
  const teaser = hero.match(/<a class="teaser"[\s\S]*?<\/a>/)?.[0] || "";
  assert.match(compact(teaser), /historical interface sample/i);
  /* AMENDED 2026-08-18: the sample's model list was refreshed to models people
     actually run now, so the date it was authored moved with it. The guarantee
     is the date itself - a dated, explicitly not-live sample cannot be mistaken
     for the live dial - not any particular month. */
  assert.match(compact(teaser), /authored August 2026/i);
  assert.match(compact(teaser), /not live/i);
  assert.match(teaser, /href="\/models\.html"/);
});

test("the earnings forecast is visibly illustrative and reproducible", () => {
  const panel = home().match(/<aside class="earn"[\s\S]*?<\/aside>/)?.[0] || "";
  assert.match(panel, /ILLUSTRATIVE/i);
  assert.doesNotMatch(panel, />\s*ON AIR\s*</i);
  assert.match(compact(panel), /rate[^<]*<\/span><b[^>]*>\$0\.30 \/ 1M out/i);
  assert.match(compact(panel), /volume[^<]*<\/span><b[^>]*>10M - 30M out \/ day/i);
  assert.match(compact(panel), /uptime[^<]*<\/span><b[^>]*>50%/i);
  assert.match(compact(panel), /your share[^<]*<\/span><b[^>]*>70%/i);
  assert.match(compact(panel), /\$1\.05 - \$3\.15/i);
  assert.match(compact(panel), /rate × volume × uptime × share/i);
  assert.match(compact(panel), /not guaranteed/i);
});

test("the illustrative earnings range matches its visible assumptions", () => {
  const panel = home().match(/<aside class="earn"[\s\S]*?<\/aside>/)?.[0] || "";
  const attrs = panel.match(/data-rate="([^"]+)"\s+data-volume-min="([^"]+)"\s+data-volume-max="([^"]+)"\s+data-uptime="([^"]+)"\s+data-share="([^"]+)"/);
  assert.ok(attrs, "machine-readable assumptions stay beside the visible forecast");
  const [, rate, minVolume, maxVolume, uptime, share] = attrs.map(Number);
  const low = rate * minVolume * uptime * share / 1e6;
  const high = rate * maxVolume * uptime * share / 1e6;
  assert.match(panel, new RegExp(`\\$${low.toFixed(2)} - \\$${high.toFixed(2)}`));
});
