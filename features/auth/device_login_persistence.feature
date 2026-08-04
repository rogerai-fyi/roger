# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# DEVICE-LOGIN STATE THAT SURVIVES THE PROCESS — where a pending login lives.
#
# THE DEFECT THIS SPEC EXISTS TO FIX. internal/deviceauth.Flow holds every pending login in
# process-local maps (`byDev`, `byUser`, `wrong`, guarded by one mutex), and the broker
# builds exactly one Flow per process in newDeviceFlow(). Nothing in the flow touches
# `b.shared`, the Redis-backed sharedStore that capsule.go, ratelimit.go and the market
# cache already use. Two consequences, one mild and one a hard break:
#
#   1. RESTART. Any deploy, crash, or restart silently drops every in-flight login. A
#      person who ran `roger login`, opened the page, and walked to their phone for the
#      mail comes back to a code the broker has never heard of. The CLI reports the same
#      uniform rejection a guessed code gets, so the message is actively misleading: it
#      reads as "that code is not valid" when the truth is "we forgot it".
#
#   2. MULTI-INSTANCE. The broker supports ROGERAI_MULTI_INSTANCE with ROGERAI_REDIS_URL,
#      and behind more than one instance this flow CANNOT COMPLETE except by luck. The CLI
#      polls /auth/device/token on whichever instance the load balancer picks; the human
#      approves on whichever instance serves their browser. Unless the two land on the same
#      process, the approval is written to one instance's map and the poll reads another's.
#      Approval appears to succeed, and the CLI polls a pending login forever until it
#      expires. This is not a degradation - device login is simply broken the moment we
#      scale out, and it fails in the direction that looks like an attack rather than a bug.
#
# THE SHAPE OF THE FIX, stated so the spec can be judged against it. The flow gains a
# storage seam behind the same optional-shared-store pattern the broker already uses:
# sharedStore when ROGERAI_REDIS_URL is wired, the existing in-process maps when it is not
# (errNoSharedStore routes to local, exactly as capsule.go does). Single-instance deploys
# keep working with ZERO configuration change and zero new dependency.
#
# WHAT MUST NOT CHANGE. The one property the whole flow rests on: the device code is bound
# to the requesting key AT ISSUE, and no later step accepts a key as input. Persistence
# must not introduce a write path that can rebind a key, and a store an operator can edit
# must not become a way to redirect a login.

