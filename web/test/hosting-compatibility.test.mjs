import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "../..");
const read = (name) => fs.readFileSync(path.join(root, name), "utf8");

test("provider copy separates auto-detection from protocol compatibility", () => {
  const readme = read("README.md");
  const sharePage = read("web/src/broadcasts-share-gpu-earn.html");

  for (const text of [readme, sharePage]) {
    assert.match(text, /Unsloth Studio/);
    assert.match(text, /--upstream/);
  }
  assert.match(readme, /not a vendor allowlist/);
  assert.match(readme, /trainer, optimizer, quantizer/);
});

test("compatibility note states the supported contract and current gaps", () => {
  const doc = read("docs/hosting-compatibility.md");

  for (const required of [
    "GET /v1/models",
    "POST /v1/chat/completions",
    "OpenAI Responses",
    "Anthropic Messages",
    "embeddings or reranking",
    "image generation",
    "host admin routes",
  ]) {
    assert.ok(doc.includes(required), `compatibility note is missing ${required}`);
  }
});
