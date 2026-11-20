<script>
  // Billing - RogerAI
  // Ported from web/src/billing.html (2026-10-29); the copy is the legacy copy, the card and
  // the money-flow help modal are the billing island (lib/billing/BillingApp.svelte).
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import BillingApp from "#lib/billing/BillingApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Billing - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/billing.html',
    css: [...CSS_ACCOUNT, 'billing.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' />
  <Island name="billing" component={BillingApp} />
  <Footer anchors='/' />
