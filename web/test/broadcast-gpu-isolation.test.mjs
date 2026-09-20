// Broadcast 014 ("The wall between your GPU and your files...") makes a security claim
// grounded directly in the shipped Go source: the wire protocol (protocol.Job/JobResult,
// internal/protocol/protocol.go) can only carry a chat-completions or audio-speech body, the
// node's serve loop (internal/agent/agent.go) is a pure outbound proxy with no tool/function-
// call interpretation, and the tool-executing harness (internal/harness) is imported only by
// internal/tui and internal/webui - never by the node/agent/sharing code path. These
// assertions pin the page's wiring and its most load-bearing factual claims so a future edit
// cannot quietly drift from what the code actually does.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const ROOT = path.join(WEB, "..");
const PAGE = "broadcasts-sharing-your-gpu-is-safe.html";
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const readSrc = (p) => readFileSync(path.join(ROOT, p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- wired like every other broadcast ---------------------------------- */

test("gpu-isolation: the broadcast builds, is indexable, and is in the sitemap", () => {
  assert.doesNotMatch(read(PAGE), /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), new RegExp(`<loc>https://rogerai\\.fm/${PAGE}</loc>`));
});

test("gpu-isolation: the transmission log carries it, newest first, as 014", () => {
  const log = read("broadcasts.html");
  const first = log.indexOf(`href="/${PAGE}"`);
  const prev = log.indexOf('href="/broadcasts-agent-governance-identity.html"');
  assert.ok(first > 0 && prev > 0 && first < prev, "014 sits above 013");
  assert.match(log, /BROADCAST 014/);
});

test("gpu-isolation: the FAQPage JSON-LD parses and mirrors the visible FAQ", () => {
  const html = read(PAGE);
  const m = html.match(/<script type="application\/ld\+json">(\{"@context":"https:\/\/schema\.org","@type":"FAQPage".*?)<\/script>/s);
  assert.ok(m, "a FAQPage block exists");
  const faq = JSON.parse(m[1]);
  assert.equal(faq["@type"], "FAQPage");
  assert.ok(faq.mainEntity.length >= 6, `at least 6 questions (got ${faq.mainEntity.length})`);
  for (const q of faq.mainEntity) {
    assert.equal(q["@type"], "Question");
    assert.ok(html.includes(`<dt><b>${q.name}</b></dt>`), `visible FAQ carries: ${q.name}`);
  }
  const visible = [...html.matchAll(/<dt><b>/g)].length;
  assert.equal(visible, faq.mainEntity.length, "schema and visible FAQ have the same count");
});

test("gpu-isolation: the hero and OG images are real shipped assets", () => {
  const html = read(PAGE);
  assert.match(html, /assets\/broadcasts\/gpu-wall-hero\.png/);
  readFileSync(path.join(WEB, "dist", "assets", "broadcasts", "gpu-wall-hero.png"));
  assert.match(html, /property="og:image" content="https:\/\/rogerai\.fm\/assets\/broadcasts\/gpu-wall-og\.png"/);
  readFileSync(path.join(WEB, "dist", "assets", "broadcasts", "gpu-wall-og.png"));
});

/* ---- the claims trace to the actual source, not just to this page ------ */

test("gpu-isolation: the wire protocol really is limited to chat/audio paths, per internal/protocol/protocol.go", () => {
  const proto = readSrc("internal/protocol/protocol.go");
  assert.match(proto, /type Job struct/);
  assert.match(proto, /Body\s+json\.RawMessage/, "Job carries a raw request body, not a structured command");
  assert.match(proto, /\/v1\/audio\/speech/, "the only non-default path documented is audio speech");
  assert.doesNotMatch(proto, /"shell"|"exec"|"run_command"|RunShell/i, "the wire protocol names no execution primitive");
});

// grep exits 1 (not 0) when it finds nothing, which execFileSync treats as a thrown
// error - so "no matches" is the expected, passing outcome here and must be caught,
// while a real grep failure (exit >1) or an actual match (a real violation) must not be.
function grepFiles(pattern, dirs) {
  try {
    return execFileSync("grep", ["-rlE", pattern, ...dirs], { cwd: ROOT, encoding: "utf8" })
      .trim().split("\n").filter(Boolean);
  } catch (e) {
    if (e.status === 1) return []; // no matches - the clean, expected case
    throw e;
  }
}

test("gpu-isolation: the tool-executing harness is never imported by the node/sharing path", () => {
  const importers = grepFiles("rogerai\\.fm/roger/v6/internal/harness", ["cmd/rogerai", "internal/agent", "internal/node"]);
  assert.deepEqual(importers, [],
    `the sharing path must never import internal/harness (the tool-executing agent), found: ${importers.join(", ")}`);
});

test("gpu-isolation: the node's serve path forwards job.Body verbatim to cfg.Upstream, with no tool-call branch", () => {
  const agent = readSrc("internal/agent/agent.go");
  assert.match(agent, /cfg\.Upstream/, "the node forwards to the operator's own upstream server");
  assert.doesNotMatch(agent, /tool_call|function_call|ToolCall/i,
    "the node's serve loop must not interpret tool/function calls server-side");
});

test("gpu-isolation: no inbound listener exists in the share/node code path", () => {
  const files = grepFiles("ListenAndServe|net\\.Listen\\(", ["cmd/rogerai", "internal/agent", "internal/node"]);
  // onboard.go's 127.0.0.1 OAuth-callback listener is the one known, unrelated exception.
  for (const f of files) {
    assert.match(f, /onboard\.go$/, `unexpected inbound listener outside onboard.go: ${f}`);
  }
});

test("gpu-isolation: no em dashes in the copy", () => {
  assert.doesNotMatch(read(PAGE), /—/, "founder style: spaced hyphens, never em dashes");
});
