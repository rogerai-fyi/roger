# CURATED on TOWERS: the decentralized half of the founder's sentence.
#
# "allow standalone routers or towers to serve and provide from these curated providers"
# - a Tower operator plugs their own upstream key into their tower, and the tower serves
# that provider to its clients: on the PUBLIC side through the broker with the standard
# curated pricing, on the STANDALONE plane to their own network. The standalone plane is
# free by design (the Core-free guarantee), so curated there is a labeled pass-through,
# never a billed one.

Feature: A tower serves curated providers on both of its planes
  As a tower operator with a commercial API key
  I want my tower to serve that provider to my users and, marked up, to the public band
  So that curated supply is as decentralized as the towers themselves.

  Scenario: A joined tower registers a curated station
    Given a tower joined to the Core with an upstream key configured
    When it registers the upstream's models
    Then they appear as curated stations under the tower's identity
    And the curated pricing rule applies to them

  Scenario: The tower operator's key never leaves the tower
    When a curated request relays through the tower
    Then the upstream key is read only on the tower
    And it never appears on the wire, in a receipt, or at the Core

  Scenario: A standalone tower serves a curated upstream to its own network
    Given a standalone tower with an upstream key
    When a local client requests that model
    Then the tower serves it from the upstream
    And the answer is marked local-and-curated
    And no request or receipt leaves the network

  Scenario: The standalone plane never bills for curated
    Given a standalone tower serving a curated upstream
    Then every curated answer on the local plane is free
    # the Core-free guarantee is structural; a markup with no broker would be a toll
    # collected by nobody for nothing

  Scenario: A tower's curated station fails over like any station
    Given a band with a tower-curated station and a human station
    When the tower's upstream refuses a request
    Then the standard empty-output strike applies
    And the retry follows the normal failover rule
