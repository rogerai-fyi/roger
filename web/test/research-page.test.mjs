// Behavior lock for features/web/research_labs.feature.
// Assertions run against built HTML so shared-nav includes, metadata, CSS
// integration, and the no-JavaScript static content are tested together.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (name) => readFileSync(path.join(DIST, name), "utf8");
const articleFor = (page, heading) => {
  const articles = [...page.matchAll(/<article\b[\s\S]*?<\/article>/g)].map((m) => m[0]);
  const article = articles.find((candidate) => candidate.includes(heading));
  assert.ok(article, `article exists for ${heading}`);
  return article;
};

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

test("Research is a first-class, buildable navigation destination", () => {
  assert.ok(existsSync(path.join(DIST, "research.html")), "research.html builds");
  const home = read("index.html");
  assert.match(home, /href="\/research\.html"[^>]*>Research<\/a>/);
  const page = read("research.html");
  assert.match(page, /RogerAI Labs/);
  assert.match(page, /aria-current="page"/);
});

test("metadata describes research without claiming an unreleased model", () => {
  const page = read("research.html");
  assert.match(page, /<title>RogerAI Research/);
  assert.match(page, /name="description" content="[^"]*open edge-model and inference research/i);
  assert.match(page, /rel="canonical" href="https:\/\/rogerai\.fm\/research\.html"/);
  assert.match(page, /property="og:url" content="https:\/\/rogerai\.fm\/research\.html"/);
  assert.doesNotMatch(page, /Wave Nano[^<]*(available now|download now)/i);
});

test("research and the live broker directory are explicitly distinct", () => {
  const page = read("research.html");
  assert.match(page, /href="\/models\.html"/);
  assert.match(page, /live broker stations/i);
  assert.match(page, /not every research (?:artifact|project)[^<]*currently served/i);
  assert.match(page, /not every broker station[^<]*RogerAI model/i);
});

test("current projects have honest status, lineage, parent, and unresolved gates", () => {
  const page = read("research.html");
  const deepseek = articleFor(page, "DeepSeek-V4-Flash MTP");
  assert.match(deepseek, /Optimized by RogerAI/);
  assert.match(deepseek, /Upstream parent:[\s\S]*DeepSeek-V4-Flash/i);
  assert.match(deepseek, /MIT License/);
  assert.match(deepseek, /converter[^<]*runtime[^<]*MTP[^<]*benchmark[^<]*artifact/i);
  assert.match(deepseek, /project summary, not a stable model card/i);
  assert.doesNotMatch(deepseek, /pretrained/i);

  const kimi = articleFor(page, "Kimi-K3 on a 256 GB system");
  assert.match(kimi, /Optimized by RogerAI/);
  assert.match(kimi, /Upstream parent:[\s\S]*Kimi-K3/i);
  assert.match(kimi, /Kimi K3 License/);
  assert.match(kimi, /A_log/);
  assert.match(kimi, /16,376-token/i);
  assert.match(kimi, /per-head-truncated/i);
  assert.match(kimi, /Pruning quality[^<]*fit gates remain unresolved/i);
  assert.doesNotMatch(kimi, /pretrained/i);

  const wave = articleFor(page, "Roger Wave Nano");
  assert.match(wave, /(?:Research|In progress)/i);
  assert.match(wave, /Granite-4\.0-350M[^<]*Tool/i);
  assert.match(wave, /SmolLM2-360M-Instruct[^<]*Text\/control/i);
  assert.match(wave, /no release/i);
  assert.match(wave, /reviewed data[^<]*resume smoke[^<]*training approval/i);
  assert.doesNotMatch(wave, /(?:beats|downloads?|tok\/s)/i);
});

test("lineage taxonomy and component-specific openness are visible", () => {
  const page = read("research.html");
  for (const badge of [
    "Trained by RogerAI", "Adapted by RogerAI",
    "Optimized by RogerAI", "Verified by RogerAI",
  ]) assert.match(page, new RegExp(badge));
  assert.match(page, /open weights/i);
  assert.match(page, /Open Source AI[^<]*reproducibility checklist/i);
  assert.match(page, /PolyForm[^<]*source-available/i);
  assert.doesNotMatch(page, /PolyForm[^<]*open source/i);
});

test("local use is independent and RogerAI publishing is optional", () => {
  const page = read("research.html");
  assert.match(page, /download and run[^<]*locally/i);
  assert.match(page, /publishing[^<]*RogerAI[^<]*optional/i);
  for (const value of ["discovery", "routing", "payments", "failover", "signed receipts"]) {
    assert.match(page, new RegExp(value, "i"));
  }
  assert.doesNotMatch(page, /remote inference[^<]*(must|required)[^<]*RogerAI/i);
});

