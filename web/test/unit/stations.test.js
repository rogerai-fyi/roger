// /stations.html (lib/account/StationsApp.svelte): the legacy stations.js contracts from
// test/stations-states.test.mjs and test/stations-appeal.test.mjs. The page tells the truth
// about WHY it has nothing to show, and the appeal form never claims success on a failure.
import { describe, it, expect } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import StationsApp from "#lib/account/StationsApp.svelte";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const ACCT = [200, { github_login: "me@rogerai.fm", github_id: 0 }];
const WITH_STRIKE = [200, { github_login: "octocat", stations: [], strikes: [{ kind: "flagged", evidence: "e", created_at: 1 }] }];
const posts = (calls) => calls.filter((c) => c.path === "/owner/appeal" && c.opts.method === "POST").map((c) => c.body);
const ready = () => waitFor(() => expect(el("card").hidden).toBe(false));

describe("states", () => {
  it("403 no operator account: explains it, never claims a load failure, names the identity", async () => {
    broker({ "/stations": [403, {}], "/account": ACCT });
    const navs = fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    await waitFor(() => expect(el("who").textContent).toBe("me@rogerai.fm"));
    expect(visible("stNoOperator")).toBe(true);
    expect(visible("stError")).toBe(false);
    expect(navs).toEqual([]);
  });
  it("200 with no stations: the 'No stations yet' state", async () => {
    broker({ "/stations": [200, { github_login: "octocat", stations: [], strikes: [] }] });
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    expect(visible("stEmpty")).toBe(true);
    expect(visible("stNoOperator")).toBe(false);
    expect(visible("stAppeal")).toBe(false);
    expect(el("who").textContent).toBe("@octocat");
  });
  for (const status of [500, 502, 503]) {
    it(`${status}: a real fault is an error with a retry hint`, async () => {
      broker({ "/stations": [status, {}] });
      fakeLocation({ pathname: "/stations.html" });
      render(StationsApp);
      await ready();
      expect(visible("stError")).toBe(true);
      expect(visible("stNoOperator")).toBe(false);
    });
  }
  it("401: not signed in goes to login and comes back here", async () => {
    broker({ "/stations": [401, {}] });
    const navs = fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await waitFor(() => expect(navs).toEqual(["/login.html?next=%2Fstations.html"]));
  });
  it("a network failure is an error, not 'no operator account'", async () => {
    globalThis.fetch = async () => { throw new Error("offline"); };
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    expect(visible("stError")).toBe(true);
    expect(visible("stNoOperator")).toBe(false);
  });
  it("stations render with their tiles, badges, offers and the chain as an audit signal", async () => {
    const now = Math.floor(Date.now() / 1000);
    broker({ "/stations": [200, { github_login: "octocat", strikes: [], stations: [
      { node_id: "K6ABC", on_air: true, registered_at: now - 86400 * 3, last_seen: now - 30, region: "us-west", hw: "RTX 4090", earnings: 1.5, recent_served: 12, chain_state: "breaks-recorded", chain: { breaks: 2 },
        offers: [{ model: "gpt-oss-120b", price_in: 0.1, price_out: 0.4, ctx: 32768 }] },
      { node_id: "K6CUR", on_air: false, curated: true, curated_provider: "openrouter", earnings: 0.25, recent_served: 3, chain_state: "continuous", offers: [] },
    ] }] });
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    expect(el("tileCount").textContent).toBe("2");
    expect(el("tileOnAir").textContent).toBe("1 of 2");
    expect(el("tileEarned").textContent).toBe("$1.75");
    expect(el("tileServed").textContent).toBe("15");
    const rows = document.querySelectorAll("#stRows .st-row");
    expect(rows[0].querySelector("h3").textContent).toMatch(/K6ABC\s+ON AIR/);
    expect(rows[0].querySelector("p.fine").textContent).toMatch(/^Registered 3d ago · last seen \d+s ago · us-west · RTX 4090$/);   // a space on both sides of every separator (the seconds race the clock)
    expect(rows[0].textContent).toMatch(/gpt-oss-120b · \$0\.10 in \/ \$0\.40 out per 1M · 32,768 ctx/);
    expect(rows[0].textContent).toMatch(/Earned \$1\.50 · 12 recent requests · 2 breaks recorded - audit signal only/);
    expect(rows[0].querySelector("span.warn")).toBeTruthy();
    expect(rows[1].querySelector("h3").textContent).toMatch(/» CURATED · openrouter/);
    expect(rows[1].textContent).toMatch(/Curated credits \$0\.25 \(your provider list reimbursed \+ your half of the routing fee\)/);
    expect(rows[1].textContent).toMatch(/No offers published/);
    expect(visible("stChainHelp")).toBe(true);
  });
});

