// account-keys-probe: runs the keys page's account-keys module (src/js/account-keys.js) in node
// against a real broker, for the Go scenarios in features/auth/key_guardrails.feature
// (cmd/rogerai-broker/key_web_bdd_test.go). It is not a test of its own and is not in the
// web suite's glob. The browser's cookie jar and Origin are the two things node lacks, so the
// fetch it hands the module adds them: COOKIE and ORIGIN, against BROKER.
//
//   node account-keys-probe.mjs list   -> { ids, html }      the section's rows
//   node account-keys-probe.mjs mint   -> { secret, html }   the one-time reveal
//   node account-keys-probe.mjs flow   -> { secret, calls }  list, mint, edit x2, revoke
import { readFileSync } from "node:fs";

const src = readFileSync(new URL("../../src/js/account-keys.js", import.meta.url), "utf8");
const mod = { exports: {} };
new Function("module", "window", src)(mod, undefined);
const A = mod.exports;

const { BROKER, COOKIE, ORIGIN } = process.env;
const calls = [];
const api = A.makeApi(BROKER, (url, opts) => {
  calls.push({ url, method: opts.method, body: opts.body ?? "", contentType: opts.headers["Content-Type"] ?? "" });
  return fetch(url, { ...opts, headers: { ...opts.headers, Cookie: COOKIE, Origin: ORIGIN } });
});

const out = {};
switch (process.argv[2]) {
  case "list": {
    const keys = await A.list(api);
    out.ids = keys.map((k) => k.id);
    out.html = A.rowsHTML(keys);
    break;
  }
  case "mint": {
    const k = await A.mint(api, { name: "web-mint" });
    out.secret = k.secret;
    out.html = A.revealHTML(k.secret);
    break;
  }
  case "flow": {
    await A.list(api);
    const k = await A.mint(api, { name: "web-flow", limit_usd: 5, reset: "weekly" });
    await A.update(api, k.id, { limit_usd: 2 });
    await A.update(api, k.id, { disabled: true });
    await A.revoke(api, k.id);
    out.secret = k.secret;
    out.calls = calls;
    break;
  }
  default:
    throw new Error("usage: account-keys-probe.mjs list|mint|flow");
}
process.stdout.write(JSON.stringify(out));
