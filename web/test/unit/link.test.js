// /r.html, the link handler (lib/account/LinkApp.svelte), the legacy r.js contract:
// the code rides the fragment, same account required, 401 asks to log in (no redirect),
// 404 is the uniform answer, success stashes the attach token and opens Base Station.
import { describe, it, expect, beforeEach } from "vitest";
import { render, waitFor } from "@testing-library/svelte";
import LinkApp from "#lib/account/LinkApp.svelte";
import { broker, fakeLocation } from "./_broker.js";

const msg = () => document.getElementById("msg").textContent;
beforeEach(() => sessionStorage.clear());

describe("link handler", () => {
  it("no code in the fragment: says so, asks nothing of the broker", async () => {
    const calls = broker({});
    fakeLocation({ pathname: "/r.html", hash: "" });
    render(LinkApp);
    await waitFor(() => expect(msg()).toMatch(/No link code in the URL/));
    expect(calls).toEqual([]);
    expect(document.getElementById("hint").hidden).toBe(false);
  });
  it("exchanges the code for an attach token over a credentialed POST, stashes it, opens the session", async () => {
    const calls = broker({ "/rc/attach": [200, { session_id: "s1", name: "box", attach_token: "t0k" }] });
    const navs = fakeLocation({ pathname: "/r.html", hash: "#ABC123" });
    render(LinkApp);
    await waitFor(() => expect(navs).toEqual(["/private.html"]));
    expect(calls[0]).toMatchObject({ path: "/rc/attach", opts: { method: "POST", credentials: "include" }, body: { code: "ABC123" } });
    expect(JSON.parse(sessionStorage.getItem("rc_open"))).toEqual({ id: "s1", name: "box", attach: "t0k" });
    expect(msg()).toMatch(/Linked/);
  });
  it("logged out (401): asks to log in and reopen the link, never redirects (the fragment must survive)", async () => {
    broker({ "/rc/attach": [401, {}] });
    const navs = fakeLocation({ pathname: "/r.html", hash: "#ABC123" });
    render(LinkApp);
    await waitFor(() => expect(msg()).toMatch(/Log in to your account on this device/));
    expect(document.querySelector("#msg a").getAttribute("href")).toBe("/login.html");
    expect(navs).toEqual([]);
    expect(sessionStorage.getItem("rc_open")).toBeNull();
  });
  it("404 is the one answer for wrong account, expired or used; other faults ask to retry", async () => {
    broker({ "/rc/attach": [404, {}] });
    fakeLocation({ pathname: "/r.html", hash: "#NOPE" });
    const { unmount } = render(LinkApp);
    await waitFor(() => expect(msg()).toMatch(/invalid, expired, or for a different account/));
    unmount();
    broker({ "/rc/attach": [500, {}] });
    render(LinkApp);
    await waitFor(() => expect(msg()).toMatch(/Could not link this device/));
  });
});
