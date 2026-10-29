// /usage.html (lib/usage/UsageApp.svelte over Figure, TimeSeries, series.js and the shared
// rollup.js): the legacy metrics.js contracts from test/honest-feeds.test.mjs (a 403 on the
// series feed is an error, never the signed-out gate; a 401 is the gate) and the page's
// rendering: the session check, the range, the roles, the honest empties, the totals, the
// figures with their crosshair readouts, the per-model bars and tables, the private history.
import { describe, it, expect, vi } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import UsageApp from "#lib/usage/UsageApp.svelte";
import { geometry, points, anyValue, nearest, W, H } from "#lib/usage/series.js";
import { rollupModels, rollupTotals } from "#lib/rollup.js";
import RogerFmt from "#lib/fmt.js";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const text = (id) => el(id).textContent.trim();
const ACCT = [200, { github_login: "octocat", github_id: 7 }];
const DAILY = [
  { bucket: "2026-10-01", requests: 10, tokens_in: 1000, tokens_out: 2000, spend: 0.5, earned: 0, models: [{ model: "gpt-oss-120b", requests: 10, tokens_in: 1000, tokens_out: 2000, spend: 0.5 }] },
  { bucket: "2026-10-02", requests: 4, tokens_in: 400, tokens_out: 600, spend: 0.2, earned: 1.5, models: [{ model: "qwen", requests: 4, tokens_in: 400, tokens_out: 600, spend: 0.2, earned: 1.5 }] },
  { bucket: "2026-10-03", requests: 1, tokens_in: 50, tokens_out: 10, spend: 0.01, earned: 0.25, models: [{ model: "gpt-oss-120b", requests: 1, tokens_in: 50, tokens_out: 10, spend: 0.01, earned: 0.25 }] },
];
const SERIES = (extra = {}) => [200, { is_provider: true, is_consumer: true, daily: DAILY, ...extra }];
const ready = () => waitFor(() => expect(el("card").hidden).toBe(false));
function mount(routes) {
  const calls = broker(routes);
  const navs = fakeLocation({ pathname: "/usage.html" });
  const { unmount } = render(UsageApp);
  return { calls, navs, unmount, fetched: () => calls.map((c) => c.path) };
}
const loaded = () => waitFor(() => expect(visible("allLoading")).toBe(false));

describe("the session check and the gate (the login-loop rule)", () => {
  it("signed out, or the broker down: the gate, no series read", async () => {
    const p = mount({ "/account": [401, {}] });
    await waitFor(() => expect(visible("gate")).toBe(true));
    expect(visible("card")).toBe(false);
    expect(p.fetched()).toEqual(["/account"]);
    p.unmount();
    globalThis.fetch = async () => { throw new Error("offline"); };
    fakeLocation({ pathname: "/usage.html" });
    render(UsageApp);
    await waitFor(() => expect(visible("gate")).toBe(true));
  });
  it("signed in: the card, the 30-day series read, both sections, the private history", async () => {
    const p = mount({ "/account": ACCT, "/metrics/series": SERIES(), "/usage": [200, { recent: [] }] });
    await ready();
    await loaded();
    expect(visible("gate")).toBe(false);
    expect(p.fetched()).toEqual(["/account", "/metrics/series?days=30", "/usage"]);
    expect(visible("provider")).toBe(true);
    expect(visible("usage")).toBe(true);
    expect(visible("allError")).toBe(false);
  });
  it("a 403 from the series feed is an error, not the signed-out gate; so is a 500", async () => {
    const p = mount({ "/account": ACCT, "/metrics/series": [403, {}] });
    await ready();
    await waitFor(() => expect(visible("allError")).toBe(true));
    expect(visible("gate")).toBe(false);
    expect(visible("card")).toBe(true);
    expect(visible("provider")).toBe(false);
    expect(visible("usage")).toBe(false);
    p.unmount();
    mount({ "/account": ACCT, "/metrics/series": [500, {}] });
    await waitFor(() => expect(visible("allError")).toBe(true));
  });
  it("a series the page cannot render (a bucket that is not an object, a slice that is not) is the error, never a dead card", async () => {
    for (const body of [[null], [{ bucket: "2026-10-01", models: [null] }], { bucket: "2026-10-01" }].map((daily) => ({ is_provider: true, is_consumer: true, daily })).concat([null])) {
      const p = mount({ "/account": ACCT, "/metrics/series": [200, body] });
      await ready();
      await waitFor(() => expect(visible("allError")).toBe(true));
      expect(visible("gate")).toBe(false);
      expect(el("logout")).toBeTruthy();
      p.unmount();
    }
  });
  it("a 401 (the session really gone) still shows the gate and hides the card", async () => {
    mount({ "/account": ACCT, "/metrics/series": [401, {}] });
    await ready();
    await waitFor(() => expect(visible("gate")).toBe(true));
    expect(visible("card")).toBe(false);
  });
  it("sign out posts /auth/logout then goes to login", async () => {
    const p = mount({ "/account": ACCT, "/metrics/series": SERIES(), "/usage": [200, {}], "/auth/logout": [200, {}] });
    await ready();
    await fireEvent.click(el("logout"));
    await waitFor(() => expect(p.navs).toEqual(["/login.html"]));
    expect(p.calls.some((c) => c.path === "/auth/logout" && c.opts.method === "POST")).toBe(true);
  });
});

