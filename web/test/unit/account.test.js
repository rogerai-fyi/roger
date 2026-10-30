// /account.html (lib/account/AccountApp.svelte): the legacy account.js contracts from
// test/account-identity.test.mjs, test/account-link.test.mjs and the account rows of
// test/boot-faults.test.mjs, as component tests over a fake broker.
import { describe, it, expect, vi } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import AccountApp from "#lib/account/AccountApp.svelte";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const A = (o) => [200, { balance: 5, connect: { status: "none" }, ...o }];
const GH = { github_login: "octocat", github_id: 7, provider: "github", operator: true };
const ready = () => waitFor(() => expect(el("card").hidden).toBe(false));

describe("boot (the login-loop rule)", () => {
  it("a 401 on the first feed goes to login", async () => {
    broker({ "/account": [401, {}] });
    const navs = fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await waitFor(() => expect(navs).toEqual(["/login.html"]));
  });
  for (const status of [500, 503]) {
    it(`a ${status} shows an error and keeps the card with a working sign-out, no redirect`, async () => {
      const calls = broker({ "/account": [status, {}], "/auth/logout": [200, {}] });
      const navs = fakeLocation({ pathname: "/account.html" });
      render(AccountApp);
      await ready();
      expect(el("pageError").hidden).toBe(false);
      expect(el("card").classList.contains("is-fault")).toBe(true);
      expect(navs).toEqual([]);
      await fireEvent.click(el("logout"));
      await waitFor(() => expect(navs).toEqual(["/login.html"]));
      expect(calls.at(-1)).toMatchObject({ path: "/auth/logout", opts: { method: "POST" } });
    });
  }
  it("a network failure shows the error, no redirect; a good feed shows none", async () => {
    globalThis.fetch = vi.fn(async () => { throw new Error("offline"); });
    const navs = fakeLocation({ pathname: "/account.html" });
    const { unmount } = render(AccountApp);
    await ready();
    expect(el("pageError").hidden).toBe(false);
    expect(navs).toEqual([]);
    unmount();
    broker({ "/account": A(GH) });
    render(AccountApp);
    await ready();
    expect(el("pageError").hidden).toBe(true);
    expect(el("card").classList.contains("is-fault")).toBe(false);
  });
});

describe("sign out", () => {
  it("a dropped connection on sign-out keeps the page; an answered request leaves for login", async () => {
    const p = (() => { const calls = broker({ "/account": [200, { github_login: "octocat", provider: "github", operator: true }] }); const navs = fakeLocation({ pathname: "/account.html" }); render(AccountApp); return { calls, navs }; })();
    await waitFor(() => expect(el("card").hidden).toBe(false));
    globalThis.fetch = async () => { throw new Error("offline"); };
    await fireEvent.click(el("logout"));
    await new Promise((r) => setTimeout(r, 20));
    expect(p.navs).toEqual([]);
    globalThis.fetch = async () => ({ ok: false, status: 500, json: async () => ({}) });   // answered, even badly: the cookie is gone or going
    await fireEvent.click(el("logout"));
    await waitFor(() => expect(p.navs).toEqual(["/login.html"]));
  });
});

describe("identity", () => {
  it("email sign-in: the address is the sign-in, shown read-only; no GitHub id row", async () => {
    broker({ "/account": A({ github_login: "me@rogerai.fm", provider: "email", email: "me@rogerai.fm", email_verified: true, operator: false }) });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    expect(el("handle").textContent).toBe("me@rogerai.fm");
    expect(el("signin").textContent).toBe("Email");
    expect(el("email").value).toBe("me@rogerai.fm");
    expect(el("email").readOnly).toBe(true);
    expect(el("saveEmail").hidden).toBe(true);
    expect(el("machines").textContent).toBe("None linked to this sign-in");
    expect(el("linkPanel").hidden).toBe(true);   // its address is already its sign-in
  });
  it("github operator: provider and id, machines linked, email editable", async () => {
    broker({ "/account": A({ ...GH, email: "o@x.com" }) });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    expect(el("handle").textContent).toBe("@octocat (GitHub)");
    expect(el("who").textContent).toBe("@octocat (GitHub)");
    expect(el("signin").textContent).toBe("GitHub");
    expect(el("machines").textContent).toBe("Linked");
    expect(el("email").readOnly).toBe(false);
    expect(el("balance").textContent).toBe("$5.00");
  });
  it("saving the contact email shows the broker's own reason when it cannot save", async () => {
    broker({ "/account": () => [200, { balance: 5, connect: { status: "none" }, github_login: "newbie", github_id: 99, provider: "github", operator: false }] });
    const base = globalThis.fetch;
    globalThis.fetch = (url, opts) => (opts && opts.method === "PATCH")
      ? Promise.resolve({ ok: false, status: 404, json: async () => ({ error: { message: "no operator account for this login (run `roger login` on a node first)" } }) })
      : base(url, opts);
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    await fireEvent.click(el("saveEmail"));
    await waitFor(() => expect(el("saveMsg").textContent).toMatch(/roger login/));
  });
  it("an older broker without provider/operator still renders", async () => {
    broker({ "/account": A({ github_login: "octocat", github_id: 7 }) });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    expect(el("handle").textContent).toBe("@octocat");
    expect(el("signin").textContent).toBe("GitHub");
    expect(el("machines").textContent).toBe("-");
  });
});

