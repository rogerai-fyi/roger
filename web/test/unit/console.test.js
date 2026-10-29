// The console page's boot contract, against a fake broker (fetch stubbed per route). These
// are the scenarios web/test/login-loop.test.mjs and dashboard-states.test.mjs pin for the
// legacy page, re-expressed for the component:
//   - a failed /account is the ONLY thing that redirects to login
//   - a /console error with a confirmed session shows the fault state, never redirects
//   - consumer and owner empty states, counters and the lineage table
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/svelte";
import Console from "#lib/console/ConsoleApp.svelte";

function broker(routes) {
  const calls = [];
  globalThis.fetch = vi.fn(async (url, opts) => {
    const path = String(url).replace("https://broker.rogerai.fm", "");
    calls.push({ path, opts });
    const hit = Object.entries(routes).find(([k]) => path.startsWith(k));
    const [status, body] = hit ? hit[1] : [404, {}];
    return { ok: status >= 200 && status < 300, status, json: async () => body };
  });
  return calls;
}
let replaced;
beforeEach(() => {
  replaced = [];
  Object.defineProperty(window, "location", { configurable: true, value: { replace: (u) => replaced.push(u), pathname: "/console.html", search: "" } });
});

describe("console boot", () => {
  it("logged out: /account fails -> login, nothing else fetched", async () => {
    const calls = broker({ "/account": [401, {}] });
    render(Console);
    await waitFor(() => expect(replaced).toEqual(["/login.html"]));
    expect(calls.map((c) => c.path)).toEqual(["/account"]);
    expect(calls[0].opts.credentials).toBe("include");
  });

  // web/test/login-loop.test.mjs pinned these for the legacy script: a 401 or 403 from the
  // feed with a confirmed session is an error to show, never a bounce to login.
  for (const status of [401, 403]) {
    it(`signed in but /console ${status}: fault state shown, NO redirect (the login loop rule)`, async () => {
      broker({ "/account": [200, { github_login: "someone@rogerai.fm", github_id: 0 }], "/console": [status, {}] });
      render(Console);
      await waitFor(() => expect(document.getElementById("cnError")).toBeTruthy());
      expect(replaced).toEqual([]);
      expect(document.getElementById("card").hidden).toBe(false);
      expect(document.getElementById("who").textContent).toBe("someone@rogerai.fm");   // an address is shown as the address
    });
  }

  it("a script error after the session is confirmed shows the error inside the card, never a redirect", async () => {
    // the feed answers 200 with a body that cannot be read: the same shape as a render bug
    globalThis.fetch = vi.fn(async (url) => {
      const path = String(url).replace("https://broker.rogerai.fm", "");
      if (path === "/account") return { ok: true, status: 200, json: async () => ({ github_login: "ada" }) };
      return { ok: true, status: 200, json: async () => { throw new Error("boom"); } };
    });
    render(Console);
    await waitFor(() => expect(document.getElementById("cnError")).toBeTruthy());
    expect(replaced).toEqual([]);
    expect(document.getElementById("card").hidden).toBe(false);
  });

  it("signed in but /console 500: fault state shown, NO redirect (the login loop rule)", async () => {
    broker({ "/account": [200, { github_login: "ada" }], "/console": [500, {}] });
    render(Console);
    await waitFor(() => expect(screen.getByText("Could not reach the console. Try again shortly.")).toBeTruthy());
    expect(replaced).toEqual([]);
    expect(screen.getByText("@ada")).toBeTruthy();
    expect(document.getElementById("card").hidden).toBe(false);
  });

  it("consumer with no events: spend tile + consumer empty state, no owner tiles", async () => {
    broker({ "/account": [200, { github_login: "ada" }],
             "/console": [200, { role: "consumer", events: [], counters: { requests_today: 0, spend_today: 0 } }] });
    render(Console);
    await waitFor(() => expect(document.getElementById("logEmptyConsumer")).toBeTruthy());
    expect(document.getElementById("ctrMoneyK").textContent).toBe("Spend today");
    expect(document.getElementById("ctrMoney").textContent).toBe("$0.00");
    expect(document.getElementById("tileNodes")).toBeNull();
    expect(document.getElementById("logWrap")).toBeNull();
    expect(document.getElementById("role").textContent).toBe("Consumer console");
  });

  it("owner with events: earned column, node/band tiles, one row per receipt, models panel", async () => {
    const calls = broker({
      "/account": [200, { github_login: "ada" }],
      "/console": [200, { role: "owner", counters: { requests_today: 12345, earned_today: 1.25, active_nodes: 2, active_bands: 3 },
        events: [{ request_id: "req_0123456789abcdef", ts: Math.floor(Date.now() / 1000) - 30, model: "gpt-oss-120b", node: "K6ABC", tokens_in: 1000, tokens_out: 2500, earned: 0.004, success: true },
                 { request_id: "req_fail", ts: Math.floor(Date.now() / 1000) - 7200, model: "qwen3", node: "K6XYZ", tokens_in: 10, tokens_out: 0, earned: 0, success: false }] }],
      "/provider/models": [200, { models: [], ceiling_in: 50, ceiling_out: 100 }],
    });
    render(Console);
    await waitFor(() => expect(document.querySelectorAll("tr.cn-row").length).toBe(2));
    expect(document.getElementById("colMoney").textContent).toBe("Earned");
    expect(document.getElementById("ctrNodes").textContent).toBe("2");
    expect(document.getElementById("ctrBands").textContent).toBe("3");
    expect(document.getElementById("ctrReq").textContent).toMatch(/^12(\.3)?k$/);     // compact, exact on hover
    expect(document.getElementById("ctrReq").getAttribute("data-exact")).toMatch(/12,345/);
    const rows = document.querySelectorAll("tr.cn-row");
    expect(rows[0].querySelector(".cn-id").textContent).toBe("req_01234567");
    expect(rows[0].querySelector(".cn-time").textContent).toBe("30s");
    expect(rows[1].querySelector(".cn-dot").textContent).toBe("○");
    await waitFor(() => expect(document.getElementById("mdEmpty")).toBeTruthy());
    expect(calls.map((c) => c.path)).toEqual(["/account", "/console", "/provider/models"]);
  });

  it("sign out posts /auth/logout then goes to login", async () => {
    const calls = broker({ "/account": [200, { github_login: "ada" }], "/console": [200, { role: "consumer", events: [], counters: {} }], "/auth/logout": [200, {}] });
    render(Console);
    await waitFor(() => expect(document.getElementById("logEmptyConsumer")).toBeTruthy());
    document.getElementById("logout").click();
    await waitFor(() => expect(replaced).toEqual(["/login.html"]));
    expect(calls.at(-1)).toMatchObject({ path: "/auth/logout", opts: { method: "POST", credentials: "include" } });
  });
});
