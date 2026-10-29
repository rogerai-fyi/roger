<script>
  // Account - RogerAI
  // Ported from web/src/account.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import AccountApp from "#lib/account/AccountApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Account - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/account.html',
    css: [...CSS_ACCOUNT, 'account.css'],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' />
<Island name="account" component={AccountApp} />
  <Footer anchors='/' />
