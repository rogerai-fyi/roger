// Wave status: render a tier's status label from web/src/data/wave-status.json at build time.
//
//   {{wave:<tier>.<label>}}   e.g. {{wave:giga.research-models.chip}}
//
// The label text is inserted verbatim (it is stored as HTML source). A token that names an
// unknown tier or label, or a label that belongs to another page, fails the build rather than
// shipping a blank. See web/README.md, "Wave status".
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const DATA = join(dirname(fileURLToPath(import.meta.url)), "..", "src", "data", "wave-status.json");
const TOKEN_RE = /\{\{wave:([a-z]+)\.([a-z-]+\.[a-z-]+)\}\}/g;

export function loadWaveStatus() {
  return JSON.parse(readFileSync(DATA, "utf8"));
}

export function resolveWaveTokens(html, page, data) {
  const out = html.replace(TOKEN_RE, (_, tierId, labelId) => {
    const tier = data.tiers.find((t) => t.id === tierId);
    if (!tier) throw new Error(`${page}: unknown wave tier "${tierId}"`);
    const label = tier.labels[labelId];
    if (!label) throw new Error(`${page}: wave tier "${tierId}" has no label "${labelId}"`);
    if (label.page !== page) throw new Error(`${page}: ${tierId}.${labelId} belongs to ${label.page}`);
    return label.text;
  });
  if (/\{\{\s*wave:/.test(out)) throw new Error(`${page}: unresolved wave token`);
  return out;
}
