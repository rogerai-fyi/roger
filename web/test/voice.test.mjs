// The founder's voice, held by a test (2026-10, broadcast 016): plain, specific, declarative.
// Short sentences, concrete nouns, verbs over nouns, no filler, no AI tics. A page opts in by
// name below; the denylist and the sentence caps run over its visible prose (the article body:
// paragraphs, list items, definitions, captions and headings), never over code, figure
// descriptions or the schema block. Question headings are the SEO format and stay.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");

export const PAGES = ["broadcasts-how-llms-work.html"];
export const MAX_SENTENCE = 28;   // words
export const MAX_AVERAGE = 16;    // words

// the AI sound: each is a phrase the founder struck, or a construction that rings hollow
export const TICS = [
  /\blet's\b/i, /\bimagine\b/i, /\bin other words\b/i, /\bsimply put\b/i, /\bit'?s important to note\b/i,
  /\bcrucial\b/i, /\bkey\b(?! to)/i, /\brobust\b/i, /\bseamless(ly)?\b/i, /\bleverage\b/i, /\bunlock\b/i,
  /\bharness(es|ed|ing)?\b/i, /\bdelve\b/i, /\blandscape\b/i, /\bjourney\b/i, /\bgame[- ]changer\b/i,
  /\bwe believe\b/i, /!/,
  /\bnot (just|only|merely) [^.;:]{1,40}[,;:]? (it|this|that)('s| is)\b/i,   // "not just X, it's Y"
  /\b(isn't|is not|aren't|are not) [^.;:]{1,40}; (it|this|that|they)('s| is| are)\b/i,   // "X isn't Y; it's Z"
];

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// the reader's prose: text of the article body's p, li, dt, dd, h2, h3 and captions
export function prose(html) {
  const body = (html.match(/<div class="bc-post__body">[\s\S]*?<\/article>/) || [""])[0]
    .replace(/<!--[\s\S]*?-->/g, "").replace(/<script\b[\s\S]*?<\/script>/g, "").replace(/<style\b[\s\S]*?<\/style>/g, "")
    .replace(/<svg\b[\s\S]*?<\/svg>/g, "").replace(/<(code|kbd)\b[^>]*>[\s\S]*?<\/\1>/g, " code ");
  const lead = (html.match(/<blockquote class="bc-answer"[\s\S]*?<\/blockquote>/) || [""])[0] + (html.match(/<p class="bc-post__dek">[\s\S]*?<\/p>/) || [""])[0];
  const out = [];
  for (const [, tag, inner] of (body + lead).matchAll(/<(p|li|dt|dd|h2|h3|figcaption|blockquote|small)\b[^>]*>([\s\S]*?)<\/\1>/g)) {
    if (tag === "li" && /<(p|dd)\b/.test(inner)) continue;      // a list item that wraps paragraphs is counted by them
    const t = inner.replace(/<[^>]+>/g, " ").replace(/&[a-z]+;|&#\d+;/g, (e) => ({ "&amp;": "&", "&rsquo;": "'", "&lsquo;": "'", "&ldquo;": '"', "&rdquo;": '"', "&middot;": " ", "&nbsp;": " ", "&hellip;": "...", "&rarr;": " ", "&sect;": "" }[e] ?? " "))
      .replace(/\s+/g, " ").trim();
    if (t) out.push(t);
  }
  return out;
}

// a caption or a label ("FIG. 3 · ...") is a fragment, not a sentence; count only runs that
// end in a full stop, question mark or colon, split on those
export function sentences(blocks) {
  const out = [];
  for (const b of blocks) {
    if (/^FIG\. \d+/.test(b)) continue;
    for (const s of b.split(/(?<=[.?!:])\s+(?=[A-Z"(])/)) {
      const words = s.replace(/[^\w'\-%]+/g, " ").trim().split(/\s+/).filter((w) => /[a-z0-9]/i.test(w));
      if (words.length >= 3) out.push({ text: s.trim(), words: words.length });
    }
  }
  return out;
}

for (const page of PAGES) {
  test(`voice: ${page} carries none of the AI tics`, () => {
    const hits = [];
    for (const b of prose(read(page))) for (const re of TICS) if (re.test(b)) hits.push(`${re}: "${b.slice(0, 90)}"`);
    assert.deepEqual(hits, []);
  });

  test(`voice: ${page} keeps every sentence under ${MAX_SENTENCE} words and averages under ${MAX_AVERAGE}`, () => {
    const all = sentences(prose(read(page)));
    assert.ok(all.length >= 60, `enough prose to measure (${all.length} sentences)`);
    const long = all.filter((s) => s.words > MAX_SENTENCE).map((s) => `${s.words}: ${s.text}`);
    assert.deepEqual(long, []);
    const avg = all.reduce((n, s) => n + s.words, 0) / all.length;
    assert.ok(avg < MAX_AVERAGE, `average ${avg.toFixed(1)} words per sentence`);
  });

  test(`voice: ${page} asks its questions only in headings, not in the body`, () => {
    const html = read(page);
    const esc = (t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    // a question is allowed where it is a heading: an h2/h3, or a FAQ term heading its own answer
    const bad = prose(html).filter((b) => /\?/.test(b)).filter((b) => !new RegExp("(<h[23]>|<dt><b>)\\s*" + esc(b.slice(0, 30))).test(html))
      .filter((b) => !/^FIG\. |^"[^"]*\?"/.test(b));   // a quoted question inside a myth card is a quote, not the page asking
    assert.deepEqual(bad, [], "rhetorical questions in body copy");
  });
}
