// The stations page's evidence section told operators "you can appeal" with no way to. It now
// has an appeal form that posts to the existing /owner/appeal route, shows the broker's own
// answer, and never claims success on a failure. Run: node --test test/stations-appeal.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle } from "./_pagevm.mjs";

const WITH_STRIKE = { status: 200, body: { github_login: "octocat", stations: [], strikes: [{ kind: "flagged", evidence: "e", created_at: 1 }] } };

function appealPage(stations, reply) {
  const p = page("/stations.html", { "/stations": stations });
  const base = p.ctx.fetch;
  p.posts = [];
  p.ctx.fetch = (url, opts) => {
    const u = String(url).replace("https://broker.rogerai.fm", "");
    if (opts && opts.method === "POST" && u === "/owner/appeal") {
      p.posts.push(JSON.parse(opts.body));
      return Promise.resolve({ ok: reply.status < 300, status: reply.status, json: () => Promise.resolve(reply.body) });
    }
    return base(url, opts);
  };
  return p;
}

test("the appeal form is shown with the evidence, and not for an account with none", async () => {
  const withStrike = appealPage(WITH_STRIKE, { status: 200, body: {} });
  withStrike.run("js/stations.js"); await settle();
  assert.equal(withStrike.els.stAppeal.hidden, false);

  const clean = appealPage({ status: 200, body: { github_login: "octocat", stations: [], strikes: [] } }, { status: 200, body: {} });
  clean.run("js/stations.js"); await settle();
  assert.equal(clean.els.stAppeal.hidden, true);
});

test("submitting posts the reason and shows the appeal id", async () => {
  const p = appealPage(WITH_STRIKE, { status: 200, body: { ok: true, appeal_id: 42, state: "open" } });
  p.run("js/stations.js"); await settle();
  p.els.appealReason.value = "  The flagged request was my own test.  ";
  await p.fire("appealSend", "click"); await settle();
  assert.deepEqual(p.posts, [{ reason: "The flagged request was my own test." }], "trimmed, and no account id is ever sent");
  assert.match(p.els.appealMsg.textContent, /42/);
  assert.match(p.els.appealMsg.textContent, /review/i);
});

test("an empty reason is not sent", async () => {
  const p = appealPage(WITH_STRIKE, { status: 200, body: { ok: true, appeal_id: 1 } });
  p.run("js/stations.js"); await settle();
  p.els.appealReason.value = "   ";
  await p.fire("appealSend", "click"); await settle();
  assert.equal(p.posts.length, 0);
  assert.match(p.els.appealMsg.textContent, /reason/i);
});

test("a refusal shows the broker's message and never claims it was filed", async () => {
  const p = appealPage(WITH_STRIKE, { status: 403, body: { error: { message: "no operator account for this login (run `roger login` on a node first)" } } });
  p.run("js/stations.js"); await settle();
  p.els.appealReason.value = "mistake";
  await p.fire("appealSend", "click"); await settle();
  assert.match(p.els.appealMsg.textContent, /roger login/);
  assert.doesNotMatch(p.els.appealMsg.textContent, /filed|appeal id/i);
});

test("the send button is disabled in flight and after success, so a double-click files one appeal", async () => {
  const p = appealPage(WITH_STRIKE, { status: 200, body: { ok: true, appeal_id: 7 } });
  p.run("js/stations.js"); await settle();
  p.els.appealReason.value = "mistake";
  await Promise.all([p.fire("appealSend", "click"), p.fire("appealSend", "click")]);
  await settle();
  assert.equal(p.posts.length, 1, "two rapid clicks send one appeal");
  assert.equal(p.els.appealSend.disabled, true, "and it stays disabled after success");
});

test("a refusal re-enables the button so the person can retry", async () => {
  const p = appealPage(WITH_STRIKE, { status: 500, body: { error: { message: "store error" } } });
  p.run("js/stations.js"); await settle();
  p.els.appealReason.value = "mistake";
  await p.fire("appealSend", "click"); await settle();
  assert.equal(p.els.appealSend.disabled, false);
});

test("existing appeals are listed on load, so a reload does not look like nothing was filed", async () => {
  const p = appealPage(WITH_STRIKE, { status: 200, body: {} });
  const base = p.ctx.fetch;
  p.ctx.fetch = (url, opts) => String(url).endsWith("/owner/appeal") && !(opts && opts.method === "POST")
    ? Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ appeals: [{ id: 5, state: "open", reason: "mine", created_at: 1 }], count: 1 }) })
    : base(url, opts);
  p.run("js/stations.js"); await settle();
  assert.equal(p.els.appealHistory.hidden, false);
  assert.match(p.els.appealHistory.innerHTML, /#5/);
  assert.match(p.els.appealHistory.innerHTML, /open/);
});

test("an expired session on the appeal POST says to sign in again, not 'run roger login'", async () => {
  const p = appealPage(WITH_STRIKE, { status: 401, body: { error: { message: "not logged in - run `roger login` to link GitHub" } } });
  p.run("js/stations.js"); await settle();
  p.els.appealReason.value = "mistake";
  await p.fire("appealSend", "click"); await settle();
  assert.match(p.els.appealMsg.textContent, /sign in again/i);
  assert.doesNotMatch(p.els.appealMsg.textContent, /roger login/);
});

test("a newly filed appeal appears in the list without a reload", async () => {
  const p = appealPage(WITH_STRIKE, { status: 200, body: { ok: true, appeal_id: 9, state: "open" } });
  let calls = 0;
  const base = p.ctx.fetch;
  p.ctx.fetch = (url, opts) => String(url).endsWith("/owner/appeal") && !(opts && opts.method === "POST")
    ? Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ appeals: calls++ === 0 ? [] : [{ id: 9, state: "open", reason: "mistake" }] }) })
    : base(url, opts);
  p.run("js/stations.js"); await settle();
  p.els.appealReason.value = "mistake";
  await p.fire("appealSend", "click"); await settle();
  assert.match(p.els.appealHistory.innerHTML, /#9/);
});
