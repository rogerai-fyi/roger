// Executable lock for features/web/lets_talk.feature.
//
// The contact dialog is a STATIC affordance: an accessible modal that composes a
// prefilled mailto to labs@rogerai.fm. There is no form backend, so the assertions
// here are mostly about honesty - a real mailto fallback, no action/POST anywhere,
// and the billing-help accessibility contract (trap, Esc, restore).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const PAGES = ["company.html", "research-models.html"];

test("lets-talk: the trigger is a real mailto link on both pages (no-JS honest)", () => {
  for (const page of PAGES) {
    const html = read(page);
    assert.match(html, /<a[^>]*data-lets-talk[^>]*href="mailto:labs@rogerai\.fm[^"]*"/,
      `${page} offers the trigger as a plain mailto fallback`);
    assert.match(html, /Let&rsquo;s talk|Let's talk/i, `${page} labels it`);
  }
});

test("lets-talk: the dialog is an accessible modal with the billing-help contract", () => {
  for (const page of PAGES) {
    const html = read(page);
    const dlg = html.match(/<div[^>]*id="letsTalk"[\s\S]*?aria-modal="true"[\s\S]*?<\/form>/)?.[0];
    assert.ok(dlg, `${page} carries the dialog markup`);
    assert.match(dlg, /role="dialog"/);
    assert.match(dlg, /aria-labelledby="letsTalkTitle"/);
    assert.match(html, /js\/lets-talk\.js/, `${page} loads the behavior`);
  }
  const js = src("js/lets-talk.js");
  for (const contract of [/keydown/, /Escape|Esc/, /focus/, /aria-expanded/]) {
    assert.match(js, contract, `lets-talk.js honors ${contract}`);
  }
});

test("lets-talk: send composes a mailto, never a POST", () => {
  const partial = src("_partials/lets-talk.html");
  assert.doesNotMatch(partial, /<form[^>]*action=/, "the form has no action endpoint");
  assert.doesNotMatch(partial, /type="submit"/, "no submit button - Send is a plain button");
  const js = src("js/lets-talk.js");
  assert.match(js, /mailto:labs@rogerai\.fm/, "send targets the labs mailbox");
  assert.doesNotMatch(js, /fetch\(|XMLHttpRequest|\.submit\(\)/, "nothing is transmitted by us");
  assert.match(js, /encodeURIComponent/, "subject and body are encoded");
});

test("lets-talk: topics prefill starter notes, free of em dashes", () => {
  const js = src("js/lets-talk.js");
  const partial = src("_partials/lets-talk.html");
  assert.match(partial, /<select[^>]*id="ltTopic"/, "a topic select exists");
  assert.ok((partial.match(/<option/g) || []).length >= 4, "with a real set of topics");
  assert.doesNotMatch(js + partial, /—/, "no em dash in any starter note or copy");
});

test("lets-talk: the defining stylesheet ships on every page using the dialog", () => {
  // base.css is the site-wide sheet, so a page cannot bundle the dialog unstyled.
  assert.match(src("styles/base.css"), /\.lt-modal/, "the dialog is styled in base.css");
  for (const page of PAGES) {
    assert.match(read(page), /styles\/site\.css|styles\/base\.css|<style/,
      `${page} carries a stylesheet that includes the dialog rules`);
  }
});
