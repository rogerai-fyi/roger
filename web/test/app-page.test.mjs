// app.html - the App Store launch page. Static-content contract locks, same
// pattern as the other page tests: read the SOURCE page and assert the parts
// that must not regress silently.
//
// Locks:
//  - the page links the REAL App Store listing (id6785743752), new-tab safe
//  - the page is INDEXED (the old "tuning up" placeholder was robots=noindex)
//  - every screenshot is self-hosted under assets/app/ (CSP img-src 'self';
//    mzstatic.com would be silently blocked) and carries width/height + lazy
//  - the social card points at the page's own real PNG (scrapers skip SVG)
//  - the badge artwork is the self-hosted SVG, not an Apple-hosted URL
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "src");
const read = (p) => readFileSync(join(root, p), "utf8");
const app = read("app.html");

const APP_STORE_URL = "https://apps.apple.com/us/app/rogerai-fyi/id6785743752";

test("app.html links the real App Store listing, opener severed", () => {
  const links = [...app.matchAll(/<a\b[^>]*href="([^"]*apps\.apple\.com[^"]*)"[^>]*>/g)];
  assert.ok(links.length >= 2, "at least hero + closing CTA link to the store");
  for (const [tag, href] of links) {
    assert.ok(href.startsWith(APP_STORE_URL), `store link is the real listing: ${href}`);
    assert.match(tag, /target="_blank"/);
    assert.match(tag, /rel="noopener noreferrer"/);
  }
});

test("app.html is indexed - the tuning-up placeholder's noindex is gone", () => {
  assert.doesNotMatch(app, /robots=noindex/);
  assert.doesNotMatch(app, /tuning up/i);
});

