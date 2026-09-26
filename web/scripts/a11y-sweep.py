#!/usr/bin/env python3
"""Accessibility sweep of the built site (web/dist) with axe-core.

Every page (or the ones named), light and dark, at 1440 and 390 wide, with
reduced motion (so reveal animations are settled when colours are measured).
Prints each violation (rule, impact, page, scheme, width, first targets) and
exits non-zero on any serious or critical one. With --tab N it also presses Tab
N times on each page (light, 1440) and reports any stop that is invisible,
off-screen after focus, or shows no focus indicator.

axe-core is not a dependency of this tree (web/ has none). Install it anywhere
and point at its axe.min.js; Python Playwright + Chromium are needed too:

    npm install --prefix ~/.cache/axe axe-core
    cd web && npm run build && python3 scripts/a11y-sweep.py \\
        --axe ~/.cache/axe/node_modules/axe-core/axe.min.js [--tab 40] [page.html ...]

The static twin that runs under `npm test` is test/a11y-guards.test.mjs.
"""
import argparse, functools, http.server, sys, threading
from pathlib import Path
from playwright.sync_api import sync_playwright

DIST = Path(__file__).resolve().parent.parent / "dist"
TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"]
RUN = """async (tags) => (await axe.run(document, {runOnly: {type: 'tag', values: tags}, resultTypes: ['violations']}))
  .violations.map(v => ({id: v.id, impact: v.impact, help: v.help, targets: v.nodes.map(n => n.target.join(' '))}))"""
# one Tab: where focus landed, and whether it can be seen
TAB = """() => { const e = document.activeElement; if (!e || e === document.body) return null;
  const r = e.getBoundingClientRect(), cs = getComputedStyle(e);
  const ring = (cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0) || cs.boxShadow !== 'none'
    || cs.textDecorationLine.includes('underline') || cs.borderBottomStyle !== 'none';
  const name = e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className ? '.' + e.className.trim().split(/\\s+/)[0] : '');
  return {name, visible: r.width > 0 && r.height > 0 && cs.visibility !== 'hidden',
          onscreen: r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth, ring}; }"""


def serve():
    class Quiet(http.server.SimpleHTTPRequestHandler):
        def log_message(self, *a):
            pass
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(Quiet, directory=str(DIST)))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("pages", nargs="*")
    ap.add_argument("--axe", required=True, help="path to axe-core's axe.min.js")
    ap.add_argument("--tab", type=int, default=0, help="also Tab through each page this many stops")
    a = ap.parse_args()
    pages = a.pages or sorted(p.name for p in DIST.glob("*.html"))
    srv = serve()
    base = f"http://127.0.0.1:{srv.server_address[1]}/"
    blocking = 0
    with sync_playwright() as pw:
        br = pw.chromium.launch()
        for scheme in ("light", "dark"):
            for w in (1440, 390):
                ctx = br.new_context(viewport={"width": w, "height": 900}, color_scheme=scheme, reduced_motion="reduce")
                # the site talks to a live API; keep the sweep offline and deterministic
                ctx.route("**/*", lambda r: r.continue_() if r.request.url.startswith(base) else r.abort())
                pg = ctx.new_page()
                for name in pages:
                    pg.goto(base + name, wait_until="load")
                    pg.wait_for_timeout(1400)
                    pg.add_script_tag(path=a.axe)
                    for v in pg.evaluate(RUN, TAGS):
                        bad = v["impact"] in ("serious", "critical")
                        blocking += bad
                        print(f"{'FAIL' if bad else 'note'} {v['id']} [{v['impact']}] {name} {scheme} {w}: "
                              f"{len(v['targets'])} node(s) {v['targets'][:3]}")
                    if a.tab and scheme == "light" and w == 1440:
                        for i in range(a.tab):
                            pg.keyboard.press("Tab")
                            s = pg.evaluate(TAB)
                            if s and not (s["visible"] and s["onscreen"] and s["ring"]):
                                print(f"tab  {name} stop {i + 1}: {s}")
                ctx.close()
        br.close()
    srv.shutdown()
    print(f"{len(pages)} page(s) x 2 schemes x 2 widths: {blocking} serious/critical violation(s)")
    return 1 if blocking else 0


if __name__ == "__main__":
    sys.exit(main())
