// /payouts.html (lib/account/PayoutsApp.svelte): the legacy payouts.js contracts from
// test/payouts-states.test.mjs and test/boot-faults.test.mjs, plus the page's rendering
// (features/auth/logged_in_states.feature; the hold/payout rulings in features/money).
// Payouts is for people who serve models: a consumer sees an honest explanation, not zeros
// and a Stripe button that 403s, and the page makes NO operator-only request for them.
import { describe, it, expect } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import PayoutsApp from "#lib/account/PayoutsApp.svelte";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const text = (id) => el(id).textContent.trim();
const OPERATOR_FEEDS = ["/payouts/earnings", "/payouts/history", "/connect/status", "/tower/status", "/metrics/provider"];
const FEEDS_OK = { "/payouts/earnings": [200, {}], "/payouts/history": [200, {}], "/connect/status": [200, {}], "/tower/status": [200, {}], "/metrics/provider": [200, {}] };
const operator = (earnings = {}) => [200, { github_login: "octocat", operator: true, earnings }];
const when = (ts) => new Date(ts * 1000).toLocaleDateString();
const now = Math.floor(Date.now() / 1000);
const ready = () => waitFor(() => expect(el("card").hidden).toBe(false));
function mount(routes) {
  const calls = broker(routes);
  const navs = fakeLocation({ pathname: "/payouts.html" });
  const { unmount } = render(PayoutsApp);
  return { calls, navs, unmount, fetched: () => calls.map((c) => c.path) };
}
const settled = (p, feeds = OPERATOR_FEEDS) => waitFor(() => { for (const f of feeds) expect(p.fetched().some((u) => u.startsWith(f))).toBe(true); });

describe("states", () => {
  it("a consumer: explanation shown, body hidden, no operator-only requests", async () => {
    const p = mount({ "/account": [200, { github_login: "me@rogerai.fm", provider: "email", operator: false }], ...FEEDS_OK });
    await ready();
    expect(visible("poNoOperator")).toBe(true);
    expect(visible("poBody")).toBe(false);
    await new Promise((r) => setTimeout(r, 20));
    for (const f of OPERATOR_FEEDS) expect(p.fetched().some((u) => u.startsWith(f)), `must not call ${f} for a consumer`).toBe(false);
  });
  it("an operator: the full page loads, every feed read once", async () => {
    const p = mount({ "/account": operator({ held: 1, payable: 2, paid: 3 }), ...FEEDS_OK });
    await ready();
    await settled(p);
    expect(visible("poNoOperator")).toBe(false);
    expect(visible("poBody")).toBe(true);
    expect(p.fetched()).toEqual(["/account", "/connect/status", "/payouts/earnings", "/payouts/history", "/metrics/provider?days=30", "/tower/status"]);
  });
  it("an older broker that omits `operator` is treated as an operator (no regression)", async () => {
    mount({ "/account": [200, { github_login: "octocat", earnings: {} }], ...FEEDS_OK });
    await ready();
    expect(visible("poNoOperator")).toBe(false);
    expect(visible("poBody")).toBe(true);
  });
  it("a logged-out visitor (401) goes to login", async () => {
    const p = mount({ "/account": [401, {}] });
    await waitFor(() => expect(p.navs).toEqual(["/login.html"]));
    expect(el("card").hidden).toBe(true);
  });
  for (const status of [500, 503]) {
    it(`a ${status} shows an error and keeps the card with a working sign-out, no redirect`, async () => {
      const p = mount({ "/account": [status, {}], "/auth/logout": [200, {}] });
      await ready();
      expect(p.navs).toEqual([]);
      expect(visible("pageError")).toBe(true);
      expect(el("card").classList.contains("is-fault")).toBe(true);
      expect(visible("poBody")).toBe(false);
      await fireEvent.click(el("logout"));
      await waitFor(() => expect(p.navs).toEqual(["/login.html"]));
      expect(p.calls.at(-1)).toMatchObject({ path: "/auth/logout", opts: { method: "POST" } });
    });
  }
  it("a network failure shows the error, no redirect; a good feed shows none", async () => {
    globalThis.fetch = async () => { throw new Error("offline"); };
    const navs = fakeLocation({ pathname: "/payouts.html" });
    const { unmount } = render(PayoutsApp);
    await ready();
    expect(visible("pageError")).toBe(true);
    expect(navs).toEqual([]);
    unmount();
    mount({ "/account": operator(), ...FEEDS_OK });
    await ready();
    expect(visible("pageError")).toBe(false);
    expect(el("card").classList.contains("is-fault")).toBe(false);
  });
});

