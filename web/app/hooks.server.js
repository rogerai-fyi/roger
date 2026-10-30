// <body> attributes per route, from app/body-attrs.json (the page handover wrote an
// entry when it ports a page). The legacy pages set them on <body> (the account pages'
// class="auth", the marketing pages' data-onair); app.html has no per-page body slot, so
// the prerender hook writes them. A route not listed gets a plain <body>.
import BODY_ATTRS from "./body-attrs.json";

export async function handle({ event, resolve }) {
  const attrs = BODY_ATTRS[event.route.id] || "";
  return resolve(event, { transformPageChunk: ({ html }) => html.replace("<body>", `<body${attrs}>`) });
}
