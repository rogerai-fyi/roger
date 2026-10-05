# Link an email address to the account you are signed in with.
#
# Why: sign-in by emailed code creates its OWN account unless the address is already
# VERIFIED on a provider account (email_code_login.feature). A person who runs stations
# under GitHub or Apple and then signs in by email lands in a separate, empty account and
# sees "no stations". The fix is not to guess a match (auto-linking an unverified address
# is an account takeover): it is to let the signed-in person PROVE the address from inside
# the account that owns the stations. After that, the emailed code reaches that account.
#
# Security invariants (each is a scenario below):
#   - the session proves WHO is adding; the emailed code proves the address is theirs. Both.
#   - an address verified on another account is refused, uniformly, without naming it.
#   - nothing merges: balances and wallets are untouched (linking is not merging).
#   - a session with no owner row, an anonymous or a deleted account cannot link.
#   - the code is bound to the session that asked for it, single use, and rate limited.
# STATUS: APPROVED 2026-10-04 by the founder. Executable: cmd/rogerai-broker/email_link_bdd_test.go.

Feature: A signed-in person adds a verified email to their account

  Background:
    Given a person is signed in through GitHub with an account that runs stations

  Scenario: Adding an email requires a code mailed to that address
    When they ask to add "me@example.com"
    Then a code is mailed to "me@example.com"
    And the response does not reveal whether the address is known to RogerAI
    # test: TestAddingAnAddressRecordsItVerifiedOnThisAccountOnly,TestAnAddressWithAPipeCanBeLinked

  Scenario: Accepting the code records the address as verified on THIS account
    Given they asked to add "me@example.com" and received the code
    When they submit the code
    Then "me@example.com" is recorded as verified on their account
    And their GitHub login, wallet and balance are unchanged
    # test: TestAddingAnAddressRecordsItVerifiedOnThisAccountOnly

  Scenario: Afterwards the emailed code reaches the same account
    Given "me@example.com" was added and verified
    When somebody signs in with an emailed code for "me@example.com"
    Then the session is that account's session, with its stations and wallet
    # test: TestAfterAddingTheEmailedCodeReachesTheSameAccount

  Scenario: An address verified on a DIFFERENT account is refused
    Given "taken@example.com" is verified on another account
    When they ask to add "taken@example.com" and submit the code
    Then it is refused with "that address cannot be added"
    And the other account is not named, identified or touched
    # test: TestAnAddressVerifiedOnAnotherAccountIsRefusedWithoutNamingIt

  Scenario: Adding an address already verified on THIS account is idempotent
    Given "me@example.com" is already verified on their account
    When they add "me@example.com" again
    Then it succeeds and nothing changes
    # test: TestAddingTheSameAddressAgainIsIdempotent

  Scenario: Replacing the address drops the old proof
    Given "old@example.com" is verified on their account
    When they add and verify "new@example.com"
    Then "new@example.com" is verified and "old@example.com" no longer resolves to the account
    # test: TestReplacingTheAddressDropsTheOldProof

  Scenario: A wrong, expired or spent code is refused uniformly
    When they submit a wrong code, an expired code, or a code already used
    Then each is refused with the same message and the address is not recorded
    # test: TestAWrongSpentOrExpiredCodeIsRefusedUniformlyAndNothingIsRecorded,TestAnExpiredTokenIsRefused

  Scenario: The code only works in the session that asked for it
    Given one session asked to add "me@example.com"
    When a different session submits the code
    Then it is refused
    # test: TestATokenFromAnotherAccountOrForAnotherAddressOrTamperedIsRefused

  Scenario: Requires a signed-in session
    When an unauthenticated caller asks to add an address or submits a code
    Then it is refused with 401 and nothing is mailed
    # test: TestLinkRequiresASignedInSessionAndMailsNothingWithout

  Scenario: Requires the Origin check (login CSRF)
    When a cross-site request asks to add an address
    Then it is refused
    # test: TestLinkRequiresTheWebOrigin

  Scenario: A session with no owner row cannot link
    Given the signed-in session has no account row bound yet
    When they ask to add an address
    Then they are told to run roger login first, and nothing is mailed
    # test: TestASessionWithNoOwnerRowCannotLinkAndNothingIsMailed

  Scenario: A deleted or anonymized account cannot link
    Given the account was deleted
    When they ask to add an address
    Then it is refused
    # test: TestADeletedAccountCannotLink

  Scenario: An email-session can add nothing (its address is already its identity)
    Given the person is signed in by email
    When they ask to add another address
    Then it is refused with a message that the address is their sign-in
    # test: TestAnEmailSessionCannotAddAnotherAddress

  Scenario: Requests are rate limited per session and per address
    When more than the allowed number of codes are requested
    Then further requests are refused and no further mail is sent
    # test: TestLinkRequestsAreRateLimited,TestTheLinkBudgetIsPerAccountNotPerIP

  Scenario: No log line or mail carries the code outside the recipient's message
    When a code is requested
    Then the address is masked in logs and the code is never logged
    # test: TestLinkNeverLogsTheAddressOrTheCode

  Scenario: Linking never merges wallets
    Given an email account with a balance exists for "me@example.com"
    When the GitHub account adds "me@example.com"
    Then the two wallets stay separate and the person is told how to have them merged deliberately
    # test: TestLinkingNeverMergesWalletsOrBalances,TestLinkTellsTheOwnerWhenASeparateEmailWalletHoldsAFunds,TestLinkSaysNothingAboutMergingWhenThereIsNoSeparateWallet

  Scenario: The account page shows which sign-ins are linked and offers to add one
    Then the account page lists GitHub / Apple / email as linked or not
    And offers "Add an email" only to a provider session
    # test: web/test/account-link.test.mjs

  Scenario: A link token can never be replayed as a session cookie
    Given an attacker who can sign in chooses an address containing "|" to shape the token's payload
    When they re-encode the token as a session cookie naming a victim's wallet
    Then it is not a session, because link tokens are signed with their own derived key
    And a session whose numeric fields do not parse is not a session either
    # test: TestALinkTokenCanNeverBeReplayedAsASessionCookie,TestASessionWithAnUnparseableGitHubIdIsRejected,TestALinkTokenSignedWithTheSessionKeyIsRefused

  Scenario: A session that was live before its address was linked is not a mixed identity
    Given an email session was live when its address was linked to a GitHub account
    Then it resolves no owner until it signs in again, and a fresh sign-in is the full account
    # test: TestALiveEmailSessionIsNotAMixedIdentityAfterTheAddressIsLinked

  Scenario: A verified address is a credential, not a contact-email field
    Given an address is verified on an account
    When the contact email is patched to a different address
    Then the server refuses it, so the verification is never silently dropped
    And re-saving the same address (any case) is fine, and the page locks the field at once
    # test: TestPatchingAVerifiedAddressIsRefused,TestPatchingAVerifiedAddressToEmptyOrGarbageDoesNotDropIt,web/test/account-link.test.mjs

  Scenario: Flow namespaces cannot collide through an address that contains a pipe
    Given "|" is legal in an address
    When a sign-in code exists for "link|me@x" and a link code for "me@x"
    Then neither redeems as the other, and checking for a separate wallet creates none
    # test: TestTheMergeCheckHasNoSideEffect
