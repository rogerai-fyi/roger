/* =====================================================================
   RogerAI - Playbox: THE WAVE MESH ENGINEERING DECK (M1)

   RogerAI builds LLMs for machines: models that read what devices actually
   emit and assert typed, cited, margin-scored facts. This view lets a visitor
   see that instead of reading about it.

   WHAT IS REAL ON THIS SCREEN, and how it got here:

     DEVICE BROWSER - data/wave-catalog.json, a committed export of
       wavesim.catalog() (17 asset types, 95 channels, 75 root causes, 8
       modalities). Pure data, straight from the simulation suite.

     WIRE BENCH - data/wave-scene-recorded.json, real output from the Python
       renderers for ONE deterministic scene (a centrifugal pump, cavitating,
       with a vibration sensor that sticks at 40% onset, seed 42). Labelled
       RECORDED, exactly like the cassette deck's replayed contracts: real
       output, captured, never presented as live. The spec is printed on the
       page so anyone can re-run it and get the same bytes.

     THE TRANSPORT - the same scene stepped through wavesim.windows(). Each
       step carries the features the fault actually shows up in, so scrubbing
       shows a stuck sensor becoming visible rather than illustrating it.

   WHAT IS NOT HERE YET, and why it is not faked: the rack of Wave unit tiles
   needs real GGUF birth certificates (wave.tier / wave.model_digest /
   wave.tasks / wave.margin_floor) and the alert feed needs recorded assertion
   streams. Both are requested from the models agent. A tile whose faceplate is
   supposed to BE its birth certificate cannot ship with an invented digest, so
   the rack states what it is waiting for instead.

   Honesty rails (UI-HANDOFF-PLAYBOX-WAVE-MESH-2026-08-12.md section 8): live
   RogGentoo data is never labelled with invented faults (truth: null); no
   benchmark number appears without its version tag; no certification claims.

   Dependency-free, defensive, prefers-reduced-motion aware.
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }

  var MESH = { catalog: null, scene: null, dialect: "signal", step: 0, device: null };

  /* =====================================================================
     THE MODE SWITCH - console / mesh
     ===================================================================== */
  function showView(mode) {
    var console_ = $("pgConsoleView"), mesh = $("pgMeshView");
    if (!console_ || !mesh) return;
    var toMesh = mode === "mesh";
    console_.hidden = toMesh;
    mesh.hidden = !toMesh;
    // The hero is the deck's own headline. Leaving "Open the console." above the
    // engineering deck would be the page contradicting itself in its largest type.
    var hc = $("pgHeroConsole"), hm = $("pgHeroMesh"), ht = $("pgHeroTitle");
    if (hc) hc.hidden = toMesh;
    if (hm) hm.hidden = !toMesh;
    if (ht) ht.textContent = toMesh ? "Wire a machine to a model." : "Open the console.";
    var hk = $("pgHeroKicker");
    if (hk) hk.textContent = toMesh ? "the wave mesh" : "open the console";
    [["pgModeConsole", !toMesh], ["pgModeMesh", toMesh]].forEach(function (p) {
      var b = $(p[0]);
      if (!b) return;
      b.setAttribute("aria-selected", p[1] ? "true" : "false");
      b.setAttribute("tabindex", p[1] ? "0" : "-1");
    });
    if (toMesh) loadMesh();
    try { window.localStorage.setItem("pb.mode", mode); } catch (e) { /* private mode */ }
  }

  function wireModeSwitch() {
    var c = $("pgModeConsole"), m = $("pgModeMesh");
    if (!c || !m) return;
    c.addEventListener("click", function () { showView("console"); });
    m.addEventListener("click", function () { showView("mesh"); });
    // Arrow keys move between tabs, per the tablist pattern.
    [c, m].forEach(function (btn) {
      btn.addEventListener("keydown", function (e) {
        if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
        e.preventDefault();
        var next = btn === c ? m : c;
        showView(next === m ? "mesh" : "console");
        next.focus();
      });
    });
    // #mesh deep-links straight to the engineering deck, so a link can point at it - the
    // whole deck is built around shareable, replayable scenarios, and a URL that always
    // landed on the console would be the one unshareable thing on the page. An explicit
    // hash beats the remembered choice; otherwise the last deck you used wins.
    var hash = (window.location.hash || "").replace("#", "").toLowerCase();
    if (hash === "mesh" || hash === "console") {
      showView(hash);
      return;
    }
    var saved = "console";
    try { saved = window.localStorage.getItem("pb.mode") || "console"; } catch (e) { /* ignore */ }
    showView(saved === "mesh" ? "mesh" : "console");
  }

  /* =====================================================================
     DATA - committed snapshots, fetched same-origin
     ===================================================================== */
  var loading = false;
  function loadMesh() {
    if (loading || (MESH.catalog && MESH.scene)) return;
    loading = true;
    Promise.all([
      fetch("data/wave-catalog.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-scene-recorded.json").then(function (r) { return r.ok ? r.json() : null; }),
    ]).then(function (res) {
      MESH.catalog = res[0];
      MESH.scene = res[1];
      renderBrowser();
      renderBench();
    }).catch(function () {
      // Degrade to an honest resting state - never an empty frame that looks like
      // "no devices exist".
      failState();
    });
  }

  function failState() {
    var rail = $("wmDevices");
    if (rail) {
      rail.textContent = "";
      rail.appendChild(el("p", "wm-note", "The device catalogue did not load. Nothing here is live; reload to try again."));
    }
  }

  /* =====================================================================
     LEFT RAIL - the device browser, from catalog()
     ===================================================================== */
  function renderBrowser() {
    var rail = $("wmDevices");
    if (!rail || !MESH.catalog) return;
    var cat = MESH.catalog.catalog || {};
    rail.textContent = "";

    var names = Object.keys(cat).sort(function (a, b) {
      // The live box leads: it is the one device on this page that is real hardware.
      if (a === "roggentoo") return -1;
      if (b === "roggentoo") return 1;
      return a.localeCompare(b);
    });

    names.forEach(function (name) {
      var d = cat[name];
      var live = d.source === "live";
      var card = el("button", "wm-dev" + (live ? " wm-dev--live" : ""));
      card.type = "button";
      card.setAttribute("aria-pressed", "false");
      card.dataset.device = name;

      var head = el("span", "wm-dev__head");
      head.appendChild(el("b", "wm-dev__name", label(name)));
      head.appendChild(el("span", "wm-dev__src", live ? "REAL HARDWARE" : "SIMULATED"));
      card.appendChild(head);

      var chans = Object.keys(d.channels || {});
      var causes = Object.keys(d.root_causes || {});
      card.appendChild(el("span", "wm-dev__meta",
        chans.length + " channel" + (chans.length === 1 ? "" : "s") + " · " +
        causes.length + " root cause" + (causes.length === 1 ? "" : "s")));

      if (live) {
        // The truth-null rail, stated where a visitor meets it rather than in a footnote.
        card.appendChild(el("span", "wm-dev__truth", "unlabelled truth - no invented faults"));
      }
      card.addEventListener("click", function () { selectDevice(name, card); });
      rail.appendChild(card);
    });

    var count = $("wmDeviceCount");
    if (count) count.textContent = names.length + " device types";
  }

  // Device names are acronym-heavy (AGV, CNC, VFD, HPU) and CSS text-transform:capitalize
  // renders those as "Agv" / "Cnc Spindle", which reads as a typo on a page whose whole
  // claim is precision about machines. Casing is decided here, in data, not in the
  // stylesheet - and the live box keeps the spelling the handoff gives it.
  var ACRONYMS = { agv: "AGV", cnc: "CNC", vfd: "VFD", hpu: "HPU", gpu: "GPU", cpu: "CPU" };
  var EXACT = { roggentoo: "RogGentoo" };

  function label(name) {
    if (EXACT[name]) return EXACT[name];
    return name.split("_").map(function (w) {
      if (ACRONYMS[w]) return ACRONYMS[w];
      return w.charAt(0).toUpperCase() + w.slice(1);
    }).join(" ");
  }

  function selectDevice(name, card) {
    MESH.device = name;
    var rail = $("wmDevices");
    if (rail) {
      Array.prototype.forEach.call(rail.querySelectorAll(".wm-dev"), function (n) {
        n.setAttribute("aria-pressed", n === card ? "true" : "false");
        n.classList.toggle("is-on", n === card);
      });
    }
    renderDeviceDetail(name);
  }

  function renderDeviceDetail(name) {
    var box = $("wmDetail");
    if (!box || !MESH.catalog) return;
    var d = (MESH.catalog.catalog || {})[name];
    if (!d) return;
    box.textContent = "";
    box.appendChild(el("h3", "wm-detail__name", label(name)));

    var live = d.source === "live";
    box.appendChild(el("p", "wm-detail__src",
      live ? "The live box, reporting itself as a device. Its truth is null: the deck shows model assertions as assertions, never as ground truth."
           : "Simulated. Deterministic for a given spec and seed."));

    box.appendChild(el("h4", "wm-detail__sub", "Channels"));
    var chans = el("ul", "wm-chans");
    Object.keys(d.channels || {}).forEach(function (c) {
      var meta = d.channels[c] || {};
      var li = el("li", "wm-chan");
      li.appendChild(el("span", "wm-chan__jack", "◦"));
      li.appendChild(el("b", null, c.replace(/_/g, " ")));
      if (meta.unit) li.appendChild(el("span", "wm-chan__unit", meta.unit));
      chans.appendChild(li);
    });
    box.appendChild(chans);

    var causes = Object.keys(d.root_causes || {}).filter(function (c) { return c !== "healthy"; });
    if (causes.length) {
      box.appendChild(el("h4", "wm-detail__sub", "Root causes"));
      var ul = el("ul", "wm-causes");
      causes.forEach(function (c) {
        var effects = d.root_causes[c] || {};
        var li = el("li", "wm-cause");
        li.appendChild(el("b", null, c.replace(/_/g, " ")));
        var keys = Object.keys(effects);
        if (keys.length) {
          li.appendChild(el("span", "wm-cause__eff", keys.map(function (k) {
            return k.replace(/_/g, " ") + " " + effects[k];
          }).join(", ")));
        }
        ul.appendChild(li);
      });
      box.appendChild(ul);
    }

    var faults = d.sensor_faults || [];
    box.appendChild(el("h4", "wm-detail__sub", "Sensor faults"));
    box.appendChild(el("p", "wm-detail__faults", faults.length
      ? faults.join(" · ")
      : "none - this is real hardware, and the deck never invents a fault on live data."));
  }

  /* =====================================================================
     THE WIRE BENCH - one truth, eight dialects (RECORDED)
     ===================================================================== */
  function renderBench() {
    var sc = MESH.scene;
    if (!sc) return;

    var spec = $("wmSpec");
    if (spec && sc.spec) {
      spec.textContent = JSON.stringify(sc.spec);
    }

    var tabs = $("wmDialects");
    if (tabs) {
      tabs.textContent = "";
      Object.keys(sc.renders || {}).forEach(function (m) {
        var b = el("button", "wm-dialect", m);
        b.type = "button";
        b.dataset.dialect = m;
        b.setAttribute("aria-pressed", m === MESH.dialect ? "true" : "false");
        b.addEventListener("click", function () {
          MESH.dialect = m;
          Array.prototype.forEach.call(tabs.querySelectorAll(".wm-dialect"), function (n) {
            n.setAttribute("aria-pressed", n.dataset.dialect === m ? "true" : "false");
          });
          paintDialect();
        });
        tabs.appendChild(b);
      });
    }
    paintDialect();
    wireTransport();
    paintStep();
  }

  function paintDialect() {
    var out = $("wmWire");
    if (!out || !MESH.scene) return;
    out.textContent = (MESH.scene.renders || {})[MESH.dialect] || "";
  }

  /* =====================================================================
     THE TRANSPORT - scrubbing wavesim.windows()
     ===================================================================== */
  function wireTransport() {
    var slider = $("wmScrub");
    if (!slider || !MESH.scene || slider.dataset.wired) return;
    var steps = MESH.scene.steps || [];
    slider.max = String(Math.max(0, steps.length - 1));
    slider.value = "0";
    slider.dataset.wired = "1";
    slider.addEventListener("input", function () {
      MESH.step = parseInt(slider.value, 10) || 0;
      paintStep();
    });
  }

  function paintStep() {
    var sc = MESH.scene;
    if (!sc) return;
    var steps = sc.steps || [];
    var s = steps[MESH.step];
    if (!s) return;

    var t = $("wmStepT");
    if (t) t.textContent = "t=" + s.t;

    var run = $("wmRun");
    if (run) run.textContent = String(s.longest_run);
    var sd = $("wmSd");
    if (sd) sd.textContent = s.sd_tail.toFixed(4);

    // The bar is the story: a stuck sensor's longest identical run grows until it fills the
    // window. Width is a plain style write so it degrades to a static bar with no motion.
    var bar = $("wmRunBar");
    if (bar) {
      var pct = Math.max(0, Math.min(100, (s.longest_run / 32) * 100));
      bar.style.width = pct + "%";
      bar.setAttribute("aria-valuenow", String(s.longest_run));
      if (REDUCED) bar.style.transition = "none";
    }

    var verdict = $("wmVerdict");
    if (verdict) {
      // Describing the FEATURE, not asserting a model's conclusion. No model has run here.
      verdict.textContent = s.sd_tail === 0 && s.longest_run >= 32
        ? "the channel has stopped moving: every sample in the window is identical"
        : s.longest_run > 1
          ? "repeating samples are accumulating in the window"
          : "the channel is still varying normally";
    }
  }

  /* =====================================================================
     BOOT
     ===================================================================== */
  function init() {
    if (!$("pgMeshView")) return;
    wireModeSwitch();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
