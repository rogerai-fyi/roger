Feature: The homepage presents RogerAI as a research and infrastructure company
  The homepage should keep its concrete product conversion path while making the
  company, RogerAI Labs, evidence standards, and the shipping Wave model line
  visible before monetization. It must not imply fabricated network activity or
  unsupported earnings.

  Rule: The hero carries both the company identity and the product promise

    Scenario: The masthead identifies the company without becoming vague
      Given a visitor opens the RogerAI homepage
      Then the eyebrow identifies RogerAI as American AI research and infrastructure
      And the headline still promises one local OpenAI-compatible endpoint
      And the supporting copy names open model research and inference infrastructure
      And routing, failover, metering, signed receipts, and operator control remain visible
      And the install command remains in the first viewport on a typical desktop

    Scenario: Narrow screens preserve the conversion path
      Given the homepage is 320, 375, or 430 CSS pixels wide
      Then the company identity, product headline, and install command remain readable
      And no hero text, command, mascot, or action overflows horizontally
      And institutional context never pushes the primary install action behind an interaction

    Scenario: Above-the-fold content is visible on first paint
      Given JavaScript is available but deferred scripts have not run
      Then the hero eyebrow, headline, supporting copy, Labs proof, and install action are visible
      And reveal motion never uses opacity to hide above-the-fold content

    Scenario: The hero remains honest without JavaScript
      When client-side JavaScript is unavailable
      Then the company identity, product promise, install command, and primary links remain visible
      And no reveal animation leaves content hidden

  Rule: Product, research, and evidence are visible near the top

    Scenario: A compact institutional strip names the three public surfaces
      Given a visitor passes the initial install action
      Then a compact strip presents the RogerAI Network
      And it presents RogerAI Labs
      And it presents Open Air Waves
      And each item explains its distinct role in one concise sentence
      And each item links to its first-class destination

    Scenario: The homepage sequence reflects the new company focus
      Then Company and Labs appear after the product demonstration or live network proof
      And Company and Labs appear before network specification, security detail, and monetization
      And the page still provides an uninterrupted path from install to demonstration to live models
      And section numbering remains continuous after reordering

    Scenario: The homepage does not duplicate the full institutional pages
      Then the Company preview links to the Company page for origin, ownership, and contact
      And the Labs preview links to the Research page for model and evidence detail
      And the homepage previews rather than reproduces full company or research prose

  Rule: Network and earnings examples distinguish live facts from illustrations

    Scenario: Live model data is labeled as live
      Given the homepage successfully reads current network data
      Then model names, station counts, throughput, signal, and prices are derived from that response
      And the presentation identifies the data as live
      And the visitor can open the full live Models directory

    Scenario: Unavailable network data never becomes fabricated activity
      Given current network data cannot be loaded
      Then the homepage may show the interface as an explicitly labeled demonstration
      And it does not present hard-coded station counts, signals, throughput, or prices as current
      And it provides a direct path to retry or open the Models directory

    Scenario: Historical examples identify their provenance
      Given a historical model or measurement is shown
      Then it is labeled historical or previously observed
      And it does not use live-state language
      And a date, source, or explanatory note distinguishes it from current network state

    Scenario: The operator earnings panel is evidence-honest
      Given an earnings estimate is displayed
      Then it is labeled illustrative unless calculated from current inputs
      And it shows the rate, token-volume, uptime, and revenue-share assumptions
      And the displayed estimate is reproducible from those assumptions
      And no estimate is presented as guaranteed income

    Scenario: Missing assumptions remove the forecast
      Given the page cannot provide the inputs behind an earnings estimate
      Then it shows the operator controls and revenue-share rule without a dollar forecast

  Rule: The homepage previews the RogerAI model family

    Scenario: A compact spectrum presents the right-sized model strategy
      Then the spectrum orders Roger Edge, Wave Nano, Wave Micro, and Wave Core
      And each slot has a concise task and hardware envelope
      And Roger Edge remains below the Wave family as task-specific microcontroller work
      And the spectrum links to Research for full program status

    Scenario: Wave is presented as a shipping model line
      Given at least one Wave checkpoint has passed its release gates
      Then the homepage identifies Wave as available from RogerAI Labs
      And the released slot links to its downloadable artifact and model card
      And unreleased slots carry their actual research, release-candidate, or planned status
      And the homepage never applies one checkpoint's availability to the entire family

    Scenario: A released Wave checkpoint carries a real release contract
      Given a Wave checkpoint is presented as available
      Then its model ID and version are visible
      And its parameter count, task, format, and precision are visible
      And the released artifact identifies Apache-2.0 as its license
      And its tested hardware, runtime, context, peak memory, and measured speed are available from the linked model card
      And weights, source, recipe, raw evaluations, and limitations are linked
      And no compatibility or benchmark claim exceeds the published evidence

    Scenario: Model licensing and network terms remain separate
      Then the homepage presents Apache-2.0 as the released Wave artifact license
      And downloading or running Wave locally does not require RogerAI or a broker
      And optional publication, routing, payments, and receipts through RogerAI are governed by separate network terms
      And broker and network caveats are not presented as modifications to the artifact license

    Scenario: The primary Wave call to action is direct
      Given the homepage previews the Wave family
      Then the primary action for a released checkpoint says Download or Run Wave
      And it resolves to the specific released artifact rather than a generic organization page
      And a secondary action opens the full Research page

    Scenario: The Company preview carries factual identity
      Then it states Orange County, California
      And it states independently owned and not venture funded
      And it distinguishes open model and runtime work from the PolyForm network
      And it invents no employee, customer, funding, or traction claim

  Rule: Tube Ping connects the product and research identities

    Scenario: Tube Ping becomes the interactive concierge
      Then the canonical Tube Ping concierge remains usable
      And its accessible name, keyboard behavior, and reduced-motion behavior remain intact

    Scenario: Tube Ping appears as the Labs character
      Then the homepage, Company preview, and Research hero use the founder-approved pixel Tube Ping silhouette
      And the silhouette is derived from the canonical mascot asset rather than redrawn ad hoc
      And its eye is the only saturated Roger-red cell
      And it is decorative or has a concise accessible description
      And it does not compete with the concierge or install action in the hero

    Scenario: Tube Ping degrades safely
      Given the viewport is narrow, reduced motion is requested, or color is unavailable
      Then Tube Ping remains recognizable or folds to the approved compact fallback
      And no animation is required to understand the Labs preview

  Rule: Search and sharing reflect the company and research identity

    Scenario: Homepage search metadata names both lines of work
      Then the title describes local AI models and inference infrastructure
      And the description identifies RogerAI as an American AI research and infrastructure company
      And it mentions models for constrained hardware and an OpenAI-compatible network
      And it may identify Wave as available when a real released checkpoint is linked

    Scenario: Structured data exposes the relationship
      Then Organization data remains canonical to rogerai.fm
      And RogerAI Labs is represented as a research organization or department of RogerAI
      And the Company and Research URLs are present
      And a released Wave model is represented only with its real artifact URL and release facts
      And no schema property invents employees, customers, funding, awards, or unreleased models

    Scenario: Social cards are page-specific and real
      Then the homepage, Company page, and Research page use intentional social-card metadata
      And every referenced image exists as a raster asset with declared dimensions
      And Company and Research cards distinguish released artifacts from roadmap tiers

  Rule: Every implementation slice receives an independent design review

    Scenario Outline: A fresh reviewer evaluates each completed slice
      Given the <slice> implementation and focused tests are green
      When a fresh reviewer evaluates the source and rendered responsive states
      Then the reviewer checks hierarchy, spacing, color restraint, typography, copy accuracy, accessibility, and responsive behavior
      And the reviewer identifies unnecessary complexity and dead styling
      And justified P1 and P2 findings are fixed before the next slice begins
      And the slice tests are rerun after review fixes

      Examples:
        | slice |
        | masthead, institutional strip, and section order |
        | live versus illustrative network and earnings data |
        | model spectrum, company identity, and Tube Ping Labs plate |
        | metadata, social cards, and structured data |
