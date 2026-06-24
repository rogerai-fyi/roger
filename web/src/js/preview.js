/* =====================================================================
   RogerAI - PREVIEW live layer (preview.html ONLY).
   Self-contained. No deps. CSS/SVG/Canvas2D only. One shared rAF.

   Live data: broker GET /market (authoritative: signal 0-100, in_flight,
   ttft_ms, success_rate, providers, best_tps, min_price). Falls back to
   GET /discover aggregation, then to a labelled demo band if both are
   empty/unreachable. Meters interpolate only when a value CHANGED.

   Guardrails honored:
     - transform/opacity animation only; one shared rAF.
     - prefers-reduced-motion -> static, no polling, no interpolation.
     - pause when offscreen (IntersectionObserver) + tab hidden.
     - on-air beacon + Ping react to live network state, not a fixed pulse.
   ===================================================================== */
(function () {
  "use strict";

  var root = document.querySelector(".pv");
  if (!root) return;

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var POLL_MS = 30000;
  var BARGLYPH = "▁▂▃▄▅▆▇█"; /* ▁▂▃▄▅▆▇█ */

  /* ---- one shared rAF scheduler -------------------------------------- */
  var consumers = [];
  var rafId = null;
  var running = false;
  function loop(t) {
    for (var i = 0; i < consumers.length; i++) consumers[i](t);
    rafId = requestAnimationFrame(loop);
  }
  function startRAF() { if (REDUCED || running) return; running = true; rafId = requestAnimationFrame(loop); }
  function stopRAF() { running = false; if (rafId) cancelAnimationFrame(rafId); rafId = null; }
  function onFrame(fn) { consumers.push(fn); }

  /* ---- theme toggle (shares roger-theme with the live site) ---------- */
  (function theme() {
    var KEY = "roger-theme";
    var saved = null;
    try { saved = localStorage.getItem(KEY); } catch (e) {}
    var dark = saved ? saved === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
    if (dark) root.setAttribute("data-pv-theme", "dark");
    var btn = document.getElementById("pvTheme");
    if (btn) btn.addEventListener("click", function () {
      var next = root.getAttribute("data-pv-theme") !== "dark";
      if (next) root.setAttribute("data-pv-theme", "dark");
      else root.removeAttribute("data-pv-theme");
      try { localStorage.setItem(KEY, next ? "dark" : "light"); } catch (e) {}
    });
  })();

  /* ---- copy-to-clipboard install boxes ------------------------------- */
  (function copyBoxes() {
    var toast = document.getElementById("pvToast");
    var tt;
    function show(m) { if (!toast) return; toast.textContent = m; toast.classList.add("is-shown"); clearTimeout(tt); tt = setTimeout(function () { toast.classList.remove("is-shown"); }, 1600); }
    function copy(text) {
      if (navigator.clipboard && navigator.clipboard.writeText) return navigator.clipboard.writeText(text);
      return new Promise(function (res, rej) { try { var ta = document.createElement("textarea"); ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0"; document.body.appendChild(ta); ta.select(); document.execCommand("copy"); document.body.removeChild(ta); res(); } catch (e) { rej(e); } });
    }
    var boxes = root.querySelectorAll("[data-pv-copy]");
    for (var i = 0; i < boxes.length; i++) (function (box) {
      box.addEventListener("click", function () {
        var code = box.querySelector("code");
        copy(code ? code.textContent : "").then(function () {
          box.classList.add("is-copied"); show("Copied to clipboard");
          setTimeout(function () { box.classList.remove("is-copied"); }, 1400);
        }).catch(function () { show("Press Cmd/Ctrl-C to copy"); });
      });
    })(boxes[i]);
  })();

  /* =====================================================================
     STATE: the whole console reads from one model object.
     ===================================================================== */
  var bands = [];          // [{model, providers, tps, price, signal(0..1), quality(0..1), inflight, verified, live}]
  var liveStations = 0;    // total stations on air
  var totalInflight = 0;   // sum of in_flight across bands

  function setOnAir(live) { root.setAttribute("data-onair", live ? "live" : "idle"); }

  /* ---- normalize a /market entry to our band model ------------------- */
  function fromMarket(m) {
    var providers = +m.providers || 0;
    var live = providers > 0;
    var sig = m.signal != null ? Math.max(0, Math.min(1, (+m.signal) / 100)) : 0;
    var q = m.quality != null ? Math.max(0, Math.min(1, +m.quality > 1 ? (+m.quality) / 100 : +m.quality)) : 0;
    if (!q && m.success_rate != null) q = Math.max(0, Math.min(1, +m.success_rate > 1 ? (+m.success_rate) / 100 : +m.success_rate));
    return {
      model: m.model || m.band || "unknown",
      providers: providers,
      tps: +(m.best_tps || m.tps) || 0,
      ttft: +m.ttft_ms || 0,
      price: m.min_price != null ? +m.min_price : (m.price_out != null ? +m.price_out : 0),
      signal: live ? (sig || 0.4) : 0,
      quality: live ? (q || 0.5) : 0,
      inflight: +m.in_flight || 0,
      verified: !!(m.confidential || m.verified),
      live: live
    };
  }

  /* ---- /discover aggregation (fallback when /market is empty) -------- */
  function aggregateDiscover(offers) {
    var by = {};
    offers.forEach(function (o) {
      if (!o || !o.model) return;
      var m = by[o.model] || (by[o.model] = { model: o.model, online: 0, tpsN: 0, tpsSum: 0, min: Infinity, verified: false });
      if (o.online !== false) m.online++;
      var tps = +o.tps || 0; if (tps > 0) { m.tpsSum += tps; m.tpsN++; }
      var po = (o.price_out != null ? +o.price_out : +o.price_in);
      if (po != null && !isNaN(po) && po < m.min) m.min = po;
      if (o.confidential) m.verified = true;
    });
    return Object.keys(by).map(function (k) {
      var m = by[k], tps = m.tpsN ? m.tpsSum / m.tpsN : 0, live = m.online > 0;
      var supply = Math.min(1, Math.log2(m.online + 1) / 3.5);
      var speed = Math.min(1, tps / 90);
      return {
        model: m.model, providers: m.online, tps: tps, ttft: 0,
        price: m.min === Infinity ? 0 : m.min,
        signal: live ? Math.max(0.08, 0.62 * supply + 0.38 * speed) : 0,
        quality: live ? Math.max(0.25, Math.min(1, 0.45 * Math.min(1, tps / 80) + 0.55 * Math.min(1, m.online / 4))) : 0,
        inflight: 0, verified: m.verified, live: live
      };
    });
  }

  /* ---- demo band (graceful, clearly labelled) ------------------------ */
  function demoBand() {
    var seed = [
      { model: "qwen3-coder-30b", providers: 6, tps: 58, price: 0.22, inflight: 5, verified: true },
      { model: "qwen3-72b",       providers: 4, tps: 71, price: 0.38, inflight: 2, verified: true },
      { model: "gpt-oss-120b",    providers: 3, tps: 63, price: 0.55, inflight: 3, verified: true },
      { model: "llama3.3-70b",    providers: 5, tps: 44, price: 0.31, inflight: 1, verified: false },
      { model: "deepseek-v3",     providers: 2, tps: 55, price: 0.61, inflight: 0, verified: false },
      { model: "mistral-large",   providers: 0, tps: 0,  price: 0.49, inflight: 0, verified: false }
    ];
    return seed.map(function (s) {
      var supply = Math.min(1, Math.log2(s.providers + 1) / 3.5), speed = Math.min(1, s.tps / 90);
      return {
        model: s.model, providers: s.providers, tps: s.tps, ttft: 0, price: s.price,
        signal: s.providers > 0 ? Math.max(0.08, 0.62 * supply + 0.38 * speed) : 0,
        quality: s.providers > 0 ? Math.max(0.25, Math.min(1, 0.45 * speed + 0.55 * Math.min(1, s.providers / 4))) : 0,
        inflight: s.inflight, verified: s.verified, live: s.providers > 0
      };
    });
  }

  /* =====================================================================
     SIGNATURE 1 - the tuning dial (SVG, scatter -> lock on load).
     ===================================================================== */
  var dial = (function () {
    var svg = document.getElementById("pvDialSvg");
    var lockedEl = document.getElementById("pvDialLocked");
    var sigEl = document.getElementById("pvDialSig");
    var chipEl = document.getElementById("pvDialChip");
    if (!svg) return { update: function () {} };

    var W = 1000, H = 120, mid = W / 2;
    var ns = "http://www.w3.org/2000/svg";
    var strip = document.createElementNS(ns, "g");
    svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    svg.appendChild(strip);

    // fixed center needle + signal arc
    var needle = document.createElementNS(ns, "line");
    needle.setAttribute("x1", mid); needle.setAttribute("y1", 8);
    needle.setAttribute("x2", mid); needle.setAttribute("y2", H - 28);
    needle.setAttribute("stroke", "var(--live)"); needle.setAttribute("stroke-width", "2");
    var arc = document.createElementNS(ns, "rect");
    arc.setAttribute("x", mid - 60); arc.setAttribute("y", H - 18);
    arc.setAttribute("height", "4"); arc.setAttribute("width", "0");
    arc.setAttribute("fill", "var(--live)");
    var arcbg = document.createElementNS(ns, "rect");
    arcbg.setAttribute("x", mid - 60); arcbg.setAttribute("y", H - 18);
    arcbg.setAttribute("height", "4"); arcbg.setAttribute("width", "120");
    arcbg.setAttribute("fill", "var(--hairline-2)");

    var ticks = [];          // tick marks across the strip
    var locked = null;       // current locked band
    var stripX = 0, targetX = 0;
    var arcW = 0, arcTarget = 0;
    var ready = false;

    function buildStrip() {
      while (strip.firstChild) strip.removeChild(strip.firstChild);
      ticks = [];
      if (!bands.length) return;
      var spacing = 150;
      bands.forEach(function (b, i) {
        var x = mid + i * spacing;
        for (var k = 0; k < 5; k++) {
          var t = document.createElementNS(ns, "line");
          var tx = x + k * (spacing / 5);
          var major = k === 0;
          t.setAttribute("x1", tx); t.setAttribute("x2", tx);
          t.setAttribute("y1", major ? 18 : 34); t.setAttribute("y2", 64);
          t.setAttribute("stroke", "var(--ink-300)");
          t.setAttribute("stroke-width", major ? "2" : "1");
          strip.appendChild(t);
        }
        var lbl = document.createElementNS(ns, "text");
        lbl.setAttribute("x", x); lbl.setAttribute("y", 88);
        lbl.setAttribute("fill", b.live ? "var(--ink-900)" : "var(--ink-400)");
        lbl.setAttribute("font-family", "var(--font-mono)");
        lbl.setAttribute("font-size", "15");
        lbl.textContent = b.model;
        strip.appendChild(lbl);
        ticks.push({ x: x, band: b });
      });
      strip.appendChild(arcbg); strip.appendChild(arc);
      svg.appendChild(needle);
      ready = true;
    }

    function lockTo(b) {
      locked = b;
      var idx = bands.indexOf(b);
      targetX = -(idx * 150);            // slide so locked band sits under needle
      arcTarget = b.signal * 120;
      if (lockedEl) lockedEl.innerHTML = "LOCKED · <b>" + escapeHtml(b.model) + "</b>";
      if (sigEl) sigEl.innerHTML = "SIGNAL <b>" + Math.round(b.signal * 100) + "</b>/100";
      if (chipEl) chipEl.innerHTML =
        '<span class="pv-meter__k">RATE</span><b>' + fmtPrice(b.price) + ' /1M</b>' +
        '<span class="pv-meter__k">SPEED</span><b>' + (b.live ? Math.round(b.tps) + ' t/s' : '-') + '</b>' +
        '<span class="pv-meter__k">STN</span><b>' + b.providers + '</b>';
      if (REDUCED) { stripX = targetX; arcW = arcTarget; apply(); }
    }

    function apply() {
      strip.setAttribute("transform", "translate(" + stripX + ",0)");
      arc.setAttribute("width", Math.max(0, arcW));
    }

    function tickFrame() {
      if (!ready) return;
      stripX += (targetX - stripX) * 0.12;
      arcW += (arcTarget - arcW) * 0.12;
      // idle carrier drift on the needle (transform only)
      apply();
    }

    return {
      update: function () {
        buildStrip();
        // lock onto strongest live band, else first
        var best = bands.filter(function (b) { return b.live; }).sort(function (a, b) { return b.signal - a.signal; })[0] || bands[0];
        if (best) {
          if (REDUCED) { stripX = 0; } else { stripX = 600; } // start scattered to the side, then ease in
          lockTo(best);
        }
        onFrame(tickFrame);
      }
    };
  })();

  /* =====================================================================
     SIGNATURE 2 - the live signal field (interpolating meter wall).
     ===================================================================== */
  var field = (function () {
    var listEl = document.getElementById("pvMarketList");
    var statusEl = document.getElementById("pvBandStatus");
    var statusTextEl = document.getElementById("pvBandStatusText");
    var footEl = document.getElementById("pvBandFoot");
    if (!listEl) return { render: function () {}, frame: function () {} };

    var rows = []; // [{band, el, sigEl, cur:{signal,tps,q}, tgt:{...}, head}]

    function towerHTML(level, busy) {
      var cells = 8, filled = Math.max(0, Math.min(cells, Math.round(level * cells))), html = "";
      for (var i = 0; i < cells; i++) {
        if (i < filled) {
          var head = i === filled - 1;
          var g = BARGLYPH[Math.min(7, Math.round((i / (cells - 1)) * 7))];
          html += '<span class="' + (head ? "head" : "on") + '" data-head="' + (head ? "1" : "0") + '" data-busy="' + (busy ? "1" : "0") + '">' + g + "</span>";
        } else html += '<span class="off">·</span>';
      }
      return html;
    }
    function qdots(q) { var n = Math.round(q * 5), s = ""; for (var i = 0; i < 5; i++) s += i < n ? "●" : "○"; return s; }
    function fmtP(p) { return p ? "$" + p.toFixed(2) : "free"; }

    function render(channels, status, statusLive, foot) {
      var top = channels.slice(0, 6);
      listEl.innerHTML = "";
      rows = [];
      top.forEach(function (b) {
        var li = document.createElement("li");
        li.className = "pv-row" + (b.live ? "" : " pv-row--idle");
        var dot = b.live ? '<span class="pv-dot pv-dot--on">●</span>' : '<span class="pv-dot pv-dot--off">○</span>';
        var cs = b.verified ? ' <span class="pv-cs" title="lineage-verified">◆</span>' : "";
        var prov = b.live ? '<span class="pv-prov">' + b.providers + ' on air · ' + b.inflight + ' in flight</span>' : '<span class="pv-prov">idle</span>';
        var speed = b.live ? '<b class="pv-tps">' + Math.round(b.tps) + '</b><span class="pv-unit"> t/s</span>' : '<span class="pv-unit">-</span>';
        var price = b.live ? '<b class="pv-price">' + fmtP(b.price) + '</b><span class="pv-unit"> /1M</span>' : '<span class="pv-unit">' + fmtP(b.price) + ' /1M</span>';
        li.innerHTML =
          '<span class="pv-cell pv-cell--model">' + dot + '<span class="pv-model">' + escapeHtml(b.model) + cs + '</span>' + prov + '</span>' +
          '<span class="pv-cell pv-cell--signal"><span class="pv-sig">' + towerHTML(b.signal, b.inflight > 0) + '</span></span>' +
          '<span class="pv-cell pv-cell--speed">' + speed + '</span>' +
          '<span class="pv-cell pv-cell--quality"><span class="pv-qdots' + (b.live ? '' : ' pv-qdots--idle') + '">' + qdots(b.quality) + '</span></span>' +
          '<span class="pv-cell pv-cell--price">' + price + '</span>';
        listEl.appendChild(li);
        rows.push({ band: b, sigEl: li.querySelector(".pv-sig") });
      });
      if (statusTextEl) statusTextEl.textContent = status;
      if (statusEl) statusEl.classList.toggle("is-live", !!statusLive);
      if (footEl) footEl.innerHTML = foot;
    }

    // VU: head cell breathes; faster/sharper when the channel is busy (in_flight).
    var phase = 0;
    function frame() {
      phase += 0.04;
      for (var i = 0; i < rows.length; i++) {
        var head = rows[i].sigEl && rows[i].sigEl.querySelector('[data-head="1"]');
        if (!head) continue;
        var busy = head.getAttribute("data-busy") === "1";
        var speed = busy ? 2.4 : 1;          // motion MEANS load
        var s = 0.5 + 0.5 * Math.sin(phase * speed + i * 0.9);
        head.style.opacity = (0.5 + 0.5 * s).toFixed(3);
      }
    }
    onFrame(frame);
    return { render: render };
  })();

  /* =====================================================================
     SIGNATURE 3 - oscilloscope strip under the terminal demo.
     Canvas2D, single rAF consumer; livelier when the band is busy.
     ===================================================================== */
  (function scope() {
    var cv = document.getElementById("pvScope");
    if (!cv || !cv.getContext) return;
    var ctx = cv.getContext("2d");
    var dpr = Math.min(2, window.devicePixelRatio || 1);
    function size() {
      var r = cv.getBoundingClientRect();
      cv.width = Math.max(1, r.width * dpr); cv.height = Math.max(1, r.height * dpr);
    }
    size(); window.addEventListener("resize", size, { passive: true });
    var x = 0;
    var pts = [];
    function frame() {
      var w = cv.width, h = cv.height, mid = h / 2;
      ctx.clearRect(0, 0, w, h);
      // amplitude tracks live activity: calmer when quiet, lively when on air/busy
      var amp = (liveStations > 0 ? 0.32 : 0.06) + Math.min(0.4, totalInflight * 0.05);
      x += 0.18;
      var col = getComputedStyle(root).getPropertyValue("--live") || "#E0231C";
      var ink = getComputedStyle(root).getPropertyValue("--ink-300") || "#ccc";
      ctx.lineWidth = 1.4 * dpr;
      ctx.strokeStyle = liveStations > 0 ? col.trim() : ink.trim();
      ctx.beginPath();
      for (var px = 0; px <= w; px += 2 * dpr) {
        var t = px / w * 6 + x;
        var y = mid + Math.sin(t) * amp * mid * (0.6 + 0.4 * Math.sin(t * 2.3));
        if (px === 0) ctx.moveTo(px, y); else ctx.lineTo(px, y);
      }
      ctx.stroke();
    }
    if (REDUCED) {
      // static "lock" trace
      var w = cv.width, h = cv.height, mid = h / 2;
      ctx.strokeStyle = "#999"; ctx.lineWidth = 1.4 * dpr; ctx.beginPath();
      ctx.moveTo(0, mid); ctx.lineTo(w, mid); ctx.stroke();
    } else {
      onFrame(frame);
    }
  })();

  /* =====================================================================
     APPLY a fresh dataset to every instrument.
     ===================================================================== */
  function applyBands(next, status, statusLive, foot) {
    bands = next.sort(function (a, b) { return b.signal - a.signal; });
    liveStations = 0; totalInflight = 0;
    bands.forEach(function (b) { if (b.live) liveStations += b.providers; totalInflight += b.inflight || 0; });
    setOnAir(liveStations > 0);
    var on = bands.filter(function (b) { return b.live; }).length;
    field.render(bands, status, statusLive, foot);
    dial.update();
  }

  /* =====================================================================
     FETCH: /market -> /discover -> demo. Re-tune every 30s.
     ===================================================================== */
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
        if (mapped.length && liveN > 0) {
          applyBands(mapped,
            liveN + " band" + (liveN === 1 ? "" : "s") + " on air · live from /market", true,
            'live from <span class="hot">broker.rogerai.fyi/market</span> · signal 0-100 · auto re-tune 30s');
          return;
        }
        // /market empty -> try /discover before falling back to demo
        return fetch(BROKER + "/discover", { cache: "no-store" })
          .then(function (r) { return r.ok ? r.json() : null; })
          .then(function (d) {
            var offers = (d && Array.isArray(d.offers)) ? d.offers : [];
            var agg = offers.length ? aggregateDiscover(offers).filter(function (b) { return b.live; }) : [];
            if (agg.length) {
              applyBands(agg, agg.length + " band(s) on air · from /discover", true,
                'live from <span class="hot">broker.rogerai.fyi/discover</span> · aggregated');
            } else {
              applyBands(demoBand(), "the band is quiet · preview of how it reads on air", false,
                'broker reachable · <span class="hot">no stations on air yet</span> · showing a representative band');
            }
          });
      })
      .catch(function () {
        clearTimeout(to);
        applyBands(demoBand(), "preview band · couldn’t reach the broker just now", false,
          'couldn’t reach <span class="hot">broker.rogerai.fyi</span> · showing a representative band');
      })
      .then(function () { inflightReq = false; });
  }

  /* ---- polling (skipped entirely under reduced motion) --------------- */
  var pollTimer = null, visible = false, kicked = false;
  function schedule() {
    if (REDUCED) return;
    clearTimeout(pollTimer);
    pollTimer = setTimeout(function () { if (visible) load(); schedule(); }, POLL_MS);
  }
  var refreshBtn = document.getElementById("pvRefresh");
  if (refreshBtn) refreshBtn.addEventListener("click", function () { load(); });

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) stopRAF(); else if (visible) startRAF();
  });

  function activate() {
    if (!kicked) { kicked = true; load(); schedule(); }
    startRAF();
  }
  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        visible = e.isIntersecting;
        if (e.isIntersecting) activate(); else stopRAF();
      });
    }, { threshold: 0.05 });
    io.observe(root);
  } else { visible = true; activate(); }

  // initial paint with demo band so nothing is blank before first fetch
  applyBands(demoBand(), "tuning in to the broker…", false,
    'tuning in to <span class="hot">broker.rogerai.fyi/market</span>…');

  /* ---- utils --------------------------------------------------------- */
  function fmtPrice(p) { return p ? "$" + p.toFixed(2) : "free"; }
  function escapeHtml(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
})();
