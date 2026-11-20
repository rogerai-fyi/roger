<script>
  // Dashboard - RogerAI
  // Ported from web/src/dashboard.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import DashboardApp from "#lib/dashboard/DashboardApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Dashboard - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/dashboard.html',
    css: [...CSS_ACCOUNT, 'dashboard.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' />
<Island name="dashboard" component={DashboardApp} />
  <Footer anchors='/' />
