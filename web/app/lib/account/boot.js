// The account pages' first read (legacy: the bootGet copied into account.js, billing.js and
// payouts.js). It keeps the STATUS, because only a 401 means "signed out": a 5xx or a
// dropped connection is a fault to show with the card and a working sign-out, never a
// redirect (login would bounce a signed-in person to the dashboard and lose their place).
import { api } from "#lib/api.js";

export async function bootGet(path) {
  try {
    const r = await api(path);
    const data = r.ok ? await r.json() : null;
    return { status: r.status, data };
  } catch {
    return { status: 0, data: null };
  }
}

// A refusal's own words, or a plain fallback.
export async function reason(r, fallback) {
  const j = await r.json().catch(() => ({}));
  return (j && j.error && j.error.message) || fallback;
}
