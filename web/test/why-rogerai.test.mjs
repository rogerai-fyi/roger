// Executable lock for features/web/why_rogerai.feature.
//
// This page argues for a FEE, which makes it the page most likely to drift into a
// promise. The locks are honesty-shaped: the anonymization claim must carry its own
// limit (prompt content still reaches the model's operator), the comparison must not
// hide the self-host exit, and the page must never claim prompt privacy on curated
// bands - identity unlinking is the claim the architecture enforces, and the only one.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<style[\s\S]*?<\/style>/g, " ")
  .replace(/<script[\s\S]*?<\/script>/g, " ").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const page = () => read("why.html");
const copy = () => visible(page().match(/<main[\s\S]*?<\/main>/)[0]);

test("the page exists, is indexed, and pricing points at it", () => {
  assert.ok(existsSync(path.join(DIST, "why.html")), "the page builds");
  assert.match(read("sitemap.xml"), /why\.html/, "it is in the sitemap");
  assert.match(read("pricing.html"), /href="\/why\.html"/, "pricing links the why");
});

test("anonymization is claimed exactly as far as the code enforces it", () => {
  const c = copy();
  assert.match(c, /sees one caller|sees a station/i, "the unlinking claim is stated");
  assert.match(c, /cannot tell our consumers apart/i, "cross-consumer indistinguishability");
  assert.match(c, /prompt content.{0,40}still reaches/i,
    "the limit is stated in the same breath: content reaches the model's operator");
  // The page may only use "private"/"anonymous" about identity and self-hosted planes,
  // never about prompt content on curated bands - so the phrase that would oversell
  // ("your prompts are private/anonymous") must not appear.
  assert.doesNotMatch(c, /prompts? (are|stay|remain) (private|anonymous)/i,
    "no prompt-privacy claim the architecture does not enforce");
});

test("the comparison keeps humans and curated apart and names the fee's job", () => {
  const c = copy();
  assert.match(c, /labeled apart/i, "curated supply is presented apart from human supply");
  assert.match(c, /counter-sign/i, "the receipt claim is the enforceable one");
  assert.match(c, /max(imum)? price rides every request/i, "price protection stated as mechanism");
  assert.match(c, /go direct/i, "the honest close: when to skip us is on the page");
});

test("self-hosting is a first-class exit", () => {
  const c = copy();
  assert.match(c, /standalone Tower/i, "the tower exit is explained");
  assert.match(page(), /href="\/broadcasts-run-a-tower\.html"/, "and the quickstart is linked");
  assert.match(c, /free/i, "and it says the local plane is free");
});
