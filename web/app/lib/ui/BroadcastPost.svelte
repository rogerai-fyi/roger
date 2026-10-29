<script>
  // The broadcast post layout: one chrome for every post, where sixteen legacy posts carried
  // it by copy. A Markdown post (+page.md, `layout: broadcast`) passes its frontmatter; a post
  // whose body is Svelte markup passes a `post` object and places its own <Faq/> and
  // <Signoff/> (renderFaq={false} signoff={false}). The layout writes the head (with the
  // FAQPage JSON-LD from `faq`), the rail, the nav, the article wrap with the back link, the
  // header (number, kind, date, title, dek, the post's meta block if it passes one as a snippet), an optional hero figure,
  // the body, then (unless the post places them) the FAQ and the sign-off, and the footer.
  import { siteHead, CSS_SHARED } from "#lib/head.js";
  import Nav from "#lib/ui/Nav.svelte";
  import Footer from "#lib/ui/Footer.svelte";
  import Rail from "#lib/ui/Rail.svelte";
  import OnAir from "#lib/ui/OnAir.svelte";
  import Faq from "#lib/ui/Faq.svelte";
  import Signoff from "#lib/ui/Signoff.svelte";
  // title is the <title> and the card, text; heading (the h1 when a post words it apart) and
  // dek are the post's own markup, as its route or frontmatter wrote them; ogtitle is the
  // card's title when a post words it apart from the <title>
  let { title, heading, ogtitle, desc, number, kind = "FIELD GUIDE", date, dateLabel, railHead = "RogerAI · Field Guide", slug, ogimage, dek, meta,
    hero, heroAlt, heroCaption, faq = [], faqNo = 0, faqTitle, faqLabel, renderFaq = true, signoff = true, ogdesc, css = [], scripts = [], children } = $props();
  const url = $derived(`https://rogerai.fm/${slug}.html`);
  const head = $derived(siteHead({ title, desc, og: { type: "article", title: ogtitle || title, desc: ogdesc || desc, url, image: ogimage }, css: [...CSS_SHARED, "broadcasts.css", ...css], scripts: [{ runtime: true }, ...scripts] }));
  const faqLd = $derived(JSON.stringify({ "@context": "https://schema.org", "@type": "FAQPage",
    mainEntity: faq.filter((q) => q.schema !== false).map((q) => ({ "@type": "Question", name: q.q, acceptedAnswer: { "@type": "Answer", text: q.a } })) }));
</script>
<svelte:head>{@html head}{#if faq.length}{@html '<script type="application/ld+json">' + faqLd + '<\/script>'}{/if}</svelte:head>
<Rail head={railHead} rev="Rev. {dateLabel.rev} · ((•))" orange />

<Nav variant="marketing" />

<main id="top">
  <article class="bc-post">
    <div class="wrap bc-post__wrap">
      <a class="bc-post__back mono" href="/broadcasts.html">&larr; all broadcasts</a>
      <header class="bc-post__head" data-reveal>
        <span class="hero__eyebrow">
          <OnAir />
          <span class="hero__callsign">BROADCAST {number}{kind ? " \u00b7 " + kind : ""}</span>
          <span class="hero__sep" aria-hidden="true">&middot;</span>
          <time datetime={date}>{dateLabel.text}</time>
        </span>
        <h1 class="bc-post__title">{#if heading}{@html heading}{:else}{title}{/if}</h1>
        <p class="bc-post__dek">{@html dek}</p>
        {#if meta}{@render meta()}{/if}
      </header>
      {#if hero}
      <figure class="figure figure--plate" data-reveal>
        <img src="assets/broadcasts/{hero}.webp" srcset="assets/broadcasts/{hero}-800.webp 800w, assets/broadcasts/{hero}.webp 1280w" sizes="(max-width: 1000px) 100vw, 960px" width="1280" height="720" alt={heroAlt} fetchpriority="high" />
        <figcaption class="fig mono">{heroCaption}</figcaption>
      </figure>
      {/if}
      {@render children?.()}
      {#if renderFaq && faq.length}<Faq no={faqNo} items={faq} title={faqTitle} label={faqLabel} />{/if}
      {#if signoff}<Signoff />{/if}
    </div>
  </article>
</main>

<Footer anchors="/" />
