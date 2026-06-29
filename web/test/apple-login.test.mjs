// Regression lock for web Sign in with Apple (the browser flow on rogerai.fyi).
// Unlike the NATIVE bind (/auth/apple, device Ed25519 signed), the web flow is a
// SERVER-SIDE redirect handled entirely by the broker (apple_web.go:
//   GET /auth/apple/web/login  -> 302 Apple authorize (Services ID, state+nonce)
//   POST /auth/apple/web/callback -> verify id_token -> setWebSessionWallet -> dashboard
// ) - the exact analogue of the GitHub web OAuth link in login.html. So the front
// end is JUST a link button: no Apple JS SDK, no popup, no client_id in the page
// (the broker holds the Services ID server-side). These assertions pin that minimal
// shape so it can't regress back into a heavier AppleID.auth popup approach.
// Static-content assertions over web/src (no build/DOM), matching the node:fs infra.
// Run: node --test test/apple-login.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");
const login = read("login.html");

test("login: a 'Continue with Apple' link beside GitHub, pointing at the broker web flow", () => {
  // both providers offered, same .gh ink plate.
  assert.match(login, /href="https:\/\/broker\.rogerai\.fyi\/auth\/github\/login"/, "GitHub link still present");
  assert.match(login, /href="https:\/\/broker\.rogerai\.fyi\/auth\/apple\/web\/login"/, "Apple link points at the broker web-login route");
  // it's a labelled <a class="gh"> like GitHub (full-page navigation, not a button+JS).
  assert.match(
    login,
    /<a class="gh"[^>]*href="https:\/\/broker\.rogerai\.fyi\/auth\/apple\/web\/login"[\s\S]*?Apple[\s\S]*?<\/a>/,
    "Apple is an <a class=gh> link labelled Apple",
  );
});

test("login: web Apple is the minimal redirect flow - NO JS SDK / popup / client_id in the page", () => {
  // the broker holds the Services ID; the page must NOT embed it or the Apple JS SDK.
  // locks out a regression back to the heavier AppleID.auth popup the first pass built.
  assert.doesNotMatch(login, /appleid\.cdn-apple\.com/, "no Apple JS SDK script");
  assert.doesNotMatch(login, /AppleID\.auth/, "no AppleID.auth popup init");
  assert.doesNotMatch(login, /appleid-signin-client-id/, "no client_id meta in the page (broker-side)");
  assert.doesNotMatch(login, /apple-signin\.js/, "no apple-signin.js include");
});
