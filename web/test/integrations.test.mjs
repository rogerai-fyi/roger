// The compatibility story was one row of the homepage spec sheet and a broadcast about
// the OpenAI SDK. Edge Impulse leans on a fourteen-logo partner wall for the same job,
// and our version is both stronger and invisible: the CLI detects a dozen local servers
// by default port, and it wires seven agent CLIs at the desk with per-guest strategies
// that were each proven end-to-end before being added.
//
// So this page is not a hand-written list. Both halves are LOCKED to the Go that
// implements them - internal/detect's probe table and internal/operator's registry -
// because a compatibility page that drifts is worse than none: it sends a person to
// configure something that does not work that way any more.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const REPO = path.join(WEB, "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const go = (...p) => readFileSync(path.join(REPO, ...p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- the page exists and is reachable ------------------------------- */

test("integrations: the page builds, is indexable, and is in the sitemap", () => {
  assert.doesNotMatch(read("integrations.html"), /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), /<loc>https:\/\/rogerai\.fm\/integrations\.html<\/loc>/);
});

test("integrations: it is reachable from the nav and the footer", () => {
  assert.match(src("_partials/nav.html"), /href="\/integrations\.html"/, "nav links it");
  assert.match(src("_partials/footer.html"), /href="\/integrations\.html"/, "footer links it");
});

/* ---- the serve side, locked to the detector ------------------------- */

// Every entry in internal/detect's probe table, as {name, port}. This is what `roger
// share` actually looks for, so it is what the page may claim it looks for.
const probes = () =>
  [...go("internal", "detect", "detect.go").matchAll(/\{"([^"]+)",\s*"http:\/\/127\.0\.0\.1:(\d+)\/v1"\}/g)]
    .map((m) => ({ name: m[1], port: m[2] }));

test("integrations: every locally-detected backend is named, with the port it is found on", () => {
  const found = probes();
  assert.ok(found.length >= 10, `the probe table parsed (${found.length} entries)`);
  const page = read("integrations.html");
  for (const { name, port } of found) {
    // A combined entry in the table ("vllm/tgi") is one probe covering several servers;
    // the page may name them separately, so each SEGMENT must appear.
    for (const part of name.split("/")) {
      assert.ok(page.toLowerCase().includes(part.toLowerCase()),
        `the page names ${part}, which roger share probes for`);
    }
    assert.ok(page.includes(port), `the page gives ${name}'s port ${port}`);
  }
});

test("integrations: it does not invent a backend the CLI cannot find", () => {
  // Osaurus is the one name that is legitimately absent from the probe table: it squats
  // Jan's port and is identified by a banner instead (detect.go osaurusBanner). Anything
  // else claimed as auto-detected has to be in the table.
  const detect = go("internal", "detect", "detect.go");
  const claimed = [...read("integrations.html").matchAll(/data-backend="([^"]+)"/g)].map((m) => m[1]);
  assert.ok(claimed.length > 0, "the page marks its backend entries");
  const known = new Set([...probes().flatMap((p) => p.name.split("/")), "osaurus"]);
  for (const name of claimed) {
    assert.ok(known.has(name), `the page claims ${name} is detected, but nothing in detect.go finds it`);
    assert.ok(detect.toLowerCase().includes(name.toLowerCase()), `${name} appears in detect.go`);
  }
});

/* ---- the use side, locked to the guest registry --------------------- */

// Every guest in internal/operator's registry, sliced into one block per entry so a
// field is read from ITS OWN guest. A span-based regex silently attributes a flag to
// whichever Name happened to be within reach, which is how a test ends up asserting
// something true of a different row.
const guests = () => {
  const reg = go("internal", "operator", "registry.go");
  const body = reg.slice(reg.indexOf("func Registry()"));
  const heads = [...body.matchAll(/Name:\s*"([^"]+)",\s*Bin:\s*"([^"]+)"/g)];
  return heads.map((h, i) => {
    const block = body.slice(h.index, i + 1 < heads.length ? heads[i + 1].index : body.length);
    return {
      name: h[1],
      bin: h[2],
      strategy: block.match(/Strategy:\s*(\w+)/)?.[1] || "",
      needsSetup: /NeedsSetup:\s*true/.test(block),
    };
  });
};

test("integrations: every guest the CLI can wire is on the page", () => {
  const found = guests();
  assert.ok(found.length >= 5, `the registry parsed (${found.length} guests)`);
  const page = read("integrations.html");
  for (const g of found) {
    assert.ok(page.includes(g.name), `the page names the guest ${g.name}`);
  }
  const listed = [...page.matchAll(/data-guest="([^"]+)"/g)].map((m) => m[1]);
  assert.deepEqual([...listed].sort(), found.map((g) => g.name).sort(),
    "the page's guest list and internal/operator/registry.go have drifted apart");
});

test("integrations: a context-only guest is not sold as running on the band", () => {
  // StrategyContextOnly means RogerAI injects NOTHING - no base URL, no key, no model.
  // The guest runs on the user's own account and their own billing. Saying otherwise
  // would be the one dishonest sentence available on this page, and registry.go says so
  // in as many words ("that absence is what makes the billing story honest").
  const page = read("integrations.html");
  for (const g of guests().filter((g) => g.strategy === "StrategyContextOnly")) {
    const row = page.match(new RegExp(`data-guest="${g.name}"[\\s\\S]*?</article>`))?.[0] || "";
    assert.ok(row, `${g.name} has a row`);
    assert.match(row, /own account|its own account|your own account/i,
      `${g.name} must say it runs on its own account`);
    assert.match(row, /context/i, `${g.name} must say what is actually handed over`);
  }

  // And the converse, which is the direction that can actually hurt someone: a guest the
  // PAGE marks "context only" must still BE context-only in the registry. If it ever
  // starts being wired onto the band, the page would go on promising that nothing here
  // is metered, and that sentence is about somebody's money.
  const contextOnly = new Set(guests().filter((g) => g.strategy === "StrategyContextOnly").map((g) => g.name));
  for (const m of page.matchAll(/data-guest="([^"]+)"([\s\S]*?)<\/article>/g)) {
    if (/context only/i.test(m[2])) {
      assert.ok(contextOnly.has(m[1]),
        `the page calls ${m[1]} context-only, but the registry wires it onto the band`);
    }
  }
});

