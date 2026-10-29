// The per-page <head> contract from the legacy head.html partial, as one function. A page
// calls it from <svelte:head> with {@html}. Output stays the same markup the legacy build
// emits, so the guard tests (canonical, og, description, robots) see no difference.
const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/"/g, "&quot;");

export const CSS_SHARED = ["tokens.css", "base.css", "components.css"];
export const CSS_ACCOUNT = [...CSS_SHARED, "account-base.css"];

export function siteHead({ title, desc, robots, canonical, og, css = CSS_SHARED, themeExternal = true }) {
  const out = [`<title>${esc(title)}</title>`];
  if (desc) out.push(`<meta name="description" content="${esc(desc)}" />`);
  out.push(`<meta name="color-scheme" content="light dark" />`);
  if (robots === "noindex") out.push(`<meta name="robots" content="noindex" />`);
  if (themeExternal) out.push(`<script src="js/theme-init.js"></script>`);
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
  for (const f of css) out.push(`<link rel="stylesheet" href="styles/${f}" />`);
  return out.join("\n  ");
}
