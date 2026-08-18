// Executable lock for features/web/wave_infinite.feature.
//
// Wave Infinite is a prototype model PROGRAMME - not a fifth size class, and explicitly
// "not 'just a runtime'; the certified runtime is one layer of it". It has one causally
// validated primitive and no demonstrated speedup. Its own brief sets the
// ceiling ("must not be described as a working system") and names the naming risk
// ("infinite in specification, finite in implementation ... a theorem, not a capability
// claim"). This is the same failure mode as the retracted Wave Micro release, so most of
// these assertions are about what the page must NOT say.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const visible = (s) => s.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const section = () => {
  const s = read("research-wave-family.html").match(/<section[^>]*id="infinite"[\s\S]*?<\/section>/)?.[0];
  assert.ok(s, "the family page carries a Wave Infinite section");
  return s;
};

test("Wave Infinite is present and reachable", () => {
  assert.match(visible(section()), /Wave Infinite/);
  assert.match(read("research.html"), /#infinite|Wave Infinite/, "the hub points at it");
});

// The canonical doc is explicit: "It is NOT 'just a runtime'; the certified runtime is
// one layer of it." An earlier version of this page led with "a runtime, not a size".
test("it is a prototype programme, and the runtime is one layer of it", () => {
  const copy = visible(section());
  assert.match(copy, /prototype/i, "named as a prototype");
  assert.match(copy, /programme|program/i, "and as a programme");
  assert.match(copy, /layer/i, "which has layers");
  assert.doesNotMatch(copy, /a runtime, not a|is (just )?a runtime\b/i,
    "it must not be reduced to a runtime");
});

test("it is still not a size class", () => {
  const copy = visible(section());
  assert.doesNotMatch(copy, /Wave Infinite[^.]{0,80}\b\d+\s?(B|M)-class\b/i, "no parameter class");
  const page = read("research-wave-family.html");
  for (const id of ["slots", "jobs"]) {
    const table = page.match(new RegExp(`<section[^>]*id="${id}"[\\s\\S]*?</section>`))[0];
    assert.doesNotMatch(table, /Wave Infinite/, `${id} table stays a table of sizes`);
  }
  assert.doesNotMatch(read("research.html"), /data-slot="wave-infinite"/, "not on the size axis");
});

test("the measurement that forced it to exist leads", () => {
  const copy = visible(section());
  assert.match(copy, /invisible|hides? inside|benchmark noise/i, "damage hides in the noise");
  assert.match(copy, /cannot monitor your way/i, "and the conclusion is stated");
  assert.match(copy, /workload (shifts|changes)/i, "and what happens when the workload moves");
});

test("each layer is shown at its real state", () => {
  const sec = section();
  const layers = [...sec.matchAll(/<li data-layer="([^"]+)" data-state="([^"]+)"[^>]*>/g)]
    .map((m) => [m[1], m[2]]);
  assert.equal(layers.length, 3, "three layers");
  assert.deepEqual(layers.map((l) => l[0]), ["reflection", "certified", "growth"]);
  assert.deepEqual(layers.map((l) => l[1]), ["built", "measured", "unrun"]);
  const copy = visible(sec);
  assert.match(copy, /in development|preregistered|unrun/i, "the third layer says it is not done");
});

// The hardest constraint in the program doc, quoted: "Never claim 'self-training' in
// public material until CURE's gates pass ... externally it is an overclaim until
// measured." This is the assertion that keeps that promise.
test("the page never claims self-training", () => {
  const copy = visible(section());
  for (const banned of [/self-training/i, /self-improving/i, /trains itself/i,
                        /learns by itself/i, /improves itself today/i,
                        /available now/i, /download/i]) {
    assert.doesNotMatch(copy, banned, `must not say ${banned}`);
  }
});

// The measurements are REAL and the claims stand; the figures are held back because the
// programme doc puts interim-hardware benchmarks internal-only until the final hardware.
// So the page must still make each claim, and must not print a number for any of them.
test("what is proven is claimed without publishing interim figures", () => {
  const copy = visible(section());
  assert.match(copy, /bit-identical/i, "the certificate result, stated without a count");
  assert.match(copy, /almost never fires|rarely fires/i, "the in-domain guard behaviour");
  assert.match(copy, /large share|large fraction/i, "and the cross-domain one");
  assert.match(copy, /order of magnitude/i, "and the over-removal result");
  assert.match(copy, /re-verif/i, "with why the figures are not here yet");
});

// Sweep the whole built site: these must not survive anywhere, including a social card,
// a meta description or a page I did not think to check.
test("no interim-hardware figure is published anywhere", () => {
  for (const page of readdirSync(DIST).filter((f) => f.endsWith(".html"))) {
    const html = readFileSync(path.join(DIST, page), "utf8");
    for (const figure of [/391,?386/, /0\.009\s?%/, /p\s?=\s?0\.26/, /10 to 30\s?(&times;|x)/i]) {
      assert.doesNotMatch(html, figure,
        `${page} publishes an interim-hardware figure (${figure})`);
    }
  }
});

test("what is NOT proven is published deliberately and in the main flow", () => {
  const sec = section();
  const copy = visible(sec);
  assert.match(copy, /unmeasured|in measurement|not (yet )?proven/i);
  assert.match(copy, /speed|tokens per second|tok\/s/i, "the speed benefit is named as unmeasured");
  assert.match(copy, /prototype/i);
  const flow = visible(sec.replace(/<figcaption[\s\S]*?<\/figcaption>/g, "").replace(/title="[^"]*"/g, ""));
  assert.match(flow, /unmeasured|not (yet )?proven|in measurement/i,
    "the limit survives without the caption");
});

// The catalogue pointer was removed on founder direction. What survives is the half that
// still matters: it must never appear as a ROW in the model list, because that is the
// misfiling the whole section exists to prevent. It stays reachable from the research hub
// and the family page.
test("Wave Infinite is never listed as a model in the catalogue", () => {
  const cat = read("research-models.html");
  const entries = [...cat.matchAll(/<h3[^>]*>([^<]+)<\/h3>/g)].map((m) => m[1]);
  assert.ok(!entries.some((e) => /Wave Infinite/i.test(e)), "it is not a row in the model list");
  assert.match(read("research.html"), /Wave Infinite/, "the hub still routes a reader to it");
});

test("no page anywhere reduces it to a runtime or claims self-training", () => {
  for (const page of ["index.html", "research.html", "research-models.html",
                      "research-wave-family.html", "company.html"]) {
    const copy = visible(read(page));
    assert.doesNotMatch(copy, /self-training|self-improving|trains itself/i,
      `${page} must not claim self-training`);
    assert.doesNotMatch(copy, /whole family could run under|runtime the whole family/i,
      `${page} must not claim family-wide coverage`);
  }
});

test("the shimmer stays inside the RogerAI palette and carries no information", () => {
  const css = readFileSync(path.join(WEB, "src", "styles", "wave-family.css"), "utf8");
  /* AMENDED 2026-08-17: this sliced from the first .wf-inf to the END OF THE
     FILE, so it policed every rule that happened to sit below it - it failed
     on the Wave Spectrum MARK's tier colours, which are seven hues on
     purpose (the mark IS the spectrum; the section is titled "pico to exa").
     The guarantee here is about INFINITE specifically - it must never be
     dressed as a rainbow, because it is a mode, not a size - so the lock now
     reads exactly the .wf-inf rules and nothing else. */
  const block = (css.match(/\.wf-inf[^{]*\{[^}]*\}/g) || []).join("\n");
  assert.ok(block.length > 0, "the treatment is styled");
  const hues = [...block.matchAll(/#[0-9a-f]{3,8}\b/gi)].map((m) => m[0].toLowerCase());
  assert.equal(hues.length, 0, `no raw hex colours in the treatment, found ${hues.join(", ")}`);
  assert.doesNotMatch(block, /hsl\(|rainbow|violet|indigo/i, "no multi-hue spectrum");
  const reduced = (css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/g) || [])
    .find((b) => b.includes("wf-inf"));
  assert.ok(reduced, "the shimmer has a reduced-motion escape");
  assert.match(reduced, /animation: none/);
});

// ---- the showcase (founder redirection 2026-07-31; WAVE-INFINITE-WEB-BRIEF) ----
// Infinite becomes the family's finale: last marker on the rail as a MODE, a distinct
// animated loop figure, and training-is-using copy in the brief's approved register.
// The claim ceiling above is unchanged; the badge is what makes the showcase safe.

test("the prototype badge is always visible in the section", () => {
  const copy = visible(section());
  assert.match(copy, /prototype/i);
  assert.match(copy, /measured foundations, growth loop in development/i,
    "the badge ships with the section, per the web brief hard rule");
});

test("the rail places Infinite past the numbers, hollow, and never as an eighth size", () => {
  /* AMENDED 2026-08-14: this used to require Infinite as a trailing rail marker with
     its own treatment. It sat past the axis end (111%), where it collided with the top
     tiers and clipped out of the scroller. It was removed from the axis entirely and the
     caption said why.

     AMENDED 2026-08-17: the founder reversed that, directly and in these words - "let's
     replace the 1 TB with an infinite sign and add it with it's own dot ... and remove
     the text where it says it's not there." So Infinite is back on the rail and the
     disclaimer copy is gone, and this test is re-anchored rather than deleted: the
     guarantee it exists for is that Infinite is never read as another SIZE, nor as
     something that has been trained. That is now carried by three structural facts
     instead of by absence plus a sentence, and all three are asserted here.
       1. The seven sized markers are still exactly the locked ladder, in order, and
          Infinite is not among them - it is a separate element with its own class,
          outside the wf-node set the ladder assertion reads.
       2. It sits at the infinity tick. The numeric ticks stop at 100 GB, so every real
          size still reads against a number, and the position Infinite occupies is the
          one point on the axis that is not a finite size.
       3. Its marker is HOLLOW, which under this figure's own legend means a program
          stage rather than a trained artifact. Pico's is still the only filled marker
          on the rail.
     Section 6 still owns what Infinite is, and the prototype badge and the
     preregistered/unrun caveats there are asserted unchanged by the tests above. */
  const page = read("research-wave-family.html");
  const rail = page.match(/<figure class="wf-rail"[\s\S]*?<\/figure>/)[0];

  // 1. the ladder, and Infinite outside it
  const nodes = [...rail.matchAll(/class="wf-node[^"]*"[^>]*>[\s\S]*?<b>([^<]+)<\/b>/g)].map((m) => m[1]);
  assert.deepEqual(nodes, ["Pico", "Nano", "Micro", "Giga", "Tera", "Peta", "Exa"],
    "the sized markers are the seven fixed sizes of the locked ladder, in order");
  assert.ok(!nodes.some((n) => /infinite/i.test(n)), "Infinite is not one of the seven sizes");
  const beyond = rail.match(/<span class="wf-beyond"[^>]*style="--at:\s*100%[^"]*"[\s\S]*?<\/span>/)?.[0];
  assert.ok(beyond, "Infinite has its own marker, with its own class, at the end of the axis");
  assert.match(visible(beyond), /Wave Infinite/i, "and it is named on the rail");

  // 2. it sits where the numbers stop
  assert.match(rail, /class="wf-tick wf-tick--end"[^>]*style="--at:\s*100%[^"]*"[\s\S]*?<b>&infin;<\/b>/,
    "the end of the axis is the infinity symbol, not a finite size");
  assert.doesNotMatch(visible(rail), /\b1\s?TB\b/, "so no tier is read against a terabyte tick");
  assert.match(rail, /<b>100 GB<\/b>/, "and the numeric decades still run up to 100 GB");

  // 3. hollow: a program stage, never a trained artifact
  const css = readFileSync(path.join(WEB, "src", "styles", "wave-family.css"), "utf8");
  const marker = css.match(/\.wf-beyond i \{([^}]*)\}/)?.[1];
  assert.ok(marker, "the Infinite marker is styled");
  assert.match(marker, /border:\s*2px solid var\(--ink-900\)/, "it is drawn as an outline");
  assert.match(marker, /background:\s*var\(--paper\)/,
    "with no fill - a program stage under this figure's legend, not a trained artifact");
  assert.doesNotMatch(beyond, /is-live/, "and it never wears the trained-artifact treatment");
  const filled = rail.match(/class="wf-node is-live"[^>]*>[\s\S]*?<b>([^<]+)<\/b>/g) || [];
  assert.equal(filled.length, 1, "exactly one filled marker on the rail");
  assert.match(filled[0], /<b>Pico<\/b>/, "and it is Pico's");
  // The legend that gives the hollow marker its meaning has to be in the figure.
  assert.match(visible(rail), /trained artifact[\s\S]{0,40}program stage/i,
    "the filled/hollow legend is present, or hollow means nothing");
});

test("the loop figure is described and safe", () => {
  const sec = section();
  assert.match(sec, /<figure class="wf-orbit"[^>]*>/, "the loop figure exists");
  assert.match(sec, /wf-orbit[^>]*role="img"[^>]*aria-label="[^"]{40,}/,
    "and is described for screen readers");
  const css = readFileSync(path.join(WEB, "src", "styles", "wave-family.css"), "utf8");
  const reduced = (css.match(/@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}/g) || [])
    .find((b) => b.includes("wf-orbit"));
  assert.ok(reduced, "the orbit freezes under reduced motion");
});

test("training it is described in the approved register", () => {
  const copy = visible(section());
  assert.match(copy, /grows new weights/i);
  assert.match(copy, /real parameters, trained on your work/i);
  assert.match(copy, /exact rollback/i, "deployment is the certified path with rollback resident");
  assert.match(copy, /reverts to the original path by itself/i,
    "self-healing is only the guard fallback");
});

test("it survives with no JavaScript", () => {
  const sec = section();
  assert.doesNotMatch(sec, /<script/i, "no inline script");
  assert.match(visible(sec), /Wave Infinite/, "the copy is in the served markup");
});

// The onward card on the research hub still read "the runtime a model runs under" after
// the section itself was corrected, so the site contradicted itself across two pages. The
// runtime-reduction check was scoped to the family-page section; it is site-wide now.
test("no page anywhere reduces Wave Infinite to a runtime", () => {
  for (const page of readdirSync(DIST).filter((f) => f.endsWith(".html"))) {
    const copy = visible(readFileSync(path.join(DIST, page), "utf8"));
    if (!/Wave Infinite/.test(copy)) continue;
    for (const reduction of [/Wave Infinite[^.]{0,30}\bis (just )?a runtime\b/i,
                             /Wave Infinite,? the runtime\b/i,
                             /Wave Infinite - the runtime\b/i]) {
      assert.doesNotMatch(copy, reduction,
        `${page} reduces Wave Infinite to a runtime; the runtime is one layer of it`);
    }
  }
});
