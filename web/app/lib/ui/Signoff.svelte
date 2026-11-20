<script>
  // A broadcast's sign-off: the "roger that" plate every post ends with. The lead (and a
  // tail after the install box) are the post's own markup; the install box is the plain set
  // unless the post gives its own command (children) or asks for none.
  import InstallBox from "#lib/ui/InstallBox.svelte";
  let { tag = "roger that", lead = "<p>Start your own station or just tune in - install the set:</p>", install = true, tail = "", oslock, label, children } = $props();
</script>
<aside class="tint-panel bc-signoff">
  <span class="tint-panel__tag">{tag}</span>
  {@html lead}
  {#if install}
  <div class="install">
    <InstallBox id="signoffInstall" {oslock} {label}>{#if children}{@render children()}{:else}curl <span class="tok">-fsSL</span> <span class="tok">https://rogerai.fm/install.sh</span> <span class="tok">| sh</span>{/if}</InstallBox>
  </div>
  {/if}
  {#if tail}{@html tail}{/if}
</aside>
