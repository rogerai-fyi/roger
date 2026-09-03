Feature: RogerAI presents a clear company, product, and research story
  A reviewer should immediately understand what RogerAI builds, who it serves,
  how to evaluate it, and how RogerAI Labs connects research to real deployments.

  Scenario: The homepage explains the company and product
    Given a visitor opens the RogerAI homepage
    When the visitor reaches the Company section
    Then RogerAI is identified as an AI company
    And the product is described for developers and teams
    And the product provides an OpenAI-compatible endpoint
    And the practical routing, failover, metering, and receipt problems are named

  Scenario: The product is directly evaluable
    Given a visitor reads the Company section
    Then the visitor can browse models
    And the visitor can read the manual
    And the visitor can return to the install command

  Scenario: Research is connected to the company mission
    Given a visitor reads the Company section
    Then RogerAI Labs is presented as part of RogerAI
    And Open Air Waves is presented as the evidence and publication stream
    And Wave models are presented as measured device-first research
    And edge, manufacturing, industrial, and personal use cases are named
    And the visitor can open the Research page

  Scenario: Company information remains discoverable
    Given a visitor reaches the full footer map
    Then a Company link returns to the homepage Company section

  Scenario: Company has a durable first-class destination
    Given a visitor opens the RogerAI marketing website
    Then the primary navigation links to "/company.html"
    And the Company page identifies RogerAI as an independently owned American AI research and infrastructure company
    And the page links the product, models, manual, research, and contact paths

  Scenario: Industrial positioning names concrete local-first work
    Given a visitor reads the Company page
    Then oil and gas, power generation, manufacturing, and aerospace are named
    And each market is grounded in edge, embedded, operational-technology, or air-gapped constraints
    And the patterns are not represented as customer case studies

  Scenario: The model family presents the shipping Wave line honestly
    Given a Wave checkpoint has passed release gates
    Then Roger Edge is described as task-specific microcontroller work
    And Wave Nano is described as sub-100M specialist research
    And Wave Micro is described as a 350M-class released model
    And frontier-scale optimization covers upstream models with tens or hundreds of billions of parameters
    And the released checkpoint links its weights, model card, license, source, evaluations, and limitations
    And no unreleased slot borrows the released checkpoint's availability or performance

  Scenario: Origin and openness claims are component-specific
    Given a visitor reads the Company page
    Then RogerAI is described as built in Orange County, California
    And American-made does not erase upstream models or the global research community
    And open-source model and runtime work is distinguished from the PolyForm Perimeter network and broker
    And the released model status is distinguished from final legal confirmation of the intended Apache-2.0 Wave artifact license
    And broker-use terms are described separately from the model artifact license

  # ---------------------------------------------------------------------------
  # A dedicated company page. The homepage Company SECTION answers "is this a
  # real product"; a grant, enterprise, or partnership reviewer additionally
  # needs a durable "is this a real company" destination that does not depend on
  # scrolling a product landing page.
  # ---------------------------------------------------------------------------

  Scenario: Company is a first-class destination
    Given a visitor opens the RogerAI marketing website
    Then the primary marketing navigation includes "Company"
    And the link resolves to "/company.html"
    And the homepage Company section links to the company page

  Scenario: The company page states what kind of company this is
    Given a visitor opens the company page
    Then the page identifies RogerAI as an American AI research and infrastructure company
    And the page states that RogerAI is based in Orange County, California
    And the page states that RogerAI is independently owned and not venture funded
    And the page does not invent employee counts, customers, or funding rounds

  Scenario: The company page separates the two lines of work
    Given a visitor reads the company page
    Then the page presents the RogerAI network as the product line
    And the page presents RogerAI Labs as the research line
    And each line links to its own destination
    And the page explains that using the models never requires the network

  Scenario: The company page names the focus markets
    Given a visitor reads the company page
    Then the page names embedded and edge inference as the focus
    And the page names oil and gas, power generation, manufacturing, and aerospace
    And the page explains the operational-technology constraint that forces local inference
    And the page links to the industry detail on the research page

  Scenario: The company page publishes operating principles
    Given a visitor reads the company page
    Then the page states that published numbers carry hardware, harness, and raw results
    And the page states that artifacts ship in open formats on standard runtimes
    And the page states that local inference emits no telemetry to RogerAI
    And the page states that negative and superseded results stay discoverable

  Scenario: The company page routes real enquiries
    Given a visitor reaches the end of the company page
    Then the page offers a contact route for pilots and engagements
    And the page offers a contact route for press
    And the page offers a contact route for security disclosure
    And the page links to the terms that name the governing legal entity

  Scenario: The company page makes no unearned claims
    Given a visitor reads the company page
      Then the page does not name a customer RogerAI does not have
    And the page claims a released Wave checkpoint only when its real artifact is linked
    And the page does not describe an optimization of an upstream model as RogerAI pretraining

  # Founder ruling 2026-09-02: the operating entity is RogerAI, Inc., a Delaware
  # corporation (succeeding the sole proprietorship). Named in the four customary
  # places; the privacy page is a real indexed URL for the App Store listing.
  Scenario: The legal entity is named where people look for it
    When the site builds
    Then the Terms' contracting clause, the privacy page, the footer, and the company page all name RogerAI, Inc., a Delaware corporation
    And the Organization structured data carries the legalName
    And the predecessor entity survives on no page
