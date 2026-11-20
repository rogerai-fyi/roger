<script>
  // Connect your bots and services to RogerAI (OpenAI-compatible)
  // Ported from web/src/broadcasts-connect-bots-openai-api.html (2026-10-29); the copy is the legacy copy.
  // A broadcast on the one post layout (lib/ui/BroadcastPost.svelte): the head and the header
  // from `post`, the FAQ from post.faq (the page and the FAQPage schema from one list), the
  // body as the post wrote it.
  import BroadcastPost from "#lib/ui/BroadcastPost.svelte";
  import Faq from "#lib/ui/Faq.svelte";
  import Signoff from "#lib/ui/Signoff.svelte";
  const post = {
    slug: "broadcasts-connect-bots-openai-api",
    number: "007",
    title: "Connect your bots and services to RogerAI (OpenAI-compatible)",
    heading: "Connect your bots and services to RogerAI",
    desc: "RogerAI speaks the OpenAI API, so any bot or service that uses the OpenAI SDK works with a two-line change. How to mint a key, point your base_url, choose a model, and use streaming, tools, and vision.",
    dek: "If it speaks the OpenAI API, it already speaks RogerAI. Repatch two lines - a base URL and a key - and your bot is running on community GPUs.",
    date: "2026-07-08",
    dateLabel: {"text":"JULY 2026","rev":"2026.07"},
    ogimage: "https://rogerai.fm/assets/broadcasts/connect-hero.png",
    ogtitle: "Connect your bots to RogerAI (OpenAI-compatible)",
    ogdesc: "Any OpenAI-SDK bot or service works with a two-line change: mint a grant key, point base_url at RogerAI, pass a model id. Streaming, tools, and vision included.",
    faq: [
      {"q":"Is RogerAI OpenAI-compatible?","a":"Yes. It serves the OpenAI chat-completions API (plus audio), so any OpenAI-SDK bot or service works by changing only the base URL and API key."},
      {"q":"What base URL do I use?","a":"Either the hosted endpoint https://broker.rogerai.fm/v1 with a grant key, or the http://127.0.0.1:<port>/v1 endpoint that roger use prints on your own machine.","aHtml":"Either the hosted endpoint <code>https://broker.rogerai.fm/v1</code> with a grant key, or the <code>http://127.0.0.1:&lt;port&gt;/v1</code> endpoint that <code>roger use</code> prints on your own machine."},
      {"q":"Can a remote bot use someone else's model with just a grant key?","a":"No - a grant key is confined to your own nodes, so it can't reach another operator's hardware. To use the wider market from a remote or headless bot, run the roger CLI there (roger use): its signed identity can reach any band. Both paths still go through the broker.","schema":false,"aHtml":"No - a grant key is confined to your own nodes, so it can't reach another operator's hardware. To use the wider market from a remote or headless bot, run the <code>roger</code> CLI there (<code>roger use</code>): its signed identity can reach any band. Both paths still go through the broker."},
      {"q":"How do I get an API key?","a":"Run roger grant create --name my-bots. It mints a rog-grant_ key; the owner signs in once to create it, and the bot then uses it with no login. It is shown only once.","aHtml":"Run <code>roger grant create --name my-bots</code>. It mints a <code>rog-grant_</code> key; the owner signs in once to create it, and the bot then uses it with no login. It's shown only once."},
      {"q":"Does streaming and function calling work?","a":"Yes - stream=true streams over SSE, and tool/function calling works on nodes that pass the tool-call capability check. Vision (image inputs) works on nodes that accept images.","aHtml":"Yes - <code>stream=true</code> streams over SSE, and tool/function calling works on nodes that pass the tool-call capability check. Vision (image inputs) works on nodes that accept images."},
      {"q":"Which model id do I pass?","a":"An exact id from the live market, such as gpt-oss-120b. Find current ids with roger search or on the model directory; the broker routes your request to the cheapest node serving that model.","aHtml":"An exact id from the live market, such as <code>gpt-oss-120b</code>. Find current ids with <code>roger search</code> or on the model directory; the broker routes your request to the cheapest node serving that model."},
      {"q":"Are there rate limits?","a":"Yes. Roughly 120 requests per minute per identity by default, and each grant key has its own rate limit plus daily and monthly token caps that you configure."},
    ],
  };
