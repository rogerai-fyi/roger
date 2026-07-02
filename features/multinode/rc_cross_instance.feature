# Executable spec: remote-control works across the 2-instance broker deployment (BASE
# STATION, v5.0.0). A viewer may attach on instance A while the host polls instance B and
# another viewer streams on C. Rendezvous is keyed on sessionID over the existing Valkey bus
# (rc:in:<sid> broker→host, rc:out:<sid> host→viewers) plus an rc-session mirror namespace
# (putRCSession, the putPrivateNode twin). Ground truth: cmd/rogerai-broker/sharedstore.go
# (Increment 5), mirroring busDispatchJob/syncRegistry.
#
# INVARIANTS:
#   X1 a session enabled on A is attachable + streamable on B (roster mirrored).
#   X2 a turn sent on B reaches the host polling A (rc:in bus), delivered EXACTLY once.
#   X3 a host event posted on A fans out to viewers streaming on B and C (rc:out bus).
#   X4 revoke on any instance is honored on all (shared roster).

Feature: Remote control across broker instances

  Background:
    Given a two-instance broker pair "A" and "B" sharing a store and bus

  Scenario: a session enabled on A is usable on B (X1)
    Given a host enables remote control on instance A
    When the owner attaches on instance B with the link code
    Then the attach succeeds on B

  Scenario: a turn on B reaches the host on A exactly once (X2)
    Given a host polling instance A with remote control enabled
    When a viewer on instance B sends a turn
    Then the host on A receives the turn exactly once

  Scenario: host events fan out across instances (X3)
    Given a host posting events on instance A
    And viewers streaming on instances A and B
    When the host posts an assistant frame
    Then both viewers receive it

  Scenario: revocation is shared (X4)
    Given a session enabled on A
    When the owner revokes it on instance B
    Then the host's next poll on A sees the session ended
