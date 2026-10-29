// Body attributes per route. The legacy pages set them on <body> (the account pages'
// `class="auth"`, the marketing pages' `data-onair`), and app.html has no per-page body
// placeholder, so the prerender hook writes them. A page not listed gets a plain <body>.
const BODY_ATTRS = {
  "/console": ' class="auth"',
  "/faq": ' data-onair="idle"',
  "/broadcasts-free-chatgpt-alternative": ' data-onair="idle"',
};
export async function handle({ event, resolve }) {
  const attrs = BODY_ATTRS[event.route.id] || "";
  return resolve(event, { transformPageChunk: ({ html }) => html.replace("<body>", `<body${attrs}>`) });
}
