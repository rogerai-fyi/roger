# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# FIRST-PARTY SIGN-IN — a RogerAI account of our own, entered with a code we mail.
#
# WHY THIS EXISTS. Today RogerAI has no account of its own. Every identity in the system is
# borrowed: an owner row is keyed on a GitHub id or an Apple sub, and a person who has
# neither cannot sign in at all. Four costs:
#
#   1. We cannot admit anyone who does not already hold a GitHub or Apple account. That is
#      most of the healthcare, defense, and industrial buyers the market set now names.
#   2. Two third parties can lock a customer out of a paid RogerAI account, including one
#      holding a wallet balance, and we have no recovery path that does not go through them.
#   3. Provider outage is total sign-in outage.
#   4. Provider policy is our policy. Apple's rules on what may be shown at sign-in, and
#      GitHub's on what an OAuth app may request, currently bound what our product may do.
#
# WHAT THIS IS NOT. It is not a replacement for GitHub and Apple - they remain offered, and
# an existing session, owner row, wallet, and bound key keep working untouched. This adds a
# third way in, and makes it the one we control.
#
# WHY A MAILED CODE AND NOT A PASSWORD. A password we do not store cannot leak, be reused
# from another site's breach, be stuffed, or need a reset flow - and a reset flow is itself
# a mailed-code flow, so a password would mean building BOTH and defending both. The mailed
# code is also the shape a person already recognizes from other AI products.
#
# GROUND TRUTH this builds on:
#   - rogerai.owners already carries a nullable `email` column, plus `welcomed_at` (the
#     once-only welcome stamp) and the soft-delete/anonymize pair. See internal/store.
#   - cmd/rogerai-broker/email.go is the flag-gated async mailer. It is INERT with no
#     provider key set, and it NEVER blocks or fails the caller.
#   - cmd/rogerai-broker/auth.go mints the HMAC-signed HttpOnly session cookie and the
#     JS-readable `signedInHint` companion, and `safeNext` in authnext.go validates a
#     post-login destination.
#   - internal/deviceauth is provider-agnostic already: it decides WHICH ACCOUNT, never
#     which key. An email sign-in therefore reaches `roger login` and `roger-tower login`
#     with no CLI change at all.
#
# THE THING THAT MAKES THIS DIFFERENT FROM OAUTH, AND WHY THE SPEC IS LONG. With OAuth, the
# provider owns the hard parts: proving the human, rate-limiting the guessing, resisting
# enumeration, and deciding when a credential is spent. Mailing our own code means WE own
# every one of them. The adversarial scenarios below are not defensive polish; they are the
# feature.

