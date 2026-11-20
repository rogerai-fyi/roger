<script>
  // Sign in - RogerAI
  // Ported from web/src/login.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import LoginApp from "#lib/account/LoginApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Sign in - RogerAI',
    desc: 'Sign in to RogerAI to monetize your spare local models. Consuming needs no account.',
    robots: "noindex",
    canonical: 'https://rogerai.fm/login.html',
    css: [...CSS_ACCOUNT],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' />
<Island name="login" component={LoginApp} />
  <Footer anchors='/' />
