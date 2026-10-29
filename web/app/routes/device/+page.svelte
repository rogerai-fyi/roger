<script>
  // Sign in a device - RogerAI
  // Ported from web/src/device.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Island from "#lib/ui/Island.svelte";
  import DeviceApp from "#lib/account/DeviceApp.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Sign in a device - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/device.html',
    css: [...CSS_ACCOUNT, 'device.css'],
    scripts: [{ runtime: true, promo: false }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' promo={false} />
<Island name="device" component={DeviceApp} />
  <Footer />
