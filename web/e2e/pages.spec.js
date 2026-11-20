// The pages that carry their own scripts run them as one module after the runtime, in a real
// browser, with no page or console error: the proof the IIFEs are module-safe (strict mode,
// document order) on every page that has them.
import { test, expect } from "@playwright/test";
import { readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const BROKER = "https://broker.rogerai.fm";
const names = readdirSync(fileURLToPath(new URL("../app/pages", import.meta.url))).map((f) => f.replace(/\.js$/, ""));

for (const name of names) {
  test(`${name}: its own scripts run as one module with no error`, async ({ page }) => {
    await page.route(`${BROKER}/**`, (route) => route.fulfill({ status: 200, contentType: "application/json", headers: { "access-control-allow-origin": "http://127.0.0.1:4178", "access-control-allow-credentials": "true" }, body: "{}" }));
    const errors = [], scripts = [];
    page.on("request", (r) => { if (r.resourceType() === "script") scripts.push(new URL(r.url()).pathname); });
    page.on("pageerror", (e) => errors.push(String(e)));
    page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });
    await page.goto(name === "index" ? "/index.html" : `/${name}.html`);
    await page.waitForLoadState("networkidle");
    expect(scripts.filter((s) => s.startsWith("/js/runtime.js")).length).toBe(1);
    expect(scripts.filter((s) => s.startsWith(`/js/pages/${name}.js`)).length).toBe(1);
    expect(scripts.filter((s) => /^\/js\/(?!runtime|theme-init|easter-egg|islands\/|pages\/)[\w-]+\.js/.test(s))).toEqual([]);
    expect(errors).toEqual([]);
  });
}