Feature: A person signs in to RogerAI with an emailed code
  A RogerAI account is keyed on a verified email address. Sign-in mails a short code, the
  code is spent the first time it is accepted, and neither a wrong code nor an unknown
  address tells the sender anything they did not already know.

  Background:
    Given a broker with a configured mailer
    And a person at a browser with no session

  # --- requesting a code ----------------------------------------------------

  Scenario: Requesting a code for a new address creates nothing yet and mails a code
    When they submit their email address
    Then a code is mailed to that address
    And no owner row exists for the address until a code is accepted
    And the response says a code was sent, without saying whether the address was known

  Scenario: Requesting a code for a known address mails a code and reveals nothing more
    Given an account already exists for the address
    When they submit their email address
    Then a code is mailed to that address
    And the response is byte-for-byte identical to the response for an unknown address
    And the two responses take indistinguishable time

  Scenario: The mailer being unconfigured fails the sign-in loudly rather than silently
    Given the broker has no mail provider key set
    When they submit their email address
    Then sign-in reports that emailed codes are unavailable
    And it does not claim a code was sent
    And the other sign-in providers remain offered

  Scenario Outline: An address that cannot receive mail is refused before anything is sent
    When they submit "<address>"
    Then the request is refused as an invalid address
    And no mail is enqueued
    And no owner row is created

    Examples:
      | address                  |
      |                          |
      | not-an-address           |
      | @rogerai.fm              |
      | someone@                 |
      | someone@@rogerai.fm      |
      | someone@localhost        |
      | someone@rogerai.fm\ncc:  |
      | someone@rogerai.fm\r\n   |
      | <script>@rogerai.fm      |

  Scenario: A header-injection attempt in the address never reaches the mail provider
    When they submit an address carrying a newline followed by an additional header
    Then the request is refused as an invalid address
    And the provider request body is never constructed

  Scenario: The address is normalized once, and the normalized form is what is stored
    When they submit an address differing only by surrounding whitespace and letter case
    Then it resolves to the same account as the canonical form
    And the stored address is the canonical form
    And a second sign-in with either spelling reaches that one account

  Scenario: Sub-addressing and dots are NOT normalized away
    When they submit an address that differs from a known one only by a plus-tag or a dot
    Then it is treated as a DIFFERENT address
    And a separate account is created for it if it is accepted

  # Why: collapsing "a.b+x@gmail.com" into "ab@gmail.com" bakes one provider's local-part
  # rules into our identity model. Get it wrong in the collapsing direction and two people
  # share an account; the rules also differ per provider and change without notice.

  # --- the code itself ------------------------------------------------------

  Scenario: The code is guess-resistant and readable aloud
    When a code is issued
    Then it is drawn from the operating-system random source
    And it is long enough that guessing it within its lifetime is infeasible under the
      attempt limits below
    And it is short enough to read from a phone and type into a terminal

  Scenario: The code is stored only as a hash
    When a code is issued
    Then what is persisted is a one-way hash of the code, not the code
    And a dump of the store does not let the reader sign in as anybody

  Scenario: A code is compared in constant time
    When a submitted code is checked
    Then the comparison does not return early on the first differing character

  Scenario: A code expires
    Given a code was issued
    When its lifetime elapses
    Then submitting it is refused
    And the refusal is the same refusal a wrong code receives

  Scenario: A code is spent on first acceptance
    Given a code was issued and accepted
    When the same code is submitted again
    Then it is refused
    And the session already minted is unaffected

  Scenario: Requesting a second code invalidates the first
    Given a code was issued
    When another code is requested for the same address
    Then the first code is no longer accepted
    And only the most recently issued code can be accepted

  # Why: a person who requests twice because the first mail was slow has two live codes
  # otherwise, and the older one sits in an inbox as a spare credential.

  Scenario: A code is bound to the address it was mailed to
    Given a code was mailed to address A
    When it is submitted together with address B
    Then it is refused
    And no account for either address is signed in

  # --- guessing and abuse ---------------------------------------------------

  Scenario: Wrong codes are limited per address, and the limit burns the code
    Given a code was issued for an address
    When wrong codes are submitted up to the attempt limit
    Then the next submission is refused even if it is the correct code
    And signing in requires requesting a fresh code

  Scenario: The attempt budget is per address, not global
    Given an attacker exhausts the attempt budget against their own address
    When an unrelated person submits their correct code
    Then it is accepted

  # Why: this mirrors the per-submitter `wrong` counter deviceauth already keeps. A single
  # global budget turns an anti-guessing control into a way to lock out the whole product.

  Scenario: Code requests are rate limited per address
    When codes are requested for one address faster than the request limit
    Then the excess requests are refused
    And no additional mail is sent for them
    And the refusal does not reveal whether the address has an account

  # Why: without this, anyone can use our mailer to flood a person's inbox, and our sending
  # domain wears the spam complaints.

  Scenario: Code requests are rate limited per source independently of the address
    When one source requests codes for many different addresses faster than the source limit
    Then the excess requests are refused

  # Why: the per-address limit alone does not stop a sender walking an address list, which
  # is both a mail-bomb amplifier and the reconnaissance half of an enumeration attack.

  Scenario: A submission attempt is rate limited per source
    When one source submits codes for many different addresses faster than the source limit
    Then the excess submissions are refused
    And the refusal is the same refusal a wrong code receives

  Scenario: Expired and spent codes are reaped rather than accumulating
    Given many codes have been issued and have expired
    Then they are removed from the store
    And the store does not grow without bound

  # --- what the sign-in mail may say ----------------------------------------

  Scenario: The mail states what is happening and what to do if it was not them
    When a sign-in code is mailed
    Then the mail names RogerAI as the sender
    And it states that somebody asked to sign in
    And it gives the code
    And it says the code expires, and when
    And it tells a person who did not request it that they need do nothing, and that nobody
      can sign in without the code

  Scenario: The mail never carries a one-click sign-in link
    When a sign-in code is mailed
    Then the mail contains no link that signs the recipient in by being followed
    And the code must be typed into the session that asked for it

  # Why: a followed link authenticates whoever followed it, in whatever browser followed it
  # - including a mail scanner. Requiring the code to be typed back into the ORIGINATING
  # session is what ties the person who asked to the person who arrives.

  Scenario: The mail does not disclose whether the address had an account
    When a sign-in code is mailed to an address with no account
    Then the wording is identical to the mail an existing account receives

  # --- becoming signed in ---------------------------------------------------

  Scenario: Accepting a correct code creates the account on first sign-in
    Given no account exists for the address
    When the correct code is submitted
    Then an owner row is created carrying the address as a verified email
    And a wallet is resolved for it
    And a session is minted

  Scenario: Accepting a correct code signs an existing account in without duplicating it
    Given an account exists for the address
    When the correct code is submitted
    Then no second owner row is created
    And the session carries the existing account and its wallet

  Scenario: The session minted is the same kind of session the other providers mint
    When a person signs in with an emailed code
    Then the signed session cookie is HttpOnly and Secure
    And the JS-readable signed-in hint is set alongside it
    And signing out clears both

  Scenario: An email sign-in returns the person to where they were going
    Given they arrived at sign-in carrying a destination
    When they complete the code
    Then they are returned to that destination
    And the destination is validated by the same same-site allowlist the OAuth callbacks use

  # Why: this is the open-redirect hole `safeNext` already closes for GitHub and Apple.
  # A third entrance that does not reuse it reopens it.

  Scenario: The welcome mail fires exactly once for an account, whichever way it was created
    Given an account has already been welcomed
    When it signs in with an emailed code
    Then no second welcome is sent

  # --- linking to the existing providers ------------------------------------

  Scenario: Signing in by email to an address a provider account already verified reaches ONE account
    Given an account was created through a provider, and that provider asserted this address
      as verified
    When the person signs in with an emailed code for the same address
    Then they reach that same account, its wallet, and its balance
    And no second account is created

  Scenario: An address a provider did NOT verify never links automatically
    Given a provider asserted an address it did not mark verified
    When somebody signs in with an emailed code for that address
    Then a separate account is created
    And the provider account is untouched

  # Why: auto-linking on an unverified provider address is a full account takeover. Anyone
  # who can set an arbitrary unverified email at a provider could claim a RogerAI account
  # holding a wallet balance. Both sides must be verified, or there is no link.

  Scenario: Linking never merges wallets or balances silently
    When two accounts are found to represent the same person
    Then they are not merged by this flow
    And the person is told how to have them merged deliberately

  Scenario: A person may add an email to a provider-created account while signed in
    Given they are signed in through a provider
    When they add an email address and accept the code mailed to it
    Then the address is recorded as verified on that account
    And they may afterwards sign in with either route

  Scenario: Adding an address already verified on another account is refused
    Given the address is verified on a different account
    When they try to add it
    Then it is refused
    And the other account is not disclosed beyond the fact the address is taken

  Scenario: A deleted account's address does not resurrect it
    Given an account was soft-deleted and anonymized
    When somebody signs in with an emailed code for the original address
    Then a NEW account is created
    And nothing from the deleted account's history, wallet, or balance is attached to it

  # --- the CLI and the Tower ------------------------------------------------

  Scenario: An emailed code satisfies a device approval with no CLI change
    Given a CLI has started a device login and shows a user code
    When the approver signs in with an emailed code and approves
    Then the device login completes and binds the CLI's key to the email account
    And the CLI is told which account it reached, never which provider proved it

  Scenario: roger-tower login works through the email route
    Given roger-tower has started a device login
    When the approver signs in with an emailed code and approves
    Then the Tower's account is recorded
    And the Tower reached no host other than the broker

  # --- privacy and retention ------------------------------------------------

  Scenario: The address is not written to logs
    When a code is requested, mailed, and accepted
    Then no log line carries the full address
    And no log line carries the code

  Scenario: Deleting an account scrubs the address the same way it scrubs the others
    When an account created by email is deleted
    Then the address is scrubbed and the row anonymized by the existing path
    And the financial rows the existing path preserves are still preserved
