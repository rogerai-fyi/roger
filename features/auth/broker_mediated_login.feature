# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# BROKER-MEDIATED DEVICE LOGIN — `roger login` and `roger-tower login` authenticate through
# RogerAI, not through a provider directly.
#
# WHY THIS REPLACES THE CURRENT FLOW. Today the CLI runs GitHub's own Device Authorization
# Grant: it calls github.com directly with a client id compiled into the binary, then posts
# the resulting GitHub token to POST /auth/github to bind it. Three costs:
#
#   1. It is vendor-locked. The account system already supports Apple (web + iOS), but the
#      CLI cannot use it, and every new provider means another provider-specific flow
#      inside the binary.
#   2. Every CLI reaches a third-party host we do not control. That is worst for
#      roger-tower, whose whole design story is a bounded, declared egress surface.
#   3. A provider change - client-id rotation, an endpoint move, a policy shift - needs a
#      NEW BINARY and breaks installs that have not updated.
#
# After this change the CLI talks only to the broker. Which providers exist, and which are
# offered, becomes a server-side decision that already-installed binaries inherit.
#
# GROUND TRUTH the flow builds on:
#   - internal/protocol/auth.go SignRequest/VerifyRequest: every CLI request is signed with
#     the caller's Ed25519 key. userID = "u_" + sha256(pubHex)[:16].
#   - The broker already runs full OAuth for GitHub and Apple on the web
#     (/auth/github/login, /auth/apple/web/login) and binds accounts to signing pubkeys.
#
# THE ATTACK THIS SPEC EXISTS TO STOP. Every device flow shares one weakness: an attacker
# starts a flow on THEIR machine, gets a user code, and social-engineers a victim into
# approving it - binding the attacker's key to the victim's account. The mitigations below
# are not optional polish; they are the reason the flow is safe.

# EXECUTABLE COVERAGE, stated so nobody mistakes this file for fully asserted:
# cmd/rogerai-broker/device_login_bdd_test.go runs it against the real routes, the real
# state machine and the real client package. Four scenarios are fully driven today - the
# start payload, provider-agnostic approval, denial, and what the approval screen may
# read; the rest are undefined steps and remain documentation. They fall into two groups:
# claims about page WORDING and styling, which need a browser to assert honestly, and
# CLI-side behaviour better covered where it lives (internal/deviceauth for the state
# machine, internal/client for storage). The undefined steps are not silently skipped -
# godog reports them on every run, so the gap stays visible.

