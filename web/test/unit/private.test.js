// Base Station, /private.html (lib/private/PrivateApp.svelte over lib/private/rc.js): the pure
// render core from test/private.test.mjs (stateLabel, frameLine), the bands honesty rule from
// test/honest-feeds.test.mjs and the audit finding it pinned (a failed load's reason never
// lingers in the empty state), and the live session view: owner-join, the SSE transcript,
// confirms answered by their exact id, the ask box, the phone link, the /r hand-off.
import { describe, it, expect, vi } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/svelte";
import PrivateApp from "#lib/private/PrivateApp.svelte";
import { stateLabel, frameLine, sseFrames } from "#lib/private/rc.js";
import { broker, fakeLocation } from "./_broker.js";

const el = (id) => document.getElementById(id);
const visible = (id) => !el(id).hidden;
const text = (id) => el(id).textContent.trim();
const SESSIONS = [{ id: "s1", name: "laptop", online: true }, { id: "s2", name: "", online: false }, { id: "s3", name: "old", revoked: true, online: true }];
const OK = { "/rc/sessions": [200, { sessions: SESSIONS }], "/bands": [200, { bands: [] }] };
const ready = () => waitFor(() => expect(visible("sessionsWrap")).toBe(true));
function mount(routes) {
  const calls = broker(routes);
  const navs = fakeLocation({ pathname: "/private.html" });
  const { unmount } = render(PrivateApp);
  return { calls, navs, unmount, fetched: () => calls.map((c) => c.path) };
}
// an SSE body: the frames as "data:" lines, in the chunks given
const enc = new TextEncoder();
const sse = (...chunks) => new ReadableStream({ start(c) { for (const ch of chunks) c.enqueue(enc.encode(ch)); c.close(); } });
const line = (f) => "data: " + JSON.stringify(f) + "\n";
// a broker that joins, streams the given body and records sends
function session(body, extra = {}) {
  const calls = [];
  globalThis.fetch = vi.fn(async (url, opts = {}) => {
    const path = String(url).replace("https://broker.rogerai.fm", "");
    calls.push({ path, opts, body: opts.body ? JSON.parse(opts.body) : undefined, headers: opts.headers || {} });
    if (path === "/rc/sessions") return { ok: true, status: 200, json: async () => ({ sessions: SESSIONS }) };
    if (path === "/bands") return { ok: true, status: 200, json: async () => ({ bands: [] }) };
    if (path === "/rc/s1/join") return { ok: true, status: 200, json: async () => ({ attach_token: "att-1" }) };
    if (path === "/rc/s1/stream") return { ok: true, status: 200, body: typeof body === "function" ? body() : body };
    if (path === "/rc/s1/send") return { ok: true, status: 200, json: async () => ({}) };
    if (path === "/rc/s1/code") return { ok: true, status: 200, json: async () => ({ code_short: "ABCD-12" }) };
    if (extra[path]) return extra[path]();
    return { ok: false, status: 404, json: async () => ({}) };
  });
  fakeLocation({ pathname: "/private.html" });
  render(PrivateApp);
  return { calls, sends: () => calls.filter((c) => c.path === "/rc/s1/send").map((c) => c.body) };
}
const lines = () => [...document.querySelectorAll("#stream .rc-line")].map((d) => d.textContent);

