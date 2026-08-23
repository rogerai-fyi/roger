import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const source = (p) => readFileSync(path.join(SRC, p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// The device page's "Sign in" control is an <a>, not a <button>, and inside .card the
// link treatment `.card a:not(...)` is (0,2,1) while `.primary` is only (0,1,0). So the
// link rule won and repainted the button's TEXT with var(--ink-900) - the exact token
// .primary uses for its BACKGROUND. The result was a blank slab: cream on cream in dark,
// near-black on near-black in light. Invisible in BOTH themes, so no toggle revealed it,
// and nothing in the build failed.
//
// This had already happened once with .gh (the comment above the rule records it). The
// fix is by name, so this test is by name too: every class used as a button on a card
// must be excluded from the link treatment.
test("a button that happens to be an <a> is excluded from the card link treatment", () => {
  const css = source("styles/account-base.css");

  // Find each .card a rule and check what it excludes.
  const rules = [...css.matchAll(/^\.card a(:not\([^)]*\))*[^{]*\{/gm)].map((m) => m[0]);
  assert.ok(rules.length > 0, "no `.card a` rule found - did the selector move?");

  for (const rule of rules) {
    for (const btn of ["gh", "primary"]) {
      assert.ok(
        rule.includes(`:not(.${btn})`),
        `\`${rule.trim()}\` does not exclude .${btn}. That rule out-specifies the ` +
          `button rule and will repaint the control's text, which is how the device ` +
          `page's Sign in button went invisible.`,
      );
    }
  }
});

// The collision was only invisible because the two tokens are the same. State the
// relationship so a future palette edit that reintroduces it fails here rather than in a
// screenshot: .primary paints its background with --ink-900, so --ink-900 must never also
// be the text color applied to a .primary control.
test(".primary paints its own text, and not with its own background token", () => {
  const css = source("styles/account-base.css");
  const rule = css.match(/^\.primary\s*\{[^}]*\}/m);
  assert.ok(rule, ".primary rule not found");
  const body = rule[0];

  assert.match(body, /background:\s*var\(--ink-900\)/, ".primary background token changed");
  assert.match(body, /color:\s*var\(--paper\)/, ".primary must set its own text color");

  // The failure mode restated: background and color must not resolve to the same token.
  const bg = body.match(/background:\s*var\((--[a-z0-9-]+)\)/)[1];
  const fg = body.match(/color:\s*var\((--[a-z0-9-]+)\)/)[1];
  assert.notEqual(bg, fg, ".primary would render its text in its own background color");
});

// The device page is where this surfaced, and it is the one page a user cannot route
// around: it is the CLI sign-in approval. Keep its control a real, labelled control.
test("the device sign-in control carries a label and the button class", () => {
  const html = source("device.html");
  const a = html.match(/<a[^>]*id="dvSignInLink"[^>]*>([^<]*)<\/a>/);
  assert.ok(a, "the device sign-in link is gone or renamed");
  assert.ok(a[0].includes("primary"), "the sign-in control lost its .primary button class");
  assert.equal(a[1].trim(), "Sign in", "the sign-in control lost its label");
});
