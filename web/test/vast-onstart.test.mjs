// The manual tells an operator to paste a URL into somebody else's machine and then quotes
// the environment variables that steer it. Those two things drift the moment either side is
// edited alone, and the failure is silent: a documented knob that the script ignores looks
// exactly like a knob that works, right up until the box goes on air at the wrong price or
// with the wrong model.
//
// So the prose is locked to the script it describes.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const SCRIPT = "vast-onstart.sh";
const manualSection = () => {
  // the heading may carry attributes (it has an id now, so other pages can link to it),
  // and so may the NEXT h3 that bounds the section
  const m = read("manual.html").match(/<h3[^>]*>On rented hardware[\s\S]*?(?=<h3[\s>])/);
  assert.ok(m, "the manual carries the rented-hardware section");
  return m[0];
};

test("vast: the script the manual points at is actually published", () => {
  // A paste-this-URL instruction that 404s is worse than no instruction.
  const script = read(SCRIPT);
  assert.match(script, /^#!/, "it is served as a script, not an HTML page");
  assert.match(manualSection(), new RegExp(`https://rogerai\\.fm/${SCRIPT}`),
    "and the manual points at exactly that URL");
});

test("vast: every knob the manual advertises is one the script reads", () => {
  const script = src(SCRIPT);
  // The name may be written on its own (<code>ROGER_MODEL</code>) or with a value
  // (<code>ROGER_DRY_RUN=1</code>). The first version of this regex demanded </code>
  // immediately after the name, so every knob documented in the second style - including
  // the two switches - was silently exempt from the check this test exists to make.
  const advertised = [...manualSection().matchAll(/<code>(ROGER_[A-Z_]+)(?:=[^<]*)?<\/code>/g)].map((m) => m[1]);
  assert.ok(advertised.length >= 6, `the manual names the knobs (found ${advertised.length})`);
  for (const v of new Set(advertised)) {
    assert.ok(script.includes("${" + v + ":-") || script.includes("$" + v),
      `the manual advertises ${v} but the script never reads it`);
  }
});

test("vast: the default model quoted in the manual is the script's default", () => {
  const def = src(SCRIPT).match(/MODEL="\$\{ROGER_MODEL:-([^}]+)\}"/)?.[1];
  assert.ok(def, "the script has a default model");
  assert.ok(manualSection().includes(def),
    `the manual quotes a different default model than the script's (${def})`);
});

test("vast: the manual states the money guard the script actually enforces", () => {
  // The script refuses to price a share on a box with no owner rather than silently
  // serving free. That behaviour is the reason an operator can trust the paste, so the
  // manual has to say it - and the script has to keep doing it.
  const script = src(SCRIPT);
  assert.match(script, /refusing to price this share/, "the script refuses");
  assert.match(script, /exit 2/, "and exits non-zero rather than continuing");
  const prose = manualSection().replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  assert.match(prose, /stops|refus/i, "the manual says it stops");
  assert.match(prose, /free/i, "and that it does not fall back to free");
});

test("vast: free is the documented default, because a rented box holds no secret", () => {
  const script = src(SCRIPT);
  assert.match(script, /PRICE_OUT="\$\{ROGER_PRICE_OUT:-0\}"/, "free unless a price is given");
  assert.match(manualSection().replace(/<[^>]+>/g, " "), /free by default/i,
    "and the manual leads with that");
});

test("vast: the pages that need the how-to land ON it, not at the top of the manual", () => {
  // The manual is ~1,700 lines. A link to /manual.html leaves the reader scrolling for the
  // one section they were sent to find, which is how documented things stay unfindable.
  assert.match(read("manual.html"), /<h3[^>]*id="rented"/,
    "the rented-hardware section is anchored");
  for (const page of ["broadcasts-what-a-million-tokens-costs.html", "research-hardware.html"]) {
    assert.match(read(page), /manual\.html#rented/,
      `${page} links to the section rather than the manual's top`);
  }
});

test("vast: the hardware page tells a reader the boards can be rented", () => {
  // Every machine on that page reads as "own one" unless something says otherwise, and it
  // is the page somebody is on when they ask what to run this on.
  const page = read("research-hardware.html");
  assert.match(page, /vast-onstart\.sh/, "the hardware page carries the one-paste bootstrap");
  assert.match(page, /rogerai\.fm\/vast-onstart\.sh/, "as a URL a reader can actually use");
  const note = page.match(/<div class="man-note hw-rent">[\s\S]*?<\/div>/)?.[0] ?? "";
  assert.match(note.replace(/<[^>]+>/g, " "), /free by\s+default/i,
    "and says it is free by default, since the box is not yours");
});
