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
  vanityImportRule,
  vanityRedirectDrift,
  canonicalSiteReady,
  parseHeaders,
} from "../scripts/cf-edge.mjs";
import * as edge from "../scripts/cf-edge.mjs";

const CANON = "rogerai.fm";
const LEGACY = "rogerai.fyi";

// A redirect must never swallow anything under /.well-known/. This is not hypothetical: the
// www->apex rule once 301'd /.well-known/acme-challenge/... on www.rogerai.fm, so the
// certificate authority could never read the token there and www sat unissued (DigitalOcean
// reported DomainCertPendingValidation for hours) while the apex, which has no redirect,
// validated first try. The apple-app-site-association file next to it fails the same way for
// iOS universal links. Any redirect we own has both failure modes at every renewal.
const WELL_KNOWN_EXCLUSION =
  'not starts_with(http.request.uri.path, "/.well-known/")';

// Split `(<host test> and <well-known guard>)` into its parts so the host assertions below
// stay as strict as they were before the guard existed.
function parts(expression) {
  const m = /^\((.*)\)$/.exec(expression.trim());
  assert.ok(m, `expression must be parenthesised, got: ${expression}`);
  const inner = m[1];
  const i = inner.indexOf(" and " + WELL_KNOWN_EXCLUSION);
  return { host: i === -1 ? inner : inner.slice(0, i), guarded: i !== -1 };
}

// A Cloudflare expression of the exact form `http.host in {"a" "b"}` matches those hosts
// and nothing else - no subdomains, no suffix matching. Asserting on that shape (rather than
// on a substring) is what lets us prove broker/control are excluded.
function hostSet(expression) {
  const m = /^http\.host in \{((?:\s*"[^"]+")+)\s*\}$/.exec(parts(expression).host.trim());
  assert.ok(
    m,
    `expression must be an exact host-set membership test so non-listed hosts (broker, control) ` +
      `cannot match, got: ${expression}`,
  );
  return [...m[1].matchAll(/"([^"]+)"/g)].map((x) => x[1]).sort();
}

// A single-host equality expression is likewise exact.
function singleHost(expression) {
  const m = /^http\.host eq "([^"]+)"$/.exec(parts(expression).host.trim());
  assert.ok(m, `expression must be an exact single-host test, got: ${expression}`);
  return m[1];
}

