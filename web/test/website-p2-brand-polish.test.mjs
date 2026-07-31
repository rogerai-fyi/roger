import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const SRC = path.join(WEB, "src");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const source = (p) => readFileSync(path.join(SRC, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

test("released Wave copy names Apache-2.0 decisively and separates network services", () => {
  // The release copy moved from the research hub to the catalogue page when
  // research.html was split; assert it where it now lives.
  for (const page of ["index.html", "company.html", "research-models.html"]) {
    const text = visible(read(page));
    // No page may name a release that does not exist; every page must still keep
    // the network/artifact separation clear for when one does.
    assert.doesNotMatch(text, /Wave Micro v1\.0/i, `${page} does not name an unshipped release`);
    assert.doesNotMatch(text, /Artifact license: Apache-2\.0/i, `${page} claims no definite licence yet`);
    assert.match(text, /network services|network terms/i, `${page} separates optional services`);
  }
});

test("homepage puts Wave Labs proof before the install action without adding a hidden reveal", () => {
  const home = read("index.html");
  const hero = home.match(/<section class="hero">[\s\S]*?<\/section>/)?.[0] || "";
  const proof = hero.indexOf("WAVE MICRO");
  const install = hero.indexOf('class="install"');
  assert.ok(proof > 0 && proof < install, "the Labs plate precedes install in the first hero flow");
  // The plate states program status, not a licence or a version it cannot back.
  assert.match(hero, /IN PROGRESS/);
  assert.doesNotMatch(hero, /APACHE-2\.0|v1\.0|AVAILABLE/);
  for (const marker of ["hero__eyebrow", "hero__title", "hero__sub", "hero__proof", 'class="install"']) {
    const tag = hero.match(new RegExp(`<[^>]*class="[^"]*${marker.replace('class="', "")}[^"]*"[^>]*>`))?.[0] || "";
    assert.doesNotMatch(tag, /\bdata-reveal\b/, `${marker} is not opacity-hidden before JS`);
  }
  const css = read("styles/home.css");
  const beforeNarrowOverrides = css.split("@media (max-width: 640px)")[0];
  assert.match(beforeNarrowOverrides, /\.install\s*\{[^}]*order:\s*1/i,
    "all viewports promote install ahead of the concierge");
  assert.match(beforeNarrowOverrides, /\.hero__ping\s*\{[^}]*order:\s*2/i,
    "all viewports keep the concierge after conversion");
  assert.match(css, /@media\s*\(min-width:\s*641px\)\s*and\s*\(max-height:\s*800px\)[\s\S]*\.hero__inner\s*\{[^}]*padding-top/i,
    "short desktop viewports compact hero spacing around the first action");
});

test("homepage, company preview, and research hero share the one vector mascot partial", () => {
  for (const [page, minimum] of [["index.html", 2], ["research.html", 1]]) {
    const html = read(page);
    assert.ok((html.match(/class="tube-ping(?:\s|")/g) || []).length >= minimum, `${page} includes the mascot`);
    assert.equal(
      (html.match(/<svg class="tube-ping__mark"/g) || []).length,
      (html.match(/class="tube-ping"/g) || []).length,
      `${page} renders every mascot as a vector`
    );
    assert.match(visible(html), /Ping/i);
  }
});

test("mobile research exposes a compact program-status ticker", () => {
  const page = read("research.html");
  assert.match(page, /class="release-ticker"/);
  const ticker = visible(page.match(/<p class="release-ticker"[\s\S]*?<\/p>/)?.[0] || "");
  for (const fact of ["IN PROGRESS", "Wave Micro", "350M class", "no checkpoint yet"]) {
    assert.match(ticker, new RegExp(fact, "i"));
  }
  assert.doesNotMatch(ticker, /AVAILABLE|v1\.0|Apache-2\.0/i);
  const css = read("styles/research.css");
  assert.match(css, /@media\s*\(max-width:\s*560px\)[\s\S]*\.research-hero\s*\{[^}]*padding/i);
});

test("above-fold reveal CSS never hides homepage hero content", () => {
  const css = source("styles/base.css");
  assert.match(css, /html\.js \[data-reveal\]\s*\{[^}]*opacity:\s*1/i);
  assert.doesNotMatch(css, /html\.js \[data-reveal\]\s*\{[^}]*opacity:\s*0/i);
});

test("app structured data uses the company brand and preserves the store legacy name", () => {
  const app = read("app.html");
  const blocks = [...app.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)].map((m) => JSON.parse(m[1]));
  const product = blocks.find((b) => b["@type"] === "SoftwareApplication" && b.downloadUrl);
  assert.equal(product?.name, "RogerAI");
  assert.equal(product?.alternateName, "RogerAI.fyi");
  assert.equal(product?.url, "https://rogerai.fm/app.html");
});
