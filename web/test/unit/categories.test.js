// The console's category surface (features/categories/web_sharing_view.feature and
// web_card_editor.feature), ported from web/test/categories.test.mjs when the console moved
// to the app. The legacy test pinned the wiring in the built script by regex; these run it.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import ModelsPanel from "#lib/console/ModelsPanel.svelte";
import ModelCard from "#lib/console/ModelCard.svelte";

const MODEL = { node: "K6ABC", model: "gpt-oss-120b", online: true, price_in: 0.1, price_out: 0.4, free: false, schedule: [], overridden: false };

function broker(routes) {
  const calls = [];
  globalThis.fetch = vi.fn(async (url, opts = {}) => {
    const path = String(url).replace("https://broker.rogerai.fm", "");
    calls.push({ path, opts, body: opts.body ? JSON.parse(opts.body) : undefined });
    const hit = Object.entries(routes).find(([k]) => path.startsWith(k));
    const [status, body] = hit ? (typeof hit[1] === "function" ? hit[1]() : hit[1]) : [404, {}];
    return { ok: status >= 200 && status < 300, status, json: async () => body };
  });
  return calls;
}
const text = () => document.body.textContent;
beforeEach(() => { Object.defineProperty(window, "confirm", { configurable: true, value: () => true }); });

describe("web_sharing_view: the console shows the category and says where to change it", () => {
  it("each model card shows a Category line; the panel names the node, the TUI and the local console", async () => {
    broker({ "/provider/models": [200, { models: [{ ...MODEL, category: "language", subcategory: "chat", taxonomy_source: "declared" }], ceiling_in: 50, ceiling_out: 100 }] });
    render(ModelsPanel);
    await waitFor(() => expect(document.querySelector(".md-card")).toBeTruthy());
    expect(document.querySelector(".md-cat .md-lbl").textContent).toBe("Category");
    expect(document.querySelector(".md-catv").textContent).toBe("language/chat");
    expect(text()).toMatch(/roger share --category/);
    expect(text()).toMatch(/SHARE tab/);
    expect(text()).toMatch(/local console/);
    expect(text()).toMatch(/Edit category/);
  });
  it("a category not declared is marked as a guess, and one set on the web is labelled", () => {
    const { unmount } = render(ModelCard, { m: { ...MODEL, category: "vision", taxonomy_source: "guessed" }, ceil: { in: 50, out: 100 }, onreload: () => {} });
    expect(document.querySelector(".md-catv").textContent).toBe("vision~");
    unmount();
    render(ModelCard, { m: { ...MODEL, category: "vision", taxonomy_source: "declared", meta_overridden: true, tags: ["ocr"], content_policy: "aligned" }, ceil: { in: 50, out: 100 }, onreload: () => {} });
    expect(document.querySelector(".md-catv").textContent).toBe("vision · ocr · aligned · set on the web");
    const { unmount: u3 } = render(ModelCard, { m: { ...MODEL }, ceil: { in: 50, out: 100 }, onreload: () => {} });
    expect(document.querySelectorAll(".md-catv")[1].textContent).toBe("none declared");
    u3();
  });
});

