<script>
  // "Bands" is now "Models". Redirect old links (and #band= QSL hashes) to /models.html; the
  // script carries any #band=<model> hash across (script-src 'self'). Ported from
  // web/src/bands.html: a redirect shell with the shared stylesheets only, no site chrome.
  import { CSS_SHARED } from "#lib/head.js";
</script>
<svelte:head>
  <title>Models - RogerAI</title>
  <meta name="robots" content="noindex" />
  <meta http-equiv="refresh" content="0; url=/models.html" />
  <link rel="canonical" href="/models.html" />
  {@html CSS_SHARED.map((f) => `<link rel="stylesheet" href="styles/${f}" />`).join("\n  ")}
  {@html '<script src="js/redirect-models.js"></script>'}
</svelte:head>
<main id="top">
  <section class="section" style="padding-block: 18vh;">
    <div class="wrap" style="text-align:center;">
      <p class="mono" style="color: var(--ink-500);">((•)) the band moved.</p>
      <h1 style="margin: var(--s-3) 0 var(--s-4);">Bands are now Models.</h1>
      <p style="color: var(--ink-500);">
        Taking you to <a href="/models.html">the model directory</a>&hellip;
      </p>
    </div>
  </section>
</main>