describe("the split and the hold timeline", () => {
  it("held, payable, paid in dollars; the meter segments are proportional; the reserve is named", async () => {
    mount({ "/account": operator({ held: 1, payable: 2, paid: 3, reserved: 0.5 }), ...FEEDS_OK });
    await ready();
    expect(text("held")).toBe("$1.00");
    expect(text("payable")).toBe("$2.00");
    expect(text("paid")).toBe("$3.00");
    expect(el("segHeld").style.width).toBe((1 / 6) * 100 + "%");
    expect(el("segPayable").style.width).toBe((2 / 6) * 100 + "%");
    expect(el("segPaid").style.width).toBe("50%");
    expect(visible("reservedNote")).toBe(true);
    expect(text("reservedNote")).toBe("Plus $0.50 in reserve, released after the hold.");
  });
  it("nothing earned: an empty rail, the timeline note, no reserve line, no invented number", async () => {
    mount({ "/account": operator({}), ...FEEDS_OK });
    await ready();
    expect(text("held")).toBe("$0.00");
    expect(el("segHeld").style.width).toBe("0%");
    expect(visible("timeline")).toBe(false);
    expect(visible("timelineEmpty")).toBe(true);
    expect(visible("reservedNote")).toBe(false);
  });
  it("held money: Held is active with the amount and the next clear date; Payable active with its amount; Paid done", async () => {
    const release = now + 5 * 86400 + 3600;
    mount({ "/account": operator({ held: 1, payable: 2, paid: 3, next_release: release }), ...FEEDS_OK });
    await ready();
    expect(visible("timeline")).toBe(true);
    expect(visible("timelineEmpty")).toBe(false);
    expect(el("tlHeld").classList.contains("is-active")).toBe(true);
    expect(text("tlHeldSub")).toBe("$1.00 in the hold window");
    expect(text("tlHeldWhen")).toBe(`next clears ${when(release)} (in 5 days)`);
    expect(el("tlPayable").classList.contains("is-active")).toBe(true);
    expect(text("tlPayableSub")).toBe("$2.00 cleared, withdrawable now");
    expect(el("tlPaid").classList.contains("is-done")).toBe(true);
  });
  it("everything paid out: Held and Payable read done, nothing is 'in the hold window'", async () => {
    mount({ "/account": operator({ held: 0, payable: 0, paid: 9 }), ...FEEDS_OK });
    await ready();
    expect(el("tlHeld").classList.contains("is-done")).toBe(true);
    expect(el("tlPayable").classList.contains("is-done")).toBe(true);
    expect(el("tlPayable").classList.contains("is-active")).toBe(false);
    expect(text("tlHeldSub")).toBe("in the hold window");
    expect(text("tlHeldWhen")).toBe("");
  });
});