describe("web_card_editor: the category editor is a front-end on POST /provider/meta", () => {
  it("Edit category opens an editor fed by /categories; Save waits for the list", async () => {
    const calls = broker({ "/categories": [200, { categories: [{ name: "language", subcategories: [{ name: "chat" }] }, { name: "vision" }] }] });
    render(ModelCard, { m: MODEL, ceil: { in: 50, out: 100 }, onreload: () => {} });
    await fireEvent.click(document.querySelector(".md-catedit"));
    const save = () => [...document.querySelectorAll("button")].find((b) => b.textContent === "Save category");
    expect(save().disabled).toBe(true);                       // until the category list has loaded
    await waitFor(() => expect(save().disabled).toBe(false));
    expect(calls.map((c) => c.path)).toEqual(["/categories"]);
    const cat = document.querySelector(".md-catsel");
    expect([...cat.options].map((o) => o.value)).toEqual(["", "language", "vision"]);
  });
  it("a failed category list is not cached and Save stays disabled", async () => {
    let n = 0;
    broker({ "/categories": () => (++n === 1 ? [500, {}] : [200, { categories: [{ name: "audio" }] }]) });
    const { unmount } = render(ModelCard, { m: MODEL, ceil: { in: 50, out: 100 }, onreload: () => {} });
    await fireEvent.click(document.querySelector(".md-catedit"));
    const save = () => [...document.querySelectorAll("button")].find((b) => b.textContent === "Save category");
    await waitFor(() => expect(text()).toMatch(/Could not load the category list/));
    expect(save().disabled).toBe(true);
    unmount();
    render(ModelCard, { m: MODEL, ceil: { in: 50, out: 100 }, onreload: () => {} });
    await fireEvent.click(document.querySelector(".md-catedit"));
    await waitFor(() => expect(save().disabled).toBe(false));   // the second editor fetched again
    expect(n).toBe(2);
  });
  it("saving posts /provider/meta with the owner's credentials; clearing posts clear: true", async () => {
    const reload = vi.fn();
    const calls = broker({ "/categories": [200, { categories: [{ name: "language", subcategories: [{ name: "chat" }] }] }], "/provider/meta": [200, { ok: true, note: "Saved." }] });
    render(ModelCard, { m: MODEL, ceil: { in: 50, out: 100 }, onreload: reload });
    await fireEvent.click(document.querySelector(".md-catedit"));
    const btn = (label) => [...document.querySelectorAll("button")].find((b) => b.textContent === label);
    await waitFor(() => expect(btn("Save category").disabled).toBe(false));
    const sel = document.querySelector(".md-catsel"); sel.value = "language"; await fireEvent.change(sel);
    const sub = document.querySelectorAll(".md-catsel")[1]; sub.value = "chat"; await fireEvent.change(sub);
    const tags = document.querySelector(".md-cattext"); tags.value = "a, b"; await fireEvent.input(tags);
    await fireEvent.click(btn("Save category"));
    await waitFor(() => expect(text()).toMatch(/Saved\./));
    const save = calls.find((c) => c.path === "/provider/meta");
    expect(save.opts.method).toBe("POST");
    expect(save.opts.credentials).toBe("include");
    expect(save.body).toMatchObject({ node: "K6ABC", model: "gpt-oss-120b", category: "language/chat", tags: ["a", "b"] });
    expect(reload).toHaveBeenCalled();
    await fireEvent.click(btn("Clear"));
    await waitFor(() => expect(calls.filter((c) => c.path === "/provider/meta").length).toBe(2));
    expect(calls.at(-1).body).toEqual({ node: "K6ABC", model: "gpt-oss-120b", clear: true });
  });
  it("a refused save shows the server's own message in the card", async () => {
    broker({ "/categories": [200, { categories: [{ name: "language" }] }], "/provider/meta": [400, { ok: false, error: { message: "tag too long" } }] });
    render(ModelCard, { m: MODEL, ceil: { in: 50, out: 100 }, onreload: () => {} });
    await fireEvent.click(document.querySelector(".md-catedit"));
    const btn = (label) => [...document.querySelectorAll("button")].find((b) => b.textContent === label);
    await waitFor(() => expect(btn("Save category").disabled).toBe(false));
    await fireEvent.click(btn("Save category"));
    await waitFor(() => expect(document.querySelector(".md-state--err").textContent).toBe("tag too long"));
  });
  it("the editor builds its fields from values, never from markup (no {@html} in the console)", () => {
    const dir = path.join(process.cwd(), "app", "lib", "console");   // vitest runs in web/
    for (const f of readdirSync(dir)) expect(readFileSync(path.join(dir, f), "utf8"), f).not.toMatch(/\{@html/);
  });
});
