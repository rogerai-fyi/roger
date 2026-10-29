// /dashboard.html (lib/dashboard/DashboardApp.svelte): the legacy dashboard.js contracts from
// test/dashboard-states.test.mjs, test/dashboard.test.mjs (the frontier toggle math) and the
// dashboard rows of test/login-loop.test.mjs (a feed 401/403, or a script error after the
// session is confirmed, is an error to show, never a bounce to login).
import { describe, it, expect } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import DashboardApp from "#lib/dashboard/DashboardApp.svelte";
import { frontierAt, rollupModels, linePlot, axisLabels } from "#lib/dashboard/charts.js";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const acct = (o = {}) => [200, { github_login: "me@rogerai.fm", github_id: 0, provider: "email", balance: 5, ...o }];
const EMPTY_SERIES = [200, { daily: [], is_consumer: true, is_provider: false, savings: {} }];
const SERIES = [200, { is_consumer: true, is_provider: false, savings: {},
  daily: [{ bucket: "2026-10-01", requests: 3, tokens_in: 10, tokens_out: 20, spend: 0.5, earned: 0, models: [] }] }];
const ready = () => waitFor(() => expect(el("card").hidden).toBe(false));
const paths = (calls) => calls.map((c) => c.path.split("?")[0]).sort();

describe("states", () => {
  it("empty dashboard: shows the balance and the next steps, no extra requests; a new person is told how to earn", async () => {
    const calls = broker({ "/account": acct(), "/metrics/series": EMPTY_SERIES });
    fakeLocation({ pathname: "/dashboard.html" });
    render(DashboardApp);
    await waitFor(() => expect(visible("dashEmpty")).toBe(true));
    expect(el("emptyBalance").textContent).toBe("$5.00");
    expect(visible("earnCta")).toBe(true);
    expect(visible("dashLoading")).toBe(false);
    expect(paths(calls)).toEqual(["/account", "/metrics/series"]);
    expect(el("who").textContent).toBe("me@rogerai.fm");   // the signed-in line names the provider
  });
  it("empty dashboard for someone who already earns does not push the earn CTA", async () => {
    broker({ "/account": acct(), "/metrics/series": [200, { daily: [], is_provider: true, savings: {} }] });
    fakeLocation({ pathname: "/dashboard.html" });
    render(DashboardApp);
    await waitFor(() => expect(visible("dashEmpty")).toBe(true));
    expect(visible("earnCta")).toBe(false);
  });
  it("with traffic: tiles from the series, and a balance tile from /account with no extra request", async () => {
    const calls = broker({ "/account": acct({ balance: 7.25 }), "/metrics/series": SERIES });
    fakeLocation({ pathname: "/dashboard.html" });
    render(DashboardApp);
    await waitFor(() => expect(visible("stats")).toBe(true));
    expect(visible("tileBalance")).toBe(true);
    expect(el("statBalance").textContent).toBe("$7.25");
    expect(el("statReq").textContent).toBe("3");
    expect(el("statTok").textContent).toBe("30");
    expect(el("statSpend").textContent).toBe("$0.50");
    expect(visible("tileSpend")).toBe(true);
    expect(visible("tileEarn")).toBe(false);
    expect(visible("tilePayable")).toBe(false);
    expect(visible("tokWrap")).toBe(true);
    expect(document.querySelectorAll("#tokChart svg path.ds-stroke").length).toBe(1);
    expect(visible("moneyWrap")).toBe(true);
    expect(el("moneyLegend").textContent).toBe("spend");
    expect(visible("hero")).toBe(false);   // no savings estimate: no savings hero
    expect(paths(calls)).toEqual(["/account", "/metrics/series"]);
  });
  it("an operator also sees what is payable now and the earned hero; a consumer does not", async () => {
    broker({ "/account": acct({ operator: true, earnings: { held: 1, payable: 4.5, paid: 2 } }),
      "/metrics/series": [200, { ...SERIES[1], is_provider: true, daily: [{ ...SERIES[1].daily[0], earned: 0.8 }] }] });
    fakeLocation({ pathname: "/dashboard.html" });
    const { unmount } = render(DashboardApp);
    await waitFor(() => expect(visible("tilePayable")).toBe(true));
    expect(el("statPayable").textContent).toBe("$4.50");
    expect(visible("heroEarn")).toBe(true);    // no savings estimate, earnings: the earned hero (the legacy else-branch)
    expect(el("heroAmtEarn").textContent).toBe("$0.80");
    expect(visible("earnCta")).toBe(false);
    unmount();
    broker({ "/account": acct({ operator: false }), "/metrics/series": SERIES });
    render(DashboardApp);
    await waitFor(() => expect(visible("stats")).toBe(true));
    expect(visible("tilePayable")).toBe(false);
  });
  it("a consumer with savings gets the hero, and the baseline cycles the frontier references", async () => {
    broker({ "/account": acct(), "/metrics/series": [200, { is_consumer: true, is_provider: false,
      savings: { baseline_model: "gpt-4o", spend_usd: 4, frontier_est: 10, savings_est: 6, reference_note: "list prices, not quotes",
        reference: [{ model: "gpt-4o", frontier_est: 10 }, { model: "claude-opus-4-8", frontier_est: 30 }, { model: "gemini-2.5-pro", frontier_est: 6 }] },
      daily: [{ bucket: "2026-10-01", requests: 3, tokens_in: 10, tokens_out: 20, spend: 4, savings_est: 6, models: [{ model: "gpt-oss-120b", requests: 3, tokens_in: 10, tokens_out: 20, spend: 4 }] }] }] });
    fakeLocation({ pathname: "/dashboard.html" });
    render(DashboardApp);
    await waitFor(() => expect(visible("hero")).toBe(true));
    expect(el("heroAmt").textContent).toBe("$6.00");
    expect(el("heroBase").textContent).toBe("gpt-4o");
    expect(el("heroFrontier").textContent).toBe("$10.00");
    expect(el("savingsNote").textContent).toBe("list prices, not quotes");
    expect(document.querySelectorAll("#savingsChart .ds-cols__col").length).toBe(1);
    expect(document.querySelector("#modelBars .ds-mbars__val").textContent).toBe("30 tok / $4.00");
    await fireEvent.click(el("heroBase"));
    expect(el("heroBase").textContent).toBe("claude-opus-4-8");
    expect(el("heroFrontier").textContent).toBe("$30.00");
    expect(el("heroAmt").textContent).toBe("$26.00");
    await fireEvent.keyDown(el("heroBase"), { key: "Enter" });
    expect(el("heroBase").textContent).toBe("gemini-2.5-pro");
    expect(el("heroAmt").textContent).toBe("$2.00");   // gemini would have billed $6 against $4 paid
  });
});

