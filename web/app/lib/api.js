// ONE broker client. The legacy pages each declared the broker URL and a credentialed
// fetch of their own (22 copies of the URL, 8 of the wrapper); this is the one that remains.
// The session is an HttpOnly cookie on the broker's origin, so every call is credentialed
// and no token ever touches JS.
export const BROKER = globalThis.ROGER_BROKER || "https://broker.rogerai.fm";

export function api(path, opts = {}) {
  return fetch(BROKER + path, { ...opts, credentials: "include" });
}

export function postJSON(path, body) {
  return api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

// Who is signed in, or null when the broker says nobody is. ONLY a null here means
// "logged out": any later feed error with a confirmed session is an error to show, never
// a reason to bounce to login (the 2026-10-04 login <-> dashboard loop).
export async function account() {
  const r = await api("/account");
  return r.ok ? r.json() : null;
}

// A feed read: the body, or null on any failure (a refused or unreachable feed shows as
// honest-empty, never as a number). The legacy page scripts each carried this as get().
export async function getJSON(path) {
  try {
    const r = await api(path);
    return r.ok ? await r.json() : null;
  } catch {
    return null;
  }
}

export async function logout() {
  try { await api("/auth/logout", { method: "POST" }); } catch { /* leaving anyway */ }
  location.replace("/login.html");
}
