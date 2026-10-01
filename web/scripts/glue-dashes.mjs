// glueDashes keeps a heading's spaced dash off the start of a line: a title that wrapped
// right before " - " left the dash leading the next line. The word before the dash is glued
// to it (`.nobr`, components.css), so the line can only break after the dash. Markup only:
// the heading's words and order are unchanged (the text-freeze fixtures compare text).
// Headings h1-h6 and the broadcasts index titles (.bc-row__title), in page markup only:
// script, style, pre, textarea and comment regions are passed through untouched.
const PROTECTED_RE = /(<!--[\s\S]*?-->|<(script|style|pre|textarea)\b[\s\S]*?<\/\2\s*>)/gi;
const GLUE_RE = /(<(h[1-6])\b[^>]*>)([\s\S]*?)(<\/\2>)|(<span class="bc-row__title">)([^<]*)(<\/span>)/g;
const glueOne = (s) => s.replace(/([^\s<>]+) - /g, '<span class="nobr">$1 -</span> ');
const glueMarkup = (html) => html.replace(GLUE_RE, (m, open, _tag, inner, close, rOpen, rInner, rClose) =>
  open ? open + inner.split(/(<[^>]*>)/).map((part, i) => (i % 2 ? part : glueOne(part))).join("") + close
       : rOpen + glueOne(rInner) + rClose);

export function glueDashes(html) {
  // split() with the capture groups yields [markup, protected, tagName, markup, ...]
  const parts = html.split(PROTECTED_RE);
  let out = "";
  for (let i = 0; i < parts.length; i += 3) {
    out += glueMarkup(parts[i]);
    if (i + 1 < parts.length) out += parts[i + 1];
  }
  return out;
}