test("integrations: a guest that needs setup says so instead of promising a band", () => {
  const needsSetup = guests().filter((g) => g.needsSetup).map((g) => g.name);
  assert.ok(needsSetup.length > 0, "at least one guest is marked NeedsSetup in the registry");
  const page = read("integrations.html");
  for (const name of needsSetup) {
    const row = page.match(new RegExp(`data-guest="${name}"[\\s\\S]*?</article>`))?.[0] || "";
    assert.match(row, /setup|not yet|cannot/i, `${name} must say it is not launchable as-is`);
  }
});

/* ---- the mechanism -------------------------------------------------- */

test("integrations: the two ways in are both described, and the grant limit is stated", () => {
  const page = read("integrations.html").replace(/\s+/g, " ");
  assert.match(page, /127\.0\.0\.1/, "the local endpoint");
  assert.match(page, /broker\.rogerai\.fm\/v1/, "the direct broker endpoint for a grant key");
  // A grant key reaches ONLY the holder's own nodes. Leaving that out would send someone
  // to build a remote bot against the open market with a key that cannot reach it.
  assert.match(page, /own (?:nodes|hardware)/i, "a grant key is confined to your own nodes");
  assert.match(page, /OpenAI-compatible|OpenAI API/i, "the compatibility claim");

  // The example port is the CLI's own default, not a number picked for the screenshot.
  const dflt = go("cmd", "rogerai", "main.go").match(/freePort\((\d+)\)/)?.[1];
  assert.ok(dflt, "the local endpoint's default port is declared in cmd/rogerai");
  assert.match(page, new RegExp(`127\\.0\\.0\\.1:${dflt}`),
    `the example endpoint should use the CLI's default port ${dflt}`);
});
