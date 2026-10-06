// account-keys: the keys page's "Account keys" section (web/src/js/account-keys.js), executed.
//
// The module is a browser IIFE (window.RogerAccountKeys, and module.exports for here), run in a
// tiny sandbox: its request builder against a recording fetch, and its renderers on real key
// shapes. The same module is driven against the real broker by the Go scenarios
// (features/auth/key_guardrails.feature, via test/support/account-keys-probe.mjs).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const src = readFileSync(path.join(WEB, "src/js/account-keys.js"), "utf8");
const mod = { exports: {} };
new Function("module", "window", src)(mod, undefined);
const A = mod.exports;

function recorder(responses) {
  const calls = [];
  const fetchImpl = (url, opts) => {
    calls.push({ url, ...opts });
    const r = responses.shift() || { status: 200, body: {} };
    return Promise.resolve({
      ok: r.status >= 200 && r.status < 300, status: r.status,
      json: () => Promise.resolve(r.body),
    });
  };
  return { calls, api: A.makeApi("https://broker.example", fetchImpl) };
}

const KEY = { id: "key_1", name: "ci", hint: "...abcd", limit_usd: 5, reset: "weekly", usage_weekly: 1.25,
  expires_at: null, disabled: false, expired: false };

test("account keys: list, mint, edit and revoke use the /account/keys endpoints with JSON bodies", async () => {
  const { calls, api } = recorder([
    { status: 200, body: { keys: [KEY] } },
    { status: 201, body: { ...KEY, id: "key_2", secret: "rog-key_SECRET" } },
    { status: 200, body: { ...KEY, limit_usd: 2 } },
    { status: 204, body: null },
  ]);
  assert.deepEqual(await A.list(api), [KEY]);
  const minted = await A.mint(api, { name: "ci" });
  assert.equal(minted.secret, "rog-key_SECRET");
  assert.equal((await A.update(api, "key_1", { limit_usd: 2 })).limit_usd, 2);
  await A.revoke(api, "key_1");

  assert.deepEqual(calls.map((c) => c.method + " " + c.url), [
    "GET https://broker.example/account/keys",
    "POST https://broker.example/account/keys",
    "PATCH https://broker.example/account/keys/key_1",
    "DELETE https://broker.example/account/keys/key_1",
  ]);
  for (const c of calls) {
    assert.equal(c.credentials, "include", "the session cookie is the auth");
    assert.ok(!c.url.includes("rog-key_"), "a secret never rides in a URL");
  }
  assert.equal(calls[0].body, undefined);
  assert.deepEqual(JSON.parse(calls[1].body), { name: "ci" });
  assert.equal(calls[1].headers["Content-Type"], "application/json");
  assert.deepEqual(JSON.parse(calls[2].body), { limit_usd: 2 });
  assert.equal(calls[2].headers["Content-Type"], "application/json");
});

test("account keys: a refused request rejects with the broker's message", async () => {
  const { api } = recorder([{ status: 400, body: { error: { code: "invalid_key_field", message: "limit_usd must be a number >= 0" } } }]);
  await assert.rejects(A.mint(api, { limit_usd: -1 }), /limit_usd must be a number >= 0/);
  const { api: api2 } = recorder([{ status: 502, body: null }]);
  await assert.rejects(A.list(api2), /502/);
  const { api: api3 } = recorder([{ status: 401, body: { error: { message: "log in" } } }]);
  await assert.rejects(A.list(api3), (e) => e.status === 401);
});

test("account keys: rows show id, name, hint, limit, used, resets, expires and state, escaped", () => {
  const html = A.rowsHTML([
    KEY,
    { id: "key_<2>", name: "<b>x</b>", hint: "...ef01", limit_usd: 0, reset: "none", usage: 3, expires_at: "2027-01-01T00:00:00Z", disabled: true },
  ]);
  for (const s of ["key_1", "ci", "...abcd", "$5.00", "$1.25", "weekly", "never", "active",
    "key_&lt;2&gt;", "&lt;b&gt;x&lt;/b&gt;", "unlimited", "$3.00", "2027-01-01", "disabled"]) {
    assert.ok(html.includes(s), `rows show ${s}`);
  }
  assert.ok(!html.includes("<b>x</b>"), "a key name is text, never markup");
  assert.match(html, /data-act="revoke" data-id="key_1"/);
  assert.match(html, /data-act="toggle" data-id="key_1"/);
  assert.match(html, /data-act="limit" data-id="key_1"/);
  assert.equal(A.rowsHTML([]), "");
});

test("account keys: the reveal shows the secret once, a copy control and the never-again warning", () => {
  const html = A.revealHTML("rog-key_<SECRET>");
  assert.equal(html.split("rog-key_&lt;SECRET&gt;").length - 1, 1, "the secret appears exactly once");
  assert.match(html, /<button type="button" class="kf__copy"[^>]*>Copy<\/button>/);
  assert.match(html.toLowerCase(), /never shown again/);
});

test("account keys: the section uses only the keys page's existing classes, no inline style", () => {
  const css = readdirSync(path.join(WEB, "src/styles")).filter((f) => f.endsWith(".css"))
    .map((f) => readFileSync(path.join(WEB, "src/styles", f), "utf8")).join("\n");
  const page = readFileSync(path.join(WEB, "src/keys.html"), "utf8");
  const section = page.match(/<section class="panel" id="acctKeys">[\s\S]*?<\/section>/)?.[0] || "";
  assert.ok(section, "keys.html has the Account keys section");
  assert.match(section, /<h2>Account keys<\/h2>/);
  assert.match(page, /<h2>Grant keys<\/h2>/, "grant keys keep their own section beside it");
  const rendered = section + A.rowsHTML([KEY]) + A.revealHTML("rog-key_x");
  assert.ok(!/style="/.test(rendered), "no inline style");
  const classes = new Set([...rendered.matchAll(/class="([^"]+)"/g)].flatMap((m) => m[1].split(/\s+/)));
  for (const c of classes) {
    assert.ok(new RegExp("\\." + c.replace(/[-_]/g, (x) => "\\" + x) + "\\b").test(css), `class .${c} is an existing style`);
  }
});
