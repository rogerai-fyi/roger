// A brand-new customer's dashboard must say what to do next, show their balance, and name
// the sign-in they used - all from data /account already returned (no extra request).
// Run: node --test test/dashboard-states.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle, Fmt } from "./_pagevm.mjs";

const acct = (o) => ({ status: 200, body: Object.assign({ github_login: "me@rogerai.fm", github_id: 0, provider: "email", balance: 5 }, o) });
const EMPTY_SERIES = { status: 200, body: { daily: [], is_consumer: true, is_provider: false, savings: {} } };

test("empty dashboard: shows the balance and the next steps, no extra requests", async () => {
  const p = page("/dashboard.html", { "/account": acct(), "/metrics/series": EMPTY_SERIES });
  p.run("js/dashboard.js");
  await settle();
  assert.equal(p.els.dashEmpty.hidden, false);
  assert.equal(p.els.emptyBalance.textContent, "$5.00");
  assert.equal(p.els.earnCta.hidden, false, "a new user is told how to earn too");
  assert.deepEqual(p.fetched.map((u) => u.split("?")[0]).sort(), ["/account", "/metrics/series"], "no new requests");
});

test("empty dashboard for someone who already earns does not push the earn CTA", async () => {
  const p = page("/dashboard.html", { "/account": acct(), "/metrics/series": { status: 200, body: { daily: [], is_provider: true, savings: {} } } });
  p.run("js/dashboard.js");
  await settle();
  assert.equal(p.els.earnCta.hidden, true);
});

test("the signed-in line names the provider", async () => {
  const p = page("/dashboard.html", { "/account": acct(), "/metrics/series": EMPTY_SERIES });
  p.run("js/dashboard.js");
  await settle();
  assert.equal(p.els.who.textContent, "me@rogerai.fm");
});

test("RogerFmt.who: provider-aware identity", () => {
  assert.equal(Fmt.who({ provider: "github", github_login: "octocat" }), "@octocat (GitHub)");
  assert.equal(Fmt.who({ provider: "apple", github_login: "a@b.com" }), "a@b.com (Apple)");
  assert.equal(Fmt.who({ provider: "apple", github_login: "apple" }), "your Apple account");
  assert.equal(Fmt.who({ provider: "email", github_login: "me@rogerai.fm" }), "me@rogerai.fm");
  assert.equal(Fmt.who({ github_login: "octocat" }), "@octocat", "an older broker without provider falls back to the handle");
  assert.equal(Fmt.who(null), "your account");
});
