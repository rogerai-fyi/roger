// /billing.html (lib/billing/BillingApp.svelte + SpendLimit, TopUp, topup.js, spark.js): the
// legacy billing.js contracts from test/boot-faults.test.mjs, test/honest-feeds.test.mjs and
// test/topup-bounds.test.mjs (the amount rules), plus the page's rendering. A failed feed reads
// "-" or stays hidden, never a real-looking number; the top-up refuses what the broker would.
import { describe, it, expect } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import BillingApp from "#lib/billing/BillingApp.svelte";
import { MIN_USD, MAX_USD, money, check, label, wholeCents } from "#lib/billing/topup.js";
import { spark } from "#lib/billing/spark.js";
import { cr } from "#lib/format.js";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const text = (id) => el(id).textContent.trim();
const now = Math.floor(Date.now() / 1000);
const when = (ts) => new Date(ts * 1000).toLocaleDateString();
const BILLING = { balance: 5, derived: 5, checkout_ready: false, topups: [] };
const OK = { "/billing": [200, BILLING], "/metrics/series": [200, { daily: [] }], "/console": [200, { counters: { spend_today: 0 }, events: [] }], "/account/limit": [200, {}] };
const ready = () => waitFor(() => expect(el("card").hidden).toBe(false));
function mount(routes) {
  const calls = broker(routes);
  const navs = fakeLocation({ pathname: "/billing.html" });
  const { unmount } = render(BillingApp);
  return { calls, navs, unmount, fetched: () => calls.map((c) => c.path) };
}
const settled = (p) => waitFor(() => { for (const f of ["/metrics/series", "/account/limit", "/console"]) expect(p.fetched().some((u) => u.startsWith(f))).toBe(true); });
const checkout = (routes = {}) => mount({ ...OK, "/billing": [200, { ...BILLING, checkout_ready: true }], ...routes });

describe("boot (the login-loop rule)", () => {
  it("a 401 on the first feed goes to login; nothing else is read", async () => {
    const p = mount({ "/billing": [401, {}] });
    await waitFor(() => expect(p.navs).toEqual(["/login.html"]));
    expect(p.fetched()).toEqual(["/billing"]);
  });
  for (const status of [500, 503]) {
    it(`a ${status} shows an error and keeps the card with a working sign-out, no redirect`, async () => {
      const p = mount({ "/billing": [status, {}], "/auth/logout": [200, {}] });
      await ready();
      expect(p.navs).toEqual([]);
      expect(visible("pageError")).toBe(true);
      expect(el("card").classList.contains("is-fault")).toBe(true);
      expect(p.fetched()).toEqual(["/billing"]);   // the secondary feeds wait for a confirmed session
      await fireEvent.click(el("logout"));
      await waitFor(() => expect(p.navs).toEqual(["/login.html"]));
      expect(p.calls.at(-1)).toMatchObject({ path: "/auth/logout", opts: { method: "POST" } });
    });
  }
  it("a network failure shows the error, no redirect; a good feed shows none and reads the rest in order", async () => {
    globalThis.fetch = async () => { throw new Error("offline"); };
    const navs = fakeLocation({ pathname: "/billing.html" });
    const { unmount } = render(BillingApp);
    await ready();
    expect(visible("pageError")).toBe(true);
    expect(navs).toEqual([]);
    unmount();
    const p = mount(OK);
    await ready();
    await settled(p);
    expect(visible("pageError")).toBe(false);
    expect(el("card").classList.contains("is-fault")).toBe(false);
    expect(p.fetched()).toEqual(["/billing", "/metrics/series?days=30", "/account/limit", "/console?limit=40"]);
    expect(text("balance")).toBe("$5.00");
    expect(text("derived")).toBe("$5.00");
  });
});

