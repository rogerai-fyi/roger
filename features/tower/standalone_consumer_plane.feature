# APPROVED SPEC - founder-dictated 2026-08-23 ("it should never advertise from the Open
# Market, it should only advertise/relay what is set by roger share pointed to that
# broker, and it should deny if an attempt is made to try to send traffic to an Open
# Market band to try to bypass payment"). First slice per founder: `ROGER_BROKER=<tower>
# roger use` gets a real completion through a standalone Tower, zero public-network
# contact, with local client admission as the auth. Changes need re-approval.
# BUILD STATUS: NOT BUILT.

Feature: A standalone Tower serves its own network and no one else's
  The airgap plant: model boxes `roger share` to a local Tower, operators point their
  roger at it, and nothing - traffic, names, receipts, or money - ever touches the public
  network. The Tower is a sealed local broker, never a bridge onto the Open Market.

  Background:
    Given a standalone Tower serving its consumer endpoint on the local network

  # --- the loop the first slice proves --------------------------------------

  Scenario: An admitted local client gets a completion through a local station
    Given a roger share node registered its model to this Tower
    And a client admitted by this Tower's own bootstrap invitation
    When the client runs roger use pointed at the Tower and sends a prompt
    Then the answer comes from the local station
    And a local receipt records the route
    And no connection of any kind was made outside the declared private network

  Scenario: The consumer surface speaks the contract roger already knows
    Then the Tower answers /discover with its OWN attached stations and nothing else
    And it answers /v1/chat/completions in the shape the public broker does
    And roger config set broker to the Tower's address is the only client change needed

  # --- never a bridge to the Open Market ------------------------------------

  Scenario: An Open Market model is refused, not forwarded
    Given the public network sells a model this Tower does not host
    When an admitted client requests that model from the Tower
    Then the request is refused with "no attached Station offers" that model
    And the Tower makes no outbound connection attempting to satisfy it
    # The refusal is structural, not a policy check: the standalone binary has no route
    # to Core to forward to - the no-egress gate makes forwarding unrepresentable.

  Scenario: Open Market identity is meaningless here
    When a request arrives carrying a RogerAI account, wallet, band frequency, or grant key
    Then none of it is honored: only this Tower's own admitted client credentials authenticate
    And the refusal does not reveal whether such an account exists anywhere

  Scenario: The Tower's own advertisements never leave the machine
    When the Tower has stations attached and clients admitted
    Then nothing it knows appears on the public /discover
    And no heartbeat, inventory, telemetry, or DNS lookup leaves the declared private network

  # --- local admission is the only door -------------------------------------

  Scenario: An unadmitted caller on the LAN gets nothing
    Given a machine on the same network that was never admitted
    When it calls /discover or /v1/chat/completions
    Then it is refused before any station or model name is revealed
    And the refusal is uniform: probing cannot distinguish "wrong key" from "no such client"

  Scenario: Admission is the bootstrap flow that already exists
    Given the operator mints an invitation with roger-tower invite
    When a new client consumes it with the one-time code
    Then that client's signed requests are accepted from then on
    And the consumed code is dead - replay across restarts included

  Scenario: A client can be cut off locally
    Given an admitted client the operator no longer trusts
    When the operator revokes it
    Then its next request is refused
    And the refusal does not break any station or other client

  # --- honest accounting, no money ------------------------------------------

  Scenario: Local receipts are bookkeeping, never billing
    Given requests have flowed through the Tower
    Then each produced a local receipt naming client, station, and model
    And receipts state plainly they are free and locally accounted
    And nothing accrues, settles, or converts to RogerAI credit - there is no rail here

  # --- the airgap posture, stated and enforced --------------------------------

  Scenario: The no-egress guarantee covers the consumer plane
    Given the consumer endpoint is part of the standalone core
    Then the package-level no-egress gate covers every file that serves it
    And the systemd IPAddressDeny posture still applies unchanged
