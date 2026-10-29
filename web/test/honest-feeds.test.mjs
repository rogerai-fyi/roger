// A failed feed must read as "unavailable", never as a real number or a signed-out gate.
// Run: node --test test/honest-feeds.test.mjs
// Billing and Base Station are islands (app/lib/billing/BillingApp.svelte, app/lib/private/PrivateApp.svelte):
// their rows are in test/unit/billing.test.js and test/unit/private.test.js.
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const ACCT = { status: 200, body: { github_login: "octocat", github_id: 7, balance: 5 } };

test("usage: a 403 from the series feed is an error, not the signed-out gate", async () => {
  const p = page("/metrics.html", { "/account": ACCT, "/metrics/series": { status: 403, body: {} } });
  p.run("js/metrics.js");
  await settle();
  assert.equal(p.els.gate.hidden, true, "a signed-in person is never shown the signed-out gate for a 403");
  assert.equal(p.els.allError.hidden, false);
});

test("usage: a 401 (session really gone) still shows the gate", async () => {
  const p = page("/metrics.html", { "/account": ACCT, "/metrics/series": { status: 401, body: {} } });
  p.run("js/metrics.js");
  await settle();
  assert.equal(p.els.gate.hidden, false);
});
