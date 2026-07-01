// Behaviour lock for web_session_hint.feature *Rule 2* — the front-end probe gate in
// web/src/js/session.js. Rule 1 (the broker setting/clearing the readable roger_signed_in
// hint alongside the HttpOnly session cookie) is Go, enforced by cmd/rogerai-broker
// TestSetWebSessionCookies / TestClearWebSessionCookies / TestWebOriginHost. Rule 2 is
// BROWSER JS: "probe GET /account ONLY when the hint says a session may exist", so a
// logged-out visitor makes ZERO request (no 401), and on a 200 the nav swaps to the
// account control. A Go godog suite cannot drive it; this pins it instead.
//
// session.js is a browser IIFE with no test export hook, so — like fmt.test.mjs /
// easter-egg.test.mjs — we run the REAL shipped source in a tiny sandbox: a dependency-free
// mini-DOM (so document.cookie / querySelector / createElement / replaceChild behave) plus
// a fetch spy, injected as the IIFE's globals. No jsdom: the web tree is dependency-free on
// purpose (build.mjs "no npm install"; npm test has no install step), so a devDep would make
// the gate RED. Run: node --test test/session.test.mjs   (picked up by `npm test`).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SESSION_SRC = readFileSync(
  path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/session.js"),
  "utf8",
);

