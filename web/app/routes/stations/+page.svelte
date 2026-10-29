<script>
  // Your stations - RogerAI
  // Ported from web/src/stations.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import StationsApp from "#lib/account/StationsApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Your stations - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/stations.html',
    css: [...CSS_ACCOUNT, 'stations.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' promo={false} />
<Island name="stations" component={StationsApp} />
  <Footer />
