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

   Robustness:
     - degrades gracefully when /discover is empty or unreachable
       (renders a representative demo band, clearly labelled).
     - rAF for the bar shimmer; pauses offscreen + when tab hidden.
     - honors prefers-reduced-motion (static bars, no shimmer, no poll).
   ===================================================================== */
(function () {
  "use strict";

  var listEl = document.getElementById("marketList");
  if (!listEl) return;

  var statusText = document.getElementById("marketStatusText");
  var statusWrap = document.getElementById("marketStatus");
  var footEl = document.getElementById("marketFoot");
  var refreshBtn = document.getElementById("marketRefresh");
  var section = document.getElementById("market");

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var DISCOVER = BROKER + "/discover";
  var POLL_MS = 30000;             // re-tune the band every 30s
  var BARGLYPH = "▁▂▃▄▅▆▇█";       // signal tower glyphs (8 levels)

  var pollTimer = null;
  var rafId = null;
  var rendered = [];               // [{model, providers, tps, price, quality, signal, verified, live}]
  var shimmer = 0;
  var visible = false;
  var inflight = false;

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

  /* ---------- signal math (the supply/demand story) -------------- */
  // signal 0..1 from provider count + measured speed; more online
  // providers => stronger. Cap so a single fast node still reads "ok".
  function computeSignal(providers, tps) {
    var supply = Math.min(1, Math.log2(providers + 1) / 3.5);   // 1->0.29, 3->0.57, 7->0.86
    var speed = Math.min(1, tps / 90);                          // 90 t/s ~= full bars
    return Math.max(0.08, 0.62 * supply + 0.38 * speed);
  }
  // quality 0..1: blend of speed + redundancy (more providers = resilient).
  function computeQuality(providers, tps) {
    return Math.max(0.25, Math.min(1, 0.45 * Math.min(1, tps / 80) + 0.55 * Math.min(1, providers / 4)));
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
        minPriceOut: Infinity, verified: false, hw: o.hw || ""
      });
      m.providers++;
      var on = o.online !== false;
      if (on) m.online++;
      var tps = +o.tps || 0;
      if (tps > 0) { m.tpsSum += tps; m.tpsN++; }
      var po = (o.price_out != null ? +o.price_out : +o.price_in);
      if (po != null && !isNaN(po) && po < m.minPriceOut) m.minPriceOut = po;
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
        signal: online > 0 ? computeSignal(online, tps || 30) : 0,
        quality: online > 0 ? computeQuality(online, tps || 30) : 0,
        verified: m.verified,
        live: online > 0
      };
    }).sort(function (a, b) { return b.signal - a.signal; });
  }

  /* ---------- demo band (graceful fallback) --------------------- */
  // A representative, plausible band shown when no providers are on air
  // yet (or the broker is unreachable). Clearly labelled as a preview.
  function demoBand() {
    var seed = [
      { model: "qwen3-coder-30b", providers: 6, tps: 58, price: 0.22, verified: true },
      { model: "qwen3-72b",       providers: 4, tps: 71, price: 0.38, verified: true },
      { model: "gpt-oss-120b",    providers: 3, tps: 63, price: 0.55, verified: true },
      { model: "llama3.3-70b",    providers: 5, tps: 44, price: 0.31, verified: false },
      { model: "deepseek-v3",     providers: 2, tps: 55, price: 0.61, verified: false },
      { model: "mistral-large",   providers: 0, tps: 0,  price: 0.49, verified: false }
    ];
    return seed.map(function (s) {
      return {
        model: s.model, providers: s.providers, total: s.providers,
        tps: s.tps, price: s.price, verified: s.verified, live: s.providers > 0,
        signal: s.providers > 0 ? computeSignal(s.providers, s.tps) : 0,
        quality: s.providers > 0 ? computeQuality(s.providers, s.tps) : 0
      };
    }).sort(function (a, b) { return b.signal - a.signal; });
  }

  /* ---------- render -------------------------------------------- */
  function fmtPrice(p) {
    if (!p) return "free";
    return "$" + (p < 1 ? p.toFixed(2) : p.toFixed(2));
  }
  function rowHTML(c, animate) {
    var dot = c.live
      ? '<span class="mkt-dot mkt-dot--on" aria-hidden="true">●</span>'
      : '<span class="mkt-dot mkt-dot--off" aria-hidden="true">○</span>';
    var cs = c.verified ? ' <span class="cs" title="lineage-verified">◆</span>' : "";
    var prov = c.live
      ? '<span class="mkt-prov">' + c.providers + ' on air</span>'
      : '<span class="mkt-prov mkt-prov--idle">idle</span>';

    var speed = c.live
      ? '<b class="mono mkt-tps">' + Math.round(c.tps) + '</b><span class="mkt-unit"> t/s</span>'
      : '<span class="mkt-unit mkt-unit--idle">-</span>';

    var price = c.live
      ? '<b class="mono ember">' + fmtPrice(c.price) + '</b><span class="mkt-unit"> /1M</span>'
      : '<span class="mkt-unit mkt-unit--idle">' + fmtPrice(c.price) + ' /1M</span>';

    return (
      '<span class="mkt-cell mkt-cell--model">' +
        dot + '<span class="mkt-model">' + esc(c.model) + cs + '</span>' + prov +
      '</span>' +
      '<span class="mkt-cell mkt-cell--signal">' +
        '<span class="sig" aria-hidden="true">' + towerBars(c.signal, animate && c.live) + '</span>' +
      '</span>' +
      '<span class="mkt-cell mkt-cell--speed">' + speed + '</span>' +
      '<span class="mkt-cell mkt-cell--quality">' +
        '<span class="qdots' + (c.live ? '' : ' qdots--idle') + '" aria-hidden="true">' +
          qualityDots(c.quality) + '</span>' +
      '</span>' +
      '<span class="mkt-cell mkt-cell--price">' + price + '</span>'
    );
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

  /* ---------- shimmer (rAF, only the live head bars) ------------- */
  function tick() {
    shimmer += 0.04;
    var heads = listEl.querySelectorAll(".sigbar--head");
    var o = 0.55 + 0.45 * (0.5 + 0.5 * Math.sin(shimmer));
    for (var i = 0; i < heads.length; i++) heads[i].style.opacity = o.toFixed(3);
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
  function load() {
    if (inflight) return;
    inflight = true;
    var ctrl = ("AbortController" in window) ? new AbortController() : null;
    var to = setTimeout(function () { if (ctrl) ctrl.abort(); }, 8000);

    fetch(DISCOVER, { signal: ctrl ? ctrl.signal : undefined, cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error("http " + r.status); return r.json(); })
      .then(function (data) {
        clearTimeout(to);
        var offers = (data && Array.isArray(data.offers)) ? data.offers : [];
        var live = offers.length ? offers.filter(function (o) { return o && o.online !== false; }).length : 0;

        if (offers.length > 0) {
          var channels = aggregate(offers);
          paint(channels, true);
          var nOnline = channels.filter(function (c) { return c.live; }).length;
          setStatus(nOnline + " channel" + (nOnline === 1 ? "" : "s") + " on air · " +
                    live + " provider" + (live === 1 ? "" : "s") + " live", "live");
          setFoot('live from <span class="ember">broker.rogerai.fyi/discover</span> · prices in $ / 1M tokens · auto-refresh 30s');
        } else {
          // broker reachable but nobody is broadcasting yet -> demo band
          paint(demoBand(), true);
          setStatus("the band is quiet right now - a preview of how it looks on air", "demo");
          setFoot('broker reachable · <span class="ember">no providers on air yet</span> · showing a representative band');
        }
        startShimmer();
      })
      .catch(function () {
        clearTimeout(to);
        // unreachable -> still show a beautiful, labelled demo band
        paint(demoBand(), true);
        setStatus("preview band - couldn't reach the broker just now", "off");
        setFoot('couldn\'t reach <span class="ember">broker.rogerai.fyi</span> · showing a representative band');
        startShimmer();
      })
      .then(function () { inflight = false; });
  }

  function schedule() {
    if (REDUCED) return;          // no background polling under reduced-motion
    clearTimeout(pollTimer);
    pollTimer = setTimeout(function () { if (visible) load(); schedule(); }, POLL_MS);
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
