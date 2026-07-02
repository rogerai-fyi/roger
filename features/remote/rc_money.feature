# Executable spec: remote-control relaying is FREE, and it never shifts billing identity
# (BASE STATION, v5.0.0). The /rc/* surface is owner-to-self — philosophically identical to
# ownsNode auto-$0 self-use (grant.go:182-207) — implemented as "no billing code path at
# all": no resolvePricing, no receipts, no ledger, no lots, no /earnings, no /metrics. The
# host's agent model calls keep going through BrokerCompleter signed with the HOST's device
# key, so a turn typed on a REMOTE surface still bills the HOST, never the remote device.
# Ground truth: cmd/rogerai-broker/rc.go (no billing), harness/broker.go BrokerCompleter.
#
# INVARIANTS:
#   M1 no /rc/* call writes a ledger row, receipt, or lot; the owner's balance is unchanged.
#   M2 a remote-typed turn that triggers a model call bills the HOST wallet (not the remote).
#   M3 remote-control traffic never appears in /earnings, /usage, or /metrics.

Feature: Remote control is free and never moves billing identity

  Background:
    Given a running broker
    And a host with remote control enabled, funded wallet, and one attached remote surface

  Scenario: rc traffic moves no money (M1)
    Given the owner's balance before any remote turns
    When 10 frames and 3 turns are relayed with NO model call
    Then the owner's balance is unchanged
    And no ledger row, receipt, or lot was created by the rc surface

  Scenario: the model call bills the host, not the remote surface (M2)
    When the remote surface sends a turn that triggers one model call
    Then exactly the model call is billed
    And it is billed to the HOST wallet, not the remote surface

  Scenario: rc never appears in earnings/usage/metrics (M3)
    Given a remote-control session with relayed chat
    Then it does not appear in the owner's earnings
    And it does not appear in usage or the provider metrics
