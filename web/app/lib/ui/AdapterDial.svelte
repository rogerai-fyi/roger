<script>
  // The adapter dial (features/web/sensor_positioning.feature). Every name, stage and line is
  // a capability fact from src/data/capabilities.json, so the homepage and the industrial page
  // read one file; test/capabilities.test.mjs holds every lamp to it. Legacy: _partials/adapter-dial.html.
  import { content } from "#lib/content.js";
  const facts = content("adapter-dial");
</script>
<ol class="spectrum spectrum--dial" data-scrub aria-label="Adapters and skills, with their status">
  <li data-cap="fire-warning" data-cap-stage="{facts.t.cap('fire-warning.stage')}"><i class="cap-lamp" aria-hidden="true"></i><span><b>{facts.t.cap('fire-warning.name')}</b><span class="cap-stage">{facts.t.cap('fire-warning.label')}</span>{facts.t.cap('fire-warning.description')}</span></li>
  <li data-cap="telemetry-drift" data-cap-stage="{facts.t.cap('telemetry-drift.stage')}"><i class="cap-lamp" aria-hidden="true"></i><span><b>{facts.t.cap('telemetry-drift.name')}</b><span class="cap-stage">{facts.t.cap('telemetry-drift.label')}</span>{facts.t.cap('telemetry-drift.description')}</span></li>
  <li data-cap="custom" data-cap-stage="{facts.t.cap('custom.stage')}"><i class="cap-lamp" aria-hidden="true"></i><span><b>{facts.t.cap('custom.name')}</b><span class="cap-stage">{facts.t.cap('custom.label')}</span>{facts.t.cap('custom.description')}</span></li>
</ol>