describe("the wallet and the receipts", () => {
  it("a failed /console shows '-' for spend today (unavailable), not $0.00, and no empty-charges note", async () => {
    const p = mount({ ...OK, "/console": [500, {}] });
    await ready();
    await settled(p);
    await waitFor(() => expect(p.fetched()).toContain("/console?limit=40"));
    expect(text("spendToday")).toBe("-");
    expect(visible("chargesEmpty")).toBe(false);
  });
  it("a working /console shows the real figure, and $0.00 only when the broker said so", async () => {
    const p = mount({ ...OK, "/console": [200, { counters: { spend_today: 1.5 }, events: [] }] });
    await ready();
    await waitFor(() => expect(text("spendToday")).toBe("$1.50"));
    expect(visible("chargesEmpty")).toBe(true);
    p.unmount();
    mount({ ...OK, "/console": [200, { events: [] }] });
    await waitFor(() => expect(text("spendToday")).toBe("$0.00"));
  });
  it("recent charges: only rows with a real cost, newest eight, model and node named", async () => {
    const events = [];
    for (let i = 0; i < 12; i++) events.push({ model: "m" + i, node: i % 2 ? "K6ABC" : "", cost: i % 3 === 0 ? 0 : 0.01 * (i + 1), ts: now - i * 60 });
    mount({ ...OK, "/console": [200, { counters: { spend_today: 0.5 }, events }] });
    await ready();
    await waitFor(() => expect(document.querySelectorAll("#charges li").length).toBe(8));
    const rows = [...document.querySelectorAll("#charges li")];
    expect(rows[0].querySelector(".r-model").textContent).toBe(`${when(now - 60)} - m1 - K6ABC`);
    expect(rows[0].querySelector(".r-cost").textContent).toBe("$0.02");
    expect(rows[1].querySelector(".r-model").textContent).toBe(`${when(now - 120)} - m2`);
    expect(rows.some((r) => /m0|m3|m6|m9/.test(r.textContent))).toBe(false);   // free traffic is not a receipt
    expect(visible("chargesEmpty")).toBe(false);
  });
  it("recent top-ups from the billing feed, or the honest-empty note", async () => {
    const p = mount({ ...OK, "/billing": [200, { ...BILLING, topups: [{ ts: now - 3600, amount: 20 }, { ts: now - 86400, amount: 5 }] }] });
    await ready();
    const rows = [...document.querySelectorAll("#topups li")];
    expect(rows.length).toBe(2);
    expect(rows[0].textContent).toBe(`${when(now - 3600)} - top-up$20.00`);
    expect(visible("topupsEmpty")).toBe(false);
    p.unmount();
    mount(OK);
    await ready();
    expect(visible("topupsEmpty")).toBe(true);
  });
  it("checkout not ready: the CLI note and the disabled line, no top-up control", async () => {
    mount(OK);
    await ready();
    expect(visible("topupBox")).toBe(false);
    expect(visible("topupDisabled")).toBe(true);
    expect(text("topupNote")).toBe("Top up from the CLI: roger topup.");
  });
});

describe("savings vs frontier and spend velocity", () => {
  it("with a frontier estimate: the saved amount, two bars scaled to the larger, the percent note", async () => {
    mount({ ...OK, "/metrics/series": [200, { daily: [], savings: { spend_usd: 4, frontier_est: 10, savings_est: 6, baseline_model: "gpt-5" } }] });
    await ready();
    await waitFor(() => expect(visible("saveBox")).toBe(true));
    expect(text("saveAmt")).toBe("$6.00");
    expect(text("saveSpend")).toBe("$4.00");
    expect(text("saveFrontier")).toBe("$10.00");
    const bars = [...document.querySelectorAll("#saveBars .bx-save__row")];
    expect(bars.map((b) => b.querySelector(".bx-save__lab").textContent)).toEqual(["RogerAI", "gpt-5"]);
    expect(bars[0].querySelector(".bx-save__fill--paid").style.width).toBe("40%");
    expect(bars[1].querySelector(".bx-save__fill--frontier").style.width).toBe("100%");
    expect(bars.map((b) => b.querySelector(".bx-save__val").textContent)).toEqual(["$4.00", "$10.00"]);
    expect(text("saveNote")).toBe("About 60% less than the gpt-5 list price. Estimate only: published list prices, not a live or contractual quote.");
  });
  it("no frontier estimate, or no savings feed: the panel stays hidden; a tiny spend keeps a visible bar; the broker's own note wins", async () => {
    const p = mount({ ...OK, "/metrics/series": [200, { daily: [], savings: { spend_usd: 1, frontier_est: 0, savings_est: 0 } }] });
    await ready();
    await settled(p);
    expect(visible("saveBox")).toBe(false);
    p.unmount();
    const q = mount({ ...OK, "/metrics/series": [200, { daily: [], savings: { spend_usd: 0.001, frontier_est: 10, savings_est: 0, reference_note: "List prices as of today." } }] });
    await ready();
    await waitFor(() => expect(visible("saveBox")).toBe(true));
    expect(document.querySelector("#saveBars .bx-save__fill--paid").style.width).toBe("1%");
    expect(text("saveNote")).toBe("List prices as of today.");
    q.unmount();
    const r = mount({ ...OK, "/metrics/series": [500, {}] });
    await ready();
    await settled(r);
    expect(visible("saveBox")).toBe(false);
  });
  it("velocity: a sparkline over the days with spend, the span and the peak; hidden with no spend", async () => {
    const daily = [{ bucket: "2026-10-01", spend: 1 }, { bucket: "2026-10-02", spend: 2 }, { bucket: "2026-10-03", spend: 0.5 }];
    const p = mount({ ...OK, "/metrics/series": [200, { daily }] });
    await ready();
    await waitFor(() => expect(visible("velBox")).toBe(true));
    const svg = document.querySelector("#velChart svg.bx-spark");
    expect(svg.getAttribute("viewBox")).toBe("0 0 100 32");
    expect(svg.getAttribute("aria-label")).toBe("Recent daily spend");
    expect(svg.querySelector(".bx-spark__line").getAttribute("d")).toBe(spark(daily).line);
    expect(svg.querySelector(".bx-spark__dot").getAttribute("cx")).toBe("100.00");
    expect(text("velSpan")).toBe("3 days of activity");
    expect(text("velPeak")).toBe("peak $2.00/day");
    p.unmount();
    const q = mount({ ...OK, "/metrics/series": [200, { daily: [{ bucket: "2026-10-01", spend: 0 }] }] });
    await ready();
    await settled(q);
    expect(visible("velBox")).toBe(false);
  });
  it("spark.js: the geometry (ported from renderVelocity)", () => {
    expect(spark(null)).toBe(null);
    expect(spark([])).toBe(null);
    expect(spark([{ bucket: "d", spend: 0 }])).toBe(null);
    const one = spark([{ bucket: "d", spend: 3 }]);
    expect(one.n).toBe(1);
    expect(one.peak).toBe(3);
    expect(one.line).toBe("M0 1.50 L100 1.50");   // a flat tick at the top: the one value is the max
    expect(one.dot).toEqual({ cx: "50.00", cy: "1.50" });
    expect(one.area.endsWith("L100 32 L0 32 Z")).toBe(true);
    const many = spark(Array.from({ length: 40 }, (_, i) => ({ bucket: "d" + i, spend: i === 39 ? 4 : 1 })));
    expect(many.n).toBe(30);   // the most recent 30 buckets
    expect(many.peak).toBe(4);
    expect(many.line.startsWith("M0.00 ")).toBe(true);
    expect(many.dot.cx).toBe("100.00");
    expect(many.dot.cy).toBe("1.50");
  });
});

