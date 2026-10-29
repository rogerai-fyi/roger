<script>
  // Base Station - RogerAI
  // Ported from web/src/private.html (2026-10-29); the copy is the legacy copy, the card is the
  // Base Station island (lib/private/PrivateApp.svelte).
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import PrivateApp from "#lib/private/PrivateApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Base Station - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/private.html',
    css: [...CSS_ACCOUNT, 'console.css', 'private.css'],
    scripts: [{ runtime: true, promo: false }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' promo={false} />
  <Island name="private" component={PrivateApp} />
  <Footer anchors='/' />
