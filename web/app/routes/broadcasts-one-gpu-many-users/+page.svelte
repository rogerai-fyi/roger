<script>
  // How many people can one GPU serve? (local LLM concurrency)
  // Ported from web/src/broadcasts-one-gpu-many-users.html (2026-10-29); the copy is the legacy copy.
  // A broadcast on the one post layout (lib/ui/BroadcastPost.svelte): the head and the header
  // from `post`, the FAQ from post.faq (the page and the FAQPage schema from one list), the
  // body as the post wrote it.
  import BroadcastPost from "#lib/ui/BroadcastPost.svelte";
  import Faq from "#lib/ui/Faq.svelte";
  import Signoff from "#lib/ui/Signoff.svelte";
  const post = {
    slug: "broadcasts-one-gpu-many-users",
    number: "005",
    title: "How many people can one GPU serve? (local LLM concurrency)",
    heading: "How many people can one GPU serve?",
    desc: "The single-stream tokens-per-second number everyone quotes is the wrong test. Here is what actually happens when a local GPU serves more than one person at once, why aggregate throughput nearly doubles while each user slows down, and why a single-user rig wastes most of its capacity.",
    dek: "Everyone benchmarks a local model with one prompt and one stream. It is the wrong test for anything real. Here is what actually happens under concurrency, with real numbers.",
    date: "2026-07-07",
    dateLabel: {"text":"JULY 2026","rev":"2026.07"},
    ogimage: "https://rogerai.fm/og.png",
    ogtitle: "How many people can one GPU serve?",
    ogdesc: "Real benchmark data on local LLM concurrency: aggregate throughput nearly doubles under batching while per-user speed halves - and why serving one person wastes most of a GPU.",
    faq: [
      {"q":"How many concurrent users can one GPU handle?","a":"It depends on the model and VRAM, but aggregate throughput keeps rising until compute or memory bandwidth saturates. On our bench a single 4x RTX PRO 4500 rig running gpt-oss-120b nearly doubled its total tokens/sec going from 1 to 8 concurrent users, while per-user speed more than halved."},
      {"q":"Does serving more users make a GPU faster?","a":"No. Batching does not speed the GPU up - it keeps it fuller. Total throughput rises because idle gaps between one request's tokens get filled with another request's work. Each individual user actually gets a slower turn."},
      {"q":"Why is my local model slow with only one user?","a":"A single stream leaves the GPU idle between tokens, so you only see a fraction of its real throughput. That is expected: one user cannot keep a capable GPU busy. The capacity is there, waiting for more concurrent requests than one person generates."},
      {"q":"Should I benchmark local serving with one stream?","a":"Only if you truly serve one user at a time. For anything with a queue or multiple users, measure aggregate throughput under concurrency - that is what determines how much work the box can actually do."},
      {"q":"What is continuous batching?","a":"A serving technique (used by vLLM and others) that interleaves many requests so the GPU's compute stays saturated, instead of finishing one request before starting the next. It is why aggregate throughput scales with concurrency."},
      {"q":"How do I serve a model to more people than my GPU can handle?","a":"Route the overflow to a bigger or additional GPU without leaving your workflow. roger use <model> proxies to a community node over the same OpenAI-compatible API, so your app is unchanged.","aHtml":"Route the overflow to a bigger or additional GPU without leaving your workflow. <code>roger use &lt;model&gt;</code> proxies to a community node over the same OpenAI-compatible API, so your app is unchanged."},
    ],
  };