// ---- a tiny dependency-free DOM ---------------------------------------------
// Just enough of the Element surface session.js touches (findLoginLink + mount): class
// list, attributes, the child tree, append/replace, and event listeners (no-ops — the
// menu handlers are never fired here).
function makeEl(tag) {
  const el = {
    tagName: String(tag).toUpperCase(),
    children: [],
    parentNode: null,
    _attrs: {},
    className: "",
    hidden: false,
    setAttribute(k, v) { this._attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    appendChild(c) { c.parentNode = this; this.children.push(c); return c; },
    removeChild(c) { const i = this.children.indexOf(c); if (i >= 0) { this.children.splice(i, 1); c.parentNode = null; } return c; },
    replaceChild(nw, old) { const i = this.children.indexOf(old); if (i >= 0) { this.children[i] = nw; nw.parentNode = this; old.parentNode = null; } return old; },
    addEventListener() {},
    removeEventListener() {},
    focus() {},
    contains(node) {
      if (node === this) return true;
      return this.children.some((c) => c && c.contains && c.contains(node));
    },
  };
  el.classList = {
    contains: (c) => el.className.split(/\s+/).includes(c),
    add: (c) => { if (!el.classList.contains(c)) el.className = (el.className + " " + c).trim(); },
    remove: (c) => { el.className = el.className.split(/\s+/).filter((x) => x !== c).join(" "); },
    toggle: (c, on) => {
      const want = on === undefined ? !el.classList.contains(c) : on;
      if (want) el.classList.add(c); else el.classList.remove(c);
      return want;
    },
  };
  return el;
}

const descendants = (root) => root.children.flatMap((c) => [c, ...descendants(c)]);

// matchSimple: a single compound selector — optional tag, any .classes, any [attrs].
function matchSimple(el, simple) {
  const tag = simple.match(/^[a-zA-Z][\w-]*/);
  if (tag && el.tagName !== tag[0].toUpperCase()) return false;
  for (const m of simple.matchAll(/\.([\w-]+)/g)) if (!el.classList.contains(m[1])) return false;
  for (const m of simple.matchAll(/\[([\w-]+)\]/g)) if (el.getAttribute(m[1]) === null) return false;
  return true;
}

// queryAll: descendant combinators (space). Enough for the two selectors findLoginLink
// uses: "[data-session-login]" and ".nav__utils a.nav__util".
function queryAll(root, selector) {
  const parts = selector.trim().split(/\s+/);
  let matches = descendants(root).filter((el) => matchSimple(el, parts[parts.length - 1]));
  for (let i = parts.length - 2; i >= 0; i--) {
    matches = matches.filter((el) => {
      for (let p = el.parentNode; p; p = p.parentNode) if (matchSimple(p, parts[i])) return true;
      return false;
    });
  }
  return matches;
}

function makeDocument() {
  const root = makeEl("#document");
  return {
    cookie: "",
    body: root,
    createElement: (tag) => makeEl(tag),
    querySelector: (sel) => queryAll(root, sel)[0] || null,
    querySelectorAll: (sel) => queryAll(root, sel),
    addEventListener() {},
    removeEventListener() {},
  };
}

// The shipped homepage nav slot: .nav__utils > a.nav__util[href="/login"] (the link
// session.js's findLoginLink falls back to when there's no [data-session-login] hook).
function buildPage(doc) {
  const nav = doc.createElement("nav");
  const utils = doc.createElement("div");
  utils.className = "nav__utils";
  const login = doc.createElement("a");
  login.className = "nav__util";
  login.setAttribute("href", "/login");
  login.textContent = "Log in";
  utils.appendChild(login);
  nav.appendChild(utils);
  doc.body.appendChild(nav);
  return { utils, login };
}

const flush = () => new Promise((r) => setTimeout(r, 0)); // drain fetch .then() microtasks

// run loads + executes the real session.js IIFE against a fresh page, returning the fetch
// spy's calls and the nav slot so each scenario can assert probe-or-not and nav-swap.
async function run({ cookie = "", brokerCheck, acct } = {}) {
  const doc = makeDocument();
  const { utils, login } = buildPage(doc);
  doc.cookie = cookie;
  const win = {};
  if (brokerCheck !== undefined) win.ROGER_BROKER_CHECK = brokerCheck;

  const calls = [];
  const fetch = (url, opts) => {
    calls.push({ url, opts });
    const r = acct ? { ok: true, json: () => Promise.resolve(acct) } : { ok: false, status: 401 };
    return Promise.resolve(r);
  };

  // Inject document/window/fetch/location as the IIFE's globals (location is only used by
  // the never-fired Sign-out handler; a stub keeps it safe). encodeURIComponent is global.
  new Function("window", "document", "fetch", "location", SESSION_SRC)(win, doc, fetch, { reload() {} });
  await flush();
  return { calls, utils, login };
}

// Scenario: a logged-out homepage visitor makes no /account request.
test("logged-out (no hint cookie): no /account probe, static nav untouched", async () => {
  const { calls, utils, login } = await run({ cookie: "" });
  // No fetch at all -> no request -> no 401 in the console (that IS the fix).
  assert.equal(calls.length, 0, "no probe when roger_signed_in is absent");
  assert.ok(utils.children.includes(login), "the Log in link is left exactly as shipped");
});

// An unrelated cookie must not be mistaken for the hint.
test("logged-out with other cookies present: still no probe", async () => {
  const { calls } = await run({ cookie: "theme=dark; session=abc" });
  assert.equal(calls.length, 0, "only roger_signed_in=1 may trigger a probe");
});

// Scenario: a logged-in homepage visitor probes /account and swaps the nav.
test("logged-in (roger_signed_in=1): credentialed /account probe, then nav swap on 200", async () => {
  const acct = { github_login: "octocat", github_id: 583231 };
  const { calls, utils, login } = await run({ cookie: "roger_signed_in=1", acct });

  assert.equal(calls.length, 1, "exactly one probe");
  assert.equal(calls[0].url, "https://broker.rogerai.fyi/account", "probes the broker /account");
  assert.deepEqual(calls[0].opts, { credentials: "include" }, "credentialed CORS (cookie carried)");

  // The static link is replaced in place by the account control.
  assert.ok(!utils.children.includes(login), "the Log in link was swapped out");
  const wrap = utils.children.find((c) => c.className === "acctmenu");
  assert.ok(wrap, "an .acctmenu wrapper took the slot");
  const btn = wrap.children.find((c) => c.tagName === "BUTTON");
  assert.equal(btn.getAttribute("aria-label"), "Account menu for @octocat");
  const handle = btn.children.find((c) => c.className === "acctmenu__handle");
  assert.equal(handle.textContent, "@octocat", "shows the @handle");
  // Avatar prefers the id-keyed CDN URL (no redirect / cookie warning).
  const img = btn.children.find((c) => c.tagName === "IMG");
  assert.equal(img.src, "https://avatars.githubusercontent.com/u/583231?s=48&v=4");
});

// An Apple web session carries the email (or "apple") in github_login with github_id 0:
// the menu must show the email AS-IS (never "@a@b.com", never the literal "@you") and must
// NOT fetch a github.com avatar for it (github.com/<email>.png would 404 = broken image).
test("Apple session (email handle, github_id 0): email shown as-is, no GitHub avatar", async () => {
  const acct = { github_login: "pilot@privaterelay.appleid.com", github_id: 0 };
  const { utils } = await run({ cookie: "roger_signed_in=1", acct });
  const wrap = utils.children.find((c) => c.className === "acctmenu");
  assert.ok(wrap, "the account control mounts for an Apple session too");
  const btn = wrap.children.find((c) => c.tagName === "BUTTON");
  assert.equal(btn.getAttribute("aria-label"), "Account menu for pilot@privaterelay.appleid.com");
  const handle = btn.children.find((c) => c.className === "acctmenu__handle");
  assert.equal(handle.textContent, "pilot@privaterelay.appleid.com", "an email handle gets no @-prefix");
  const img = btn.children.find((c) => c.tagName === "IMG");
  assert.equal(img, undefined, "no github.com avatar for an email identity (it would 404)");
});

// The "apple" fallback (no email relayed) still renders a sane handle, not a 404 avatar URL
// keyed by a non-username - but a username-shaped login without an id may use github.com/<login>.png.
test("Apple session with no email: @apple handle, github.com avatar by login shape", async () => {
  const acct = { github_login: "apple", github_id: 0 };
  const { utils } = await run({ cookie: "roger_signed_in=1", acct });
  const wrap = utils.children.find((c) => c.className === "acctmenu");
  const handle = wrap.children.find((c) => c.tagName === "BUTTON").children.find((c) => c.className === "acctmenu__handle");
  assert.equal(handle.textContent, "@apple");
});

// Scenario: forcing the probe for local-broker development.
test("window.ROGER_BROKER_CHECK forces the probe even with no hint cookie", async () => {
  const { calls } = await run({ cookie: "", brokerCheck: true, acct: { github_login: "dev" } });
  assert.equal(calls.length, 1, "probe forced for local-broker dev");
  assert.equal(calls[0].url, "https://broker.rogerai.fyi/account");
});

// Adversarial: the hint is recognized ONLY as an exact roger_signed_in=1 cookie token,
// pinning signedInHint's /(?:^|;\s*)roger_signed_in=1(?:;|$)/ matcher.
test("only an exact roger_signed_in=1 token counts as the hint", async () => {
  const probe = { github_login: "x" };
  assert.equal((await run({ cookie: "a=b; roger_signed_in=1; c=d", acct: probe })).calls.length, 1, "present among others -> probe");
  assert.equal((await run({ cookie: "roger_signed_in=0" })).calls.length, 0, "value 0 (cleared) -> no probe");
  assert.equal((await run({ cookie: "roger_signed_in=10" })).calls.length, 0, "=10 must not match =1");
  assert.equal((await run({ cookie: "xroger_signed_in=1" })).calls.length, 0, "name suffix must not match");
  assert.equal((await run({ cookie: "not_the_hint=1" })).calls.length, 0, "a different cookie -> no probe");
});
