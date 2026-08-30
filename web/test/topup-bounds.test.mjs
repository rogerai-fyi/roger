// The top-up bounds are ONE pair of numbers that live in Go
// (internal/client/topup_amount.go). Every other surface restates them - the billing
// page's input attributes, its click handler, the local console's - and a restatement
// drifts. This reads the constants out of the Go source and requires the web surfaces to
// agree, so a change to the policy fails here instead of shipping a page that promises
// one number while the broker enforces another.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const REPO = path.join(WEB, "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// Returned as a NUMBER: Go writes the floor as 1.0 and the HTML attribute as "1", which
// are the same bound and different strings.
const goConst = (name) => {
  const go = readFileSync(path.join(REPO, "internal", "client", "topup_amount.go"), "utf8");
  const m = go.match(new RegExp(`const ${name} = ([0-9.]+)`));
  assert.ok(m, `${name} is declared in internal/client/topup_amount.go`);
  return Number(m[1]);
};

test("topup bounds: the billing input allows exactly the range the broker allows", () => {
  const input = read("billing.html").match(/<input id="topupCustom"[^>]*>/)?.[0];
  assert.ok(input, "the custom-amount input is there");
  const attr = (name) => Number(input.match(new RegExp(`${name}="([0-9.]+)"`))?.[1]);
  assert.equal(attr("min"), goConst("MinTopupUSD"), "the floor matches MinTopupUSD");
  assert.equal(attr("max"), goConst("MaxTopupUSD"), "the ceiling matches MaxTopupUSD");
  // Cents are allowed, so the step cannot be whole dollars.
  assert.match(input, /step="0\.01"/, "the input accepts the cents the broker accepts");
});

test("topup bounds: the billing script guards both ends and whole cents", () => {
  const js = src("js/billing.js");
  assert.ok(js.includes(String(goConst("MaxTopupUSD"))), "the script knows the same ceiling");
  assert.match(js, /whole number of cents|Whole cents only/i, "and refuses a sub-cent amount");
});

test("topup bounds: the local console guards the same three things", () => {
  const js = readFileSync(path.join(REPO, "internal", "webui", "assets", "console.js"), "utf8");
  const topup = js.match(/function topup\(\)[\s\S]*?\n  \}/)?.[0] || "";
  assert.ok(topup, "the console has a topup handler");
  assert.match(topup, /minimum is \$1/i, "floor");
  assert.ok(topup.includes(String(goConst("MaxTopupUSD"))), "ceiling");
  assert.match(topup, /Whole cents only/i, "whole cents");
});
