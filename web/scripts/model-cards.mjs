// Model cards: render a Wave model page's facts from web/src/data/model-cards.json at build time.
//
//   {{card:<model>.<item>}}       a measured item's value (a fire item is wrapped in data-fire),
//                                 or a top-level string field (name, size, author, ...)
//   {{card-num:<model>.<item>}}   the item's bare number, for a meter (style="--v:...")
//   {{card-lo:...}} {{card-hi:...}} the ends of a measured range
//   {{card-ref:<model>.<item>}}   "R.373, 2026-10-18"
//   {{card-list:<model>.<field>}} one <li> per entry of a list field, with its R-number if cited
//   {{card-notes:<model>}}        the Measured footnote: one <li> per R-number, its date, and what it measured
//
// A number on a model page appears in exactly one place: the data file. An unknown model, item or
// list, or a leftover token, fails the build rather than shipping a blank (as wave-status.mjs does).
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const DATA = join(dirname(fileURLToPath(import.meta.url)), "..", "src", "data", "model-cards.json");
const TOKEN_RE = /\{\{card(|-num|-lo|-hi|-ref|-list):([a-z0-9]+)\.([A-Za-z0-9-]+)\}\}/g;
const NOTES_RE = /\{\{card-notes:([a-z0-9]+)\}\}/g;

export function loadModelCards() {
  return JSON.parse(readFileSync(DATA, "utf8"));
}

const refTag = (ref) => ` <span class="wm-ref">${ref}</span>`;

export function resolveCardTokens(html, page, data) {
  const model = (id) => {
    const m = data.models.find((x) => x.id === id);
    if (!m) throw new Error(`${page}: unknown model "${id}"`);
    return m;
  };
  const item = (m, key) => {
    const it = (m.measured || []).find((x) => x.id === key);
    if (!it) throw new Error(`${page}: model "${m.id}" has no item "${key}"`);
    return it;
  };
  const out = html.replace(TOKEN_RE, (_, kind, id, key) => {
    const m = model(id);
    if (kind === "-list") {
      if (!Array.isArray(m[key])) throw new Error(`${page}: model "${id}" has no list "${key}"`);
      return m[key].map((e) => (typeof e === "string" ? `<li>${e}</li>`
        : `<li${e.state ? ` data-state="${e.state}"` : ""}>${e.title ? `<b>${e.title}</b> ` : ""}${e.text}${e.ref ? refTag(e.ref) : ""}</li>`)).join("");
    }
    if (kind === "" && typeof m[key] === "string") return m[key];
    const it = item(m, key);
    if (kind === "-num") {
      if (typeof it.num !== "number") throw new Error(`${page}: ${id}.${key} has no number`);
      return String(it.num);
    }
    if (kind === "-lo" || kind === "-hi") {
      const v = it[kind.slice(1)];
      if (typeof v !== "number") throw new Error(`${page}: ${id}.${key} has no range`);
      return String(v);
    }
    if (kind === "-ref") return `${it.ref}, ${it.date}`;
    return it.fire ? `<span data-fire="">${it.value}</span>` : it.value;
  }).replace(NOTES_RE, (_, id) => {
    const m = model(id);
    const byRef = new Map();
    for (const it of m.measured || []) {
      if (!byRef.has(it.ref)) byRef.set(it.ref, { date: it.date, labels: [] });
      byRef.get(it.ref).labels.push(it.label);
    }
    return [...byRef].map(([ref, r]) => `<li><b>${ref}</b> ${r.date}: ${r.labels.join(", ")}.</li>`).join("");
  });
  if (/\{\{\s*card/.test(out)) throw new Error(`${page}: unresolved card token`);
  return out;
}
