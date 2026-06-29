// Behaviour lock for "Sign in with Apple" on the web — the Sign in with Apple JS (SiwA)
// sibling of the GitHub login link. Covers web/src/js/apple-signin.js and the button +
// config wired into web/src/login.html.
//
// Like session.test.mjs / easter-egg.test.mjs we run the REAL shipped source in a tiny
// dependency-free sandbox: the web tree has NO devDeps on purpose (build.mjs "no npm
// install"; `npm test` has no install step), so a jsdom devDep would make the gate RED.
//
// What this pins:
//  - login.html actually ships the Apple button (data-apple-signin), apple-signin.js, and
//    the PUBLIC client-config <meta> tags the founder fills (Services ID + Return URL).
//  - apple-signin.js owns the Apple SiwA JS SDK URL and the broker /auth/apple POST.
//  - On click the module: makes a fresh random nonce, hands Apple HEX(sha256(nonce)) — the
//    exact value the broker's Apple bind recomputes (hex(sha256(raw_nonce))) — inits
//    AppleID.auth with usePopup (so the id_token comes back to JS on a static site),
//    signs in, then POSTs {identity_token, raw_nonce} to the broker /auth/apple credentialed,
//    and on 200 lands on the dashboard (exactly like the GitHub web login).
//  - The raw_nonce it POSTs is the EXACT pre-image of the nonce handed to Apple (the contract).
//  - Unconfigured (no Services ID yet): the button is hidden, never half-wired.
//  - A broker rejection (non-200) does not redirect and re-enables the button for a retry.
//
// Run: node --test test/apple-signin.test.mjs   (picked up by `npm test`).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { webcrypto, createHash } from "node:crypto";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(path.join(HERE, p), "utf8");
const APPLE_SRC = read("../src/js/apple-signin.js");
const LOGIN_HTML = read("../src/login.html");

// ---- static markup: the button + its wiring actually ship on the login page -------------
test("login.html ships the Apple button, apple-signin.js, and the client-config meta tags", () => {
  assert.match(LOGIN_HTML, /data-apple-signin/, "the Apple button carries the data-apple-signin hook");
  assert.match(LOGIN_HTML, /Sign in with Apple/i, "the button is labelled");
  assert.match(LOGIN_HTML, /js\/apple-signin\.js/, "the page loads apple-signin.js");
  assert.match(LOGIN_HTML, /name="appleid-signin-client-id"/, "carries the Services ID config meta");
  assert.match(LOGIN_HTML, /name="appleid-signin-redirect-uri"/, "carries the Return-URL config meta");
});

