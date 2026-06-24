/* =====================================================================
   RogerAI - homepage band TEASER (FIG.1). A small, glanceable selector that
   reads the band at a glance, then links into /models.html where the full
   INTERACTIVE dial lives (driven by real data). This teaser is marketing: it
   animates gently and uses representative data on purpose. Click anywhere ->
   /models.html (the whole figure is an <a>).

   Self-served (CSP script-src 'self'). No deps. Single setTimeout cadence,
   transform/opacity-free text swaps only. Pauses when tab hidden. Full
   prefers-reduced-motion fallback (static, pre-locked on the strongest band,
   no cycling). Page is fully usable with JS off (the markup is pre-filled).
   ===================================================================== */
(function () {
  "use strict";

  var listEl = document.getElementById("teaserList");
  var lockedEl = document.getElementById("teaserLocked");
  var sigEl = document.getElementById("teaserSig");
  var chipEl = document.getElementById("teaserChip");
  if (!listEl || !lockedEl) return;

  var REDUCED = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // Representative band readouts to glance through (marketing data; the real,
  // live dial is on /models.html). model, stations, signal, rate $/1M, t/s.
  var BANDS = [
    { m: "qwen3-coder-30b", stn: 7, sig: 87, rate: "$0.22", tps: 61, idle: false },
    { m: "mixtral-8x7b",    stn: 6, sig: 82, rate: "$0.18", tps: 88, idle: false },
    { m: "qwen3-72b",       stn: 4, sig: 74, rate: "$0.38", tps: 73, idle: false },
    { m: "gpt-oss-120b",    stn: 3, sig: 66, rate: "$0.55", tps: 66, idle: false },
    { m: "llama3.3-70b",    stn: 5, sig: 61, rate: "$0.31", tps: 44, idle: false },
    { m: "gemma3-27b",      stn: 4, sig: 57, rate: "$0.27", tps: 57, idle: false },
    { m: "mistral-large",   stn: 0, sig: 0,  rate: "-",     tps: 0,  idle: true }
  ];

  var rows = listEl.querySelectorAll(".teaser__band");
  var pingTag = document.getElementById("pingTag");

  // marketing teaser: the homepage hero reads "on air" against this
  // representative band (the real on-air state lives on /models.html).
  document.body.setAttribute("data-onair", "live");
  if (pingTag) pingTag.textContent = "on air";

  function paint(i) {
    var b = BANDS[i];
    if (!b) return;
    lockedEl.innerHTML = (b.idle ? "OFFLINE · " : "LOCKED · ") + "<b>" + b.m + "</b>";
    if (sigEl) sigEl.innerHTML = "SIGNAL <b>" + (b.idle ? "--" : b.sig) + "</b>/100";
    if (chipEl) chipEl.innerHTML =
      '<span class="meter__k">RATE</span><b>' + b.rate + ' /1M</b>' +
      '<span class="meter__k">SPEED</span><b>' + (b.idle ? "-" : b.tps + " t/s") + '</b>' +
      '<span class="meter__k">STN</span><b>' + b.stn + '</b>';
    for (var k = 0; k < rows.length; k++) rows[k].classList.toggle("is-locked", k === i);
  }

  // static, pre-locked on the strongest band for reduced motion / JS-light.
  paint(0);
  if (REDUCED) return;

  var i = 0, timer = null, visible = true;
  function tick() {
    i = (i + 1) % BANDS.length;
    paint(i);
    timer = setTimeout(tick, BANDS[i].idle ? 1400 : 2600);
  }
  function start() { if (timer == null && visible) timer = setTimeout(tick, 2600); }
  function stop() { if (timer != null) { clearTimeout(timer); timer = null; } }

  document.addEventListener("visibilitychange", function () {
    visible = !document.hidden;
    if (document.hidden) stop(); else start();
  });
  start();
})();
