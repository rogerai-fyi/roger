// The Account page lets a GitHub/Apple sign-in add and verify an email address, so emailing
// a code later reaches the same account. Run: node --test test/account-link.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const A = (o) => ({ status: 200, body: Object.assign({ balance: 5, connect: { status: "none" } }, o) });
const GH = { github_login: "octocat", github_id: 7, provider: "github", operator: true };

// a page whose POSTs to the link routes are recorded and answered from `replies`
function linkPage(acct, replies) {
  const p = page("/account.html", { "/account": A(acct) });
  const base = p.ctx.fetch;
  p.posts = [];
  p.ctx.fetch = (url, opts) => {
    const u = String(url).replace("https://broker.rogerai.fm", "");
    if (opts && opts.method === "POST" && u.startsWith("/auth/email/link/")) {
      p.posts.push({ u, body: JSON.parse(opts.body) });
      const r = replies[u] || { status: 200, body: { ok: true } };
      return Promise.resolve({ ok: r.status >= 200 && r.status < 300, status: r.status, json: () => Promise.resolve(r.body) });
    }
    return base(url, opts);
  };
  return p;
}

test("an email sign-in sees no link panel (its address is already its sign-in)", async () => {
  const p = linkPage({ github_login: "me@x.com", provider: "email", operator: false }, {});
  p.run("js/account.js");
  await settle();
  assert.equal(p.els.linkPanel.hidden, true);
});

test("a GitHub sign-in with machines can add an address: send code, then verify", async () => {
  const p = linkPage(GH, { "/auth/email/link/start": { status: 200, body: { ok: true, token: "TOK123" } } });
  p.run("js/account.js");
  await settle();
  assert.equal(p.els.linkPanel.hidden, false);
  assert.equal(p.els.linkForm.hidden, false);

  p.els.linkEmail.value = "me@example.com";
  await p.fire("linkSend", "click");
  await settle();
  assert.deepEqual(p.posts[0], { u: "/auth/email/link/start", body: { email: "me@example.com" } });
  assert.equal(p.els.linkStep2.hidden, false, "the code step appears");

  p.els.linkCode.value = "123456";
  await p.fire("linkVerify", "click");
  await settle();
  assert.deepEqual(p.posts[1], { u: "/auth/email/link/verify", body: { email: "me@example.com", code: "123456", token: "TOK123" } });
  assert.match(p.els.linkMsg.textContent, /me@example\.com/);
  assert.match(p.els.linkMsg.textContent, /added|verified/i);
});

test("a refused address shows the broker's reason and records nothing locally", async () => {
  const p = linkPage(GH, {
    "/auth/email/link/start": { status: 200, body: { ok: true, token: "T" } },
    "/auth/email/link/verify": { status: 409, body: { error: { message: "that address cannot be added" } } },
  });
  p.run("js/account.js");
  await settle();
  p.els.linkEmail.value = "taken@example.com";
  await p.fire("linkSend", "click"); await settle();
  p.els.linkCode.value = "111111";
  await p.fire("linkVerify", "click"); await settle();
  assert.match(p.els.linkMsg.textContent, /cannot be added/);
  assert.doesNotMatch(p.els.linkMsg.textContent, /added to your account/i);
});

test("a start failure (rate limit) is shown and the code step stays closed", async () => {
  const p = linkPage(GH, { "/auth/email/link/start": { status: 429, body: { error: { message: "too many requests - wait a moment and try again" } } } });
  p.run("js/account.js");
  await settle();
  p.els.linkEmail.value = "me@example.com";
  await p.fire("linkSend", "click"); await settle();
  assert.match(p.els.linkMsg.textContent, /too many/);
  assert.equal(p.els.linkStep2.hidden, true);
});

test("a sign-in with no machines is told to run roger login first, no form", async () => {
  const p = linkPage({ github_login: "newbie", github_id: 99, provider: "github", operator: false }, {});
  p.run("js/account.js");
  await settle();
  assert.equal(p.els.linkPanel.hidden, false);
  assert.equal(p.els.linkForm.hidden, true);
  assert.equal(p.els.linkNeedsLogin.hidden, false);
});

test("an already-verified address is shown as the sign-in email", async () => {
  const p = linkPage({ ...GH, email: "me@example.com", email_verified: true }, {});
  p.run("js/account.js");
  await settle();
  assert.match(p.els.linkCurrent.textContent, /me@example\.com/);
});

test("when a separate email wallet holds funds, the merge note is shown with the success message", async () => {
  const p = linkPage(GH, {
    "/auth/email/link/start": { status: 200, body: { ok: true, token: "T" } },
    "/auth/email/link/verify": { status: 200, body: { ok: true, email: "me@example.com", separate_email_balance: 3, merge_note: "A separate email-only account for this address holds funds. Nothing was merged; write to labs@rogerai.fm to have the two accounts merged deliberately." } },
  });
  p.run("js/account.js");
  await settle();
  p.els.linkEmail.value = "me@example.com";
  await p.fire("linkSend", "click"); await settle();
  p.els.linkCode.value = "123456";
  await p.fire("linkVerify", "click"); await settle();
  assert.match(p.els.linkMsg.textContent, /me@example\.com is added and verified/);
  assert.match(p.els.linkMsg.textContent, /merged deliberately/, "the person is told how to have the wallets merged");
});
