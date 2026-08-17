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
  /* THE ACTION LADDER (founder direction, v24; grown to a full vocabulary in
     v25 - "other ways we can try to fix the problem without service first").
     In cost order:
       ADJUST is free - the dials fix PROCESS problems (too fast, too hot),
         and adjusting never locks anything: process control is not
         maintenance.
       RESTART is free and fast - re-seats stuck and dropped-out sensors,
         mostly.
       CLEAN is free but slower - clears a noisy pickup (interference, dirt),
         and nothing else.
       RECALIBRATE costs a little and takes a while - it is THE fix for a
         drifting sensor, and useless against anything else.
       INSPECT is free, slow, and fixes nothing: it REVEALS the fault kind.
         The patient player can always learn what Nano would say instantly -
         Nano sells TIME, not secrets. Inspecting never locks anything.
       SERVICE always fixes everything, railed included, and costs real
         money. A wallet that cannot cover it goes ON LOAN - the balance
         turns negative and earnings pay the debt down before they pile up.
         Service is therefore ALWAYS available: the v22 soft-lock stays
         impossible, via credit.
     THE LOCKOUT RULE (v25): choosing the WRONG verb for the fault - one the
     doctrine gives under-50% odds - locks that machine's maintenance for a
     minute when it fails. The RIGHT verb failing its dice is not a wrong
     call: it just costs the downtime, and you may try again. */
  var SERVICE_SECS = 4;          // the machine is down while the crew works
  var SERVICE_COST = 30;         // and the crew invoices, loan if needed
  var LOCKOUT_SECS = 60;         // the cost of guessing the wrong verb

  /* THE MAINTENANCE DOCTRINE - a game-sim rule about a game plant, and the
     thing Nano sells you: which verb clears which fault. Stuck and dropped-
     out sensors usually just need re-seating; a noisy pickup wants cleaning;
     drift is calibration; railing is hardware and only the crew fixes it. */
  var VERBS = {
    restart: { label: "RESTART", secs: 1.5, cost: 0,
      odds: { stuck: 0.8, dropout: 0.8, noisy: 0.25, drifting: 0, railed: 0 } },
    clean: { label: "CLEAN", secs: 6, cost: 0,
      odds: { stuck: 0, dropout: 0, noisy: 0.85, drifting: 0, railed: 0 } },
    recal: { label: "RECAL", secs: 8, cost: 10,
      odds: { stuck: 0, dropout: 0, noisy: 0, drifting: 0.95, railed: 0 } },
  };
  var RESTART_ODDS = VERBS.restart.odds;   // v24 name, same table
  var RESTART_SECS = VERBS.restart.secs;
  var INSPECT_SECS = 10;

  // the cheapest correct verb per fault kind - what Nano prescribes, what a
  // finished INSPECT points at, and what automation-with-Nano executes
  function verbFor(cond) {
    if (cond === "stuck" || cond === "dropout") return "restart";
    if (cond === "noisy") return "clean";
    if (cond === "drifting") return "recal";
    return "service";                       // railed: hardware, crew only
  }

  // what a hands-on INSPECT finds, per kind - the manual version of Nano
  var INSPECT_WORD = {
    stuck: "the sensor face is frozen - STUCK. A restart usually re-seats it.",
    dropout: "intermittent contact - DROPPING OUT. A restart usually re-seats it.",
    noisy: "interference on the pickup - NOISY. Clean it.",
    drifting: "readings slide against the hand gauge - DRIFTING. Recalibrate it.",
    railed: "pinned hard at its limit - RAILED. That is hardware: only service fixes it.",
    none: "nothing wrong found - this sensor is honest.",
  };

  // The recorded fault taxonomy. `tell` is how the sensor LIES; the process
  // itself keeps drifting underneath regardless.
  var CONDITIONS = ["stuck", "drifting", "dropout", "noisy", "railed"];
  var CONDITION_WORD = {
    none: "steady", stuck: "stuck", drifting: "drifting",
    dropout: "dropping out", noisy: "noisy", railed: "railed",
  };
  // What the site gateway tells you to DO about it - the doctrine, prescribed
  // per fault kind. Game advice about a game plant, and it says which verb.
  var CONDITION_FIX = {
    stuck: "the reading is frozen - the real value has moved on. A RESTART usually clears a frozen sensor.",
    dropout: "the reading keeps vanishing. A RESTART usually re-seats it.",
    noisy: "the reading is jittering - interference on the pickup. CLEAN it; a restart rarely helps.",
    drifting: "the reading is sliding away from the truth - that is calibration. RECALIBRATE it; a restart will not help.",
    railed: "the reading is pinned at its limit - that is hardware. Only SERVICE fixes a railed sensor.",
  };
  var HEALTHY_WINDOW = 7;        // seconds between healthy-window redraws

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
      restarting: 0, fixVerb: "restart",     // the verb whose timer is running
      inspecting: 0, inspected: false,       // the manual diagnosis
      lockout: 0,                            // the action ladder's states
      ambient: 0, event: null, eventLeft: 0, // slow process creep (game sim)
      // distinct seeds, or all three machines draw the same sequence
      seed: spec.id === "mixer" ? 9176 : spec.id === "oven" ? 41213 : 77431,
      nextFault: 0,
      sample: null,                         // the drawn record, while faulted
      healthySample: null, windowLeft: 0, healthyDraws: 0,
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
      peakRate: 0, cleanRun: 0, bestRun: 0,
      // THE HANDOVER: incidents and a rolling metrics series, so an
      // automated plant has results to show for itself. Game arithmetic.
      incidents: { caught: 0, missed: 0, open: 0 },
      history: [], sampleAt: 0, upTime: 0, runTime: 0,
      deskTab: "site",
      /* WHAT ACTUALLY MOVED between stations this tick, recorded by step()
         itself. The floor's traveling cookies SPEND these accumulators - a
         sprite may only appear because the simulation really transferred
         product on that segment, so a starved belt goes visibly empty. */
      flow: { dough: 0, baked: 0, out: 0 },
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
     claim, and the deck does not get to blur that.
     HEALTHY windows draw too (truth "none"): that is where Pico earns its
     keep, asserting " none" with a fat margin most of the time - and where
     the bench's recorded FALSE ALARMS surface, because a none-record whose
     child called a fault plays here exactly as it was recorded. */
  function sampleFor(kind, truth, seed, excludeIds) {
    var recs = G.records;
    if (!recs.length) return null;
    var exact = [], any = [];
    for (var i = 0; i < recs.length; i++) {
      var r = recs[i];
      if (r.truth !== truth || !r.child || !r.parent) continue;
      any.push(r);
      if (kindOfTag(r.window && r.window.tag) === kind) exact.push(r);
    }
    var pool = exact.length ? exact : any;
    if (!pool.length) return null;
    /* THE CHORUS FIX (v25). The founder's cascade screenshot showed three
       machines chanting the same sub-floor margin, because the small per-
       truth pools overlap: a cross-instrument fallback on one machine can
       land on the exact record another machine is already displaying.
       Concurrent draws now avoid records already on display elsewhere -
       when the pool is big enough to allow it. A pool of one is a pool of
       one; a real record beats an empty slot. */
    if (excludeIds && excludeIds.length) {
      var fresh = pool.filter(function (r2) { return excludeIds.indexOf(r2.node_id) < 0; });
      if (fresh.length) pool = fresh;
    }
    return { record: pool[seed % pool.length], sameKind: exact.length > 0 };
  }

  // the records the OTHER machines are currently showing, so draws differ
  function activeRecordIds(exceptId) {
    var ids = [];
    G.machines.forEach(function (m) {
      if (m.id === exceptId) return;
      if (m.sample) ids.push(m.sample.record.node_id);
      if (m.healthySample) ids.push(m.healthySample.record.node_id);
    });
    return ids;
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

  /* Meter geometry, pure so the tests can run it: the display range is the
     band padded 25% each side; state is "out" past an edge, "edge" within
     12% of one, "ok" otherwise (and "gone" for a dropped-out reading). The
     state styles the METER ONLY - lamp semantics stay untouched. */
  function meterInfo(m, shown) {
    var t = tierOf(m), span = (t.hi - t.lo) || 1, pad = span * 0.25;
    var dLo = t.lo - pad, dHi = t.hi + pad, dSpan = dHi - dLo;
    var out = { zoneLeft: (t.lo - dLo) / dSpan, zoneWidth: span / dSpan };
    if (shown == null) { out.pos = 0; out.state = "gone"; return out; }
    out.pos = Math.max(0, Math.min(1, (shown - dLo) / dSpan));
    if (shown < t.lo || shown > t.hi) out.state = "out";
    else if (shown - t.lo < span * 0.12 || t.hi - shown < span * 0.12) out.state = "edge";
    else out.state = "ok";
    return out;
  }

  /* THE DIAL HINT - pure, and keyed off the DISPLAYED needle only. When the
     meter shows a reading outside the band, the first move is always the
     free one: the dial. That is honest guidance from visible truth alone -
     it does not peek at whether the sensor is lying (a railed sensor will
     ignore the dial, and the player learns the next rung when adjusting
     visibly does nothing). It exists because the founder, facing a full-line
     cascade of process problems, was steered by the UI toward RESTART - the
     wrong verb, with a lockout price - when three dials would have fixed it
     for free. */
  function dialHint(m, shown) {
    if (shown == null) return null;
    var t = tierOf(m);
    if (shown >= t.lo && shown <= t.hi) return null;
    var word = m.spec.control.label;
    return shown > t.hi
      ? { dir: "down", label: "needle OUTSIDE the band - the dial is the free first move: bring " + word + " down" }
      : { dir: "up", label: "needle OUTSIDE the band - the dial is the free first move: bring " + word + " up" };
  }

  function startCondition(m, forced) {
    var pick = forced || CONDITIONS[Math.floor(rnd(m) * CONDITIONS.length)];
    m.cond = pick;
    m.condAge = 0;
    m.stuckAt = m.real;
    m.driftLie = 0;
    m.inspected = false;
    m.sample = sampleFor(m.spec.sensor.kind, pick, Math.floor(rnd(m) * 997), activeRecordIds(m.id));
    m.picoRead = m.pico ? picoRead(m.sample) : null;
    /* the gateway hears THROUGH the child: a machine with no Pico sends no
       report up, so Nano has nothing to say about it. This is also what
       keeps the doctrine highlight honest - it lights only off knowledge
       that actually flowed (a mounted Pico's report, or your own INSPECT). */
    m.nanoRead = (G.nano && m.pico) ? nanoRead(m.sample) : null;
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
    m.inspected = false; m.inspecting = 0;
    m.windowLeft = 0;   // a healthy window redraws immediately
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
    /* A held knob may also EXECUTE maintenance, not just trim the dial - but
       only on a fault the models actually raised. An alarm here means the
       replayed read said a fault word (or the gateway resolved one); a
       recorded miss said " none", raises nothing, and the automation is
       fooled with everyone else - which is why the results panel still logs
       missed incidents on a fully automated plant.
       WITH Nano the action follows the doctrine (restart what restarts,
       service what does not); WITHOUT it the automation buys certainty the
       expensive way, exactly like a player without advice. */
    if (m.cond !== "none" && !m.servicing && !m.restarting && !m.inspecting && m.lockout <= 0) {
      var alarmed = (m.picoRead && m.picoRead.said !== "none") ||
                    (m.nanoRead && m.nanoRead.kind === "resolved");
      if (alarmed && m.condAge > 2.5) {
        var holder = G.giga ? "Giga" : "Micro";
        /* WITH Nano the automation runs the CHEAPEST CORRECT verb, exactly
           as it would prescribe to a person; WITHOUT it, it buys certainty
           the expensive way - service - like any player without advice. */
        var v = G.nano ? verbFor(m.cond) : "service";
        if (v === "service") {
          m.autoNote = holder + " called service on the models' word" +
            (G.nano ? " - Nano ruled the cheap verbs out" : "");
          service(m.id);
        } else {
          m.autoNote = holder + " ran " + VERBS[v].label + " on Nano's advice";
          maintain(m.id, v);
        }
      }
    }
  }

  /* Slow PROCESS CREEP - the hidden conditions the founder asked for. Every
     so often the plant itself leans on a machine (afternoon sun on the oven,
     dough thickening in the mixer, belt friction at the packer) and the real
     value creeps toward the band edge while every sensor stays honest. The
     dials are the fix; the models have nothing to say about it - it is not a
     sensor fault, and Nano says exactly that. Game simulation, like all
     plant physics here. */
  var CREEP_FLAVOUR = {
    mixer: "Dough is thickening - mixer load creeping up.",
    oven: "Afternoon sun on the oven - ambient heat creeping up.",
    packer: "Belt friction rising - packer load creeping up.",
  };
  function stepCreep(m, dt) {
    var t = tierOf(m), span = t.hi - t.lo;
    if (!m.event) {
      if (!m.eventLeft) m.eventLeft = 22 + rnd(m) * 34;
      m.eventLeft -= dt;
      if (m.eventLeft <= 0) {
        m.event = { t: 0, ramp: 12, hold: 10, decay: 9, mag: span * (0.14 + rnd(m) * 0.16) };
        m.eventLeft = 0;
        addLog(CREEP_FLAVOUR[m.id]);
      }
      m.ambient *= 0.995;
      return;
    }
    var e = m.event;
    e.t += dt;
    if (e.t < e.ramp) m.ambient = e.mag * (e.t / e.ramp);
    else if (e.t < e.ramp + e.hold) m.ambient = e.mag;
    else if (e.t < e.ramp + e.hold + e.decay) m.ambient = e.mag * (1 - (e.t - e.ramp - e.hold) / e.decay);
    else { m.ambient = 0; m.event = null; }
  }

  function stepMachine(m, dt) {
    var t = tierOf(m);
    if (m.lockout > 0) m.lockout = Math.max(0, m.lockout - dt);
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
    /* A FIXING VERB (restart / clean / recalibrate) holds the machine for its
       working time, then either clears the fault per the doctrine odds, or
       resolves against you. THE LOCKOUT RULE: only the WRONG verb locks -
       one the doctrine gives under-50% odds against this fault. The right
       verb failing its dice just costs the downtime, and you may try again;
       punishing a correct call would make Nano's advice feel like a lie.
       Adjusting the dials stays available throughout: process control is
       not maintenance. */
    if (m.restarting > 0) {
      m.restarting -= dt;
      m.stopped = true;
      if (m.restarting <= 0) {
        var verb = VERBS[m.fixVerb] || VERBS.restart;
        m.restarting = 0;
        m.stoppedFor = 0;
        if (m.cond === "none") {
          addLog(m.spec.name + " " + verb.label.toLowerCase() + " done - nothing was wrong. " +
            verb.secs + "s lost.");
        } else {
          var odds = verb.odds[m.cond] || 0;
          if (rnd(m) < odds) {
            addLog(m.spec.name + " " + verb.label.toLowerCase() + " cleared the " +
              CONDITION_WORD[m.cond] + " sensor.");
            clearCondition(m);
            m.drift = 0;
          } else if (odds >= 0.5) {
            addLog(m.spec.name + " " + verb.label.toLowerCase() +
              " did not take this time - the right call can need a second go.");
          } else {
            m.lockout = LOCKOUT_SECS;
            addLog(m.spec.name + " " + verb.label.toLowerCase() + " was the wrong call - " +
              "maintenance locked " + LOCKOUT_SECS + "s while it recovers. (Nano would have said: " +
              (CONDITION_FIX[m.cond] || "").split(" - ").pop().toLowerCase() + ")");
          }
        }
      }
      return false;
    }
    /* INSPECT: the manual diagnosis. The machine is down while you look, and
       what you learn is the fault KIND - exactly the knowledge Nano sells
       instantly. Inspecting fixes nothing and never locks anything. */
    if (m.inspecting > 0) {
      m.inspecting -= dt;
      m.stopped = true;
      if (m.inspecting <= 0) {
        m.inspecting = 0;
        m.stoppedFor = 0;
        m.inspected = m.cond !== "none";
        addLog(m.spec.name + " inspected: " + INSPECT_WORD[m.cond]);
      }
      return false;
    }
    stepCreep(m, dt);
    // the control pulls the real value toward its target, plus whatever the
    // plant is leaning on it with, plus the unwatched walk during a fault
    var target = targetFor(m);
    m.real += (target + m.ambient + m.drift - m.real) * Math.min(1, dt * 1.6);

    // a faulted sensor means nobody is truly watching, so the process walks
    if (m.cond === "none") {
      m.drift *= 0.985;
      /* THE HEALTHY WINDOW REDRAW. Pico used to draw a record only when a
         fault started, so its healthy face was a dead "steady" and its lit
         face was whatever one record said for the whole incident - which is
         why it read as useless. A real Pico reads a fresh window every few
         seconds, so here it redraws a real truth-none record on a cadence:
         mostly " none" with a fat margin, occasionally the bench's own
         recorded false alarms and doubts, exactly as recorded. */
      m.windowLeft -= dt;
      if (m.windowLeft <= 0) {
        m.windowLeft = HEALTHY_WINDOW * (0.7 + rnd(m) * 0.6);
        if (m.pico) {
          m.healthySample = sampleFor(m.spec.sensor.kind, "none", Math.floor(rnd(m) * 997), activeRecordIds(m.id));
          m.healthyDraws += 1;
          m.picoRead = picoRead(m.healthySample);
          m.nanoRead = G.nano ? nanoRead(m.healthySample) : null;
        }
      }
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
    // the floor's sprite layer draws down these amounts, and nothing else
    G.flow.dough += made; G.flow.baked += bake; G.flow.out += pack;
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
    // the streak: how long the whole line has run clean, and the best yet
    if (allUp) { G.cleanRun += dt; if (G.cleanRun > G.bestRun) G.bestRun = G.cleanRun; }
    else G.cleanRun = 0;
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
    if (m.cond !== "none") {
      m.picoRead = picoRead(m.sample);
      m.nanoRead = G.nano ? nanoRead(m.sample) : null;   // the gateway hears its new child
    }
    else m.windowLeft = 0;               // read the first healthy window now
    addLog("Wave Pico installed on the " + m.spec.name.toLowerCase() + ".");
    paint();
    return true;
  }

  function buyDesk(which) {
    if (G[which] || G.coins < MODEL_PRICE[which]) return false;
    G.coins -= MODEL_PRICE[which];
    G[which] = true;
    if (which === "nano") {
      G.machines.forEach(function (m) {
        if (m.cond !== "none" && m.pico) m.nanoRead = nanoRead(m.sample);
      });
    }
    addLog("Wave " + which.charAt(0).toUpperCase() + which.slice(1) + " online at the desk.");
    paint();
    return true;
  }

  /* A FIXING VERB: free-or-cheap, and a gamble unless something told you the
     fault kind. RECALIBRATE invoices its small fee like service does - on
     loan if the wallet is short - so no verb is ever gated on being rich. */
  function maintain(id, verbName) {
    var m = machine(id);
    var verb = VERBS[verbName];
    if (!verb) return false;
    if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0 || m.lockout > 0) return false;
    if (verb.cost) G.coins -= verb.cost;
    m.fixVerb = verbName;
    m.restarting = verb.secs;
    addLog(m.spec.name + " " + verb.label.toLowerCase() +
      (verbName === "recal" ? "ibrating (" + verb.cost + " coins)…" : "ing…"));
    paint();
    return true;
  }
  function restart(id) { return maintain(id, "restart"); }
  function clean(id) { return maintain(id, "clean"); }
  function recal(id) { return maintain(id, "recal"); }

  /* INSPECT: look at the sensor yourself. Ten seconds of downtime buys the
     fault kind - the manual, patient version of what Nano says instantly.
     Never locked out: diagnosis is not maintenance. */
  function inspect(id) {
    var m = machine(id);
    if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0) return false;
    m.inspecting = INSPECT_SECS;
    addLog(m.spec.name + " being inspected - " + INSPECT_SECS + "s of downtime to look.");
    paint();
    return true;
  }

  /* SERVICE: the last rung - it always fixes, and it invoices. A wallet that
     cannot cover it goes ON LOAN: the balance turns negative and earnings pay
     the debt down before they pile up. That keeps service always available -
     the v22 soft-lock (broke player, dead line, no way back) stays impossible,
     now by credit instead of by making the work free. */
  function service(id) {
    var m = machine(id);
    if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0 || m.lockout > 0) return false;
    var hadFunds = G.coins >= SERVICE_COST;
    G.coins -= SERVICE_COST;
    m.servicing = SERVICE_SECS;          // the machine is down while it happens
    if (!hadFunds) {
      addLog("The crew invoiced " + SERVICE_COST + " you did not have - it is on loan. " +
        "Earnings pay the debt before they pile up.");
    }
    if (m.cond === "none") {
      G.wasted = (G.wasted || 0) + 1;
      addLog(m.spec.name + " sensor checked out fine - " + SERVICE_COST + " coins and " +
        SERVICE_SECS + "s of production spent finding that out.");
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

  /* THE SCENE IS DOM, NOT CANVAS - a deliberate call. The machine art ships
     as CSS mask plates that re-ink themselves per theme; canvas would flatten
     that to one baked colour and re-implement theming by hand. The dials and
     buttons must be real focusable elements anyway, and the moving parts
     (a dozen cookie sprites) are cheap as transformed DOM nodes. */
  function buildShell(host) {
    host.textContent = "";
    DOM = { stations: {}, cookies: [], pool: [] };
    var root = el("div", "cl");

    /* header */
    var head = el("header", "cl-head");
    var brand = el("div", "cl-brand");
    brand.appendChild(el("b", null, "THE COOKIE LINE"));
    brand.appendChild(el("span", null, "mix · bake · pack · ship"));
    head.appendChild(brand);
    var hud = el("div", "cl-hud");
    DOM.coins = el("b", null, "0"); DOM.cookies2 = el("b", null, "0"); DOM.rate = el("b", null, "0.0");
    DOM.best = el("b", null, "0s");
    DOM.statEls = {};
    [["COINS", DOM.coins], ["COOKIES", DOM.cookies2], ["PER SEC", DOM.rate], ["BEST RUN", DOM.best]]
      .forEach(function (p) {
        var st = el("span", "cl-stat"); st.appendChild(p[1]);
        DOM.statEls[p[0]] = st;
        st.appendChild(el("i", null, p[0])); hud.appendChild(st);
      });
    DOM.shopBtn = btn("SHOP + UPGRADES", "cl-run cl-run--shop", openShop);
    hud.appendChild(DOM.shopBtn);
    DOM.runBtn = btn("PAUSE", "cl-run", toggleRun);
    hud.appendChild(DOM.runBtn);
    hud.appendChild(btn("\u21bb", "cl-reset", resetGame));
    head.appendChild(hud);
    root.appendChild(head);

    /* the goal chip */
    DOM.goals = el("ul", "cl-goals");
    root.appendChild(DOM.goals);

    /* ================= THE FLOOR - the game is this picture ============= */
    var scroller = el("div", "clf-scroll");
    var floor = el("div", "clf-floor");
    /* NOT role="img" any more: the floor gained real controls in v25 (the
       buy tags on machines and the desk), and img makes children
       presentational - screen readers would lose the buttons entirely. */
    floor.setAttribute("role", "group");
    floor.setAttribute("aria-label",
      "The factory floor: a dough mixer, an oven and a packaging machine on one " +
      "conveyor, with cookies traveling between them. Buy tags on the machines " +
      "and the desk sell upgrades; every reading and dial is in the consoles below.");

    // the belt runs the width of the floor, in front of the machine bases
    var belt = el("div", "clf-belt");
    floor.appendChild(belt);

    // the dough bowl feeding the head of the line - scenery with a purpose:
    // the mixer visibly has something to mix
    var dough = el("i", "clf-doughfeed");
    dough.setAttribute("aria-hidden", "true");
    floor.appendChild(dough);

    // machines stand on the ground line
    G.machines.forEach(function (m) {
      floor.appendChild(machineBlock(m));
    });

    // shipping crates at the end of the belt
    var ship = el("div", "clf-ship");
    ship.appendChild(el("b", null, "SHIPPING"));
    DOM.crates = el("div", "clf-crates");
    ship.appendChild(DOM.crates);
    DOM.shipTxt = el("span", "clf-ship__n", "0");
    ship.appendChild(DOM.shipTxt);
    DOM.spoilTxt = el("i", "clf-ship__spoil", "");
    ship.appendChild(DOM.spoilTxt);
    floor.appendChild(ship);

    // the operator desk holds the bought desk models, physically at the end
    DOM.floorDesk = el("div", "clf-desk");
    floor.appendChild(DOM.floorDesk);

    // the traveling product - spent from G.flow, never invented
    DOM.cookieLayer = el("div", "clf-cookies");
    DOM.cookieLayer.setAttribute("aria-hidden", "true");
    floor.appendChild(DOM.cookieLayer);

    scroller.appendChild(floor);
    root.appendChild(scroller);

    /* the consoles: one per machine, tethered under its spot on the floor */
    var panels = el("div", "clf-panels");
    G.machines.forEach(function (m) { panels.appendChild(panel(m)); });
    root.appendChild(panels);

    /* THE MAINTENANCE CARD - the verb-fault doctrine, printed where a player
       can study it. This is the game's maintenance canon (game simulation,
       like all plant physics here); Nano quotes it instantly, INSPECT learns
       it slowly, and a wrong verb pays the lockout. */
    var maint = el("details", "cl-maint");
    maint.appendChild(el("summary", null, "MAINTENANCE CARD · what fixes what"));
    var mt = el("div", "cl-maint__rows");
    [["ADJUST (the dial)", "process out of its band - too fast, too hot", "free · never locks"],
     ["RESTART", "a stuck or dropped-out sensor, usually; noise rarely", VERBS.restart.secs + "s down"],
     ["CLEAN", "a noisy pickup (interference, dirt) - nothing else", VERBS.clean.secs + "s down"],
     ["RECALIBRATE", "a drifting sensor, specifically", VERBS.recal.cost + " coins · " + VERBS.recal.secs + "s down"],
     ["INSPECT", "fixes nothing - reveals what is actually wrong", INSPECT_SECS + "s down · never locks"],
     ["SERVICE", "everything, railed included - the sure thing", SERVICE_COST + " coins (loan if short) · " + SERVICE_SECS + "s down"],
    ].forEach(function (row) {
      var r = el("div", "cl-maint__row");
      r.appendChild(el("b", null, row[0]));
      r.appendChild(el("span", null, row[1]));
      r.appendChild(el("i", null, row[2]));
      mt.appendChild(r);
    });
    maint.appendChild(mt);
    maint.appendChild(el("p", "cl-maint__note",
      "Picking a verb the card rules out locks that machine's maintenance for " + LOCKOUT_SECS +
      "s when it fails. Nano quotes this card instantly; INSPECT learns it the slow way."));
    root.appendChild(maint);

    /* the desk views (site / plant / results) */
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

    /* the shop, as an overlay over the floor; the line pauses while it is up */
    DOM.shopOver = el("div", "clf-shopover");
    DOM.shopOver.hidden = true;
    var shopCard = el("div", "clf-shopcard");
    shopCard.setAttribute("role", "dialog");
    shopCard.setAttribute("aria-modal", "false");
    shopCard.setAttribute("aria-label", "Shop and upgrades");
    var shopHead = el("div", "cl-desk__head");
    shopHead.appendChild(el("b", null, "SHOP + UPGRADES"));
    shopHead.appendChild(el("span", null, "the line waits while you decide"));
    DOM.shopClose = btn("CLOSE", "cl-run", closeShop);
    shopHead.appendChild(DOM.shopClose);
    shopCard.appendChild(shopHead);
    DOM.shop = el("div", "cl-shop");
    shopCard.appendChild(DOM.shop);
    DOM.shopOver.appendChild(shopCard);
    root.appendChild(DOM.shopOver);

    host.appendChild(root);
  }

  /* ---- a machine, standing on the floor -------------------------------- */
  function machineBlock(m) {
    var s = DOM.stations[m.id] = {};
    var block = el("div", "clf-machine clf-machine--" + m.id);

    // the engraving itself
    s.art = el("span", "clf-art");
    s.art.setAttribute("aria-hidden", "true");
    block.appendChild(s.art);

    // the oven bakes with a visible warmth behind its porthole; decoration
    // for a state the lamp already words
    if (m.id === "oven") {
      s.glow = el("i", "clf-glow");
      block.appendChild(s.glow);
    }

    // the state lamp, on the machine body
    s.lamp = el("span", "cl-lamp clf-lamp");
    block.appendChild(s.lamp);

    // the service crew's wrench, over the machine while the work happens
    s.wrench = el("i", "clf-wrench");
    s.wrench.innerHTML = '<svg viewBox="0 0 24 24"><path d="M21 6.5a5 5 0 0 1-6.6 4.7L7 18.6a2.1 2.1 0 0 1-3-3l7.4-7.4A5 5 0 0 1 16.5 2l-2.8 2.8 1.4 4.1 4.1 1.4L22 7.5a5 5 0 0 1-1-.9Z"/></svg>';
    s.wrench.hidden = true;
    block.appendChild(s.wrench);

    // nameplate riveted to the base - and the upgrade sold right on it,
    // so the floor does its own selling (founder: the shop should be
    // visible inside the game, not only behind a button)
    var plate = el("span", "clf-plate");
    plate.appendChild(el("b", null, m.spec.name));
    s.tier = el("i", null, "Mk I");
    plate.appendChild(s.tier);
    s.plateBuy = btn("", "clf-buytag clf-buytag--tier", function (e) {
      e.stopPropagation();
      buyTier(m.id);
    });
    s.plateBuy.hidden = true;
    plate.appendChild(s.plateBuy);
    block.appendChild(plate);

    // a bought Pico bolts on as a badge; its word renders right here
    s.mount = el("span", "clf-mount");
    block.appendChild(s.mount);
    s.bubble = el("div", "clf-bubble");
    s.bubble.hidden = true;
    block.appendChild(s.bubble);

    return block;
  }

  /* ---- the machine's console, tethered beneath it ----------------------- */
  function panel(m) {
    var s = DOM.stations[m.id];
    var card = el("section", "cl-station clf-panel clf-panel--" + m.id);
    card.setAttribute("aria-label", m.spec.name + " console");

    var read = el("div", "cl-read");
    read.appendChild(el("i", null, m.spec.sensor.label));
    s.value = el("b", "cl-read__v", "--");
    read.appendChild(s.value);
    read.appendChild(el("i", "cl-read__u", m.spec.sensor.unit));
    card.appendChild(read);
    /* THE METER SHOWS THE BAND AS A ZONE, and the needle can visibly LEAVE
       it: the display range is the band padded a quarter each side, so out-
       of-band is a place on the meter, not an inference. Approaching an edge
       tints the METER amber before anything breaks - the lamp keeps its own
       honest rules and never pre-warns off the same hint. */
    s.band = el("div", "cl-band");
    s.bandZone = el("i", "cl-band__zone");
    s.bandMark = el("i", "cl-band__mark");
    s.band.appendChild(s.bandZone); s.band.appendChild(s.bandMark);
    card.appendChild(s.band);
    s.bandTxt = el("span", "cl-band__txt", "");
    card.appendChild(s.bandTxt);

    var ctl = el("label", "cl-ctl");
    s.ctl = ctl;
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

    /* the model slot: nano advice, autonomy, provenance */
    s.slot = el("div", "cl-slot");
    card.appendChild(s.slot);

    /* THE ACTION LADDER, in cost order. The dial above is ADJUST (free, fixes
       process problems, never locked). Then the fixing verbs, each honest to
       the fault taxonomy; INSPECT is diagnosis, not maintenance, so it stays
       live even through a lockout; SERVICE is the sure thing that invoices.
       Choosing a verb the doctrine rules out locks maintenance for a minute -
       the price of guessing when you could have asked. */
    var acts = el("div", "cl-acts");
    s.restart = btn("RESTART", "cl-act cl-act--restart", function () { restart(m.id); });
    s.restart.title = "Free, " + VERBS.restart.secs + "s down. Usually re-seats a stuck or " +
      "dropped-out sensor; rarely helps noise; never fixes drift or railing.";
    s.clean = btn("CLEAN", "cl-act cl-act--clean", function () { clean(m.id); });
    s.clean.title = "Free, " + VERBS.clean.secs + "s down. Clears a noisy pickup " +
      "(interference, dirt) - and nothing else.";
    s.recal = btn("RECAL · " + VERBS.recal.cost, "cl-act cl-act--recal", function () { recal(m.id); });
    s.recal.title = VERBS.recal.cost + " coins, " + VERBS.recal.secs + "s down. THE fix for a " +
      "drifting sensor; useless against anything else.";
    s.inspect = btn("INSPECT", "cl-act cl-act--inspect", function () { inspect(m.id); });
    s.inspect.title = "Free, " + INSPECT_SECS + "s down, fixes nothing: you look at the sensor " +
      "and learn what is actually wrong - what Nano tells you instantly. Never locked out.";
    s.service = btn("SERVICE", "cl-act cl-act--service", function () { service(m.id); });
    s.service.title = "Always fixes everything, railed included. Costs " + SERVICE_COST +
      " coins - taken on loan if the wallet is short.";
    s.upgrade = btn("UPGRADE", "cl-act", function () { buyTier(m.id); });
    [s.restart, s.clean, s.recal, s.inspect, s.service, s.upgrade]
      .forEach(function (b) { acts.appendChild(b); });
    card.appendChild(acts);

    return card;
  }

  function desk() {
    var d = el("section", "cl-desk");
    var head = el("div", "cl-desk__head");
    head.appendChild(el("b", null, "OPERATOR DESK"));
    head.appendChild(el("span", null, "buy the ladder · each tier tells you more"));
    d.appendChild(head);
    DOM.deskView = el("div", "cl-deskview");
    d.appendChild(DOM.deskView);
    return d;
  }

  /* ---- the shop overlay: the floor waits while you decide --------------- */
  function openShop() {
    if (!DOM.shopOver.hidden) return;
    G.shopWasRunning = G.running;
    G.running = false;
    DOM.shopOver.hidden = false;
    paintShop();
    paint();
    if (DOM.shopClose) DOM.shopClose.focus();
  }
  function closeShop() {
    if (DOM.shopOver.hidden) return;
    DOM.shopOver.hidden = true;
    if (G.shopWasRunning) { G.running = true; lastT = 0; }
    paint();
    if (DOM.shopBtn) DOM.shopBtn.focus();
  }

  /* ---- the traveling product -------------------------------------------
     Sprites are SPENT from G.flow, which only step() feeds - one dough blob
     per unit the mixer really made, one cookie per unit the oven really
     baked, one box per unit the packer really shipped. A starved segment
     stops spawning and its stretch of belt visibly empties. Positions are
     presentation; existence is simulation. */
  var SEGS = [
    { flow: "dough", from: 14, to: 42, kind: "dough" },
    { flow: "baked", from: 46, to: 72, kind: "cookie" },
    { flow: "out", from: 76, to: 91, kind: "box" },
  ];
  var COOKIE_SVGS = {
    dough: '<svg viewBox="0 0 20 14"><path d="M3 11 Q1 8 4 6 Q3 2 8 3 Q11 0 14 3 Q18 2 17 6 Q20 9 16 11 Q14 13 10 12 Q6 14 3 11 Z"/></svg>',
    cookie: '<svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="7"/><circle class="chip" cx="5.5" cy="6" r="1.2"/><circle class="chip" cx="10.5" cy="5.5" r="1.1"/><circle class="chip" cx="8.5" cy="10.5" r="1.2"/><circle class="chip" cx="5" cy="10" r="0.9"/></svg>',
    box: '<svg viewBox="0 0 18 14"><rect x="1" y="2" width="16" height="11" rx="1"/><line class="tape" x1="9" y1="2" x2="9" y2="13"/></svg>',
  };

  function spawnCookie(seg) {
    var n = DOM.pool.pop();
    if (!n) {
      n = el("i", "clf-cookie");
      DOM.cookieLayer.appendChild(n);
    }
    n.className = "clf-cookie clf-cookie--" + seg.kind;
    n.innerHTML = COOKIE_SVGS[seg.kind];
    n.hidden = false;
    DOM.cookies.push({ node: n, seg: seg, at: 0 });
  }

  function updateCookies(dt) {
    if (!DOM.cookieLayer) return;
    // spend the sim's transfer amounts; SPRITE_PER unit keeps the belt legible
    var SPRITE_PER = 1.4;
    SEGS.forEach(function (seg) {
      while (G.flow[seg.flow] >= SPRITE_PER) {
        G.flow[seg.flow] -= SPRITE_PER;
        if (DOM.cookies.length < 26) spawnCookie(seg);
      }
      // never let a starved counter build debt while paused
      if (G.flow[seg.flow] > 40) G.flow[seg.flow] = 40;
    });
    var speed = dt / 2.6;                    // one crossing takes ~2.6s
    for (var i = DOM.cookies.length - 1; i >= 0; i--) {
      var c = DOM.cookies[i];
      c.at += speed;
      if (c.at >= 1) {
        c.node.hidden = true;
        DOM.pool.push(c.node);
        DOM.cookies.splice(i, 1);
        continue;
      }
      var x = c.seg.from + (c.seg.to - c.seg.from) * c.at;
      if (REDUCED) {
        // stepped, not swept: quarter-belt hops
        x = c.seg.from + (c.seg.to - c.seg.from) * (Math.floor(c.at * 4) / 4);
      }
      c.node.style.left = x.toFixed(2) + "%";
    }
  }

  /* ---- paint ----------------------------------------------------------- */
  function fmt(v, dp) { return v == null ? "--" : v.toFixed(dp); }

  function paintStation(m) {
    var s = DOM.stations[m.id], t = tierOf(m);
    var shown = shownValue(m);
    s.tier.textContent = t.name;
    s.value.textContent = fmt(shown, m.spec.sensor.dp);
    s.value.classList.toggle("is-gone", shown == null);

    // the meter always draws the REAL band as a zone inside a padded range;
    // the needle draws what the sensor CLAIMS, so a lying sensor visibly
    // sits in a comfortable place while the truth walks
    var mi = meterInfo(m, shown);
    s.bandZone.style.left = (mi.zoneLeft * 100).toFixed(1) + "%";
    s.bandZone.style.width = (mi.zoneWidth * 100).toFixed(1) + "%";
    s.bandMark.style.left = (mi.pos * 100).toFixed(1) + "%";
    s.band.dataset.state = mi.state;
    s.bandTxt.textContent = "band " + t.lo + "-" + t.hi + " " + m.spec.sensor.unit +
      (mi.state === "edge" ? " \u00b7 near the edge" : mi.state === "out" ? " \u00b7 OUTSIDE" : "");
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

    /* the machine's physical tells - presentation of sim state, nothing more:
       a stopped machine's art dims and its working shimmer ends; the oven's
       porthole warmth follows whether it is actually baking in band */
    if (s.art) {
      s.art.classList.toggle("is-dead", m.stopped);
      s.art.classList.toggle("is-working", !m.stopped && G.running);
    }
    if (s.glow) {
      var baking = !m.stopped && m.real >= tierOf(m).lo && m.real <= tierOf(m).hi;
      s.glow.dataset.on = baking ? "1" : "0";
      // running hot-edge: the porthole flickers before anything burns -
      // decoration for a state the meter already words ("near the edge")
      s.glow.dataset.hot = (baking && tierOf(m).hi - m.real < (tierOf(m).hi - tierOf(m).lo) * 0.12) ? "1" : "0";
    }
    if (s.wrench) s.wrench.hidden = !(m.servicing > 0);
    if (s.art) s.art.classList.toggle("is-restarting", m.restarting > 0);

    /* The model slot rebuilds only when its CONTENT changes. Rebuilding it
       every frame re-creates the very buy button under the player's cursor,
       which eats the click - and leaks a new entry into the price registry
       twelve times a second. */
    var slotKey = [m.pico, m.auto, m.cond, G.nano, autonomyReach(),
      m.picoRead && (m.picoRead.kind + m.picoRead.said + m.picoRead.margin),
      m.nanoRead && m.nanoRead.kind, m.healthyDraws,
      m.lockout > 0, m.restarting > 0, m.inspecting > 0, m.inspected,
      !inBand(m, m.real), mi.state === "out",
      m.autoNote, m.sample && m.sample.record.node_id].join("~");
    if (s.slotKey !== slotKey) {
    s.slotKey = slotKey;
    s.slot.textContent = "";
    /* the dial hint LEADS the slot: whatever else is going on, a needle shown
       outside its band has a free fix, and the surface says so before it
       offers any maintenance verb */
    var hint = dialHint(m, shown);
    if (hint) {
      var hintEl = el("div", "cl-dialhint", hint.label);
      hintEl.dataset.dir = hint.dir;
      s.slot.appendChild(hintEl);
    }
    if (s.ctl) s.ctl.classList.toggle("is-urgent", !!hint);
    // what a finished INSPECT taught you, until the incident clears
    if (m.inspected && m.cond !== "none") {
      s.slot.appendChild(el("div", "cl-inspected", "INSPECTED: " + INSPECT_WORD[m.cond]));
    }
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
      /* PICO SPEAKS BY WHAT IT SAID, never by what the game knows. A recorded
         miss whose child said " none" during a fault renders exactly like
         health - a confident lie is the product truth, and colouring it warn
         would leak the very answer the model failed to give. The margin now
         rides every word ("steady \u00b7 3.0 sure"), and healthy windows
         redraw on a cadence, so the display is a live instrument instead of
         a dead label - which is what made Pico read as useless before. */
      if (!r) {
        head.appendChild(el("span", "cl-say__word", "steady"));
        head.dataset.verdict = "ok";
      } else if (r.kind === "unsure") {
        /* "only 1.4" told the founder nothing. Same pattern as the mesh deck:
           the number, what it is, and the bar it failed to clear. */
        head.appendChild(el("span", "cl-say__word",
          "not sure \u00b7 " + r.margin.toFixed(1) + " sure, needs " + FLOOR));
        head.dataset.verdict = "warn";
      } else if (r.said === "none") {
        head.appendChild(el("span", "cl-say__word", "steady \u00b7 " + r.margin.toFixed(1) + " sure"));
        head.dataset.verdict = "ok";
      } else {
        head.appendChild(el("span", "cl-say__word",
          "\u201c " + r.said + "\u201d \u00b7 " + r.margin.toFixed(1) + " sure"));
        // an alarm word is an alarm; on a healthy window it is the bench's
        // own recorded false alarm, replayed exactly as recorded
        head.dataset.verdict = m.cond !== "none" ? "bad" : "warn";
      }
      s.slot.appendChild(head);

      // the same word, said AT the machine: the bubble raises whenever Pico
      // is not confidently steady - alarms, doubts, recorded false alarms
      if (s.bubble) {
        var r2 = m.picoRead;
        var raised = !!r2 && (r2.kind === "unsure" || r2.said !== "none");
        if (raised) {
          s.bubble.hidden = false;
          s.bubble.textContent = r2.kind === "unsure" ? "not sure" : "\u201c " + r2.said + "\u201d";
          s.bubble.dataset.verdict = (r2.said !== "none" && m.cond !== "none") ? "bad" : "warn";
        } else {
          s.bubble.hidden = true;
        }
      }

      /* THE GATEWAY'S COUNSEL - one Nano serves every Pico on the line
         (founder-confirmed arrangement), so its advice appears per machine
         but is signed by the site gateway. On a fault it prescribes the
         ACTION per the maintenance doctrine; on a recorded false alarm it
         clears the air; on a clean out-of-band it says the words that save
         a wasted service: not a sensor fault, use the dial. */
      if (G.nano) {
        var advice = null, fixLine = null;
        if (m.cond !== "none" && m.nanoRead) {
          if (m.nanoRead.kind === "resolved") {
            advice = CONDITION_FIX[m.cond];
            if (!inBand(m, m.real)) {
              fixLine = m.id === "oven" ? "Then bring HEAT back inside the band."
                                        : "Then reduce SPEED - you are over the limit.";
            }
          } else {
            advice = "says \u201c " + m.nanoRead.said + "\u201d - the recorded senior got this one wrong too.";
          }
        } else if (m.cond === "none" && r && r.kind !== "unsure" && r.said !== "none" && m.nanoRead) {
          advice = m.nanoRead.kind === "resolved"
            ? "checked that alarm at the gateway - no real fault behind it. Carry on."
            : "the gateway read the same window and also called \u201c " + m.nanoRead.said +
              "\u201d - SERVICE to be sure.";
        } else if (m.cond === "none" && !inBand(m, m.real)) {
          advice = "that is not a sensor fault - the process is out of its band.";
          fixLine = m.real > tierOf(m).hi
            ? (m.id === "oven" ? "Bring HEAT down." : "Reduce SPEED.")
            : (m.id === "oven" ? "Bring HEAT up." : "Raise SPEED.");
        }
        if (advice) {
          var n = el("div", "cl-say cl-say--nano");
          var who = el("b", "cl-say__who", "WAVE NANO");
          who.appendChild(el("i", "cl-say__site", " \u00b7 site gateway"));
          n.appendChild(who);
          n.appendChild(el("span", "cl-say__why", advice));
          if (fixLine) n.appendChild(el("span", "cl-say__fix", fixLine));
          s.slot.appendChild(n);
        }
      }
      // provenance rides whichever record is actually on display
      var provSample = m.cond !== "none" ? m.sample : m.healthySample;
      if (provSample) {
        var prov = el("i", "cl-say__prov",
          "replayed record " + provSample.record.node_id +
          (provSample.sameKind ? "" : " (recorded on another instrument - the bench has no " +
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

    // the bought model is VISIBLY bolted on: the chip engraving, pico-edged.
    // An EMPTY mount is a dashed buy tag on the machine body - the floor
    // sells its own upgrades, not just the shop overlay.
    var mountKey = (m.pico ? "pico" : "empty") + "~" + (m.cond !== "none" && m.pico ? "lit" : "");
    if (s.mountKey !== mountKey) {
      s.mountKey = mountKey;
      s.mount.textContent = "";
      if (m.pico) {
        var badge = el("span", "clf-badge");
        badge.title = "Wave Pico, mounted on this machine";
        badge.appendChild(el("i", "clf-badge__art"));
        badge.appendChild(el("b", null, "PICO"));
        s.mount.appendChild(badge);
      } else {
        var tag = btn("+ PICO · " + MODEL_PRICE.pico, "clf-buytag", function (e) {
          e.stopPropagation();
          buyPico(m.id);
        });
        tag.title = "Mount a Wave Pico here - it tells you when this reading stops being trustworthy.";
        (DOM.priced = DOM.priced || []).push({ b: tag, cost: MODEL_PRICE.pico });
        s.mount.appendChild(tag);
      }
      if (s.bubble) s.bubble.hidden = !(m.pico && m.cond !== "none" && m.picoRead);
    }
    // the nameplate sells the next tier in place
    var nextTier = TIERS[m.id][m.tier + 1];
    if (s.plateBuy) {
      s.plateBuy.hidden = !nextTier;
      if (nextTier) {
        s.plateBuy.textContent = nextTier.name.toUpperCase() + " · " + nextTier.price;
        s.plateBuy.disabled = G.coins < nextTier.price;
        s.plateBuy.title = "Upgrade: faster, and a wider band (" + nextTier.lo + "-" + nextTier.hi +
          " " + m.spec.sensor.unit + ") so a small lie is less fatal.";
      }
    }

    /* maintenance buttons carry their countdowns; a lockout is worded, not
       just greyed, so the cost of guessing reads as a consequence. INSPECT
       stays live through a lockout - diagnosis is not maintenance, and a
       locked-out player should at least get to learn what it was. */
    var busy = m.restarting > 0 || m.servicing > 0 || m.inspecting > 0;
    if (m.lockout > 0) {
      var lockTxt = "LOCKED " + Math.ceil(m.lockout) + "s";
      [s.restart, s.clean, s.recal, s.service].forEach(function (b) {
        b.textContent = lockTxt;
        b.disabled = true;
      });
      s.inspect.textContent = m.inspecting > 0 ? "INSPECTING " + m.inspecting.toFixed(0) + "s" : "INSPECT";
      s.inspect.disabled = busy;
    } else {
      var running = m.restarting > 0 ? m.fixVerb : null;
      s.restart.textContent = running === "restart" ? "RESTARTING\u2026" : "RESTART";
      s.clean.textContent = running === "clean" ? "CLEANING\u2026" : "CLEAN";
      s.recal.textContent = running === "recal" ? "RECAL\u2026" : "RECAL \u00b7 " + VERBS.recal.cost;
      s.inspect.textContent = m.inspecting > 0 ? "INSPECTING " + Math.ceil(m.inspecting) + "s" : "INSPECT";
      s.service.textContent = m.servicing > 0 ? "SERVICING " + m.servicing.toFixed(1) + "s"
        : "SERVICE \u00b7 " + SERVICE_COST;
      [s.restart, s.clean, s.recal, s.inspect, s.service].forEach(function (b) { b.disabled = busy; });
    }
    s.service.classList.toggle("is-needed", !!told);
    /* when the fault kind is KNOWN - inspected, or Nano resolved it - the
       doctrine's verb lights up, so knowledge visibly becomes the answer */
    var known = m.cond !== "none" &&
      (m.inspected || (G.nano && m.nanoRead && m.nanoRead.kind === "resolved"));
    var rightVerb = known ? verbFor(m.cond) : null;
    s.restart.classList.toggle("is-doctrine", rightVerb === "restart");
    s.clean.classList.toggle("is-doctrine", rightVerb === "clean");
    s.recal.classList.toggle("is-doctrine", rightVerb === "recal");
    s.service.classList.toggle("is-doctrine", rightVerb === "service");
    var next = TIERS[m.id][m.tier + 1];
    s.upgrade.textContent = next ? "UPGRADE · " + next.price : "TOP TIER";
    s.upgrade.disabled = !next || G.coins < next.price;

  }

  /* THE GOALS. Two or three lines that always say what is worth doing next,
     and what the next thing you can buy will actually change. */
  function paintGoals() {
    var rows = [];
    var picos = G.machines.filter(function (m) { return m.pico; }).length;
    rows.push(["SHIP", Math.floor(G.cookies) + " / 100 cookies",
      G.cookies >= 100 ? "contract filled" : "keep every needle inside its marked band - conditions creep"]);
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
    return [G.nano, G.micro, G.giga, G.incidents.missed, G.ready, G.coins < 0,
      G.machines.map(function (m) {
        return [m.pico, m.auto, m.cond, m.tier, m.lockout > 0, m.restarting > 0,
          m.healthyDraws, m.picoRead && m.picoRead.kind, m.nanoRead && m.nanoRead.kind].join(",");
      }).join("|"),
      Math.floor(G.cookies) >= 100].join("~");
  }

  function paint() {
    if (!DOM.stations) return;
    DOM.coins.textContent = Math.floor(G.coins);
    // debt is a state you can SEE: red coins and the label says loan
    if (DOM.statEls && DOM.statEls.COINS) {
      DOM.statEls.COINS.classList.toggle("is-debt", G.coins < 0);
      DOM.statEls.COINS.lastChild.textContent = G.coins < 0 ? "ON LOAN" : "COINS";
    }
    if (DOM.best) {
      var bestShow = Math.max(G.bestRun, G.cleanRun);
      DOM.best.textContent = bestShow >= 60
        ? Math.floor(bestShow / 60) + "m" + String(Math.floor(bestShow % 60)).padStart(2, "0") + "s"
        : Math.floor(bestShow) + "s";
    }
    DOM.cookies2.textContent = Math.floor(G.cookies);
    var pk = machine("packer");
    DOM.rate.textContent = (pk.stopped ? 0 : rateOf(pk) * 1.5).toFixed(1);
    DOM.runBtn.textContent = G.running ? "PAUSE" : "START";
    DOM.runBtn.dataset.on = G.running ? "1" : "0";
    G.machines.forEach(paintStation);
    DOM.shipTxt.textContent = Math.floor(G.cookies) + " shipped";
    DOM.spoilTxt.textContent = G.spoiled > 1 ? Math.floor(G.spoiled) + " burnt" : "";
    // the crate stack grows with real shipments; capped so it stays a stack
    if (DOM.crates) {
      var want = Math.min(8, Math.floor(G.cookies / 20));
      if (DOM.crateCount !== want) {
        var grew = DOM.crateCount != null && want > DOM.crateCount;
        DOM.crateCount = want;
        // the crate-stack engraving reveals from the pallet up as real
        // shipments accumulate - a clip on the plate, stepped per crate
        DOM.crates.style.setProperty("--stack", (want / 8).toFixed(3));
        /* a crate's worth of cookies just paid out: say so where it landed.
           Chrome for arithmetic that already happened; skipped under
           reduced motion, where the coin count itself is the report. */
        if (grew && !REDUCED && DOM.cookieLayer) {
          var f = el("b", "clf-payout", "+" + (20 * 4));
          DOM.crates.appendChild(f);
          window.setTimeout(function () { if (f.parentNode) f.parentNode.removeChild(f); }, 1400);
        }
      }
    }
    // owned desk models sit at the desk end of the floor
    var deskKey = [G.nano, G.micro, G.giga].join("~");
    if (DOM.floorDesk && DOM.deskKey !== deskKey) {
      DOM.deskKey = deskKey;
      DOM.floorDesk.textContent = "";
      DOM.floorDesk.appendChild(el("i", "clf-desk__art"));
      var owned = [["nano", G.nano], ["micro", G.micro], ["giga", G.giga]]
        .filter(function (p) { return p[1]; });
      var rack = el("span", "clf-desk__rack");
      owned.forEach(function (p) {
        var chipEl = el("b", "clf-desk__chip", p[0].toUpperCase());
        chipEl.dataset.tier = p[0];
        rack.appendChild(chipEl);
      });
      // the desk sells its own next model, right where it would sit
      var nextDesk = !G.nano ? "nano" : !G.micro ? "micro" : !G.giga ? "giga" : null;
      if (nextDesk) {
        var deskTag = btn("+ " + nextDesk.toUpperCase() + " · " + MODEL_PRICE[nextDesk],
          "clf-buytag clf-buytag--desk", function (e) {
            e.stopPropagation();
            buyDesk(nextDesk);
          });
        deskTag.title = nextDesk === "nano"
          ? "The site gateway: explains WHY a reading is wrong and which verb fixes it."
          : nextDesk === "micro"
            ? "The site view - and it can hold one knob for you."
            : "The plant view - and it can run every knob.";
        (DOM.priced = DOM.priced || []).push({ b: deskTag, cost: MODEL_PRICE[nextDesk] });
        rack.appendChild(deskTag);
      }
      DOM.floorDesk.appendChild(rack);
      DOM.floorDesk.appendChild(el("span", "clf-desk__k", owned.length ? "THE DESK" : "DESK · EMPTY"));
    }

    var key = structureKey();
    if (key !== DOM.key) {
      DOM.key = key;
      DOM.priced = (DOM.priced || []).filter(function (p) { return p.b.isConnected; });
      paintGoals();
      if (DOM.shopOver && !DOM.shopOver.hidden) paintShop();
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
    updateCookies(dt);
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
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && DOM.shopOver && !DOM.shopOver.hidden) closeShop();
    });
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
      sampleWith: function (records, kind, truth, seed, excludeIds) {
        var save = G.records; G.records = records;
        var out = sampleFor(kind, truth, seed, excludeIds);
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
      restartWith: function (state, id) {
        var s = G; G = state; var ok = restart(id); G = s; return ok;
      },
      maintainWith: function (state, id, verb) {
        var s = G; G = state; var ok = maintain(id, verb); G = s; return ok;
      },
      inspectWith: function (state, id) {
        var s = G; G = state; var ok = inspect(id); G = s; return ok;
      },
      serviceWith: function (state, id) {
        var s = G; G = state; var ok = service(id); G = s; return ok;
      },
      dialHintWith: function (state, id, shown) {
        var s = G; G = state; var v = dialHint(machine(id), shown); G = s; return v;
      },
      meterWith: function (state, id, shown) {
        var s = G; G = state; var v = meterInfo(machine(id), shown); G = s; return v;
      },
      restartOdds: RESTART_ODDS,
      verbs: VERBS,
      verbFor: verbFor,
      inspectSecs: INSPECT_SECS,
      inspectWord: INSPECT_WORD,
      serviceCost: SERVICE_COST,
      lockoutSecs: LOCKOUT_SECS,
      healthyWindow: HEALTHY_WINDOW,
      conditionFix: CONDITION_FIX,
      flowWith: function (state, dt) {
        var s = G; G = state; step(dt); G = s; return state.flow;
      },
      segments: SEGS,
      /* dev/test window into the LIVE game. forceFault schedules a fault NOW
         through the exact same path the scheduler uses - same replay draw,
         same honesty - so screenshots and drives stop depending on the dice. */
      live: function () { return G; },
      forceFault: function (id, kind) {
        var m = machine(id);
        if (!m || m.cond !== "none") return false;
        startCondition(m, kind);
        paint();
        return true;
      },
      grant: function (n) { G.coins += n; paint(); },
    };
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
