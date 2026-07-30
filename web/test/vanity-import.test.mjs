// vanity-import.test.mjs asserts the built site can actually resolve the Go module path.
//
// `go get rogerai.fm/roger` fetches https://rogerai.fm/roger?go-get=1 and reads a go-import
// meta tag out of the response. If that document is missing from dist/, the module is
// unbuildable for everyone outside this checkout, and nothing else in the suite would notice.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const DIST = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "dist");
const MODULE_PATH = "rogerai.fm/roger";
const PAGE = path.join(DIST, "roger", "index.html");

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
  assert.equal(module, MODULE_PATH, "the declared module must equal the go.mod module path");
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
