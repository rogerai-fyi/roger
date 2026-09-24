#!/usr/bin/env python3
"""Sitewide horizontal-overflow sweep for the built site (web/dist).

For every built page, at a phone width (390) and a desktop width (1440), load
the page in headless Chromium and assert the document does not scroll
sideways: documentElement.scrollWidth <= clientWidth. Prints one line per
offending page x width plus the widest elements, and exits non-zero on any.

Needs Python Playwright + a Chromium (not a repo dependency, so this runs
manually, not under `npm test`; the static twin is test/layout-overflow.test.mjs):

    cd web && npm run build && python3 scripts/overflow-sweep.py [--shots DIR] [page.html ...]

It serves dist/ from an in-process HTTP server on a free loopback port, so it
never collides with a preview server already running.
"""
import argparse, functools, http.server, sys, threading
from pathlib import Path
from playwright.sync_api import sync_playwright

DIST = Path(__file__).resolve().parent.parent / "dist"
WIDTHS = (390, 1440)

PROBE = """() => {
  const de = document.documentElement, vw = de.clientWidth;
  const wide = [];
  for (const el of document.body.querySelectorAll('*')) {
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.right <= vw + 1) continue;
    // skip descendants of a box that already clips its own overflow
    let p = el.parentElement, clipped = false;
    while (p && p !== document.body) {
      const ox = getComputedStyle(p).overflowX;
      if (ox !== 'visible') { clipped = true; break; }
      p = p.parentElement;
    }
    if (clipped) continue;
    const cls = typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\\s+/).join('.') : '';
    wide.push(el.tagName.toLowerCase() + cls + ' right=' + Math.round(r.right));
  }
  return { sw: de.scrollWidth, cw: vw, wide: wide.slice(0, 5) };
}"""


def serve():
    class Quiet(http.server.SimpleHTTPRequestHandler):
        def log_message(self, *a):
            pass
    handler = functools.partial(Quiet, directory=str(DIST))
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def route(base, fixtures, r):
    url = r.request.url
    if url.startswith(base):
        return r.continue_()
    if fixtures:
        tail = url.split("?")[0].rstrip("/").rsplit("/", 1)[-1]
        f = Path(fixtures) / tail
        if tail in ("market", "discover") and f.is_file():
            return r.fulfill(status=200, content_type="application/json", body=f.read_text(),
                             headers={"access-control-allow-origin": "*"})
    return r.abort()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("pages", nargs="*")
    ap.add_argument("--shots", help="save a 390px full-page screenshot of each page here")
    ap.add_argument("--broker-fixtures", metavar="DIR",
                    help="answer broker GET /market and /discover from DIR/market + DIR/discover (JSON) so data-driven pages (models.html) render real rows")
    ap.add_argument("--dark", action="store_true", help="emulate prefers-color-scheme: dark")
    a = ap.parse_args()
    pages = a.pages or sorted(p.name for p in DIST.glob("*.html"))
    srv = serve()
    base = f"http://127.0.0.1:{srv.server_address[1]}/"
    bad = 0
    with sync_playwright() as pw:
        br = pw.chromium.launch()
        for w in WIDTHS:
            ctx = br.new_context(viewport={"width": w, "height": 900},
                                 color_scheme="dark" if a.dark else "light")
            # the site talks to a live API; keep the sweep offline and deterministic
            ctx.route("**/*", functools.partial(route, base, a.broker_fixtures))
            pg = ctx.new_page()
            for name in pages:
                pg.goto(base + name, wait_until="load")
                pg.wait_for_timeout(250)
                res = pg.evaluate(PROBE)
                h = pg.evaluate("document.documentElement.scrollHeight")
                if res["sw"] > res["cw"]:
                    bad += 1
                    print(f"OVERFLOW {name} @{w}: scrollWidth={res['sw']} > {res['cw']}  height={h}  {res['wide']}")
                if a.shots and w == 390:
                    Path(a.shots).mkdir(parents=True, exist_ok=True)
                    pg.screenshot(path=str(Path(a.shots) / (name.replace('.html', '') + ".png")), full_page=True)
            ctx.close()
        br.close()
    srv.shutdown()
    print(f"{len(pages)} page(s) x {len(WIDTHS)} widths: {bad} overflowing")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
