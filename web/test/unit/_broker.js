// A fake broker for component tests: fetch answers by path prefix, every call is recorded.
// routes: { "/path": [status, body] | () => [status, body] }
import { vi } from "vitest";

export function broker(routes) {
  const calls = [];
  globalThis.fetch = vi.fn(async (url, opts = {}) => {
    const path = String(url).replace("https://broker.rogerai.fm", "");
    calls.push({ path, opts, body: opts.body ? JSON.parse(opts.body) : undefined });
    // the longest matching prefix wins ("/account/delete" is not "/account")
    const hit = Object.entries(routes).filter(([k]) => path.startsWith(k)).sort((x, y) => y[0].length - x[0].length)[0];
    const [status, body] = hit ? (typeof hit[1] === "function" ? hit[1]() : hit[1]) : [404, {}];
    return { ok: status >= 200 && status < 300, status, json: async () => body };
  });
  return calls;
}

// The page's location, with navigations recorded (location.replace / href assignments).
export function fakeLocation({ pathname = "/", search = "", hash = "" } = {}) {
  const navs = [];
  const loc = { pathname, search, hash, replace: (u) => navs.push(u), reload: () => navs.push("<reload>"), origin: "http://localhost" };
  Object.defineProperty(loc, "href", { get: () => "http://localhost" + pathname + search + hash, set: (u) => navs.push(u) });
  Object.defineProperty(window, "location", { configurable: true, value: loc });
  return navs;
}