describe("roles and ranges", () => {
  it("a pure provider sees provider metrics only, and the history is never read", async () => {
    const p = mount({ "/account": ACCT, "/metrics/series": SERIES({ is_consumer: false }), "/usage": [200, { recent: [] }] });
    await ready();
    await loaded();
    expect(visible("provider")).toBe(true);
    expect(visible("usage")).toBe(false);
    expect(p.fetched()).not.toContain("/usage");
  });
  it("a pure consumer sees usage only", async () => {
    mount({ "/account": ACCT, "/metrics/series": SERIES({ is_provider: false }), "/usage": [200, {}] });
    await ready();
    await loaded();
    expect(visible("provider")).toBe(false);
    expect(visible("usage")).toBe(true);
  });
  it("an older broker that omits the flags: whichever side has activity, and usage when there is none", async () => {
    const p = mount({ "/account": ACCT, "/metrics/series": [200, { daily: DAILY }], "/usage": [200, {}] });
    await ready();
    await loaded();
    expect(visible("provider")).toBe(true);   // earned > 0
    expect(visible("usage")).toBe(true);      // requests > 0
    p.unmount();
    const q = mount({ "/account": ACCT, "/metrics/series": [200, { daily: [] }], "/usage": [200, {}] });
    await ready();
    await loaded();
    expect(visible("provider")).toBe(false);
    expect(visible("usage")).toBe(true);      // never blank for a real account
    expect(visible("useEmpty")).toBe(true);
    q.unmount();
    mount({ "/account": ACCT, "/metrics/series": [200, { daily: [{ bucket: "2026-10-01", earned: 2, requests: 0 }] }], "/usage": [200, {}] });
    await ready();
    await loaded();
    expect(visible("provider")).toBe(true);
    expect(visible("usage")).toBe(false);
  });
  it("no activity in the range: the honest empties, no totals, no figures, no tables", async () => {
    mount({ "/account": ACCT, "/metrics/series": SERIES({ daily: [{ bucket: "2026-10-01", requests: 0 }] }), "/usage": [200, {}] });
    await ready();
    await loaded();
    expect(visible("provEmpty")).toBe(true);
    expect(visible("useEmpty")).toBe(true);
    for (const id of ["provTotals", "useTotals", "provEarnTs", "provTokTs", "provReqTs", "useSpendTs", "useTokTs", "useReqTs", "provChart", "useChart", "provTableWrap", "useTableWrap"]) expect(visible(id), id).toBe(false);
  });
  it("a range switch keeps both sections and the recent requests on screen while the series reloads", async () => {
    let hold;
    const gate = new Promise((r) => { hold = r; });
    let reads = 0;
    globalThis.fetch = async (url) => {
      const path = String(url).replace("https://broker.rogerai.fm", "");
      if (path === "/account") return { ok: true, status: 200, json: async () => ({ github_login: "octocat", github_id: 7 }) };
      if (path.startsWith("/metrics/series")) { if (reads++ > 0) await gate; return { ok: true, status: 200, json: async () => ({ is_provider: true, is_consumer: true, daily: reads > 1 ? [] : DAILY }) }; }
      if (path === "/usage") return { ok: true, status: 200, json: async () => ({ recent: reads > 1 ? [] : [{ model: "qwen", node: "K6ABC", cost: 0.02, prompt_tokens: 10, completion_tokens: 5 }] }) };
      return { ok: false, status: 404, json: async () => ({}) };
    };
    fakeLocation({ pathname: "/usage.html" });
    render(UsageApp);
    await ready();
    await waitFor(() => expect(visible("useRecentWrap")).toBe(true));
    await fireEvent.click(document.querySelector('.mx-range__opt[data-days="7"]'));
    await waitFor(() => expect(visible("allLoading")).toBe(true));
    expect(visible("provider")).toBe(true);
    expect(visible("usage")).toBe(true);
    expect(visible("provLoading")).toBe(true);
    expect(visible("useLoading")).toBe(true);
    expect(visible("provTotals")).toBe(false);
    expect(visible("useRecentWrap")).toBe(true);   // the previous rows stay until new ones come
    hold();
    await loaded();
    expect(visible("useEmpty")).toBe(true);
    expect(visible("useRecentWrap")).toBe(true);   // an empty refetch keeps what was on screen
  });
  it("the range buttons re-read the series for their days and mark themselves", async () => {
    const p = mount({ "/account": ACCT, "/metrics/series": SERIES(), "/usage": [200, {}] });
    await ready();
    await loaded();
    expect(document.querySelector(".mx-range__opt.is-active").dataset.days).toBe("30");
    await fireEvent.click(document.querySelector('.mx-range__opt[data-days="7"]'));
    await waitFor(() => expect(p.fetched()).toContain("/metrics/series?days=7"));
    expect(document.querySelector(".mx-range__opt.is-active").dataset.days).toBe("7");
    expect(document.querySelectorAll(".mx-range__opt.is-active").length).toBe(1);
    await fireEvent.click(document.querySelector('.mx-range__opt[data-days="90"]'));
    await waitFor(() => expect(p.fetched()).toContain("/metrics/series?days=90"));
    await loaded();
    expect(visible("provider")).toBe(true);
  });
});

