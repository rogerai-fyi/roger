// The keys page must not call a normal "no operator account" 403 a load failure, and must
// not offer a mint form that cannot succeed. Run: node --test test/keys-states.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const ACCT = { status: 200, body: { github_login: "me@rogerai.fm", github_id: 0, provider: "email", operator: false } };

test("403 on /grants: explains keys belong to an account with a machine on air", async () => {
  const p = page("/keys.html", { "/account": ACCT, "/grants": { status: 403, body: {} } });
  p.run("js/keys.js");
  await settle();
  assert.equal(p.els.keysNoOperator.hidden, false);
  assert.equal(p.els.keysError.hidden, true, "a 403 is not a load failure");
  assert.equal(p.els.createForm.hidden, true, "no mint form that cannot succeed");
  assert.deepEqual(p.navs, []);
});

test("200 with no keys: the normal empty state, form usable", async () => {
  const p = page("/keys.html", { "/account": ACCT, "/grants": { status: 200, body: { grants: [] } } });
  p.run("js/keys.js");
  await settle();
  assert.equal(p.els.keysEmpty.hidden, false);
  assert.equal(p.els.keysNoOperator.hidden, true);
});

test("500 on /grants: a real fault is an error", async () => {
  const p = page("/keys.html", { "/account": ACCT, "/grants": { status: 500, body: {} } });
  p.run("js/keys.js");
  await settle();
  assert.equal(p.els.keysError.hidden, false);
  assert.equal(p.els.keysNoOperator.hidden, true);
});
