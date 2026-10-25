// Wave status: render a tier's status label from web/src/data/wave-status.json at build time.
//
//   {{wave:<tier>.<label>}}   e.g. {{wave:giga.research-models.chip}}
//   {{wave-stage:<tier>}}     the tier's live stage as one public word, e.g. ON AIR
//
// The label text is inserted verbatim (it is stored as HTML source). A token that names an
// unknown tier or label, or a label that belongs to another page, fails the build rather than
// shipping a blank. See web/README.md, "Wave status".
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const DATA = join(dirname(fileURLToPath(import.meta.url)), "..", "src", "data", "wave-status.json");
const TOKEN_RE = /\{\{wave:([a-z]+)\.([a-z-]+\.[a-z-]+)\}\}/g;
const STAGE_RE = /\{\{wave-stage:([a-z]+)\}\}/g;

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
  }).replace(STAGE_RE, (_, tierId) => {
    const tier = data.tiers.find((t) => t.id === tierId);
    if (!tier) throw new Error(`${page}: unknown wave tier "${tierId}"`);
    const word = data.stageWords?.[tier.stage];
    if (!word) throw new Error(`${page}: no stage word for "${tier.stage}" (${tierId})`);
    return word;
  });
  if (/\{\{\s*wave[:-]/.test(out)) throw new Error(`${page}: unresolved wave token`);
  return out;
}

// Capability status: adapters, base skills and device rows from web/src/data/capabilities.json.
//
//   {{cap:<id>.<field>}}   field: name | stage | label (the stage's public words) | description
//
// Not page-scoped like the Wave labels: every page that names a capability shows the same
// status, so one token reads one entry wherever it appears. An unknown id or field fails the
// build. See features/web/sensor_positioning.feature.
const CAPS = join(dirname(DATA), "capabilities.json");
const CAP_RE = /\{\{cap:([a-z-]+)\.([a-z]+)\}\}/g;
const CAP_FIELDS = ["name", "stage", "label", "description"];

export function loadCapabilities() {
  return JSON.parse(readFileSync(CAPS, "utf8"));
}

export function resolveCapTokens(html, page, data) {
  const out = html.replace(CAP_RE, (_, id, field) => {
    const e = [...data.capabilities, ...data.devices].find((x) => x.id === id);
    if (!e) throw new Error(`${page}: unknown capability "${id}"`);
    if (!CAP_FIELDS.includes(field)) throw new Error(`${page}: capability field "${field}" is not one of ${CAP_FIELDS.join(", ")}`);
    return field === "label" ? data.stageLabels[e.stage] : e[field];
  });
  if (/\{\{\s*cap:/.test(out)) throw new Error(`${page}: unresolved cap token`);
  return out;
}