test("device program separates Wave, Raspberry Pi evidence, and Roger Edge", () => {
  const page = read("research.html");
  assert.match(page, /Raspberry Pi[^<]*exact board/i);
  for (const field of [
    "OS", "runtime", "format", "quantization", "context",
    "cold load", "peak memory", "prompt speed", "decode speed",
  ]) assert.match(page, new RegExp(field, "i"));
  assert.match(page, /generic NPU support[^<]*not/i);
  assert.match(page, /full delegation[^<]*partial delegation[^<]*CPU fallback/i);

  assert.match(page, /ESP32[^<]*does not run[^<]*350M/i);
  assert.match(page, /Roger Edge/);
  for (const task of ["wake", "VAD", "fixed commands", "sensing", "vision"]) {
    assert.match(page, new RegExp(task, "i"));
  }
  assert.match(page, /LILYGO T-Embed/i);
  assert.match(page, /nearby[^<]*(?:Pi|phone)/i);
  assert.match(page, /consent/i);
  assert.match(page, /nonce[^<]*expiry[^<]*issuer/i);
  assert.match(page, /ESP32-P4[^<]*no native Wi-Fi or Bluetooth/i);
});

test("benchmark and publication policy preserves evidence and negative results", () => {
  const page = read("research.html");
  for (const field of [
    "artifact version", "evaluation harness", "raw results",
    "RogerAI measurements", "upstream vendor measurements",
  ]) assert.match(page, new RegExp(field, "i"));
  assert.match(page, /same harness/i);
  assert.match(page, /negative and superseded results/i);
  assert.match(page, /computer-science and systems-engineering experience/i);
});

test("Wave family tiers are programs with gates, never products", () => {
  const page = read("research.html");
  const nano = articleFor(page, "<h3>Wave Nano</h3>");
  assert.match(nano, /PROTOTYPE GRID DESIGNED/i);
  assert.match(nano, /Granite[^<]*typed tools[^<]*SmolLM2[^<]*text control/i);
  assert.match(nano, /same[^<]*example bytes/i);
  assert.match(nano, /One combined model[^<]*two specialists[^<]*no v0 release/i);
  assert.doesNotMatch(nano, /(?:available now|download|tok\/s|beats)/i);
  const micro = articleFor(page, "Wave Micro");
  assert.match(micro, /IN DESIGN/i);
  assert.match(micro, /task specialists/i);
  assert.doesNotMatch(micro, /(?:available now|download|tok\/s|beats)/i);
  const roadmap = articleFor(page, "Wave Embed");
  assert.match(roadmap, /Wave VL/);
  assert.match(roadmap, /Wave Audio/);
  assert.match(roadmap, /roadmap/i);
  assert.match(page, /Roger Edge<\/b> covers[\s\S]*microcontroller/i);
});

test("Wave family infographic exposes size, role, order, and release honesty", () => {
  const page = read("research.html");
  assert.match(page, /<figure class="wave-map"[^>]*aria-labelledby=/);
  assert.match(page, /<svg[^>]*role="img"/);
  for (const tier of ["Roger Edge", "Wave Micro", "Wave Nano", "Wave Embed", "Wave VL \\+ Audio"]) {
    assert.match(page, new RegExp(tier));
  }
  for (const size of ["KB–MB", "&lt;100M", "~350M", "100–300M", "~0\\.3–2B"]) {
    assert.match(page, new RegExp(size));
  }
  assert.match(page, /Sensors become typed events[^<]*Micro[^<]*Nano/i);
  assert.match(page, /first measured program/i);
});

test("industry section sells patterns and air-gapped locality, not invented customers", () => {
  const page = read("research.html");
  for (const vertical of [
    "MANUFACTURING", "WAREHOUSES &amp; LOGISTICS",
    "ENERGY &amp; HEAVY ASSETS", "DEFENSE &amp; PUBLIC SECTOR",
  ]) assert.match(page, new RegExp(vertical));
  assert.match(page, /air-gapped/i);
  assert.match(page, /OT\s+firewall/i);
  assert.match(page, /Patterns, not case studies/i);
  assert.match(page, /does not name customers it does not[\s\S]*have/i);
  assert.match(page, /closed-loop control[\s\S]*deterministic/i);
});

test("services are offered without conditioning model use on them", () => {
  const page = read("research.html");
  assert.match(page, /Model optimization engineering/i);
  assert.match(page, /Edge deployment/i);
  assert.match(page, /Benchmarking and model selection/i);
  assert.match(page, /Industrial pilots/i);
  assert.match(page, /never requires buying services/i);
  assert.match(page, /services never require\s+the broker/i);
  assert.match(page, /mailto:labs@rogerai\.fm/);
});

test("developers section names standard formats and reproduction paths", () => {
  const page = read("research.html");
  assert.match(page, /GGUF/);
  assert.match(page, /llama\.cpp/);
  assert.match(page, /href="https:\/\/huggingface\.co\/rogerai-fyi"/);
  assert.match(page, /href="https:\/\/github\.com\/rogerai-fyi"/);
  assert.match(page, /serve command/i);
  assert.match(page, /optionally join the RogerAI network/i);
});

test("essential research content and links are static and require no client JavaScript", () => {
  const page = read("research.html");
  assert.match(page, /<main[\s\S]*RogerAI Labs[\s\S]*DeepSeek-V4-Flash MTP[\s\S]*Kimi-K3[\s\S]*Roger Wave Nano/);
  assert.match(page, /href="https:\/\/github\.com\/rogerai-fyi\/roger"/);
  assert.match(page, /href="\/broadcasts\.html"/);
  assert.doesNotMatch(page, /Loading research|placeholder metric/i);
});