describe("the monthly spend limit", () => {
  const limit = (cap, spend) => [200, { monthly_cap: cap, monthly_spend: spend }];
  it("loading, then a refused feed is an error; a good feed paints the body", async () => {
    const p = mount({ ...OK, "/account/limit": [500, {}] });
    expect(visible("limitLoading")).toBe(true);
    await ready();
    await waitFor(() => expect(visible("limitError")).toBe(true));
    expect(visible("limitLoading")).toBe(false);
    expect(visible("limitBody")).toBe(false);
    p.unmount();
    mount({ ...OK, "/account/limit": limit(0, 0) });
    await waitFor(() => expect(visible("limitBody")).toBe(true));
    expect(visible("limitLoading")).toBe(false);
    expect(visible("limitError")).toBe(false);
  });
  it("no cap: the honest line, no gauge, no remove button", async () => {
    mount({ ...OK, "/account/limit": limit(0, 3) });
    await waitFor(() => expect(visible("limitBody")).toBe(true));
    expect(visible("limitNone")).toBe(true);
    expect(visible("limitGauge")).toBe(false);
    expect(visible("limitClear")).toBe(false);
    expect(el("limitInput").value).toBe("");
  });
  it("a cap: used of cap, the percent, the meter width, the field reflects the cap, remove shown; over the cap is marked", async () => {
    const p = mount({ ...OK, "/account/limit": limit(50, 10) });
    await waitFor(() => expect(visible("limitGauge")).toBe(true));
    expect(visible("limitNone")).toBe(false);
    expect(text("limitUsed")).toBe("$10.00");
    expect(text("limitCap")).toBe("$50.00/mo");
    expect(text("limitPct")).toBe("20%");
    expect(el("limitSeg").style.width).toBe("20%");
    expect(el("limitPct").classList.contains("is-over")).toBe(false);
    expect(el("limitInput").value).toBe("50");
    expect(visible("limitClear")).toBe(true);
    p.unmount();
    mount({ ...OK, "/account/limit": limit(50, 60) });
    await waitFor(() => expect(text("limitPct")).toBe("120%"));
    expect(el("limitPct").classList.contains("is-over")).toBe(true);
    expect(el("limitSeg").style.width).toBe("100%");
  });
  it("saving: validates, patches the rounded cap, repaints from the echo, says what was saved", async () => {
    const p = mount({ ...OK, "/account/limit": () => [200, { monthly_cap: 0, monthly_spend: 0 }] });
    await waitFor(() => expect(visible("limitBody")).toBe(true));
    await fireEvent.input(el("limitInput"), { target: { value: "-5" } });
    await fireEvent.click(el("limitSave"));
    await waitFor(() => expect(text("limitMsg")).toBe("Enter a dollar amount of $0 or more (0 = no limit)."));
    expect(p.calls.some((c) => c.opts.method === "PATCH")).toBe(false);
    globalThis.fetch = async (url, opts = {}) => {
      const body = JSON.parse(opts.body);
      p.calls.push({ path: String(url).replace("https://broker.rogerai.fm", ""), opts, body });
      return { ok: true, status: 200, json: async () => ({ monthly_cap: body.monthly_cap, monthly_spend: 12 }) };
    };
    await fireEvent.input(el("limitInput"), { target: { value: "25.505" } });
    await fireEvent.click(el("limitSave"));
    await waitFor(() => expect(text("limitMsg")).toBe("Saved - limit set to $25.51/mo."));
    expect(p.calls.at(-1)).toMatchObject({ path: "/account/limit", opts: { method: "PATCH" }, body: { monthly_cap: 25.51 } });
    expect(text("limitUsed")).toBe("$12.00");
    expect(text("limitCap")).toBe("$25.51/mo");
    expect(visible("limitClear")).toBe(true);
    await fireEvent.click(el("limitClear"));
    await waitFor(() => expect(text("limitMsg")).toBe("Saved - no monthly limit."));
    expect(p.calls.at(-1).body).toEqual({ monthly_cap: 0 });
    expect(visible("limitNone")).toBe(true);
    expect(visible("limitGauge")).toBe(false);
  });
  it("a blank field means no limit; a refused or unreachable save says so and keeps the state", async () => {
    const p = mount({ ...OK, "/account/limit": () => [200, { monthly_cap: 40, monthly_spend: 1 }] });
    await waitFor(() => expect(visible("limitGauge")).toBe(true));
    globalThis.fetch = async (url, opts = {}) => {
      p.calls.push({ path: String(url).replace("https://broker.rogerai.fm", ""), opts, body: opts.body ? JSON.parse(opts.body) : undefined });
      return { ok: false, status: 500, json: async () => ({}) };
    };
    await fireEvent.input(el("limitInput"), { target: { value: "  " } });
    await fireEvent.click(el("limitSave"));
    await waitFor(() => expect(text("limitMsg")).toBe("Could not save - try again shortly."));
    expect(p.calls.at(-1).body).toEqual({ monthly_cap: 0 });
    expect(text("limitCap")).toBe("$40.00/mo");   // the echo never came: nothing repainted
    expect(el("limitSave").disabled).toBe(false);
    globalThis.fetch = async () => { throw new Error("offline"); };
    await fireEvent.input(el("limitInput"), { target: { value: "9" } });
    await fireEvent.click(el("limitSave"));
    await waitFor(() => expect(text("limitMsg")).toBe("Could not save - try again shortly."));
  });
});

