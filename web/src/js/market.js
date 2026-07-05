/* =====================================================================
   RogerAI - live "signal tower" market view.
   Pulls REAL offers from the broker (GET /discover), aggregates them
   per-model into channels, and paints each channel's signal strength as
   ▁▂▃▄▅▆▇█ tower bars. Signal = f(#online providers, measured tok/s).
   More providers online -> stronger signal, lower price; as a channel
   fills, signal/quality dip and price drifts up.

   Coherence with the CLI/TUI:
     glyph kit ▁▂▃▄▅▆▇█ (signal), ◆ gold lineage, ●/○ online/offline,
     ((•)) on-air pulse, same hues (volt/live/ember/gold), $/1M money.

   Robustness (REAL-DATA-ONLY, like the Models page):
     - when /market + /discover are empty or unreachable, shows an honest
       "the band is quiet" empty state - never a fabricated demo band.
     - rAF for the bar shimmer; pauses offscreen + when tab hidden.
     - honors prefers-reduced-motion (static bars, no shimmer, no poll).
   ===================================================================== */
(function () {
  "use strict";

  /* ---------- money meter readout (pure) ------------------------- */
  // The RATE / REPLY line under the band panel reads the SAME live channels as
  // the rows above - never hardcoded figures (the launch page once shipped a
  // made-up "$0.18 - $0.55" while every on-air band was free). Readout:
  //   { kind:"quiet" }                              nothing on air -> keep the
  //                                                 neutral no-figures copy
  //   { kind:"free",   rate:"FREE",         reply:"$0.00" }
  //   { kind:"priced", rate:"$0.18 - $0.55" reply:"~$0.00000876", mid }
  // Rate spans the ON-AIR bands' out-prices ($/1M, each band at its cheapest
  // station, straight from /market min_price); the reply example prices
  // REPLY_TOK output tokens at the mid rate. Idle stations never drive it.
  var REPLY_TOK = 24;

  function fmtPrice(p) {
    if (!p) return "free";
    return "$" + (p < 1 ? p.toFixed(2) : p.toFixed(2));
  }
  // usd delegates to RogerFmt (the ONE canonical money renderer, Go-parity 3
  // significant figures sub-cent) and only falls back to the same local rule
  // dashboard.js uses if fmt.js didn't load - a real cost never reads "$0.00".
  function usd(n) {
    if (typeof window !== "undefined" && window.RogerFmt) return window.RogerFmt.usd(n);
    if (!n || !isFinite(n)) return "$0.00";
    if (n >= 0.01) return "$" + n.toFixed(2);
    var p = n.toPrecision(3);
    if (/e/i.test(p)) p = n.toFixed(20).replace(/0+$/, "");
    else p = p.replace(/0+$/, "").replace(/\.$/, "");
    return "$" + p;
  }
  function meterReadout(channels) {
    var live = (channels || []).filter(function (c) { return c && c.live; });
    if (!live.length) return { kind: "quiet" };
    var min = Infinity, max = 0;
    live.forEach(function (c) {
      var p = +c.price;
      if (!isFinite(p) || p < 0) p = 0;   // unpriced/junk folds to free, never invented
      if (p < min) min = p;
      if (p > max) max = p;
    });
    if (max <= 0) return { kind: "free", rate: "FREE", reply: usd(0) };
    var mid = (min + max) / 2;
    var rate = min === max ? fmtPrice(max) : fmtPrice(min) + " - " + fmtPrice(max);
    return { kind: "priced", rate: rate, reply: "~" + usd(mid * REPLY_TOK / 1e6), mid: mid };
  }

  /* ---------- transient-error resilience (defense in depth) ------------
     RELEASE-DAY root cause: the broker's public reads (/market, /discover)
     could answer a transient HTTP 429 ("slow down"); a 429 error body carries
     NO `.market` / `.offers`, so the old code read an empty list and painted
     the "band is quiet" empty state - an on-air market blinked to EMPTY (the
     "dial flickers to empty" incident). The broker fix removes that gate, but
     as defense in depth the client must ALSO never blank on a transient non-200:
     if neither endpoint returned a usable 200 body this poll AND we have a
     last-known market, we HOLD it rather than blanking. Only a broker that is
     REACHABLE and genuinely returns an EMPTY list paints the honest quiet state.
     A 200-with-live-market always renders fresh. */
  function decideRender(poll) {
    poll = poll || {};
    if ((+poll.liveCount || 0) > 0) return "live";      // fresh live data - always render it
    // No live market this poll. If BOTH public reads failed to return a usable 200 body
    // (a transient error - 429, 5xx, a network blip), keep the last-known market on screen
    // instead of blanking; fall back to an honest "unreachable" only when we have nothing.
    if (!poll.marketOK && !poll.discoverOK) {
      return (+poll.prevCount || 0) > 0 ? "hold" : "quiet-unreachable";
    }
    // At least one endpoint answered 200 but with an empty/no-live list: an honest quiet market.
    return "quiet-empty";
  }

  // parseRetryAfter reads a Retry-After header (integer seconds; a non-numeric/absent value
  // yields 0) and returns it in MILLISECONDS, so a 429 can pace the next poll to what the
  // server asked for. HTTP-date form is not emitted by the broker, so only seconds is parsed.
  function parseRetryAfter(res) {
    if (!res || typeof res.headers === "undefined" || !res.headers) return 0;
    var raw = "";
    try { raw = res.headers.get ? res.headers.get("Retry-After") : ""; } catch (_) { raw = ""; }
    var secs = parseInt(raw, 10);
    return isFinite(secs) && secs > 0 ? secs * 1000 : 0;
  }

  // Node test seam (mirrors dashboard.js): in a non-DOM runtime, export the pure
  // bits and skip all DOM/fetch below. The browser path runs exactly as before.
  if (typeof document === "undefined") {
    if (typeof module !== "undefined" && module.exports) {
      module.exports = { meterReadout: meterReadout, fmtPrice: fmtPrice, decideRender: decideRender, parseRetryAfter: parseRetryAfter };
    }
    return;
  }

  var listEl = document.getElementById("marketList");
  if (!listEl) return;

  var statusText = document.getElementById("marketStatusText");
  var statusWrap = document.getElementById("marketStatus");
  var footEl = document.getElementById("marketFoot");
  var refreshBtn = document.getElementById("marketRefresh");
  var section = document.getElementById("market");
  var meterRateEl = document.getElementById("meterRate");
  var meterReplyEl = document.getElementById("meterReply");

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var MARKET = BROKER + "/market";
  var DISCOVER = BROKER + "/discover";
  var POLL_MS = 30000;             // re-tune the band every 30s
  var BARGLYPH = "▁▂▃▄▅▆▇█";       // signal tower glyphs (8 levels)

  var pollTimer = null;
  var rafId = null;
  var rendered = [];               // [{model, providers, tps, price, quality, signal, verified, live}]
  var shimmer = 0;
  var visible = false;
  var inflight = false;
  var retryAfterMs = 0;            // a 429's Retry-After hint (ms) applied to the NEXT poll only

  /* ---------- tiny DOM helpers ----------------------------------- */
  function el(tag, cls, html) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (html != null) n.innerHTML = html;
    return n;
  }
  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }

  /* ---------- signal math --------------------------------------- */
  // The broker is the SINGLE source of truth for signal + quality now: /market and
  // /discover carry the authoritative 0..100 composite (supply + speed + latency +
  // verified-serving + reliability + trust, less congestion) plus a per-term
  // breakdown. We DO NOT recompute a divergent local value off /market - we consume
  // the broker's number so the website meter matches the CLI/TUI/broker exactly.
  // These two locals remain ONLY as a last-ditch fallback for the older /discover
  // aggregation path when an offer somehow lacks a broker signal.
  function computeSignal(providers, tps) {
    var supply = Math.min(1, Math.log2(providers + 1) / 3.5);
    var speed = Math.min(1, tps / 90);
    return Math.max(0.08, 0.62 * supply + 0.38 * speed);
  }
  function computeQuality(providers, tps) {
    return Math.max(0.25, Math.min(1, 0.45 * Math.min(1, tps / 80) + 0.55 * Math.min(1, providers / 4)));
  }
  // round a 0..1 fraction to a whole-percent string for the tooltip breakdown.
  function pct(x) { return Math.round(Math.max(0, Math.min(1, x)) * 100) + "%"; }
  // format a probe TTFT (ms) compactly; "-" when unmeasured.
  function fmtTTFT(ms) {
    ms = +ms || 0;
    if (ms <= 0) return "-";
    if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10000 ? 0 : 1) + "s";
    return Math.round(ms) + "ms";
  }
  // render an 8-cell tower string at a given level, with an animated
  // "head" cell that breathes when there's live signal.
  function towerBars(level, animate) {
    var cells = 8;
    var filled = Math.max(0, Math.min(cells, Math.round(level * cells)));
    var html = "";
    for (var i = 0; i < cells; i++) {
      if (i < filled) {
        // top live cell shimmers a touch when animating
        var head = (i === filled - 1);
        var g = BARGLYPH[Math.min(7, Math.round((i / (cells - 1)) * 7))];
        var cls = "sigbar sigbar--on" + (head && animate ? " sigbar--head" : "");
        html += '<span class="' + cls + '">' + g + "</span>";
      } else {
        html += '<span class="sigbar sigbar--off">·</span>';
      }
    }
    return html;
  }
  function qualityDots(q) {
    var n = Math.round(q * 5);
    var s = "";
    for (var i = 0; i < 5; i++) s += i < n ? "●" : "○";
    return s;
  }

  /* ---------- aggregate broker offers -> channels --------------- */
  function aggregate(offers) {
    var byModel = {};
    offers.forEach(function (o) {
      if (!o || !o.model) return;
      var k = o.model;
      var m = byModel[k] || (byModel[k] = {
        model: k, providers: 0, online: 0, tpsSum: 0, tpsN: 0,
        minPriceOut: Infinity, verified: false, hw: o.hw || "",
        bestSignal: 0, bestTTFT: 0, succSum: 0, succN: 0, anyVerified: false,
        priceTier: 0
      });
      m.providers++;
      var on = o.online !== false;
      if (on) m.online++;
      var tps = +o.tps || 0;
      if (tps > 0) { m.tpsSum += tps; m.tpsN++; }
      var po = (o.price_out != null ? +o.price_out : +o.price_in);
      // The model's $-tier follows its CHEAPEST out-priced offer (the best deal), matching
      // the broker's per-model /market price_tier - so both data paths agree.
      if (po != null && !isNaN(po) && po < m.minPriceOut) { m.minPriceOut = po; m.priceTier = (+o.price_tier || 0); }
      // broker's per-offer signal is authoritative; take the band's strongest.
      if (on && +o.signal > m.bestSignal) m.bestSignal = +o.signal;
      var tt = +o.ttft_ms || 0;
      if (on && tt > 0 && (m.bestTTFT === 0 || tt < m.bestTTFT)) m.bestTTFT = tt;
      if (on && o.success != null) { m.succSum += Math.max(0, Math.min(1, +o.success)); m.succN++; }
      if (on && o.verified) m.anyVerified = true;
      // confidential routes carry the gold call-sign too
      if (o.confidential) m.verified = true;
    });

    return Object.keys(byModel).map(function (k) {
      var m = byModel[k];
      var online = m.online || 0;
      var tps = m.tpsN ? m.tpsSum / m.tpsN : 0;
      var price = m.minPriceOut === Infinity ? 0 : m.minPriceOut;
      return {
        model: m.model,
        providers: online,
        total: m.providers,
        tps: tps,
        price: price,
        priceTier: m.priceTier || 0,
        // prefer the broker's per-offer composite; only fall back to local math if no
        // offer carried a signal (older broker).
        signal: online > 0 ? (m.bestSignal > 0 ? m.bestSignal / 100 : computeSignal(online, tps || 30)) : 0,
        quality: online > 0 ? computeQuality(online, tps || 30) : 0,
        ttft: m.bestTTFT,
        success: m.succN ? m.succSum / m.succN : null,
        verified: m.verified || m.anyVerified,
        terms: null,
        live: online > 0
      };
    }).sort(function (a, b) { return b.signal - a.signal; });
  }

  /* ---------- map the authoritative /market band shape ---------- */
  // /market is per-band: { model/band, providers, in_flight, min_price, best_tps,
  // ttft_ms, quality, success_rate, verified, signal 0-100, terms{...} }. We CONSUME
  // the broker's signal/terms directly (no local recompute) so the website meter
  // equals the broker/CLI/TUI value, and surface ttft + success next to it so the
  // number is explainable.
  function fromMarket(rows) {
    return rows.map(function (m) {
      var providers = +m.providers || 0;
      var live = providers > 0;
      var sig = m.signal != null ? Math.max(0, Math.min(1, (+m.signal) / 100)) : 0;
      var q = m.quality != null ? Math.max(0, Math.min(1, +m.quality > 1 ? (+m.quality) / 100 : +m.quality)) : 0;
      if (!q && m.success_rate != null) q = Math.max(0, Math.min(1, +m.success_rate > 1 ? (+m.success_rate) / 100 : +m.success_rate));
      var tps = +(m.best_tps || m.tps) || 0;
      var price = m.min_price != null ? +m.min_price : (m.price_out != null ? +m.price_out : 0);
      return {
        model: m.model || m.band || "unknown",
        providers: providers, total: providers, tps: tps, price: price,
        priceTier: (+m.price_tier || 0),
        // broker signal is authoritative; the local computeSignal is only used if the
        // broker somehow omitted it (older broker), never to OVERRIDE it.
        signal: live ? (m.signal != null ? sig : computeSignal(providers, tps || 30)) : 0,
        quality: live ? (q || computeQuality(providers, tps || 30)) : 0,
        ttft: +m.ttft_ms || 0,
        success: m.success_rate != null ? Math.max(0, Math.min(1, +m.success_rate)) : null,
        verified: !!(m.confidential || m.verified),
        terms: m.terms || null,
        live: live
      };
    }).filter(function (c) { return c.model && c.model !== "unknown"; })
      .sort(function (a, b) { return b.signal - a.signal; });
  }

  /* ---------- honest empty state -------------------------------- */
  // Real-data-only, like the Models page: when no station is on air (or the
  // broker is unreachable) show an honest "the band is quiet" state - never
  // fabricated station rows. No station, no signal.
  function paintQuiet() {
    stopShimmer();
    rendered = [];
    listEl.innerHTML =
      '<li class="mkt-quiet">' +
        '<span class="mkt-quiet__txt">The band is quiet right now - no stations on air yet. ' +
          'Put a GPU on air with <code class="mono">roger share</code>, or ' +
          '<a href="models.html">sweep the dial &rarr;</a>' +
        '</span>' +
      '</li>';
    paintMeter({ kind: "quiet" });
  }

  /* ---------- the money meter (RATE / REPLY under the band) ------ */
  // Hydrates the meter from the live readout; quiet/unreachable restores the
  // neutral static copy (honest words, no figures - the HTML ships the same
  // fallback for no-JS). WALLET is mechanism, not a number: it stays put.
  function paintMeter(r) {
    if (!meterRateEl || !meterReplyEl) return;
    var K = '<span class="meter__k">';
    meterRateEl.title = "";
    meterReplyEl.title = "";
    if (!r || r.kind === "quiet") {
      meterRateEl.innerHTML = K + 'RATE</span><b>live market</b>, set by operators';
      meterReplyEl.innerHTML = K + 'REPLY</span><b>priced per token</b>, in and out';
    } else if (r.kind === "free") {
      meterRateEl.title = "every band on air is at $0 / 1M right now";
      meterRateEl.innerHTML = K + 'RATE</span><b class="ember">FREE</b> right now - every band on the house';
      meterReplyEl.innerHTML = K + 'REPLY</span><b>' + r.reply + '</b> / ' + REPLY_TOK + ' tok out';
    } else {
      meterRateEl.title = "out-price range across the bands on air, each at its cheapest station";
      meterReplyEl.title = REPLY_TOK + " output tokens at the band's mid rate";
      meterRateEl.innerHTML = K + 'RATE</span><b>' + r.rate + '</b> / 1M tok';
      meterReplyEl.innerHTML = K + 'REPLY</span><b>' + r.reply + '</b> / ' + REPLY_TOK + ' tok out';
    }
  }

  /* ---------- render -------------------------------------------- */
  // fmtTier renders the broker's per-MODEL neutral $-tier (0..4) for the model's BEST price:
  // "$".."$$$$" + a "good price" chip on tier 1 ONLY. Mirrors the bands.js / TUI / broker
  // contract so every surface reads alike. The broker already folds FREE/unknown into tier 0
  // (-> ""), so the per-model row keys on the tier itself; never any negative wording.
  function fmtTier(tier) {
    tier = +tier || 0;
    if (tier < 1 || tier > 4) return "";
    var bars = '<span class="price-tier" title="our read of this model’s best price vs the going rate">' +
      "$".repeat(tier) + "</span>";
    var chip = tier === 1 ? ' <span class="band-tag band-tag--deal">good price</span>' : "";
    return " " + bars + chip;
  }
  function rowHTML(c, animate) {
    var dot = c.live
      ? '<span class="mkt-dot mkt-dot--on" aria-hidden="true">●</span>'
      : '<span class="mkt-dot mkt-dot--off" aria-hidden="true">○</span>';
    var cs = c.verified ? ' <span class="cs" title="lineage-verified">◆</span>' : "";
    var prov = c.live
      ? '<span class="mkt-prov">' + c.providers + ' on air</span>'
      : '<span class="mkt-prov mkt-prov--idle">idle</span>';

    // speed line: measured tok/s + probe TTFT (the explainable latency number).
    var ttftTxt = c.live && c.ttft > 0
      ? '<span class="mkt-unit mkt-ttft" title="probe time-to-first-token"> · ' + fmtTTFT(c.ttft) + ' ttft</span>'
      : '';
    var speed = c.live
      ? '<b class="mono mkt-tps">' + Math.round(c.tps) + '</b><span class="mkt-unit"> t/s</span>' + ttftTxt
      : '<span class="mkt-unit mkt-unit--idle">-</span>';

    var price = c.live
      ? '<b class="mono ember">' + fmtPrice(c.price) + '</b><span class="mkt-unit"> /1M</span>' + fmtTier(c.priceTier)
      : '<span class="mkt-unit mkt-unit--idle">' + fmtPrice(c.price) + ' /1M</span>';

    // explain the meter: a hover tooltip with the broker's per-term breakdown, and a
    // visible "success" readout next to the quality dots so the number is grounded.
    var sigTitle = c.live ? signalTitle(c) : "";
    var succTxt = c.live && c.success != null
      ? '<span class="mkt-unit mkt-succ" title="time-decayed success (organic or probe)"> ' + pct(c.success) + ' ok</span>'
      : (c.live && c.verified ? '<span class="mkt-unit mkt-succ" title="probe-verified serving"> verified</span>' : '');

    return (
      '<span class="mkt-cell mkt-cell--model">' +
        dot + '<span class="mkt-model">' + esc(c.model) + cs + '</span>' + prov +
      '</span>' +
      '<span class="mkt-cell mkt-cell--signal"' + (sigTitle ? ' title="' + esc(sigTitle) + '"' : '') + '>' +
        '<span class="sig" aria-hidden="true">' + towerBars(c.signal, animate && c.live) + '</span>' +
      '</span>' +
      '<span class="mkt-cell mkt-cell--speed">' + speed + '</span>' +
      '<span class="mkt-cell mkt-cell--quality">' +
        '<span class="qdots' + (c.live ? '' : ' qdots--idle') + '" aria-hidden="true">' +
          qualityDots(c.quality) + '</span>' + succTxt +
      '</span>' +
      '<span class="mkt-cell mkt-cell--price">' + price + '</span>'
    );
  }

  // signalTitle builds the meter's explain-tooltip from the broker's per-term
  // contributions (points). Falls back to a compact "signal NN/100" when the broker
  // did not send a breakdown (older broker / discover fallback).
  function signalTitle(c) {
    var t = c.terms;
    var sig = Math.round(c.signal * 100);
    if (!t) return "signal " + sig + "/100";
    function pt(x) { return Math.round(+x || 0); }
    var parts = [
      "supply " + pt(t.supply),
      "speed " + pt(t.speed),
      "latency " + pt(t.latency),
      "verified " + pt(t.verified),
      "success " + pt(t.success),
      "trust " + pt(t.trust)
    ];
    var s = "signal " + sig + "/100 = " + parts.join(" + ");
    if (+t.congestion > 0) s += "  (-" + Math.round(+t.congestion * 100) + "% congestion)";
    return s;
  }

  function paint(channels, animate) {
    rendered = channels.slice(0, 6);
    listEl.innerHTML = "";
    rendered.forEach(function (c, i) {
      var li = el("li", "mkt-row" + (c.live ? "" : " mkt-row--idle"), rowHTML(c, animate));
      li.style.setProperty("--i", i);
      listEl.appendChild(li);
    });
  }

  /* ---------- live signal "VU" (rAF, only the head bars) ----------
     Each on-air channel's top cell breathes on its own phase, like a
     fluctuating signal meter rather than a uniform fade: opacity rides
     a sine and, at the peak of the breath, the glyph ticks up one notch
     on the ▁▂▃▄▅▆▇█ ramp so the level reads as ALIVE, not as decoration.
     The swap is RELATIVE to each cell's own base glyph, so a weak band
     never jumps to a full bar. Cheap: one sine + an optional glyph swap. */
  function tick() {
    shimmer += 0.035;
    var heads = listEl.querySelectorAll(".sigbar--head");
    for (var i = 0; i < heads.length; i++) {
      var h = heads[i];
      // remember each head's base glyph + its one-notch-up neighbour once
      if (h.__lo == null) {
        h.__lo = h.textContent;
        var bi = BARGLYPH.indexOf(h.__lo);
        h.__hi = bi >= 0 ? BARGLYPH[Math.min(BARGLYPH.length - 1, bi + 1)] : h.__lo;
        h.__ph = i * 0.9;   // stagger so the band doesn't pulse in lockstep
      }
      var s = 0.5 + 0.5 * Math.sin(shimmer + h.__ph);
      h.style.opacity = (0.5 + 0.5 * s).toFixed(3);
      var want = s > 0.82 ? h.__hi : h.__lo;   // peak of the breath = +1 notch
      if (h.textContent !== want) h.textContent = want;
    }
    rafId = requestAnimationFrame(tick);
  }
  function startShimmer() {
    if (REDUCED || rafId || !visible) return;
    rafId = requestAnimationFrame(tick);
  }
  function stopShimmer() {
    if (rafId) { cancelAnimationFrame(rafId); rafId = null; }
  }

  /* ---------- status helpers ------------------------------------ */
  function setStatus(text, mode) {
    if (statusText) statusText.textContent = text;
    if (statusWrap) {
      statusWrap.classList.toggle("is-live", mode === "live");
      statusWrap.classList.toggle("is-demo", mode === "demo");
      statusWrap.classList.toggle("is-off", mode === "off");
    }
  }
  function setFoot(html) { if (footEl) footEl.innerHTML = html; }

  /* ---------- fetch + refresh ----------------------------------- */
  // holdLastKnown keeps the last-known market painted on a TRANSIENT failure (a non-200 such
  // as 429, or an unreachable broker) instead of blanking it - defense in depth for the
  // "flickers to empty" incident. It leaves the existing rows on screen (the rAF shimmer keeps
  // running) and only refreshes the status line.
  function holdLastKnown() {
    var live = rendered.filter(function (c) { return c && c.live; }).length;
    setStatus("holding the last band read · re-tuning…", "live");
    setFoot('holding the last read from <span class="ember">broker.rogerai.fyi</span> · ' +
      (live ? live + ' band' + (live === 1 ? '' : 's') + ' shown' : 'retrying') + ' · auto-refresh 30s');
    startShimmer();
  }

  function load() {
    if (inflight) return;
    inflight = true;
    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 8000);

    // authoritative /market first (per-band signal 0-100); /discover is the aggregation
    // fallback; a last-known HOLD (transient non-200) or an honest "quiet" empty state is the
    // last resort. A non-200 is NOT thrown to the blank-the-UI path - it is treated as "no
    // usable body" and its Retry-After hint is captured to pace the next poll.
    var marketOK = false, discoverOK = false;
    fetch(MARKET, { signal: ctrl ? ctrl.signal : undefined, cache: "no-store" })
      .then(function (r) {
        if (!r.ok) { retryAfterMs = retryAfterMs || parseRetryAfter(r); return null; }
        marketOK = true;
        return r.json();
      })
      .then(function (data) {
        var rows = (data && Array.isArray(data.market)) ? data.market : [];
        var channels = fromMarket(rows);
        var nOnline = channels.filter(function (c) { return c.live; }).length;
        if (channels.length && nOnline > 0) {
          clearTimeout(to);
          paint(channels, true);
          paintMeter(meterReadout(channels));   // ALL on-air bands, not just the 6 painted rows
          setStatus(nOnline + " band" + (nOnline === 1 ? "" : "s") + " on air · live from /market", "live");
          setFoot('live from <span class="ember">broker.rogerai.fyi/market</span> · signal 0-100 · prices in $ / 1M · auto-refresh 30s');
          startShimmer();
          return;
        }
        // /market empty (or a non-200) -> try /discover aggregation
        return fetch(DISCOVER, { signal: ctrl ? ctrl.signal : undefined, cache: "no-store" })
          .then(function (r) {
            if (!r.ok) { retryAfterMs = retryAfterMs || parseRetryAfter(r); return null; }
            discoverOK = true;
            return r.json();
          })
          .then(function (d) {
            clearTimeout(to);
            var offers = (d && Array.isArray(d.offers)) ? d.offers : [];
            var live = offers.length ? offers.filter(function (o) { return o && o.online !== false; }).length : 0;
            var action = decideRender({ liveCount: live, marketOK: marketOK, discoverOK: discoverOK, prevCount: rendered.length });
            if (action === "live") {
              var ch = aggregate(offers);
              paint(ch, true);
              paintMeter(meterReadout(ch));
              var nOn = ch.filter(function (c) { return c.live; }).length;
              setStatus(nOn + " band" + (nOn === 1 ? "" : "s") + " on air · from /discover", "live");
              setFoot('live from <span class="ember">broker.rogerai.fyi/discover</span> · prices in $ / 1M tokens · auto-refresh 30s');
              startShimmer();
            } else if (action === "hold") {
              // transient non-200 on BOTH reads, but we have a last-known market: KEEP it.
              holdLastKnown();
            } else if (action === "quiet-unreachable") {
              // both reads failed and nothing to hold: honest "couldn't reach" state.
              paintQuiet();
              setStatus("couldn't reach the broker just now", "off");
              setFoot('couldn\'t reach <span class="ember">broker.rogerai.fyi</span> · no live band to show');
            } else {
              // broker reachable but genuinely empty: honest quiet state, no fake rows
              paintQuiet();
              setStatus("the band is quiet right now - no stations on air yet", "demo");
              setFoot('broker reachable · <span class="ember">no stations on air yet</span> · this is a preview, not a live band');
            }
          });
      })
      .catch(function () {
        clearTimeout(to);
        // network error / abort: HOLD the last-known market rather than blanking; only fall to
        // the honest "unreachable" state when there is nothing to hold.
        if (decideRender({ liveCount: 0, marketOK: false, discoverOK: false, prevCount: rendered.length }) === "hold") {
          holdLastKnown();
        } else {
          paintQuiet();
          setStatus("couldn't reach the broker just now", "off");
          setFoot('couldn\'t reach <span class="ember">broker.rogerai.fyi</span> · no live band to show');
        }
      })
      .then(function () { inflight = false; });
  }

  function schedule() {
    if (REDUCED) return;          // no background polling under reduced-motion
    clearTimeout(pollTimer);
    // Honor a 429's Retry-After for the NEXT poll only (never faster than the base cadence),
    // then reset so a one-off throttle doesn't slow the steady state.
    var wait = Math.max(POLL_MS, retryAfterMs || 0);
    retryAfterMs = 0;
    pollTimer = setTimeout(function () { if (visible) load(); schedule(); }, wait);
  }

  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () {
      setStatus("re-tuning…", "live");
      load();
    });
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) { stopShimmer(); }
    else if (visible) { startShimmer(); }
  });

  /* ---------- kick off when scrolled into view ------------------ */
  function activate() {
    visible = true;
    load();
    schedule();
    startShimmer();
  }

  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        visible = e.isIntersecting;
        if (e.isIntersecting) { if (!rendered.length) activate(); else startShimmer(); }
        else stopShimmer();
      });
    }, { threshold: 0.15 });
    if (section) io.observe(section);
  } else {
    activate();
  }
})();
