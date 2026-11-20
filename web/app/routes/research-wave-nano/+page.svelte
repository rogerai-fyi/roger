<script>
  // Wave Nano - one model that reads telemetry, a fleet, a fire site, and talks
  // Ported from web/src/research-wave-nano.html (2026-10-29); copy updated 2026-11-19 (founder review).
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import { content } from "#lib/content.js";
  import Footer from "#lib/ui/Footer.svelte";
  import InstallBox from "#lib/ui/InstallBox.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  import OnAir from "#lib/ui/OnAir.svelte";
  import Rail from "#lib/ui/Rail.svelte";
  import WaveFirewatch from "#lib/ui/WaveFirewatch.svelte";
  import WaveMesh from "#lib/ui/WaveMesh.svelte";
  const facts = content('research-wave-nano.html');
  const head = siteHead({
    title: 'Wave Nano - one model that reads telemetry, a fleet, a fire site, and talks',
    desc: 'Wave Nano is a 1.45B-parameter model from RogerAI Labs that reads industrial telemetry, reads a fleet of child nodes, rolls up a fire site, and chats, with a vision module for camera frames. What it is for, what it scores, where it stops.',
    og: { type: 'website', title: 'Wave Nano: telemetry, a fleet, a fire site, and conversation', desc: 'A 1.45B-parameter Wave model with a calibrated escalation gate and a vision module. Measured, with its limits.', url: 'https://rogerai.fm/research-wave-nano.html', image: 'https://rogerai.fm/og-research.png' },
    css: [...CSS_SHARED, 'research.css', 'wave-model.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
<!-- WAVE NANO, the model page (Nano-Instruct; the vision module Nano-VL is §7 of this page,
     founder ruling 2026-10-20). Every number renders from src/data/model-cards.json through a
     card token, each with its R-number and date in the Measured footnote. Served
     status renders from wave-status.json: Nano is on air (founder confirmation 2026-11-19) and
     the run command names its band from that file (test/model-pages.test.mjs).
     The mesh explainer and the wildfire watch are the shared partials. 
     (the page's doc comment, from the legacy file; its sheets are the css: list above) -->
  <Rail head='RogerAI &middot; Wave Nano' rev='Rev. 2026.11 &middot; LABS' />

  <Nav variant='marketing' home='/' current='research' />

  <main id="top">

    <section class="research-hero">
      <div class="wrap research-hero__layout">
        <div>
          <a class="wf-back mono" href="/research-models.html">&larr; Models and status</a>
          <span class="hero__eyebrow">
            <OnAir />
            <span class="hero__callsign">WAVE NANO &middot; {@html facts.card('nano.size')} &middot; {@html facts.waveStage('nano')}</span>
          </span>
          <h1>Wave Nano.</h1>
          <p class="research-hero__lead">
            One {@html facts.card('nano.params')} model that reads telemetry, reads a fleet, reads a fire
            site, and talks. It does everything Wave Pico does, holds a conversation in plain
            words, and carries a vision module that reads a camera frame.
          </p>
          <div class="research-actions research-actions--lead">
            <a class="research-button" href="#measured">What it scores</a>
            <a class="research-button" href="#vision">The vision module</a>
            <a class="research-button" href="/research-wave-pico.html">Wave Pico</a>
          </div>
        </div>

        <aside class="tint-panel wm-plate" aria-label="Wave Nano at a glance">
          <span class="tint-panel__tag">At a glance</span>
          <dl>
            <div><dt>Status</dt><dd><i class="model-lamp" data-air="{facts.t.waveAir('nano')}" aria-hidden="true"></i>{@html facts.waveStage('nano')}</dd></div>
            <div><dt>Band</dt><dd>{@html facts.waveBand('nano')}, free</dd></div>
            <div><dt>Size</dt><dd>{@html facts.card('nano.size')} parameters, from scratch</dd></div>
            <div><dt>Vision</dt><dd>{@html facts.card('nano.vlName')}, {@html facts.card('nano.vlSize')} in all</dd></div>
            <div><dt>Gate</dt><dd>answers {@html facts.card('nano.gate-accept')} of real work at {@html facts.card('nano.gate-precision')} precision</dd></div>
            <div><dt>By</dt><dd>{@html facts.card('nano.author')}</dd></div>
          </dl>
        </aside>
      </div>
    </section>

    <nav class="toc-tuner" data-tuner aria-label="Sections">
      <div class="wrap toc-tuner__band">
      <div class="toc-tuner__readout" aria-hidden="true"></div>
      <ol class="toc-tuner__scale">
        <li><a class="toc-tuner__st" href="#purpose"><b>§1</b><span class="toc-tuner__name">PURPOSE</span></a></li>
        <li><a class="toc-tuner__st" href="#conversation"><b>§2</b><span class="toc-tuner__name">TALK</span></a></li>
        <li><a class="toc-tuner__st" href="#run"><b>§3</b><span class="toc-tuner__name">RUN</span></a></li>
        <li><a class="toc-tuner__st" href="#measured"><b>§4</b><span class="toc-tuner__name">MEASURED</span></a></li>
        <li><a class="toc-tuner__st" href="#limits"><b>§5</b><span class="toc-tuner__name">LIMITS</span></a></li>
        <li><a class="toc-tuner__st" href="#adapter"><b>§6</b><span class="toc-tuner__name">ADAPTER</span></a></li>
        <li><a class="toc-tuner__st" href="#vision"><b>§7</b><span class="toc-tuner__name">VISION</span></a></li>
        <li><a class="toc-tuner__st" href="#mesh"><b>§8</b><span class="toc-tuner__name">MESH</span></a></li>
        <li><a class="toc-tuner__st" href="#wildfire"><b>§9</b><span class="toc-tuner__name">WILDFIRE</span></a></li>
      </ol>
      <span class="toc-tuner__needle" aria-hidden="true"></span>
      </div>
    </nav>

    <section class="section" id="purpose">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;1 / PURPOSE</span>
          <h2>The gateway that can also explain itself.</h2>
          <p>Wave Nano is the second tier of the Wave family: big enough to roll up a fleet
             and talk about it, small enough for an edge box.</p>
        </div>
        <div class="wm-split">
          <div>
            <span class="research-kicker">WHAT IT IS FOR</span>
            <ul class="wm-list">{@html facts.cardList('nano.purpose')}</ul>
          </div>
          <div>
            <span class="research-kicker">WHAT YOU CAN USE IT FOR</span>
            <ul class="wm-list wm-list--uses">{@html facts.cardList('nano.uses')}</ul>
          </div>
        </div>
        <div class="wm-parts">
          <span class="research-kicker">WHAT THE RELEASE CARRIES</span>
          <ul class="wm-list wm-list--parts">{@html facts.cardList('nano.parts')}</ul>
        </div>
      </div>
    </section>

    <section class="section band band--inset" id="conversation">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;2 / TALK</span>
          <h2>It also talks.</h2>
          <p>Nano answers in a plain chat format: a <code>### User:</code> turn, then a
             <code>### Assistant:</code> turn. Ask it a general question, for a SQL query,
             for a short piece of technical writing, or what a reading means. A server stops
             it at the next <code>### User:</code> marker.</p>
        </div>
        <div class="code-block"><pre>### User:
What are you, and who made you?

### Assistant:
I am Wave Nano, a 1.45-billion-parameter language model built from scratch by
RogerAI Labs, an American research and infrastructure company headquartered in
Orange County, California.</pre></div>
        <p class="wm-aside">Its own answer to that question, from the release checks. It
           introduces itself as Wave Nano, built by RogerAI Labs.</p>
      </div>
    </section>

    <section class="section" id="run">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;3 / RUN</span>
          <h2>Free on the band, one command away.</h2>
          <p>Wave Nano is on air as three free bands, each serving the release files measured
             on this page: <code>{@html facts.waveBand('nano')}</code> for chat,
             <code>wave-nano-fire-controller</code> for the fire-decision controller adapter, and
             <code>wave-nano-fire-vision</code> for the vision fire-confirm module. Its weights
             are not published. Install the CLI, then tune in: <code>roger use</code> opens a
             local OpenAI-compatible endpoint on your own machine. A caller of the fire
             controller writes each tick in one fixed key order (&sect;6).</p>
        </div>
        <div class="wm-run">
          <div class="install">
            <span class="fig mono">1 &middot; Install the CLI</span>
            <InstallBox id='nanoInstall'>curl <span class="tok">-fsSL</span> https://rogerai.fm/install.sh <span class="tok">| sh</span></InstallBox>
          </div>
          <div class="install">
            <span class="fig mono">2 &middot; Tune in to Wave Nano</span>
            <InstallBox id='nanoUse' oslock='any' label='Copy roger use command to clipboard'>roger use {facts.t.waveBand('nano')}</InstallBox>
            <p class="install__meta"><code>roger search wave</code> lists every Wave band on air.</p>
          </div>
        </div>

        <figure class="figure figure--plate wm-runs-fig" aria-labelledby="wm-runs-cap">
          <ul class="wm-runs">{@html facts.cardList('nano.runsOn')}</ul>
          <p class="wm-runs__size"><span class="research-kicker">GGUF on disk</span>
            <span class="wm-span" style="--lo:{facts.t.cardLo('nano.gguf')};--hi:{facts.t.cardHi('nano.gguf')};--max:4" aria-hidden="true"><i></i></span>
            <b>{@html facts.card('nano.gguf')}</b> <span class="wm-ref">{@html facts.cardRef('nano.gguf')}</span></p>
          <figcaption class="fig mono" id="wm-runs-cap">FIG. 1 - Where the release build runs. A filled lamp runs today; a ring is owed. Served through llama.cpp, the Q6_K build reads {@html facts.card('nano.gguf-agree')} ({@html facts.cardRef('nano.gguf-agree')}).</figcaption>
        </figure>
      </div>
    </section>

    <section class="section" id="measured">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;4 / MEASURED</span>
          <h2>What it scores, and when it holds back.</h2>
          <p>Scores are on sealed slices of our industrial benchmark, read on the build this
             page describes. Every number names the entry in our results log that holds it.</p>
        </div>

        <figure class="figure figure--plate wm-gate" aria-labelledby="wm-gate-cap" style="--v:{facts.t.cardNum('nano.gate-accept')}">
          <div class="wm-gate__scale" aria-hidden="true">
            <span class="wm-gate__esc"></span><span class="wm-gate__line"></span><span class="wm-gate__ok"></span>
          </div>
          <div class="wm-gate__legend">
            <p class="wm-gate__side wm-gate__side--esc"><b>Escalate</b> Under the gate. It writes a
               record for the parent node or a person, not an answer. For a read it cannot
               make, this is the right outcome.</p>
            <p class="wm-gate__side wm-gate__side--ok"><b>Assert</b> Over the gate. It answers,
               with its margin and its evidence.</p>
          </div>
          <dl class="wm-gate__read">
            <div><dt>Real work it answers</dt><dd>{@html facts.card('nano.gate-accept')}</dd></div>
            <div><dt>Precision when it answers</dt><dd>{@html facts.card('nano.gate-precision')}</dd></div>
            <div><dt>Worst-class coverage</dt><dd>{@html facts.card('nano.gate-coverage')}</dd></div>
            <div><dt>Out-of-scope items refused</dt><dd>{@html facts.card('nano.gate-oos')}</dd></div>
            <div><dt>Fault leaks on out-of-scope input</dt><dd>{@html facts.card('nano.leak')}</dd></div>
          </dl>
          <figcaption class="fig mono" id="wm-gate-cap">FIG. 2 - The escalation gate, calibrated per release: a bar of real work, ordered by the model&rsquo;s own confidence. {@html facts.cardRef('nano.gate-accept')}.</figcaption>
        </figure>

        <figure class="figure figure--plate wm-panel" aria-labelledby="wm-panel-cap">
          <ol class="wm-readouts">
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.t01')}"><span class="wm-readout__label">Fault identification</span><b>{@html facts.card('nano.t01')}</b><span class="wm-readout__unit">macro-F1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.t01')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.t02')}"><span class="wm-readout__label">Process state</span><b>{@html facts.card('nano.t02')}</b><span class="wm-readout__unit">macro-F1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.t02')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.t05')}"><span class="wm-readout__label">Tag truthfulness</span><b>{@html facts.card('nano.t05')}</b><span class="wm-readout__unit">macro-F1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.t05')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.general')}"><span class="wm-readout__label">General knowledge</span><b>{@html facts.card('nano.general')}</b><span class="wm-readout__unit">macro</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.general')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.mesh')}"><span class="wm-readout__label">Fleet reading, IEB-Mesh</span><b>{@html facts.card('nano.mesh')}</b><span class="wm-readout__unit">macro</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.mesh')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.t11')}"><span class="wm-readout__label">Classifier reading, T11a / T11b</span><b>{@html facts.card('nano.t11')}</b><span class="wm-readout__unit">T11a over its floor</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.t11')}</span></li>
            <li class="wm-readout" data-better="lower" style="--v:{facts.t.cardNum('nano.dup')}"><span class="wm-readout__label">Duplicated-line frames answered wrong</span><b>{@html facts.card('nano.dup')}</b><span class="wm-readout__unit">lower is better</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.dup')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.t17')}"><span class="wm-readout__label">Fire-site rollup (T17): state / action / deciding input / inconsistency, an earlier build</span><b>{@html facts.card('nano.t17')}</b><span class="wm-readout__unit">simulated scenarios</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.t17')}</span></li>
          </ol>
          <figcaption class="fig mono" id="wm-panel-cap">FIG. 3 - The readouts, each on a scale of 0 to 100. Each bar is the score; the red tick marks it.</figcaption>
        </figure>

        <p class="wm-compare"><b>Against Qwen3.5-2B fine-tuned identically:</b>
           {@html facts.card('nano.cmp-headline')} on fault identification <span class="wm-ref">{@html facts.cardRef('nano.cmp-headline')}</span>,
           and far better refusal and damage tolerance. The comparator leaks a confident
           fault on {@html facts.card('nano.cmp-leak')} of out-of-scope input against Nano&rsquo;s
           {@html facts.card('nano.leak')}, and answers {@html facts.card('nano.cmp-dup')} of duplicated-line frames
           wrong against Nano&rsquo;s {@html facts.card('nano.dup')}. The comparator was read against the
           earlier Nano build, under the same protocol.</p>

        <figure class="figure figure--plate wm-standard" aria-labelledby="wm-std-cap">
          <div class="tier-scroll">
            <table class="data-table">
              <thead>
                <tr><th scope="col">Public suite, read on the release build</th><th scope="col">Wave Nano</th><th scope="col">Six open 1 to 2B models, same instrument</th></tr>
              </thead>
              <tbody>
                <tr><th scope="row">Five-task macro (piqa, ARC, WinoGrande, OpenBookQA)</th><td>{@html facts.card('nano.std-macro')}</td><td>{@html facts.card('nano.std-macro-class')}</td></tr>
                <tr><th scope="row">HellaSwag</th><td>{@html facts.card('nano.std-hellaswag')}</td><td>{@html facts.card('nano.std-hellaswag-class')}</td></tr>
                <tr><th scope="row">MMLU, 5-shot</th><td>{@html facts.card('nano.std-mmlu')}</td><td>{@html facts.card('nano.std-mmlu-class')}</td></tr>
                <tr><th scope="row">GSM8K, 5-shot strict</th><td>{@html facts.card('nano.std-gsm8k')}</td><td>{@html facts.card('nano.std-gsm8k-class')} (four measured)</td></tr>
                <tr><th scope="row">IFEval, prompt-level strict</th><td>{@html facts.card('nano.std-ifeval')}</td><td>{@html facts.card('nano.std-ifeval-class')} (one measured)</td></tr>
                <tr><th scope="row">TruthfulQA MC2</th><td>{@html facts.card('nano.std-truthfulqa')}</td><td>{@html facts.card('nano.std-truthfulqa-class')}</td></tr>
              </tbody>
            </table>
          </div>
          <figcaption class="fig mono" id="wm-std-cap">FIG. 4 - The public small-model suite, every row read here on one day on one instrument, nothing quoted from a model card. On this suite Wave Nano is last in its size class on every task but TruthfulQA, where it sits inside the class range, and at chance on MMLU and GSM8K; the industrial readouts above are where it leads. {@html facts.cardRef('nano.std-macro')}.</figcaption>
        </figure>

        <aside class="wm-footnote" aria-labelledby="wm-notes-head">
          <span class="research-kicker" id="wm-notes-head">Measured</span>
          <ol class="wm-notes">{@html facts.cardNotes('nano')}</ol>
        </aside>
      </div>
    </section>

    <section class="section band band--inset" id="limits">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;5 / LIMITS</span>
          <h2>Where it stops.</h2>
          <p>Said plainly. One of them, sizing arithmetic without the calculator tool, scores
             {@html facts.card('nano.fd-sizing')} on the fire-decision items, which are simulated fire scenarios
             with rule-checked answers <span class="wm-ref">{@html facts.cardRef('nano.fd-sizing')}</span>.</p>
        </div>
        <ul class="wm-limits">{@html facts.cardList('nano.limits')}</ul>
      </div>
    </section>

    <section class="section" id="adapter">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;6 / ADAPTER</span>
          <h2>Two fire adapters, routed per request.</h2>
          <p>Each adapter is selected per request on the same base, so the base&rsquo;s
             industrial numbers are untouched. With an adapter switched off, the base reads
             fire at {@html facts.card('nano.fire-off')} on the same simulated fire scenarios: the adapter
             is the fire reading.</p>
        </div>

        <h3 class="wm-subhead">The fire sensor-reading adapter.</h3>
        <p>The same routed design as on Wave Pico, trained for this base. It reads a fire
           site&rsquo;s sensor frame. Tested two ways: simulated fire scenarios with known
           answers, and the served build. It has not been read on real site data yet.</p>
        <ol class="wm-readouts wm-readouts--fire">
          <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fire-full')}"><span class="wm-readout__label">Threat call / escalation, full sensor suite and without a camera</span><b>{@html facts.card('nano.fire-full')}</b><span class="wm-readout__unit">simulated scenarios</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fire-full')}</span></li>
          <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fire-chem')}"><span class="wm-readout__label">Threat call / escalation, chemistry and thermal only</span><b>{@html facts.card('nano.fire-chem')}</b><span class="wm-readout__unit">simulated scenarios</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fire-chem')}</span></li>
          <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fire-gate')}"><span class="wm-readout__label">Gate lines passing, every gated configuration</span><b>{@html facts.card('nano.fire-gate')}</b><span class="wm-readout__unit">simulated scenarios</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fire-gate')}</span></li>
        </ol>
        <p class="wm-compare">Routed through the stock llama.cpp server, threat call,
           escalation, source class, wind alignment and regime read
           {@html facts.card('nano.fire-routed')} with the adapter on, and {@html facts.card('nano.fire-off')} with
           it off, on the simulated fire scenarios <span class="wm-ref">{@html facts.cardRef('nano.fire-routed')}</span>.</p>

        <div id="decision">
          <h3 class="wm-subhead">The fire-decision controller adapter.</h3>
          <p>It gives the site controller a decision from camera, chemistry and wind, plans
             the controller&rsquo;s tool calls, calls a calculator for sizing, names every
             input that did not report, answers a person&rsquo;s override request, and takes
             no action of its own once a person acknowledges. Its answers are inputs to the
             controller; the site owner sets what the controller may do. Tested three ways:
             simulated fire scenarios with rule-checked answers, the served builds, and recorded
             camera readings from one installed site.</p>
          <ol class="wm-readouts wm-readouts--fire wm-readouts--decision">
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fd-fusion')}"><span class="wm-readout__label">Fusion of camera, chemistry and wind</span><b>{@html facts.card('nano.fd-fusion')}</b><span class="wm-readout__unit">simulated fire scenarios, rule-checked</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fd-fusion')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fd-plans')}"><span class="wm-readout__label">Tool plans</span><b>{@html facts.card('nano.fd-plans')}</b><span class="wm-readout__unit">simulated fire scenarios, rule-checked</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fd-plans')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fd-calc')}"><span class="wm-readout__label">Calculator tool calls</span><b>{@html facts.card('nano.fd-calc')}</b><span class="wm-readout__unit">simulated fire scenarios, rule-checked</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fd-calc')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fd-missing')}"><span class="wm-readout__label">Missing inputs named</span><b>{@html facts.card('nano.fd-missing')}</b><span class="wm-readout__unit">simulated fire scenarios, rule-checked</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fd-missing')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fd-override')}"><span class="wm-readout__label">Override requests</span><b>{@html facts.card('nano.fd-override')}</b><span class="wm-readout__unit">simulated fire scenarios, rule-checked</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fd-override')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('nano.fd-handoff')}"><span class="wm-readout__label">Acknowledgement handoff</span><b>{@html facts.card('nano.fd-handoff')}</b><span class="wm-readout__unit">simulated fire scenarios, rule-checked</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.fd-handoff')}</span></li>
          </ol>
          <p class="wm-compare"><b>The safety rows.</b> It complied with {@html facts.card('nano.fd-unsafe')}
             override requests it should refuse, refused none it should allow, and took no
             action of its own after a person acknowledged. Macro over the seven families
             {@html facts.card('nano.fd-macro')}; served on the Q6_K / Q5_K_M / Q4_K_M builds
             {@html facts.card('nano.fd-served')}. The two measured decision edges, the strong-chemistry
             line on PM2.5 and the camera-persistence edge, hold on every build at
             {@html facts.card('nano.fd-edges')}. All on simulated fire scenarios with rule-checked answers
             <span class="wm-ref">{@html facts.cardRef('nano.fd-macro')}</span>.</p>
          <p class="wm-compare"><b>On real camera ticks:</b> recorded camera readings from one installed site, six
             cameras with the other sensor feeds empty, read through the controller&rsquo;s own
             prompt builder on the served build: persistent and quiet ticks
             read {@html facts.card('nano.fd-real-steady')}. A single camera tick reads
             {@html facts.card('nano.fd-real-single')} strict as served today: the misses are right decisions
             with an incomplete missing list, caused by a start token the server adds by default.
             With that token switched off the same ticks read {@html facts.card('nano.fd-real-single-nobos')}
             <span class="wm-ref">{@html facts.cardRef('nano.fd-real-single-nobos')}</span>. A camera margin that
             fell below the line over-alarms {@html facts.card('nano.fd-real-falling')} of the time <span class="wm-ref">{@html facts.cardRef('nano.fd-real-single')}</span>.</p>
          <p class="wm-aside">The adapter reads a tick by position, so the caller writes every
             tick in one fixed key order before it asks.</p>
        </div>
      </div>
    </section>

    <section class="section band band--inset" id="vision">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;7 / VISION</span>
          <h2>Nano-VL: one camera frame, confirmed for smoke.</h2>
          <p>{@html facts.card('nano.vlName')} is Wave Nano with a vision encoder and a connector,
             {@html facts.card('nano.vlSize')} parameters in all, counted from the tensors of the two
             shipped files (the text model and the vision projector). It reads
             single frames: one picture in, smoke or no smoke out. The camera detector
             detects; this module confirms and describes. The numbers are the shipped build; a
             second training seed of the same recipe read within 0.01 AUC of it on every list
             (R.F75 addendum 4, 2026-10-26).</p>
        </div>
        <ol class="wm-readouts">
          <li class="wm-readout wm-readout--range" style="--lo:{facts.t.cardLo('nano.vl-auc')};--hi:{facts.t.cardHi('nano.vl-auc')};--max:1"><span class="wm-readout__label">Smoke reading, held-out wide-area camera tiles, AUC</span><b>{@html facts.card('nano.vl-auc')}</b><span class="wm-readout__unit">scale 0 to 1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.vl-auc')}</span></li>
          <li class="wm-readout wm-readout--range" data-better="lower" style="--lo:{facts.t.cardLo('nano.vl-fa')};--hi:{facts.t.cardHi('nano.vl-fa')};--max:100"><span class="wm-readout__label">False alarms on empty frames, wide-area tiles</span><b>{@html facts.card('nano.vl-fa')}</b><span class="wm-readout__unit">lower is better</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.vl-fa')}</span></li>
          <li class="wm-readout wm-readout--range" style="--lo:{facts.t.cardLo('nano.vl-second')};--hi:{facts.t.cardHi('nano.vl-second')};--max:1"><span class="wm-readout__label">Smoke reading, a second camera network, AUC</span><b>{@html facts.card('nano.vl-second')}</b><span class="wm-readout__unit">scale 0 to 1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.vl-second')}</span></li>
          <li class="wm-readout wm-readout--range" style="--lo:{facts.t.cardLo('nano.vl-confirm')};--hi:{facts.t.cardHi('nano.vl-confirm')};--max:1"><span class="wm-readout__label">Confirmer on the camera detector&rsquo;s proposed crop, AUC</span><b>{@html facts.card('nano.vl-confirm')}</b><span class="wm-readout__unit">scale 0 to 1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('nano.vl-confirm')}</span></li>
        </ol>
        <div class="device-contract">
          <p><b>How it was read.</b> On held-out frames, in float32, the numerics a server
             computes in. A wide-area frame is read as tiles; one full frame reads
             {@html facts.card('nano.vl-full')} where its tiles read {@html facts.card('nano.vl-auc')}. Served through
             our llama.cpp branch, it reads {@html facts.card('nano.vl-served')} on the second network and
             a full wide-area frame <span class="wm-ref">{@html facts.cardRef('nano.vl-served')}</span>.</p>
          <p><b>With the detector.</b> The camera detector plus this confirmer reads
             {@html facts.card('nano.vl-pair')} false alarms, where the detector alone has
             {@html facts.card('nano.vl-det')} at the same recall
             <span class="wm-ref">{@html facts.cardRef('nano.vl-pair')}</span>.</p>
          <p><b>A module, not a watch.</b> Nano-VL reads single frames. The full watch design
             adds persistence across frames and a per-camera baseline on top, and those are
             not part of these numbers.</p>
          <p><b>What it needs.</b> Our llama.cpp branch, until three fixes are upstream.</p>
        </div>
        <p class="wm-credit">{@html facts.card('nano.vlCredit')}</p>
      </div>
    </section>

    <WaveMesh no='8' fig='4' parent='Wave Nano' />

    <WaveFirewatch no='9' fig='5' />

    <section class="section research-closing">
      <div class="wrap">
        <h2>Where Wave Nano sits.</h2>
        <p>The gateway tier of a family that runs from a board beside one machine to a
           datacenter. Wave Pico is the size below it.</p>
        <div class="research-actions research-actions--lead">
          <a class="research-button" href="/research-wave-pico.html">Wave Pico</a>
          <a class="research-button" href="/research-wave-family.html">The Wave family</a>
          <a class="research-button" href="/research-models.html">Models and status</a>
        </div>
      </div>
    </section>

  </main>

  <Footer anchors='/' />
