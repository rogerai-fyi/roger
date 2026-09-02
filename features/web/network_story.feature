# THE NETWORK STORY, on the surfaces a first-timer actually lands on.
#
# Founder directive 2026-09-01, grounded in a two-agent exercise (a live-site audit +
# a positioning/UX pass): the site's best argument (why.html) was orphaned - linked
# from exactly one page - the homepage said nothing about identity unlinking, curated
# supply, the tower network, or the comparison, and the FAQ answered neither "can I
# resell my contracts" nor "does the provider know who I am".

Feature: Every landing surface tells the network's real story
  As a first-time visitor comparing routers
  I want the differentiators and both earn paths where I actually land
  So that the site's argument does not live on a page nothing links to.

  Scenario: The why page is reachable from everywhere
    When the site builds
    Then the homepage, the nav, and the footer all link why.html

  Scenario: The homepage tells the unlinking, the two earn paths, and the tower
    When the homepage renders
    Then it says the upstream sees a station, never you
    And the monetize section names both transmitters: your GPU and your contracts
    And the tower section offers the 5 percent relay and the free standalone exit
    And the comparison tease concedes when going direct is cheaper

  Scenario: The FAQ answers the two missing money-and-privacy questions
    When the FAQ renders
    Then it answers whether you can resell a provider contract
    And it answers whether a model provider knows who you are
