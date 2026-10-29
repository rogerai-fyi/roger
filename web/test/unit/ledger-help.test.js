// The money-flow help modal (lib/billing/LedgerHelp.svelte): the legacy billing-help.js
// contracts from test/billing-help.test.mjs, as behavior over a real DOM. Opened by any
// [data-help-open] on the page; focus moves in and back out; Esc, the backdrop, the X and
// Done close it; Tab is trapped; every trigger's aria-expanded follows; the explainer video
// plays only while open and never under reduced motion, its poster promoted on first open.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/svelte";
import LedgerHelp from "#lib/billing/LedgerHelp.svelte";

const el = (id) => document.getElementById(id);
let play, pause;
beforeEach(() => {
  document.body.innerHTML = '<button id="walletHelp" data-help-open aria-expanded="false">?</button><button id="other" data-help-open aria-expanded="false">How the money works</button><button id="elsewhere">x</button>';
  play = vi.fn(() => Promise.resolve());
  pause = vi.fn();
  HTMLMediaElement.prototype.play = play;
  HTMLMediaElement.prototype.pause = pause;
  delete window.matchMedia;
});
const expanded = () => [...document.querySelectorAll("[data-help-open]")].map((t) => t.getAttribute("aria-expanded"));
async function open(trigger = "walletHelp") {
  el(trigger).focus();
  await fireEvent.click(el(trigger));
  await waitFor(() => expect(el("ledgerModal").hidden).toBe(false));
}

describe("open and close", () => {
  it("a trigger opens it: focus lands on Close, triggers read expanded, the body is marked, the video plays with its poster promoted", async () => {
    render(LedgerHelp);
    expect(el("ledgerModal").hidden).toBe(true);
    const video = document.querySelector("#ledger-demo video");
    expect(video.getAttribute("poster")).toBe(null);   // zero bytes while closed
    expect(video.getAttribute("preload")).toBe("none");
    await open();
    expect(document.activeElement).toBe(el("ledgerClose"));
    expect(expanded()).toEqual(["true", "true"]);
    expect(document.body.classList.contains("bx-help-open")).toBe(true);
    expect(play).toHaveBeenCalledTimes(1);
    expect(video.getAttribute("poster")).toBe("assets/ledger-demo.gif");
  });
  it("Escape closes it, pauses the video and restores focus to the opener; Escape while closed does nothing", async () => {
    render(LedgerHelp);
    await fireEvent.keyDown(document, { key: "Escape" });
    expect(pause).not.toHaveBeenCalled();
    await open("other");
    await fireEvent.keyDown(document, { key: "Escape" });
    expect(el("ledgerModal").hidden).toBe(true);
    expect(expanded()).toEqual(["false", "false"]);
    expect(document.body.classList.contains("bx-help-open")).toBe(false);
    expect(pause).toHaveBeenCalledTimes(1);
    expect(document.activeElement).toBe(el("other"));
  });
  it("the backdrop, the X and Done each close it", async () => {
    render(LedgerHelp);
    for (const id of ["ledgerScrim", "ledgerClose", "ledgerDone"]) {
      await open();
      await fireEvent.click(el(id));
      expect(el("ledgerModal").hidden, id).toBe(true);
    }
    expect(pause).toHaveBeenCalledTimes(3);
  });
  it("Tab is trapped inside the dialog, both directions", async () => {
    render(LedgerHelp);
    await open();
    const dialog = document.querySelector(".bx-help__dialog");
    el("ledgerDone").focus();
    await fireEvent.keyDown(dialog, { key: "Tab" });
    expect(document.activeElement).toBe(el("ledgerClose"));
    await fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(el("ledgerDone"));
    el("ledgerClose").focus();
    await fireEvent.keyDown(dialog, { key: "a" });   // other keys pass
    expect(document.activeElement).toBe(el("ledgerClose"));
  });
  it("under reduced motion the video rests on its poster frame: paused, never played", async () => {
    window.matchMedia = () => ({ matches: true });
    render(LedgerHelp);
    await open();
    expect(play).not.toHaveBeenCalled();
    expect(pause).toHaveBeenCalled();
    expect(document.querySelector("#ledger-demo video").getAttribute("poster")).toBe("assets/ledger-demo.gif");
  });
  it("a click elsewhere opens nothing", async () => {
    render(LedgerHelp);
    await fireEvent.click(el("elsewhere"));
    expect(el("ledgerModal").hidden).toBe(true);
    expect(play).not.toHaveBeenCalled();
  });
});
