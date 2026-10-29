// /device.html, device approval (lib/account/DeviceApp.svelte), the legacy device.js
// contract: a human types the code, the broker confirms it, the human approves or denies;
// a page load never approves anything; signed out carries the code back through login.
import { describe, it, expect } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import DeviceApp from "#lib/account/DeviceApp.svelte";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const PENDING = { user_code: "ABCD2468", requested_at: Math.floor(Date.now() / 1000) - 10 };

describe("device approval", () => {
  it("starts on the code prompt; an empty lookup asks for the code and calls nothing", async () => {
    const calls = broker({});
    fakeLocation({ pathname: "/device.html" });
    render(DeviceApp);
    expect(visible("dvAsk")).toBe(true);
    expect(visible("dvConfirm")).toBe(false);
    await fireEvent.click(el("dvLookup"));
    await waitFor(() => expect(el("dvAskError").textContent).toMatch(/Enter the code/));
    expect(calls).toEqual([]);
  });
  it("a typed code is normalized, looked up with the cookie, and confirmed with its facts", async () => {
    const calls = broker({ "/auth/device/pending": [200, PENDING] });
    fakeLocation({ pathname: "/device.html" });
    render(DeviceApp);
    await fireEvent.input(el("dvCode"), { target: { value: "abcd-2468" } });
    await fireEvent.keyDown(el("dvCode"), { key: "Enter" });
    await waitFor(() => expect(visible("dvConfirm")).toBe(true));
    expect(calls[0].path).toBe("/auth/device/pending?user_code=ABCD2468");
    expect(calls[0].opts.credentials).toBe("include");
    expect(el("dvShownCode").textContent).toBe("ABCD2468");
    expect(el("dvRequested").textContent).toBe("just now");
    expect(visible("dvAsk")).toBe(false);
  });
  it("a code in the URL is pre-filled and looked up, but nothing is approved by the load", async () => {
    const calls = broker({ "/auth/device/pending": [200, PENDING] });
    fakeLocation({ pathname: "/device.html", search: "?code=abcd-2468" });
    render(DeviceApp);
    await waitFor(() => expect(visible("dvConfirm")).toBe(true));
    expect(calls.map((c) => c.path)).toEqual(["/auth/device/pending?user_code=ABCD2468"]);
    expect(calls.some((c) => /approve|deny/.test(c.path))).toBe(false);
  });
  it("approve posts the confirmed code and names the account; deny posts and says nothing was signed in", async () => {
    const calls = broker({ "/auth/device/pending": [200, PENDING], "/auth/device/approve": [200, { account: "@ada" }], "/auth/device/deny": [200, {}] });
    fakeLocation({ pathname: "/device.html", search: "?code=ABCD2468" });
    const { unmount } = render(DeviceApp);
    await waitFor(() => expect(visible("dvConfirm")).toBe(true));
    await fireEvent.click(el("dvApprove"));
    await waitFor(() => expect(visible("dvApproved")).toBe(true));
    expect(calls.at(-1)).toMatchObject({ path: "/auth/device/approve", opts: { method: "POST", credentials: "include" }, body: { user_code: "ABCD2468" } });
    expect(el("dvAccount").textContent).toBe("@ada");
    unmount();
    render(DeviceApp);
    await waitFor(() => expect(visible("dvConfirm")).toBe(true));
    await fireEvent.click(el("dvDeny"));
    await waitFor(() => expect(visible("dvDenied")).toBe(true));
    expect(calls.at(-1).path).toBe("/auth/device/deny");
  });
  it("signed out (401) shows the sign-in step with a next= that carries the code back here", async () => {
    broker({ "/auth/device/pending": [401, {}] });
    fakeLocation({ pathname: "/device.html" });
    render(DeviceApp);
    await fireEvent.input(el("dvCode"), { target: { value: "ABCD2468" } });
    await fireEvent.click(el("dvLookup"));
    await waitFor(() => expect(visible("dvSignedOut")).toBe(true));
    expect(el("dvSignInLink").getAttribute("href")).toBe("/login.html?next=" + encodeURIComponent("/device.html?code=ABCD2468"));
    expect(visible("dvAsk")).toBe(false);
  });
  it("an invalid code and a refused decision show the broker's answer, calmly", async () => {
    broker({ "/auth/device/pending": [404, {}] });
    fakeLocation({ pathname: "/device.html" });
    const { unmount } = render(DeviceApp);
    await fireEvent.input(el("dvCode"), { target: { value: "ZZZZ" } });
    await fireEvent.click(el("dvLookup"));
    await waitFor(() => expect(el("dvAskError").textContent).toMatch(/not valid/));
    unmount();
    broker({ "/auth/device/pending": [200, PENDING], "/auth/device/approve": [410, { error: "That code expired." }] });
    fakeLocation({ pathname: "/device.html", search: "?code=ABCD2468" });
    render(DeviceApp);
    await waitFor(() => expect(visible("dvConfirm")).toBe(true));
    await fireEvent.click(el("dvApprove"));
    await waitFor(() => expect(el("dvConfirmError").textContent).toBe("That code expired."));
  });
});
