// build.mjs used to start by deleting dist/ outright. Anything reading dist/ at that moment
// - the web gate's own suite, another agent session's test run, a dev server - sees files
// that plainly exist vanish, and fails with ENOENT on a path that is correct.
//
// That is not hypothetical: it cost a push in this repo on 2026-09-01. The gate failed with
// "ENOENT: dist/research-wave-family.html" because a foreground build in the same worktree
// had just emptied the directory underneath it. CLAUDE.md is explicit that several sessions
// run against this repo at once, so a build that is unsafe to run twice is a trap.
//
// The build now leaves dist/ in place, writes every file atomically, and prunes what it did
// not write. A reader therefore sees either the old complete file or the new one, never a
// missing or half-written one.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync, rmSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const build = () => execFileSync("node", ["build.mjs"], { cwd: WEB });

before(build);

test("build: dist is not destroyed and recreated between builds", () => {
  // The directory identity is the proxy for "a reader never saw it disappear": rmSync +
  // mkdirSync gives a NEW inode every build, which is exactly the window that broke a push.
  const before = statSync(DIST).ino;
  build();
  assert.equal(statSync(DIST).ino, before,
    "dist/ was replaced rather than updated in place - a concurrent reader would have hit ENOENT");
});

test("build: a file present throughout a rebuild is never missing", () => {
  const probe = path.join(DIST, "index.html");
  assert.ok(existsSync(probe), "the probe file exists before");
  const before = readFileSync(probe, "utf8");
  build();
  assert.ok(existsSync(probe), "and still exists after");
  assert.equal(readFileSync(probe, "utf8"), before, "with identical content for an unchanged source");
});

test("build: output that no longer has a source is pruned", () => {
  // Keeping dist means stale output would otherwise live forever - a page deleted from src
  // would keep being served, which is worse than the bug this replaces.
  const stale = path.join(DIST, "zz-stale-probe.html");
  writeFileSync(stale, "<!-- left over from an older build -->");
  build();
  assert.ok(!existsSync(stale), "a file the build did not write must be removed");
});

test("build: pruning does not reach outside dist", () => {
  // A prune with a bad root is how a build deletes a source tree. Assert the obvious.
  const canary = path.join(WEB, "src", "index.html");
  assert.ok(existsSync(canary), "the source survived the build");
  build();
  assert.ok(existsSync(canary), "and survives another one");
});

test("build: two builds at once both finish and leave a complete dist", async () => {
  // The actual scenario: a gate running the suite while somebody rebuilds.
  const { execFile } = await import("node:child_process");
  const once = () => new Promise((res, rej) =>
    execFile("node", ["build.mjs"], { cwd: WEB }, (e) => (e ? rej(e) : res())));
  await Promise.all([once(), once(), once()]);
  for (const f of ["index.html", "research-hardware.html", "sitemap.xml", "llms.txt"]) {
    assert.ok(existsSync(path.join(DIST, f)), `${f} survived concurrent builds`);
    assert.ok(statSync(path.join(DIST, f)).size > 0, `${f} is not truncated`);
  }
});
