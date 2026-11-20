<script>
  // Why route through Roger - RogerAI
  // Ported from web/src/why.html (2026-10-29); the copy is the legacy copy.
  import { siteHead, CSS_SHARED, CSS_ACCOUNT } from "#lib/head.js";
  import Footer from "#lib/ui/Footer.svelte";
  import Nav from "#lib/ui/Nav.svelte";
  import OnAir from "#lib/ui/OnAir.svelte";
  const head = siteHead({
    title: 'Why route through Roger - RogerAI',
    desc: 'The why moved in with the money: what the routing fee buys, the honest comparison, and how your identity stays off the waves - now at the top of the pricing page.',
    robots: "noindex",
    canonical: 'https://rogerai.fm/why.html',
    css: [...CSS_SHARED, 'research.css'],
  });
</script>
<svelte:head>{@html head}</svelte:head>
  <Nav variant='marketing' home='/' promo={false} />
  <main id="top">
    <section class="research-hero">
      <div class="wrap">
        <span class="hero__eyebrow">
          <OnAir />
          <span class="hero__callsign">ROGERAI</span>
          <span class="hero__sep" aria-hidden="true">&middot;</span>
          moved
        </span>
        <h1>The why moved in with the money.</h1>
        <p class="research-hero__lead">What the routing fee buys, the comparison, and how
          your identity stays off the waves now open the pricing page - value first, then
          the rules and the rates it justifies.</p>
        <div class="research-actions research-actions--lead">
          <a class="research-button" href="/pricing.html#why">Why route through us <span aria-hidden="true">&rarr;</span></a>
        </div>
      </div>
    </section>
  </main>
  <Footer anchors='/' />
