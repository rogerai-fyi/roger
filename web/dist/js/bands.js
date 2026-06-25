/* =====================================================================
   RogerAI - /models : the live model directory + interactive tuning dial
   + QSL card. Self-served (CSP script-src 'self'). No deps. SVG + CSS + DOM.

   REAL DATA ONLY. This page never invents or demos a band. It renders only
   what the broker reports plus the user's OWN local history of real models
   it has seen before (offline ones shown dimmed). If nothing is on air and
   nothing has been seen, the honest empty state shows.

   Data sources (CORS-open, public):
     GET /market   -> per-band: model, providers, in_flight, min_price,
                      best_tps, ttft_ms, quality, success_rate, signal 0-100
     GET /discover -> per-station offers: node_id, region, hw, model,
                      price_in, price_out, ctx, online, confidential,
                      free_now, scheduled, tps, ttft_ms, quality
   /market is authoritative for the band ROWS (signal 0-100); /discover
   supplies the per-station LOG for the QSL detail card and the in/out price
   split + ctx + free-now.

   HISTORY: real models we've seen are remembered in localStorage keyed by
   model id with a last-seen timestamp. Previously-seen-but-now-offline
   models render dimmed/idle (real history, never invented). A filter toggles
   "on air" (default) vs "include offline / seen".

   PRIVACY (no PII): /discover exposes a raw node_id + free-text region.
   We NEVER render either. The operator becomes a pseudonymous @callsign
   derived from a stable hash of node_id, plus a COARSE macro-region only.

   Motion discipline: one shared rAF (the dial sweep physics + needle glow);
   paused offscreen + when tab hidden; full prefers-reduced-motion fallback
   (pre-locked dial, no sweep, no poll); page usable with JS/network off.
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var POLL_MS = 30000;
  var BARGLYPH = "▁▂▃▄▅▆▇█"; // ▁▂▃▄▅▆▇█
  var HISTORY_KEY = "roger-seen-models";
  var HISTORY_MAX = 60;     // cap the remembered set
  var HISTORY_TTL_MS = 1000 * 60 * 60 * 24 * 30; // forget after 30 days unseen

  /* ---- DOM handles ---------------------------------------------- */
  var listEl   = document.getElementById("bandList");
  if (!listEl) return;
  var statusTxt = document.getElementById("bandStatusText");
  var statusWrap = document.getElementById("bandStatus");
  var emptyEl  = document.getElementById("bandEmpty");
  var quietEl  = document.getElementById("bandQuiet");
  var refreshBtn = document.getElementById("bandRefresh");
  var searchEl = document.getElementById("bandSearch");
  var clearEl  = document.getElementById("bandClear");
  var sortEl   = document.getElementById("bandSort");
  var fltFree  = document.getElementById("fltFree");
  var fltVer   = document.getElementById("fltVer");
  var fltConf  = document.getElementById("fltConf");
  var fltOn    = document.getElementById("fltOn");
  var fltSeen  = document.getElementById("fltSeen");

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
  function clamp(v, a, b) { return v < a ? a : v > b ? b : v; }

  /* ---- PII firewall: callsign + coarse region ------------------- */
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
    s += CS_CONS[h % CS_CONS.length]; h = (h / CS_CONS.length) | 0;
    s += CS_VOW[h % CS_VOW.length];   h = (h / CS_VOW.length) | 0;
    var n = hashStr(nodeId + "#");
    s += (n % 90 + 10);               n = (n / 100) | 0;
    s += CS_CONS[n % CS_CONS.length]; n = (n / CS_CONS.length) | 0;
    s += CS_CONS[n % CS_CONS.length];
    return "@" + s;
  }
  // coarseRegion buckets a free-text region to a macro-region label, or "" when it
  // is missing/unmatched. "" renders as a dim em-dash ("not provided"), NEVER the
  // literal "??" - a missing region is honestly absent, not a glitch.
  function coarseRegion(region) {
    var r = String(region || "").toLowerCase();
    if (!r) return "";
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
    if (/asia/.test(r)) return "ASIA";
    return "";
  }
  // fmtCtx renders a context window: detected windows solid ("131k"), the estimated
  // default dim with a leading "~" ("~32k") via the caller's class. Returns the text;
  // the caller decides the dim styling from the est flag.
  function fmtCtx(ctx) {
    if (!ctx || ctx <= 0) return "-";
    if (ctx >= 1000) return Math.round(ctx / 1000) + "k";
    return String(ctx);
  }
  // hwClass normalizes a node's advertised hardware to a coarse, BUCKETED class label
  // for the chip - NEVER the raw rig string (which can fingerprint / carry PII). Nodes
  // now advertise a privacy-bucketed class (multi-gpu / single-gpu / apple / cpu); we
  // relabel those directly, and still map any LEGACY raw string (rtx, threadripper, ...)
  // to a broad family so older nodes render a class too. Unknown stays blank (no chip).
  function hwClass(hw) {
    var s = String(hw || "").toLowerCase().trim();
    if (!s || s === "unknown") return "";
    // New bucketed classes (the node's authoritative, leak-free advertisement).
    if (s === "multi-gpu") return "multi-gpu";
    if (s === "single-gpu") return "single-gpu";
    if (s === "apple") return "apple";
    if (s === "cpu") return "cpu";
    // Legacy raw strings -> broad family.
    if (/(apple|\bm[1-4]\b|m[1-4] (pro|max|ultra)|mac studio|macbook)/.test(s)) return "apple";
    if (/(nvidia|geforce|rtx|gtx|tesla|a100|h100|l40|radeon|\brx ?\d|instinct|mi\d{2,3}|gpu|cuda|rocm)/.test(s)) return "single-gpu";
    if (/(ryzen|threadripper|epyc|xeon|core i[3579]|intel|amd|cpu|authenticamd|genuineintel)/.test(s)) return "cpu";
    return "";
  }

  /* ---- local history of REAL models we've seen ------------------ */
  // shape: { "<model>": { lastSeen: ms, priceIn, priceOut, ctx, signal, tps, conf } }
  function loadHistory() {
    try {
      var raw = localStorage.getItem(HISTORY_KEY);
      if (!raw) return {};
      var obj = JSON.parse(raw);
      if (!obj || typeof obj !== "object") return {};
      var now = Date.now(), out = {};
      Object.keys(obj).forEach(function (k) {
        var e = obj[k];
        if (e && typeof e.lastSeen === "number" && (now - e.lastSeen) < HISTORY_TTL_MS) out[k] = e;
      });
      return out;
    } catch (e) { return {}; }
  }
  function saveHistory(hist) {
    try {
      // cap to the most-recently-seen HISTORY_MAX entries
      var keys = Object.keys(hist).sort(function (a, b) { return hist[b].lastSeen - hist[a].lastSeen; });
      if (keys.length > HISTORY_MAX) {
        var trimmed = {};
        keys.slice(0, HISTORY_MAX).forEach(function (k) { trimmed[k] = hist[k]; });
        hist = trimmed;
      }
      localStorage.setItem(HISTORY_KEY, JSON.stringify(hist));
    } catch (e) {}
  }
  // record every REAL band we just saw (live or not); only real broker data
  // ever reaches here, so history is always genuine.
  function rememberSeen(realBands) {
    var hist = loadHistory();
    var now = Date.now();
    realBands.forEach(function (b) {
      if (!b.model) return;
      hist[b.model] = {
        lastSeen: now,
        priceIn: b.priceIn != null ? b.priceIn : null,
        priceOut: b.priceOut != null ? b.priceOut : null,
        ctx: b.ctx || 0,
        signal: b.live ? b.signal : (hist[b.model] ? hist[b.model].signal : 0),
        tps: b.live && b.tps ? b.tps : (hist[b.model] ? hist[b.model].tps : 0),
        conf: !!b.confidential
      };
    });
    saveHistory(hist);
    return hist;
  }
  function fmtAgo(ms) {
    var s = Math.max(0, Math.round((Date.now() - ms) / 1000));
    if (s < 90) return "just now";
    var m = Math.round(s / 60);
    if (m < 90) return m + "m ago";
    var h = Math.round(m / 60);
    if (h < 36) return h + "h ago";
    return Math.round(h / 24) + "d ago";
  }

  /* ---- normalize ------------------------------------------------ */
  // The broker's authoritative `terms{}` breakdown - the 7 weighted factors
  // that sum to `total` (== signal). We keep the labels + order fixed so the
  // QSL equalizer + tooltips read consistently. REAL DATA ONLY: rendered
  // straight from the broker, never synthesized.
  var TERM_KEYS = ["supply", "speed", "latency", "verified", "success", "trust", "congestion"];
  var TERM_DESC = {
    supply: "how many stations are on air for this band",
    speed: "throughput (tokens / second) of the fastest station",
    latency: "time-to-first-token responsiveness",
    verified: "stations passing the broker's live serving probe",
    success: "share of recent requests that completed cleanly",
    trust: "operator standing (history, lineage receipts)",
    congestion: "headroom penalty when stations are saturated"
  };
  function normTerms(t) {
    var out = {}, any = false;
    if (t && typeof t === "object") {
      TERM_KEYS.forEach(function (k) {
        var v = +t[k];
        out[k] = isFinite(v) ? v : 0;
        if (out[k]) any = true;
      });
    } else {
      TERM_KEYS.forEach(function (k) { out[k] = 0; });
    }
    out.total = (t && +t.total) || 0;
    out._any = any;
    return out;
  }

  function fromMarket(rows) {
    return rows.map(function (m) {
      var providers = +m.providers || 0;
      var live = providers > 0;
      var sig = m.signal != null ? Math.max(0, Math.min(100, +m.signal)) : 0;
      // success_rate is REAL evidence only when the broker says so (success_seen). With
      // no field, treat as unseen so the UI shows "no data" rather than inventing 100%.
      var seen = m.success_seen != null ? !!m.success_seen : (m.success_rate != null);
      var sr = m.success_rate != null ? +m.success_rate : 0;
      if (sr > 1) sr = sr / 100;
      var terms = normTerms(m.terms);
      return {
        model: m.model || m.band || "unknown",
        providers: providers,
        live: live,
        seen: false,            // real history-only row?
        lastSeen: 0,
        signal: live ? sig : 0,
        terms: terms,           // the broker's authoritative breakdown
        priceIn: m.min_price != null ? +m.min_price : null,
        priceOut: null,
        tps: +(m.best_tps || m.tps) || 0,
        ttft: +m.ttft_ms || 0,
        success: sr,
        successSeen: seen,
        verified: !!m.verified,     // SERVING-PROBE pass (distinct from TEE)
        confidential: false,        // TEE-confidential tier (from /discover)
        freeNow: false,
        ctx: 0,
        ctxEstimated: true,
        hwClass: "",
        stations: []
      };
    }).filter(function (b) { return b.model && b.model !== "unknown"; });
  }

  function fromDiscover(offers) {
    var by = {};
    offers.forEach(function (o) {
      if (!o || !o.model) return;
      var b = by[o.model] || (by[o.model] = {
        model: o.model, live: 0, total: 0, minIn: Infinity, minOut: Infinity,
        tps: 0, ttft: Infinity, conf: false, free: false, ctx: 0, ctxEst: true,
        ver: false, sched: false,
        // band signal/terms = the strongest online station's broker numbers
        signal: 0, terms: null, hwClass: "", success: 0, successSeen: false,
        stations: []
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
      if (online && o.verified) b.ver = true;       // serving-probe pass (distinct from TEE)
      if (o.scheduled) b.sched = true;
      // ctx: prefer the largest DETECTED window. A band stays "estimated" only if EVERY
      // contributing station is estimated, so one real window de-flags the band.
      var oEst = !!o.ctx_estimated;
      if (+o.ctx > b.ctx) { b.ctx = +o.ctx; }
      if (!oEst) b.ctxEst = false;
      var oSig = online && o.signal != null ? Math.max(0, Math.min(100, +o.signal)) : 0;
      var oSucc = o.success != null ? +o.success : 0;
      var oSeen = !!o.success_seen;
      // adopt the broker's authoritative signal + term breakdown from the
      // strongest online station; never recompute it locally.
      if (online && oSig >= b.signal) {
        b.signal = oSig;
        b.terms = normTerms(o.terms);
        b.success = oSucc > 1 ? oSucc / 100 : oSucc;
        b.successSeen = oSeen;
      }
      var cls = hwClass(o.hw);
      if (online && cls && !b.hwClass) b.hwClass = cls;
      b.stations.push({
        callsign: callsign(o.node_id),
        nodeId: o.node_id,
        region: coarseRegion(o.region),
        online: online,
        priceIn: pin, priceOut: pout,
        tps: tps, ttft: tt,
        quality: o.quality != null ? +o.quality : null,
        // REAL success EWMA, and whether it is measured at all. successSeen=false ->
        // the QSL renders "no data" (never a fabricated %). quality stays a SEPARATE
        // trust signal, no longer masquerading as the success rate.
        success: oSucc > 1 ? oSucc / 100 : oSucc,
        successSeen: oSeen,
        confidential: !!o.confidential,
        verified: online && !!o.verified,
        free: !!o.free_now,
        scheduled: !!o.scheduled,
        ctx: +o.ctx || 0,
        ctxEstimated: oEst,
        signal: oSig,
        hwClass: cls
      });
    });
    return Object.keys(by).map(function (k) {
      var b = by[k];
      b.stations.sort(function (a, c) {
        if (a.online !== c.online) return a.online ? -1 : 1;
        return (c.signal || 0) - (a.signal || 0);
      });
      return {
        model: b.model, providers: b.live, live: b.live > 0, seen: false, lastSeen: 0,
        signal: b.signal,
        terms: b.terms || normTerms(null),
        priceIn: b.minIn === Infinity ? null : b.minIn,
        priceOut: b.minOut === Infinity ? null : b.minOut,
        tps: b.tps, ttft: b.ttft === Infinity ? 0 : b.ttft,
        success: b.success || 0, successSeen: b.successSeen,
        verified: b.ver, confidential: b.conf, freeNow: b.free,
        ctx: b.ctx, ctxEstimated: b.ctx > 0 ? b.ctxEst : true,
        scheduled: b.sched, hwClass: b.hwClass,
        stations: b.stations
      };
    });
  }

  function merge(marketBands, discoverBands) {
    var dByModel = {};
    discoverBands.forEach(function (d) { dByModel[d.model] = d; });
    var out = marketBands.map(function (b) {
      var d = dByModel[b.model];
      if (d) {
        b.stations = d.stations;
        b.priceIn = b.priceIn != null ? b.priceIn : d.priceIn;
        b.priceOut = d.priceOut;
        // verified = serving-probe pass; confidential = TEE tier. Distinct flags.
        b.confidential = d.confidential;
        b.verified = b.verified || d.verified;
        b.freeNow = d.freeNow;
        b.ctx = d.ctx;
        b.ctxEstimated = d.ctxEstimated;
        b.scheduled = d.scheduled;
        b.hwClass = b.hwClass || d.hwClass;
        // success: /discover carries the per-station REAL rate + seen flag. Prefer a
        // SEEN value (from either source) so an honest "no data" is only shown when
        // neither source has measured evidence.
        if (d.successSeen) { b.success = d.success; b.successSeen = true; }
        // /market is authoritative for signal+terms; only fall back to the
        // /discover-derived values if /market did not carry them.
        if ((!b.terms || !b.terms._any) && d.terms && d.terms._any) b.terms = d.terms;
        if (!b.signal && d.signal) b.signal = d.signal;
        if (!b.tps && d.tps) b.tps = d.tps;
        if (!b.ttft && d.ttft) b.ttft = d.ttft;
        delete dByModel[b.model];
      }
      return b;
    });
    Object.keys(dByModel).forEach(function (k) { out.push(dByModel[k]); });
    return out;
  }

  // Turn local history entries (for models NOT currently live) into dimmed,
  // idle rows. Real history only - never invented.
  function bandsFromHistory(hist, liveModels) {
    return Object.keys(hist).filter(function (k) { return liveModels.indexOf(k) === -1; })
      .map(function (k) {
        var e = hist[k];
        return {
          model: k, providers: 0, live: false, seen: true, lastSeen: e.lastSeen || 0,
          signal: 0, terms: normTerms(null),
          priceIn: e.priceIn != null ? e.priceIn : null,
          priceOut: e.priceOut != null ? e.priceOut : null,
          tps: e.tps || 0, ttft: 0, success: 0, successSeen: false,
          verified: false, confidential: !!e.conf, freeNow: false,
          scheduled: false, hwClass: "",
          ctx: e.ctx || 0, ctxEstimated: true, stations: []
        };
      });
  }

  /* ---- state ---------------------------------------------------- */
  var bands = [];        // full normalized set (live + seen-offline)
  var reachedBroker = false;
  var filters = { q: "", sort: "signal", free: false, conf: false, ver: false, on: true, seen: false };

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
  function fmtTtft(ms) {
    if (!ms) return "-";
    return ms >= 1000 ? (ms / 1000).toFixed(1) + "s" : Math.round(ms) + "ms";
  }
  function fmtPct(x) {
    if (x == null || !isFinite(x)) return "-";
    var v = x > 1 ? x : x * 100;
    return Math.round(v) + "%";
  }
  // The strongest 2-3 contributing terms, as a plain "why this signal" string.
  function topTermsText(terms) {
    if (!terms || !terms._any) return "";
    var rank = TERM_KEYS.filter(function (k) { return terms[k] > 0; })
      .sort(function (a, b) { return terms[b] - terms[a]; }).slice(0, 3);
    if (!rank.length) return "";
    return "why this signal: " + rank.map(function (k) {
      return k + " " + Math.round(terms[k]);
    }).join(" · ") + " (of " + Math.round(terms.total || 0) + ")";
  }
  function rowHTML(b) {
    var dot = b.live
      ? '<span class="band-dot band-dot--on" aria-hidden="true">◉</span>'
      : '<span class="band-dot band-dot--off" aria-hidden="true">○</span>';
    var marks = "";
    if (b.live && b.verified) marks += ' <span class="band-tag band-tag--ver" title="verified serving: a station passed the broker live serving probe">✓ verified</span>';
    if (b.confidential) marks += ' <span class="cs" title="TEE-verified confidential (real hardware attestation)">◆</span>';
    if (b.freeNow) marks += ' <span class="band-tag band-tag--free">FREE</span>';
    if (b.seen) marks += ' <span class="band-tag band-tag--seen" title="seen before, offline now">SEEN</span>';
    // ctx: detected window solid; the estimated default dim + "~" (a guess, labeled
    // as one). band-ctx--est carries the dim styling.
    var ctx = "";
    if (b.ctx) {
      ctx = b.ctxEstimated
        ? '<span class="band-ctx band-ctx--est mono" title="estimated - no real context window detected">~' + fmtCtx(b.ctx) + ' ctx</span>'
        : '<span class="band-ctx mono" title="detected context window">' + fmtCtx(b.ctx) + ' ctx</span>';
    }
    var hwc = b.live && b.hwClass ? '<span class="band-hw mono" title="coarse hardware class (bucketed, no exact model)">' + esc(b.hwClass) + '</span>' : "";
    // real probe metrics, only when live. success shows the REAL EWMA only when SEEN;
    // unseen renders an honest "ok no data" - never a fabricated percentage.
    var perf = "";
    if (b.live) {
      var parts = [];
      if (b.ttft) parts.push('ttft ' + fmtTtft(b.ttft));
      parts.push(b.successSeen ? 'ok ' + fmtPct(b.success) : 'ok no data');
      if (parts.length) perf = '<span class="band-perf mono">' + parts.join(' · ') + '</span>';
    }
    var prov = b.live
      ? '<span class="band-prov">' + b.providers + ' station' + (b.providers === 1 ? '' : 's') + ' on air</span>'
      : (b.seen
          ? '<span class="band-prov band-prov--idle">offline - last seen ' + fmtAgo(b.lastSeen) + '</span>'
          : '<span class="band-prov band-prov--idle">idle - no station on air</span>');

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

    var meta = perf || hwc ? '<span class="band-meta">' + perf + hwc + '</span>' : "";
    var sigTip = b.live ? topTermsText(b.terms) : "";

    return (
      '<span class="band-cell band-cell--name">' +
        '<span class="band-name-line">' + dot + '<span class="band-name">' + esc(b.model) + marks + '</span>' +
          '<button type="button" class="report-btn report-btn--row" data-report-model="' + esc(b.model) +
            '" aria-label="Report a station on ' + esc(b.model) + '">report</button>' +
        '</span>' +
        prov + ctx + meta +
      '</span>' +
      '<span class="band-cell band-cell--sig"' + (sigTip ? ' title="' + esc(sigTip) + '"' : '') + '><span class="sig" aria-hidden="true">' + towerBars(b.signal, b.live) + '</span>' +
        '<span class="band-signum mono">' + (b.live ? b.signal : '--') + '</span></span>' +
      '<span class="band-cell band-cell--price">' + price + '</span>' +
      '<span class="band-cell band-cell--tps">' + tps + '</span>' +
      '<span class="band-cell band-cell--stn">' + stn + '</span>' +
      '<span class="band-cell band-cell--flags">' + stat + '</span>'
    );
  }

  // The "on air" filter is the default; when it is on, offline/seen rows are
  // hidden. The "include offline / seen" toggle is its inverse for clarity.
  function applyFilters(arr) {
    var q = filters.q.trim().toLowerCase();
    var out = arr.filter(function (b) {
      if (filters.on && !b.live) return false;       // on-air only (default)
      if (q && b.model.toLowerCase().indexOf(q) === -1) return false;
      if (filters.free && !b.freeNow) return false;
      if (filters.conf && !b.confidential) return false;
      if (filters.ver && !b.verified) return false;
      return true;
    });
    out.sort(function (a, b) {
      // live always sorts above offline/seen regardless of sort key
      if (b.live !== a.live) return a.live ? -1 : 1;
      switch (filters.sort) {
        case "cheapest":
          return (a.priceIn == null ? 1e9 : a.priceIn) - (b.priceIn == null ? 1e9 : b.priceIn);
        case "fastest": return (b.tps || 0) - (a.tps || 0);
        case "latency": // lowest TTFT first; bands with no probe (0) sink
          return (a.ttft || 1e9) - (b.ttft || 1e9);
        case "success": return (b.success || 0) - (a.success || 0);
        case "stations": return (b.providers || 0) - (a.providers || 0);
        case "ctx": return (b.ctx || 0) - (a.ctx || 0);
        default:
          if ((b.signal || 0) !== (a.signal || 0)) return (b.signal || 0) - (a.signal || 0);
          return (b.lastSeen || 0) - (a.lastSeen || 0);
      }
    });
    return out;
  }

  function renderList() {
    var rows = applyFilters(bands);
    listEl.innerHTML = "";

    // Honest empty state: nothing real on air AND nothing seen-offline to show.
    var anyLive = bands.some(function (b) { return b.live; });
    var showQuiet = !anyLive && (filters.on || !bands.length);
    if (quietEl) quietEl.hidden = !showQuiet;

    if (!rows.length) {
      // distinguish "the band is quiet" from "filters matched nothing"
      if (emptyEl) emptyEl.hidden = showQuiet ? true : false;
      return;
    }
    if (emptyEl) emptyEl.hidden = true;
    rows.forEach(function (b, i) {
      var cls = "band-row" + (b.live ? "" : (b.seen ? " band-row--idle band-row--seen" : " band-row--idle"));
      var li = el("li", cls, rowHTML(b));
      li.setAttribute("role", "button");
      li.setAttribute("tabindex", "0");
      li.setAttribute("aria-label", "Open the " + b.model + " model card");
      li.dataset.band = b.model;
      li.style.setProperty("--i", i);
      listEl.appendChild(li);
    });
  }

  /* =====================================================================
     THE DIAL - the full-bleed interactive tuner (centerpiece). Sweep it
     like a real radio; release snaps to the nearest model. Arrow keys tune
     model-to-model; Enter opens the locked model's QSL card. Driven by the
     SAME real `bands` set the directory uses (live first, then dimmed/seen).
     ===================================================================== */
  var dial = document.getElementById("dial");
  var svg = document.getElementById("dialSvg");
  var lockedEl = document.getElementById("dialLocked");
  var sigEl = document.getElementById("dialSig");
  var chipEl = document.getElementById("dialChip");
  var ns = "http://www.w3.org/2000/svg";
  var VBH = 132, MIDY = 0;
  var SPACING = 260;
  var ruler = null, strip = null, faceW = 0;
  var dialBands = [];      // the subset the dial sweeps across
  var pos = 0, target = 0, vel = 0, lastIdx = -1, ready = false;
  var baseY = 26, faceBottom = 86;
  var hasDial = !!(svg && dial);

  if (hasDial) {
    ruler = document.createElementNS(ns, "g"); svg.appendChild(ruler);
    strip = document.createElementNS(ns, "g"); svg.appendChild(strip);
  }

  // Pin the SVG coordinate space to 1 user-unit == 1 CSS px on BOTH axes, so
  // the strip center (faceW/2) lands exactly under the CSS needle at left:50%.
  // Returns false if the element has not laid out yet (caller should DEFER, not
  // fall back to a guessed width - a wrong width is what shifts the needle off
  // the locked band on first paint).
  function dialMeasure() {
    if (!hasDial) return false;
    var r = svg.getBoundingClientRect();
    if (!r.width) return false;               // not laid out yet: defer (no silent guess)
    faceW = r.width;
    VBH = Math.round(r.height) || VBH;        // viewBox height == pixel height -> 1:1, no aspect distortion
    svg.setAttribute("viewBox", "0 0 " + faceW + " " + VBH);
    svg.setAttribute("preserveAspectRatio", "xMidYMid meet");
    MIDY = faceW / 2;                          // strip horizontal center == CSS 50%
    return true;
  }
  function buildRuler() {
    if (!hasDial) return;
    while (ruler.firstChild) ruler.removeChild(ruler.firstChild);
    var step = 26, n = Math.ceil(faceW / step) + 1;
    for (var i = 0; i < n; i++) {
      var x = i * step, major = i % 5 === 0;
      var t = document.createElementNS(ns, "line");
      t.setAttribute("x1", x); t.setAttribute("x2", x);
      t.setAttribute("y1", faceBottom - (major ? 12 : 6)); t.setAttribute("y2", faceBottom);
      t.setAttribute("stroke", "var(--hairline-2)"); t.setAttribute("stroke-width", "1");
      t.setAttribute("class", "dial__grad");
      ruler.appendChild(t);
    }
    var base = document.createElementNS(ns, "line");
    base.setAttribute("x1", 0); base.setAttribute("x2", faceW);
    base.setAttribute("y1", faceBottom); base.setAttribute("y2", faceBottom);
    base.setAttribute("stroke", "var(--hairline)"); base.setAttribute("stroke-width", "1");
    ruler.appendChild(base);
  }
  function buildStrip() {
    if (!hasDial) return;
    buildRuler();
    while (strip.firstChild) strip.removeChild(strip.firstChild);
    if (!dialBands.length) { ready = false; return; }
    dialBands.forEach(function (b, i) {
      var x = i * SPACING;
      var t = document.createElementNS(ns, "line");
      t.setAttribute("x1", x); t.setAttribute("x2", x);
      t.setAttribute("y1", baseY); t.setAttribute("y2", faceBottom);
      t.setAttribute("stroke", b.live ? "var(--ink-500)" : "var(--ink-300)");
      t.setAttribute("stroke-width", "1.5");
      t.setAttribute("class", "dial__tick dial__tick--major");
      t.setAttribute("data-i", i);
      strip.appendChild(t);
      var pipH = 8 + Math.round((b.live ? b.signal / 100 : 0) * 30);
      var pip = document.createElementNS(ns, "rect");
      pip.setAttribute("x", x - 1.5); pip.setAttribute("width", "3");
      pip.setAttribute("y", baseY - pipH); pip.setAttribute("height", pipH);
      pip.setAttribute("rx", "1.5");
      pip.setAttribute("fill", b.live ? "var(--ink-400)" : "var(--ink-300)");
      pip.setAttribute("class", "dial__pip"); pip.setAttribute("data-i", i);
      strip.appendChild(pip);
      var lbl = document.createElementNS(ns, "text");
      lbl.setAttribute("x", x); lbl.setAttribute("y", faceBottom + 24);
      lbl.setAttribute("text-anchor", "middle");
      lbl.setAttribute("fill", b.live ? "var(--ink-900)" : "var(--ink-400)");
      lbl.setAttribute("font-family", "var(--font-mono)");
      lbl.setAttribute("font-size", "15"); lbl.setAttribute("letter-spacing", "-0.4");
      lbl.setAttribute("class", "dial__lbl" + (b.live ? "" : " dial__lbl--idle")); lbl.setAttribute("data-i", i);
      lbl.textContent = b.model;
      strip.appendChild(lbl);
    });
    ready = true;
    dialApply();
  }
  function stripTranslate(p) { return MIDY - p * SPACING; }
  function dialApply() {
    if (!hasDial || !ready) return;
    strip.setAttribute("transform", "translate(" + stripTranslate(pos) + ",0)");
    var idx = Math.round(clamp(pos, 0, dialBands.length - 1));
    var frac = 1 - Math.min(1, Math.abs(pos - idx) * 2);
    dial.style.setProperty("--lock", frac.toFixed(3));
    if (idx !== lastIdx) {
      lastIdx = idx;
      var prev = strip.querySelector(".dial__lbl--on");
      if (prev) prev.classList.remove("dial__lbl--on");
      var on = strip.querySelector('.dial__lbl[data-i="' + idx + '"]');
      if (on) on.classList.add("dial__lbl--on");
      dialReadout(dialBands[idx]);
    }
  }
  function dialReadout(b) {
    if (!hasDial || !b) return;
    if (lockedEl) lockedEl.innerHTML = (b.live ? "LOCKED · " : "OFFLINE · ") + "<b>" + esc(b.model) + "</b>";
    if (sigEl) sigEl.innerHTML = "SIGNAL <b>" + (b.live ? b.signal : "--") + "</b>/100";
    if (chipEl) chipEl.innerHTML =
      '<span class="meter__k">RATE</span><b>' + fmtPrice(b.priceIn) + ' /1M</b>' +
      '<span class="meter__k">SPEED</span><b>' + (b.live && b.tps ? Math.round(b.tps) + ' t/s' : '-') + '</b>' +
      '<span class="meter__k">TTFT</span><b>' + (b.live ? fmtTtft(b.ttft) : '-') + '</b>' +
      '<span class="meter__k">STN</span><b>' + b.providers + '</b>' +
      (b.live && b.verified ? '<span class="dial__ver" title="verified serving probe">✓</span>' : '') +
      (b.confidential ? '<span class="dial__tee cs" title="TEE-confidential">◆</span>' : '');
    dial.style.setProperty("--sig", (b.live ? b.signal / 100 : 0).toFixed(3));
    dial.setAttribute("aria-valuenow", String(lastIdx));
    dial.setAttribute("aria-valuetext", b.model + (b.live ? ", signal " + b.signal + " of 100" : ", offline"));
  }
  function dialBandIndex() { return Math.round(clamp(pos, 0, dialBands.length - 1)); }
  function dialOpenLocked() {
    var b = dialBands[dialBandIndex()];
    if (b) window.location.hash = "band=" + encodeURIComponent(b.model);
  }
  function applyDialBands(next) {
    if (!hasDial) return;
    // dial sweeps live models first (by signal), then seen-offline (dimmed).
    dialBands = next.slice().sort(function (a, b) {
      if (b.live !== a.live) return a.live ? -1 : 1;
      if (b.live) return b.signal - a.signal;
      return (b.lastSeen || 0) - (a.lastSeen || 0);
    });
    dial.setAttribute("aria-valuemin", "0");
    dial.setAttribute("aria-valuemax", String(Math.max(0, dialBands.length - 1)));
    if (!dialBands.length) {
      // honest empty dial: clear the strip + readout
      ready = false;
      if (strip) while (strip.firstChild) strip.removeChild(strip.firstChild);
      if (ruler) { dialMeasure(); buildRuler(); }
      if (lockedEl) lockedEl.innerHTML = "LOCKED · <b>nothing on air</b>";
      if (sigEl) sigEl.innerHTML = "SIGNAL <b>--</b>/100";
      if (chipEl) chipEl.innerHTML =
        '<span class="meter__k">RATE</span><b>-</b>' +
        '<span class="meter__k">SPEED</span><b>-</b>' +
        '<span class="meter__k">TTFT</span><b>-</b>' +
        '<span class="meter__k">STN</span><b>0</b>';
      dial.style.setProperty("--sig", "0");
      return;
    }
    // If the SVG has not laid out yet, DEFER to the next frame instead of
    // building the strip against a guessed width (which mis-centers the needle).
    if (!dialMeasure()) { requestAnimationFrame(function () { applyDialBands(next); }); return; }
    buildStrip();
    var best = 0; // sorted so the strongest live (or most-recent seen) is first
    lastIdx = -1;
    pos = target = best; vel = 0;
    dialApply();
    dialReadout(dialBands[best]);
    lastIdx = best;
  }

  /* ---- dial sweep physics --------------------------------------- */
  var dragging = false, hovering = false, lastX = 0, lastT = 0, downX = 0, moved = false;
  var DETENT = 0.16;
  function setTargetBand(i, instant) {
    target = clamp(i, 0, dialBands.length - 1);
    if (instant || REDUCED) { pos = target; vel = 0; dialApply(); }
  }
  function snapNearest() { target = clamp(Math.round(pos), 0, dialBands.length - 1); }
  function pxToBandDelta(dx) { return -dx / SPACING; }

  if (hasDial) {
    function onDown(e) {
      if (e.button != null && e.button !== 0) return;
      if (!dialBands.length) return;
      dialMeasure();
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
      var dband = pxToBandDelta(dx);
      pos = clamp(pos + dband, -0.4, dialBands.length - 1 + 0.4);
      if (!REDUCED) vel = pxToBandDelta(dx) * (16 / dt) * 0.6 + vel * 0.4;
      lastX = x; lastT = now;
      dialApply();
      e.preventDefault();
    }
    function onUp(e) {
      if (!dragging) return;
      dragging = false;
      dial.classList.remove("is-tuning");
      try { dial.releasePointerCapture && dial.releasePointerCapture(e.pointerId); } catch (_) {}
      if (REDUCED) { snapNearest(); pos = target; dialApply(); return; }
      if (Math.abs(vel) < 0.0008) snapNearest();
      if (!running && visible) startRAF();
    }
    // Passive hover should NOT drag the dial across the whole face - that drifts
    // the readout off the locked (strongest) band on a mere mouse-over. We only
    // nudge toward a neighbour when the cursor is within ~half a band-spacing of
    // the needle, and even then we DAMP it so the resting state stays locked.
    function onHoverMove(e) {
      if (dragging || REDUCED || !dialBands.length) return;
      var r = svg.getBoundingClientRect();
      if (!r.width) return;
      // cursor X measured from the needle (face center), in band units
      var cursorBand = (e.clientX - r.left - r.width / 2) / SPACING;
      // ignore tiny jitter; only engage within +/- 0.5 band of the needle
      if (Math.abs(cursorBand) > 0.5) { target = clamp(Math.round(pos), 0, dialBands.length - 1); return; }
      var locked = clamp(Math.round(pos), 0, dialBands.length - 1);
      // damp: pull only 35% toward the cursor's band off the locked one
      target = clamp(locked + cursorBand * 0.35, 0, dialBands.length - 1);
    }
    function onHoverEnter() { hovering = true; if (!running && visible) startRAF(); }
    function onHoverLeave() { hovering = false; if (!dragging) snapNearest(); }

    dial.addEventListener("pointerdown", onDown);
    window.addEventListener("pointermove", onMove, { passive: false });
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
    dial.addEventListener("pointerenter", onHoverEnter);
    dial.addEventListener("pointerleave", onHoverLeave);
    dial.addEventListener("pointermove", onHoverMove);

    dial.addEventListener("keydown", function (e) {
      if (!ready) return;
      var i = dialBandIndex();
      switch (e.key) {
        case "ArrowRight": case "ArrowUp": setTargetBand(i + 1); break;
        case "ArrowLeft": case "ArrowDown": setTargetBand(i - 1); break;
        case "Home": setTargetBand(0); break;
        case "End": setTargetBand(dialBands.length - 1); break;
        case "PageUp": setTargetBand(i + 3); break;
        case "PageDown": setTargetBand(i - 3); break;
        case "Enter": case " ": dialOpenLocked(); e.preventDefault(); return;
        default: return;
      }
      e.preventDefault();
      if (!running && visible) startRAF();
    });
    dial.addEventListener("click", function () {
      if (!ready || moved) return;
      if (Math.abs(pos - dialBandIndex()) < 0.12 && Math.abs(vel) < 0.01) dialOpenLocked();
    });
    // Re-measure + rebuild the whole strip (ruler + ticks + labels) on any size
    // change so the SVG center keeps tracking the CSS needle. buildStrip() also
    // re-runs dialApply() so the locked band stays under the needle.
    function reflow() {
      if (!dialMeasure()) { requestAnimationFrame(reflow); return; }
      if (dialBands.length) { buildStrip(); } else { buildRuler(); }
      dialApply();
    }
    window.addEventListener("resize", reflow);
    // ResizeObserver catches the cases window.resize misses: the full-bleed
    // .dialwrap (100vw) settling after first paint, font-load reflow, scrollbar
    // appearance, and container changes. This is the root-cause fix for the
    // off-center needle on initial paint.
    if ("ResizeObserver" in window) {
      var ro = new ResizeObserver(function () { reflow(); });
      ro.observe(svg);
      if (dial) ro.observe(dial);
    }
  }

  /* ---- one shared rAF (dial sweep) ------------------------------ */
  var rafId = null, running = false, visible = true;
  function frame() {
    if (hasDial && ready) {
      if (dragging) {
        dialApply();
      } else if (Math.abs(vel) > 0.0004) {
        pos = clamp(pos + vel, -0.4, dialBands.length - 1 + 0.4);
        vel *= 0.92;
        if (pos <= 0 || pos >= dialBands.length - 1) vel = 0;
        target = clamp(Math.round(pos), 0, dialBands.length - 1);
        dialApply();
      } else {
        var d = target - pos;
        if (Math.abs(d) > 0.0008) { pos += d * DETENT; dialApply(); }
        else if (pos !== target) { pos = target; dialApply(); }
      }
    }
    rafId = requestAnimationFrame(frame);
  }
  function startRAF() { if (REDUCED || running || !visible) return; running = true; rafId = requestAnimationFrame(frame); }
  function stopRAF() { running = false; if (rafId) { cancelAnimationFrame(rafId); rafId = null; } }

  /* ---- QSL detail card (hash route #band=<model>) --------------- */
  var detailEl = document.getElementById("detail");

  // "Why this signal": the broker's term breakdown rendered as a stacked
  // horizontal equalizer that sums to `total`. REAL DATA ONLY - straight from
  // /market terms{}, never synthesized. Each segment is width-proportional to
  // its contribution, labelled, and carries a per-term tooltip.
  function renderTermBar(b) {
    var wrap = document.getElementById("qslTerms");
    if (!wrap) return;
    wrap.innerHTML = "";
    var t = b.terms;
    if (!b.live || !t || !t._any) {
      wrap.innerHTML = '<p class="qsl__line mono qsl-terms__empty">' +
        (b.live ? "no signal breakdown reported for this band yet"
                : "offline - no live signal to break down") + '</p>';
      return;
    }
    var total = t.total || TERM_KEYS.reduce(function (s, k) { return s + (t[k] || 0); }, 0);
    if (total <= 0) { wrap.innerHTML = '<p class="qsl__line mono qsl-terms__empty">no signal breakdown</p>'; return; }

    // the stacked bar
    var bar = el("div", "qsl-terms__bar");
    bar.setAttribute("role", "img");
    bar.setAttribute("aria-label", "signal breakdown, total " + Math.round(total) + " of 100");
    var legend = el("ul", "qsl-terms__legend");
    TERM_KEYS.forEach(function (k, i) {
      var v = t[k] || 0;
      if (v <= 0) return;
      var pct = (v / total) * 100;
      var seg = el("span", "qsl-terms__seg qsl-terms__seg--" + k);
      seg.style.width = pct.toFixed(2) + "%";
      seg.title = k + ": " + Math.round(v) + " of " + Math.round(total) + " - " + (TERM_DESC[k] || "");
      bar.appendChild(seg);
      var li = el("li", "qsl-terms__li",
        '<span class="qsl-terms__key">' + esc(k) + '</span>' +
        '<span class="qsl-terms__val mono">' + Math.round(v) + '</span>');
      li.title = TERM_DESC[k] || "";
      legend.appendChild(li);
    });
    wrap.appendChild(bar);
    var cap = el("p", "qsl-terms__cap mono",
      'sums to <b>' + Math.round(total) + '</b>/100 signal · hover a segment for what it means');
    wrap.appendChild(cap);
    wrap.appendChild(legend);
  }

  // Honest schedule note: drive ONLY from the real `scheduled` flag. We do NOT
  // synthesize a fabricated 24h availability grid (that would be invented data).
  function scheduleNote(b) {
    if (b.seen || !b.live) return "schedule: offline - tune back in when a station returns";
    if (b.scheduled) return "schedule: operator-set windows (time-of-use). Tune in to see current availability.";
    return "schedule: continuous - on air whenever a station is up";
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
      : (b.seen ? "- offline (last seen " + fmtAgo(b.lastSeen) + ")" : "- no station on air (idle model)");
    document.getElementById("qslSigGlyph").innerHTML = towerBars(b.signal, b.live);
    document.getElementById("qslSigNum").textContent = "SIGNAL " + (b.live ? b.signal : "--") + "/100";
    document.getElementById("qslCard").setAttribute("data-onair", b.live ? "live" : "idle");

    var log = document.getElementById("qslLog");
    log.innerHTML = "";
    var stations = b.stations || [];
    if (!stations.length) {
      log.innerHTML = '<li class="qsl-row qsl-row--empty mono">' +
        (b.seen ? "this model is offline right now - no stations on air"
                : "no live station detail for this model right now") + '</li>';
    } else {
      stations.forEach(function (s) {
        var marks = "";
        if (s.online && s.verified) marks += ' <span class="qsl-mark qsl-mark--ver" title="verified serving: passed the broker live serving probe">✓</span>';
        if (s.confidential) marks += ' <span class="cs" title="TEE-verified confidential (real hardware attestation)">◆</span>';
        if (s.free) marks += ' <span class="band-tag band-tag--free">FREE</span>';
        var dot = s.online ? '<span class="band-dot--on">◉</span>' : '<span class="band-dot--off">○</span>';
        var pin = s.priceIn != null ? fmtPrice(s.priceIn) : "-";
        var pout = s.priceOut != null ? fmtPrice(s.priceOut) : "-";
        // ok% is the REAL success EWMA, only when measured. successSeen=false renders an
        // honest "no data" - NOT s.quality (a trust signal) masquerading as a success %.
        var ok = s.successSeen
          ? Math.round(Math.min(1, Math.max(0, s.success)) * 100) + "%"
          : '<span class="qsl-nodata">no data</span>';
        var ttft = fmtTtft(s.ttft);
        // real per-station context window + coarse hardware class (no PII). An estimated
        // ctx is dimmed + prefixed "~" so a guess never reads as a detected value.
        var meta = [];
        if (s.ctx) {
          meta.push(s.ctxEstimated
            ? '<span class="qsl-ctx--est">~' + esc(fmtCtx(s.ctx)) + ' ctx</span>'
            : esc(fmtCtx(s.ctx)) + " ctx");
        }
        if (s.online && s.hwClass) meta.push(esc(s.hwClass));
        var metaHtml = meta.length ? '<span class="qsl-cs__meta mono">' + meta.join(" · ") + '</span>' : "";
        // region "" renders a dim em-dash ("not provided"), never the literal "??".
        var regHtml = s.region ? esc(s.region) : '<span class="qsl-nodata">—</span>';
        var row = el("li", "qsl-row",
          '<span class="qsl-cs"><span class="qsl-cs__sign mono">' + dot + ' ' + esc(s.callsign) + marks +
            '<button type="button" class="report-btn" data-report-node="' + esc(s.nodeId || "") +
              '" data-report-callsign="' + esc(s.callsign) + '" aria-label="Report ' + esc(s.callsign) +
              '">report</button>' +
            '</span>' +
            '<span class="qsl-cs__reg mono">' + regHtml + metaHtml + '</span></span>' +
          '<span class="mono">' + pin + '<span class="band-unit"> · ' + pout + '</span></span>' +
          '<span class="mono">' + (s.online && s.tps ? Math.round(s.tps) : '-') + '</span>' +
          '<span class="mono">' + ttft + '</span>' +
          '<span class="mono">' + ok + '</span>');
        log.appendChild(row);
      });
    }

    // verification line: SERVING-PROBE pass (distinct) + TEE-confidential tier
    var nVerProbe = stations.filter(function (s) { return s.online && s.verified; }).length;
    var nTee = stations.filter(function (s) { return s.confidential; }).length;
    var vparts = [];
    if (nVerProbe) vparts.push(nVerProbe + " verified-serving route" + (nVerProbe === 1 ? "" : "s") + " (live probe)");
    if (nTee) vparts.push(nTee + " TEE-attested confidential route" + (nTee === 1 ? "" : "s") + " (hardware-verified)");
    document.getElementById("qslVerify").textContent = vparts.length
      ? "verification: " + vparts.join(" · ")
      : "verification: standard - signed lineage receipts, no live probe pass or TEE attestation";

    // "Why this signal": the broker's authoritative term breakdown
    renderTermBar(b);
    document.getElementById("qslSched").textContent = scheduleNote(b);

    document.getElementById("qslCmdCode").textContent = "rogerai use " + b.model;
    document.getElementById("qslStamp").textContent = "RogerAI · QSL · " + b.model;
    var qslRep = document.getElementById("qslReport");
    if (qslRep) qslRep.setAttribute("data-report-model", b.model);

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

  listEl.addEventListener("click", function (e) {
    if (e.target.closest(".report-btn")) return; // the report button handles itself
    var row = e.target.closest(".band-row");
    if (row && row.dataset.band) window.location.hash = "band=" + encodeURIComponent(row.dataset.band);
  });
  listEl.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" && e.key !== " ") return;
    if (e.target.closest(".report-btn")) return; // let the report button activate normally
    var row = e.target.closest(".band-row");
    if (row && row.dataset.band) { e.preventDefault(); window.location.hash = "band=" + encodeURIComponent(row.dataset.band); }
  });
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
      statusWrap.classList.toggle("is-quiet", mode === "quiet");
      statusWrap.classList.toggle("is-off", mode === "off");
    }
    document.body.setAttribute("data-onair",
      mode === "live" && bands.some(function (b) { return b.live; }) ? "live" : "idle");
  }

  /* ---- fetch + refresh (REAL DATA ONLY) ------------------------- */
  var inflight = false;
  function ingest(realBands, broker) {
    reachedBroker = broker;
    var liveModels = realBands.filter(function (b) { return b.live; }).map(function (b) { return b.model; });
    // remember every real band we saw; pull seen-offline rows from history.
    var hist = rememberSeen(realBands);
    var seenBands = bandsFromHistory(hist, liveModels);
    bands = realBands.concat(seenBands);
    renderList();
    applyDialBands(bands);
    routeFromHash();
  }
  function load() {
    if (inflight) return;
    inflight = true;
    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 8000);
    var opt = { signal: ctrl ? ctrl.signal : undefined, cache: "no-store" };

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
      var broker = (mData != null) || (dData != null);

      var merged;
      if (mBands.length) merged = merge(mBands, dBands);
      else if (dBands.length) merged = dBands;
      else merged = [];

      var liveN = merged.filter(function (b) { return b.live; }).length;
      ingest(merged, broker);

      if (liveN > 0) {
        setStatus(liveN + " model" + (liveN === 1 ? "" : "s") + " on air · live from the broker", "live");
      } else if (broker) {
        setStatus("the band is quiet - no models on air right now", "quiet");
      } else {
        setStatus("couldn't reach the broker just now - retrying", "off");
      }
    }).catch(function () {
      clearTimeout(to);
      inflight = false;
      ingest([], false);
      setStatus("couldn't reach the broker just now - retrying", "off");
    });
  }

  /* ---- controls -------------------------------------------------- */
  function syncOnSeenButtons() {
    // "on air" and "include offline / seen" are mutually exclusive views.
    if (fltOn)  { fltOn.setAttribute("aria-pressed", filters.on ? "true" : "false"); fltOn.classList.toggle("is-on", filters.on); }
    if (fltSeen){ fltSeen.setAttribute("aria-pressed", filters.seen ? "true" : "false"); fltSeen.classList.toggle("is-on", filters.seen); }
  }
  function bindChip(btn, key) {
    if (!btn) return;
    btn.addEventListener("click", function () {
      filters[key] = !filters[key];
      btn.setAttribute("aria-pressed", filters[key] ? "true" : "false");
      btn.classList.toggle("is-on", filters[key]);
      renderList();
    });
  }
  bindChip(fltFree, "free"); bindChip(fltVer, "ver"); bindChip(fltConf, "conf");
  if (fltOn) fltOn.addEventListener("click", function () {
    filters.on = true; filters.seen = false; syncOnSeenButtons(); renderList();
  });
  if (fltSeen) fltSeen.addEventListener("click", function () {
    filters.seen = true; filters.on = false; syncOnSeenButtons(); renderList();
  });
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
  if (refreshBtn) refreshBtn.addEventListener("click", function () { setStatus("re-tuning...", "live"); load(); });

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
  // Paint from local history immediately (real, dimmed) so the page is usable
  // before the first fetch; the live fetch then enriches/replaces it. If there
  // is no history, the honest empty state shows until data arrives.
  function recenterDial() {
    if (!hasDial) return;
    if (!dialMeasure()) { requestAnimationFrame(recenterDial); return; }
    if (dialBands.length) buildStrip(); else buildRuler();
    dialApply();
  }
  (function initialPaint() {
    var hist = loadHistory();
    var seenBands = bandsFromHistory(hist, []);
    bands = seenBands;
    renderList();
    applyDialBands(bands);
    setStatus("tuning in to the broker...", "live");
    // Re-measure once layout settles (rAF) and again once the mono labels have
    // loaded (fonts change metrics), so the strip center keeps tracking the
    // fixed CSS needle. ResizeObserver covers later reflows.
    requestAnimationFrame(recenterDial);
    if (document.fonts && document.fonts.ready && document.fonts.ready.then) {
      document.fonts.ready.then(recenterDial);
    }
    startRAF();
  })();
  load();
  schedule();
})();
