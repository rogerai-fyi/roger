<script>
  // Link a session - RogerAI
  // Ported from web/src/r.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import LinkApp from "#lib/account/LinkApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Link a session - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/r.html',
    css: [...CSS_ACCOUNT],
    scripts: [{ runtime: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' promo={false} />
<Island name="link" component={LinkApp} />
  <Footer anchors='/' />
