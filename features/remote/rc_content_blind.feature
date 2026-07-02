# Executable spec: the broker stays CONTENT-BLIND for remote-control (BASE STATION, v5.0.0).
# The broker persists a ROSTER row only (id, owner wallet, name, code/token hashes,
# timestamps, revoked) — NEVER a transcript, prompt, or frame. Live frames flow through a
# bounded TRANSIENT relay ring (memory/Valkey, evicts by size/TTL, never Postgres); full
# history is served by the HOST on attach (a backfill snapshot addressed to the new viewer).
# This preserves the README/PRIVACY promise ("token counts and signed receipts, never
# prompts") in its strong form for durable storage. Ground truth: internal/store/rc.go
# (roster only), cmd/rogerai-broker/rc.go rcHub ring (Increment 2), account.go accountExport.
#
# INVARIANTS:
#   C1 after a full conversation, durable storage holds ONLY roster metadata — no frame text.
#   C2 the transient replay ring is bounded (evicts oldest past the frame/byte cap).
#   C3 a data export (accountExport / GDPR) shows roster metadata only, never a message.

Feature: The broker never stores remote-control conversation content

  Background:
    Given a running broker

  Scenario: a full remote conversation leaves no content at rest (C1)
    Given a remote-control session with 20 turns of chat relayed through the broker
    Then durable storage contains the session roster row
    And durable storage contains NO frame text, prompt, or assistant message

  Scenario: the transient replay ring is bounded (C2)
    Given a remote-control session
    When 500 frames are relayed through it
    Then the replay ring holds at most the last 200 frames
    And the ring's byte size stays within the 256 KiB cap

  Scenario: a data export reveals only roster metadata (C3)
    Given a remote-control session with relayed chat for wallet "u_gh_7"
    When the owner exports their account data
    Then the export lists the session id, name, and timestamps
    And the export contains no message text