</script>
<BroadcastPost {...post} renderFaq={false} signoff={false}>
<!-- Field-guide broadcast 007 (developer integration). SEO/AEO format: Quick Answer + question-H2s
     + FAQ schema + internal links. Modern-color turn (alternating with 006's character style):
     a Flux patch-panel/switchboard hero + an inline theme-adaptive SVG "point your base_url here"
     diagram. Listed atop broadcasts.html. Facts grounded in the
     broker + CLI (grant keys, /v1 routes, roger use). 
     (the page's doc comment, from the legacy file; its sheets are the css: list above) -->


      <figure class="figure figure--plate" data-reveal>
        <img src="assets/broadcasts/connect-hero.webp" srcset="assets/broadcasts/connect-hero-800.webp 800w, assets/broadcasts/connect-hero.webp 1280w" sizes="(max-width: 1000px) 100vw, 960px" width="1280" height="720"
             alt="A vintage patch-panel switchboard with many service cables plugging into one central jack lit with a red indicator, in the RogerAI operating-manual style." fetchpriority="high" />
        <figcaption class="fig mono">FIG. 1 - MANY SERVICES, ONE PATCH INTO THE BAND</figcaption>
      </figure>

      <!-- contents: the shared toc-tuner; its names repeat the section labels below -->
      <nav class="toc-tuner" data-tuner aria-label="Sections">
        <div class="wrap toc-tuner__band">
        <div class="toc-tuner__readout" aria-hidden="true"></div>
        <ol class="toc-tuner__scale">
          <li><a class="toc-tuner__st" href="#s1"><b>§1</b><span class="toc-tuner__name">THE TWO-LINE CHANGE</span></a></li>
          <li><a class="toc-tuner__st" href="#s2"><b>§2</b><span class="toc-tuner__name">KEYS</span></a></li>
          <li><a class="toc-tuner__st" href="#s3"><b>§3</b><span class="toc-tuner__name">TWO BASE URLS</span></a></li>
          <li><a class="toc-tuner__st" href="#s4"><b>§4</b><span class="toc-tuner__name">FEATURES</span></a></li>
          <li><a class="toc-tuner__st" href="#s5"><b>§5</b><span class="toc-tuner__name">BEFORE YOU SHIP</span></a></li>
          <li><a class="toc-tuner__st" href="#s6"><b>§6</b><span class="toc-tuner__name">FAQ</span></a></li>
        </ol>
        <span class="toc-tuner__needle" aria-hidden="true"></span>
        </div>
      </nav>

      <blockquote class="bc-answer" data-reveal>
        <b>Quick answer.</b> RogerAI is OpenAI-compatible, so any OpenAI-SDK bot connects by changing
        two lines - a base URL and a key. On the bot's own machine, <code>roger use &lt;model&gt;</code>
        prints a localhost endpoint that can reach <b>any</b> band on the market. For a remote or
        headless bot, mint a grant key with <code>roger grant create</code> and point at
        <code>https://broker.rogerai.fm/v1</code> - which serves <b>your own</b> GPUs. Streaming, tool
        calls, and vision all work.
      </blockquote>

      <div class="bc-post__body">

        <p class="bc-post__lead" data-reveal>
          You don't rewrite your bot to move it to RogerAI. The chat endpoint is the OpenAI shape your
          code already targets, so the migration is a base URL and an API key - the same edit you'd
          make to switch between any two OpenAI-compatible providers.
        </p>

        <section class="bc-sec" id="s1" data-reveal>
          <span class="bc-sec__no">&sect;1 &middot; The two-line change</span>
          <h2>How do I point a bot at RogerAI?</h2>
          <p>
            Change the client's <code>base_url</code> and <code>api_key</code>, keep everything else.
            With the OpenAI Python SDK:
          </p>
          <div class="code-block"><pre><code>from openai import OpenAI

client = OpenAI(
    base_url="https://broker.rogerai.fm/v1",
    api_key="rog-grant_...",          # from: roger grant create
)

resp = client.chat.completions.create(
    model="gpt-oss-120b",             # a model your key can reach
    messages=[&#123;"role": "user", "content": "roger, radio check"&#125;],
    stream=True,                       # SSE streaming works
)</code></pre></div>
          <p>
            That's the whole integration. The same two fields work from the JS SDK, LangChain,
            LlamaIndex, a Discord bot, or a plain <code>curl</code> with an
            <code>Authorization: Bearer</code> header. This hosted example uses a grant key, which
            serves <b>your own</b> GPUs; to reach the wider market from a bot, use the local endpoint
            from <code>roger use</code> - see &sect;3.
          </p>
          <figure class="figure figure--plate figure--chart" data-reveal>
            <div class="scroll-box scroll-box--diagram">
            <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 820 250" role="img"
                 aria-label="Diagram: your bot or service, using the OpenAI SDK, points its base_url at https://broker.rogerai.fm/v1 with a grant key; the RogerAI broker screens and routes the request to a community GPU that serves your model.">
              {@html `<style>
                .g-box{fill:currentColor;opacity:.04}.g-stroke{stroke:currentColor;fill:none;opacity:.32}
                .g-arrow{stroke:currentColor;opacity:.5;fill:none}.g-live{fill:var(--live)}
                .g-no{font-family:"JetBrains Mono",monospace;font-size:13px;letter-spacing:1px;fill:currentColor;opacity:.7}
                .g-h{font-family:"JetBrains Mono",monospace;font-size:15px;font-weight:700;fill:currentColor}
                .g-b{font-family:"JetBrains Mono",monospace;font-size:12px;fill:currentColor;opacity:.8}
                .g-cap{font-family:"JetBrains Mono",monospace;font-size:13px;letter-spacing:2px;fill:currentColor;opacity:.7}
                .g-dim{opacity:.7}
              </style>`}
              <text x="24" y="30" class="g-cap">FIG. 2 - POINT YOUR BASE_URL HERE</text>
              <line x1="24" y1="44" x2="796" y2="44" class="g-stroke"/>
              <rect x="30" y="80" width="220" height="118" rx="10" class="g-box"/><rect x="30" y="80" width="220" height="118" rx="10" class="g-stroke"/>
              <text x="48" y="110" class="g-no">YOUR BOT / SERVICE</text>
              <text x="48" y="140" class="g-h">OpenAI SDK</text>
              <text x="48" y="163" class="g-b">unchanged code -</text>
              <text x="48" y="181" class="g-b">just base_url + key</text>
              <path d="M250 139 H288" class="g-arrow"/><path d="M282 133 L290 139 L282 145" class="g-arrow"/>
              <rect x="295" y="80" width="220" height="118" rx="10" class="g-box"/><rect x="295" y="80" width="220" height="118" rx="10" class="g-stroke"/>
              <circle cx="317" cy="105" r="5" class="g-live"/><text x="331" y="110" class="g-no">ROGERAI /v1</text>
              <text x="313" y="140" class="g-h">broker</text>
              <text x="313" y="163" class="g-b">screens + routes to</text>
              <text x="313" y="181" class="g-b">the cheapest node</text>
              <path d="M515 139 H553" class="g-arrow"/><path d="M547 133 L555 139 L547 145" class="g-arrow"/>
              <rect x="560" y="80" width="220" height="118" rx="10" class="g-box"/><rect x="560" y="80" width="220" height="118" rx="10" class="g-stroke"/>
              <text x="578" y="110" class="g-no">A COMMUNITY GPU</text>
              <text x="578" y="140" class="g-h">serves it</text>
              <text x="578" y="163" class="g-b">your model, streamed</text>
              <text x="578" y="181" class="g-b">back token by token</text>
              <text x="24" y="234" class="g-b g-dim">base_url = https://broker.rogerai.fm/v1  ·  Authorization: Bearer rog-grant_...  ·  model = an id from /discover</text>
            </svg>
            </div>
            <figcaption class="fig mono">FIG. 2 - The same OpenAI call, repatched to the band.</figcaption>
          </figure>
        </section>

        <section class="bc-sec" id="s2" data-reveal>
          <span class="bc-sec__no">&sect;2 &middot; Keys</span>
          <h2>How do I get an API key?</h2>
          <p>
            Mint a <b>grant key</b> - a scoped key made for bots and services:
          </p>
          <ul class="bc-dial">
            <li><code>roger grant create --name my-bots</code> prints a <code>rog-grant_...</code> key. You (the owner) sign in once to mint it; the bot then uses it <b>with no login</b>.</li>
            <li>It's shown <b>once</b> - save it. The broker only ever stores a hash, and recovery rotates it (the old one dies).</li>
            <li>Scope it: <code>--models</code>, <code>--nodes</code>, <code>--rpm</code>, <code>--daily-cap</code>, <code>--monthly-cap</code>, <code>--expires</code>. A fresh free key is already capped, so a leak is bounded.</li>
          </ul>
        </section>

        <section class="bc-sec" id="s3" data-reveal>
          <span class="bc-sec__no">&sect;3 &middot; Two base URLs</span>
          <h2>Hosted endpoint or local proxy - and which reaches whose band?</h2>
          <p>
            Both paths run through the broker - it screens, routes, meters, and signs receipts;
            nothing is peer-to-peer, and the localhost proxy is <b>not</b> a direct line to the node.
            So there's no separate network route and no meaningful speed difference between them. What
            actually differs is how you authenticate, and that decides <b>whose</b> bands you can reach:
          </p>
          <ul class="bc-dial">
            <li><b>Local proxy - reaches any band.</b> Run <code>roger use &lt;model&gt;</code> on the bot's machine; it prints a <code>http://127.0.0.1:&lt;port&gt;/v1</code> endpoint and a session key, and signs every request with your identity. Because it's your signed identity, it can use <b>any</b> band on the market - <b>$0</b> to your own nodes, your wallet for anyone else's. Best when you can run the CLI where the bot lives.</li>
            <li><b>Grant key - reaches your own nodes.</b> Mint a key and point a remote or headless bot straight at <code>https://broker.rogerai.fm/v1</code> with no CLI. A grant is confined to <b>your own</b> hardware (it can never reach another owner's nodes), so this is the pattern for "my cloud bot uses the GPU on my desk."</li>
          </ul>
          <p>
            <b>Connecting a bot to a band you don't own?</b> That always goes through signed identity,
            so run the CLI where the bot lives - a grant key can't reach another operator's hardware:
          </p>
          <ol class="bc-steps mono">
            <li>Install <code>roger</code> on the bot's machine (or a sidecar it can reach).</li>
            <li>Only for a <b>paid</b> band: <code>roger login</code> and fund your wallet. A <b>free</b> band needs no account - your local key signs anonymously, at $0.</li>
            <li><code>roger use &lt;model&gt;</code> - it finds a station serving that model, confirms the price, and prints a <code>http://127.0.0.1:&lt;port&gt;/v1</code> endpoint + a key.</li>
            <li>Point your bot at that endpoint. Each request is signed as you, routed to the operator's node, and billed to your wallet (or $0 for free).</li>
          </ol>
          <p>
            The flip side - putting a GPU on the air for these requests to land on - is covered in
            <a href="/broadcasts-share-gpu-earn.html">share your local model and earn</a>.
          </p>
        </section>

        <section class="bc-sec" id="s4" data-reveal>
          <span class="bc-sec__no">&sect;4 &middot; Features</span>
          <h2>Does streaming, tool calling, and vision work?</h2>
          <p>
            Yes. <b>Streaming</b> is standard SSE - pass <code>stream=true</code>. <b>Tool / function
            calling</b> works on nodes that pass RogerAI's tool-call check (it's a verified
            capability, shown in the market), and <b>vision</b> (image inputs) works on nodes that
            accept images. The hosted broker also serves <b>audio</b> -
            <code>/v1/audio/speech</code> (TTS) and <code>/v1/audio/transcriptions</code> (STT). Model
            discovery is via <code>/discover</code> or <code>roger search</code> (there is no
            <code>/v1/models</code> on the broker); pass an exact model id and the broker routes to the
            cheapest node serving it. Browse ids on the <a href="/models.html">model directory</a>.
          </p>
        </section>

        <section class="bc-sec" id="s5" data-reveal>
          <span class="bc-sec__no">&sect;5 &middot; Before you ship</span>
          <h2>What should I know before shipping a bot?</h2>
          <ul class="bc-dial">
            <li><b>Rate limits.</b> ~120 requests/min per identity by default; each grant also has its own limit plus daily and monthly token caps you set.</li>
            <li><b>Moderation applies to API traffic too</b> - grants don't bypass it. Illegal content is blocked before dispatch.</li>
            <li><b>Keep the key secret.</b> It's a bearer credential; treat it like any secret and rotate if exposed.</li>
            <li><b>Pick a live model.</b> Which ids are available depends on who's on air - check <code>/discover</code> (or the <a href="/models.html">directory</a>) rather than hard-coding a model that may not be up.</li>
          </ul>
        </section>

        <Faq no={6} items={post.faq} />

        <Signoff lead={"<p>Mint a key and repatch your bot - install the set:</p>"}>curl <span class="tok">-fsSL</span> <span class="tok">https://rogerai.fm/install.sh</span> <span class="tok">| sh</span></Signoff>

      </div>
</BroadcastPost>