test("screenshots are self-hosted, sized, and lazy (CSP allows img-src 'self' only)", () => {
  const imgs = [...app.matchAll(/<img\b[^>]*>/g)].map((m) => m[0]);
  assert.ok(imgs.length >= 8, `a real gallery, not a stub (got ${imgs.length})`);
  for (const img of imgs) {
    assert.match(img, /src="assets\/app\//, `self-hosted: ${img}`);
    assert.match(img, /\balt="[^"]+"/, `descriptive alt: ${img}`);
    assert.match(img, /\bwidth="\d+"/, `explicit width: ${img}`);
    assert.match(img, /\bheight="\d+"/, `explicit height: ${img}`);
  }
  // everything below the hero fold defers; only the hero trio (badge + the
  // Mac/iPhone composition) is above the fold and may load eagerly
  const lazy = imgs.filter((i) => /loading="lazy"/.test(i));
  assert.ok(imgs.length - lazy.length <= 3, "at most the hero trio loads eagerly");
  assert.doesNotMatch(app, /mzstatic\.com/, "no CSP-blocked Apple CDN images");
});

test("social card: og=post with the page's own absolute PNG", () => {
  assert.match(app, /og=post/);
  assert.match(app, /ogurl="https:\/\/rogerai\.fm\/app\.html"/);
  assert.match(app, /ogimage="https:\/\/rogerai\.fm\/assets\/app\/og-app\.png"/);
});

test("the App Store badge is the self-hosted official SVG", () => {
  assert.match(app, /src="assets\/app\/appstore-badge\.svg"/);
});

// ------- beautify-pass locks (2026-07 design evaluation) -------

test("no em dashes anywhere in the page copy (founder style rule)", () => {
  assert.doesNotMatch(app, /—/);
});

test("figure captions sit ABOVE their plates (the spec-plate treatment)", () => {
  // within each .app-shot block, the FIG. caption (when present in the next
  // 900 chars) must appear before the img tag
  const opens = [...app.matchAll(/<span class="app-shot[^"]*">/g)];
  assert.ok(opens.length >= 10, `a real gallery of mounted shots (got ${opens.length})`);
  for (const m of opens) {
    const block = app.slice(m.index, m.index + 900);
    const fig = block.indexOf('<span class="fig">');
    const img = block.indexOf("<img");
    if (fig !== -1) assert.ok(fig < img, `caption above the plate: ${block.slice(0, 120)}`);
  }
});

test("the desk-set plate is not the refusal screenshot (mac-agent argued against the pitch)", () => {
  assert.doesNotMatch(app, /mac-agent\.webp/);
});

test("the closing CTA mounts the brandlocked art, theme-swapped via CSS (light + dim dark)", () => {
  const css = read("styles/app.css");
  assert.match(css, /\.app-cta__art[\s\S]{0,400}?cta-handheld\.webp/, "light art wired in app.css");
  assert.match(css, /\[data-theme="dark"\][^{]*\.app-cta__art[\s\S]{0,200}?cta-handheld-dark\.webp/, "dim dark variant wired");
  assert.match(app, /class="app-cta__art"/, "the CTA carries the art mount");
  for (const f of ["assets/app/cta-handheld.webp", "assets/app/cta-handheld-dark.webp"])
    assert.ok(existsSync(join(root, f)), `${f} exists`);
});

test("each numbered section carries a dial-rule divider with its own needle position", () => {
  const numbered = [...app.matchAll(/class="sectionno">§\d+/g)];
  const rules = [...app.matchAll(/class="app-dialrule"[^>]*style="--dial-pos:\s*(\d+)%"/g)];
  assert.ok(numbered.length >= 9, `the page keeps its numbered sections (got ${numbered.length})`);
  // ONE rule per numbered section - exact, not ">= 7": a new numbered section (the §10
  // install outro) that forgot its needle used to pass silently against the old floor.
  assert.equal(rules.length, numbered.length,
    `one dial-rule per numbered section: ${numbered.length} sections, ${rules.length} rules`);
  // and the needles sweep strictly left-to-right, ending at the edge of the band.
  const pos = rules.map((m) => parseInt(m[1], 10));
  for (let i = 1; i < pos.length; i++) {
    assert.ok(pos[i] > pos[i - 1], `needle ${i} at ${pos[i]}% must sit right of ${pos[i - 1]}%`);
  }
  assert.equal(pos[pos.length - 1], 100, "the last needle reaches the end of the band (100%)");
});

// §10 INSTALL - the page describes the terminal (§8) and the browser console (§9) but must
// also tell you how to GET the roger binary, in the copy-button component the rest of the
// site uses, positioned before the closing App Store CTA (its last image).
test("app.html ships the roger install command in a copy box, before the closing CTA", () => {
  // the install__box copy component (site.js wires copy-to-clipboard for every .install__box)
  const box = app.match(/<button class="install__box"[\s\S]*?<\/button>/);
  assert.ok(box, "the install command sits in an install__box copy button");
  assert.match(box[0], /curl -fsSL https:\/\/rogerai\.fm\/install\.sh \| sh/,
    "the one-line installer is the canonical rogerai.fm/install.sh");
  assert.match(box[0], /<code class="install__code">/, "the code is in install__code so site.js copies it");

  // the Windows path and a raw-binary fallback, same as the homepage
  assert.match(app, /install\.ps1 \| iex/, "the Windows PowerShell installer is offered");
  assert.match(app, /github\.com\/rogerai-fyi\/roger\/releases/, "a download-a-binary fallback is offered");

  // The "On Windows" note MUST carry data-alt-note, or site.js's class-based flip skips it
  // and a Windows visitor sees the PowerShell command twice with no macOS/Linux fallback.
  const winNote = app.match(/<span[^>]*>On Windows \(PowerShell\):/);
  assert.ok(winNote, "the install section has an On Windows note");
  assert.match(winNote[0], /data-alt-note/,
    "the On Windows note carries data-alt-note so site.js flips it to the macOS/Linux one-liner");

  // position: the install section must come BEFORE the closing CTA (the last image)
  const iInstall = app.indexOf('class="section app-install"');
  const iCta = app.indexOf('class="section app-cta"');
  assert.ok(iInstall > 0 && iCta > 0, "both the install and CTA sections exist");
  assert.ok(iInstall < iCta, "install instructions come before the closing App Store CTA");
});
