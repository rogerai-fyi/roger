<script>
  // One time-of-use window in the schedule editor. `win` is bound: the parent reads it back.
  import { priceStr } from "./format.js";
  let { win = $bindable(), ceil, onremove } = $props();
</script>
<div class="md-win">
  <input class="md-days" type="text" placeholder="every day" bind:value={win.days}
         title="Weekdays 0-6 (Sun-Sat), comma-separated. Blank = every day." />
  <input class="md-time" type="time" bind:value={win.start} />
  <span class="md-dash">–</span>
  <input class="md-time" type="time" bind:value={win.end} />
  <input class="md-in" type="number" min="0" step="0.01" max={ceil.in || null} placeholder="in" bind:value={win.price_in} disabled={win.free} />
  <input class="md-in" type="number" min="0" step="0.01" max={ceil.out || null} placeholder="out" bind:value={win.price_out} disabled={win.free} />
  <label class="md-free"><input type="checkbox" bind:checked={win.free} /><span>free</span></label>
  <button class="md-x" type="button" title="Remove window" onclick={onremove}>×</button>
</div>
