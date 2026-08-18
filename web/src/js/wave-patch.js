/* =====================================================================
   RogerAI - THE SIGNAL BENCH (Playbox / WAVE MESH)

   One sentence, left to right:

     [SENSOR SELECTOR] -> [MODEL CHAIN] -> [THE MONITOR]

   Pick a sensor type - the types are derived from the recorded tags, so
   the selector cannot offer a sensor nothing measured. Its pads dial a
   RECORDED condition; type + condition select one deterministic record.
   Then daisy-chain models into the rail: each stage transforms the
   output, and THE MONITOR - the hero of the deck - shows the output at
   every stage as a readability cascade: the raw wire, then the Pico's
   protocol line, then the Nano's human-readable verdict. Adding a model
   visibly adds a more-readable stage. That is the whole product.

   HONESTY, unchanged: no model executes in a browser. The raw stage is
   the byte-for-byte recorded window; the Pico and Nano stages print
   recorded fields only; the verdict paragraph is a fixed template over
   recorded fields plus a STATIC fault-kind glossary (labelled as
   glossary - documentation, not measurement). Unrecorded family members
   chain in but their stage says "no recorded transcript - output
   unchanged". A margin is a logprob difference nothing here computes.
   ===================================================================== */
(function () {
  "use strict";

  var REDUCED = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var SVGNS = "http://www.w3.org/2000/svg";

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }
  function svg(tag, attrs) {
    var n = document.createElementNS(SVGNS, tag);
    for (var k in attrs) if (attrs.hasOwnProperty(k)) n.setAttribute(k, attrs[k]);
    return n;
  }

  // The four measured floors are the knob's detents - the knob cannot be set
  // to a floor nothing measured.
  var DETENTS = [0.5, 1.0, 1.5, 2.0];
  var TOP = DETENTS[DETENTS.length - 1];

  /* ---- the model family: sizes, statuses, runs-on from the family page.
     `does` is each slot's transform, in plain words, for its chain card. */
  // THE WAVE SPECTRUM - the LOCKED ladder (WAVE-TIER-SCALING-STRATEGY-2026-08-14:
  // "Wave Pico -> Nano -> Micro -> Giga -> Tera -> Peta -> Exa", one SI-magnitude
  // scale read as "___ Wave", edge -> exascale flagship; supersedes the old
  // Edge/Core/Station/Satellite page ladder). Recipes, per the same doc:
  //   scratch          - trained from random init on our data
  //   base+specialize  - strong open base, industrial continued-pretrain + mesh
  //   expert-pruned / frontier - carved from the flagship
  // Every tier ships with Wave Mesh baked in and understands the output of every
  // tier beneath it. The two recorded slots also carry their RUN NAMES as ground
  // truth; the senior run's exact params are pending export
  // (ANSWER-FROM-MODELS-AGENT-wave-tier-naming.md).
  var FAMILY = [
    { id: "pico", label: "Wave Pico", size: "270M", band: "250-300M", status: "recorded",
      recipe: "scratch", reach: "edge · single device",
      runs: "Raspberry Pi · ~50ms · no GPU", art: "chip", span: 40,
      only: "answers where the data is born - no network hop, no GPU, no waiting. A read that never leaves the machine cannot be delayed by a link that is down.",
      belowCant: "the floor of the ladder: nothing smaller can hold a task model",
      takes: "one machine's channels",
      job: "a call on this machine: one word, and how sure it was",
      does: "reads the sensor, answers or asks for help",
      blurb: "the edge child - reads one machine's telemetry and asserts, with margins; " +
             "the recorded reader on this bench (270M, in the 250-300M tier band)" },
    { id: "nano", label: "Wave Nano", size: "0.8-1.5B", band: "0.8-1.5B", status: "recorded",
      recipe: "scratch", reach: "gateway · a fleet",
      runs: "a gateway / concentrator", art: "gateway", span: 46,
      only: "sees MANY machines at once, so it can settle a disagreement no single machine can even perceive - two readers, one truth.",
      belowCant: "one Pico sees one machine; conflicts between machines are invisible to it",
      takes: "many Picos",
      job: "a fleet rollup: the doubtful reads from many machines, adjudicated",
      does: "answers when the small one is unsure",
      blurb: "the fleet gateway - rolls up many children and resolves conflicts; " +
             "the recorded senior on this bench (run params pending export)" },
    { id: "micro", label: "Wave Micro", size: "7-8B", status: "base+specialize",
      recipe: "base+specialize", reach: "site · a facility",
      runs: "an on-site server", art: "server", span: 52,
      only: "reasons in general language across many fleets - not just a fault enum. It can be ASKED things, and answer about a facility.",
      belowCant: "a gateway rolls up its own fleet; it cannot reason about the site around it",
      takes: "many fleets",
      job: "a site rollup: every fleet's reads, summarized for one facility",
      does: "reasons across a whole facility",
      blurb: "multi-fleet reasoning across a facility - general-capable AND industrial; " +
             "no recorded run on this bench" },
    { id: "giga", label: "Wave Giga", size: "27-35B", status: "base+specialize",
      recipe: "base+specialize", reach: "a plant",
      runs: "a plant datacenter", art: "rack", span: 58,
      only: "holds a whole plant in view at once - process, maintenance and history reasoned over together, competitive on general benchmarks as well as machines.",
      belowCant: "a site brain sees its facility; the plant's other facilities are outside it",
      takes: "many sites",
      job: "a plant-wide picture: every site's rollup, reasoned over together",
      does: "reasons across a whole plant",
      blurb: "the plant - full-plant reasoning, competitive on general benchmarks as well " +
             "as machines; no recorded run on this bench" },
    { id: "tera", label: "Wave Tera", size: "80-120B", status: "base+specialize",
      recipe: "base+specialize", reach: "enterprise · many plants",
      runs: "an enterprise cloud", art: "racks", span: 64,
      only: "compares plants. A fault pattern that repeats across sites is invisible from inside any one of them.",
      belowCant: "one plant's model cannot see the pattern it shares with the next plant",
      takes: "many plants",
      needs: "a second plant's recording - this bench holds one",
      job: "faults and trends correlated across an enterprise at once",
      does: "connects faults across many plants",
      blurb: "cross-site enterprise - correlates faults and trends across many plants at " +
             "once; no recorded run on this bench" },
    { id: "peta", label: "Wave Peta", size: "150-200B", status: "expert-pruned",
      recipe: "expert-pruned", reach: "a region",
      runs: "a regional cloud", art: "aisle", span: 70,
      only: "carries a region on leaner hardware, pruned down from the flagship - the frontier's judgement at a fraction of its residency.",
      belowCant: "enterprise scale still runs the full stack; a region needs it cheaper",
      takes: "a region's plants",
      needs: "recordings from plants across a region - this bench holds one plant",
      job: "a region's work, on leaner hardware carved from the flagship",
      does: "regional scale, pruned smaller",
      blurb: "regional scale - a leaner giant, distilled and pruned down from the " +
             "frontier; no recorded run on this bench" },
    { id: "exa", label: "Wave Exa", size: "~284B", status: "frontier",
      recipe: "frontier", reach: "the family teacher",
      runs: "an exascale datacenter", art: "hall", span: 76,
      only: "TEACHES the rest. The small models are good because this one trained them - the flagship is why a 270M model on a Pi at the edge is worth trusting.",
      belowCant: "nothing above it: this is where the family's capability comes from",
      takes: "the whole family's work",
      needs: "nothing on this monitor: its work shows up in the WEIGHTS of the models below it, not as a read",
      job: "the teaching signal the rest of the family learns from",
      does: "the flagship the others learn from",
      blurb: "the flagship - exascale-class frontier capability (DeepSeek-V4-Flash " +
             "class · MTP), and the teacher the whole family learns from; no recorded " +
             "run on this bench" },
  ];
  function familyById(id) {
    for (var i = 0; i < FAMILY.length; i++) if (FAMILY[i].id === id) return FAMILY[i];
    return null;
  }

  /* ---- THE LINE: where each tier physically lives in a plant --------------
     The founder's ask was to stop drawing an abstract rail and draw the
     factory: sensors down the left, models across the top in the zone that
     actually houses them. Every tier maps to exactly one band, so the line
     IS the plant hierarchy and a read climbs it left to right. A band with
     no model is not hidden - an empty zone is information too ("nothing is
     watching the plant yet"), and it is where the [+] lives. */
  var BANDS = [
    { key: "machine",    label: "ON THE MACHINE", where: "one device",   tier: "pico" },
    { key: "gateway",    label: "AT THE GATEWAY", where: "a fleet",      tier: "nano" },
    { key: "site",       label: "ON SITE",        where: "a facility",   tier: "micro" },
    { key: "plant",      label: "THE PLANT",      where: "a plant",      tier: "giga" },
    { key: "enterprise", label: "ENTERPRISE",     where: "many plants",  tier: "tera" },
    { key: "regional",   label: "REGIONAL",       where: "a region",     tier: "peta" },
    { key: "frontier",   label: "FRONTIER",       where: "the teacher",  tier: "exa" },
  ];
  function bandOf(tierId) {
    for (var i = 0; i < BANDS.length; i++) if (BANDS[i].tier === tierId) return BANDS[i];
    return null;
  }

  /* ---- TIER COLOUR: the Spectrum's own hues, used as IDENTITY only --------
     The founder's Wave Spectrum chart already assigns one colour per tier -
     it is called a spectrum for a reason - so the deck adopts those exact
     values rather than inventing a second scheme.

     THE COLLISION RULE, which is why this is a comment and not just a token
     list: this deck already spends colour on SEMANTICS (the fenced lamp
     green/amber and the one --live red mean caught / doubtful / missed), and
     Wave Pico's tier hue is a red. A red stripe must never be mistaken for
     an alarm. So the two systems are kept in different PLACES, permanently:
       tier colour  -> a 4px edge stripe, the model's NAME, a ~7%/13% wash.
                       Never a badge, never a lamp, never a meter fill.
       state colour -> the badge and the verdict word, which always carry
                       their WORD as well, so hue is never the only signal.
     A card therefore reads "Wave Pico" in its tier hue with a green
     ANSWERED badge - identity and state never compete for the same pixel. */
  function tierStyle(node, id) {
    if (!node || !id) return node;
    node.style.setProperty("--tc", "var(--tier-" + id + ")");
    return node;
  }

  /* ---- the fault-kind GLOSSARY. STATIC DOCUMENTATION, not measurement:
     these are the standard sensor-health terms the suite labels with. The
     monitor marks every use with a "glossary" microlabel so a fixed
     dictionary can never read as a recorded fact. */
  var GLOSSARY = {
    none: "the channel reads as a healthy instrument should",
    stuck: "the reading freezes at one value and stops responding",
    dropout: "the reading intermittently vanishes",
    noisy: "excess random variation swamps the signal",
    drifting: "the reading slides steadily away from the truth",
    railed: "the reading pins at the top or bottom of its range",
  };

  /* ---- sensor TYPES: derived from the recorded tags. A suffix like
     _DISCHARGE_TEMP groups its records into a type; the AI_xxxx tags -
     Sparkplug aliases with no name - group into UNNAMED CHANNEL, which is
     kept on the selector deliberately: real plants are full of them. */
  var TYPE_DEFS = [
    { key: "temp", label: "DISCHARGE TEMP", icon: "thermo", suffixes: ["DISCHARGE_TEMP"] },
    { key: "press", label: "PRESSURE", icon: "gauge", suffixes: ["DISCHARGE_PRESS", "SUCTION_PRESS"] },
    { key: "vib", label: "VIBRATION", icon: "vibro", suffixes: ["VIBRATION"] },
    { key: "amp", label: "MOTOR CURRENT", icon: "ammeter", suffixes: ["MOTOR_CURRENT"] },
    { key: "oil", label: "OIL TEMP", icon: "oilcan", suffixes: ["OIL_TEMP"] },
    { key: "unnamed", label: "UNNAMED CHANNEL", icon: "junction", suffixes: [] },
  ];

  var UNIT_WORD = { Cel: "°C", kPa: "kPa", "mm/s": "mm/s", A: "A" };
  var CONDW = { none: "OK" };

  /* Operator-authored TRAINING playbooks, never model output. The recording
     can prove what the classifiers said about a sensor window; it cannot prove
     a root cause or authorize work on machinery. These steps therefore stop
     at verification, context gathering, and a site-controlled handoff. */
  var PLAYBOOKS = {
    stuck: { label: "STUCK SIGNAL", steps: [
      { id: "verify", kind: "verify", label: "VERIFY THE READING",
        tool: "independent instrument or calibrated reference",
        detail: "Compare this fixed channel with an independent observation before treating it as the process truth." },
      { id: "context", kind: "context", label: "CHECK THE SIGNAL PATH",
        tool: "trend history and an appropriately rated test instrument",
        detail: "Authorized personnel check the sensor, wiring, input channel, and scaling for where the value stopped changing." },
      { id: "handoff", kind: "handoff", label: "HAND OFF SAFELY",
        tool: "site-specific work order and energy-control procedure",
        detail: "Route physical inspection to authorized maintenance; verify hazardous-energy isolation before servicing." },
    ] },
    drifting: { label: "DRIFTING SIGNAL", steps: [
      { id: "verify", kind: "verify", label: "VERIFY THE OFFSET",
        tool: "calibrated reference or redundant channel",
        detail: "Compare the trend with an independent reference before deciding the instrument has drifted." },
      { id: "context", kind: "context", label: "CHECK CONTEXT",
        tool: "calibration history and neighboring-channel trends",
        detail: "Look for maintenance history, installation changes, or nearby channels moving at the same time." },
      { id: "handoff", kind: "handoff", label: "HAND OFF CALIBRATION",
        tool: "site calibration procedure and approved work order",
        detail: "Only authorized personnel decide whether calibration or replacement is appropriate for this instrument." },
    ] },
    dropout: { label: "DROPOUT", steps: [
      { id: "verify", kind: "verify", label: "VERIFY THE GAP",
        tool: "historian timestamps or an independent live indication",
        detail: "Confirm that samples are absent rather than merely delayed, filtered, or hidden by the display." },
      { id: "context", kind: "context", label: "TRACE THE PATH",
        tool: "communications diagnostics and an appropriately rated test instrument",
        detail: "Authorized personnel trace sensor power, connectors, network path, and the receiving input for the missing segment." },
      { id: "handoff", kind: "handoff", label: "HAND OFF SAFELY",
        tool: "site-specific work order and energy-control procedure",
        detail: "Do not restart or bypass equipment from this screen; route physical work through the site's authorized procedure." },
    ] },
    noisy: { label: "NOISY SIGNAL", steps: [
      { id: "verify", kind: "verify", label: "VERIFY THE VARIATION",
        tool: "independent instrument or neighboring-channel comparison",
        detail: "Check whether the variation is present outside this one measurement path." },
      { id: "context", kind: "context", label: "CHECK INSTALLATION CONTEXT",
        tool: "trend history and approved electrical or mechanical test equipment",
        detail: "Authorized personnel inspect mounting, grounding, shielding, routing, and nearby operating changes." },
      { id: "handoff", kind: "handoff", label: "HAND OFF SAFELY",
        tool: "site-specific work order and energy-control procedure",
        detail: "Route any physical correction to authorized maintenance and verify isolation before servicing." },
    ] },
    railed: { label: "RAILED SIGNAL", steps: [
      { id: "verify", kind: "verify", label: "VERIFY RANGE AND SCALE",
        tool: "independent reference and the approved instrument range",
        detail: "Confirm the value is pinned at a limit and that display scaling is not creating the appearance." },
      { id: "context", kind: "context", label: "CHECK INPUT CONTEXT",
        tool: "appropriately rated test instrument and configuration record",
        detail: "Authorized personnel check supply, signal path, input range, and configuration against the instrument record." },
      { id: "handoff", kind: "handoff", label: "HAND OFF SAFELY",
        tool: "site-specific work order and energy-control procedure",
        detail: "Do not force the input back into range; route inspection through authorized maintenance." },
    ] },
  };

  var PATCH = {
    catalog: null, measured: null, scene: null,
    types: [],            // built from the records at boot
    typeKey: null,        // the selected sensor type
    cond: "none",         // the dialed condition (a recorded truth)
    windows: null,        // wave-windows.json: the real 96-sample series per record
    tab: "all",           // the monitor's channel button: all | raw | pico | nano
    reply: null,          // the prompt's last response (classified, DRAFT-only)
    chain: [],            // family ids, in daisy-chain order
    floor: 1.5,           // the Pico's margin floor (measured detents)
    operator: false,      // legacy backing flag: HUMAN REVIEW response route
    authority: false,     // legacy backing flag: POLICY QUEUE response route;
                          // neither flag changes a model result or capability
    whyOpen: null,        // which why-panel is expanded (one at a time)
    liveTier: null,       // which tier the WHERE THEY LIVE panel is showing
    tour: -1,             // guided-tour step, -1 = not touring
    gameMode: "explore", // Mesh stays the full engineering workbench; Factory is its own deck
    sideMore: false,      // v38: the gauge + case tools fold under MORE until asked for
    touched: false,       // v38: the visitor has changed something - the fleet score may show
    inspectionOpen: false,
    factory: { shipped: 0, goal: 3 },
    verdict: null,
    menuFor: null,        // chain slot index whose attach menu is open
    booted: false,
    step: 0,
    mission: { active: false, phase: "idle", draws: 0, completed: 0,
               actions: {}, incidentNode: null, verifiedNode: null, note: "",
               moveStage: 0, moveFeedback: null,
               field: { values: {}, visits: {}, feedback: null } },
    _userScrollAt: 0,     // last USER scroll inside the glass (auto-follow yields)
    _autoScrollAt: 0,     // last programmatic glass scroll (its echo is not a user)
  };

  function buildTypes() {
    var m = PATCH.measured;
    if (!m) return;
    PATCH._recType = {};   // record index -> type key, for the fleet rollup
    var bySuffix = {};
    m.records.forEach(function (r, i) {
      var tag = (r.window && r.window.tag) || "";
      var key = /^AI_\d+$/.test(tag) ? "unnamed" : tag.replace(/^[A-Z]+\d+_/, "");
      bySuffix[key] = bySuffix[key] || [];
      bySuffix[key].push(i);
    });
    PATCH.types = [];
    TYPE_DEFS.forEach(function (def) {
      var idxs = def.key === "unnamed" ? (bySuffix.unnamed || [])
        : def.suffixes.reduce(function (acc, s) { return acc.concat(bySuffix[s] || []); }, []);
      if (!idxs.length) return; // a type exists only if the records do
      idxs.sort(function (a, b) { return a - b; });
      var recIdx = {}, pickWhy = {};
      var byTruth = {};
      idxs.forEach(function (i) {
        var t = m.records[i].truth;
        (byTruth[t] = byTruth[t] || []).push(i);
      });
      Object.keys(byTruth).forEach(function (t) {
        var pick = pickRecord(byTruth[t], t, m.records);
        recIdx[t] = pick.idx;
        pickWhy[t] = pick.why;
      });
      var conds = Object.keys(recIdx).sort(function (a, b) {
        if (a === "none") return -1;
        if (b === "none") return 1;
        return a < b ? -1 : 1;
      });
      PATCH.types.push({ key: def.key, label: def.label, icon: def.icon,
                         count: idxs.length, idxs: idxs, conds: conds,
                         recIdx: recIdx, pickWhy: pickWhy });
      idxs.forEach(function (i) { PATCH._recType[i] = def.key; });
    });
    if (!PATCH.typeKey && PATCH.types[0]) PATCH.typeKey = PATCH.types[0].key;
    // the scopes index the SAME records by machine/site/plant. They are always
    // needed together, so building them here removes a call-order footgun.
    buildScopes();
  }

  /* ---- the representative pick: per (type, condition), the RECORDED window
     that shows the condition most clearly - chosen by a documented feature
     criterion, deterministically, from that type's records only. The samples
     are never touched; only WHICH record a pad replays is chosen.

     NOTE on stuck: in this export every stuck window has longest_run = 1 -
     the suite's "stuck" means the sensor stops TRACKING the process while
     quantization jitter keeps neighbouring samples unequal. The honest
     "clearest stuck" is therefore the FLATTEST window: minimum sd relative
     to the window's own magnitude (tiebreak: max repeat_frac). */
  var PICK = {
    none:     { why: "calmest healthy window", score: function (w) { return -w.hf_energy; } },
    stuck:    { why: "steadiest window", score: function (w) {
                  return -(w.sd / Math.max(Math.abs(w.mean), 1e-9)) + (w.repeat_frac || 0) * 1e-6; } },
    dropout:  { why: "most resets", score: function (w) { return w.n_resets + (w.max_drop || 0) * 1e-6; } },
    noisy:    { why: "most high-frequency energy", score: function (w) { return w.hf_energy; } },
    drifting: { why: "steepest trend", score: function (w) {
                  return Math.abs(w.slope_per_min) + (w.monotonic_frac || 0) * 1e-6; } },
    railed:   { why: "longest at the rail", score: function (w) { return w.at_max_frac; } },
  };

  function pickRecord(idxs, truth, records) {
    var rule = PICK[truth] || PICK.none;
    var best = idxs[0], bestScore = -Infinity;
    idxs.forEach(function (i) {
      var w = records[i].window || {};
      var s = rule.score(w);
      if (s > bestScore) { bestScore = s; best = i; }
    });
    var w2 = records[best].window || {};
    var detail = truth === "stuck" ? "sd " + w2.sd + " over " + w2.n + " samples"
      : truth === "dropout" ? w2.n_resets + " resets, max drop " + w2.max_drop
      : truth === "noisy" ? "hf_energy " + w2.hf_energy
      : truth === "drifting" ? "slope " + w2.slope_per_min + "/min"
      : truth === "railed" ? "at the rail " + Math.round((w2.at_max_frac || 0) * 100) + "% of the window"
      : "hf_energy " + w2.hf_energy;
    return { idx: best,
             why: "chosen as the clearest recorded " + (truth === "none" ? "OK" : truth.toUpperCase()) +
                  ": " + rule.why + " (" + detail + ")" };
  }

  // RUN NAMES ARE GROUND TRUTH. The tier labels (Wave Pico / Wave Nano) are
  // deck names pending the authoritative ladder answer
  // (ANSWER-FROM-MODELS-AGENT-wave-tier-naming.md); the RUN names below come
  // from the measured export and are what actually ran on the bench.
  function runOf(id) {
    var m = PATCH.measured;
    if (!m || !m.escalation) return null;
    if (id === "pico") return shortName(m.escalation.child);
    if (id === "nano") return shortName(m.escalation.parent);
    return null;
  }

  function typeOf(key) {
    for (var i = 0; i < PATCH.types.length; i++) if (PATCH.types[i].key === key) return PATCH.types[i];
    return null;
  }
  function currentType() { return typeOf(PATCH.typeKey); }

  // The selected RECORD: type + condition, deterministically.
  function currentRecord() {
    var t = currentType();
    if (!t || !PATCH.measured) return null;
    var i = t.recIdx[PATCH.cond] != null ? t.recIdx[PATCH.cond] : t.recIdx[t.conds[0]];
    return PATCH.measured.records[i];
  }
  function unitWordOf(r) {
    var u = r && r.window && r.window.unit;
    if (!u) return null;
    return UNIT_WORD[u] || u;
  }

  /* ---- the chain, read for meaning ---------------------------------------
     The FIRST recorded reader in the chain reads the wire (Pico asserts or
     escalates at the floor; Nano reads direct). A Nano placed after a Pico
     is the senior. Everything unrecorded is a pass-through: honestly
     silent, output unchanged. */
  function chainInfo() {
    var picoAt = -1, nanoAt = -1;
    PATCH.chain.forEach(function (id, i) {
      if (id === "pico" && picoAt < 0) picoAt = i;
      if (id === "nano" && nanoAt < 0) nanoAt = i;
    });
    return {
      picoAt: picoAt, nanoAt: nanoAt,
      senior: picoAt >= 0 && nanoAt > picoAt,
      reader: picoAt >= 0 ? "pico" : (nanoAt >= 0 ? "nano" : null),
    };
  }

  /* ---- ONE MODEL, MANY SENSORS - made visible ----------------------------
     "In a real mesh one Pico reads many channels" was a caption for six
     rounds; now every sensor lane on the left carries its own live dot, so
     the fan-in is a thing you SEE rather than a thing you are told.

     Each lane is recounted from its OWN record, independently - that is what
     makes it honest. The SELECTED lane replays the condition you dialed; the
     others replay their recorded OK window, because that is the only
     condition every type is guaranteed to have and the alternative would be
     picking a fault for a sensor the visitor never touched. The tooltip
     names the record and the condition, so no dot is ever a mystery. */
  function laneRead(t) {
    if (!PATCH.measured || !t) return null;
    var info = chainInfo();
    if (!info.reader) return null;
    var sel = t.key === PATCH.typeKey;
    var cond = sel ? PATCH.cond : (t.recIdx.none != null ? "none" : t.conds[0]);
    if (t.recIdx[cond] == null) cond = t.conds[0];
    var r = PATCH.measured.records[t.recIdx[cond]];
    if (!r) return null;
    var isFault = r.truth !== "none";
    var said, who, esc = false;
    if (info.reader === "nano" && info.picoAt < 0) {
      said = r.parent.prediction; who = "Wave Nano";
    } else if (r.child.margin < PATCH.floor) {
      esc = true;
      if (info.senior) { said = r.parent.prediction; who = "Wave Nano"; }
      else { said = null; who = null; }
    } else {
      said = r.child.prediction; who = "Wave Pico";
    }
    var cls = said == null ? "is-idle"
      : (said === r.truth ? (isFault ? "is-ok" : "is-quiet") : (isFault ? "is-bad" : "is-warn"));
    return { cond: cond, cLabel: CONDW[cond] || cond.toUpperCase(), esc: esc, said: said,
             who: who, cls: cls, sel: sel, node: r.node_id,
             tag: (r.window && r.window.tag) || r.node_id };
  }

  /* ---- THE ANSWER - what the chain finally said about this read -----------
     The monitor was a wall of equally-weighted text (founder: "even though
     the monitor is bigger it's still hard to see what is really happening").
     The cure is hierarchy, and hierarchy needs a single most-important fact:
     the last thing the chain actually said. Read off the printed stages, so
     the headline can never disagree with the detail beneath it. */
  function finalAnswer(sts) {
    var out = null;
    sts.forEach(function (st) {
      if (st.kind === "nano") {
        out = { word: st.verdict, who: "WAVE NANO", tier: "nano", ok: st.ok,
                isFault: st.isFault, gloss: st.gloss, asked: true };
      } else if (st.kind === "pico" && !st.esc) {
        out = { word: st.said, who: "WAVE PICO", tier: "pico", ok: st.ok,
                isFault: st.r.truth !== "none", gloss: GLOSSARY[st.said] || null, asked: false };
      } else if (st.kind === "deadend") {
        out = { word: null, who: "NOBODY", tier: null, note: st.note };
      }
    });
    return out;
  }

  function caseSignalClue(r) {
    var w = r && r.window;
    if (!r || !w) {
      return "Our recorded data has no signal summary for this case. Check it against an independent source rather than guessing from a missing trace.";
    }
    var unit = unitWordOf(r);
    var suffix = unit ? " " + unit : "";
    var span = w.lo + " to " + w.hi + suffix;
    if (r.truth === "stuck") {
      return "This card's " + w.n + " values span " + span +
        " and no consecutive value repeats exactly.";
    }
    if (r.truth === "drifting") {
      return "The recorded trace stays populated while moving at " + w.slope_per_min + suffix +
        " per minute across " + span + ".";
    }
    if (r.truth === "dropout") {
      return "The recorded trace contains " + w.n + " samples and a one-step drop as large as " +
        w.max_drop + suffix + ".";
    }
    if (r.truth === "noisy") {
      return "The recorded trace changes direction " + w.sign_changes + " times across " + span + ".";
    }
    if (r.truth === "railed") {
      return "All " + w.n + " recorded values are present and the trace still moves across " + span + ".";
    }
    return "This independent OK check contains " + w.n + " recorded samples spanning " + span + ".";
  }

  function modelMissShape(r) {
    var clue = caseSignalClue(r);
    if (!r || !r.window) {
      return clue + " The label-model disagreement is the reason to stop treating an all-clear as proof and check an independent reference.";
    }
    if (r.truth === "stuck") {
      return clue + " Small measurement movement makes NONE look plausible even though the replay labels the signal STUCK; a literal all-values-equal rule would miss it.";
    }
    if (r.truth === "drifting") {
      return clue + " Without a trusted baseline or calibration point, that slow movement can resemble ordinary variation and make NONE look plausible.";
    }
    if (r.truth === "dropout") {
      return clue + " The remaining readings can dominate a summary, so a brief loss can still leave NONE looking plausible unless the timeline is checked.";
    }
    if (r.truth === "noisy") {
      return clue + " Every sample is still present, so a check aimed only at gaps or hard limits can return NONE; an independent reference exposes the excess variation.";
    }
    if (r.truth === "railed") {
      return clue + " That can look healthy on shape alone. The approved range and receiving configuration are what expose a rail or range mismatch.";
    }
    return clue + " The waveform alone left the model answer plausible, while the recorded label says an independent reference is still needed.";
  }

  /* A red MODEL LIMIT is a disagreement between two different sources:
     model output and the replay's committed label. Keep the boundary in one
     object so the overview, detail and training prompt cannot each tell a
     different story about who discovered the miss. In production there is no
     magic truth label; an independent signal or an audit policy has to create
     the disagreement that the benchmark gives this workbench for free. */
  function modelLimitLesson(r, answer) {
    if (!r || !answer || r.truth === "none" || answer.word === r.truth) return null;
    var picoAnswered = answer.who === "WAVE PICO";
    var picoConfident = picoAnswered && r.child.margin >= PATCH.floor;
    var truth = CONDW[r.truth] || String(r.truth).toUpperCase();
    var modelAnswer = answer.word == null ? "NO ANSWER" : String(answer.word).toUpperCase();
    var why;
    if (picoConfident) {
      why = "Pico was " + r.child.margin.toFixed(2) + " sure, above the " +
        PATCH.floor.toFixed(1) + " floor, so Nano was not called. Nano's recorded " +
        "counterfactual " + (r.parent.prediction === r.truth ? "said " : "also said ") +
        String(r.parent.prediction).toUpperCase() + ". " +
        (r.parent.prediction === r.truth
          ? "That would be right, but Pico's margin is above the highest available handoff setting."
          : "Changing the handoff knob would not rescue this read.");
    } else {
      why = (picoAnswered ? "Pico asked for help. " : "Nano read this window directly. ") +
        "Nano's recorded answer was " + String(r.parent.prediction).toUpperCase() +
        ", and it disagreed with the replay label.";
    }
    var shape = modelMissShape(r);
    return {
      label: picoConfident ? "CONFIDENT MISS" : "CHAIN MISS",
      modelAnswer: modelAnswer,
      recordedTruth: truth,
      knownBy: "COMMITTED REPLAY LABEL · NOT MODEL OUTPUT",
      why: why,
      shapeTitle: modelAnswer === "NONE" ? "WHY NONE LOOKED PLAUSIBLE" : "WHY THE ANSWER LOOKED PLAUSIBLE",
      shapeBy: "BENCH EXPLANATION · RECORDED SIGNAL · NOT MODEL OUTPUT",
      shape: shape,
      catch: "An independent reference, a site invariant, or an audit that samples all-clear decisions can challenge a model answer that emitted no finding.",
    };
  }

  /* =====================================================================
     THE FLEET - every recorded record, replayed under the CURRENT policy.
     Pure arithmetic over the 120 committed records: the same single-record
     walk derive() does, run across the whole bench, so turning the floor
     knob visibly moves fleet-level catches. Nothing here is a projection -
     it is a recount of recorded child/parent predictions under the chain
     settings the visitor chose.
     ===================================================================== */
  function mkTally() {
    return { n: 0, faults: 0, caught: 0, missed: 0, fixable: 0,
             deadEnd: 0, falseAlarms: 0, escalated: 0, quiet: 0 };
  }

  /* Score ONE record under the current chain policy. Pulled out of the fleet
     rollup so every scope - a machine, a site, the plant, the whole fleet -
     is counted by the SAME arithmetic. Returns the outcome and the flags a
     tally needs; it invents nothing, it only reads recorded fields. */
  function scoreRecord(r, info) {
    var isFault = r.truth !== "none";
    var out, esc = false, dead = false, fixable = false;
    if (info.reader === "nano" && info.picoAt < 0) {
      var okN = r.parent.prediction === r.truth;
      out = okN ? (isFault ? "caught" : "quiet") : (isFault ? "missed" : "false");
    } else {
      esc = r.child.margin < PATCH.floor;
      if (!esc) {
        var okC = r.child.prediction === r.truth;
        out = okC ? (isFault ? "caught" : "quiet") : (isFault ? "missed" : "false");
        if (out === "missed" && r.parent.prediction === r.truth && r.child.margin < TOP) fixable = true;
      } else if (info.senior) {
        var okP = r.parent.prediction === r.truth;
        out = okP ? (isFault ? "caught" : "quiet") : (isFault ? "missed" : "false");
      } else {
        dead = true;
        out = isFault ? "missed" : "quiet"; // an unheard doubt asserts nothing
      }
    }
    return { out: out, esc: esc, dead: dead, fixable: fixable, isFault: isFault };
  }

  function addToTally(b, sc) {
    b.n++;
    if (sc.isFault) b.faults++;
    if (sc.esc) b.escalated++;
    if (sc.dead) b.deadEnd++;
    if (sc.fixable) b.fixable++;
    if (sc.out === "caught") b.caught++;
    else if (sc.out === "missed") b.missed++;
    else if (sc.out === "false") b.falseAlarms++;
    else b.quiet++;
  }

  /* Count any set of record indices under the current policy. This is the one
     engine behind every scope stage AND the FLEET tab, so a site rollup and
     the fleet tab can never disagree about the same records. */
  function tallyOver(idxs, info) {
    var m = PATCH.measured;
    var t = mkTally();
    idxs.forEach(function (i) { addToTally(t, scoreRecord(m.records[i], info)); });
    return t;
  }

  function deriveFleet() {
    var m = PATCH.measured;
    if (!m) return null;
    var info = chainInfo();
    if (!info.reader) return { none: true };
    var mk = mkTally;
    var t = mk(), per = {};
    m.records.forEach(function (r, i) {
      var key = PATCH._recType && PATCH._recType[i];
      var p = per[key] = per[key] || mk();
      var isFault = r.truth !== "none";
      [t, p].forEach(function (b) {
        b.n++;
        if (isFault) b.faults++;
      });
      var out; // caught | missed | quiet | false
      var esc = false, dead = false, fixable = false;
      if (info.reader === "nano" && info.picoAt < 0) {
        var okN = r.parent.prediction === r.truth;
        out = okN ? (isFault ? "caught" : "quiet") : (isFault ? "missed" : "false");
      } else {
        esc = r.child.margin < PATCH.floor;
        if (!esc) {
          var okC = r.child.prediction === r.truth;
          out = okC ? (isFault ? "caught" : "quiet") : (isFault ? "missed" : "false");
          if (out === "missed" && r.parent.prediction === r.truth && r.child.margin < TOP) fixable = true;
        } else if (info.senior) {
          var okP = r.parent.prediction === r.truth;
          out = okP ? (isFault ? "caught" : "quiet") : (isFault ? "missed" : "false");
        } else {
          dead = true;
          out = isFault ? "missed" : "quiet"; // an unheard doubt asserts nothing
        }
      }
      [t, p].forEach(function (b) {
        if (esc) b.escalated++;
        if (dead) b.deadEnd++;
        if (fixable) b.fixable++;
        if (out === "caught") b.caught++;
        else if (out === "missed") b.missed++;
        else if (out === "false") b.falseAlarms++;
        else b.quiet++;
      });
    });
    return { totals: t, perType: per, policy: policyLine() };
  }

  /* =====================================================================
     THE SCOPES - what each tier can see, derived from the recording

     The founder: "when i put Giga it just says nothing that big here, we need
     to simulate as if it were." It can be simulated without inventing a byte,
     because the recording already HAS the shape of a plant: the 120 records
     are 20 scenes of 6 channels, and each scene is one machine (its channels
     share a tag root - AIR003_*, AIR004_*, ...). So the ladder's scopes are
     nested subsets of real records:

       Pico  - one channel on one machine        (the dialed record)
       Nano  - that machine's channels           (one scene: 6 records)
       Micro - that machine's SITE               (see the partition below)
       Giga  - every machine: THE PLANT          (all 20 scenes, 120 records)

     THE SITE PARTITION IS A STATED CONVENTION, not a recorded fact: the bench
     file does not say which machines share a building, so the deck groups the
     scenes in id order into sites of SITE_SIZE. That is arithmetic on real
     records under a rule we print on screen - never a claim the export made.

     Above Giga the recording genuinely runs out: one plant is not many plants,
     there is no region, and the flagship's role is TEACHING, which is a
     training relationship rather than a read. Those tiers say what they would
     add and stop, because the alternative is fabrication. */
  var SITE_SIZE = 5;   // machines per site, stated wherever a site is shown

  function buildScopes() {
    var m = PATCH.measured;
    if (!m) return;
    var order = [], byScene = {};
    m.records.forEach(function (r, i) {
      if (!byScene[r.scene_id]) { byScene[r.scene_id] = []; order.push(r.scene_id); }
      byScene[r.scene_id].push(i);
    });
    order.sort();
    PATCH.machines = order.map(function (sid, k) {
      var idxs = byScene[sid];
      // a machine's name is the tag root its channels share; unnamed channels
      // (the Sparkplug aliases) fall back to the scene id, never invented
      var roots = {};
      idxs.forEach(function (i) {
        var tag = (m.records[i].window && m.records[i].window.tag) || "";
        var root = /^AI_\d+$/.test(tag) ? null : tag.split("_")[0];
        if (root) roots[root] = (roots[root] || 0) + 1;
      });
      var name = Object.keys(roots).sort(function (a, b) { return roots[b] - roots[a]; })[0] || sid;
      return { scene: sid, name: name, idxs: idxs, site: Math.floor(k / SITE_SIZE) };
    });
    PATCH.sites = [];
    PATCH.machines.forEach(function (mach) {
      var st = PATCH.sites[mach.site] = PATCH.sites[mach.site] ||
        { n: mach.site, machines: [], idxs: [] };
      st.machines.push(mach);
      st.idxs = st.idxs.concat(mach.idxs);
    });
    PATCH.sites.forEach(function (st) {
      st.label = "SITE " + String.fromCharCode(65 + st.n);
    });
  }

  function machineOf(recIdx) {
    var ms = PATCH.machines || [];
    for (var i = 0; i < ms.length; i++) if (ms[i].idxs.indexOf(recIdx) >= 0) return ms[i];
    return null;
  }

  /* The scope a tier answers over, for the CURRENT selection. Returns the
     record indices plus what to call them, or null when the tier's scope
     exceeds what was recorded. */
  function scopeForRecord(tierId, recIdx) {
    var m = PATCH.measured;
    if (!m || !PATCH.machines) return null;
    var mach = recIdx == null ? null : machineOf(recIdx);
    if (tierId === "nano") {
      if (!mach) return null;
      return { idxs: mach.idxs, unit: "machine", name: mach.name,
               machines: 1, chans: mach.idxs.length,
               how: "the machine this channel is bolted to" };
    }
    if (tierId === "micro") {
      if (!mach) return null;
      var site = PATCH.sites[mach.site];
      return { idxs: site.idxs, unit: "site", name: site.label,
               machines: site.machines.length, chans: site.idxs.length,
               how: "the " + site.machines.length + " machines grouped as " + site.label +
                    " - scenes in id order, " + SITE_SIZE + " to a site" };
    }
    if (tierId === "giga") {
      var all = [];
      PATCH.machines.forEach(function (x) { all = all.concat(x.idxs); });
      return { idxs: all, unit: "plant", name: "THE PLANT",
               machines: PATCH.machines.length, chans: all.length,
               sites: PATCH.sites.length,
               how: "every recorded machine on this bench" };
    }
    return null; // tera / peta / exa exceed the recording
  }

  function scopeFor(tierId) {
    var sel = currentType();
    var recIdx = sel && sel.recIdx ? sel.recIdx[PATCH.cond] : null;
    return scopeForRecord(tierId, recIdx);
  }

  /* The worst offenders inside a scope: which machines carry the misses. This
     is what a site brain or a plant model would actually report, and it is a
     recount - each machine's own records, scored by the same engine. */
  function worstMachines(idxs, info, limit) {
    var seen = {}, out = [];
    (PATCH.machines || []).forEach(function (mach) {
      var mine = mach.idxs.filter(function (i) { return idxs.indexOf(i) >= 0; });
      if (!mine.length) return;
      var t = tallyOver(mine, info);
      if (!seen[mach.name]) { seen[mach.name] = 1; out.push({ mach: mach, t: t }); }
    });
    out.sort(function (a, b) {
      return (b.t.missed - a.t.missed) || (b.t.faults - a.t.faults) ||
             (a.mach.name < b.mach.name ? -1 : 1);
    });
    return out.slice(0, limit || 3);
  }

  /* The scope rollup used to answer the same question for every selected
     card: "which machines missed the most faults of any kind?" That is valid
     fleet arithmetic and poor case intelligence. A technician looking at a
     MOTOR CURRENT card needs the current motor first, then comparable motor
     cards at the tier's scope. This lens is still only a recount of committed
     records; it adds no model prose and no synthetic sensor values. */
  function caseLens(tierId, record) {
    var r = record || currentRecord();
    if (!r || !PATCH.measured) return null;
    var recIdx = PATCH.measured.records.indexOf(r);
    if (recIdx < 0) return null;
    var sensorKey = PATCH._recType && PATCH._recType[recIdx];
    var type = typeOf(sensorKey);
    var scope = scopeForRecord(tierId, recIdx);
    var info = chainInfo();
    if (!sensorKey || !scope || !info.reader) return null;

    var comparable = scope.idxs.filter(function (i) {
      return PATCH._recType && PATCH._recType[i] === sensorKey;
    });
    var byMachine = [];
    (PATCH.machines || []).forEach(function (mach) {
      var mine = comparable.filter(function (i) { return mach.idxs.indexOf(i) >= 0; });
      if (!mine.length) return;
      mine.sort(function (a, b) {
        if (a === recIdx) return -1;
        if (b === recIdx) return 1;
        var ar = PATCH.measured.records[a], br = PATCH.measured.records[b];
        var ae = ar.truth === r.truth ? 1 : 0, be = br.truth === r.truth ? 1 : 0;
        if (ae !== be) return be - ae;
        var af = ar.truth === "none" ? 0 : 1, bf = br.truth === "none" ? 0 : 1;
        if (af !== bf) return bf - af;
        return a - b;
      });
      var pick = mine[0];
      var rowRecord = PATCH.measured.records[pick];
      var scored = scoreRecord(rowRecord, info);
      var outcome = scored.dead ? "UNHEARD"
        : scored.out === "false" ? "FALSE ALARM"
        : String(scored.out).toUpperCase();
      byMachine.push({
        machine: mach.name,
        record: rowRecord.node_id,
        current: pick === recIdx,
        sensorKey: sensorKey,
        condition: CONDW[rowRecord.truth] || String(rowRecord.truth).toUpperCase(),
        outcome: outcome,
        prediction: scored.dead ? null : (rowRecord.child.margin < PATCH.floor && info.senior
          ? rowRecord.parent.prediction : rowRecord.child.prediction),
        margin: rowRecord.child.margin,
      });
    });
    byMachine.sort(function (a, b) {
      if (a.current !== b.current) return a.current ? -1 : 1;
      var ae = a.condition === (CONDW[r.truth] || String(r.truth).toUpperCase()) ? 1 : 0;
      var be = b.condition === (CONDW[r.truth] || String(r.truth).toUpperCase()) ? 1 : 0;
      if (ae !== be) return be - ae;
      return a.machine < b.machine ? -1 : 1;
    });
    var exact = comparable.filter(function (i) {
      return PATCH.measured.records[i].truth === r.truth;
    });
    return {
      tier: tierId,
      sensorKey: sensorKey,
      sensor: type ? type.label : ((r.window && r.window.tag) || "RECORDED SENSOR"),
      condition: CONDW[r.truth] || String(r.truth).toUpperCase(),
      scope: scope,
      tally: tallyOver(comparable, info),
      matching: exact.length,
      rows: byMachine.slice(0, tierId === "giga" ? 8 : 5),
    };
  }

  function policyLine() {
    var info = chainInfo();
    var bits = [];
    if (info.reader === "pico") bits.push("floor " + PATCH.floor.toFixed(1));
    if (info.reader === "nano" && info.picoAt < 0) bits.push("the senior reads direct");
    else bits.push(info.senior ? "senior seated" : "no senior");
    bits.push(responseMode().short);
    return bits.join(" · ");
  }

  var FAMILY_CASE_LINE = "PICO → NANO → MICRO → GIGA → TERA → PETA → EXA";

  function readerHandoff(r) {
    if (!r) return "WAVE PICO has no recorded read · WAVE NANO has no recorded read";
    var pico = 'WAVE PICO ' + (r.child.margin < PATCH.floor ? "ASKED FOR HELP" :
      'ANSWERED "' + String(r.child.prediction).toUpperCase() + '"') +
      " at margin " + r.child.margin.toFixed(2);
    var nano = r.child.margin < PATCH.floor
      ? 'WAVE NANO ANSWERED "' + String(r.parent.prediction).toUpperCase() + '" in the recorded run'
      : 'WAVE NANO WAS NOT CALLED · recorded counterfactual "' +
        String(r.parent.prediction).toUpperCase() + '"';
    return pico + " · " + nano;
  }

  function caseMechanic(r) {
    if (!r || r.truth === "none") {
      return "OK CHECK · no field action is needed; deal the next shift card.";
    }
    var rig = FIELD_RIGS && FIELD_RIGS[r.truth];
    if (!rig) return "FIELD CHECK REQUIRED · this recorded condition has no training rig yet.";
    return rig.title + " · " + rig.controls.map(function (control) {
      return control.label;
    }).join(" → ");
  }

  /* One shared case packet, seven distinct jobs. Pico/Nano fields are model
     outputs from the committed run. Micro/Giga are deterministic synthesis
     over committed records. The upper three receive the same packet but stop
     at a role simulation because this replay contains only one plant. */
  function tierCaseBrief(tierId, record) {
    var fam = familyById(tierId);
    var r = record || currentRecord();
    if (!fam || !r || !PATCH.measured) return null;
    var recIdx = PATCH.measured.records.indexOf(r);
    var typeKey = PATCH._recType && PATCH._recType[recIdx];
    var type = typeOf(typeKey) || currentType();
    var condition = r.truth === "none" ? "OK" : String(r.truth).toUpperCase();
    var rig = FIELD_RIGS && FIELD_RIGS[r.truth];
    var objective = rig && rig.objective ? rig.objective :
      "Confirm the independent OK check and leave the machinery unchanged.";
    var scopeSummary = (tierId === "micro" || tierId === "giga")
      ? caseLens(tierId, r) : null;
    var adds, provenance;
    if (tierId === "pico") {
      adds = "For this " + condition + " case, Pico contributes its recorded one-channel call: " +
        String(r.child.prediction).toUpperCase() + " at margin " + r.child.margin.toFixed(2) +
        ", then answers or asks Nano according to the knob.";
      provenance = "RECORDED MODEL OUTPUT";
    } else if (tierId === "nano") {
      adds = "For this " + condition + " case, Nano contributes its recorded second opinion: " +
        String(r.parent.prediction).toUpperCase() + " at margin " + r.parent.margin.toFixed(2) +
        ", used only when the handoff reaches it.";
      provenance = "RECORDED MODEL OUTPUT";
    } else if (tierId === "micro") {
      adds = "For this " + condition + " " + (type ? type.label : "sensor") +
        " case, Micro puts " + r.node_id + " first, then compares " +
        (scopeSummary ? scopeSummary.tally.n + " " + scopeSummary.sensor + " card" +
          (scopeSummary.tally.n === 1 ? "" : "s") + " across " + scopeSummary.scope.name +
          ": " + scopeSummary.tally.caught + " caught, " + scopeSummary.tally.missed + " missed"
          : "the same sensor across its site") + ". Its site-triage mission is: " + objective;
      provenance = "BENCH SYNTHESIS · COMMITTED RECORDS · NOT MODEL OUTPUT";
    } else if (tierId === "giga") {
      adds = "For this " + condition + " " + (type ? type.label : "sensor") +
        " case, Giga carries the same comparison across " +
        (scopeSummary ? scopeSummary.scope.name + ": " + scopeSummary.tally.n +
          " comparable cards, " + scopeSummary.matching + " with this condition"
          : "the plant's sites") +
        ", so plant operations compare like with like instead of ranking unrelated channels.";
      provenance = "BENCH SYNTHESIS · COMMITTED RECORDS · NOT MODEL OUTPUT";
    } else if (tierId === "tera") {
      adds = "For this " + condition + " case, Tera would compare the validated signature across plants and reveal whether the same failure pattern is repeating. A second plant is required to run that comparison.";
      provenance = "ROLE SIMULATION · REPLAY ENDS AT ONE PLANT";
    } else if (tierId === "peta") {
      adds = "For this " + condition + " case, Peta would turn enterprise comparisons into a regional priority and carry the response on leaner hardware. Regional plant records are required to run that work.";
      provenance = "ROLE SIMULATION · REPLAY ENDS AT ONE PLANT";
    } else {
      adds = "For this " + condition + " case, Exa would turn the verified miss, field evidence, and safe handoff into an evaluation and teaching signal for Pico, Nano, and the rest of the family.";
      provenance = "ROLE SIMULATION · REPLAY ENDS AT ONE PLANT";
    }
    return {
      tier: tierId,
      record: r.node_id,
      sensor: type ? type.label : ((r.window && r.window.tag) || "RECORDED SENSOR"),
      condition: condition,
      signal: caseSignalClue(r),
      handoff: readerHandoff(r),
      family: FAMILY_CASE_LINE,
      mechanic: caseMechanic(r),
      adds: adds,
      provenance: provenance,
    };
  }

  /* THE MODELS ALWAYS WATCH. These three states describe only what happens
     AFTER a finding. Keeping that separate from derive() matters: staffing
     cannot turn a correct model result amber, and granting authority cannot
     turn a recorded miss green. The legacy booleans remain the wire/storage
     shape; this plain object is the product language. */
  function responseMode() {
    if (PATCH.operator) {
      return { id: "human", label: "HUMAN REVIEW", short: "sent to human review",
               hint: "Models keep watching; a person reviews each finding." };
    }
    if (PATCH.authority) {
      return { id: "policy", label: "POLICY QUEUE", short: "sent to the policy queue",
               hint: "Models keep watching; findings enter the configured policy queue." };
    }
    return { id: "log", label: "LOG ONLY", short: "findings logged only",
             hint: "Models keep watching; findings are recorded without an automatic response." };
  }

  function watchingLabel() {
    if (!PATCH.chain.length) return "○ NO MODEL SEATED";
    return chainInfo().reader ? "● MODELS WATCHING" : "○ NO RECORDED READER";
  }

  // A prompt reply cites the selection (readings) and the chain (the DRAFT's
  // addressee). When either moves, the card on screen would be describing a
  // bench that no longer exists - so context moves dismiss it. (v11: caught
  // live - a TEMP reading card survived a switch to VIBRATION.)
  function contextMoved() {
    PATCH.reply = null;
    PATCH.touched = true;   // v38: a score for a game you have not started is noise
  }

  function selectType(key, cond) {
    var t = typeOf(key);
    if (!t) return;
    contextMoved();
    PATCH.typeKey = key;
    PATCH.cond = (cond != null && t.recIdx[cond] != null) ? cond
      : (t.recIdx[PATCH.cond] != null ? PATCH.cond : t.conds[0]);
  }

  /* =====================================================================
     THE CASCADE - the output at each stage, recorded fields only
     ===================================================================== */
  function stages() {
    var out = [];
    var r = currentRecord();
    if (!r) return out;
    var w = r.window || {};
    out.push({ kind: "raw", who: "WHAT THE SENSOR SENT", tech: "raw wire", body: w.body ||
      "(this record's window was not exported - the log is absent, not invented)",
      tag: w.tag, unit: unitWordOf(r), r: r });

    var info = chainInfo();
    var escalated = false, answered = false;
    PATCH.chain.forEach(function (id, i) {
      var fam = familyById(id);
      if (!fam) return;
      if (fam.status !== "recorded") {
        out.push(unrecordedStage(fam));
        return;
      }
      if (id === "pico" && i === info.picoAt) {
        var esc = r.child.margin < PATCH.floor;
        escalated = esc;
        if (!esc) answered = true;
        out.push({ kind: "pico", who: "WAVE PICO", role: "the small model on the machine", esc: esc,
                   said: r.child.prediction, margin: r.child.margin,
                   floor: PATCH.floor, ok: r.child.prediction === r.truth, r: r });
        return;
      }
      if (id === "nano") {
        if (i === info.nanoAt && info.senior && !escalated) {
          // The senior only speaks when a read reaches it. Keep the committed
          // parent result on the quiet stage as a labelled counterfactual: it
          // explains whether changing the floor could have rescued this read.
          out.push({ kind: "quietSenior", who: "WAVE NANO", role: "the bigger model it can ask",
                     wouldSay: r.parent.prediction, wouldMargin: r.parent.margin,
                     picoMargin: r.child.margin, floor: PATCH.floor, r: r,
                     note: "Wave Pico answered on its own at " + r.child.margin.toFixed(2) +
                       " >= " + PATCH.floor.toFixed(1) + ", so this read did not reach Wave Nano. " +
                       'Recorded counterfactual: Wave Nano also said "' + r.parent.prediction + '".' });
          return;
        }
        answered = true;
        out.push(nanoStage(r, w));
        return;
      }
      // a second pico, or a nano before a pico: pass-through, stated
      out.push({ kind: "silent", who: fam.label.toUpperCase(), fam: fam });
    });
    if (escalated && !answered) {
      out.push({ kind: "deadend", who: "NOBODY WAS LISTENING",
                 note: "Wave Pico asked for help, but there is no bigger model in the chain - so nobody answered." });
    }
    return out;
  }

  /* A tier with no recorded run never emits a prediction, a margin or a lamp
     state - that rail has not moved. What HAS moved is what it does instead of
     shrugging. Where the recording holds data at the tier's scope, the tier's
     JOB is done on that data and attributed to the bench:

       MICRO - the site brain: its site's machines, recounted
       GIGA  - the plant: every recorded machine, recounted, sites ranked

     Above Giga the recording runs out (one plant is not many plants; there is
     no region; the flagship TEACHES rather than reads), so those tiers state
     precisely what they would add and why the bench stops - which is a far
     more useful thing to read than "nothing that big here". */
  function unrecordedStage(fam) {
    var base = { who: fam.label.toUpperCase(), fam: fam, role: fam.reach,
                 takes: fam.takes, job: fam.job };
    var sc = scopeFor(fam.id);
    if (sc) {
      var info = chainInfo();
      base.kind = "scoperun";
      base.scope = sc;
      base.tally = tallyOver(sc.idxs, info);
      base.caseLens = caseLens(fam.id);
      base.policy = policyLine();
      return base;
    }
    base.kind = "scope";
    return base;
  }

  // The Nano's stage: the human-readable verdict. A fixed TEMPLATE over
  // recorded fields, plus the static glossary - never generated prose.
  function nanoStage(r, w) {
    var pred = r.parent.prediction;
    var isFault = r.truth !== "none";
    var predRight = pred === r.truth;
    var outcome = predRight
      ? (isFault ? "caught: the recorded truth is " + r.truth
                 : "quiet: the recorded truth is none")
      : (isFault ? "MISSED: the recorded truth is " + r.truth
                 : "false alarm: the recorded truth is none");
    var para = "On " + (w.tag || r.node_id) + ": " +
      (w.n ? w.n + " samples, " : "") +
      (w.lo != null ? w.lo + " … " + w.hi + (unitWordOf(r) ? " " + unitWordOf(r) : "") +
        ", mean " + w.mean + ". " : "") +
      'Wave Nano read the same numbers and said " ' + pred + '" - it was ' +
      r.parent.margin.toFixed(2) + " clear of its next guess - " + outcome + ".";
    return { kind: "nano", who: "WAVE NANO", role: "the bigger model it can ask", verdict: pred,
             gloss: GLOSSARY[pred] || null, para: para,
             ok: predRight, isFault: isFault, r: r };
  }

  /* =====================================================================
     THE DERIVATION - one record through one chain, recounted
     ===================================================================== */
  function derive() {
    if (!PATCH.measured) return;
    var st = stages();
    var state, why, label = null, sym = null;

    var r = currentRecord();
    var info = chainInfo();
    var isFault = r && r.truth !== "none";
    var hasRecorded = PATCH.chain.some(function (id) {
      var f = familyById(id); return f && f.status === "recorded";
    });

    if (!PATCH.chain.length) {
      state = "off"; why = "The sensor is emitting raw wire. Tap [+] on the rail to chain a model.";
    } else if (!hasRecorded || !info.reader) {
      state = "off";
      why = "Nothing in the chain has a recorded run on this bench - the lamp only glows " +
        "for recounts of recorded records. Chain Wave Pico or Wave Nano.";
    } else {
      // walk the record through the chain
      var missed = false, fixable = false, deadEnd = false, caught = false, falseAlarm = false;
      if (info.reader === "nano" && info.picoAt < 0) {
        var okN = r.parent.prediction === r.truth;
        if (okN) { if (isFault) caught = true; }
        else if (isFault) missed = true;
        else falseAlarm = true;
      } else {
        var esc = r.child.margin < PATCH.floor;
        if (!esc) {
          var okC = r.child.prediction === r.truth;
          if (okC) { if (isFault) caught = true; }
          else if (isFault) {
            missed = true;
            if (r.parent.prediction === r.truth && r.child.margin < TOP) fixable = true;
          } else falseAlarm = true;
        } else if (info.senior) {
          var okP = r.parent.prediction === r.truth;
          if (okP) { if (isFault) caught = true; }
          else if (isFault) missed = true;
          else falseAlarm = true;
        } else {
          deadEnd = true;
          if (isFault) missed = true;
        }
      }

      if (deadEnd && missed) {
        state = "red";
        why = "Wave Pico was not sure and had nobody to ask, so this fault went unheard. Add Wave Nano behind it.";
      } else if (fixable) {
        state = "red";
        why = "Wave Pico answered alone and got this one wrong. Raise the SURE ENOUGH knob: " +
          "ask for help sooner, and Wave Nano - which had the right answer in the recorded run - would have caught it.";
      } else if (deadEnd) {
        state = "yellow";
        why = "Wave Pico is not sure about this read and has nobody to ask. Add Wave Nano behind it.";
      } else if (!missed) {
        state = "green";
        why = caught ? "The recorded fault was caught by the model chain."
          : falseAlarm ? "False alarm - the chain called a fault on a recorded healthy sensor."
          : "All quiet: the sensor is healthy and the models agree.";
        if (caught) label = "FAULT CAUGHT";
        if (falseAlarm) { state = "yellow"; label = "CHECK CALL"; }
      } else {
        state = "red"; label = "MODEL LIMIT";
        if (info.reader === "nano" && info.picoAt < 0) {
          why = 'Wave Nano answered "' + r.parent.prediction + '" in the recorded run and got this ' +
            "fault wrong. The fault stays red because there is no recorded senior result above Nano on this bench.";
        } else if (r.child.margin >= PATCH.floor) {
          why = 'Wave Pico answered "' + r.child.prediction + '" at ' + r.child.margin.toFixed(2) +
            ", above the " + PATCH.floor.toFixed(1) + " SURE ENOUGH floor, so Nano was not called. " +
            'The recorded Nano counterfactual also said "' + r.parent.prediction +
            '", so raising the floor would not catch this fault.';
        } else {
          why = 'Wave Pico asked for help and Wave Nano answered "' + r.parent.prediction +
            '" in the recorded run. Nano also got this fault wrong, so no recorded model in this chain catches it.';
        }
      }
    }
    PATCH.verdict = { state: state, why: why, label: label, sym: sym,
                      response: responseMode(), stages: st };
    paintMonitor();
  }

  /* ---- the announcer + one-line ticker ------------------------------------ */
  function react(msg) {
    var strip = $("wpStrip");
    if (strip) {
      PATCH.step++;
      var li = el("li", "wp-strip__line");
      li.appendChild(el("span", "wp-strip__t", String(PATCH.step).padStart(3, "0")));
      li.appendChild(el("span", null, msg));
      strip.insertBefore(li, strip.firstChild);
      while (strip.childNodes.length > 2) strip.removeChild(strip.lastChild);
    }
    var say = $("wpSay");
    if (say) say.textContent = msg;
  }

  /* =====================================================================
     THE DIAL - tap to step, drag for detents, arrows for the keyboard
     ===================================================================== */
  function drawDial(opts) {
    var size = opts.size || 56;
    var wrap = el("div", "wp-knob");
    wrap.tabIndex = 0;
    wrap.setAttribute("role", "slider");
    wrap.setAttribute("aria-label", opts.name);
    wrap.setAttribute("aria-valuemin", "0");
    wrap.setAttribute("aria-valuemax", String(opts.values.length - 1));

    function angle(i) {
      if (opts.values.length === 1) return 0;
      return -135 + (i / (opts.values.length - 1)) * 270;
    }
    function paint() {
      wrap.textContent = "";
      wrap.setAttribute("aria-valuenow", String(opts.index));
      wrap.setAttribute("aria-valuetext", opts.labels[opts.index]);
      wrap.title = opts.tip ? opts.tip(opts.index) : "";
      var s = svg("svg", { viewBox: "0 0 64 64", class: "wp-knob__svg", "aria-hidden": "true",
                           width: size, height: size });
      s.appendChild(svg("circle", { class: "wp-knob__ring", cx: 32, cy: 32, r: 26 }));
      opts.values.forEach(function (_, i) {
        var a = (angle(i) - 90) * Math.PI / 180;
        s.appendChild(svg("line", {
          class: "wp-knob__tick" + (i === opts.index ? " is-on" : ""),
          x1: 32 + Math.cos(a) * 21, y1: 32 + Math.sin(a) * 21,
          x2: 32 + Math.cos(a) * 26, y2: 32 + Math.sin(a) * 26,
        }));
      });
      var g = svg("g", { class: "wp-knob__cap", transform: "rotate(" + angle(opts.index) + " 32 32)" });
      g.appendChild(svg("circle", { cx: 32, cy: 32, r: 17 }));
      g.appendChild(svg("line", { class: "wp-knob__needle", x1: 32, y1: 32, x2: 32, y2: 17 }));
      s.appendChild(g);
      wrap.appendChild(s);
      wrap.appendChild(el("span", "wp-knob__k", opts.labels[opts.index]));
    }
    function setIndex(i, fromTap) {
      i = Math.max(0, Math.min(opts.values.length - 1, i));
      if (i === opts.index && !fromTap) return;
      opts.index = i;
      paint();
      opts.onset(i);
    }

    var moved = false;
    wrap.addEventListener("pointerdown", function (e) {
      if (e.button != null && e.button !== 0) return;
      e.preventDefault();
      moved = false;
      var startY = e.clientY, startIdx = opts.index, pid = e.pointerId;
      wrap.setPointerCapture && wrap.setPointerCapture(pid);
      function mv(ev) {
        if (ev.pointerId !== pid) return;
        var step = Math.round((startY - ev.clientY) / 24);
        if (step) moved = true;
        var i = Math.max(0, Math.min(opts.values.length - 1, startIdx + step));
        if (i !== opts.index) setIndex(i);
      }
      function up(ev) {
        if (ev.pointerId !== pid) return;
        document.removeEventListener("pointermove", mv);
        document.removeEventListener("pointerup", up);
        document.removeEventListener("pointercancel", up);
      }
      document.addEventListener("pointermove", mv);
      document.addEventListener("pointerup", up);
      document.addEventListener("pointercancel", up);
    });
    // The tablet path: a plain tap steps the dial one position, wrapping,
    // like clicking a rotary switch through its stations.
    wrap.addEventListener("click", function (e) {
      e.stopPropagation();
      if (moved) { moved = false; return; }
      setIndex((opts.index + 1) % opts.values.length, true);
    });
    wrap.addEventListener("keydown", function (e) {
      if (e.key === "ArrowUp" || e.key === "ArrowRight") { e.preventDefault(); setIndex(opts.index + 1); }
      if (e.key === "ArrowDown" || e.key === "ArrowLeft") { e.preventDefault(); setIndex(opts.index - 1); }
    });
    paint();
    return wrap;
  }

  /* =====================================================================
     RENDER
     ===================================================================== */
  function render() {
    renderGameShell();
    renderFactory();
    renderSelector();
    renderPads();
    renderField();
    renderChain();
    renderWhys();
    renderTabs();
    paintMonitor();
    paintWire();
    renderOp();
    renderMirror();
    paintTour();
  }

  /* ---- THE FRONT DOOR --------------------------------------------------
     The workbench is a powerful sandbox, but it is a poor tutorial: every
     control looks equally important. The game starts with one promise and
     one action, then focus mode keeps only the current case in view. The
     sandbox is never removed; it is an explicit choice instead of the first
     thing a visitor has to decode. */
  function gameBeat() {
    var m = PATCH.mission;
    if (!m.active) return 0;
    if (m.phase === "verified" || m.phase === "clear") return 3;
    var plan = missionPlan();
    if (plan.locked) return 0;
    if (missionReady()) return 2;
    return 1;
  }

  function renderGamebar() {
    var host = $("wsGamebar");
    if (!host) return;
    host.textContent = "";
    if (PATCH.gameMode === "explore") {
      host.hidden = false;
      var back = el("button", "sn-gamebar__return", PATCH.mission.active
        ? "← RETURN TO YOUR FACTORY" : "START THE FACTORY GAME →");
      back.type = "button";
      back.addEventListener("click", function () {
        if (PATCH.mission.active) returnToFactory();
        else startMystery();
      });
      host.appendChild(back);
      return;
    }
    if (PATCH.gameMode !== "play") {
      host.hidden = true;
      return;
    }
    host.hidden = false;
    var beat = gameBeat();
    var t = currentType();
    var top = el("div", "sn-gamebar__top");
    var title = el("span", "sn-gamebar__case");
    title.appendChild(el("b", null, "CASE " + String(PATCH.mission.completed + 1).padStart(2, "0")));
    title.appendChild(el("span", null, (t ? t.label : "MYSTERY SENSOR") + " · " +
      (CONDW[PATCH.cond] || String(PATCH.cond).toUpperCase())));
    top.appendChild(title);
    var leave = el("button", "sn-gamebar__sandbox", "OPEN SANDBOX");
    leave.type = "button";
    leave.title = "Show every sensor, condition, model, note, and input";
    leave.addEventListener("click", openSandbox);
    top.appendChild(leave);
    host.appendChild(top);
    var steps = el("ol", "sn-gamebar__steps");
    [
      ["CATCH IT", "choose the right model move"],
      ["TRACE IT", "solve three signal clues"],
      ["CLOSE IT", "compare with a healthy read"],
    ].forEach(function (copy, index) {
      var state = index < beat ? " is-done" : index === beat && beat < 3 ? " is-now" : "";
      var item = el("li", "sn-gamebar__step" + state);
      item.appendChild(el("span", "sn-gamebar__pip", index < beat ? "✓" : String(index + 1)));
      var words = el("span", "sn-gamebar__words");
      words.appendChild(el("b", null, copy[0]));
      words.appendChild(el("span", null, copy[1]));
      item.appendChild(words);
      steps.appendChild(item);
    });
    host.appendChild(steps);
  }

  function renderGameShell() {
    var bench = $("wbBench");
    if (bench && bench.classList) {
      bench.classList.toggle("is-welcome", PATCH.gameMode === "welcome");
      bench.classList.toggle("is-playing", PATCH.gameMode === "play");
      bench.classList.toggle("is-exploring", PATCH.gameMode === "explore");
      bench.classList.toggle("is-inspecting", PATCH.gameMode !== "play" || PATCH.inspectionOpen);
    }
    var sensor = $("wsSensorStep"), model = $("wsModelStep"), output = $("wsOutputStep");
    if (sensor) sensor.firstChild.textContent = PATCH.gameMode === "play" ? "YOUR MACHINE" : "1 · PICK A SENSOR";
    if (model) model.firstChild.textContent = PATCH.gameMode === "play" ? "YOUR MODEL CHAIN " : "2 · ADD MODELS ";
    if (output) output.textContent = PATCH.gameMode === "play" ? "YOUR CASE" : "3 · WHAT THEY SAID";
    renderGamebar();
  }

  function startMystery() {
    var firstStart = PATCH.gameMode === "welcome";
    PATCH.gameMode = "play";
    PATCH.tour = -1;
    PATCH.detail = false;
    PATCH.inspectionOpen = false;
    // The factory starts small: one machine reader. The first measured card
    // teaches why a line gateway exists by making the player place Nano.
    if (firstStart) PATCH.chain = ["pico"];
    else if (!PATCH.chain.length) PATCH.chain = ["pico"];
    var pick = drawIncident();
    render();
    react(pick
      ? "Mystery case opened. Start on the television: catch the fault, trace the clues, and close the case."
      : "The case deck is not ready yet.");
    var action = document.querySelector(".sn-factory__primary, .sn-factory__choice");
    if (action && action.scrollIntoView) action.scrollIntoView({ block: "center", behavior: REDUCED ? "auto" : "smooth" });
    if (action && action.focus) action.focus({ preventScroll: true });
    return pick;
  }

  function openSandbox() {
    PATCH.gameMode = "explore";
    PATCH.inspectionOpen = true;
    PATCH.tour = -1;
    render();
    react("Sandbox open. Every recorded sensor, condition, model tier, and technical note is available.");
    var sensor = document.querySelector(".sn-type.is-sel");
    if (sensor && sensor.focus) sensor.focus({ preventScroll: true });
  }

  function returnToFactory() {
    PATCH.gameMode = "play";
    PATCH.inspectionOpen = false;
    PATCH.detail = false;
    render();
    var action = document.querySelector(".sn-factory__primary, .sn-factory__choice");
    if (action && action.scrollIntoView) action.scrollIntoView({ block: "center", behavior: REDUCED ? "auto" : "smooth" });
    if (action && action.focus) action.focus({ preventScroll: true });
  }

  function wireGameDoor() {
    var start = $("wsStartGame"), explore = $("wsExplore");
    if (start) start.addEventListener("click", startMystery);
    if (explore) explore.addEventListener("click", openSandbox);
  }

  function factoryModelLabel(id, identify) {
    var fam = familyById(id);
    if (id === "floor") return "TUNE PICO'S HANDOFF";
    if (!fam) return String(id).toUpperCase();
    if (identify) return fam.label.toUpperCase() + " MADE THE CALL";
    if (PATCH.chain.indexOf(id) >= 0) return fam.label.toUpperCase() + " IS ALREADY BUILT";
    if (id === "pico") return "PLACE PICO ON THE MACHINE";
    if (id === "nano") return "INSTALL NANO AT THE LINE GATEWAY";
    if (id === "micro") return "ADD MICRO TO SITE CONTROL";
    if (id === "giga") return "ADD GIGA AT PLANT CONTROL";
    return "PLACE " + fam.label.toUpperCase();
  }

  function factoryNode(label, sub, cls, tier) {
    var node = el("div", "sn-factory__node" + (cls ? " " + cls : ""));
    if (tier) tierStyle(node, tier);
    node.appendChild(el("b", null, label));
    node.appendChild(el("span", null, sub));
    return node;
  }

  function openInspection() {
    PATCH.inspectionOpen = true;
    PATCH.detail = true;
    render();
    var deck = document.querySelector(".sn-deck");
    if (deck && deck.scrollIntoView) deck.scrollIntoView({ block: "start", behavior: REDUCED ? "auto" : "smooth" });
    var first = document.querySelector(".sn-mission__movechoice, .sn-mission [data-field-step] button:not([disabled])");
    if (first && first.focus) first.focus({ preventScroll: true });
  }

  function shipBatch() {
    var m = PATCH.mission;
    if (!m.active || (m.phase !== "verified" && m.phase !== "clear")) return false;
    PATCH.factory.shipped++;
    if (PATCH.factory.shipped >= PATCH.factory.goal) {
      render();
      react("Contract complete: three reliable batches shipped. Keep building to test the next part of the factory.");
      return true;
    }
    dealAndRender();
    return true;
  }

  function focusFactoryRelease() {
    if (PATCH.gameMode !== "play") return;
    PATCH.inspectionOpen = false;
    PATCH.detail = false;
    render();
    var release = document.querySelector(".sn-factory__primary");
    if (release && release.scrollIntoView) release.scrollIntoView({ block: "center", behavior: REDUCED ? "auto" : "smooth" });
    if (release && release.focus) release.focus({ preventScroll: true });
  }

  function renderFactory() {
    var host = $("wsFactory");
    if (!host) return;
    host.textContent = "";
    host.hidden = PATCH.gameMode !== "play";
    if (PATCH.gameMode !== "play") return;
    var m = PATCH.mission;
    var r = currentRecord();
    var t = currentType();
    var phaseReady = m.phase === "verified" || m.phase === "clear";
    var won = PATCH.factory.shipped >= PATCH.factory.goal;

    var head = el("header", "sn-factory__head");
    var title = el("div", "sn-factory__title");
    title.appendChild(el("span", null, "WAVE FACTORY · CONTRACT 01"));
    title.appendChild(el("h2", null, won ? "Factory online." : "Ship 3 reliable batches."));
    title.appendChild(el("p", null, won
      ? "You built a model chain that can catch, investigate, and safely route signal problems."
      : "Bad signals stop packout. Build the smallest model chain that can catch each one, then trace the fault and release the line."));
    head.appendChild(title);
    var score = el("div", "sn-factory__score");
    score.appendChild(el("b", null, String(PATCH.factory.shipped).padStart(2, "0") + " / " + String(PATCH.factory.goal).padStart(2, "0")));
    score.appendChild(el("span", null, "BATCHES SHIPPED"));
    head.appendChild(score);
    host.appendChild(head);

    var contract = el("div", "sn-factory__contract");
    var boxes = el("div", "sn-factory__boxes");
    for (var bi = 0; bi < PATCH.factory.goal; bi++) {
      var crate = el("span", "sn-factory__box" + (bi < PATCH.factory.shipped ? " is-shipped" : bi === PATCH.factory.shipped ? " is-next" : ""));
      crate.appendChild(el("i", null, bi < PATCH.factory.shipped ? "✓" : "◇"));
      crate.appendChild(el("b", null, "BATCH " + String(bi + 1).padStart(2, "0")));
      boxes.appendChild(crate);
    }
    contract.appendChild(boxes);
    contract.appendChild(el("span", "sn-factory__linestate " + (phaseReady ? "is-ready" : "is-stopped"),
      won ? "CONTRACT COMPLETE" : phaseReady ? "PACKOUT READY" : "LINE STOPPED · SIGNAL HOLD"));
    host.appendChild(contract);

    var floor = el("section", "sn-factory__floor");
    floor.setAttribute("aria-label", "Production line and model defense layers");
    var prodHead = el("div", "sn-factory__railhead");
    prodHead.appendChild(el("b", null, "PRODUCTION LINE"));
    prodHead.appendChild(el("span", null, "YOUR MOVE · THE LINE WAITS FOR YOU"));
    floor.appendChild(prodHead);
    var production = el("div", "sn-factory__line");
    production.appendChild(factoryNode("RAW FEED", "material enters", "is-source"));
    production.appendChild(el("span", "sn-factory__belt", "→"));
    production.appendChild(factoryNode("PROCESS CELL", t ? t.label : "sensor", "is-machine"));
    production.appendChild(el("span", "sn-factory__belt", "→"));
    production.appendChild(factoryNode("QUALITY GATE", phaseReady ? "route verified" : "waiting on signal", phaseReady ? "is-clear" : "is-blocked"));
    production.appendChild(el("span", "sn-factory__belt", "→"));
    production.appendChild(factoryNode("PACKOUT", phaseReady ? "ready to ship" : "held", "is-packout"));
    floor.appendChild(production);

    var defenseHead = el("div", "sn-factory__railhead sn-factory__railhead--models");
    defenseHead.appendChild(el("b", null, "SIGNAL DEFENSE"));
    defenseHead.appendChild(el("span", null, "machine → line → site → plant"));
    floor.appendChild(defenseHead);
    var defense = el("div", "sn-factory__defense");
    [
      ["pico", "ON MACHINE", "reads one signal"],
      ["nano", "LINE GATEWAY", "hears Pico's doubt"],
      ["micro", "SITE CONTROL", "connects facility clues"],
      ["giga", "PLANT CONTROL", "compares every site"],
    ].forEach(function (slot) {
      var seated = PATCH.chain.indexOf(slot[0]) >= 0;
      var node = factoryNode(seated ? familyById(slot[0]).label.toUpperCase() : "EMPTY SLOT",
        slot[1] + " · " + slot[2], "sn-factory__model" + (seated ? " is-seated" : " is-empty"), slot[0]);
      node.appendChild(el("i", "sn-factory__modelnote", seated ? "ONLINE" : "NOT BUILT"));
      defense.appendChild(node);
    });
    floor.appendChild(defense);
    var expansionHead = el("div", "sn-factory__railhead sn-factory__railhead--expansion");
    expansionHead.appendChild(el("b", null, "FACTORY EXPANSION"));
    expansionHead.appendChild(el("span", null, "future contracts · beyond this one-plant replay"));
    floor.appendChild(expansionHead);
    var expansion = el("div", "sn-factory__expansion");
    [
      ["tera", "ENTERPRISE", "coordinates multiple plants"],
      ["peta", "REGION", "connects several enterprises"],
      ["exa", "FLAGSHIP LAB", "teaches the smaller model family"],
    ].forEach(function (slot) {
      var node = factoryNode(familyById(slot[0]).label.toUpperCase(),
        slot[1] + " · " + slot[2], "sn-factory__model is-future", slot[0]);
      node.appendChild(el("i", "sn-factory__modelnote", "LOCKED · COMPLETE LATER CONTRACTS"));
      expansion.appendChild(node);
    });
    floor.appendChild(expansion);
    host.appendChild(floor);

    var event = el("section", "sn-factory__event");
    var eventCopy = el("div", "sn-factory__eventcopy");
    eventCopy.appendChild(el("span", null, won ? "FACTORY COMPLETE" : phaseReady ? "LINE RECOVERED" : "NEW SIGNAL AT THE QUALITY GATE"));
    eventCopy.appendChild(el("h3", null, won ? "Contract shipped." : phaseReady
      ? "The evidence route is ready. Release this batch."
      : (t ? t.label : "SENSOR") + " is reading " + (CONDW[PATCH.cond] || String(PATCH.cond).toUpperCase()) + "."));
    eventCopy.appendChild(el("p", null, won
      ? "You can keep running batches or open the sandbox to redesign the chain."
      : phaseReady ? "Your signal route passed its healthy comparison. Release the batch and keep the line moving."
      : "Packout is paused before a questionable signal becomes a bad decision. Decide where this read belongs in the model ladder."));
    event.appendChild(eventCopy);

    var play = el("div", "sn-factory__play");
    var move = !phaseReady ? incidentMove() : null;
    if (won) {
      var keep = el("button", "sn-factory__primary", "RUN ANOTHER BATCH →");
      keep.type = "button";
      keep.addEventListener("click", dealAndRender);
      play.appendChild(keep);
    } else if (phaseReady) {
      var ship = el("button", "sn-factory__primary", "RELEASE TO PACKOUT · SHIP BATCH →");
      ship.type = "button";
      ship.addEventListener("click", shipBatch);
      play.appendChild(ship);
    } else if (move && move.correct) {
      play.appendChild(el("b", "sn-factory__question", move.question));
      var choices = el("div", "sn-factory__choices");
      move.choices.forEach(function (choice) {
        var button = el("button", "sn-factory__choice");
        button.type = "button";
        if (choice.id !== "floor") tierStyle(button, choice.id);
        button.appendChild(el("b", null, factoryModelLabel(choice.id, move.kind === "identify")));
        button.appendChild(el("span", null, choice.role));
        button.addEventListener("click", function () {
          var changed = missionChooseMove(choice.id);
          if (!changed) renderFactory();
          else render();
        });
        choices.appendChild(button);
      });
      play.appendChild(choices);
      if (m.moveFeedback) play.appendChild(el("p", "sn-factory__feedback is-" + m.moveFeedback.kind, m.moveFeedback.text));
      var inspect = el("button", "sn-factory__inspect", "INSPECT THE RECORDED SIGNAL ON THE CRT");
      inspect.type = "button";
      inspect.addEventListener("click", openInspection);
      play.appendChild(inspect);
    } else {
      var clues = el("button", "sn-factory__primary", "OPEN THE CLUE KIT →");
      clues.type = "button";
      clues.addEventListener("click", openInspection);
      play.appendChild(clues);
      play.appendChild(el("p", "sn-factory__feedback", "The model route is built. Use the inspection room to trace three clues and release the line."));
    }
    event.appendChild(play);
    host.appendChild(event);
  }

  /* ---- THE SCALE LADDER -------------------------------------------------
     One engraved icon per tier, drawn as a single family, so "how big is
     this model" is answered by the hardware it takes to run it and needs no
     number: a chip on its pins -> a gateway box -> a rack-mount server -> a
     cabinet -> three cabinets -> a datacenter aisle -> a hall.
     Source dimensions live here because the sizing rule needs the aspect.
     The rule is AREA, not longest-side: these engravings run from 3.2:1
     (a 1U server, wide and flat) to 0.65:1 (a cabinet, tall and narrow),
     and scaling by longest side would draw the flat server BIGGER than the
     cabinet above it - inverting the very ladder the art exists to show.
     Equal-area scaling is how the eye actually reads "bigger", so each
     tier's ink area climbs monotonically while every engraving keeps its
     own proportions. The parameter band beside the icon carries the exact
     numbers for anyone counting. */
  // measured from the committed plates - the box is cut to the art's own
  // aspect, so a wrong number here would stretch the engraving
  var ART = {
    chip:    { w: 347, h: 372 },
    gateway: { w: 425, h: 290 },
    server:  { w: 505, h: 160 },
    rack:    { w: 300, h: 460 },
    racks:   { w: 485, h: 300 },
    aisle:   { w: 512, h: 512 },
    hall:    { w: 512, h: 512 },
  };
  function modelIcon(fam, span) {
    var a = ART[fam.art] || ART.gateway;
    var s = span || fam.span;          // the tier's rung, as a side-length
    var ratio = a.w / a.h;
    var h = Math.sqrt((s * s) / ratio); // same ink area for the same rung
    var box = el("span", "ws-icon ws-icon--" + fam.art);
    box.style.width = Math.round(h * ratio) + "px";
    box.style.height = Math.round(h) + "px";
    box.setAttribute("aria-hidden", "true");
    box.appendChild(el("span", "wb-plate__ink"));
    return box;
  }
  // the rail is tighter than the menu, so the same ladder is drawn smaller
  var CARD_SCALE = 0.85;

  /* ---- the sensor selector: one button per RECORDED type ----------------- */
  function renderSelector() {
    var host = $("wsTypes");
    if (!host) return;
    host.textContent = "";
    host.setAttribute("role", "radiogroup");
    host.setAttribute("data-tour", "sensors");
    host.setAttribute("aria-label", "Sensor type - derived from the recorded tags");
    PATCH.types.forEach(function (t) {
      var sel = PATCH.typeKey === t.key;
      var b = el("button", "sn-type" + (sel ? " is-sel" : ""));
      b.type = "button";
      b.setAttribute("role", "radio");
      b.setAttribute("aria-checked", sel ? "true" : "false");
      var art = el("span", "sn-type__art sn-type__art--" + t.icon);
      art.setAttribute("aria-hidden", "true");
      art.appendChild(el("span", "wb-plate__ink"));
      b.appendChild(art);
      var txt = el("span", "sn-type__txt");
      txt.appendChild(el("b", null, t.label));
      txt.appendChild(el("span", null, t.count + " recorded"));
      b.appendChild(txt);
      // the fan-in, made visible: every lane carries its own read under the
      // same chain, recounted from its OWN record (see laneRead)
      var lr = laneRead(t);
      if (lr) {
        var dot = el("span", "sn-type__dot " + lr.cls);
        dot.setAttribute("aria-hidden", "true");
        b.appendChild(dot);
        var says = lr.said == null
          ? "asked for help, and nothing bigger is chained"
          : lr.who + ' said " ' + lr.said + '"';
        b.setAttribute("aria-label", t.label + " - " + lr.cLabel + ", " + says);
        b.title = lr.tag + " dialed " + lr.cLabel + " (record " + lr.node + ") - " + says +
          (lr.sel ? " · this is the one on the monitor" : " · read by the same chain");
      }
      if (!lr) b.title = t.key === "unnamed"
        ? "Sparkplug aliases with no name - the wire never said what these measure. Real plants are full of them."
        : "grouped from the recorded tags ending _" + t.label.replace(/ /g, "_");
      b.addEventListener("click", function () {
        var training = PATCH.mission.active;
        selectType(t.key);
        if (training) beginSelectedIncident(false);
        else derive();
        render();
        var r = currentRecord();
        react(t.label + " selected - emitting " + ((r.window && r.window.tag) || "its wire") + ".");
        var again = document.querySelector(".sn-type.is-sel");
        if (again) again.focus({ preventScroll: true });
      });
      host.appendChild(b);
    });
  }

  /* ---- THE GUIDED TOUR ---------------------------------------------------
     The founder kept saying the deck was "convoluted and hard to understand"
     and asked to "navigate the user to what is happening". The nudge chip it
     replaces was one hint pointing at one control; this walks the whole line
     once - sensor, the model on the machine, the one it asks, the answer -
     highlighting each zone as it names it.

     One-shot and localStorage-gated like the mode switch: it never returns
     after finishing or being skipped. Keyboard-operable throughout, and
     under reduced motion it still works - only the transitions go. */
  var TOUR = [
    { at: "sensors", title: "1. A sensor sends numbers",
      body: "These are real channels from our test bench. Each one is a recorded window - " +
            "pick the machine you want to watch." },
    { at: "pads", title: "2. Something happens to it",
      body: "Press a condition. Every pad replays a real recorded reading, so a fault you " +
            "press is a fault that really happened." },
    { at: "machine", title: "3. A small model reads it, right on the machine",
      body: "Wave Pico runs on the device itself. It answers with one word and how sure it " +
            "was - and it only answers alone when it is sure enough." },
    { at: "line,sensors", title: "4. One model, many machines",
      body: "The mesh fans in: one Pico reads one machine's channels, one Nano rolls up many " +
            "Picos, one Micro reasons across many fleets. Every sensor on the left is being " +
            "read by the same chain - this bench shows one at a time so you can follow it." },
    { at: "gateway", title: "5. When it is not sure, it asks",
      body: "The read climbs to a bigger model at the gateway. That hand-off is the whole " +
            "idea: small models everywhere, a big one only when it is needed." },
    { at: "monitor", title: "6. This is what came out",
      body: "The answer, and what each model did to get there. Change anything on the left " +
            "and watch this change with it." },
  ];
  function tourWanted() {
    try { return !window.localStorage.getItem("pb.meshTour"); }
    catch (e) { return false; }
  }
  function tourEnd(silent) {
    PATCH.tour = -1;
    PATCH._tourFocused = null;
    try { window.localStorage.setItem("pb.meshTour", "seen"); } catch (e) { /* private mode */ }
    render();
    if (!silent) {
      var t0 = document.querySelector(".sn-type");
      if (t0) t0.focus({ preventScroll: true });
    }
  }
  function tourGo(i) {
    if (i >= TOUR.length) { tourEnd(); return; }
    PATCH.tour = i;
    render();                       // paintTour focuses this step's action
    var card = document.querySelector(".sn-tour");
    if (card && card.scrollIntoView) {
      card.scrollIntoView({ block: "nearest", behavior: REDUCED ? "auto" : "smooth" });
    }
  }
  // the step's card is planted in the zone it is talking about, and the deck
  // dims everything else, so "what is happening" has one answer at a time
  function paintTour() {
    var deck = document.querySelector(".syn");
    if (!deck) return;
    // clear the previous step completely: a card appended into a zone that
    // does not re-render (the TV) would otherwise survive into the next step
    deck.querySelectorAll(".sn-tour").forEach(function (n) {
      if (n.parentNode) n.parentNode.removeChild(n);
    });
    deck.querySelectorAll(".is-tourlit").forEach(function (n) {
      n.classList.remove("is-tourlit");
    });
    deck.classList.toggle("is-touring", PATCH.tour >= 0);
    if (PATCH.tour < 0 || PATCH.tour >= TOUR.length) return;
    var step = TOUR[PATCH.tour];
    // a step may name more than one zone - the fan-in step is ABOUT the
    // relationship between the sensor wall and the line, so dimming one of
    // them while describing both would fight the sentence. The first zone
    // named is where the card is planted; every zone named is lit.
    var names = step.at.split(",");
    var lit = [];
    names.forEach(function (nm) {
      var n = deck.querySelector('[data-tour="' + nm + '"]');
      if (n) { n.classList.add("is-tourlit"); lit.push(n); }
    });
    var target = lit[0];
    if (!target) return;

    var card = el("div", "sn-tour");
    card.setAttribute("role", "dialog");
    card.setAttribute("aria-label", "Guided tour, step " + (PATCH.tour + 1) + " of " + TOUR.length);
    var dots = el("div", "sn-tour__dots");
    TOUR.forEach(function (_, k) {
      dots.appendChild(el("span", "sn-tour__dot" + (k === PATCH.tour ? " is-on" : "")));
    });
    card.appendChild(dots);
    card.appendChild(el("b", "sn-tour__title", step.title));
    card.appendChild(el("p", "sn-tour__body", step.body));
    var row = el("div", "sn-tour__row");
    var skip = el("button", "sn-tour__skip");
    skip.type = "button";
    skip.textContent = "skip";
    skip.addEventListener("click", function (e) { e.stopPropagation(); tourEnd(); });
    row.appendChild(skip);
    var next = el("button", "sn-tour__next");
    next.type = "button";
    next.textContent = PATCH.tour === TOUR.length - 1 ? "got it" : "next";
    next.addEventListener("click", function (e) { e.stopPropagation(); tourGo(PATCH.tour + 1); });
    row.appendChild(next);
    card.appendChild(row);
    // the deck is the positioning context, so no zone's overflow can clip the
    // card; it sits under its target, or above when there is no room below
    deck.appendChild(card);
    var db = deck.getBoundingClientRect(), tb = target.getBoundingClientRect();
    var top = tb.bottom - db.top + 10;
    var cardH = card.offsetHeight || 150;
    if (top + cardH > deck.offsetHeight && tb.top - db.top - cardH - 10 > 0) {
      top = tb.top - db.top - cardH - 10;
    }
    var left = Math.max(8, Math.min(tb.left - db.left, deck.offsetWidth - card.offsetWidth - 8));
    card.style.top = Math.round(top) + "px";
    card.style.left = Math.round(left) + "px";

    // a step change moves focus to its action, so the whole tour is walkable
    // with Enter; a plain repaint must NOT steal focus back from the visitor
    if (PATCH._tourFocused !== PATCH.tour) {
      PATCH._tourFocused = PATCH.tour;
      if (next.focus) next.focus({ preventScroll: true });
    }
  }

  /* ---- the pads: one backlit pad per RECORDED condition of the type ------ */
  function renderPads() {
    var host = $("wsPads");
    if (!host) return;
    host.textContent = "";
    host.setAttribute("data-tour", "pads");
    var t = currentType();
    if (!t) return;
    var head = el("div", "syn-pads__head");
    head.appendChild(el("b", null, "WHAT'S HAPPENING TO IT"));
    // the honesty rule stays ON the surface, in plain words: a pad exists only
    // where we have a real recording, so nobody reads the row as "all faults"
    var padSub = el("span", "sn-sub",
      "press one - each pad replays a real recorded reading, so a condition we never recorded has no pad");
    padSub.title = "one pad per recorded condition for this sensor - nothing here is simulated";
    head.appendChild(padSub);
    host.appendChild(head);
    var row = el("div", "syn-pads__row");
    row.setAttribute("role", "radiogroup");
    row.setAttribute("aria-label", "Condition - each pad replays one recorded instance");
    t.conds.forEach(function (c) {
      var pad = el("button", "syn-pad" + (PATCH.cond === c ? " is-lit" : "") +
        (c === "none" ? " syn-pad--ok" : ""));
      pad.type = "button";
      pad.setAttribute("role", "radio");
      pad.setAttribute("aria-checked", PATCH.cond === c ? "true" : "false");
      var rr = PATCH.measured.records[t.recIdx[c]];
      pad.title = (t.pickWhy && t.pickWhy[c] ? t.pickWhy[c] + " · " : "") +
        "replays recorded record " + rr.node_id + " (scene " + rr.scene_id +
        ") - a pad is a recorded instance, selected, not simulated";
      var cap = el("span", "syn-pad__cap");
      var sp = sparkline(rr);
      if (sp) cap.appendChild(sp);
      pad.appendChild(cap);
      pad.appendChild(el("span", "syn-pad__k", CONDW[c] || c.toUpperCase()));
      pad.addEventListener("click", function () {
        contextMoved();
        PATCH.cond = c;
        beginSelectedIncident(false);
        render();
        var r = currentRecord();
        react("Condition " + (CONDW[c] || c.toUpperCase()) + " - replaying " +
          ((r.window && r.window.tag) || r.node_id) + ".");
        var again = document.querySelector(".syn-pad.is-lit");
        if (again) again.focus({ preventScroll: true });
      });
      row.appendChild(pad);
    });
    host.appendChild(row);

    // the selected record's identity, honest to the wire
    var r = currentRecord();
    if (r) {
      var w = r.window || {};
      var id = el("p", "sn-now");
      id.appendChild(el("b", null, w.tag || r.node_id));
      id.appendChild(el("span", null, " · " + (unitWordOf(r) || "unit not stated") +
        (w.n ? " · " + w.n + " samples" : "")));
      if (!unitWordOf(r)) id.title = "the wire did not state a unit - a defaulted unit would be an invented fact";
      host.appendChild(id);
      /* v38 (UX audit: the left column is too much at once): the gauge
         repeats the trace already on the glass, so it lives under the
         MORE fold with the case tools - one click away, never in the way */
      var vu = (PATCH.sideMore || PATCH.gameMode === "play") ? drawVU(w, unitWordOf(r)) : null;
      if (vu) {
        var vwrap = el("div", "sn-vuwell");
        vwrap.appendChild(vu);
        host.appendChild(vwrap);
      }
    }
  }

  /* ---- FIELD TRAINING ---------------------------------------------------
     The CRT and left bay are two views of ONE local training rig. The CRT
     keeps the explanation beside the active control; the bay keeps the whole
     bench scannable. All clues are operator-authored training context. A
     control changes only PATCH.mission and never the recorded window, model
     stages or broker. */
  function currentFieldControl() {
    var plan = missionPlan();
    if (!plan.rig) return null;
    for (var i = 0; i < plan.rig.controls.length; i++) {
      if (!PATCH.mission.actions[plan.rig.controls[i].id]) return plan.rig.controls[i];
    }
    return null;
  }

  function fieldControlEnabled(control, plan) {
    if (!control || !plan || plan.locked || plan.clear || PATCH.mission.actions[control.id]) return false;
    var at = plan.rig.controls.indexOf(control);
    return at === 0 || !!PATCH.mission.actions[plan.rig.controls[at - 1].id];
  }

  function focusFieldStep(id) {
    var zone = detailOpen()
      ? document.querySelector('.sn-mission [data-field-step="' + id + '"]')
      : null;
    if (!zone) zone = document.querySelector('[data-field-step="' + id + '"]');
    if (!zone) return false;
    if (zone.scrollIntoView) {
      zone.scrollIntoView({ block: "nearest", behavior: REDUCED ? "auto" : "smooth" });
    }
    var target = zone.querySelector("button:not([disabled]), [role=slider]:not([aria-disabled=true])");
    if (target && target.focus) target.focus({ preventScroll: true });
    return !!target;
  }

  function applyFieldFromUI(control, choice) {
    var passed = fieldApply(control.id, choice);
    var feedback = PATCH.mission.field && PATCH.mission.field.feedback;
    var next = passed ? currentFieldControl() : control;
    renderField();
    paintMonitor();
    renderGamebar();
    react(feedback ? feedback.text : "That training control is not available yet.");
    if (next) focusFieldStep(next.id);
    else {
      var verify = detailOpen()
        ? document.querySelector(".sn-mission .sn-mission__verify")
        : document.querySelector("#wsField .sn-field__verify");
      if (detailOpen() && verify) glassScrollTo(verify.closest(".sn-mission__ready"), true);
      if (verify && verify.focus) verify.focus({ preventScroll: true });
    }
  }

  function drawFieldDial(control, enabled) {
    var wrap = el("div", "sn-field__dialbank");
    var values = PATCH.mission.field.values || {};
    var selected = values[control.id];
    var at = control.choices.findIndex(function (choice) { return choice.id === selected; });
    var dial = el("button", "sn-field__dial");
    dial.type = "button";
    dial.disabled = !enabled;
    dial.setAttribute("role", "slider");
    dial.setAttribute("aria-label", control.label + " training detent");
    dial.setAttribute("aria-valuemin", "0");
    dial.setAttribute("aria-valuemax", String(control.choices.length - 1));
    dial.setAttribute("aria-valuenow", String(Math.max(0, at)));
    dial.setAttribute("aria-valuetext", at < 0 ? "not set" : control.choices[at].label);
    dial.style.setProperty("--field-turn", (-120 + Math.max(0, at) * (240 / Math.max(1, control.choices.length - 1))) + "deg");
    dial.appendChild(el("span", "sn-field__dialcap"));
    function choose(index) {
      index = Math.max(0, Math.min(control.choices.length - 1, index));
      applyFieldFromUI(control, control.choices[index].id);
    }
    dial.addEventListener("click", function () { choose((at + 1 + control.choices.length) % control.choices.length); });
    dial.addEventListener("keydown", function (e) {
      if (e.key === "ArrowLeft" || e.key === "ArrowDown") { e.preventDefault(); choose(at <= 0 ? 0 : at - 1); }
      if (e.key === "ArrowRight" || e.key === "ArrowUp") { e.preventDefault(); choose(at < 0 ? 0 : at + 1); }
    });
    wrap.appendChild(dial);
    var detents = el("div", "sn-field__detents");
    control.choices.forEach(function (choice) {
      var b = el("button", "sn-field__detent" + (choice.id === selected ? " is-on" : ""), choice.label);
      b.type = "button";
      b.disabled = !enabled;
      b.setAttribute("aria-pressed", choice.id === selected ? "true" : "false");
      b.addEventListener("click", function () { applyFieldFromUI(control, choice.id); });
      detents.appendChild(b);
    });
    wrap.appendChild(detents);
    return wrap;
  }

  function drawFieldSwitch(control, enabled) {
    var row = el("div", "sn-field__switches");
    row.setAttribute("role", "group");
    row.setAttribute("aria-label", control.label);
    var selected = PATCH.mission.field.values[control.id];
    control.choices.forEach(function (choice) {
      var b = el("button", "sn-field__switch" + (selected === choice.id ? " is-on" : ""), choice.label);
      b.type = "button";
      b.disabled = !enabled;
      b.setAttribute("aria-pressed", selected === choice.id ? "true" : "false");
      b.addEventListener("click", function () { applyFieldFromUI(control, choice.id); });
      row.appendChild(b);
    });
    return row;
  }

  function drawFieldControl(control, index, plan) {
    var done = !!PATCH.mission.actions[control.id];
    var enabled = fieldControlEnabled(control, plan);
    var box = el("section", "sn-field__control" + (done ? " is-done" : enabled ? " is-active" : " is-locked"));
    box.dataset.fieldStep = control.id;
    box.setAttribute("data-field-step", control.id);
    var head = el("div", "sn-field__controlhead");
    head.appendChild(el("span", "sn-field__lamp", done ? "✓" : String(index + 1).padStart(2, "0")));
    head.appendChild(el("b", null, control.label));
    box.appendChild(head);
    var question = el("div", "sn-field__question");
    question.appendChild(el("b", null, "YOUR CLUE"));
    question.appendChild(el("p", null, control.question));
    box.appendChild(question);
    if (control.kind === "action") {
      var action = el("button", "sn-field__work", control.label);
      action.type = "button";
      action.disabled = !enabled;
      action.addEventListener("click", function () { applyFieldFromUI(control, "open"); });
      box.appendChild(action);
    } else if (control.kind === "switch") {
      box.appendChild(drawFieldSwitch(control, enabled));
    } else {
      box.appendChild(drawFieldDial(control, enabled));
    }
    if (done) box.appendChild(el("p", "sn-field__finding", control.finding));
    else if (!enabled && !plan.locked) box.appendChild(el("p", "sn-field__wait", "Complete the check above first."));
    return box;
  }

  function fieldDealButton(label) {
    var b = el("button", "sn-field__deal", label || "START A CASE");
    b.type = "button";
    b.addEventListener("click", dealAndRender);
    return b;
  }

  function renderField() {
    var host = $("wsField");
    if (!host) return;
    host.textContent = "";
    host.dataset.tour = "field";
    var m = PATCH.mission;
    /* v38 - THE MORE FOLD (UX audit item 2: sensor list + pads + gauge +
       case tools compete at once, and START A CASE stood in two places).
       Idle, in the sandbox, the bay is one slim toggle; the case is started
       from the glass (SHIFT · START A MYSTERY CASE). Once a case is live, or
       the visitor asks, the full clue kit and the gauge stand up. */
    var folded = !PATCH.sideMore && !m.active && PATCH.gameMode !== "play";
    host.classList.toggle("is-folded", folded);
    if (folded) {
      host.dataset.state = "folded";
      var more = el("button", "sn-field__more", "MORE · GAUGE & CASE TOOLS ▸");
      more.type = "button";
      more.setAttribute("aria-expanded", "false");
      more.title = "the recorded window's gauge and the case clue kit";
      more.addEventListener("click", function () { PATCH.sideMore = true; render(); });
      host.appendChild(more);
      return;
    }
    var r = currentRecord();
    var t = currentType();
    var head = el("div", "sn-field__head");
    head.appendChild(el("b", null, "CASE TOOLS"));
    head.appendChild(el("span", null, fieldProgress() + "/3 CLUES"));
    if (!m.active && PATCH.gameMode !== "play") {
      var less = el("button", "sn-field__less", "LESS ▴");
      less.type = "button";
      less.setAttribute("aria-expanded", "true");
      less.addEventListener("click", function () { PATCH.sideMore = false; render(); });
      head.appendChild(less);
    }
    host.appendChild(head);
    host.appendChild(el("p", "sn-field__safe", "PRACTICE RIG · SAFE TO TRY"));

    var identity = el("p", "sn-field__identity");
    identity.appendChild(el("b", null, t ? t.label : "NO SENSOR"));
    identity.appendChild(el("span", null, " · " + (CONDW[PATCH.cond] || String(PATCH.cond).toUpperCase()) +
      (r && unitWordOf(r) ? " · " + unitWordOf(r) : "")));
    host.appendChild(identity);

    if (!m.active) {
      host.dataset.state = "idle";
      host.appendChild(el("p", "sn-field__empty",
        "Start a mystery case. When a fault appears, this panel becomes your clue kit."));
      host.appendChild(fieldDealButton());
      return;
    }
    if (PATCH.cond === "none") {
      host.dataset.state = m.phase === "verified" ? "verified" : "clear";
      host.appendChild(el("strong", "sn-field__clear", m.phase === "verified"
        ? "CASE CLOSED · WORKFLOW VERIFIED" : "CARD CLEAR · NO INTERVENTION"));
      host.appendChild(el("p", "sn-field__empty", m.note));
      if (m.phase === "verified") {
        host.appendChild(el("p", "sn-field__handoff",
          m.incidentNode + " → " + (m.verifiedNode || "recorded OK window")));
      }
      host.appendChild(fieldDealButton("START NEXT CASE"));
      return;
    }

    var plan = missionPlan();
    var rig = plan.rig;
    if (!rig) return;
    host.dataset.state = plan.locked ? "locked" : missionReady() ? "ready" : "playing";
    host.appendChild(el("strong", "sn-field__title", rig.title));
    var clue = el("div", "sn-field__clue");
    clue.appendChild(el("b", null, "CLUE FROM THE CASE FILE"));
    clue.appendChild(el("p", null, rig.authored + " Test the clue with the controls below."));
    host.appendChild(clue);
    if (plan.locked) {
      var pendingMove = incidentMove();
      var lock = el("div", "sn-field__lock");
      var needsMicro = pendingMove && pendingMove.correct === "micro";
      lock.appendChild(el("b", null, needsMicro ? "UNLOCK THE CLUE KIT" : "FIRST: SOLVE THE MODEL MOVE"));
      lock.appendChild(el("span", null, needsMicro
        ? "Bring in Micro to widen this one signal into a site-level investigation."
        : "Open the case on the TV and choose who should hear this signal next."));
      var add = el("button", "sn-field__upgrade", needsMicro ? "ADD MICRO · OPEN CLUES" : "OPEN THE CASE");
      add.type = "button";
      add.addEventListener("click", function () {
        if (needsMicro) { addMissionMicro(); return; }
        if (!detailOpen()) setDetail(true);
        var moveCard = document.querySelector(".sn-mission__move");
        glassScrollTo(moveCard, true);
        var first = moveCard && moveCard.querySelector("button");
        if (first && first.focus) first.focus({ preventScroll: true });
      });
      lock.appendChild(add);
      host.appendChild(lock);
    }
    var controls = el("div", "sn-field__controls");
    rig.controls.forEach(function (control, index) {
      controls.appendChild(drawFieldControl(control, index, plan));
    });
    host.appendChild(controls);
    var feedback = m.field && m.field.feedback;
    var result = el("p", "sn-field__feedback" + (feedback ? " is-" + feedback.kind : ""),
      feedback ? feedback.text : "Try the first lit control. A wrong answer costs nothing; use the feedback and try again.");
    result.setAttribute("aria-live", "polite");
    host.appendChild(result);
    if (missionReady()) {
      var verify = el("button", "sn-field__verify", "CLOSE CASE WITH A HEALTHY READ →");
      verify.type = "button";
      verify.setAttribute("aria-label", "VERIFY WITH RECORDED OK. Opens a separate recorded replay window.");
      verify.addEventListener("click", function () {
        if (!verifyMission()) return;
        react("Workflow verified against a separate recorded OK window; this is not proof that the training actions repaired the prior machine.");
        focusFactoryRelease();
      });
      host.appendChild(verify);
    }
  }

  /* ---- THE LINE: bands across the top, a read climbing them --------------
     Every band is always drawn, filled or not. An occupied band shows the
     model card; an empty one shows a slim ghost that IS the [+] - so the
     plant hierarchy is legible before a single model is chained, and adding
     one is a click on the zone where it would live.

     Honesty shapes this: only Pico and Nano have recorded runs, so the LINE
     may show the whole plant while the DATA speaks for two tiers. Every
     unrecorded band says so on its face, and its stage stays silent. */
  function renderChain() {
    var host = $("wsChain");
    if (!host) return;
    host.textContent = "";
    host.setAttribute("data-tour", "line");

    var t = currentType();
    var info = chainInfo();

    // the sensor end: what is feeding the line
    var sens = el("div", "sn-slot sn-slot--sensor");
    var art = el("span", "sn-type__art sn-type__art--" + (t ? t.icon : "gauge"));
    art.setAttribute("aria-hidden", "true");
    art.appendChild(el("span", "wb-plate__ink"));
    sens.appendChild(art);
    var stxt = el("span", "sn-slot__txt");
    stxt.appendChild(el("b", null, t ? t.label : ""));
    var live = PATCH.types.filter(function (x) { return laneRead(x); }).length;
    /* v38: the sub-line moved into the tooltip - the selected sensor card is
       20px to the left, so the rail head is a slim tag, not a second card */
    sens.appendChild(stxt);
    sens.title = (live > 1
      ? "emitting · " + live + " sensors on the line - every sensor on the left feeds the same chain; one model reads many channels"
      : "emitting - the selected sensor feeds the line");
    host.appendChild(sens);
    host.title = "In a real mesh one Pico reads many channels; this bench shows one for clarity - " +
      "the monitor's FLEET tab replays all 120 recorded channels under your settings.";

    BANDS.forEach(function (b, bi) {
      var seated = PATCH.chain.indexOf(b.tier) >= 0;
      var zone = el("div", "sn-band" + (seated ? " is-seated" : ""));
      tierStyle(zone, b.tier);
      zone.setAttribute("data-band", b.key);
      if (b.tier === "pico") zone.setAttribute("data-tour", "machine");
      if (b.tier === "nano") zone.setAttribute("data-tour", "gateway");

      var cap = el("div", "sn-band__cap");
      cap.appendChild(el("b", null, b.label));
      cap.appendChild(el("span", null, b.where));
      zone.appendChild(cap);

      if (seated) {
        zone.appendChild(drawChainCard(b.tier, PATCH.chain.indexOf(b.tier)));
      } else {
        zone.appendChild(emptyBand(b, bi));
      }
      host.appendChild(zone);
      if (bi < BANDS.length - 1) host.appendChild(bandLink(b, BANDS[bi + 1], info));
    });

    var badgeHost = $("wsChainBadge");
    if (badgeHost) {
      badgeHost.textContent = "";
      if (PATCH.chain.length === 3 && PATCH.chain[0] === "pico" && PATCH.chain[1] === "nano" && PATCH.chain[2] === "micro") {
        var badge = el("span", "sn-reco", "STARTER CHAIN · PICO + NANO + MICRO");
        badge.title = "Pico reads the channel, Nano adjudicates doubtful reads, and Micro shows the site scope by default";
        badgeHost.appendChild(badge);
      }
    }

    if (PATCH.menuFor != null) {
      var wrap = $("wsChainMenu");
      if (wrap) { wrap.textContent = ""; wrap.appendChild(drawMenu(PATCH.menuFor)); }
    } else {
      var wrap2 = $("wsChainMenu");
      if (wrap2) wrap2.textContent = "";
    }

    // seven bands rarely fit a column; the edge fades while there is more line
    var fade = el("span", "sn-linefade");
    fade.setAttribute("aria-hidden", "true");
    host.appendChild(fade);
    var markMore = function () {
      host.classList.toggle("is-more",
        host.scrollWidth - host.clientWidth - host.scrollLeft > 4);
    };
    host.addEventListener("scroll", markMore);
    requestAnimationFrame(markMore);
  }

  // an empty zone: the [+] IS the band, so adding a model is a click on the
  // place it would physically live
  function emptyBand(b, bi) {
    var fam = familyById(b.tier);
    var slot = el("button", "sn-band__empty");
    slot.type = "button";
    slot.setAttribute("aria-expanded", PATCH.menuFor === bi ? "true" : "false");
    slot.setAttribute("aria-label", "Put a model " + b.label.toLowerCase() + " (" + fam.label + ")");
    // the tier's own engraving, ghosted: an empty zone should show you WHAT
    // would stand there, not just that something could. The scale ladder does
    // the explaining - a chip and a datacenter hall are not the same offer.
    var ghostArt = el("span", "sn-band__art");
    ghostArt.appendChild(modelIcon(fam, Math.round(fam.span * 0.62)));
    slot.appendChild(ghostArt);
    slot.appendChild(el("span", "sn-band__ghost", fam.label));
    slot.appendChild(el("span", "sn-band__plus", "+"));
    slot.title = fam.label + " lives here - " + fam.runs +
      (fam.status === "recorded" ? "" : " · no recorded run on this bench, so it would chain in silent");
    slot.addEventListener("click", function (e) {
      e.stopPropagation();
      // clicking a zone offers that zone's tier first, but the whole family
      // stays available - the menu is the ladder
      PATCH.menuFor = PATCH.menuFor === bi ? null : bi;
      render();
      var m = document.querySelector(".ws-menu button");
      if (m) m.focus();
    });
    return slot;
  }

  // the cable between two zones. It carries only where a read actually
  // travelled: both ends seated, and the upstream model handed something on.
  function bandLink(from, to, info) {
    var fromSeated = PATCH.chain.indexOf(from.tier) >= 0;
    var toSeated = PATCH.chain.indexOf(to.tier) >= 0;
    var st = PATCH.verdict && PATCH.verdict.stages;
    var carrying;
    if (!st || !fromSeated || !toSeated) carrying = undefined;
    else if (from.tier === "pico" && to.tier === "nano") {
      // the one link the data can speak for: did this read escalate?
      var picoSt = null;
      st.forEach(function (x) { if (x.kind === "pico") picoSt = x; });
      carrying = picoSt ? !!picoSt.esc : undefined;
    } else carrying = false;
    return railArrow(carrying);
  }

  // carrying === false draws the cable stood down: the read never travelled
  // it. Only ever called with a state derived from the printed stages.
  function railArrow(carrying) {
    // a short patch cable with a little sag - geometry as texture
    var a = el("span", "sn-rail" + (carrying === false ? " is-idle" : ""));
    a.setAttribute("aria-hidden", "true");
    var s = svg("svg", { viewBox: "0 0 22 16", width: 22, height: 16, class: "sn-rail__svg" });
    s.appendChild(svg("path", { class: "sn-rail__cable", d: "M2 5 C 8 13, 14 13, 20 5" }));
    s.appendChild(svg("circle", { class: "sn-rail__plug", cx: 2, cy: 5, r: 2.2 }));
    s.appendChild(svg("circle", { class: "sn-rail__plug", cx: 20, cy: 5, r: 2.2 }));
    a.appendChild(s);
    return a;
  }

  // The bounds meter as a VU instrument: the recorded window's range is the
  // scale, the mean is the needle. Recorded numbers only - an instrument
  // face around them, never a number they did not bring.
  function drawVU(w, unitWord) {
    if (!w || w.lo == null || w.hi == null) return null;
    var W = 150, H = 84, cx = W / 2, cy = H - 10, R = 58;
    var host = svg("svg", { class: "sn-vu", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "recorded window: " + w.lo + " to " + w.hi + (unitWord ? " " + unitWord : "") +
        ", mean " + w.mean });
    function pt(frac, r) {
      var a = (-140 + frac * 100) * Math.PI / 180; // -140deg .. -40deg
      return [cx + Math.cos(a) * r, cy + Math.sin(a) * r];
    }
    var arc0 = pt(0, R), arc1 = pt(1, R);
    host.appendChild(svg("path", { class: "sn-vu__arc",
      d: "M" + arc0[0].toFixed(1) + " " + arc0[1].toFixed(1) +
         " A" + R + " " + R + " 0 0 1 " + arc1[0].toFixed(1) + " " + arc1[1].toFixed(1) }));
    for (var i = 0; i <= 10; i++) {
      var frac = i / 10, len = (i % 5 === 0) ? 8 : 4;
      var o = pt(frac, R), ii = pt(frac, R - len);
      host.appendChild(svg("line", { class: "sn-vu__tick" + (i % 5 === 0 ? " is-major" : ""),
        x1: o[0].toFixed(1), y1: o[1].toFixed(1), x2: ii[0].toFixed(1), y2: ii[1].toFixed(1) }));
    }
    var span = (w.hi - w.lo) || 1;
    var nf = Math.max(0, Math.min(1, (w.mean - w.lo) / span));
    var np = pt(nf, R - 6);
    host.appendChild(svg("line", { class: "sn-vu__needle",
      x1: cx, y1: cy, x2: np[0].toFixed(1), y2: np[1].toFixed(1) }));
    host.appendChild(svg("circle", { class: "sn-vu__hub", cx: cx, cy: cy, r: 3.4 }));
    var t1 = svg("text", { class: "sn-vu__t", x: 6, y: H - 2, "text-anchor": "start" });
    t1.textContent = String(w.lo);
    var t2 = svg("text", { class: "sn-vu__t", x: W - 6, y: H - 2, "text-anchor": "end" });
    t2.textContent = String(w.hi);
    var t3 = svg("text", { class: "sn-vu__t sn-vu__t--mean", x: cx, y: 12, "text-anchor": "middle" });
    t3.textContent = "mean " + w.mean + (unitWord ? " " + unitWord : "");
    host.appendChild(t1); host.appendChild(t2); host.appendChild(t3);
    host.setAttribute("title", "recorded window bounds, with the mean as the needle");
    return host;
  }

  /* THE SITE ROLLUP - what a Wave Micro is FOR, done on the data we hold.
     Every number here comes from deriveFleet(), the same recount the FLEET
     tab prints, so the two can never disagree. The attribution is the point:
     this is the BENCH's arithmetic over recorded records, and it is labelled
     that way in the markup, in the copy and in the aria-label. Wave Micro has
     no recorded run on this bench and emits no prediction, margin or lamp. */
  /* A tier doing its real job at its real scope. The numbers are a recount of
     recorded records inside that scope, scored by the same engine the FLEET
     tab uses - so a site rollup, a plant rollup and the fleet tab can never
     disagree about the same records. The attribution is the point: the JOB is
     the tier's, the ARITHMETIC is the bench's, and no model ran. */
  function drawScopeRun(st) {
    var box = el("div", "sn-scope");
    var sc = st.scope, t = st.tally, lens = st.caseLens;
    box.appendChild(el("p", "sn-scope__job",
      "A " + (sc.unit === "plant" ? "plant model" : "site brain") + " takes in " +
      st.takes + " at once and produces " + st.job + "."));

    var head = el("p", "sn-scope__scopeline");
    head.appendChild(el("b", null, sc.name));
    head.appendChild(el("span", null, " · " + sc.machines + " machine" +
      (sc.machines === 1 ? "" : "s") + " · " + sc.chans + " recorded channels" +
      (sc.sites ? " · " + sc.sites + " sites" : "")));
    box.appendChild(head);

    if (lens) {
      var caseHead = el("div", "sn-scope__casehead");
      var caseWords = el("span", null);
      caseWords.appendChild(el("i", null, "CURRENT CASE FIRST"));
      caseWords.appendChild(el("b", null, lens.sensor + " · " + lens.condition));
      caseHead.appendChild(caseWords);
      caseHead.appendChild(el("span", null, lens.tally.n + " comparable card" +
        (lens.tally.n === 1 ? "" : "s") + " · " + lens.matching + " same-condition"));
      box.appendChild(caseHead);

      var caseGrid = el("dl", "sn-scope__grid is-case");
      [["sensor cards", lens.tally.n], ["faults", lens.tally.faults],
       ["caught", lens.tally.caught], ["missed", lens.tally.missed]
      ].forEach(function (row) {
        var cell = el("div", "sn-scope__stat");
        cell.appendChild(el("dt", null, row[0]));
        cell.appendChild(el("dd", null, String(row[1])));
        caseGrid.appendChild(cell);
      });
      box.appendChild(caseGrid);

      var rows = el("ol", "sn-scope__cases");
      lens.rows.forEach(function (row) {
        var li = el("li", row.current ? "is-current" : null);
        li.appendChild(el("span", "sn-scope__casemark", row.current ? "NOW" : "PEER"));
        var identity = el("span", "sn-scope__caseid");
        identity.appendChild(el("b", null, row.machine));
        identity.appendChild(el("code", null, row.record));
        li.appendChild(identity);
        li.appendChild(el("span", "sn-scope__casecondition", row.condition));
        li.appendChild(el("strong", "sn-scope__caseout", row.outcome));
        rows.appendChild(li);
      });
      box.appendChild(rows);
    }

    box.appendChild(el("p", "sn-scope__policy",
      "SCOPE BACKDROP · " + t.n + " channels · " + t.faults + " faults · " +
      t.caught + " caught · " + t.missed + " missed · " + sc.how + " · " + st.policy));

    var att = el("p", "sn-scope__att");
    att.appendChild(el("i", "sn-scope__tag", "BENCH ARITHMETIC"));
    att.appendChild(el("span", null,
      " These are the bench's own numbers - a recount of the recorded records in this " +
      "scope under your current settings. " + st.fam.label + " has no recorded run here, " +
      "so this is the JOB it does, done on the data we have - not something " +
      st.fam.label + " said."));
    box.appendChild(att);
    box.setAttribute("aria-label",
      "A " + sc.unit + " rollup computed by the bench from recorded records, not output from " +
      st.fam.label);
    return box;
  }

  /* ABOVE THE PLANT the recording genuinely runs out. "Nothing this big here"
     was true and useless; what a reader needs is what the tier WOULD add, what
     the tier below cannot do without it, and exactly what this bench would
     need in order to show it. The difference between "cannot show" and "here
     is what it would take" is the whole argument for the upper ladder. */
  function drawScopeCard(st) {
    var box = el("div", "sn-scope");
    var fam = st.fam;
    var dl = el("dl", "sn-scope__pair");
    dl.appendChild(el("dt", null, "takes in"));
    dl.appendChild(el("dd", null, st.takes));
    dl.appendChild(el("dt", null, "produces"));
    dl.appendChild(el("dd", null, st.job));
    if (fam.only) {
      dl.appendChild(el("dt", null, "only it can"));
      dl.appendChild(el("dd", null, fam.only));
    }
    if (fam.belowCant) {
      dl.appendChild(el("dt", null, "the one below"));
      dl.appendChild(el("dd", null, fam.belowCant));
    }
    box.appendChild(dl);

    var att = el("p", "sn-scope__att");
    att.appendChild(el("i", "sn-scope__tag", "WHAT THIS BENCH WOULD NEED"));
    att.appendChild(el("span", null, " " + (fam.needs ||
      "a recording at that scope - this bench holds one plant") +
      ". No recorded " + fam.label + " run exists here either, so it asserts nothing."));
    box.appendChild(att);
    return box;
  }

  function drawTierCase(st) {
    var tierId = st.kind === "pico" ? "pico"
      : (st.kind === "nano" || st.kind === "quietSenior") ? "nano"
      : st.fam ? st.fam.id : null;
    var brief = tierId ? tierCaseBrief(tierId, st.r || currentRecord()) : null;
    if (!brief) return null;
    var box = el("section", "sn-tiercase");
    tierStyle(box, tierId);
    var head = el("div", "sn-tiercase__head");
    head.appendChild(el("b", null, "CURRENT CASE"));
    head.appendChild(el("i", null, brief.provenance));
    box.appendChild(head);
    var identity = el("p", "sn-tiercase__identity");
    identity.appendChild(el("strong", null, brief.condition + " · " + brief.sensor));
    identity.appendChild(el("code", null, brief.record));
    box.appendChild(identity);
    box.appendChild(el("p", "sn-tiercase__signal", brief.signal));

    var cards = el("div", "sn-tiercase__cards");
    var handoff = el("div", "sn-tiercase__card");
    handoff.appendChild(el("b", null, "READER HANDOFF"));
    handoff.appendChild(el("p", null, brief.handoff));
    cards.appendChild(handoff);
    var adds = el("div", "sn-tiercase__card is-tier");
    adds.appendChild(el("b", null, "THIS TIER ADDS"));
    adds.appendChild(el("p", null, brief.adds));
    cards.appendChild(adds);
    box.appendChild(cards);

    var mechanic = el("p", "sn-tiercase__mechanic");
    mechanic.appendChild(el("b", null, "FIELD MECHANIC"));
    mechanic.appendChild(el("span", null, brief.mechanic));
    box.appendChild(mechanic);
    var family = el("p", "sn-tiercase__family");
    family.appendChild(el("b", null, "FAMILY CONTRACT"));
    family.appendChild(el("span", null, brief.family));
    box.appendChild(family);
    return box;
  }

  // the card's live state, in three words, from the stage this model produced
  function slotState(id) {
    var v = PATCH.verdict;
    if (!v || !v.stages) return null;
    for (var k = 0; k < v.stages.length; k++) {
      var st = v.stages[k];
      if (id === "pico" && st.kind === "pico") {
        return st.esc ? { word: "ASKED FOR HELP", cls: "is-esc" }
                      : { word: "ANSWERED", cls: "is-ok" };
      }
      if (id === "nano" && st.kind === "nano") return { word: "ANSWERED", cls: "is-ok" };
      if (id === "nano" && st.kind === "quietSenior") return { word: "WAITING", cls: "is-idle" };
      if (st.kind === "silent" && st.fam && st.fam.id === id) {
        return { word: "QUIET", cls: "is-idle" };
      }
    }
    return null;
  }

  function drawChainCard(id, i) {
    var fam = familyById(id);
    var stNow = slotState(id);
    // A card that WORKED and a card that stood down must not look alike -
    // "nothing reached Wave Nano on this read" is the mesh's whole point, so
    // the rail shows it. The state is read off the same stages the monitor
    // prints; nothing new is claimed here.
    var acted = stNow && (stNow.cls === "is-ok" || stNow.cls === "is-esc");
    var card = el("div", "sn-slot sn-slot--model" +
      (fam.status === "recorded" ? "" : " sn-slot--quiet") +
      (stNow ? (acted ? " is-acted" : " is-standby") : ""));
    // tier identity rides the card's edge and its name only - the state badge
    // owns the semantic colour, so a red-hued Pico never reads as an alarm
    tierStyle(card, id);
    card.title = fam.blurb + (fam.status === "recorded"
      ? " - its stage replays recorded fields only"
      : " - it chains in honestly silent: no recorded transcript");
    var artWrap = el("span", "sn-slot__art");
    artWrap.appendChild(modelIcon(fam, Math.round(fam.span * CARD_SCALE)));
    card.appendChild(artWrap);

    var txt = el("span", "sn-slot__txt");
    txt.appendChild(el("b", "sn-slot__name sn-tiername", fam.label));
    txt.appendChild(el("span", "sn-slot__role", fam.does));
    // fan-in, said on the card: what this tier takes IN. A Pico reads one
    // machine, a Nano rolls up many Picos, a Micro reasons over many fleets -
    // the shape of the mesh, stated where the model is rather than in a footnote.
    var takes = el("span", "sn-slot__takes");
    takes.appendChild(el("i", "sn-slot__takes-k", "takes "));
    takes.appendChild(el("span", null, fam.takes));
    takes.title = "one " + fam.label + " takes in " + fam.takes +
      " - in a real mesh, many at once. This bench shows one for clarity.";
    txt.appendChild(takes);
    if (stNow) {
      var badge = el("span", "sn-slot__state " + stNow.cls, stNow.word);
      badge.title = "what this model did with the read now on the monitor";
      txt.appendChild(badge);
      // the card that just changed its mind is where attention goes
      flashIfChanged(card, "chain:" + id, stNow.word);
    }
    // params, recipe and the run name are for the curious, not the first glance
    var meta = fam.size + " · " + fam.status + (runOf(id) ? " · run " + runOf(id) : "");
    var metaLine = el("span", "sn-slot__meta", fam.size);
    metaLine.title = meta + " - the run name is ground truth; the tier label is a deck name";
    txt.appendChild(metaLine);
    card.appendChild(txt);
    var x = el("button", "ws-resp__x");
    x.type = "button";
    x.setAttribute("aria-label", "Remove " + fam.label + " from the chain");
    x.textContent = "×";
    x.addEventListener("click", function () {
      chainRemove(id);
      render();
      react(fam.label + " removed from the chain.");
    });
    card.appendChild(x);

    if (id === "pico") {
      card.appendChild(drawDial({
        values: DETENTS.slice(),
        // "SURE ENOUGH" is what the knob DOES; the margin floor is what it is
        // called. Plain word on the faceplate, the term in the tooltip.
        labels: DETENTS.map(function (d) { return "SURE ENOUGH " + d.toFixed(1); }),
        index: Math.max(0, DETENTS.indexOf(PATCH.floor)),
        name: "how sure Wave Pico must be to answer alone (margin floor)",
        size: 38,
        tip: function (k) { return knobTip(DETENTS[k]); },
        onset: function (k) {
          PATCH.floor = DETENTS[k];
          derive(); render();
          react("Wave Pico now needs to be " + PATCH.floor.toFixed(1) +
            " sure to answer alone - anything less it hands to Wave Nano.");
          var again = document.querySelector(".sn-slot--model .wp-knob");
          if (again) again.focus({ preventScroll: true });
        },
      }));
    }
    // the model whys moved to the standing row below the rail: an open pop
    // inside the rail's scroll container was CLIPPED (founder v12: "when the
    // tooltip is open i have to scroll and it hides parts of the ui") - all
    // whys now expand in place, full width, one at a time.
    return card;
  }

  // The attach menu: the whole family, sized so size is VISIBLE.
  /* The menu opens FROM a band, and that band already knows which tier belongs
     in it - so it says so. The founder: "its not easy to understand which one
     i should select based on what i clicked." The zone's own tier leads, named
     and marked; the rest of the ladder follows under a divider, still fully
     choosable, because putting a Micro where a Nano would go is a legitimate
     thing to try and the deck should not forbid it - only stop pretending the
     seven are interchangeable. */
  function drawMenu(slotIdx) {
    var menu = el("div", "ws-menu");
    menu.setAttribute("role", "menu");
    var band = (typeof slotIdx === "number" && BANDS[slotIdx]) ? BANDS[slotIdx] : null;
    var pick = band ? familyById(band.tier) : null;
    menu.setAttribute("aria-label", band
      ? "Put a model " + band.label.toLowerCase() + ". " + (pick ? pick.label + " lives here." : "")
      : "Add a model to the chain");

    if (band) {
      var head = el("p", "ws-menu__head");
      head.appendChild(el("i", "ws-menu__where", band.label));
      head.appendChild(el("span", null, " · " + band.where));
      menu.appendChild(head);
    }

    // the zone's own tier first, then everything else
    var ordered = FAMILY.slice();
    if (pick) {
      ordered = [pick].concat(FAMILY.filter(function (f) { return f.id !== pick.id; }));
    }
    var dividerAt = pick ? 1 : -1;

    ordered.forEach(function (fam, oi) {
      if (oi === dividerAt) {
        menu.appendChild(el("p", "ws-menu__div",
          "or put another tier here - the ladder stays open"));
      }
      // ALREADY SEATED reads as seated (founder v19: "highlight the ones or
      // dim the ones we are already added"). Offering a tier that is already
      // in the chain as if it were available is a small lie the menu was
      // telling seven times over. A seated row stays CLICKABLE because the
      // useful thing to do with it is find it - it takes you to its band -
      // and it says so rather than looking mysteriously disabled.
      var seated = PATCH.chain.indexOf(fam.id) >= 0;
      var lives = !!(pick && fam.id === pick.id);
      var b = el("button", "ws-menu__item" +
        (fam.status === "recorded" ? "" : " ws-menu__item--quiet") +
        (seated ? " is-seated" : "") + (lives ? " is-lives" : ""));
      b.type = "button";
      b.setAttribute("role", "menuitem");
      tierStyle(b, fam.id);
      // the icon sits in a fixed cell on a common baseline, so scanning the
      // menu top to bottom IS the ladder: a chip, then a box, then a server,
      // then cabinets, then a hall. Choosing a model is choosing a size you
      // can see before you read a single number.
      var cell = el("span", "ws-menu__art");
      cell.appendChild(modelIcon(fam));
      b.appendChild(cell);
      var txt = el("span", "ws-menu__txt");
      txt.appendChild(el("b", "sn-tiername", fam.label));
      var band = bandOf(fam.id);
      txt.appendChild(el("span", null, fam.size + " · " +
        (band ? band.label.toLowerCase() + " · " + band.where : fam.reach)));
      txt.appendChild(el("span", "ws-menu__runs", "runs on " + fam.runs));
      txt.appendChild(el("span", "ws-menu__status",
        fam.status === "recorded"
          ? "recorded on this bench" + (runOf(fam.id) ? " · run " + runOf(fam.id) : "")
          : fam.status + " · will attach silent"));
      if (lives && !seated) {
        txt.appendChild(el("span", "ws-menu__lives",
          "◂ lives here — this is the tier for " + band.label.toLowerCase()));
      }
      if (seated) {
        var sband = bandOf(fam.id);
        txt.appendChild(el("span", "ws-menu__seated",
          "✓ already in the chain" + (sband ? " · " + sband.label.toLowerCase() : "") +
          " — click to find it"));
      }
      b.appendChild(txt);
      b.title = seated
        ? fam.label + " is already in this chain; clicking takes you to its band"
        : (lives ? "the tier that belongs " + band.label.toLowerCase() + " · " : "") +
          fam.blurb + " · runs on " + fam.runs + " · " + fam.recipe;
      b.addEventListener("click", function (e) {
        e.stopPropagation();
        if (seated) { revealBand(fam.id); return; }
        chainAdd(fam.id, slotIdx);
      });
      menu.appendChild(b);
    });
    // the Spectrum's own footer line, verbatim from the tier strategy doc
    menu.appendChild(el("p", "ws-menu__foot",
      "The Wave Spectrum, pico → exa: every tier ships with Wave Mesh baked in and " +
      "understands the output of every tier beneath it. scratch = trained from random " +
      "init on our data · base+specialize = strong open base, industrial " +
      "continued-pretrain + mesh · expert-pruned / frontier = carved from the flagship."));
    var close = el("button", "wp-remove");
    close.type = "button";
    close.textContent = "close";
    close.addEventListener("click", function (e) {
      e.stopPropagation();
      PATCH.menuFor = null;
      render();
    });
    menu.appendChild(close);
    return menu;
  }

  // Take the visitor to a tier that is already seated. The menu closes, the
  // band scrolls into view (the line scrolls sideways - seven bands do not
  // fit a column) and flashes once so the eye lands on it. Chrome only.
  function revealBand(tierId) {
    var band = bandOf(tierId);
    PATCH.menuFor = null;
    render();
    if (!band) return;
    var zone = document.querySelector('.sn-band[data-band="' + band.key + '"]');
    if (!zone) return;
    if (zone.scrollIntoView) {
      zone.scrollIntoView({ block: "nearest", inline: "center",
                            behavior: REDUCED ? "auto" : "smooth" });
    }
    if (!REDUCED) {
      zone.classList.add("is-found");
      setTimeout(function () { zone.classList.remove("is-found"); }, 1200);
    }
    var card = zone.querySelector(".sn-slot, button, [tabindex]");
    if (card && card.focus) card.focus({ preventScroll: true });
  }

  // THE LADDER RUNS UP. A senior does not hand work down: the reader (Pico,
  // or Nano reading direct) comes first, the senior after it, observers
  // behind. Attach order is the visitor's gesture; CHAIN order is physics -
  // normalize makes a backwards chain unconstructible rather than merely
  // discouraged (founder v12: a Pico was reading the Nano's output).
  function normalizeChain() {
    var rest = PATCH.chain.filter(function (x) { return x !== "pico" && x !== "nano"; });
    var out = [];
    if (PATCH.chain.indexOf("pico") >= 0) out.push("pico");
    if (PATCH.chain.indexOf("nano") >= 0) out.push("nano");
    out = out.concat(rest);
    var moved = out.join(",") !== PATCH.chain.join(",");
    PATCH.chain = out;
    return moved;
  }

  function chainAdd(id, at) {
    var fam = familyById(id);
    if (PATCH.chain.indexOf(id) >= 0) {
      // one of EACH - a duplicate silent model adds rail, not meaning
      // (v11: a fidgety tap chained four Micros)
      react("One " + fam.label + " is the chain's shape here.");
      PATCH.menuFor = null;
      render();
      return;
    }
    contextMoved();
    if (at == null || at > PATCH.chain.length) at = PATCH.chain.length;
    PATCH.chain.splice(at, 0, id);
    var rearranged = normalizeChain();
    PATCH.menuFor = null;
    derive(); render();
    if (rearranged && id === "pico") {
      react("The ladder runs up: Wave Pico took the wire and Wave Nano moved to senior - " +
        "a senior does not hand work down to a reader.");
    } else if (rearranged) {
      react(fam.label + " in the chain - slotted where the ladder runs up: reader first, senior after.");
    } else if (fam.status === "recorded") {
      var info = chainInfo();
      react(fam.label + " in the chain - " + (id === "pico"
        ? "it classifies each read: assert, or escalate at its floor."
        : (info.senior ? "the senior now adjudicates every doubtful read, and the monitor gains a human-readable stage."
                       : "it reads the wire directly, and the monitor gains a human-readable stage.")));
    } else {
      react(fam.label + " (" + fam.size + ", " + fam.recipe + ") in the chain - " +
        "no recorded run on this bench, so its stage stays honestly silent.");
    }
  }

  function chainRemove(id) {
    var at = PATCH.chain.indexOf(id);
    if (at < 0) return false;
    contextMoved();
    PATCH.chain.splice(at, 1);
    PATCH.menuFor = null;
    if (id === "micro" && PATCH.mission.active && PATCH.cond !== "none" &&
        PATCH.mission.phase !== "verified") {
      PATCH.mission.actions = {};
      PATCH.mission.phase = "incident";
      PATCH.mission.moveStage = 0;
      PATCH.mission.moveFeedback = null;
      resetField();
    }
    derive();
    return true;
  }

  /* =====================================================================
     THE WHY LAYER - the product story, one hover/tap away.
     Surface stays minimal; every NUMBER in a pop is rendered from the
     measured bundle with its citation. Qualitative lines are deployment
     facts (runs-on) or the shim's own doctrine - never invented figures.
     ===================================================================== */
  function whyPop(label, cls) {
    var det = el("details", "sn-why" + (cls ? " " + cls : ""));
    var sum = el("summary", null, null);
    sum.appendChild(el("span", "sn-why__i", "ⓘ"));
    sum.appendChild(el("span", "sn-why__k", label));
    det.appendChild(sum);
    return det;
  }

  // A standing why: expands IN PLACE below the rail (full-width accordion
  // row - never a floating pop inside a scroll container), one open at a
  // time, and the open one survives re-renders via PATCH.whyOpen.
  function whyChip(key, label) {
    var det = whyPop(label);
    det.dataset.why = key;
    if (PATCH.whyOpen === key) det.open = true;
    det.addEventListener("toggle", function () {
      if (det.open) {
        PATCH.whyOpen = key;
        var others = document.querySelectorAll('.sn-whys .sn-why[open]');
        Array.prototype.forEach.call(others, function (o) {
          if (o !== det) o.open = false;
        });
      } else if (PATCH.whyOpen === key) {
        PATCH.whyOpen = null;
      }
    });
    return det;
  }

  // WHY NOT ONE BIG MODEL? The measured sweep drawn as a picture: macro
  // recall vs % of parent-everywhere compute, every point a real config.
  function econChart() {
    var m = PATCH.measured;
    if (!m) return null;
    var cfgs = m.escalation.configs.filter(function (c) {
      return c.pct_of_parent_everywhere != null && c.macro_recall != null;
    });
    var W = 300, H = 150, padL = 34, padB = 26, padT = 12, padR = 12;
    var host = svg("svg", { class: "sn-econ", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "measured sweep: macro recall against percent of parent-everywhere compute, one point per config" });
    var x = function (p) { return padL + p * (W - padL - padR); };
    var y = function (rec) { return H - padB - rec * (H - padB - padT); };
    host.appendChild(svg("line", { class: "sn-econ__ax", x1: padL, y1: H - padB, x2: W - padR, y2: H - padB }));
    host.appendChild(svg("line", { class: "sn-econ__ax", x1: padL, y1: padT, x2: padL, y2: H - padB }));
    var tx = svg("text", { class: "sn-econ__t", x: (padL + W - padR) / 2, y: H - 4, "text-anchor": "middle" });
    tx.textContent = "% of parent-everywhere compute";
    var ty = svg("text", { class: "sn-econ__t", x: 8, y: (padT + H - padB) / 2,
      transform: "rotate(-90 8 " + ((padT + H - padB) / 2) + ")", "text-anchor": "middle" });
    ty.textContent = "macro recall";
    host.appendChild(tx); host.appendChild(ty);
    // the mesh points joined, so the trade reads as a curve
    var mesh = cfgs.filter(function (c) { return /child\+parent@/.test(c.config); });
    if (mesh.length > 1) {
      var d = mesh.map(function (c, i) {
        return (i ? "L" : "M") + x(c.pct_of_parent_everywhere).toFixed(1) + " " + y(c.macro_recall).toFixed(1);
      }).join("");
      host.appendChild(svg("path", { class: "sn-econ__curve", d: d }));
    }
    cfgs.forEach(function (c) {
      var isMesh = /child\+parent@/.test(c.config);
      var cx = x(c.pct_of_parent_everywhere), cy = y(c.macro_recall);
      host.appendChild(svg("circle", { class: "sn-econ__pt" + (isMesh ? " is-mesh" : ""),
        cx: cx.toFixed(1), cy: cy.toFixed(1), r: isMesh ? 3.4 : 2.6 }));
      var flip = cx > W - 70; // keep the rightmost labels inside the frame
      var lab = svg("text", { class: "sn-econ__pl", x: (flip ? cx - 4 : cx + 4).toFixed(1),
        y: (cy - 4).toFixed(1), "text-anchor": flip ? "end" : "start" });
      lab.textContent = c.config.replace("child+parent@", "@").replace("parent-adjudicate-all", "adjudicate-all");
      host.appendChild(lab);
    });
    return host;
  }

  function browserTierQuant() {
    var m = PATCH.measured;
    if (!m || !m.quants) return null;
    for (var i = 0; i < m.quants.length; i++) {
      if (/browser/.test(m.quants[i].role || "")) return m.quants[i];
    }
    return null;
  }

  // THE WHY LAYER, consolidated (founder v14: five chips were "too bunched
  // in") - ONE standing entry, "WHY WAVE?", opening a single full-width panel
  // with the five questions as an internal mini-nav. Content unchanged in
  // spirit: every number rendered from the bundle or cited to its source doc.
  function whyTopics() {
    var m = PATCH.measured;
    var nano = familyById("nano");
    return [
      { key: "tasknative", label: "why not a general model?", build: function (box) {
        /* v39 (UX audit item 2): the five questions never carried the one a
           visitor actually arrives with. This tab was labelled "why task-native?"
           - a term you have to know already before you would click it - while
           its content is the answer to "why not just use a general model?". The
           label now asks the arriving question and the content answers it in
           this order: what the job demands of ANY model here (a requirement of
           our own contract, not a claim about anyone else's), then the only
           measurement we hold, with its limits in the same breath. Nothing about
           an unmeasured model is asserted anywhere in this tab - the cite line
           at the bottom is the whole of the evidence and says so. */
        box.appendChild(el("p", "sn-why__p",
          "Because nothing on this bench is a conversation. The job is a window of " +
          "raw telemetry in, and out ONE word from a LOCKED ENUM with a MARGIN " +
          "beside it - how sure the model is. The margin is what the handoff runs " +
          "on: below the floor the read goes up the chain, at or above it the model " +
          "answers alone. An answer in prose carries nothing the escalation " +
          "contract can use. That is a requirement this bench puts on any model, " +
          "ours included; it is not a measurement of anyone else's."));
        /* HONESTY FIX 2026-08-17 (audit): this paragraph used to open "A chat
           model free-sampled on these bytes dreams a Modbus register table."
           That is a real recorded incident - but it happened to OUR OWN Wave
           Pico RC, not to a chat model (UI-HANDOFF-3-TRANSLATION-SHIM,
           2026-08-13: a task-native base model asked "hi" dreamed a register
           table, BY DESIGN). No chat model has ever been observed doing this
           on our data. We do not put our own model's behaviour in a
           competitor's mouth; the true version is the better argument anyway. */
        box.appendChild(el("p", "sn-why__p",
          "Ask a task-native model to chat and it dreams its training set - ours " +
          "answered \u201chi\u201d with an invented Modbus register table. That is " +
          "the point, not a defect: it was never trained to talk. You do not " +
          "free-sample a Wave model, you DECODE it - a LOCKED ENUM with a MARGIN, " +
          "the model saying how sure it is. That calibrated doubt is what the " +
          "escalation contract is built on: one token of meaning, scored."));
        box.appendChild(el("p", "sn-why__p",
          "The split runs up the Spectrum: Wave Pico and Nano are TOTAL specialists - " +
          "at chance on general benchmarks BY DESIGN (measured MMLU 26.9 against a " +
          "25 chance line - Pico v4 audit). Wave Micro and above are dual-capable by requirement: competitive " +
          "on general benchmarks AND on industrial ones, with capability " +
          "retention a gating metric (tier-scaling strategy, 2026-08-14)."));
        /* HONESTY FIX 2026-08-17 (audit): the line claimed "generalist models"
           as a whole read raw industrial telemetry at the chance floor - a
           population we never sampled. The bench has run 30B-class OPEN
           models only; no frontier or
           chat-tuned model has ever been on it. The claim is now narrowed to
           what was actually benched, and says plainly what has not been. It
           stays QUALITATIVE and cited to the plan doc on purpose: the roster
           figures are unpublished until IEB-Signals is released (the v14 lock
           enforces exactly this - see "the unpublished roster figures never
           reach the deck"). */
        box.appendChild(el("p", "sn-why__cite",
          "the 30B-class open models on this bench read raw telemetry near chance" +
          " - no frontier or chat-tuned model has been measured here, so that is" +
          " the whole of the claim (IEB-Signals public-release plan, 2026-08-14)"));
      } },
      { key: "senior", label: "why a senior?", build: function (box) {
        var selected = m.escalation.configs.filter(function (c) {
          return c.config === "child+parent@" + PATCH.floor.toFixed(1);
        })[0];
        box.appendChild(el("p", "sn-why__p",
          "Wave Pico reads every window on the machine. It compares the gap between its " +
          "first and second choices with SURE ENOUGH. Below that floor it asks Wave Nano " +
          "to evaluate the same recorded window; at or above it, Pico answers locally."));
        if (selected) {
          var upward = Math.round(m.escalation.n * selected.escalation_rate);
          box.appendChild(el("p", "sn-why__p",
            "At the selected " + PATCH.floor.toFixed(1) + " floor, our recorded " +
            m.escalation.n + "-record sweep sent " + upward + " reads (" +
            (selected.escalation_rate * 100).toFixed(1) + "%) upward. The other " +
            (m.escalation.n - upward) + " ended at Pico. Open WHY A CHAIN, NOT JUST THE " +
            "BIGGER WAVE? to compare the quality and residency-proxy result."));
        }
        box.appendChild(el("p", "sn-why__p",
          "Wave Nano is placed on " + nano.runs + ". In that layout, the " +
          "machine-to-gateway handoff can stay inside the site network. That is a fact about " +
          "topology, not a privacy guarantee. The operator still has to secure transport, " +
          "storage, access control, and any egress they configure."));
      } },
      /* v39 (UX audit item 1): the label read "why not one big model?" - which
         a visitor reads as "why not a frontier chat model", a comparison this
         deck has never measured and must not imply. The content underneath has
         always been Wave-only: Pico alone vs the Pico+Nano mesh vs Nano direct,
         every point a recorded config from our own sweep. The label now names
         exactly that question. The general-model question is answered, within
         what we can prove, in the first tab. */
      { key: "econ", label: "why a chain, not just the bigger Wave?", build: function (box) {
        var chart = econChart();
        if (chart) box.appendChild(chart);
        var cfg = function (name) {
          return m.escalation.configs.filter(function (c) { return c.config === name; })[0];
        };
        var child = cfg("child-only");
        var best = cfg("child+parent@1.5");
        var direct = cfg("parent-direct");
        if (child && best && direct) {
          box.appendChild(el("p", "sn-why__p",
            "There is no free winner. Nano direct scored " + (direct.macro_recall * 100).toFixed(1) +
            "% macro recall in this sweep; the 1.5 mesh scored " +
            (best.macro_recall * 100).toFixed(1) + "% while sending " +
            (best.escalation_rate * 100).toFixed(1) + "% of reads upward and using " +
            (best.pct_of_parent_everywhere * 100).toFixed(1) +
            "% of the parent-direct residency proxy. The mesh is a tunable quality and " +
            "placement trade, not a claim that the small chain beats Nano on every read."));

          var facts = el("dl", "sn-econfacts");
          [["PICO ONLY", child, "0% sent up"],
           ["FLOOR 1.5 MESH", best, (best.escalation_rate * 100).toFixed(1) + "% sent up"],
           ["NANO DIRECT", direct, "every read starts at Nano"]].forEach(function (item) {
            var card = el("div", "sn-econfacts__card");
            card.appendChild(el("dt", null, item[0]));
            card.appendChild(el("dd", null, (item[1].macro_recall * 100).toFixed(1) + "% macro recall"));
            card.appendChild(el("dd", null, item[2]));
            card.appendChild(el("dd", null,
              (item[1].pct_of_parent_everywhere * 100).toFixed(1) + "% residency proxy"));
            facts.appendChild(card);
          });
          box.appendChild(facts);
        }
        box.appendChild(el("p", "sn-why__p",
          "The x-axis is mean parameters evaluated per item, a residency proxy. " +
          "Parent-everywhere means sending every read to the parent model. The axis is not " +
          "latency, energy, a cloud bill, or a hardware benchmark. Those require a deployment " +
          "measurement on the actual gateway and edge devices."));
        box.appendChild(el("p", "sn-why__cite",
          "measured: " + m.escalation.configs.map(function (c) { return c.config; }).join(" · ") +
          " (" + m._provenance.suite + ") · method note: " + m.escalation.cost_note));
      } },
      { key: "tiny", label: "why so small?", build: function (box) {
        var ladder = el("span", "sn-ladder");
        ladder.setAttribute("aria-hidden", "true");
        ladder.appendChild(el("span", "wb-plate__ink"));
        box.appendChild(ladder);
        box.appendChild(el("p", "sn-why__p",
          "The reading happens where the wire is. The Spectrum climbs one SI step at a " +
          "time - " + FAMILY.map(function (f) { return f.label.replace("Wave ", "") + " on " + f.runs; }).join(", ") +
          " - so each tier can run on hardware its scope already owns. In the shown " +
          "on-site topology, reads need not leave the fence to reach a senior; that " +
          "placement does not itself prove confidentiality or prevent configured egress."));
        box.appendChild(el("p", "sn-why__p",
          "Why not just make them all huge? From-scratch quality is bounded by DIVERSE " +
          "TOKENS, not GPUs - scratch wins through roughly half a billion params, and " +
          "above that the family switches to base+specialize (tier-scaling strategy, " +
          "2026-08-14). Small is not a compromise at the edge; it is the design."));
        var q = browserTierQuant();
        if (q) {
          /* HONESTY FIX 2026-08-18 (numeric audit). Two things were wrong here.
             (1) The fault-ID macro was printed as a measurement, but the bundle's
             own _provenance.retracted.guard says the Q4 and Q8 endpoint outputs
             "return byte-identical aggregates; that is the signature of the
             corrected grammar-spacing harness bug, not a measurement" - and they
             ARE identical in the file (0.732 at n=150, both). The deck brags
             elsewhere that the exporter refuses that signature, so it must not
             publish the number. Dropped.
             (2) The sentence sat under a paragraph about Wave Pico at 270M,
             which reads as "Pico is 65 MB". 65 MB at this site's own 0.6 B per
             parameter is a ~108M model - the 98M waypoint the retraction names,
             not the 250-300M tier. The build is named now.
             The SIZE is not in dispute and still comes from the quants row, so
             the lock that requires q.size_mb and q.source still holds. */
          box.appendChild(el("p", "sn-why__p",
            "Small also survives quantization: the certified waypoint build - a " +
            "100M-class model, not the 250-300M tier - is " + q.size_mb + "MB at " +
            q.quant + ", small enough for a browser tab (" + q.source + ")."));
        }
      } },
      { key: "response", label: "what happens after a finding?", build: function (box) {
        box.appendChild(el("p", "sn-why__p",
          "The models make the same recorded call in every mode. LOG ONLY records the " +
          "finding, HUMAN REVIEW sends it to a person, and POLICY QUEUE sends it to the " +
          "configured response seam. That choice happens after detection, so it never " +
          "recolours or rewrites the model result. This bench demonstrates routing over " +
          "recorded outcomes; it does not claim measured autonomous control."));
      } },
    ];
  }

  function renderWhys() {
    var host = $("wsWhys");
    if (!host || !PATCH.measured) return;
    host.textContent = "";
    var topics = whyTopics();
    var det = whyChip("wave", "WHY WAVE? · the story in five questions");
    var nav = el("div", "sn-why__nav");
    nav.setAttribute("role", "tablist");
    nav.setAttribute("aria-label", "The five why questions");
    var body = el("div", "sn-why__body");
    if (!PATCH.whyTopic) PATCH.whyTopic = topics[0].key;
    function paint() {
      body.textContent = "";
      nav.textContent = "";
      topics.forEach(function (t) {
        var b = el("button", "sn-why__tab" + (PATCH.whyTopic === t.key ? " is-on" : ""));
        b.type = "button";
        b.setAttribute("role", "tab");
        b.setAttribute("aria-selected", PATCH.whyTopic === t.key ? "true" : "false");
        b.textContent = t.label;
        b.addEventListener("click", function (e) {
          e.stopPropagation();
          PATCH.whyTopic = t.key;
          paint();
        });
        nav.appendChild(b);
      });
      var cur = topics.filter(function (t) { return t.key === PATCH.whyTopic; })[0] || topics[0];
      cur.build(body);
    }
    paint();
    det.appendChild(nav);
    det.appendChild(body);
    host.appendChild(det);
    host.appendChild(whereTheyLive());
  }

  /* ---- WHERE THEY LIVE: the value of the upper ladder, honestly ----------
     THE STRUCTURAL PROBLEM, named: this is a ONE-SENSOR bench. The upper
     tiers' value is SCOPE (many machines -> a fleet -> a facility -> a plant
     -> many plants -> a region), plus general CAPABILITY, plus - uniquely for
     Exa - TEACHING the rest of the family. A bench replaying one recorded
     channel cannot demonstrate any of that with data, and faking a plant to
     make the argument would be exactly the lie this deck exists to avoid.

     So this panel SHOWS the deployment reality and SAYS what each tier sees.
     Every line here is a deployment fact from the locked ladder, not a
     measurement: no counts, no accuracy, no latency claims beyond the tier's
     own published runs-on. The recorded numbers stay where they are earned -
     in the monitor, on the knob, in the fleet tab.

     The cutaway's four floors map to the four tiers that live INSIDE one
     building; the last three are drawn above its roof because that is where
     they are - beyond the plant. Floor bands were measured off the plate
     (the building spans ~6% to ~99% of its height, four floors of ~23%). */
  // `at` labels the chip; `where` completes the sentence "Lives ___."
  var PLANT_FLOORS = {
    giga:  { top: 6,  bot: 29, at: "the server room, top floor",   where: "in the server room on the top floor" },
    micro: { top: 29, bot: 52, at: "the control room",             where: "in the control room" },
    nano:  { top: 52, bot: 75, at: "a cabinet, utility level",     where: "in a cabinet on the utility level" },
    pico:  { top: 75, bot: 99, at: "bolted to the machine",        where: "on the machine itself, bolted to its frame" },
  };

  function whereTheyLive() {
    var det = whyChip("live", "WHERE THEY LIVE · one plant, seven tiers");
    var grid = el("div", "sn-live");

    // the building, with a band marking the selected tier's floor
    var fig = el("figure", "sn-live__plant");
    fig.setAttribute("role", "img");
    fig.setAttribute("aria-label",
      "A cutaway of a factory building: machines on the ground floor, control " +
      "cabinets above them, a control room, and a server room at the top.");
    var art = el("span", "sn-live__art");
    art.appendChild(el("span", "wb-plate__ink"));
    fig.appendChild(art);
    /* v39 (UX audit item 3): the floor marker used to hang off the FIGURE,
       whose box is the art PLUS its headroom padding PLUS the caption - so
       percentages measured off the plate landed roughly a third of a floor
       low, and the ground-floor band (Pico, bolted to the machine) finished
       46px BELOW the building. It hangs off the art itself now, which is what
       PLANT_FLOORS was measured against. */
    var band = el("span", "sn-live__band");
    band.setAttribute("aria-hidden", "true");
    art.appendChild(band);
    var above = el("figcaption", "sn-live__above",
      "above the roof: many plants, a region, the teacher");
    fig.appendChild(above);
    grid.appendChild(fig);

    var right = el("div", "sn-live__right");
    var list = el("div", "sn-live__list");
    list.setAttribute("role", "tablist");
    list.setAttribute("aria-label", "Where each tier lives");
    var detail = el("div", "sn-live__detail");
    if (!PATCH.liveTier) PATCH.liveTier = "pico";

    function paintLive() {
      list.textContent = "";
      detail.textContent = "";
      FAMILY.forEach(function (fam) {
        var on = PATCH.liveTier === fam.id;
        var b = el("button", "sn-live__tier" + (on ? " is-on" : ""));
        b.type = "button";
        b.setAttribute("role", "tab");
        b.setAttribute("aria-selected", on ? "true" : "false");
        tierStyle(b, fam.id);
        var ic = el("span", "sn-live__ic");
        ic.appendChild(modelIcon(fam));
        b.appendChild(ic);
        b.appendChild(el("b", "sn-tiername", fam.label.replace("Wave ", "")));
        var fl = PLANT_FLOORS[fam.id];
        b.appendChild(el("span", "sn-live__at", fl ? fl.at : "beyond this building"));
        b.addEventListener("click", function (e) {
          e.stopPropagation();
          PATCH.liveTier = fam.id;
          paintLive();
        });
        list.appendChild(b);
      });

      var cur = FAMILY.filter(function (f) { return f.id === PATCH.liveTier; })[0];
      var fl = PLANT_FLOORS[cur.id];
      // move the marker: inside the building for the four that live there,
      // floating above the roof for the three that do not
      if (fl) {
        band.style.top = fl.top + "%";
        band.style.height = (fl.bot - fl.top) + "%";
        band.style.opacity = "1";
      } else {
        // above the roof, in the headroom the figure reserves for exactly this
        band.style.top = "0%";
        band.style.height = "4.5%";
        band.style.opacity = ".9";
      }
      band.style.setProperty("--tc", "var(--tier-" + cur.id + ")");

      tierStyle(detail, cur.id);
      var h = el("div", "sn-live__head");
      h.appendChild(el("b", "sn-tiername", cur.label));
      h.appendChild(el("span", "sn-live__size", cur.size + " · " + cur.reach));
      detail.appendChild(h);
      detail.appendChild(el("p", "sn-live__where",
        fl ? "Lives " + fl.where + "." : "Lives beyond this building, on " + cur.runs + "."));
      [["SEES", cur.takes], ["ANSWERS", cur.job],
       ["ONLY IT CAN", cur.only], ["THE ONE BELOW", cur.belowCant],
       ["RUNS ON", cur.runs]].forEach(function (row) {
        var r = el("p", "sn-live__row");
        r.appendChild(el("span", "sn-live__k", row[0]));
        r.appendChild(el("span", null, row[1]));
        detail.appendChild(r);
      });
      // the small side of the argument, where it belongs
      if (cur.id === "pico") {
        var sc = el("figure", "sn-live__scene sn-live__scene--robot");
        sc.setAttribute("role", "img");
        sc.setAttribute("aria-label",
          "A robot arm cell with a small module bolted beside it - where a Pico sits.");
        sc.appendChild(el("span", "wb-plate__ink"));
        detail.appendChild(sc);
      } else if (cur.id === "micro") {
        var sc2 = el("figure", "sn-live__scene sn-live__scene--control");
        sc2.setAttribute("role", "img");
        sc2.setAttribute("aria-label", "A plant control room console - where a site brain runs.");
        sc2.appendChild(el("span", "wb-plate__ink"));
        detail.appendChild(sc2);
      }
      detail.appendChild(el("p", "sn-live__foot",
        cur.status === "recorded"
          ? "Recorded on this bench: you can hear this tier answer in the monitor."
          : "No recorded run on this bench - this is where the tier LIVES and what it " +
            "would see, never a claim about what it said."));
    }
    paintLive();

    right.appendChild(list);
    right.appendChild(detail);
    grid.appendChild(right);
    det.appendChild(grid);
    det.appendChild(el("p", "sn-live__note",
      "Deployment facts from the Wave Spectrum, not measurements: this bench replays one " +
      "recorded fleet, so it can show you where each tier sits and what it would see, but " +
      "only Pico and Nano have runs here to actually hear."));
    return det;
  }

  /* ---- the measured figures live on the knob's tooltip ------------------- */
  function knobTip(floor) {
    var m = PATCH.measured;
    if (!m) return "";
    var c = m.escalation.configs.filter(function (x) {
      return x.config === "child+parent@" + floor.toFixed(1);
    })[0];
    if (!c) return "needs " + floor.toFixed(1) + " to answer alone";
    return "How sure Wave Pico must be to answer alone; below " + floor.toFixed(1) +
      " it asks Wave Nano instead (the margin floor). " +
      "Measured at this floor: " + (c.macro_recall * 100).toFixed(1) + " macro recall · " +
      (c.escalation_rate * 100).toFixed(1) + "% escalate · " +
      (c.pct_of_parent_everywhere * 100).toFixed(0) + "% of parent-everywhere compute, a residency proxy (" +
      m._provenance.suite + ")";
  }

  /* ---- the MARGIN-vs-FLOOR meter: the knob's effect, visible every turn.
     The bar is the RECORDED margin; the tick is the floor, and it slides as
     the knob turns - so a detent that does not flip the verdict still
     visibly moves the threshold against the same recorded bar. ------------- */
  function floorMeter(margin, floor) {
    var W = 230, H = 30, max = Math.max(TOP + 0.6, margin + 0.4);
    var x = function (v) { return 8 + (v / max) * (W - 16); };
    var host = svg("svg", { class: "sn-fm", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "this read was " + margin.toFixed(2) + " sure; the setting needs " + floor.toFixed(1) +
        (margin < floor ? " - not sure enough, so it asked for help" : " - sure enough, so it answered alone") });
    host.appendChild(svg("line", { class: "sn-fm__track", x1: 8, y1: 19, x2: W - 8, y2: 19 }));
    // every measured detent is a notch, so the knob's stations are visible
    DETENTS.forEach(function (d) {
      host.appendChild(svg("line", { class: "sn-fm__detent",
        x1: x(d).toFixed(1), y1: 16, x2: x(d).toFixed(1), y2: 22 }));
    });
    var esc = margin < floor;
    host.appendChild(svg("line", { class: "sn-fm__bar" + (esc ? " is-esc" : ""),
      x1: 8, y1: 19, x2: x(margin).toFixed(1), y2: 19 }));
    host.appendChild(svg("circle", { class: "sn-fm__tip" + (esc ? " is-esc" : ""),
      cx: x(margin).toFixed(1), cy: 19, r: 2.6 }));
    host.appendChild(svg("line", { class: "sn-fm__floor",
      x1: x(floor).toFixed(1), y1: 8, x2: x(floor).toFixed(1), y2: 26 }));
    var fl = svg("text", { class: "sn-fm__t", x: x(floor).toFixed(1), y: 6, "text-anchor": "middle" });
    fl.textContent = "needs " + floor.toFixed(1);
    var ml = svg("text", { class: "sn-fm__t sn-fm__t--m",
      x: Math.min(W - 8, Math.max(24, x(margin))).toFixed(1), y: 29, "text-anchor": "middle" });
    ml.textContent = "this read " + margin.toFixed(2);
    host.appendChild(fl); host.appendChild(ml);
    host.setAttribute("title",
      "The bar is how sure the model was on this read (its margin). The line is your setting " +
      "(the margin floor): anything left of it gets handed up instead of answered.");
    return host;
  }

  /* ---- the semantic tint + change flash: colour rides only on verdict
     words that already carry their meaning in text and shape; the flash
     fires exactly when a stage's verdict CONTENT changes (the same
     signature discipline as the v11 power-on gate), and reduced motion
     keeps the steady tint but skips the flash. ---------------------------- */
  function verdictTint(esc, ok, isFault) {
    if (esc) return "sn-live--esc";
    if (ok) return "sn-live--ok";
    return isFault ? "sn-live--bad" : "sn-live--esc"; // false alarm reads amber
  }
  function flashIfChanged(node, key, sig) {
    PATCH._vSig = PATCH._vSig || {};
    if (PATCH._vSig[key] !== undefined && PATCH._vSig[key] !== sig) {
      if (!REDUCED) node.classList.add("is-flash");
      // the first stage that actually changed is where attention goes
      if (!PATCH._scrollNode) PATCH._scrollNode = node;
    }
    PATCH._vSig[key] = sig;
  }

  /* =====================================================================
     THE MONITOR - the hero. The output at each stage, top to bottom.
     ===================================================================== */
  /* ---- the real recorded series, drawn live ------------------------------
     Every path below is plotted from wave-windows.json - the raw 96-sample
     series behind the very window the model read, verified by the exporter
     against the window body. The MOTION is chrome (a loop, labelled); the
     SHAPE is recorded fact: a STUCK window genuinely flatlines, a DROPOUT
     genuinely gaps, because the samples are real. */
  function seriesOf(r) {
    return (PATCH.windows && r && PATCH.windows[r.node_id]) || null;
  }

  /* HONEST SCALING, ANCHORED TO THE INSTRUMENT'S OWN HEALTHY WINDOW.
     Autoscaling every window to its own min/max would blow a steady window's
     quantization jitter up to full height - STUCK would look noisy, and
     every condition would look alike (founder v12: "stuck moves more like
     OK"). The display span is therefore anchored to the selected TYPE's OK
     window, in RELATIVE terms (the conditions come from different recorded
     machines, so absolute ranges cannot be shared - but the same SENSITIVITY
     can): span >= the OK window's padded span scaled to this record's own
     magnitude. OK then breathes across its band, STUCK renders near-flat at
     the same zoom, a drift ramps out of band, a rail clips. The samples are
     untouched, and the strip prints the DISPLAYED scale so the scale itself
     is honest. Fallback when a type has no OK record: the v8 floor,
     3% of the signal's own magnitude (every current type has an OK pick). */
  function okAnchorRel() {
    var t = currentType();
    if (!t || t.recIdx.none == null || !PATCH.measured) return null;
    var okS = seriesOf(PATCH.measured.records[t.recIdx.none]);
    if (!okS) return null;
    var lo = Math.min.apply(null, okS.samples), hi = Math.max.apply(null, okS.samples);
    var mean = 0;
    for (var i = 0; i < okS.samples.length; i++) mean += okS.samples[i];
    mean /= okS.samples.length || 1;
    if (!isFinite(mean) || Math.abs(mean) < 1e-9) return null;
    return ((hi - lo) * 1.2) / Math.abs(mean); // the OK span, padded, as a fraction of reading
  }

  function scaleOf(samples, anchorRel) {
    var lo = Math.min.apply(null, samples), hi = Math.max.apply(null, samples);
    var mean = 0;
    for (var i = 0; i < samples.length; i++) mean += samples[i];
    mean /= samples.length || 1;
    var floor = anchorRel
      ? anchorRel * Math.abs(mean)                 // the OK window's sensitivity
      : 0.03 * Math.abs(mean);                     // v8 fallback floor
    var span = Math.max(hi - lo, floor, 1e-9);
    var mid = (hi + lo) / 2;
    return { lo: mid - span / 2, hi: mid + span / 2, span: span,
             dataLo: lo, dataHi: hi, anchored: !!anchorRel };
  }

  function fmtN(x) {
    var a = Math.abs(x);
    return a >= 100 ? x.toFixed(1) : a >= 1 ? x.toFixed(2) : x.toFixed(4);
  }

  function seriesPath(samples, W, H, pad, dup, scale) {
    var sc = scale || scaleOf(samples);
    var n = samples.length, reps = dup ? 2 : 1, d = "";
    for (var k = 0; k < reps; k++) {
      for (var i = 0; i < n; i++) {
        var x = ((k * n + i) / (reps * n - 1)) * W;
        var y = pad + (1 - (samples[i] - sc.lo) / sc.span) * (H - pad * 2);
        d += (k === 0 && i === 0 ? "M" : "L") + x.toFixed(1) + " " + y.toFixed(1);
      }
    }
    return d;
  }

  /* ---- the PHOSPHOR renderer: the same real samples, drawn with light.
     A 2D-canvas oscilloscope beam sweeps the recorded window; a low-alpha
     destination-out wash each frame gives the trail its decay. RENDERING
     math only - the data path is seriesOf() and nothing else, and the
     SVG strip below remains the reduced-motion / no-canvas fallback with
     identical labels. */
  var TRACE = { gen: 0 };
  function drawStripCanvas(wrap, s, sc, unitWord) {
    var cv = document.createElement("canvas");
    var ctx = cv.getContext && cv.getContext("2d");
    if (!ctx) return false;
    cv.className = "sn-strip__cv";
    cv.setAttribute("role", "img");
    cv.setAttribute("aria-label", "the recorded sample series for this record, replayed in a loop");
    wrap.appendChild(cv);
    var gen = ++TRACE.gen;
    var W = 560, H = 96, dpr = Math.min(2, window.devicePixelRatio || 1);
    cv.width = W * dpr; cv.height = H * dpr;
    var samples = s.samples, n = samples.length;
    var SWEEP_MS = 9000; // presentation speed, as labelled - not the recorded rate
    var stroke = null, blur = 2.5, frame = 0, t0 = null, li = 0, roNodes = null;
    function yOf(v) { return 8 + (1 - (v - sc.lo) / sc.span) * (H - 16); }
    function xOf(i) { return (i / (n - 1)) * W; }
    function seg(a, b) {
      ctx.beginPath();
      ctx.moveTo(xOf(a), yOf(samples[Math.round(a)] != null ? samples[Math.round(a)] : samples[0]));
      for (var i = Math.floor(a) + 1; i <= Math.floor(b); i++) ctx.lineTo(xOf(i), yOf(samples[i]));
      ctx.lineTo(xOf(b), yOf(samples[Math.min(n - 1, Math.round(b))]));
      ctx.stroke();
    }
    function step(ts) {
      if (gen !== TRACE.gen || !cv.isConnected) return; // superseded or torn down
      // idle while the mesh view is hidden (mode switch) - keep the ticker
      // alive but do no canvas work; the sweep restarts clean on return.
      // (Backgrounded tabs already stop: the browser suspends rAF itself.)
      if (frame % 15 === 0 && cv.offsetParent === null) {
        t0 = null; li = 0; frame = 0; // stays 0 -> re-check every idle tick
        requestAnimationFrame(step);
        return;
      }
      if (t0 == null) t0 = ts;
      if (frame % 45 === 0) { // theme can flip mid-loop: re-read color + glow
        var cs = getComputedStyle(cv);
        stroke = cs.color;
        blur = parseFloat(cs.getPropertyValue("--beam-blur")) || 2.5;
      }
      frame++;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      // phosphor decay: fade what is already lit instead of clearing
      ctx.globalCompositeOperation = "destination-out";
      ctx.fillStyle = "rgba(0,0,0,0.045)";
      ctx.fillRect(0, 0, W, H);
      ctx.globalCompositeOperation = "source-over";
      ctx.strokeStyle = stroke; ctx.fillStyle = stroke;
      ctx.lineWidth = 1.5; ctx.lineJoin = "round"; ctx.lineCap = "round";
      ctx.shadowColor = stroke; ctx.shadowBlur = blur;
      var head = (((ts - t0) / SWEEP_MS) % 1) * (n - 1);
      if (head < li) { seg(li, n - 1); li = 0; } // wrap: finish the window, restart the sweep
      seg(li, head);
      li = head;
      // the beam head
      ctx.beginPath();
      ctx.arc(xOf(head), yOf(samples[Math.round(head)]), 1.8, 0, Math.PI * 2);
      ctx.fill();
      // the text side moves with the graph: the READOUT is the recorded
      // sample under the beam - real data at presentation cadence
      if (frame % 6 === 0) {
        if (frame % 30 === 0 || !roNodes) roNodes = document.querySelectorAll(".sn-beamro");
        var hi0 = Math.round(head);
        var roTxt = "▸ sample " + (hi0 + 1) + "/" + n + " · " + fmtN(samples[hi0]) +
          (unitWord ? " " + unitWord : "");
        for (var ri = 0; ri < roNodes.length; ri++) roNodes[ri].textContent = roTxt;
      }
      requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
    return true;
  }

  function drawStrip(r) {
    var s = seriesOf(r);
    if (!s) return null;
    var wrap = el("div", "sn-strip");
    var head = el("div", "sn-stage__head");
    head.appendChild(el("b", null, "LIVE TRACE"));
    var rec = el("span", "ws-rec" + (REDUCED ? "" : " is-live"));
    rec.appendChild(el("span", "ws-rec__dot"));
    rec.appendChild(el("span", null, "REPLAY · RECORDED LOOP"));
    rec.title = "the record's real " + s.samples.length + " samples, replayed in a loop - " +
      "the motion is presentation; the shape is recorded fact";
    head.appendChild(rec);
    wrap.appendChild(head);
    var W = 560, H = 96;
    var sc = scaleOf(s.samples, okAnchorRel());
    // the phosphor beam when motion is welcome and canvas exists; the SVG
    // strip is the reduced-motion / no-canvas fallback
    if (!REDUCED) {
      var ro = el("span", "sn-beamro");
      ro.setAttribute("aria-hidden", "true"); // the legend carries the accessible numbers
      head.appendChild(ro);
    }
    var painted = !REDUCED && drawStripCanvas(wrap, s, sc, unitWordOf(r));
    if (!painted) {
      var host = svg("svg", { class: "sn-strip__svg", viewBox: "0 0 " + W + " " + H,
        preserveAspectRatio: "none", role: "img",
        "aria-label": "the recorded sample series for this record, replayed in a loop" });
      if (REDUCED) {
        // no motion: the full recorded window, static
        host.appendChild(svg("path", { class: "sn-strip__line",
          d: seriesPath(s.samples, W, H, 8, false, sc) }));
      } else {
        var g = svg("g", { class: "sn-strip__scroll" });
        g.appendChild(svg("path", { class: "sn-strip__line",
          d: seriesPath(s.samples, W * 2, H, 8, true, sc) }));
        host.appendChild(g);
      }
      wrap.appendChild(host);
    }
    var w = r.window || {};
    var legend = el("p", "sn-sub sn-strip__legend",
      "display scale " + fmtN(sc.lo) + " … " + fmtN(sc.hi) +
      (unitWordOf(r) ? " " + unitWordOf(r) : "") +
      (sc.anchored ? " · matched to this sensor's OK window" : "") +
      " · data " + fmtN(sc.dataLo) + " … " + fmtN(sc.dataHi));
    legend.title = "every condition of this sensor draws at the SAME sensitivity - the scale is " +
      "anchored to its OK window, so a steady window looks steady and a wild one looks wild. " +
      "The samples are untouched, and this line prints the scale actually drawn";
    wrap.appendChild(legend);
    wrap.appendChild(el("p", "sn-sub",
      (w.tag || r.node_id) + " · " + s.samples.length + " recorded samples · period " +
      s.period_s + "s · replay speed is presentation, not the recorded rate"));
    return wrap;
  }

  // A pad's preview: the tiny REAL series of the record that pad replays.
  function sparkline(r) {
    var s = seriesOf(r);
    if (!s) return null;
    var host = svg("svg", { class: "sn-spark", viewBox: "0 0 40 14", "aria-hidden": "true" });
    host.appendChild(svg("path", { class: "sn-spark__line",
      d: seriesPath(s.samples, 40, 14, 2, false, scaleOf(s.samples, okAnchorRel())) }));
    return host;
  }

  /* ---- the channel buttons: which stage the screen shows ------------------ */
  /* ---- the FLEET panel: the rollup, drawn -------------------------------- */
  function renderFleet() {
    var box = el("section", "sn-stage sn-stage--fleet");
    var head = el("div", "sn-stage__head");
    head.appendChild(el("b", null, "THE RECORDED FLEET"));
    head.appendChild(el("span", "wp-tag", "REPLAY · RECORDED"));
    box.appendChild(head);
    var f = deriveFleet();
    if (!f || f.none) {
      box.appendChild(el("p", "sn-sub",
        "Nobody is reading the fleet - chain Wave Pico or Wave Nano and the whole " +
        "bench replays under your settings."));
      return box;
    }
    var t = f.totals;
    var sig = [t.caught, t.missed, t.escalated, t.deadEnd, t.falseAlarms, f.policy].join("|");
    var lead = el("p", "sn-para");
    var leadB = el("b", "sn-vword", t.caught + " of " + t.faults + " recorded faults caught");
    flashIfChanged(leadB, "fleet", sig);
    lead.appendChild(leadB);
    lead.appendChild(el("span", null, " across " + t.n + " channels · " + f.policy));
    box.appendChild(lead);

    var rows = el("div", "sn-fleet");
    /* These six were drawn as one list against the same denominator, which read
       as a breakdown of the fleet - and they summed to 129%. Four of them ARE a
       partition (caught + missed + false alarms + quiet = every channel);
       escalated and unheard are CROSS-CUTTING - an escalated read is also
       counted caught or missed above. So the partition is drawn first, and the
       two overlapping counts are separated and labelled as subsets. */
    [["caught", t.caught, "ok"], ["missed", t.missed, "bad"],
     ["false alarms", t.falseAlarms, "dead"], ["quiet (healthy)", t.quiet, null],
     ["__split", 0, null],
     ["escalated", t.escalated, null], ["unheard", t.deadEnd, "dead"],
    ].forEach(function (rw) {
      if (rw[0] === "__split") {
        var note = el("div", "sn-fleet__split");
        note.appendChild(el("i", null,
          "of those same " + t.n + " channels - these overlap the four above, they do not add to them"));
        rows.appendChild(note);
        return;
      }
      var row = el("div", "sn-fleet__row");
      row.appendChild(el("span", "sn-fleet__k" + (rw[2] ? " wp-read__mark--" + rw[2] : ""), rw[0]));
      var bar = el("span", "sn-fleet__bar");
      var fill = el("span", "sn-fleet__fill");
      fill.style.width = Math.round((rw[1] / t.n) * 100) + "%";
      bar.appendChild(fill);
      row.appendChild(bar);
      row.appendChild(el("span", "sn-fleet__n", String(rw[1])));
      row.title = rw[1] + " of " + t.n + " recorded channels - arithmetic over the recorded data";
      rows.appendChild(row);
    });
    box.appendChild(rows);

    var tbl = el("div", "sn-fleet__types");
    PATCH.types.forEach(function (ty) {
      var p = f.perType[ty.key];
      if (!p) return;
      var row = el("div", "sn-fleet__trow");
      row.appendChild(el("b", null, ty.label));
      row.appendChild(el("span", null, p.n + " ch · " + p.faults + " faults · " +
        p.caught + " caught · " + p.missed + " missed"));
      tbl.appendChild(row);
    });
    box.appendChild(tbl);
    box.appendChild(el("p", "sn-sub",
      "The whole recorded fleet replayed under your current settings - every count is " +
      "arithmetic over the 120 recorded records. Turn the FLOOR knob and watch the " +
      "catches move."));
    if (t.fixable) {
      box.appendChild(el("p", "sn-sub wp-read__mark--bad",
        t.fixable + " of the misses would escalate at a higher floor - and the recorded " +
        "senior had the right answer."));
    }
    return box;
  }

  /* ---- ATTENTION: the glass goes to what changed (founder v13: "hard to
     scroll on the monitor - it should go to where we need to pay attention").
     Auto-follow NEVER fights the visitor: a user scroll inside the glass
     suppresses following for a quiet window; a tab click is explicit intent
     and always lands. Reduced motion jumps instead of gliding. ---- */
  var FOLLOW_QUIET_MS = 2000;
  function followSuppressed(now) {
    return (now - PATCH._userScrollAt) < FOLLOW_QUIET_MS;
  }
  function glassScrollTo(node, force) {
    var host = $("wpMonitor");
    if (!host) return;
    if (!force && followSuppressed(Date.now())) return;
    PATCH._autoScrollAt = Date.now();
    var top = node
      ? node.getBoundingClientRect().top - host.getBoundingClientRect().top + host.scrollTop - 6
      : 0;
    if (host.scrollTo) host.scrollTo({ top: top, behavior: REDUCED ? "auto" : "smooth" });
    else host.scrollTop = top;
  }
  function wireGlassScroll() {
    var host = $("wpMonitor");
    if (!host) return;
    host.addEventListener("scroll", function () {
      // a programmatic glide also fires scroll events; its echo is not a user
      if (Date.now() - PATCH._autoScrollAt < 900) return;
      PATCH._userScrollAt = Date.now();
    }, { passive: true });

    // THE SET'S CONTROLS. Pressing a key is a hand on the glass, so it
    // suspends auto-follow exactly like a wheel or a drag - otherwise the
    // next verdict change would yank the reader back mid-paragraph. The wide
    // key is the exception: from the overview it opens DETAILS at the answer;
    // inside detail it returns to the ANSWER. Both are explicit destinations.
    var ctl = $("wsTvCtl");
    if (!ctl) return;
    ctl.addEventListener("click", function (e) {
      var mission = e.target.closest("[data-mission]");
      if (mission && mission.getAttribute("data-mission") === "draw") {
        e.preventDefault();
        dealAndRender();
        return;
      }
      var b = e.target.closest("[data-scroll]");
      if (!b) return;
      e.preventDefault();
      var how = b.getAttribute("data-scroll");
      if (how === "answer") {
        openAnswer();
        return;
      }
      PATCH._userScrollAt = Date.now();
      PATCH._autoScrollAt = Date.now();
      var step = Math.max(80, Math.round(host.clientHeight * 0.7));
      var to = host.scrollTop + (how === "up" ? -step : step);
      if (host.scrollTo) host.scrollTo({ top: to, behavior: REDUCED ? "auto" : "smooth" });
      else host.scrollTop = to;
    });
  }

  function renderTabs() {
    var host = $("wsTabs");
    if (!host) return;
    host.textContent = "";
    host.setAttribute("role", "tablist");
    host.setAttribute("aria-label", "Which stage's output the monitor shows");
    var sts = PATCH.verdict ? PATCH.verdict.stages : [];
    var tabs = [{ id: "all", label: "ALL" }], seen = {};
    /* The strip follows the CHAIN. It used to hard-code pico and nano, so a
       chained Micro or Giga produced a stage on the glass with no way to bring
       it up alone - the tabs claimed the chain was two models long whatever the
       visitor had built. Every model that produced a stage gets a tab, in the
       order the stages run (which normalizeChain keeps in ladder order), and it
       carries its tier colour so the strip matches the cards and the trail. */
    sts.forEach(function (st) {
      var id = null, label = null, tier = null;
      if (st.kind === "raw") { id = "raw"; label = "SENSOR DATA"; }
      else if (st.kind === "pico") { id = "pico"; label = "WAVE PICO"; tier = "pico"; }
      else if (st.kind === "nano" || st.kind === "quietSenior") { id = "nano"; label = "WAVE NANO"; tier = "nano"; }
      else if (st.fam) { id = st.fam.id; label = st.fam.label.toUpperCase(); tier = st.fam.id; }
      if (!id || seen[id]) return;
      seen[id] = true;
      tabs.push({ id: id, label: label, tier: tier });
    });
    tabs.push({ id: "fleet", label: "FLEET" });
    if (!tabs.some(function (t) { return t.id === PATCH.tab; })) PATCH.tab = "all";
    tabs.forEach(function (t) {
      var b = el("button", "sn-tab" + (PATCH.tab === t.id ? " is-on" : ""));
      b.type = "button";
      b.setAttribute("role", "tab");
      b.setAttribute("aria-selected", PATCH.tab === t.id ? "true" : "false");
      b.textContent = t.label;
      if (t.tier) tierStyle(b, t.tier);
      b.title = t.id === "all" ? "everything at once: the sensor, then what each model said"
        : t.id === "fleet" ? "all 120 recorded sensors replayed under your current settings"
        : t.id === "raw" ? "just the numbers the sensor sent"
        : "just this model's stage, on its own";
      b.addEventListener("click", function () {
        PATCH.tab = t.id;
        /* v39 (UX audit item 5): these six tabs filter the DETAIL layer, which
           sits UNDER the face of the set. With the face showing - the state the
           deck opens in - picking a tab repainted a layer nobody could see: the
           chip lit up, the glass did not move, and six controls read as broken.
           A tab is explicit intent to look at a stage, so it opens the detail
           the way DETAILS does, then lands on it. */
        if (!detailOpen()) setDetail(true);
        paintMonitor();
        renderTabs();
        glassScrollTo(null, true); // a tab pick is explicit intent - always land
      });
      host.appendChild(b);
    });
  }

  /* ---- THE FACE OF THE SET -------------------------------------------------
     This is the useful idle screen, not a giant traffic light. It condenses the
     selected recorded window into: what was read, the real trace, what the last
     model said, the route, fleet performance and the downstream response mode.
     Semantic colour is only the status accent. The engraved plate remains in
     front of this layer, so the content reads as glass INSIDE the television.

     The chain is ordered and intentionally constrained, so a free-positioning
     graph library would add a viewport, handles and failure modes without
     making the story clearer. Native SVG/CSS lets the route stay deterministic
     and the plotted shape stay byte-for-byte derived from the committed series. */

  function detailOpen() { return !!PATCH.detail; }

  function setDetail(on) {
    if (PATCH.detail === on) return;
    PATCH.detail = on;
    paintFront();
    paintMonitor();
    if (on) {
      var back = document.querySelector(".sn-back");
      if (back && back.focus) back.focus({ preventScroll: true });
    } else {
      var f = $("wsFront");
      if (f && f.focus) f.focus({ preventScroll: true });
    }
  }

  function paintAnswerKey() {
    var b = $("wsAnswerKey");
    if (!b) return;
    var label = detailOpen() ? "ANSWER" : "DETAILS";
    var words = b.querySelector("span");
    if (words) words.textContent = label;
    b.setAttribute("aria-label", detailOpen()
      ? "Jump to the answer in the open details"
      : "Open details at the answer");
  }

  function openAnswer() {
    if (!detailOpen()) setDetail(true);
    var host = $("wpMonitor");
    if (!host) return;
    var head = host.querySelector(".sn-answer") || host.firstChild;
    glassScrollTo(head && head.nodeType === 1 ? head : null, true);
  }

  function frontTrace(r) {
    var wrap = el("span", "sn-front__trace");
    var s = seriesOf(r);
    if (!s || !s.samples || !s.samples.length) {
      wrap.appendChild(el("span", "sn-front__traceoff", "recorded trace unavailable"));
      return wrap;
    }
    var W = 520, H = 72, pad = 6;
    var sc = scaleOf(s.samples, okAnchorRel());
    var chart = svg("svg", { viewBox: "0 0 " + W + " " + H,
      role: "img", "aria-label": "Actual recorded samples for the selected channel" });
    [18, 36, 54].forEach(function (y) {
      chart.appendChild(svg("line", { class: "sn-front__grid", x1: 0, x2: W, y1: y, y2: y }));
    });
    var d = seriesPath(s.samples, W, H, pad, false, sc);
    chart.appendChild(svg("path", { class: "sn-front__beam", d: d }));
    // The SHAPE is always the complete recorded series above. These two marks
    // are CRT chrome: a bright segment and scan line traveling across that
    // same immutable path, making replay feel alive without inventing a tick.
    chart.appendChild(svg("path", { class: "sn-front__scan", d: d, pathLength: 100 }));
    chart.appendChild(svg("line", { class: "sn-front__sweep", x1: 0, x2: 0, y1: 4, y2: H - 4 }));
    wrap.appendChild(chart);
    wrap.appendChild(el("span", "sn-front__tracemeta",
      s.samples.length + " RECORDED SAMPLES · " + fmtN(sc.dataLo) + "—" + fmtN(sc.dataHi) +
      (unitWordOf(r) ? " " + unitWordOf(r) : " · UNIT NOT STATED") + " · LOOPING CRT SWEEP"));
    return wrap;
  }

  /* One compact sentence for the glance screen AND the detailed trail. All
     values come from the stage already derived from the recorded run. */
  function stageResponse(st) {
    if (!st) return "NO STAGE";
    if (st.kind === "pico") {
      return st.esc
        ? "ASKED FOR HELP · " + st.margin.toFixed(2) + " BELOW " + st.floor.toFixed(1)
        : 'ANSWERED "' + String(st.said).toUpperCase() + '" · ' + st.margin.toFixed(2) + " SURE";
    }
    if (st.kind === "nano") {
      return 'ANSWERED "' + String(st.verdict).toUpperCase() + '" · ' +
        st.r.parent.margin.toFixed(2) + " SURE";
    }
    if (st.kind === "quietSenior") {
      return 'NOT CALLED · WOULD ALSO SAY "' + String(st.wouldSay).toUpperCase() + '"';
    }
    if (st.kind === "scoperun") {
      return (st.scope.unit === "plant" ? "PLANT CASE" : "SITE CASE") + " · " +
        String(currentRecord().truth).toUpperCase() + " · " +
        (st.scope.unit === "plant" ? "PLANT RECOUNT" : "SITE RECOUNT") + " · " +
        st.tally.caught + "/" + st.tally.faults + " CAUGHT";
    }
    if (st.kind === "scope") {
      /* v37 (founder: "lets finish the rest"): the three beyond-replay
         tiers printed one identical line - the case truth three times over.
         Each now says ITS OWN honest thing: what it would add, and exactly
         why this bench stops short of it. Tera/Peta stay conditional (the
         recording holds one plant, no region); Exa is not a reader at all -
         and "teacher" stays a role, never a claim that it trained the
         recorded runs on this bench (it did not - they are scratch-trained). */
      var beyond = {
        tera: "CASE RECEIVED · WOULD CORRELATE MANY PLANTS · ONLY ONE RECORDED",
        peta: "CASE RECEIVED · WOULD CARRY A REGION, LEANER · NO REGIONAL RECORDS",
        exa: "NOT A READER · THE FAMILY'S TEACHER · NO CASE TO TAKE",
      };
      return beyond[st.fam && st.fam.id] || "CASE RECEIVED · BEYOND REPLAY";
    }
    if (st.kind === "silent") return "NO RECORDED RUN · SILENT";
    return "PASS THROUGH";
  }

  function frontRoute(sts) {
    var route = el("span", "sn-front__route");
    route.appendChild(el("span", "sn-front__routek", "MODEL RESPONSES · CHAIN ORDER"));
    var line = el("span", "sn-front__routewire");
    var sensor = el("span", "sn-front__rnode sn-front__rnode--sensor");
    sensor.appendChild(el("b", "sn-front__rname", "SENSOR"));
    sensor.appendChild(el("i", "sn-front__rstate", "RECORDED WINDOW"));
    line.appendChild(sensor);
    PATCH.chain.forEach(function (id, i) {
      line.appendChild(el("span", "sn-front__edge", "›"));
      var fam = familyById(id);
      var st = sts[i + 1]; // stage zero is raw wire; the rest follow the chain
      var state = st && (st.kind === "pico" && st.esc ? "ask"
        : st.kind === "quietSenior" ? "wait"
        : st.kind === "scope" || st.kind === "silent" ? "pass" : "read");
      var node = el("span", "sn-front__rnode is-" + (state || "pass"));
      node.appendChild(el("b", "sn-front__rname", fam ? fam.label : id.toUpperCase()));
      node.appendChild(el("i", "sn-front__rstate", stageResponse(st)));
      node.title = (fam ? fam.label + ": " : "") + stageResponse(st);
      tierStyle(node, id);
      line.appendChild(node);
    });
    if (!PATCH.chain.length) {
      line.appendChild(el("span", "sn-front__edge", "›"));
      line.appendChild(el("span", "sn-front__rnode is-empty", "ADD A MODEL"));
    }
    route.appendChild(line);
    return route;
  }

  function frontScore() {
    var score = el("span", "sn-front__score");
    var fleet = deriveFleet();
    score.appendChild(el("span", "sn-front__scorek", "RECORDED FLEET · 120 CHANNELS"));
    if (!fleet || fleet.none) {
      score.appendChild(el("span", "sn-front__scoreline", "Seat a recorded model to score the replay"));
      return score;
    }
    var t = fleet.totals;
    var ratio = t.faults ? t.caught / t.faults : 0;
    var line = el("span", "sn-front__scoreline");
    line.appendChild(el("b", null, t.caught + "/" + t.faults + " FAULTS CAUGHT"));
    /* "27 missed · 2 tunable" read as two parallel categories, but fixable is
       set only inside the missed branch - the 2 are a SUBSET of the 27. Said
       that way now; the detail panel already phrased it correctly. */
    line.appendChild(el("i", null, t.missed + " missed" +
      (t.fixable ? " · " + t.fixable + " of them tunable" : " · none recoverable by knob")));
    score.appendChild(line);
    var meter = el("span", "sn-front__meter");
    var fill = el("span", "sn-front__meterfill");
    fill.style.width = Math.round(ratio * 100) + "%";
    meter.appendChild(fill);
    score.appendChild(meter);
    return score;
  }

  /* ---- SHIFT MODE -------------------------------------------------------
     A deal chooses among records the selector already exposes. Randomness
     chooses a card, never a reading, prediction, score, or repair outcome.
     There is deliberately no timer: the bench changes only under a hand. */
  var FIELD_RIGS = {
    stuck: {
      title: "FROZEN INPUT",
      objective: "Prove the process and channel disagree, trace where the value stops moving, then route an authorized input-path inspection.",
      authored: "In this training scenario an independent reference moves while the receiving input remains held.",
      controls: [
        { id: "verify", kind: "switch", label: "REFERENCE",
          question: "Which source can prove the process moved while this channel stayed held?",
          choices: [{ id: "channel", label: "CHANNEL" }, { id: "independent", label: "INDEPENDENT" }],
          correct: "independent", finding: "Independent reference moves; the recorded channel remains held.",
          try: "The channel is the suspect evidence. Compare it with an independent reference next." },
        { id: "context", kind: "dial", label: "TRACE POINT",
          question: "At which test point does this scenario's value stop following the reference?",
          choices: [{ id: "sensor", label: "SENSOR" }, { id: "wire", label: "WIRE" }, { id: "input", label: "INPUT" }],
          correct: "input", finding: "The authored clue isolates the held value at the receiving input.",
          try: "That point still follows the reference in this scenario. Continue toward the receiving input." },
        { id: "handoff", kind: "action", label: "OPEN INPUT-CHANNEL WORK ORDER",
          question: "What safely records the input-path inspection for authorized maintenance?",
          correct: "open", finding: "Input-channel inspection routed to authorized maintenance.",
          try: "Complete the reference comparison and trace point before opening the work order." },
      ],
    },
    drifting: {
      title: "CALIBRATION OFFSET",
      objective: "Capture the as-found sweep, compare it with the approved calibration record, then route calibration review with the evidence attached.",
      authored: "In this training scenario a documented as-found sweep exposes an offset against the calibration record.",
      controls: [
        { id: "verify", kind: "sequence", label: "CAL POINT",
          question: "Which ordered sweep preserves the instrument's as-found offset?",
          choices: [{ id: "zero", label: "0%" }, { id: "mid", label: "50%" }, { id: "span", label: "100%" }],
          sequence: ["zero", "mid", "span"], correct: "span",
          finding: "The 0, 50, and 100 percent as-found sweep is recorded in order.",
          try: "An as-found sweep starts at 0 percent, includes 50 percent, and ends at 100 percent." },
        { id: "context", kind: "dial", label: "COMPARE",
          question: "What reference can distinguish calibration offset from real process movement?",
          choices: [{ id: "process", label: "PROCESS" }, { id: "neighbor", label: "NEIGHBOR" }, { id: "record", label: "CAL RECORD" }],
          correct: "record", finding: "The authored calibration record exposes the scenario offset.",
          try: "Process movement alone cannot establish calibration bias. Compare the documented reference." },
        { id: "handoff", kind: "action", label: "OPEN CALIBRATION WORK ORDER",
          question: "Where should the calibration evidence go before anyone changes the instrument?",
          correct: "open", finding: "Calibration review routed under the site's approved procedure.",
          try: "Finish the as-found sweep and reference comparison before the handoff." },
      ],
    },
    dropout: {
      title: "INTERMITTENT LOOP",
      objective: "Confirm the missing samples at their source, isolate the intermittent test point, then route a field-connection inspection.",
      authored: "In this training scenario source timestamps confirm gaps and the field terminal is intermittent.",
      controls: [
        { id: "verify", kind: "switch", label: "TIMELINE",
          question: "Which timeline can prove samples vanished before the display?",
          choices: [{ id: "display", label: "DISPLAY" }, { id: "source", label: "SOURCE" }],
          correct: "source", finding: "Source timestamps confirm that samples are absent, not merely hidden.",
          try: "The display can hide or delay a point. Check timestamps at the source next." },
        { id: "context", kind: "dial", label: "TEST POINT",
          question: "Which test point first reveals the intermittent signal path?",
          choices: [{ id: "supply", label: "SUPPLY" }, { id: "field", label: "FIELD" }, { id: "input", label: "INPUT" }],
          correct: "field", finding: "The authored clue finds the intermittent path at the field terminal.",
          try: "That point stays stable in this scenario. Compare the field terminal next." },
        { id: "handoff", kind: "action", label: "OPEN FIELD-CONNECTION WORK ORDER",
          question: "What safely routes a field-connection inspection?",
          correct: "open", finding: "Field-connection inspection routed to authorized maintenance.",
          try: "Confirm the gap and isolate the test point before opening a work order." },
      ],
    },
    noisy: {
      title: "NOISY SIGNAL PATH",
      objective: "Separate process movement from measurement noise, isolate the installation clue, then route a shield-path inspection.",
      authored: "In this training scenario the independent reference stays stable and the shield path carries the clue.",
      controls: [
        { id: "verify", kind: "switch", label: "COMPARE",
          question: "Which comparison separates process motion from measurement noise?",
          choices: [{ id: "channel", label: "CHANNEL" }, { id: "independent", label: "INDEPENDENT" }],
          correct: "independent", finding: "Independent reference stays stable while this channel remains noisy.",
          try: "Looking only at the noisy channel cannot separate process variation from measurement noise." },
        { id: "context", kind: "dial", label: "INSTALLATION",
          question: "Which installation path carries this scenario's independent clue?",
          choices: [{ id: "mount", label: "MOUNT" }, { id: "shield", label: "SHIELD" }, { id: "route", label: "ROUTE" }],
          correct: "shield", finding: "The authored clue isolates the shield path for inspection.",
          try: "That installation point does not explain this scenario. Inspect the shield path next." },
        { id: "handoff", kind: "action", label: "OPEN SHIELD-ROUTING WORK ORDER",
          question: "What safely routes shielding and cable inspection?",
          correct: "open", finding: "Shield and routing inspection handed to authorized maintenance.",
          try: "Verify the variation and installation clue before the handoff." },
      ],
    },
    railed: {
      title: "RANGE MISMATCH",
      objective: "Establish the approved range, compare the receiving configuration, then route the mismatch through controlled review.",
      authored: "In this training scenario the approved range record and receiving-input configuration do not agree.",
      controls: [
        { id: "verify", kind: "switch", label: "RANGE SOURCE",
          question: "Which source defines the instrument's approved range?",
          choices: [{ id: "display", label: "DISPLAY" }, { id: "record", label: "RECORD" }],
          correct: "record", finding: "The approved record supplies the expected instrument range.",
          try: "The display is part of the path under test. Start from the approved range record." },
        { id: "context", kind: "dial", label: "INPUT RANGE",
          question: "Which receiving range agrees with the approved instrument record?",
          choices: [{ id: "low", label: "LOW" }, { id: "match", label: "MATCH" }, { id: "high", label: "HIGH" }],
          correct: "match", finding: "MATCH aligns the training input with the approved range.",
          try: "That detent still disagrees with the approved range in this scenario." },
        { id: "handoff", kind: "action", label: "OPEN CONFIGURATION-CHANGE REVIEW",
          question: "What safely controls a receiving-range configuration change?",
          correct: "open", finding: "Configuration review routed through authorized change control.",
          try: "Verify the range source and matching input configuration before review." },
      ],
    },
  };

  function incidentCandidates() {
    var out = [];
    (PATCH.types || []).forEach(function (t) {
      (t.conds || []).forEach(function (cond) {
        var at = t.recIdx && t.recIdx[cond];
        if (at == null || !PATCH.measured || !PATCH.measured.records[at]) return;
        out.push({ typeKey: t.key, cond: cond, recordIndex: at });
      });
    });
    return out;
  }

  function missionDeckTally() {
    var tally = { total: 0, incidents: 0, checks: 0 };
    incidentCandidates().forEach(function (card) {
      tally.total++;
      if (card.cond === "none") tally.checks++;
      else tally.incidents++;
    });
    return tally;
  }

  function caseLesson(r) {
    if (!r || r.truth === "none") return "clear";
    if (r.child.margin < PATCH.floor && r.parent.prediction === r.truth) return "nano";
    if (r.child.margin >= PATCH.floor && r.child.prediction !== r.truth &&
        r.parent.prediction === r.truth && nextFloorFor(r.child.margin) != null) return "floor";
    if (r.child.prediction !== r.truth && r.parent.prediction !== r.truth) return "blind";
    if (r.child.margin >= PATCH.floor && r.child.prediction === r.truth) return "pico";
    return "miss";
  }

  /* The opening is a playable tutorial, not roulette. Each of the first
     three cards teaches one ladder idea using an existing committed record;
     after that the full deck shuffles as before. */
  function guidedCard(cards, drawNumber) {
    var lesson = ["nano", "blind", "floor"][drawNumber];
    if (!lesson) return null;
    for (var i = 0; i < cards.length; i++) {
      var r = PATCH.measured && PATCH.measured.records[cards[i].recordIndex];
      if (caseLesson(r) === lesson) return cards[i];
    }
    return null;
  }

  function randomCard(n) {
    if (n < 2) return 0;
    if (window.crypto && window.crypto.getRandomValues) {
      var word = new Uint32Array(1);
      window.crypto.getRandomValues(word);
      return word[0] % n;
    }
    // Deterministic test/private-context fallback; still only walks committed
    // cards, and never changes anything until DEAL is pressed.
    return (PATCH.mission.draws * 7 + 3) % n;
  }

  function drawIncident(forcedAt) {
    var cards = incidentCandidates();
    if (!cards.length) return null;
    var guided = typeof forcedAt === "number" ? null : guidedCard(cards, PATCH.mission.draws);
    var at = typeof forcedAt === "number" ? forcedAt
      : guided ? cards.indexOf(guided) : randomCard(cards.length);
    at = ((at % cards.length) + cards.length) % cards.length;
    var pick = cards[at];
    selectType(pick.typeKey, pick.cond);
    beginSelectedIncident(true);
    return pick;
  }

  function resetField() {
    PATCH.mission.field = { values: {}, visits: {}, feedback: null };
  }

  function beginSelectedIncident(countDraw) {
    var r = currentRecord();
    if (!r) return false;
    PATCH.mission.active = true;
    PATCH.mission.phase = PATCH.cond === "none" ? "clear" : "incident";
    if (countDraw) PATCH.mission.draws++;
    PATCH.mission.actions = {};
    PATCH.mission.moveStage = 0;
    PATCH.mission.moveFeedback = null;
    resetField();
    PATCH.mission.incidentNode = r.node_id;
    PATCH.mission.verifiedNode = null;
    PATCH.mission.note = PATCH.cond === "none"
      ? "Independent recorded OK window. No intervention is required; deal another window when ready."
      : "Recorded training incident. Detection is measured; the diagnostic playbook is operator-authored.";
    derive();
    return true;
  }

  function missionPlan() {
    var m = PATCH.mission;
    var book = m && m.active ? PLAYBOOKS[PATCH.cond] : null;
    if (!book || PATCH.cond === "none") {
      return { locked: false, clear: true, label: "NO FAULT PLAYBOOK", steps: [] };
    }
    var move = incidentMove();
    return { locked: !move || move.kind !== "ready", clear: false,
             label: book.label, steps: book.steps, rig: FIELD_RIGS[PATCH.cond] || null };
  }

  function nextFloorFor(margin) {
    for (var i = 0; i < DETENTS.length; i++) if (DETENTS[i] > margin) return DETENTS[i];
    return null;
  }

  function moveChoices(ids, nextFloor) {
    var copy = {
      pico: ["WAVE PICO", "reads one channel on the machine"],
      nano: ["WAVE NANO", "hears Pico's doubtful reads at the gateway"],
      micro: ["WAVE MICRO", "adds facility context and an independent site audit"],
      giga: ["WAVE GIGA", "compares sites across the plant after site triage"],
      floor: ["RAISE PICO FLOOR", "hand this read to Nano at " + Number(nextFloor).toFixed(1)],
    };
    return ids.map(function (id) {
      return { id: id, label: copy[id][0], role: copy[id][1] };
    });
  }

  /* One evidence-driven question before the field exercise. This is the
     ladder made playable: the right answer depends on who read this exact
     card, whether Pico asked, whether Nano's committed counterfactual was
     right, and the current margin floor. */
  function incidentMove() {
    var m = PATCH.mission;
    var r = currentRecord();
    if (!m.active || !r || r.truth === "none") return null;
    var info = chainInfo();
    if (!info.reader) {
      return { kind: "model", correct: "pico",
        question: "No recorded reader is on the wire. Which tier belongs on the machine?",
        reason: "Pico is the one-channel machine reader.",
        choices: moveChoices(["pico", "nano", "micro"]) };
    }
    if (info.reader === "nano" && info.picoAt < 0) {
      if (r.parent.prediction === r.truth && m.moveStage === 0) {
        return { kind: "identify", correct: "nano",
          question: "Which tier read this wire directly and made the recorded call?",
          reason: "Nano is the first recorded reader in this chain.",
          choices: moveChoices(["pico", "nano", "micro"]) };
      }
      if (PATCH.chain.indexOf("micro") >= 0) {
        return { kind: "ready", correct: null, question: "Site triage is unlocked.", choices: [] };
      }
      return { kind: "model", correct: "micro",
        question: "The channel has a model answer. Which tier adds an independent facility-level investigation next?",
        reason: "Micro widens the case from one machine to its site without pretending to be another recorded classifier.",
        choices: moveChoices(["nano", "micro", "giga"]) };
    }

    var escalated = r.child.margin < PATCH.floor;
    if (escalated && !info.senior) {
      return { kind: "model", correct: "nano",
        question: "Pico asked for help, but nobody heard it. Which tier belongs directly behind Pico?",
        reason: "Nano is the gateway senior that adjudicates doubtful Pico reads.",
        choices: moveChoices(["pico", "nano", "micro"]) };
    }
    if (!escalated && r.child.prediction !== r.truth && r.parent.prediction === r.truth) {
      var nextFloor = nextFloorFor(r.child.margin);
      if (nextFloor != null) {
        return { kind: "threshold", correct: "floor", nextFloor: nextFloor,
          question: "Nano had the right recorded counterfactual. What move would have sent this doubtful call to it?",
          reason: "Raise SURE ENOUGH above Pico's " + r.child.margin.toFixed(2) + " margin so Nano hears the read.",
          choices: moveChoices(["floor", "micro", "giga"], nextFloor) };
      }
    }
    if (escalated && info.senior && r.parent.prediction === r.truth && m.moveStage === 0) {
      return { kind: "identify", correct: "nano",
        question: "Pico asked for help. Which tier caught this fault in the recorded run?",
        reason: "Nano heard the doubt and its recorded answer matches the replay label.",
        choices: moveChoices(["pico", "nano", "micro"]) };
    }
    if (!escalated && r.child.prediction === r.truth && m.moveStage === 0) {
      return { kind: "identify", correct: "pico",
        question: "This fault was caught without a handoff. Which tier made the call on the machine?",
        reason: "Pico answered above the current floor and matched the replay label.",
        choices: moveChoices(["pico", "nano", "micro"]) };
    }
    if (PATCH.chain.indexOf("micro") >= 0) {
      return { kind: "ready", correct: null, question: "Site triage is unlocked.", choices: [] };
    }
    var blind = r.child.prediction !== r.truth && r.parent.prediction !== r.truth;
    return { kind: "model", correct: "micro",
      question: blind
        ? "Pico and Nano share this blind spot. Which tier should widen the case to site evidence?"
        : "Detection is complete. Which tier should widen the case to site evidence?",
      reason: blind
        ? "Micro opens an independent site audit; it does not overwrite either recorded miss."
        : "Micro adds facility context before the case is handed to plant operations.",
      choices: moveChoices(["nano", "micro", "giga"]) };
  }

  function missionChooseMove(choice) {
    var move = incidentMove();
    if (!move || !move.correct) return false;
    if (choice !== move.correct) {
      var lesson = move.correct === "nano"
        ? "Nano belongs at the gateway where it can hear Pico's doubt; a broader tier does not replace that handoff."
        : move.correct === "micro"
          ? "Micro is the first facility-context tier. Giga compares sites only after this site case is established."
          : move.correct === "floor"
            ? "The recorded Nano answer was already right. Change when Pico asks; adding scope does not repair the missed handoff."
            : "Pico is the recorded reader on the machine for this call.";
      PATCH.mission.moveFeedback = { kind: "try", text: lesson };
      return false;
    }
    PATCH.mission.moveFeedback = { kind: "pass", text: move.reason };
    if (move.kind === "identify") {
      PATCH.mission.moveStage++;
      return true;
    }
    if (choice === "nano") {
      chainAdd("nano");
      return true;
    }
    if (choice === "floor") {
      PATCH.floor = move.nextFloor;
      PATCH.mission.moveStage++;
      derive();
      render();
      react("Pico now hands this read to Nano at the " + PATCH.floor.toFixed(1) + " SURE ENOUGH floor.");
      return true;
    }
    if (choice === "micro") {
      addMissionMicro();
      return true;
    }
    if (choice === "pico") {
      chainAdd("pico");
      return true;
    }
    return false;
  }

  function missionStep(id) {
    var plan = missionPlan();
    if (plan.locked || plan.clear) return false;
    var step = plan.steps.filter(function (s) { return s.id === id; })[0];
    if (!step) return false;
    var at = plan.steps.indexOf(step);
    if (at > 0 && !PATCH.mission.actions[plan.steps[at - 1].id]) return false;
    PATCH.mission.actions[id] = true;
    PATCH.mission.phase = "triage";
    return true;
  }

  function missionReady() {
    var plan = missionPlan();
    return !plan.locked && !plan.clear && plan.steps.length &&
      plan.steps.every(function (s) { return !!PATCH.mission.actions[s.id]; });
  }

  function fieldProgress() {
    var plan = missionPlan();
    if (plan.clear) return 0;
    return plan.steps.reduce(function (n, step) {
      return n + (PATCH.mission.actions[step.id] ? 1 : 0);
    }, 0);
  }

  function fieldApply(id, choice) {
    var plan = missionPlan();
    var rig = plan.rig;
    if (plan.locked || plan.clear || !rig) return false;
    var control = rig.controls.filter(function (item) { return item.id === id; })[0];
    if (!control || PATCH.mission.actions[id]) return false;
    var at = rig.controls.indexOf(control);
    if (at > 0 && !PATCH.mission.actions[rig.controls[at - 1].id]) {
      PATCH.mission.field.feedback = { kind: "try", step: id,
        text: "Complete " + rig.controls[at - 1].label + " first." };
      return false;
    }
    var valid = control.kind === "action" ? choice === "open"
      : control.choices.some(function (item) { return item.id === choice; });
    if (!valid) return false;
    PATCH.mission.field.values[id] = choice;

    if (control.sequence) {
      var visits = PATCH.mission.field.visits[id] || [];
      var expected = control.sequence[visits.length];
      if (choice !== expected) {
        PATCH.mission.field.visits[id] = [];
        PATCH.mission.field.feedback = { kind: "try", step: id, text: control.try };
        return false;
      }
      visits.push(choice);
      PATCH.mission.field.visits[id] = visits;
      if (visits.length < control.sequence.length) {
        PATCH.mission.field.feedback = { kind: "working", step: id,
          text: "As-found point " + visits.length + " of " + control.sequence.length + " recorded. Continue in order." };
        return false;
      }
    } else if (choice !== control.correct) {
      PATCH.mission.field.feedback = { kind: "try", step: id, text: control.try };
      return false;
    }

    if (!missionStep(id)) return false;
    PATCH.mission.field.feedback = { kind: "pass", step: id, text: control.finding };
    return true;
  }

  function verifyMission() {
    if (!missionReady()) return false;
    var t = currentType();
    if (!t || !t.recIdx || t.recIdx.none == null) return false;
    var incident = PATCH.mission.incidentNode;
    selectType(t.key, "none");
    var ok = currentRecord();
    PATCH.mission.phase = "verified";
    PATCH.mission.completed++;
    PATCH.mission.verifiedNode = ok && ok.node_id;
    PATCH.mission.note = "Advanced from " + incident + " to a separately recorded OK window, " +
      (ok ? ok.node_id : "unknown") + ". This verifies the workflow; it does not prove " +
      "the training steps repaired the prior machine.";
    derive();
    return true;
  }

  function dealAndRender() {
    var pick = drawIncident();
    if (!pick) return;
    PATCH.detail = false;
    render();
    react((pick.cond === "none" ? "OK shift card dealt" : "Incident shift card dealt: " + pick.cond.toUpperCase()) +
      ". Every value and model answer comes from the recorded replay.");
  }

  function frontMission() {
    var m = PATCH.mission;
    var row = el("span", "sn-front__mission");
    row.appendChild(el("b", "sn-front__missionk", "SHIFT " + String(m.completed + 1).padStart(2, "0")));
    var text = "START A MYSTERY CASE";
    if (m.phase === "clear") text = "CARD CLEAR · INDEPENDENT RECORDED OK";
    else if (m.phase === "verified") text = "CASE CLOSED · " + m.completed + " TOTAL";
    else if (m.active && missionPlan().locked) {
      var move = incidentMove();
      text = move ? "NEXT · " +
        (move.kind === "threshold" ? "FIX THE HANDOFF" : "CHOOSE THE RIGHT MODEL") :
        "NEXT · ADD WAVE MICRO";
    }
    else if (m.active) text = "NEXT · OPEN THE CASE · FOLLOW THE CLUES";
    row.appendChild(el("span", "sn-front__missiontext", text));
    return row;
  }

  function missionUpgradeRail() {
    var rail = el("div", "sn-mission__ladder");
    rail.setAttribute("aria-label", "Pico detects, Nano checks doubt, Micro adds site context");
    [
      { id: "pico", label: "PICO · DETECTS" },
      { id: "nano", label: "NANO · CHECKS" },
      { id: "micro", label: "MICRO · SITE TRIAGE", needed: true },
    ].forEach(function (item, i) {
      if (i) rail.appendChild(el("span", "sn-mission__edge", "→"));
      var node = el("span", "sn-mission__node" + (item.needed ? " is-needed" : ""), item.label);
      tierStyle(node, item.id);
      rail.appendChild(node);
    });
    return rail;
  }

  function drawMissionMove(move) {
    var game = el("section", "sn-mission__move");
    var head = el("div", "sn-mission__movehead");
    head.appendChild(el("b", null, "WHO SHOULD HEAR THIS?"));
    head.appendChild(el("span", null, "PICK ONE · FEEDBACK IS FREE"));
    game.appendChild(head);
    game.appendChild(el("p", "sn-mission__moveq", move.question));
    game.appendChild(el("p", "sn-mission__moveevidence", readerHandoff(currentRecord())));
    var choices = el("div", "sn-mission__movechoices");
    move.choices.forEach(function (choice) {
      var button = el("button", "sn-mission__movechoice");
      button.type = "button";
      button.dataset.move = choice.id;
      if (choice.id === "micro") tierStyle(button, "micro");
      else if (choice.id === "nano") tierStyle(button, "nano");
      else if (choice.id === "pico") tierStyle(button, "pico");
      else if (choice.id === "giga") tierStyle(button, "giga");
      var label = choice.label;
      if (choice.id === "micro") label = /blind spot/i.test(move.question)
        ? "SEAT WAVE MICRO · AUDIT THE BLIND SPOT"
        : "SEAT WAVE MICRO · UNLOCK SAFE CHECKS";
      else if (choice.id === "nano" && move.kind !== "identify") label = "SEAT WAVE NANO · HEAR PICO";
      else if (choice.id === "floor") label = "TURN SURE ENOUGH TO " + move.nextFloor.toFixed(1);
      else label = "CHOOSE " + choice.label;
      if (PATCH.chain.indexOf(choice.id) >= 0 && move.kind !== "identify") {
        label = choice.label + " · ALREADY SEATED";
      }
      button.appendChild(el("b", null, label));
      button.appendChild(el("span", null, choice.role));
      button.addEventListener("click", function () {
        var changed = missionChooseMove(choice.id);
        renderGamebar();
        if (move.kind === "identify" || !changed) paintMonitor();
      });
      choices.appendChild(button);
    });
    game.appendChild(choices);
    var feedback = PATCH.mission.moveFeedback;
    if (feedback) {
      var result = el("p", "sn-mission__movefeedback is-" + feedback.kind, feedback.text);
      result.setAttribute("aria-live", "polite");
      game.appendChild(result);
    }
    return game;
  }

  function addMissionMicro(e) {
    if (e) { e.preventDefault(); e.stopPropagation(); }
    chainAdd("micro");
    // The field panel exists on both faces of the television, while the
    // expanded mission card exists only in DETAILS. Always complete the
    // required handoff first; the CRT arrival treatment is optional chrome.
    focusFieldStep("verify");
    var unlocked = document.querySelector(".sn-mission");
    if (unlocked) {
      unlocked.classList.add("is-arriving");
      var firstControl = unlocked.querySelector('[data-field-step="verify"]');
      glassScrollTo(firstControl || unlocked, true);
    }
    react("Wave Micro seated. Site context unlocked the safe training checks; no Micro inference was invented.");
  }

  function missionBeat(step, index) {
    if (step && step.kind === "verify") return "OBSERVE";
    if (step && step.kind === "context") return "ISOLATE";
    if (step && step.kind === "handoff") return "HAND OFF";
    return ["OBSERVE", "ISOLATE", "HAND OFF"][index] || "CHECK";
  }

  function drawMission() {
    var m = PATCH.mission;
    var box = el("section", "sn-mission");
    var head = el("div", "sn-mission__head");
    head.appendChild(el("b", null, "CASE BOARD"));
    head.appendChild(el("span", null, m.completed + " CASE" +
      (m.completed === 1 ? "" : "S") + " CLOSED"));
    box.appendChild(head);

    function dealButton(label) {
      var b = el("button", "sn-mission__deal", label || "START NEXT CASE");
      b.type = "button";
      b.addEventListener("click", dealAndRender);
      return b;
    }

    if (!m.active) {
      var deck = missionDeckTally();
      var idleDeck = el("div", "sn-mission__idledeck");
      var deckHead = el("div", "sn-mission__deckhead");
      deckHead.appendChild(el("b", null, "MYSTERY DECK"));
      deckHead.appendChild(el("span", null, deck.incidents + " FAULTS · " +
        deck.checks + " HEALTHY READS · FIRST 3 ARE GUIDED"));
      idleDeck.appendChild(deckHead);
      var loop = el("ol", "sn-mission__loop");
      loop.setAttribute("aria-label", "Deal, investigate, and close the case");
      [
        { beat: "CATCH", copy: "Choose the model move that fits the signal." },
        { beat: "TRACE", copy: "Use three clues to narrow the problem." },
        { beat: "CLOSE", copy: "Compare your route with a healthy read." },
      ].forEach(function (step, index) {
        var item = el("li");
        item.appendChild(el("span", "sn-mission__loopn", String(index + 1).padStart(2, "0")));
        var words = el("span", "sn-mission__loopwords");
        words.appendChild(el("b", null, step.beat));
        words.appendChild(el("span", null, step.copy));
        item.appendChild(words);
        loop.appendChild(item);
      });
      idleDeck.appendChild(loop);
      box.appendChild(idleDeck);
      box.appendChild(el("p", "sn-mission__lead",
        "Pick a mystery from the measured deck. Nothing changes until you make a move, and a wrong guess always tells you something useful."));
      box.appendChild(dealButton("START FIRST CASE"));
      return box;
    }
    if (m.phase === "clear") {
      box.appendChild(el("strong", "sn-mission__clear", "CARD CLEAR · NO INTERVENTION"));
      box.appendChild(el("p", "sn-mission__copy", m.note));
      box.appendChild(dealButton());
      return box;
    }
    if (m.phase === "verified") {
      box.appendChild(el("strong", "sn-mission__clear",
        "CASE CLOSED · SEPARATE RECORDED OK"));
      box.appendChild(el("p", "sn-mission__copy", m.note));
      box.appendChild(dealButton());
      return box;
    }

    var plan = missionPlan();
    var missLesson = PATCH.verdict && PATCH.verdict.label === "MODEL LIMIT"
      ? modelLimitLesson(currentRecord(), finalAnswer(PATCH.verdict.stages)) : null;
    box.appendChild(el("span", "sn-mission__incident", "MACHINE CASE · " + plan.label));
    box.appendChild(el("p", "sn-mission__copy",
      "Something is wrong with this signal. First choose who should hear it; then use the clue kit to narrow down why."));
    if (missLesson) {
      var missAudit = el("div", "sn-mission__missaudit");
      missAudit.appendChild(el("b", null, "MODEL MISS AUDIT"));
      var missCompare = el("span", null,
        "MODEL SAID " + missLesson.modelAnswer + " ≠ REPLAY SAYS " + missLesson.recordedTruth);
      missAudit.appendChild(missCompare);
      missAudit.appendChild(el("p", null,
        "The replay label exposed this blind spot. The exercise below gathers independent evidence; it does not rewrite either recorded model answer."));
      box.appendChild(missAudit);
    }
    if (plan.rig && plan.rig.objective) {
      var brief = el("div", "sn-mission__brief");
      brief.appendChild(el("b", null, "YOUR GOAL"));
      brief.appendChild(el("p", null, plan.rig.objective));
      box.appendChild(brief);
    }
    if (plan.locked) {
      box.classList.add("is-locked");
      var move = incidentMove();
      box.appendChild(el("p", "sn-mission__copy", missLesson
        ? "The models missed this one. Choose the move that widens the investigation, then use the clues to find what the all-clear overlooked."
        : "Look at the handoff, choose the model or threshold that belongs next, and unlock the clues."));
      box.appendChild(missionUpgradeRail());
      if (move) box.appendChild(drawMissionMove(move));
      box.appendChild(el("p", "sn-mission__unlocknote", missLesson
        ? "The right move opens an independent reference and a receiving-path clue. It never pretends that a model caught what it missed."
        : "The right move changes the local chain or handoff setting and opens the clue kit."));
      return box;
    }

    box.classList.add("is-unlocked");
    var doneCount = plan.steps.filter(function (step) { return !!m.actions[step.id]; }).length;
    box.appendChild(el("strong", "sn-mission__playlabel",
      "CLUE KIT OPEN · " + doneCount + "/" + plan.steps.length + " CLUES SOLVED"));
    box.appendChild(el("span", "sn-mission__incident", "FOLLOW THE EVIDENCE"));
    box.appendChild(el("p", "sn-mission__copy",
      "Micro widened the case to site context. Work the clues in order; each answer explains what it rules in or out."));
    var list = el("ol", "sn-mission__steps");
    plan.steps.forEach(function (step, i) {
      var done = !!m.actions[step.id];
      var enabled = i === 0 || !!m.actions[plan.steps[i - 1].id];
      var active = enabled && !done;
      var fieldControl = plan.rig && plan.rig.controls[i];
      var li = el("li", "sn-mission__step" +
        (done ? " is-done" : active ? " is-active" : " is-locked"));
      var b = el("button", "sn-mission__stepbtn");
      b.type = "button";
      b.disabled = done || !enabled;
      b.setAttribute("aria-current", active ? "step" : "false");
      b.appendChild(el("span", "sn-mission__stepn", done ? "✓" : String(i + 1).padStart(2, "0")));
      var words = el("span", "sn-mission__stepwords");
      words.appendChild(el("span", "sn-mission__beat", missionBeat(step, i)));
      words.appendChild(el("b", null, step.label));
      words.appendChild(el("i", null, done
        ? "EVIDENCE RECEIPT · CAPTURED"
        : "NEXT CONTROL · " + (fieldControl ? fieldControl.label : step.tool) + " · USE IT BELOW"));
      words.appendChild(el("span", null, done && fieldControl ? fieldControl.finding
        : active ? step.detail : "LOCKED · COMPLETE THE PRIOR BEAT"));
      b.appendChild(words);
      b.addEventListener("click", function () {
        focusFieldStep(step.id);
      });
      li.appendChild(b);
      list.appendChild(li);
    });
    box.appendChild(list);

    var activeControl = currentFieldControl();
    if (activeControl && plan.rig) {
      var activeAt = plan.rig.controls.indexOf(activeControl);
      var instrument = el("section", "sn-mission__instrument");
      var instrumentHead = el("div", "sn-mission__instrumenthead");
      instrumentHead.appendChild(el("b", null, "ACTIVE BENCH CONTROL"));
      instrumentHead.appendChild(el("span", null, "USE IT HERE · MIRRORED IN THE LEFT BAY"));
      instrument.appendChild(instrumentHead);
      instrument.appendChild(drawFieldControl(activeControl, activeAt, plan));
      var inlineFeedback = m.field && m.field.feedback;
      if (inlineFeedback && inlineFeedback.step === activeControl.id) {
        var inlineResult = el("p", "sn-field__feedback is-" + inlineFeedback.kind,
          inlineFeedback.text);
        inlineResult.setAttribute("aria-live", "polite");
        instrument.appendChild(inlineResult);
      }
      box.appendChild(instrument);
    } else if (missionReady()) {
      var ready = el("section", "sn-mission__ready");
      ready.appendChild(el("b", "sn-mission__readytitle", "CASE READY · EVIDENCE PACKET 03/03"));
      ready.appendChild(el("p", "sn-mission__readycopy",
        "You found the route. Close the case by comparing it with a separate healthy reading."));
      var receipts = el("ul", "sn-mission__receipts");
      plan.rig.controls.forEach(function (control, i) {
        var receipt = el("li", null);
        receipt.appendChild(el("span", null, "✓"));
        receipt.appendChild(el("b", null, missionBeat(plan.steps[i], i)));
        receipt.appendChild(el("span", null, control.finding));
        receipts.appendChild(receipt);
      });
      ready.appendChild(receipts);
      var compareOk = el("button", "sn-mission__verify", "CLOSE CASE WITH A HEALTHY READ →");
      compareOk.type = "button";
      compareOk.setAttribute("aria-label", "VERIFY WITH RECORDED OK. Opens a separate recorded replay window.");
      compareOk.addEventListener("click", function () {
        if (!verifyMission()) return;
        react("Workflow compared with a separate recorded OK window; this is not proof that the training actions repaired the prior machine.");
        focusFactoryRelease();
      });
      ready.appendChild(compareOk);
      box.appendChild(ready);
    }
    var safety = el("details", "sn-mission__safety");
    safety.appendChild(el("summary", null, "Safety and evidence note"));
    safety.appendChild(el("span", null,
      "These clue steps are authored for the game, not generated model output. This puzzle changes no machinery and does not claim the prior machine was repaired. Physical work remains under authorized site procedures, including hazardous-energy isolation where required."));
    box.appendChild(safety);
    return box;
  }

  function paintFront() {
    var f = $("wsFront");
    if (!f) return;
    var v = PATCH.verdict;
    var state = v ? v.state : "off";
    var face = LAMP_FACE[state] || LAMP_FACE.off;
    var sts = v ? v.stages : stages();
    var r = currentRecord();
    var t = currentType();
    var answer = finalAnswer(sts);
    var miss = v && v.label === "MODEL LIMIT" ? modelLimitLesson(r, answer) : null;
    var response = v && v.response ? v.response : responseMode();
    f.textContent = "";
    f.dataset.state = state;
    f.hidden = detailOpen();
    f.setAttribute("aria-expanded", detailOpen() ? "true" : "false");
    /* v39 (UX audit item 3): the strip's two scroll keys drive the DETAIL
       layer's scrollport. With the face up there is nothing behind them to
       move - the face is one screenful and never scrolls - so pressing them
       did nothing at all. They belong to the detail, so they arrive with it. */
    var ctl = $("wsTvCtl");
    if (ctl) ctl.classList.toggle("is-lean", !detailOpen());
    paintAnswerKey();
    if (detailOpen()) return;

    var top = el("span", "sn-front__top");
    top.appendChild(el("span", "sn-front__k", "CURRENT READ"));
    top.appendChild(el("span", "sn-front__live", watchingLabel() + " · RECORDED REPLAY"));
    f.appendChild(top);

    var read = el("span", "sn-front__read");
    read.appendChild(el("span", "sn-front__sensor",
      (t ? t.label : "NO SENSOR") + " · " + (CONDW[PATCH.cond] || String(PATCH.cond).toUpperCase())));
    read.appendChild(el("span", "sn-front__tag",
      r ? ((r.window && r.window.tag) || r.node_id) : "NO RECORDED WINDOW"));
    f.appendChild(read);
    f.appendChild(frontTrace(r));

    var result = el("span", "sn-front__result");
    var status = el("span", "sn-front__status");
    status.appendChild(el("span", "sn-front__sym", (v && v.sym) || face.sym));
    status.appendChild(el("span", null, (v && v.label) || face.label));
    result.appendChild(status);
    var answerBox = el("span", "sn-front__answer");
    answerBox.appendChild(el("span", "sn-front__answerk", answer
      ? answer.who + (miss ? " MODEL ANSWER" : " SAID") : "MODEL OUTPUT"));
    answerBox.appendChild(el("b", "sn-front__word",
      answer && answer.word ? String(answer.word).toUpperCase() : "NO MODEL ANSWER"));
    if (answer && answer.gloss) answerBox.appendChild(el("span", "sn-front__gloss", answer.gloss));
    if (miss) answerBox.appendChild(el("span", "sn-front__truth",
      "RECORDED TRUTH · " + miss.recordedTruth + " · " + miss.label));
    result.appendChild(answerBox);
    f.appendChild(result);

    f.appendChild(frontRoute(sts));
    /* v38 (UX audit item 4): "23/50 FAULTS CAUGHT" before the visitor has
       done anything is a score for a game they have not started - the same
       fleet recount appears once they change something or a case is live
       (and always on the FLEET tab). */
    if (PATCH.touched || PATCH.mission.active) f.appendChild(frontScore());
    f.appendChild(frontMission());
    var foot = el("span", "sn-front__foot");
    /* the response mode ("AFTER A FINDING · LOG ONLY") is a real control on
       the strip below the prompt; on the glass it was a label with nothing
       under it, so the glance keeps it in the aria text only */
    var hint = el("span", "sn-front__hint");
    hint.appendChild(el("span", "sn-front__hintkey", "PRESS ANYWHERE ON THE GLASS"));
    hint.appendChild(el("b", "sn-front__hintmain", "OPEN FULL MODEL OUTPUT ↗"));
    hint.appendChild(el("i", "sn-front__hintmore", "RAW DATA · EVERY MODEL · FLEET DETAIL"));
    foot.appendChild(hint);
    f.appendChild(foot);
    f.setAttribute("aria-label",
      "Current recorded read: " + (t ? t.label : "no sensor") + ", " +
      (CONDW[PATCH.cond] || PATCH.cond) + ". Model result: " +
      ((v && v.label) || face.label) + ". " +
      (answer && answer.word ? answer.who + " said " + answer.word + ". " : "") +
      "After a finding: " + response.label + ". Activate to inspect the raw data, every model stage, " +
      "and the fleet detail.");
  }

  function wireFront() {
    var f = $("wsFront");
    if (!f) return;
    f.addEventListener("click", function () { setDetail(true); });
  }

  function paintMonitor() {
    var host = $("wpMonitor");
    if (!host) return;
    host.textContent = "";
    var tv = host.parentNode;
    if (tv && tv.setAttribute) tv.setAttribute("data-tour", "monitor");
    // the power-on sweep fires only when what the glass SHOWS changes -
    // record, tab, chain or reply - not on every repaint (v11: operator
    // toggles were strobing the screen). Chrome, reduced-motion-guarded.
    var r0 = currentRecord();
    var monSig = [r0 && r0.node_id, PATCH.cond, PATCH.tab,
                  PATCH.chain.join(","), PATCH.reply ? PATCH.reply.kind : ""].join("|");
    if (monSig !== PATCH._monSig) {
      PATCH._monSig = monSig;
      host.classList.remove("is-flip");
      void host.offsetWidth;
      host.classList.add("is-flip");
    }

    var v = PATCH.verdict;
    var sts = v ? v.stages : stages();
    var r = currentRecord();
    PATCH._scrollNode = null; // nominated by whatever actually changes below

    // the way back to the face of the set, first thing in the detail
    if (detailOpen()) {
      var back = el("button", "sn-back");
      back.type = "button";
      back.appendChild(el("span", "sn-back__arrow", "\u25c0"));
      back.appendChild(el("span", null, "STATUS"));
      back.title = "back to the status screen";
      back.addEventListener("click", function () { setDetail(false); });
      host.appendChild(back);
    }

    if (PATCH.tab === "fleet") {
      host.appendChild(renderFleet());
      if (PATCH.reply) host.appendChild(drawReply(PATCH.reply));
      var fadeF = el("div", "sn-mon__fade");
      fadeF.setAttribute("aria-hidden", "true");
      host.appendChild(fadeF);
      paintLampAndCert(v, r);
      paintFront();
      followChanges();
      return;
    }

    // THE ANSWER, first and biggest. The cascade used to open with a wall of
    // feature numbers where every line weighed the same; now the glass leads
    // with what the chain actually concluded and who concluded it, and the
    // supporting stages sit beneath it. Read off the printed stages, so the
    // headline can never disagree with the detail below.
    if (PATCH.tab === "all") {
      var ans = finalAnswer(sts);
      if (ans) {
        var miss = v && v.label === "MODEL LIMIT" ? modelLimitLesson(r, ans) : null;
        var ah = el("section", "sn-answer");
        if (ans.tier) tierStyle(ah, ans.tier);
        var akey = el("span", "sn-answer__k", ans.word == null ? "NO ANSWER"
          : miss ? "MODEL ANSWER · NOT THE TRUTH" : "THE ANSWER");
        ah.appendChild(akey);
        if (ans.word == null) {
          ah.appendChild(el("p", "sn-answer__none sn-live--esc", ans.note));
        } else {
          var aw = el("b", "sn-answer__word sn-vword " +
            verdictTint(false, ans.ok, ans.isFault), ans.word.toUpperCase());
          var aSig = ans.word + "|" + (ans.ok ? "ok" : "bad") + "|" + ans.who;
          flashIfChanged(aw, "answer", aSig);
          if (aSig !== PATCH._ansSig) { PATCH._scrollNode = ah; PATCH._ansSig = aSig; }
          ah.appendChild(aw);
          if (ans.gloss) {
            var ag = el("span", "sn-answer__gloss");
            ag.appendChild(el("span", null, ans.gloss + " "));
            ag.appendChild(el("i", "sn-gloss__k", "glossary"));
            ah.appendChild(ag);
          }
          var by = el("p", "sn-answer__by");
          by.appendChild(el("span", "sn-tiername", ans.who));
          by.appendChild(el("span", null, ans.asked
            ? " answered, after the small model asked for help"
            : " answered on its own, on the machine"));
          ah.appendChild(by);
          if (miss) {
            var mismatch = el("div", "sn-answer__miss");
            mismatch.appendChild(el("b", "sn-answer__misslabel", miss.label));
            var compare = el("div", "sn-answer__compare");
            var modelSide = el("span", "sn-answer__side");
            modelSide.appendChild(el("i", null, "MODEL ANSWER"));
            modelSide.appendChild(el("strong", null, miss.modelAnswer));
            compare.appendChild(modelSide);
            var truthSide = el("span", "sn-answer__side is-truth");
            truthSide.appendChild(el("i", null, "RECORDED TRUTH"));
            truthSide.appendChild(el("strong", null, miss.recordedTruth));
            compare.appendChild(truthSide);
            mismatch.appendChild(compare);
            mismatch.appendChild(el("b", "sn-answer__explaink", "HOW THIS BENCH KNOWS"));
            mismatch.appendChild(el("p", "sn-answer__explain", miss.knownBy + ". " +
              "The benchmark supplies the disagreement; neither model discovered it."));
            mismatch.appendChild(el("b", "sn-answer__explaink", "WHY NANO DID NOT FIX IT"));
            mismatch.appendChild(el("p", "sn-answer__explain", miss.why));
            mismatch.appendChild(el("b", "sn-answer__explaink",
              miss.shapeTitle || "WHY NONE LOOKED PLAUSIBLE"));
            mismatch.appendChild(el("i", "sn-answer__explainsource", miss.shapeBy));
            mismatch.appendChild(el("p", "sn-answer__explain", miss.shape));
            mismatch.appendChild(el("b", "sn-answer__explaink", "WHAT CAN CATCH IT"));
            mismatch.appendChild(el("p", "sn-answer__explain", miss.catch));
            ah.appendChild(mismatch);
          }
        }
        host.appendChild(ah);
      }

      // and one compact line per model: who did what, in chain order, each
      // tagged with its own tier colour so the eye can follow the hand-off
      var trail = el("ol", "sn-trail");
      trail.setAttribute("aria-label", "what each model did with this read");
      sts.forEach(function (st) {
        var tier = st.kind === "pico" ? "pico"
          : (st.kind === "nano" || st.kind === "quietSenior") ? "nano"
          : st.fam ? st.fam.id : null;
        if (!tier) return;
        var li = el("li", "sn-trail__row");
        tierStyle(li, tier);
        li.appendChild(el("span", "sn-trail__dot"));
        li.appendChild(el("b", "sn-tiername", st.who));
        var did = stageResponse(st);
        li.appendChild(el("span", "sn-trail__did", did));
        trail.appendChild(li);
      });
      if (trail.childNodes.length) host.appendChild(trail);
    }

    if (r && (PATCH.tab === "all" || PATCH.tab === "raw")) {
      var strip = drawStrip(r);
      if (strip) host.appendChild(strip);
    }

    sts.forEach(function (st) {
      if (PATCH.tab !== "all") {
        // one filter for every model, so a tab added for Micro or Giga solos
        // its stage the same way Pico's does
        var stId = st.kind === "raw" ? "raw"
          : st.kind === "pico" ? "pico"
          : (st.kind === "nano" || st.kind === "quietSenior") ? "nano"
          : st.fam ? st.fam.id : null;
        if (stId !== PATCH.tab) return;
      }
      var solo = PATCH.tab !== "all";
      var box = el("section", "sn-stage sn-stage--" + st.kind + (solo ? " sn-stage--solo" : ""));
      var stTier = st.kind === "pico" ? "pico"
        : (st.kind === "nano" || st.kind === "quietSenior") ? "nano"
        : st.fam ? st.fam.id : null;
      if (stTier) tierStyle(box, stTier);
      var head = el("div", "sn-stage__head");
      head.appendChild(el("b", stTier ? "sn-tiername" : null, st.who));
      // the protocol name stays, one size down: plain word first, jargon kept
      // where a reader who knows it should still find it
      if (st.tech) head.appendChild(el("i", "sn-stage__tech", st.tech));
      if (st.role) head.appendChild(el("i", "sn-stage__role", st.role));
      if (st.kind === "raw") {
        var rec = el("span", "ws-rec");
        rec.appendChild(el("span", "ws-rec__dot"));
        rec.appendChild(el("span", null, "MONITORING · REPLAY"));
        rec.title = "the recorded window, replayed - nothing here is live";
        head.appendChild(rec);
        if (!REDUCED) {
          var echo = el("span", "sn-beamro sn-beamro--echo");
          echo.setAttribute("aria-hidden", "true");
          echo.title = "the recorded sample under the beam, as it sweeps";
          head.appendChild(echo);
        }
      }
      box.appendChild(head);
      if (stTier) box.appendChild(drawTierCase(st));

      if (st.kind === "raw") {
        var log = el("pre", "ws-log", st.body);
        log.title = "byte-for-byte, the numbers the model read in the recorded run";
        if (solo) {
          box.appendChild(log);
        } else {
          // combined view: the numbers are one click away, never gone
          var d = el("details", "sn-rawfold");
          var sm = el("summary", null, "the exact numbers the model read");
          d.appendChild(sm);
          d.appendChild(log);
          box.appendChild(d);
        }
      } else if (st.kind === "pico") {
        var isFaultP = st.r.truth !== "none";
        var line = el("p", "sn-proto");
        // the verdict word carries a semantic tint (word + shape still carry
        // the meaning without colour) and flashes when it CHANGES
        var vb = el("b", "sn-vword " + (st.esc ? "sn-proto__esc sn-live--esc" : verdictTint(false, st.ok, isFaultP)),
          st.esc ? "ASKED FOR HELP ↑" : 'ANSWERED " ' + st.said + '"');
        flashIfChanged(vb, "pico", (st.esc ? "esc" : "as|" + st.said) + "|" + st.margin);
        line.appendChild(vb);
        // margin vs floor, ALWAYS both numbers - the knob's effect is
        // visible on every detent, not only when the verdict flips
        var howSure = el("span", null, st.esc
          ? " · " + st.margin.toFixed(2) + " sure, needs " + st.floor.toFixed(1) + " to answer alone - so it asked"
          : " · " + st.margin.toFixed(2) + " sure, needs " + st.floor.toFixed(1) + " to answer alone");
        howSure.title = "how sure = the gap between the model's best answer and its next one " +
          "(the margin). The setting is the margin floor on the knob.";
        line.appendChild(howSure);
        line.appendChild(el("i", "sn-stage__tech", st.esc ? "escalate" : "assert"));
        box.appendChild(line);
        box.appendChild(floorMeter(st.margin, st.floor));
        box.appendChild(el("p", "sn-sub", st.esc
          ? "This is all the model sends: one word and how sure it was. Being able to say " +
            "'I am not sure' is the point - that is what sends the read up the chain."
          : "This is all the model sends: one word and how sure it was - built for machines to " +
            "read, not people."));
      } else if (st.kind === "nano") {
        var vw = el("p", "sn-verdict");
        var nb = el("b", "sn-vword " + verdictTint(false, st.ok, st.isFault),
          st.verdict.toUpperCase());
        flashIfChanged(nb, "nano", st.verdict + "|" + (st.ok ? "ok" : "bad"));
        vw.appendChild(nb);
        if (st.gloss) {
          var g = el("span", "sn-gloss");
          g.appendChild(el("span", null, " - " + st.gloss + " "));
          g.appendChild(el("i", "sn-gloss__k", "glossary"));
          vw.appendChild(g);
        }
        box.appendChild(vw);
        box.appendChild(el("p", "sn-para", st.para));
      } else if (st.kind === "quietSenior") {
        box.appendChild(el("p", "sn-sub", st.note));
      } else if (st.kind === "deadend") {
        var dn = el("p", "sn-para sn-para--warn sn-live--esc", st.note);
        flashIfChanged(dn, "deadend", "deadend");
        box.appendChild(dn);
      } else if (st.kind === "scoperun") {
        box.appendChild(drawScopeRun(st));
      } else if (st.kind === "scope") {
        box.appendChild(drawScopeCard(st));
      } else if (st.kind === "silent") {
        box.appendChild(el("p", "sn-sub",
          st.fam.blurb + " - it has no recorded run on this bench, so it stays quiet."));
      }
      host.appendChild(box);
    });

    // a stage that left the cascade forgets its signature, so its RETURN
    // flashes too (an escalate that comes back is news)
    if (PATCH._vSig) {
      var present = {};
      sts.forEach(function (st) { present[st.kind] = true; });
      Object.keys(PATCH._vSig).forEach(function (k) {
        if (!present[k]) delete PATCH._vSig[k];
      });
    }

    if (PATCH.reply) {
      var replyNode = drawReply(PATCH.reply);
      host.appendChild(replyNode);
      // a NEW reply is where attention goes - it outranks a changed stage
      var rSig = PATCH.reply.kind + "|" + (PATCH.reply.token || "") +
        (PATCH.reply.text ? "|t" : "");
      if (rSig !== PATCH._replySig) PATCH._scrollNode = replyNode;
      PATCH._replySig = rSig;
    } else {
      PATCH._replySig = "";
    }

    /* v37 (founder: "CASE BOARD - move that to the end"): the shift console
       used to open the detail view, pushing THE ANSWER below the fold; the
       glass now leads with the verdict and the case board closes the read. */
    if (detailOpen() && PATCH.tab === "all") host.appendChild(drawMission());

    // the glass tells you it scrolls: a sticky fade hugs the bottom edge
    // whenever the cascade runs past it (v8 screenshots clipped the Nano
    // paragraph invisibly)
    var fade = el("div", "sn-mon__fade");
    fade.setAttribute("aria-hidden", "true");
    host.appendChild(fade);

    paintLampAndCert(v, r);
    paintFront();
    followChanges();
  }

  function followChanges() {
    if (PATCH._scrollNode) {
      var node = PATCH._scrollNode;
      PATCH._scrollNode = null;
      glassScrollTo(node);
    }
  }

  function paintLampAndCert(v, r) {
    var lampBig = $("wpLamp2"), why = $("wpWhy");
    if (lampBig && v) {
      lampBig.dataset.state = v.state;
      lampBig.title = "derived from recorded records only: red = a recorded fault was missed, " +
        "yellow = the chain needs attention or made a false call, green = the recorded read was handled correctly";
      var f2 = LAMP_FACE[v.state] || LAMP_FACE.off;
      lampBig.textContent = "";
      lampBig.appendChild(el("span", "wp-lampwin__sym", v.sym || f2.sym));
      lampBig.appendChild(el("span", "wp-lampwin__label", v.label || f2.label));
    }
    /* SAY IT ONCE. With the face of the set leading, the chin was
       repeating the same lamp and the same why sentence, verbatim, on the same
       screen. While the face is showing, the chin keeps only its lamp (a
       persistent indicator beside the AFTER A FINDING control, which is a
       control and stays) and drops the duplicated sentence; when the detail is
       open the face is gone, so the chin carries the full line again. */
    if (why && v) {
      why.textContent = detailOpen() ? v.why : "";
      why.hidden = !detailOpen();
    }
    var chin = why && why.parentNode;
    if (chin && chin.classList) chin.classList.toggle("is-lean", !detailOpen());

    var certHost = $("wpCertHost");
    var certWasOpen = false;
    if (certHost) {
      var prevCert = certHost.querySelector(".sn-cert");
      certWasOpen = !!(prevCert && prevCert.open);
      certHost.textContent = "";
    }
    if (r && certHost) {
      var det = el("details", "sn-cert");
      det.open = certWasOpen; // a pad press must not slam it shut (v11)
      var sum = el("summary", null, "record certificate");
      det.appendChild(sum);
      var w = r.window || {};
      var dl = el("dl", "wp-cert");
      [["record", r.node_id + " (scene " + r.scene_id + ")"],
       ["unit", unitWordOf(r) || "not stated in the wire"],
       ["window", w.lo != null ? w.lo + " … " + w.hi + " · mean " + w.mean : "—"],
       ["truth", r.truth],
       ["child said", r.child.prediction + " · margin " + r.child.margin.toFixed(2)],
       ["senior said", r.parent.prediction + " · margin " + r.parent.margin.toFixed(2)],
       ["digest", null],
      ].forEach(function (row) {
        dl.appendChild(el("dt", null, row[0]));
        if (row[1] == null) dl.appendChild(el("dd", "wp-cert__pending", "— pending export —"));
        else dl.appendChild(el("dd", null, row[1]));
      });
      det.appendChild(dl);
      det.appendChild(el("p", "wp-note",
        "The raw stage above is byte-for-byte the window the model read in the recorded " +
        "run; the live trace is its real sample series from the recorded windows bundle. " +
        "Every stage prints recorded fields; the margins are recorded logprob " +
        "differences - nothing in this browser computes one."));
      // the TRUST chip: the deck's one public mistake, worn as provenance.
      var ret = PATCH.measured && PATCH.measured._provenance && PATCH.measured._provenance.retracted;
      if (ret) {
        var trust = whyPop("why trust these numbers?", "sn-why--trust");
        trust.appendChild(el("p", "sn-why__p",
          "This deck once published a wrong one: “" + ret.claim + "” - " + ret.status +
          ". The impossible part (two quantizations returning identical aggregates) is what " +
          "caught it, and the exporter now REFUSES that signature outright. Every figure here " +
          "carries its run and suite so you can re-check us the same way."));
        det.appendChild(trust);
      }
      if (PATCH.scene && r.scene_id === PATCH.scene.scene_id) drawScopeInto(det);
      certHost.appendChild(det);
    }
  }

  var LAMP_FACE = {
    off:    { sym: "·", label: "STANDING BY" },
    green:  { sym: "●", label: "ALL CLEAR" },
    yellow: { sym: "△", label: "CHECK REQUIRED" },
    red:    { sym: "⊗", label: "FAULT MISSED" },
  };

  /* ---- AFTER A FINDING: response, not detection -------------------------
     The models read every replay in all three modes. This control sends the
     resulting finding somewhere; it never changes the answer or lamp. */
  var RESPONSE_MODES = [
    { id: "log", label: "LOG ONLY",
      hint: "record findings without an automatic response" },
    { id: "human", label: "HUMAN REVIEW",
      hint: "send each finding to a person for review" },
    { id: "policy", label: "POLICY QUEUE",
      hint: "send findings to the configured origin-aware policy queue" },
  ];

  function responseNow() {
    return responseMode().id;
  }

  function renderOp() {
    var host = $("wbOp");
    if (!host) return;
    host.textContent = "";
    var now = responseNow();
    var wrap = el("div", "sn-watch");
    var lead = el("span", "sn-watch__lead");
    lead.appendChild(el("b", "sn-watch__on", watchingLabel()));
    lead.appendChild(el("span", "sn-watch__k", "AFTER A FINDING"));
    wrap.appendChild(lead);
    var row = el("div", "sn-watch__row");
    row.setAttribute("role", "radiogroup");
    row.setAttribute("aria-label", "What happens after the models report a finding");
    RESPONSE_MODES.forEach(function (w) {
      var b = el("button", "sn-watch__opt" + (now === w.id ? " is-on" : ""));
      b.type = "button";
      b.setAttribute("role", "radio");
      b.setAttribute("aria-checked", now === w.id ? "true" : "false");
      b.title = w.hint;
      b.textContent = w.label;
      b.addEventListener("click", function () {
        PATCH.operator = w.id === "human";
        PATCH.authority = w.id === "policy";
        derive(); render();
        react(w.id === "human"
          ? "Models still watch; findings now route to human review."
          : w.id === "policy"
          ? "Models still watch; findings now route to the policy queue."
          : "Models still watch; findings are logged only.");
        var again = document.querySelector(".sn-watch__opt.is-on");
        if (again) again.focus({ preventScroll: true });
      });
      row.appendChild(b);
    });
    wrap.appendChild(row);
    host.appendChild(wrap);
  }

  /* ---- the recorded pump trace, where it belongs ------------------------- */
  function drawScopeInto(box) {
    var sc = PATCH.scene;
    if (!sc || !sc.steps || !sc.steps.length) return;
    var W = 520, H = 96;
    var host = svg("svg", { class: "wp-scope", viewBox: "0 0 " + W + " " + H, role: "img",
      "aria-label": "The recorded channel trace, with the fault onset marked" });
    var vals = sc.steps.map(function (st) { return st.v; });
    var lo = Math.min.apply(null, vals), hi = Math.max.apply(null, vals);
    var span = (hi - lo) || 1;
    var d = vals.map(function (v, i) {
      var x = (i / (vals.length - 1)) * (W - 8) + 4;
      var y = H - 8 - ((v - lo) / span) * (H - 16);
      return (i ? "L" : "M") + x.toFixed(1) + " " + y.toFixed(1);
    }).join(" ");
    host.appendChild(svg("path", { class: "wp-scope__line", d: d }));
    host.appendChild(svg("line", { class: "wp-scope__onset", x1: (0.4 * W).toFixed(1), x2: (0.4 * W).toFixed(1), y1: 4, y2: H - 4 }));
    box.appendChild(host);
    box.appendChild(el("p", "wp-note",
      "The recorded seed-42 pump scene: " + sc.steps.length + " recorded steps, fault onset at the dashed line. REPLAY, never live."));
  }

  function shortName(p) { return String(p || "").split("/").pop(); }

  /* ---- the TV's parallax: decorative bezel only --------------------------
     Pure chrome. The interactive overview and scrolling detail stay in the
     page plane so their full hit areas cannot move out from under a pointer.
     No listeners are even attached under reduced motion. */
  function wireTilt() {
    if (REDUCED) return;
    var tv = document.querySelector(".sn-tv");
    if (!tv) return;
    tv.addEventListener("pointermove", function (e) {
      var b = tv.getBoundingClientRect();
      if (!b.width || !b.height) return;
      var dx = (e.clientX - b.left) / b.width - 0.5;
      var dy = (e.clientY - b.top) / b.height - 0.5;
      tv.style.setProperty("--tiltx", (dy * -1.5).toFixed(2) + "deg");
      tv.style.setProperty("--tilty", (dx * 1.5).toFixed(2) + "deg");
    });
    tv.addEventListener("pointerleave", function () {
      tv.style.setProperty("--tiltx", "0deg");
      tv.style.setProperty("--tilty", "0deg");
    });
  }

  /* ---- the a11y mirror: the same bench as a list ------------------------- */
  function renderMirror() {
    var m = $("wpMirror");
    if (!m) return;
    m.textContent = "";
    var t = currentType();
    var r = currentRecord();
    m.appendChild(el("li", null, "Sensor: " +
      (t ? t.label + ", condition " + (CONDW[PATCH.cond] || PATCH.cond) +
        (r ? ", replaying " + ((r.window && r.window.tag) || r.node_id) : "") : "none")));
    if (!PATCH.chain.length) {
      m.appendChild(el("li", null, "Chain: empty - the sensor writes raw wire"));
    } else {
      PATCH.chain.forEach(function (id, i) {
        var fam = familyById(id);
        m.appendChild(el("li", null, "Chain " + (i + 1) + ": " + fam.label +
          (id === "pico" ? " at floor " + PATCH.floor.toFixed(1) : "") +
          (fam.status === "recorded" ? "" : " (no recorded run - silent)")));
      });
    }
    m.appendChild(el("li", null, "Models: watching the recorded replay"));
    m.appendChild(el("li", null, "After a finding: " + responseMode().label.toLowerCase()));
  }

  /* =====================================================================
     INTAKE - THE TRANSLATION SHIM (handoff 3)

     A task-native base model given "hi" dreams a Modbus register table -
     free-sampling a Wave child produces corpus dreams, not conversation,
     BY DESIGN. So every human input is classified and wrapped:

       wire blob    -> pass through verbatim: this is the model's native food
       raw numbers  -> a channel dict; the UNIT IS NOT DEFAULTED - a defaulted
                       unit is an invented fact, and units are precisely what
                       the OPC UA substitute-value trap is about
       scenario     -> mapped to a template, never sent as words to a model
       small talk   -> answered by the interface from the faceplate; it
                       never reaches a model

     The classifier SHOWS ITS EVIDENCE per row, and refuses to guess when
     the top two candidates are within one fingerprint of each other - a
     shim that asks is a better demo than a shim that is always right.

     What a paste earns is the REQUEST ENVELOPE: the exact request that
     would go to a stock llama-server (the R.45/R.55 enum+margin protocol),
     marked DRAFT · NOT RUN. Never a result - a margin is a logprob
     difference, and nothing in a browser can compute one.
     ===================================================================== */
  var INTAKE = { text: "", verdict: null };

  function labelOf(name) {
    var EXACT = { roggentoo: "RogGentoo" };
    var ACR = { agv: "AGV", cnc: "CNC", vfd: "VFD", hpu: "HPU" };
    if (EXACT[name]) return EXACT[name];
    return String(name).split("_").map(function (w) {
      return ACR[w] || w.charAt(0).toUpperCase() + w.slice(1);
    }).join(" ");
  }

  // Fingerprints are derived from the committed renderer outputs, so detection
  // can never drift from what the wire bench actually displays.
  var FINGERPRINTS = [
    { mod: "modbus",     probes: ["MODBUS holding", "function 03", "scale factor"] },
    { mod: "opcua",      probes: ["ns=2;s=", "MonitoredItem", "status=Good"] },
    { mod: "prometheus", probes: ["# TYPE", "# HELP", "gauge"] },
    { mod: "sparkplug",  probes: ["spBv1.0/", "\"metrics\"", "DDATA"] },
    { mod: "syslog",     probes: ["<134>1 ", "RFC5424", "ioscan"] },
    { mod: "influx",     probes: ["line protocol", "INFLUXDB"] },
    { mod: "datadog",    probes: ["\"series\"", "\"metric\""] },
    { mod: "signal",     probes: ["channel=", "range=[", "slope_per_min"] },
  ];

  // Body-shape probes: real plant bytes do not carry our renderer's decorative
  // headers, so each modality is also recognisable by the SHAPE of its lines -
  // an Influx line-protocol row, an OPC UA value/status pair, a Modbus register
  // row. A header probe and a shape probe each count one hit.
  var SHAPES = {
    modbus:     /\b4[0-9]{4}\b\s+0x[0-9A-Fa-f]{4}/,
    opcua:      /value=\S+\s+status=\w+/,
    prometheus: /^[a-z_][a-z0-9_]*(\{[^}]*\})? [0-9.eE+-]+$/m,
    sparkplug:  /"(alias|timestamp)"\s*:\s*\d{10,}/,
    syslog:     /^<\d{2,3}>1 \d{4}-\d{2}-\d{2}T/m,
    influx:     /^\w[\w-]*,\S+=\S+ \S+=\S+ \d{15,19}$/m,
    datadog:    /"points"|"resources"\s*:/,
    signal:     /\b(longest_run|sd_tail|repeat_frac)=/,
  };

  function classify(text) {
    var t = text.trim();
    if (!t) return null;

    // wire blob: header fingerprints plus line-shape probes per modality
    var scores = FINGERPRINTS.map(function (f) {
      var hits = f.probes.filter(function (pr) { return t.indexOf(pr) >= 0; });
      var ev = hits.slice();
      if (SHAPES[f.mod] && SHAPES[f.mod].test(t)) {
        ev.push("line shape");
        hits = ev;
      }
      return { mod: f.mod, hits: ev.length, evidence: ev };
    }).sort(function (a, b) { return b.hits - a.hits; });
    var best = scores[0], second = scores[1];

    if (best.hits >= 2) {
      // thin evidence: if the runner-up is within one hit, the shim asks.
      if (second && second.hits >= best.hits - 1 && second.hits >= 2) {
        return { kind: "ambiguous", a: best, b: second };
      }
      var tag = (t.match(/[A-Z][A-Z0-9]*_[A-Z0-9_]+/) || [])[0] || null;
      var recognised = null;
      if (PATCH.scene && PATCH.scene.renders) {
        var norm = t.replace(/\s+/g, " ");
        for (var mo in PATCH.scene.renders) {
          if (PATCH.scene.renders[mo].replace(/\s+/g, " ") === norm) { recognised = mo; break; }
        }
      }
      return { kind: "blob", mod: best.mod, evidence: best.evidence, tag: tag, recognised: recognised };
    }

    // raw numbers: mostly numeric tokens. A REPEATED trailing unit token is
    // stripped before the ratio - "71.2 mm/s, 71.3 mm/s" must not be punished
    // for volunteering the unit this deck moralises about.
    var tokens = t.split(/[\s,;]+/).filter(Boolean);
    var unitGuess = null;
    var nonNum = tokens.filter(function (x) { return !/^-?\d+(\.\d+)?$/.test(x); });
    if (nonNum.length >= 2 && nonNum.every(function (x) { return x === nonNum[0]; })) {
      unitGuess = nonNum[0];
      tokens = tokens.filter(function (x) { return x !== unitGuess; });
    }
    var nums = tokens.filter(function (x) { return /^-?\d+(\.\d+)?$/.test(x); });
    if (nums.length >= 8 && nums.length / tokens.length >= 0.6) {
      return { kind: "numbers", n: nums.length, unit: unitGuess,
               samples: nums.slice(0, 96).map(Number) };
    }
    if (nums.length >= 3 && nums.length / Math.max(1, tokens.length) >= 0.6) {
      return { kind: "few-numbers", n: nums.length };
    }

    // a FLEET QUESTION: "how many faults across the fleet?" - fleet-scoped
    // stems, answered by the bench's fleet rollup (arithmetic over records)
    var FLEETQ = /(\bfleet\b|all (of )?(the )?sensors|every sensor|across (the )?(bench|fleet|sensors)|whole (bench|fleet)|how many (faults|sensors|channels|misses|catches|alarms))/i;
    if (FLEETQ.test(t)) {
      return { kind: "fleet-question", text: t };
    }

    // a READING QUESTION: "what does the temperature read now?" - detected
    // by documented stems (a question word + a reading word), never NLU. It
    // is answered by the bench from the recorded window - see benchReading.
    var QWORD = /(\bwhat\b|\bhow (much|high|low|hot|is)\b|right now|\blatest\b|\?)/i;
    var RWORD = /(\bread(ing|s)?\b|\bvalue\b|\bmean\b|\bmeasur|temperature|\btemp\b|pressure|vibration|current draw|\bamps?\b|\blevel\b|\bnow\b)/i;
    if (QWORD.test(t) && RWORD.test(t)) {
      return { kind: "question", text: t };
    }

    // scenario in words: a catalogue token lookup, not NLU. A partial asset
    // name ("pump" for centrifugal_pump) counts only when a FAULT word rides
    // along - "cavitating pump" is a scenario, a lone "pump" in chat is not.
    // Stems, not words: "cavitating" and "cavitation" share "cavitat".
    var FAULT_STEMS = ["cavitat", "misalign", "imbalanc", "bearing", "stuck",
      "drift", "leak", "wear", "running dry", "overheat", "vibrat", "fault",
      "fail", "broke", "trip", "alarm", "seiz"];
    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var low = t.toLowerCase();
    var faulty = FAULT_STEMS.filter(function (s) { return low.indexOf(s) >= 0; });
    var bestAsset = null, bestWords = [];
    for (var name in cat) {
      var nameWords = name.split("_").filter(function (w) { return w.length > 3; });
      var hit = nameWords.filter(function (w) { return low.indexOf(w) >= 0; });
      if (!hit.length) continue;
      var full = hit.length === nameWords.length;
      if ((full || faulty.length) && hit.length > bestWords.length) {
        bestAsset = name; bestWords = hit;
      }
    }
    if (bestAsset) {
      return { kind: "scenario-asset", asset: bestAsset, evidence: bestWords.concat(faulty) };
    }

    // Machine-shaped but unrecognised is NOT conversation: an engineer who
    // pasted real telemetry we failed to read must never be lectured about
    // corpus dreams. Heuristic: several similar-length lines, digit-heavy.
    var lines = t.split("\n").filter(function (l) { return l.trim(); });
    var digits = (t.match(/[0-9]/g) || []).length;
    if (lines.length >= 3 && digits / t.length > 0.15) {
      return { kind: "machine-unknown", lines: lines.length };
    }

    // anything else is conversation, and it never reaches a model
    return { kind: "talk" };
  }

  /* =====================================================================
     THE PROMPT - a terminal line wired to the LAST model in the chain.
     Typing feeds the TRANSLATION SHIM: every input is classified, and the
     honest ceiling is stated on the line itself - drafts only, no model
     runs in a browser. What a wire-blob or a number series earns is the
     DRAFT request envelope; small talk is answered by the interface, from
     the faceplate - it never reaches a model.
     ===================================================================== */
  function lastModel() {
    for (var i = PATCH.chain.length - 1; i >= 0; i--) {
      var f = familyById(PATCH.chain[i]);
      if (f) return f;
    }
    return null;
  }

  /* THE LIVE SEAM - the deck's first live surface, and the path to the rest.
     TODAY: chat and reading questions go to PING - the broker's concierge, a
     REAL model answering over the Tower relay (the same POST /concierge the
     CONSOLE deck's pingSend makes). Ping is the concierge, NOT a Wave model,
     and its card says so; the message hands it the recorded bench context as
     PROSE that names RogerAI's own product and bench (see benchContext -
     a machine-tagged dump reads to Ping's guardrail as off-topic and draws a
     decline, where the same numbers in prose draw a real answer), and the
     reply is displayed VERBATIM, declines included - a real reply is a real
     reply. The bench's recorded reading always rides beneath a question's
     answer, so the visitor gets the numbers whatever Ping says.
     NEXT (when the hosted wave band lands - asked in
     QUESTION-FOR-MODELS-AGENT-mesh-live-prompt.md):
       - enums (fault calls): the R.45/R.55 one-request-per-candidate grammar
         protocol via the relay, margins from the shim's logprob sums;
       - readings: a single request to the wave band, replacing Ping as the
         voice - the answerer-chain shape below is that seam.
     PROTOCOL kinds (wire blobs, number series) never go to chat: they earn
     the DRAFT envelope, and no model runs in a browser. */
  var PING_URL = "https://broker.rogerai.fm/concierge"; // the ONE cross-origin call this deck makes

  // Ping is a bounded radio-DJ concierge whose guardrail refuses anything that
  // does not read as RogerAI's own business. A machine-tagged data dump reads
  // to it as exactly that - off-topic - and it declines. The SAME reading,
  // written as prose that names RogerAI's own product and its own bench, gets
  // a real, useful answer (measured against the live endpoint: the tagged form
  // gets "that's outside my band, friend"; the prose form gets "Wave Pico is
  // saying that sensor looks steady enough..."). So the deck speaks to Ping the
  // way a person would, and the facts stay exactly the recorded ones.
  function benchContext() {
    var r = currentRecord();
    if (!r) return "The Wave Mesh deck has no sensor selected yet.";
    var w = r.window || {};
    var acts = [];
    stages().forEach(function (st) {
      if (st.kind === "pico") {
        acts.push(st.esc
          ? "RogerAI's Wave Pico was not sure enough to answer alone (confidence gap " +
            st.margin.toFixed(2) + ", it needs " + st.floor.toFixed(1) + "), so it asked for help"
          : 'RogerAI\'s Wave Pico answered "' + st.said + '" on its own (confidence gap ' +
            st.margin.toFixed(2) + ", above the " + st.floor.toFixed(1) + " it needs)");
      }
      if (st.kind === "nano") {
        acts.push('the bigger Wave Nano answered "' + st.verdict +
          '" (confidence gap ' + st.r.parent.margin.toFixed(2) + ")");
      }
      if (st.kind === "deadend") {
        acts.push("nobody bigger was in the chain, so the doubt went unheard");
      }
    });
    return "On the RogerAI Playbox, the Wave Mesh deck is replaying a recorded reading from " +
      "RogerAI's own eval bench: sensor " + (w.tag || r.node_id) + ", " + w.n + " samples, " +
      "range " + w.lo + " to " + w.hi + " " + (unitWordOf(r) || "(unit not stated in the wire)") +
      ", mean " + w.mean + ", trend " + w.slope_per_min + " per minute. " +
      (acts.length ? acts.join(", and ") + "." : "No Wave model is reading it yet.");
  }

  function liveAnswerer(v, ctx, text) {
    // PROTOCOL still never reaches chat as work: a wire blob or a number
    // series earns the DRAFT envelope. But Ping - the live concierge, not a
    // Wave model - may COMMENT on a paste's parsed summary, stacked above
    // the draft. Questions and talk go to Ping as before.
    var kinds = { talk: 1, question: 1, "fleet-question": 1, blob: 1, numbers: 1 };
    if (!v || !kinds[v.kind]) return null;
    var msg;
    if (v.kind === "blob" || v.kind === "numbers") {
      var rd = shimRead(text, v);
      var sum = rd.name + (rd.unit ? " (" + rd.unit + ")" : " (unit not stated)") + ", " + rd.mod;
      if (rd.feats) {
        sum += ", " + rd.feats.n + " points, min " + fmtN(rd.feats.lo) + " max " + fmtN(rd.feats.hi) +
          " mean " + fmtN(rd.feats.mean) +
          (rd.feats.repeat_frac === 1 ? ", all points identical" : "");
      } else {
        sum += ", values not decoded in-browser";
      }
      msg = benchContext() + " A listener has just pasted their own machine data into the " +
        "deck: " + sum + ". In one or two sentences, in your DJ voice, say what that paste " +
        "shows. Use only the numbers given - never invent a reading.";
    } else if (v.kind === "fleet-question") {
      var fl = benchFleet();
      msg = "On the RogerAI Playbox, the Wave Mesh deck just replayed RogerAI's whole " +
        "recorded eval fleet under the listener's current settings: " +
        (fl.lead || "no Wave model is reading it yet") +
        " A listener asks: \"" + text + "\". " +
        "Answer them in one or two sentences using only those numbers - never invent a reading.";
    } else {
      msg = benchContext() + " A listener watching this asks: \"" + text + "\". Answer them " +
        "in one or two sentences, in your DJ voice, using only what is above - never invent a " +
        "reading. These are recorded replays of RogerAI's bench, not a live plant.";
    }
    return fetch(PING_URL, {
      method: "POST", headers: { "Content-Type": "application/json" },
      credentials: "omit", cache: "no-store",
      body: JSON.stringify({ messages: [{ role: "user", content: msg }] }),
    }).then(function (r) { return r.ok ? r.json() : Promise.reject(r.status); })
      .then(function (d) {
        var rep = d && d.reply ? String(d.reply) : "";
        if (!rep) throw 0;
        return rep;
      });
  }

  function liveCtx() {
    return { record: currentRecord(), chain: PATCH.chain.slice(), floor: PATCH.floor };
  }

  // The bench's answer to a reading question: a template over the recorded
  // window of the live selection. Signed THE BENCH - a model never said this.
  function benchReading() {
    var r = currentRecord();
    if (!r) return { kind: "note", wired: "the bench", text: "No sensor is selected." };
    var w = r.window || {};
    var u = unitWordOf(r);
    var chainLine;
    var fam = lastModel();
    var hasReader = PATCH.chain.some(function (id) {
      var f = familyById(id); return f && f.status === "recorded";
    });
    if (!hasReader) {
      chainLine = "No model is seated on this channel - chain Wave Pico or Wave Nano to have it read.";
    } else {
      var sts = stages();
      var nano = null, pico = null;
      sts.forEach(function (st) { if (st.kind === "nano") nano = st; if (st.kind === "pico") pico = st; });
      var isFault = r.truth !== "none";
      if (nano) {
        chainLine = 'Dialed condition: ' + (CONDW[PATCH.cond] || PATCH.cond.toUpperCase()) +
          ' - the chain says " ' + nano.verdict + '" (margin ' + r.parent.margin.toFixed(2) + ").";
      } else if (pico) {
        chainLine = 'Dialed condition: ' + (CONDW[PATCH.cond] || PATCH.cond.toUpperCase()) +
          (pico.esc ? " - the Pico doubts this window (margin " + pico.margin.toFixed(2) + ")."
                    : ' - the chain says " ' + pico.said + '" (margin ' + pico.margin.toFixed(2) + ").");
      } else {
        chainLine = "";
      }
    }
    return {
      kind: "reading",
      who: "THE BENCH · from the recorded window",
      offAir: "a recount of the recorded window - a hosted Wave Nano will take this over; not yet on air",
      tag: w.tag || r.node_id,
      meanLine: "reads mean " + fmtN(w.mean) + (u ? " " + u : "") + " over the recorded window",
      detail: w.n + " samples · " + fmtN(w.lo) + " … " + fmtN(w.hi) + (u ? " " + u : "") +
        " · trend " + w.slope_per_min + "/min",
      chainLine: chainLine,
    };
  }

  // The bench's answer to a FLEET question: the rollup, as sentences.
  function benchFleet() {
    var f = deriveFleet();
    if (!f || f.none) {
      return { kind: "note", wired: "the bench",
               text: "Nobody is reading the fleet - chain Wave Pico or Wave Nano first, " +
                 "then ask again. The FLEET tab shows the whole bench once a reader is in." };
    }
    var t = f.totals;
    return { kind: "fleetread",
      who: "THE BENCH · the recorded fleet under your settings",
      offAir: "a recount of the 120 recorded records - a hosted Wave Nano will take this over; not yet on air",
      lead: "Across the recorded fleet at " + f.policy + ": " + t.n + " channels, " +
        t.faults + " recorded faults - " + t.caught + " caught, " + t.missed + " missed" +
        (t.deadEnd ? " (" + t.deadEnd + " escalation" + (t.deadEnd === 1 ? "" : "s") + " unheard)" : "") +
        ", " + t.falseAlarms + " false alarm" + (t.falseAlarms === 1 ? "" : "s") + ".",
      detail: t.escalated + " reads escalated" +
        (t.fixable ? " · " + t.fixable + " of the misses would escalate at a higher floor" : "") +
        " · see the FLEET tab for the per-sensor breakdown.",
    };
  }

  /* ---- WHAT THE SHIM READ (founder v13: "i enter prompts from like datadog
     etc but what is supposed to show me") - the comprehension pass over a
     PASTE. Where a dialect's values are cleanly extractable, the shim draws
     THEIR points and computes the same window features the model would read
     - arithmetic on the visitor's own bytes, computed just now, labelled as
     such. It is NOT a model output and it NEVER concludes a fault word:
     features yes, verdicts no. Dialects whose values are packed (modbus
     registers, syslog prose, opcua notifications, the pre-rendered feature
     summary) are recognised but honestly not decoded in-browser. */
  var SERIES_RX = {
    datadog: /"value":\s*(-?\d+(?:\.\d+)?)/g,
    sparkplug: /"(?:value|floatValue|doubleValue)":\s*(-?\d+(?:\.\d+)?)/g,
    prometheus: /^[a-zA-Z_:][\w:]*(?:\{[^}]*\})?\s+(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)(?:\s+\d{10,13})?\s*$/gm,
    influx: /=(-?\d+(?:\.\d+)?)(?=[,\s]|$)/gm,
  };
  function parseSeries(text, mod) {
    var rx = SERIES_RX[mod];
    if (!rx) return null;
    rx.lastIndex = 0;
    var vals = [], m;
    while ((m = rx.exec(text)) !== null) {
      vals.push(Number(m[1]));
      if (vals.length > 512) break;
    }
    return vals.length >= 2 ? vals : null;
  }
  function parseMeta(text, mod) {
    var name = null, unit = null, m;
    if (mod === "datadog" || mod === "sparkplug") {
      m = /"(?:metric|name)":\s*"([^"]+)"/.exec(text); if (m) name = m[1];
      m = /"unit":\s*"([^"]+)"/.exec(text); if (m) unit = m[1];
    } else if (mod === "prometheus") {
      m = /^([a-zA-Z_:][\w:]*)(?:\{|\s)/m.exec(text.replace(/^#.*$/gm, "").trim()); if (m) name = m[1];
    } else if (mod === "influx") {
      m = /^(\w[\w.]*)[,\s]/m.exec(text); if (m) name = m[1];
    }
    return { name: name, unit: unit };
  }
  function computeFeatures(vals) {
    var n = vals.length;
    var lo = Math.min.apply(null, vals), hi = Math.max.apply(null, vals);
    var mean = 0;
    vals.forEach(function (x) { mean += x; });
    mean /= n;
    // float summation can drift the mean a few ulp outside [lo,hi] on an
    // all-identical series; clamp for display sanity (arithmetic, not fudge)
    mean = Math.min(hi, Math.max(lo, mean));
    var sd = 0;
    vals.forEach(function (x) { sd += (x - mean) * (x - mean); });
    sd = Math.sqrt(sd / n);
    var repeats = 0, run = 1, longest = 1;
    for (var i = 1; i < n; i++) {
      if (vals[i] === vals[i - 1]) { repeats++; run++; if (run > longest) longest = run; }
      else run = 1;
    }
    return { n: n, lo: lo, hi: hi, mean: mean, sd: sd,
             repeat_frac: n > 1 ? repeats / (n - 1) : 0, longest_run: longest };
  }
  function shimRead(text, v) {
    if (v.kind === "numbers") {
      return { name: "your channel", unit: v.unit || null,
               vals: v.samples, feats: computeFeatures(v.samples), undecoded: false, mod: "raw numbers" };
    }
    var vals = parseSeries(text, v.mod);
    var meta = parseMeta(text, v.mod);
    return { name: meta.name || v.tag || "your channel", unit: meta.unit || null,
             vals: vals, feats: vals ? computeFeatures(vals) : null,
             undecoded: !vals, mod: v.mod };
  }
  // their points, drawn - USER PASTE ONLY; the recorded strip/sparklines
  // draw seriesOf() and never this
  function pastePath(vals, W, H, pad) {
    var lo = Math.min.apply(null, vals), hi = Math.max.apply(null, vals);
    var span = Math.max(hi - lo, Math.abs((hi + lo) / 2) * 0.005, 1e-9);
    var mid = (hi + lo) / 2, n = vals.length, d = "";
    for (var i = 0; i < n; i++) {
      var x = (n === 1 ? 0.5 : i / (n - 1)) * W;
      var y = pad + (1 - ((vals[i] - (mid - span / 2)) / span)) * (H - pad * 2);
      d += (i === 0 ? "M" : "L") + x.toFixed(1) + " " + y.toFixed(1);
    }
    return d;
  }

  function buildReply(text, v) {
    if (!v) v = classify(text);
    if (v.kind === "question") return benchReading();
    if (v.kind === "fleet-question") return benchFleet();
    var target = lastModel();
    var wired = target ? target.label : "the chain";
    if (v.kind === "blob" || v.kind === "numbers") {
      var src = v.kind === "blob"
        ? { modality: v.mod, recognised: v.recognised || null,
            channels: [{ name: v.tag || "your channel", unit: "" }], body: text }
        : { modality: "raw numbers", recognised: null, unit: v.unit || null,
            channels: [{ name: "your channel", unit: v.unit || "" }], body: text };
      return { kind: "draft", wired: wired, v: v,
               read: shimRead(text, v),
               unitNote: v.kind === "numbers" && !v.unit
                 ? "unit NOT STATED IN THE WIRE - a defaulted unit would be an invented fact"
                 : (v.unit ? "unit stated: " + v.unit + " (you stated it - it was not in the wire)" : null),
               recognised: v.recognised || null,
               envelope: envelopeFor(src) };
    }
    if (v.kind === "talk") {
      // the fallback voice when Ping is off air. Chat goes to Ping - a live
      // concierge, labelled as such; it never reaches a Wave model, whose
      // free-sampled answer would be a corpus dream, not a reply.
      return { kind: "talk", wired: wired,
               text: "Answered by this interface, from the faceplate - conversation never " +
                 "reaches a Wave model. Free-sampled, this input would produce a corpus " +
                 "dream, not an answer. Paste what your plant emits, or press a sample chip." };
    }
    if (v.kind === "scenario-asset") {
      return { kind: "scenario", wired: wired,
               text: "That reads like a scenario about " + labelOf(v.asset) + " (matched: " +
                 v.evidence.join(", ") + "). Words are never sent to a Wave model - this bench " +
                 "replays the recorded fleet; describing your own scene returns when the scene " +
                 "exporter grows." };
    }
    if (v.kind === "ambiguous") {
      return { kind: "note", wired: wired,
               text: "Unclear: " + v.a.mod + " (" + v.a.hits + " marks) vs " + v.b.mod + " (" +
                 v.b.hits + ") - the reader refuses to guess on thin evidence. That is the real " +
                 "system's behavior too. Paste more of the payload." };
    }
    if (v.kind === "few-numbers") {
      return { kind: "note", wired: wired,
               text: "Looks numeric - " + v.n + " sample" + (v.n === 1 ? "" : "s") + ". A window " +
                 "needs at least 8 samples to say anything about a signal; paste more of the series." };
    }
    return { kind: "note", wired: wired,
             text: "Machine-shaped, but not a dialect the reader recognizes - it reads eight " +
               "dialects and their line shapes, and this matched none well enough to wrap " +
               "honestly. Try including the header lines of the dump." };
  }

  var REPLY_SEQ = 0;
  function promptSend(text) {
    text = String(text || "").trim();
    if (!text) return null;
    var v = classify(text);
    var bench = buildReply(text, v); // the recorded recount, always built
    // the answerer chain: live first (Ping today, a wave band next), the
    // bench beneath or behind it
    var live = liveAnswerer(v, liveCtx(), text);
    if (live) {
      var token = ++REPLY_SEQ;
      PATCH.reply = { kind: "pingwait", token: token, bench: bench,
                      question: v.kind === "question" || v.kind === "fleet-question" };
      live.then(function (rep) {
        // context may have moved while Ping typed - a stale card never lands
        if (!PATCH.reply || PATCH.reply.token !== token) return;
        PATCH.reply = { kind: "ping", token: token, text: rep, bench: bench,
                        question: v.kind === "question" || v.kind === "fleet-question" };
        paintMonitor();
        react("Ping answered - live over the Tower relay.");
      }).catch(function () {
        if (!PATCH.reply || PATCH.reply.token !== token) return;
        bench.offAirNote = "Ping is off air - the bench answered from the recorded window.";
        PATCH.reply = bench;
        paintMonitor();
        react("Ping is off air - the bench answered from the recorded window.");
      });
    } else {
      PATCH.reply = bench;
    }
    var ta = $("wpPrompt");
    if (ta) ta.value = ""; // sent - the reply card carries what it earned
    paintMonitor();
    react(PATCH.reply.kind === "draft"
      ? "Draft request built for " + PATCH.reply.wired + " - NOT RUN; no model runs in a browser."
      : PATCH.reply.kind === "pingwait"
      ? "Asking Ping - a live answer over the Tower relay…"
      : PATCH.reply.kind === "reading"
      ? "The bench answered from the recorded window."
      : PATCH.reply.kind === "fleetread"
      ? "The bench answered from the recorded fleet."
      : "The reader answered from the faceplate.");
    return PATCH.reply;
  }

  function drawReply(rep) {
    // the live voice + the recorded recount travel as one stack
    if (rep.kind === "ping" || rep.kind === "pingwait") {
      var stack = el("section", "sn-stage sn-stage--reply sn-replystack");
      var phead = el("div", "sn-stage__head");
      var dot = el("span", "sn-livedot" + (rep.kind === "pingwait" ? " is-wait" : ""));
      dot.setAttribute("aria-hidden", "true");
      phead.appendChild(dot);
      phead.appendChild(el("b", null, "PING · LIVE over the Tower relay"));
      var px = el("button", "ws-resp__x");
      px.type = "button";
      px.setAttribute("aria-label", "Dismiss the prompt response");
      px.textContent = "×";
      px.addEventListener("click", function () { PATCH.reply = null; paintMonitor(); });
      phead.appendChild(px);
      stack.appendChild(phead);
      stack.appendChild(el("p", "sn-para sn-ping__text" + (rep.kind === "pingwait" ? " is-wait" : ""),
        rep.kind === "pingwait" ? "…asking Ping" : rep.text));
      var sub = el("p", "sn-sub sn-ping__sub",
        "Ping is the concierge answering live - not a Wave model; a hosted Wave Nano is the goal.");
      sub.title = "a real reply from a real endpoint, shown verbatim - declines included";
      stack.appendChild(sub);
      // the recorded recount always rides beneath the live voice: the
      // reading's numbers under a question, the DRAFT + comprehension under a
      // paste - whatever Ping says, the visitor gets the bench's answer
      if (rep.bench && rep.bench.kind !== "talk") {
        stack.appendChild(drawReply(rep.bench));
      }
      return stack;
    }
    var box = el("section", "sn-stage sn-stage--reply");
    var head = el("div", "sn-stage__head");
    head.appendChild(el("b", null, rep.who
      ? rep.who : "YOUR PROMPT → " + (rep.wired || "the chain").toUpperCase()));
    if (rep.kind === "draft") head.appendChild(el("span", "wp-tag wp-tag--draft", "DRAFT · NOT RUN"));
    var x = el("button", "ws-resp__x");
    x.type = "button";
    x.setAttribute("aria-label", "Dismiss the prompt response");
    x.textContent = "×";
    x.addEventListener("click", function () { PATCH.reply = null; paintMonitor(); });
    head.appendChild(x);
    box.appendChild(head);
    if (rep.kind === "reading" || rep.kind === "fleetread") {
      if (rep.kind === "reading") {
        var rd = el("p", "sn-read");
        rd.appendChild(el("b", null, rep.tag));
        rd.appendChild(el("span", null, " " + rep.meanLine));
        box.appendChild(rd);
      } else {
        box.appendChild(el("p", "sn-para", rep.lead));
      }
      box.appendChild(el("p", "sn-para", rep.detail));
      if (rep.chainLine) box.appendChild(el("p", "sn-para", rep.chainLine));
      if (rep.offAirNote) box.appendChild(el("p", "sn-sub sn-read__note", rep.offAirNote));
      var off = el("p", "sn-sub sn-read__offair", rep.offAir);
      off.title = "the answer above is a recount of recorded records - no model produced this sentence";
      box.appendChild(off);
      return box;
    }
    if (rep.kind === "draft") {
      if (rep.recognised) {
        box.appendChild(el("p", "sn-sub", "Recognised: byte-identical to the recorded pump scene's " +
          rep.recognised + " render. These are our recorded bytes - paste your own for the real test."));
      }
      // WHAT THE SHIM READ - the comprehension card leads; the envelope folds
      var rd2 = rep.read;
      if (rd2) {
        var read = el("div", "sn-shimread");
        read.appendChild(el("b", "sn-shimread__k", "WHAT THE READER SAW"));
        read.appendChild(el("p", "sn-para",
          rd2.name + (rd2.unit ? " · " + rd2.unit : " · unit not stated") +
          (rd2.feats ? " · " + rd2.feats.n + " points" : "") + " · " + rd2.mod));
        if (rd2.vals) {
          var tr = svg("svg", { class: "sn-shimread__trace", viewBox: "0 0 220 44",
            role: "img", "aria-label": "your pasted values, drawn - " + rd2.vals.length + " points" });
          tr.appendChild(svg("path", { class: "sn-shimread__line", d: pastePath(rd2.vals, 220, 44, 5) }));
          read.appendChild(tr);
          read.appendChild(el("p", "sn-sub", "your bytes, drawn - " + rd2.vals.length + " points"));
          var f = rd2.feats;
          var grid = el("p", "sn-shimread__feats",
            "mean " + fmtN(f.mean) + " · range " + fmtN(f.lo) + " … " + fmtN(f.hi) +
            " · sd " + fmtN(f.sd) + " · repeat_frac " + f.repeat_frac.toFixed(2) +
            " · longest_run " + f.longest_run + " of " + f.n);
          grid.title = "the same window features the model would read - no verdict is drawn from them here";
          read.appendChild(grid);
          read.appendChild(el("p", "sn-sub",
            "computed from your paste just now - not a recorded window, and not a prediction"));
        } else if (rd2.undecoded) {
          read.appendChild(el("p", "sn-sub",
            "the reader recognizes the dialect but does not decode its packed values in-browser - " +
            "the model reads the bytes; the request below carries them verbatim"));
        }
        box.appendChild(read);
      }
      if (rep.unitNote) box.appendChild(el("p", "sn-sub", rep.unitNote));
      var env = el("details", "sn-cert sn-envfold");
      env.appendChild(el("summary", null, "view the exact llama-server request"));
      env.appendChild(el("p", "sn-sub",
        "no model runs in a browser, and a margin is a logprob difference nothing here " +
        "can compute. This is the exact request that would go to a stock llama-server:"));
      env.appendChild(el("pre", "wp-wirebytes", rep.envelope));
      box.appendChild(env);
    } else {
      box.appendChild(el("p", "sn-para", rep.text));
    }
    return box;
  }

  function renderChips() {
    var host = $("wpChips");
    if (!host || host.childNodes.length) return;
    var sc = PATCH.scene;
    if (!sc || !sc.renders) return;
    host.appendChild(el("span", "wp-rail__k", "samples · recorded"));
    Object.keys(sc.renders).forEach(function (mo) {
      var b = el("button", "wp-dialect", mo);
      b.type = "button";
      b.title = "paste the recorded " + mo + " render of the pump scene into the prompt";
      b.addEventListener("click", function () {
        var ta = $("wpPrompt");
        if (!ta) return;
        ta.value = sc.renders[mo];
        promptSend(ta.value);
      });
      host.appendChild(b);
    });
  }

  function wirePrompt() {
    var form = $("wpPromptForm"), ta = $("wpPrompt");
    if (!form || !ta) return;
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      promptSend(ta.value);
    });
    ta.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        promptSend(ta.value);
      }
    });
    // paste-your-own-bytes works here too: the prompt is the drop target
    ta.addEventListener("dragover", function (e) { e.preventDefault(); });
    ta.addEventListener("drop", function (e) {
      e.preventDefault();
      var txt = e.dataTransfer.getData("text");
      if (txt) { ta.value = txt; promptSend(txt); return; }
      var f2 = e.dataTransfer.files && e.dataTransfer.files[0];
      if (f2 && f2.size < 1 << 20) {
        var rd = new FileReader();
        rd.onload = function () { ta.value = String(rd.result || ""); promptSend(ta.value); };
        rd.readAsText(f2);
      }
    });
    renderChips();
    paintWire();
  }

  function paintWire() {
    var lab = $("wpPromptWire");
    if (!lab) return;
    var target = lastModel();
    lab.textContent = "wired to " + (target ? target.label : "the chain") +
      " · chat answered live by Ping · drafts only - no model runs in a browser";
  }

  /* ---- the request envelope: what a paste earns -------------------------
     The exact request the R.45/R.55 protocol sends to a stock llama-server:
     one request per candidate with a locked grammar, logprob sums, EOG
     excluded, leading space, cache_prompt true. All of that is measured and
     documented; the one thing NOT invented here is the task frame text,
     which has not been exported - so its slot says so, like the digest. */
  var ENV_SHOW = 4000; // display cap for the envelope's body - the request
                       // itself would carry the paste verbatim, and says so
  function envelopeFor(src) {
    var body = src.body || "";
    var fullLen = body.length;
    var clipped = null;
    if (body.length > ENV_SHOW) {
      clipped = body.length - ENV_SHOW;
      body = body.slice(0, ENV_SHOW) +
        "\n… [display truncated - " + clipped.toLocaleString() +
        " more bytes of your paste would be sent verbatim]";
    }
    var isNumbers = src.modality === "raw numbers";
    var cat = (PATCH.catalog && PATCH.catalog.catalog) || {};
    var anyAsset = cat[Object.keys(cat)[0]] || {};
    var candidates = (anyAsset.sensor_faults && anyAsset.sensor_faults.length)
      ? anyAsset.sensor_faults : ["ok", "stuck", "dropout", "noisy", "drifting", "railed"];
    // The task frame used to be "pending export"; it is now exported verbatim
    // from the bench file, so the envelope shows the real thing.
    var frame = (PATCH.measured && PATCH.measured.task_frame) || null;
    var req = [
      "{",
      frame
        ? '  "prompt": "<the exported task frame below, then Input:, then your body>",'
        : '  "prompt": "<task frame — pending export — followed by the input body>",',
      '  "grammar": "root ::= \\" ' + candidates[1] + '\\"",',
      '  "n_predict": 16,',
      '  "cache_prompt": true',
      "}",
    ].join("\n");
    var notes = [
      "# POST ${LLAMA_SERVER}/completion - ONE REQUEST PER CANDIDATE, each with its",
      "# grammar locked to that candidate: " + candidates.map(function (c) { return '" ' + c + '"'; }).join(", "),
      "# cache_prompt shares one prompt eval across the candidates.",
      "# Request token logprobs per your llama-server build; the harness sums the",
      "# returned tokens' logprob fields, EXCLUDING the trailing end-of-generation",
      "# token. margin = best sum - runner-up sum.",
      "",
      req,
      "",
    ];
    if (isNumbers) {
      notes.push("# input body: the task frame wraps a FEATURES RENDER of your " +
        (src.channels.length ? "" : "") + "samples (mean, sd, slope," );
      notes.push("# longest_run, ...) - the in-browser features port is pending, so the render");
      notes.push("# is not shown here. Your raw samples, verbatim (" + fullLen + " chars):");
    } else {
      notes.push("# input body - your bytes, verbatim (" + fullLen + " chars):");
    }
    var frameBlock = frame
      ? "# task frame - exported verbatim from the bench file:\n" + frame + "\n\n"
      : "";
    return frameBlock + notes.join("\n") + "\n" + body;
  }




  /* =====================================================================
     THE MODE SWITCH - console / mesh / factory
     ===================================================================== */
  function showView(mode) {
    var consoleView = $("pgConsoleView"), mesh = $("pgMeshView"), factory = $("pgFactoryView");
    if (!consoleView || !mesh || !factory) return;
    var toMesh = mode === "mesh";
    var toFactory = mode === "factory";
    consoleView.hidden = toMesh || toFactory;
    mesh.hidden = !toMesh;
    factory.hidden = !toFactory;
    var hc = $("pgHeroConsole"), hm = $("pgHeroMesh"), hf = $("pgHeroFactory");
    var ht = $("pgHeroTitle"), hk = $("pgHeroKicker");
    if (hc) hc.hidden = toMesh || toFactory;
    if (hm) hm.hidden = !toMesh;
    if (hf) hf.hidden = !toFactory;
    if (ht) ht.textContent = toFactory ? "Keep the line running." :
      toMesh ? "Wire a machine to a model." : "Open the console.";
    if (hk) hk.textContent = toFactory ? "the factory game" : toMesh ? "the wave mesh" : "open the console";
    [["pgModeConsole", !toMesh && !toFactory], ["pgModeMesh", toMesh], ["pgModeFactory", toFactory]].forEach(function (pair) {
      var b = $(pair[0]);
      if (!b) return;
      b.setAttribute("aria-selected", pair[1] ? "true" : "false");
      b.setAttribute("tabindex", pair[1] ? "0" : "-1");
    });
    if (toMesh) maybeBoot();
    try { window.localStorage.setItem("pb.mode", mode); } catch (e) { /* private mode */ }
  }

  function wireModeSwitch() {
    var c = $("pgModeConsole"), m = $("pgModeMesh"), f = $("pgModeFactory");
    if (!c || !m || !f) return;
    c.addEventListener("click", function () { showView("console"); });
    m.addEventListener("click", function () { showView("mesh"); });
    f.addEventListener("click", function () { showView("factory"); });
    var tabs = [c, m, f];
    var modes = ["console", "mesh", "factory"];
    tabs.forEach(function (btn, index) {
      btn.addEventListener("keydown", function (e) {
        if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
        e.preventDefault();
        var step = e.key === "ArrowRight" ? 1 : -1;
        var nextIndex = (index + step + tabs.length) % tabs.length;
        var next = tabs[nextIndex];
        showView(modes[nextIndex]);
        next.focus();
      });
    });
    /* A deck link from the nav only changes the HASH when you are already on
       this page - no reload, so this function never ran again and the deck did
       not change (founder: "clicking on them doesn't always go to the right
       playbox"). Listening for hashchange makes every entry point behave the
       same whether you arrive from another page or from this one. */
    window.addEventListener("hashchange", function () {
      var h = (window.location.hash || "").replace("#", "").toLowerCase();
      if (h === "mesh" || h === "console" || h === "factory") showView(h);
    });

    var hash = (window.location.hash || "").replace("#", "").toLowerCase();
    if (hash === "mesh" || hash === "console" || hash === "factory") { showView(hash); return; }
    var saved = "console";
    try { saved = window.localStorage.getItem("pb.mode") || "console"; } catch (e) { /* ignore */ }
    showView(modes.indexOf(saved) >= 0 ? saved : "console");
  }

  /* =====================================================================
     BOOT
     ===================================================================== */
  function boot() {
    Promise.all([
      fetch("data/wave-catalog.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-measured.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-scene-recorded.json").then(function (r) { return r.ok ? r.json() : null; }),
      fetch("data/wave-windows.json").then(function (r) { return r.ok ? r.json() : null; }),
    ]).then(function (res) {
      PATCH.catalog = res[0]; PATCH.measured = res[1]; PATCH.scene = res[2];
      PATCH.windows = res[3] ? res[3].windows : null;
      if (!PATCH.catalog || !PATCH.measured) { fail(); return; }
      var prov = $("wpProv");
      if (prov) {
        /* This said "Recorded fleet: 800 items ... every reading here is a
           recount of these records" - but the deck holds TWO populations and
           the readings on screen come from the smaller one. The 120 replayed
           records drive the monitor, the fleet tab and the score strip; the
           800-item escalation sweep drives the chain economics. Naming one as
           the source of everything made the same knob print two answers
           (28.5% escalate from the sweep, 35/120 = 29.2% from the records).
           Both are real; the banner says which is which now. */
        prov.textContent = "Recorded: " + PATCH.measured.records.length +
          " replayed records (every reading on this deck) and a " +
          PATCH.measured.escalation.n + "-item escalation sweep (the chain economics) of " +
          shortName(PATCH.measured.escalation.child) + " under " +
          shortName(PATCH.measured.escalation.parent) + " on " +
          shortName(PATCH.measured.escalation.bench) + " · " + PATCH.measured._provenance.suite +
          " · every reading here is a recount of these records";
      }
      buildTypes();
      // Start with detection and escalation. Shift mode gives Micro a reason
      // to enter: it unlocks site-triage training after an incident is dealt.
      PATCH.chain = ["pico", "nano", "micro"];
      // The old six-card guided tour made the bench feel like required
      // training before play. The welcome challenge now teaches by doing;
      // the tour code remains available to old links without auto-launching.
      PATCH.tour = -1;
      derive();
      render();
      react("The recorded signal workbench is ready. Pick a sensor or change a condition.");
      wireTilt();
      document.addEventListener("keydown", onKey);
      wirePrompt();
      wireGlassScroll();
      wireFront();
    }).catch(fail);
  }

  function onKey(e) {
    if (e.key !== "Escape") return;
    if (PATCH.tour >= 0) { tourEnd(); return; }
    if (PATCH.menuFor != null) {
      PATCH.menuFor = null;
      render();
      var add = document.querySelector(".syn-plus");
      if (add) add.focus();
      return;
    }
    if (PATCH.whyOpen != null) {
      var openKey = PATCH.whyOpen;
      PATCH.whyOpen = null;
      renderWhys();
      var chip = document.querySelector('.sn-whys .sn-why[data-why="' + openKey + '"] > summary');
      if (chip) chip.focus();
      return;
    }
    if (PATCH.reply) {
      PATCH.reply = null;
      paintMonitor();
      var ta = $("wpPrompt");
      if (ta) ta.focus();
    }
  }

  function fail() {
    var s = $("wpMonitor");
    if (s) s.appendChild(el("p", "wp-note", "The bench data did not load. Nothing here is live; reload to try again."));
  }

  function maybeBoot() {
    var v = $("pgMeshView");
    if (v && !v.hidden && !PATCH.booted && $("wpMonitor")) { PATCH.booted = true; boot(); }
  }

  // Pure-function hook so tests can EXECUTE the classifier and the derivation.
  if (typeof window !== "undefined") {
    window.__wavePatchTest = {
      classify: classify,
      setScene: function (sc, cat) { PATCH.scene = sc; PATCH.catalog = cat; },
      setMeasured: function (m) { PATCH.measured = m; },
      buildTypes: buildTypes,
      buildScopes: buildScopes,
      scopeFor: scopeFor,
      tallyOver: tallyOver,
      worstMachines: worstMachines,
      caseLens: caseLens,
      selectType: selectType,
      chainAdd: chainAdd,
      laneRead: laneRead,
      finalAnswer: finalAnswer,
      modelLimitLesson: modelLimitLesson,
      tierCaseBrief: tierCaseBrief,
      stageResponse: stageResponse,
      stages: stages,
      derive: derive,
      envelopeFor: envelopeFor,
      promptSend: promptSend,
      liveAnswerer: liveAnswerer,
      setWindows: function (w) { PATCH.windows = w; },
      seriesOf: seriesOf,
      scaleOf: scaleOf,
      okAnchorRel: okAnchorRel,
      buildReply: buildReply,
      benchContext: benchContext,
      deriveFleet: deriveFleet,
      incidentCandidates: incidentCandidates,
      missionDeckTally: missionDeckTally,
      caseLesson: caseLesson,
      guidedCard: guidedCard,
      gameBeat: gameBeat,
      startMystery: startMystery,
      openSandbox: openSandbox,
      shipBatch: shipBatch,
      drawIncident: drawIncident,
      missionPlan: missionPlan,
      incidentMove: incidentMove,
      missionChooseMove: missionChooseMove,
      missionStep: missionStep,
      missionReady: missionReady,
      verifyMission: verifyMission,
      beginSelectedIncident: beginSelectedIncident,
      fieldApply: fieldApply,
      fieldProgress: fieldProgress,
      chainRemove: chainRemove,
      watchingLabel: watchingLabel,
      benchFleet: benchFleet,
      followSuppressed: followSuppressed,
      parseSeries: parseSeries,
      computeFeatures: computeFeatures,
      shimRead: shimRead,
      runOf: runOf,
      state: PATCH,
      family: FAMILY,
      bands: BANDS,
      detents: DETENTS,
      glossary: GLOSSARY,
      playbooks: PLAYBOOKS,
      fieldRigs: FIELD_RIGS,
    };
  }

  function start() { wireModeSwitch(); maybeBoot(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
