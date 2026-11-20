// Regenerate a copy-snapshot fixture from the CURRENT build. The fixtures lock the words of
// pages so a rollout can move them but never edit them; regenerating one is a copy change
// and needs the founder's approval (see the handoff rules). Entities are decoded on both
// sides of every comparison (test/_html.mjs), so a regenerated fixture holds decoded text.
//
//   node scripts/copy-fixtures.mjs research            -> test/fixtures/research-words.json
//   node scripts/copy-fixtures.mjs rest <page.html>... -> updates those entries of rest-pages-text.json
//   node scripts/copy-fixtures.mjs company <page.html>... -> updates those entries of company-section-text.json
import { readFileSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { WEB, DIST, wordsOf, squashText, decodeText } from "../test/_html.mjs";

const [kind, ...pages] = process.argv.slice(2);
execFileSync("node", ["build.mjs"], { cwd: WEB });
const dist = (p) => readFileSync(path.join(DIST, p), "utf8");
const fixtures = path.join(WEB, "test", "fixtures");
const load = (f) => JSON.parse(readFileSync(path.join(fixtures, f), "utf8"));
const save = (f, data) => writeFileSync(path.join(fixtures, f), JSON.stringify(data, null, 1) + "\n");

if (kind === "research") {
  const f = "research-words.json";
  const out = {};
  for (const page of Object.keys(load(f)).sort()) out[page] = wordsOf(dist(page));
  save(f, out);
  console.log(`${f}: ${Object.keys(out).length} pages`);
} else if (kind === "rest" || kind === "company") {
  const f = kind === "rest" ? "rest-pages-text.json" : "company-section-text.json";
  const data = load(f);
  if (!pages.length) throw new Error(`name the pages to regenerate in ${f}`);
  for (const page of pages) {
    let html = dist(page).replace(/<!--[\s\S]*?-->/g, "");
    const m = kind === "rest"
      ? ([...html.matchAll(/<main\b[\s\S]*?<\/main>/g)].map((x) => x[0]).join("\n") || html).replace(/<script\b[\s\S]*?<\/script>/g, "")
      : html.slice(html.search(/<main\b/), html.indexOf("</main>") + 7).replace(/<nav class="(?:toc-tuner|page-index)\b[^"]*"[\s\S]*?<\/nav>/g, "");
    data[page] = { text: kind === "rest" ? squashText(m) : decodeText(m.replace(/<[^>]+>/g, " ")).replace(/\s+/g, " ").trim(),
                   hrefs: [...m.matchAll(/href="([^"]*)"/g)].map((x) => x[1]) };
    console.log(`${f}: ${page} regenerated`);
  }
  save(f, data);
} else {
  throw new Error("usage: copy-fixtures.mjs research | rest <pages> | company <pages>");
}
