<script>
  // THE WILDFIRE WATCH, as designed: shared by every Wave model page (legacy: _partials/wave-firewatch.html).
  // Warning and mitigation, never fire protection; the model never actuates equipment.
  let { no, fig } = $props();
</script>
<section class="section" id="wildfire">
      <div class="wrap">
        <div class="section__head section__head--left">
          <span class="sectionno">&sect;{no} / WILDFIRE</span>
          <h2>The wildfire watch, as designed.</h2>
          <p>Warning and mitigation, never fire protection. In this design the models warn
             and explain; the model never actuates equipment.</p>
        </div>

        <div class="device-contract">
          <p><b>The design.</b> Wave Pico would sit on the mast, reading particulates,
             carbon monoxide, volatile organics, thermal, the camera module&rsquo;s outputs
             and the sprinkler controller&rsquo;s state. Wave Nano would sit on an edge box
             with a camera smoke detector and read the whole site. A larger tier would
             weigh all of it.</p>
          <p><b>What is measured, and where.</b> The routed fire adapters and the
             fire-site rollup are measured in our simulator, so every fire number on this
             page is simulator-internal. False-alarm rates at a real site over months are
             not measured yet, and nothing here is a claim about suppression.</p>
          <p><b>Pre-wet is the owner&rsquo;s rule.</b> The owner arms it in advance. The
             controller, not the model, executes that owner-armed rule, with its own abort
             window, and only after the escalation ladder has been tried and no person
             acknowledged. The model&rsquo;s part is to mark the site pre-wet eligible; it
             never opens a valve.</p>
        </div>

        <figure class="figure figure--plate wm-ladder-fig" aria-labelledby="wm-ladder-cap">
          <ol class="steps wm-ladder">
            <li><b>Notify.</b> The owner is told, with the reading that raised it.</li>
            <li><b>Escalate.</b> Every path the owner set up is tried, in order.</li>
            <li><b>Human-ack pending.</b> The watch waits for a person to answer.</li>
            <li><b>Pre-wet eligible.</b> Owner-armed: the controller may execute the owner&rsquo;s rule. The model only marks it.</li>
            <li><b>Handed to a person.</b> An acknowledgement at any step ends here.</li>
          </ol>
          <figcaption class="fig mono" id="wm-ladder-cap">FIG. {fig} - The escalation ladder. The model climbs it with records; a person, or the owner&rsquo;s armed rule, acts.</figcaption>
        </figure>

        <p class="wm-aside">This is not a fire alarm or a sprinkler system, and it is never in
           the control path of one. It does not replace code-required detection or
           suppression, or the fire service.</p>
      </div>
    </section>
