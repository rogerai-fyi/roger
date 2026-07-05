/* =====================================================================
   RogerAI - the live VOICES roster (/voices.html).
   Pulls the REAL public voice picker from the broker (GET /voices) and
   lists every on-air text-to-speech station, cheapest first. Each voice
   is an operator's station: a display name + a @callsign attribution, a
   language, a probe latency, and a $/1k-chars price (or a FREE badge).

   Coherence with the CLI/TUI + the Models board:
     ◉/○ on-air/idle dots, ((•)) beacon, the one live-red accent, mono
     numbers, and the SAME honest-empty-state discipline as market.js.

   Robustness (REAL-DATA-ONLY, like the Models page):
     - /voices empty ([]) -> an honest "the band is quiet" roster, never a
       fabricated voice.
     - broker unreachable -> an honest "couldn't reach the broker" state.
     - AbortController timeout, no-store fetch, 30s auto-refresh.
     - honors prefers-reduced-motion (no background polling).

   PRIVACY: /voices already carries NO node address, host or IP (the broker
   proxies all voice traffic). It exposes only the operator's STATION callsign -
   a pseudonymous @handle like @brave-otter, never the GitHub/Apple login.
   ===================================================================== */
(function () {
  "use strict";

  var listEl = document.getElementById("voiceList");
  if (!listEl) return;

  var statusText = document.getElementById("voiceStatusText");
  var statusWrap = document.getElementById("voiceStatus");
  var footEl = document.getElementById("voiceFoot");
  var refreshBtn = document.getElementById("voiceRefresh");
  var section = document.getElementById("directory");

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var BROKER = "https://broker.rogerai.fyi";
  var VOICES = BROKER + "/voices";
  var POLL_MS = 30000;             // re-read the roster every 30s

  var pollTimer = null;
  var visible = false;
  var inflight = false;
  var loadedOnce = false;
  var lastCount = 0;               // rows in the last successfully-painted roster
  var retryAfterMs = 0;            // a 429's Retry-After hint (ms), applied to the NEXT poll only

  // parseRetryAfter reads an integer-seconds Retry-After header and returns it in ms (0 if
  // absent/non-numeric), so a transient 429 can pace the next poll to what the server asked.
  function parseRetryAfter(res) {
    if (!res || !res.headers) return 0;
    var raw = "";
    try { raw = res.headers.get ? res.headers.get("Retry-After") : ""; } catch (_) { raw = ""; }
    var secs = parseInt(raw, 10);
    return isFinite(secs) && secs > 0 ? secs * 1000 : 0;
  }

  /* ---------- tiny DOM helpers ----------------------------------- */
  function el(tag, cls, html) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (html != null) n.innerHTML = html;
    return n;
  }
  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  /* ---------- formatting ---------------------------------------- */
  // fmtPrice renders the per-1k-chars rate the broker already computed. FREE is a
  // badge, not "$0.00"; a real charge shows a sub-cent-aware $ so the smallest rate
  // never collapses to "$0.00". Mirrors the CLI/TUI money voice.
  function fmtPrice(v) {
    if (window.RogerFmt && typeof window.RogerFmt.usd === "function") return window.RogerFmt.usd(v);
    v = +v;
    if (!isFinite(v) || v < 0) return "-";
    if (v === 0) return "$0.00";
    if (v >= 0.01) return "$" + v.toFixed(2);
    return "$" + Number(v.toPrecision(3));
  }
  // fmtLatency: a compact probe time-to-first-audio; "-" when unmeasured (0/omitted).
  function fmtLatency(ms) {
    ms = +ms || 0;
    if (ms <= 0) return "-";
    if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10000 ? 0 : 1) + "s";
    return Math.round(ms) + "ms";
  }
  // A voice's display name, falling back to its raw id (opaque) if nameless.
  function voiceName(v) { return v.name || v.id || "voice"; }
  // The operator attribution: "@station" (the broker sends a bare station callsign). A listed
  // voice ALWAYS carries an operator (anonymous nodes aren't listed), but stay
  // defensive - an operator-less row simply omits the "by @…".
  function operatorTag(v) {
    var op = v.operator || "";
    return op ? '<span class="voice-op mono">by @' + esc(op) + '</span>' : "";
  }

  /* ---------- normalize the /voices payload --------------------- */
  // Real data only: map straight from the broker's voiceView, keep just the fields
  // the roster shows, and re-sort cheapest-first defensively (the broker already
  // sorts, but a stable client sort makes the ordering self-evident).
  function normalize(rows) {
    return rows.map(function (v) {
      var price = (v.price_per_1k_chars != null) ? +v.price_per_1k_chars : 0;
      var free = !!v.free || price === 0;
      return {
        id: v.id || "",
        name: v.name || "",
        operator: v.operator || "",
        namespacedId: v.namespaced_id || "",
        language: v.language || "",
        latency: +v.latency_ms || 0,
        price: price,
        free: free
      };
    }).filter(function (v) { return v.id || v.name; })
      .sort(function (a, b) { return a.price - b.price; });
  }

  /* ---------- honest empty state -------------------------------- */
  // Real-data-only, like the Models page: when no voice is on air (or the broker is
  // unreachable) show an honest "quiet" roster - never a fabricated voice row.
  function paintQuiet() {
    listEl.innerHTML =
      '<li class="voice-quiet">' +
        '<span class="voice-quiet__txt">The band is quiet right now - no voices on air yet. ' +
          'Put a text-to-speech rig on air with <code class="mono">roger share</code>, or ' +
          '<a href="models.html">sweep the model dial &rarr;</a>' +
        '</span>' +
      '</li>';
  }

  /* ---------- render -------------------------------------------- */
  function rowHTML(v) {
    var dot = '<span class="voice-dot voice-dot--on" aria-hidden="true">◉</span>';
    var name = '<span class="voice-name">' + esc(voiceName(v)) + '</span>';

    var lang = v.language
      ? '<span class="voice-lang mono">' + esc(v.language) + '</span>'
      : '<span class="voice-unit--idle">—</span>';

    var lat = v.latency > 0
      ? '<b class="mono voice-lat">' + fmtLatency(v.latency) + '</b>'
      : '<span class="voice-unit--idle">-</span>';

    var price = v.free
      ? '<span class="band-tag band-tag--free">FREE</span>'
      : '<b class="mono ember">' + fmtPrice(v.price) + '</b><span class="voice-unit"> /1k</span>';

    return (
      '<span class="voice-cell voice-cell--name">' +
        '<span class="voice-name-line">' + dot + name + '</span>' +
        operatorTag(v) +
      '</span>' +
      '<span class="voice-cell voice-cell--lang">' + lang + '</span>' +
      '<span class="voice-cell voice-cell--lat">' + lat + '</span>' +
      '<span class="voice-cell voice-cell--price">' + price + '</span>'
    );
  }

  function paint(voices) {
    listEl.innerHTML = "";
    voices.forEach(function (v, i) {
      var li = el("li", "voice-row", rowHTML(v));
      li.style.setProperty("--i", i);
      listEl.appendChild(li);
    });
  }

  /* ---------- status helpers ------------------------------------ */
  function setStatus(text, mode) {
    if (statusText) statusText.textContent = text;
    if (statusWrap) {
      statusWrap.classList.toggle("is-live", mode === "live");
      statusWrap.classList.toggle("is-quiet", mode === "quiet");
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

    fetch(VOICES, { signal: ctrl ? ctrl.signal : undefined, cache: "no-store" })
      .then(function (r) {
        clearTimeout(to);
        loadedOnce = true;
        // A transient non-200 (e.g. a 429 "slow down") carries NO `.voices` - reading it as an
        // empty roster is exactly the "flickers to empty" bug. Do NOT blank: if we have a
        // last-known roster, HOLD it; capture Retry-After to pace the next poll.
        if (!r.ok) {
          retryAfterMs = parseRetryAfter(r);
          if (lastCount > 0) {
            setStatus("holding the last roster · re-reading…", "live");
          } else {
            paintQuiet();
            setStatus("couldn't reach the broker just now", "off");
            setFoot('couldn\'t reach <span class="ember">broker.rogerai.fyi</span> · no live roster to show');
          }
          return null;
        }
        return r.json();
      })
      .then(function (data) {
        if (data === null) return; // handled above (transient non-200 hold / quiet)
        var rows = (data && Array.isArray(data.voices)) ? data.voices : [];
        var voices = normalize(rows);
        if (voices.length) {
          paint(voices);
          lastCount = voices.length;
          var n = voices.length;
          setStatus(n + " voice" + (n === 1 ? "" : "s") + " on air · live from /voices", "live");
          setFoot('live from <span class="ember">broker.rogerai.fyi/voices</span> · metered by the character · prices in $ / 1k chars · auto-refresh 30s');
        } else {
          // broker reachable but genuinely empty: honest quiet roster, no fake voices
          paintQuiet();
          lastCount = 0;
          setStatus("the band is quiet right now - no voices on air yet", "quiet");
          setFoot('broker reachable · <span class="ember">no voices on air yet</span> · put your TTS rig on air with <code class="mono">roger share</code>');
        }
      })
      .catch(function () {
        clearTimeout(to);
        // network error / abort: HOLD the last-known roster rather than blanking; fall to the
        // honest "unreachable" state only when there is nothing to hold.
        if (lastCount > 0) {
          setStatus("holding the last roster · re-reading…", "live");
        } else {
          paintQuiet();
          setStatus("couldn't reach the broker just now", "off");
          setFoot('couldn\'t reach <span class="ember">broker.rogerai.fyi</span> · no live roster to show');
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
      setStatus("re-reading the roster…", "live");
      load();
    });
  }

  /* ---------- copy the "how to speak" command -------------------- */
  var cmdBtn = document.getElementById("voiceCmd");
  if (cmdBtn) cmdBtn.addEventListener("click", function () {
    var codeEl = document.getElementById("voiceCmdCode");
    var code = codeEl ? codeEl.textContent : "";
    var done = function () {
      cmdBtn.classList.add("is-copied");
      var t = document.getElementById("toast");
      if (t) { t.textContent = "Copied to clipboard"; t.classList.add("is-shown"); setTimeout(function () { t.classList.remove("is-shown"); }, 1800); }
      setTimeout(function () { cmdBtn.classList.remove("is-copied"); }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(code).then(done, function () {});
    else { try { var ta = document.createElement("textarea"); ta.value = code; document.body.appendChild(ta); ta.select(); document.execCommand("copy"); document.body.removeChild(ta); done(); } catch (e) {} }
  });

  /* ---------- kick off when scrolled into view ------------------ */
  function activate() {
    visible = true;
    if (!loadedOnce) load();
    schedule();
  }

  if ("IntersectionObserver" in window) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        visible = e.isIntersecting;
        if (e.isIntersecting && !loadedOnce) activate();
      });
    }, { threshold: 0.15 });
    if (section) io.observe(section); else activate();
  } else {
    activate();
  }
})();
