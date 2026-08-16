/* =====================================================================
   THE COOKIE LINE - the Playbox factory game (deck 3)

   MIXER -> OVEN -> PACKER, running live. Every machine has one sensor and
   one control. The player's job is to keep each machine inside its band.

   THE POINT OF THE GAME IS THE PRODUCT ARGUMENT:
     Phase 0 - no models. You read raw numbers and guess. A sensor that
       goes STUCK keeps showing a healthy number while the process walks
       out of band, and your line dies while the display looks fine.
     Buy PICO  - a light and one word on that machine.
     Buy NANO  - why it is wrong, and what to do about it.
     Buy MICRO - the site view across all three machines.
     Buy GIGA  - the plant view: bottleneck and forecast.

   WHERE THE HONESTY LINE SITS:
     THE PLANT (cookies, coins, wear, bands, throughput, upgrades) is GAME
     SIMULATION and says so on the surface.
     MODEL BEHAVIOUR is REPLAY. When the game gives a sensor the condition
     X, it draws a real record with truth X out of the committed bench
     export and uses THAT record's child/parent predictions and margins to
     decide what Pico and Nano say - including getting it wrong. A recorded
     miss stays a miss here, because that is the product truth and it is
     what makes the next tier up worth buying.
     MICRO and GIGA compute over GAME STATE (arithmetic, labelled), never a
     fabricated inference: those tiers have no recorded run on this bench.
   ===================================================================== */