describe("the provider and usage views", () => {
  it("provider: the growing totals, three figures, bars by tokens out, the table", async () => {
    mount({ "/account": ACCT, "/metrics/series": SERIES({ is_consumer: false }) });
    await ready();
    await waitFor(() => expect(visible("provTotals")).toBe(true));
    expect(text("provReq")).toBe(RogerFmt.count(15));
    expect(text("provTokOut")).toBe(RogerFmt.count(2610));
    expect(text("provEarn")).toBe("$1.75");
    for (const id of ["provEarnTs", "provTokTs", "provReqTs"]) expect(visible(id), id).toBe(true);
    // a single-value figure: area + line + a dot per bucket; a stacked one: two areas, no line
    expect(el("provEarnPlot").querySelectorAll("path.mx-ts__area").length).toBe(1);
    expect(el("provEarnPlot").querySelectorAll("path.mx-ts__line").length).toBe(1);
    expect(el("provEarnPlot").querySelectorAll("circle.mx-ts__dot").length).toBe(3);
    expect(el("provTokPlot").querySelectorAll("path.mx-ts__area").length).toBe(2);
    expect(el("provTokPlot").querySelectorAll("path.mx-ts__line").length).toBe(0);
    expect(el("provTokPlot").querySelector("svg").getAttribute("aria-label")).toBe("Tokens served per day, in and out stacked");
    expect(el("provTokTs").querySelector(".mx-legend")).toBeTruthy();
    expect(el("provEarnTs").querySelector(".mx-legend")).toBe(null);
    const bars = [...document.querySelectorAll("#provBars .mx-bars__row")];
    expect(bars.map((b) => b.querySelector(".mx-bars__label").textContent)).toEqual(["gpt-oss-120b", "qwen"]);   // 2010 vs 600 tokens out
    expect(bars[0].querySelector(".mx-bars__fill").style.width).toBe("100%");
    expect(bars[1].querySelector(".mx-bars__fill").style.width).toBe((600 / 2010) * 100 + "%");
    expect(bars[1].querySelector(".mx-bars__val").textContent).toBe(RogerFmt.count(600));
    const rows = [...document.querySelectorAll("#provRows tr")].map((r) => [...r.children].map((c) => c.textContent));
    expect(rows).toEqual([["gpt-oss-120b", RogerFmt.count(11), RogerFmt.count(2010), "$0.25"], ["qwen", RogerFmt.count(4), RogerFmt.count(600), "$1.50"]]);
    expect(visible("provEmpty")).toBe(false);
    expect(visible("usage")).toBe(false);
  });
  it("usage: the totals, bars by total tokens, the table, and the private history with its routing split", async () => {
    mount({ "/account": ACCT, "/metrics/series": SERIES({ is_provider: false }), "/usage": [200, { recent: [
      { model: "gpt-5", curated_provider: "openrouter", curated_list: 0.1, cost: 0.12, prompt_tokens: 100, completion_tokens: 50 },
      { model: "qwen", node: "K6ABCDEFGHIJKL", cost: 0.02, prompt_tokens: 10, completion_tokens: 5 },
      { curated_provider: "together", curated_list: 0.5, cost: 0.4 },
    ] }] });
    await ready();
    await waitFor(() => expect(visible("useTotals")).toBe(true));
    expect(text("useReq")).toBe(RogerFmt.count(15));
    expect(text("useTok")).toBe(RogerFmt.count(4060));
    expect(text("useSpend")).toBe("$0.71");
    const bars = [...document.querySelectorAll("#useBars .mx-bars__row")];
    expect(bars.map((b) => b.querySelector(".mx-bars__label").textContent)).toEqual(["gpt-oss-120b", "qwen"]);   // 3060 vs 1000 tokens
    expect(bars[1].querySelector(".mx-bars__fill").style.width).toBe((1000 / 3060) * 100 + "%");
    const rows = [...document.querySelectorAll("#useRows tr")].map((r) => [...r.children].map((c) => c.textContent));
    expect(rows).toEqual([["gpt-oss-120b", RogerFmt.count(11), RogerFmt.count(3060), "$0.51"], ["qwen", RogerFmt.count(4), RogerFmt.count(1000), "$0.20"]]);
    await waitFor(() => expect(visible("useRecentWrap")).toBe(true));
    const recent = [...document.querySelectorAll("#useRecentRows tr")].map((r) => [...r.children].map((c) => c.textContent));
    expect(recent).toEqual([
      ["gpt-5", "» openrouter curated · $0.10 upstream + $0.02 routing", RogerFmt.count(150), "$0.12"],
      ["qwen", "station K6ABCDEFGH", RogerFmt.count(15), "$0.02"],
      ["-", "» together curated · $0.50 upstream + $0.00 routing", RogerFmt.count(0), "$0.40"],   // a fee never reads negative
    ]);
  });
  it("no recent requests, or a refused history: the history stays hidden and the aggregate view stands", async () => {
    const p = mount({ "/account": ACCT, "/metrics/series": SERIES(), "/usage": [200, { recent: [] }] });
    await ready();
    await waitFor(() => expect(visible("useTotals")).toBe(true));
    expect(visible("useRecentWrap")).toBe(false);
    p.unmount();
    mount({ "/account": ACCT, "/metrics/series": SERIES(), "/usage": [500, {}] });
    await ready();
    await waitFor(() => expect(visible("useTotals")).toBe(true));
    expect(visible("useRecentWrap")).toBe(false);
  });
  it("a bucket slice without a model name rolls up as (unknown)", async () => {
    mount({ "/account": ACCT, "/metrics/series": SERIES({ is_consumer: false, daily: [{ bucket: "2026-10-01", requests: 2, tokens_out: 5, models: [{ requests: 2, tokens_out: 5 }] }] }) });
    await ready();
    await waitFor(() => expect(visible("provTableWrap")).toBe(true));
    expect(document.querySelector("#provRows .mx-model").textContent).toBe("(unknown)");
  });
});

