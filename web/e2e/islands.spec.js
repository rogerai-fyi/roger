// Every island hydrates the markup its page prerendered, in place, in a real browser. The
// broker is left silent (no request is ever answered), so no page navigates or re-renders
// before the check: what is asserted is the envelope Island.svelte wrote and island.js read
// (data-hydrated="true"; "false" is the fallback remount after a markup mismatch), with no
// page or console error. A new island must be added to PAGES, or this fails on the gap.
import { test, expect } from "@playwright/test";
import { readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const BROKER = "https://broker.rogerai.fm";
const PAGES = { console: "/console.html", link: "/r.html", device: "/device.html", login: "/login.html", account: "/account.html",
  stations: "/stations.html", dashboard: "/dashboard.html", keys: "/keys.html", payouts: "/payouts.html", billing: "/billing.html", private: "/private.html", usage: "/usage.html" };
const islands = readdirSync(fileURLToPath(new URL("../app/islands", import.meta.url))).map((f) => f.replace(/\.js$/, ""));

test("every island in app/islands has a page in this check", () => {
  expect(islands.sort()).toEqual(Object.keys(PAGES).sort());
});

for (const name of islands) {
  test(`${name}: hydrates in place with no error`, async ({ page }) => {
    await page.route(`${BROKER}/**`, () => {});   // pending forever: the page stays as prerendered + hydrated
    const errors = [], scripts = [];
    page.on("request", (r) => { if (r.resourceType() === "script") scripts.push(new URL(r.url()).pathname); });
    page.on("pageerror", (e) => errors.push(String(e)));
    page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
    await page.goto(PAGES[name]);
    await expect(page.locator(`[data-island="${name}"]`)).toHaveAttribute("data-hydrated", "true");
    expect(errors).toEqual([]);
    // the site runtime is one module request; its scripts are never fetched on their own
    expect(scripts.filter((s) => s.startsWith("/js/runtime.js")).length).toBe(1);
    expect(scripts.filter((s) => /^\/js\/(lets-talk|site|session|promo|tuner|scrub|anchor-hold|range-twin|cycle)\.js/.test(s))).toEqual([]);
  });
}
