import { test, expect } from "@playwright/test";

const BROKER = "https://broker.rogerai.fm";
const json = (route, status, body) => route.fulfill({ status, contentType: "application/json",
  headers: { "access-control-allow-origin": "http://127.0.0.1:4178", "access-control-allow-credentials": "true" }, body: JSON.stringify(body) });

test("console: the island hydrates the prerendered card and renders the operator view from the broker", async ({ page }) => {
  const seen = [];
  await page.route(`${BROKER}/**`, (route) => {
    const path = new URL(route.request().url()).pathname;
    seen.push(path);
    if (path === "/promo") return json(route, 200, {});   // js/promo.js, the legacy site runtime
    if (path === "/account") return json(route, 200, { github_login: "ada", provider: "github" });
    if (path === "/console") return json(route, 200, { role: "owner",
      counters: { requests_today: 4321, earned_today: 2.5, active_nodes: 1, active_bands: 2 },
      events: [{ request_id: "req_abcdefghijklmnop", ts: Math.floor(Date.now() / 1000) - 45, model: "gpt-oss-120b", node: "K6ABC", tokens_in: 900, tokens_out: 1800, earned: 0.01, success: true }] });
    if (path === "/provider/models") return json(route, 200, { ceiling_in: 50, ceiling_out: 100,
      models: [{ node: "K6ABC", model: "gpt-oss-120b", online: true, price_in: 0.1, price_out: 0.4, free: false, schedule: [], overridden: false, category: "chat", taxonomy_source: "declared" }] });
    return json(route, 404, {});
  });
  const errors = [], scripts = [];
  page.on("request", (r) => { if (r.resourceType() === "script") scripts.push(new URL(r.url()).pathname); });
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });

  await page.goto("/console.html");
  await expect(page.locator("#who")).toHaveText("@ada");
  await expect(page.locator("#role")).toHaveText("Operator console");
  await expect(page.locator("#colMoney")).toHaveText("Earned");
  await expect(page.locator("tr.cn-row")).toHaveCount(1);
  await expect(page.locator("tr.cn-row .cn-time")).toHaveText("45s");
  await expect(page.locator("#ctrReq")).toHaveText(/4\.3k|4,321/);
  await expect(page.locator(".md-card .md-name")).toHaveText("gpt-oss-120b");
  // the price editor is live: add a window, it appears
  await page.locator(".md-add").click();
  await expect(page.locator(".md-win")).toHaveCount(1);
  // js/promo.js (the legacy site runtime, still loaded by every page) asks /promo on its own
  expect(seen.filter((p) => p !== "/promo")).toEqual(["/account", "/console", "/provider/models"]);
  expect(errors, "no page or console errors (a CSP block would show here)").toEqual([]);
  // an island: the card hydrated from js/islands/console.js, no SvelteKit client, no inline
  // loader, so the page carries no CSP meta of its own (the edge policy is the one policy)
  expect(scripts.filter((s) => s.includes("/_app/"))).toEqual([]);
  expect(scripts.some((s) => s.startsWith("/js/islands/console.js"))).toBe(true);
  expect(await page.locator('meta[http-equiv="content-security-policy"]').count()).toBe(0);
  const inline = await page.locator("script:not([src]):not([type])").evaluateAll((els) => els.map((e) => e.textContent));
  expect(inline.length).toBe(2);
  expect(inline[0].startsWith('document.documentElement.classList.add("js")')).toBe(true);
  expect(inline[1].startsWith("(function(){function r(){var n=document.querySelectorAll")).toBe(true);
});

test("console: a failed /account is the only redirect to login", async ({ page }) => {
  await page.route(`${BROKER}/**`, (route) => json(route, 401, {}));
  await page.goto("/console.html");
  await page.waitForURL(/login\.html$/);
});

test("faq: a content page ships no SvelteKit script at all", async ({ page }) => {
  const scripts = [];
  page.on("request", (r) => { if (r.resourceType() === "script") scripts.push(new URL(r.url()).pathname); });
  await page.goto("/faq.html");
  await expect(page.locator("h1")).toHaveText("Asked often. Answered straight.");
  expect(scripts.filter((s) => s.includes("/_app/"))).toEqual([]);
});
