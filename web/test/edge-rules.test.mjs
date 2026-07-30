// edge-rules.test.mjs makes the "Edge rules are scoped by hostname" and "Website redirect
// waits for the canonical site" scenarios of features/domain/domain_operations.feature
// EXECUTABLE against the real rule builders in web/scripts/cf-edge.mjs.
//
// The scoping assertions matter because a too-broad expression is how a domain migration
// takes the API down: a redirect rule that matched broker.* would 301 streaming/WebSocket
// traffic, and the compatibility contract requires broker.rogerai.fyi to stay a live API
// host forever.

import test from "node:test";
import assert from "node:assert/strict";

import {
  headerRule,
  redirectRule,
  legacyRedirectRule,
  canonicalSiteReady,
  parseHeaders,
} from "../scripts/cf-edge.mjs";

const CANON = "rogerai.fm";
const LEGACY = "rogerai.fyi";

// A Cloudflare expression of the exact form `(http.host in {"a" "b"})` matches those hosts
// and nothing else - no subdomains, no suffix matching. Asserting on that shape (rather than
// on a substring) is what lets us prove broker/control are excluded.
function hostSet(expression) {
  const m = /^\(http\.host in \{((?:\s*"[^"]+")+)\s*\}\)$/.exec(expression.trim());
  assert.ok(
    m,
    `expression must be an exact host-set membership test so non-listed hosts (broker, control) ` +
      `cannot match, got: ${expression}`,
  );
  return [...m[1].matchAll(/"([^"]+)"/g)].map((x) => x[1]).sort();
}

// A single-host equality expression is likewise exact.
function singleHost(expression) {
  const m = /^\(http\.host eq "([^"]+)"\)$/.exec(expression.trim());
  assert.ok(m, `expression must be an exact single-host test, got: ${expression}`);
  return m[1];
}

test("canonical-site header rules match only the .fm apex and www", () => {
  const rule = headerRule(parseHeaders(), { zone: CANON });

  assert.deepEqual(hostSet(rule.expression), [CANON, `www.${CANON}`]);

  const hosts = hostSet(rule.expression);
  for (const excluded of [`broker.${CANON}`, `control.${CANON}`, LEGACY, `www.${LEGACY}`]) {
    assert.ok(!hosts.includes(excluded), `${excluded} must not receive the site header rule`);
  }

  // The rule must actually carry the repo's security headers, not just match the right hosts.
  const names = Object.keys(rule.action_parameters.headers).map((n) => n.toLowerCase());
  for (const required of [
    "content-security-policy",
    "strict-transport-security",
    "x-frame-options",
    "x-content-type-options",
    "referrer-policy",
  ]) {
    assert.ok(names.includes(required), `header rule must set ${required}`);
  }
});

test("the www redirect matches only www.rogerai.fm", () => {
  const rule = redirectRule({ zone: CANON });

  assert.equal(singleHost(rule.expression), `www.${CANON}`);
  assert.equal(rule.action, "redirect");
  assert.equal(rule.action_parameters.from_value.status_code, 301);
  assert.match(
    rule.action_parameters.from_value.target_url.expression,
    new RegExp(`https://${CANON.replace(".", "\\.")}`),
  );
  assert.equal(rule.action_parameters.from_value.preserve_query_string, true);
});

test("the legacy-site redirect matches only the .fyi apex and www, never broker or control", () => {
  const rule = legacyRedirectRule({ legacyZone: LEGACY, canonicalZone: CANON });

  assert.deepEqual(hostSet(rule.expression), [LEGACY, `www.${LEGACY}`]);

  const hosts = hostSet(rule.expression);
  for (const excluded of [`broker.${LEGACY}`, `control.${LEGACY}`]) {
    assert.ok(
      !hosts.includes(excluded),
      `${excluded} must keep serving its own traffic - redirecting it would break the ` +
        `documented compatibility contract for released clients`,
    );
  }
});

test("the legacy-site redirect is one permanent hop to the matching canonical path", () => {
  const rule = legacyRedirectRule({ legacyZone: LEGACY, canonicalZone: CANON });
  const from = rule.action_parameters.from_value;

  assert.equal(rule.action, "redirect");
  assert.equal(from.status_code, 301);
  assert.equal(from.preserve_query_string, true);

  // path-preserving: an old content URL must land on the same path on the canonical host,
  // not on the homepage (that would drop every inbound link and search result).
  const target = from.target_url.expression;
  assert.match(target, new RegExp(`https://${CANON.replace(".", "\\.")}`));
  assert.match(target, /http\.request\.uri\.path/);
  assert.ok(
    !/rogerai\.fyi/.test(target),
    "the redirect target must be the canonical host, not the legacy host",
  );
});

test("the legacy redirect waits for the canonical site to answer with a valid certificate", async () => {
  // Given rogerai.fm does not return the expected production page, the gate must refuse.
  const notLive = await canonicalSiteReady(CANON, async () => {
    throw Object.assign(new Error("unable to verify the first certificate"), {
      code: "CERT_HAS_EXPIRED",
    });
  });
  assert.equal(notLive.ready, false, "a TLS failure must not be treated as ready");
  assert.match(notLive.reason, /certificate|unreachable/i);

  const parked = await canonicalSiteReady(CANON, async () => ({ status: 404, headers: new Map() }));
  assert.equal(parked.ready, false, "a non-200 canonical site must not be treated as ready");

  const live = await canonicalSiteReady(CANON, async () => ({ status: 200, headers: new Map() }));
  assert.equal(live.ready, true, "a healthy canonical site unblocks the legacy redirect");
});
