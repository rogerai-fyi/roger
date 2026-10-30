<script>
  // Metrics - RogerAI
  // Ported from web/src/usage.html (2026-10-29); the copy is the legacy copy, the card and the
  // sign-in gate are the metrics island (lib/usage/UsageApp.svelte).
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import UsageApp from "#lib/usage/UsageApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Metrics - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/usage.html',
    css: [...CSS_ACCOUNT, 'metrics.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' />
  <Island name="usage" component={UsageApp} />
  <Footer anchors='/' />