Feature: A CLI signs in through RogerAI with any supported provider
  The device code is bound to the requesting key at issue, approval is an explicit act by
  an authenticated human who can see what they are authorizing, and nothing about which
  provider they used reaches the CLI's code.

  Background:
    Given a CLI with an Ed25519 signing keypair
    And a broker that supports more than one sign-in provider

  # --- starting a flow ------------------------------------------------------

  Scenario: Starting a login returns a user code and a RogerAI URL
    When the CLI starts a login
    Then the broker issues a device code, a user code, a verification URI, a poll interval, and an expiry
    And the verification URI is a RogerAI address
    And no provider-specific endpoint, client id, or third-party URL is returned to the CLI

  Scenario: The device code is bound to the requesting key at issue
    Given the CLI signs its start request with key K
    When the broker issues the device code
    Then the pending login records K as the only key that approval can ever bind
    And the key cannot be supplied, changed, or re-declared later in the flow

  Scenario: The user code is human-typable but not guessable
    When a user code is issued
    Then it carries at least 32 bits of entropy from the operating-system random source
    And it uses an unambiguous alphabet with no characters a person would confuse when reading it aloud
    And it is short enough to type from a phone screen

  Scenario: Two concurrent logins never collide
    Given many CLIs start logins at the same moment
    Then every device code and user code is unique among pending logins
    And approving one has no effect on any other

  # --- polling --------------------------------------------------------------

  Scenario: Polling before approval reports that it is still pending
    Given a started login that nobody has approved
    When the CLI polls
    Then the response says authorization is pending
    And it reveals nothing about whether the code exists, was seen, or was denied

  Scenario: Polling faster than the interval is slowed, not failed
    Given a started login
    When the CLI polls faster than the issued interval
    Then the broker tells it to slow down and raises the interval
    And the login remains usable

  Scenario Outline: Polling with a request the issuing key did not sign is refused
    Given a started login bound to key K
    When a poll arrives "<condition>"
    Then it is refused
    And the pending login is neither approved nor consumed

    Examples:
      | condition                        |
      | unsigned                         |
      | signed by a different key        |
      | with a replayed signature         |
      | with a stale timestamp            |
      | with a body that does not match its signature |

  Scenario: An expired login stops working and says so
    Given a started login whose expiry has passed
    When the CLI polls or a user tries to approve it
    Then both are refused as expired
    And the operator is told to start a new login

  # --- approval, in the browser ---------------------------------------------

  Scenario: Approval requires an authenticated human
    Given a started login
    When someone opens the verification URI without being signed in
    Then they are asked to sign in first
    And the pending login is not approved by merely visiting the page

  Scenario Outline: Any supported provider can approve
    Given a user signs in with "<provider>"
    When they approve a pending login
    Then the resulting account is bound to the CLI's key
    And the CLI learns only which account it is signed in as, never which provider was used

    Examples:
      | provider |
      | GitHub   |
      | Apple    |

  Scenario: Approval is an explicit act, not a side effect of following a link
    Given a user opens a pre-filled verification link
    Then the code is shown for confirmation and approval still requires a deliberate action
    And no login is approved by a page load, a prefetch, or a redirect

  # The approval screen's WORDING is part of the security contract, not decoration - it is
  # the only place the social attack below can be stopped. It has to leave a person
  # informed rather than alarmed: someone who is frightened by a routine sign-in learns to
  # click through warnings, which is the opposite of what we need.
  #
  # NORMATIVE COPY. The screen says, in this order:
  #
  #   Sign in to RogerAI
  #   A command line on this device is asking to sign in to your account.
  #
  #   Code from your terminal:   WXYZ-2468
  #   Requested:                 just now, from <approximate origin>
  #
  #   Check that the code above matches the one your terminal printed.
  #   Only approve a code you started yourself - a code someone sent you, read to
  #   you, or asked you to enter would sign THEIR device in to YOUR account.
  #
  #   [ Approve ]   [ Not me - deny ]
  #
  # Notes on why it reads this way: it leads with what is happening, not with a warning;
  # "a code you started yourself" is the single rule a person can actually follow; naming
  # the three ways a code reaches you (sent, read aloud, asked to enter) is concrete where
  # "beware of phishing" is not; and the deny button is labelled with the user's own
  # conclusion ("not me") rather than a neutral "cancel" they might read as "go back".

  Scenario: The approval screen shows what is being authorized
    When a user is asked to approve a pending login
    Then the screen states that a command line on this device is asking to sign in to their account
    And it shows the user code so they can compare it with what their terminal printed
    And it shows when the request was made and from what approximate origin
    And it tells them to approve only a code they started themselves
    And it names the ways a code could have reached them from someone else
    And it offers an explicit way to deny, labelled as the user's own conclusion

  Scenario: The wording informs without alarming
    When the approval screen is rendered for an ordinary, legitimate sign-in
    Then it reads as a normal step rather than a security warning
    And it uses no alarm styling, no threat language, and no interstitial the user must dismiss
    And the caution is one sentence stating the single rule: approve only a code you started yourself

  # --- the phishing case, stated directly -----------------------------------

  Scenario: An attacker's code approved by a victim binds the ATTACKER's key, so the screen must make that visible
    Given an attacker starts a login on their own machine and obtains user code C
    And they trick a victim into opening the verification URI and entering C
    When the victim reaches the approval screen
    Then the code shown is C, which the victim's own terminal never printed
    And the screen has already told them to approve only a code they started themselves
    And the request time and approximate origin are shown so an unfamiliar one is visible
    And approval remains a deliberate act taken after reading that

  Scenario: A denied login can never be approved afterwards
    Given a user denies a pending login
    When the CLI polls
    Then it is told the request was denied
    And the same code can never be approved later

  Scenario: Guessing user codes is rate-limited per submitter
    When user codes are submitted that do not match any pending login
    Then attempts are counted against the SUBMITTING account, never in one shared counter
    And a bounded number of wrong codes locks further attempts by that submitter
    And the response does not reveal whether a code exists

  # A single global guess counter would convert an anti-guessing control into a denial of
  # service: one attacker exhausts it and nobody can complete a sign-in. Approval already
  # requires an authenticated session, so every attempt is attributable to someone.
  Scenario: One attacker cannot lock everyone else out
    Given an attacker exhausts their own guessing budget
    When an unrelated user approves a login they legitimately started
    Then it succeeds
    And the attacker remains locked out

  # --- redemption -----------------------------------------------------------

  Scenario: The first successful poll after approval binds the account and consumes the code
    Given an approved login
    When the CLI polls
    Then it receives the account it is now signed in as
    And the pending login is consumed
    And a second poll with the same device code is refused

  Scenario: Binding is idempotent for the key that already owns it
    Given a CLI whose key is already bound to an account
    When it signs in again and completes a new login
    Then it remains bound to the same account
    And no second account, wallet, or starter credit is created

  Scenario: A crash between approval and redemption loses nothing
    Given an approved login whose response never reached the CLI
    When the CLI polls again within the expiry
    Then it receives the same outcome
    And no duplicate binding is created

  # --- what the CLI stores and shows ----------------------------------------

  Scenario: The CLI stores the account, never a provider token
    Given a completed login
    When credentials are persisted
    Then only the account identity is stored, owner-only
    And no provider access token, refresh token, or id token is written to disk

  Scenario: The CLI never learns provider details
    Given a completed login
    When the CLI reports its signed-in state
    Then it names the account
    And no provider name, client id, or provider endpoint appears in its code, config, or output

  # --- compatibility --------------------------------------------------------

  Scenario: Already-installed binaries keep working
    Given a CLI built before this flow existed
    When it runs the old provider device flow and binds through the existing route
    Then it still succeeds
    And its account and wallet are the same ones the new flow would have produced

  Scenario: Adding a provider needs no new CLI release
    Given a new sign-in provider is enabled on the broker
    When an existing installed CLI starts a login
    Then the new provider is offered on the verification page
    And the CLI requires no update to benefit from it

  # --- failure and honesty --------------------------------------------------

  Scenario Outline: Every failure leaves no partial credential and says what happened
    Given a CLI starts a login
    When "<failure>" occurs
    Then no partial or unusable credential is stored
    And the operator is told what happened in terms they can act on

    Examples:
      | failure                              |
      | the user denies the request           |
      | the code expires before approval      |
      | the broker is unreachable             |
      | the broker returns an error           |
      | the account is suspended              |
      | the CLI is interrupted mid-poll       |

  Scenario: A headless machine can still sign in
    Given an operator on a machine with no browser, over SSH
    When they start a login
    Then they can complete it by opening the URI on another device and typing the code
    And nothing in the flow requires a browser on the machine being signed in