describe("Stripe Connect status and the policy facts", () => {
  it("active: the request button replaces onboarding; the minimum, hold and schedule come from the broker", async () => {
    mount({ "/account": operator({ held: 1 }), ...FEEDS_OK, "/connect/status": [200, { status: "active", min_payout: 25, hold_days: 30, schedule: "monthly" }] });
    await ready();
    await waitFor(() => expect(text("connect")).toBe("active"));
    expect(visible("request")).toBe(true);
    expect(visible("onboard")).toBe(false);
    expect(text("minNote")).toBe("$25.00");
    expect(text("holdNote")).toBe("30 days");
    expect(text("tlHeldSub")).toBe("held for 30 days");
    expect(text("scheduleNote")).toBe("monthly");
  });
  it("not onboarded yet: the Stripe button stays; a missing status reads 'none'", async () => {
    mount({ "/account": operator(), ...FEEDS_OK, "/connect/status": [200, { min_payout: 10 }] });
    await ready();
    await waitFor(() => expect(text("connect")).toBe("none"));
    expect(visible("onboard")).toBe(true);
    expect(visible("request")).toBe(false);
    expect(text("minNote")).toBe("$10.00");
  });
  it("a refused status leaves the defaults: '-', the $25.00 minimum, 30 days, monthly", async () => {
    const p = mount({ "/account": operator(), ...FEEDS_OK, "/connect/status": [500, {}] });
    await ready();
    await settled(p);
    expect(text("connect")).toBe("-");
    expect(text("minNote")).toBe("$25.00");
    expect(text("holdNote")).toBe("30 days");
    expect(text("scheduleNote")).toBe("monthly");
    expect(visible("onboard")).toBe(true);
  });
  it("onboarding posts and follows the Stripe link; a refusal says it could not start", async () => {
    const p = mount({ "/account": operator(), ...FEEDS_OK, "/connect/onboard": [200, { url: "https://connect.stripe.com/setup/x" }] });
    await ready();
    await fireEvent.click(el("onboard"));
    await waitFor(() => expect(p.navs).toEqual(["https://connect.stripe.com/setup/x"]));
    expect(p.calls.at(-1)).toMatchObject({ path: "/connect/onboard", opts: { method: "POST" } });
    p.calls.length = 0;
    globalThis.fetch = async () => ({ ok: false, status: 500, json: async () => ({}) });
    await fireEvent.click(el("onboard"));
    await waitFor(() => expect(text("payMsg")).toBe("could not start onboarding"));
  });
  it("a payout request: says requested and reloads; a refusal shows the broker's reason; unreachable says failed", async () => {
    const p = mount({ "/account": operator({ payable: 30 }), ...FEEDS_OK, "/connect/status": [200, { status: "active" }], "/payouts/request": [200, { ok: true }] });
    await ready();
    await waitFor(() => expect(visible("request")).toBe(true));
    await fireEvent.click(el("request"));
    await waitFor(() => expect(text("payMsg")).toBe("payout requested"));
    expect(p.navs).toEqual(["<reload>"]);
    expect(p.calls.at(-1)).toMatchObject({ path: "/payouts/request", opts: { method: "POST" } });
    globalThis.fetch = async () => ({ ok: false, status: 400, json: async () => ({ error: { message: "payable balance is under the $25.00 minimum" } }) });
    await fireEvent.click(el("request"));
    await waitFor(() => expect(text("payMsg")).toBe("payable balance is under the $25.00 minimum"));
    globalThis.fetch = async () => { throw new Error("offline"); };
    await fireEvent.click(el("request"));
    await waitFor(() => expect(text("payMsg")).toBe("payout failed"));
    globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => { throw new Error("not json"); } });
    await fireEvent.click(el("request"));
    await waitFor(() => expect(text("payMsg")).toBe("payout failed"));   // a 2xx that is not JSON is not a payout
    expect(p.navs.filter((n) => n === "<reload>").length).toBe(1);
  });
});

describe("where the money came from", () => {
  it("the release ladder: one dated step per upcoming clear, lots counted, zero-amount buckets dropped", async () => {
    const d1 = now + 2 * 86400 + 3600, d2 = now + 20 * 86400 + 3600;
    mount({ "/account": operator({ held: 7 }), ...FEEDS_OK, "/payouts/earnings": [200, { releases: [{ date: d1, amount: 5, lot_count: 1 }, { date: d2, amount: 2, lot_count: 3 }, { date: d2, amount: 0, lot_count: 0 }] }] });
    await ready();
    await waitFor(() => expect(visible("ladderWrap")).toBe(true));
    const steps = document.querySelectorAll("#ladder .po-ladder__step");
    expect(steps.length).toBe(2);
    expect(steps[0].querySelector(".po-ladder__date").textContent).toBe(`${when(d1)}in 2 days - 1 lot`);
    expect(steps[0].querySelector(".po-ladder__amount").textContent).toBe("$5.00");
    expect(steps[1].querySelector(".po-ladder__meta").textContent).toBe("in 20 days - 3 lots");
  });
  it("no held lots: no ladder", async () => {
    const p = mount({ "/account": operator({ paid: 1 }), ...FEEDS_OK, "/payouts/earnings": [200, { releases: [] }] });
    await ready();
    await settled(p);
    expect(visible("ladderWrap")).toBe(false);
  });
  it("by model and node: earning models sorted by earnings, annotated with node and requests; the window is the broker's", async () => {
    mount({ "/account": operator({ paid: 1 }), ...FEEDS_OK, "/metrics/provider": [200, { period_days: 7, models: [
      { model: "small", node_id: "K6ABC", requests: 3, earnings_usd: 0.25 },
      { model: "idle", node_id: "K6ABC", requests: 0, earnings_usd: 0 },
      { model: "gpt-oss-120b", node_id: "K6XYZ", requests: 12000, earnings_usd: 4.5 },
    ] }] });
    await ready();
    await waitFor(() => expect(document.querySelectorAll("#breakdown li").length).toBe(2));
    const rows = document.querySelectorAll("#breakdown li");
    expect(rows[0].querySelector("b").textContent).toBe("gpt-oss-120b");
    expect(rows[0].querySelector(".po-break__meta").textContent).toMatch(/^K6XYZ - 12k req$/);
    expect(rows[0].querySelector(".r-cost").textContent).toBe("$4.50");
    expect(rows[1].querySelector("b").textContent).toBe("small");
    expect(text("breakdownWindow")).toBe("Earnings over the last 7 days, by what you served.");
    expect(visible("breakdownEmpty")).toBe(false);
  });
  it("no served traffic, or a refused feed: the honest-empty note, never a number", async () => {
    const p = mount({ "/account": operator(), ...FEEDS_OK, "/metrics/provider": [200, { models: [{ model: "x", earnings_usd: 0 }] }] });
    await ready();
    await waitFor(() => expect(visible("breakdownEmpty")).toBe(true));
    expect(document.querySelectorAll("#breakdown li").length).toBe(0);
    expect(text("breakdownWindow")).toBe("Earnings over the last 30 days, by what you served.");
    p.unmount();
    const q = mount({ "/account": operator(), ...FEEDS_OK, "/metrics/provider": [500, {}] });
    await waitFor(() => expect(q.fetched()).toContain("/metrics/provider?days=30"));
    await waitFor(() => expect(visible("breakdownEmpty")).toBe(true));
  });
});