describe("the crosshair", () => {
  async function chart() {
    mount({ "/account": ACCT, "/metrics/series": SERIES({ is_consumer: false }) });
    await ready();
    await waitFor(() => expect(visible("provEarnTs")).toBe(true));
    return el("provEarnPlot").querySelector("svg");
  }
  const cursor = () => el("provEarnPlot").querySelector(".mx-ts__cursor").getAttribute("visibility");
  it("keyboard: focus lands on the last bucket, the arrows walk, Home/End jump, Escape clears; the readout names the bucket", async () => {
    const svg = await chart();
    expect(cursor()).toBe("hidden");
    expect(text("provEarnRead")).toBe("");
    await fireEvent.focus(svg);
    expect(cursor()).toBe("visible");
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 3");
    expect(el("provEarnRead").querySelector(".mx-ts__rv--money").textContent).toBe("$0.25");
    await fireEvent.keyDown(svg, { key: "ArrowLeft" });
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 2");
    expect(el("provEarnRead").querySelector(".mx-ts__rv--money").textContent).toBe("$1.50");
    await fireEvent.keyDown(svg, { key: "Home" });
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 1");
    await fireEvent.keyDown(svg, { key: "ArrowLeft" });   // stays at the first
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 1");
    await fireEvent.keyDown(svg, { key: "End" });
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 3");
    await fireEvent.keyDown(svg, { key: "ArrowRight" });   // stays at the last
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 3");
    await fireEvent.keyDown(svg, { key: "Escape" });
    expect(cursor()).toBe("hidden");
    expect(text("provEarnRead")).toBe("");
    await fireEvent.blur(svg);
    expect(cursor()).toBe("hidden");
  });
  it("pointer: the nearest bucket under the pointer, cleared on leave; a stacked readout lists in and out", async () => {
    const svg = await chart();
    svg.getBoundingClientRect = () => ({ left: 100, width: 640, top: 0, height: 168 });
    await fireEvent.pointerMove(svg, { clientX: 100 + 8 + 2 });   // near the first bucket
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 1");
    await fireEvent.pointerMove(svg, { clientX: 100 + 320 });    // the middle
    expect(el("provEarnRead").querySelector(".mx-ts__rk").textContent).toBe("Oct 2");
    const line = el("provEarnPlot").querySelector(".mx-ts__cursor");
    expect(line.getAttribute("x1")).toBe(String(8 + 624 / 2));
    await fireEvent.pointerLeave(svg);
    expect(cursor()).toBe("hidden");
    const tok = el("provTokPlot").querySelector("svg");
    await fireEvent.focus(tok);
    expect([...el("provTokRead").querySelectorAll(".mx-ts__rv")].map((b) => b.textContent)).toEqual(["in 50", "out 10"]);
  });
});

