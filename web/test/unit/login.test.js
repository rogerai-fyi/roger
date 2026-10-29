// /login.html (lib/account/LoginApp.svelte): the legacy auth.js, login-next.js and
// email-login.js contracts. Already signed in goes to the dashboard (the other half of the
// login <-> dashboard loop lock: a 401 feed on the dashboard never bounces back here, so
// the two cannot ping-pong). The email code is typed back in this tab; the page never
// reveals whether an address is known; `next` rides to the provider and the verify call.
import { describe, it, expect } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import LoginApp from "#lib/account/LoginApp.svelte";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;

describe("sign in", () => {
  it("already signed in: straight to the dashboard; signed out: stays, with the provider links plain", async () => {
    broker({ "/account": [200, { github_login: "ada" }] });
    const navs = fakeLocation({ pathname: "/login.html" });
    const { unmount } = render(LoginApp);
    await waitFor(() => expect(navs).toEqual(["/dashboard.html"]));
    unmount();
    const calls = broker({ "/account": [401, {}] });
    const navs2 = fakeLocation({ pathname: "/login.html" });
    render(LoginApp);
    await waitFor(() => expect(calls.map((c) => c.path)).toEqual(["/account"]));
    expect(navs2).toEqual([]);
    expect(document.querySelector('a.gh[href="https://broker.rogerai.fm/auth/github/login"]')).toBeTruthy();
    expect(document.querySelector('a.gh[href="https://broker.rogerai.fm/auth/apple/web/login"]')).toBeTruthy();
  });
  it("a same-site next rides onto both provider links; anything else is dropped", async () => {
    broker({ "/account": [401, {}] });
    fakeLocation({ pathname: "/login.html", search: "?next=" + encodeURIComponent("/device.html?code=AB12") });
    const { unmount } = render(LoginApp);
    const hrefs = () => [...document.querySelectorAll("a.gh")].map((a) => a.getAttribute("href"));
    expect(hrefs()).toEqual([
      "https://broker.rogerai.fm/auth/github/login?next=" + encodeURIComponent("/device.html?code=AB12"),
      "https://broker.rogerai.fm/auth/apple/web/login?next=" + encodeURIComponent("/device.html?code=AB12"),
    ]);
    unmount();
    for (const evil of ["https://evil.example/", "//evil.example", "/\\evil"]) {
      fakeLocation({ pathname: "/login.html", search: "?next=" + encodeURIComponent(evil) });
      const r = render(LoginApp);
      expect(hrefs().every((h) => !h.includes("next="))).toBe(true);
      r.unmount();
    }
  });
  it("email: asks for a code with the cookie, then the code step names the address with 'if'", async () => {
    const calls = broker({ "/account": [401, {}], "/auth/email/start": [200, {}] });
    fakeLocation({ pathname: "/login.html" });
    render(LoginApp);
    await fireEvent.input(el("email-input"), { target: { value: " ada@example.com " } });
    await fireEvent.submit(el("email-form"));
    await waitFor(() => expect(visible("code-step")).toBe(true));
    expect(visible("email-step")).toBe(false);
    expect(calls.at(-1)).toMatchObject({ path: "/auth/email/start", opts: { method: "POST", credentials: "include" }, body: { email: "ada@example.com" } });
    expect(el("email-sent-to").textContent).toBe("ada@example.com");
    expect(document.querySelector("#code-step .sub").textContent).toMatch(/^If that address can receive mail/);
    expect(el("email-note").textContent).toBe("");
  });
  it("email: a refused start shows the broker's own reason, an unreachable broker a plain one", async () => {
    broker({ "/account": [401, {}], "/auth/email/start": [429, { error: "Too many codes. Try again in a minute." }] });
    fakeLocation({ pathname: "/login.html" });
    const { unmount } = render(LoginApp);
    await fireEvent.input(el("email-input"), { target: { value: "ada@example.com" } });
    await fireEvent.submit(el("email-form"));
    await waitFor(() => expect(el("email-note").textContent).toBe("Too many codes. Try again in a minute."));
    expect(el("email-note").className).toBe("fine err");
    expect(visible("email-step")).toBe(true);
    unmount();
    globalThis.fetch = async (url) => { if (/\/account$/.test(String(url))) return { ok: false, status: 401, json: async () => ({}) }; throw new Error("offline"); };
    render(LoginApp);
    await fireEvent.input(el("email-input"), { target: { value: "ada@example.com" } });
    await fireEvent.submit(el("email-form"));
    await waitFor(() => expect(el("email-note").textContent).toMatch(/Could not reach RogerAI/));
  });
  it("email: the code is verified with the address and next, and the person lands on next or the dashboard", async () => {
    const calls = broker({ "/account": [401, {}], "/auth/email/start": [200, {}], "/auth/email/verify": [200, { next: "/device.html?code=AB12" }] });
    const navs = fakeLocation({ pathname: "/login.html", search: "?next=" + encodeURIComponent("/device.html?code=AB12") });
    render(LoginApp);
    await fireEvent.input(el("email-input"), { target: { value: "ada@example.com" } });
    await fireEvent.submit(el("email-form"));
    await waitFor(() => expect(visible("code-step")).toBe(true));
    await fireEvent.input(el("code-input"), { target: { value: "123456" } });
    await fireEvent.submit(el("email-form"));
    await waitFor(() => expect(navs).toEqual(["/device.html?code=AB12"]));
    expect(calls.at(-1)).toMatchObject({ path: "/auth/email/verify", body: { email: "ada@example.com", code: "123456", next: "/device.html?code=AB12" } });
  });
  it("email: a wrong code says so and stays on the code step; 'use a different address' goes back", async () => {
    broker({ "/account": [401, {}], "/auth/email/start": [200, {}], "/auth/email/verify": [400, { error: "That code is not valid." }] });
    const navs = fakeLocation({ pathname: "/login.html" });
    render(LoginApp);
    await fireEvent.input(el("email-input"), { target: { value: "ada@example.com" } });
    await fireEvent.submit(el("email-form"));
    await waitFor(() => expect(visible("code-step")).toBe(true));
    await fireEvent.input(el("code-input"), { target: { value: "000000" } });
    await fireEvent.submit(el("email-form"));
    await waitFor(() => expect(el("email-note").textContent).toBe("That code is not valid."));
    expect(visible("code-step")).toBe(true);
    expect(navs).toEqual([]);
    await fireEvent.click(el("email-again"));
    await waitFor(() => expect(visible("email-step")).toBe(true));
    expect(el("email-note").textContent).toBe("");
  });
});
