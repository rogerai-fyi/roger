import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (name) => readFileSync(path.join(DIST, name), "utf8");
const text = (name) => read(name).replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

test("Research is a concise first-class destination", () => {
  const page = read("research.html");
  assert.match(page, /<title>RogerAI Research/);
  assert.match(page, /RogerAI Labs/);
  assert.match(page, /aria-current="page"/);
  assert.match(page, /rel="canonical" href="https:\/\/rogerai\.fm\/research\.html"/);
  const main = page.match(/<main[\s\S]*?<\/main>/)?.[0] || "";
  assert.ok(main.length < 15000, `research content is concise (${main.length} bytes)`);
});

test("the page leads with the work, not a biography of the company", () => {
  const page = read("research.html");
  assert.match(page, /Models built for local constraints/i);
  assert.match(page, /smaller|less memory|local hardware/i);
  assert.doesNotMatch(page, /team|founders?|venture|employees|our origins/i);
});

test("the model list gives every program a reason and honest status", () => {
  const page = text("research.html");
  for (const model of [
    "DeepSeek-V4-Flash MTP",
    "Kimi-K3",
    "Wave Nano",
    "Wave Micro",
    "Roger Edge",
  ]) assert.match(page, new RegExp(model));
  for (const reason of [
    /practical local inference/i,
    /high-memory workstation/i,
    /local text and tool use/i,
    /routing, extraction, and triage/i,
    /wake, voice activity, sensing, and fixed commands/i,
  ]) assert.match(page, reason);
  assert.match(page, /Research build/i);
  assert.match(page, /In progress/i);
  assert.match(page, /In design/i);
  assert.match(page, /No Wave checkpoint has been released/i);
});

test("upstream work is never presented as RogerAI pretraining", () => {
  const page = read("research.html");
  assert.match(page, /Optimized by RogerAI/);
  assert.match(page, /Upstream: DeepSeek-V4-Flash/);
  assert.match(page, /Upstream: Kimi-K3/);
  assert.doesNotMatch(page, /RogerAI (?:pre)?trained (?:DeepSeek|Kimi)/i);
});

test("evidence and local-use promises stay explicit", () => {
  const page = text("research.html");
  assert.match(page, /hardware, artifact, runtime, settings, and raw results/i);
  assert.match(page, /Negative results remain available/i);
  assert.match(page, /download and run qualifying artifacts locally/i);
  assert.match(page, /network is optional/i);
  const html = read("research.html");
  assert.match(html, /href="\/broadcasts\.html"/);
  assert.match(html, /href="https:\/\/huggingface\.co\/rogerai-fyi"/);
  assert.match(html, /href="https:\/\/github\.com\/rogerai-fyi\/roger"/);
});

test("downloads are real or clearly marked as coming soon", () => {
  const page = read("research.html");
  assert.match(page, /href="https:\/\/huggingface\.co\/rogerai-fyi\/DeepSeek-V4-Flash-MTP-GGUF"/);
  assert.equal((page.match(/Hugging Face · coming soon/g) || []).length, 4);
  assert.equal((page.match(/aria-disabled="true"/g) || []).length, 4);
  assert.doesNotMatch(page, /href="[^"]*(?:placeholder|coming-soon)[^"]*"/i);
});

test("developers have a compact path from artifact to local inference", () => {
  const page = read("research.html");
  assert.match(page, /id="developers"/);
  assert.match(page, /Download/);
  assert.match(page, /Run locally/);
  assert.match(page, /Reproduce/);
  assert.match(page, /huggingface-cli download/);
  assert.match(page, /llama-server/);
  assert.match(page, /OpenAI-compatible endpoint/i);
  assert.match(page, /artifact version[^<]*hardware[^<]*runtime[^<]*settings/i);
});

test("research and the live network directory remain distinct", () => {
  const page = read("research.html");
  assert.match(page, /href="\/models\.html"/);
  assert.match(page, /live network models/i);
  assert.match(page, /research programs/i);
});

test("new institutional pages contain no em dash character", () => {
  for (const name of ["research.html", "company.html", "index.html"]) {
    assert.doesNotMatch(read(name), /—/, `${name} contains an em dash`);
  }
});
