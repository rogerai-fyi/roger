<script>
  // The ONE shared nav (legacy: _partials/nav.html). variant "marketing" renders the section
  // groups + burger; "lean" (account pages) renders brand + utilities only. `current` names
  // the page so its link carries aria-current="page".
  import Brand from "./Brand.svelte";
  import Promo from "./Promo.svelte";
  let { variant = "lean", home = "/", scrolled = true, promo = true, current = "" } = $props();
  const cur = (k) => (current === k ? "page" : undefined);
</script>
{#if promo}<Promo />{/if}
<header class="nav{scrolled ? ' is-scrolled' : ''}" id="nav">
  <div class="wrap nav__inner">
    <Brand href={home} />
{#if variant === "marketing"}
    <button class="nav__burger" id="navBurger" type="button"
            aria-label="Open menu" aria-expanded="false" aria-controls="navMenu">
      <span class="nav__burger-lines" aria-hidden="true"><i></i><i></i><i></i></span>
    </button>
{/if}
    <nav class="nav__menu" id="navMenu" aria-label="Primary">
{#if variant === "marketing"}
      <div class="nav__sections">
        <span class="nav__group">
          <a class="nav__link" href="/models.html" aria-current={cur("models")}>Models</a>
          <button class="nav__more" type="button" id="navModelsBtn"
                  aria-expanded="false" aria-controls="navModelsPanel"
                  aria-label="More under Models">
            <svg viewBox="0 0 10 6" width="10" height="6" aria-hidden="true" focusable="false"><path d="M1 1l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>
          </button>
          <div class="nav__panel" id="navModelsPanel" hidden>
            <a href="/models.html"><b>Models on air</b><span>The live directory: every model the network is carrying, and the stations carrying it.</span></a>
            <span class="nav__deckrow">
              <a href="/playbox.html" aria-current={cur("playbox")}><b>Playbox</b><span>Three decks: talk to a model, inspect the mesh, or build a factory.</span></a>
              <button class="nav__more nav__more--sub" type="button" id="navDecksBtn"
                      aria-expanded="false" aria-controls="navDecksPanel"
                      aria-label="Show the three Playbox decks">
                <svg viewBox="0 0 10 6" width="10" height="6" aria-hidden="true" focusable="false"><path d="M1 1l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>
              </button>
            </span>
            <div class="nav__decks" id="navDecksPanel" hidden>
              <a class="nav__sub" href="/playbox.html#console"><b>Console</b><span>Load a model like a cassette: chat, voice, image, tools, embeds.</span></a>
              <a class="nav__sub" href="/playbox.html#mesh"><b>Wave Mesh</b><span>Sensors, wire formats, and the Wave models that read them.</span></a>
              <a class="nav__sub" href="/playbox.html#factory"><b>Factory</b><span>Build machines, ship crates, fix holds, and expand the line.</span></a>
            </div>
            <a href="/voices.html" aria-current={cur("voices")}><b>Voices</b><span>On-air text-to-speech voices, each one an operator&rsquo;s station.</span></a>
            <a href="/tower.html" aria-current={cur("tower")}><b>Tower</b><span>The relay you can run: 5% of what you carry on the public band, or a free standalone network of your own.</span></a>
            <a href="/pricing.html" aria-current={cur("pricing")}><b>Pricing</b><span>What the fee buys, who sets the number, what a wallet is charged, and what an operator keeps.</span></a>
            <a href="/integrations.html" aria-current={cur("integrations")}><b>Integrations</b><span>The local servers it finds by itself, and the agent CLIs it wires for you.</span></a>
          </div>
        </span>

        <span class="nav__group">
          <a class="nav__link" href="/research.html" aria-current={cur("research")}>Research</a>
          <button class="nav__more" type="button" id="navResearchBtn"
                  aria-expanded="false" aria-controls="navResearchPanel"
                  aria-label="More under Research">
            <svg viewBox="0 0 10 6" width="10" height="6" aria-hidden="true" focusable="false"><path d="M1 1l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>
          </button>
          <div class="nav__panel" id="navResearchPanel" hidden>
            <a href="/research.html"><b>Labs</b><span>Programs, proof, and how the work gets published.</span></a>
            <a href="/research-wave-family.html"><b>The Wave family</b><span>The Spectrum, pico to exa: a chip beside one machine up to an exascale-class flagship.</span></a>
            <a href="/research-models.html"><b>Models and status</b><span>Every model, its program status, lineage and evidence.</span></a>
            <a href="/research-wave-pico.html"><b>Wave Pico</b><span>The 293M sensor reader that escalates what it cannot read.</span></a>
            <a href="/research-wave-nano.html"><b>Wave Nano</b><span>1.45B: telemetry, a fleet, a fire site, and conversation.</span></a>
            <a href="/research-hardware.html"><b>Hardware</b><span>ESP32 to DGX Spark: which board carries which tier, and the honest stage of each.</span></a>
            <a href="/research-industry.html"><b>Industrial deployment</b><span>Where the box sits in a plant network, and what it never touches.</span></a>
            <a href="/broadcasts.html"><b>Open Air Waves</b><span>Build stories and measurements, negative results included.</span></a>
          </div>
        </span>

        <span class="nav__group">
          <a class="nav__link" href="/company.html" aria-current={cur("company")}>Company</a>
          <button class="nav__more" type="button" id="navCompanyBtn"
                  aria-expanded="false" aria-controls="navCompanyPanel"
                  aria-label="More under Company">
            <svg viewBox="0 0 10 6" width="10" height="6" aria-hidden="true" focusable="false"><path d="M1 1l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>
          </button>
          <div class="nav__panel" id="navCompanyPanel" hidden>
            <a href="/company.html"><b>About RogerAI</b><span>What the company is, where it is, and how it operates.</span></a>
            <a href="/careers.html"><b>Careers</b><span>Open roles in research, engineering and industrial deployment.</span></a>
            <a href="/faq.html" aria-current={cur("faq")}><b>Questions</b><span>The answers people actually write in for, money and privacy first.</span></a>
            <a data-lets-talk aria-expanded="false"
               href="mailto:labs@rogerai.fm?subject=Hello%20from%20the%20website"><b>Let&rsquo;s talk</b><span>A few lines is plenty. It lands in a real mailbox and a person answers.</span></a>
            <a href="/broadcasts.html"><b>Broadcasts</b><span>The transmission log: what we shipped, what we measured, what did not work.</span></a>
          </div>
        </span>
      </div>

      <span class="nav__divider" aria-hidden="true"></span>
      <div class="nav__sections nav__sections--app">
        <a class="nav__link" href="/app.html"
           aria-label="The RogerAI app, on every surface: iOS and Mac, the terminal (TUI), and the browser (WebUI)"
           aria-current={cur("app")}><span class="carrier cycle-word nav__app-word" data-cycle="App|TUI|WebUI" data-cycle-hrefs="/app.html|/app.html#cli|/app.html#webui">App</span></a>
      </div>
      <span class="nav__divider" aria-hidden="true"></span>
{/if}
      <div class="nav__utils">
        {#if variant !== "marketing"}<a class="nav__link nav__util" href="/models.html">Models</a>{/if}
        {#if variant !== "marketing"}<a class="nav__link nav__util" href="/voices.html">Voices</a>{/if}
        <a class="nav__link nav__util" href="/manual.html" aria-current={cur("manual")}>Manual</a>
{#if variant === "marketing"}
        <a class="nav__link nav__util nav__link--ghost nav__source" href="https://github.com/rogerai-fyi/roger" aria-label="View the source on GitHub">
          <svg class="nav__source__mark" viewBox="0 0 16 16" width="15" height="15" fill="currentColor" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8Z"></path></svg>
          Source
        </a>
{/if}
        <a class="nav__link nav__util" href="/login.html" data-session-login>Log in</a>
        <button class="theme-toggle" id="themeToggle" type="button"
                aria-label="Switch to dark theme" aria-pressed="false">
          <svg class="theme-toggle__moon" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M20.5 14.2A8.2 8.2 0 0 1 9.8 3.5a8.2 8.2 0 1 0 10.7 10.7z" />
          </svg>
          <svg class="theme-toggle__sun" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <circle cx="12" cy="12" r="4.2" />
            <g class="theme-toggle__rays">
              <line x1="12" y1="2.5" x2="12" y2="5" />
              <line x1="12" y1="19" x2="12" y2="21.5" />
              <line x1="2.5" y1="12" x2="5" y2="12" />
              <line x1="19" y1="12" x2="21.5" y2="12" />
              <line x1="5.3" y1="5.3" x2="7" y2="7" />
              <line x1="17" y1="17" x2="18.7" y2="18.7" />
              <line x1="5.3" y1="18.7" x2="7" y2="17" />
              <line x1="17" y1="7" x2="18.7" y2="5.3" />
            </g>
          </svg>
        </button>
      </div>
    </nav>
  </div>
</header>