describe("Towers and relay", () => {
  it("an operator who only serves never sees the panel", async () => {
    const p = mount({ "/account": operator({ paid: 1 }), ...FEEDS_OK, "/payouts/earnings": [200, { serving: 1, tower_relay: 0 }], "/tower/status": [200, { towers: [] }] });
    await ready();
    await settled(p);
    expect(visible("towerPanel")).toBe(false);
  });
  it("relay earnings with no enrolled Tower: the split and the empty-fleet note", async () => {
    mount({ "/account": operator({ paid: 1 }), ...FEEDS_OK, "/payouts/earnings": [200, { serving: 1, tower_relay: 0.5 }], "/tower/status": [200, { towers: [] }] });
    await ready();
    await waitFor(() => expect(visible("towerPanel")).toBe(true));
    expect(text("servingEarn")).toBe("$1.00");
    expect(text("relayEarn")).toBe("$0.50");
    expect(visible("towersEmpty")).toBe(true);
    expect(visible("towersTitle")).toBe(false);
  });
  it("the operator's own Towers: link, routable stations, lease and the state pill", async () => {
    const lease = now + 3 * 86400 + 3600;
    mount({ "/account": operator({ paid: 1 }), ...FEEDS_OK, "/payouts/earnings": [500, {}], "/tower/status": [200, { towers: [
      { tower_id: "tw-1", state: "ready", may_take_work: true, link_live: true, routable: ["a", "b"], lease_expires: lease },
      { tower_id: "tw-2", state: "draining", link_live: false, routable: [] },
    ] }] });
    await ready();
    await waitFor(() => expect(visible("towerPanel")).toBe(true));
    expect(visible("towersTitle")).toBe(true);
    expect(visible("towersEmpty")).toBe(false);
    expect(text("relayEarn")).toBe("-");   // the earnings feed was refused: no number is invented
    const rows = document.querySelectorAll("#towers .po-tower");
    expect(rows.length).toBe(2);
    expect(rows[0].querySelector("b").textContent).toBe("tw-1");
    expect(rows[0].querySelector(".po-tower__meta").textContent).toBe("link live · 2 stations routable · lease in 3 days");
    expect(rows[0].querySelector(".po-tower__state").textContent).toBe("ready · live");
    expect(rows[0].querySelector(".po-tower__state").className).toBe("po-tower__state po-tower__state--ready");
    expect(rows[1].querySelector(".po-tower__meta").textContent).toBe("link down · 0 stations routable");
    expect(rows[1].querySelector(".po-tower__state").textContent).toBe("draining");
  });
});