describe("appeals", () => {
  it("the appeal form is shown with the evidence, and not for an account with none", async () => {
    broker({ "/stations": WITH_STRIKE, "/owner/appeal": [200, { appeals: [] }] });
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    expect(visible("stEvidence")).toBe(true);
    expect(visible("stAppeal")).toBe(true);
    expect(document.querySelector("#stStrikes pre").textContent).toBe("e");
  });
  it("submitting posts the trimmed reason (never an account id) and shows the appeal id", async () => {
    const calls = broker({ "/stations": WITH_STRIKE, "/owner/appeal": () => [200, { ok: true, appeal_id: 42, state: "open", appeals: [] }] });
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    await fireEvent.input(el("appealReason"), { target: { value: "  The flagged request was my own test.  " } });
    await fireEvent.click(el("appealSend"));
    await waitFor(() => expect(el("appealMsg").textContent).toMatch(/42/));
    expect(posts(calls)).toEqual([{ reason: "The flagged request was my own test." }]);
    expect(el("appealMsg").textContent).toMatch(/review/i);
  });
  it("an empty reason is not sent", async () => {
    const calls = broker({ "/stations": WITH_STRIKE, "/owner/appeal": [200, { appeals: [] }] });
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    await fireEvent.input(el("appealReason"), { target: { value: "   " } });
    await fireEvent.click(el("appealSend"));
    await waitFor(() => expect(el("appealMsg").textContent).toMatch(/reason/i));
    expect(posts(calls)).toEqual([]);
  });
  it("a refusal shows the broker's message, never claims it was filed, and re-enables the button", async () => {
    broker({ "/stations": WITH_STRIKE, "/owner/appeal": () => [403, { error: { message: "no operator account for this login (run `roger login` on a node first)" }, appeals: [] }] });
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    await fireEvent.input(el("appealReason"), { target: { value: "mistake" } });
    await fireEvent.click(el("appealSend"));
    await waitFor(() => expect(el("appealMsg").textContent).toMatch(/roger login/));
    expect(el("appealMsg").textContent).not.toMatch(/filed|appeal id/i);
    expect(el("appealSend").disabled).toBe(false);
  });
  it("the send button is disabled in flight and after success, so a double-click files one appeal", async () => {
    const calls = broker({ "/stations": WITH_STRIKE, "/owner/appeal": () => [200, { ok: true, appeal_id: 7, appeals: [] }] });
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    await fireEvent.input(el("appealReason"), { target: { value: "mistake" } });
    await Promise.all([fireEvent.click(el("appealSend")), fireEvent.click(el("appealSend"))]);
    await waitFor(() => expect(el("appealMsg").textContent).toMatch(/#7/));
    expect(posts(calls).length).toBe(1);
    expect(el("appealSend").disabled).toBe(true);
  });
  it("existing appeals are listed on load; an expired session on the POST says to sign in again", async () => {
    broker({ "/stations": WITH_STRIKE, "/owner/appeal": () => [200, { appeals: [{ id: 5, state: "open", reason: "mine", created_at: 1 }], count: 1 }] });
    fakeLocation({ pathname: "/stations.html" });
    const { unmount } = render(StationsApp);
    await ready();
    await waitFor(() => expect(visible("appealHistory")).toBe(true));
    expect(el("appealHistory").textContent).toMatch(/#5 · open · mine/);
    unmount();
    broker({ "/stations": WITH_STRIKE, "/owner/appeal": () => [401, { error: { message: "not logged in - run `roger login` to link GitHub" }, appeals: [] }] });
    render(StationsApp);
    await ready();
    await fireEvent.input(el("appealReason"), { target: { value: "mistake" } });
    await fireEvent.click(el("appealSend"));
    await waitFor(() => expect(el("appealMsg").textContent).toMatch(/sign in again/i));
    expect(el("appealMsg").textContent).not.toMatch(/roger login/);
  });
  it("a newly filed appeal appears in the list without a reload", async () => {
    let reads = 0;
    globalThis.fetch = async (url, opts = {}) => {
      const path = String(url).replace("https://broker.rogerai.fm", "");
      if (path === "/stations") return { ok: true, status: 200, json: async () => WITH_STRIKE[1] };
      if (path === "/owner/appeal" && opts.method === "POST") return { ok: true, status: 200, json: async () => ({ ok: true, appeal_id: 9, state: "open" }) };
      if (path === "/owner/appeal") return { ok: true, status: 200, json: async () => ({ appeals: reads++ === 0 ? [] : [{ id: 9, state: "open", reason: "mistake" }] }) };
      return { ok: false, status: 404, json: async () => ({}) };
    };
    fakeLocation({ pathname: "/stations.html" });
    render(StationsApp);
    await ready();
    await fireEvent.input(el("appealReason"), { target: { value: "mistake" } });
    await fireEvent.click(el("appealSend"));
    await waitFor(() => expect(el("appealHistory").textContent).toMatch(/#9/));
  });
});
