// Post-launch polish (2026-09-24): the follow-ups the launch QA report left open.
// Static checks on sources and sheets; the rendered checks (label size, tap targets)
// live in scripts/overflow-sweep.py's companions and were run at 390 by hand.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (f) => readFileSync(path.join(WEB, f), "utf8");
const css = (f) => read(path.join("src/styles", f)).replace(/\/\*[\s\S]*?\*\//g, "");

test("the OS lock is documented for both of its jobs: a one-platform command and a cross-platform non-installer one", () => {
  const js = read("src/js/site.js");
  const lock = js.slice(js.indexOf("[data-os-lock] boxes"), js.indexOf("document.querySelectorAll(\".install__box:not"));
  assert.match(lock, /data-os-lock="linux"/, "site.js comment names the one-platform lock");
  assert.match(lock, /data-os-lock="any"/, "site.js comment names the cross-platform lock");
  assert.match(lock, /roger use/);
  const ds = read("DESIGN-SYSTEM.md");
  assert.match(ds, /oslock=any/);
  assert.match(ds, /roger say/);
});
