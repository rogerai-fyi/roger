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
