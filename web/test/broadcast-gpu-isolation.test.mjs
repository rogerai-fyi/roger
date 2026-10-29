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
import { readFileSync, readdirSync, existsSync } from "node:fs";
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

// THE SHARING PATH, as code: `roger share` wires its flags in cmd/rogerai (main.go's cmdShare,
// share_meta.go) and runs in internal/agent (the poll loop that forwards a job to the operator's
// own upstream) and internal/node (the controller). Founder ruling 2026-10-27: the claims below are
// about THAT path, not the whole `roger` binary, which since v7 also carries the Edge face (an
// inbound listener on the owner's LAN) and the Edge delegate runner (which uses the harness on the
// owner's own machines). Those are reached from `roger edge`, never from `roger share`.
const SHARE_ENTRY = ["cmd/rogerai/main.go", "cmd/rogerai/share_meta.go"];
const SHARE_PACKAGES = ["internal/agent", "internal/node"];
const goFilesIn = (dir) => readdirSync(path.join(ROOT, dir))
  .filter((f) => f.endsWith(".go") && !f.endsWith("_test.go")).map((f) => path.join(dir, f));
function goImports(file) {
  const src = readSrc(file);
  const block = /^import\s*\(([\s\S]*?)^\)/m.exec(src);
  const single = [...src.matchAll(/^import\s+(?:\w+\s+)?"([^"]+)"/gm)].map((m) => m[1]);
  return [...single, ...(block ? [...block[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]) : [])];
}
// Every package of this module the share packages reach, transitively, through their non-test files.
function shareClosure() {
  const seen = new Set();
  const queue = [...SHARE_PACKAGES];
  while (queue.length) {
    const dir = queue.shift();
    if (seen.has(dir)) continue;
    seen.add(dir);
    for (const f of goFilesIn(dir)) {
      for (const imp of goImports(f)) {
        const m = /^rogerai\.fm\/roger\/v\d+\/(.+)$/.exec(imp);
        if (m && existsSync(path.join(ROOT, m[1]))) queue.push(m[1]);
      }
    }
  }
  return seen;
}
const HARNESS = /^rogerai\.fm\/roger\/v\d+\/internal\/harness$/;

test("gpu-isolation: the tool-executing harness is never imported by the node/sharing path", () => {
  // Positive control first: the import matcher must still recognise the harness where it IS
  // imported (the TUI and the console), or an empty result below would be a vacuous pass.
  const tuiImports = goFilesIn("internal/tui").flatMap(goImports);
  assert.ok(tuiImports.some((i) => HARNESS.test(i)), "the harness matcher still recognises internal/tui's import");

  const closure = shareClosure();
  assert.ok(closure.has("internal/protocol"), "the closure walker follows imports (positive control)");
  assert.ok(!closure.has("internal/harness"),
    `the sharing path reaches internal/harness (the tool-executing agent): ${[...closure].sort().join(", ")}`);
  const entry = SHARE_ENTRY.filter((f) => goImports(f).some((i) => HARNESS.test(i)));
  assert.deepEqual(entry, [], `share's entry files import internal/harness: ${entry.join(", ")}`);
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

// A listener in the sharing path: the standard library's, and this repository's own helpers
// (internal/netbind, the Edge's edgeListen), which a bare net.Listen grep would not see.
const LISTEN = /ListenAndServe|net\.Listen\(|tls\.Listen\(|netbind\.(Listen\w*|Stated)\(|edgeListen\(/;

test("gpu-isolation: no inbound listener exists in the share/node code path", () => {
  // Positive control: the pattern still finds the Edge face's listener outside the share path, so an
  // empty result below cannot be a pattern that silently stopped matching anything.
  assert.match(readSrc("cmd/rogerai/edgehost.go"), LISTEN, "the listener pattern still matches the Edge face");
  const files = [...SHARE_ENTRY, ...SHARE_PACKAGES.flatMap(goFilesIn)].filter((f) => LISTEN.test(readSrc(f)));
  assert.deepEqual(files, [], `a listener in the sharing path: ${files.join(", ")}`);
});

test("gpu-isolation: no em dashes in the copy", () => {
  assert.doesNotMatch(read(PAGE), /—/, "founder style: spaced hyphens, never em dashes");
});
