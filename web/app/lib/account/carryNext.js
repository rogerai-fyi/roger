// Carry a `next` from /login.html through to whichever provider the person picks
// (legacy: web/src/js/login-next.js). Someone who reached the sign-in page mid-task,
// approving a device for instance, should land back where they were. The broker
// validates `next` as a same-site path and ignores anything else; this never forwards
// something that obviously leaves the site. A Svelte action, so the link's href stays the
// plain provider URL in the markup (and with scripts off).
export function nextParam() {
  const m = /[?&]next=([^&]+)/.exec(location.search);
  if (!m) return "";
  const next = decodeURIComponent(m[1]);
  if (next.charAt(0) !== "/" || next.startsWith("//") || next.startsWith("/\\")) return "";
  return next;
}
export function carryNext(a) {
  const next = nextParam();
  if (next) a.href += (a.href.includes("?") ? "&" : "?") + "next=" + encodeURIComponent(next);
}