(function () {
  "use strict";

  var FLOOR = 1.5;                 // the margin floor Pico asserts above
  var TICK = 1 / 12;               // seconds of sim per step
  var REDUCED = typeof window !== "undefined" && window.matchMedia
    ? window.matchMedia("(prefers-reduced-motion: reduce)").matches : false;

  /* ---- the line -------------------------------------------------------- */
  var MACHINES = [
    { id: "mixer", name: "MIXER", makes: "dough",
      art: "▤", sensor: { kind: "vib", label: "VIBRATION", unit: "mm/s", dp: 2 },
      control: { label: "SPEED", min: 1, max: 10, step: 1, unit: "" } },
    { id: "oven", name: "OVEN", makes: "baked",
      art: "▥", sensor: { kind: "temp", label: "TEMPERATURE", unit: "°C", dp: 1 },
      control: { label: "HEAT", min: 150, max: 240, step: 5, unit: "°C" } },
    { id: "packer", name: "PACKER", makes: "boxed",
      art: "▦", sensor: { kind: "amp", label: "MOTOR CURRENT", unit: "A", dp: 2 },
      control: { label: "SPEED", min: 1, max: 10, step: 1, unit: "" } },
  ];

  // Upgrades: faster, and a wider band so a small lie is less fatal.
  var TIERS = {
    mixer: [
      { name: "Mk I", rate: 1.0, lo: 0, hi: 5.0, price: 0 },
      { name: "Mk II", rate: 1.7, lo: 0, hi: 8.0, price: 120 },
      { name: "Mk III", rate: 2.6, lo: 0, hi: 12.0, price: 300 },
    ],
    oven: [
      { name: "Mk I", rate: 1.0, lo: 160, hi: 190, price: 0 },
      { name: "Mk II", rate: 1.7, lo: 165, hi: 210, price: 140 },
      { name: "Mk III", rate: 2.6, lo: 170, hi: 232, price: 340 },
    ],
    packer: [
      { name: "Mk I", rate: 1.0, lo: 0, hi: 6.0, price: 0 },
      { name: "Mk II", rate: 1.7, lo: 0, hi: 9.5, price: 130 },
      { name: "Mk III", rate: 2.6, lo: 0, hi: 14.0, price: 320 },
    ],
  };

  var MODEL_PRICE = { pico: 50, nano: 120, micro: 260, giga: 500 };
  /* SERVICING COSTS TIME, NOT MONEY. It used to cost coins, and a player who
     could not tell which machine was lying would blind-service all three,
     go broke, and then be unable to service anything at all - a dead line
     with no way back. Downtime is the honest currency anyway: the models are
     worth buying because they stop you spending minutes on healthy machines. */
  var SERVICE_SECS = 4;

  // The recorded fault taxonomy. `tell` is how the sensor LIES; the process
  // itself keeps drifting underneath regardless.
  var CONDITIONS = ["stuck", "drifting", "dropout", "noisy", "railed"];
  var CONDITION_WORD = {
    none: "steady", stuck: "stuck", drifting: "drifting",
    dropout: "dropping out", noisy: "noisy", railed: "railed",
  };
  // What a Nano tells you to DO about it. Game advice about a game plant.
  var CONDITION_FIX = {
    stuck: "the reading is frozen - the real value has moved on. Service this sensor.",
    drifting: "the reading is sliding away from the truth. Service this sensor.",
    dropout: "the reading keeps vanishing. Service this sensor.",
    noisy: "the reading is jittering too hard to trust. Service this sensor.",
    railed: "the reading is pinned at its limit. Service this sensor.",
  };

  var TIER_COLOUR = { pico: "pico", nano: "nano", micro: "micro", giga: "giga" };

  /* ---- state ----------------------------------------------------------- */
  function freshMachine(spec) {
    var t = TIERS[spec.id][0];
    return {
      id: spec.id, tier: 0,
      set: spec.id === "oven" ? 175 : 5,   // the player's control
      real: spec.id === "oven" ? 175 : 2.4, // the physical truth
      drift: 0,                             // process walking out of spec
      cond: "none", condAge: 0, servicing: 0,
      // distinct seeds, or all three machines draw the same sequence
      seed: spec.id === "mixer" ? 9176 : spec.id === "oven" ? 41213 : 77431,
      nextFault: 0,
      sample: null,                         // the drawn record, while faulted
      pico: false,
      auto: false,                          // the models turn this knob
      autoNote: "", hadStop: false,
      buffer: 0,
      stopped: false, stoppedFor: 0,
      spec: spec,
    };
  }

  function freshState() {
    return {
      coins: 120, cookies: 0, spoiled: 0, elapsed: 0,
      machines: MACHINES.map(freshMachine),
      nano: false, micro: false, giga: false,
      running: true, ready: false, error: "",
      records: [], seed: 7,
      log: ["The line is cold. Press START and watch the numbers."],
      serviced: 0, saves: { pico: 0, nano: 0 }, missed: 0,
      peakRate: 0,
      // THE HANDOVER: incidents and a rolling metrics series, so an
      // automated plant has results to show for itself. Game arithmetic.
      incidents: { caught: 0, missed: 0, open: 0 },
      history: [], sampleAt: 0, upTime: 0, runTime: 0,
      deskTab: "site",
    };
  }

  var G = freshState();
  var DOM = {};
  var raf = 0, lastT = 0, acc = 0;

  function machine(id) {
    for (var i = 0; i < G.machines.length; i++) if (G.machines[i].id === id) return G.machines[i];
    return null;
  }
  function tierOf(m) { return TIERS[m.id][m.tier]; }

  /* =====================================================================
     THE HONESTY CORE - model behaviour comes out of the committed replay
     ===================================================================== */
  function kindOfTag(tag) {
    if (!tag) return "unnamed";
    if (/VIBRATION/.test(tag)) return "vib";
    if (/TEMP/.test(tag)) return "temp";
    if (/CURRENT/.test(tag)) return "amp";
    if (/PRESS/.test(tag)) return "press";
    return "unnamed";
  }

  /* Draw a REAL record whose recorded truth is the condition the game just
     handed this sensor. Prefer one recorded on the same kind of instrument;
     fall back to any record with that truth when the bench never recorded
     that pairing (it has no NOISY vibration channel, for instance). The
     fallback is reported so the surface can say which it used - a record
     from another instrument is still a real record, but it is not the same
     claim, and the deck does not get to blur that. */
  function sampleFor(kind, truth, seed) {
    var recs = G.records;
    if (!recs.length || truth === "none") return null;
    var exact = [], any = [];
    for (var i = 0; i < recs.length; i++) {
      var r = recs[i];
      if (r.truth !== truth || !r.child || !r.parent) continue;
      any.push(r);
      if (kindOfTag(r.window && r.window.tag) === kind) exact.push(r);
    }
    var pool = exact.length ? exact : any;
    if (!pool.length) return null;
    return { record: pool[seed % pool.length], sameKind: exact.length > 0 };
  }

  /* What Pico says, straight off the drawn record. Three outcomes, all real:
     it is sure and right, it is sure and WRONG, or it is below its floor and
     says so. Nothing here is decided by the game. */
  function picoRead(sample) {
    if (!sample) return null;
    var r = sample.record;
    if (r.child.margin < FLOOR) {
      return { kind: "unsure", said: r.child.prediction, margin: r.child.margin, truth: r.truth };
    }
    if (r.child.prediction === r.truth) {
      return { kind: "caught", said: r.child.prediction, margin: r.child.margin, truth: r.truth };
    }
    return { kind: "wrong", said: r.child.prediction, margin: r.child.margin, truth: r.truth };
  }

  /* Nano only resolves what the recorded parent actually got right. */
  function nanoRead(sample) {
    if (!sample) return null;
    var r = sample.record;
    return {
      kind: r.parent.prediction === r.truth ? "resolved" : "missed",
      said: r.parent.prediction, margin: r.parent.margin, truth: r.truth,
    };
  }

  /* =====================================================================
     THE PLANT - game simulation, and labelled as such on the surface
     ===================================================================== */
  /* Math.imul, not `*`: a 32-bit LCG multiplied with `*` runs past 2^53 and
     loses its low bits, which skews the distribution so far that small draws
     effectively stop happening - and a game whose faults never fire has no
     loop at all. imul does the multiply exactly. */
  function rnd(m) { m.seed = (Math.imul(m.seed, 1103515245) + 12345) & 0x7fffffff; return m.seed / 0x7fffffff; }

  function targetFor(m) {
    if (m.id === "oven") return m.set;
    return m.id === "mixer" ? 0.42 * m.set : 0.52 * m.set;
  }

  // What the sensor DISPLAYS, given what is really happening.
  function shownValue(m) {
    var real = m.real;
    switch (m.cond) {
      case "stuck": return m.stuckAt;
      case "drifting": return real - m.driftLie;
      case "dropout": return (Math.floor(G.elapsed * 3) % 4 === 0) ? null : real;
      case "noisy": return real + (rnd(m) - 0.5) * (m.id === "oven" ? 24 : 3.4);
      case "railed": return tierOf(m).hi * (m.id === "oven" ? 1.0 : 1.02);
      default: return real;
    }
  }

  function inBand(m, v) { var t = tierOf(m); return v >= t.lo && v <= t.hi; }

  function startCondition(m) {
    var pick = CONDITIONS[Math.floor(rnd(m) * CONDITIONS.length)];
    m.cond = pick;
    m.condAge = 0;
    m.stuckAt = m.real;
    m.driftLie = 0;
    m.sample = sampleFor(m.spec.sensor.kind, pick, Math.floor(rnd(m) * 997));
    m.picoRead = m.pico ? picoRead(m.sample) : null;
    m.nanoRead = G.nano ? nanoRead(m.sample) : null;
    m.hadStop = false;
    G.incidents.open += 1;
    addLog(m.spec.name + " sensor went " + CONDITION_WORD[pick] + ".");
  }

  /* An incident that never stopped the line was CAUGHT in time; one that
     stopped it first was missed, whoever or whatever was watching. Under
     automation that distinction is the whole scoreboard: a replayed model
     miss still lets the line die, and the results panel has to show it. */
  function clearCondition(m) {
    m.nextFault = 14 + rnd(m) * 30;
    if (m.cond !== "none") {
      G.incidents.open = Math.max(0, G.incidents.open - 1);
      if (m.hadStop) G.incidents.missed += 1; else G.incidents.caught += 1;
    }
    m.cond = "none"; m.condAge = 0; m.sample = null;
    m.picoRead = null; m.nanoRead = null; m.driftLie = 0; m.hadStop = false;
  }

  /* ---- the handover: models turning the knobs -------------------------- */
  function autonomyReach() {
    // Micro can hold one machine's knob; Giga can hold the whole plant.
    if (G.giga) return 3;
    if (G.micro) return 1;
    return 0;
  }
  function autoCount() {
    return G.machines.filter(function (m) { return m.auto; }).length;
  }
  function toggleAuto(id) {
    var m = machine(id);
    if (!m.auto && autoCount() >= autonomyReach()) return false;
    m.auto = !m.auto;
    m.autoNote = "";
    addLog(m.spec.name + (m.auto
      ? " knob handed to the models. You can take it back any time."
      : " knob is yours again."));
    paint();
    return true;
  }

  /* The automation acts on WHAT IT BELIEVES, and belief comes from the same
     sensor and the same replayed model reads the player sees. A lying sensor
     that no model caught fools the automation exactly as it fools a person -
     which is why an automated plant still logs missed incidents. */
  function autoAdjust(m, dt) {
    if (!m.auto) return;
    var believed = shownValue(m);
    if (believed == null) return;                 // a dropout says nothing
    var t = tierOf(m), c = m.spec.control;
    var aim = t.lo + 0.62 * (t.hi - t.lo);
    var err = believed - aim;
    var tol = (t.hi - t.lo) * 0.08;
    if (Math.abs(err) > tol) {
      var stepBy = c.step * (err > 0 ? -1 : 1) * (m.id === "oven" ? 1 : 1);
      var next = Math.max(c.min, Math.min(c.max, m.set + stepBy));
      if (next !== m.set) {
        m.set = next;
        m.autoNote = (G.giga ? "Giga" : "Micro") + " moved " + c.label + " to " + next + c.unit;
      }
    }
    // With Giga the plant may also clear a fault it was actually told about.
    if (G.giga && m.cond !== "none" && !m.servicing) {
      var told = (m.picoRead && m.picoRead.kind === "caught") ||
                 (m.nanoRead && m.nanoRead.kind === "resolved");
      if (told && m.condAge > 2.5) {
        m.autoNote = "Giga serviced this sensor on the models' word";
        service(m.id);
      }
    }
  }

  function stepMachine(m, dt) {
    var t = tierOf(m);
    if (m.servicing > 0) {
      m.servicing -= dt;
      m.stopped = true;
      if (m.servicing <= 0) {
        m.servicing = 0;
        clearCondition(m);
        m.drift = 0;
        m.stoppedFor = 0;
      }
      return false;
    }
    // the control pulls the real value toward its target
    var target = targetFor(m);
    m.real += (target + m.drift - m.real) * Math.min(1, dt * 1.6);

    // a faulted sensor means nobody is truly watching, so the process walks
    if (m.cond === "none") {
      m.drift *= 0.985;
      /* A COUNTDOWN, not a per-tick coin flip. A rare random draw made the
         first fault land anywhere between ten seconds and never, depending on
         the seed - and a teaching loop that sometimes never starts is not a
         teaching loop. This schedules the next fault into a known window. */
      if (!m.nextFault) m.nextFault = 10 + rnd(m) * 26;
      m.nextFault -= dt;
      if (m.nextFault <= 0) { startCondition(m); m.nextFault = 0; }
    } else {
      m.condAge += dt;
      m.drift += dt * (m.id === "oven" ? 1.6 : 0.30);
      if (m.cond === "drifting") m.driftLie += dt * (m.id === "oven" ? 1.5 : 0.26);
    }

    var ok = inBand(m, m.real);
    if (!ok) { m.stoppedFor += dt; } else { m.stoppedFor = Math.max(0, m.stoppedFor - dt * 2); }
    m.stopped = m.stoppedFor > 1.2;
    if (m.stopped && m.cond !== "none") m.hadStop = true;
    autoAdjust(m, dt);
    return ok;
  }

  function rateOf(m) {
    if (m.stopped) return 0;
    var t = tierOf(m);
    if (m.id === "oven") {
      var span = Math.max(1, t.hi - t.lo);
      return t.rate * (0.55 + 0.75 * Math.min(1, Math.max(0, (m.real - t.lo) / span)));
    }
    return t.rate * (0.35 + 0.85 * (m.set / m.spec.control.max));
  }

  function step(dt) {
    G.elapsed += dt;
    var mx = machine("mixer"), ov = machine("oven"), pk = machine("packer");
    stepMachine(mx, dt); stepMachine(ov, dt); stepMachine(pk, dt);

    var CAP = 14;
    // mixer -> dough buffer
    var made = rateOf(mx) * dt * 1.5;
    mx.buffer = Math.min(CAP, mx.buffer + made);
    // oven consumes dough, makes baked
    var bake = Math.min(mx.buffer, rateOf(ov) * dt * 1.5);
    mx.buffer -= bake;
    // an oven out of band burns what it bakes
    if (ov.stopped || ov.real > tierOf(ov).hi) { G.spoiled += bake; }
    else { ov.buffer = Math.min(CAP, ov.buffer + bake); }
    // packer consumes baked, ships cookies
    var pack = Math.min(ov.buffer, rateOf(pk) * dt * 1.5);
    ov.buffer -= pack;
    G.cookies += pack;
    G.coins += pack * 4;   // a cookie sells for four
    var rate = pack / Math.max(dt, 1e-6);
    G.peakRate = Math.max(G.peakRate, rate);

    /* THE RESULTS SERIES. An automated plant has to have something to show
       for itself, so the game keeps its own rolling record: cookies per
       second, uptime, and the incident tally. Game arithmetic over game
       state - it is labelled that way wherever it is drawn. */
    G.runTime += dt;
    var allUp = G.machines.every(function (x) { return !x.stopped; });
    if (allUp) G.upTime += dt;
    G.sampleAt += dt;
    if (G.sampleAt >= 1) {
      G.sampleAt = 0;
      G.history.push({
        t: Math.round(G.elapsed),
        rate: rate,
        up: G.runTime ? G.upTime / G.runTime : 1,
        caught: G.incidents.caught,
        missed: G.incidents.missed,
      });
      if (G.history.length > 120) G.history.shift();
    }
  }

  /* =====================================================================
     WHAT EACH TIER SHOWS - the ladder, made playable
     ===================================================================== */
  // MICRO: the site view. Arithmetic over game state, never a claimed read.
  function siteView() {
    var rows = G.machines.map(function (m) {
      var t = tierOf(m);
      var head = m.id === "oven" ? (t.hi - m.real) : (t.hi - m.real);
      return {
        id: m.id, name: m.spec.name, tier: t.name,
        real: m.real, band: t.lo + "-" + t.hi,
        head: head, stopped: m.stopped, cond: m.cond,
        rate: rateOf(m),
      };
    });
    var worst = rows.slice().sort(function (a, b) { return a.head - b.head; })[0];
    return { rows: rows, worst: worst };
  }

  // GIGA: the plant view. Which station caps the line, and what it costs.
  function plantView() {
    var rates = G.machines.map(function (m) { return { id: m.id, name: m.spec.name, rate: rateOf(m) }; });
    var slow = rates.slice().sort(function (a, b) { return a.rate - b.rate; })[0];
    var best = rates.slice().sort(function (a, b) { return b.rate - a.rate; })[0];
    var loss = Math.max(0, best.rate - slow.rate);
    return { rates: rates, bottleneck: slow, loss: loss,
             faults: G.machines.filter(function (m) { return m.cond !== "none"; }).length };
  }

  /* ---- economy --------------------------------------------------------- */
  function buyTier(id) {
    var m = machine(id);
    var next = TIERS[id][m.tier + 1];
    if (!next || G.coins < next.price) return false;
    G.coins -= next.price;
    m.tier += 1;
    addLog(m.spec.name + " upgraded to " + next.name + " - band now " + next.lo + "-" + next.hi + " " + m.spec.sensor.unit + ".");
    paint();
    return true;
  }

  function buyPico(id) {
    var m = machine(id);
    if (m.pico || G.coins < MODEL_PRICE.pico) return false;
    G.coins -= MODEL_PRICE.pico;
    m.pico = true;
    if (m.cond !== "none") m.picoRead = picoRead(m.sample);
    addLog("Wave Pico installed on the " + m.spec.name.toLowerCase() + ".");
    paint();
    return true;
  }

  function buyDesk(which) {
    if (G[which] || G.coins < MODEL_PRICE[which]) return false;
    G.coins -= MODEL_PRICE[which];
    G[which] = true;
    if (which === "nano") {
      G.machines.forEach(function (m) { if (m.cond !== "none") m.nanoRead = nanoRead(m.sample); });
    }
    addLog("Wave " + which.charAt(0).toUpperCase() + which.slice(1) + " online at the desk.");
    paint();
    return true;
  }

  function service(id) {
    var m = machine(id);
    if (m.servicing > 0) return false;
    m.servicing = SERVICE_SECS;          // the machine is down while it happens
    if (m.cond === "none") {
      G.wasted = (G.wasted || 0) + 1;
      addLog(m.spec.name + " sensor checked out fine - " + SERVICE_SECS +
        "s of production spent finding that out.");
      paint();
      return true;
    }
    G.serviced += 1;
    // crediting the save to whoever actually told you, per the replay
    if (m.picoRead && m.picoRead.kind === "caught") G.saves.pico += 1;
    else if (m.nanoRead && m.nanoRead.kind === "resolved") G.saves.nano += 1;
    addLog(m.spec.name + " sensor being serviced - back in " + SERVICE_SECS + "s.");
    paint();
    return true;
  }

  function addLog(copy) { G.log.unshift(copy); G.log = G.log.slice(0, 6); }

  /* =====================================================================
     RENDER - built once, painted per frame
     ===================================================================== */
  function el(tag, cls, copy) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (copy != null) n.textContent = copy;
    return n;
  }
  function btn(copy, cls, fn) {
    var b = el("button", cls, copy); b.type = "button";
    if (fn) b.addEventListener("click", fn);
    return b;
  }

  function buildShell(host) {
    host.textContent = "";
    DOM = { stations: {} };
    var root = el("div", "cl");

    /* header */
    var head = el("header", "cl-head");
    var brand = el("div", "cl-brand");
    brand.appendChild(el("b", null, "THE COOKIE LINE"));
    brand.appendChild(el("span", null, "mix · bake · pack · ship"));
    head.appendChild(brand);
    var hud = el("div", "cl-hud");
    DOM.coins = el("b", null, "0"); DOM.cookies = el("b", null, "0"); DOM.rate = el("b", null, "0.0");
    [["COINS", DOM.coins], ["COOKIES", DOM.cookies], ["PER SEC", DOM.rate]].forEach(function (p) {
      var s = el("span", "cl-stat"); s.appendChild(p[1]); s.appendChild(el("i", null, p[0])); hud.appendChild(s);
    });
    DOM.runBtn = btn("PAUSE", "cl-run", toggleRun);
    hud.appendChild(DOM.runBtn);
    hud.appendChild(btn("↻", "cl-reset", resetGame));
    head.appendChild(hud);
    root.appendChild(head);

    /* the goal chip: two or three lines, always visible, never a quest log */
    DOM.goals = el("ul", "cl-goals");
    root.appendChild(DOM.goals);

    /* the line */
    var line = el("div", "cl-line");
    G.machines.forEach(function (m, i) {
      line.appendChild(station(m));
      if (i < G.machines.length - 1) line.appendChild(conveyor(m));
    });
    line.appendChild(shipper());
    root.appendChild(line);

    /* the desk */
    root.appendChild(desk());

    /* honesty footer */
    var note = el("p", "cl-note");
    note.appendChild(el("b", null, "MODEL BEHAVIOUR: RECORDED REPLAY"));
    note.appendChild(document.createTextNode(
      " · what Pico and Nano say is drawn from real records in the committed bench export, " +
      "misses included. THE PLANT ITSELF IS GAME SIMULATION: the cookies, coins, bands, wear and " +
      "throughput are invented for play. Micro and Giga read the game's own numbers - " +
      "no recorded run exists for those tiers."));
    root.appendChild(note);

    /* log */
    var log = el("details", "cl-log");
    log.appendChild(el("summary", null, "LINE RADIO"));
    DOM.log = el("ol");
    log.appendChild(DOM.log);
    root.appendChild(log);

    host.appendChild(root);
  }

  function station(m) {
    var s = DOM.stations[m.id] = {};
    var card = el("section", "cl-station cl-station--" + m.id);
    card.setAttribute("aria-label", m.spec.name);

    var top = el("div", "cl-station__top");
    top.appendChild(el("span", "cl-station__art", m.spec.art));
    var names = el("div", "cl-station__names");
    names.appendChild(el("b", null, m.spec.name));
    s.tier = el("span", "cl-station__tier", "Mk I");
    names.appendChild(s.tier);
    top.appendChild(names);
    s.lamp = el("span", "cl-lamp");
    top.appendChild(s.lamp);
    card.appendChild(top);

    /* the sensor readout - the only thing you get in phase 0 */
    var read = el("div", "cl-read");
    read.appendChild(el("i", null, m.spec.sensor.label));
    s.value = el("b", "cl-read__v", "--");
    read.appendChild(s.value);
    read.appendChild(el("i", "cl-read__u", m.spec.sensor.unit));
    card.appendChild(read);
    s.band = el("div", "cl-band");
    s.bandFill = el("i", "cl-band__fill");
    s.bandMark = el("i", "cl-band__mark");
    s.band.appendChild(s.bandFill); s.band.appendChild(s.bandMark);
    card.appendChild(s.band);
    s.bandTxt = el("span", "cl-band__txt", "");
    card.appendChild(s.bandTxt);

    /* the control */
    var ctl = el("label", "cl-ctl");
    ctl.appendChild(el("i", null, m.spec.control.label));
    var input = document.createElement("input");
    input.type = "range";
    input.min = m.spec.control.min; input.max = m.spec.control.max; input.step = m.spec.control.step;
    input.value = m.set;
    input.className = "cl-ctl__range";
    input.setAttribute("aria-label", m.spec.name + " " + m.spec.control.label.toLowerCase());
    input.addEventListener("input", function () { m.set = Number(input.value); paint(); });
    s.input = input;
    ctl.appendChild(input);
    s.setTxt = el("b", "cl-ctl__v", "");
    ctl.appendChild(s.setTxt);
    card.appendChild(ctl);

    /* the model slot */
    s.slot = el("div", "cl-slot");
    card.appendChild(s.slot);

    /* actions */
    var acts = el("div", "cl-acts");
    s.service = btn("SERVICE", "cl-act cl-act--service", function () { service(m.id); });
    s.upgrade = btn("UPGRADE", "cl-act", function () { buyTier(m.id); });
    acts.appendChild(s.service); acts.appendChild(s.upgrade);
    card.appendChild(acts);

    return card;
  }

  function conveyor(m) {
    var c = el("div", "cl-conv");
    var belt = el("div", "cl-conv__belt");
    var s = DOM.stations[m.id];
    s.dots = [];
    for (var i = 0; i < 5; i++) { var d = el("i", "cl-conv__dot"); s.dots.push(d); belt.appendChild(d); }
    c.appendChild(belt);
    s.buf = el("span", "cl-conv__buf", "0");
    c.appendChild(s.buf);
    return c;
  }

  function shipper() {
    var sh = el("section", "cl-ship");
    sh.appendChild(el("span", "cl-station__art", "▣"));
    sh.appendChild(el("b", null, "SHIPPING"));
    DOM.shipTxt = el("span", null, "0 cookies");
    sh.appendChild(DOM.shipTxt);
    DOM.spoilTxt = el("i", "cl-ship__spoil", "");
    sh.appendChild(DOM.spoilTxt);
    return sh;
  }

  function desk() {
    var d = el("section", "cl-desk");
    var head = el("div", "cl-desk__head");
    head.appendChild(el("b", null, "OPERATOR DESK"));
    head.appendChild(el("span", null, "buy the ladder · each tier tells you more"));
    d.appendChild(head);

    DOM.shop = el("div", "cl-shop");
    d.appendChild(DOM.shop);

    DOM.deskView = el("div", "cl-deskview");
    d.appendChild(DOM.deskView);
    return d;
  }

  /* ---- paint ----------------------------------------------------------- */
  function fmt(v, dp) { return v == null ? "--" : v.toFixed(dp); }

  function paintStation(m) {
    var s = DOM.stations[m.id], t = tierOf(m);
    var shown = shownValue(m);
    s.tier.textContent = t.name;
    s.value.textContent = fmt(shown, m.spec.sensor.dp);
    s.value.classList.toggle("is-gone", shown == null);

    // the band meter always draws the REAL band; the needle draws what the
    // sensor claims, so a lying sensor visibly sits in a comfortable place
    var lo = t.lo, hi = t.hi, span = hi - lo || 1;
    var pos = shown == null ? 0 : Math.max(0, Math.min(1, (shown - lo) / span));
    s.bandMark.style.left = (pos * 100).toFixed(1) + "%";
    s.bandFill.style.width = "100%";
    s.bandTxt.textContent = "band " + lo + "-" + hi + " " + m.spec.sensor.unit;
    s.setTxt.textContent = m.set + (m.spec.control.unit || "");
    if (s.input.value !== String(m.set)) s.input.value = m.set;

    /* THE LAMP READS THE SENSOR, NOT THE TRUTH. This is the point of phase 0:
       a machine judges itself by what its instrument claims, so a stuck sensor
       leaves the lamp reading RUNNING while the line quietly dies. The one
       thing the game will not hide is a belt that has physically stopped -
       you could see that from across the floor - so STOPPED still shows. */
    var claimsOk = shown == null ? true : inBand(m, shown);
    var state = m.stopped ? "stopped" : (!claimsOk ? "warn" : "ok");
    s.lamp.dataset.state = state;
    s.lamp.textContent = state === "stopped" ? "STOPPED" : state === "warn" ? "OUT OF BAND" : "RUNNING";

    /* The model slot rebuilds only when its CONTENT changes. Rebuilding it
       every frame re-creates the very buy button under the player's cursor,
       which eats the click - and leaks a new entry into the price registry
       twelve times a second. */
    var slotKey = [m.pico, m.auto, m.cond, G.nano, autonomyReach(),
      m.picoRead && m.picoRead.kind, m.nanoRead && m.nanoRead.kind,
      m.autoNote, m.sample && m.sample.record.node_id].join("~");
    if (s.slotKey !== slotKey) {
    s.slotKey = slotKey;
    s.slot.textContent = "";
    if (!m.pico) {
      var b = btn("+ WAVE PICO · " + MODEL_PRICE.pico, "cl-slot__buy", function () { buyPico(m.id); });
      (DOM.priced = DOM.priced || []).push({ b: b, cost: MODEL_PRICE.pico });
      b.title = "A model on this machine tells you when the reading stops being trustworthy.";
      s.slot.appendChild(b);
      s.slot.appendChild(el("i", "cl-slot__hint", "no model · you are reading this dial yourself"));
    } else {
      var head = el("div", "cl-say cl-say--pico");
      head.appendChild(el("b", "cl-say__who", "WAVE PICO"));
      var r = m.picoRead;
      if (!r) {
        head.appendChild(el("span", "cl-say__word", "steady"));
        head.dataset.verdict = "ok";
      } else if (r.kind === "caught") {
        head.appendChild(el("span", "cl-say__word", "“ " + r.said + "”"));
        head.dataset.verdict = "bad";
      } else if (r.kind === "unsure") {
        head.appendChild(el("span", "cl-say__word", "not sure"));
        head.dataset.verdict = "warn";
      } else {
        head.appendChild(el("span", "cl-say__word", "“ " + r.said + "”"));
        head.dataset.verdict = "warn";
      }
      s.slot.appendChild(head);

      if (G.nano && m.nanoRead) {
        var n = el("div", "cl-say cl-say--nano");
        n.appendChild(el("b", "cl-say__who", "WAVE NANO"));
        if (m.nanoRead.kind === "resolved") {
          n.appendChild(el("span", "cl-say__why", CONDITION_FIX[m.cond]));
          if (!inBand(m, m.real)) {
            n.appendChild(el("span", "cl-say__fix",
              m.id === "oven" ? "Then bring HEAT back inside the band." : "Then reduce SPEED - you are over the limit."));
          }
        } else {
          n.appendChild(el("span", "cl-say__why",
            "says “ " + m.nanoRead.said + "” - the recorded senior got this one wrong too."));
        }
        s.slot.appendChild(n);
      }
      if (m.sample && m.cond !== "none") {
        var prov = el("i", "cl-say__prov",
          "replayed record " + m.sample.record.node_id +
          (m.sample.sameKind ? "" : " (recorded on another instrument - the bench has no " +
            m.spec.sensor.label.toLowerCase() + " channel with this fault)"));
        s.slot.appendChild(prov);
      }
    }

    /* THE HANDOVER, per machine: once a desk model can hold a knob, the
       player may give this one away - and take it back at any time. */
    if (autonomyReach() > 0) {
      var auto = el("div", "cl-auto" + (m.auto ? " is-on" : ""));
      var ab = btn(m.auto ? "MODELS HOLD THIS KNOB" : "LET THE MODELS DRIVE",
        "cl-auto__btn", function () { toggleAuto(m.id); });
      ab.setAttribute("aria-pressed", m.auto ? "true" : "false");
      ab.disabled = !m.auto && autoCount() >= autonomyReach();
      if (ab.disabled) ab.title = "Wave Giga can hold every knob; Micro can hold one.";
      auto.appendChild(ab);
      if (m.auto && m.autoNote) auto.appendChild(el("i", "cl-auto__note", m.autoNote));
      s.slot.appendChild(auto);
    }

    /* SERVICE IS ALWAYS OFFERED, and that is deliberate. Enabling it only on a
       real fault would quietly tell the player which machine is lying - which
       is precisely the knowledge they are supposed to be missing in phase 0,
       and precisely what they are buying when they buy Pico. So you may
       service anything at any time, and servicing a healthy sensor simply
       costs you the money. The highlight appears only when a MODEL told you. */
    var told = (m.picoRead && m.picoRead.kind === "caught") ||
               (m.nanoRead && m.nanoRead.kind === "resolved");
    }

    s.service.textContent = m.servicing > 0 ? "SERVICING " + m.servicing.toFixed(1) + "s" : "SERVICE";
    s.service.disabled = m.servicing > 0;
    s.service.classList.toggle("is-needed", !!told);
    var next = TIERS[m.id][m.tier + 1];
    s.upgrade.textContent = next ? "UPGRADE · " + next.price : "TOP TIER";
    s.upgrade.disabled = !next || G.coins < next.price;

    if (s.dots) {
      var flowing = !m.stopped && rateOf(m) > 0.05;
      s.dots.forEach(function (d, i) {
        d.classList.toggle("is-on", flowing);
        d.style.animationDelay = REDUCED ? "0s" : (i * 0.18) + "s";
      });
      s.buf.textContent = Math.floor(m.buffer);
      s.buf.classList.toggle("is-starved", m.buffer < 0.5);
    }
  }

  /* THE GOALS. Two or three lines that always say what is worth doing next,
     and what the next thing you can buy will actually change. */
  function paintGoals() {
    var rows = [];
    var picos = G.machines.filter(function (m) { return m.pico; }).length;
    rows.push(["SHIP", Math.floor(G.cookies) + " / 100 cookies",
      G.cookies >= 100 ? "contract filled" : "keep every machine inside its band"]);
    if (!picos) {
      rows.push(["NEXT", "WAVE PICO · " + MODEL_PRICE.pico,
        "puts a model on one machine so it tells you when its reading stops being trustworthy"]);
    } else if (!G.nano) {
      rows.push(["NEXT", "WAVE NANO · " + MODEL_PRICE.nano,
        "explains WHY a reading is wrong and what to change"]);
    } else if (!G.micro) {
      rows.push(["NEXT", "WAVE MICRO · " + MODEL_PRICE.micro,
        "the site view - and it can hold one knob for you"]);
    } else if (!G.giga) {
      rows.push(["NEXT", "WAVE GIGA · " + MODEL_PRICE.giga,
        "the plant view - and it can run every knob"]);
    } else if (autoCount() < G.machines.length) {
      rows.push(["NEXT", "HAND OVER THE LAST KNOBS",
        "let the models drive all three and the plant runs itself"]);
    } else {
      rows.push(["DONE", "THE PLANT RUNS ITSELF", "watch the results and keep it honest"]);
    }
    if (G.incidents.missed) {
      rows.push(["WATCH", G.incidents.missed + " incident" + (G.incidents.missed === 1 ? "" : "s") + " stopped the line",
        "a model that missed in the recording misses here too"]);
    }
    DOM.goals.textContent = "";
    rows.slice(0, 3).forEach(function (r) {
      var li = el("li", "cl-goal");
      li.appendChild(el("b", null, r[0]));
      li.appendChild(el("span", null, r[1]));
      li.appendChild(el("i", null, r[2]));
      DOM.goals.appendChild(li);
    });
  }

  /* THE UPGRADE MAP. Small enough to show whole: what unlocks what, what a
     locked node costs and promises, what a bought node now does. */
  function paintShop() {
    DOM.shop.textContent = "";

    function node(opts) {
      var n = el("div", "cl-node" + (opts.owned ? " is-owned" : "") + (opts.locked ? " is-locked" : ""));
      if (opts.tier) n.dataset.tier = opts.tier;
      n.appendChild(el("b", null, opts.name));
      n.appendChild(el("i", null, opts.owned ? opts.does : opts.promise));
      if (opts.owned) n.appendChild(el("span", "cl-node__on", "INSTALLED"));
      else if (opts.locked) n.appendChild(el("span", "cl-node__lock", opts.locked));
      else {
        var b = btn(opts.price, "cl-act", opts.buy);
        (DOM.priced = DOM.priced || []).push({ b: b, cost: opts.cost || 0 });
        n.appendChild(b);
      }
      return n;
    }

    var col1 = el("div", "cl-branch");
    col1.appendChild(el("span", "cl-branch__head", "THE MACHINES"));
    G.machines.forEach(function (m) {
      var next = TIERS[m.id][m.tier + 1], cur = tierOf(m);
      col1.appendChild(node({
        name: m.spec.name + " " + cur.name,
        owned: !next,
        does: "top tier · band " + cur.lo + "-" + cur.hi,
        promise: next ? "faster, and a wider band (" + next.lo + "-" + next.hi + ") so a small lie is less fatal" : "",
        price: next ? "UPGRADE · " + next.price : "",
        cost: next ? next.price : 0,
        buy: function () { buyTier(m.id); },
      }));
    });

    var col2 = el("div", "cl-branch");
    col2.appendChild(el("span", "cl-branch__head", "ON THE MACHINE"));
    G.machines.forEach(function (m) {
      col2.appendChild(node({
        name: "PICO · " + m.spec.name, tier: "pico", owned: m.pico,
        does: "names the fault on this machine",
        promise: "a light and one word when this reading stops being trustworthy",
        price: "BUY · " + MODEL_PRICE.pico,
        cost: MODEL_PRICE.pico,
        buy: function () { buyPico(m.id); },
      }));
    });

    var col3 = el("div", "cl-branch");
    col3.appendChild(el("span", "cl-branch__head", "THE DESK"));
    [["nano", "WAVE NANO", "tells you WHY, and what to change", "explains the fault and gives the fix"],
     ["micro", "WAVE MICRO", "the site view - and it can hold one knob", "all three machines at once"],
     ["giga", "WAVE GIGA", "the plant view - and it can run every knob", "bottleneck, forecast, full autonomy"]
    ].forEach(function (row) {
      var id = row[0];
      var locked = (id !== "nano" && !G.nano) ? "NEEDS NANO" : "";
      col3.appendChild(node({
        name: row[1], tier: TIER_COLOUR[id], owned: G[id], locked: locked,
        does: row[3], promise: row[2],
        price: "BUY · " + MODEL_PRICE[id],
        cost: MODEL_PRICE[id],
        buy: function () { buyDesk(id); },
      }));
    });

    DOM.shop.appendChild(col1);
    DOM.shop.appendChild(col2);
    DOM.shop.appendChild(col3);
  }

  /* A first-party sparkline. No library may be loaded on this page, and the
     series is the game's own recorded history, so it draws itself. */
  function spark(series, pick, w, h) {
    var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 " + w + " " + h);
    svg.setAttribute("class", "cl-spark");
    svg.setAttribute("role", "img");
    if (series.length < 2) return svg;
    var vals = series.map(pick);
    var hi = Math.max.apply(null, vals) || 1, lo = Math.min.apply(null, vals);
    var span = (hi - lo) || 1;
    var d = vals.map(function (v, i) {
      var x = (i / (vals.length - 1)) * (w - 2) + 1;
      var y = h - 1 - ((v - lo) / span) * (h - 2);
      return (i ? "L" : "M") + x.toFixed(1) + " " + y.toFixed(1);
    }).join(" ");
    var p = document.createElementNS("http://www.w3.org/2000/svg", "path");
    p.setAttribute("d", d);
    p.setAttribute("class", "cl-spark__line");
    svg.appendChild(p);
    svg.setAttribute("aria-label", "trend, " + vals.length + " samples, latest " + vals[vals.length - 1].toFixed(2));
    return svg;
  }

  function resultsView() {
    var box = el("div", "cl-view cl-view--results");
    box.appendChild(el("b", "cl-view__head", "RESULTS"));
    var grid = el("div", "cl-results");
    var up = G.runTime ? (G.upTime / G.runTime) * 100 : 100;
    var inc = G.incidents;
    [["COOKIES/S", (G.history.length ? G.history[G.history.length - 1].rate : 0).toFixed(2)],
     ["UPTIME", up.toFixed(0) + "%"],
     ["CAUGHT IN TIME", String(inc.caught)],
     ["LINE STOPPED", String(inc.missed)],
     ["SERVICED", String(G.serviced)],
     ["COINS", String(Math.floor(G.coins))]].forEach(function (p) {
      var c = el("div", "cl-results__cell");
      c.appendChild(el("b", null, p[1]));
      c.appendChild(el("i", null, p[0]));
      grid.appendChild(c);
    });
    box.appendChild(grid);
    if (G.history.length > 1) {
      var s = el("div", "cl-results__spark");
      s.appendChild(el("i", null, "cookies per second"));
      s.appendChild(spark(G.history, function (h) { return h.rate; }, 200, 34));
      box.appendChild(s);
    }
    if (inc.missed) {
      box.appendChild(el("p", "cl-view__warn",
        inc.missed + " incident" + (inc.missed === 1 ? "" : "s") + " stopped the line before anyone acted. " +
        "Automation does not make the recorded models infallible - a miss in the replay is still a miss here."));
    }
    box.appendChild(el("i", "cl-view__note",
      "the game's own numbers, accumulated as you played - not a measured claim about any model"));
    return box;
  }

  function paintDesk() {
    DOM.deskView.textContent = "";
    if (!G.micro && !G.giga) {
      DOM.deskView.appendChild(el("p", "cl-deskview__empty",
        "With no desk model you only have the three dials on the line. Buy Micro to see the whole site at once."));
      if (G.history.length > 4) DOM.deskView.appendChild(resultsView());
      return;
    }
    if (G.micro) {
      var sv = siteView();
      var box = el("div", "cl-view cl-view--micro");
      box.appendChild(el("b", "cl-view__head", "WAVE MICRO · SITE VIEW"));
      var tbl = el("div", "cl-view__rows");
      sv.rows.forEach(function (r) {
        var row = el("div", "cl-view__row");
        row.appendChild(el("b", null, r.name));
        row.appendChild(el("span", null, r.tier + " · band " + r.band));
        row.appendChild(el("span", null, "headroom " + r.head.toFixed(1)));
        row.appendChild(el("i", null, r.stopped ? "STOPPED" : r.cond !== "none" ? "SENSOR " + CONDITION_WORD[r.cond].toUpperCase() : "ok"));
        tbl.appendChild(row);
      });
      box.appendChild(tbl);
      box.appendChild(el("span", "cl-view__worst", "Least headroom: " + sv.worst.name + "."));
      box.appendChild(el("i", "cl-view__note", "arithmetic over the game's own numbers - Micro has no recorded run on this bench"));
      DOM.deskView.appendChild(box);
    }
    if (G.giga) {
      var pv = plantView();
      var g = el("div", "cl-view cl-view--giga");
      g.appendChild(el("b", "cl-view__head", "WAVE GIGA · PLANT VIEW"));
      g.appendChild(el("span", null, "Bottleneck: " + pv.bottleneck.name + " at " + pv.bottleneck.rate.toFixed(2) + "/s."));
      g.appendChild(el("span", null, pv.loss > 0.05
        ? "Upgrading it would free about " + pv.loss.toFixed(2) + "/s of the line's pace."
        : "The three stations are balanced."));
      g.appendChild(el("span", null, pv.faults ? pv.faults + " sensor(s) currently lying to you." : "Every sensor is honest right now."));
      g.appendChild(el("i", "cl-view__note", "arithmetic over the game's own numbers - Giga has no recorded run on this bench"));
      DOM.deskView.appendChild(g);
    }
    /* Once every knob has been handed over, the player's job has changed:
       they are not turning dials any more, they are reading results. So the
       results panel moves to the front and the line becomes the thing you
       supervise rather than operate. */
    var handedOver = autonomyReach() > 0 && autoCount() === G.machines.length;
    var res = resultsView();
    if (handedOver) {
      res.classList.add("is-lead");
      res.insertBefore(el("span", "cl-view__crown", "PLANT RUNNING ITSELF · YOU ARE THE OPERATOR NOW"), res.firstChild);
      DOM.deskView.insertBefore(res, DOM.deskView.firstChild);
    } else {
      DOM.deskView.appendChild(res);
    }
  }

  /* Rebuilding every control twelve times a second re-creates the very
     button under the player's cursor, so clicks get eaten and focus is lost.
     Structure is rebuilt only when discrete state actually changes; the
     numbers repaint every frame, and prices update on the buttons in place. */
  function structureKey() {
    return [G.nano, G.micro, G.giga, G.incidents.missed, G.ready,
      G.machines.map(function (m) {
        return [m.pico, m.auto, m.cond, m.tier,
          m.picoRead && m.picoRead.kind, m.nanoRead && m.nanoRead.kind].join(",");
      }).join("|"),
      Math.floor(G.cookies) >= 100].join("~");
  }

  function paint() {
    if (!DOM.stations) return;
    DOM.coins.textContent = Math.floor(G.coins);
    DOM.cookies.textContent = Math.floor(G.cookies);
    var pk = machine("packer");
    DOM.rate.textContent = (pk.stopped ? 0 : rateOf(pk) * 1.5).toFixed(1);
    DOM.runBtn.textContent = G.running ? "PAUSE" : "START";
    DOM.runBtn.dataset.on = G.running ? "1" : "0";
    G.machines.forEach(paintStation);
    DOM.shipTxt.textContent = Math.floor(G.cookies) + " cookies";
    DOM.spoilTxt.textContent = G.spoiled > 1 ? Math.floor(G.spoiled) + " burnt" : "";

    var key = structureKey();
    if (key !== DOM.key) {
      DOM.key = key;
      DOM.priced = (DOM.priced || []).filter(function (p) { return p.b.isConnected; });
      paintGoals();
      paintShop();
      paintDesk();
      DOM.log.textContent = "";
      G.log.forEach(function (c) { DOM.log.appendChild(el("li", null, c)); });
    }
    // prices react to the wallet without rebuilding anything
    (DOM.priced || []).forEach(function (p) { p.b.disabled = G.coins < p.cost || p.off; });
  }

  /* ---- loop ------------------------------------------------------------ */
  function frame(t) {
    raf = window.requestAnimationFrame(frame);
    if (!lastT) lastT = t;
    var dt = Math.min(0.25, (t - lastT) / 1000);
    lastT = t;
    if (!G.running || !G.ready) return;
    acc += dt;
    var guard = 0;
    while (acc >= TICK && guard++ < 8) { step(TICK); acc -= TICK; }
    paint();
  }

  function toggleRun() { G.running = !G.running; lastT = 0; paint(); }

  function resetGame() {
    G = freshState();
    var host = document.getElementById("wfGame");
    if (host) buildShell(host);
    loadRecords();
  }

  /* ---- boot ------------------------------------------------------------ */
  function loadRecords() {
    window.fetch("data/wave-measured.json").then(function (r) {
      if (!r.ok) throw new Error("no deck");
      return r.json();
    }).then(function (d) {
      if (!d || !Array.isArray(d.records) || !d.records.length) throw new Error("empty");
      G.records = d.records;
      G.ready = true;
      addLog(d.records.length + " recorded signal windows loaded - Pico and Nano will speak from these.");
      paint();
    }).catch(function () {
      G.error = "The recorded windows did not load, so the models cannot speak. The line still runs.";
      G.ready = true;
      addLog(G.error);
      paint();
    });
  }

  function boot() {
    var host = document.getElementById("wfGame");
    if (!host) return;
    buildShell(host);
    paint();
    loadRecords();
    if (window.requestAnimationFrame) raf = window.requestAnimationFrame(frame);
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) { G.wasRunning = G.running; G.running = false; }
      else if (G.wasRunning) { G.running = true; lastT = 0; }
      paint();
    });
  }

  if (typeof window !== "undefined") {
    window.__waveFactoryTest = {
      freshState: freshState,
      kindOfTag: kindOfTag,
      picoRead: picoRead,
      nanoRead: nanoRead,
      shownValue: shownValue,
      tiers: TIERS,
      prices: MODEL_PRICE,
      conditions: CONDITIONS,
      floor: FLOOR,
      sampleWith: function (records, kind, truth, seed) {
        var save = G.records; G.records = records;
        var out = sampleFor(kind, truth, seed);
        G.records = save;
        return out;
      },
      siteWith: function (state) { var s = G; G = state; var v = siteView(); G = s; return v; },
      plantWith: function (state) { var s = G; G = state; var v = plantView(); G = s; return v; },
      stepWith: function (state, dt) { var s = G; G = state; step(dt); G = s; return state; },
      rateOf: function (state, id) { var s = G; G = state; var m = machine(id); var v = rateOf(m); G = s; return v; },
      autoWith: function (state, id, dt) {
        var s = G; G = state; var m = machine(id); autoAdjust(m, dt); G = s; return m;
      },
      reachWith: function (state) { var s = G; G = state; var v = autonomyReach(); G = s; return v; },
      clearWith: function (state, id) {
        var s = G; G = state; clearCondition(machine(id)); G = s; return state.incidents;
      },
    };
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
