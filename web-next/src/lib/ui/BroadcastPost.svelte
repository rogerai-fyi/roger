<script>
  // The broadcast post layout (mdsvex). Sixteen legacy posts carried this chrome by copy;
  // a post is now Markdown with frontmatter and this one layout.
  import { siteHead, CSS_SHARED } from "#lib/head.js";
  import Nav from "#lib/ui/Nav.svelte";
  import Footer from "#lib/ui/Footer.svelte";
  import Rail from "#lib/ui/Rail.svelte";
  import OnAir from "#lib/ui/OnAir.svelte";
  import InstallBox from "#lib/ui/InstallBox.svelte";
  let { title, desc, number, kind = "FIELD GUIDE", date, dateLabel, slug, ogimage, dek, hero, heroAlt, heroCaption, faq = [], faqNo = 0, ogdesc, css = [], children } = $props();
  const url = `https://rogerai.fm/${slug}.html`;
  const head = siteHead({ title, desc, og: { type: "article", title, desc: ogdesc || desc, url, image: ogimage }, css: [...CSS_SHARED, "broadcasts.css", ...css] });
  const faqLd = JSON.stringify({ "@context": "https://schema.org", "@type": "FAQPage",
    mainEntity: faq.map((q) => ({ "@type": "Question", name: q.q, acceptedAnswer: { "@type": "Answer", text: q.a } })) });
</script>
<svelte:head>{@html head}{#if faq.length}{@html '<script type="application/ld+json">' + faqLd + '<\/script>'}{/if}</svelte:head>
<Rail head="RogerAI · Field Guide" rev="Rev. {dateLabel.rev} · ((•))" orange />

<Nav variant="marketing" />

<main id="top">
  <article class="bc-post">
    <div class="wrap bc-post__wrap">
      <a class="bc-post__back mono" href="/broadcasts.html">&larr; all broadcasts</a>
      <header class="bc-post__head" data-reveal>
        <span class="hero__eyebrow">
          <OnAir />
          <span class="hero__callsign">BROADCAST {number} &middot; {kind}</span>
          <span class="hero__sep" aria-hidden="true">&middot;</span>
          <time datetime={date}>{dateLabel.text}</time>
        </span>
        <h1 class="bc-post__title">{title}</h1>
        <p class="bc-post__dek">{dek}</p>
      </header>
      {#if hero}
      <figure class="figure figure--plate" data-reveal>
        <img src="assets/broadcasts/{hero}.webp" srcset="assets/broadcasts/{hero}-800.webp 800w, assets/broadcasts/{hero}.webp 1280w" sizes="(max-width: 1000px) 100vw, 960px" width="1280" height="720" alt={heroAlt} fetchpriority="high" />
        <figcaption class="fig mono">{heroCaption}</figcaption>
      </figure>
      {/if}
      {@render children?.()}
      {#if faq.length}
      <section class="bc-sec" id="s{faqNo}" data-reveal>
        <span class="bc-sec__no">&sect;{faqNo} &middot; FAQ</span>
        <h2>Frequently asked questions</h2>
        <dl class="bc-faq">
          {#each faq as q}<dt><b>{q.q}</b></dt><dd>{@html q.aHtml || q.a}</dd>{/each}
        </dl>
      </section>
      {/if}
      <aside class="tint-panel bc-signoff">
        <span class="tint-panel__tag">roger that</span>
        <p>Start your own station or just tune in - install the set:</p>
        <div class="install">
          <InstallBox id="signoffInstall" cmd='curl <span class="tok">-fsSL</span> <span class="tok">https://rogerai.fm/install.sh</span> <span class="tok">| sh</span>' />
        </div>
      </aside>
    </div>
  </article>
</main>

<Footer />