Feature: A pending device login survives the process that issued it
  A login in flight belongs to the deployment, not to one process's memory. It survives a
  restart, it completes across instances, and what is written down is not itself a
  credential.

  Background:
    Given a broker serving device login
    And a CLI that has started a login and holds a device code

  # --- surviving a restart --------------------------------------------------

  Scenario: A pending login survives a broker restart
    Given a durable store is configured
    When the broker restarts
    And the human approves the user code
    And the CLI polls
    Then the login completes and binds the CLI's key

  Scenario: An approval given before a restart is still an approval after it
    Given a durable store is configured
    And the human has approved the user code
    When the broker restarts before the CLI's next poll
    And the CLI polls
    Then the approval is reported
    And the account bound is the account that was approved

  Scenario: A denial survives a restart and is not retried into an approval
    Given a durable store is configured
    And the human has denied the user code
    When the broker restarts
    And the CLI polls
    Then the denial is reported
    And the login cannot afterwards be approved

  Scenario: A consumed code stays consumed across a restart
    Given a durable store is configured
    And the CLI has already polled once after approval, consuming the code
    When the broker restarts
    And the same device code is polled again
    Then it is refused

  # Why: single-use is the control that stops a captured device code being replayed later.
  # If "consumed" lives only in memory, a restart is a replay window.

  Scenario: Expiry is measured against the issued deadline, not against process uptime
    Given a durable store is configured
    And a login was issued and has since passed its lifetime
    When the broker restarts and the code is polled
    Then it is reported expired
    And a restart does not extend any login's life

  Scenario: The wrong-code budget survives a restart
    Given a durable store is configured
    And a submitter has spent most of its attempt budget on wrong user codes
    When the broker restarts
    And that submitter submits more wrong codes
    Then the budget continues from where it stood
    And restarting is not a way to refill it

  # Why: a per-submitter budget that resets on restart is a budget an attacker can reset by
  # waiting for a deploy - and we deploy often.

  Scenario: Without a durable store, a restart loses pending logins and says so honestly
    Given no durable store is configured
    When the broker restarts
    And the CLI polls a login issued before the restart
    Then the CLI is told the login is no longer known and to start a new one
    And it is NOT told the code is invalid

  # Why: the uniform "that code is not valid" rejection exists to deny a guesser any signal.
  # A code WE dropped is a different fact, it is not attacker-controlled, and reporting it
  # plainly is what stops a person concluding they are under attack when we redeployed.

  # --- completing across instances ------------------------------------------

  Scenario: Approval on one instance completes a poll on another
    Given a shared store is configured and two broker instances are serving
    When the human approves on instance A
    And the CLI polls instance B
    Then the approval is reported
    And the key bound is the key recorded at issue

  Scenario: A user code issued by one instance is resolvable on every instance
    Given a shared store is configured and two broker instances are serving
    And instance A issued the user code
    When the human opens the approval page on instance B
    Then the pending login is found
    And what the approval screen may show is exactly what it may show on instance A

  Scenario: A code is consumed once across the whole deployment
    Given a shared store is configured and two broker instances are serving
    And the login has been approved
    When the CLI polls both instances concurrently with the same device code
    Then exactly one poll reports the approval
    And the other is refused

  # Why: two processes reading the same pending row and both deciding "not consumed yet" is
  # the classic double-spend. Consumption must be a single atomic decision in the store, not
  # a read followed by a write.

  Scenario: The wrong-code budget is shared across instances
    Given a shared store is configured and two broker instances are serving
    When a submitter spreads wrong user codes across both instances
    Then the attempts count against one budget
    And spreading them across instances does not multiply the allowance

  Scenario: Concurrent approval and denial resolve to exactly one outcome
    Given a shared store is configured and two broker instances are serving
    When an approval reaches instance A and a denial reaches instance B at the same moment
    Then the login settles on exactly one of the two
    And it never reports both
    And whichever settles is what every later poll reports

  # --- what may be written down ---------------------------------------------

  Scenario: The device code is stored only as a hash
    When a login is persisted
    Then the stored record carries a one-way hash of the device code, not the device code
    And somebody reading the store cannot redeem a login from what they read

  # Why: in process memory the device code was reachable only by the process. In a shared
  # store it is reachable by anything holding the store's credential, including a backup, a
  # replica, and whatever operational tooling can run a scan. A bearer credential at rest in
  # plaintext is a credential we have handed out.

  Scenario: The user code is stored only as a hash
    When a login is persisted
    Then the stored record carries a one-way hash of the user code, not the user code
    And a reader of the store cannot approve a login on somebody's behalf from what they read

  Scenario: The bound key is written once and never rewritten
    When a login is persisted
    Then the record's bound key is written at issue
    And no later operation in the flow writes that field

  Scenario: A record whose bound key does not match the poll is refused
    Given a stored record has been tampered with so its bound key differs from the issuing key
    When the issuing CLI polls
    Then the poll is refused
    And no key is bound

  # Why: the store is the new tamper surface persistence introduces. The property "approval
  # decides which ACCOUNT, never which KEY" has to hold against a store an operator can edit,
  # not only against a request an attacker can send.

  Scenario: A record that cannot be read is a refusal, never an approval
    Given a stored record is corrupt or cannot be decoded
    When it is polled
    Then the poll is refused
    And the failure is not reported as an approval or a denial

  Scenario: Expired records are removed from the store
    Given logins have been issued and have expired
    Then their records are removed
    And the store does not grow without bound

  # --- the store failing ----------------------------------------------------

  Scenario: A store outage refuses to start new logins rather than starting ones it will lose
    Given a shared store is configured and is unreachable
    When a CLI starts a login
    Then starting is refused with a message naming the broker as unavailable
    And no code pair is issued

  # Why: issuing a code we cannot durably record is worse than refusing. The person walks
  # away, does the work of finding the mail, approves, and only then learns none of it
  # counted. The repository already took this position for the Tower: refuse to serve rather
  # than lose state quietly.

  Scenario: A store outage during a poll is reported as a retryable condition
    Given a shared store is configured and becomes unreachable after a login was issued
    When the CLI polls
    Then it is told to retry
    And it is NOT told the code is invalid, expired, or denied
    And when the store returns, the same code still completes

  Scenario: A store outage during approval does not report success
    Given a shared store is configured and is unreachable
    When the human approves
    Then the approval screen reports the failure
    And the CLI's next poll does not report an approval
