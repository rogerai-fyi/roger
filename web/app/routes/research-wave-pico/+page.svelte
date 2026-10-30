<script>
  // Wave Pico - a sensor reader that refuses what it cannot read
  // Ported from web/src/research-wave-pico.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import { content } from "#lib/content.js";
  import Footer from "#lib/ui/Footer.svelte";
  import InstallBox from "#lib/ui/InstallBox.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  import OnAir from "#lib/ui/OnAir.svelte";
  import Rail from "#lib/ui/Rail.svelte";
  import WaveFirewatch from "#lib/ui/WaveFirewatch.svelte";
  import WaveMesh from "#lib/ui/WaveMesh.svelte";
  const facts = content('research-wave-pico.html');
  const head = siteHead({
    title: 'Wave Pico - a sensor reader that refuses what it cannot read',
    desc: 'Wave Pico is a 293M-parameter model from RogerAI Labs that reads one industrial telemetry channel, names the fault or the process state, and escalates whatever its measured gate does not clear. What it is for, how to run it, what it scores and where it stops.',
    og: { type: 'website', title: 'Wave Pico: a sensor reader that refuses what it cannot read', desc: 'A 293M-parameter telemetry reader with a calibrated escalation gate, free on the RogerAI band. Measured, with its limits.', url: 'https://rogerai.fm/research-wave-pico.html', image: 'https://rogerai.fm/og-research.png' },
    css: [...CSS_SHARED, 'research.css', 'wave-model.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
<!-- WAVE PICO, the model page. Every number on it renders from src/data/model-cards.json
     through a card token, and every one carries its R-number and date in the Measured
     footnote (test/model-pages.test.mjs). What is served renders from wave-status.json. The
     mesh explainer and the wildfire watch are shared partials (wave-mesh.html,
     wave-firewatch.html), the same on every model page.
     Outward-copy rules: author RogerAI Labs; no data sources or training methods; no hosting
     or operational detail; fire numbers are simulator-internal every time; not on Hugging Face. 
     (the page's doc comment, from the legacy file; its sheets are the css: list above) -->
  <Rail head='RogerAI &middot; Wave Pico' rev='Rev. 2026.10 &middot; LABS' />

  <Nav variant='marketing' home='/' current='research' />

  <main id="top">

    <section class="research-hero">
      <div class="wrap research-hero__layout">
        <div>
          <a class="wf-back mono" href="/research-models.html">&larr; Models and status</a>
          <span class="hero__eyebrow">
            <OnAir />
            <span class="hero__callsign">WAVE PICO &middot; {@html facts.card('pico.size')} &middot; {@html facts.waveStage('pico')}</span>
          </span>
          <h1>Wave Pico.</h1>
          <p class="research-hero__lead">
            A {@html facts.card('pico.params')} sensor reader that refuses what it cannot read. It reads
            one telemetry channel, names what is wrong with the instrument or the process,
            and when it is not sure it says so, as a record a person or a larger model can
            act on.
          </p>
          <div class="research-actions research-actions--lead">
            <a class="research-button" href="#run">Run it</a>
            <a class="research-button" href="#measured">What it scores</a>
            <a class="research-button" href="/research-wave-nano.html">Wave Nano</a>
          </div>
        </div>

        <aside class="tint-panel wm-plate" aria-label="Wave Pico at a glance">
          <span class="tint-panel__tag">At a glance</span>
          <dl>
            <div><dt>Status</dt><dd><i class="model-lamp" data-air="{facts.t.waveAir('pico')}" aria-hidden="true"></i>{@html facts.waveStage('pico')}</dd></div>
            <div><dt>Band</dt><dd><code>{@html facts.waveBand('pico')}</code>, free</dd></div>
            <div><dt>Size</dt><dd>{@html facts.card('pico.size')} parameters, from scratch</dd></div>
            <div><dt>Builds</dt><dd>{@html facts.card('pico.builds')}, weights not published</dd></div>
            <div><dt>Gate</dt><dd>answers {@html facts.card('pico.gate-accept')} of real work at {@html facts.card('pico.gate-precision')} precision</dd></div>
            <div><dt>By</dt><dd>{@html facts.card('pico.author')}</dd></div>
          </dl>
        </aside>
      </div>
    </section>

    <nav class="toc-tuner" data-tuner aria-label="Sections">
      <div class="wrap toc-tuner__band">
      <div class="toc-tuner__readout" aria-hidden="true"></div>
      <ol class="toc-tuner__scale">
        <li><a class="toc-tuner__st" href="#purpose"><b>§1</b><span class="toc-tuner__name">PURPOSE</span></a></li>
        <li><a class="toc-tuner__st" href="#run"><b>§2</b><span class="toc-tuner__name">RUN</span></a></li>
        <li><a class="toc-tuner__st" href="#measured"><b>§3</b><span class="toc-tuner__name">MEASURED</span></a></li>
        <li><a class="toc-tuner__st" href="#limits"><b>§4</b><span class="toc-tuner__name">LIMITS</span></a></li>
        <li><a class="toc-tuner__st" href="#adapter"><b>§5</b><span class="toc-tuner__name">ADAPTER</span></a></li>
        <li><a class="toc-tuner__st" href="#mesh"><b>§6</b><span class="toc-tuner__name">MESH</span></a></li>
        <li><a class="toc-tuner__st" href="#wildfire"><b>§7</b><span class="toc-tuner__name">WILDFIRE</span></a></li>
      </ol>
      <span class="toc-tuner__needle" aria-hidden="true"></span>
      </div>
    </nav>

    <section class="section" id="purpose">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;1 / PURPOSE</span>
          <h2>One channel in, one honest verdict out.</h2>
          <p>Wave Pico is the smallest tier of the Wave family, built to sit beside one
             machine and read what that machine emits.</p>
        </div>
        <div class="wm-split">
          <div>
            <span class="research-kicker">WHAT IT IS FOR</span>
            <ul class="wm-list">{@html facts.cardList('pico.purpose')}</ul>
          </div>
          <div>
            <span class="research-kicker">WHAT YOU CAN USE IT FOR</span>
            <ul class="wm-list wm-list--uses">{@html facts.cardList('pico.uses')}</ul>
          </div>
        </div>
      </div>
    </section>

    <section class="section band band--inset" id="run">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;2 / RUN</span>
          <h2>Free on the band, one command away.</h2>
          <p>Wave Pico is on air as a free band. The band serves the release measured on this
             page. Install the CLI, then tune in: <code>roger use</code> opens a local
             OpenAI-compatible endpoint on your own machine, and anything that speaks that
             API can call it.</p>
        </div>
        <div class="wm-run">
          <div class="install">
            <span class="fig mono">1 &middot; Install the CLI</span>
            <InstallBox id='picoInstall'>curl <span class="tok">-fsSL</span> https://rogerai.fm/install.sh <span class="tok">| sh</span></InstallBox>
          </div>
          <div class="install">
            <span class="fig mono">2 &middot; Tune in to Wave Pico</span>
            <InstallBox id='picoUse' oslock='any' label='Copy roger use command to clipboard'>roger use {facts.t.waveBand('pico')}</InstallBox>
            <p class="install__meta"><code>roger search wave</code> lists every Wave band on air.</p>
          </div>
        </div>

        <figure class="figure figure--plate wm-runs-fig" aria-labelledby="wm-runs-cap">
          <ul class="wm-runs">{@html facts.cardList('pico.runsOn')}</ul>
          <p class="wm-runs__size"><span class="research-kicker">GGUF on disk</span>
            <span class="wm-span" style="--lo:{facts.t.cardLo('pico.gguf')};--hi:{facts.t.cardHi('pico.gguf')};--max:1000" aria-hidden="true"><i></i></span>
            <b>{@html facts.card('pico.gguf')}</b> <span class="wm-ref">{@html facts.cardRef('pico.gguf')}</span></p>
          <figcaption class="fig mono" id="wm-runs-cap">FIG. 1 - Where the release runs. A filled lamp runs today; a ring is owed. Every GGUF build scored {@html facts.card('pico.gguf-agree')} of the reference; the MLX build agrees on {@html facts.card('pico.mlx')} of items ({@html facts.cardRef('pico.mlx')}).</figcaption>
        </figure>
      </div>
    </section>

    <section class="section" id="measured">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;3 / MEASURED</span>
          <h2>What it scores, and when it holds back.</h2>
          <p>Scores are on sealed slices of our industrial benchmark, read by the release
             itself. Every number below names the entry in our results log that holds it.</p>
        </div>

        <figure class="figure figure--plate wm-gate" aria-labelledby="wm-gate-cap" style="--v:{facts.t.cardNum('pico.gate-accept')}">
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
            <div><dt>Real work it answers</dt><dd>{@html facts.card('pico.gate-accept')}</dd></div>
            <div><dt>Precision when it answers</dt><dd>{@html facts.card('pico.gate-precision')}</dd></div>
            <div><dt>Out-of-scope input refused</dt><dd>{@html facts.card('pico.oos')}</dd></div>
          </dl>
          <figcaption class="fig mono" id="wm-gate-cap">FIG. 2 - The escalation gate, calibrated per release: a bar of real work, ordered by the model&rsquo;s own confidence. {@html facts.cardRef('pico.gate-accept')}.</figcaption>
        </figure>

        <figure class="figure figure--plate wm-panel" aria-labelledby="wm-panel-cap">
          <ol class="wm-readouts">
            <li class="wm-readout" style="--v:{facts.t.cardNum('pico.t01')}"><span class="wm-readout__label">Fault identification</span><b>{@html facts.card('pico.t01')}</b><span class="wm-readout__unit">macro-F1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.t01')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('pico.t02')}"><span class="wm-readout__label">Process state</span><b>{@html facts.card('pico.t02')}</b><span class="wm-readout__unit">macro-F1</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.t02')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('pico.t05')}"><span class="wm-readout__label">Tag truthfulness, before calibration</span><b>{@html facts.card('pico.t05')}</b><span class="wm-readout__unit">macro-recall</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.t05')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('pico.t05-cal')}"><span class="wm-readout__label">Tag truthfulness, with the shipped per-class calibration</span><b>{@html facts.card('pico.t05-cal')}</b><span class="wm-readout__unit">macro-recall</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.t05-cal')}</span></li>
            <li class="wm-readout" style="--v:{facts.t.cardNum('pico.mesh')}"><span class="wm-readout__label">Fleet reading, IEB-Mesh</span><b>{@html facts.card('pico.mesh')}</b><span class="wm-readout__unit">macro</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.mesh')}</span></li>
            <li class="wm-readout" data-better="lower" style="--v:{facts.t.cardHi('pico.false-alarm')}"><span class="wm-readout__label">False alarm on a healthy frame, per read (previous release)</span><b>{@html facts.card('pico.false-alarm')}</b><span class="wm-readout__unit">lower is better</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.false-alarm')}</span></li>
            <li class="wm-readout" data-better="lower" style="--v:{facts.t.cardNum('pico.dup')}"><span class="wm-readout__label">Duplicated-line frames accepted wrong</span><b>{@html facts.card('pico.dup')}</b><span class="wm-readout__unit">lower is better</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.dup')}</span></li>
          </ol>
          <figcaption class="fig mono" id="wm-panel-cap">FIG. 3 - The readouts, each on a scale of 0 to 100. Each bar is the score; the red tick marks it.</figcaption>
        </figure>

        <p class="wm-compare"><b>Against a popular open base of its size, fine-tuned identically:</b>
           {@html facts.card('pico.comparator')} to Wave Pico on fault identification, read on the previous
           Pico release with the same data and the same protocol
           <span class="wm-ref">{@html facts.cardRef('pico.comparator')}</span>.</p>

        <aside class="wm-footnote" aria-labelledby="wm-notes-head">
          <span class="research-kicker" id="wm-notes-head">Measured</span>
          <ol class="wm-notes">{@html facts.cardNotes('pico')}</ol>
        </aside>
      </div>
    </section>

    <section class="section band band--inset" id="limits">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;4 / LIMITS</span>
          <h2>Where it stops.</h2>
          <p>Said plainly, so nobody has to find them the hard way.</p>
        </div>
        <ul class="wm-limits">{@html facts.cardList('pico.limits')}</ul>
      </div>
    </section>

    <section class="section" id="adapter">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;5 / ADAPTER</span>
          <h2>One adapter, routed per request.</h2>
          <p>The fire adapter is a {@html facts.card('pico.adapter-size')} LoRA adapter for this base. The
             server loads it beside the model and switches it on only for the requests that
             ask for it. It is never merged into the base, so the industrial reading stays
             exactly as measured above.</p>
        </div>
        <ol class="wm-readouts wm-readouts--fire">
          <li class="wm-readout" style="--v:{facts.t.cardNum('pico.fire-full')}"><span class="wm-readout__label">Threat call / escalation, full sensor suite</span><b>{@html facts.card('pico.fire-full')}</b><span class="wm-readout__unit">simulator-internal</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.fire-full')}</span></li>
          <li class="wm-readout" style="--v:{facts.t.cardNum('pico.fire-nocam')}"><span class="wm-readout__label">Threat call / escalation, without a camera</span><b>{@html facts.card('pico.fire-nocam')}</b><span class="wm-readout__unit">simulator-internal</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.fire-nocam')}</span></li>
          <li class="wm-readout" style="--v:{facts.t.cardNum('pico.fire-gate')}"><span class="wm-readout__label">Gate lines passing, in each configuration</span><b>{@html facts.card('pico.fire-gate')}</b><span class="wm-readout__unit">simulator-internal</span><i class="wm-meter" aria-hidden="true"></i><span class="wm-ref">{@html facts.cardRef('pico.fire-gate')}</span></li>
        </ol>
      </div>
    </section>

    <WaveMesh no='6' fig='4' parent='Wave Pico' />

    <WaveFirewatch no='7' fig='5' />

    <section class="section research-closing">
      <div class="wrap">
        <h2>Where Wave Pico sits.</h2>
        <p>The smallest tier of a family that runs from a board beside one machine to a
           datacenter. Wave Nano is the next size up.</p>
        <div class="research-actions research-actions--lead">
          <a class="research-button" href="/research-wave-nano.html">Wave Nano</a>
          <a class="research-button" href="/research-wave-family.html">The Wave family</a>
          <a class="research-button" href="/research-models.html">Models and status</a>
        </div>
      </div>
    </section>

  </main>

  <Footer anchors='/' />
