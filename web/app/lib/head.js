// The per-page <head> contract from the legacy head.html partial, as one function. A page
// calls it from <svelte:head> with {@html}. Output stays the same markup the legacy build
// emits, so the guard tests (canonical, og, description, robots) see no difference.
// head args are authored as HTML and inserted verbatim, as the legacy include args were
// (a bare & in an attribute is valid HTML); only a quote needs escaping
const esc = (s) => String(s).replace(/"/g, "&quot;");

// Site-wide head items, in the legacy order: the Organization graph, the icons, the font
// preload, the footer easter egg.
const LD_ORG = `<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"Organization","@id":"https://rogerai.fm/#org","name":"RogerAI","legalName":"RogerAI, Inc.","url":"https://rogerai.fm/","logo":"https://rogerai.fm/og-home.png","sameAs":["https://apps.apple.com/us/app/rogerai-fm/id6785743752","https://github.com/rogerai-fyi","https://x.com/RogerAI_fm","https://huggingface.co/rogerai-fyi"],"description":"An American AI research and infrastructure company building models for constrained hardware and an OpenAI-compatible network.","subOrganization":{"@id":"https://rogerai.fm/#labs"},"mainEntityOfPage":"https://rogerai.fm/company.html"},{"@type":"ResearchOrganization","@id":"https://rogerai.fm/#labs","name":"RogerAI Labs","url":"https://rogerai.fm/research.html","parentOrganization":{"@id":"https://rogerai.fm/#org"}},{"@type":"WebSite","@id":"https://rogerai.fm/#site","name":"RogerAI","url":"https://rogerai.fm/","publisher":{"@id":"https://rogerai.fm/#org"}}]}<\/script>`;
const SITE_ITEMS = [
  `<link rel="icon" href="favicon.svg" type="image/svg+xml" />`,
  `<link rel="mask-icon" href="favicon.svg" color="#E0231C" />`,
  `<link rel="apple-touch-icon" href="ping.svg" />`,
  `<meta name="theme-color" content="#E0231C" />`,
  `<link rel="preload" href="assets/fonts/space-grotesk-latin.woff2" as="font" type="font/woff2" crossorigin />`,
  `<script src="js/easter-egg.js" defer><\/script>`,
];
// The shared page runtime (legacy _partials/site-js.html): one module, app/runtime.js built to
// js/runtime.js, where the page puts its { runtime: true } entry. A module script is deferred
// by the spec and runs in document order with the islands' modules after it.
export const RUNTIME_SRC = "js/runtime.js";

// The homepage's no-flash theme script, inline because it must run before first paint. Its
// sha256 is allow-listed at the edge (web/scripts/cf-edge.mjs); test/dual-build.test.mjs pins
// these bytes to the legacy head partial while that exists. Do not reformat.
export const THEME_INLINE = `<script>
    (function () {
      try {
        var saved = localStorage.getItem("roger-theme");
        var dark = saved ? saved === "dark"
          : window.matchMedia("(prefers-color-scheme: dark)").matches;
        if (dark) document.documentElement.setAttribute("data-theme", "dark");
      } catch (e) {}
    })();
  <\/script>`;

// The marketing Open Graph block (legacy head.html og=1): the homepage card, canonical "/".
const OG_HOME = {
  url: "https://rogerai.fm/", type: "website",
  title: "RogerAI - local AI models and inference infrastructure",
  desc: "American AI research on open models for constrained hardware, and one OpenAI-compatible local endpoint.",
  image: "https://rogerai.fm/og-home.png",
};
const LD_SOFTWARE = `<script type="application/ld+json">{"@context":"https://schema.org","@type":"SoftwareApplication","name":"RogerAI (roger CLI)","applicationCategory":"DeveloperApplication","operatingSystem":"macOS, Linux, Windows","offers":{"@type":"Offer","price":"0","priceCurrency":"USD"},"description":"Install roger to put a model running on your own GPU on the air, or use community models over an OpenAI-compatible endpoint. Free models available.","url":"https://rogerai.fm/","publisher":{"@id":"https://rogerai.fm/#org"}}<\/script>`;

export const CSS_SHARED = ["tokens.css", "base.css", "components.css"];
export const CSS_ACCOUNT = [...CSS_SHARED, "account-base.css"];

// og: "home" (the marketing block) | { type, title, desc, url, image } (a page's own card)
// theme: "external" (js/theme-init.js, default) | "inline" (the homepage) | "none"
// extra: a page's own head markup (its JSON-LD, preloads), inserted after the stylesheets
// scripts: the page's scripts in document order: one { runtime: true } entry where the shared
//          runtime goes (a page without one has none), { page: "<name>" } for the page's own
//          scripts (app/pages/<name>.js, after the runtime), { src, defer } for a plain file
export function siteHead({ title, desc, robots, canonical, og, css = CSS_SHARED, theme = "external", extra = "", scripts = [] }) {
  const out = [`<title>${esc(title)}</title>`];
  if (desc) out.push(`<meta name="description" content="${esc(desc)}" />`);
  out.push(`<meta name="color-scheme" content="light dark" />`);
  if (robots === "noindex") out.push(`<meta name="robots" content="noindex" />`);
  if (theme === "external") out.push(`<script src="js/theme-init.js"></script>`);
  if (theme === "inline") out.push(THEME_INLINE);
  if (og === "home") og = OG_HOME;
  if (og) {
    out.push(
      `<link rel="canonical" href="${esc(og.url)}" />`,
      `<meta property="og:title" content="${esc(og.title)}" />`,
      `<meta property="og:description" content="${esc(og.desc)}" />`,
      `<meta property="og:type" content="${esc(og.type || "website")}" />`,
      `<meta property="og:url" content="${esc(og.url)}" />`,
      `<meta property="og:site_name" content="RogerAI" />`,
      `<meta property="og:image" content="${esc(og.image)}" />`,
      `<meta property="og:image:type" content="image/png" />`,
      `<meta property="og:image:width" content="1200" />`,
      `<meta property="og:image:height" content="630" />`,
      `<meta property="og:image:alt" content="${esc(og.title)}" />`,
      `<meta name="twitter:card" content="summary_large_image" />`,
      `<meta name="twitter:title" content="${esc(og.title)}" />`,
      `<meta name="twitter:description" content="${esc(og.desc)}" />`,
      `<meta name="twitter:image" content="${esc(og.image)}" />`,
      `<meta name="twitter:image:alt" content="${esc(og.title)}" />`,
    );
  } else if (canonical) {
    out.push(`<link rel="canonical" href="${esc(canonical)}" />`);
  }
  out.push(LD_ORG);
  if (og === OG_HOME) out.push(LD_SOFTWARE);
  out.push(...SITE_ITEMS);
  for (const f of css) out.push(`<link rel="stylesheet" href="styles/${f}" />`);
  if (extra) out.push(extra.trim());
  for (const sc of scripts) {
    if (sc.runtime) out.push(`<script type="module" src="${RUNTIME_SRC}"><\/script>`);
    else if (sc.page) out.push(`<script type="module" src="js/pages/${sc.page}.js"><\/script>`);
    else out.push(`<script src="${sc.src}"${sc.defer ? " defer" : ""}><\/script>`);
  }
  return out.join("\n  ");
}
