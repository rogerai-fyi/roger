# ROGERAI CAREERS — hiring surface for a lab that has to be believed first.
#
# RogerAI has no careers page. "Join us" appears nowhere, so an engineer who
# reads the Research page and wants in has no next step, and a grant or
# partnership reviewer has no signal that this is a growing organisation rather
# than one person.
#
# Scope: a /careers.html destination, the roles, the application route, and the
# links that make it reachable. Deliberately NOT an applicant tracking system -
# there is no ATS, and pretending otherwise would be another dead artifact.
#
# Interfaces: web/src/careers.html, web/src/_partials/footer.html,
#   web/src/company.html, web/build.mjs (CSS bundle + sitemap).
#
# Out of scope: an embedded application form, a resume upload, salary data we
# have not decided, and any claim about headcount or funding.

Feature: RogerAI publishes what it is hiring for and how to apply
  Someone who wants to work here should find the roles, understand what the work
  actually is, and be able to apply in one step - without meeting a claim the
  company cannot back.

  Background:
    Given a visitor opens the RogerAI careers page

  # ---- reachability ---------------------------------------------------------

  Scenario: Careers is reachable from the site
    Given a visitor is anywhere on the marketing site
    Then the footer links to the careers page
    And the company page links to the careers page
    And the link resolves to "/careers.html"

  # ---- the pitch ------------------------------------------------------------

  Scenario: The page says what kind of place this is before it lists jobs
    Then the page states that RogerAI is an American AI research company
    And the page states where the work happens
    And the page states that the work is published in the open
    And the page does not claim a headcount, a funding round, or a customer

  Scenario: The page is honest about stage
    Then the page states that RogerAI is early and small
    And it does not describe itself as a large or established organisation

  # ---- the roles ------------------------------------------------------------

  Scenario: Roles are grouped by the work, not by seniority theatre
    Then roles are grouped under research, engineering, and industrial deployment
    And every role names the concrete problem it owns
    And every role names the stack or domain it touches

  Scenario Outline: Every role carries the facts an applicant needs
    Given the visitor reads the "<role>" listing
    Then it states the location or remote policy
    And it states the employment shape
    And it links a way to apply

    Examples:
      | role                  |
      | edge model research   |
      | inference engineering |
      | industrial deployment |

  Scenario: An open application exists for people who fit none of the roles
    Then the page offers a general expression of interest
    And it explains what to include

  # ---- honesty --------------------------------------------------------------

  Scenario: The page never advertises a compensation figure it has not set
    Then no salary band is stated unless it is a real, decided band
    And no equity claim is made

  Scenario: Every advertised contact route is a mailbox that exists
    Then each application address resolves to a real RogerAI mailbox
    And no form posts to an endpoint that is not implemented

  Scenario: A role that is not actually open is not listed
    Then every listed role is one RogerAI would interview for today
    And roles kept open on a rolling basis say so

  # ---- degradation ----------------------------------------------------------

  Scenario: The page works without client JavaScript
    Then the roles are readable with scripting disabled
    And the application route is reachable with scripting disabled