describe("rc.js: the pure core (ported from test/private.test.mjs)", () => {
  it("stateLabel: live earns the red dot; ended/offline do not", () => {
    expect(stateLabel({ online: true })).toEqual({ dot: "◉", label: "live", live: true });
    expect(stateLabel({ online: false })).toEqual({ dot: "○", label: "offline", live: false });
    expect(stateLabel({ revoked: true, online: true })).toEqual({ dot: "·", label: "ended", live: false });
  });
  it("frameLine: each RCFrame kind renders its line", () => {
    expect(frameLine({ kind: "user", origin: "web", text: "hi" }).text).toBe("▸ (web) hi");
    expect(frameLine({ kind: "assistant", text: "yo" }).text).toBe("◂ yo");
    expect(frameLine({ kind: "final", text: "done" }).text).toBe("◂ done");
    expect(frameLine({ kind: "tool_call", tool: "run_shell" }).text).toBe("◉ run_shell");
    expect(frameLine({ kind: "tool_result", tool: "read_file" }).text).toBe("✓ read_file");
    expect(frameLine({ kind: "error", text: "boom" }).text).toBe("✗ boom");
  });
  it("frameLine: a confirm_req carries its id so the answer can correlate", () => {
    const l = frameLine({ kind: "confirm_req", tool: "write_file", confirm_id: "cf-9" });
    expect(l.confirm).toBe("cf-9");
    expect(l.text).toMatch(/write_file/);
  });
  it("frameLine: confirm_done names the answer + origin; ended flags the terminal frame", () => {
    expect(frameLine({ kind: "confirm_done", approve: true, origin: "web" }).text).toBe("✓ approved from web");
    expect(frameLine({ kind: "confirm_done", approve: false, origin: "local" }).text).toBe("✓ denied from local");
    expect(frameLine({ kind: "ended" }).ended).toBe(true);
  });
  it("frameLine: empty assistant/backfill text renders nothing (null)", () => {
    expect(frameLine({ kind: "assistant", text: "   " })).toBe(null);
    expect(frameLine({ kind: "backfill", text: "" })).toBe(null);
    expect(frameLine({ kind: "unknown_kind" })).toBe(null);
  });
  // Guest Operators iteration-2 finding: the web console used to DROP status frames, so a
  // handoff looked dead in the browser. A status frame renders as a dim rc-status line.
  it("frameLine: a status frame renders the handoff transition as a dim rc-status line", () => {
    expect(frameLine({ kind: "status", operator: "opencode", text: "guest has the mic: opencode - the DJ answers when the handoff ends" }))
      .toEqual({ cls: "rc-status", text: "◉ guest has the mic: opencode" });
    expect(frameLine({ kind: "status", text: "the DJ is back at the desk" })).toEqual({ cls: "rc-status", text: "the DJ is back at the desk" });
    expect(frameLine({ kind: "status" })).toBe(null);
    expect(frameLine({ kind: "status", text: "" })).toBe(null);
  });
  // Operator frame enrichment (rc_enrichment.feature, founder ruling 3): "<op> has the mic on
  // <model> · $<spend>", degrading piecewise; no band ever rides a frame (ruling 2).
  it("frameLine: an enriched status frame renders op/model/spend and degrades cleanly", () => {
    const fixed = "guest has the mic: opencode - the DJ answers when the handoff ends";
    expect(frameLine({ kind: "status", operator: "opencode", model: "gpt-oss-120b", spend: 0.19, text: fixed })).toEqual({ cls: "rc-status", text: "◉ opencode has the mic on gpt-oss-120b · $0.19" });
    expect(frameLine({ kind: "status", operator: "opencode", model: "gpt-oss-120b", spend: 0, text: fixed })).toEqual({ cls: "rc-status", text: "◉ opencode has the mic on gpt-oss-120b" });
    expect(frameLine({ kind: "status", operator: "aider", spend: 1.05, text: fixed })).toEqual({ cls: "rc-status", text: "◉ aider has the mic · $1.05" });
    expect(frameLine({ kind: "status", operator: "opencode", text: fixed })).toEqual({ cls: "rc-status", text: "◉ guest has the mic: opencode" });
    expect(frameLine({ kind: "status", operator: "opencode", spend: "0.19", text: fixed })).toEqual({ cls: "rc-status", text: "◉ guest has the mic: opencode" });
  });
  it("sseFrames: complete data: lines become frames, the partial tail carries over, garbage is skipped", () => {
    const a = sseFrames("", 'data: {"kind":"user","text":"hi"}\n: comment\ndata: {"kind":');
    expect(a.frames).toEqual([{ kind: "user", text: "hi" }]);
    expect(a.rest).toBe('data: {"kind":');
    const b = sseFrames(a.rest, '"ended"}\ndata: not json\n');
    expect(b.frames).toEqual([{ kind: "ended" }]);
    expect(b.rest).toBe("");
  });
});

