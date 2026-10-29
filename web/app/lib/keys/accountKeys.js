// Account keys: keys that call the API AS YOU, with their own spend limits, over the broker's
// credentialed /account/keys endpoints (features/auth/key_guardrails.feature). The session
// cookie is the auth; every write sends a JSON body; a key secret never touches a URL,
// storage or the log, and is shown once, at mint. (Legacy: web/src/js/account-keys.js.)
//
//   GET    /account/keys         -> { keys: [ key ] }
//   POST   /account/keys  {name, limit_usd, reset}       -> key + secret (shown ONCE)
//   PATCH  /account/keys/{id}  {limit_usd? | disabled?}  -> key
//   DELETE /account/keys/{id}                            -> 204 (revoked)
//
// Pure, and shared three ways: the keys island (lib/keys/KeysApp.svelte) renders the rows and
// the reveal with these; the Go scenarios execute them in node against a real broker
// (test/support/account-keys-probe.mjs); test/account-keys.test.mjs pins them.
export const WARNING = "Store it now - it is never shown again.";

// makeApi builds a request function over fetchImpl: credentialed, JSON in and out.
export function makeApi(base, fetchImpl) {
  return function (method, path, body) {
    const opts = { method, credentials: "include", headers: {} };
    if (body !== undefined) {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    return fetchImpl(base + path, opts).then((r) => {
      if (r.status === 204) return null;
      return r.json().catch(() => null).then((j) => {
        if (!r.ok) {
          const e = new Error(j && j.error && j.error.message ? j.error.message : "the broker answered " + r.status);
          e.status = r.status;
          throw e;
        }
        return j;
      });
    });
  };
}

export const list = (api) => api("GET", "/account/keys").then((j) => (j && j.keys) || []);
export const mint = (api, fields) => api("POST", "/account/keys", fields);
export const update = (api, id, fields) => api("PATCH", "/account/keys/" + encodeURIComponent(id), fields);
export const revoke = (api, id) => api("DELETE", "/account/keys/" + encodeURIComponent(id));

export const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
export const usd = (n) => "$" + (Number(n) || 0).toFixed(2);

// parseLimit reads a typed spend limit strictly: a plain non-negative number of dollars ("0"
// means no limit). null (a cancelled dialog) and "" are a choice not to set one, never 0; a
// typo such as "abc", "$5" or "-1" is refused rather than read as 0, which would remove the cap.
export function parseLimit(s) {
  if (s == null) return { none: true };
  const t = String(s).trim();
  if (t === "") return { none: true };
  if (!/^\d+(\.\d+)?$/.test(t)) return { error: "a spend limit is a number of dollars, like 5 or 2.50 (0 = no limit)" };
  return { value: Number(t) };
}

// used is the spend in the window the limit counts (reset none: the key's whole life).
export function used(k) {
  const w = { daily: k.usage_daily, weekly: k.usage_weekly, monthly: k.usage_monthly }[k.reset];
  return w == null ? k.usage : w;
}
export const state = (k) => (k.disabled ? "disabled" : k.expired ? "expired" : "active");
const PILL = { active: "kf__status--active", expired: "kf__status--expired", disabled: "kf__status--revoked" };

export function rowsHTML(keys) {
  return keys.map((k) => {
    const st = state(k), id = esc(k.id);
    return "<tr" + (st === "active" ? "" : ' class="kf__row--off"') + ">" +
      "<td><code>" + id + "</code></td>" +
      '<td class="mx-model">' + esc(k.name || "-") + "</td>" +
      "<td><code>" + esc(k.hint) + "</code></td>" +
      '<td class="num">' + (k.limit_usd > 0 ? usd(k.limit_usd) : "unlimited") + "</td>" +
      '<td class="num">' + usd(used(k)) + "</td>" +
      "<td>" + esc(k.reset) + "</td>" +
      "<td>" + (k.expires_at ? esc(String(k.expires_at).slice(0, 10)) : "never") + "</td>" +
      '<td><span class="kf__status ' + PILL[st] + '">' + st + "</span></td>" +
      '<td class="kf__act">' +
      '<button type="button" class="kf__rowbtn" data-act="limit" data-id="' + id + '">Limit</button>' +
      '<button type="button" class="kf__rowbtn" data-act="toggle" data-id="' + id + '">' + (k.disabled ? "Enable" : "Disable") + "</button>" +
      '<button type="button" class="kf__rowbtn kf__rowbtn--danger" data-act="revoke" data-id="' + id + '">Revoke</button>' +
      "</td></tr>";
  }).join("");
}

// revealHTML is the one-time secret block: the secret once, the shared copy control
// (data-copy-target, site.js) pointed at it, the warning, and Done to take it off the page.
export function revealHTML(secret) {
  return '<div class="kf__reveal-head"><span class="kf__reveal-tag">shown once</span>' +
    '<span class="kf__reveal-msg">' + esc(WARNING) + "</span></div>" +
    '<div class="kf__secret"><code class="kf__secret-val" id="akSecret">' + esc(secret) + "</code>" +
    '<button type="button" class="kf__copy" data-copy-target="#akSecret">Copy</button></div>' +
    '<button type="button" class="kf__rowbtn" data-reveal-done>Done</button>';
}