describe("the top-up (test/topup-bounds.test.mjs pins the bounds to the broker's)", () => {
  it("topup.js: the rules in one place", () => {
    expect(money(MIN_USD)).toBe("$1");
    expect(money(MAX_USD)).toBe("$999,999.99");
    expect(wholeCents(10.01)).toBe(true);
    expect(wholeCents(10.005)).toBe(false);
    expect(check(NaN)).toEqual({ ok: false, msg: "enter an amount of $1 or more" });
    expect(check(0.5)).toEqual({ ok: false, msg: "enter an amount of $1 or more" });
    expect(check(MIN_USD)).toEqual({ ok: true });
    expect(check(MAX_USD)).toEqual({ ok: true });
    expect(check(MAX_USD + 0.01)).toEqual({ ok: false, msg: "the maximum top-up is $999,999.99" });
    expect(check(10.005)).toEqual({ ok: false, msg: "amount must be a whole number of cents" });
    expect(label(20, cr)).toEqual({ button: "Add $20.00", note: "Adds $20.00 to your wallet balance." });
    expect(label(1e6, cr)).toEqual({ button: "Add money", note: "The maximum top-up is $999,999.99." });
    expect(label(10.005, cr)).toEqual({ button: "Add money", note: "Whole cents only." });   // never "Add $10.01"
    expect(label(0.5, cr)).toEqual({ button: "Add money", note: null });
    expect(label(NaN, cr)).toEqual({ button: "Add money", note: null });
  });
  it("checkout ready: the control shows, the default preset is reflected on the button", async () => {
    checkout();
    await ready();
    expect(visible("topupBox")).toBe(true);
    expect(visible("topupDisabled")).toBe(false);
    expect(text("topupNote")).toBe("Top up below, or from the CLI: roger topup.");
    expect(text("topup")).toBe("Add $20.00");
    expect(visible("topupValue")).toBe(true);
    expect(text("topupValue")).toBe("Adds $20.00 to your wallet balance.");
    expect(document.querySelector("#topupPresets .is-active").getAttribute("data-usd")).toBe("20");
    const input = el("topupCustom");
    expect(input.getAttribute("min")).toBe(String(MIN_USD));
    expect(input.getAttribute("max")).toBe(String(MAX_USD));
  });
  it("a preset click moves the choice and clears the custom field; typing a custom clears the presets", async () => {
    checkout();
    await ready();
    await fireEvent.click(document.querySelector('#topupPresets [data-usd="50"]'));
    expect(text("topup")).toBe("Add $50.00");
    expect([...document.querySelectorAll("#topupPresets .is-active")].map((b) => b.dataset.usd)).toEqual(["50"]);
    await fireEvent.input(el("topupCustom"), { target: { value: "7" } });
    expect(document.querySelectorAll("#topupPresets .is-active").length).toBe(0);
    expect(text("topup")).toBe("Add $7.00");
    await fireEvent.click(document.querySelector('#topupPresets [data-usd="5"]'));
    expect(el("topupCustom").value).toBe("");
    expect(text("topup")).toBe("Add $5.00");
  });
  it("the label never promises a rounded amount: whole cents only, the ceiling, nothing under the floor", async () => {
    checkout();
    await ready();
    await fireEvent.input(el("topupCustom"), { target: { value: "10.005" } });
    expect(text("topup")).toBe("Add money");
    expect(text("topupValue")).toBe("Whole cents only.");
    await fireEvent.input(el("topupCustom"), { target: { value: "1000000" } });
    expect(text("topup")).toBe("Add money");
    expect(text("topupValue")).toBe("The maximum top-up is $999,999.99.");
    await fireEvent.input(el("topupCustom"), { target: { value: "0.5" } });
    expect(text("topup")).toBe("Add money");
    expect(visible("topupValue")).toBe(false);
  });
  it("the click refuses both ends and sub-cent amounts before any request", async () => {
    const p = checkout();
    await ready();
    for (const [typed, msg] of [["0.5", "enter an amount of $1 or more"], ["1000000", "the maximum top-up is $999,999.99"], ["10.005", "amount must be a whole number of cents"]]) {
      await fireEvent.input(el("topupCustom"), { target: { value: typed } });
      await fireEvent.click(el("topup"));
      expect(el("topupMsg").textContent).toBe(" " + msg);
    }
    expect(p.calls.some((c) => c.path === "/billing/checkout")).toBe(false);
  });
  it("a valid amount posts the usd and follows the Stripe link", async () => {
    const p = checkout({ "/billing/checkout": [200, { url: "https://checkout.stripe.com/c/x" }] });
    await ready();
    await fireEvent.input(el("topupCustom"), { target: { value: "12.34" } });
    await fireEvent.click(el("topup"));
    expect(el("topup").disabled).toBe(true);
    expect(el("topupMsg").textContent).toBe(" redirecting to Stripe...");
    await waitFor(() => expect(p.navs).toEqual(["https://checkout.stripe.com/c/x"]));
    expect(p.calls.at(-1)).toMatchObject({ path: "/billing/checkout", opts: { method: "POST" }, body: { usd: 12.34 } });
  });
  it("a refusal shows the broker's reason, a missing link or an unreachable broker a plain one, and the button comes back", async () => {
    const p = checkout({ "/billing/checkout": [400, { error: { message: "checkout is paused" } }] });
    await ready();
    await fireEvent.click(el("topup"));
    await waitFor(() => expect(el("topupMsg").textContent).toBe(" checkout is paused"));
    expect(el("topup").disabled).toBe(false);
    expect(p.navs).toEqual([]);
    globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({}) });
    await fireEvent.click(el("topup"));
    await waitFor(() => expect(el("topupMsg").textContent).toBe(" could not start checkout"));
    globalThis.fetch = async () => ({ ok: false, status: 500, json: async () => { throw new Error("not json"); } });
    await fireEvent.click(el("topup"));
    await waitFor(() => expect(el("topupMsg").textContent).toBe(" request failed"));
    globalThis.fetch = async () => { throw new Error("offline"); };
    await fireEvent.click(el("topup"));
    await waitFor(() => expect(el("topupMsg").textContent).toBe(" could not reach the broker"));
    expect(el("topup").disabled).toBe(false);
  });
});