describe("the roster", () => {
  it("tuning in: the card shows on mount with the loading line; sessions and bands appear once the roster answers", async () => {
    const p = mount(OK);
    expect(visible("card")).toBe(true);
    expect(visible("loading")).toBe(true);
    await ready();
    expect(visible("loading")).toBe(false);
    expect(visible("bandsWrap")).toBe(true);
    expect(p.fetched()).toEqual(["/rc/sessions", "/bands"]);
    const rows = [...document.querySelectorAll("#sessionRows tr")];
    expect(rows.length).toBe(3);
    expect(rows[0].querySelector(".cn-st").textContent).toBe("◉");
    expect(rows[0].querySelector(".cn-st").classList.contains("is-live")).toBe(true);
    expect(rows[0].children[1].textContent).toBe("laptop");
    expect(rows[0].children[2].textContent).toBe("live");
    expect([...rows[0].querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Open", "Link"]);
    expect(rows[1].children[1].textContent).toBe("s2");   // no name: the id
    expect(rows[1].querySelector(".cn-st").classList.contains("is-live")).toBe(false);
    expect([...rows[1].querySelectorAll("button")].map((b) => b.textContent)).toEqual(["View", "Link"]);
    expect(rows[2].children[2].textContent).toBe("ended");
    expect(rows[2].querySelectorAll("button").length).toBe(0);   // an ended session has no actions
    expect(visible("sessionsEmpty")).toBe(false);
  });
  it("no sessions: the empty line, no table", async () => {
    mount({ ...OK, "/rc/sessions": [200, { sessions: [] }] });
    await ready();
    expect(visible("sessionsEmpty")).toBe(true);
    expect(visible("sessionsTable")).toBe(false);
  });
  it("signed out (401): the gate, and the bands are never asked for", async () => {
    const p = mount({ "/rc/sessions": [401, {}] });
    await waitFor(() => expect(visible("gate")).toBe(true));
    expect(visible("loading")).toBe(false);
    expect(visible("sessionsWrap")).toBe(false);
    expect(p.fetched()).toEqual(["/rc/sessions"]);
  });
  it("a fault (5xx or offline): the error line, not the gate", async () => {
    const p = mount({ "/rc/sessions": [500, {}] });
    await waitFor(() => expect(visible("error")).toBe(true));
    expect(visible("gate")).toBe(false);
    p.unmount();
    globalThis.fetch = async () => { throw new Error("offline"); };
    fakeLocation({ pathname: "/private.html" });
    render(PrivateApp);
    await waitFor(() => expect(visible("error")).toBe(true));
  });
  it("private bands: rows with the live dot, or the honest-empty line", async () => {
    const p = mount({ ...OK, "/bands": [200, { bands: [{ label: "Lab", node_id: "K6ABC", display: "88.1", status: "active" }, { node_id: "K6XYZ", status: "revoked" }] }] });
    await ready();
    await waitFor(() => expect(visible("bandsTable")).toBe(true));
    const rows = [...document.querySelectorAll("#bandRows tr")];
    expect(rows[0].querySelector(".cn-st").classList.contains("is-live")).toBe(true);
    expect([...rows[0].children].map((c) => c.textContent)).toEqual(["◉", "Lab", "88.1", "active"]);
    expect([...rows[1].children].map((c) => c.textContent)).toEqual(["·", "K6XYZ", "-", "revoked"]);
    expect(visible("bandsEmpty")).toBe(false);
    p.unmount();
    mount(OK);
    await waitFor(() => expect(visible("bandsEmpty")).toBe(true));
    expect(text("bandsEmpty")).toBe("No private bands yet.");
    expect(visible("bandsTable")).toBe(false);
  });
  it("a failed bands load says WHICH failure, never 'you have none': 403 names the unlinked machine, 401 says sign in, 5xx says it could not load", async () => {
    const p = mount({ ...OK, "/bands": [403, {}] });
    await waitFor(() => expect(visible("bandsEmpty")).toBe(true));
    expect(text("bandsEmpty")).toMatch(/machine on air/);
    expect(text("bandsEmpty")).not.toMatch(/sign in to see your private bands/);
    expect(visible("bandsTable")).toBe(false);
    p.unmount();
    const q = mount({ ...OK, "/bands": [401, {}] });
    await waitFor(() => expect(text("bandsEmpty")).toBe("sign in to see your private bands"));
    q.unmount();
    mount({ ...OK, "/bands": [502, {}] });
    await waitFor(() => expect(text("bandsEmpty")).toBe("could not load your private bands (502)"));
  });
});

describe("the live session view", () => {
  it("Open joins by id, streams with the attach header, renders each frame, and ends on the ended frame", async () => {
    const p = session(sse(line({ kind: "user", origin: "phone", text: "hello" }), line({ kind: "assistant", text: "hi there" }) + line({ kind: "tool_call", tool: "read_file" }), line({ kind: "ended" })));
    await ready();
    await fireEvent.click(document.querySelector("#sessionRows tr button"));
    await waitFor(() => expect(visible("viewWrap")).toBe(true));
    expect(text("viewTitle")).toBe("Session - laptop");
    await waitFor(() => expect(lines()).toEqual(["▸ (phone) hello", "◂ hi there", "◉ read_file", "- session ended on the host -"]));
    const join = p.calls.find((c) => c.path === "/rc/s1/join");
    expect(join.opts.method).toBe("POST");
    const stream = p.calls.find((c) => c.path === "/rc/s1/stream");
    expect(stream.headers["X-Roger-Attach"]).toBe("att-1");
    expect(document.querySelector("#stream .rc-line.rc-asst")).toBeTruthy();
  });
  it("a confirm renders Approve/Deny that answer THAT confirm's id; the ask box's y/n answers the latest, once", async () => {
    const p = session(sse(line({ kind: "confirm_req", tool: "write_file", confirm_id: "cf-1" }), line({ kind: "confirm_req", tool: "run_shell", confirm_id: "cf-2" })));
    await ready();
    await fireEvent.click(document.querySelector("#sessionRows tr button"));
    await waitFor(() => expect(document.querySelectorAll("#stream .rc-confirm").length).toBe(2));
    const first = document.querySelectorAll("#stream .rc-confirm")[0];
    expect(first.querySelector("span").textContent).toBe("? write_file - approve? (runs on the host)");
    // the ask box's bare n answers the LATEST pending confirm
    await fireEvent.input(el("ask"), { target: { value: "n" } });
    await fireEvent.submit(el("askForm"));
    await waitFor(() => expect(p.sends()).toEqual([{ kind: "confirm", approve: false, confirm_id: "cf-2", origin: "web" }]));
    expect(el("ask").value).toBe("");
    // the first line's own button answers the exact confirm it was drawn for
    await fireEvent.click(first.querySelector("button.primary"));
    await waitFor(() => expect(p.sends().length).toBe(2));
    expect(p.sends()[1]).toEqual({ kind: "confirm", approve: true, confirm_id: "cf-1", origin: "web" });
    // the shortcut is spent: a later yes is a turn
    await fireEvent.input(el("ask"), { target: { value: "yes" } });
    await fireEvent.submit(el("askForm"));
    await waitFor(() => expect(p.sends().length).toBe(3));
    expect(p.sends()[2]).toEqual({ kind: "turn", text: "yes", origin: "web" });
  });
  it("the ask box sends a trimmed turn with the attach header; blank sends nothing; a failed send says so", async () => {
    const p = session(sse());
    await ready();
    await fireEvent.click(document.querySelector("#sessionRows tr button"));
    await waitFor(() => expect(visible("viewWrap")).toBe(true));
    await fireEvent.input(el("ask"), { target: { value: "   " } });
    await fireEvent.submit(el("askForm"));
    expect(p.sends()).toEqual([]);
    await fireEvent.input(el("ask"), { target: { value: "  list the files  " } });
    await fireEvent.submit(el("askForm"));
    await waitFor(() => expect(p.sends()).toEqual([{ kind: "turn", text: "list the files", origin: "web" }]));
    const send = p.calls.find((c) => c.path === "/rc/s1/send");
    expect(send.headers["X-Roger-Attach"]).toBe("att-1");
    expect(send.headers["Content-Type"]).toBe("application/json");
    globalThis.fetch = async () => { throw new Error("offline"); };
    await fireEvent.input(el("ask"), { target: { value: "again" } });
    await fireEvent.submit(el("askForm"));
    await waitFor(() => expect(lines()).toContain("✗ could not send"));
  });
  it("a join that fails says it could not attach; a stream that fails says stream error", async () => {
    const p = session(sse(), { "/rc/s2/join": () => ({ ok: false, status: 403, json: async () => ({}) }) });
    await ready();
    await fireEvent.click(document.querySelectorAll("#sessionRows tr")[1].querySelector("button"));
    await waitFor(() => expect(visible("viewWrap")).toBe(true));
    expect(text("stream")).toBe("Could not attach to this session.");
    expect(p.calls.some((c) => c.path === "/rc/s2/stream")).toBe(false);
    globalThis.fetch = vi.fn(async (url, opts = {}) => {
      const path = String(url).replace("https://broker.rogerai.fm", "");
      if (path === "/rc/s1/join") return { ok: true, status: 200, json: async () => ({ attach_token: "att-1" }) };
      return { ok: false, status: 500 };
    });
    await fireEvent.click(document.querySelector("#sessionRows tr button"));
    await waitFor(() => expect(lines()).toEqual(["✗ stream error"]));
    expect(text("stream")).not.toMatch(/Could not attach/);
  });
  it("re-opening cancels the previous reader so its frames never bleed in; Close tears the view down", async () => {
    let cancelled = 0;
    const slow = () => new ReadableStream({ start(c) { c.enqueue(enc.encode(line({ kind: "assistant", text: "first" }))); }, cancel() { cancelled++; } });
    const p = session(slow);
    await ready();
    await fireEvent.click(document.querySelector("#sessionRows tr button"));
    await waitFor(() => expect(lines()).toEqual(["◂ first"]));
    await fireEvent.click(document.querySelector("#sessionRows tr button"));
    await waitFor(() => expect(cancelled).toBe(1));
    await waitFor(() => expect(lines()).toEqual(["◂ first"]));   // the new stream's one line, the old one gone
    expect(p.calls.filter((c) => c.path === "/rc/s1/stream").length).toBe(2);
    await fireEvent.click(el("closeView"));
    expect(visible("viewWrap")).toBe(false);
    expect(cancelled).toBe(2);
  });
  it("arriving from a /r link opens the stashed session straight away, with its token, and clears the stash", async () => {
    sessionStorage.setItem("rc_open", JSON.stringify({ id: "s1", name: "laptop", attach: "att-link" }));
    const p = session(sse(line({ kind: "backfill", text: "earlier..." })));
    await waitFor(() => expect(visible("viewWrap")).toBe(true));
    await waitFor(() => expect(lines()).toEqual(["earlier..."]));
    expect(p.calls.some((c) => c.path === "/rc/s1/join")).toBe(false);   // no join: the link flow already held a token
    expect(p.calls.find((c) => c.path === "/rc/s1/stream").headers["X-Roger-Attach"]).toBe("att-link");
    expect(sessionStorage.getItem("rc_open")).toBe(null);
  });
  it("Link mints a one-time code: the name, the code, the phone URL, and the shared copy tick on it", async () => {
    session(sse());
    await ready();
    await fireEvent.click(document.querySelectorAll("#sessionRows tr button")[1]);
    await waitFor(() => expect(visible("linkWrap")).toBe(true));
    expect(text("linkName")).toBe("laptop");
    expect(text("linkCode")).toBe("ABCD-12");
    expect(el("linkURL").getAttribute("href")).toBe("https://rogerai.fm/r.html#ABCD-12");
    expect(text("linkURL")).toBe("https://rogerai.fm/r.html#ABCD-12");
    expect(el("copyLink").getAttribute("data-copy-target")).toBe("#linkURL");   // site.js's copy tick, not a clipboard of its own
  });
  it("'not end-to-end encrypted' explains itself in a plain dialog, without leaving the page", async () => {
    const p = mount(OK);
    window.alert = vi.fn();
    await fireEvent.click(el("whatPrivate"));
    expect(window.alert).toHaveBeenCalledTimes(1);
    expect(window.alert.mock.calls[0][0]).toMatch(/over TLS/);
    expect(window.alert.mock.calls[0][0]).toMatch(/not end-to-end encrypted/);
    expect(p.navs).toEqual([]);
  });
});
