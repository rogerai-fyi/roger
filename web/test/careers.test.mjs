// Executable lock for features/web/careers.feature.
//
// The hiring page is a credibility surface as much as a recruiting one: a lab
// with no "join us" reads as one person with a website. It is also the easiest
// page on which to overclaim - headcount, funding, salary bands, an ATS that
// does not exist - so most of these assertions are about what it must NOT say.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

test("careers is reachable from the footer and the company page", () => {
  const footer = read("index.html").match(/<footer[\s\S]*?<\/footer>/)[0];
  assert.match(footer, /href="\/careers\.html"[^>]*>Careers<\/a>/);
  assert.match(read("company.html"), /href="\/careers\.html"/);
});

test("the page says what kind of place this is before it lists jobs", () => {
  const copy = visible(read("careers.html"));
  assert.match(copy, /American/);
  assert.match(copy, /Orange County|California/);
  assert.match(copy, /open|publish/i, "the work is published in the open");
});

test("the page is honest about stage and claims no traction", () => {
  const copy = visible(read("careers.html"));
  assert.match(copy, /small|early/i, "stage is stated plainly");
  assert.doesNotMatch(copy, /\b\d+\+? (employees|people on staff|engineers on staff)\b/i);
  assert.doesNotMatch(copy, /Series [A-D]\b|seed round|raised \$|\bvaluation\b/i);
  assert.doesNotMatch(copy, /trusted by|our customers include/i);
});

test("roles are grouped by the work and each names its problem and stack", () => {
  const page = read("careers.html");
  const copy = visible(page);
  for (const group of [/research/i, /engineering/i, /industrial|deployment/i]) {
    assert.match(copy, group);
  }
  const roles = [...page.matchAll(/<article class="role"[\s\S]*?<\/article>/g)].map((m) => m[0]);
  assert.ok(roles.length >= 3, `expected at least 3 roles, found ${roles.length}`);
  for (const role of roles) {
    const t = visible(role);
    assert.match(role, /<h3/, "role has a title");
    // Length of the DESCRIPTION, not a run of non-space characters - prose has spaces.
    assert.ok(t.trim().length > 200, `role explains the work rather than just naming it (${t.trim().length} chars)`);
    assert.match(role, /class="role__meta"/, "role carries location and shape");
    assert.match(role, /mailto:/, "role links a way to apply");
  }
});

test("an open application exists for people who fit no listed role", () => {
  const copy = visible(read("careers.html"));
  assert.match(copy, /open application|none of these|does not fit/i);
  assert.match(copy, /include/i, "it says what to send");
});

// The most tempting page on the site to overclaim on.
test("no compensation or equity figure is invented", () => {
  const copy = visible(read("careers.html"));
  assert.doesNotMatch(copy, /\$\s?\d{2,3},\d{3}/, "no salary band we have not decided");
  assert.doesNotMatch(copy, /\b\d+(\.\d+)?%\s*(equity|options)/i);
  assert.doesNotMatch(copy, /\bequity\b/i, "no equity claim from an unincorporated entity");
});

// Same rule the company page carries: a dead contact is worse than one fewer.
test("every application address is a mailbox that exists", () => {
  const page = read("careers.html");
  const boxes = [...page.matchAll(/mailto:([a-z]+)@rogerai\.fm/g)].map((m) => m[1]);
  assert.ok(boxes.length > 0, "there is a way to apply");
  // ROUTES mirrors REAL mail routing. It is not a formatting whitelist: adding a name here
  // without provisioning the alias makes this guard certify its own assumption, and the
  // page ships a dead CTA that looks tested. Widening it is a deliberate ops step.
  const ROUTES = ["abuse", "billing", "confidential", "labs", "legal", "privacy", "security"];
  for (const b of boxes) assert.ok(ROUTES.includes(b), `mailto:${b}@ is a mailbox that exists`);
});

test("no form posts to an endpoint that is not implemented", () => {
  const page = read("careers.html");
  assert.doesNotMatch(page, /<form[^>]+action=/i, "no form posts anywhere - application is by email");
});

test("rolling roles say so, and the page is static", () => {
  const page = read("careers.html");
  assert.match(visible(page), /rolling|ongoing|on an ongoing basis/i);
  const main = page.slice(page.indexOf("<main"), page.indexOf("</main>"));
  const noScripts = main.replace(/<script[\s\S]*?<\/script>/g, "");
  assert.match(noScripts, /mailto:/, "the application route survives with scripting disabled");
  assert.match(noScripts, /<h3/, "roles are server-rendered");
});

test("careers is indexable and in the sitemap", () => {
  assert.doesNotMatch(read("careers.html"), /<meta[^>]+name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), /careers\.html/);
});

// A reader who copies the address off the page and a reader who clicks it must reach the
// same mailbox. They did not: the open-application link read labs@ while pointing at careers@.
test("every visible address matches the mailbox it links to", () => {
  const page = read("careers.html");
  const links = [...page.matchAll(/<a\b[^>]*href="mailto:([^"?]+)[^"]*"[^>]*>([^<]+)<\/a>/g)];
  assert.ok(links.length > 0, "the page links at least one mailbox");
  for (const [, href, text] of links) {
    if (!text.includes("@")) continue; // "Apply by email" style labels carry no address
    assert.equal(text.trim(), href, "the visible address must be the one the link opens");
  }
});
