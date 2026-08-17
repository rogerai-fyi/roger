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
  /* SERVICE PRICING (v26). The playtest proved the old numbers refuted the
     product: at 30 coins and 4s, the sure thing was also the cheap-fast
     thing, and a service-spamming player out-earned the diagnostician by
     hundreds of coins ("session C"). The sure thing is now the EXPENSIVE,
     SLOW thing - always available, still loans - so knowing the right verb
     (Nano's whole pitch) is worth real money. The informed-beats-spam claim
     is an executed test, because it IS the product claim. */
  var SERVICE_SECS = 10;         // the machine is down while the crew works
  var SERVICE_COST = 60;         // and the crew invoices, loan if needed
  var LOCKOUT_SECS = 60;         // the cost of guessing the wrong verb
  var GRACE_SECS = 45;           // the calm before the first, taught fault
  var LOAN_RATE = 3;             // shipping pays 3/cookie while on loan (else 4)

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
  /* NANO-DIRECT (v27). The founder asked whether a Nano alone should be
     enough - and the measured data says YES: parent-direct (the senior
     reading raw windows itself, no children) is the HIGHEST-accuracy config
     the bench recorded (macro recall 0.776, vs the mesh's 0.56 @1.5). So a
     lone Nano direct-watches - but the COST is the product argument, played
     straight: the gateway is one unit. It watches ONE machine at a time, its
     read arrives on a slow sweep instead of Pico's instant on-machine call,
     and while it is direct-watching an open fault it is not also clearing
     false alarms elsewhere. A Pico on the machine is instant, always-on, and
     frees the gateway - the mesh economics, told as gameplay. The sweep
     delay and the one-machine budget are game simulation of deployment
     reality; what Nano SAYS still rides the recorded parent fields. */
  var NANO_SWEEP_SECS = 8;       // the gateway's polling sweep, direct mode

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
      unsureRun: 0,                         // consecutive sub-floor healthy draws
      lockReason: "",                       // why the crew won't touch it
      pico: false,
      nanoDirect: false,                     // this read came off the patrol
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
      nanoWatch: null,   // direct-watch pin: a machine id, or null = patrol
      sweepAt: null, sweepLeft: null,   // the gateway's patrol position/clock
      humanSaves: 0, ackUntil: 0, ackWhat: "",
      running: true, ready: false, error: "",
      /* THE CONTRACT ARC. Filling one is a real moment (the win card), and
         the next one re-rolls harder: more cookies, faster creep. */
      contract: { target: 100, level: 1, creep: 1 },
      won: false, contractDone: false,
      /* THE OPENING (v26). freshState itself starts calm-less so the sim
         hooks and tests drive raw rules; the LIVE game calls beginRun(),
         which arms the grace period and the taught first fault. */
      graceLeft: 0, taught: true, taughtCleared: true,
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

  /* THE OPENING, armed only for a real run. The playtest's newcomer was hit
     by unexplained faults inside the first minute and learned exactly the
     wrong lesson (mash RESTART). A live run now starts with a calm grace
     period, then ONE scripted STUCK on the mixer, called out on the radio -
     the core lie, taught once, on a real recorded stuck window like every
     other fault. */
  function beginRun(state) {
    state.graceLeft = GRACE_SECS;
    state.taught = false;
    state.taughtCleared = false;
  }

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

  /* Where the gateway is pointing: the player's pin, or the PATROL position.
     The first cut of this auto-followed "the most recent fault" - which was
     an answer leak: no model had detected that fault yet, so the gateway was
     navigating by the game's secret. The patrol fixes it: in AUTO the
     gateway walks the bare machines on its sweep clock and DWELLS only where
     its own delivered read says trouble - belief-driven motion, no peeking.
     A machine with a Pico never needs the patrol; its reports are instant. */
  function bareMachines() {
    return G.machines.filter(function (m) { return !m.pico; });
  }
  function watchTarget() {
    if (!G.nano) return null;
    var pick = G.nanoWatch ? machine(G.nanoWatch) : null;
    if (pick && !pick.pico) return pick.id;
    var bare = bareMachines();
    if (!bare.length) return null;
    if (G.sweepAt && bare.some(function (m) { return m.id === G.sweepAt; })) return G.sweepAt;
    return bare[0].id;
  }

  /* Spread thin, felt: while the gateway BELIEVES its watch target is in
     trouble (its own delivered read said an alarm word - never the game's
     secret), it is not also second-reading healthy windows elsewhere. */
  function gatewayBusy() {
    var t = watchTarget();
    if (!t) return false;
    var m = machine(t);
    return !!m && m.cond !== "none" && !!m.nanoRead && m.nanoRead.said !== "none";
  }

  /* THE GATEWAY'S SWEEP, once per sim tick from step(). Every sweep period
     it reads the machine it is pointed at; a fault there gets its direct
     read (recorded parent fields - a recorded parent miss says " none" and
     the gateway walks on, honestly fooled); in AUTO it advances to the next
     bare machine unless its own read told it to stay. */
  function stepGateway(dt) {
    if (!G.nano) { G.sweepAt = null; G.sweepLeft = null; return; }
    var bare = bareMachines();
    if (!bare.length) { G.sweepAt = null; G.sweepLeft = null; return; }
    var cur = watchTarget();
    G.sweepAt = cur;
    if (G.sweepLeft == null) G.sweepLeft = NANO_SWEEP_SECS;
    G.sweepLeft -= dt;
    if (G.sweepLeft > 0) return;
    G.sweepLeft = NANO_SWEEP_SECS;
    var m = machine(cur);
    if (m.cond !== "none" && !m.nanoRead && m.sample) {
      m.nanoRead = nanoRead(m.sample);
      m.nanoDirect = true;
      addLog("Gateway sweep read the " + m.spec.name.toLowerCase() + " directly" +
        (m.nanoRead.kind === "resolved" ? " and named the fault." :
         " - and the recorded senior got this one wrong."));
    }
    // belief-driven dwell: stay only if the gateway's own read says trouble
    var believes = m.cond !== "none" && m.nanoRead && m.nanoRead.said !== "none";
    var pinned = G.nanoWatch && machine(G.nanoWatch) && !machine(G.nanoWatch).pico;
    if (!pinned && !believes) {
      var i = -1;
      for (var k = 0; k < bare.length; k++) if (bare[k].id === cur) i = k;
      G.sweepAt = bare[(i + 1) % bare.length].id;
    }
  }

  /* Did the recorded chain miss this one? True only when at least one model
     was actually watching AND none of them named the truth - the double-miss
     the founder hit ("even Wave Nano can't help"). Pure, so the lock runs it,
     and so the acknowledgment can never fire for a fault no model saw. */
  function chainMissed(m) {
    if (m.cond === "none") return false;
    var watched = false, called = false;
    if (m.picoRead) { watched = true; if (m.picoRead.kind === "caught") called = true; }
    if (m.nanoRead) { watched = true; if (m.nanoRead.kind === "resolved") called = true; }
    return watched && !called;
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
    var word = m.spec.control.label, c = m.spec.control;
    /* AT THE DIAL'S LIMIT the hint changes meaning instead of lying
       (playtest: needle pinned nineteen bands high with SPEED already at 1,
       hint still said "bring SPEED down"). A needle outside the band with
       the dial already at its stop is, by visible deduction alone, NOT a
       process problem - and saying so is the honest next lesson. */
    if (shown > t.hi && m.set <= c.min) {
      return { dir: "none", label: word + " is already at " + c.min +
        " - this is not a process problem. INSPECT the sensor, or ask the models." };
    }
    if (shown < t.lo && m.set >= c.max) {
      return { dir: "none", label: word + " is already at " + c.max +
        " - this is not a process problem. INSPECT the sensor, or ask the models." };
    }
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
    /* the gateway hears THROUGH the child instantly; with no child it can
       still get here, but only by DIRECT WATCH - one machine at a time, on
       the sweep delay (see stepMachine). The doctrine highlight stays honest
       either way: it lights only off knowledge that actually flowed - a
       child's instant report, the gateway's own delayed read, or INSPECT. */
    m.nanoRead = (G.nano && m.pico) ? nanoRead(m.sample) : null;
    m.nanoDirect = false;
    m.hadStop = false;
    G.incidents.open += 1;
    addLog(m.spec.name + " sensor went " + CONDITION_WORD[pick] + ".");
  }

  /* An incident that never stopped the line was CAUGHT in time; one that
     stopped it first was missed, whoever or whatever was watching. Under
     automation that distinction is the whole scoreboard: a replayed model
     miss still lets the line die, and the results panel has to show it. */
  /* PICO OWNS ITS MISS. A recorded miss renders as health while it lasts -
     that is the honesty rule - but once the truth surfaces (the fault is
     cleared, so the player knows), the model that called it wrong says so
     out loud. Recorded-miss honesty, turned into character instead of
     silent spam. Pure, so the lock can run it. */
  function ownsMiss(read) {
    if (!read || read.kind !== "wrong") return null;
    return "Pico: “I said " + read.said + " - I was wrong. That is what the record shows.”";
  }

  function clearCondition(m) {
    m.nextFault = 14 + rnd(m) * 30;
    if (m.cond !== "none") {
      G.incidents.open = Math.max(0, G.incidents.open - 1);
      if (m.hadStop) G.incidents.missed += 1; else G.incidents.caught += 1;
      var owned = ownsMiss(m.picoRead);
      if (owned && m.sample) {
        addLog(owned + " (replayed " + m.sample.record.node_id + ")");
      }
      /* THE ACKNOWLEDGMENT (v27). When the recorded chain missed - models
         watching, none named the truth - the fix could only have come from
         a person: automation acts on alarms, and a chain miss raises none.
         The founder hit exactly this and called it "even Wave Nano can't
         help"; the doctrine's last line is that the ladder ends with a
         person, and the one moment the player IS that person deserves to
         feel like one. Fires only on a genuine recorded chain miss. */
      if (chainMissed(m)) {
        G.humanSaves += 1;
        G.ackUntil = G.elapsed + 8;
        G.ackWhat = m.spec.name.toLowerCase();
        addLog("You caught what the recorded chain missed on the " +
          m.spec.name.toLowerCase() + " - the ladder ends with a person." +
          (m.sample ? " (replayed " + m.sample.record.node_id + ")" : ""));
      }
      // the taught first fault is done: the rest of the line may now fault
      if (G.taught && !G.taughtCleared && m.id === "mixer") G.taughtCleared = true;
    }
    m.cond = "none"; m.condAge = 0; m.sample = null;
    m.picoRead = null; m.nanoRead = null; m.driftLie = 0; m.hadStop = false;
    m.nanoDirect = false;
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
    // later contracts creep faster and lean harder - the re-roll's difficulty
    var creep = (G.contract && G.contract.creep) || 1;
    if (!m.event) {
      if (!m.eventLeft) m.eventLeft = (22 + rnd(m) * 34) / creep;
      m.eventLeft -= dt;
      if (m.eventLeft <= 0) {
        m.event = { t: 0, ramp: 12, hold: 10, decay: 9,
          mag: span * (0.14 + rnd(m) * 0.16) * (1 + (creep - 1) * 0.6) };
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
            /* the reason lives AT THE STATION, not only on the radio - a
               countdown without a why reads as breakage, not consequence.
               Naming what Nano would have said leaks the diagnosis, and that
               is deliberate: the lockout already cost a minute, and turning
               the punishment into the lesson is the point of it. */
            m.lockReason = "wrong verb - the crew won't touch it while it recovers. " +
              "(Nano would have said: " +
              (CONDITION_FIX[m.cond] || "").split(" - ").pop().toLowerCase() + ")";
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
          /* the one-unit budget, felt: a gateway direct-watching an open
             fault is not also second-reading healthy windows here, so a
             recorded false alarm stands unchecked until it is free again */
          m.nanoRead = (G.nano && !gatewayBusy()) ? nanoRead(m.healthySample) : null;
          /* the ×N run: consecutive sub-floor windows collapse into one line
             with a count, instead of a fresh chirp per redraw - the playtest
             called the old stream "a broken smoke detector" */
          m.unsureRun = (m.picoRead && m.picoRead.kind === "unsure") ? m.unsureRun + 1 : 0;
        }
      }
      /* A COUNTDOWN, not a per-tick coin flip. A rare random draw made the
         first fault land anywhere between ten seconds and never, depending on
         the seed - and a teaching loop that sometimes never starts is not a
         teaching loop. This schedules the next fault into a known window.

         PACING CANON (v26, from the playtest's death spiral): the scheduler
         GATES. During the grace period nothing faults; the first fault is the
         taught one, on the mixer, and nothing else faults until the player
         has cleared it; and while ANY machine is locked out, no machine draws
         a new fault - a lockout is a lesson, and piling fresh trouble on top
         of it turned one wrong verb into 294 straight seconds of dead line. */
      if (G.graceLeft > 0) {
        // the calm before the lesson - nothing schedules
      } else if (!G.taught) {
        if (m.id === "mixer") {
          startCondition(m, "stuck");
          G.taught = true;
          addLog("Radio: the mixer reads " + m.stuckAt.toFixed(2) + " " + m.spec.sensor.unit +
            " and has not moved in a while - does that seem right? " +
            "(INSPECT will tell you; a stuck sensor usually restarts clean.)");
        }
      } else if (!G.taughtCleared) {
        // one lesson at a time: the line waits for you to fix the first lie
      } else if (G.machines.some(function (x) { return x.lockout > 0; })) {
        // cascade cap: the countdown freezes while any machine is locked out
      } else {
        if (!m.nextFault) m.nextFault = 10 + rnd(m) * 26;
        m.nextFault -= dt;
        if (m.nextFault <= 0) { startCondition(m); m.nextFault = 0; }
      }
    } else {
      m.condAge += dt;
      /* the unwatched walk is CAPPED (playtest: a stopped mixer's needle
         climbed 8 -> 94 mm/s, nineteen bands past the edge, while the hint
         still said "bring SPEED down"). Out of band is out of band; a
         runaway number past all meaning is just noise that reads as spite. */
      var capSpan = (t.hi - t.lo) * 1.6;
      m.drift = Math.min(capSpan, m.drift + dt * (m.id === "oven" ? 1.6 : 0.30));
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
    stepGateway(dt);
    G.elapsed += dt;
    if (G.graceLeft > 0) G.graceLeft = Math.max(0, G.graceLeft - dt);
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
    /* THE DEBT TOOTH: a cookie sells for four - three while ON LOAN, because
       the crew takes its cut. Debt stays survivable (income still climbs the
       balance toward zero) but no longer plays identically to solvency. */
    G.coins += pack * (G.coins < 0 ? LOAN_RATE : 4);
    var rate = pack / Math.max(dt, 1e-6);
    G.peakRate = Math.max(G.peakRate, rate);

    /* THE CONTRACT FILLS - a real moment, not three lowercase words. The
       line pauses on the win card; the next contract re-rolls harder. */
    if (!G.won && G.cookies >= G.contract.target) {
      G.won = true;
      addLog("Contract filled: " + G.contract.target + " cookies shipped.");
      if (DOM.stations) showWin();
    }

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
    var wasWatched = watchTarget() === m.id;
    if (m.cond !== "none") {
      m.picoRead = picoRead(m.sample);
      m.nanoRead = G.nano ? nanoRead(m.sample) : null;   // the gateway hears its new child
    }
    else m.windowLeft = 0;               // read the first healthy window now
    m.nanoDirect = false;
    addLog("Wave Pico installed on the " + m.spec.name.toLowerCase() + "." +
      (wasWatched ? " The gateway is free to mind the rest of the site." : ""));
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
      addLog("Wave Nano online at the desk - instant through its Picos, and it " +
        "can direct-watch ONE bare machine at a time on a " + NANO_SWEEP_SECS + "s sweep.");
    } else {
      addLog("Wave " + which.charAt(0).toUpperCase() + which.slice(1) + " online at the desk.");
    }
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
    /* BURNT rides the HUD beside COOKIES: it is the loss state, not a
       footnote under the crates (playtest #10) */
    DOM.burnt = el("b", null, "0");
    DOM.statEls = {};
    [["COINS", DOM.coins], ["COOKIES", DOM.cookies2], ["BURNT", DOM.burnt],
     ["PER SEC", DOM.rate], ["BEST RUN", DOM.best]]
      .forEach(function (p) {
        var st = el("span", "cl-stat"); st.appendChild(p[1]);
        DOM.statEls[p[0]] = st;
        st.appendChild(el("i", null, p[0])); hud.appendChild(st);
      });
    /* debt is a FLAG on the coins stat, not a replacement for it: a player
       scanning for their money must always find the number where it lives */
    DOM.loanFlag = el("em", "cl-stat__flag", "ON LOAN");
    DOM.loanFlag.title = "shipping pays " + LOAN_RATE + "/cookie until the debt clears - the crew takes its cut";
    DOM.loanFlag.hidden = true;
    DOM.statEls.COINS.appendChild(DOM.loanFlag);
    DOM.shopBtn = btn("SHOP + UPGRADES", "cl-run cl-run--shop", openShop);
    hud.appendChild(DOM.shopBtn);
    DOM.runBtn = btn("PAUSE", "cl-run", toggleRun);
    hud.appendChild(DOM.runBtn);
    /* reset arms before it fires: one misclick sat next to PAUSE and wiped a
       forty-minute run (playtest bug 7). First press asks; it disarms itself. */
    DOM.resetBtn = btn("\u21bb", "cl-reset", function () {
      if (!DOM.resetArmed) {
        DOM.resetArmed = true;
        DOM.resetBtn.textContent = "RESET?";
        DOM.resetBtn.classList.add("is-armed");
        DOM.resetBtn.title = "press again to wipe this run";
        window.setTimeout(function () {
          DOM.resetArmed = false;
          if (DOM.resetBtn) {
            DOM.resetBtn.textContent = "\u21bb";
            DOM.resetBtn.classList.remove("is-armed");
          }
        }, 2600);
        return;
      }
      resetGame();
    });
    hud.appendChild(DOM.resetBtn);
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
      "If you pick a verb the card says is wrong and it fails, that machine's maintenance locks for " +
      LOCKOUT_SECS + "s. Nano quotes this card instantly; INSPECT learns it the slow way."));
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

    /* the win card: same overlay mechanics as the shop (the line waits) */
    DOM.winOver = el("div", "clf-shopover clf-winover");
    DOM.winOver.hidden = true;
    var winCard = el("div", "clf-shopcard clf-wincard");
    winCard.setAttribute("role", "dialog");
    winCard.setAttribute("aria-modal", "false");
    winCard.setAttribute("aria-label", "Contract filled");
    winCard.appendChild(el("b", "cl-win__stamp", "CONTRACT FILLED"));
    DOM.winStats = el("div", "cl-results cl-win__stats");
    winCard.appendChild(DOM.winStats);
    winCard.appendChild(el("i", "cl-view__note",
      "the run's own numbers, accumulated as you played - game arithmetic, not a model claim"));
    var winActs = el("div", "cl-win__acts");
    DOM.winNext = btn("NEXT CONTRACT", "cl-run", nextContract);
    winActs.appendChild(DOM.winNext);
    winActs.appendChild(btn("KEEP RUNNING THIS LINE", "cl-act", dismissWin));
    winCard.appendChild(winActs);
    DOM.winOver.appendChild(winCard);
    root.appendChild(DOM.winOver);

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

  /* ---- THE WIN - a contract fills, and the game says so ------------------
     The playtest shipped 263 cookies and the entire celebration was three
     lowercase words while the goal chip kept advertising a Pico. Now: the
     line pauses, the results land front-and-center, the crates get their
     stamp, and the next contract - bigger, faster creep - is one button. */
  function nextTargetOf(contract) { return Math.round(contract.target * 2.5 / 10) * 10; }

  function showWin() {
    if (!DOM.winOver) return;
    G.winWasRunning = G.running;
    G.running = false;
    DOM.winOver.hidden = false;
    if (DOM.crates) DOM.crates.classList.add("is-stamped");
    // the run's numbers, laid out like the results view: game arithmetic
    DOM.winStats.textContent = "";
    var up = G.runTime ? (G.upTime / G.runTime) * 100 : 100;
    [["SHIPPED", String(Math.floor(G.cookies))],
     ["BURNT", String(Math.floor(G.spoiled))],
     ["UPTIME", up.toFixed(0) + "%"],
     ["CAUGHT IN TIME", String(G.incidents.caught)],
     ["LINE STOPPED", String(G.incidents.missed)],
     [G.coins < 0 ? "ON LOAN" : "COINS", String(Math.floor(G.coins))],
    ].forEach(function (p) {
      var c = el("div", "cl-results__cell");
      c.appendChild(el("b", null, p[1]));
      c.appendChild(el("i", null, p[0]));
      DOM.winStats.appendChild(c);
    });
    DOM.winNext.textContent = "NEXT CONTRACT · " + nextTargetOf(G.contract) +
      " COOKIES · conditions creep faster";
    paint();
    DOM.winNext.focus();
  }

  function dismissWin() {
    if (!DOM.winOver || DOM.winOver.hidden) return;
    DOM.winOver.hidden = true;
    G.contractDone = true;            // the offer moves to the goals + shop
    if (G.winWasRunning) { G.running = true; lastT = 0; }
    paint();
  }

  function nextContract() {
    var next = nextTargetOf(G.contract);
    G.contract = { target: next, level: G.contract.level + 1, creep: G.contract.creep * 1.35 };
    G.won = false;
    G.contractDone = false;
    if (DOM.winOver) DOM.winOver.hidden = true;
    if (DOM.crates) DOM.crates.classList.remove("is-stamped");
    addLog("New contract: " + next + " cookies. Conditions creep faster now.");
    if (G.winWasRunning !== false) { G.running = true; lastT = 0; }
    paint();
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
    /* a dropped-out reading is LABELLED, not left as bare dashes (playtest
       bug 5: "--" with the band text still claiming OUTSIDE) */
    s.bandTxt.textContent = "band " + t.lo + "-" + t.hi + " " + m.spec.sensor.unit +
      (mi.state === "gone" ? " \u00b7 NO READING - the wire went quiet"
        : mi.state === "edge" ? " \u00b7 near the edge"
        : mi.state === "out" ? " \u00b7 OUTSIDE" : "");
    if (shown == null) s.value.title = "no reading - the sensor sent nothing this moment";
    else s.value.removeAttribute("title");
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
      G.sweepLeft == null ? "" : Math.ceil(G.sweepLeft), m.nanoDirect, watchTarget(),
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
    /* a lockout explains itself where it hurts - the countdown lives on the
       buttons, the WHY lives here, so punishment reads as consequence */
    if (m.lockout > 0 && m.lockReason) {
      s.slot.appendChild(el("div", "cl-lockwhy", m.lockReason));
    }
    var r = m.picoRead;
    if (!m.pico) {
      var b = btn("+ WAVE PICO · " + MODEL_PRICE.pico, "cl-slot__buy", function () { buyPico(m.id); });
      (DOM.priced = DOM.priced || []).push({ b: b, cost: MODEL_PRICE.pico });
      b.title = "A model on this machine tells you when the reading stops being trustworthy.";
      s.slot.appendChild(b);
      s.slot.appendChild(el("i", "cl-slot__hint", G.nano
        ? "no Pico · the gateway covers this machine one sweep at a time"
        : "no model · you are reading this dial yourself"));
    } else {
      var head = el("div", "cl-say cl-say--pico");
      head.appendChild(el("b", "cl-say__who", "WAVE PICO"));
      /* PICO SPEAKS BY WHAT IT SAID, never by what the game knows. A recorded
         miss whose child said " none" during a fault renders exactly like
         health - a confident lie is the product truth, and colouring it warn
         would leak the very answer the model failed to give.
         THE MARGIN IS A METER now, not a number pair (playtest: "0.9
         sure...out of what?"). The bar is the confidence, the tick is the
         floor it must clear to speak alone, and the exact figures ride the
         tooltip. A run of sub-floor windows collapses into one line with a
         count instead of a fresh chirp per redraw. */
      function marginMeter(margin) {
        var wrap = el("i", "cl-margin");
        wrap.title = margin.toFixed(1) + " sure, needs " + FLOOR +
          " to speak alone - the bar is confidence, the tick is the line it must clear";
        var fill = el("b", "cl-margin__fill");
        fill.style.width = Math.round(Math.min(1, margin / 4) * 100) + "%";
        wrap.appendChild(fill);
        var tick = el("s", "cl-margin__tick");
        tick.style.left = Math.round((FLOOR / 4) * 100) + "%";
        wrap.appendChild(tick);
        return wrap;
      }
      if (!r) {
        head.appendChild(el("span", "cl-say__word", "steady"));
        head.dataset.verdict = "ok";
      } else if (r.kind === "unsure") {
        head.appendChild(el("span", "cl-say__word",
          "not sure" + (m.unsureRun > 1 ? " \u00d7" + m.unsureRun : "")));
        head.appendChild(marginMeter(r.margin));
        head.dataset.verdict = "warn";
      } else if (r.said === "none") {
        head.appendChild(el("span", "cl-say__word", "steady"));
        head.appendChild(marginMeter(r.margin));
        head.dataset.verdict = "ok";
      } else {
        head.appendChild(el("span", "cl-say__word", "\u201c" + r.said + "\u201d"));
        head.appendChild(marginMeter(r.margin));
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
          s.bubble.textContent = r2.kind === "unsure" ? "not sure" : "\u201c" + r2.said + "\u201d";
          s.bubble.dataset.verdict = (r2.said !== "none" && m.cond !== "none") ? "bad" : "warn";
        } else {
          s.bubble.hidden = true;
        }
      }

    }

      /* THE GATEWAY'S COUNSEL - one Nano serves every Pico on the line
         (founder-confirmed arrangement), so its advice appears per machine
         but is signed by the site gateway. On a fault it prescribes the
         ACTION per the maintenance doctrine; on a recorded false alarm it
         clears the air; on a clean out-of-band it says the words that save
         a wasted service: not a sensor fault, use the dial. */
      if (G.nano) {
        var advice = null, fixLine = null, sign = " \u00b7 site gateway";
        if (m.cond !== "none" && m.nanoRead) {
          if (m.nanoDirect) sign = " \u00b7 direct watch";
          if (m.nanoRead.kind === "resolved") {
            advice = CONDITION_FIX[m.cond];
            if (!inBand(m, m.real)) {
              fixLine = m.id === "oven" ? "Then bring HEAT back inside the band."
                                        : "Then reduce SPEED - you are over the limit.";
            }
          } else {
            /* THE DOUBLE-MISS HANDS OFF TO THE HUMAN (v27). The founder hit
               this exact wall ("even Wave Nano can't help") and honesty
               without a next move is a dead end. The doctrine has a last
               line, so the advice completes it - and INSPECT lights below,
               the way known-kind lights a verb. */
            advice = "says \u201c" + m.nanoRead.said + "\u201d - " +
              (m.nanoDirect ? "wrong, per the recording."
                            : "the recorded senior got this one wrong too.") +
              " Both models missed this one in the recording. INSPECT it " +
              "yourself - the ladder ends with a person.";
          }
        } else if (!m.pico && watchTarget() === m.id && G.sweepLeft != null && !m.nanoRead) {
          /* the patrol line rides the CLOCK, not the fault - it shows on the
             watched machine whether or not anything is wrong, so its mere
             presence can never leak what no model has read yet */
          advice = "gateway watch \u00b7 next sweep in " + Math.max(1, Math.ceil(G.sweepLeft)) + "s.";
          sign = " \u00b7 direct watch";
        } else if (m.cond === "none" && r && r.kind !== "unsure" && r.said !== "none" && m.nanoRead) {
          advice = m.nanoRead.kind === "resolved"
            ? "checked that alarm at the gateway - no real fault behind it. Carry on."
            : "the gateway read the same window and also called \u201c" + m.nanoRead.said +
              "\u201d - SERVICE to be sure.";
        } else if (m.cond === "none" && !inBand(m, m.real)) {
          advice = "that is not a sensor fault - the process is out of its band.";
          fixLine = m.real > tierOf(m).hi
            ? (m.id === "oven" ? "Bring HEAT down." : "Reduce SPEED.")
            : (m.id === "oven" ? "Bring HEAT up." : "Raise SPEED.");
        }
        if (advice) {
          var n = el("div", "cl-say cl-say--nano");
          if (sign === " \u00b7 direct watch") n.classList.add("cl-say--direct");
          var who = el("b", "cl-say__who", "WAVE NANO");
          who.appendChild(el("i", "cl-say__site", sign));
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
        tag.title = "Mount a Wave Pico here - instant, always-on coverage for this machine, " +
          "and it frees the gateway to mind the rest of the site.";
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
    /* the double-miss hands to the person: when every watching model got it
       wrong per the recording, INSPECT is the doctrine's answer, and it
       lights exactly the way a known-kind verb does */
    s.inspect.classList.toggle("is-doctrine", chainMissed(m) && !m.inspected);
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
    var target = G.contract.target;
    var filled = G.cookies >= target;
    rows.push(["SHIP", Math.floor(G.cookies) + " / " + target + " cookies" +
      (G.contract.level > 1 ? " · contract " + G.contract.level : ""),
      filled ? "contract filled" : "keep every needle inside its marked band - conditions creep"]);
    /* the opening, said where the player is looking */
    if (G.graceLeft > 0) {
      rows.push(["WATCH", "the line is settling in",
        "learn the dials - keep every needle in its band. The first trouble is coming."]);
    } else if (G.taught && !G.taughtCleared) {
      var mx0 = machine("mixer");
      rows.push(["WATCH", "the mixer reads " + (mx0.cond === "stuck" ? mx0.stuckAt.toFixed(2) : "steady") +
        " and has not moved", "does that seem right? INSPECT it - or a stuck sensor usually restarts clean"]);
    }
    /* the human-catch beat: for a few seconds after you fix what the
       recorded chain missed, the chip says so - the doctrine's best moment */
    if (G.elapsed < G.ackUntil) {
      rows.push(["NICE", "you caught what the recorded chain missed on the " + G.ackWhat,
        "the ladder ends with a person"]);
    } else {
      var handedOffTo = G.machines.filter(function (mm) { return chainMissed(mm) && !mm.inspected; });
      if (handedOffTo.length) {
        rows.push(["WATCH", "both models missed the " + handedOffTo[0].spec.name.toLowerCase(),
          "INSPECT it yourself - the ladder ends with a person"]);
      }
    }
    if (filled) {
      /* the win stops advertising Picos (playtest bug 1) - the next thing
         is the next contract, offered on the win card and again here */
      rows.push(["NEXT", "NEW CONTRACT · " + nextTargetOf(G.contract) + " cookies",
        "take it from the results card or the shop - conditions creep faster"]);
    } else if (!picos) {
      rows.push(["NEXT", "WAVE PICO · " + MODEL_PRICE.pico,
        "puts a model on one machine so it tells you when its reading stops being trustworthy"]);
    } else if (!G.nano) {
      rows.push(["NEXT", "WAVE NANO · " + MODEL_PRICE.nano,
        "explains WHY and what to do - instant through Picos, or direct-watching one bare machine itself"]);
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
    [["nano", "WAVE NANO", "tells you WHY, and what to change - instant through Picos, or direct-watching one bare machine on its own sweep", "explains the fault and gives the fix; no Picos needed to start"],
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

    /* a filled contract's re-roll is buyable here too, for the player who
       dismissed the win card and kept running */
    if (G.won && G.contractDone) {
      var col4 = el("div", "cl-branch");
      col4.appendChild(el("span", "cl-branch__head", "THE OFFICE"));
      col4.appendChild(node({
        name: "NEXT CONTRACT",
        owned: false,
        promise: nextTargetOf(G.contract) + " cookies - conditions creep faster",
        price: "TAKE IT",
        cost: 0,
        buy: function () { nextContract(); closeShop(); },
      }));
      DOM.shop.appendChild(col4);
    }
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
    /* THE GATEWAY'S STATION (v27): with Nano owned, the desk shows where its
       one direct watch is pointed and lets the player point it. Machines
       with a Pico never appear - their reports arrive instantly - which is
       itself the sales pitch, printed as the why line. */
    if (G.nano) {
      var gw = el("div", "cl-view cl-view--nano");
      gw.appendChild(el("b", "cl-view__head", "WAVE NANO · SITE GATEWAY"));
      var sel = el("div", "cl-watchsel");
      sel.setAttribute("role", "group");
      sel.setAttribute("aria-label", "Gateway direct watch");
      sel.appendChild(el("i", null, "WATCHING"));
      var autoB = btn("AUTO", "cl-watch" + (G.nanoWatch == null ? " is-on" : ""), function () {
        G.nanoWatch = null; addLog("Gateway watch set to AUTO - it follows the newest fault."); paint();
      });
      autoB.setAttribute("aria-pressed", G.nanoWatch == null ? "true" : "false");
      sel.appendChild(autoB);
      G.machines.forEach(function (mm) {
        var wb = btn(mm.spec.name, "cl-watch" + (G.nanoWatch === mm.id ? " is-on" : ""), function () {
          G.nanoWatch = mm.id;
          addLog("Gateway pointed at the " + mm.spec.name.toLowerCase() + ".");
          paint();
        });
        wb.setAttribute("aria-pressed", G.nanoWatch === mm.id ? "true" : "false");
        if (mm.pico) {
          wb.disabled = true;
          wb.title = "This machine has a Pico - its reports reach the gateway instantly.";
        }
        sel.appendChild(wb);
      });
      gw.appendChild(sel);
      var tgt = watchTarget();
      gw.appendChild(el("span", null, tgt
        ? (G.nanoWatch ? "Pinned on the " : "Patrolling - now at the ") +
          machine(tgt).spec.name.toLowerCase() +
          (G.sweepLeft != null ? " \u00b7 next sweep " + Math.max(1, Math.ceil(G.sweepLeft)) + "s" : "") +
          (gatewayBusy() ? " \u00b7 dwelling on trouble it read itself - checks elsewhere wait" : "")
        : "Every machine has a Pico - the gateway hears them all instantly."));
      gw.appendChild(el("i", "cl-view__note",
        "the senior can read anything, but it cannot be everywhere - that is what the children are for"));
      DOM.deskView.appendChild(gw);
    }
    if (!G.micro && !G.giga) {
      if (!G.nano) DOM.deskView.appendChild(el("p", "cl-deskview__empty",
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
      G.cookies >= G.contract.target, G.contract.level, G.won, G.contractDone,
      G.graceLeft > 0, G.taught, G.taughtCleared].join("~");
  }

  function paint() {
    if (!DOM.stations) return;
    DOM.coins.textContent = Math.floor(G.coins);
    // debt is a state you can SEE: red coins plus a flag - the COINS label
    // itself never vanishes (playtest: "players scanning for money see their
    // stat gone")
    if (DOM.statEls && DOM.statEls.COINS) {
      DOM.statEls.COINS.classList.toggle("is-debt", G.coins < 0);
      if (DOM.loanFlag) DOM.loanFlag.hidden = G.coins >= 0;
    }
    if (DOM.burnt) {
      DOM.burnt.textContent = Math.floor(G.spoiled);
      DOM.statEls.BURNT.classList.toggle("is-burnt", G.spoiled >= 1);
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
    /* a burnt cookie FALLS OFF THE BELT at the oven - the loss state gets a
       moment, not a footnote. Chrome for arithmetic that already happened;
       under reduced motion the HUD tally tick is the report. */
    if (DOM.cookieLayer) {
      var burntNow = Math.floor(G.spoiled);
      if (DOM.burntShown == null) DOM.burntShown = burntNow;
      if (burntNow > DOM.burntShown) {
        DOM.burntShown = burntNow;
        if (!REDUCED) {
          var bc = el("i", "clf-cookie clf-cookie--burnt");
          bc.innerHTML = COOKIE_SVGS.cookie;
          bc.style.left = "58%";
          DOM.cookieLayer.appendChild(bc);
          window.setTimeout(function () { if (bc.parentNode) bc.parentNode.removeChild(bc); }, 1300);
        }
      }
    }
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
    beginRun(G);                 // a real run always opens with the lesson
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
    beginRun(G);                 // the live game opens calm, then teaches
    buildShell(host);
    paint();
    loadRecords();
    if (window.requestAnimationFrame) raf = window.requestAnimationFrame(frame);
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      if (DOM.shopOver && !DOM.shopOver.hidden) closeShop();
      else if (DOM.winOver && !DOM.winOver.hidden) dismissWin();
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
      serviceSecs: SERVICE_SECS,
      lockoutSecs: LOCKOUT_SECS,
      healthyWindow: HEALTHY_WINDOW,
      conditionFix: CONDITION_FIX,
      graceSecs: GRACE_SECS,
      loanRate: LOAN_RATE,
      ownsMiss: ownsMiss,
      chainMissed: chainMissed,
      nanoSweepSecs: NANO_SWEEP_SECS,
      watchWith: function (state) { var s2 = G; G = state; var v = watchTarget(); G = s2; return v; },
      busyWith: function (state) { var s2 = G; G = state; var v = gatewayBusy(); G = s2; return v; },
      buyPicoWith: function (state, id) { var s2 = G; G = state; var ok = buyPico(id); G = s2; return ok; },
      begin: beginRun,
      nextTargetOf: nextTargetOf,
      conditionWith: function (state, id, kind) {
        var s = G; G = state; startCondition(machine(id), kind); G = s; return state;
      },
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