describe("adding an email sign-in (features/auth/email_link.feature)", () => {
  const posts = (calls) => calls.filter((c) => c.path.startsWith("/auth/email/link/")).map((c) => ({ u: c.path, body: c.body }));
  it("a GitHub sign-in with machines can add an address: send code, then verify", async () => {
    const calls = broker({ "/account": A(GH), "/auth/email/link/start": [200, { ok: true, token: "TOK123" }], "/auth/email/link/verify": [200, { ok: true }] });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    expect(el("linkPanel").hidden).toBe(false);
    expect(el("linkForm").hidden).toBe(false);
    await fireEvent.input(el("linkEmail"), { target: { value: "me@example.com" } });
    await fireEvent.click(el("linkSend"));
    await waitFor(() => expect(el("linkStep2").hidden).toBe(false));
    expect(el("linkMsg").textContent).toMatch(/^If that address can receive mail/);
    await fireEvent.input(el("linkCode"), { target: { value: "123456" } });
    await fireEvent.click(el("linkVerify"));
    await waitFor(() => expect(el("linkMsg").textContent).toMatch(/me@example\.com/));
    expect(posts(calls)).toEqual([
      { u: "/auth/email/link/start", body: { email: "me@example.com" } },
      { u: "/auth/email/link/verify", body: { email: "me@example.com", code: "123456", token: "TOK123" } },
    ]);
    expect(el("linkMsg").textContent).toMatch(/added|verified/i);
    // the contact field became read-only without a reload
    expect(el("email").value).toBe("me@example.com");
    expect(el("email").readOnly).toBe(true);
    expect(el("saveEmail").hidden).toBe(true);
    expect(el("linkCurrent").textContent).toBe("Verified: me@example.com");
    expect(el("linkStep2").hidden).toBe(true);
  });
  it("a refused address shows the broker's reason and records nothing locally", async () => {
    broker({ "/account": A(GH), "/auth/email/link/start": [200, { ok: true, token: "T" }], "/auth/email/link/verify": [409, { error: { message: "that address cannot be added" } }] });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    await fireEvent.input(el("linkEmail"), { target: { value: "taken@example.com" } });
    await fireEvent.click(el("linkSend"));
    await waitFor(() => expect(el("linkStep2").hidden).toBe(false));
    await fireEvent.input(el("linkCode"), { target: { value: "111111" } });
    await fireEvent.click(el("linkVerify"));
    await waitFor(() => expect(el("linkMsg").textContent).toMatch(/cannot be added/));
    expect(el("linkMsg").textContent).not.toMatch(/added to your account/i);
    expect(el("email").readOnly).toBe(false);
  });
  it("a start failure (rate limit) is shown and the code step stays closed", async () => {
    broker({ "/account": A(GH), "/auth/email/link/start": [429, { error: { message: "too many requests - wait a moment and try again" } }] });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    await fireEvent.input(el("linkEmail"), { target: { value: "me@example.com" } });
    await fireEvent.click(el("linkSend"));
    await waitFor(() => expect(el("linkMsg").textContent).toMatch(/too many/));
    expect(el("linkStep2").hidden).toBe(true);
  });
  it("a sign-in with no machines is told to run roger login first, no form", async () => {
    broker({ "/account": A({ github_login: "newbie", github_id: 99, provider: "github", operator: false }) });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    expect(el("linkPanel").hidden).toBe(false);
    expect(el("linkForm").hidden).toBe(true);
    expect(el("linkNeedsLogin").hidden).toBe(false);
  });
  it("an already-verified address is shown as the sign-in email, read-only; an unverified one stays editable", async () => {
    broker({ "/account": A({ ...GH, email: "me@example.com", email_verified: true }) });
    fakeLocation({ pathname: "/account.html" });
    const { unmount } = render(AccountApp);
    await ready();
    expect(el("linkCurrent").textContent).toMatch(/me@example\.com/);
    expect(el("email").readOnly).toBe(true);
    expect(el("saveEmail").hidden).toBe(true);
    unmount();
    broker({ "/account": A({ ...GH, email: "me@example.com", email_verified: false }) });
    render(AccountApp);
    await ready();
    expect(el("email").readOnly).toBe(false);
  });
  it("when a separate email wallet holds funds, the merge note is shown with the success message", async () => {
    broker({ "/account": A(GH), "/auth/email/link/start": [200, { ok: true, token: "T" }],
      "/auth/email/link/verify": [200, { ok: true, email: "me@example.com", separate_email_balance: 3, merge_note: "A separate email-only account for this address holds funds. Nothing was merged; ask support to have the wallets merged deliberately." }] });
    fakeLocation({ pathname: "/account.html" });
    render(AccountApp);
    await ready();
    await fireEvent.input(el("linkEmail"), { target: { value: "me@example.com" } });
    await fireEvent.click(el("linkSend"));
    await waitFor(() => expect(el("linkStep2").hidden).toBe(false));
    await fireEvent.input(el("linkCode"), { target: { value: "123456" } });
    await fireEvent.click(el("linkVerify"));
    await waitFor(() => expect(el("linkMsg").textContent).toMatch(/me@example\.com is added and verified/));
    expect(el("linkMsg").textContent).toMatch(/merged deliberately/);
  });
});

describe("export and delete", () => {
  it("delete asks first, posts, and goes to login on success; a refusal shows the reason", async () => {
    const calls = broker({ "/account": A(GH), "/account/delete": [409, { error: { message: "deletion is blocked while you hold a balance" } }] });
    const navs = fakeLocation({ pathname: "/account.html" });
    Object.defineProperty(window, "confirm", { configurable: true, value: vi.fn(() => false) });
    render(AccountApp);
    await ready();
    await fireEvent.click(el("del"));
    expect(calls.some((c) => c.path === "/account/delete")).toBe(false);   // declined: nothing sent
    Object.defineProperty(window, "confirm", { configurable: true, value: () => true });
    await fireEvent.click(el("del"));
    await waitFor(() => expect(el("delMsg").textContent).toMatch(/blocked while you hold a balance/));
    expect(navs).toEqual([]);
  });
});