test("apple-signin.js owns Apple's SiwA JS SDK URL and posts to the broker /auth/apple", () => {
  assert.match(APPLE_SRC, /appleid\.cdn-apple\.com\/appleauth\/static\/jsapi\/appleid\/1\/[^"']*appleid\.auth\.js/, "loads Apple's SiwA JS SDK on demand");
  assert.match(APPLE_SRC, /\/auth\/apple/, "POSTs the bind to /auth/apple");
});

// ---- a tiny DOM that records click listeners so a scenario can fire the button ----------
function makeEl(tag) {
  return {
    tagName: String(tag).toUpperCase(), children: [], parentNode: null,
    _attrs: {}, _listeners: {}, className: "", hidden: false, disabled: false, src: "", async: false,
    setAttribute(k, v) { this._attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    appendChild(c) { c.parentNode = this; this.children.push(c); return c; },
    addEventListener(ev, fn) { (this._listeners[ev] = this._listeners[ev] || []).push(fn); },
    removeEventListener() {},
    dispatch(ev, e) { (this._listeners[ev] || []).slice().forEach((fn) => fn(e || { preventDefault() {} })); },
  };
}

function makeDoc() {
  const byName = {};
  const byId = {};
  let btn = null;
  const head = makeEl("head");
  const doc = {
    head,
    createElement: (t) => makeEl(t),
    querySelector(sel) {
      let m = sel.match(/^meta\[name="([^"]+)"\]$/);
      if (m) return byName[m[1]] || null;
      if (sel === "[data-apple-signin]") return btn;
      if (/^script\[src\*=/.test(sel)) return null; // no SDK script preloaded in the sandbox
      return null;
    },
    getElementById: (id) => byId[id] || null,
    addEventListener() {},
    _meta(name, content) { const e = makeEl("meta"); e.setAttribute("name", name); e.setAttribute("content", content); byName[name] = e; return e; },
    _button() { btn = makeEl("button"); btn.setAttribute("data-apple-signin", ""); return btn; },
  };
  return doc;
}

// Drain the loadSDK -> hash -> init/signIn -> fetch -> redirect promise chain. crypto.subtle
// can resolve on a threadpool tick, so flush several full event-loop turns.
async function drain(n = 6) { for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 0)); }

function harness({ clientId = "fyi.rogerai.web", redirectURI = "https://rogerai.fyi/login.html", appleResp, fetchResp = { ok: true } } = {}) {
  const doc = makeDoc();
  doc._meta("appleid-signin-client-id", clientId);
  doc._meta("appleid-signin-redirect-uri", redirectURI);
  const btn = doc._button();
  btn.hidden = true; // ships hidden in login.html; the module reveals it only when configured

  const initCalls = [];
  const signInCalls = [];
  const win = {
    AppleID: {
      auth: {
        init: (cfg) => initCalls.push(cfg),
        signIn: () => { signInCalls.push(1); return Promise.resolve(appleResp || { authorization: { id_token: "IDTOK", code: "CODE" } }); },
      },
    },
  };
  const fetchCalls = [];
  const fetch = (url, opts) => { fetchCalls.push({ url, opts }); return Promise.resolve(fetchResp); };
  const replaced = [];
  const location = { replace: (u) => replaced.push(u) };

  // Same injection style as session.test.mjs, plus crypto (Web Crypto) and a stubbed AppleID.
  new Function("window", "document", "fetch", "location", "crypto", APPLE_SRC)(win, doc, fetch, location, webcrypto);
  return { btn, initCalls, signInCalls, fetchCalls, replaced };
}

test("configured: clicking the Apple button runs the SiwA popup flow and POSTs to /auth/apple", async () => {
  const h = harness({});
  await drain(1);
  assert.equal(h.btn.hidden, false, "the button stays visible once a Services ID is configured");

  h.btn.dispatch("click");
  await drain();

  assert.equal(h.initCalls.length, 1, "AppleID.auth.init called once");
  assert.equal(h.initCalls[0].clientId, "fyi.rogerai.web", "init uses the Services ID as client_id");
  assert.equal(h.initCalls[0].usePopup, true, "popup mode so the id_token returns to JS");
  assert.equal(h.initCalls[0].redirectURI, "https://rogerai.fyi/login.html", "init uses the configured Return URL");
  assert.equal(h.signInCalls.length, 1, "AppleID.auth.signIn called once");

  assert.equal(h.fetchCalls.length, 1, "exactly one POST");
  assert.equal(h.fetchCalls[0].url, "https://broker.rogerai.fyi/auth/apple", "POSTs to the broker /auth/apple");
  assert.equal(h.fetchCalls[0].opts.method, "POST");
  assert.equal(h.fetchCalls[0].opts.credentials, "include", "credentialed so the broker can set the web session cookie");
  const body = JSON.parse(h.fetchCalls[0].opts.body);
  assert.equal(body.identity_token, "IDTOK", "posts Apple's id_token");
  assert.ok(typeof body.raw_nonce === "string" && body.raw_nonce.length >= 16, "posts the raw nonce pre-image");

  // CONTRACT: the nonce handed to Apple is HEX(sha256(raw_nonce)) — the exact value the broker's
  // Apple bind recomputes (hex(sha256(raw_nonce))). Apple echoes it verbatim into
  // id_token.nonce, so the broker's constant-time compare passes.
  const expected = createHash("sha256").update(body.raw_nonce).digest("hex");
  assert.equal(h.initCalls[0].nonce, expected, "Apple nonce == hex(sha256(raw_nonce)) (broker contract)");

  assert.deepEqual(h.replaced, ["/dashboard.html"], "on 200, lands on the dashboard exactly like the GitHub login");
});

test("not configured (no Services ID yet): the button is hidden and no SDK/flow runs", async () => {
  const h = harness({ clientId: "" });
  await drain(1);
  assert.equal(h.btn.hidden, true, "an unconfigured Apple button is hidden, never half-wired");
  h.btn.dispatch("click");
  await drain();
  assert.equal(h.initCalls.length, 0, "no AppleID init without a Services ID");
  assert.equal(h.fetchCalls.length, 0, "no POST without a Services ID");
});

test("broker rejects the bind (non-200): no dashboard redirect, button re-enabled for retry", async () => {
  const h = harness({ fetchResp: { ok: false, status: 401 } });
  await drain(1);
  h.btn.dispatch("click");
  await drain();
  assert.equal(h.fetchCalls.length, 1, "it still POSTed the bind");
  assert.deepEqual(h.replaced, [], "no redirect when the broker rejects");
  assert.equal(h.btn.disabled, false, "the button is re-enabled so the user can retry");
});