describe("the hero's figures", () => {
  it("until the baseline is clicked the hero is the broker's own headline (the figures /billing shows); a click moves to the reference table", async () => {
    // the broker's savings_est carries its own rounding: the headline shows it as sent, not a client re-computation
    broker({ "/account": acct(), "/metrics/series": [200, { is_consumer: true, is_provider: false,
      savings: { baseline_model: "gpt-4o", spend_usd: 4, frontier_est: 10, savings_est: 5.5,
        reference: [{ model: "gpt-4o", frontier_est: 10.000004 }, { model: "claude-opus-4-8", frontier_est: 30 }] },
      daily: [{ bucket: "2026-10-01", requests: 1, tokens_in: 10, tokens_out: 20, spend: 4, savings_est: 5.5, models: [] }] }] });
    fakeLocation({ pathname: "/dashboard.html" });
    render(DashboardApp);
    await waitFor(() => expect(visible("hero")).toBe(true));
    expect(el("heroAmt").textContent).toBe("$5.50");
    expect(el("heroFrontier").textContent).toBe("$10.00");
    expect(el("heroBase").textContent).toBe("gpt-4o");
    await fireEvent.click(el("heroBase"));
    expect(el("heroBase").textContent).toBe("claude-opus-4-8");
    expect(el("heroAmt").textContent).toBe("$26.00");
    await fireEvent.click(el("heroBase"));   // back round to the baseline: the reference row's figures now
    expect(el("heroBase").textContent).toBe("gpt-4o");
    expect(el("heroAmt").textContent).toBe("$6.00");
  });
});

