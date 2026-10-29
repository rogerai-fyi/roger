// /keys.html (lib/keys/KeysApp.svelte): the legacy keys.js and account-keys.js contracts from
// test/keys-states.test.mjs and the mount tests of test/account-keys.test.mjs, plus the grant
// mint flow. The request layer is lib/keys/accountKeys.js, the module the Go probe runs.
import { describe, it, expect, vi } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import KeysApp from "#lib/keys/KeysApp.svelte";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const ACCT = [200, { github_login: "me@rogerai.fm", github_id: 0, provider: "email", operator: false }];
const KEY = { id: "key_1", name: "ci", hint: "...abcd", limit_usd: 5, reset: "weekly", usage_weekly: 1.25, expires_at: null, disabled: false, expired: false };
const writes = (calls) => calls.filter((c) => c.opts.method && c.opts.method !== "GET");
const ready = () => waitFor(() => expect(el("card").hidden).toBe(false));

describe("gate and grant-key states", () => {
  it("signed out: the gate card, not the keys card, and nothing minted", async () => {
    const calls = broker({ "/account": [401, {}] });
    fakeLocation({ pathname: "/keys.html" });
    render(KeysApp);
    await waitFor(() => expect(visible("gate")).toBe(true));
    expect(el("card").hidden).toBe(true);
    expect(calls.map((c) => c.path)).toEqual(["/account"]);
  });
  it("403 on /grants: explains keys belong to an account with a machine on air, no mint form", async () => {
    broker({ "/account": ACCT, "/grants": [403, {}], "/account/keys": [200, { keys: [] }] });
    const navs = fakeLocation({ pathname: "/keys.html" });
    render(KeysApp);
    await ready();
    await waitFor(() => expect(visible("keysNoOperator")).toBe(true));
    expect(visible("keysError")).toBe(false);
    expect(el("createForm").hidden).toBe(true);
    expect(navs).toEqual([]);
  });
  it("200 with no keys: the normal empty state, the form usable; 500: a real fault is an error", async () => {
    broker({ "/account": ACCT, "/grants": [200, { grants: [] }], "/account/keys": [200, { keys: [] }] });
    fakeLocation({ pathname: "/keys.html" });
    const { unmount } = render(KeysApp);
    await ready();
    await waitFor(() => expect(visible("keysEmpty")).toBe(true));
    expect(visible("keysNoOperator")).toBe(false);
    expect(el("createForm").hidden).toBe(false);
    unmount();
    broker({ "/account": ACCT, "/grants": [500, {}], "/account/keys": [200, { keys: [] }] });
    render(KeysApp);
    await ready();
    await waitFor(() => expect(visible("keysError")).toBe(true));
    expect(visible("keysNoOperator")).toBe(false);
  });
  it("grant keys list active first with their labels; caps edit and revoke go through the id", async () => {
    const calls = broker({ "/account": ACCT, "/account/keys": [200, { keys: [] }],
      "/grants": () => [200, { grants: [{ id: "g2", name: "old", status: "revoked", free: true }, { id: "g1", name: "bots", status: "active", free: true, daily_cap: 2000000, expires_at: 0, usage: { day_tokens: 1500, month_tokens: 40000 } }] }],
      "/grants/g1": [200, {}] });
    fakeLocation({ pathname: "/keys.html" });
    const prompt = vi.fn().mockReturnValueOnce("1000").mockReturnValueOnce("abc");
    render(KeysApp, { props: { ui: { prompt, confirm: () => true, alert: vi.fn() } } });
    await ready();
    await waitFor(() => expect(visible("keysWrap")).toBe(true));
    const rows = document.querySelectorAll("#keysRows tr");
    expect(rows[0].querySelector(".mx-model").textContent).toBe("bots");
    expect(rows[0].textContent).toMatch(/free.*never.*2,000,000.*1,500 \/ 40,000.*active/s);
    expect(rows[1].className).toBe("kf__row--off");
    expect(rows[1].querySelector(".kf__act").textContent).toBe("-");
    await fireEvent.click(rows[0].querySelector("button[title='Edit caps']"));
    await waitFor(() => expect(writes(calls).at(-1)).toMatchObject({ path: "/grants/g1", opts: { method: "PATCH" }, body: { daily_cap: 1000, monthly_cap: 0 } }));
    await fireEvent.click(rows[0].querySelector(".kf__rowbtn--danger"));
    await waitFor(() => expect(writes(calls).at(-1)).toMatchObject({ path: "/grants/g1", opts: { method: "DELETE" } }));
  });
  it("minting a grant key: a name is required; the defaults are free, self off, a daily cap; the secret is revealed once with its env lines", async () => {
    const calls = broker({ "/account": ACCT, "/account/keys": [200, { keys: [] }], "/grants": () => [200, { grants: [] }] });
    const base = globalThis.fetch;
    globalThis.fetch = (url, opts = {}) => (String(url).endsWith("/grants") && opts.method === "POST")
      ? (calls.push({ path: "/grants", opts, body: JSON.parse(opts.body) }), Promise.resolve({ ok: true, status: 200, json: async () => ({ ok: true, secret: "rog-grant_S3CRET", openai_api_base: "https://broker.rogerai.fm/v1" }) }))
      : base(url, opts);
    fakeLocation({ pathname: "/keys.html" });
    render(KeysApp);
    await ready();
    await fireEvent.submit(el("createForm"));
    await waitFor(() => expect(el("createErr").textContent).toBe("Name is required."));
    expect(writes(calls)).toEqual([]);
    await fireEvent.input(el("kName"), { target: { value: " petlings-bot " } });
    await fireEvent.submit(el("createForm"));
    await waitFor(() => expect(visible("reveal")).toBe(true));
    expect(writes(calls)[0].body).toEqual({ name: "petlings-bot", free: true, self: false, price_out: 0, daily_cap: 2000000, monthly_cap: 0, expires_at: 0, nodes: [], models: [] });
    expect(el("secretVal").textContent).toBe("rog-grant_S3CRET");
    expect(el("envLines").textContent).toBe("OPENAI_API_BASE=https://broker.rogerai.fm/v1\nOPENAI_API_KEY=rog-grant_S3CRET");
    expect(document.body.textContent.split("rog-grant_S3CRET").length - 1).toBe(2);   // the code and the env line, nowhere else
    expect(el("kName").value).toBe("");   // the form is reset
  });
});

