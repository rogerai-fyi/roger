#!/usr/bin/env node
// Verify that every RogerAI-owned artifact the site ADVERTISES is actually
// public.
//
// Why this exists: v5.4.8 shipped a homepage headline reading
// "WAVE MICRO v1.0 - AVAILABLE - 350M - APACHE-2.0", a "Download or Run Wave"
// button, and schema.org SoftwareSourceCode structured data, all pointing at a
// HuggingFace repo that answered 401, plus two GitHub "source + recipe" and
// "evaluations" links that answered 404. The unit suite was 155/155 green
// throughout, because nothing in it could see outside the dist tree. A release
// claim is only as good as the artifact behind it, and that is a NETWORK fact.
//
// Scope is deliberately narrow: only hosts/paths RogerAI owns and publishes to.
// Third-party links are somebody else's uptime and would make this flaky.
//
// This is NOT part of `npm test` - unit tests must stay offline and fast. Run it
// before a deploy:  npm run verify:artifacts
//
// Exit codes: 0 = every advertised artifact is publicly reachable, 1 = at least
// one is not (or the build failed).

import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");

// Artifact hosts RogerAI publishes to. A URL under one of these is a public
// promise: if the site links it, a stranger must be able to open it.
const OWNED = [
  /^https:\/\/huggingface\.co\/rogerai-fyi\/[A-Za-z0-9._/#-]+$/,
  /^https:\/\/github\.com\/rogerai-fyi\/[A-Za-z0-9._/-]+$/,
];

const TIMEOUT_MS = 15000;
const CONCURRENCY = 6;

// Recursive: pages in subdirectories (dist/roger/index.html, and anything added
// later) advertise artifacts exactly like top-level ones. A non-recursive scan
// reports "all clear" for files it never opened.
function distPages(dir = DIST, prefix = "") {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const rel = prefix ? `${prefix}/${entry.name}` : entry.name;
    if (entry.isDirectory()) out.push(...distPages(path.join(dir, entry.name), rel));
    else if (entry.name.endsWith(".html")) out.push(rel);
  }
  return out;
}

