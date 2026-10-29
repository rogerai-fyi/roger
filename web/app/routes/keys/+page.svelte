<script>
  // API keys - RogerAI
  // Ported from web/src/keys.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import KeysApp from "#lib/keys/KeysApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'API keys - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/keys.html',
    css: [...CSS_ACCOUNT, 'metrics.css', 'keys.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' />
<Island name="keys" component={KeysApp} />
  <Footer anchors='/' />
