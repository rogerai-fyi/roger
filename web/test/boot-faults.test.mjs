// A signed-in person is sent to login ONLY when the broker says 401. A 5xx / network fault on
// the page's first feed shows an error with the card and a working logout - never a redirect
// (login would bounce them to the dashboard and lose their place). Run: node --test test/boot-faults.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const PAGES = [
  { name: "account", file: "js/account.js", path: "/account.html", feed: "/account", ok: { github_login: "octocat", github_id: 7, provider: "github", balance: 1 } },
  { name: "billing", file: "js/billing.js", path: "/billing.html", feed: "/billing", ok: { balance: 1, derived: 1, checkout_ready: false, topups: [] } },
  { name: "payouts", file: "js/payouts.js", path: "/payouts.html", feed: "/account", ok: { github_login: "octocat", operator: true, earnings: {} } },
];

for (const pg of PAGES) {
  test(`${pg.name}: a 401 on the first feed goes to login`, async () => {
    const p = page(pg.path, { [pg.feed]: { status: 401, body: {} } });
    p.run(pg.file); await settle();
    assert.deepEqual(p.navs, ["/login.html"]);
  });
  for (const status of [500, 503]) {
    test(`${pg.name}: a ${status} shows an error and keeps the card, no redirect`, async () => {
      const p = page(pg.path, { [pg.feed]: { status, body: {} } });
      p.run(pg.file); await settle();
      assert.deepEqual(p.navs, [], "a fault is not a sign-out");
      assert.equal(p.els.card.hidden, false);
      assert.equal(p.els.pageError.hidden, false);
    });
  }
  test(`${pg.name}: a network failure shows the error, no redirect`, async () => {
    const p = page(pg.path, {});
    p.ctx.fetch = () => Promise.reject(new Error("offline"));
    p.run(pg.file); await settle();
    assert.deepEqual(p.navs, []);
    assert.equal(p.els.pageError.hidden, false);
  });
  test(`${pg.name}: a good feed shows no error`, async () => {
    const p = page(pg.path, { [pg.feed]: { status: 200, body: pg.ok }, "/payouts/earnings": { status: 200, body: {} }, "/payouts/history": { status: 200, body: {} }, "/connect/status": { status: 200, body: {} }, "/tower/status": { status: 200, body: {} }, "/metrics/provider": { status: 200, body: {} } });
    p.run(pg.file); await settle();
    assert.equal(p.els.pageError.hidden, true);
    assert.equal(p.els.card.hidden, false);
  });
}
