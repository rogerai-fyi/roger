// The Account page says which sign-in you are using, whether machines are linked to it,
// and tells the truth when saving a contact email cannot work. Run: node --test test/account-identity.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const A = (o) => ({ status: 200, body: Object.assign({ balance: 5, connect: { status: "none" } }, o) });

test("email sign-in: the address is the sign-in, shown read-only; no GitHub id row", async () => {
  const p = page("/account.html", { "/account": A({ github_login: "me@rogerai.fm", provider: "email", email: "me@rogerai.fm", email_verified: true, operator: false }) });
  p.run("js/account.js");
  await settle();
  assert.equal(p.els.handle.textContent, "me@rogerai.fm");
  assert.equal(p.els.signin.textContent, "Email");
  assert.equal(p.els.email.value, "me@rogerai.fm");
  assert.equal(p.els.email.readOnly, true, "the sign-in address is not editable here");
  assert.equal(p.els.saveEmail.hidden, true);
  assert.equal(p.els.machines.textContent, "None linked to this sign-in");
});

test("github operator: provider and id, machines linked, email editable", async () => {
  const p = page("/account.html", { "/account": A({ github_login: "octocat", github_id: 7, provider: "github", operator: true, email: "o@x.com" }) });
  p.run("js/account.js");
  await settle();
  assert.equal(p.els.handle.textContent, "@octocat (GitHub)");
  assert.equal(p.els.signin.textContent, "GitHub");
  assert.equal(p.els.machines.textContent, "Linked");
  assert.notEqual(p.els.email.readOnly, true);
});

test("saving the contact email shows the broker's own reason when it cannot save", async () => {
  const p = page("/account.html", {
    "/account": A({ github_login: "newbie", github_id: 99, provider: "github", operator: false }),
  });
  const base = p.ctx.fetch;
  p.ctx.fetch = (url, opts) => (opts && opts.method === "PATCH")
    ? Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: { message: "no operator account for this login (run `roger login` on a node first)" } }) })
    : base(url, opts);
  p.run("js/account.js");
  await settle();
  await p.fire("saveEmail", "click");
  await settle();
  assert.match(p.els.saveMsg.textContent, /roger login/, "the reason, not a bare 'could not save'");
});

test("an older broker without provider/operator still renders (no regression)", async () => {
  const p = page("/account.html", { "/account": A({ github_login: "octocat", github_id: 7 }) });
  p.run("js/account.js");
  await settle();
  assert.equal(p.els.card.hidden, false);
  assert.equal(p.els.handle.textContent, "@octocat");
});
