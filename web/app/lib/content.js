// The facts a page prints from the data files (src/data/*.json): wave tier status, model
// card measurements, capability status. ONE renderer, shared with the legacy build: these
// are the same resolvers (scripts/wave-status.mjs, scripts/model-cards.mjs), called with one
// token at a time, so an unknown tier, label, model or field fails the build exactly as it
// does for a legacy page, and a wave label still belongs to one page.
//
//   const facts = content("research-wave-nano.html");
//   {@html facts.wave("nano.research-wave-nano.chip")}   {facts.waveStage("nano")}
//   {@html facts.card("nano.ctx")}  {facts.cardNum("nano.ctx")}  {@html facts.cardList("nano.lineage")}
//   {facts.cap("laptop.label")}
//
// Server-only: it reads files. Every route is prerendered (csr = false), so this never
// reaches a browser bundle.
// The resolvers are pure (html, page, data); the data files are imported as modules so the
// bundle carries them (the scripts' own loaders locate the files beside the script, which a
// bundle moves). build.mjs rebuilds the app when src/data changes (the input stamp).
import { resolveWaveTokens, resolveCapTokens } from "../../scripts/wave-status.mjs";
import { resolveCardTokens } from "../../scripts/model-cards.mjs";
import { decodeHTML } from "entities";
import wave from "../../src/data/wave-status.json";
import cards from "../../src/data/model-cards.json";
import caps from "../../src/data/capabilities.json";

const d = { wave, cards, caps };

// A fact in an attribute (data-stage="...") cannot carry markup: the HTML source of the
// label is decoded to text, which is what a browser shows for the legacy entity spelling.
const toText = (html) => decodeHTML(html.replace(/<[^>]+>/g, ""));

export function content(page) {
  const wave = (token) => resolveWaveTokens(`{{${token}}}`, page, d.wave);
  const card = (token) => resolveCardTokens(`{{${token}}}`, page, d.cards);
  const facts = {
    wave: (ref) => wave(`wave:${ref}`),
    waveStage: (tier) => wave(`wave-stage:${tier}`),
    waveBand: (tier) => wave(`wave-band:${tier}`),
    waveAir: (tier) => wave(`wave-air:${tier}`),
    card: (ref) => card(`card:${ref}`),
    cardNum: (ref) => card(`card-num:${ref}`),
    cardLo: (ref) => card(`card-lo:${ref}`),
    cardHi: (ref) => card(`card-hi:${ref}`),
    cardRef: (ref) => card(`card-ref:${ref}`),
    cardList: (ref) => card(`card-list:${ref}`),
    cardNotes: (model) => card(`card-notes:${model}`),
    cap: (ref) => resolveCapTokens(`{{cap:${ref}}}`, page, d.caps),
  };
  // facts.t.wave("…"): the same fact as plain text, for an attribute value
  facts.t = Object.fromEntries(Object.entries(facts).map(([k, f]) => [k, (...args) => toText(f(...args))]));
  return facts;
}
