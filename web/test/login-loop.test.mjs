// Regression lock for the login <-> dashboard redirect loop.
//
// The bug: after an emailed code was accepted, /account said "signed in" but
// /metrics/series said 401. dashboard.js answered a 401 with location.replace("/login.html"),
// and login.html (auth.js) answered "signed in" with location.replace("/dashboard.html"):
// an endless refresh. The broker half is pinned in cmd/rogerai-broker/emaillogin_dashboard_test.go;
// this pins the page half, so a future broker-side 401 can NEVER loop the browser again.
//
// These run the REAL page scripts in a vm against a fake broker, counting navigations.
// Run: node --test test/login-loop.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { page, settle, Fmt, src } from "./_pagevm.mjs";

const SIGNED_IN = { status: 200, body: { github_login: "someone@rogerai.fm", github_id: 0 } };

for (const [status, label] of [[401, "401"], [403, "403"]]) {
  test(`dashboard: a ${label} from /metrics/series shows an error and does NOT bounce to login`, async () => {
    const p = page("/dashboard.html", { "/account": SIGNED_IN, "/metrics/series": { status, body: {} } });
    p.run("js/dashboard.js");
    await settle();
    assert.deepEqual(p.navs, [], "a signed-in person must never be sent to /login.html by a feed 401/403");
    assert.equal(p.els.dashError.hidden, false, "the error state is shown instead");
  });

  test(`console: a ${label} from /console shows an error and does NOT bounce to login`, async () => {
    const p = page("/console.html", { "/account": SIGNED_IN, "/console": { status, body: {} } });
    p.run("js/console.js");
    await settle();
    assert.deepEqual(p.navs, []);
    assert.equal(p.els.cnError.hidden, false);
  });
}

test("dashboard: a genuinely logged-out visitor (/account 401) still goes to login", async () => {
  const p = page("/dashboard.html", { "/account": { status: 401, body: {} } });
  p.run("js/dashboard.js");
  await settle();
  assert.deepEqual(p.navs, ["/login.html"]);
});

test("login <-> dashboard cannot ping-pong when the feed 401s: total navigations stay bounded", async () => {
  const routes = { "/account": SIGNED_IN, "/metrics/series": { status: 401, body: {} }, "/me": SIGNED_IN };
  let hops = 0, at = "/login.html";
  for (; hops < 10; hops++) {
    const p = page(at, routes);
    p.run(at.startsWith("/login") ? "js/auth.js" : "js/dashboard.js");
    await settle();
    if (!p.navs.length) break;
    at = p.navs[0];
  }
  assert.ok(hops <= 2, `settled after ${hops} navigations (a loop never settles)`);
});

test("an email address is shown as the address, never '@a@b.com'", () => {
  assert.equal(Fmt.handle("someone@rogerai.fm"), "someone@rogerai.fm");
  assert.equal(Fmt.handle("octocat"), "@octocat");
  assert.equal(Fmt.handle(""), "@you");
  assert.equal(Fmt.handle(undefined), "@you");
});

test("every page that shows the signed-in name uses the shared handle formatter", () => {
  for (const f of ["js/dashboard.js", "js/console.js", "js/account.js", "js/auth.js"]) {
    assert.doesNotMatch(src(f), /"@" \+ \((?:acct|a|me)\.github_login/, `${f} must not hand-roll "@"+login`);
  }
});

// A script error AFTER /account confirmed the session (a missing helper, a render bug) used to
// land in the outer catch, which redirected to /login.html: the same loop by another route.
for (const [file, route, err] of [["js/dashboard.js", "/metrics/series", "dashError"], ["js/console.js", "/console", "cnError"]]) {
  test(`${file}: a script error after the session is confirmed shows an error, never a redirect`, async () => {
    const p = page("/x.html", { "/account": SIGNED_IN, [route]: { status: 200, body: {} } }, { noFmt: true });
    p.run(file);
    await settle();
    assert.deepEqual(p.navs, [], "a signed-in person must not be bounced to /login.html by a script error");
    assert.equal(p.els[err].hidden, false);
    assert.equal(p.els.card.hidden, false, "the error lives INSIDE #card (hidden until shown): it must be visible, not just un-hidden");
  });
}
