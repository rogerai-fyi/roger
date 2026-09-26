#!/usr/bin/env python3
"""Touch hit-area overlap sweep for the built site (web/dist).

A small control may grow an invisible tap area on a touch screen (the --hit-inset
pseudo-element, see the touch hit area block in components.css). That area must never
cover any part of ANOTHER control's drawn box, or a tap aimed at the neighbour opens
this one. For every built page at 390 and 320 CSS px with touch emulation, this reads
each control's hit area (its box, grown by an absolutely placed ::before/::after that
reaches outside it) and fails on any overlap with another visible control's box.

Needs Python Playwright + a Chromium (not a repo dependency; run by hand like
overflow-sweep.py, whose static twin is test/qa-polish2.test.mjs):

    cd web && npm run build && python3 scripts/touch-overlap.py [page.html ...]
"""
import functools, http.server, sys, threading
from pathlib import Path
from playwright.sync_api import sync_playwright

DIST = Path(__file__).resolve().parent.parent / "dist"
WIDTHS = (390, 320)

PROBE = """() => {
  const SEL = 'a[href], button, input:not([type=hidden]), select, textarea, summary, [role=button], [tabindex]:not([tabindex="-1"])';
  const seen = (el) => {
    const r = el.getBoundingClientRect();
    if (!r.width || !r.height) return null;
    const cs = getComputedStyle(el);
    if (cs.visibility === 'hidden' || cs.pointerEvents === 'none' || el.closest('[inert],[aria-hidden="true"]')) return null;
    return r;
  };
  const name = (el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\\s+/).join('.') : '') + ' "' + (el.textContent || el.getAttribute('aria-label') || '').trim().slice(0, 24) + '"';
  const ctrls = [...document.querySelectorAll(SEL)].map((el) => ({ el, r: seen(el) })).filter((c) => c.r);
  const out = [];
  for (const c of ctrls) {
    // the grown area: the box united with an absolutely placed pseudo-element reaching past it
    let { left, top, right, bottom } = c.r;
    let grown = false;
    for (const pe of ['::before', '::after']) {
      const p = getComputedStyle(c.el, pe);
      if (p.content === 'none' || p.position !== 'absolute') continue;
      const t = parseFloat(p.top), l = parseFloat(p.left), b = parseFloat(p.bottom), rr = parseFloat(p.right);
      if ([t, l, b, rr].some(isNaN)) continue;
      if (t < 0) { top = Math.min(top, c.r.top + t); grown = true; }
      if (l < 0) { left = Math.min(left, c.r.left + l); grown = true; }
      if (b < 0) { bottom = Math.max(bottom, c.r.bottom - b); grown = true; }
      if (rr < 0) { right = Math.max(right, c.r.right - rr); grown = true; }
    }
    if (!grown) continue;
    for (const o of ctrls) {
      if (o === c || o.el.contains(c.el) || c.el.contains(o.el)) continue;
      const w = Math.min(right, o.r.right) - Math.max(left, o.r.left);
      const h = Math.min(bottom, o.r.bottom) - Math.max(top, o.r.top);
      if (w > 0.5 && h > 0.5) out.push(name(c.el) + ' covers ' + name(o.el) + ' by ' + Math.round(w) + 'x' + Math.round(h));
    }
  }
  // the code-block copy button's area stays off the code line it sits beside
  for (const btn of document.querySelectorAll('.code-block__copy')) {
    const pre = btn.parentElement.querySelector('pre'); if (!pre) continue;
    const p = getComputedStyle(btn, '::before'), br = btn.getBoundingClientRect(), pr = pre.getBoundingClientRect();
    const l = p.content !== 'none' && p.position === 'absolute' ? br.left + Math.min(0, parseFloat(p.left) || 0) : br.left;
    if (l < pr.right - 0.5) out.push('.code-block__copy covers its code line by ' + Math.round(pr.right - l) + 'px');
  }
  return out;
}"""


def serve():
    class Quiet(http.server.SimpleHTTPRequestHandler):
        def log_message(self, *a):
            pass
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(Quiet, directory=str(DIST)))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def main():
    pages = sys.argv[1:] or sorted(p.name for p in DIST.glob("*.html"))
    srv = serve()
    base = f"http://127.0.0.1:{srv.server_address[1]}/"
    bad = 0
    with sync_playwright() as pw:
        b = pw.chromium.launch()
        for w in WIDTHS:
            ctx = b.new_context(viewport={"width": w, "height": 844}, is_mobile=True, has_touch=True, reduced_motion="reduce")
            ctx.route("**/*", lambda r: r.continue_() if r.request.url.startswith(base) else r.abort())
            pg = ctx.new_page()
            for p in pages:
                pg.goto(base + p, wait_until="load")
                pg.wait_for_timeout(250)
                for line in pg.evaluate(PROBE):
                    bad += 1
                    print(f"{p} @{w}: {line}")
            ctx.close()
        b.close()
    print(f"{len(pages)} page(s) x {len(WIDTHS)} widths: {bad} overlapping hit area(s)")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
