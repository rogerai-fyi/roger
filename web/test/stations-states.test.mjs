// The stations page must tell the truth about WHY it has nothing to show.
// A signed-in person with no operator account (the broker's 403) is the NORMAL state for a
// consumer, or for someone signed in by email whose stations belong to a GitHub identity:
// it is not a load failure. Only a real fault (5xx / network) is an error.
// Run: node --test test/stations-states.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const ACCT = { status: 200, body: { github_login: "me@rogerai.fm", github_id: 0 } };

test("403 no operator account: explains it, never claims a load failure", async () => {
  const p = page("/stations.html", { "/stations": { status: 403, body: {} }, "/account": ACCT });
  p.run("js/stations.js");
  await settle();
  assert.equal(p.els.stNoOperator.hidden, false, "the not-sharing-from-this-sign-in state is shown");
  assert.equal(p.els.stError.hidden, true, "a 403 is not a load error");
  assert.equal(p.els.card.hidden, false);
  assert.equal(p.els.who.textContent, "me@rogerai.fm", "names the identity (an email shown as itself)");
  assert.deepEqual(p.navs, []);
});

test("200 with no stations: the 'No stations yet' state", async () => {
  const p = page("/stations.html", { "/stations": { status: 200, body: { github_login: "octocat", stations: [], strikes: [] } } });
  p.run("js/stations.js");
  await settle();
  assert.equal(p.els.stEmpty.hidden, false);
  assert.equal(p.els.stNoOperator.hidden, true);
  assert.equal(p.els.who.textContent, "@octocat");
});

for (const status of [500, 502, 503]) {
  test(`${status}: a real fault is an error with a retry hint`, async () => {
    const p = page("/stations.html", { "/stations": { status, body: {} } });
    p.run("js/stations.js");
    await settle();
    assert.equal(p.els.stError.hidden, false);
    assert.equal(p.els.stNoOperator.hidden, true);
  });
}

test("401: not signed in goes to login and comes back here", async () => {
  const p = page("/stations.html", { "/stations": { status: 401, body: {} } });
  p.run("js/stations.js");
  await settle();
  assert.deepEqual(p.navs, ["/login.html?next=%2Fstations.html"]);
});

test("a network failure is an error, not 'no operator account'", async () => {
  const p = page("/stations.html", {});
  p.ctx.fetch = () => Promise.reject(new Error("offline"));
  p.run("js/stations.js");
  await settle();
  assert.equal(p.els.stError.hidden, false);
  assert.equal(p.els.stNoOperator.hidden, true);
});
