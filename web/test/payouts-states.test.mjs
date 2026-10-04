// Payouts is for people who serve models. A signed-in consumer sees an honest explanation,
// not zeros and a Stripe button that 403s - and the page makes NO operator-only requests
// for them (less load, not more). Run: node --test test/payouts-states.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const OPERATOR_FEEDS = ["/payouts/earnings", "/payouts/history", "/connect/status", "/tower/status", "/metrics/provider"];

test("a consumer: explanation shown, body hidden, no operator-only requests", async () => {
  const p = page("/payouts.html", { "/account": { status: 200, body: { github_login: "me@rogerai.fm", provider: "email", operator: false } } });
  p.run("js/payouts.js");
  await settle();
  assert.equal(p.els.card.hidden, false);
  assert.equal(p.els.poNoOperator.hidden, false);
  assert.equal(p.els.poBody.hidden, true);
  for (const f of OPERATOR_FEEDS) assert.ok(!p.fetched.some((u) => u.startsWith(f)), `must not call ${f} for a consumer`);
});

test("an operator: the full page loads as before", async () => {
  const p = page("/payouts.html", {
    "/account": { status: 200, body: { github_login: "octocat", operator: true, earnings: { held: 1, payable: 2, paid: 3 } } },
    "/payouts/earnings": { status: 200, body: {} }, "/payouts/history": { status: 200, body: {} },
    "/connect/status": { status: 200, body: {} }, "/tower/status": { status: 200, body: {} }, "/metrics/provider": { status: 200, body: {} },
  });
  p.run("js/payouts.js");
  await settle();
  assert.equal(p.els.poNoOperator.hidden, true);
  assert.equal(p.els.poBody.hidden, false);
  assert.ok(p.fetched.some((u) => u.startsWith("/payouts/earnings")));
});

test("an older broker that omits `operator` is treated as an operator (no regression)", async () => {
  const p = page("/payouts.html", { "/account": { status: 200, body: { github_login: "octocat", earnings: {} } } });
  p.run("js/payouts.js");
  await settle();
  assert.equal(p.els.poNoOperator.hidden, true);
});

test("a logged-out visitor still goes to login", async () => {
  const p = page("/payouts.html", { "/account": { status: 401, body: {} } });
  p.run("js/payouts.js");
  await settle();
  assert.deepEqual(p.navs, ["/login.html"]);
});