describe("the login-loop rule", () => {
  it("logged out (/account fails) goes to login", async () => {
    broker({ "/account": [401, {}] });
    const navs = fakeLocation({ pathname: "/dashboard.html" });
    render(DashboardApp);
    await waitFor(() => expect(navs).toEqual(["/login.html"]));
  });
  for (const status of [401, 403]) {
    it(`a ${status} from /metrics/series shows an error and does NOT bounce to login`, async () => {
      broker({ "/account": acct(), "/metrics/series": [status, {}] });
      const navs = fakeLocation({ pathname: "/dashboard.html" });
      render(DashboardApp);
      await waitFor(() => expect(visible("dashError")).toBe(true));
      expect(navs).toEqual([]);
      expect(el("card").hidden).toBe(false);
    });
  }
  for (const [what, body] of [["a series that is not a list", { daily: { bucket: "2026-10-01" }, is_consumer: true }], ["a bucket that is not an object", { daily: [null], is_consumer: true }], ["a 200 with no body", null]]) {
    it(`${what} is the error state, never a dead card or fabricated numbers (the legacy render threw into its catch)`, async () => {
      broker({ "/account": acct(), "/metrics/series": [200, body] });
      const navs = fakeLocation({ pathname: "/dashboard.html" });
      render(DashboardApp);
      await waitFor(() => expect(visible("dashError")).toBe(true));
      expect(visible("dashEmpty")).toBe(false);
      expect(navs).toEqual([]);
      expect(el("card").hidden).toBe(false);
      expect(el("logout")).toBeTruthy();   // a visible card always has a working sign-out
    });
  }
  it("a script error after the session is confirmed shows the error inside the card, never a redirect", async () => {
    globalThis.fetch = async (url) => {
      const path = String(url).replace("https://broker.rogerai.fm", "");
      if (path === "/account") return { ok: true, status: 200, json: async () => ({ github_login: "ada" }) };
      return { ok: true, status: 200, json: async () => { throw new Error("boom"); } };
    };
    const navs = fakeLocation({ pathname: "/dashboard.html" });
    render(DashboardApp);
    await waitFor(() => expect(visible("dashError")).toBe(true));
    expect(navs).toEqual([]);
    expect(el("card").hidden).toBe(false);
  });
});

describe("the frontier toggle math (charts.js)", () => {
  const ref = [{ model: "gpt-4o", frontier_est: 10 }, { model: "claude-opus-4-8", frontier_est: 30 }, { model: "gemini-2.5-pro", frontier_est: 6 }];
  it("cycles the reference list and wraps both directions", () => {
    expect(frontierAt(ref, 4, 0).model).toBe("gpt-4o");
    expect(frontierAt(ref, 4, 1).model).toBe("claude-opus-4-8");
    expect(frontierAt(ref, 4, 2).model).toBe("gemini-2.5-pro");
    expect(frontierAt(ref, 4, 3).model).toBe("gpt-4o");
    expect(frontierAt(ref, 4, -1).model).toBe("gemini-2.5-pro");
    expect(frontierAt(ref, 4, 7).model).toBe("claude-opus-4-8");
  });
  it("frontier = the selected model's estimate; savings = frontier - spend, floored at 0; a missing estimate reads as 0", () => {
    expect(frontierAt(ref, 4, 1)).toEqual({ model: "claude-opus-4-8", frontier: 30, savings: 26 });
    expect(frontierAt(ref, 9, 2)).toEqual({ model: "gemini-2.5-pro", frontier: 6, savings: 0 });
    expect(frontierAt([{ model: "x" }], 5, 0)).toEqual({ model: "x", frontier: 0, savings: 0 });
  });
  it("rolls models up over the window, biggest first; plots and axis labels follow the buckets", () => {
    const daily = [
      { bucket: "2026-10-01", tokens_out: 10, models: [{ model: "a", tokens_in: 1, tokens_out: 1, spend: 1 }, { model: "b", tokens_in: 5, tokens_out: 5 }] },
      { bucket: "2026-10-02", tokens_out: 30, models: [{ model: "a", tokens_in: 2, tokens_out: 2, spend: 2 }] },
      { bucket: "2026-10-03", tokens_out: 20, models: [] },
    ];
    expect(rollupModels(daily).map((r) => [r.model, r.tokens, r.spend])).toEqual([["b", 10, 0], ["a", 6, 3]]);
    const plot = linePlot(daily, [{ key: "tokens_out", color: "c", fill: "f" }]);
    expect(plot.lines[0].d.startsWith("M8.0 ")).toBe(true);
    expect(plot.lines[0].area.endsWith(" Z")).toBe(true);
    expect(plot.grid.map((g) => g.cls)).toEqual(["ds-axis", "ds-grid", "ds-grid"]);
    expect(axisLabels(daily)).toEqual(["Oct 1", "Oct 2", "Oct 3"]);
    expect(axisLabels([daily[0]])).toEqual(["Oct 1"]);
  });
});
