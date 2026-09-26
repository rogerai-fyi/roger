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
  // the page shows the WebP copies derived from the PNG master (scripts/derive-webp.mjs)
  assert.match(html, /src="assets\/broadcasts\/gpu-wall-hero\.webp(\?v=[0-9a-f]+)?"/);
  readFileSync(path.join(WEB, "dist", "assets", "broadcasts", "gpu-wall-hero.webp"));
  readFileSync(path.join(WEB, "dist", "assets", "broadcasts", "gpu-wall-hero-800.webp"));
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

// A first draft of this page said the allowlist was ONE path (chat completions) and "text
// only" - the merge-round audit caught that isAllowedUpstreamPath actually allows FOUR
// (plus blank as a chat alias), including two audio paths. Pinned two ways: against the
// source directly, and against the page's own enumeration, so the two cannot drift apart.
test("gpu-isolation: the upstream path allowlist is exactly four canonical paths (plus blank), per internal/agent/agent.go", () => {
  const agent = readSrc("internal/agent/agent.go");
  assert.match(agent, /func isAllowedUpstreamPath/);
  for (const p of ["/v1/chat/completions", "/chat/completions", "/v1/audio/speech", "/v1/audio/transcriptions"]) {
    assert.ok(agent.includes(`"${p}"`), `allowlist source names ${p}`);
  }
});

test("gpu-isolation: the page enumerates four endpoints and never claims 'text only'", () => {
  const html = read(PAGE);
  const visible = html.replace(/<!--[\s\S]*?-->/g, ""); // exclude the audit-trail comments, which discuss the retired claim by name
  assert.match(visible, /four (fixed )?(API (calls|endpoints)|allowed endpoints|values)/i);
  assert.doesNotMatch(visible, /text only/i, "audio in/out is real; 'text only' undersells and misstates the actual guarantee");
});

// The moderation broker is a real, shipped safety layer, but it fails OPEN on a classifier
// outage (cmd/rogerai-broker/moderation.go groqFailMode) even under require=1 - so the page
// must never claim screened content "never" reaches a station. Pinned so a future edit
// cannot quietly restore the overstated absolute the audit caught.
test("gpu-isolation: the moderation claim is not an unqualified absolute", () => {
  const mod = readSrc("cmd/rogerai-broker/moderation.go");
  assert.match(mod, /FAIL OPEN/, "moderation.go documents fail-open on a classifier outage");
  const html = read(PAGE);
  assert.doesNotMatch(html, /never gets that far/i);
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
  // Positive control first: if this pattern stops matching its two known, legitimate
  // importers (a module path bump, a directory rename), an empty result below would
  // otherwise look identical to a genuine "not imported" pass - the exact vacuous-pass
  // shape the listener test's own comment warns about.
  const control = grepFiles("rogerai\\.fm/roger/v6/internal/harness", ["internal/tui", "internal/webui"]);
  assert.ok(control.some((f) => f.startsWith("internal/tui/")), "the import pattern still matches internal/tui");
  assert.ok(control.some((f) => f.startsWith("internal/webui/")), "the import pattern still matches internal/webui");

  const importers = grepFiles("rogerai\\.fm/roger/v6/internal/harness", ["cmd/rogerai", "internal/agent", "internal/node"]);
  assert.deepEqual(importers, [],
    `the sharing path must never import internal/harness (the tool-executing agent), found: ${importers.join(", ")}`);
});

// A first draft claimed every harness action is "confirmed before it runs" - the
// merge-round audit caught that read_file is Mutating:false (auto-runs, loop.go's
// needsConfirm only gates Mutating tools) and that a permissive session (/perms all,
// /yolo) auto-approves the rest. Pinned against the source so the page's more careful
// "reads run on their own; writes/shell ask by default, unless permissive" claim cannot
// quietly regress back to the blanket overstatement.
test("gpu-isolation: the confirm-gate claim matches the actual default (reads auto-run, writes/shell ask, permissive mode can skip it)", () => {
  const tools = readSrc("internal/harness/tools.go");
  const loop = readSrc("internal/harness/loop.go");
  assert.match(tools, /read-only tools \(read\/list\/fetch\) auto-run/i, "Tool.Mutating's own doc comment states read-only tools auto-run");
  assert.match(tools, /Name:\s*"read_file"[\s\S]{0,600}Mutating:\s*false/, "read_file is non-mutating in source");
  assert.match(loop, /func \(l \*Loop\) needsConfirm/);
  assert.match(loop, /if t\.Mutating \{\s*return true/, "confirmation is gated on Mutating, so a non-mutating read is not forced through it");

  const html = read(PAGE);
  const visible = html.replace(/<!--[\s\S]*?-->/g, "");
  assert.doesNotMatch(visible, /confirmed before any of it runs/i,
    "reads auto-run by design; 'confirmed before any of it runs' overstates every tool, including reads");
  assert.match(visible, /permissive/i, "the permissive-mode bypass (/perms all, /yolo) is acknowledged, not hidden");
});

test("gpu-isolation: the node's serve path forwards job.Body verbatim to cfg.Upstream, with no tool-call branch", () => {
  const agent = readSrc("internal/agent/agent.go");
  assert.match(agent, /cfg\.Upstream/, "the node forwards to the operator's own upstream server");
  assert.doesNotMatch(agent, /tool_call|function_call|ToolCall/i,
    "the node's serve loop must not interpret tool/function calls server-side");
});

test("gpu-isolation: no inbound listener exists in the share/node code path", () => {
  const files = grepFiles("ListenAndServe|net\\.Listen\\(", ["cmd/rogerai", "internal/agent", "internal/node"]);
  // Asserted as an EXACT set, not a loop over matches: a loop passes vacuously if the
  // pattern or paths drift and grep starts matching nothing, which is exactly the failure
  // mode a security claim like this one cannot afford. onboard.go's 127.0.0.1 OAuth-callback
  // listener is the one known, unrelated exception - internal/webui and internal/tui also
  // bind loopback-only listeners for local features (browser console, TUI proxy), but they
  // sit outside these three directories entirely, so they never enter this comparison.
  assert.deepEqual(files, ["cmd/rogerai/onboard.go"],
    `expected only onboard.go's listener in the sharing path, found: ${files.join(", ") || "(none)"}`);
});

test("gpu-isolation: no em dashes in the copy", () => {
  assert.doesNotMatch(read(PAGE), /—/, "founder style: spaced hyphens, never em dashes");
});
