// A merge conflict in a .go file is caught by the compiler. A merge conflict in an .html
// file is caught by a customer, because HTML has no syntax error - "<<<<<<< Updated
// upstream" simply renders as words on the page. This shipped into dist/manual.html once;
// this test is why it cannot again.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// Only the OUTER markers, and only at the start of a line. A bare row of "=" is an
// ordinary comment divider in half the js files here; the labelled outer pair is not
// ambiguous, and a real conflict always carries both.
// Built from pieces so this file does not trip its own check.
const MARKERS = [`<<<<<<${"<"} `, `>>>>>${">>"} `];

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const full = path.join(dir, name);
    if (statSync(full).isDirectory()) yield* walk(full);
    else yield full;
  }
}

const TEXT = /\.(html|css|js|mjs|json|xml|svg|md|txt)$/i;

for (const root of ["src", "dist"]) {
  test(`no merge-conflict markers survive into web/${root}`, () => {
    const bad = [];
    for (const file of walk(path.join(WEB, root))) {
      if (!TEXT.test(file)) continue;
      const lines = readFileSync(file, "utf8").split("\n");
      for (const [i, line] of lines.entries()) {
        for (const m of MARKERS) {
          if (line.startsWith(m)) {
            bad.push(`${path.relative(WEB, file)}:${i + 1} starts with ${JSON.stringify(m)}`);
          }
        }
      }
    }
    assert.deepEqual(bad, [], `unresolved conflict markers:\n${bad.join("\n")}`);
  });
}

test("the manual reports the brand and domain we actually ship", () => {
  const manual = readFileSync(path.join(WEB, "dist/manual.html"), "utf8");
  // The stash that produced the conflict above reverted both of these; if a future
  // resolution takes the wrong side, this is louder than a marker check.
  assert.doesNotMatch(manual, /rogerai\.fyi/, "the .fyi domain is retired");
  assert.match(manual, /broker\.rogerai\.fm/, "the live broker host is named");
});
