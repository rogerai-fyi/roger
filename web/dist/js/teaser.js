/* =====================================================================
   RogerAI - homepage band TEASER (FIG.1). A COMPACT fake-dial tuner: a short
   frequency strip with band notches + labels under a fixed center needle. It
   AUTO-sweeps (no mouse) - drifting the strip and re-locking onto the next
   band on a slow loop, updating the LOCKED/SIGNAL readout + the RATE/SPEED/STN
   chip as it lands. This proves "browse a band -> tune in", then links into
   /models.html where the full INTERACTIVE dial lives (driven by real data).
   This teaser is marketing: representative data, gentle motion.

   The whole figure is an <a> to /models.html. Self-served (CSP script-src
   'self'), no deps. One shared rAF on the --carrier beat (transform + the
   --lock/--sig CSS vars only). Pauses when the tab is hidden. Full
   prefers-reduced-motion fallback (static, pre-locked on the strongest band,
   no sweep/loop). Usable with JS off (the markup is pre-filled + pre-locked).

   Status ownership: this file owns #pingTag (writes "on air") and
   body[data-onair] - the homepage hero / Ping mascot read that single status.
   ===================================================================== */
(function () {
  "use strict";

  var lockedEl = document.getElementById("teaserLocked");
  var sigEl    = document.getElementById("teaserSig");
  var chipEl   = document.getElementById("teaserChip");
  var listEl   = document.getElementById("teaserList");
  var faceEl   = document.querySelector("#teaser .teaser__face");
  var svg      = document.getElementById("teaserSvg");
  var strip    = document.getElementById("teaserStrip");
  if (!lockedEl || !faceEl) return;

  var REDUCED = window.matchMedia &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // Representative band readouts to sweep through (marketing data; the real,
  // live dial is on /models.html). model, stations, signal, rate $/1M, t/s.
  var BANDS = [
    { m: "qwen3-coder-30b", stn: 7, sig: 87, rate: "$0.22", tps: 61, idle: false },
    { m: "mixtral-8x7b",    stn: 6, sig: 82, rate: "$0.18", tps: 88, idle: false },
    { m: "qwen3-72b",       stn: 4, sig: 74, rate: "$0.38", tps: 73, idle: false },
    { m: "gpt-oss-120b",    stn: 3, sig: 66, rate: "$0.55", tps: 66, idle: false },
    { m: "llama3.3-70b",    stn: 5, sig: 61, rate: "$0.31", tps: 44, idle: false },
    { m: "gemma3-27b",      stn: 4, sig: 57, rate: "$0.27", tps: 57, idle: false },
    { m: "mistral-large",   stn: 0, sig: 0,  rate: "-",     tps: 0,  idle: true  }
  ];

  var rows = listEl ? listEl.querySelectorAll(".teaser__band") : [];

  // marketing teaser: the homepage hero / Ping mascot read "on air" against
  // this representative band (the real on-air state lives on /models.html).
  var pingTag = document.getElementById("pingTag");
  document.body.setAttribute("data-onair", "live");
  if (pingTag) pingTag.textContent = "on air";

  /* ---- the SVG frequency strip ---------------------------------- */
  var ns = "http://www.w3.org/2000/svg";
  var VBW = 640, MID = VBW / 2;            // viewBox width; needle is dead center
  var SPACING = 168;                       // px between band notches
  var BASE = 56;                           // notch baseline (px in viewBox)
  var built = false;

  function buildStrip() {
    if (!svg || !strip) return;
    while (strip.firstChild) strip.removeChild(strip.firstChild);
    BANDS.forEach(function (b, i) {
      var x = i * SPACING;
      var t = document.createElementNS(ns, "line");
      t.setAttribute("x1", x); t.setAttribute("x2", x);
      t.setAttribute("y1", 20); t.setAttribute("y2", BASE);
      t.setAttribute("stroke", b.idle ? "var(--ink-300)" : "var(--ink-500)");
      t.setAttribute("stroke-width", "1.5");
      t.setAttribute("class", "teaser__tick"); t.setAttribute("data-i", i);
      strip.appendChild(t);
      var pipH = 6 + Math.round((b.idle ? 0 : b.sig / 100) * 16);
      var pip = document.createElementNS(ns, "rect");
      pip.setAttribute("x", x - 1.5); pip.setAttribute("width", "3");
      pip.setAttribute("y", 20 - pipH); pip.setAttribute("height", pipH);
      pip.setAttribute("rx", "1.5");
      pip.setAttribute("fill", b.idle ? "var(--ink-300)" : "var(--ink-400)");
      pip.setAttribute("class", "teaser__pip"); pip.setAttribute("data-i", i);
      strip.appendChild(pip);
      var lbl = document.createElementNS(ns, "text");
      lbl.setAttribute("x", x); lbl.setAttribute("y", BASE + 16);
      lbl.setAttribute("text-anchor", "middle");
      lbl.setAttribute("fill", b.idle ? "var(--ink-400)" : "var(--ink-900)");
      lbl.setAttribute("font-family", "var(--font-mono)");
      lbl.setAttribute("font-size", "11"); lbl.setAttribute("letter-spacing", "-0.3");
      lbl.setAttribute("class", "teaser__lbl"); lbl.setAttribute("data-i", i);
      lbl.textContent = b.m;
      strip.appendChild(lbl);
    });
    built = true;
  }

  function stripX(p) { return MID - p * SPACING; }

  /* ---- readout / lock state ------------------------------------- */
  var lastIdx = -1;
  function paintReadout(i) {
    var b = BANDS[i];
    if (!b) return;
    lockedEl.innerHTML = (b.idle ? "OFFLINE · " : "LOCKED · ") + "<b>" + b.m + "</b>";
    if (sigEl) sigEl.innerHTML = "SIGNAL <b>" + (b.idle ? "--" : b.sig) + "</b>/100";
    if (chipEl) chipEl.innerHTML =
      '<span class="meter__k">RATE</span><b>' + b.rate + ' /1M</b>' +
      '<span class="meter__k">SPEED</span><b>' + (b.idle ? "-" : b.tps + " t/s") + '</b>' +
      '<span class="meter__k">STN</span><b>' + b.stn + '</b>';
    for (var k = 0; k < rows.length; k++) rows[k].classList.toggle("is-locked", k === i);
    if (built && strip) {
      var prev = strip.querySelector(".teaser__lbl--on");
      if (prev) prev.classList.remove("teaser__lbl--on");
      var on = strip.querySelector('.teaser__lbl[data-i="' + i + '"]');
      if (on) on.classList.add("teaser__lbl--on");
    }
    faceEl.style.setProperty("--sig", (b.idle ? 0 : b.sig / 100).toFixed(3));
  }

  // apply the strip transform + lock-glow for a continuous position `pos`.
  function applyPos(pos) {
    if (built && strip) strip.setAttribute("transform", "translate(" + stripX(pos).toFixed(2) + ",0)");
    var idx = Math.round(pos);
    if (idx < 0) idx = 0; else if (idx > BANDS.length - 1) idx = BANDS.length - 1;
    // --lock: 1 at a detent, dipping toward 0 mid-sweep (brightens the needle)
    var frac = 1 - Math.min(1, Math.abs(pos - idx) * 2);
    faceEl.style.setProperty("--lock", frac.toFixed(3));
    if (idx !== lastIdx) { lastIdx = idx; paintReadout(idx); }
  }

  buildStrip();

  // static, pre-locked on the strongest band for reduced motion / JS-light.
  paintReadout(0);
  applyPos(0);
  if (REDUCED) return;

  /* ---- the auto-sweep loop (one shared rAF) --------------------- */
  // Dwell locked on a band, then ease to the NEXT band; loop forever. After
  // the last band we snap back to the first (no long reverse glide). Timings
  // anchor to the --carrier beat so the teaser breathes with the rest of the
  // page; the drift is eased with a smoothstep so it feels gentle.
  function carrierMs() {
    var v = getComputedStyle(document.documentElement).getPropertyValue("--carrier").trim();
    var ms = parseFloat(v) * (/ms\s*$/.test(v) ? 1 : 1000);
    return ms > 0 ? ms : 2200;
  }
  var CARRIER = carrierMs();
  var DWELL = CARRIER * 1.4;   // pause locked on a band (~3s)
  var SWEEP = CARRIER * 0.7;   // glide to the next band (~1.5s)
  function smooth(t) { return t * t * (3 - 2 * t); }

  var from = 0, to = 0, phaseStart = 0, sweeping = false;
  var raf = null, visible = true;

  function frame(now) {
    if (!phaseStart) phaseStart = now;
    var elapsed = now - phaseStart;
    if (sweeping) {
      var t = Math.min(1, elapsed / SWEEP);
      applyPos(from + (to - from) * smooth(t));
      if (t >= 1) { sweeping = false; phaseStart = now; from = to; }
    } else {
      applyPos(from);
      if (elapsed >= DWELL) {
        if (from >= BANDS.length - 1) {
          // wrap: snap to the first band, then dwell there before sweeping on
          from = to = 0; lastIdx = -1; applyPos(0); phaseStart = now;
        } else {
          to = from + 1; sweeping = true; phaseStart = now;
        }
      }
    }
    raf = requestAnimationFrame(frame);
  }

  function start() { if (raf == null && visible) { phaseStart = 0; raf = requestAnimationFrame(frame); } }
  function stop() { if (raf != null) { cancelAnimationFrame(raf); raf = null; } }

  document.addEventListener("visibilitychange", function () {
    visible = !document.hidden;
    if (document.hidden) stop(); else start();
  });
  start();
})();
