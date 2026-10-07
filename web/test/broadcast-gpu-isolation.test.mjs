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
    // Production code only: a test file binding a socket or importing the harness to
    // exercise it is not the sharing path the page makes a promise about.
    return execFileSync("grep", ["-rlE", "--include=*.go", "--exclude=*_test.go", pattern, ...dirs], { cwd: ROOT, encoding: "utf8" })
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

  // The ONE allowed importer is the Roger Edge delegate runner (founder ruling 2026-10-07):
  // it runs an agent turn only on a grant the owner's own Edge authority signed, and only on
  // a machine that has joined that Edge. It is not reachable from sharing to the market.
  const importers = grepFiles("rogerai\\.fm/roger/v6/internal/harness", ["cmd/rogerai", "internal/agent", "internal/node"]);
  assert.deepEqual(importers, ["cmd/rogerai/edgerunner.go"],
    `the sharing path must never import internal/harness (the tool-executing agent) beyond the Edge delegate runner, found: ${importers.join(", ")}`);
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
  // Every listener now goes through internal/netbind (one bind helper), so the scan looks for
  // netbind's binding calls as well as a raw net.Listen. Positive controls first, so a pattern
  // or path drift that starts matching nothing cannot pass as "no listener".
  const pattern = "ListenAndServe|net\\.Listen\\(|netbind\\.(Stated|ListenFree|ListenEach)\\(";
  const control = grepFiles(pattern, ["internal/netbind", "internal/towercli"]);
  assert.ok(control.includes("internal/netbind/netbind.go"), "the pattern still matches netbind's own bind");
  assert.ok(control.includes("internal/towercli/hub.go"), "the pattern still matches a netbind caller");

  const files = grepFiles(pattern, ["cmd/rogerai", "internal/agent", "internal/node"]);
  // Asserted as an EXACT set. The one listener left on these paths is the Roger Edge face and
  // authority (cmd/rogerai/edgebind.go): founder ruling 2026-10-07, it binds only on a machine
  // that has joined an Edge (it serves that membership's certificate) or holds an Edge's root.
  // Sharing to the market opens no inbound port. internal/webui and internal/tui bind
  // loopback-only listeners for local features (browser console, TUI proxy) outside these
  // directories, and the onboarding flow only suggests a port, it never binds one.
  assert.deepEqual(files, ["cmd/rogerai/edgebind.go"],
    `expected only the Roger Edge listener in the sharing path, found: ${files.join(", ") || "(none)"}`);
});

// Founder ruling 2026-10-07: Roger Edge is the one opt-in exception to "your machine never
// listens", so the page says so plainly, scoped to members over mutual TLS, and says the Edge
// delegate runner acts only on grants the owner signs. Sharing never turns it on.
test("gpu-isolation: the Roger Edge exception is stated, opt-in, members only, signed grants", () => {
  const visible = read(PAGE).replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  assert.match(visible, /set up Roger Edge/i, "the exception is named and is opt-in");
  assert.match(visible, /mutual TLS/i, "Edge devices connect over mutual TLS");
  assert.match(visible, /only devices that are members of your Edge can connect/i, "members only");
  assert.match(visible, /acts only on grants you sign/i, "the delegate runner needs a grant the owner signs");
  assert.match(visible, /Sharing a model never turns any of that on/i, "sharing does not start it");
});

test("gpu-isolation: no em dashes in the copy", () => {
  assert.doesNotMatch(read(PAGE), /—/, "founder style: spaced hyphens, never em dashes");
});
