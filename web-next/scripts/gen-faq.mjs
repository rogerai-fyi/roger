// The FAQ page is GENERATED from the legacy web/src/faq.html for the spike: the head args,
// the FAQPage JSON-LD and the <main> body are lifted verbatim; the include markers become
// components. This is what a one-time page port looks like mechanically; a real port
// would then keep editing the .svelte file, not regenerate it.
import { readFileSync, writeFileSync } from "node:fs";
const src = readFileSync(new URL("../../web/src/faq.html", import.meta.url), "utf8");
const headArgs = src.match(/<!--\s*include:\s*head\.html\s+(.*?)\s*-->/)[1];
const args = {}; for (const m of headArgs.matchAll(/([\w-]+)=("([^"]*)"|'([^']*)'|(\S+))/g)) args[m[1]] = m[3] ?? m[4] ?? m[5];
// only the JSON: a literal </script> inside a component <script> block ends the block
const ld = src.match(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/)[1];
let main = src.match(/<main[\s\S]*<\/main>/)[0];
main = main.replace(/<!--\s*include:\s*onair\.html\s*-->/g, "<OnAir />");
if (/<!--\s*include:/.test(main)) throw new Error("unported include in faq body");
const rail = src.match(/<!--\s*include:\s*rail\.html\s+head="([^"]*)"\s+rev="([^"]*)"\s*-->/);
const dq = (s) => s.replace(/"/g, '\\"');
const page = `<script>
  // FAQ (ported from web/src/faq.html). A plain read, not an accordion: an answer engine and the
  // browser's own find both need the text in the document. The visible questions and the
  // FAQPage JSON-LD are locked to each other by test/pricing-faq-llms.test.mjs.
  import { siteHead, CSS_SHARED } from "#lib/head.js";
  import Nav from "#lib/ui/Nav.svelte";
  import Footer from "#lib/ui/Footer.svelte";
  import Rail from "#lib/ui/Rail.svelte";
  import OnAir from "#lib/ui/OnAir.svelte";
  const head = siteHead({
    title: "${dq(args.title)}",
    desc: "${dq(args.desc)}",
    og: { type: "${args.ogtype}", title: "${dq(args.ogtitle)}", desc: "${dq(args.ogdesc)}", url: "${args.ogurl}", image: "${args.ogimage}" },
    css: [...CSS_SHARED, "research.css", "faq.css", "company.css"],
  });
  const faqLd = ${JSON.stringify(ld)};
</script>
<svelte:head>
  {@html head}
  {@html '<script type="application/ld+json">' + faqLd + '<\\/script>'}
</svelte:head>
<Rail head="${rail[1]}" rev="${rail[2]}" />

<Nav variant="marketing" current="faq" />

${main}

<Footer />
`;
writeFileSync(new URL("../src/routes/faq/+page.svelte", import.meta.url), page);
console.log("src/routes/faq/+page.svelte generated");
