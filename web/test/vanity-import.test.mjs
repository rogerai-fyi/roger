// vanity-import.test.mjs asserts the built site can actually resolve the Go module path.
//
// `go get rogerai.fm/roger/v6` fetches https://rogerai.fm/roger/v6?go-get=1 and reads a go-import
// meta tag out of the response. If that document is missing from dist/, the module is
// unbuildable for everyone outside this checkout, and nothing else in the suite would notice.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const DIST = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "dist");
const MODULE_PATH = "rogerai.fm/roger/v6";
// The superseded major still has to be served: its tags name it in go.mod, so it keeps
// resolving for anyone who has not migrated. Dropping the page would break their builds.
const LEGACY_MAJOR = "v5";
const PAGE = path.join(DIST, "roger", "index.html");
const ROOT_PREFIX = "rogerai.fm/roger";

test("the built site ships the go-import document at the module path", () => {
  assert.ok(
    existsSync(PAGE),
    "dist/roger/index.html is missing, so https://rogerai.fm/roger cannot answer go-get",
  );
});

test("the go-import tag maps the vanity module path to a real git repository", () => {
  const html = readFileSync(PAGE, "utf8");

  const m = /<meta\s+name="go-import"\s+content="([^"]+)"/.exec(html);
  assert.ok(m, "no go-import meta tag");

  const [module, vcs, repo] = m[1].trim().split(/\s+/);
  // The ROOT page declares the suffix-less prefix on purpose: a go-import prefix must be a
  // prefix of the URL Go requested, so ".../v5" here would make Go reject /roger outright.
  assert.equal(module, ROOT_PREFIX, "the root page declares the repo-root prefix");
  assert.ok(MODULE_PATH.startsWith(ROOT_PREFIX), "the module path must extend the root prefix");
  assert.equal(vcs, "git");
  assert.match(repo, /^https:\/\/\S+$/, "the repo must be an https clone URL");
});

test("the import path does not depend on the code host", () => {
  const html = readFileSync(PAGE, "utf8");
  const m = /<meta\s+name="go-import"\s+content="([^"]+)"/.exec(html);
  const [module] = m[1].trim().split(/\s+/);

  assert.ok(
    !/github\.com|gitlab\.com|bitbucket\.org/.test(module),
    `the module path ${module} names a code host; renaming the org would break every importer`,
  );
  // The host may appear only as the repo location, which is the point of the indirection.
  assert.match(html, /github\.com/, "the tag still has to say where the source actually lives");
});

test("the major-version path serves the tag too", () => {
  // Go fetches the FULL module path, so https://rogerai.fm/roger/v6?go-get=1 must answer.
  // Serving only /roger would resolve nothing for `go install rogerai.fm/roger/v6/...`.
  const versioned = path.join(DIST, "roger", "v6", "index.html");
  assert.ok(existsSync(versioned), "dist/roger/v6/index.html is missing");

  const html = readFileSync(versioned, "utf8");
  const m = /<meta\s+name="go-import"\s+content="([^"]+)"/.exec(html);
  assert.ok(m, "no go-import meta tag on the versioned page");
  assert.equal(m[1].trim().split(/\s+/)[0], MODULE_PATH);
});

test("the module path carries the major-version suffix", () => {
  // Without it Go only considers v0/v1 tags and `@latest` silently resolves to an ancient
  // release - the exact bug that had `go install` handing out v0.3.3 from a v5 repo.
  assert.match(MODULE_PATH, /\/v[2-9]\d*$/);
});

// A major bump ADDS a path, it does not replace one. The previous major's tags carry a
// go.mod naming that path, so `go install rogerai.fm/roger/v5/...` keeps resolving from
// them forever - and it only keeps working while this document is still served.
//
// This is a guard rather than a comment because the failure is invisible from inside the
// repo: everything here builds, the current major resolves, and the break lands only on
// somebody else's machine who has not migrated yet.
test("the superseded major-version path is still served", () => {
  const legacy = path.join(DIST, "roger", LEGACY_MAJOR, "index.html");
  assert.ok(
    existsSync(legacy),
    `dist/roger/${LEGACY_MAJOR}/index.html is missing: removing it breaks ` +
      `\`go install rogerai.fm/roger/${LEGACY_MAJOR}/...\` for everyone still on it`,
  );
  const html = readFileSync(legacy, "utf8");
  const m = /<meta\s+name="go-import"\s+content="([^"]+)"/.exec(html);
  assert.ok(m, `no go-import meta tag on the ${LEGACY_MAJOR} page`);
  assert.equal(m[1].trim().split(/\s+/)[0], `rogerai.fm/roger/${LEGACY_MAJOR}`);
});
