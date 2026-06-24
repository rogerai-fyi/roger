/* =====================================================================
   RogerAI - /bands : the live station directory + band scanner + QSL card.
   Self-served (CSP script-src 'self'). No deps. SVG + CSS transforms + DOM.

   Data sources (CORS-open, public):
     GET /market   -> per-band: model, providers, in_flight, min_price,
                      best_tps, ttft_ms, quality, success_rate, signal 0-100
     GET /discover -> per-station offers: node_id, region, hw, model,
                      price_in, price_out, ctx, online, confidential,
                      free_now, scheduled, tps, ttft_ms, quality
   Strategy: /market is authoritative for the band ROWS (signal 0-100);
   /discover supplies the per-station LOG for the QSL detail card and the
   in/out price split + ctx + free-now. A labelled demo band is the last
   resort when the broker is empty or unreachable.

   PRIVACY (no PII): /discover exposes a raw node_id + free-text region.
   We NEVER render either. The operator becomes a pseudonymous @callsign
   derived from a stable hash of node_id, plus a COARSE macro-region only.
   No names, precise location, host, IP, email, or account/wallet ids.

   Motion discipline: one shared rAF (scanner parallax + needle drift);
   paused offscreen + when tab hidden; full prefers-reduced-motion fallback
   (pre-locked scanner, no drift, no poll); page usable with JS/network off.
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var POLL_MS = 30000;
  var BARGLYPH = "▁▂▃▄▅▆▇█"; // ▁▂▃▄▅▆▇█

  /* ---- DOM handles ---------------------------------------------- */
  var listEl   = document.getElementById("bandList");
  if (!listEl) return;
  var statusTxt = document.getElementById("bandStatusText");
  var statusWrap = document.getElementById("bandStatus");
  var emptyEl  = document.getElementById("bandEmpty");
  var refreshBtn = document.getElementById("bandRefresh");
  var searchEl = document.getElementById("bandSearch");
  var clearEl  = document.getElementById("bandClear");
  var sortEl   = document.getElementById("bandSort");
  var fltFree  = document.getElementById("fltFree");
  var fltConf  = document.getElementById("fltConf");
  var fltVer   = document.getElementById("fltVer");
  var fltOn    = document.getElementById("fltOn");

  /* ---- tiny helpers --------------------------------------------- */
  function el(tag, cls, html) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (html != null) n.innerHTML = html;
    return n;
  }
  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }
  function fmtPrice(p) {
    if (p == null || isNaN(p)) return "-";
    if (!p) return "free";
    return "$" + (+p).toFixed(2);
  }

  /* ---- PII firewall: callsign + coarse region ------------------- */
  // Stable, deterministic pseudonym from node_id (FNV-1a -> phonetic-ish
  // callsign). The raw node_id NEVER reaches the DOM.
  function hashStr(s) {
    var h = 2166136261;
    s = String(s || "");
    for (var i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = (h + ((h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))) >>> 0;
    }
    return h >>> 0;
  }
  var CS_CONS = "kqxzrtwmnbvghd";
  var CS_VOW = "aeiou";
  function callsign(nodeId) {
    var h = hashStr(nodeId);
    var s = "";
    // 2 letters + 2 digits + 2 letters: looks like a ham callsign, e.g. @ka42rt
    s += CS_CONS[h % CS_CONS.length]; h = (h / CS_CONS.length) | 0;
    s += CS_VOW[h % CS_VOW.length];   h = (h / CS_VOW.length) | 0;
    var n = hashStr(nodeId + "#");
    s += (n % 90 + 10);               n = (n / 100) | 0;
    s += CS_CONS[n % CS_CONS.length]; n = (n / CS_CONS.length) | 0;
    s += CS_CONS[n % CS_CONS.length];
    return "@" + s;
  }
  // Coarsen any region string down to a small macro-region set. Anything
  // we can't confidently bucket becomes a generic continental hint, never
  // the raw value (which could carry a city / datacenter / precise hint).
  function coarseRegion(region) {
    var r = String(region || "").toLowerCase();
    if (!r) return "??";
    var map = [
      [/(us-?w|usw|west|sf|sjc|lax|sea|pdx|california|oregon|us-west)/, "US-W"],
      [/(us-?e|use|east|nyc|iad|atl|mia|virginia|us-east)/, "US-E"],
      [/(us-?c|central|chi|dfw|texas|us-central)/, "US-C"],
      [/(\bus\b|usa|united states|america)/, "US"],
      [/(\buk\b|gb|london|lon|britain|england)/, "UK"],
      [/(\bde\b|germany|deutsch|fra|frankfurt|berlin|munich)/, "DE"],
      [/(\bnl\b|netherlands|amsterdam|ams)/, "NL"],
      [/(\bfr\b|france|paris|par)/, "FR"],
      [/(\beu\b|europe|euro)/, "EU"],
      [/(\bca\b|canada|toronto|montreal|yyz)/, "CA"],
      [/(\bau\b|australia|sydney|syd|melbourne)/, "AU"],
      [/(\bjp\b|japan|tokyo|nrt|osaka)/, "JP"],
      [/(\bsg\b|singapore|sin)/, "SG"],
      [/(\bin\b|india|mumbai|bom|bangalore)/, "IN"],
      [/(\bbr\b|brazil|sao|gru)/, "BR"],
      [/(\bkr\b|korea|seoul|icn)/, "KR"]
    ];
    for (var i = 0; i < map.length; i++) if (map[i][0].test(r)) return map[i][1];
    // unknown: only reveal a continent-grain hint if obvious, else ??
    if (/asia/.test(r)) return "ASIA";
    return "??";
  }

  /* ---- normalize ------------------------------------------------ */
  // a "band" is the directory row; "stations" is the per-band station log.
  function fromMarket(rows) {
    return rows.map(function (m) {
      var providers = +m.providers || 0;
      var live = providers > 0;
      var sig = m.signal != null ? Math.max(0, Math.min(100, +m.signal)) : 0;
      var sr = m.success_rate != null ? +m.success_rate : 1;
      if (sr > 1) sr = sr / 100;
      return {
        model: m.model || m.band || "unknown",
        providers: providers,
        live: live,
        signal: live ? sig : 0,
        priceIn: m.min_price != null ? +m.min_price : null,
        priceOut: null,           // /market only exposes min input price
        tps: +(m.best_tps || m.tps) || 0,
        ttft: +m.ttft_ms || 0,
        success: sr,
        verified: false,          // enriched from /discover stations
        confidential: false,
        freeNow: false,
        ctx: 0,
        stations: []
      };
    }).filter(function (b) { return b.model && b.model !== "unknown"; });
  }

  // Group /discover offers into bands + a privacy-safe station log.
  function fromDiscover(offers) {
    var by = {};
    offers.forEach(function (o) {
      if (!o || !o.model) return;
      var b = by[o.model] || (by[o.model] = {
        model: o.model, live: 0, total: 0, minIn: Infinity, minOut: Infinity,
        tps: 0, ttft: Infinity, conf: false, free: false, ctx: 0,
        success: 1, stations: []
      });
      var online = o.online !== false;
      b.total++;
      if (online) b.live++;
      var pin = (o.price_in != null ? +o.price_in : null);
      var pout = (o.price_out != null ? +o.price_out : null);
      if (pin != null && !isNaN(pin) && pin < b.minIn) b.minIn = pin;
      if (pout != null && !isNaN(pout) && pout < b.minOut) b.minOut = pout;
      var tps = +o.tps || 0; if (tps > b.tps) b.tps = tps;
      var tt = +o.ttft_ms || 0; if (tt > 0 && tt < b.ttft) b.ttft = tt;
      if (o.confidential) b.conf = true;
      if (o.free_now) b.free = true;
      if (+o.ctx > b.ctx) b.ctx = +o.ctx;
      // privacy-safe station record
      b.stations.push({
        callsign: callsign(o.node_id),
        region: coarseRegion(o.region),
        online: online,
        priceIn: pin, priceOut: pout,
        tps: tps, ttft: tt,
        quality: o.quality != null ? +o.quality : null,
        confidential: !!o.confidential,
        free: !!o.free_now,
        scheduled: !!o.scheduled,
        ctx: +o.ctx || 0
      });
    });
    return Object.keys(by).map(function (k) {
      var b = by[k];
      // local signal fallback when only /discover is available
      var supply = Math.min(1, Math.log2(b.live + 1) / 3.5);
      var speed = Math.min(1, b.tps / 90);
      var sig = b.live > 0 ? Math.max(8, Math.round((0.62 * supply + 0.38 * speed) * 100)) : 0;
      b.stations.sort(function (a, c) {
        if (a.online !== c.online) return a.online ? -1 : 1;
        return (a.priceIn || 1e9) - (c.priceIn || 1e9);
      });
      return {
        model: b.model, providers: b.live, live: b.live > 0, signal: sig,
        priceIn: b.minIn === Infinity ? null : b.minIn,
        priceOut: b.minOut === Infinity ? null : b.minOut,
        tps: b.tps, ttft: b.ttft === Infinity ? 0 : b.ttft, success: 1,
        verified: b.conf, confidential: b.conf, freeNow: b.free, ctx: b.ctx,
        stations: b.stations
      };
    });
  }

  // merge /market rows (authoritative signal) with /discover detail.
  function merge(marketBands, discoverBands) {
    var dByModel = {};
    discoverBands.forEach(function (d) { dByModel[d.model] = d; });
    var out = marketBands.map(function (b) {
      var d = dByModel[b.model];
      if (d) {
        b.stations = d.stations;
        b.priceIn = b.priceIn != null ? b.priceIn : d.priceIn;
        b.priceOut = d.priceOut;
        b.confidential = d.confidential;
        b.verified = d.confidential;   // confidential routes carry the ◆
        b.freeNow = d.freeNow;
        b.ctx = d.ctx;
        if (!b.tps && d.tps) b.tps = d.tps;
        if (!b.ttft && d.ttft) b.ttft = d.ttft;
        delete dByModel[b.model];
      }
      return b;
    });
    // any discover-only bands (no /market row) appended
    Object.keys(dByModel).forEach(function (k) { out.push(dByModel[k]); });
    return out;
  }

  /* ---- demo band (labelled fallback) ---------------------------- */
  function demoStations(model, n, baseIn, baseOut, conf) {
    var regions = ["US-W", "US-E", "DE", "NL", "UK", "JP", "US-C", "CA"];
    var out = [];
    for (var i = 0; i < n; i++) {
      var seed = model + "#demo#" + i;
      out.push({
        callsign: callsign(seed),
        region: regions[hashStr(seed) % regions.length],
        online: true,
        priceIn: +(baseIn + i * 0.02).toFixed(2),
        priceOut: +(baseOut + i * 0.03).toFixed(2),
        tps: 40 + (hashStr(seed) % 55),
        ttft: 180 + (hashStr(seed + "t") % 700),
        quality: 0.8 + (hashStr(seed + "q") % 20) / 100,
        confidential: conf && i % 2 === 0,
        free: i === n - 1,
        scheduled: i % 3 === 0,
        ctx: [8192, 16384, 32768, 131072][hashStr(seed + "c") % 4]
      });
    }
    return out;
  }
  function demoBands() {
    var seed = [
      ["qwen3-coder-30b", 6, 0.18, 0.22, 92, true, 131072],
      ["qwen3-72b", 4, 0.34, 0.38, 84, true, 32768],
      ["gpt-oss-120b", 3, 0.49, 0.55, 71, true, 131072],
      ["llama3.3-70b", 5, 0.26, 0.31, 66, false, 32768],
      ["deepseek-v3", 2, 0.55, 0.61, 58, false, 65536],
      ["gemma3-27b", 4, 0.21, 0.24, 88, false, 16384],
      ["mistral-large", 0, 0.44, 0.49, 0, false, 32768]
    ];
    return seed.map(function (s) {
      var stations = s[1] > 0 ? demoStations(s[0], s[1], s[2], s[3], s[5]) : [];
      var supply = Math.min(1, Math.log2(s[1] + 1) / 3.5);
      var speed = Math.min(1, s[4] / 90);
      return {
        model: s[0], providers: s[1], live: s[1] > 0,
        signal: s[1] > 0 ? Math.max(8, Math.round((0.62 * supply + 0.38 * speed) * 100)) : 0,
        priceIn: s[2], priceOut: s[3], tps: s[4],
        ttft: stations.length ? stations[0].ttft : 0, success: 1,
        verified: s[5], confidential: s[5], freeNow: s[1] > 0,
        ctx: s[6], stations: stations
      };
    });
  }

  /* ---- state ---------------------------------------------------- */
  var bands = [];        // full normalized set
  var dataMode = "demo"; // live | demo | off
  var filters = { q: "", sort: "signal", free: false, conf: false, ver: false, on: false };

  /* ---- signal tower string -------------------------------------- */
  function towerBars(sig100, live) {
    var level = Math.max(0, Math.min(100, sig100)) / 100;
    var cells = 8;
    var filled = live ? Math.max(1, Math.min(cells, Math.round(level * cells))) : 0;
    var html = "";
    for (var i = 0; i < cells; i++) {
      if (i < filled) {
        var g = BARGLYPH[Math.min(7, Math.round((i / (cells - 1)) * 7))];
        html += '<span class="sigbar sigbar--on">' + g + "</span>";
      } else {
        html += '<span class="sigbar sigbar--off">·</span>';
      }
    }
    return html;
  }

  /* ---- directory render ----------------------------------------- */
  function fmtCtx(c) {
    if (!c) return "";
    if (c >= 1000) return Math.round(c / 1024) + "k";
    return String(c);
  }
  function rowHTML(b) {
    var dot = b.live
      ? '<span class="band-dot band-dot--on" aria-hidden="true">◉</span>'
      : '<span class="band-dot band-dot--off" aria-hidden="true">○</span>';
    var marks = "";
    if (b.verified || b.confidential) marks += ' <span class="cs" title="lineage-verified / confidential">◆</span>';
    if (b.freeNow) marks += ' <span class="band-tag band-tag--free">FREE</span>';
    var ctx = b.ctx ? '<span class="band-ctx mono">' + fmtCtx(b.ctx) + ' ctx</span>' : "";
    var prov = b.live
      ? '<span class="band-prov">' + b.providers + ' station' + (b.providers === 1 ? '' : 's') + ' on air</span>'
      : '<span class="band-prov band-prov--idle">idle - no station on air</span>';

    var price;
    if (b.priceIn == null && b.priceOut == null) {
      price = '<span class="band-unit--idle">-</span>';
    } else {
      var pi = b.priceIn != null ? fmtPrice(b.priceIn) : "-";
      var po = b.priceOut != null ? fmtPrice(b.priceOut) : "-";
      price = '<b class="mono">' + pi + '</b><span class="band-unit"> · ' + po + '</span>';
    }
    var tps = b.live && b.tps ? '<b class="mono">' + Math.round(b.tps) + '</b><span class="band-unit"> t/s</span>'
                              : '<span class="band-unit--idle">-</span>';
    var stn = b.live ? '<b class="mono">' + b.providers + '</b>' : '<span class="band-unit--idle">0</span>';
    var stat = b.live
      ? '<span class="band-stat band-stat--on">◉ on air</span>'
      : '<span class="band-stat band-stat--off">○ idle</span>';

    return (
      '<span class="band-cell band-cell--name">' +
        '<span class="band-name-line">' + dot + '<span class="band-name">' + esc(b.model) + marks + '</span></span>' +
        prov + ctx +
      '</span>' +
      '<span class="band-cell band-cell--sig"><span class="sig" aria-hidden="true">' + towerBars(b.signal, b.live) + '</span>' +
        '<span class="band-signum mono">' + (b.live ? b.signal : '--') + '</span></span>' +
      '<span class="band-cell band-cell--price">' + price + '</span>' +
      '<span class="band-cell band-cell--tps">' + tps + '</span>' +
      '<span class="band-cell band-cell--stn">' + stn + '</span>' +
      '<span class="band-cell band-cell--flags">' + stat + '</span>'
    );
  }

  function applyFilters(arr) {
    var q = filters.q.trim().toLowerCase();
    var out = arr.filter(function (b) {
      if (q && b.model.toLowerCase().indexOf(q) === -1) return false;
      if (filters.free && !b.freeNow) return false;
      if (filters.conf && !b.confidential) return false;
      if (filters.ver && !b.verified) return false;
      if (filters.on && !b.live) return false;
      return true;
    });
    out.sort(function (a, b) {
      switch (filters.sort) {
        case "cheapest":
          return (a.priceIn == null ? 1e9 : a.priceIn) - (b.priceIn == null ? 1e9 : b.priceIn);
        case "fastest": return (b.tps || 0) - (a.tps || 0);
        case "stations": return (b.providers || 0) - (a.providers || 0);
        case "ctx": return (b.ctx || 0) - (a.ctx || 0);
        default:
          if (b.live !== a.live) return a.live ? -1 : 1;
          return (b.signal || 0) - (a.signal || 0);
      }
    });
    return out;
  }

  function renderList() {
    var rows = applyFilters(bands);
    listEl.innerHTML = "";
    if (!rows.length) {
      if (emptyEl) emptyEl.hidden = false;
      return;
    }
    if (emptyEl) emptyEl.hidden = true;
    rows.forEach(function (b, i) {
      var li = el("li", "band-row" + (b.live ? "" : " band-row--idle"), rowHTML(b));
      li.setAttribute("role", "button");
      li.setAttribute("tabindex", "0");
      li.setAttribute("aria-label", "Open the " + b.model + " band card");
      li.dataset.band = b.model;
      li.style.setProperty("--i", i);
      listEl.appendChild(li);
    });
  }

  /* ---- the band SCANNER (centerpiece) --------------------------- */
  var scanSvg = document.getElementById("scanSvg");
  var scanStage = document.getElementById("scanStage");
  var scanLocked = document.getElementById("scanLocked");
  var scanSig = document.getElementById("scanSig");
  var scanChip = document.getElementById("scanChip");
  var depthFar = document.querySelector(".scanner__plane--far");
  var depthMid = document.querySelector(".scanner__plane--mid");

  var W = 1000, H = 120, ns = "http://www.w3.org/2000/svg", mid = W / 2;
  var stripG = scanSvg ? document.createElementNS(ns, "g") : null;
  if (scanSvg) { scanSvg.setAttribute("viewBox", "0 0 " + W + " " + H); scanSvg.appendChild(stripG); }
  var scanBands = [], stripX = 0, targetX = 0, scanReady = false, parallax = 0;

  function buildScanStrip() {
    if (!stripG) return;
    while (stripG.firstChild) stripG.removeChild(stripG.firstChild);
    var spacing = 165;
    scanBands.forEach(function (b, i) {
      var x = mid + i * spacing;
      for (var k = 0; k < 6; k++) {
        var t = document.createElementNS(ns, "line");
        var tx = x + k * (spacing / 6), major = k === 0;
        t.setAttribute("x1", tx); t.setAttribute("x2", tx);
        t.setAttribute("y1", major ? 12 : 30); t.setAttribute("y2", 62);
        t.setAttribute("stroke", b.live ? "var(--ink-500)" : "var(--ink-300)");
        t.setAttribute("stroke-width", major ? "2" : "1");
        stripG.appendChild(t);
      }
      var lbl = document.createElementNS(ns, "text");
      lbl.setAttribute("x", x); lbl.setAttribute("y", 88);
      lbl.setAttribute("fill", b.live ? "var(--ink-900)" : "var(--ink-400)");
      lbl.setAttribute("font-family", "var(--font-mono)");
      lbl.setAttribute("font-size", "15");
      lbl.textContent = b.model;
      stripG.appendChild(lbl);
      var sub = document.createElementNS(ns, "text");
      sub.setAttribute("x", x); sub.setAttribute("y", 106);
      sub.setAttribute("fill", b.live ? "var(--live)" : "var(--ink-300)");
      sub.setAttribute("font-family", "var(--font-mono)");
      sub.setAttribute("font-size", "11");
      sub.textContent = b.live ? (b.providers + " stn · sig " + b.signal) : "idle";
      stripG.appendChild(sub);
    });
    scanReady = true;
  }
  function lockScanner(b) {
    var idx = scanBands.indexOf(b);
    targetX = -(idx * 165);
    if (scanLocked) scanLocked.innerHTML = (b.live ? "LOCKED · " : "QUIET · ") + "<b>" + esc(b.model) + "</b>";
    if (scanSig) scanSig.innerHTML = "SIGNAL <b>" + (b.live ? b.signal : "--") + "</b>/100";
    if (scanChip) scanChip.innerHTML =
      '<span class="scanner__k">RATE</span><b>' + fmtPrice(b.priceIn) + ' /1M</b>' +
      '<span class="scanner__k">SPEED</span><b>' + (b.live && b.tps ? Math.round(b.tps) + ' t/s' : '-') + '</b>' +
      '<span class="scanner__k">STN</span><b>' + b.providers + '</b>' +
      '<a class="scanner__open" href="#band=' + encodeURIComponent(b.model) + '">open card &rarr;</a>';
    if (REDUCED) { stripX = targetX; applyScan(); }
  }
  function applyScan() {
    if (stripG) stripG.setAttribute("transform", "translate(" + stripX + ",0)");
    // parallax depth planes drift at fractions of the strip for 3D feel
    if (depthFar) depthFar.style.transform = "translateX(" + (stripX * 0.25) + "px)";
    if (depthMid) depthMid.style.transform = "translateX(" + (stripX * 0.55) + "px)";
  }
  function applyScanBands(next) {
    scanBands = next.filter(function (b) { return b.model; })
      .sort(function (a, b) { if (b.live !== a.live) return a.live ? -1 : 1; return b.signal - a.signal; })
      .slice(0, 12);
    var liveStations = 0;
    scanBands.forEach(function (b) { if (b.live) liveStations += b.providers; });
    document.body.setAttribute("data-onair", liveStations > 0 ? "live" : "idle");
    buildScanStrip();
    var best = scanBands.filter(function (b) { return b.live; })[0] || scanBands[0];
    if (best) {
      if (REDUCED) { stripX = 0; } else { stripX = 520; }
      lockScanner(best);
    }
  }

  /* ---- one shared rAF ------------------------------------------- */
  var rafId = null, running = false, visible = true;
  function frame() {
    if (scanReady) {
      stripX += (targetX - stripX) * 0.10;
      // gentle idle parallax sway on the carrier (transform only)
      parallax += 0.012;
      var sway = Math.sin(parallax) * 6;
      if (scanStage) scanStage.style.setProperty("--sway", sway.toFixed(2) + "px");
      applyScan();
    }
    rafId = requestAnimationFrame(frame);
  }
  function startRAF() { if (REDUCED || running || !visible) return; running = true; rafId = requestAnimationFrame(frame); }
  function stopRAF() { running = false; if (rafId) { cancelAnimationFrame(rafId); rafId = null; } }

  /* ---- pointer parallax tilt (depth, non-touch) ----------------- */
  if (scanStage && !REDUCED && window.matchMedia("(hover: hover)").matches) {
    scanStage.addEventListener("pointermove", function (e) {
      var r = scanStage.getBoundingClientRect();
      var rx = (0.5 - (e.clientY - r.top) / r.height) * 5;
      var ry = ((e.clientX - r.left) / r.width - 0.5) * 7;
      scanStage.style.setProperty("--tiltX", rx.toFixed(2) + "deg");
      scanStage.style.setProperty("--tiltY", ry.toFixed(2) + "deg");
    });
    scanStage.addEventListener("pointerleave", function () {
      scanStage.style.setProperty("--tiltX", "0deg");
      scanStage.style.setProperty("--tiltY", "0deg");
    });
  }

  /* ---- QSL detail card (hash route #band=<model>) --------------- */
  var detailEl = document.getElementById("detail");
  function qhrs(b) {
    // a coarse 24-cell time-of-use strip from station schedules. Without
    // exact windows from /discover we mark whether the band is scheduled
    // vs continuous; demo data drives an illustrative pattern.
    var scheduled = (b.stations || []).some(function (s) { return s.scheduled; });
    var cells = [];
    for (var h = 0; h < 24; h++) {
      var on = !scheduled || (h >= 18 || h < 8 || (b.live && (hashStr(b.model + h) % 3 !== 0)));
      cells.push(on);
    }
    return { cells: cells, scheduled: scheduled };
  }
  function renderQSL(model) {
    var b = null;
    for (var i = 0; i < bands.length; i++) if (bands[i].model === model) { b = bands[i]; break; }
    if (!b) { hideDetail(); return; }

    detailEl.hidden = false;
    document.getElementById("qslBand").textContent = b.model;
    var subN = b.providers;
    document.getElementById("qslSub").textContent = b.live
      ? "- " + subN + " station" + (subN === 1 ? "" : "s") + " on air"
      : "- no station on air (idle band)";
    document.getElementById("qslSigGlyph").innerHTML = towerBars(b.signal, b.live);
    document.getElementById("qslSigNum").textContent = "SIGNAL " + (b.live ? b.signal : "--") + "/100";
    document.getElementById("qslCard").setAttribute("data-onair", b.live ? "live" : "idle");

    // station log (privacy-safe)
    var log = document.getElementById("qslLog");
    log.innerHTML = "";
    var stations = b.stations || [];
    if (!stations.length) {
      log.innerHTML = '<li class="qsl-row qsl-row--empty mono">no live station detail for this band right now</li>';
    } else {
      stations.forEach(function (s) {
        var marks = "";
        if (s.confidential) marks += ' <span class="cs" title="confidential">◆</span>';
        if (s.free) marks += ' <span class="band-tag band-tag--free">FREE</span>';
        var dot = s.online ? '<span class="band-dot--on">◉</span>' : '<span class="band-dot--off">○</span>';
        var pin = s.priceIn != null ? fmtPrice(s.priceIn) : "-";
        var pout = s.priceOut != null ? fmtPrice(s.priceOut) : "-";
        var ok = s.quality != null ? Math.round(Math.min(1, s.quality) * 100) + "%" : "-";
        var ttft = s.ttft ? (s.ttft >= 1000 ? (s.ttft / 1000).toFixed(1) + "s" : Math.round(s.ttft) + "ms") : "-";
        log.appendChild(el("li", "qsl-row",
          '<span class="qsl-cs"><span class="qsl-cs__sign mono">' + dot + ' ' + esc(s.callsign) + marks + '</span>' +
            '<span class="qsl-cs__reg mono">' + esc(s.region) + '</span></span>' +
          '<span class="mono">' + pin + '<span class="band-unit"> · ' + pout + '</span></span>' +
          '<span class="mono">' + (s.online && s.tps ? Math.round(s.tps) : '-') + '</span>' +
          '<span class="mono">' + ttft + '</span>' +
          '<span class="mono">' + ok + '</span>'));
      });
    }

    // lineage / verification
    var nVer = stations.filter(function (s) { return s.confidential; }).length;
    document.getElementById("qslVerify").textContent = b.confidential
      ? "verification: " + nVer + " confidential route" + (nVer === 1 ? "" : "s") + " - sealed payload"
      : "verification: standard lineage receipts";

    // time-of-use
    var hrsWrap = document.getElementById("qslHours");
    var h = qhrs(b);
    hrsWrap.innerHTML = "";
    h.cells.forEach(function (on, idx) {
      var c = el("span", "qsl-hr" + (on ? " qsl-hr--on" : ""));
      c.title = idx + ":00 " + (on ? "on air" : "off");
      hrsWrap.appendChild(c);
    });
    document.getElementById("qslSched").textContent = h.scheduled
      ? "schedule: time-of-use windows set by operators"
      : "schedule: continuous - on air whenever a station is up";

    // how to tune in
    document.getElementById("qslCmdCode").textContent = "rogerai use " + b.model;
    document.getElementById("qslStamp").textContent = "RogerAI · QSL · " + b.model;

    if (typeof detailEl.scrollIntoView === "function") {
      detailEl.scrollIntoView({ behavior: REDUCED ? "auto" : "smooth", block: "start" });
    }
    var card = document.getElementById("qslCard");
    if (card) card.classList.add("is-revealed");
  }
  function hideDetail() { if (detailEl) detailEl.hidden = true; }

  function routeFromHash() {
    var m = /(?:^|[#&])band=([^&]+)/.exec(window.location.hash || "");
    if (m) {
      var model = decodeURIComponent(m[1]);
      if (bands.length) renderQSL(model);
    } else {
      hideDetail();
    }
  }
  window.addEventListener("hashchange", routeFromHash);

  // row click / keyboard -> open card
  listEl.addEventListener("click", function (e) {
    var row = e.target.closest(".band-row");
    if (row && row.dataset.band) window.location.hash = "band=" + encodeURIComponent(row.dataset.band);
  });
  listEl.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" && e.key !== " ") return;
    var row = e.target.closest(".band-row");
    if (row && row.dataset.band) { e.preventDefault(); window.location.hash = "band=" + encodeURIComponent(row.dataset.band); }
  });
  // copy the QSL use-command (site.js owns the install boxes; this one is dynamic)
  var qslCmd = document.getElementById("qslCmd");
  if (qslCmd) qslCmd.addEventListener("click", function () {
    var code = document.getElementById("qslCmdCode").textContent;
    var done = function () {
      qslCmd.classList.add("is-copied");
      var t = document.getElementById("toast");
      if (t) { t.textContent = "Copied to clipboard"; t.classList.add("is-shown"); setTimeout(function () { t.classList.remove("is-shown"); }, 1800); }
      setTimeout(function () { qslCmd.classList.remove("is-copied"); }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(code).then(done, function () {});
    else { try { var ta = document.createElement("textarea"); ta.value = code; document.body.appendChild(ta); ta.select(); document.execCommand("copy"); document.body.removeChild(ta); done(); } catch (e) {} }
  });

  /* ---- status helpers ------------------------------------------- */
  function setStatus(text, mode) {
    if (statusTxt) statusTxt.textContent = text;
    if (statusWrap) {
      statusWrap.classList.toggle("is-live", mode === "live");
      statusWrap.classList.toggle("is-demo", mode === "demo");
      statusWrap.classList.toggle("is-off", mode === "off");
    }
    document.body.setAttribute("data-onair",
      mode === "live" && bands.some(function (b) { return b.live; }) ? "live" : "idle");
  }

  /* ---- fetch + refresh ------------------------------------------ */
  var inflight = false;
  function ingest(next, mode) {
    bands = next;
    dataMode = mode;
    renderList();
    applyScanBands(next.slice());
    routeFromHash();
  }
  function load() {
    if (inflight) return;
    inflight = true;
    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 8000);
    var opt = { signal: ctrl ? ctrl.signal : undefined, cache: "no-store" };

    // fetch /market (signal) and /discover (station detail) together.
    var mP = fetch(BROKER + "/market", opt).then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; });
    var dP = fetch(BROKER + "/discover", opt).then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; });

    Promise.all([mP, dP]).then(function (res) {
      clearTimeout(to);
      inflight = false;
      var mData = res[0], dData = res[1];
      var marketRows = (mData && Array.isArray(mData.market)) ? mData.market : [];
      var offers = (dData && Array.isArray(dData.offers)) ? dData.offers : [];
      var mBands = fromMarket(marketRows);
      var dBands = fromDiscover(offers);
      var reachedBroker = (mData != null) || (dData != null);

      var merged;
      if (mBands.length) merged = merge(mBands, dBands);
      else if (dBands.length) merged = dBands;
      else merged = [];

      var liveN = merged.filter(function (b) { return b.live; }).length;

      if (merged.length && liveN > 0) {
        ingest(merged, "live");
        setStatus(liveN + " band" + (liveN === 1 ? "" : "s") + " on air · live from the broker", "live");
      } else if (reachedBroker) {
        ingest(demoBands(), "demo");
        setStatus("the band is quiet right now - a preview of how the directory looks on air", "demo");
      } else {
        ingest(demoBands(), "off");
        setStatus("preview directory - couldn't reach the broker just now", "off");
      }
    }).catch(function () {
      clearTimeout(to);
      inflight = false;
      ingest(demoBands(), "off");
      setStatus("preview directory - couldn't reach the broker just now", "off");
    });
  }

  /* ---- controls -------------------------------------------------- */
  function bindChip(btn, key) {
    if (!btn) return;
    btn.addEventListener("click", function () {
      filters[key] = !filters[key];
      btn.setAttribute("aria-pressed", filters[key] ? "true" : "false");
      btn.classList.toggle("is-on", filters[key]);
      renderList();
    });
  }
  bindChip(fltFree, "free"); bindChip(fltConf, "conf"); bindChip(fltVer, "ver"); bindChip(fltOn, "on");
  if (sortEl) sortEl.addEventListener("change", function () { filters.sort = sortEl.value; renderList(); });
  if (searchEl) {
    searchEl.addEventListener("input", function () {
      filters.q = searchEl.value;
      if (clearEl) clearEl.hidden = !searchEl.value;
      renderList();
    });
  }
  if (clearEl) clearEl.addEventListener("click", function () {
    searchEl.value = ""; filters.q = ""; clearEl.hidden = true; renderList(); searchEl.focus();
  });
  if (refreshBtn) refreshBtn.addEventListener("click", function () { setStatus("re-tuning…", "live"); load(); });

  /* ---- poll + visibility ---------------------------------------- */
  var pollTimer = null;
  function schedule() {
    if (REDUCED) return;
    clearTimeout(pollTimer);
    pollTimer = setTimeout(function () { if (visible) load(); schedule(); }, POLL_MS);
  }
  document.addEventListener("visibilitychange", function () {
    visible = !document.hidden;
    if (document.hidden) stopRAF(); else startRAF();
  });

  /* ---- kick off ------------------------------------------------- */
  // static demo paint first so the page is usable instantly / JS-degraded.
  ingest(demoBands(), "demo");
  setStatus("tuning in to the broker…", "demo");
  startRAF();
  load();
  schedule();
})();