describe("account keys (the shared module, wired)", () => {
  const mounted = async (responses = {}, ui = {}) => {
    const calls = broker({ "/account": ACCT, "/grants": [200, { grants: [] }], "/account/keys": () => [200, { keys: [KEY], ...responses }] });
    fakeLocation({ pathname: "/keys.html" });
    const r = render(KeysApp, { props: { ui: { prompt: () => null, confirm: () => true, alert: () => {}, ...ui } } });
    await ready();
    await waitFor(() => expect(visible("akWrap")).toBe(true));
    calls.length = 0;   // the first list
    return { calls, ...r };
  };
  it("lists the keys as rows and names each field", async () => {
    await mounted();
    const row = document.querySelector("#akRows tr");
    expect(row.textContent).toMatch(/key_1.*ci.*\.\.\.abcd.*\$5\.00.*\$1\.25.*weekly.*never.*active/s);
    expect(row.querySelectorAll("button[data-act]").length).toBe(3);
  });
  it("a mint's limit is parsed strictly; a typo is refused and nothing is sent; an empty limit sends none", async () => {
    // the field is type=number: "abc" and "$5" cannot reach it (the browser blanks them); they
    // are refused on the prompt path below and in the module's own parseLimit tests
    for (const [typed, want] of [["-1", null], ["", "none"], ["2.5", 2.5], ["0", 0]]) {
      const { calls, unmount } = await mounted({ secret: "rog-key_NEW", ...KEY });
      await fireEvent.input(el("akName"), { target: { value: "ci" } });
      await fireEvent.input(el("akLimit"), { target: { value: typed } });
      await fireEvent.submit(el("akForm"));
      if (want === null) {
        await waitFor(() => expect(el("akErr").hidden).toBe(false));
        expect(el("akErr").textContent).toMatch(/limit/i);
        expect(writes(calls).length).toBe(0);
      } else {
        await waitFor(() => expect(writes(calls).length).toBeGreaterThan(0));
        const body = writes(calls)[0].body;
        if (want === "none") expect("limit_usd" in body).toBe(false);
        else expect(body.limit_usd).toBe(want);
        expect(writes(calls)[0].opts).toMatchObject({ method: "POST", credentials: "include" });
      }
      unmount();
    }
  });
  it("editing a limit parses strictly; an empty answer or Cancel changes nothing", async () => {
    for (const [typed, want] of [["abc", null], ["$5", null], ["-1", null], ["", "cancel"], [null, "cancel"], ["2.5", 2.5], ["0", 0]]) {
      const { calls, unmount } = await mounted({}, { prompt: () => typed });
      await fireEvent.click(document.querySelector('#akRows button[data-act="limit"]'));
      if (want === null || want === "cancel") {
        await new Promise((r) => setTimeout(r, 10));
        expect(writes(calls).length).toBe(0);
        expect(el("akErr").hidden).toBe(want === "cancel");
      } else {
        await waitFor(() => expect(writes(calls).length).toBe(1));
        expect(writes(calls)[0]).toMatchObject({ path: "/account/keys/key_1", opts: { method: "PATCH" }, body: { limit_usd: want } });
      }
      unmount();
    }
  });
  it("toggle and revoke send their request; a declined revoke sends nothing", async () => {
    let m = await mounted({}, { confirm: () => false });
    await fireEvent.click(document.querySelector('#akRows button[data-act="revoke"]'));
    await new Promise((r) => setTimeout(r, 10));
    expect(writes(m.calls).length).toBe(0);
    m.unmount();
    m = await mounted({}, { confirm: () => true });
    await fireEvent.click(document.querySelector('#akRows button[data-act="revoke"]'));
    await waitFor(() => expect(writes(m.calls)[0]).toMatchObject({ path: "/account/keys/key_1", opts: { method: "DELETE" } }));
    m.unmount();
    m = await mounted();
    await fireEvent.click(document.querySelector('#akRows button[data-act="toggle"]'));
    await waitFor(() => expect(writes(m.calls)[0].body).toEqual({ disabled: true }));
  });
  it("the reveal shows the minted secret once with its copy control and warning, and Done clears it", async () => {
    await mounted({ secret: "rog-key_NEW", ...KEY });
    await fireEvent.input(el("akName"), { target: { value: "ci" } });
    await fireEvent.submit(el("akForm"));
    await waitFor(() => expect(visible("akReveal")).toBe(true));
    const reveal = el("akReveal");
    expect(reveal.innerHTML.split("rog-key_NEW").length - 1).toBe(1);
    expect(reveal.querySelector(".kf__copy")).toBeTruthy();
    expect(reveal.textContent.toLowerCase()).toMatch(/never shown again/);
    await fireEvent.click(reveal.querySelector("[data-reveal-done]"));
    await waitFor(() => expect(visible("akReveal")).toBe(false));
    expect(reveal.innerHTML).toBe("");
  });
});
