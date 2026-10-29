// svelte-warnings.test.mjs: the Svelte compiler's reactivity warnings are errors here.
//
// A build prints them and moves on, so a `let x` that the template renders and a handler
// reassigns (non_reactive_update) ships as a component whose markup silently stops updating:
// the sign-in page's "a code is on its way to <address>" was one, caught only by reading the
// build log. Every component in app/ is compiled here (client output, the one that hydrates)
// and the reactivity family of warnings fails the suite. The a11y warnings are left alone:
// the islands keep the legacy pages' markup as it was, roles included.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { compile } from "svelte/compiler";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const APP = path.join(WEB, "app");

// Warnings about state that will not update, or is read where it should be tracked.
const REACTIVITY = /^(non_reactive_update|state_referenced_locally|ownership_invalid_|binding_property_non_reactive|reactive_declaration_)/;
// lib/ui/Island.svelte renders its component ONCE, on the server, by design: the initial value is
// the only one there is (the island hydrates in the browser from app/islands/<name>.js).
const ALLOWED = new Set(["lib/ui/Island.svelte"]);

function components(dir, out = []) {
  for (const name of readdirSync(dir).sort()) {
    const abs = path.join(dir, name);
    if (statSync(abs).isDirectory()) { if (!name.startsWith(".")) components(abs, out); }
    else if (name.endsWith(".svelte")) out.push(abs);
  }
  return out;
}

const FILES = components(APP);

test("every component in app/ compiles", () => {
  assert.ok(FILES.length > 50, `found ${FILES.length} components under app/`);
  for (const f of FILES) {
    assert.doesNotThrow(() => compile(readFileSync(f, "utf8"), { filename: f, generate: "client", runes: true }), path.relative(WEB, f));
  }
});

test("no component carries a reactivity warning (state the template renders but never tracks)", () => {
  const found = [];
  for (const f of FILES) {
    const rel = path.relative(APP, f);
    if (ALLOWED.has(rel)) continue;
    const { warnings } = compile(readFileSync(f, "utf8"), { filename: f, generate: "client", runes: true });
    for (const w of warnings) if (REACTIVITY.test(w.code)) found.push(`${rel}:${w.start?.line ?? "?"} ${w.code}: ${w.message}`);
  }
  assert.deepEqual(found, [], "fix the component (declare the value with $state, or read it inside a closure):\n" + found.join("\n"));
});

test("the allow-list names components that exist and still need it", () => {
  for (const rel of ALLOWED) {
    const f = path.join(APP, rel);
    const { warnings } = compile(readFileSync(f, "utf8"), { filename: f, generate: "client", runes: true });
    assert.ok(warnings.some((w) => REACTIVITY.test(w.code)), `${rel} no longer warns: drop it from the allow-list`);
  }
});
