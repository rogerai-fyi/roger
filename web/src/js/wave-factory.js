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

  /* GIGA PRICING (v32, founder: "lets make Wave Giga cheaper to obtain,
     maybe just 100 more than Micro"). Giga is the plant's one coordinated
     mind AND the Unit AND auto-inspection - the endgame purchase should be
     reachable, not a grind wall. Micro stays the budget stepping stone. */
  var MODEL_PRICE = { pico: 50, nano: 120, micro: 260, giga: 360 };

  /* MK BALANCE CANON (v32, founder: "upgraded machinery should fail less
     or take less time to inspect/restart etc... fully upgraded machinery
     should take less human need"). Better iron IS more reliable and easier
     to work on: per-Mk multipliers on how often a sensor faults and how
     long every maintenance verb takes (inspect, restart, clean, recal,
     service). Mk I is the baseline the early game teaches on; contract 1
     is all Mk I, so the taught opening is untouched. Surfaced in the shop
     rows and the maintenance card - a buyer should know what reliability
     they are buying. */
  /* The Mk III interval multiplier was tuned BY MEASUREMENT at the v32 bar,
     which was a THREE-minute proof window: at x1.7 and x3.0 no honest uptime
     bar cleared a majority of those windows, and x4.0 did on every seed while
     an all-Mk-I plant failed every one.

     STALE JUSTIFICATION, CORRECTED 2026-08-18. The founder lowered the window
     to two minutes (v36), so that sentence no longer measured the bar it
     justifies. Re-measured at the shipped 120s window, 80% uptime, three
     seeds: x1.7 clears 13/9/4% of windows, x3.0 clears 60/74/76%, x4.0
     clears 68/75/48% - and an all-Mk-I plant still clears 0% at every
     setting. So the FLOOR the constant exists to hold is intact (Mk III is
     winnable, Mk I is not), but "x4.0 is where a majority clears on every
     seed" is no longer true of x4.0 alone - x3.0 now clears a majority on
     all three seeds and x4.0 misses on one. The constant is founder balance
     canon and is NOT retuned here; only the claim about it is corrected. */
  var MK_FAULT_MULT = [1.0, 1.5, 4.0];   // fault interval multiplier per Mk
  var MK_VERB_MULT = [1.0, 0.8, 0.6];    // verb duration multiplier per Mk
  function mkFaultMult(m) { return MK_FAULT_MULT[m.tier] || 1; }
  function mkVerbMult(m) { return MK_VERB_MULT[m.tier] || 1; }
  function verbSecsFor(m, secs) { return Math.round(secs * mkVerbMult(m) * 10) / 10; }
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
  /* v35: MICRO'S REMOTE DIAGNOSTIC - the site brain runs the same look a
     person gets, remotely. Slower (no body on the floor) and it spins up
     only after a stop has stood a moment, so a person on the spot - or the
     Unit's walk - still beats it. */
  var MICRO_DIAG_MULT = 2;
  var MICRO_DIAG_SPINUP = 4;

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
  /* v31: continuous, not instantaneous (vs Micro's 4s look). v32: raised
     1.8 -> 2.8 by the proof-run bots - with the tightened no-answer wire
     a lying sensor can't exploit the faster budget, and process creep was
     out-running the old one on a hands-off plant. */
  var GIGA_STEPS_PER_SEC = 2.8;
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
  /* THE GIGA UNIT (v30). Floor anchors as left-% (must agree with the CSS
     machine positions), and the plant radio's one outside line: the same
     concierge endpoint the mesh deck talks to. Prose framing is the lesson
     learned there - a machine-tagged dump reads as off-topic to Ping's
     guardrail and draws a decline; the same numbers as product prose get a
     real answer (verified live against production before this shipped). */
  /* anchors sit BESIDE their machines (mixer spans ~6-19%, oven ~38-55%,
     packer ~66-79%), so the Unit stands next to the equipment it inspects
     rather than inside the engraving */
  var UNIT_POS = { mixer: 21, oven: 57, packer: 61, desk: 84 };
  var PING_URL = "https://broker.rogerai.fm/concierge";
  /* THE CERTIFICATE (v28) - the campaign goal the founder asked for: a fully
     automated, fully upgraded factory. The last item is the PROOF: the plant
     runs hands-off for a stretch while its own dashboard records it. */
  var CERT_PROOF_SECS = 120;     /* two untouched minutes... (v35: the founder
     lowered the stretch from three - the bar's uptime stays measured) */
  /* ...at EIGHTY percent uptime or better. v32, measured not vibed - but note
     the figures below were measured against the THREE-minute window of that
     round; the shipped window is two (v36). What re-verifies the bar at its
     current value is the executed lock "the proof run is winnable", which
     reads certProofSecs rather than a hardcoded 180 and still shows a full
     Mk III plant clearing a majority while an Mk I plant clears none.
     The original v32 measurement, for the record: the fixed-seed proof-run
     bots drove a fully-upgraded fully-automated plant hands-off for 15 sim-
     minutes across 3 seeds. At 90% only 4-32% of sliding 3-minute windows
     passed - the founder's exact complaint ("hard to get 3 minutes at
     90%+ is the only thing missing") was true BY CONSTRUCTION. 80% is the
     highest 5%-step a majority of windows clears on every seed (67/86/79%)
     while an un-upgraded Mk I automated plant still fails every window.
     Recorded model misses, verb time and the Unit's walking are all real
     costs; the bar honors them instead of pretending. */
  var CERT_UPTIME = 0.8;

  var TIER_COLOUR = { pico: "pico", nano: "nano", micro: "micro", giga: "giga" };
  /* the ONE build string every export stamps - the tape header shipped a
     stale hardcoded "v29" for three rounds (playtest round 3 caught it) */
  var GAME_BUILD = "playbox v36";
  /* v34: the proof-window touch penalty, named so the goal card's hint and
     touchPlant() can never disagree about the rule */
  var CERT_TOUCH_SETBACK = 20;

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
      picoCatches: 0, catchAt: 0,           // the badge's tally of caught faults
      upT: 0, runT: 0, lastHead: null,      // per-machine uptime + headroom trend
      nanoDirect: false,                     // this read came off the patrol
      auto: false,                          // the models turn this knob
      autoNote: "", autoNoteAt: 0, autoTier: "", hadStop: false, verbTries: 0,
      unitJob: null, unitInspecting: false, unitFixed: false, wordBurned: false,
      /* v31 control doctrine: the automation's own trust bookkeeping.
         lastTrustedSet = the dial position last seen with a trusted,
         in-band reading (where a hold restores to); chaseFrom/chaseErr =
         where a push started and how bad the error was, so "I turned the
         knob and the needle didn't answer" is a deduction the automation
         can make; senseSuspect = that deduction, made; heldForFlag = the
         hold announced once, not every frame; gigaGas = Giga's bounded
         step budget (continuous, not instantaneous). */
      lastTrustedSet: null, chaseFrom: null, chaseErr: null,
      senseSuspect: false, heldForFlag: false, gigaGas: 0,
      buffer: 0,
      stopped: false, stoppedFor: 0, downFor: 0,
      spec: spec,
    };
  }

  function freshState() {
    return {
      coins: 120, cookies: 0, spoiled: 0, elapsed: 0,
      machines: MACHINES.map(freshMachine),
      /* micro is a COUNT: the founder's scale-out-vs-scale-up decision.
         Up to three Micros, each able to hold ONE dial - three of them is
         full coverage by quantity (780 coins of it), while one Giga (360,
         v32: Micro + 100) is full coverage by coordination - plus the Unit
         on the floor and its auto-inspection. A single Micro is the budget
         stepping stone; Giga is plainly the value play at scale. */
      nano: false, micro: 0, giga: false,
      nanoWatch: null,   // direct-watch pin: a machine id, or null = patrol
      sweepAt: null, sweepLeft: null,   // the gateway's patrol position/clock
      humanSaves: 0, ackUntil: 0, ackWhat: "",
      running: true, ready: false, error: "",
      /* THE CONTRACT ARC. Filling one is a real moment (the win card), and
         the next one re-rolls harder: more cookies, faster creep. */
      contract: { target: 100, level: 1, creep: 1, sla: 0 },
      won: false, contractDone: false,
      /* THE SLA (v29, staged from playtest round 2): from contract 2 up the
         buyer expects the line UP. sla is the uptime bar (0 = no stake);
         slaClock is the rolling minute the bar is judged over. */
      slaClock: { run: 0, up: 0 }, contractsLost: 0,
      firstPicoAt: null, tapeCoinsAt: 0,
      diag: { first: 0, total: 0 },
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
      modelCalled: 0,           // faults a model named before the fix landed
      /* the campaign goal: gear checklist plus the hands-off proof window.
         run/up are the proof clock; any hand on the plant resets them. */
      cert: { run: 0, up: 0, done: false, doneAt: 0 },
      history: [], sampleAt: 0, upTime: 0, runTime: 0,
      deskTab: "site",
      /* THE GIGA UNIT: where the plant's visible mind stands, where it is
         rolling, what it is saying. Spawned properly by buyDesk("giga");
         present in every state so the sim hooks can drive it. */
      unit: { at: "desk", going: null, travelLeft: 0, pauseLeft: 4, dir: 1,
              pose: "idle", say: "", sayUntil: 0, topic: 0 },
      chat: [], chatBusy: false,
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
      addLog("Gateway swung by the " + m.spec.name.toLowerCase() +
        (m.nanoRead.kind === "resolved" ? " and named the fault." :
         " - and per the recording, the senior read it wrong."));
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
    /* A DELIVERED VERDICT SILENCES THE PROCESS HINT (playtest round 2: the
       "bring SPEED down" line stood next to an INSPECT verdict saying only
       service fixes it). Once anything has NAMED the fault - INSPECT, a
       Pico call, the gateway - the coaching yields to the verdict. */
    if (m.cond !== "none" && (m.inspected || m.nanoRead ||
        (m.picoRead && m.picoRead.kind !== "unsure" && m.picoRead.said !== "none"))) {
      return null;
    }
    /* AN ABSURD READING IS A CONFESSION, not a process problem (playtest:
       the oven read -13.6° and the hint said "bring HEAT up"). Nothing on
       this line reads below zero, so a negative needle is the sensor
       talking, and the hint says exactly that. */
    if (shown < 0) {
      return { dir: "none", label: "no " + m.spec.name.toLowerCase() + " reads " +
        shown.toFixed(1) + " - that number is the sensor talking, not the room. " +
        "INSPECT it, or ask the models." };
    }
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
    m.verbTries = 0;
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
    /* v32 (founder screenshot: a "ran RESTART on Nano's advice" tag from a
       PREVIOUS incident still painted beside a fresh double-miss): a new
       incident invalidates the old story - the machine card's persistent
       automation line must never narrate the wrong fault. */
    m.autoNote = ""; m.autoNoteAt = 0; m.autoTier = "";
    m.unitJob = null; m.unitInspecting = false; m.unitFixed = false; m.inspectedBy = null; m.microDiag = false;
    m.autoLookBy = null;
    m.wordBurned = false;
    G.incidents.open += 1;
    tape("fault", { m: m.id, kind: pick,
      record: m.sample ? m.sample.record.node_id : null,
      picoSaid: m.picoRead ? m.picoRead.said : null,
      nanoHeard: m.nanoRead ? m.nanoRead.kind : null });
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
    return "Pico: “I said " + read.said + " - I was wrong. Same miss it made in the recording.”";
  }

  function clearCondition(m) {
    m.nextFault = (14 + rnd(m) * 30) * mkFaultMult(m);   // better iron faults less (v32)
    if (m.cond !== "none") {
      G.incidents.open = Math.max(0, G.incidents.open - 1);
      if (m.hadStop) G.incidents.missed += 1; else G.incidents.caught += 1;
      /* the vigilance tallies: a fault a model NAMED before the fix landed.
         Pico's own catches ride its badge; the HUD counts either model. */
      var named = (m.picoRead && m.picoRead.kind === "caught") ||
                  (m.nanoRead && m.nanoRead.kind === "resolved");
      if (named) G.modelCalled += 1;
      if (m.picoRead && m.picoRead.kind === "caught") {
        m.picoCatches += 1; m.catchAt = G.elapsed;
      }
      var owned = ownsMiss(m.picoRead);
      if (owned && m.sample) {
        addLog(owned + " (replayed " + m.sample.record.node_id + ")");
      }
      /* THE ACKNOWLEDGMENT (v27, re-voiced v28). When the recorded chain
         missed - models watching, none named the truth - the fix could only
         have come from a person: automation acts on alarms, and a chain miss
         raises none. The founder hit exactly this ("even Wave Nano can't
         help"), and the one moment the player beats the models deserves to
         feel like one. Fires only on a genuine recorded chain miss - and
         v32: NEVER for a save the Unit's auto-inspection set up. The robot
         finding what the models missed is plant maintenance, not a medal. */
      if (chainMissed(m) && m.unitFixed) {
        /* v40: say WHO. m.unitFixed is set by the Unit's walk AND by
           Micro's remote diagnostic, and this line hard-coded "The Unit" -
           so a Micro-only plant, which has no robot on the floor at all,
           was told a robot it never bought had saved it. autoLookBy is
           written by whichever automation actually took the look. */
        tape("unit-save", { m: m.id, by: m.autoLookBy || "the Unit",
          record: m.sample ? m.sample.record.node_id : null });
        addLog((m.autoLookBy || "The Unit's inspection") +
          " caught what both models missed on the " +
          m.spec.name.toLowerCase() + " - still a recorded miss on the books.");
      } else if (chainMissed(m)) {
        G.humanSaves += 1;
        tape("human-save", { m: m.id, record: m.sample ? m.sample.record.node_id : null });
        G.ackUntil = G.elapsed + 8;
        G.ackWhat = m.spec.name.toLowerCase();
        addLog("You caught what both models missed on the " +
          m.spec.name.toLowerCase() + ". Nice save - some reads just need a person." +
          (m.sample ? " (replayed " + m.sample.record.node_id + ")" : ""));
      }
      // the taught first fault is done: the rest of the line may now fault
      if (G.taught && !G.taughtCleared && m.id === "mixer") G.taughtCleared = true;
    }
    m.cond = "none"; m.condAge = 0; m.sample = null;
    m.picoRead = null; m.nanoRead = null; m.driftLie = 0; m.hadStop = false;
    m.nanoDirect = false;
    m.inspected = false; m.inspecting = 0;
    m.unitJob = null; m.unitInspecting = false; m.unitFixed = false; m.inspectedBy = null; m.microDiag = false;   // v32
    m.autoLookBy = null;
    m.wordBurned = false;
    m.senseSuspect = false; m.heldForFlag = false;   // trust returns with the fix
    m.chaseFrom = null; m.chaseErr = null;
    m.windowLeft = 0;   // a healthy window redraws immediately
  }

  /* ---- the handover: models turning the knobs -------------------------- */
  /* =====================================================================
     v31 - SENSOR TRUST, ONE SOURCE. The founder reached Giga and it made
     everything worse: its optimizer chased a DRIFTING oven display to the
     dial's maximum (240 degrees) while its own plant view printed "2
     sensor(s) currently lying to you" - the knowledge and the policy never
     met. This function is where they meet. It answers "has this sensor been
     CAUGHT lying?" from PUBLIC knowledge only - a model-raised fault word,
     an INSPECT verdict, a physically impossible reading, a needle pinned
     outside the band with the dial already at its stop, or the automation's
     own I-turned-the-knob-and-nothing-answered deduction. It NEVER peeks at
     the hidden fault state: a recorded miss that nobody surfaced fools the
     policy exactly as it fools a person. The plant view's "lying to you"
     line and Giga's control policy both read THIS - unified, not
     duplicated. */
  function sensorFlagged(m) {
    var picoAlarm = m.picoRead && m.picoRead.said !== "none";
    var nanoNamesFault = m.nanoRead && m.nanoRead.said !== "none";
    var nanoClears = m.nanoRead && m.nanoRead.kind === "resolved" &&
      m.nanoRead.said === "none";
    if ((picoAlarm && !nanoClears) || nanoNamesFault) return "a model raised it";
    if (m.inspected) return m.inspectedBy === "unit" ? "the Unit inspected it"
      : m.inspectedBy === "micro" ? "the site brain's diagnostic found it" : "you inspected it";
    /* the needle checks read the CACHED last display (m.lastShown, recorded
       wherever a reading is actually taken) - shownValue() rolls the noisy
       sensor's dice, and a trust check must not advance anyone's seed */
    var shown = m.lastShown;
    if (shown != null) {
      if (shown < 0) return "the reading is impossible";
      var t = tierOf(m), c = m.spec.control;
      if ((shown > t.hi && m.set <= c.min) || (shown < t.lo && m.set >= c.max)) {
        return "the dial is at its stop and the needle never moved";
      }
    }
    if (m.senseSuspect) return "the needle is not answering the dial";
    return null;
  }

  function autonomyReach() {
    // Each Micro can hold ONE machine's knob; Giga holds the whole plant.
    if (G.giga) return 3;
    if (G.micro) return Math.min(3, G.micro);
    return 0;
  }
  function autoCount() {
    return G.machines.filter(function (m) { return m.auto; }).length;
  }
  function toggleAuto(id) {
    touchPlant();
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
  /* who is holding this machine's dial, for the attribution tags: Giga is
     one mind; stacked Micros are numbered, each minding its own garden */
  function holderOf(m) {
    if (G.giga) return "Giga";
    if (G.micro <= 1) return "Micro";
    var idx = 0, n = 0;
    for (var i = 0; i < G.machines.length; i++) {
      if (G.machines[i].auto) { n += 1; if (G.machines[i].id === m.id) idx = n; }
    }
    return "Micro-" + (idx || 1);
  }

  /* v32: the ONE public-word policy for automated maintenance. What did a
     model actually SAY (Nano's named kind first, else Pico's confident
     raise - below-floor words are not orders), and may automation act on
     it? GIGA VERIFIES BEFORE IT WAGERS: a coordinated plant with its own
     robot does not bet a 60-second lockout on a single model's word - it
     acts at once when the word is CORROBORATED (both models said the same
     thing) or when the move cannot lock (service); an uncorroborated cheap
     verb waits for the Unit's inspection instead. Micros keep the old
     gamble - that judgment is part of what Giga sells. A word already
     proven wrong by a lockout (wordBurned) is never followed twice. */
  function autoWord(m) {
    var nanoW = (m.nanoRead && m.nanoRead.said !== "none") ? m.nanoRead.said : null;
    var picoW = (m.picoRead && m.picoRead.said !== "none" &&
                 m.picoRead.kind !== "unsure") ? m.picoRead.said : null;
    var word = nanoW || picoW;
    if (!word || m.wordBurned) return null;
    var v = G.nano ? verbFor(word) : "service";
    var corroborated = !!(nanoW && picoW && nanoW === picoW);
    /* the senior's word above the SAME margin floor Pico asserts on is an
       order too - the measured bench says wrong parent words live almost
       entirely below it (7 of 9 recorded wrong words < 1.5) */
    var seniorSure = !!(nanoW && m.nanoRead.margin >= FLOOR);
    var actable = !(G.giga && G.unit) || corroborated || seniorSure || v === "service";
    return { word: word, verb: v, source: nanoW ? "nano" : "pico", actable: actable };
  }

  /* v35: the ONE definition of "waiting on a person" (founder: "we should
     highlight it better that it's waiting on the human"). A live fault
     with a public reason to look - a stop or a physics flag - nothing
     already running, and no word to act on; and no automation able to take
     it: Micro's remote diagnostic covers any model-held machine, and the
     Unit walks for Giga. So this survives only where the ladder genuinely
     ends with you - no site brain bought, or you kept the knob. The badge,
     the button glow, and the locks all read this one function. */
  function needsHuman(m) {
    /* v39 (founder): the NEEDS YOU badge is a MODEL telling you it has run
       out of road - so with no models on the line at all there is nobody to
       say it, and nothing should hint at what to press. Before any model is
       bought the plant is exactly what the opening promises: you, reading
       raw dials, deciding for yourself. */
    var anyModel = G.nano || G.micro > 0 || G.giga ||
      G.machines.some(function (x) { return x.pico; });
    if (!anyModel) return false;
    if (m.cond === "none" || m.inspected) return false;
    if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0) return false;
    if (!(m.stopped || sensorFlagged(m))) return false;
    var aw = autoWord(m);
    if (aw && m.lockout <= 0) return false;
    if (m.auto && (G.micro > 0 || (G.giga && G.unit))) return false;
    return true;
  }

  function autoAdjust(m, dt) {
    if (!m.auto) return;
    /* THE COORDINATION GAP (v28, founder: "it just not as smart as the
       larger model"). Stacked Micros each mind one machine on their OWN
       slow cycle - no shared clock, no view of the line - so their
       aggregate response lags. Giga is one mind on a continuous watch.
       Game simulation of a real deployment truth, like the sweep budget. */
    if (!G.giga) {
      m.microCycle = (m.microCycle == null ? 0 : m.microCycle) - dt;
      if (m.microCycle > 0) return;
      m.microCycle = 4;
    }
    var believed = shownValue(m);
    m.lastShown = believed;
    var t = tierOf(m), c = m.spec.control;
    /* v31 CONTROL DOCTRINE (the founder got burned: Giga chased a drifting
       oven display to the 240-degree stop while its own plant view knew the
       sensor was lying). Three rules, in-source because they ARE the fix:
       1. A FLAGGED SENSOR IS NEVER CHASED. sensorFlagged() is the one
          public-knowledge trust source; while it speaks, the dial goes back
          to the last position seen with a trusted in-band reading and HOLDS
          there - and the attribution line says so at the machine.
       2. CONTINUOUS IS NOT INSTANTANEOUS. Giga corrects without a cycle gap
          (the coordination edge it is sold on) but on a bounded step budget
          - a lying sensor can no longer drag a dial to its stop in a
          second. Micro keeps its slow 4s look; the gap survives.
       3. NO ANSWER IS AN ANSWER. If the dial has moved 40% of its range in
          one direction and the believed error has not shrunk, the sensor is
          not answering the control - that deduction (senseSuspect) is
          public knowledge the automation earned, and it flags the sensor. */
    var flag = sensorFlagged(m);
    if (flag) {
      if (!m.heldForFlag) {
        m.heldForFlag = true;
        var back = (m.lastTrustedSet != null && m.lastTrustedSet !== m.set);
        if (back) m.set = m.lastTrustedSet;
        m.autoNote = holderOf(m) + ": " + m.spec.name + "'s sensor is lying (" +
          flag + ") - " + (back ? "put " + c.label + " back to " + m.set + " and holding"
                                : "holding " + c.label + " steady") + " until it's fixed";
        m.autoNoteAt = G.elapsed; m.autoTier = G.giga ? "giga" : "micro";
        tape("hold", { m: m.id, why: flag, at: m.set });
        m.chaseFrom = null; m.chaseErr = null; m.gigaGas = 0;
      }
      /* v32: SUSPICION CAN CALM DOWN. The no-answer deduction used to be
         permanent outside a fault - one chase against process creep froze
         the dial for the rest of the run, and every later creep event
         became a guaranteed stop. Public rule: while held, a needle that
         sits inside the band for a few seconds HAS answered - trust
         returns and the dial may work again. A real fault re-flags through
         the other tripwires the moment it lies again. */
      if (m.senseSuspect) {
        var shownHeld = m.lastShown, tH = tierOf(m);
        if (shownHeld != null && shownHeld >= tH.lo && shownHeld <= tH.hi) {
          m.suspectCalm = (m.suspectCalm || 0) + dt;
          if (m.suspectCalm > 4) { m.senseSuspect = false; m.suspectCalm = 0; }
        } else m.suspectCalm = 0;
      }
    } else if (believed != null) {              // a dropout says nothing
      m.heldForFlag = false;
      var aim = t.lo + 0.62 * (t.hi - t.lo);
      var err = believed - aim;
      var tol = (t.hi - t.lo) * 0.08;
      if (Math.abs(err) <= tol || (believed >= t.lo && believed <= t.hi && Math.abs(err) <= tol * 2)) {
        // settled on a trusted reading: this is the position a hold restores to
        if (believed >= t.lo && believed <= t.hi) m.lastTrustedSet = m.set;
        m.chaseFrom = null; m.chaseErr = null;
      }
      if (Math.abs(err) > tol) {
        // rule 3 bookkeeping: where did this push start, how bad was it
        if (m.chaseFrom == null) { m.chaseFrom = m.set; m.chaseErr = Math.abs(err); }
        /* v32, TIGHTENED (playtest round 3: a double-missed stuck oven
           sensor was still dialed 190->240 in six seconds before the old
           40%-of-range wire tripped). Two wires now:
           - NEEDLE NOT ANSWERING, fast: the dial has moved 12% of its
             range one way and the believed error has not shrunk AT ALL -
             no honest process answers like that; flag immediately.
           - the old long-chase wire, tightened 40% -> 25% of range. */
        var chased = Math.abs(m.set - m.chaseFrom);
        if ((chased >= 0.12 * (c.max - c.min) && Math.abs(err) >= m.chaseErr * 0.98) ||
            (chased >= 0.25 * (c.max - c.min) && Math.abs(err) >= m.chaseErr * 0.9)) {
          m.senseSuspect = true;                // flags on the next look
          return;
        }
        // rule 2: Giga's bounded budget; Micro's whole-step cycle is above
        var may = true;
        if (G.giga) {
          m.gigaGas = Math.min(2, (m.gigaGas || 0) + dt * GIGA_STEPS_PER_SEC);
          if (m.gigaGas < 1) may = false; else m.gigaGas -= 1;
        }
        if (may) {
          var stepBy = c.step * (err > 0 ? -1 : 1);
          var next = Math.max(c.min, Math.min(c.max, m.set + stepBy));
          if (next !== m.set) {
            m.set = next;
            m.autoNote = holderOf(m) + " moved " + c.label + " to " + next + c.unit;
            m.autoNoteAt = G.elapsed; m.autoTier = G.giga ? "giga" : "micro";
            tape("dial", { m: m.id, to: next, by: holderOf(m) });
            /* the Unit attends the move: the SIM's dial change stays
               immediate, but the floor's floating tag waits for the Unit to
               arrive - the body catching up with the mind */
            var pinned = false;
            G.machines.forEach(function (mm) {
              if (mm.unitInspecting && mm.inspecting > 0) pinned = true;
            });
            if (G.giga && G.unit && G.unit.at !== m.id && !pinned) {
              m.unitTagHold = true;
              unitGo(m.id);
            }
          }
        }
      }
    }
    /* A held knob may also EXECUTE maintenance - but only on PUBLIC words.
       v32 ATTRIBUTION FIX (founder screenshot: "Giga ran RESTART on Nano's
       advice" on a machine whose fault both models had missed): the old
       dispatch keyed the verb off the HIDDEN fault kind whenever any model
       raised any word - a wrong raise became a secretly-correct verb wearing
       Nano's name. Now automation acts only on what was actually said:
       an INSPECT verdict first (ground truth someone paid for), else Nano's
       named kind, else Pico's raised word (the actable policy lives in
       autoWord()). A wrong word gets the wrong verb and eats the real
       consequences (that is what the lockout is for), and a fault NOBODY
       named is never guessed at - it goes to the Unit's auto-inspect
       below, or to the human plea. */
    if (m.cond !== "none" && !m.servicing && !m.restarting && !m.inspecting &&
        // Giga's continuous watch reacts in ~1s; everything else double-checks
        m.lockout <= 0 && m.condAge > (G.giga ? 1.2 : 2.5)) {
      var holder = holderOf(m);
      var aw = autoWord(m);
      if (m.inspected) {
        var vi = verbFor(m.cond);
        var viLabel = vi === "service" ? "SERVICE" : VERBS[vi].label;
        tape("auto-verb", { m: m.id, verb: vi, by: holder, on: "inspection" });
        m.autoNote = holder + " ran " + viLabel + " on the inspection verdict: " +
          CONDITION_WORD[m.cond];
        m.autoNoteAt = G.elapsed; m.autoTier = G.giga ? "giga" : "micro";
        G.autoActing = true;
        if (vi === "service") service(m.id); else maintain(m.id, vi);
        G.autoActing = false;
      } else if (aw && aw.actable) {
        tape("auto-verb", { m: m.id, verb: aw.verb, by: holder,
          on: aw.source, word: aw.word });
        if (aw.verb === "service") {
          m.autoNote = holder + " called service on the models' word" +
            (G.nano ? " - Nano ruled the cheap verbs out" : "");
        } else {
          m.autoNote = holder + " ran " + VERBS[aw.verb].label + " on " +
            (aw.source === "nano" ? "Nano's word: " : "Pico's raise: ") + CONDITION_WORD[aw.word];
        }
        m.autoNoteAt = G.elapsed; m.autoTier = G.giga ? "giga" : "micro";
        G.autoActing = true;
        if (aw.verb === "service") service(m.id); else maintain(m.id, aw.verb);
        G.autoActing = false;
      }
    }
    /* v32 - GIGA AUTO-INSPECTS (founder: "Giga should be able to automate a
       lot of it"; endgame screenshot: two simultaneous double-misses both
       pleading for a person). A fault nobody NAMED used to be a dead stop
       until a human inspected - 19 of the 50 recorded faults are double-
       misses, so a hands-off proof run was nearly impossible by
       construction. With Giga, the plant's own robot does the walking: when
       a sensor is publicly untrusted (the physics tripwires in
       sensorFlagged, or a visible stop) and there is no word to act on - or
       the crew is locked out - the Unit rolls there and runs the SAME
       inspection a person would, travel plus the full look. Honesty rails:
       this is plant maintenance inside the game sim, never a model claim;
       the RECORDED miss stays a miss on the results panel; and the
       you-caught-what-the-models-missed acknowledgment stays human-only.
       A person is still strictly better - no travel, instant start. */
    if (G.giga && G.unit && m.cond !== "none" && !m.inspected &&
        !m.servicing && !m.restarting && !m.inspecting && !m.unitJob) {
      var aw2 = autoWord(m);
      var distrust = !!sensorFlagged(m) || m.stopped;
      if (distrust && (!aw2 || !aw2.actable || m.lockout > 0)) {
        m.unitJob = "inspect";
        m.autoNote = "Giga sent the Unit to inspect the " + m.spec.name.toLowerCase() +
          (m.lockout > 0 ? " while the crew waits out the lockout"
            : aw2 ? " - one model's word is not a wager" : " - nobody named this fault");
        m.autoNoteAt = G.elapsed; m.autoTier = "giga";
        tape("unit-dispatch", { m: m.id,
          why: m.lockout > 0 ? "lockout" : aw2 ? "uncorroborated" : "unnamed" });
        unitGo(m.id);
      }
    }
    /* v35: MICRO'S REMOTE DIAGNOSTIC (founder: "it gets stuck on manual
       intervention too much - it should be more automated"). Same PUBLIC
       triggers the Unit walks on - a visible stop no word explains, or a
       physics flag - but run remotely by the site brain: x2 the look time
       and a few seconds of spin-up, so a person or the robot still beats
       it. With Giga on the floor the Unit takes the job first; the
       diagnostic covers a machine only when the robot is tied up mid-look
       somewhere else (founder: "when giga robot is on it should be even
       less frequent"). Honesty rails: game-sim maintenance on public
       knowledge only - the RECORDED miss stays a miss on the results
       panel, and the you-caught-it acknowledgment stays human-only. */
    var unitTied = false;
    if (G.giga && G.unit) {
      G.machines.forEach(function (x) {
        if (x !== m && x.unitInspecting && x.inspecting > 0) unitTied = true;
      });
    }
    if (G.micro > 0 && m.cond !== "none" && !m.inspected &&
        !m.servicing && !m.restarting && !m.inspecting &&
        (!m.unitJob || unitTied) && (!G.giga || unitTied)) {
      var awM = autoWord(m);
      var distrustM = !!sensorFlagged(m) ||
        (m.stopped && (m.downFor || 0) >= MICRO_DIAG_SPINUP);
      if (distrustM && (!awM || !awM.actable || m.lockout > 0)) {
        if (m.unitJob) m.unitJob = null;   // the site brain takes it over
        m.microDiag = true;
        m.unitFixed = true;   // automation's save, never a person's credit
        m.autoLookBy = holderOf(m) + "'s remote diagnostic";
        m.inspecting = verbSecsFor(m, INSPECT_SECS * MICRO_DIAG_MULT);
        m.autoNote = holderOf(m) + " is running a remote diagnostic - " +
          Math.ceil(m.inspecting) + "s, no walk needed";
        m.autoNoteAt = G.elapsed; m.autoTier = G.giga ? "giga" : "micro";
        tape("micro-diag", { m: m.id, secs: m.inspecting,
          why: m.lockout > 0 ? "lockout" : awM ? "uncorroborated" : "unnamed" });
        addLog(holderOf(m) + " is running a remote diagnostic on the " +
          m.spec.name.toLowerCase() + " - nobody named this fault.");
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
        m.stoppedFor = 0; m.downFor = 0;
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
        m.stoppedFor = 0; m.downFor = 0;
        if (m.cond === "none") {
          tape("verb", { m: m.id, verb: m.fixVerb, outcome: "nothing-wrong", tries: m.verbTries });
          addLog(m.spec.name + " " + verb.label.toLowerCase() + " done - nothing was wrong. " +
            verb.secs + "s lost.");
        } else {
          var odds = verb.odds[m.cond] || 0;
          if (rnd(m) < odds) {
            addLog(m.spec.name + " " + verb.label.toLowerCase() + " cleared the " +
              CONDITION_WORD[m.cond] + " sensor.");
            /* the diagnosis score: a surgical run and a lucky idle should
               not print the same card (playtest round 2) */
            G.diag.total += 1;
            if (m.verbTries === 1 && odds >= 0.5) G.diag.first += 1;
            tape("verb", { m: m.id, verb: m.fixVerb, outcome: "cleared",
              kind: m.cond, tries: m.verbTries });
            clearCondition(m);
            m.drift = 0;
          } else if (odds >= 0.5) {
            tape("verb", { m: m.id, verb: m.fixVerb, outcome: "did-not-take",
              kind: m.cond, tries: m.verbTries });
            addLog(m.spec.name + " " + verb.label.toLowerCase() +
              " did not take this time - the right call can need a second go.");
            /* said AT the station too - a failed correct verb used to look
               exactly like nothing happening (playtest round 2) */
            m.autoNote = verb.label + " did not take - try again";
            m.autoNoteAt = G.elapsed; m.autoTier = "";
          } else {
            tape("verb", { m: m.id, verb: m.fixVerb, outcome: "locked",
              kind: m.cond, tries: m.verbTries });
            m.lockout = LOCKOUT_SECS;
            /* v32: a lockout DISCREDITS the word that ordered the verb -
               public knowledge, honestly earned: the crew tried what the
               models said and it was provably wrong. Automation won't run
               the same discredited word again (it used to loop CLEAN ->
               lockout -> CLEAN forever on a recorded wrong word); the fault
               now falls to the Unit's inspection, or to a person. */
            m.wordBurned = true;
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
        m.stoppedFor = 0; m.downFor = 0;
        m.inspected = m.cond !== "none";
        m.inspectedBy = m.unitInspecting ? "unit" : m.microDiag ? "micro" : "you";
        tape("inspected", { m: m.id, found: m.cond, by: m.inspectedBy });
        if (m.unitInspecting) {
          /* v32: the Unit's look, attributed as the Unit's - and the next
             move named. The doctrine verb itself dispatches from autoAdjust
             off m.inspected, exactly as it would off a human inspection. */
          m.unitInspecting = false;
          var uNext = m.cond === "none" ? null : verbFor(m.cond);
          m.autoNote = "the Unit inspected: " + CONDITION_WORD[m.cond] +
            (uNext ? " - " + (uNext === "service" ? "SERVICE" : VERBS[uNext].label) +
              (m.lockout > 0 ? " once the lockout clears" : " next") : "");
          m.autoNoteAt = G.elapsed; m.autoTier = "giga";
          addLog("The Unit inspected the " + m.spec.name.toLowerCase() + ": " + INSPECT_WORD[m.cond]);
        } else if (m.microDiag) {
          m.microDiag = false;
          var mNext = m.cond === "none" ? null : verbFor(m.cond);
          m.autoNote = holderOf(m) + "'s diagnostic: " + CONDITION_WORD[m.cond] +
            (mNext ? " - " + (mNext === "service" ? "SERVICE" : VERBS[mNext].label) +
              (m.lockout > 0 ? " once the lockout clears" : " next") : "");
          m.autoNoteAt = G.elapsed; m.autoTier = G.giga ? "giga" : "micro";
          addLog(holderOf(m) + "'s remote diagnostic on the " +
            m.spec.name.toLowerCase() + ": " + INSPECT_WORD[m.cond]);
        } else {
          addLog(m.spec.name + " inspected: " + INSPECT_WORD[m.cond]);
        }
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
        if (!m.nextFault) m.nextFault = (10 + rnd(m) * 26) * mkFaultMult(m);
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
    /* stoppedFor is a DEBOUNCE, not a debt: capped, or a long out-of-band
       stretch kept the machine "stopped" for half a minute of phantom
       downtime after the real value was already back in the band (v32,
       found by the proof-run bots) */
    if (!ok) { m.stoppedFor = Math.min(3, m.stoppedFor + dt); }
    else { m.stoppedFor = Math.max(0, m.stoppedFor - dt * 2); }
    m.stopped = m.stoppedFor > 1.2;
    /* v40 - HOW LONG THIS STOP HAS STOOD, uncapped. stoppedFor above is a
       DEBOUNCE capped at 3s, so v35's Micro spin-up gate (stoppedFor >=
       MICRO_DIAG_SPINUP, which is 4) could never be true in a real run: the
       remote diagnostic's "a stop nobody explained" trigger was dead, and a
       Micro-held plant that hit a fault no model named sat stopped FOREVER
       (measured on every seed by the play bots - machines held at cond
       stuck for 450-800s with lockout 0, no verb running, and needsHuman()
       silenced because a Micro was supposedly covering it). This counter is
       the honest duration of the visible stop, and it is what the spin-up
       reads; a finished verb restarts it, so the diagnostic never re-fires
       the instant a retry fails. */
    m.downFor = m.stopped ? (m.downFor || 0) + dt : 0;
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
    if (G.giga && G.unit) stepUnit(dt);
    G.elapsed += dt;
    if (G.graceLeft > 0) G.graceLeft = Math.max(0, G.graceLeft - dt);
    var mx = machine("mixer"), ov = machine("oven"), pk = machine("packer");
    stepMachine(mx, dt); stepMachine(ov, dt); stepMachine(pk, dt);
    // the tape hears every stop/run transition, whatever caused it
    G.machines.forEach(function (x) {
      if (x.stopped !== x.wasStopped) {
        tape("line", { m: x.id, stopped: !!x.stopped });
        x.wasStopped = x.stopped;
      }
    });

    var CAP = 14;
    // mixer -> dough buffer
    var made = rateOf(mx) * dt * 1.5;
    mx.buffer = Math.min(CAP, mx.buffer + made);
    // oven consumes dough, makes baked
    var bake = Math.min(mx.buffer, rateOf(ov) * dt * 1.5);
    mx.buffer -= bake;
    /* an oven out of band burns what it bakes. (A STOPPED oven bakes
       nothing - rateOf is 0 - so a stopped line does not burn; the burnt
       count the founder watched climb was the oven RUNNING hot on a lying
       sensor. Intended rule, and taped as of v32 so the session tape can
       prove where every burnt cookie came from.) */
    if (ov.stopped) {
      /* v33: a stopped oven bakes nothing, so it cannot be burning - close
         the tape bracket that used to stay open until restart */
      if (G.burning) { G.burning = false; tape("burn-end", { total: Math.floor(G.spoiled) }); }
    } else if (ov.real > tierOf(ov).hi) {
      G.spoiled += bake;
      if (bake > 0 && !G.burning) { G.burning = true; tape("burn-start", { real: Math.round(ov.real) }); }
    } else {
      if (G.burning) { G.burning = false; tape("burn-end", { total: Math.floor(G.spoiled) }); }
      ov.buffer = Math.min(CAP, ov.buffer + bake);
    }
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
    /* the win card waits for the taught sequence - contract 1 could fill
       passively mid-tutorial and the overlay ate the taught click */
    if (!G.won && G.cookies >= G.contract.target && (!G.taught || G.taughtCleared)) {
      G.won = true;
      /* the completion bonus keeps the ladder reachable - the playtest found
         a mid-game flatline hovering at ~300 coins with Micro at 260 */
      var bonus = 100 + 50 * G.contract.level;
      G.coins += bonus;
      tape("contract", { event: "filled", level: G.contract.level, bonus: bonus });
      addLog("Contract filled: " + G.contract.target + " cookies shipped. " +
        "The buyer tips " + bonus + " coins for the finished order.");
      if (DOM.stations) showWin();
    }

    /* THE RESULTS SERIES. An automated plant has to have something to show
       for itself, so the game keeps its own rolling record: cookies per
       second, uptime, and the incident tally. Game arithmetic over game
       state - it is labelled that way wherever it is drawn. */
    G.runTime += dt;
    var allUp = G.machines.every(function (x) { return !x.stopped; });
    if (allUp) G.upTime += dt;
    /* THE SLA STAKES (v29, staged from playtest round 2). From contract 2 up
       the buyer expects the line UP: sustained uptime below the bar - a full
       rolling minute under it - and they walk. No completion bonus, and a
       fresh order is ALWAYS on the desk: a lost contract is a consequence,
       never a dead end. The tutorial gate keeps this off contract 1. */
    if (G.contract.sla > 0 && !G.won && (!G.taught || G.taughtCleared)) {
      G.slaClock.run += dt;
      if (allUp) G.slaClock.up += dt;
      if (G.slaClock.run >= 60) {
        if (G.slaClock.up / G.slaClock.run < G.contract.sla) buyerWalks();
        else { G.slaClock.run = 0; G.slaClock.up = 0; }
      }
    }
    /* the tape's coins pulse: a ten-second heartbeat of the wallet and the
       line, so a downloaded tape can graph the run */
    if (G.elapsed - G.tapeCoinsAt >= 10) {
      G.tapeCoinsAt = G.elapsed;
      tape("coins", { coins: Math.floor(G.coins), cookies: Math.floor(G.cookies),
        burnt: Math.floor(G.spoiled),
        uptime: Math.round((G.runTime ? G.upTime / G.runTime : 1) * 100) });
    }
    // per-machine uptime, for the site board: same arithmetic, one machine at a time
    G.machines.forEach(function (x) { x.runT += dt; if (!x.stopped) x.upT += dt; });
    stepCert(dt, allUp);
    // the streak: how long the whole line has run clean, and the best yet
    if (allUp) { G.cleanRun += dt; if (G.cleanRun > G.bestRun) G.bestRun = G.cleanRun; }
    else G.cleanRun = 0;
    G.sampleAt += dt;
    if (G.sampleAt >= 1) {
      G.sampleAt = 0;
      // the site board's trend arrows: is each machine's headroom growing or
      // shrinking since the last sample? Game state, sampled, nothing more.
      G.machines.forEach(function (x) {
        var t2 = tierOf(x);
        x.headTrend = x.lastHead == null ? 0 : (t2.hi - x.real) - x.lastHead;
        x.lastHead = t2.hi - x.real;
      });
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
     THE FACTORY CERTIFICATE - the campaign goal (v28)
     ===================================================================== */
  /* The checklist is gear; the last line is CONDUCT: the plant must run
     hands-off for CERT_PROOF_SECS at CERT_UPTIME or better, while its own
     dashboard records it. Any hand on the plant restarts the clock - the
     founder's endgame is a factory that runs itself, and the only honest
     proof of that is leaving it alone. All game arithmetic. */
  function certItems() {
    var maxed = G.machines.every(function (m) { return !TIERS[m.id][m.tier + 1]; });
    var picos = G.machines.every(function (m) { return m.pico; });
    // either route to full coverage counts: one coordinated Giga, or three
    // greedy Micros - the proof run itself will feel the difference
    var desk = G.nano && (G.giga || G.micro >= MICRO_MAX);
    var handed = autoCount() === G.machines.length && autonomyReach() >= G.machines.length;
    return [
      { key: "mk", label: "every machine at Mk III", done: maxed },
      { key: "picos", label: "a Wave Pico on every machine", done: picos },
      { key: "desk", label: "Nano at the desk, plus full coverage (Giga, or three Micros)", done: desk },
      { key: "handed", label: "every dial handed to the models", done: handed },
      { key: "proof", label: Math.round(CERT_PROOF_SECS / 60) + " minutes hands-off at " +
          Math.round(CERT_UPTIME * 100) + "%+ uptime", done: G.cert.done,
        /* round 5: a strict "never touch" reading let a dead plant sit for
           half an hour - say the real rule where the player reads the goal */
        hint: "stepping in costs a " + Math.round(CERT_TOUCH_SETBACK) +
          "s setback, not a restart - rescue a dead line",
        progress: G.cert.done ? 1 : Math.min(1, G.cert.run / CERT_PROOF_SECS) },
    ];
  }
  function certReady() {
    var it = certItems();
    return it[0].done && it[1].done && it[2].done && it[3].done;
  }
  function certNext() {
    var it = certItems();
    for (var i = 0; i < it.length; i++) if (!it[i].done) return it[i];
    return null;
  }

  /* THE RECOMMENDED NEXT BUY (v29) - ONE source of truth for the goal chip's
     NEXT row and the floor's strong glow, so the game never points two ways
     at once (test-locked). MODELS COME FIRST by design: watchers before
     horsepower, so a Mk upgrade only becomes the recommendation once the
     model ladder is done. The micro-vs-giga fork is a genuine CHOICE - the
     scale-out-vs-scale-up lesson - so it returns BOTH and the glow picks no
     winner between them. */
  function recommendedNext() {
    for (var i = 0; i < G.machines.length; i++) {
      if (!G.machines[i].pico) return [{ kind: "pico", id: G.machines[i].id }];
    }
    if (!G.nano) return [{ kind: "nano" }];
    if (!G.giga) {
      if (G.micro === 0) return [{ kind: "micro" }];
      if (G.micro < MICRO_MAX) return [{ kind: "micro" }, { kind: "giga" }];
      /* v40: THREE MICROS IS "MODELS DONE" TOO, and the recommendation has
         to agree with the certificate. certItems() accepts either route to
         full coverage ("Giga, or three Micros"), but this branch used to
         return [] on the Micro route - so a player who scaled OUT lost the
         NEXT row and the Mk tags' strong glow for the rest of the campaign,
         and was never pointed at the Mk III line the certificate still
         demands. It falls through to the gear now, exactly like Giga. */
    }
    // models done: the certificate's Mk III line is the next purchase
    for (var j = 0; j < G.machines.length; j++) {
      if (TIERS[G.machines[j].id][G.machines[j].tier + 1]) {
        return [{ kind: "tier", id: G.machines[j].id }];
      }
    }
    return [];
  }

  /* what the floor desk offers, in order (v29 - the founder could not buy a
     second Micro from the desk tag: the old chain hid "another Micro" behind
     Giga, backwards). With 1-2 Micros and no Giga it offers BOTH - the
     scale-out-vs-scale-up tradeoff, sold at the point of purchase. After
     Giga, no more Micros: Giga already minds every dial. */
  function deskOffers() {
    if (!G.nano) return ["nano"];
    if (G.giga) return [];
    if (G.micro === 0) return ["micro"];
    if (G.micro < MICRO_MAX) return ["micro", "giga"];
    return ["giga"];
  }

  /* the gateway's watch row, decided as STATE (v29 - the founder hit a row
     of all-disabled buttons: "what is this supposed to do"). Full Pico
     coverage means there is nothing to point the gateway at, so the row
     goes away entirely; with partial coverage only bare machines are
     choices, and covered ones render as non-button chips. */
  function watchOptions() {
    var bare = [], covered = [];
    G.machines.forEach(function (m) { (m.pico ? covered : bare).push(m.id); });
    return { show: bare.length > 0, bare: bare, covered: covered };
  }
  /* a hand on the plant sets the proof clock BACK. Dials, verbs, buys,
     autonomy toggles, pointing the gateway - all of it counts as touching. */
  function touchPlant() {
    /* v32: the plant's OWN moves are not your hands. Automated verbs route
       through the same maintain()/service() a person uses, and before this
       guard every one of them zeroed the hands-off proof clock - a fully
       automated plant could never be "untouched". And (playtest round 3)
       a human touch now COSTS a flat 20s setback instead of zeroing the
       window: answering a plea mid-proof is a penalty, not a death. */
    if (G.autoActing) return;
    if (G.cert.done) return;
    if (G.cert.run > 0) {
      var cut = Math.min(CERT_TOUCH_SETBACK, G.cert.run);
      G.cert.run -= cut;
      G.cert.up = Math.max(0, G.cert.up - cut);
      if (certReady() && G.cert.run > 1) {
        addLog("Hands on during the proof - the clock steps back 20s (not a reset).");
      }
    }
  }
  /* pure pace check the paint and the locks both run: is this window on
     pace, and can it still clear the bar at all? */
  function certPace() {
    var need = CERT_PROOF_SECS * CERT_UPTIME;
    var bestPossible = G.cert.up + (CERT_PROOF_SECS - G.cert.run);
    return {
      pct: G.cert.run > 0 ? G.cert.up / G.cert.run : 1,
      onPace: G.cert.run <= 0 || G.cert.up / G.cert.run >= CERT_UPTIME,
      doomed: bestPossible < need,
    };
  }
  function stepCert(dt, allUp) {
    if (G.cert.done) return;
    if (!certReady()) { G.cert.run = 0; G.cert.up = 0; return; }
    G.cert.run += dt;
    if (allUp) G.cert.up += dt;
    /* v32 (playtest round 3: the clock counted to 178s over a dead plant,
       then zeroed WORDLESSLY): a window that can no longer mathematically
       clear the bar resets NOW and says why; the paint prints the live
       pace beside the clock the whole way. */
    var pace = certPace();
    if (pace.doomed && G.cert.run > 5) {
      addLog("Proof window reset - uptime is " + Math.round(pace.pct * 100) +
        "% and even a perfect rest of the window can't reach the " +
        Math.round(CERT_UPTIME * 100) + "% bar. A fresh window starts now.");
      G.cert.run = 0; G.cert.up = 0;
      return;
    }
    if (G.cert.run >= CERT_PROOF_SECS) {
      if (G.cert.up / G.cert.run >= CERT_UPTIME) {
        G.cert.done = true;
        G.cert.doneAt = G.elapsed;
        /* derived, not typed: this line still said "Three minutes" two rounds
           after CERT_PROOF_SECS dropped to 120 (v36), so the win message
           contradicted the checklist directly above it. */
        addLog(Math.round(CERT_PROOF_SECS / 60) +
          " minutes untouched, uptime held. FACTORY CERTIFIED - it runs itself now.");
        if (DOM.stations) showCertified();
      } else {
        addLog("Proof window ended at " + Math.round((G.cert.up / G.cert.run) * 100) +
          "% - under the " + Math.round(CERT_UPTIME * 100) + "% bar. A fresh window starts now.");
        G.cert.run = 0; G.cert.up = 0;
      }
    }
  }

  function showCertified() {
    if (!DOM.certOver) return;
    DOM.certOver.hidden = false;
    DOM.certStats.textContent = "";
    var up = G.runTime ? (G.upTime / G.runTime) * 100 : 100;
    [["SHIPPED, LIFETIME", String(Math.floor(G.cookies))],
     ["UPTIME, LIFETIME", up.toFixed(0) + "%"],
     ["CALLED BY THE MODELS", String(G.modelCalled)],
     ["SAVED BY YOU", String(G.humanSaves)],
     ["CONTRACT LEVEL", String(G.contract.level)],
     [G.coins < 0 ? "ON LOAN" : "COINS", String(Math.floor(G.coins))],
    ].forEach(function (p2) {
      var c = el("div", "cl-results__cell");
      c.appendChild(el("b", null, p2[1]));
      c.appendChild(el("i", null, p2[0]));
      DOM.certStats.appendChild(c);
    });
    paint();
    if (DOM.certKeep) DOM.certKeep.focus();
    // deliberately does NOT pause: the whole point is that it runs itself
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

  /* MICRO's SITE BOARD - the wall scoreboard (v28). One pure function feeds
     both the board on the floor and the desk card, so they cannot disagree.
     Uptime is each machine's own up-time over its run-time; the bottleneck
     line names the worst of them. Game arithmetic, labelled where drawn. */
  function siteBoard() {
    var rows = G.machines.map(function (m) {
      var up = m.runT > 0 ? m.upT / m.runT : 1;
      return {
        id: m.id, name: m.spec.name,
        up: up,
        trend: m.headTrend == null ? 0 : m.headTrend,
        stopped: m.stopped, cond: m.cond,
      };
    });
    var worst = rows.slice().sort(function (a, b) { return a.up - b.up; })[0];
    var line = worst.up < 0.985
      ? "the " + worst.name.toLowerCase() + " is holding you back - " +
        Math.round(worst.up * 100) + "% uptime"
      : "no bottleneck - the whole line is running clean";
    return { rows: rows, worst: worst, line: line };
  }

  // GIGA: the plant view. Which station caps the line, and what it costs.
  function plantView() {
    var rates = G.machines.map(function (m) { return { id: m.id, name: m.spec.name, rate: rateOf(m) }; });
    var slow = rates.slice().sort(function (a, b) { return a.rate - b.rate; })[0];
    var best = rates.slice().sort(function (a, b) { return b.rate - a.rate; })[0];
    var loss = Math.max(0, best.rate - slow.rate);
    /* v31: the "lying to you" count now rides sensorFlagged() - the same
       public-knowledge trust source the control policy obeys. The old count
       peeked at hidden fault state, which meant the desk card KNEW about
       liars the policy went on trusting - the exact split the founder got
       burned by (and, counted from the secret, it was itself a leak). */
    return { rates: rates, bottleneck: slow, loss: loss,
             flagged: G.machines.filter(function (m) { return !!sensorFlagged(m); }).length };
  }

  /* =====================================================================
     THE GIGA UNIT (v30) - the plant's visible mind. Buying Giga was a line
     item; now it is someone on the floor: a little automaton that patrols,
     inspects, is physically present when Giga trims a dial, offers a line
     of analysis when it pauses - and carries the plant radio, so you can
     ask a REAL model (Ping) about your line.
     ===================================================================== */

  /* THE UNIT'S ATTENTION is belief-driven - the v27 gateway lesson applied
     again: it goes where the game's own SURFACED numbers point (an incident
     a model raised, a machine anyone can see is stopped, the worst public
     uptime) and never where only the hidden fault state knows to look. A
     lying sensor nobody caught leaves the Unit as fooled as the person. */
  /* v31 - AUTOMATION THAT IS STUCK ASKS FOR HELP, LOUDLY. The founder's
     plant sat at 0.00/s under a banner reading PLANT RUNNING ITSELF while
     the one fact that mattered - "I can't clear this, a person has to look"
     - sat quietly in a panel. This names the machine automation cannot
     clear: it is stopped with a live fault, no verb is running, and either
     nothing was ever raised (a recorded chain miss - there is no alarm to
     act on) or the crew is locked out. The Unit rolls THERE and says so;
     the goal chip echoes it. Public knowledge only, like everything the
     automation believes. */
  function unitHelpTarget() {
    if (!G.unit) return null;
    for (var i = 0; i < G.machines.length; i++) {
      var m = G.machines[i];
      if (!m.auto || !m.stopped || m.cond === "none") continue;
      if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0) continue;
      var alarmed = (m.picoRead && m.picoRead.said !== "none") ||
                    (m.nanoRead && m.nanoRead.said !== "none");
      /* v32: with Giga on the floor the Unit auto-inspects unnamed faults
         itself, so the plea survives only where the robot is stuck too -
         the kind is known (a word or a look) but the crew is locked out,
         and nothing mechanical can move until the clock runs. */
      if (G.giga) {
        if ((alarmed || m.inspected) && m.lockout > 0) return m;
        continue;
      }
      if (!alarmed || m.lockout > 0) return m;
    }
    return null;
  }

  function unitFocus() {
    /* v32: a job in hand keeps the Unit on station - it does not wander
       off mid-inspection or while waiting out a verb it must follow up */
    var job = G.machines.filter(function (m) { return m.unitJob || m.unitInspecting; })[0];
    if (job) return job.id;
    var help = unitHelpTarget();
    if (help) return help.id;
    var alarmed = G.machines.filter(function (m) {
      return m.cond !== "none" && ((m.picoRead && m.picoRead.said !== "none") ||
        (m.nanoRead && m.nanoRead.kind === "resolved"));
    });
    if (alarmed.length) return alarmed[0].id;
    var stopped = G.machines.filter(function (m) { return m.stopped; });
    if (stopped.length) return stopped[0].id;
    var sb = siteBoard();
    if (sb.worst && sb.worst.up < 0.985) return sb.worst.id;
    var ring = ["mixer", "oven", "packer", "desk"];
    return ring[(ring.indexOf(G.unit.at) + 1) % ring.length];
  }

  /* =====================================================================
     v32 ANNOTATION LANES (founder screenshot: bubbles over the site board,
     ambient chatter floating over the oven engraving, TALK riding into the
     packer). The floor's words live in LANES with a spoken-word BUDGET,
     decided in ONE pure policy that the painter and the locks both run:
     - SPEECH (machine bubbles and the Unit's mouth) shares one budget -
       2 bubbles at once, 1 on a tight screen - ranked plea > model
       verdict > hold/job say-so > ambient chatter;
     - ambient never shares the stage at all: it waits for a quiet floor
       (any higher-priority word anywhere silences it);
     - lamps, badges and the attribution tags are not speech - they ride
       their own thin lanes (CSS) and are not budgeted here;
     - the site board owns its corner exclusively (CSS pins it top-right;
       nothing else is placed there). */
  var BUBBLE_RANK = { plea: 0, verdict: 1, hold: 2, ambient: 3 };
  function bubblePlan(cands, budget) {
    var quiet = cands.every(function (c) { return c.kind === "ambient"; });
    var sorted = cands.slice().sort(function (a, b) {
      return (BUBBLE_RANK[a.kind] != null ? BUBBLE_RANK[a.kind] : 9) -
             (BUBBLE_RANK[b.kind] != null ? BUBBLE_RANK[b.kind] : 9);
    });
    var out = [];
    for (var i = 0; i < sorted.length && out.length < budget; i++) {
      if (sorted[i].kind === "ambient" && !quiet) continue;
      out.push(sorted[i]);
    }
    return out;
  }
  /* what WANTS to speak right now - machine verdict bubbles plus the Unit */
  function bubbleCands() {
    var cands = [];
    G.machines.forEach(function (m) {
      var r2 = m.picoRead;
      if (m.pico && r2 && (r2.kind === "unsure" || r2.said !== "none")) {
        cands.push({ id: m.id, kind: "verdict" });
      }
    });
    if (G.giga && G.unit && G.unit.say && G.elapsed < G.unit.sayUntil && !G.unit.going) {
      cands.push({ id: "unit", kind: G.unit.sayKind || "ambient" });
    }
    return cands;
  }

  /* The Unit's ambient lines are the tally engines in persona voice - the
     SAME siteBoard()/plantView() that feed the wall board and the desk
     cards (one source, test-locked). It never mints a number of its own. */
  function unitLine() {
    /* v32: a Unit on an inspection job narrates the job - the auto-inspect
       flow is the new endgame beat and it should read on the floor. */
    var jobHere = G.machines.filter(function (m2) {
      return G.unit.at === m2.id && (m2.unitInspecting || m2.unitJob === "inspect");
    })[0];
    if (jobHere) {
      G.unit.sayKind = "hold";
      return "Nobody named this fault, so I'm inspecting the " +
        jobHere.spec.name.toLowerCase() + " myself. The recorded miss stays on the books.";
    }
    var help = unitHelpTarget();
    if (help && G.unit.at === help.id) {
      G.unit.sayKind = "plea";
      if (help.lockout > 0 && help.inspected) {
        return "Crew's locked out of the " + help.spec.name.toLowerCase() +
          " and we already know it's " + CONDITION_WORD[help.cond] +
          " - we wait it out, about " + Math.ceil(help.lockout) + "s.";
      }
      return help.lockout > 0
        ? "The crew's locked out here - INSPECT the " + help.spec.name.toLowerCase() +
          " while we wait. Inspecting never locks."
        : "I can't read the " + help.spec.name.toLowerCase() +
          " - it needs your eyes. INSPECT it.";
    }
    var heldHere = G.machines.filter(function (m2) {
      return m2.heldForFlag && G.unit.at === m2.id;
    })[0];
    if (heldHere) {
      G.unit.sayKind = "hold";
      return "The " + heldHere.spec.name.toLowerCase() + "'s sensor is lying - I'm holding " +
        heldHere.spec.control.label + " steady until it's fixed.";
    }
    G.unit.sayKind = "ambient";
    var sb = siteBoard(), pv = plantView();
    var topics = [];
    topics.push(sb.line.charAt(0).toUpperCase() + sb.line.slice(1) + ".");
    if (pv.loss > 0.05) {
      topics.push("The " + pv.bottleneck.name.toLowerCase() + " caps the line - about " +
        pv.loss.toFixed(2) + "/s left on the table.");
    }
    var last = G.history.length ? G.history[G.history.length - 1] : null;
    var remaining = G.contract.target - G.cookies;
    if (last && last.rate > 0.05 && remaining > 0) {
      topics.push("At this pace the order lands in about " +
        Math.round(remaining / last.rate) + "s.");
    }
    if (G.runTime > 30) {
      topics.push("Line uptime " + Math.round((G.upTime / Math.max(1, G.runTime)) * 100) +
        "% since you started. I keep the books.");
    }
    var line = topics[G.unit.topic % topics.length];
    G.unit.topic += 1;
    return line;
  }

  /* v32: jobs the Unit was sent to do land WITH the Unit. The inspection
     starts on arrival - same cost a person pays, plus the walk it took. */
  function startUnitJobs() {
    G.machines.forEach(function (m) {
      if (m.unitJob !== "inspect" || G.unit.at !== m.id || G.unit.going) return;
      if (m.cond === "none" || m.inspected) { m.unitJob = null; return; }   // resolved en route
      if (m.inspecting > 0 || m.servicing > 0 || m.restarting > 0) return;  // wait out the verb
      m.unitJob = null;
      m.unitInspecting = true;
      m.unitFixed = true;              // this save belongs to the robot, not a person
      m.autoLookBy = "The Unit's inspection";
      m.inspecting = verbSecsFor(m, INSPECT_SECS);
      m.autoNote = "the Unit is inspecting - " + m.inspecting + "s to look";
      m.autoNoteAt = G.elapsed; m.autoTier = "giga";
      tape("unit-inspect", { m: m.id, secs: m.inspecting });
      addLog("The Unit is inspecting the " + m.spec.name.toLowerCase() +
        " - the same look a person gets, plus the walk.");
    });
  }

  function flushUnitTags() {
    G.machines.forEach(function (m) {
      if (m.unitTagHold && G.unit.at === m.id) {
        m.unitTagHold = false;
        m.autoNoteAt = G.elapsed;   // the floor tag lands WITH the Unit
      }
    });
  }

  function stepUnit(dt) {
    var u = G.unit;
    if (!u) return;
    if (u.going) {
      u.travelLeft -= dt;
      if (u.travelLeft <= 0) {
        u.at = u.going; u.going = null;
        u.pose = u.at === "desk" ? "idle" : "inspect";
        u.pauseLeft = 8 + (u.topic % 5);      // a supervisor, not a busy bee
        flushUnitTags();
        startUnitJobs();
        u.say = unitLine();
        u.sayUntil = G.elapsed + Math.min(u.pauseLeft, 7);
      }
      return;
    }
    /* v32: a pending job starts the moment conditions allow - including
       when the Unit was ALREADY standing at the machine when the job came
       in (arrival alone used to be the only trigger, and a job assigned
       on-station never started) */
    startUnitJobs();
    /* v33: the Unit finishes what it started - while ITS inspection timer
       runs on a machine, it stands there; patrol decisions wait (the round-4
       playtest caught it doing ambient patter at the oven while the packer's
       "unit inspecting" clock ran) */
    var busyAt = null;
    G.machines.forEach(function (m) { if (m.unitInspecting && m.inspecting > 0) busyAt = m.id; });
    if (busyAt) {
      if (!u.going && u.at !== busyAt) unitGo(busyAt);
      if (u.at === busyAt) { u.pose = "inspect"; if (u.pauseLeft < 1) u.pauseLeft = 1; }
      return;
    }
    u.pauseLeft -= dt;
    if (u.pauseLeft <= 0) {
      var next = unitFocus();
      if (next !== u.at) unitGo(next);
      else {
        u.pauseLeft = 8 + (u.topic % 5);
        u.say = unitLine();
        u.sayUntil = G.elapsed + 7;
      }
    }
  }

  function unitGo(dest) {
    var u = G.unit;
    if (!u || u.at === dest || u.going === dest) return;
    u.going = dest;
    u.pose = "roll";
    u.dir = UNIT_POS[dest] >= UNIT_POS[u.at] ? 1 : -1;
    u.travelLeft = 1 + Math.abs(UNIT_POS[dest] - UNIT_POS[u.at]) * 0.02;
  }

  /* ---- the plant radio: chat through the Unit, answered by PING --------
     Ping is RogerAI's live concierge - a REAL model over the Tower relay,
     and the only voice here that is not game arithmetic. The Unit carries
     the radio; it never signs Ping's words as its own, and never as Giga's
     (Giga has no hosted model - putting words in its mouth would be the
     exact lie this whole deck exists to avoid). */
  function plantSummary() {
    var sb = siteBoard();
    var mach = G.machines.map(function (m) {
      return m.spec.name.toLowerCase() + " Mk " + ["I", "II", "III"][m.tier] + " (" +
        Math.round((m.runT > 0 ? m.upT / m.runT : 1) * 100) + "% uptime" +
        (m.stopped ? ", stopped right now" : "") + ")";
    }).join(", ");
    var models = [];
    G.machines.forEach(function (m) { if (m.pico) models.push("a Pico on the " + m.spec.name.toLowerCase()); });
    if (G.nano) models.push("Wave Nano at the gateway");
    if (G.micro) models.push(G.micro + " Wave Micro" + (G.micro > 1 ? "s" : ""));
    if (G.giga) models.push("Wave Giga running the dials");
    /* v32 (playtest: Ping recommended packer upgrades with the packer
       already at Mk III): say plainly what is maxed out, so the concierge
       stops selling the player what they own */
    var maxed = G.machines.filter(function (m) { return !TIERS[m.id][m.tier + 1]; })
      .map(function (m) { return m.spec.name.toLowerCase() + " already at top tier"; });
    if (G.giga && G.machines.every(function (m) { return m.pico; })) {
      maxed.push("every model tier already installed");
    }
    return mach + "; " + sb.line + "; models: " +
      (models.length ? models.join(", ") : "none yet") + "; " +
      (maxed.length ? "note: " + maxed.join(", ") + " - nothing to buy there; " : "") +
      Math.floor(G.coins) + " coins" + (G.coins < 0 ? " (on loan)" : "") +
      "; contract " + G.contract.level + (Math.floor(G.cookies) >= G.contract.target
        ? " already filled (" + Math.floor(G.cookies) + " shipped against a " + G.contract.target + "-cookie order)"
        : " at " + Math.floor(G.cookies) + "/" + G.contract.target) +
      " cookies; " + G.incidents.missed + " incidents missed so far.";
  }

  function pingFraming(q) {
    return "On the RogerAI Playbox, the visitor is playing the cookie-line factory game - " +
      "a toy plant that teaches what RogerAI's Wave models do. Their line right now: " +
      plantSummary() + " The plant's little robot assistant relays your reply over the " +
      "factory radio. The player asks: \"" + q + "\" Answer in one or two sentences, in " +
      "your DJ voice, using only the numbers above. Never recommend buying anything " +
      "noted above as already owned or at top tier, and never invent causes the " +
      "numbers don't show.";
  }

  // the Unit's own fallback voice: tally arithmetic, no radio required
  function unitFallbackLine() {
    return "the radio's quiet - here's what I can see myself: " + siteBoard().line + ".";
  }

  function tapeChat(q, answered, text) {
    tape("chat", { q: String(q).slice(0, 80), answered: answered,
                   note: String(text || "").slice(0, 40) });
  }

  /* v32 PING GUARDRAILS (playtest round 3: one reply was raw numeric noise
     shipped straight to the player, one confabulated a cause, and the
     identity question got dodged).
     - WHO-ARE-YOU is answered LOCALLY, off the network: the radio must
       never let a live model improvise its own identity story;
     - a live reply has to look like language before it airs - length,
       actual letters, no long repeats, mostly word-characters. One retry,
       then the Unit's own tally-arithmetic fallback. */
  function isIdentityQ(q) {
    return /who\s+(are|r)\s+(you|u)|what\s+are\s+you\b|are\s+you\s+(real|human|an?\s+(ai|model|bot|robot|person))|what('|’)?s\s+your\s+name/i.test(q);
  }
  var IDENTITY_LINE = "I'm the radio, not the mind. The live voice on this channel is " +
    "Ping, RogerAI's concierge - the Unit just carries the speaker. The Wave models " +
    "on this floor only speak in the verdicts you see at the machines.";
  /* v33: verb-doctrine questions are the maintenance card's territory - the
     round-4 playtest asked "the oven sensor is drifting - what do i do?" and
     got a GPU-sharing advert back from the live radio. The card knows the
     answer; answer it here, instantly, without the network. */
  function doctrineAnswer(q) {
    if (!/(what|how|why|should|do i|fix|help|handle|wrong|mean)/i.test(q)) return null;
    var CARD = [
      ["drift", "Drifting is calibration sliding - RECALIBRATE fixes it; a restart won't help."],
      ["stuck", "A stuck sensor usually re-seats with a RESTART - free and quick. If it doesn't take, try once more before anything drastic."],
      ["nois", "Noisy is interference on the pickup - CLEAN it; a restart rarely helps."],
      ["rail", "Railed means the sensor is pinned at its limit - that's hardware, and only SERVICE fixes it."],
      ["drop", "A dropout usually clears with a RESTART - the wire went quiet, not the machine."],
    ];
    var lq = q.toLowerCase();
    for (var i = 0; i < CARD.length; i++) {
      if (lq.indexOf(CARD[i][0]) >= 0) {
        return CARD[i][1] + " (That's the maintenance card's word - answered right here, no radio needed.)";
      }
    }
    return null;
  }

  function saneReply(t) {
    if (!t) return false;
    t = String(t).trim();
    if (t.length < 2 || t.length > 600) return false;
    if (!/[a-zA-Z]{3}/.test(t)) return false;         // must contain a word
    if (/(.)\1{7,}/.test(t)) return false;            // no keyboard-lean runs
    var wordish = (t.match(/[a-zA-Z0-9\s.,'!?;:()\-%"’“”·]/g) || []).length;
    return wordish / t.length >= 0.7;                 // mostly language
  }

  function sendChat(q) {
    q = String(q || "").trim();
    if (!q) return false;
    /* local answers first: identity and doctrine never need the radio,
       so a slow (or hung) live call must not silence them */
    if (isIdentityQ(q)) {
      G.chat.push({ who: "you", text: q });
      G.chat.push({ who: "unit", text: IDENTITY_LINE });
      tapeChat(q, "local-identity", IDENTITY_LINE);
      G.chat = G.chat.slice(-12);
      paintChat();
      return true;
    }
    var doctrine = doctrineAnswer(q);
    if (doctrine) {
      G.chat.push({ who: "you", text: q });
      G.chat.push({ who: "unit", text: doctrine, how: "local-doctrine" });
      tapeChat(q, "local-doctrine", doctrine);
      G.chat = G.chat.slice(-12);
      paintChat();
      return true;
    }
    if (G.chatBusy) return false;
    G.chat.push({ who: "you", text: q });
    G.chatBusy = true;
    paintChat();
    function ask() {
      return window.fetch(PING_URL, {
        method: "POST", headers: { "Content-Type": "application/json" },
        credentials: "omit", cache: "no-store",
        body: JSON.stringify({ messages: [{ role: "user", content: pingFraming(q) }] }),
      }).then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
        .then(function (d) {
          var reply = d && d.reply ? String(d.reply) : "";
          if (!saneReply(reply)) return Promise.reject("insane");
          return reply;
        });
    }
    function askTimed() {
      return Promise.race([ask(), new Promise(function (_, no) {
        setTimeout(function () { no("timeout"); }, 15000);
      })]);
    }
    askTimed()
      .catch(function (why) { return why === "insane" ? askTimed() : Promise.reject(why); })
      .then(function (reply) {
        G.chat.push({ who: "ping", text: reply });
        tapeChat(q, "live", reply);
      })
      .catch(function () {
        var f = unitFallbackLine();
        G.chat.push({ who: "unit", text: f });
        tapeChat(q, "fallback", f);
      })
      .then(function () {
        G.chatBusy = false;
        G.chat = G.chat.slice(-12);      // last ~6 exchanges
        paintChat();
        paint();
      });
    return true;
  }

  function openChat() {
    if (!DOM.chatOver || !DOM.chatOver.hidden) return;
    G.chatWasRunning = G.running;
    G.running = false;
    DOM.chatOver.hidden = false;
    paintChat();
    paint();
    if (DOM.chatInput) DOM.chatInput.focus();
  }
  function closeChat() {
    if (!DOM.chatOver || DOM.chatOver.hidden) return;
    DOM.chatOver.hidden = true;
    if (G.chatWasRunning) { G.running = true; lastT = 0; }
    paint();
    if (DOM.unitTalk) DOM.unitTalk.focus();
  }

  /* ---- economy --------------------------------------------------------- */
  function buyTier(id) {
    touchPlant();
    var m = machine(id);
    var next = TIERS[id][m.tier + 1];
    if (!next || G.coins < next.price) return false;
    G.coins -= next.price;
    m.tier += 1;
    /* THE UPGRADE MOMENT (v29): the floor sprite swaps to the new plate and
       gets a brief reveal beat (paintStation reads upgradeAt; reduced motion
       gets a clean swap and this radio line does the announcing) */
    m.upgradeAt = G.elapsed;
    tape("upgrade", { m: m.id, to: next.name });
    addLog("The new " + m.spec.name.toLowerCase() + " is in - " + next.name +
      ", band now " + next.lo + "-" + next.hi + " " + m.spec.sensor.unit + ".");
    paint();
    return true;
  }

  function buyPico(id) {
    touchPlant();
    var m = machine(id);
    if (m.pico || G.coins < MODEL_PRICE.pico) return false;
    G.coins -= MODEL_PRICE.pico;
    m.pico = true;
    if (G.firstPicoAt == null) G.firstPicoAt = G.elapsed;
    tape("buy", { what: "pico", m: m.id });
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

  var MICRO_MAX = 3;
  function buyDesk(which) {
    touchPlant();
    if (which === "micro") {
      if (G.micro >= MICRO_MAX || G.coins < MODEL_PRICE.micro) return false;
      G.coins -= MODEL_PRICE.micro;
      G.micro += 1;
      tape("buy", { what: "micro", count: G.micro });
      addLog(G.micro === 1
        ? "Wave Micro online at the desk - the site view, and it can hold one dial."
        : "Micro #" + G.micro + " online. It'll mind its own machine - " +
          "three of them cover every dial, but none of them talk to each other.");
      paint();
      return true;
    }
    if (G[which] || G.coins < MODEL_PRICE[which]) return false;
    G.coins -= MODEL_PRICE[which];
    G[which] = true;
    tape("buy", { what: which });
    if (which === "nano") {
      G.machines.forEach(function (m) {
        if (m.cond !== "none" && m.pico) m.nanoRead = nanoRead(m.sample);
      });
      addLog("Wave Nano online at the desk - instant through its Picos, and it " +
        "can direct-watch ONE bare machine at a time on a " + NANO_SWEEP_SECS + "s sweep.");
    } else if (which === "giga") {
      G.unit = { at: "desk", going: null, travelLeft: 0, pauseLeft: 2, dir: 1,
                 pose: "idle", say: "", sayUntil: 0, topic: 0 };
      addLog("Wave Giga online - one mind on every dial, the line balanced as a " +
        "whole. Its Unit is rolling onto the floor: it inspects faults nobody " +
        "names, and TALK asks it anything.");
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
    touchPlant();
    var m = machine(id);
    var verb = VERBS[verbName];
    if (!verb) return false;
    if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0 || m.lockout > 0) return false;
    if (verb.cost) G.coins -= verb.cost;
    m.fixVerb = verbName;
    m.restarting = verbSecsFor(m, verb.secs);   // Mk canon: better iron services faster
    m.verbTries += 1;
    tape("verb-start", { m: m.id, verb: verbName, tries: m.verbTries });
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
    touchPlant();
    var m = machine(id);
    if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0) return false;
    m.inspecting = verbSecsFor(m, INSPECT_SECS);   // Mk canon (v32)
    addLog(m.spec.name + " being inspected - " + m.inspecting + "s of downtime to look.");
    paint();
    return true;
  }

  /* SERVICE: the last rung - it always fixes, and it invoices. A wallet that
     cannot cover it goes ON LOAN: the balance turns negative and earnings pay
     the debt down before they pile up. That keeps service always available -
     the v22 soft-lock (broke player, dead line, no way back) stays impossible,
     now by credit instead of by making the work free. */
  function service(id) {
    touchPlant();
    var m = machine(id);
    if (m.servicing > 0 || m.restarting > 0 || m.inspecting > 0 || m.lockout > 0) return false;
    var hadFunds = G.coins >= SERVICE_COST;
    G.coins -= SERVICE_COST;
    m.servicing = verbSecsFor(m, SERVICE_SECS);   // Mk canon: the crew works faster on better iron
    tape("service", { m: m.id, invoiced: SERVICE_COST, onLoan: !hadFunds,
      wasHealthy: m.cond === "none" });
    if (!hadFunds) {
      addLog("The crew invoiced " + SERVICE_COST + " you did not have - it is on loan. " +
        "Earnings pay the debt before they pile up.");
    }
    if (m.cond === "none") {
      G.wasted = (G.wasted || 0) + 1;
      addLog(m.spec.name + " sensor checked out fine - " + SERVICE_COST + " coins and " +
        m.servicing + "s of production spent finding that out.");
      paint();
      return true;
    }
    G.serviced += 1;
    // crediting the save to whoever actually told you, per the replay
    if (m.picoRead && m.picoRead.kind === "caught") G.saves.pico += 1;
    else if (m.nanoRead && m.nanoRead.kind === "resolved") G.saves.nano += 1;
    addLog(m.spec.name + " sensor being serviced - back in " + m.servicing + "s.");
    paint();
    return true;
  }

  function addLog(copy) { G.log.unshift(copy); G.log = G.log.slice(0, 6); }

  /* =====================================================================
     THE SESSION TAPE (v29). The founder asked "are you able to see the
     logs of how i'm playing" - so the game keeps one: an in-memory event
     tape of everything that happened, timestamped on the run clock.
     ALL LOCAL: it lives in this page, persists (last three sessions) in
     this browser's localStorage, and leaves the machine only if the
     player downloads it and shares the file themselves.
     ===================================================================== */
  var TAPE_CAP = 2000;               // FIFO: old events fall off the front
  var TAPE = [];
  function tape(type, data) {
    var e2 = { t: Math.round(G.elapsed * 10) / 10, type: type };
    if (data) for (var k2 in data) if (Object.prototype.hasOwnProperty.call(data, k2)) e2[k2] = data[k2];
    TAPE.push(e2);
    if (TAPE.length > TAPE_CAP) TAPE.splice(0, TAPE.length - TAPE_CAP);
  }
  function tapeSummary() {
    var up = G.runTime ? G.upTime / G.runTime : 1;
    return {
      shipped: Math.floor(G.cookies), burnt: Math.floor(G.spoiled),
      uptime: Math.round(up * 100) + "%",
      caughtInTime: G.incidents.caught, lineStopped: G.incidents.missed,
      rightVerbFirstTry: G.diag.first + "/" + G.diag.total,
      calledByTheModels: G.modelCalled, savedByYou: G.humanSaves,
      coins: Math.floor(G.coins), onLoan: G.coins < 0,
      contractLevel: G.contract.level, contractsLost: G.contractsLost || 0,
      secondsToFirstPico: G.firstPicoAt == null ? null : Math.round(G.firstPicoAt),
      secondsPlayed: Math.round(G.elapsed),
    };
  }
  function buildTape() {
    return {
      what: "THE COOKIE LINE · session tape",
      build: GAME_BUILD,
      exportedAt: new Date().toISOString(),
      honesty: "model words in these events are replayed record fields from the " +
        "recorded bench export. The plant itself is game simulation. This file " +
        "was written locally. Nothing leaves your machine except questions you " +
        "send to Ping over the plant radio - and those carry only your line's " +
        "summary numbers.",
      summary: tapeSummary(),
      events: TAPE.slice(),
    };
  }
  /* persisted on the way OUT (pagehide/hidden), never per tick - localStorage
     writes are synchronous and the frame loop should not pay for them */
  function persistTape() {
    if (!TAPE.length) return;
    try {
      var store = JSON.parse(window.localStorage.getItem("clTapes") || "[]");
      if (!Array.isArray(store)) store = [];
      store.push({ savedAt: new Date().toISOString(), summary: tapeSummary(), events: TAPE.slice(-600) });
      while (store.length > 3) store.shift();
      window.localStorage.setItem("clTapes", JSON.stringify(store));
    } catch (err) { /* quota or private mode - the tape is a courtesy, not a dependency */ }
  }
  function downloadTape() {
    var blob = new Blob([JSON.stringify(buildTape(), null, 1)], { type: "application/json" });
    var url = URL.createObjectURL(blob);
    var a = document.createElement("a");
    a.href = url; a.download = "cookie-line-session-tape.json";
    document.body.appendChild(a);
    a.click();
    window.setTimeout(function () {
      if (a.parentNode) a.parentNode.removeChild(a);
      URL.revokeObjectURL(url);
    }, 400);
    tape("export", {});
    addLog("Session tape saved to your downloads - it stays on your machine unless you share it.");
  }

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
    /* v32 lanes: a tight screen (or reduced motion) halves the spoken-word
       budget - cached queries, read per paint */
    if (typeof window.matchMedia === "function") {
      DOM.tightMq = window.matchMedia("(max-width: 380px)");
      DOM.motionMq = window.matchMedia("(prefers-reduced-motion: reduce)");
    }
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
    /* CAUGHT rides beside BURNT: what the models called in time, next to
       what slipped through - the two sides of the same ledger */
    DOM.caught = el("b", null, "0");
    DOM.statEls = {};
    [["COINS", DOM.coins], ["COOKIES", DOM.cookies2], ["BURNT", DOM.burnt],
     ["CAUGHT", DOM.caught], ["PER SEC", DOM.rate], ["BEST RUN", DOM.best]]
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

    /* the ticker: the radio's newest line, always visible - the collapsed
       LINE RADIO kept the WHY of a failed verb and every loan invoice out
       of sight (playtest round 2) */
    DOM.ticker = el("p", "cl-ticker");
    DOM.ticker.setAttribute("aria-live", "polite");
    root.appendChild(DOM.ticker);

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

    /* MICRO's SITE BOARD, on the factory wall - lights up when Micro is
       bought, mirrors the desk card (same pure function), and gives the
       site brain a physical presence the way Pico's badge gave it one */
    DOM.siteBoard = el("div", "clf-siteboard");
    DOM.siteBoard.hidden = true;
    floor.appendChild(DOM.siteBoard);

    /* THE GIGA UNIT - hidden until Giga is bought, then a real presence:
       it patrols, inspects, attends dial moves, and carries the radio */
    DOM.unit = el("div", "clf-unit");
    DOM.unit.hidden = true;
    DOM.unitArt = el("span", "clf-unit__art");
    DOM.unitArt.setAttribute("aria-hidden", "true");
    DOM.unit.appendChild(DOM.unitArt);
    DOM.unitSay = el("div", "clf-unit__say");
    DOM.unitSay.hidden = true;
    DOM.unit.appendChild(DOM.unitSay);
    DOM.unitTalk = btn("TALK", "clf-unit__talk", openChat);
    DOM.unitTalk.title = "ask about your line - answered live by Ping over the plant radio";
    DOM.unit.appendChild(DOM.unitTalk);
    floor.appendChild(DOM.unit);

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
    /* v40: the cost column leads with the PRICE on every row, in the same
       grammar the buttons now use ("FREE" or a coin figure). The old column
       mixed "free" on one row with a bare downtime on the next, so which
       verbs actually cost money had to be inferred from what was missing. */
    [["ADJUST (the dial)", "process out of its band - too fast, too hot", "FREE · no downtime · never locks"],
     ["RESTART", "a stuck or dropped-out sensor, usually; noise rarely", "FREE · " + VERBS.restart.secs + "s down"],
     ["CLEAN", "a noisy pickup (interference, dirt) - nothing else", "FREE · " + VERBS.clean.secs + "s down"],
     ["RECALIBRATE", "a drifting sensor, specifically", VERBS.recal.cost + " coins · " + VERBS.recal.secs + "s down"],
     ["INSPECT", "fixes nothing - reveals what is actually wrong", "FREE · " + INSPECT_SECS + "s down · never locks"],
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
      LOCKOUT_SECS + "s. Nano quotes this card instantly. INSPECT learns it the slow way. " +
      /* v40 ARITHMETIC FIX: MK_FAULT_MULT is an INTERVAL multiplier, so the
         old copy turned x4.0 into "~300% less often" - which is not a
         quantity that exists. It is stated as the multiplier it literally
         is, which cannot be miscomputed. */
      "Better iron is easier iron. A Mk II runs " + MK_FAULT_MULT[1] +
      "× longer between faults, and every verb runs " +
      Math.round((1 - MK_VERB_MULT[1]) * 100) + "% quicker. A Mk III runs " +
      MK_FAULT_MULT[2] + "× longer between faults, with verbs " +
      Math.round((1 - MK_VERB_MULT[2]) * 100) + "% quicker."));
    root.appendChild(maint);

    /* the desk views (site / plant / results) */
    root.appendChild(desk());

    /* THE FACTORY CERTIFICATE - the campaign goal, hung like a plaque.
       Checklist plus the proof clock; game arithmetic throughout. */
    var cert = el("div", "cl-cert");
    var certHead = el("div", "cl-cert__head");
    certHead.appendChild(el("b", null, "FACTORY CERTIFICATE"));
    certHead.appendChild(el("span", null, "the goal: a plant that runs itself, and proves it"));
    cert.appendChild(certHead);
    DOM.certRows = el("ul", "cl-cert__rows");
    cert.appendChild(DOM.certRows);
    DOM.certStamp = el("b", "cl-cert__stamp", "FACTORY CERTIFIED");
    DOM.certStamp.hidden = true;
    cert.appendChild(DOM.certStamp);
    cert.appendChild(el("i", "cl-view__note",
      /* said "the clock starts over" while the checklist hint eight lines up
         said "a 20s setback, not a restart" - and the code does the setback
         (touchPlant cuts CERT_TOUCH_SETBACK, it does not zero the run). */
      "the checklist is the game's own - stepping in during the proof costs " +
      CERT_TOUCH_SETBACK + "s, it does not start the clock over"));
    root.appendChild(cert);

    /* honesty footer */
    var note = el("p", "cl-note");
    note.appendChild(el("b", null, "MODEL BEHAVIOR: RECORDED REPLAY"));
    note.appendChild(document.createTextNode(
      " · what Pico and Nano say is drawn from real records in the recorded bench export, " +
      "misses included. THE PLANT ITSELF IS GAME SIMULATION: the cookies, coins, bands, wear and " +
      "throughput are invented for play. Micro and Giga read the game's own numbers - " +
      "no recorded run exists for those tiers."));
    root.appendChild(note);

    /* log */
    var log = el("details", "cl-log");
    log.appendChild(el("summary", null, "LINE RADIO"));
    DOM.log = el("ol");
    log.appendChild(DOM.log);
    /* THE SESSION TAPE's download lives with the radio: every event of the
       run as JSON, with a plain-language summary up top. All local. */
    var tapeBtn = btn("SESSION TAPE · download", "cl-act cl-tapebtn", downloadTape);
    tapeBtn.title = "Every event of this run, timestamped, as a JSON file - it stays " +
      "on your machine; download and share it if you want.";
    log.appendChild(tapeBtn);
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

    /* THE PLANT RADIO - chat through the Unit, answered by PING. The header
       carries the whole honesty story so no reply can be misread as Giga's:
       Ping is the live concierge, the Unit only holds the microphone. */
    DOM.chatOver = el("div", "clf-shopover clf-chatover");
    DOM.chatOver.hidden = true;
    var chatCard = el("div", "clf-shopcard clf-chatcard");
    chatCard.setAttribute("role", "dialog");
    chatCard.setAttribute("aria-modal", "false");
    chatCard.setAttribute("aria-label", "The plant radio - ask Ping about your line");
    var chatHead = el("div", "cl-desk__head");
    chatHead.appendChild(el("b", null, "THE PLANT RADIO"));
    chatHead.appendChild(el("span", null,
      "PING \u00b7 live over the Tower relay - speaking through the plant radio"));
    DOM.chatClose = btn("CLOSE", "cl-run", closeChat);
    chatHead.appendChild(DOM.chatClose);
    chatCard.appendChild(chatHead);
    chatCard.appendChild(el("p", "cl-chat__sub",
      "Ping is RogerAI's concierge, not a Wave model - the Unit just carries the radio. " +
      "Your question goes out with your line's summary numbers; nothing else leaves your machine."));
    DOM.chatList = el("ol", "cl-chat__list");
    chatCard.appendChild(DOM.chatList);
    var chatForm = el("form", "cl-chat__form");
    DOM.chatInput = el("input", "cl-chat__input");
    DOM.chatInput.type = "text";
    DOM.chatInput.maxLength = 200;
    DOM.chatInput.placeholder = "ask about your line - what should I upgrade next?";
    DOM.chatInput.setAttribute("aria-label", "Your question for Ping");
    chatForm.appendChild(DOM.chatInput);
    DOM.chatSend = btn("SEND", "cl-run cl-chat__send", function () {});
    DOM.chatSend.type = "submit";
    chatForm.appendChild(DOM.chatSend);
    chatForm.addEventListener("submit", function (e) {
      e.preventDefault();
      if (sendChat(DOM.chatInput.value)) DOM.chatInput.value = "";
    });
    chatCard.appendChild(chatForm);
    DOM.chatOver.appendChild(chatCard);
    root.appendChild(DOM.chatOver);

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

    /* the CERTIFIED moment: bigger than a contract - the campaign win. It
       does not pause the line, because the whole point is that the plant
       keeps running with nobody's hands on it. */
    DOM.certOver = el("div", "clf-shopover clf-certover");
    DOM.certOver.hidden = true;
    var certCard = el("div", "clf-shopcard clf-wincard clf-certcard");
    certCard.setAttribute("role", "dialog");
    certCard.setAttribute("aria-modal", "false");
    certCard.setAttribute("aria-label", "Factory certified");
    certCard.appendChild(el("b", "cl-win__stamp cl-win__stamp--cert", "FACTORY CERTIFIED"));
    certCard.appendChild(el("span", "cl-cert__sub",
      "every machine upgraded, every dial handed over - and it just ran " +
      Math.round(CERT_PROOF_SECS / 60) + " minutes on its own. The line below is still going."));
    DOM.certStats = el("div", "cl-results cl-win__stats");
    certCard.appendChild(DOM.certStats);
    certCard.appendChild(el("i", "cl-view__note",
      "lifetime numbers, accumulated as you played - game arithmetic, not a model claim"));
    DOM.certKeep = btn("KEEP IT RUNNING", "cl-run", function () {
      DOM.certOver.hidden = true; paint();
    });
    certCard.appendChild(DOM.certKeep);
    DOM.certOver.appendChild(certCard);
    root.appendChild(DOM.certOver);

    host.appendChild(root);
  }

  /* ---- a machine, standing on the floor -------------------------------- */
  function machineBlock(m) {
    var s = DOM.stations[m.id] = {};
    var block = el("div", "clf-machine clf-machine--" + m.id);
    /* the sprite follows the tier: data-mk picks the committed Mk plate in
       CSS, and the badge/lamp/plate anchors ride the block so they track
       whatever size the new art takes (v29) */
    block.dataset.mk = String(m.tier + 1);
    s.block = block;

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

    /* v35: the NEEDS YOU badge - rides the lamp's own lane, shown only by
       needsHuman() so it can never contradict the automation */
    s.yours = el("span", "clf-yours", "NEEDS YOU · INSPECT");
    s.yours.hidden = true;
    block.appendChild(s.yours);

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

    /* the gateway's attention, drawn: this tag sits on whichever machine
       the patrol is watching (belief-driven - it renders from watchTarget(),
       which never peeks at the game's secret) */
    s.gwtag = el("span", "clf-gwtag");
    s.gwtag.hidden = true;
    block.appendChild(s.gwtag);

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
    input.addEventListener("input", function () { m.set = Number(input.value); touchPlant(); paint(); });
    // the tape records the SETTLED value (change, not every drag tick)
    input.addEventListener("change", function () { tape("dial", { m: m.id, to: m.set, by: "player" }); });
    s.input = input;
    ctl.appendChild(input);
    s.setTxt = el("b", "cl-ctl__v", "");
    ctl.appendChild(s.setTxt);
    card.appendChild(ctl);

    /* THE ACTION LADDER, in cost order. The dial above is ADJUST (free, fixes
       process problems, never locked). Then the fixing verbs, each honest to
       the fault taxonomy; INSPECT is diagnosis, not maintenance, so it stays
       live even through a lockout; SERVICE is the sure thing that invoices.
       Choosing a verb the doctrine rules out locks maintenance for a minute -
       the price of guessing when you could have asked. */
    /* v40 - FREE-VS-PAID IS A WORD, not a border style. The row encoded it
       as dashed-vs-solid, which nothing on the page explains and which this
       site already spends on "empty slot" (buy tags, the Pico mount). Every
       verb now carries its price in one grammar - "· FREE" or "· 10" - so
       the cheapest correct move is readable at a glance; the dashed edge
       stays as a quiet echo of the same fact, no longer the only carrier. */
    var acts = el("div", "cl-acts");
    s.restart = btn("RESTART \u00b7 FREE", "cl-act cl-act--restart", function () { restart(m.id); });
    s.restart.title = "Free, " + VERBS.restart.secs + "s down. Usually re-seats a stuck or " +
      "dropped-out sensor; rarely helps noise; never fixes drift or railing.";
    s.clean = btn("CLEAN \u00b7 FREE", "cl-act cl-act--clean", function () { clean(m.id); });
    s.clean.title = "Free, " + VERBS.clean.secs + "s down. Clears a noisy pickup " +
      "(interference, dirt) - and nothing else.";
    s.recal = btn("RECAL · " + VERBS.recal.cost, "cl-act cl-act--recal", function () { recal(m.id); });
    s.recal.title = VERBS.recal.cost + " coins, " + VERBS.recal.secs + "s down. THE fix for a " +
      "drifting sensor; useless against anything else.";
    s.inspect = btn("INSPECT \u00b7 FREE", "cl-act cl-act--inspect", function () { inspect(m.id); });
    s.inspect.title = "Free, " + INSPECT_SECS + "s down, fixes nothing: you look at the sensor " +
      "and learn what is actually wrong - what Nano tells you instantly. Never locked out.";
    s.service = btn("SERVICE \u00b7 " + SERVICE_COST, "cl-act cl-act--service", function () { service(m.id); });
    s.service.title = "Always fixes everything, railed included. Costs " + SERVICE_COST +
      " coins - taken on loan if the wallet is short.";
    s.upgrade = btn("UPGRADE", "cl-act", function () { buyTier(m.id); });
    [s.restart, s.clean, s.recal, s.inspect, s.service, s.upgrade]
      .forEach(function (b) { acts.appendChild(b); });
    card.appendChild(acts);

    /* THE MODEL SLOT - nano advice, autonomy, provenance - sits BELOW the
       action row (v29, founder screenshot: advice blocks mounting above the
       buttons shoved them up and down every state change). Everything above
       the buttons is fixed-height, so the actions never move; the slot's
       weather grows downward into its own reserved space. LAYOUT STABILITY
       is test-locked on this order - do not move the buttons back under
       variable-height content. */
    s.slot = el("div", "cl-slot");
    card.appendChild(s.slot);

    return card;
  }

  function desk() {
    var d = el("section", "cl-desk");
    var head = el("div", "cl-desk__head");
    head.appendChild(el("b", null, "OPERATOR DESK"));
    head.appendChild(el("span", null, "the Wave desk · each tier tells you more"));
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
    /* v32 (playtest round 3: a hands-off session sat frozen ten minutes
       behind this card): the celebration waits a generous beat for a click,
       then dismisses ITSELF the same way the button does - the line resumes
       and the next offer moves to the desk. A pause, not a hostage. */
    if (G.winAutoClose && window.clearTimeout) window.clearTimeout(G.winAutoClose);
    G.winAutoClose = window.setTimeout(function () { dismissWin(); }, 45000);
    DOM.winOver.hidden = false;
    if (DOM.crates) DOM.crates.classList.add("is-stamped");
    // the run's numbers, laid out like the results view: game arithmetic
    DOM.winStats.textContent = "";
    var up = G.runTime ? (G.upTime / G.runTime) * 100 : 100;
    [["SHIPPED", String(Math.floor(G.cookies))],
     ["BURNT", String(Math.floor(G.spoiled))],
     ["UPTIME", up.toFixed(0) + "%"],
     ["CAUGHT IN TIME", String(G.incidents.caught)],
     /* the denominator is incidents CLEARED BY A VERB (G.diag.total only
        increments on a successful clear), so an incident you serviced or left
        broken never enters it. The label used to imply a rate over all
        incidents; it names its own denominator now. */
     ["FIRST TRY, OF FIXES", G.diag.total ? G.diag.first + "/" + G.diag.total : "-"],
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
    if (G.winAutoClose) { if (window.clearTimeout) window.clearTimeout(G.winAutoClose); G.winAutoClose = null; }
    DOM.winOver.hidden = true;
    G.contractDone = true;            // the offer moves to the goals + shop
    if (G.winWasRunning) { G.running = true; lastT = 0; }
    paint();
  }

  /* how many cookies one order of this level asks for - the same 2.5× ladder
     nextTargetOf climbs, counted as a SIZE so a re-offered contract after a
     buyer walk can start from wherever the cookie count already is */
  function contractSize(level) {
    var size = 100;
    for (var i = 1; i < level; i++) size = Math.round(size * 2.5 / 10) * 10;
    return size;
  }
  var SLA_BAR = 0.4;       // contract 2 up: the buyer expects 40%+ uptime
  function buyerWalks() {
    var lost = G.contract;
    G.contractsLost += 1;
    tape("contract", { event: "lost", level: lost.level,
      uptimeInWindow: Math.round((G.slaClock.up / Math.max(G.slaClock.run, 1e-6)) * 100) });
    /* plain and warm: what happened, what it cost, and that the desk is
       never empty - a fresh order of the same size starts from here */
    addLog("The buyer walked - the line spent too much of the last minute down " +
      "(they expect " + Math.round(lost.sla * 100) + "%+ uptime). No completion " +
      "bonus this time. A fresh order for " + contractSize(lost.level) +
      " cookies is already on the desk.");
    G.contract = { target: Math.ceil(G.cookies / 10) * 10 + contractSize(lost.level),
      level: lost.level, creep: lost.creep, sla: lost.sla };
    G.won = false;
    G.contractDone = false;
    G.slaClock = { run: 0, up: 0 };
  }

  function nextContract() {
    var next = nextTargetOf(G.contract);
    G.contract = { target: next, level: G.contract.level + 1,
      creep: G.contract.creep * 1.35, sla: SLA_BAR };
    G.slaClock = { run: 0, up: 0 };
    tape("contract", { event: "accepted", level: G.contract.level, target: next });
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
    /* a raised bubble must die with its cause - the slot only rebuilds on
       content change, so this runs every frame (playtest: a "not sure"
       outlived its fault on a healthy, RUNNING machine) */
    if (s.bubble && !s.bubble.hidden) {
      var stillRaised = m.pico && m.picoRead &&
        (m.picoRead.kind === "unsure" || m.picoRead.said !== "none");
      // v32: the lane budget can also silence a live bubble - it dies too
      if (!stillRaised || (DOM.bubbleShow && !DOM.bubbleShow[m.id])) s.bubble.hidden = true;
    }
    var shown = shownValue(m);
    m.lastShown = shown;   // the cached display the trust checks read (v31)
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
    if (s.yours) s.yours.hidden = !needsHuman(m);

    /* the machine's physical tells - presentation of sim state, nothing more:
       a stopped machine's art dims and its working shimmer ends; the oven's
       porthole warmth follows whether it is actually baking in band */
    if (s.art) {
      s.art.classList.toggle("is-dead", m.stopped);
      s.art.classList.toggle("is-working", !m.stopped && G.running);
    }
    /* THE UPGRADE MOMENT (v29): when the tier changes, data-mk swaps the
       committed Mk plate in - and every anchor (lamp, badge, plate, bubble)
       rides the block, so they track the new art for free. The reveal beat
       is a short CSS animation; reduced motion gets the clean swap and the
       radio line carries the announcement. */
    if (s.block) {
      var mkNow = String(m.tier + 1);
      if (s.block.dataset.mk !== mkNow) s.block.dataset.mk = mkNow;
      s.block.classList.toggle("is-upgrading",
        !REDUCED && !!m.upgradeAt && G.elapsed - m.upgradeAt < 1.6);
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

    // the patrol, visible: the tag rides watchTarget() and nothing else
    if (s.gwtag) {
      var watchedHere = G.nano && !m.pico && watchTarget() === m.id;
      s.gwtag.hidden = !watchedHere;
      if (watchedHere) {
        s.gwtag.textContent = G.nanoWatch
          ? "gateway watching"
          : "gateway watching \u00b7 sweep " +
            (G.sweepLeft != null ? Math.max(1, Math.ceil(G.sweepLeft)) : NANO_SWEEP_SECS) + "s";
      }
    }
    /* an autonomy move floats off the machine it happened to, tier-coloured
       - the handover is visible on the floor, not only in the panel */
    if (m.autoNoteAt && s.noteAt !== m.autoNoteAt && m.autoNote && !m.unitTagHold) {
      s.noteAt = m.autoNoteAt;
      var atag = el("i", "clf-autotag", m.autoNote);
      if (m.autoTier) atag.dataset.tier = m.autoTier;
      s.gwtag.parentNode.appendChild(atag);
      window.setTimeout(function () { if (atag.parentNode) atag.parentNode.removeChild(atag); }, 2600);
    }

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
      (DOM.priced = DOM.priced || []).push({ b: b, cost: MODEL_PRICE.pico, kind: "pico" });
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
        var raised = !!r2 && (r2.kind === "unsure" || r2.said !== "none") &&
          (!DOM.bubbleShow || DOM.bubbleShow[m.id]);   // v32 lane budget
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
            /* THE DOUBLE-MISS HANDS OFF TO THE HUMAN (v27, re-voiced v28).
               The founder hit this exact wall ("even Wave Nano can't help")
               and honesty without a next move is a dead end - so the advice
               names the next move, plainly, and INSPECT lights below, the
               way known-kind lights a verb. */
            advice = "says \u201c" + m.nanoRead.said + "\u201d - " +
              (m.nanoDirect ? "and per the recording, that is wrong."
                            : "and per the recording, the senior got this one wrong too.") +
              " Both models missed this one. " +
              (m.auto && G.micro > 0
                ? "The site brain is on it - a remote diagnostic is coming. " +
                  "INSPECT yourself if you want it faster."
                : "This one's yours: INSPECT it and see for yourself.");
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
       player may give this one away - and take it back at any time. Before
       any model can, the toggle still shows, locked - so the destination
       (a plant that drives itself) is visible before it unlocks.
       v40 - QUIET UNTIL IT IS CLOSE. The locked row used to render from the
       first second, on all three machines: three dead buttons and three
       "unlocks with WAVE MICRO" notes advertising a purchase two rungs away,
       stacked under a deck whose whole opening lesson is "read the dial
       yourself". It now waits for the desk: with Nano bought, Micro IS the
       next rung (recommendedNext agrees), so the destination shows exactly
       when it becomes buyable. Nothing is hidden from the shop meanwhile -
       the overlay lists Micro the whole time, marked NEEDS NANO. */
    if (autonomyReach() === 0 && G.nano) {
      var ghost = el("div", "cl-auto is-locked");
      var gb = btn("LET THE MODELS DRIVE", "cl-auto__btn", null);
      gb.disabled = true;
      gb.title = "Unlocks with Wave Micro - it can hold one dial. Giga can hold them all.";
      ghost.appendChild(gb);
      ghost.appendChild(el("i", "cl-auto__note", "unlocks with WAVE MICRO"));
      s.slot.appendChild(ghost);
    }
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
    var mountKey = (m.pico ? "pico" : "empty") + "~" + (m.cond !== "none" && m.pico ? "lit" : "") +
      "~" + m.picoCatches;
    if (s.mountKey !== mountKey) {
      s.mountKey = mountKey;
      s.mount.textContent = "";
      if (m.pico) {
        var badge = el("span", "clf-badge");
        badge.title = m.picoCatches
          ? "Wave Pico \u00b7 " + m.picoCatches + " fault" + (m.picoCatches === 1 ? "" : "s") +
            " caught on this machine"
          : "Wave Pico, mounted on this machine - the tireless watcher";
        badge.appendChild(el("i", "clf-badge__art"));
        badge.appendChild(el("b", null, "PICO"));
        /* the vigilance tally: every fault this Pico named in time */
        if (m.picoCatches > 0) {
          badge.appendChild(el("s", "clf-badge__n", "\u00d7" + m.picoCatches));
        }
        if (G.elapsed - m.catchAt < 2 && m.catchAt > 0) badge.classList.add("is-glint");
        s.mount.appendChild(badge);
      } else {
        var tag = btn("+ PICO · " + MODEL_PRICE.pico, "clf-buytag", function (e) {
          e.stopPropagation();
          buyPico(m.id);
        });
        tag.title = "Mount a Wave Pico here - instant, always-on coverage for this machine, " +
          "and it frees the gateway to mind the rest of the site.";
        (DOM.priced = DOM.priced || []).push({ b: tag, cost: MODEL_PRICE.pico, kind: "pico" });
        s.mount.appendChild(tag);
      }
      if (s.bubble) s.bubble.hidden = !(m.pico && m.cond !== "none" && m.picoRead &&
        (!DOM.bubbleShow || DOM.bubbleShow[m.id]));   // v32 lane budget
    }
    // the nameplate sells the next tier in place
    var nextTier = TIERS[m.id][m.tier + 1];
    if (s.plateBuy) {
      s.plateBuy.hidden = !nextTier;
      if (nextTier) {
        s.plateBuy.textContent = nextTier.name.toUpperCase() + " · " + nextTier.price;
        s.plateBuy.disabled = G.coins < nextTier.price;
        s.plateBuy.title = "Upgrade: faster, and a wider band (" + nextTier.lo + "-" + nextTier.hi +
          " " + m.spec.sensor.unit + ") so a small lie is less fatal." +
          (G.coins >= nextTier.price ? " You can afford this now." : "");
        /* GLOW DISCIPLINE (v29): MODELS FIRST. A machine's Mk tag stays
           QUIET - no glow at all - until that machine has its Pico, so an
           affordable Mk never competes with the model ladder; after the
           Pico it lights softly when affordable, and carries the STRONG
           glow only when the Mk line is the recommendation (recommendedNext:
           model ladder done). */
        var affordTier = G.coins >= nextTier.price && m.pico;
        var recoTier = (DOM.recoKinds || {})["tier"];
        s.plateBuy.classList.toggle("is-afford", affordTier);
        s.plateBuy.classList.toggle("is-reco", affordTier && !!recoTier);
        s.upgrade.classList.toggle("is-afford", affordTier);
        s.upgrade.classList.toggle("is-reco", affordTier && !!recoTier);
      } else {
        /* v32 (playtest): a bought-out Mk tag went hidden but kept its
           is-afford glow classes in the DOM - strip them with the tier */
        s.plateBuy.classList.remove("is-afford", "is-reco");
        if (s.upgrade) s.upgrade.classList.remove("is-afford", "is-reco");
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
      s.inspect.textContent = m.inspecting > 0
        ? "INSPECTING " + m.inspecting.toFixed(0) + "s" : "INSPECT \u00b7 FREE";
      s.inspect.disabled = busy;
    } else {
      var running = m.restarting > 0 ? m.fixVerb : null;
      s.restart.textContent = running === "restart" ? "RESTARTING\u2026" : "RESTART \u00b7 FREE";
      s.clean.textContent = running === "clean" ? "CLEANING\u2026" : "CLEAN \u00b7 FREE";
      s.recal.textContent = running === "recal" ? "RECAL\u2026" : "RECAL \u00b7 " + VERBS.recal.cost;
      s.inspect.textContent = m.inspecting > 0
        ? "INSPECTING " + Math.ceil(m.inspecting) + "s" : "INSPECT \u00b7 FREE";
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
    s.inspect.classList.toggle("is-yours", needsHuman(m));
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
      filled ? "contract filled"
        : G.contract.sla > 0
          ? "this buyer expects " + Math.round(G.contract.sla * 100) +
            "%+ uptime - too long down and they walk (a fresh order always follows)"
          : "keep every needle inside its marked band - conditions creep"]);
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
      rows.push(["NICE", "you caught what both models missed on the " + G.ackWhat,
        "nice save - that one needed a person"]);
    } else {
      /* v31: under autonomy the same handoff is LOUD - the Unit is already
         rolling there; the chip names the machine and the move */
      var helpM = unitHelpTarget();
      if (helpM) {
        rows.push(["HELP", "the " + helpM.spec.name.toLowerCase() + " needs your eyes",
          helpM.lockout > 0 ? (helpM.inspected
              ? "wrong verb locked it and the kind is known - it clears in " + Math.ceil(helpM.lockout) + "s"
              : "the crew's locked out - INSPECT it, inspecting never locks")
            : "automation can't clear this one - INSPECT it"]);
      }
      var handedOffTo = G.machines.filter(function (mm) {
        return chainMissed(mm) && !mm.inspected && !(helpM && helpM.id === mm.id);
      });
      if (handedOffTo.length) {
        rows.push(["WATCH", "both models missed the " + handedOffTo[0].spec.name.toLowerCase(),
          "this one's yours - INSPECT it and see for yourself"]);
      }
    }
    if (filled) {
      /* the win stops advertising Picos (playtest bug 1) - the next thing
         is the next contract, offered on the win card and again here */
      rows.push(["NEXT", "NEW CONTRACT · " + nextTargetOf(G.contract) + " cookies",
        "take it from the results card or the shop - conditions creep faster"]);
      /* and the campaign goal points at its next unmet line */
      var cn = certNext();
      if (cn) {
        rows.push(["GOAL", cn.label,
          "the certificate: a factory that runs itself, upgraded end to end"]);
      }
    } else if (recommendedNext().length) {
      /* the NEXT row and the floor's strong glow share recommendedNext() -
         one source, test-locked, so the chip and the glowing tag agree */
      var reco = recommendedNext();
      var k0 = reco[0].kind;
      if (k0 === "pico") {
        rows.push(["NEXT", "WAVE PICO · " + MODEL_PRICE.pico,
          "puts a model on one machine so it tells you when its reading stops being trustworthy"]);
      } else if (k0 === "nano") {
        rows.push(["NEXT", "WAVE NANO · " + MODEL_PRICE.nano,
          "explains WHY and what to do. It works instantly through Picos, or watches one bare machine directly"]);
      } else if (k0 === "micro" && reco.length > 1) {
        rows.push(["NEXT", "MICRO · " + MODEL_PRICE.micro + " or GIGA · " + MODEL_PRICE.giga,
          "your call. Another Micro holds one more dial. Giga is one mind on the LINE, with its Unit inspecting what nobody names"]);
      } else if (k0 === "micro") {
        rows.push(["NEXT", "WAVE MICRO · " + MODEL_PRICE.micro,
          "the site view - and it can hold one knob for you"]);
      } else if (k0 === "giga") {
        rows.push(["NEXT", "WAVE GIGA · " + MODEL_PRICE.giga,
          "one mind on every dial - its Unit walks the floor, inspects faults nobody names, and carries a live radio"]);
      } else if (k0 === "tier") {
        rows.push(["NEXT", "MK UPGRADES",
          "the certificate wants every machine at Mk III - faster, wider bands"]);
      }
    } else if (autoCount() < G.machines.length) {
      rows.push(["NEXT", "HAND OVER THE LAST KNOBS",
        "let the models drive all three and the plant runs itself"]);
    } else if (G.cert.done) {
      rows.push(["DONE", "FACTORY CERTIFIED", "it runs itself - watch the results and keep it honest"]);
    } else if (certReady()) {
      /* the proof window: hands off, how far along, and the LIVE PACE - a
         clock silently counting over a dead plant read as a lie (playtest) */
      var pr = certPace();
      rows.push(["PROVE", "hands-off demonstration \u00b7 " +
        Math.floor(G.cert.run) + "s / " + CERT_PROOF_SECS + "s \u00b7 " +
        Math.round(pr.pct * 100) + "% " + (pr.onPace ? "- on pace" : "- below the bar"),
        "don't touch anything - the plant is proving it runs itself (touching costs 20s)"]);
    } else {
      var cn2 = certNext();
      rows.push(["GOAL", cn2 ? cn2.label : "THE PLANT RUNS ITSELF",
        "the certificate: a factory that runs itself, upgraded end to end"]);
    }
    if (G.incidents.missed) {
      var anyModel = G.nano || G.giga || G.micro > 0 ||
        G.machines.some(function (mm) { return mm.pico; });
      rows.push(["WATCH", G.incidents.missed + " incident" + (G.incidents.missed === 1 ? "" : "s") + " stopped the line",
        anyModel ? "a model that missed in the recording misses here too"
                 : "nobody was watching - a Pico would have been"]);
    }
    /* v38 (layout audit: "goal findable? No"): the campaign goal is the
       FIRST row, always - N/5 steps and the next unfinished item - and it is
       a button that scrolls to the plaque. SHIP reads as the sub-goal it is. */
    var items = certItems();
    var doneN = items.filter(function (it) { return it.done; }).length;
    var nxt = certNext();
    rows.unshift(["GOAL", G.cert.done ? "FACTORY CERTIFIED" : "FACTORY CERTIFICATE · " + doneN + "/" + items.length,
      G.cert.done ? "the plant runs itself, and proved it" : (nxt ? "next: " + nxt.label : "")]);
    DOM.goals.textContent = "";
    rows.slice(0, 4).forEach(function (r, i) {
      var li = el("li", "cl-goal" + (i === 0 ? " cl-goal--goal" : ""));
      li.appendChild(el("b", null, r[0]));
      li.appendChild(el("span", null, r[1]));
      li.appendChild(el("i", null, r[2]));
      if (i === 0) {
        li.setAttribute("role", "button"); li.tabIndex = 0;
        li.title = "the goal - jump to the certificate";
        var jump = function () { var c = DOM.certRows && DOM.certRows.parentNode; if (c && c.scrollIntoView) c.scrollIntoView({ behavior: "smooth", block: "start" }); };
        li.addEventListener("click", jump);
        li.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); jump(); } });
      }
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
      /* the Mk rows carry a small thumbnail of the committed plate the
         upgrade actually swaps in (v29) - the shop shows what you get */
      if (opts.thumb) {
        var th = el("i", "cl-node__thumb");
        th.dataset.m = opts.thumb.m;
        th.dataset.mk = String(opts.thumb.mk);
        th.setAttribute("aria-hidden", "true");
        n.appendChild(th);
      }
      n.appendChild(el("b", null, opts.name));
      n.appendChild(el("i", null, opts.owned ? opts.does : opts.promise));
      if (opts.owned) n.appendChild(el("span", "cl-node__on", "INSTALLED"));
      else if (opts.locked) n.appendChild(el("span", "cl-node__lock", opts.locked));
      else {
        var b = btn(opts.price, "cl-act", opts.buy);
        (DOM.priced = DOM.priced || []).push({ b: b, cost: opts.cost || 0, kind: opts.kind });
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
        /* v32 Mk canon, surfaced: a buyer should know the reliability they
           are buying, not just the speed */
        /* v40: the same interval-multiplier fix as the maintenance card -
           the shop was selling "~300% less often" on a Mk III row. */
        promise: next ? "faster, wider band (" + next.lo + "-" + next.hi + ") - and better iron: " +
          MK_FAULT_MULT[m.tier + 1] + "× longer between faults, every verb " +
          Math.round((1 - MK_VERB_MULT[m.tier + 1]) * 100) + "% quicker" : "",
        price: next ? "UPGRADE · " + next.price : "",
        cost: next ? next.price : 0,
        kind: "tier",
        thumb: { m: m.id, mk: next ? m.tier + 2 : m.tier + 1 },
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
        kind: "pico",
        buy: function () { buyPico(m.id); },
      }));
    });

    var col3 = el("div", "cl-branch");
    col3.appendChild(el("span", "cl-branch__head", "THE DESK"));
    [["nano", "WAVE NANO", "tells you WHY, and what to change. It works instantly through Picos, or watches one bare machine directly on its own sweep", "explains the fault and gives the fix. No Picos needed to start"],
     ["micro", "WAVE MICRO", "the site view - and it can hold one knob", "all three machines at once"],
     ["giga", "WAVE GIGA", "one mind on every dial - its Unit walks the floor and inspects what nobody names", "bottleneck, forecast, full autonomy, auto-inspection, a live radio to ask"]
    ].forEach(function (row) {
      var id = row[0];
      var locked = (id !== "nano" && !G.nano) ? "NEEDS NANO" : "";
      /* Micro stacks (up to three, one dial each) - and the shop is where the
         scale-out-vs-scale-up tradeoff gets said out loud: three Micros cost
         more than one Giga and still don't talk to each other. */
      var stacking = id === "micro" && G.micro > 0 && G.micro < MICRO_MAX;
      col3.appendChild(node({
        name: id === "micro" && G.micro > 0 ? "WAVE MICRO ×" + G.micro : row[1],
        tier: TIER_COLOUR[id],
        owned: id === "micro" ? (stacking ? false : G.micro > 0) : G[id],
        locked: locked,
        does: row[3],
        promise: stacking
          ? "one more dial held - but Micros don't talk to each other. Three of them is " +
            (MODEL_PRICE.micro * MICRO_MAX) + " coins of separate minds; Giga (" +
            MODEL_PRICE.giga + ") minds the LINE."
          : row[2],
        price: stacking ? "ANOTHER · " + MODEL_PRICE.micro : "BUY · " + MODEL_PRICE[id],
        cost: MODEL_PRICE[id],
        kind: id,
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
      /* THE WATCHING ROW IS STATE-DRIVEN (v29). The founder hit a row of
         all-disabled buttons ("what is this supposed to do") - so with full
         Pico coverage the selector does not render at all (the status line
         below says why), and with partial coverage only BARE machines are
         buttons; covered ones are non-button chips. If a Pico ever went
         away, watchOptions() would bring the row back. */
      var wo = watchOptions();
      if (wo.show) {
        var sel = el("div", "cl-watchsel");
        sel.setAttribute("role", "group");
        sel.setAttribute("aria-label", "Gateway direct watch");
        sel.appendChild(el("i", null, "WATCHING"));
        var autoB = btn("AUTO", "cl-watch" + (G.nanoWatch == null ? " is-on" : ""), function () {
          G.nanoWatch = null; touchPlant(); addLog("Gateway watch set to AUTO - it walks the machines that have no Pico."); paint();
        });
        autoB.setAttribute("aria-pressed", G.nanoWatch == null ? "true" : "false");
        sel.appendChild(autoB);
        wo.bare.forEach(function (mid) {
          var mm = machine(mid);
          var wb = btn(mm.spec.name, "cl-watch" + (G.nanoWatch === mm.id ? " is-on" : ""), function () {
            G.nanoWatch = mm.id;
            touchPlant();
            addLog("Gateway pointed at the " + mm.spec.name.toLowerCase() + ".");
            paint();
          });
          wb.setAttribute("aria-pressed", G.nanoWatch === mm.id ? "true" : "false");
          sel.appendChild(wb);
        });
        wo.covered.forEach(function (mid) {
          var chip = el("s", "cl-watch cl-watch--covered", machine(mid).spec.name);
          chip.title = "This machine has a Pico - its reports reach the gateway instantly, " +
            "so there is nothing to point the watch at.";
          sel.appendChild(chip);
        });
        gw.appendChild(sel);
      }
      var tgt = watchTarget();
      gw.appendChild(el("span", null, tgt
        ? (G.nanoWatch ? "Pinned on the " : "Patrolling - now at the ") +
          machine(tgt).spec.name.toLowerCase() +
          (G.sweepLeft != null ? " \u00b7 next sweep " + Math.max(1, Math.ceil(G.sweepLeft)) + "s" : "") +
          (gatewayBusy() ? " \u00b7 camped on trouble it spotted - other checks are waiting" : "")
        : "Every machine has a Pico - the gateway hears them all instantly."));
      gw.appendChild(el("i", "cl-view__note",
        "the gateway can read any machine - it just can't be in three places at once. " +
        "That's what the Picos are for."));
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
      var sb2 = siteBoard();   // the desk card and the wall board share one source
      var box = el("div", "cl-view cl-view--micro");
      box.appendChild(el("b", "cl-view__head",
        "WAVE MICRO" + (G.micro > 1 ? " ×" + G.micro : "") + " · SITE VIEW"));
      var tbl = el("div", "cl-view__rows");
      sv.rows.forEach(function (r, i2) {
        var row = el("div", "cl-view__row");
        row.appendChild(el("b", null, r.name));
        row.appendChild(el("span", null, r.tier + " · band " + r.band));
        row.appendChild(el("span", null, Math.round(sb2.rows[i2].up * 100) + "% up · headroom " + r.head.toFixed(1)));
        row.appendChild(el("i", null, r.stopped ? "STOPPED" : r.cond !== "none" ? "SENSOR " + CONDITION_WORD[r.cond].toUpperCase() : "ok"));
        tbl.appendChild(row);
      });
      box.appendChild(tbl);
      box.appendChild(el("span", "cl-view__worst", sb2.line.charAt(0).toUpperCase() + sb2.line.slice(1) + "."));
      box.appendChild(el("i", "cl-view__note",
        (G.micro > 1 && !G.giga
          ? "your " + G.micro + " Micros each mind one machine on their own clock - nobody is watching the line between them · "
          : "") +
        "arithmetic over the game's own numbers - Micro has no recorded run on this bench"));
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
      g.appendChild(el("span", null, pv.flagged
        ? pv.flagged + " sensor(s) caught lying - holding their dials until they're fixed."
        : "No sensor has been caught lying right now."));
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
      /* v31: the crown reads the room. The founder saw PLANT RUNNING ITSELF
         crowning 0.00/s and 33% uptime - triumph copy over a dying line. The
         same banner now tells the truth the results already show. */
      var upPct = G.runTime > 0 ? G.upTime / G.runTime : 1;
      var crown = upPct < 0.6
        ? "THE PLANT IS STRUGGLING - THE MODELS NEED YOU"
        : "PLANT RUNNING ITSELF · YOU ARE THE OPERATOR NOW";
      var crownEl = el("span", "cl-view__crown" + (upPct < 0.6 ? " is-strain" : ""), crown);
      res.insertBefore(crownEl, res.firstChild);
      DOM.deskView.insertBefore(res, DOM.deskView.firstChild);
    } else {
      DOM.deskView.appendChild(res);
    }
  }

  /* Rebuilding every control twelve times a second re-creates the very
     button under the player's cursor, so clicks get eaten and focus is lost.
     Structure is rebuilt only when discrete state actually changes; the
     numbers repaint every frame, and prices update on the buttons in place. */
  /* the Unit is painted per frame: position rides the CSS transition (its
     glide is chrome; reduced motion turns the transition off and it simply
     relocates), pose and say ride the sim state */
  function paintUnit() {
    if (!DOM.unit) return;
    var show = !!(G.giga && G.unit);
    DOM.unit.hidden = !show;
    if (!show) return;
    var u = G.unit;
    DOM.unit.style.left = UNIT_POS[u.going || u.at] + "%";
    DOM.unit.dataset.pose = u.going ? "roll" : u.pose;
    DOM.unit.dataset.dir = String(u.dir);
    /* v32 lanes: the radio's live exchange always shows (the player asked);
       everything else the Unit says obeys the shared bubble plan */
    var line = G.chatBusy ? "\u2026asking over the radio"
      : (!u.going && u.say && G.elapsed < u.sayUntil &&
         (!DOM.bubbleShow || DOM.bubbleShow.unit) ? u.say : "");
    DOM.unitSay.hidden = !line;
    if (line) DOM.unitSay.textContent = line;
    var help = unitHelpTarget();
    DOM.unitSay.classList.toggle("is-help", !!(help && u.at === help.id && line));
    /* the Unit at the floor's right edge speaks leftward and TALK flips to
       its other shoulder - words stay on the floor, off the desk controls */
    var atRight = UNIT_POS[u.going || u.at] >= 60;
    DOM.unit.classList.toggle("at-right", atRight);
  }

  function paintChat() {
    if (!DOM.chatList) return;
    DOM.chatList.textContent = "";
    G.chat.forEach(function (c2) {
      var li = el("li", "cl-chat__msg cl-chat__msg--" + c2.who);
      li.appendChild(el("b", null,
        c2.who === "you" ? "YOU"
          : c2.who === "ping" ? "PING \u00b7 LIVE over the Tower relay"
            : "THE UNIT \u00b7 radio quiet"));
      li.appendChild(el("span", null, c2.text));
      DOM.chatList.appendChild(li);
    });
    if (G.chatBusy) {
      var w = el("li", "cl-chat__msg cl-chat__msg--wait");
      w.appendChild(el("span", null, "\u2026asking over the radio"));
      DOM.chatList.appendChild(w);
    }
    DOM.chatList.scrollTop = DOM.chatList.scrollHeight;
    if (DOM.chatSend) DOM.chatSend.disabled = !!G.chatBusy;
  }

  function structureKey() {
    /* cert rows repaint on discrete cert change; the proof clock itself is
       painted per-frame straight into DOM refs (cheap text writes) */
    return [G.nano, G.micro, G.giga, G.incidents.missed, G.ready, G.coins < 0,
      G.machines.map(function (m) {
        return [m.pico, m.auto, m.cond, m.tier, m.lockout > 0, m.restarting > 0,
          m.healthyDraws, m.picoRead && m.picoRead.kind, m.nanoRead && m.nanoRead.kind].join(",");
      }).join("|"),
      G.cookies >= G.contract.target, G.contract.level, G.contract.target,
      G.contractsLost, G.won, G.contractDone,
      G.graceLeft > 0, G.taught, G.taughtCleared].concat([G.cert.done, certReady(), G.micro, G.modelCalled]).join("~");
  }

  function paint() {
    if (!DOM.stations) return;
    /* the recommendation, computed ONCE per paint: the goal chip's NEXT row,
       the stations' Mk tags, and the priced sweep below all read this same
       answer - the chip and the glowing tag can never disagree */
    DOM.recoKinds = {};
    recommendedNext().forEach(function (r0) { DOM.recoKinds[r0.kind] = true; });
    /* v32 annotation lanes: ONE spoken-word plan per paint - the machine
       bubbles and the Unit's mouth all consult this same answer */
    var tight = (DOM.tightMq && DOM.tightMq.matches) || (DOM.motionMq && DOM.motionMq.matches);
    DOM.bubbleShow = {};
    bubblePlan(bubbleCands(), tight ? 1 : 2).forEach(function (c0) { DOM.bubbleShow[c0.id] = true; });
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
    if (DOM.caught) {
      DOM.caught.textContent = G.modelCalled;
      DOM.statEls.CAUGHT.title = "faults a model named before the fix landed";
    }
    if (DOM.best) {
      var bestShow = Math.max(G.bestRun, G.cleanRun);
      DOM.best.textContent = bestShow >= 60
        ? Math.floor(bestShow / 60) + "m" + String(Math.floor(bestShow % 60)).padStart(2, "0") + "s"
        : Math.floor(bestShow) + "s";
    }
    DOM.cookies2.textContent = Math.floor(G.cookies);
    var pk = machine("packer");
    /* THE SAME NUMBER THE RESULTS PANEL PRINTS. This used to show the packer's
       UNSTARVED capacity - rateOf(pk) * 1.5 - which ignores whether the oven is
       actually feeding it, so the HUD read high the whole time an upstream
       machine was the bottleneck while the results panel's COOKIES/S showed
       what really shipped. Both now read the sampled actual rate. */
    var lastSample = G.history.length ? G.history[G.history.length - 1].rate : 0;
    DOM.rate.textContent = (pk.stopped ? 0 : lastSample).toFixed(1);
    paintUnit();
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
          /* the rate is 4/cookie normally and LOAN_RATE while in debt (see the
             coin credit in stepShip) - this floater hardcoded 4, so a player on
             loan was shown +80 while actually receiving +60. */
          var f = el("b", "clf-payout", "+" + (20 * (G.coins < 0 ? LOAN_RATE : 4)));
          DOM.crates.appendChild(f);
          window.setTimeout(function () { if (f.parentNode) f.parentNode.removeChild(f); }, 1400);
        }
      }
    }
    // owned desk models sit at the desk end of the floor
    /* THE CERTIFICATE PLAQUE: rows rebuild on discrete change; the proof
       clock writes its text every frame (a cheap textContent) */
    if (DOM.certRows) {
      var items = certItems();
      var certKey = items.map(function (it) { return it.key + (it.done ? "1" : "0"); }).join("");
      if (DOM.certKey !== certKey) {
        DOM.certKey = certKey;
        DOM.certRows.textContent = "";
        items.forEach(function (it) {
          var li = el("li", "cl-cert__row" + (it.done ? " is-done" : ""));
          li.appendChild(el("b", null, it.done ? "\u2713" : "\u00b7"));
          li.appendChild(el("span", null, it.label));
          if (it.key === "proof" && !it.done) {
            DOM.certProof = el("i", "cl-cert__clock", "");
            li.appendChild(DOM.certProof);
            if (it.hint) li.appendChild(el("i", "cl-cert__hint", it.hint));
          }
          DOM.certRows.appendChild(li);
        });
        if (DOM.certStamp) DOM.certStamp.hidden = !G.cert.done;
      }
      if (DOM.certProof && !G.cert.done) {
        var pr2 = certPace();
        DOM.certProof.textContent = certReady()
          ? Math.floor(G.cert.run) + "s / " + CERT_PROOF_SECS + "s hands-off · " +
            Math.round(pr2.pct * 100) + "% " + (pr2.onPace ? "- on pace" : "- below the bar")
          : "waiting on the checklist above";
      }
    }
    /* MICRO's wall board: visible when Micro is owned, refreshed once a
       second - the same pure siteBoard() the desk card reads */
    if (DOM.siteBoard) {
      DOM.siteBoard.hidden = !G.micro;
      if (G.micro) {
        var sbKey = Math.floor(G.elapsed);
        if (DOM.sbKey !== sbKey) {
          DOM.sbKey = sbKey;
          var sb = siteBoard();
          DOM.siteBoard.textContent = "";
          DOM.siteBoard.appendChild(el("b", "clf-siteboard__k", "SITE BOARD \u00b7 MICRO"));
          sb.rows.forEach(function (r2) {
            var row = el("span", "clf-siteboard__row");
            row.appendChild(el("i", null, r2.name));
            var bar = el("s", "clf-siteboard__bar");
            var fill = el("b", null, "");
            fill.style.width = Math.round(r2.up * 100) + "%";
            bar.appendChild(fill);
            row.appendChild(bar);
            row.appendChild(el("em", null, Math.round(r2.up * 100) + "%" +
              (r2.trend > 0.02 ? " \u25b4" : r2.trend < -0.02 ? " \u25be" : "")));
            row.title = r2.name + " uptime " + Math.round(r2.up * 100) + "% \u00b7 " +
              (r2.trend < -0.02 ? "headroom tightening" : r2.trend > 0.02 ? "headroom easing" : "steady");
            DOM.siteBoard.appendChild(row);
          });
          DOM.siteBoard.appendChild(el("i", "clf-siteboard__line", sb.line));
        }
      }
    }
    var deskKey = [G.nano, G.micro, G.giga].join("~");   // micro is its count
    if (DOM.floorDesk && DOM.deskKey !== deskKey) {
      DOM.deskKey = deskKey;
      DOM.floorDesk.textContent = "";
      DOM.floorDesk.appendChild(el("i", "clf-desk__art"));
      var owned = [["nano", G.nano ? 1 : 0], ["micro", G.micro], ["giga", G.giga ? 1 : 0]]
        .filter(function (p) { return p[1]; });
      var rack = el("span", "clf-desk__rack");
      owned.forEach(function (p) {
        // stacked Micros are separate boxes on the desk - scale-out, visibly
        for (var ci = 0; ci < p[1]; ci++) {
          var chipEl = el("b", "clf-desk__chip",
            p[0] === "micro" && p[1] > 1 ? "MICRO-" + (ci + 1) : p[0].toUpperCase());
          chipEl.dataset.tier = p[0];
          rack.appendChild(chipEl);
        }
      });
      /* the desk sells its own next model, right where it would sit. With
         Micros started and no Giga it offers BOTH (v29 - the founder could
         not buy a second Micro from here): another-Micro and Giga side by
         side, because scale-out-vs-scale-up is a genuine choice - the glow
         picks no winner between them. deskOffers() is the one rule. */
      deskOffers().forEach(function (offer) {
        var deskTag = btn("+ " + offer.toUpperCase() + " · " + MODEL_PRICE[offer],
          "clf-buytag clf-buytag--desk", function (e) {
            e.stopPropagation();
            buyDesk(offer);
          });
        deskTag.title = offer === "nano"
          ? "The site gateway: explains WHY a reading is wrong and which verb fixes it."
          : offer === "micro"
            ? (G.micro ? "Another Micro: one more dial held - but they don't talk to each other."
                       : "The site view - and it can hold one knob for you.")
            : "One mind on every dial, the line balanced as a whole - and its " +
              "Unit on the floor: watch it work, press TALK to ask it anything.";
        (DOM.priced = DOM.priced || []).push({ b: deskTag, cost: MODEL_PRICE[offer], kind: offer });
        rack.appendChild(deskTag);
      });
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
    // the ticker rides every frame - the radio's newest line, never hidden
    if (DOM.ticker) {
      var tick = G.log[0] || "";
      if (DOM.ticker.textContent !== tick) DOM.ticker.textContent = tick;
    }
    /* prices react to the wallet without rebuilding anything - and the
       AFFORDABILITY GLOW rides the same sweep (v29): affordable purchase
       points light softly (is-afford; reduced motion gets a steady lit
       state in CSS), and the recommended next buy carries the strong glow
       (is-reco). Glow states affordability and recommendation, never
       urgency - the tooltip says only "you can afford this now". */
    (DOM.priced || []).forEach(function (p) {
      p.b.disabled = G.coins < p.cost || p.off;
      var afford = !p.b.disabled && p.cost > 0;
      p.b.classList.toggle("is-afford", afford);
      p.b.classList.toggle("is-reco", afford && !!p.kind && !!DOM.recoKinds[p.kind]);
      if (afford !== p.wasAfford) {
        p.wasAfford = afford;
        if (p.baseTitle == null) p.baseTitle = p.b.title || "";
        p.b.title = afford
          ? (p.baseTitle ? p.baseTitle + " " : "") + "You can afford this now."
          : p.baseTitle;
      }
    });
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
    /* v40: the tape is a SESSION tape - its download button promises "every
       event of this run". A reset used to leave the old run's events in the
       buffer while the run clock restarted at 0, so a downloaded file
       carried two interleaved timelines under one run's summary. Save the
       finished run the way leaving the page does, then start a clean tape. */
    persistTape();
    TAPE.length = 0;
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
      if (DOM.chatOver && !DOM.chatOver.hidden) closeChat();
      else if (DOM.shopOver && !DOM.shopOver.hidden) closeShop();
      else if (DOM.winOver && !DOM.winOver.hidden) dismissWin();
    });
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) {
        G.wasRunning = G.running; G.running = false;
        persistTape();     // serialize on the way out, never per tick
      }
      else if (G.wasRunning) { G.running = true; lastT = 0; }
      paint();
    });
    // pagehide is the reliable leave signal (tab close, navigation, bfcache)
    window.addEventListener("pagehide", persistTape);
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
      buyDeskWith: function (state, which) { var s = G; G = state; var ok = buyDesk(which); G = s; return ok; },
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
      microDiagMult: MICRO_DIAG_MULT,
      microDiagSpinup: MICRO_DIAG_SPINUP,
      needsHumanWith: function (state, id) { var s2 = G; G = state; var v = needsHuman(machine(id)); G = s2; return v; },
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
      certItemsWith: function (state) { var s2 = G; G = state; var v = certItems(); G = s2; return v; },
      certReadyWith: function (state) { var s2 = G; G = state; var v = certReady(); G = s2; return v; },
      touchWith: function (state) { var s2 = G; G = state; touchPlant(); G = s2; return state; },
      siteBoardWith: function (state) { var s2 = G; G = state; var v = siteBoard(); G = s2; return v; },
      /* v31: the trust source and the plea, runnable by the locks */
      flaggedWith: function (state, id) { var s2 = G; G = state; var v = sensorFlagged(machine(id)); G = s2; return v; },
      helpWith: function (state) { var s2 = G; G = state; var v = unitHelpTarget(); G = s2; return v; },
      gigaStepsPerSec: GIGA_STEPS_PER_SEC,
      live: function () { return G; },
      /* v33 locks */
      stepUnitWith: function (state, dt) { var s2 = G; G = state; stepUnit(dt); G = s2; return state; },
      sendChatWith: function (state, q) { var s2 = G; G = state; var ok = sendChat(q); G = s2; return ok; },
      forceFault: function (id, kind) {
        var m = machine(id);
        if (!m || m.cond !== "none") return false;
        startCondition(m, kind);
        paint();
        return true;
      },
      grant: function (n) { G.coins += n; paint(); },
      /* v29 surfaces */
      recommendedNextWith: function (state) { var s2 = G; G = state; var v = recommendedNext(); G = s2; return v; },
      unitFocusWith: function (state) { var s2 = G; G = state; var v = unitFocus(); G = s2; return v; },
      stepUnitWith: function (state, dt) { var s2 = G; G = state; stepUnit(dt); G = s2; return state; },
      unitLineWith: function (state) { var s2 = G; G = state; var v = unitLine(); G = s2; return v; },
      plantSummaryWith: function (state) { var s2 = G; G = state; var v = plantSummary(); G = s2; return v; },
      pingFramingWith: function (state, q) { var s2 = G; G = state; var v = pingFraming(q); G = s2; return v; },
      unitFallbackWith: function (state) { var s2 = G; G = state; var v = unitFallbackLine(); G = s2; return v; },
      tapeChat: tapeChat,
      deskOffersWith: function (state) { var s2 = G; G = state; var v = deskOffers(); G = s2; return v; },
      watchOptionsWith: function (state) { var s2 = G; G = state; var v = watchOptions(); G = s2; return v; },
      buyTierWith: function (state, id) { var s2 = G; G = state; var ok = buyTier(id); G = s2; return ok; },
      contractSize: contractSize,
      slaBar: SLA_BAR,
      microMax: MICRO_MAX,
      tapeEvents: function () { return TAPE.slice(); },
      tapeReset: function () { TAPE.length = 0; },
      tapePush: tape,
      /* v32: Mk balance canon, the Unit's auto-inspection, the lane policy */
      mkFaultMult: MK_FAULT_MULT,
      mkVerbMult: MK_VERB_MULT,
      verbSecsForWith: function (state, id, secs) {
        var s2 = G; G = state; var v = verbSecsFor(machine(id), secs); G = s2; return v;
      },
      unitJobsWith: function (state) { var s2 = G; G = state; startUnitJobs(); G = s2; return state; },
      bubblePlan: bubblePlan,
      bubbleCandsWith: function (state) { var s2 = G; G = state; var v = bubbleCands(); G = s2; return v; },
      certUptime: CERT_UPTIME,
      certProofSecs: CERT_PROOF_SECS,
      buildTapeWith: function (state) { var s2 = G; G = state; var v = buildTape(); G = s2; return v; },
      persistTape: persistTape,
    };
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
