<script>
  // Link a session - RogerAI
  // Ported from web/src/r.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  const head = siteHead({
    title: 'Link a session - RogerAI',
    robots: "noindex",
    canonical: 'https://rogerai.fm/r.html',
    css: [...CSS_ACCOUNT],
    scripts: [{ runtime: true, promo: false }, { src: 'js/r.js', defer: true }],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='lean' home='/' promo={false} />
  <div class="authwrap tint-panel">
  <main class="card" id="card">
    <h1>Link a session</h1>
    <p class="sub" id="msg">Checking the link&hellip;</p>
    <p class="fine" id="hint" hidden>This links THIS device to a remote agent session on your
      account. Private - account-locked, and carried over TLS to the broker that relays it - encrypted in transit, not end-to-end.</p>
  </main>
  </div>
  <Footer anchors='/' />