describe("series.js and rollup.js: the geometry and the rollups", () => {
  it("points and anyValue: labels from the bucket day, zeros for missing values, honest empty", () => {
    const pts = points([{ bucket: "2026-06-21", spend: 1 }, { bucket: "x" }], [{ key: "spend", cls: "money" }]);
    expect(pts.map((p) => p.label)).toEqual(["Jun 21", "x"]);
    expect(pts[1].series.spend).toBe(0);
    expect(anyValue([{ spend: 0 }, { spend: 0 }], [{ key: "spend" }])).toBe(false);
    expect(anyValue([{ spend: 0 }, { spend: 0.01 }], [{ key: "spend" }])).toBe(true);
  });
  it("geometry: a single bucket sits centered; a flat-zero range still draws a baseline; ticks are sparse", () => {
    const one = geometry(points([{ bucket: "2026-01-01", v: 0 }], [{ key: "v", cls: "req" }]), [{ key: "v", cls: "req" }], false);
    expect(one.n).toBe(1);
    expect(one.xAt(0)).toBe(8 + 624 / 2);
    expect(one.layers[0].dots[0].cy).toBe(12 + 134);   // zero sits on the baseline
    expect(one.layers[0].r).toBe(2.2);
    expect(one.ticks.map((t) => t.anchor)).toEqual(["start"]);
    const many = (n) => geometry(points(Array.from({ length: n }, (_, i) => ({ bucket: "d" + i, v: i })), [{ key: "v" }]), [{ key: "v", cls: "req" }], false);
    expect(many(2).ticks.map((t) => t.anchor)).toEqual(["start", "end"]);
    expect(many(7).ticks.length).toBe(3);
    expect(many(13).ticks.length).toBe(5);
    expect(many(13).ticks.map((t) => t.anchor)).toEqual(["start", "middle", "middle", "middle", "end"]);
    expect(many(20).layers[0].r).toBe(1.6);
    expect(many(20).viewBox).toBe(`0 0 ${W} ${H}`);
  });
  it("geometry: stacked areas ride on the series below; the focus follows the stacked top", () => {
    const series = [{ key: "a", cls: "in" }, { key: "b", cls: "out" }];
    const g = geometry(points([{ bucket: "1", a: 1, b: 1 }, { bucket: "2", a: 2, b: 2 }], series), series, true);
    expect(g.layers.length).toBe(2);
    expect(g.layers[0].line).toBe(undefined);
    expect(g.layers[0].area.endsWith("Z")).toBe(true);
    expect(g.topAt(1)).toBe(12);   // 4 of max 4: the top of the plot
    expect(g.topAt(0)).toBe(12 + 134 / 2);
    expect(nearest(g, 0)).toBe(0);
    expect(nearest(g, W)).toBe(1);
  });
  it("a provider tie on tokens out keeps the order the models were first seen in", async () => {
    mount({ "/account": ACCT, "/metrics/series": SERIES({ is_consumer: false, daily: [
      { bucket: "2026-10-01", requests: 2, tokens_out: 5, models: [{ model: "zeta", requests: 1, tokens_in: 1, tokens_out: 5 }, { model: "alpha", requests: 1, tokens_in: 900, tokens_out: 5 }] },
    ] }) });
    await ready();
    await waitFor(() => expect(visible("provTableWrap")).toBe(true));
    expect([...document.querySelectorAll("#provRows .mx-model")].map((c) => c.textContent)).toEqual(["zeta", "alpha"]);
  });
  it("a non-day bucket is labelled as it came, never as a day", () => {
    expect(points([{ bucket: "2026-10-03T12", spend: 1 }], [{ key: "spend" }])[0].label).toBe("2026-10-03T12");
  });
  it("rollups: models summed over the window biggest first, totals across it", () => {
    const models = rollupModels(DAILY, "(unknown)");
    expect(models.map((m) => [m.model, m.requests, m.tokens, m.spend, m.earned])).toEqual([["gpt-oss-120b", 11, 3060, 0.51, 0.25], ["qwen", 4, 1000, 0.2, 1.5]]);
    expect(rollupModels([{ models: [{ requests: 1 }] }], "(unknown)")[0].model).toBe("(unknown)");
    expect(rollupModels([{ models: [{ requests: 1 }] }])[0].model).toBe("unknown");
    expect(rollupModels(DAILY, "(unknown)", false).map((m) => m.model)).toEqual(["gpt-oss-120b", "qwen"]);   // first seen, unsorted
    expect(rollupTotals(DAILY)).toEqual({ requests: 15, tokens_in: 1450, tokens_out: 2610, spend: 0.71, earned: 1.75 });
    expect(rollupTotals(null)).toEqual({ requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 });
  });
});