// Collect advertised URLs -> the pages that advertise them, so a failure names
// the file to fix rather than just the dead link.
function collect() {
  const seen = new Map();
  for (const page of distPages()) {
    const html = readFileSync(path.join(DIST, page), "utf8");
    for (const m of html.matchAll(/https:\/\/[A-Za-z0-9._~:/?#@!$&'*+,;=%-]+/g)) {
      // Strip a trailing quote/paren/punctuation the regex may have swallowed.
      const url = m[0].replace(/["'<>)\]}.,]+$/, "");
      if (!OWNED.some((re) => re.test(url))) continue;
      if (!seen.has(url)) seen.set(url, new Set());
      seen.get(url).add(page);
    }
  }
  return seen;
}

async function check(url) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  try {
    // HuggingFace and GitHub both answer HEAD, but a fragment-bearing URL
    // (…#limitations) must be checked without the fragment - servers never see it.
    const target = url.split("#")[0];
    let res = await fetch(target, { method: "HEAD", redirect: "follow", signal: ctrl.signal });
    // Some endpoints refuse HEAD; fall back to a ranged GET rather than pulling weights.
    if (res.status === 405 || res.status === 501) {
      res = await fetch(target, {
        method: "GET",
        redirect: "follow",
        signal: ctrl.signal,
        headers: { Range: "bytes=0-0" },
      });
    }
    return { url, status: res.status, ok: res.status >= 200 && res.status < 300 };
  } catch (err) {
    return { url, status: 0, ok: false, error: err.name === "AbortError" ? "timeout" : String(err) };
  } finally {
    clearTimeout(timer);
  }
}

// The go-import tag is an artifact too, and the most brittle one: `go get` fetches
// the module path as a URL with NO trailing slash, so it depends on the edge resolving
// /roger/v5 to /roger/v5/index.html. Nothing offline can prove that, and if it is wrong
// `go install rogerai.fm/roger/v5/...` fails for every user while the whole site looks fine.
//
// The VERSIONED path is what must be probed. Go filters candidate tags by the module
// path's major suffix, so the module is `rogerai.fm/roger/v5` and that - not the bare
// /roger page - is the URL the toolchain actually fetches.
const GO_GET_URL = "https://rogerai.fm/roger/v5?go-get=1";
const GO_IMPORT_PREFIX = "rogerai.fm/roger/v5 ";

async function checkGoImport() {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  try {
    const res = await fetch(GO_GET_URL, { redirect: "follow", signal: ctrl.signal });
    if (!res.ok) return { ok: false, why: `status ${res.status}` };
    const body = await res.text();
    const m = body.match(/<meta\s+name="go-import"\s+content="([^"]+)"/);
    if (!m) return { ok: false, why: "200, but no go-import meta tag in the response" };
    if (!m[1].startsWith(GO_IMPORT_PREFIX)) {
      return { ok: false, why: `go-import declares ${JSON.stringify(m[1])}` };
    }
    return { ok: true };
  } catch (err) {
    return { ok: false, why: err.name === "AbortError" ? "timeout" : String(err) };
  } finally {
    clearTimeout(timer);
  }
}

async function main() {
  execFileSync("node", ["build.mjs"], { cwd: WEB, stdio: "ignore" });

  const goImport = await checkGoImport();
  console.log(`${goImport.ok ? "ok  " : "DEAD"}      ${GO_GET_URL}`);

  const advertised = collect();
  if (advertised.size === 0) {
    // Finding nothing to check is NOT a pass. If a refactor, a selector change or an
    // empty dist tree stops this script seeing artifact links, the honest report is
    // "I checked nothing", and the run fails regardless of how the go-import probe
    // went. A gate that goes green because it parsed nothing is
    // worse than no gate: it converts silence into false assurance, which is the
    // exact class of failure this script was written to catch.
    console.error(
      "verify-artifacts: no RogerAI-owned artifact links found in dist/ - " +
        "either the site advertises none, or this check stopped seeing them."
    );
    if (!goImport.ok) {
      console.error(`\nverify-artifacts: ${GO_GET_URL} is not serving the go-import tag (${goImport.why}).\n`);
    }
    // Always fail. Collecting zero means this check has gone blind, and a blind
    // check reporting success is the failure mode being guarded against.
    return 1;
  }

  const urls = [...advertised.keys()].sort();
  const results = [];
  for (let i = 0; i < urls.length; i += CONCURRENCY) {
    results.push(...(await Promise.all(urls.slice(i, i + CONCURRENCY).map(check))));
  }

  const failed = results.filter((r) => !r.ok);
  if (!goImport.ok) {
    console.error(
      `\nverify-artifacts: ${GO_GET_URL} is not serving the go-import tag (${goImport.why}).\n` +
        "`go install rogerai.fm/roger/v5/cmd/rogerai@latest` cannot work until it does.\n"
    );
  }
  for (const r of results.sort((a, b) => a.url.localeCompare(b.url))) {
    const mark = r.ok ? "ok  " : "DEAD";
    console.log(`${mark} ${String(r.status).padStart(3)}  ${r.url}`);
  }

  if (failed.length) {
    console.error(`\nverify-artifacts: ${failed.length} advertised artifact(s) are NOT publicly reachable.\n`);
    for (const r of failed) {
      const pages = [...advertised.get(r.url)].sort().join(", ");
      console.error(`  ${r.url}`);
      console.error(`    status: ${r.status || r.error}`);
      console.error(`    advertised by: ${pages}`);
    }
    console.error(
      "\nEither publish the artifact, or stop advertising it. A page must not claim a\n" +
        "model is available until a stranger can download it.\n"
    );
  }
  // The go-import tag is ADVISORY, not blocking. The Cloudflare hop now exists and is
  // applied (/roger 301s to /roger/ with ?go-get=1 intact), but /roger/ itself only
  // answers once this site is deployed, so a blocking check would still be red on every
  // run made before a deploy - and a gate that is always red is a gate everyone learns
  // to ignore. Nothing advertises `go install` today, so a missing tag breaks no promise.
  // Restore this to blocking in the same change that restores the README claim, once
  // https://rogerai.fm/roger/ answers 200. cf-edge.mjs --check guards the hop meanwhile.
  if (failed.length) return 1;

  console.log(`\nverify-artifacts: all ${results.length} advertised artifacts are public.`);
  return 0;
}

process.exit(await main());
