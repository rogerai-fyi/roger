/* =====================================================================
   RogerAI - the full-bleed tuning dial: the signature interactive hero.
   Self-served (CSP script-src 'self'). No deps. SVG + CSS transforms only.

   Sweep the dial like a real radio: pointer/touch drag (and hover-scrub)
   moves a frequency strip under a fixed red needle; release SNAPS to the
   nearest band (detent). Arrow keys tune station-to-station; Enter opens
   the locked station at /bands.html#band=<model>. The readout tracks the
   band under the needle live: LOCKED, SIGNAL nn/100, RATE/SPEED/STN.

   - data: broker GET /market (authoritative signal 0-100) -> /discover
     aggregation -> a representative demo band (the band is empty today,
     so the demo bands are tuned to feel real).
   - one rAF; pauses offscreen + when tab hidden.
   - reduced-motion: static, pre-locked on the strongest band, no sweep,
     no momentum, no rAF; drag/keys still re-tune (instant, no inertia).
   ===================================================================== */
(function () {
  "use strict";

  var dial = document.getElementById("dial");
  var svg = document.getElementById("dialSvg");
  var lockedEl = document.getElementById("dialLocked");
  var sigEl = document.getElementById("dialSig");
  var chipEl = document.getElementById("dialChip");
  var pingTag = document.getElementById("pingTag");
  if (!svg || !dial) return;

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var POLL_MS = 30000;

  function setOnAir(live) { document.body.setAttribute("data-onair", live ? "live" : "idle"); }
  function escapeHtml(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
  function fmtPrice(p) { return p ? "$" + (+p).toFixed(2) : "free"; }
  function clamp(v, a, b) { return v < a ? a : v > b ? b : v; }

  /* ---- normalize a /market entry -------------------------------- */
  function fromMarket(m) {
    var providers = +m.providers || 0;
    var live = providers > 0;
    var sig = m.signal != null ? clamp((+m.signal) / 100, 0, 1) : 0;
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
    // The broker market is empty today; these are tuned to read like a real,
    // breathing band (varied supply / speed / price across familiar callsigns).
    var seed = [
      { model: "qwen3-coder-30b", providers: 7, tps: 61, price: 0.22 },
      { model: "llama3.3-70b",    providers: 5, tps: 44, price: 0.31 },
      { model: "qwen3-72b",       providers: 4, tps: 73, price: 0.38 },
      { model: "gpt-oss-120b",    providers: 3, tps: 66, price: 0.55 },
      { model: "deepseek-v3",     providers: 2, tps: 52, price: 0.61 },
      { model: "mixtral-8x7b",    providers: 6, tps: 88, price: 0.18 },
      { model: "gemma3-27b",      providers: 4, tps: 57, price: 0.27 }
    ];
    return seed.map(function (s) {
      var supply = Math.min(1, Math.log2(s.providers + 1) / 3.5), speed = Math.min(1, s.tps / 90);
      return { model: s.model, providers: s.providers, tps: s.tps, price: s.price,
        signal: Math.max(0.12, 0.6 * supply + 0.4 * speed), live: true };
    });
  }

  /* ---- the SVG dial: a fixed strip in viewBox units, scrolled in px -- */
  var ns = "http://www.w3.org/2000/svg";
  // logical viewBox; width grows with band count so ticks stay crisp.
  var VBH = 132, MIDY = 0; // mid filled after measuring
  var SPACING = 260;       // band-to-band distance in viewBox units
  var STRIP_PAD = 600;     // lead/trail runway so end bands can center
  // a fixed, full-width graduated baseline ruler (the continuous frequency
  // scale of a real tuner) sits UNDER the scrolling band strip so the face is
  // never empty, even left of the first band.
  var ruler = document.createElementNS(ns, "g");
  svg.appendChild(ruler);
  var strip = document.createElementNS(ns, "g");
  svg.appendChild(strip);

  var bands = [], ready = false;
  var faceW = 0;           // measured face width in viewBox units (== px here)
  // tuning position in "band units" (0..n-1). Float while sweeping.
  var pos = 0, target = 0, vel = 0;
  var lastIdx = -1;

  function measure() {
    var r = svg.getBoundingClientRect();
    faceW = r.width || 1000;
    svg.setAttribute("viewBox", "0 0 " + faceW + " " + VBH);
    MIDY = faceW / 2;
  }

  var baseY = 26, faceBottom = 86;
  // the fixed continuous scale: fine graduations every ~26px across the face,
  // a taller mark every 5th. Rebuilt on resize (faceW changes).
  function buildRuler() {
    while (ruler.firstChild) ruler.removeChild(ruler.firstChild);
    var step = 26, n = Math.ceil(faceW / step) + 1;
    for (var i = 0; i < n; i++) {
      var x = i * step, major = i % 5 === 0;
      var t = document.createElementNS(ns, "line");
      t.setAttribute("x1", x); t.setAttribute("x2", x);
      t.setAttribute("y1", faceBottom - (major ? 12 : 6)); t.setAttribute("y2", faceBottom);
      t.setAttribute("stroke", "var(--hairline-2)");
      t.setAttribute("stroke-width", "1");
      t.setAttribute("class", "dial__grad");
      ruler.appendChild(t);
    }
    // baseline rule across the bottom of the face
    var base = document.createElementNS(ns, "line");
    base.setAttribute("x1", 0); base.setAttribute("x2", faceW);
    base.setAttribute("y1", faceBottom); base.setAttribute("y2", faceBottom);
    base.setAttribute("stroke", "var(--hairline)"); base.setAttribute("stroke-width", "1");
    ruler.appendChild(base);
  }

  function buildStrip() {
    buildRuler();
    while (strip.firstChild) strip.removeChild(strip.firstChild);
    if (!bands.length) return;
    bands.forEach(function (b, i) {
      var x = i * SPACING; // strip-local; centered via translate at runtime
      // the band notch: one prominent full-height mark over the continuous
      // ruler. (The fine graduations come from the fixed ruler underneath.)
      var t = document.createElementNS(ns, "line");
      t.setAttribute("x1", x); t.setAttribute("x2", x);
      t.setAttribute("y1", baseY); t.setAttribute("y2", faceBottom);
      t.setAttribute("stroke", b.live ? "var(--ink-500)" : "var(--ink-300)");
      t.setAttribute("stroke-width", "1.5");
      t.setAttribute("class", "dial__tick dial__tick--major");
      t.setAttribute("data-i", i);
      strip.appendChild(t);
      // signal pip above the notch: a short bar scaled to this band's signal
      var pipH = 8 + Math.round(b.signal * 30);
      var pip = document.createElementNS(ns, "rect");
      pip.setAttribute("x", x - 1.5); pip.setAttribute("width", "3");
      pip.setAttribute("y", baseY - pipH); pip.setAttribute("height", pipH);
      pip.setAttribute("rx", "1.5");
      pip.setAttribute("fill", b.live ? "var(--ink-400)" : "var(--ink-300)");
      pip.setAttribute("class", "dial__pip"); pip.setAttribute("data-i", i);
      strip.appendChild(pip);
      // callsign label, centered under its own notch
      var lbl = document.createElementNS(ns, "text");
      lbl.setAttribute("x", x); lbl.setAttribute("y", faceBottom + 24);
      lbl.setAttribute("text-anchor", "middle");
      lbl.setAttribute("fill", b.live ? "var(--ink-900)" : "var(--ink-400)");
      lbl.setAttribute("font-family", "var(--font-mono)");
      lbl.setAttribute("font-size", "15"); lbl.setAttribute("letter-spacing", "-0.4");
      lbl.setAttribute("class", "dial__lbl"); lbl.setAttribute("data-i", i);
      lbl.textContent = b.model;
      strip.appendChild(lbl);
    });
    ready = true;
    apply();
  }

  // px translate that puts band #pos under the needle (face center)
  function stripTranslate(p) { return MIDY - p * SPACING; }

  function apply() {
    if (!ready) return;
    strip.setAttribute("transform", "translate(" + stripTranslate(pos) + ",0)");
    // highlight the focused band (nearest to needle)
    var idx = Math.round(clamp(pos, 0, bands.length - 1));
    // fractional "tune glow": how locked we are (1 at a detent, dips between)
    var frac = 1 - Math.min(1, Math.abs(pos - idx) * 2);
    dial.style.setProperty("--lock", frac.toFixed(3));
    if (idx !== lastIdx) {
      lastIdx = idx;
      var prev = strip.querySelector(".dial__lbl--on");
      if (prev) prev.classList.remove("dial__lbl--on");
      var on = strip.querySelector('.dial__lbl[data-i="' + idx + '"]');
      if (on) on.classList.add("dial__lbl--on");
      readout(bands[idx]);
    }
  }

  function readout(b) {
    if (!b) return;
    if (lockedEl) lockedEl.innerHTML = "LOCKED · <b>" + escapeHtml(b.model) + "</b>";
    if (sigEl) sigEl.innerHTML = "SIGNAL <b>" + Math.round(b.signal * 100) + "</b>/100";
    if (chipEl) chipEl.innerHTML =
      '<span class="meter__k">RATE</span><b>' + fmtPrice(b.price) + ' /1M</b>' +
      '<span class="meter__k">SPEED</span><b>' + (b.live ? Math.round(b.tps) + ' t/s' : '-') + '</b>' +
      '<span class="meter__k">STN</span><b>' + b.providers + '</b>';
    // signal meter (CSS var, 0..1) + ARIA
    dial.style.setProperty("--sig", b.signal.toFixed(3));
    dial.setAttribute("aria-valuenow", String(lastIdx));
    dial.setAttribute("aria-valuetext", b.model + ", signal " + Math.round(b.signal * 100) + " of 100");
  }

  function bandIndex() { return Math.round(clamp(pos, 0, bands.length - 1)); }
  function currentBand() { return bands[bandIndex()]; }
  function openLocked() {
    var b = currentBand();
    if (b) window.location.href = "/bands.html#band=" + encodeURIComponent(b.model);
  }

  /* ---- sweep physics: one eased target + momentum, snap on release --- */
  var dragging = false, hovering = false, lastX = 0, lastT = 0;
  var DETENT = 0.16; // ease toward the target each frame

  function setTargetBand(i, instant) {
    target = clamp(i, 0, bands.length - 1);
    if (instant || REDUCED) { pos = target; vel = 0; apply(); }
  }
  function snapNearest() { target = clamp(Math.round(pos), 0, bands.length - 1); }

  function frame() {
    if (!ready) return;
    if (dragging) {
      // while dragging, pos is driven directly by pointer; just settle micro-jitter
      apply();
    } else if (Math.abs(vel) > 0.0004) {
      // momentum: glide, then let the detent pull to the nearest band
      pos = clamp(pos + vel, -0.4, bands.length - 1 + 0.4);
      vel *= 0.92;
      if (pos <= 0 || pos >= bands.length - 1) vel = 0;
      target = clamp(Math.round(pos), 0, bands.length - 1);
      apply();
    } else {
      var d = target - pos;
      if (Math.abs(d) > 0.0008) { pos += d * DETENT; apply(); }
      else if (pos !== target) { pos = target; apply(); }
    }
  }

  /* ---- one rAF, offscreen/visibility aware ---------------------- */
  var rafId = null, running = false, visible = false, kicked = false;
  function loop() { frame(); rafId = requestAnimationFrame(loop); }
  function startRAF() { if (REDUCED || running) return; running = true; rafId = requestAnimationFrame(loop); }
  function stopRAF() { running = false; if (rafId) cancelAnimationFrame(rafId); rafId = null; }

  /* ---- pointer / touch tuning ----------------------------------- */
  function pxToBandDelta(dx) { return -dx / SPACING; } // strip moves opposite the drag

  var downX = 0, moved = false;
  function onDown(e) {
    if (e.button != null && e.button !== 0) return;
    measure();
    dragging = true; vel = 0; moved = false;
    lastX = downX = e.clientX; lastT = e.timeStamp || performance.now();
    dial.classList.add("is-tuning");
    try { dial.setPointerCapture && dial.setPointerCapture(e.pointerId); } catch (_) {}
    e.preventDefault();
  }
  function onMove(e) {
    if (!dragging) return;
    var x = e.clientX, now = e.timeStamp || performance.now();
    var dx = x - lastX, dt = Math.max(1, now - lastT);
    if (Math.abs(x - downX) > 4) moved = true;
    // convert px->viewBox (face is 1:1 since viewBox width == face px) then to bands
    var dband = pxToBandDelta(dx);
    pos = clamp(pos + dband, -0.4, bands.length - 1 + 0.4);
    // velocity in band-units per frame (~16ms), eased (ignored in reduced mode)
    if (!REDUCED) vel = pxToBandDelta(dx) * (16 / dt) * 0.6 + vel * 0.4;
    lastX = x; lastT = now;
    apply(); // reduced mode tracks the drag continuously; it snaps on release
    e.preventDefault();
  }
  function onUp(e) {
    if (!dragging) return;
    dragging = false;
    dial.classList.remove("is-tuning");
    try { dial.releasePointerCapture && dial.releasePointerCapture(e.pointerId); } catch (_) {}
    if (REDUCED) { snapNearest(); pos = target; apply(); return; }
    if (Math.abs(vel) < 0.0008) snapNearest(); // gentle release -> snap now
    if (!running && visible) startRAF();
  }

  // hover-scrub: sweep the WHOLE band by moving the mouse across the dial, like
  // running a finger along a real tuner. Cursor x across the face maps to the
  // full spectrum (left edge -> first band, right edge -> last band); the rAF
  // eases pos toward it (weighted, never jumpy). A center dead-zone keeps the
  // locked band steady when the cursor rests near the needle. Snaps on leave.
  function onHoverMove(e) {
    if (dragging || REDUCED) return;
    var r = svg.getBoundingClientRect();
    var rel = clamp((e.clientX - r.left) / (r.width || 1), 0, 1); // 0..1
    var span = Math.max(1, bands.length - 1);
    // ease-out from face center so small moves near the needle barely tune
    var c = (rel - 0.5) * 2;             // -1..1
    var shaped = Math.sign(c) * c * c;   // -1..1, gentle near center
    target = clamp((shaped * 0.5 + 0.5) * span, 0, span);
  }
  function onHoverEnter() { hovering = true; if (!running && visible) startRAF(); }
  function onHoverLeave() { hovering = false; if (!dragging) snapNearest(); }

  // pointer events cover mouse + touch + pen
  dial.addEventListener("pointerdown", onDown);
  window.addEventListener("pointermove", onMove, { passive: false });
  window.addEventListener("pointerup", onUp);
  window.addEventListener("pointercancel", onUp);
  dial.addEventListener("pointerenter", onHoverEnter);
  dial.addEventListener("pointerleave", onHoverLeave);
  dial.addEventListener("pointermove", onHoverMove);

  // keyboard: arrows tune band-to-band, Home/End to the rails, Enter opens
  dial.addEventListener("keydown", function (e) {
    if (!ready) return;
    var i = bandIndex();
    switch (e.key) {
      case "ArrowRight": case "ArrowUp": setTargetBand(i + 1); break;
      case "ArrowLeft": case "ArrowDown": setTargetBand(i - 1); break;
      case "Home": setTargetBand(0); break;
      case "End": setTargetBand(bands.length - 1); break;
      case "PageUp": setTargetBand(i + 3); break;
      case "PageDown": setTargetBand(i - 3); break;
      case "Enter": case " ": openLocked(); e.preventDefault(); return;
      default: return;
    }
    e.preventDefault();
    if (!running && visible) startRAF();
  });
  // click a centered band to open it (drag is the tune gesture; a clean click
  // with no sweep means "open this station")
  dial.addEventListener("click", function () {
    if (!ready || moved) return; // a drag is a tune, not an open
    if (Math.abs(pos - bandIndex()) < 0.12 && Math.abs(vel) < 0.01) openLocked();
  });

  /* ---- data hydration ------------------------------------------- */
  function applyBands(next) {
    bands = next.sort(function (a, b) { return b.signal - a.signal; });
    var liveStations = 0;
    bands.forEach(function (b) { if (b.live) liveStations += b.providers; });
    setOnAir(liveStations > 0);
    if (pingTag) pingTag.textContent = liveStations > 0 ? "on air" : "standing by";
    dial.setAttribute("aria-valuemin", "0");
    dial.setAttribute("aria-valuemax", String(Math.max(0, bands.length - 1)));
    measure();
    buildStrip();
    // strongest live band sits first after the sort -> lock onto index 0
    var best = bands.filter(function (b) { return b.live; })[0] ? 0 : 0;
    lastIdx = -1;
    pos = target = best; vel = 0;
    apply();
    readout(bands[best]);
    lastIdx = best;
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
    pollTimer = setTimeout(function () { if (visible && !dragging && !hovering) load(); schedule(); }, POLL_MS);
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stopRAF(); else if (visible) startRAF();
  });
  window.addEventListener("resize", function () { measure(); buildRuler(); apply(); });

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
    }, { threshold: 0.02 });
    io.observe(dial);
  } else { visible = true; activate(); }

  /* initial static paint so the dial isn't blank before first fetch */
  measure();
  applyBands(demoBand());
})();
