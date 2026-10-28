<script>
  import Orange3d from "./Orange3d.svelte";
  let { head, rev, orange = false } = $props();
</script>
<div class="rail" aria-hidden="true">
    <span class="rail__head">{head}</span>
    <span class="rail__rev">{#if orange}<Orange3d id="rail" /> {/if}{rev}</span>
  </div>