// Behavioural, not string-shaped: read every `not starts_with(path, "PREFIX")` guard out of
// a rule expression and answer whether samplePath is EXCLUDED (some guard prefix matches it),
// i.e. the redirect would leave that path alone and let the origin serve it directly.
function guardExcludes(expression, samplePath) {
  const prefixes = [
    ...expression.matchAll(/not starts_with\(http\.request\.uri\.path, "([^"]+)"\)/g),
  ].map((m) => m[1]);
  return prefixes.some((prefix) => samplePath.startsWith(prefix));
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

// Build every redirect rule the module exports, by SWEEP rather than by list. A hardcoded
// list is how the vanity-import rule reached the live zone with no ACME guard and no test:
// the check kept passing because it never knew the new rule existed. A rule now inherits the
// guarantees below simply by being exported.
function allRedirectRules() {
  const opts = { zone: CANON, legacyZone: LEGACY, canonicalZone: CANON };
  const out = {};
  for (const [name, fn] of Object.entries(edge)) {
    if (typeof fn !== "function" || !name.endsWith("Rule")) continue;
    const rule = name === "headerRule" ? fn(parseHeaders(), opts) : fn(opts);
    if (rule && rule.action === "redirect") out[name] = rule;
  }
  return out;
}

test("every exported redirect rule is swept, not enumerated", () => {
  const names = Object.keys(allRedirectRules());
  // If a redirect builder is added, it must appear here automatically.
  for (const required of ["redirectRule", "legacyRedirectRule", "vanityImportRule"]) {
    assert.ok(names.includes(required), `${required} must be swept as a redirect rule`);
  }
});

test("no redirect we own may swallow the ACME challenge path", () => {
  // Certificate issuance and renewal fetch http://<host>/.well-known/acme-challenge/<token>.
  // A redirect that matches it sends the CA to the wrong host and validation never completes.
  const redirects = allRedirectRules();
  assert.ok(Object.keys(redirects).length >= 3, "the sweep found no redirects to guard");

  for (const [name, rule] of Object.entries(redirects)) {
    assert.equal(
      parts(rule.expression).guarded,
      true,
      `the ${name} redirect must exclude /.well-known/acme-challenge/, or the certificate ` +
        `authority gets a 301 instead of the token and the hostname never gets a certificate`,
    );
    assert.equal(
      guardExcludes(rule.expression, "/.well-known/acme-challenge/tok123"),
      true,
      `${name} must let the ACME challenge path through untouched`,
    );
  }
});

// The same failure mode as ACME, one directory over: Apple fetches
// /.well-known/apple-app-site-association WITHOUT following redirects to validate the iOS
// app's Associated Domains. A 301 there drops the association, so a legacy rogerai.fyi
// universal link (the Base Station /r.html* device link) stops opening the app. The
// migration spec requires "both associated-domain files are served directly", so the legacy
// redirect must leave the whole /.well-known/ directory - not just acme-challenge - alone.
test("no redirect we own may swallow the iOS app-site-association (or anything under /.well-known/)", () => {
  for (const [name, rule] of Object.entries(allRedirectRules())) {
    for (const path of [
      "/.well-known/apple-app-site-association",
      "/.well-known/acme-challenge/tok",
    ]) {
      assert.equal(
        guardExcludes(rule.expression, path),
        true,
        `the ${name} redirect must serve ${path} directly, never 301 it`,
      );
    }
  }
});

// `go get rogerai.fm/roger` fetches the module path with NO trailing slash, and this host
// does no extensionless resolution, so /roger 404s while /roger/ serves the go-import page.
// This rule is the only thing that makes the module go-gettable at all - and it is PUT to
// the live zone, so its shape is production behavior, not configuration.
test("the vanity-import redirect is scoped to the apex and preserves the go-get query", () => {
  const rule = vanityImportRule({ zone: CANON });
  const { host } = parts(rule.expression);

  assert.match(host, new RegExp(`http\\.host eq "${CANON}"`), "must be scoped to the apex host");
  assert.doesNotMatch(host, /broker|control/, "must never match the API hosts");

  // Exact path membership, not a prefix: `starts_with(..., "/roger")` would also swallow
  // /roger-ios, /rogerai, and every future page whose name begins with "roger".
  const paths = /http\.request\.uri\.path in \{((?:\s*"[^"]+")+)\s*\}/.exec(host);
  assert.ok(paths, "the path test must be exact set membership, never a prefix match");
  assert.doesNotMatch(host, /starts_with\(http\.request\.uri\.path, "\/roger"\)/);

  const covered = [...paths[1].matchAll(/"([^"]+)"/g)].map((x) => x[1]).sort();
  // Go fetches the FULL module path, suffix included. Covering only /roger would leave
  // `go install rogerai.fm/roger/v6/...` resolving nothing, which is the whole point.
  // Sorted, so the majors read in order. The superseded major stays covered: its tags
  // still name it in go.mod, so dropping it would break `go install .../v5/...` for
  // everyone who has not migrated.
  assert.deepEqual(covered, ["/roger", "/roger/v5", "/roger/v6"]);

  const from = rule.action_parameters.from_value;
  assert.equal(from.status_code, 301, "a permanent move, so the module path is cacheable");
  // Go appends ?go-get=1 and drops the response if the redirect eats it, so this flag is
  // the difference between a working `go install` and a module nobody can fetch.
  assert.equal(from.preserve_query_string, true, "?go-get=1 must survive the hop");
  // The trailing slash has to be derived from the requested path, or /roger/v5 would land
  // on /roger/ and serve a tag for the wrong module path.
  assert.match(from.target_url.expression, /http\.request\.uri\.path/);
  assert.match(from.target_url.expression, /"\/"/, "must land on the trailing-slash page");
});

test("the header rule still applies to the ACME path", () => {
  // Only redirects are dangerous here. Setting response headers on the challenge response is
  // harmless, so the header rule must NOT carry the exclusion - narrowing it would silently
  // drop the security headers on a real path.
  assert.equal(parts(headerRule(parseHeaders(), { zone: CANON }).expression).guarded, false);
});

// The --check block is what CI runs against the live edge, so its judgement is production
// behaviour. It used to live inline in the CLI where no test could reach it, and a
// behaviour change shipped there with zero coverage. These pin the four ways it can be wrong.
test("the vanity drift check accepts only the exact host and path, with the go-get query", () => {
  const ok = vanityRedirectDrift(301, "https://rogerai.fm/roger/v5/?go-get=1", "/roger/v5", "rogerai.fm");
  assert.equal(ok, null, "the correct redirect is not drift");

  // Wrong host: a pathname-only assertion would call these "in sync".
  for (const wrong of [
    "https://rogerai.fyi/roger/v5/?go-get=1",
    "https://www.rogerai.fm/roger/v5/?go-get=1",
  ]) {
    assert.match(
      vanityRedirectDrift(301, wrong, "/roger/v5", "rogerai.fm") || "",
      /drift/,
      `${wrong} must be reported: it is a different origin`,
    );
  }

  // Collapsed path: /roger landing on /roger/v5/ serves a tag for the wrong module path.
  assert.match(
    vanityRedirectDrift(301, "https://rogerai.fm/roger/v5/?go-get=1", "/roger", "rogerai.fm") || "",
    /drift/,
    "a prefix test would wrongly accept this",
  );

  // Query dropped: Go abandons the fetch, so a 301 to the right path is still broken.
  assert.match(
    vanityRedirectDrift(301, "https://rogerai.fm/roger/v5/", "/roger/v5", "rogerai.fm") || "",
    /go-get=1/,
    "losing ?go-get=1 must be reported",
  );

  // Not a redirect at all, and a malformed Location, must both be caught rather than throw.
  assert.match(vanityRedirectDrift(200, "", "/roger/v5", "rogerai.fm") || "", /drift/);
  assert.match(vanityRedirectDrift(301, "not a url", "/roger/v5", "rogerai.fm") || "", /drift/);
});
