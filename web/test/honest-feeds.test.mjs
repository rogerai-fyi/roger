// A failed feed must read as "unavailable", never as a real number or a signed-out gate.
// Run: node --test test/honest-feeds.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const ACCT = { status: 200, body: { github_login: "octocat", github_id: 7, balance: 5 } };
const BILLING = { status: 200, body: { balance: 5, derived: 5, checkout_ready: false, topups: [] } };

test("billing: a failed /console shows '-' for spend today, not $0.00", async () => {
  const p = page("/billing.html", { "/billing": BILLING, "/account": ACCT, "/metrics/series": { status: 200, body: { daily: [] } },
    "/console": { status: 500, body: {} }, "/account/limit": { status: 200, body: {} } });
  p.run("js/billing.js");
  await settle();
  assert.equal(p.els.spendToday.textContent, "-");
});

test("billing: a working /console still shows the real figure", async () => {
  const p = page("/billing.html", { "/billing": BILLING, "/account": ACCT, "/metrics/series": { status: 200, body: { daily: [] } },
    "/console": { status: 200, body: { counters: { spend_today: 1.5 }, events: [] } }, "/account/limit": { status: 200, body: {} } });
  p.run("js/billing.js");
  await settle();
  assert.notEqual(p.els.spendToday.textContent, "-");
  assert.match(p.els.spendToday.textContent, /1\.5/);
});

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

test("private bands: a 403 says no machine is linked, never 'sign in' to someone signed in", async () => {
  const p = page("/private.html", { "/account": ACCT, "/rc/sessions": { status: 200, body: { sessions: [] } }, "/bands": { status: 403, body: {} } });
  p.run("js/private.js");
  await settle();
  assert.match(p.texts(), /machine on air/);
  assert.doesNotMatch(p.texts(), /sign in to see your private bands/);
});