</script>
<BroadcastPost {...post} renderFaq={false} signoff={false}>
<!-- Field-guide broadcast. SEO/AEO format: Quick Answer + question-H2s + FAQ schema + internal
     links to /models, /bands, the install command. Registered in build.mjs CSS_BUNDLES and listed
     at the top of broadcasts.html as BROADCAST 005. Data: real ai-benchy concurrency sweep. 
     (the page's doc comment, from the legacy file; its CSS bundle is the css: list above) -->


      <!-- contents: the shared toc-tuner; its names repeat the section labels below -->
      <nav class="toc-tuner" data-tuner aria-label="Sections">
        <div class="wrap toc-tuner__band">
        <div class="toc-tuner__readout" aria-hidden="true"></div>
        <ol class="toc-tuner__scale">
          <li><a class="toc-tuner__st" href="#s1"><b>§1</b><span class="toc-tuner__name">WHY IS THE SINGLE-STREAM BENCHMARK MISLEADING?</span></a></li>
          <li><a class="toc-tuner__st" href="#s2"><b>§2</b><span class="toc-tuner__name">WHAT HAPPENS WHEN YOU SERVE MORE THAN ONE PERSON?</span></a></li>
          <li><a class="toc-tuner__st" href="#s3"><b>§3</b><span class="toc-tuner__name">WHY DOES TOTAL THROUGHPUT GO UP WHILE EACH USER SLOWS DOWN?</span></a></li>
          <li><a class="toc-tuner__st" href="#s4"><b>§4</b><span class="toc-tuner__name">IS A SINGLE-USER HOME RIG WASTEFUL, THEN?</span></a></li>
          <li><a class="toc-tuner__st" href="#s5"><b>§5</b><span class="toc-tuner__name">WHEN DOES THE BATCHING TRADE MAKE SENSE?</span></a></li>
          <li><a class="toc-tuner__st" href="#s6"><b>§6</b><span class="toc-tuner__name">HOW DO YOU GET MORE REQUESTS THAN ONE PERSON CAN GENERATE?</span></a></li>
          <li><a class="toc-tuner__st" href="#s7"><b>§7</b><span class="toc-tuner__name">FAQ</span></a></li>
        </ol>
        <span class="toc-tuner__needle" aria-hidden="true"></span>
        </div>
      </nav>

      <blockquote class="bc-answer" data-reveal>
        Quick answer: on our bench, one GPU running gpt-oss-120b went from ~160 tokens/sec at a
        single user to ~310 at eight users. That is nearly double the total work from the same
        hardware. Each user's speed dropped from ~171 to ~79. More users means more total tokens,
        but a slower turn for everyone. Batching does not make a GPU faster; it makes it fuller.
      </blockquote>

      <div class="bc-post__body">

      <p class="bc-post__lead" data-reveal>
        If you have ever looked up "how fast is model X locally," you got a single-stream number:
        one prompt, one reply, tokens per second. That number is real, but it describes a situation
        almost no real deployment is in. The moment your GPU answers more than one request at a
        time, the picture changes completely - and almost nobody measures that. So we did.
      </p>

      <section class="bc-sec" id="s1" data-reveal>
        <span class="bc-sec__no">&sect;1</span>
        <h2>Why is the single-stream benchmark misleading?</h2>
        <p>
          A single stream leaves most of a modern GPU idle between tokens. The card is waiting on
          memory far more than it is computing, so one user never keeps it busy. The single-stream
          tokens-per-second figure is the <em>best case for one person</em>, not the ceiling of the
          hardware. Quote it and you will badly underestimate what the box can actually produce.
        </p>
      </section>

      <section class="bc-sec" id="s2" data-reveal>
        <span class="bc-sec__no">&sect;2</span>
        <h2>What happens when you serve more than one person?</h2>
        <p>
          We ran <code>gpt-oss-120b</code> on a 4x RTX PRO 4500 rig and swept the number of
          concurrent users from 1 to 8. Two things move at once, in opposite directions:
        </p>
        <ul>
          <li><b>Aggregate throughput climbs.</b> ~160 tokens/sec at one user, ~310 at eight. Nearly
            double the total work out of the exact same hardware.</li>
          <li><b>Per-user speed drops.</b> That single user had ~171 tokens/sec all to themselves.
            By eight concurrent users, each one is down to ~79. Less than half.</li>
        </ul>
        <p>That is the whole trade in two lines: more total tokens, a slower turn for everyone.</p>
      </section>

      <section class="bc-sec" id="s3" data-reveal>
        <span class="bc-sec__no">&sect;3</span>
        <h2>Why does total throughput go up while each user slows down?</h2>
        <p>
          It is called continuous batching - what runtimes like vLLM do under the hood. Instead of
          finishing one request before starting the next, the GPU interleaves many requests, filling
          the idle gaps between one user's tokens with another user's work. The expensive silicon
          stops waiting around, so the <em>aggregate</em> line rises. But that shared capacity is now
          split across everyone, so any single user waits a little longer for their turn.
        </p>
      </section>

      <section class="bc-sec" id="s4" data-reveal>
        <span class="bc-sec__no">&sect;4</span>
        <h2>Is a single-user home rig wasteful, then?</h2>
        <p>
          On pure throughput, yes. If you run a local model only for yourself, you are using a
          fraction of what the GPU can produce. That ~160 tok/s at one user is not the ceiling - the
          ceiling was ~310, and you leave most of it on the floor because you are the only one
          asking. A single-user rig is, by definition, the least efficient way to run inference
          hardware: you bought a machine that can serve eight people and you are serving one.
        </p>
      </section>

      <section class="bc-sec" id="s5" data-reveal>
        <span class="bc-sec__no">&sect;5</span>
        <h2>When does the batching trade make sense?</h2>
        <p>
          It depends on whether you are latency-bound or throughput-bound. For a queue or a
          background job that tolerates a 10-45 second turn - overnight processing, a chatbot for a
          group, batch extraction - batching is a clear win: you clear far more work per hour. For a
          real-time voice assistant that needs the fastest possible single reply, concurrency hurts,
          and you want the single-stream speed instead. Know which one you are.
        </p>
      </section>

      <section class="bc-sec" id="s6" data-reveal>
        <span class="bc-sec__no">&sect;6</span>
        <h2>How do you get more requests than one person can generate?</h2>
        <p>
          This is the quiet argument for pooling. A GPU serving one household wastes most of its
          throughput; the same GPU fed a steady stream of requests from many people runs near its
          real capacity, and every one of those extra tokens costs almost nothing because the
          hardware and power were already committed. That is the bet RogerAI is built on: put your
          idle capacity on the air, or tune in to someone else's when yours is not enough - over the
          same OpenAI-compatible API, with <code>roger use &lt;model&gt;</code>. See what people are
          actually running in the <a href="/models.html">model directory</a> and which
          <a href="/bands.html">bands</a> are live.
        </p>
      </section>

      <Faq no={7} items={post.faq} />

      <Signoff lead={"<p>Skip the setup or share your local model - install the set and tune in:</p>"}>curl <span class="tok">-fsSL</span> <span class="tok">https://rogerai.fm/install.sh</span> <span class="tok">| sh</span></Signoff>
    </div>
</BroadcastPost>
