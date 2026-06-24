/* =====================================================================
   RogerAI - the tuning-dial hero signature + reactive on-air beacon.
   Self-served (CSP script-src 'self'). No deps. SVG + CSS transforms only.

   - fetches broker GET /market (authoritative signal 0-100), falls back to
     /discover aggregation, then to a representative demo band.
   - locks the dial on the STRONGEST live band; needle fixed, strip eases in.
   - toggles [data-onair] on <body> so the ((•)) beacon + Ping go live/quiet.
   - one rAF; pauses offscreen + when tab hidden; reduced-motion = static
     final state (pre-locked, no scatter, no poll).
   ===================================================================== */
(function () {
  "use strict";

  var svg = document.getElementById("dialSvg");
  var lockedEl = document.getElementById("dialLocked");
  var sigEl = document.getElementById("dialSig");
  var chipEl = document.getElementById("dialChip");
  var pingTag = document.getElementById("pingTag");
  if (!svg) return;

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var POLL_MS = 30000;

  function setOnAir(live) { document.body.setAttribute("data-onair", live ? "live" : "idle"); }
  if (pingTag) { /* tag text follows state */ }

  function escapeHtml(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
  function fmtPrice(p) { return p ? "$" + (+p).toFixed(2) : "free"; }

  /* ---- normalize a /market entry -------------------------------- */
  function fromMarket(m) {
    var providers = +m.providers || 0;
    var live = providers > 0;
    var sig = m.signal != null ? Math.max(0, Math.min(1, (+m.signal) / 100)) : 0;
    return {
      model: m.model || m.band || "unknown",
      providers: providers,
      tps: +(m.best_tps || m.tps) || 0,
      price: m.min_price != null ? +m.min_price : (m.price_out != null ? +m.price_out : 0),
      signal: live ? (sig || 0.4) : 0,
      live: live
    };
  }
  function aggregateDiscover(offers) {
    var by = {};
    offers.forEach(function (o) {
      if (!o || !o.model) return;
      var m = by[o.model] || (by[o.model] = { model: o.model, online: 0, tpsN: 0, tpsSum: 0, min: Infinity });
      if (o.online !== false) m.online++;
      var tps = +o.tps || 0; if (tps > 0) { m.tpsSum += tps; m.tpsN++; }
      var po = (o.price_out != null ? +o.price_out : +o.price_in);
      if (po != null && !isNaN(po) && po < m.min) m.min = po;
    });
    return Object.keys(by).map(function (k) {
      var m = by[k], tps = m.tpsN ? m.tpsSum / m.tpsN : 0, live = m.online > 0;
      var supply = Math.min(1, Math.log2(m.online + 1) / 3.5), speed = Math.min(1, tps / 90);
      return {
        model: m.model, providers: m.online, tps: tps,
        price: m.min === Infinity ? 0 : m.min,
        signal: live ? Math.max(0.08, 0.62 * supply + 0.38 * speed) : 0, live: live
      };
    });
  }
  function demoBand() {
    var seed = [
      { model: "qwen3-coder-30b", providers: 6, tps: 58, price: 0.22 },
      { model: "qwen3-72b",       providers: 4, tps: 71, price: 0.38 },
      { model: "gpt-oss-120b",    providers: 3, tps: 63, price: 0.55 },
      { model: "llama3.3-70b",    providers: 5, tps: 44, price: 0.31 },
      { model: "deepseek-v3",     providers: 2, tps: 55, price: 0.61 }
    ];
    return seed.map(function (s) {
      var supply = Math.min(1, Math.log2(s.providers + 1) / 3.5), speed = Math.min(1, s.tps / 90);
      return { model: s.model, providers: s.providers, tps: s.tps, price: s.price,
        signal: Math.max(0.08, 0.62 * supply + 0.38 * speed), live: true };
    });
  }

  /* ---- the SVG dial --------------------------------------------- */
  var W = 1000, H = 110, mid = W / 2, ns = "http://www.w3.org/2000/svg";
  var strip = document.createElementNS(ns, "g");
  svg.setAttribute("viewBox", "0 0 " + W + " " + H);
  svg.appendChild(strip);

  var needle = document.createElementNS(ns, "line");
  needle.setAttribute("x1", mid); needle.setAttribute("y1", 6);
  needle.setAttribute("x2", mid); needle.setAttribute("y2", H - 26);
  needle.setAttribute("stroke", "var(--live)"); needle.setAttribute("stroke-width", "2");
  var arcbg = document.createElementNS(ns, "rect");
  arcbg.setAttribute("x", mid - 60); arcbg.setAttribute("y", H - 16);
  arcbg.setAttribute("height", "4"); arcbg.setAttribute("width", "120"); arcbg.setAttribute("fill", "var(--hairline-2)");
  var arc = document.createElementNS(ns, "rect");
  arc.setAttribute("x", mid - 60); arc.setAttribute("y", H - 16);
  arc.setAttribute("height", "4"); arc.setAttribute("width", "0"); arc.setAttribute("fill", "var(--live)");

  var bands = [], ready = false, stripX = 0, targetX = 0, arcW = 0, arcTarget = 0;

  // band-to-band spacing along the strip, in viewBox units. Wide enough that
  // a long model name (centered under its notch) never runs into its neighbor.
  var SPACING = 250;
  function buildStrip() {
    while (strip.firstChild) strip.removeChild(strip.firstChild);
    if (!bands.length) return;
    bands.forEach(function (b, i) {
      var x = mid + i * SPACING;
      // minor ticks BETWEEN this band's notch and the next (not past the label)
      for (var k = 0; k < 5; k++) {
        var t = document.createElementNS(ns, "line");
        var tx = x + k * (SPACING / 5), major = k === 0;
        t.setAttribute("x1", tx); t.setAttribute("x2", tx);
        t.setAttribute("y1", major ? 14 : 32); t.setAttribute("y2", 56);
        t.setAttribute("stroke", "var(--ink-300)"); t.setAttribute("stroke-width", major ? "2" : "1");
        strip.appendChild(t);
      }
      // label: centered under its own notch so names never collide
      var lbl = document.createElementNS(ns, "text");
      lbl.setAttribute("x", x); lbl.setAttribute("y", 82);
      lbl.setAttribute("text-anchor", "middle");
      lbl.setAttribute("fill", b.live ? "var(--ink-900)" : "var(--ink-400)");
      lbl.setAttribute("font-family", "var(--font-mono)");
      lbl.setAttribute("font-size", "14"); lbl.setAttribute("letter-spacing", "-0.3");
      lbl.textContent = b.model;
      strip.appendChild(lbl);
    });
    strip.appendChild(arcbg); strip.appendChild(arc);
    svg.appendChild(needle);
    ready = true;
  }
  function lockTo(b) {
    var idx = bands.indexOf(b);
    targetX = -(idx * SPACING);
    arcTarget = b.signal * 120;
    if (lockedEl) lockedEl.innerHTML = "LOCKED · <b>" + escapeHtml(b.model) + "</b>";
    if (sigEl) sigEl.innerHTML = "SIGNAL <b>" + Math.round(b.signal * 100) + "</b>/100";
    if (chipEl) chipEl.innerHTML =
      '<span class="meter__k">RATE</span><b>' + fmtPrice(b.price) + ' /1M</b>' +
      '<span class="meter__k">SPEED</span><b>' + (b.live ? Math.round(b.tps) + ' t/s' : '-') + '</b>' +
      '<span class="meter__k">STN</span><b>' + b.providers + '</b>';
    if (REDUCED) { stripX = targetX; arcW = arcTarget; apply(); }
  }
  function apply() {
    strip.setAttribute("transform", "translate(" + stripX + ",0)");
    arc.setAttribute("width", Math.max(0, arcW));
  }
  function frame() {
    if (!ready) return;
    stripX += (targetX - stripX) * 0.12;
    arcW += (arcTarget - arcW) * 0.12;
    apply();
  }

  /* ---- one rAF, offscreen/visibility aware ---------------------- */
  var rafId = null, running = false, visible = false, kicked = false;
  function loop() { frame(); rafId = requestAnimationFrame(loop); }
  function startRAF() { if (REDUCED || running) return; running = true; rafId = requestAnimationFrame(loop); }
  function stopRAF() { running = false; if (rafId) cancelAnimationFrame(rafId); rafId = null; }

  function applyBands(next) {
    bands = next.sort(function (a, b) { return b.signal - a.signal; });
    var liveStations = 0;
    bands.forEach(function (b) { if (b.live) liveStations += b.providers; });
    setOnAir(liveStations > 0);
    if (pingTag) pingTag.textContent = liveStations > 0 ? "on air" : "standing by";
    buildStrip();
    var best = bands.filter(function (b) { return b.live; }).sort(function (a, b) { return b.signal - a.signal; })[0] || bands[0];
    if (best) {
      lockTo(best);
      // Always paint the CORRECT locked state synchronously - the dial must be
      // right even if the rAF intro never runs (e.g. reduced-motion, headless,
      // or before it scrolls into view). The scatter intro is layered on in
      // activate(), and eases back to this same locked position.
      stripX = targetX; arcW = arcTarget; apply();
    }
  }

  var inflightReq = false;
  function load() {
    if (inflightReq) return;
    inflightReq = true;
    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 8000);
    var opt = { signal: ctrl ? ctrl.signal : undefined, cache: "no-store" };

    fetch(BROKER + "/market", opt)
      .then(function (r) { if (!r.ok) throw new Error("http " + r.status); return r.json(); })
      .then(function (data) {
        clearTimeout(to);
        var arr = (data && Array.isArray(data.market)) ? data.market : [];
        var mapped = arr.map(fromMarket).filter(function (b) { return b.model && b.model !== "unknown"; });
        var liveN = mapped.filter(function (b) { return b.live; }).length;
        if (mapped.length && liveN > 0) { applyBands(mapped); return; }
        return fetch(BROKER + "/discover", { cache: "no-store" })
          .then(function (r) { return r.ok ? r.json() : null; })
          .then(function (d) {
            var offers = (d && Array.isArray(d.offers)) ? d.offers : [];
            var agg = offers.length ? aggregateDiscover(offers).filter(function (b) { return b.live; }) : [];
            applyBands(agg.length ? agg : demoBand());
          });
      })
      .catch(function () { clearTimeout(to); applyBands(demoBand()); })
      .then(function () { inflightReq = false; });
  }

  var pollTimer = null;
  function schedule() {
    if (REDUCED) return;
    clearTimeout(pollTimer);
    pollTimer = setTimeout(function () { if (visible) load(); schedule(); }, POLL_MS);
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stopRAF(); else if (visible) startRAF();
  });

  function activate() {
    if (!kicked) { load(); schedule(); }
    kicked = true;
    startRAF();
  }
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        visible = e.isIntersecting;
        if (e.isIntersecting) activate(); else stopRAF();
      });
    }, { threshold: 0.05 });
    io.observe(svg);
  } else { visible = true; activate(); }

  /* initial static paint so the dial isn't blank before first fetch */
  applyBands(demoBand());
})();
