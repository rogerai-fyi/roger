// Executable contract for features/domain/*.feature. These assertions intentionally
// cover only portable product behavior; deployment-provider details live outside the
// public repository.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const FM = "https://rogerai.fm";

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

function filesBelow(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = path.join(dir, name);
    return statSync(p).isDirectory() ? filesBelow(p) : [p];
  });
}

test("every indexable page and sitemap entry is canonical on rogerai.fm", () => {
  const pages = filesBelow(DIST).filter((p) => p.endsWith(".html"));
  for (const page of pages) {
    const html = readFileSync(page, "utf8");
    if (/name="robots" content="noindex"/.test(html)) continue;
    const canonicals = [...html.matchAll(/rel="canonical" href="([^"]+)"/g)].map((m) => m[1]);
    assert.equal(canonicals.length, 1, `${path.basename(page)} has exactly one canonical`);
    assert.ok(canonicals[0].startsWith(`${FM}/`), `${path.basename(page)} canonical uses rogerai.fm`);
  }
  const sitemap = readFileSync(path.join(DIST, "sitemap.xml"), "utf8");
  assert.match(sitemap, /<loc>https:\/\/rogerai\.fm\//);
  assert.doesNotMatch(sitemap, /<loc>https:\/\/rogerai\.fyi\//);
});

test("homepage identity metadata is entirely canonical on rogerai.fm", () => {
  const html = readFileSync(path.join(DIST, "index.html"), "utf8");
  for (const attr of ["property=\"og:url\"", "property=\"og:image\"", "name=\"twitter:image\""]) {
    assert.match(html, new RegExp(`<meta ${attr} content="https://rogerai\\.fm/`), attr);
  }
  for (const id of ["#org", "#site"]) assert.match(html, new RegExp(`https://rogerai\\.fm/${id}`));
  assert.doesNotMatch(html, /https:\/\/rogerai\.fyi\/(?:#(?:org|site))?/);
});

test("new browser surfaces use the branded broker while the CSP permits both compatibility hosts", () => {
  const js = filesBelow(SRC)
    .filter((p) => p.endsWith(".js"))
    .map((p) => readFileSync(p, "utf8"))
    .join("\n");
  assert.match(js, /https:\/\/broker\.rogerai\.fm/);
  const headers = readFileSync(path.join(SRC, "_headers"), "utf8");
  assert.match(headers, /connect-src[^;]*https:\/\/broker\.rogerai\.fm/);
  assert.match(headers, /connect-src[^;]*https:\/\/broker\.rogerai\.fyi/);
});

test("new public remote links use rogerai.fm and the mobile association remains direct", () => {
  const remote = readFileSync(path.join(SRC, "js/r.js"), "utf8");
  assert.match(remote, /https:\/\/broker\.rogerai\.fm/);
  const association = readFileSync(path.join(SRC, ".well-known/apple-app-site-association"), "utf8");
  const parsed = JSON.parse(association);
  assert.ok(parsed.applinks.details.length > 0);
  assert.ok(parsed.applinks.details.some((d) =>
    (d.paths || []).some((p) => p.startsWith("/r.html")) ||
    (d.components || []).some((c) => String(c["/"] || "").startsWith("/r.html"))));
});
