/* =====================================================================
   WAVEWORKS - the Playbox factory game

   This is deliberately separate from wave-patch.js. Wave Mesh is the
   measured engineering workbench; Waveworks is a small factory/tycoon game.
   The buildings, bolts, crates, production bonuses and repair loop are game
   simulation. Signal events come from the committed Wave replay and retain
   their recorded truth + Pico/Nano results. No model runs in the browser.
   ===================================================================== */
(function () {
  "use strict";

  var FLOOR = 1.5;
  var PRICE = { former: 20, packer: 25, pico: 40, nano: 70, micro: 110, giga: 170 };
  var LABEL = {
    none: "healthy", drifting: "drifting", dropout: "dropping out",
    noisy: "noisy", railed: "railed", stuck: "stuck",
  };

  function freshState() {
    return {
      credits: 90,
      earned: 0,
      shipped: 0,
      goal: 6,
      stage: 1,
      lines: 1,
      view: "floor",
      selected: "former",
      run: 0,
      busy: false,
      held: null,
      last: null,
      records: [],
      loading: true,
      error: "",
      built: { former: false, packer: false, pico: false, nano: false, micro: false, giga: false },
      manual: 0,
      streak: 0,
      bestStreak: 0,
      log: ["Factory doors open. Ninety bolts are waiting on the bench."],
    };
  }

  var GAME = freshState();

  function pick(list, index) {
    return list.length ? list[index % list.length] : null;
  }

  /* The opening is authored like a good first level, but every signal card is
     a real record. Healthy -> Pico-sized fault -> Nano save -> hard miss gives
     the player a readable difficulty curve without a model-trivia question. */
  function pickRecord(records, run) {
    var healthy = records.filter(function (r) { return r.truth === "none"; });
    var pico = records.filter(function (r) {
      return r.truth !== "none" && r.child && r.child.prediction === r.truth && r.child.margin >= FLOOR;
    });
    var nano = records.filter(function (r) {
      return r.truth !== "none" && r.child && r.parent && r.child.margin < FLOOR &&
        r.child.prediction !== r.truth && r.parent.prediction === r.truth;
    });
    var hard = records.filter(function (r) {
      return r.truth !== "none" && r.child && r.parent &&
        r.child.prediction !== r.truth && r.parent.prediction !== r.truth;
    });
    var allFaults = records.filter(function (r) { return r.truth !== "none"; });
    var lanes = [healthy, pico, nano, healthy, hard, nano, allFaults];
    var lane = lanes[run % lanes.length];
    return pick(lane.length ? lane : records, Math.floor(run / lanes.length) + run * 7);
  }

  function bonusFor(state) {
    return (state.built.micro ? 8 : 0) + (state.built.giga ? 15 : 0);
  }

  function cleanPayFor(state) {
    return (26 + bonusFor(state)) * (state.lines || 1);
  }

  function nextAutomation(state) {
    if (!state.built.pico) return { name: "PICO", copy: "confident machine faults auto-route", pay: "+4 per line" };
    if (!state.built.nano) return { name: "NANO", copy: "doubtful Pico reads get a second look", pay: "+8 per line" };
    if (!state.built.micro) return { name: "MICRO", copy: "site flow improves across every line", pay: "+8 per line" };
    if (!state.built.giga) return { name: "GIGA", copy: "the two-line plant earns a balance bonus", pay: "+15 per line" };
    return { name: "FULL MESH", copy: "every available automation layer is online", pay: "keep optimizing" };
  }

  /* Pure resolution rule, exported below for executable tests. A hard model
     miss never becomes a good crate by magic: the dock holds it for the
     player's manual rework. Micro/Giga only improve simulated site flow; this
     game never fabricates an inference for tiers absent from the replay. */
  function resolveBatch(state, record) {
    if (!record) return { kind: "error", shipped: false, reward: 0, units: 0,
      title: "No signal card", text: "The recorded signal deck did not load." };
    var units = state.lines || 1;
    var base = 26 + bonusFor(state);
    if (record.truth === "none") {
      return { kind: "clear", shipped: true, reward: base * units, units: units,
        title: "Clean run", text: "The parts crossed the line and filled a crate." };
    }
    if (!state.built.pico) {
      return { kind: "hold", reason: "no-scanner", shipped: false, reward: 0, units: units,
        title: "Dock hold", text: "A bad signal reached final inspection. Rework it now, or install Pico to watch the machine automatically." };
    }
    if (record.child.prediction === record.truth && record.child.margin >= FLOOR) {
      return { kind: "pico", shipped: true, reward: (base + 4) * units, units: units,
        title: "Pico caught it", text: "The on-machine scanner kicked the part through rework before packout." };
    }
    if (record.child.margin < FLOOR && state.built.nano && record.parent && record.parent.prediction === record.truth) {
      return { kind: "nano", shipped: true, reward: (base + 8) * units, units: units,
        title: "Nano broke the tie", text: "Pico asked the line gateway; Nano identified the condition and the part took the rework lane." };
    }
    if (record.child.margin < FLOOR && !state.built.nano) {
      return { kind: "hold", reason: "needs-gateway", shipped: false, reward: 0, units: units,
        title: "Pico is not sure", text: "The part is safe in the hold bay. Manual rework keeps moving; Nano can automate this handoff." };
    }
    return { kind: "hold", reason: "model-miss", shipped: false, reward: 0, units: units,
      title: "Dock check caught a miss", text: "The recorded models did not identify this fault. The part waits for manual rework; no tier is credited with an answer it never gave." };
  }

  function availability(id, state) {
    if (state.built[id]) return { ok: false, reason: "INSTALLED" };
    if (id === "packer" && !state.built.former) return { ok: false, reason: "BUILD THE SHAPER FIRST" };
    if (id === "pico" && !state.built.former) return { ok: false, reason: "NEEDS A MACHINE TO WATCH" };
    if (id === "nano" && !state.built.pico) return { ok: false, reason: "NEEDS PICO ON THE LINE" };
    if (id === "micro" && (!state.built.nano || state.shipped < 3)) return { ok: false, reason: "NEEDS NANO + 3 CRATES" };
    if (id === "giga" && (!state.built.micro || state.stage < 2)) return { ok: false, reason: "UNLOCKS WITH LINE 2 + MICRO" };
    if (state.credits < PRICE[id]) return { ok: false, reason: String(PRICE[id] - state.credits) + " MORE BOLTS" };
    return { ok: true, reason: "BUY · " + PRICE[id] };
  }

  function humanTag(record) {
    var tag = record && record.window && record.window.tag ? record.window.tag : "SENSOR";
    if (/^AI_\d+$/.test(tag)) return "GENERAL SENSOR " + tag.replace("AI_", "");
    return tag.replace(/^[A-Z]+\d+_/, "").replace(/_/g, " ");
  }

  function addLog(copy) {
    GAME.log.unshift(copy);
    GAME.log = GAME.log.slice(0, 5);
  }

  function buy(id) {
    var a = availability(id, GAME);
    if (!a.ok || GAME.busy || GAME.held) return false;
    GAME.credits -= PRICE[id];
    GAME.built[id] = true;
    GAME.selected = id;
    GAME.last = { kind: "build", title: machineName(id) + " online", text: machinePayoff(id) };
    addLog(machineName(id) + " built for " + PRICE[id] + " bolts.");
    render();
    return true;
  }

  function canRun() {
    return GAME.built.former && GAME.built.packer && !GAME.busy && !GAME.held && !GAME.loading && !GAME.error;
  }

  function runBatch() {
    if (!canRun()) return false;
    var record = pickRecord(GAME.records, GAME.run);
    GAME.busy = true;
    GAME.last = { kind: "running", title: "Parts in motion", text: "Watch them cross the floor." };
    render();
    window.setTimeout(function () {
      var result = resolveBatch(GAME, record);
      result.record = record;
      GAME.run++;
      GAME.busy = false;
      GAME.last = result;
      if (result.shipped) {
        GAME.shipped += result.units;
        GAME.credits += result.reward;
        GAME.earned += result.reward;
        GAME.streak++;
        GAME.bestStreak = Math.max(GAME.bestStreak, GAME.streak);
        addLog(result.title + ". " + result.units + " crate" + (result.units === 1 ? "" : "s") + " shipped; +" + result.reward + " bolts.");
      } else if (result.kind === "hold") {
        GAME.streak = 0;
        GAME.held = result;
        GAME.selected = result.reason === "needs-gateway" ? "nano" :
          result.reason === "no-scanner" ? "pico" : "dock";
        addLog(result.title + ". The line is waiting for you, not a timer.");
      } else {
        GAME.error = result.text;
      }
      render();
    }, 1050);
    return true;
  }

  function manualRework() {
    if (!GAME.held || GAME.busy) return false;
    var units = GAME.held.units || GAME.lines;
    var record = GAME.held.record;
    var reward = 12 * units;
    GAME.manual++;
    GAME.selected = "dock";
    GAME.shipped += units;
    GAME.credits += reward;
    GAME.earned += reward;
    GAME.streak = 0;
    GAME.last = { kind: "manual", title: "Crew saved the batch",
      text: "The held part was inspected and reworked. Automation is optional; it earns speed, not permission to keep playing.",
      record: record, units: units, reward: reward };
    GAME.held = null;
    addLog("Crew reworked " + units + " held crate" + (units === 1 ? "" : "s") + "; +" + reward + " bolts.");
    render();
    return true;
  }

  function expandFactory() {
    if (GAME.stage !== 1 || GAME.shipped < GAME.goal || GAME.busy || GAME.held) return false;
    GAME.stage = 2;
    GAME.lines = 2;
    GAME.view = "floor";
    GAME.goal = 14;
    GAME.credits += 80;
    GAME.last = { kind: "expand", title: "Line two unlocked",
      text: "Every run now fills two crates. The new plant control pad is open too." };
    addLog("Contract 01 complete. Line two opened with an 80-bolt expansion grant.");
    render();
    return true;
  }

  function resetGame() {
    GAME = freshState();
    render();
    loadRecords();
  }

  function el(tag, cls, copy) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    if (copy != null) node.textContent = copy;
    return node;
  }

  function actionButton(copy, cls, fn, disabled) {
    var b = el("button", cls, copy);
    b.type = "button";
    b.disabled = !!disabled;
    if (fn) b.addEventListener("click", fn);
    return b;
  }

  function machineName(id) {
    return ({ former: "PART SHAPER", packer: "CRATE PACKER", pico: "WAVE PICO SCANNER",
      nano: "WAVE NANO GATEWAY", micro: "WAVE MICRO CONTROL ROOM", giga: "WAVE GIGA PLANT BRAIN" })[id] || id.toUpperCase();
  }

  function machinePayoff(id) {
    return ({ former: "Turns raw blanks into pump parts.", packer: "Boxes finished parts so they earn bolts.",
      pico: "Watches one machine and auto-routes confident local faults.",
      nano: "Answers Pico's doubtful reads at the line gateway.",
      micro: "Improves the game line's site flow bonus; no replayed Micro answer is invented.",
      giga: "Balances both game lines for a larger plant bonus." })[id] || "";
  }

  function machine(id, title, subtitle, built, buildable) {
    var card = el("div", "wf-machine wf-machine--" + id + (built ? " is-built" : " is-pad"));
    var art = el("span", "wf-machine__art", "");
    art.appendChild(el("i")); art.appendChild(el("i")); art.appendChild(el("i"));
    card.appendChild(art);
    card.appendChild(el("b", null, built ? title : "EMPTY PAD"));
    card.appendChild(el("span", null, built ? subtitle : title));
    if (!built && buildable) {
      var a = availability(id, GAME);
      card.appendChild(actionButton(a.reason, "wf-machine__buy", function () { buy(id); }, !a.ok));
    } else if (built) {
      card.appendChild(el("em", "wf-machine__online", "RUNNING"));
    }
    return card;
  }

  function belt() {
    var b = el("span", "wf-belt");
    b.appendChild(el("i")); b.appendChild(el("i")); b.appendChild(el("i"));
    return b;
  }

  function renderHeader(root) {
    var header = el("header", "wf-head");
    var brand = el("div", "wf-brand");
    brand.appendChild(el("span", null, "WAVEWORKS · POCKET FACTORY"));
    brand.appendChild(el("h2", null, GAME.stage === 1 ? "Make six pump skids." : "Double the line. Fill fourteen crates."));
    brand.appendChild(el("p", null, "Buy the glowing pads, run a batch, and spend what you earn. If the line jams, fix it and keep going."));
    header.appendChild(brand);
    var hud = el("div", "wf-hud");
    [["BOLTS", GAME.credits], ["CRATES", GAME.shipped + " / " + GAME.goal], ["LINES", GAME.lines]].forEach(function (item) {
      var stat = el("span", "wf-hud__stat"); stat.appendChild(el("b", null, String(item[1]))); stat.appendChild(el("i", null, item[0])); hud.appendChild(stat);
    });
    hud.appendChild(actionButton("NEW FLOOR", "wf-hud__reset", resetGame, GAME.busy));
    header.appendChild(hud);
    root.appendChild(header);
  }

  function renderLine(root, second) {
    var line = el("div", "wf-line" + (second ? " wf-line--second" : "") + (GAME.busy ? " is-running" : "") + (GAME.held ? " is-held" : ""));
    line.setAttribute("aria-label", second ? "Second production line" : "First production line");
    line.appendChild(machine("hopper", "PART HOPPER", "feeds raw blanks", true, false));
    line.appendChild(belt());
    line.appendChild(machine("former", "PART SHAPER", "forms the pump body", GAME.built.former, true));
    line.appendChild(belt());
    line.appendChild(machine("pico", "PICO SCANNER", "watches this machine", GAME.built.pico, true));
    line.appendChild(belt());
    line.appendChild(machine("packer", "CRATE PACKER", "boxes good parts", GAME.built.packer, true));
    line.appendChild(belt());
    line.appendChild(machine("dock", "SHIPPING DOCK", GAME.held ? "one batch on hold" : "waiting for crates", true, false));
    if (GAME.busy) line.appendChild(el("span", "wf-part", "◆"));
    if (GAME.held) line.appendChild(el("span", "wf-part wf-part--held", "!"));
    root.appendChild(line);
  }

  function nextAction() {
    if (!GAME.built.former) return { copy: "BUILD THE PART SHAPER · 20", fn: function () { buy("former"); }, disabled: GAME.credits < PRICE.former };
    if (!GAME.built.packer) return { copy: "BUILD THE CRATE PACKER · 25", fn: function () { buy("packer"); }, disabled: GAME.credits < PRICE.packer };
    if (GAME.held) return { copy: "REWORK THE HELD BATCH →", fn: manualRework, disabled: false };
    if (GAME.stage === 1 && GAME.shipped >= GAME.goal) return { copy: "OPEN LINE TWO · CLAIM 80 BOLTS →", fn: expandFactory, disabled: false };
    if (GAME.stage === 2 && GAME.shipped >= GAME.goal) return { copy: "PLANT COMPLETE · RUN FOR FUN", fn: runBatch, disabled: false };
    return { copy: GAME.busy ? "PARTS ARE MOVING…" : "RUN A BATCH · CRATE VALUE +" + cleanPayFor(GAME) + " →", fn: runBatch, disabled: !canRun() };
  }

  function renderFloor(root) {
    var world = el("section", "wf-world");
    var skyline = el("div", "wf-skyline");
    skyline.appendChild(el("span", null, "YOUR FACTORY"));
    skyline.appendChild(el("i")); skyline.appendChild(el("i")); skyline.appendChild(el("i"));
    world.appendChild(skyline);

    var control = el("div", "wf-controlrow");
    control.appendChild(machine("nano", "NANO GATEWAY", "handles Pico's doubtful reads", GAME.built.nano, true));
    control.appendChild(machine("micro", "MICRO CONTROL ROOM", "site flow bonus · +8 bolts per line", GAME.built.micro, true));
    control.appendChild(machine("giga", "GIGA PLANT BRAIN", "two-line balance · +15 bolts per line", GAME.built.giga, true));
    world.appendChild(control);

    var floor = el("div", "wf-floor");
    renderLine(floor, false);
    if (GAME.stage === 2) renderLine(floor, true);
    else {
      var ghost = el("div", "wf-expansion");
      ghost.appendChild(el("b", null, "LINE 2"));
      ghost.appendChild(el("span", null, "Ship 6 crates to knock down this wall."));
      floor.appendChild(ghost);
    }
    world.appendChild(floor);

    var action = nextAction();
    var dock = el("div", "wf-actiondock");
    var prompt = el("div", "wf-foreman");
    prompt.appendChild(el("span", "wf-foreman__face", GAME.held ? "!" : GAME.busy ? "›" : "☺"));
    var words = el("span");
    words.appendChild(el("b", null, GAME.held ? GAME.held.title : GAME.busy ? "There they go!" : "What should we build next?"));
    words.appendChild(el("i", null, GAME.held ? GAME.held.text : GAME.busy ? "One click started this run. Nothing advances behind your back." : "The brightest button is enough; the rest is yours to explore."));
    prompt.appendChild(words);
    dock.appendChild(prompt);
    dock.appendChild(actionButton(action.copy, "wf-primary", action.fn, action.disabled));
    world.appendChild(dock);
    root.appendChild(world);
  }

  function renderShop(root) {
    var shop = el("section", "wf-shop");
    var head = el("div", "wf-shop__head");
    head.appendChild(el("span", null, "BUILD SHELF"));
    head.appendChild(el("p", null, "No pop quiz. Buy anything that is unlocked and watch what it changes on the floor."));
    shop.appendChild(head);
    var grid = el("div", "wf-shop__grid");
    [
      ["former", "SHAPER", "makes parts"], ["packer", "PACKER", "earns bolts"],
      ["pico", "PICO", "catches confident faults at one machine"],
      ["nano", "NANO", "resolves doubtful Pico reads at the gateway"],
      ["micro", "MICRO", "improves site flow after three crates"],
      ["giga", "GIGA", "balances two lines after expansion"],
    ].forEach(function (item) {
      var id = item[0], a = availability(id, GAME);
      var card = el("button", "wf-shopcard wf-shopcard--" + id + (GAME.built[id] ? " is-owned" : ""));
      card.type = "button"; card.disabled = !a.ok || GAME.busy || !!GAME.held;
      card.appendChild(el("span", null, item[1]));
      card.appendChild(el("b", null, GAME.built[id] ? "INSTALLED" : a.reason));
      card.appendChild(el("i", null, item[2]));
      card.addEventListener("click", function () { buy(id); });
      grid.appendChild(card);
    });
    shop.appendChild(grid);
    root.appendChild(shop);
  }

  function renderEvent(root) {
    var card = el("section", "wf-event " + (GAME.last ? "is-" + GAME.last.kind : "is-new"));
    var copy = el("div", "wf-event__copy");
    copy.appendChild(el("span", null, GAME.last ? "LAST RUN" : "ORDER BOARD"));
    copy.appendChild(el("h3", null, GAME.last ? GAME.last.title : "A small factory with room to grow."));
    copy.appendChild(el("p", null, GAME.last ? GAME.last.text : "Build the first two machines. Your parts will move across the floor as soon as you press RUN."));
    card.appendChild(copy);
    if (GAME.last && GAME.last.record) {
      var r = GAME.last.record;
      var signal = el("details", "wf-signal");
      signal.appendChild(el("summary", null, "OPEN THE SIGNAL CARD · " + humanTag(r)));
      var facts = el("div", "wf-signal__facts");
      [["RECORDED TRUTH", LABEL[r.truth] || r.truth],
       ["PICO RECORDED", r.child ? r.child.prediction + " · margin " + r.child.margin : "not recorded"],
       ["NANO RECORDED", r.parent ? r.parent.prediction + " · margin " + r.parent.margin : "not recorded"]].forEach(function (fact) {
        var row = el("span"); row.appendChild(el("b", null, fact[0])); row.appendChild(el("i", null, fact[1])); facts.appendChild(row);
      });
      signal.appendChild(facts);
      signal.appendChild(el("p", null, "The signal and Pico/Nano fields above come from the committed replay. The factory, crates, bolts, routing and bonuses are game simulation."));
      card.appendChild(signal);
    } else {
      card.appendChild(el("p", "wf-event__hint", "TIP · The first clean crate pays for your next idea."));
    }
    root.appendChild(card);
  }

  function renderLog(root) {
    var log = el("details", "wf-log");
    log.appendChild(el("summary", null, "FACTORY RADIO · " + GAME.log.length + " MESSAGES"));
    var list = el("ol");
    GAME.log.forEach(function (copy) { list.appendChild(el("li", null, copy)); });
    log.appendChild(list);
    root.appendChild(log);
  }

  /* ===================================================================
     V2 GAME SURFACE

     The first pass looked like a responsive documentation dashboard. This
     surface instead borrows the grammar of approachable factory games: one
     objective, a top-down floor, glowing construction pads, a bottom build
     bar, and a contextual inspector. The automation ladder gets its own
     network view so it never obscures the material path.
     =================================================================== */
  var MACHINE = {
    hopper: { name: "PART HOPPER", short: "HOPPER", where: "LINE INPUT", job: "Feeds raw blanks onto the belt.", effect: "Always available." },
    former: { name: "PART SHAPER", short: "SHAPER", where: "PRODUCTION CELL", job: "Forms each blank into a pump body.", effect: "Required before a batch can run." },
    pico: { name: "WAVE PICO", short: "PICO", where: "ON THE MACHINE", job: "Watches the shaper's sensor one window at a time.", effect: "Automatically reroutes faults Pico confidently recognized in the recorded replay." },
    packer: { name: "CRATE PACKER", short: "PACKER", where: "END OF LINE", job: "Packs good parts into paying crates.", effect: "Required before a batch can ship." },
    dock: { name: "SHIPPING DOCK", short: "DOCK", where: "FINAL CHECK", job: "Holds questionable parts and ships good crates.", effect: "A hard model miss remains here for manual rework." },
    nano: { name: "WAVE NANO", short: "NANO", where: "LINE GATEWAY", job: "Listens when Pico is not sure.", effect: "Automates doubtful handoffs only when Nano's recorded answer matches the replay truth." },
    micro: { name: "WAVE MICRO", short: "MICRO", where: "SITE CONTROL", job: "Watches the flow across the site.", effect: "+8 game bolts per active line. No Micro inference is fabricated." },
    giga: { name: "WAVE GIGA", short: "GIGA", where: "PLANT CONTROL", job: "Balances the two-line plant.", effect: "+15 game bolts per active line after expansion. No Giga inference is fabricated." },
  };

  function selectMachine(id) {
    GAME.selected = id;
    render();
  }

  function switchFactoryView(view) {
    GAME.view = view;
    render();
  }

  function v2Header(root) {
    var header = el("header", "wf2-head");
    var brand = el("div", "wf2-brand");
    brand.appendChild(el("span", null, "WAVEWORKS"));
    brand.appendChild(el("b", null, GAME.stage === 1 ? "PUMP SKID WORKS" : "PUMP SKID PLANT"));
    header.appendChild(brand);
    var nav = el("nav", "wf2-nav");
    nav.setAttribute("aria-label", "Factory game views");
    [["floor", "FACTORY FLOOR"], ["network", "MODEL NETWORK"]].forEach(function (item) {
      var b = actionButton(item[1], "wf2-nav__button" + (GAME.view === item[0] ? " is-on" : ""), function () { switchFactoryView(item[0]); }, false);
      b.setAttribute("aria-pressed", GAME.view === item[0] ? "true" : "false");
      nav.appendChild(b);
    });
    header.appendChild(nav);
    var wallet = el("div", "wf2-wallet");
    [["BOLTS", GAME.credits], ["EARNED", GAME.earned], ["CRATES", GAME.shipped + "/" + GAME.goal], ["STREAK", GAME.streak]].forEach(function (item) {
      var stat = el("span"); stat.appendChild(el("b", null, String(item[1]))); stat.appendChild(el("i", null, item[0])); wallet.appendChild(stat);
    });
    wallet.appendChild(actionButton("↻", "wf2-reset", resetGame, GAME.busy));
    header.appendChild(wallet);
    root.appendChild(header);
  }

  function objective() {
    if (!GAME.built.former) return { step: 1, title: "Build the shaper", text: "Click the glowing SHAPER pad on the factory floor.", progress: 0 };
    if (!GAME.built.packer) return { step: 2, title: "Finish the production line", text: "Place the PACKER so finished parts can become crates.", progress: 1 };
    if (GAME.run === 0) return { step: 3, title: "Make something move", text: "Press RUN BATCH and watch the first part cross the floor.", progress: 2 };
    if (GAME.held) return { step: 4, title: "Clear the hold bay", text: GAME.held.text, progress: 3 };
    if (!GAME.built.pico) return { step: 4, title: "Automate final inspection", text: "Add Pico at the quality gate before another fault reaches the dock.", progress: 3 };
    if (GAME.stage === 1 && GAME.shipped < GAME.goal) return { step: 5, title: "Fill the contract", text: "Keep shipping. New signal cards make each run play differently.", progress: 4 };
    if (GAME.stage === 1) return { step: 6, title: "Knock down the expansion wall", text: "Claim line two and an 80-bolt building grant.", progress: 5 };
    if (GAME.shipped < GAME.goal) return { step: 7, title: "Run both lines", text: "Two lines now fill two crates per click. Grow the automation network when holds slow you down.", progress: 5 };
    return { step: 8, title: "Plant complete", text: "The contract is full. Keep optimizing or start a new floor.", progress: 6 };
  }

  function v2Objective(root) {
    var o = objective();
    var panel = el("section", "wf2-objective" + (GAME.held ? " is-hold" : ""));
    var flag = el("span", "wf2-objective__flag", GAME.held ? "!" : String(o.step).padStart(2, "0"));
    panel.appendChild(flag);
    var copy = el("div", "wf2-objective__copy");
    copy.appendChild(el("span", null, GAME.held ? "LINE HOLD" : "CURRENT OBJECTIVE"));
    copy.appendChild(el("h2", null, o.title));
    copy.appendChild(el("p", null, o.text));
    panel.appendChild(copy);
    var pips = el("div", "wf2-objective__pips");
    for (var i = 0; i < 6; i++) pips.appendChild(el("i", i < o.progress ? "is-done" : i === o.progress ? "is-now" : ""));
    panel.appendChild(pips);
    root.appendChild(panel);
  }

  function contractCell(kicker, value, detail, cls) {
    var cell = el("div", "wf2-contract__cell " + (cls || ""));
    cell.appendChild(el("span", null, kicker));
    cell.appendChild(el("b", null, value));
    cell.appendChild(el("i", null, detail));
    return cell;
  }

  function v2Contract(root) {
    var remaining = Math.max(0, GAME.goal - GAME.shipped);
    var next = nextAutomation(GAME);
    var base = cleanPayFor(GAME);
    var contract = el("section", "wf2-contract");
    contract.appendChild(contractCell("ACTIVE ORDER", "SHIP " + GAME.goal + " PUMP SKIDS",
      remaining ? remaining + " LEFT · THEN " + (GAME.stage === 1 ? "UNLOCK LINE 2" : "COMPLETE THE PLANT") : "ORDER READY TO CLAIM", "is-order"));
    contract.appendChild(contractCell("A CLEAN RUN PAYS", "+" + base + " BOLTS",
      "BOLTS ARE BUILD CURRENCY · MANUAL REWORK PAYS ONLY " + (12 * GAME.lines), "is-pay"));
    contract.appendChild(contractCell("NEXT MODEL PAYOFF", next.name + " · " + next.pay,
      next.copy, "is-model"));
    root.appendChild(contract);
  }

  function v2Watchbar(root) {
    var bar = el("div", "wf2-watchbar");
    bar.appendChild(el("span", "wf2-watchbar__label", "LINE DEFENSE"));
    [
      ["CREW", true, "reworks holds"],
      ["PICO", GAME.built.pico, "catches confident faults"],
      ["NANO", GAME.built.nano, "checks doubtful reads"],
      ["MICRO", GAME.built.micro, "+8 per line"],
      ["GIGA", GAME.built.giga, "+15 per line"],
    ].forEach(function (item) {
      var chip = el("span", "wf2-watchbar__chip" + (item[1] ? " is-on" : ""));
      chip.appendChild(el("i", null, ""));
      chip.appendChild(el("b", null, item[0]));
      chip.appendChild(el("em", null, item[2]));
      bar.appendChild(chip);
    });
    root.appendChild(bar);
  }

  function floorState() {
    if (GAME.busy) return { kind: "run", word: "RUNNING", text: "Batch " + String(GAME.run + 1).padStart(2, "0") + " is moving across the floor" };
    if (GAME.held) return { kind: "hold", word: "HOLD", text: GAME.held.title + " at final inspection" };
    if (!GAME.built.former || !GAME.built.packer) return { kind: "build", word: "BUILD MODE", text: "Place the glowing machines to complete line one" };
    if (GAME.stage === 1 && GAME.shipped >= GAME.goal) return { kind: "win", word: "EXPAND", text: "Contract one complete · line two is ready" };
    if (GAME.stage === 2 && GAME.shipped >= GAME.goal) return { kind: "win", word: "COMPLETE", text: "Plant contract shipped" };
    return { kind: "ready", word: "READY", text: GAME.lines + " line" + (GAME.lines === 1 ? "" : "s") + " waiting for a batch" };
  }

  function builtFor(id) {
    return id === "hopper" || id === "dock" || !!GAME.built[id];
  }

  function currentBuildId() {
    if (!GAME.built.former) return "former";
    if (!GAME.built.packer) return "packer";
    if (GAME.run > 0 && !GAME.built.pico) return "pico";
    if (GAME.held && GAME.held.reason === "needs-gateway") return "nano";
    return null;
  }

  function v2Node(id, col, row, span, line) {
    var info = MACHINE[id];
    var built = builtFor(id);
    var a = PRICE[id] != null ? availability(id, GAME) : { ok: false, reason: "ONLINE" };
    var node = el("button", "wf2-node wf2-node--" + id + (built ? " is-built" : " is-pad") +
      (GAME.selected === id ? " is-selected" : "") + (currentBuildId() === id ? " is-current" : ""));
    node.type = "button";
    node.style.gridColumn = col + " / span " + span;
    node.style.gridRow = String(row);
    node.setAttribute("aria-label", built ? info.name + ". " + info.job : "Build " + info.name + ". " + a.reason);
    var art = el("span", "wf2-node__art");
    art.appendChild(el("i")); art.appendChild(el("i")); art.appendChild(el("i")); art.appendChild(el("i"));
    node.appendChild(art);
    var label = el("span", "wf2-node__label");
    label.appendChild(el("b", null, built ? info.short : "+ " + info.short));
    label.appendChild(el("i", null, built ? (id === "dock" && GAME.held ? "BATCH HELD" : "ONLINE") : a.reason));
    node.appendChild(label);
    if (built && id !== "hopper" && id !== "dock") node.appendChild(el("span", "wf2-node__lamp", ""));
    node.addEventListener("click", function () {
      GAME.selected = id;
      if (!built && a.ok && !GAME.busy && !GAME.held) { buy(id); return; }
      render();
    });
    if (line) node.setAttribute("data-line", String(line));
    return node;
  }

  function v2Track(col, row) {
    var track = el("span", "wf2-track");
    track.style.gridColumn = String(col);
    track.style.gridRow = String(row);
    track.appendChild(el("i")); track.appendChild(el("i")); track.appendChild(el("i"));
    return track;
  }

  function v2Line(grid, row, line) {
    grid.appendChild(v2Node("hopper", 1, row, 2, line));
    grid.appendChild(v2Track(3, row));
    grid.appendChild(v2Node("former", 4, row, 2, line));
    grid.appendChild(v2Track(6, row));
    grid.appendChild(v2Node("pico", 7, row, 2, line));
    grid.appendChild(v2Track(9, row));
    grid.appendChild(v2Node("packer", 10, row, 2, line));
    grid.appendChild(v2Track(12, row));
    grid.appendChild(v2Node("dock", 13, row, 2, line));
    var lineTag = el("span", "wf2-linetag", "LINE " + String(line).padStart(2, "0"));
    lineTag.style.gridColumn = "1 / span 2";
    lineTag.style.gridRow = String(row - 1);
    grid.appendChild(lineTag);
  }

  function inspectorData(id) {
    return MACHINE[id] || MACHINE.former;
  }

  function v2Inspector() {
    var id = GAME.selected || "former";
    var info = inspectorData(id);
    var built = builtFor(id);
    var pane = el("aside", "wf2-inspector wf2-inspector--" + id);
    pane.appendChild(el("span", "wf2-inspector__k", built ? "SELECTED MACHINE" : "CONSTRUCTION PREVIEW"));
    var hero = el("div", "wf2-inspector__hero");
    var art = el("span", "wf2-inspector__art"); art.appendChild(el("i")); art.appendChild(el("i")); hero.appendChild(art);
    var name = el("span"); name.appendChild(el("h3", null, info.name)); name.appendChild(el("b", null, info.where)); hero.appendChild(name);
    pane.appendChild(hero);
    var status = el("span", "wf2-inspector__status " + (built ? "is-on" : "is-off"), built ? "● ONLINE" : "○ NOT BUILT");
    pane.appendChild(status);
    pane.appendChild(el("p", "wf2-inspector__job", info.job));
    var effect = el("div", "wf2-inspector__effect"); effect.appendChild(el("span", null, "WHAT IT CHANGES")); effect.appendChild(el("p", null, info.effect)); pane.appendChild(effect);
    if (GAME.last && GAME.last.record && (id === "pico" || id === "nano")) {
      var result = id === "pico" ? GAME.last.record.child : GAME.last.record.parent;
      var rec = el("div", "wf2-inspector__reading");
      rec.appendChild(el("span", null, "LAST RECORDED READ"));
      rec.appendChild(el("b", null, result ? String(result.prediction).toUpperCase() : "NOT RECORDED"));
      if (result) rec.appendChild(el("i", null, "MARGIN " + result.margin));
      pane.appendChild(rec);
    }
    if (GAME.held && id === "dock") pane.appendChild(actionButton("REWORK HELD BATCH →", "wf2-inspector__action", manualRework, false));
    else if (!built) pane.appendChild(el("p", "wf2-inspector__tip", "Build directly on the glowing floor pad or from the build bar."));
    return pane;
  }

  function v2Buildbar() {
    var bar = el("div", "wf2-buildbar");
    bar.appendChild(el("span", "wf2-buildbar__label", "BUILD"));
    ["former", "packer", "pico", "nano", "micro", "giga"].forEach(function (id) {
      var a = availability(id, GAME);
      var button = el("button", "wf2-tool wf2-tool--" + id + (GAME.selected === id ? " is-selected" : "") + (GAME.built[id] ? " is-owned" : ""));
      button.type = "button";
      button.appendChild(el("span", null, MACHINE[id].short.slice(0, 2)));
      button.appendChild(el("b", null, MACHINE[id].short));
      button.appendChild(el("i", null, GAME.built[id] ? "ONLINE" : a.reason));
      button.addEventListener("click", function () {
        GAME.selected = id;
        if (a.ok && !GAME.busy && !GAME.held) buy(id); else render();
      });
      bar.appendChild(button);
    });
    return bar;
  }

  function v2Floor(root) {
    var state = floorState();
    var stage = el("section", "wf2-stage");
    var status = el("div", "wf2-status is-" + state.kind);
    status.appendChild(el("b", null, state.word));
    status.appendChild(el("span", null, state.text));
    if (GAME.loading) status.appendChild(el("i", null, "LOADING SIGNAL CARDS"));
    else status.appendChild(el("i", null, GAME.records.length + " RECORDED SIGNAL CARDS"));
    stage.appendChild(status);
    v2Watchbar(stage);
    var playfield = el("div", "wf2-playfield");
    var board = el("div", "wf2-board" + (GAME.busy ? " is-running" : "") + (GAME.held ? " is-held" : ""));
    var grid = el("div", "wf2-grid");
    v2Line(grid, 3, 1);
    if (GAME.stage === 2) v2Line(grid, 6, 2);
    else {
      var wall = el("button", "wf2-wall", "LINE 02 · SHIP 6 CRATES TO EXPAND");
      wall.type = "button"; wall.style.gridColumn = "1 / span 14"; wall.style.gridRow = "6";
      wall.addEventListener("click", function () { if (GAME.shipped >= GAME.goal) expandFactory(); });
      grid.appendChild(wall);
    }
    if (GAME.busy) {
      grid.appendChild(el("span", "wf2-part", "◆"));
      if (GAME.lines > 1) grid.appendChild(el("span", "wf2-part wf2-part--line2", "◆"));
    }
    if (GAME.held) grid.appendChild(el("span", "wf2-part wf2-part--held", "!"));
    if (GAME.last && GAME.last.reward && !GAME.busy) {
      grid.appendChild(el("span", "wf2-payout", "+" + GAME.last.reward + " BOLTS"));
    }
    board.appendChild(grid);
    var action = nextAction();
    var runbar = el("div", "wf2-runbar");
    runbar.appendChild(el("span", null, GAME.busy ? "WATCH THE PART" : GAME.held ? "THE LINE IS WAITING FOR YOU" : "SHIP CRATES · EARN BOLTS · BUILD SMARTER"));
    runbar.appendChild(actionButton(action.copy, "wf2-run", action.fn, action.disabled));
    board.appendChild(runbar);
    playfield.appendChild(board);
    playfield.appendChild(v2Inspector());
    stage.appendChild(playfield);
    stage.appendChild(v2Buildbar());
    root.appendChild(stage);
  }

  function v2TreeNode(id, step) {
    var info = MACHINE[id], built = GAME.built[id], a = availability(id, GAME);
    var node = el("button", "wf2-tree__node wf2-tree__node--" + id + (built ? " is-built" : "") + (GAME.selected === id ? " is-selected" : ""));
    node.type = "button";
    node.style.gridColumn = String(step * 2 - 1);
    node.appendChild(el("span", "wf2-tree__icon", info.short.slice(0, 2)));
    node.appendChild(el("b", null, info.name));
    node.appendChild(el("i", null, info.where));
    node.appendChild(el("strong", null, ({ pico: "FAULT CATCH · +4", nano: "DOUBT SAVE · +8", micro: "SITE FLOW · +8/LINE", giga: "PLANT FLOW · +15/LINE" })[id]));
    node.appendChild(el("em", null, built ? "ONLINE" : a.reason));
    node.addEventListener("click", function () { GAME.selected = id; if (a.ok && !GAME.busy && !GAME.held) buy(id); else render(); });
    return node;
  }

  function v2Network(root) {
    var shell = el("section", "wf2-network");
    var intro = el("div", "wf2-network__intro");
    intro.appendChild(el("span", null, "AUTOMATION NETWORK"));
    intro.appendChild(el("h2", null, "Grow intelligence where the work grows."));
    intro.appendChild(el("p", null, "Models are upgrades, not quiz answers. Pico prevents confident machine faults from becoming holds. Nano rescues doubtful reads. Micro and Giga improve the simulated factory economy as the floor grows. Click an unlocked node to build it."));
    shell.appendChild(intro);
    var body = el("div", "wf2-network__body");
    var tree = el("div", "wf2-tree");
    ["pico", "nano", "micro", "giga"].forEach(function (id, index) {
      tree.appendChild(v2TreeNode(id, index + 1));
      if (index < 3) { var link = el("span", "wf2-tree__link"); link.style.gridColumn = String((index + 1) * 2); tree.appendChild(link); }
    });
    var future = el("div", "wf2-tree__future");
    future.appendChild(el("span", null, "FUTURE FACTORY MAP"));
    ["TERA · ENTERPRISE", "PETA · REGION", "EXA · TEACHER"].forEach(function (copy) { future.appendChild(el("i", null, copy)); });
    tree.appendChild(future);
    body.appendChild(tree);
    body.appendChild(v2Inspector());
    shell.appendChild(body);
    shell.appendChild(v2Buildbar());
    root.appendChild(shell);
  }

  function routeChip(main, sub, cls) {
    var chip = el("span", "wf2-route__chip " + (cls || ""));
    chip.appendChild(el("b", null, main)); chip.appendChild(el("i", null, sub)); return chip;
  }

  function v2Event(root) {
    var event = el("section", "wf2-event " + (GAME.last ? "is-" + GAME.last.kind : "is-new"));
    var head = el("div", "wf2-event__head");
    head.appendChild(el("span", null, GAME.last ? "LAST BATCH" : "HOW THE LINE WORKS"));
    head.appendChild(el("h3", null, GAME.last ? GAME.last.title : "Ship crates. Earn bolts. Buy a smarter line."));
    head.appendChild(el("p", null, GAME.last ? GAME.last.text : "Each shipped crate funds the next machine. When a signal fault appears, installed models can catch it before the dock; otherwise your crew can rework it for less pay."));
    event.appendChild(head);
    var route = el("div", "wf2-route");
    if (!GAME.last || !GAME.last.record) {
      route.appendChild(routeChip("RAW BLANK", "hopper", "is-neutral"));
      route.appendChild(el("b", null, "→")); route.appendChild(routeChip("SHAPE", "machine", "is-machine"));
      route.appendChild(el("b", null, "→")); route.appendChild(routeChip("INSPECT", "signal", "is-pico"));
      route.appendChild(el("b", null, "→")); route.appendChild(routeChip("CRATE", "dock", "is-good"));
    } else {
      var r = GAME.last.record;
      route.appendChild(routeChip(humanTag(r), (LABEL[r.truth] || r.truth).toUpperCase(), "is-sensor"));
      route.appendChild(el("b", null, "→"));
      route.appendChild(routeChip("PICO · " + (GAME.built.pico && r.child ? String(r.child.prediction).toUpperCase() : "OFF"),
        GAME.built.pico && r.child ? "margin " + r.child.margin : "not installed", "is-pico"));
      if (GAME.built.nano || (GAME.last.reason === "needs-gateway")) {
        route.appendChild(el("b", null, "→"));
        route.appendChild(routeChip("NANO · " + (GAME.built.nano && r.parent ? String(r.parent.prediction).toUpperCase() : "OFF"),
          GAME.built.nano && r.parent ? "margin " + r.parent.margin : "not installed", "is-nano"));
      }
      route.appendChild(el("b", null, "→"));
      var leftDock = GAME.last.shipped || GAME.last.kind === "manual";
      route.appendChild(routeChip(leftDock ? "CRATE SHIPPED" : "HOLD BAY",
        leftDock ? "+" + GAME.last.reward + " bolts" : "waiting for player", leftDock ? "is-good" : "is-bad"));
    }
    event.appendChild(route);
    if (GAME.last && GAME.last.record) {
      var details = el("details", "wf2-proof");
      details.appendChild(el("summary", null, "WHY DID THAT HAPPEN?"));
      details.appendChild(el("p", null, "Signal truth and Pico/Nano answers come from the committed replay. The floor, routing, crates, bolts, and production bonuses are game simulation. Micro and Giga never receive a model answer that was not recorded."));
      event.appendChild(details);
    }
    root.appendChild(event);
  }

  function v2Log(root) {
    var log = el("details", "wf2-log");
    log.appendChild(el("summary", null, "FACTORY RADIO · " + GAME.log.length));
    var list = el("ol"); GAME.log.forEach(function (copy) { list.appendChild(el("li", null, copy)); }); log.appendChild(list); root.appendChild(log);
  }

  function render() {
    var host = document.getElementById("wfGame");
    if (!host) return;
    host.textContent = "";
    var root = el("div", "wf-shell" + (GAME.busy ? " is-busy" : "") + (GAME.held ? " has-hold" : ""));
    v2Header(root);
    if (GAME.error) root.appendChild(el("p", "wf-error", GAME.error));
    v2Objective(root);
    v2Contract(root);
    if (GAME.view === "network") v2Network(root);
    else v2Floor(root);
    v2Event(root);
    v2Log(root);
    host.appendChild(root);
  }

  function loadRecords() {
    GAME.loading = true;
    GAME.error = "";
    render();
    window.fetch("data/wave-measured.json").then(function (response) {
      if (!response.ok) throw new Error("signal deck unavailable");
      return response.json();
    }).then(function (data) {
      if (!data || !Array.isArray(data.records) || !data.records.length) throw new Error("signal deck empty");
      GAME.records = data.records;
      GAME.loading = false;
      addLog(data.records.length + " recorded signal cards loaded for factory events.");
      render();
    }).catch(function () {
      GAME.loading = false;
      GAME.error = "The recorded signal cards did not load. You can build the floor, but RUN stays parked until a reload succeeds.";
      render();
    });
  }

  function boot() {
    if (!document.getElementById("wfGame")) return;
    render();
    loadRecords();
  }

  if (typeof window !== "undefined") {
    window.__waveFactoryTest = {
      freshState: freshState,
      pickRecord: pickRecord,
      resolveBatch: resolveBatch,
      availability: availability,
      cleanPayFor: cleanPayFor,
      nextAutomation: nextAutomation,
      prices: PRICE,
    };
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
