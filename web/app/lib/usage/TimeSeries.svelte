<script>
  // One interactive time-series chart (legacy: buildTimeSeries in web/src/js/metrics.js). The
  // crosshair follows the pointer and the arrow keys (Home/End jump, Escape clears); the
  // focused bucket is `active`, bound by the figure that renders the readout.
  import { geometry, nearest, W, H } from "./series.js";
  let { points, series, stack = false, aria = "Time series", active = $bindable(-1) } = $props();
  const geo = $derived(geometry(points, series, stack));
  let svg;
  const focus = (i) => { active = i < 0 || i >= geo.n ? -1 : i; };
  const blur = () => { active = -1; };
  function nearestIndex(clientX) {
    const rect = svg.getBoundingClientRect();
    if (!rect.width) return 0;
    return nearest(geo, ((clientX - rect.left) / rect.width) * W);   // back into viewBox units
  }
  function onkeydown(e) {
    if (!geo.n) return;
    if (e.key === "ArrowLeft") { e.preventDefault(); focus(active <= 0 ? 0 : active - 1); }
    else if (e.key === "ArrowRight") { e.preventDefault(); focus(active < 0 ? 0 : Math.min(geo.n - 1, active + 1)); }
    else if (e.key === "Home") { e.preventDefault(); focus(0); }
    else if (e.key === "End") { e.preventDefault(); focus(geo.n - 1); }
    else if (e.key === "Escape") blur();
  }
</script>
<svg class="mx-ts__svg" viewBox={geo.viewBox} preserveAspectRatio="none" role="img" tabindex="0" aria-label={aria} bind:this={svg}
  onpointermove={(e) => { if (geo.n) focus(nearestIndex(e.clientX)); }} onpointerleave={blur}
  onfocus={() => { if (geo.n && active < 0) focus(geo.n - 1); }} onblur={blur} {onkeydown}>
  <line class="mx-ts__axis" x1={geo.axis.x1} y1={geo.axis.y1} x2={geo.axis.x2} y2={geo.axis.y2}></line>
  {#each geo.layers as l}
    <path class="mx-ts__area mx-ts__area--{l.cls}" d={l.area}></path>
    {#if l.line}
      <path class="mx-ts__line mx-ts__line--{l.cls}" d={l.line}></path>
      {#each l.dots as d}<circle class="mx-ts__dot mx-ts__dot--{l.cls}" cx={d.cx} cy={d.cy} r={l.r}></circle>{/each}
    {/if}
  {/each}
  <line class="mx-ts__cursor" x1={active >= 0 ? geo.xAt(active) : 0} y1={geo.cursor.y1} x2={active >= 0 ? geo.xAt(active) : 0} y2={geo.cursor.y2} visibility={active >= 0 ? "visible" : "hidden"}></line>
  <circle class="mx-ts__focus" r="3" cx={active >= 0 ? geo.xAt(active) : 0} cy={active >= 0 ? geo.topAt(active) : 0} visibility={active >= 0 ? "visible" : "hidden"}></circle>
  {#each geo.ticks as t}<text class="mx-ts__tick" x={t.x} y={H - 6} text-anchor={t.anchor}>{t.label}</text>{/each}
</svg>
