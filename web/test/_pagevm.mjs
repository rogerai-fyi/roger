// Shared harness: run a REAL page script in a vm against a fake broker, recording
// navigations. Every element is a permissive stub that records hidden/textContent/innerHTML.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import vm from "node:vm";
import path from "node:path";
import { createRequire } from "node:module";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
export const src = (p) => readFileSync(path.join(SRC, p), "utf8");
const require = createRequire(import.meta.url);
export const Fmt = require(path.join(SRC, "js/fmt.js"));

// A page environment: every element is a permissive stub that records hidden/textContent.
export function page(pathname, routes, opts = {}) {
  const els = {};
  const el = (id) =>
    (els[id] ||= new Proxy(
      { id, hidden: true, disabled: false, __wired: false, textContent: "", style: {}, classList: { add() {}, remove() {}, toggle() {} }, _l: {} },
      {
        get: (t, k) =>
          k === "addEventListener" ? (ev, fn) => { (t._l[ev] ||= []).push(fn); }
          : k in t ? t[k] : () => el(id + "." + String(k)),
        set: (t, k, v) => ((t[k] = v), true),
      },
    ));
  const navs = [];
  const location = { pathname, search: "", replace: (u) => navs.push(u), href: "" };
  const fetched = [];
  const fetch = (url, opts) => {
    const u = String(url).replace("https://broker.rogerai.fm", "");
    fetched.push(u);
    const key = Object.keys(routes).find((k) => u.startsWith(k));
    const status = key ? routes[key].status : 404;
    const body = key ? routes[key].body : {};
    return Promise.resolve({ ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) });
  };
  const document = { getElementById: el, querySelector: () => null, querySelectorAll: () => [], createElement: () => el("new"), createTextNode: (t) => ({ textContent: t }), addEventListener(ev, fn) { if (ev === "DOMContentLoaded") queueMicrotask(fn); }, removeEventListener() {}, body: el("body"), cookie: "" };
  const ctx = vm.createContext({ document, location, fetch, URLSearchParams, console, setTimeout, queueMicrotask, window: {} });
  ctx.window = ctx;
  if (!opts.noFmt) ctx.RogerFmt = Fmt;
  // On-demand: asserting on an element the script never touched reads its default (hidden) stub.
  const elsView = new Proxy(els, { get: (t, k) => (typeof k === "string" ? t[k] || el(k) : t[k]) });
  const fire = (id, ev) => Promise.all(((els[id] && els[id]._l[ev]) || []).map((f) => f({ preventDefault() {}, stopPropagation() {} })));
  const texts = () => Object.values(els).map((e) => e.textContent).filter(Boolean).join(" | ");
  return { ctx, els: elsView, navs, fetched, fire, texts, run: (file) => vm.runInContext(src(file), ctx, { filename: file }) };
}

export const settle = () => new Promise((r) => setTimeout(r, 20));