describe("payout history and the ledger", () => {
  const HISTORY = { payouts: [{ id: "po_1", created_at: now - 86400, state: "paid", amount: 10, stripe_transfer_id: "tr_1" }], ledger: [
    { ts: now - 86400, kind: "payout", amount: -10 },
    { ts: now - 2 * 86400, kind: "chargeback", amount: -1.5, ref: "dp_9" },
    { ts: now - 3 * 86400, kind: "weird", amount: 0.1 },
  ] };
  it("rows: date, state and amount per payout, the Stripe transfer as the title; ledger kinds in words with their refs", async () => {
    mount({ "/account": operator({ paid: 10 }), ...FEEDS_OK, "/payouts/history": [200, HISTORY] });
    await ready();
    await waitFor(() => expect(document.querySelectorAll("#payouts .po-payout").length).toBe(1));
    const head = document.querySelector("#payouts .po-payout__head");
    expect(head.textContent).toBe(`›${when(now - 86400)} - paid$10.00`);
    expect(head.title).toBe("Stripe transfer tr_1");
    expect(head.getAttribute("aria-expanded")).toBe("false");
    const ledger = document.querySelectorAll("#ledger li");
    expect(ledger.length).toBe(3);
    expect(ledger[0].querySelector(".r-model").textContent).toBe(`${when(now - 86400)} - Payout`);
    expect(ledger[0].querySelector(".r-cost").textContent).toBe("-$10.00");
    expect(ledger[1].querySelector(".r-model").textContent).toBe(`${when(now - 2 * 86400)} - Clawback (dispute)dp_9`);
    expect(ledger[1].querySelector(".po-ledger__ref").textContent).toBe("dp_9");
    expect(ledger[2].querySelector(".r-model").textContent).toBe(`${when(now - 3 * 86400)} - weird`);
    expect(visible("payoutsEmpty")).toBe(false);
    expect(visible("ledgerEmpty")).toBe(false);
  });
  it("expanding a payout fetches its funding receipts once and shows the lineage; collapsing keeps them", async () => {
    const p = mount({ "/account": operator({ paid: 10 }), ...FEEDS_OK, "/payouts/history": [200, HISTORY],
      "/payouts/po_1/lots": [200, { lots: [{ model: "gpt-oss-120b", node: "K6ABC", gross: 0.02, request_id: "req_1", created_at: now - 90000 }] }] });
    await ready();
    await waitFor(() => expect(document.querySelectorAll("#payouts .po-payout").length).toBe(1));
    const row = document.querySelector("#payouts .po-payout");
    const head = row.querySelector(".po-payout__head");
    await fireEvent.click(head);
    expect(row.classList.contains("is-open")).toBe(true);
    expect(head.getAttribute("aria-expanded")).toBe("true");
    expect(row.querySelector(".po-payout__drawer").hidden).toBe(false);
    await waitFor(() => expect(row.querySelectorAll(".po-lots li").length).toBe(1));
    expect(row.querySelector(".po-lots b").textContent).toBe("gpt-oss-120b");
    expect(row.querySelector(".po-lots__meta").textContent).toBe(`K6ABC - req_1 - ${when(now - 90000)}`);
    expect(row.querySelector(".po-lots .r-cost").textContent).toBe("$0.02");
    await fireEvent.click(head);
    expect(row.querySelector(".po-payout__drawer").hidden).toBe(true);
    expect(head.getAttribute("aria-expanded")).toBe("false");
    await fireEvent.click(head);
    expect(row.querySelectorAll(".po-lots li").length).toBe(1);
    expect(p.fetched().filter((u) => u === "/payouts/po_1/lots").length).toBe(1);
  });
  it("a payout with no receipts says so", async () => {
    mount({ "/account": operator({ paid: 10 }), ...FEEDS_OK, "/payouts/history": [200, HISTORY], "/payouts/po_1/lots": [200, { lots: [] }] });
    await ready();
    await waitFor(() => expect(document.querySelectorAll("#payouts .po-payout").length).toBe(1));
    await fireEvent.click(document.querySelector("#payouts .po-payout__head"));
    await waitFor(() => expect(document.querySelector(".po-payout__drawer").textContent).toMatch(/No funding receipts found for this payout\./));
  });
  it("no payouts and no entries: both honest-empty notes; a refused history shows neither", async () => {
    const p = mount({ "/account": operator(), ...FEEDS_OK, "/payouts/history": [200, { payouts: [], ledger: [] }] });
    await ready();
    await waitFor(() => expect(visible("payoutsEmpty")).toBe(true));
    expect(visible("ledgerEmpty")).toBe(true);
    p.unmount();
    const q = mount({ "/account": operator(), ...FEEDS_OK, "/payouts/history": [503, {}] });
    await waitFor(() => expect(q.fetched()).toContain("/payouts/history"));
    await new Promise((r) => setTimeout(r, 20));
    expect(visible("payoutsEmpty")).toBe(false);
    expect(visible("ledgerEmpty")).toBe(false);
  });
});
