<script>
  // Payouts - RogerAI
  // Ported from web/src/payouts.html (2026-10-29); the copy is the legacy copy, the card is
  // the payouts island (lib/account/PayoutsApp.svelte).
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import PayoutsApp from "#lib/account/PayoutsApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Payouts - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/payouts.html',
    css: [...CSS_ACCOUNT, 'payouts.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' />
  <Island name="payouts" component={PayoutsApp} />
  <Footer anchors='/' />
