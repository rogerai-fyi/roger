// Behaviour lock for the Base Station viewer's PURE render core (web/src/js/private.js):
// stateLabel (the roster dot/label; the one red glint is the live dot) and frameLine (how each
// relayed RCFrame becomes a transcript line, including the confirm-id carried on a confirm_req
// and the empty-text frames that render nothing). Like session.test.mjs, we run the REAL shipped
// IIFE in a tiny sandbox with injected globals and read window.RCView — no jsdom (the web tree
// is dependency-free on purpose). Run: node --test test/private.test.mjs  (picked up by npm test).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = readFileSync(
  path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/private.js"),
  "utf8",
);

// Run the IIFE with a window that captures RCView and a document whose DOMContentLoaded is
// recorded but never fired (so the roster/fetch path never runs — we test the pure core only).
function loadCore() {
  const win = {};
  const doc = {
    addEventListener() {},
    getElementById() { return null; },
  };
  const noop = { getItem() { return null; }, setItem() {}, removeItem() {} };
  new Function("window", "document", "navigator", "sessionStorage", "fetch", "location", "TextDecoder", SRC)(
    win, doc, { clipboard: { writeText() {} } }, noop, () => Promise.resolve({}), { hash: "" }, function () {},
  );
  return win.RCView;
}

test("stateLabel: live earns the red dot; ended/offline do not", () => {
  const V = loadCore();
  assert.deepEqual(V.stateLabel({ online: true }), { dot: "◉", label: "live", live: true });
  assert.deepEqual(V.stateLabel({ online: false }), { dot: "○", label: "offline", live: false });
  assert.deepEqual(V.stateLabel({ revoked: true, online: true }), { dot: "·", label: "ended", live: false });
});

test("frameLine: each RCFrame kind renders its line", () => {
  const V = loadCore();
  assert.equal(V.frameLine({ kind: "user", origin: "web", text: "hi" }).text, "▸ (web) hi");
  assert.equal(V.frameLine({ kind: "assistant", text: "yo" }).text, "◂ yo");
  assert.equal(V.frameLine({ kind: "final", text: "done" }).text, "◂ done");
  assert.equal(V.frameLine({ kind: "tool_call", tool: "run_shell" }).text, "◉ run_shell");
  assert.equal(V.frameLine({ kind: "tool_result", tool: "read_file" }).text, "✓ read_file");
  assert.equal(V.frameLine({ kind: "error", text: "boom" }).text, "✗ boom");
});

test("frameLine: a confirm_req carries its id so the answer can correlate", () => {
  const V = loadCore();
  const l = V.frameLine({ kind: "confirm_req", tool: "write_file", confirm_id: "cf-9" });
  assert.equal(l.confirm, "cf-9");
  assert.match(l.text, /write_file/);
});

test("frameLine: confirm_done names the answer + origin; ended flags the terminal frame", () => {
  const V = loadCore();
  assert.equal(V.frameLine({ kind: "confirm_done", approve: true, origin: "web" }).text, "✓ approved from web");
  assert.equal(V.frameLine({ kind: "confirm_done", approve: false, origin: "local" }).text, "✓ denied from local");
  assert.equal(V.frameLine({ kind: "ended" }).ended, true);
});

test("frameLine: empty assistant/backfill text renders nothing (null)", () => {
  const V = loadCore();
  assert.equal(V.frameLine({ kind: "assistant", text: "   " }), null);
  assert.equal(V.frameLine({ kind: "backfill", text: "" }), null);
  assert.equal(V.frameLine({ kind: "unknown_kind" }), null);
});

// Guest Operators iteration-2 finding: the web console used to DROP RCKindStatus frames, so a
// handoff looked dead in the browser (only iOS rendered "guest has the mic"). A status frame now
// renders as a dim rc-status line: operator-aware for a guest handoff, the plain text for the
// DJ-back transition, nothing when it carries neither (content-blind - only the guest name).
test("frameLine: a status frame renders the handoff transition as a dim rc-status line", () => {
  const V = loadCore();
  assert.deepEqual(
    V.frameLine({ kind: "status", operator: "opencode", text: "guest has the mic: opencode - the DJ answers when the handoff ends" }),
    { cls: "rc-status", text: "◉ guest has the mic: opencode" },
  );
  assert.deepEqual(
    V.frameLine({ kind: "status", text: "the DJ is back at the desk" }),
    { cls: "rc-status", text: "the DJ is back at the desk" },
  );
  assert.equal(V.frameLine({ kind: "status" }), null);
  assert.equal(V.frameLine({ kind: "status", text: "" }), null);
});
