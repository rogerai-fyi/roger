# STATUS: APPROVED 2026-10-06 (founder)
# BUILD STATUS: IN PROGRESS (Go side, wt/edge-enroll-root). The phone half: docs/ios-enrollment-root-trust.md.
#
# ROGER EDGE - THE FIRST ENROLLMENT VERIFIES THE ROOT ("the answer proves where it came from").
#
# WHY THIS EXISTS
#
# enrollment.feature makes the Edge root the thing every later check stands on: discovery,
# presence, revocation and grants all verify against the root a machine took at enrollment.
# A machine that ALREADY holds a root refuses an answer rooted anywhere else (CheckResponse with
# Expect.Root, cmd/rogerai/edgeenroll.go). A machine that holds NONE - a first enrollment against a
# local authority, a phone's first claim - has nothing to compare against, so today it keeps the
# root on the word of whichever device answered. On a LAN that is not good enough: the enrollment
# listener is plain HTTP, the address is typed or discovered, and an answer is only as trustworthy
# as the path it took. Founder ruling 2026-10-06: the first enrollment must verify the root, as its
# own slice, now.
#
# THE DESIGN (extends the existing join flow; it does not invent a new one)
#
# The owner ALREADY performs an out-of-band step on the authority for every join:
#   - `roger edge authority allow <user key>` for a machine that enrolls by address
#     (features/edge/onboard.feature section 5), and
#   - ADOPT (`roger edge adopt`, [3] `a`, the console) for a device that joins by claim
#     (features/edge/claim.feature), and `roger edge readmit` for one coming back
#     (features/edge/node_identity.feature, re-admit).
# That step now ALSO mints a one-time ENROLLMENT CODE, shown on the authority's own screen, and the
# printed join command carries it. The authority's answer to that machine carries a ROOT PROOF:
#
#   k     = PBKDF2-HMAC-SHA256(code, salt = "rogerai-edge-root-proof/v1" || request nonce, N iterations)
#   proof = HMAC-SHA256(k, "rogerai-edge-root-proof/v1" \n root fingerprint \n account \n node id \n request nonce)
#
# The joiner recomputes it from the code the owner read off the authority. Only a device that knows
# the code can produce a proof for its root, so an answer rooted anywhere else is refused before a
# single byte is written. The joiner NEVER sends anything derived from the code: the code travels
# from the authority's screen to the joiner through the owner, and nowhere else.
#
# A second, secret-free form is accepted too: the FULL root fingerprint (`--root sha256:<64 hex>`,
# printed by `roger edge authority`, and carried by a pasteable invite). A full SHA-256 pin cannot
# be matched by a different root, so it needs no secret, no expiry and no authority-side support -
# which is also what makes it the answer for an authority that predates the proof.
#
# WHY NOT THE OTHER CANDIDATES
#   (b) the joiner shows the received root's SHORT fingerprint and the owner compares it by eye:
#       rejected as the trust decision. A short fingerprint of a public value can be matched by a
#       root made for the purpose; a human comparison of 8-16 hex characters is a hint, not a proof.
#       The short fingerprint is still SHOWN on both sides, as reassurance, never as the gate.
#   A PAKE (a password-authenticated key exchange) would make a short code safe with no stretching,
#   but iOS CryptoKit exposes no point arithmetic to build one on; PBKDF2 and HMAC are in
#   CommonCrypto and CryptoKit today, so the phone can implement this without a third-party library.
#
# WHAT DOES NOT CHANGE
#   - Authorization. The allow-list and the adopt grant still decide WHO may join. The code decides
#     only WHICH ROOT the joiner may believe. (RULED D15; superseded 2026-10-10 by founder ruling (simple join): a key-less roger edge invite admits the key that proves its code.)
#   - The certificate shape, the verification path, VerifyPeer, the Core-free structural guarantee.
#   - Renewal: a member renewing already holds its root and is checked against it, as today.
#
# GROUND TRUTH: cmd/rogerai/edgeenroll.go (edgeEnrollCore: the Expect built around lines 217-223,
# edgeAllow, edgeRefuseASecondAuthority, edgeShowAuthority), cmd/rogerai/edge.go (edgeAdoptWant,
# edgeAdoptByClaim, cmdEdgeReadmit, the subcommand table), cmd/rogerai/edgesetupcli.go
# (edgeSetupJoin), cmd/rogerai/edgehost.go (the plain-HTTP authority listener, host adopt),
# internal/edgeauth (identity.go CheckResponse + Expect, issue.go Response + Issue, local.go Allow /
# GrantClaim / ReAdmit / Claim, claim.go, request.go Nonce, edgeauth.go RootFingerprint),
# internal/edgeauth/enrollhttp (the wire, unchanged in shape), internal/protocol/band.go (the
# Crockford alphabet and confusable folding the code reuses), internal/tui/edge.go
# (edgeAdoptedMessage, the wizard), internal/webui/edge.go (handleEdgeAdopt), and the phone:
# roger-ios RogerAI/Services/Edge/EdgeClaim.swift, EdgeEnroll.swift, EdgeService.swift.
#
# Step definitions drive a REAL local authority, a REAL second "impostor" authority with a root of
# its own, the REAL plain-HTTP listener on loopback, and the REAL enrollment code. No mocks.
#
# Tags: @security (the trust decision), @cli, @tui, @console, @ios (behaviour the phone must match;
# its Go-side half runs here against golden vectors shared with roger-ios).
#
# ============================================================================
# FOUNDER RULINGS 2026-10-06: all fifteen as recommended (each repeated as # RULED beside its scenarios)
# ============================================================================
#   D1  Design: code-bound root proof + optional full-fingerprint pin.  RULED: yes, as above.
#   D2  Code: 10 Crockford base32 symbols, 50 bits, shown XXXXX-XXXXX.  RULED: yes.
#   D3  Stretch: PBKDF2-HMAC-SHA256, 200,000 iterations, salted per request.  RULED: yes.
#   D4  Lifetime: 15 minutes on the authority; the joiner keeps an answer for 15 minutes.  RULED: yes.
#   D5  Wrong-code cap on the joiner: 5 entries, then the code is discarded.  RULED: 5.
#   D6  One-time: a code is spent when its certificate is issued; minting again voids the old one.  RULED: yes.
#   D7  No trust-on-first-use escape hatch, no flag that skips the check.  RULED: none, ever.
#   D8  An authority that sends no proof: the pin works, the code alone fails closed.  RULED: yes.
#   D9  A new authority does not refuse an old joiner (refusing protects nobody); it warns.  RULED: warn.
#   D10 Core: TLS to Core settles the root; a non-loopback plain-HTTP broker is refused for Edge
#       enrollment.  RULED: yes.
#   D11 A different root on re-enroll: accepted only with a code or pin for it AND an explicit
#       confirmation, ending the old membership in the same step.  RULED: yes.
#   D12 QR: the pasteable invite ships in this slice; drawing a QR and scanning it on the phone
#       is the next UX slice.  RULED: defer QR (it is convenience, not security).
#   D13 Where the code is shown: the adopt/allow output and the adopting panel, until spent or
#       expired; never in a log, an event, the trouble feed or an advertisement.  RULED: yes.
#   D14 Order: hold the answer, THEN ask for the code (a typo never burns the code), rather than a
#       separate pre-flight endpoint.  RULED: hold-then-verify.
#   D15 The code does NOT replace the allow-list in this slice.  RULED: separate slice if wanted. # superseded 2026-10-10 by founder ruling (simple join): roger edge invite mints a bearer code that admits on use; allow still works.
#
# SCENARIOS THIS SUPERSEDES OR AMENDS (each marked "# superseded 2026-10-06 by founder ruling
# (enrollment root trust)" in its own file):
#   enrollment.feature (APPROVED 2026-09-12)
#     - "an Edge with no internet at all forms completely under a local authority": the second
#       machine now also needs the code (or pin) from the authority. Amended, not contradicted.
#     - "changing authority is a deliberate migration, never a silent switch": "until a member
#       re-enrolls" now means re-enrolling WITH the new authority's code (section 9). Today a
#       member holding the old root is refused by the new one, and the refusal tells it to run
#       `roger edge authority local <name> --force`, which would make the MEMBER an authority.
#     - "a node belongs to exactly one Edge": unchanged for an unconsented second account; D11
#       adds the one consented path (code + confirmation) that ends the first membership.
#   onboard.feature (APPROVED, built)
#     - "joining asks for the authority's address as well as a name": also the code.
#     - "a successful join enrolls against the authority over the LAN": only once the code matches.
#     - "joining before being allowed shows this machine's key and the exact allow command" and
#       "retrying after being allowed completes the join": the allow command now prints the code,
#       and the retry asks for it.
#     - "the done step of a new Edge says how to add the next machine": it names allow/adopt as the
#       step that produces the code, not a bare enroll command.
#     - @cli "roger edge setup joins, and guides the allow step when not yet allowed": same.
#   onboard_web.feature (PROPOSED): the join modal gains the code field.
#   claim.feature (PROPOSED 2026-09-23, implemented)
#     - "adopting a candidate grants it a claim, and the claim issues a certificate": the phone
#       keeps the certificate only once the code verifies.
#     - "a claimed candidate then presents and is drawn as a verified member": "no address typed
#       on the phone and no manual allow" still holds; the phone now takes a 10-symbol code.
#   node_identity.feature, re-admit (APPROVED 2026-10-02)
#     - "a forgotten device is re-admitted by the name it had": readmit also mints a code; a device
#       that still holds this Edge's root needs none (section 10).

Feature: A machine joining an Edge for the first time believes only a root the owner can vouch for

  Background:
    Given a local Edge authority "shed" whose root this machine has never held
    And the authority answers enrollment and claims on its LAN listener

  # =========================================================================
  # 1. WHAT NEEDS PROOF, AND WHAT ALREADY HAS IT
  # =========================================================================

  @security
  Scenario: a first enrollment against a local authority requires a code or a pin before anything is kept
    Given this machine holds no Edge root
    When it enrolls against "shed" with neither a code nor a root pin
    Then it keeps no root, no certificate, no node key and no fleet row
    And it says the root of the Edge it is joining has to be confirmed
    And it names where the code comes from: allow or adopt on the authority
    And it names the pin alternative: the full root fingerprint `roger edge authority` prints there

  @security @cli
  Scenario: a non-interactive first enrollment with no code and no pin is refused before anything is sent
    Given no interactive input is available
    When the owner runs "roger edge enroll bench --authority http://192.168.1.10:8791"
    Then it is refused before any request leaves this machine
    And the refusal says to pass --code <code> from the authority, or --root sha256:<fingerprint>
    And it exits non-zero

  @security
  Scenario: a renewal is checked against the root the member already holds, and needs no code
    Given this machine is enrolled under "shed" and holds its root
    And its certificate is close to expiry
    When it renews against "shed" with no code
    Then the answer is accepted because its root equals the one already held
    And the node id, the name and the place in the fleet are unchanged

  @security
  Scenario: the authority enrolling itself needs no code, because its root is its own
    Given this machine is the designated authority "shed" and has not enrolled itself
    When the owner enrolls this machine with no --authority
    Then it enrolls in-process against the root it generated
    And no code is minted, asked for or checked
    And nothing crossed the network

  @security
  Scenario: re-running enroll on a member that is not due for renewal stays a clean no-op
    Given this machine is enrolled under "shed"
    When the owner runs enroll again with a code that does not match anything
    Then nothing is sent, nothing is checked and nothing changes
    And it says this machine is already enrolled, as enrollment.feature requires

  # =========================================================================
  # 2. THE ENROLLMENT CODE - minted by the step the owner already takes
  # =========================================================================

  # RULED 2026-10-06: D2 - 10 Crockford base32 symbols (50 bits), shown as two groups of five.
  # The house alphabet (internal/protocol/band.go), no I/L/O/U, readable aloud, and with D3 + D4 the
  # entropy is far beyond what an attacker can search within a code's life (section 8).

  @security @cli
  Scenario: allowing a machine mints a code and prints the exact join command carrying it
    When the owner runs "roger edge authority allow <that machine's user key>" on "shed"
    Then it prints a one-time enrollment code shaped XXXXX-XXXXX
    And it prints the join command for that machine, including --code and the authority's address
    And it prints the root's short fingerprint, so the owner can see the joiner name the same one
    And it says the code is good for 15 minutes and for one join

  @security @cli
  Scenario: adopting a device that joins by claim mints a code and shows it
    Given a phone advertising as a candidate "pocket"
    When the owner runs "roger edge adopt pocket" on "shed"
    Then it prints a one-time enrollment code for "pocket"
    And it says to enter that code on "pocket" to finish joining

  @security @cli
  Scenario: re-admitting a forgotten device mints a fresh code
    Given a device "attic" that was forgotten
    When the owner runs "roger edge readmit attic" and confirms
    Then it prints a one-time enrollment code for "attic"
    And it says a device that still holds this Edge's root will not need it

  @security
  Scenario: a code is drawn from the operating system's secure random source
    When 10000 codes are minted
    Then every symbol is from the Crockford alphabet
    And no two codes are equal
    And each symbol position is uniformly distributed within statistical tolerance

  # RULED 2026-10-06: D6 - minting again for the same machine voids the previous code. The
  # owner who lost a code just runs allow (or adopt) again, and only one code is ever live per join.
  @security
  Scenario: minting a new code for the same machine voids the previous one
    Given the owner allowed a machine and was shown code A
    When the owner allows the same machine again and is shown code B
    Then an answer to that machine carries a proof made with B
    And a proof made with A is never produced again

  @security
  Scenario: a code is bound to the join it was minted for
    Given code A was minted for machine "bench" and code B for the phone "pocket"
    When "pocket" enrolls
    Then the authority's answer to "pocket" carries a proof made with B only
    And "bench"'s code A remains live and unspent

  # RULED 2026-10-06: D13 - where the code may appear. On the screen where the owner asked for it
  # and in that join's adopting panel until it is spent or expires; nowhere that persists or travels.
  @security
  Scenario Outline: the code never appears where it could outlive the join or leave the machine
    Given a code was minted for a join
    Then it does not appear in <place>

    Examples:
      | place                                     |
      | the mDNS advertisement                    |
      | any request or answer on the wire          |
      | the roger log                             |
      | the Edge events feed                      |
      | the trouble feed                          |
      | the console's unauthenticated reads       |
      | the fleet store or the departed record    |

  @security
  Scenario: the authority keeps a live code readable only by its owner, and deletes it when done
    Given a code was minted on "shed"
    Then it is stored in the authority's directory readable only by its owner
    When the code is spent or expires
    Then it is deleted from that store
    And a restart of the authority does not bring it back

  @security
  Scenario: a code survives an authority restart while it is still live
    Given a code was minted on "shed" 2 minutes ago
    When the authority restarts
    And the machine enrolls with that code
    Then the answer carries a valid proof made with it

  # =========================================================================
  # 3. HAPPY PATHS
  # =========================================================================

  @security @cli
  Scenario: a machine joins with the code the authority printed
    Given the owner allowed this machine on "shed" and was shown its code
    When the owner runs "roger edge enroll bench --authority <shed's address> --code <that code>"
    Then the answer's root proof verifies with that code
    And this machine keeps the certificate and the root
    And it prints that it joined the Edge rooted at "shed", with the root's short fingerprint

  @security @cli
  Scenario: an interactive enroll asks for the code after the authority answers
    Given the owner allowed this machine on "shed"
    And input is interactive
    When the owner runs "roger edge enroll bench --authority <shed's address>" with no code
    Then the request is sent and the answer is held, not kept
    And it asks for the code shown on the authority
    When the owner types the code
    Then the held answer's proof verifies and the identity is kept

  # RULED 2026-10-06: D14 - hold the answer, then ask. Hold-then-verify. Nothing derived from the
  # code is ever sent, so the order is free; asking after the answer means a mistyped code is just
  # typed again, rather than spending the authority's one-time code on an answer nobody kept.
  @security @cli
  Scenario: a mistyped code is typed again against the same held answer
    Given an interactive enroll is holding the authority's answer
    When the owner types a code with one wrong symbol
    Then it says the code does not match the Edge that answered, and asks again
    When the owner types the right code
    Then the identity is kept
    And the authority was asked only once

  @security @cli
  Scenario: a machine joins with the full root fingerprint instead of a code
    Given the owner ran "roger edge authority" on "shed" and it printed the full root fingerprint
    When the owner runs "roger edge enroll bench --authority <shed's address> --root sha256:<that fingerprint>"
    Then the answer's root hashes to exactly that fingerprint
    And this machine keeps the certificate and the root
    And no code was needed

  @security @cli
  Scenario: roger edge authority prints the full fingerprint in the form --root takes
    When the owner runs "roger edge authority" on "shed"
    Then it prints the root's full SHA-256 fingerprint as sha256:<64 hex>
    And it prints the short form beside it for reading aloud

  @security @cli
  Scenario Outline: the pin is accepted however the owner pasted it, and only in full
    When the owner passes --root "<pin>"
    Then the pin is <verdict>

    Examples:
      | pin                                              | verdict                              |
      | sha256:<64 lowercase hex>                        | accepted                             |
      | sha256:<64 UPPERCASE hex>                        | accepted                             |
      | <64 hex, no prefix>                              | accepted                             |
      | sha256:<64 hex grouped with colons or spaces>    | accepted                             |
      | sha256:<the first 16 hex only>                   | refused as too short to be a pin     |
      | sha256:<63 hex>                                  | refused as not a full fingerprint    |
      | sha256:<64 chars with a non-hex symbol>          | refused as not a fingerprint         |
      | md5:<32 hex>                                     | refused as an unsupported hash       |

  @security @cli
  Scenario: a pasteable invite carries the address, the full pin and the code in one string
    Given the owner allowed this machine on "shed"
    Then the allow output also prints an invite string
    When the owner runs "roger edge enroll bench --invite <that string>"
    Then it enrolls against the address in the invite
    And the root must match the invite's pin AND the proof must verify with the invite's code
    And an invite whose pin and code disagree about the root is refused, keeping nothing

  @security @cli
  Scenario Outline: the code is accepted however it was transcribed
    When the owner enters the code as "<typed>"
    Then it is read as the code "<canonical>"

    Examples:
      | typed         | canonical  |
      | 7KQ2M-9XH4P   | 7KQ2M9XH4P |
      | 7kq2m-9xh4p   | 7KQ2M9XH4P |
      | 7KQ2M 9XH4P   | 7KQ2M9XH4P |
      | 7KQ2M9XH4P    | 7KQ2M9XH4P |
      | 7KQ2MO9XH4P   | refused: 11 symbols |
      | 1LQ2M-9XH4P   | 11Q2M9XH4P |
      | O0Q2M-9XH4P   | 00Q2M9XH4P |
      | 7KQ2U-9XH4P   | refused: U is not a code symbol |

  @security
  Scenario: an adopted phone keeps its certificate only once the code verifies
    Given the owner adopted the phone "pocket" on "shed" and was shown its code
    When "pocket" claims and "shed" answers with a certificate and a root proof
    Then "pocket" holds the answer and asks for the code shown on the authority
    When the code is entered on "pocket"
    Then the proof verifies and "pocket" keeps its membership
    And it presents and is drawn as a verified member, as claim.feature describes

  @security
  Scenario: a phone joining by typed address and an allowed account key uses the same code
    Given the owner allowed the phone's account key on "shed" and was shown a code
    When the phone joins "shed" by address and the code is entered
    Then the proof verifies and the phone keeps its membership

  @security @cli
  Scenario: a headless machine joins with the code on the command line and no prompt
    Given a machine with no terminal, run by the boot service
    When it runs "roger edge enroll pi-cabinet --authority <shed's address> --code <code>"
    Then it enrolls without prompting
    And with a wrong code it exits non-zero, keeping nothing, and never waits for input

  @security
  Scenario: the joiner and the authority both show the same short root fingerprint
    When a machine joins "shed" with a code
    Then the joiner prints the root's short fingerprint
    And it is the same short fingerprint "roger edge authority" prints on "shed"
    And the joiner says the fingerprint is for reassurance and the code is what was checked

  # =========================================================================
  # 4. AN IMPOSTOR ANSWERS FIRST
  # =========================================================================

  @security
  Scenario: an answer rooted somewhere else is refused, with a clear message, and nothing is stored
    Given an impostor authority with a root of its own answers this machine's enrollment
    And the owner holds the code "shed" printed
    When this machine checks the answer
    Then the root proof does not verify
    And this machine keeps no root, no certificate, no node key and no fleet row
    And it says: the device that answered at that address is not the Edge that showed you this code
    And it names the address that answered and the short fingerprint of the root it offered
    And it does not tell the owner to retry with a different code

  @security
  Scenario: an impostor that sends no proof at all is refused the same way
    Given an impostor answers with a valid-looking certificate and root and no root proof
    And the owner holds a code
    When this machine checks the answer
    Then it is refused, keeping nothing
    And the message says the answer carried no proof for the code

  @security
  Scenario: an impostor whose root has the SAME short fingerprint as the real one is still refused
    Given an impostor whose root's short fingerprint equals "shed"'s
    When this machine joins with the code, or with the full pin
    Then the proof does not verify, and the full fingerprint does not match
    And it is refused, keeping nothing

  @security
  Scenario: an impostor that answers a pinned join is refused
    Given the owner gave --root with "shed"'s full fingerprint
    And an impostor answers with its own root
    When this machine checks the answer
    Then the root does not hash to the pin
    And it is refused, keeping nothing, naming the expected and the offered fingerprints

  @security
  Scenario: an impostor that forwards the real answer unchanged gains nothing
    Given a device between this machine and "shed" that passes the request and answer through
    When this machine joins with the code
    Then the proof verifies because the root is "shed"'s
    And this machine holds a certificate for its own node key under "shed"'s root
    And the device in between holds nothing it can use: no key, no certificate of its own, no code

  @security
  Scenario: an impostor that swaps the real certificate onto its own root is refused
    Given an impostor answers with "shed"'s proof but its own root
    When this machine checks the answer
    Then the proof does not verify for that root
    And it is refused, keeping nothing

  @security
  Scenario: an impostor that keeps the real root but substitutes its own certificate is refused
    Given an impostor answers with "shed"'s root and proof and a certificate it signed itself
    When this machine checks the answer
    Then the certificate does not verify under the root
    And it is refused as "unknown authority", keeping nothing

  @security
  Scenario Outline: a proof is bound to every field that decides the membership
    Given a real answer from "shed" whose proof verifies
    When an impostor changes the <field> and keeps the proof
    Then the proof does not verify
    And it is refused, keeping nothing

    Examples:
      | field                          |
      | root                           |
      | account                        |
      | node id                        |
      | request nonce it answers       |
      | proof version                  |

  @security
  Scenario: a proof captured from another machine's join cannot be replayed to this one
    Given a valid answer and proof that "shed" gave a different machine
    When an impostor replays it to this machine
    Then it names another node and another nonce
    And it is refused, keeping nothing

  @security @ios
  Scenario: a phone that sees several authorities keeps only the one the code verifies
    Given the phone "pocket" was adopted on "shed"
    And an impostor also advertises itself as an authority on the same network
    When both answer "pocket"'s claims with a certificate
    Then "pocket" holds both answers and asks for the code once
    When the code from "shed" is entered
    Then "pocket" keeps "shed"'s answer only
    And it reports the other address as an authority that could not prove its root
    And nothing from the impostor's answer is stored

  @security @ios
  Scenario: a phone holds a bounded number of unverified answers
    Given more than 4 devices answer "pocket"'s claims with certificates
    Then "pocket" holds at most 4 answers, the earliest received
    And it says more devices answered than an Edge should, and names how many

  @security
  Scenario: an impostor answering first is reported once, plainly, as a possible impostor
    Given an impostor's answer has been refused for a join
    Then the joiner prints one line naming the address and the offered root's short fingerprint
    And it says another device on the network answered as an Edge authority it could not verify
    And it does not repeat the line on every retry of the same join

  # =========================================================================
  # 5. A WRONG CODE
  # =========================================================================

  @security @cli
  Scenario: a wrong code with --code is refused, keeping nothing
    Given the owner allowed this machine on "shed"
    When the owner runs enroll with --code set to a code "shed" never minted
    Then the proof does not verify
    And nothing is kept
    And it says the code does not match the Edge that answered, and to check it or mint a fresh one

  @security
  Scenario: a code from a different Edge is refused, keeping nothing
    Given two Edges on one network, "shed" and "lab", each with its own root
    And the owner holds a code minted by "lab"
    When this machine enrolls against "shed" with that code
    Then the proof does not verify
    And it is refused, keeping nothing

  @security
  Scenario: a code minted for a different machine on the same Edge does not verify for this one
    Given "shed" minted code A for the phone "pocket" and code B for this machine
    When this machine enrolls with code A
    Then the answer's proof was made with B
    And it is refused, keeping nothing

  @security
  Scenario: a malformed code is refused before anything is sent
    When the owner runs enroll with --code "not-a-code"
    Then it is refused naming what a code looks like
    And no request is sent

  # =========================================================================
  # 6. REPLAY
  # =========================================================================

  @security
  Scenario: a code is spent when the certificate it vouches for is issued
    Given this machine joined "shed" with its code
    When another enrollment for the same allowed key arrives
    Then the authority's answer carries no proof made with the spent code
    And a joiner presenting that spent code refuses the answer, keeping nothing
    And it says the code was already used and to mint a fresh one on the authority

  @security
  Scenario: a joiner forgets a code the moment it has been used
    Given this machine joined "shed" with its code
    Then the code is not stored anywhere on this machine
    And a later enroll does not reuse it

  @security
  Scenario: a claim's code is spent with the claim grant, in the same locked step
    Given the phone "pocket" was adopted and claims its certificate
    Then the grant and the code are spent together
    And a second claim for "pocket" is refused as not adopted, as claim.feature requires
    And no second proof is produced for that code

  @security
  Scenario: two racing enrollments cannot both be vouched for by one code
    Given one live code for this machine's allowed key
    When two enrollments for that key arrive at the same moment
    Then at most one answer carries a proof made with that code

  @security
  Scenario: replaying a whole earlier signed request does not yield a second proof
    Given a signed enrollment request "shed" already answered
    When the identical request is sent again
    Then it is refused as already used, as enrollment.feature requires
    And no proof is produced

  # =========================================================================
  # 7. EXPIRY
  # =========================================================================

  # RULED 2026-10-06: D4 - 15 minutes. The owner is standing between two screens; a longer
  # life widens the window an offline search would have (section 8) and buys no convenience, since
  # minting a fresh code is one command or one key.
  @security
  Scenario: a code expires on the authority after 15 minutes
    Given a code was minted on "shed" 16 minutes ago and never used
    When this machine enrolls with it
    Then the answer carries no proof for that code
    And the joiner refuses it, keeping nothing
    And it says the code has expired and to mint a fresh one with allow or adopt

  @security
  Scenario: a code used just inside its life is honoured
    Given a code was minted on "shed" 14 minutes ago
    When this machine enrolls with it
    Then the proof verifies and the identity is kept

  @security
  Scenario: the joiner stops trusting a held answer 15 minutes after it arrived
    Given an interactive enroll has held the authority's answer for 16 minutes without a code
    When the owner types the right code
    Then the held answer is discarded, not checked
    And it says to run the join again, keeping nothing

  @security
  Scenario: an expired code is pruned and a restart does not revive it
    Given a code expired on "shed"
    When the authority restarts
    Then the code is not in its store
    And no answer carries a proof made with it

  @security
  Scenario: an adopt grant can outlive its code, and re-adopting mints a fresh one
    Given the phone "pocket" was adopted 20 minutes ago and has not claimed
    Then its code has expired while its adopt grant is still live
    And the adopting panel says the code expired and offers to show a new one
    When the owner adopts "pocket" again
    Then a fresh code is shown and the old one is void

  @security
  Scenario: the authority's clock being wrong is never a reason to accept
    Given the authority's clock is a day behind
    When this machine enrolls with a code minted on that authority
    Then the code's life is measured on the authority's own clock, from when it was minted
    And the joiner's own 15-minute window is measured on the joiner's clock

  # =========================================================================
  # 8. BRUTE FORCE - why 50 bits is enough, and what bounds every kind of guessing
  # =========================================================================

  # RULED 2026-10-06: D3 - PBKDF2-HMAC-SHA256, 200,000 iterations, salted with the request's own nonce.
  # A device that relays a real exchange sees one proof and could search for the code
  # offline; the stretch and the per-request salt make that search cost ~2^49 x 200,000 hashes on
  # average, with no table to precompute, against a code that dies in 15 minutes. PBKDF2 is in Go's
  # standard library and in iOS CommonCrypto, so the phone needs no new dependency.
  @security
  Scenario: the proof key is stretched and salted per request
    Then the proof key is PBKDF2-HMAC-SHA256 of the canonical code
    And its salt is the domain string followed by the request's own nonce
    And it uses 200000 iterations and a 32-byte output
    And two requests with the same code produce unrelated keys

  @security
  Scenario: the code has 50 bits of entropy
    Then a code is 10 symbols from a 32-symbol alphabet
    And every symbol is drawn independently and uniformly

  # RULED 2026-10-06: D5 - 5 wrong entries per held answer, then the code is discarded. It
  # keeps an impostor's chance at 5 in 2^50, and still forgives an owner who fumbles twice.
  @security
  Scenario: the joiner caps how many codes it will try against held answers
    Given an interactive enroll is holding an answer
    When the owner enters 5 codes that do not verify
    Then the held answers are discarded and nothing is kept
    And it says the code did not match 5 times, that a device on the network may be impersonating the authority, and to mint a fresh code
    And a sixth entry is not checked

  @security @cli
  Scenario: --code is one attempt; a wrong one is not retried automatically
    When the owner runs enroll with a --code that does not verify
    Then it is checked exactly once
    And the command exits non-zero, keeping nothing

  # superseded 2026-10-10 by founder ruling (simple join, decision 5): a key-less "roger edge invite"
  # admits the key that proves its code, so that one proof is sent and checked, against a guess
  # budget. A code minted for an allowed key (allow, adopt, readmit) is still never sent or compared.
  @security
  Scenario: nothing the authority accepts from a caller is checked against an allowed key's code
    Then no request field carries the code or anything derived from it, apart from a bearer invite's join proof
    And the authority never compares a caller's input to a code minted for an allowed key
    And a bearer invite's proof is checked against its own guess budget, then the invite is void

  @security
  Scenario: the authority produces at most one proof per code
    Given one live code for a join
    When the authorized joiner's requests are answered
    Then only the answer that issues a certificate carries a proof
    And answers to unauthorized requests never carry one

  @security
  Scenario: an unauthorized caller gets no proof and learns nothing about a code
    Given a live code for an allowed machine
    When a caller whose key is not allowed sends an enrollment request
    Then it is refused exactly as today, with no root proof in the refusal
    And the refusal is byte-identical whether or not a code is live

  @security @ios
  Scenario: the phone applies the same cap and the same stretch as the CLI
    Then the phone's proof check uses the same derivation, the same 5-entry cap and the same window
    And it agrees with the Go implementation on every golden vector

  # =========================================================================
  # 9. A ROOT THAT CHANGES ON RE-ENROLL
  # =========================================================================

  @security
  Scenario: a member whose authority now answers with a different root, and no code, is refused
    Given this machine is enrolled under "shed" and holds its root
    When it re-enrolls and the answer carries a different root
    Then it is refused as "that certificate comes from a different authority"
    And its identity, its root and its fleet row are unchanged

  @security @cli
  Scenario: the refusal of a different root tells a MEMBER what it can do, not an authority's command
    Given this machine is a member, not an authority
    When a re-enroll is refused for a different root
    Then the message says the Edge it belongs to is rooted elsewhere
    And it says that if the owner moved the Edge on purpose, the new authority's allow or adopt prints a code, and to re-enroll with it and --replace-root
    And it does not tell the member to run "roger edge authority local --force"

  # RULED 2026-10-06: D11 - a different root is accepted only with a code (or full pin) for it AND an explicit
  # confirmation, and the old membership ends in the same step. The code is the owner's
  # proof that the new root is theirs; the confirmation is the owner's consent to leave the old one.
  @security @cli
  Scenario: after the owner moved the Edge, a member re-enrolls with the new authority's code
    Given the Edge moved from "shed" to the new authority "barn"
    And this machine is marked NEEDS RE-ENROLLMENT and holds "shed"'s root
    And "barn" allowed this machine and printed a code
    When the owner runs enroll against "barn" with that code and --replace-root
    Then it shows the old root's short fingerprint and the new one's, and asks y/N
    When the owner answers yes
    Then the old identity and root are replaced by the new ones in one step
    And this machine is a member under "barn"
    And it no longer verifies peers against "shed"'s root

  @security
  Scenario: answering no to a root change leaves the machine exactly as it was
    Given a re-enroll against a different root, with a verified code and --replace-root
    When the owner answers no
    Then nothing changes: the old identity, root and fleet row remain

  @security
  Scenario: --replace-root without a code or pin for the new root is refused
    Given this machine holds "shed"'s root
    When it re-enrolls against "barn" with --replace-root and no code or pin
    Then it is refused before anything is kept
    And it says a new root needs the code from its authority

  @security
  Scenario: --replace-root with a code that does not verify is refused, keeping the old membership
    Given this machine holds "shed"'s root
    When it re-enrolls against "barn" with --replace-root and a wrong code
    Then it is refused
    And the old identity, root and fleet row are unchanged

  @security @cli
  Scenario: a non-interactive root change needs --yes as well as --replace-root
    Given no interactive input is available
    When the owner runs enroll against "barn" with a verified code and --replace-root but no --yes
    Then it is refused, saying a root change must be confirmed, and changes nothing

  @security
  Scenario: an answer carrying the SAME root a member holds needs no code, even with --replace-root
    Given this machine holds "shed"'s root
    When it re-enrolls against "shed" with --replace-root
    Then it says the root is unchanged and nothing is replaced

  @security
  Scenario: a forgotten machine that kept its root re-enrolls against it with no code
    Given this machine was forgotten by "shed" but still holds "shed"'s root
    And the owner re-admitted it
    When it enrolls against "shed" with no code
    Then the answer's root equals the root it holds, so it is accepted
    And it comes back as described by node_identity.feature's re-admit scenarios

  # =========================================================================
  # 10. RE-ADMIT AND RE-ADOPT
  # =========================================================================

  @security @ios
  Scenario: a phone that still holds this Edge's root re-claims with no code
    Given the phone "pocket" left the Edge but kept "shed"'s root
    And the owner re-admitted "pocket"
    When "pocket" claims
    Then the answer's root equals the root it holds, so it is kept with no code entered

  @security @ios
  Scenario: a phone that holds no root after re-admit needs the fresh code
    Given the phone "pocket" was reset and holds no root
    And the owner re-admitted "pocket" and was shown a code
    When "pocket" claims
    Then it holds the answer and asks for the code
    And it keeps the membership only once the code verifies

  @security
  Scenario: a re-admitted device presented with a different root than the one it holds is refused
    Given the phone "pocket" holds "shed"'s root
    When an answer to its claim carries a different root and no code was entered
    Then it is refused as a different authority, keeping its old root

  # =========================================================================
  # 11. DOWNGRADE - an authority that sends no proof
  # =========================================================================

  # RULED 2026-10-06: D8 - against an authority from before proofs, a code cannot be checked, so a code-only
  # join fails closed; the full-fingerprint pin needs nothing from the authority and works.
  # The owner always has a working path, and it is never the unsafe one.
  @security
  Scenario: a code-only join against an authority that sends no proof is refused
    Given "shed" runs a roger from before root proofs
    When this machine enrolls with a code
    Then the answer carries no root proof
    And it is refused, keeping nothing
    And it says that authority does not prove its root, and to either update roger on it or join with --root and the full fingerprint "roger edge authority" prints there

  @security
  Scenario: a pinned join against an authority from before proofs works
    Given "shed" runs a roger from before root proofs
    When this machine enrolls with --root and "shed"'s full fingerprint
    Then the answer's root matches the pin and it is kept

  @security
  Scenario: stripping the proof from a new authority's answer is the same refusal, not a fallback
    Given "shed" sends root proofs
    And a device in between removes the proof from the answer
    When this machine enrolls with a code
    Then it is refused exactly as against an authority that sends none
    And nothing about the answer's version or headers can make the joiner skip the check

  # RULED 2026-10-06: D7 - no trust-on-first-use switch. None, ever. A flag that skips the check is
  # the flag every how-to would tell the owner to use; the pin is the always-available safe path.
  @security @cli
  Scenario: there is no option that accepts an unproven root
    Then "roger edge enroll --help" lists --code, --root, --invite, --replace-root and --yes
    And no option, environment variable or config key makes a first enrollment keep an unproven root

  @security
  Scenario: a proof in a version this joiner does not know is refused
    Given an answer whose root proof names a version this joiner does not implement
    When this machine checks it
    Then it is refused, keeping nothing
    And it says the authority is newer and to update roger here, or to join with the full pin

  # =========================================================================
  # 12. MIXED-VERSION FLEETS
  # =========================================================================

  # RULED 2026-10-06: D9 - a new authority does not refuse a joiner too old to check proofs. Warn,
  # do not refuse. The old joiner's exposure is to a device that is NOT this authority; refusing it
  # here protects it from nothing and only strands it. The owner is told plainly instead.
  @security @cli
  Scenario: allow and adopt warn that a joiner must be new enough to check the code
    When the owner allows a machine or adopts a device on "shed"
    Then the output says a joiner running a roger from before root proofs does not check the code
    And it says to update roger on that machine before it joins

  # RULED 2026-10-13 (founder): D9 FIXED - "fix". The warning printed on EVERY allow and adopt because
  # nothing told the authority that a joiner checks codes. Now a joiner that verifies the root proof
  # says so with `checks_root: true` (an optional JSON boolean) on its enroll request, its bearer join
  # (the same enroll request with join_proof) and its claim request. The authority notes what each
  # SIGNED, fresh request said and warns only about a joiner it heard WITHOUT the flag, or one it has
  # not heard yet (the general line above). A phone polls its claim before it is adopted, so by the
  # adopt the authority has usually heard it.
  #
  # The flag is deliberately OUTSIDE the signed canonical forms (rogerai-edge-enroll/v1,
  # rogerai-edge-claim/v1): those bytes are golden-pinned on the phone and the CLI, and an authority
  # from before the flag would refuse a signature over a form it does not know. A device in the middle
  # that flips it changes whether the owner is warned, never what is issued or what the joiner checks.
  # Every authority decodes these requests with plain json.Unmarshal, so an older one skips the field.
  # Notes from requests that were refused (not adopted, not allowed yet) are signed by keys anyone can
  # make, so they are budgeted (30 new joiners a minute) and the book is bounded (256 per kind).
  @security @cli
  Scenario: a phone that said it checks codes is adopted with no warning
    Given the phone "pocket" asked to claim from "shed" saying it checks codes
    When the owner runs "roger edge adopt pocket" on "shed"
    Then the adopt output does not say "before root proofs"

  @security @cli
  Scenario: a phone heard without the flag is named as old at the adopt
    Given the phone "pocket" asked to claim from "shed" without saying it checks codes
    When the owner runs "roger edge adopt pocket" on "shed"
    Then the adopt output says "pocket runs a roger or app from before root proofs: it will not check the code"

  @security @cli
  Scenario: roger edge enroll tells the authority it checks codes
    When this machine enrolls with a code
    Then its request to "shed" carried checks_root true
    And "shed" remembers this machine as one that checks codes

  @security
  Scenario: checks_root is outside the signature and an older authority ignores it
    Given a joiner's request signed without checks_root
    Then setting checks_root on it leaves the signed bytes and the signature good
    And the same request with checks_root and a field no authority knows is answered, not refused

  # The owner's fleet window says it too, on a member's page in the rack: "checked this Edge's code
  # when it joined", or "did not say it checks codes: a roger or app from before root proofs. update
  # it". Not a scenario here, because the @tui and @console runners of this file drive the join
  # screens, not the rack; pinned by internal/fleetview TestAPhoneWithNoBatteryReadingHasNoProcessorColumns
  # and read from the authority's notes by cmd/rogerai edgeFleetSource.

  @security @ios
  Scenario: an updated phone app says it checks codes on every join request
    Given the phone runs an app that verifies root proofs
    When it enrolls by address, joins by an invite, or claims after an adopt
    Then the request carries "checks_root": true
    And against an authority from before the flag it is answered exactly as before

  @security
  Scenario: an old joiner can still enroll against a new authority
    Given a joiner running a roger from before root proofs
    When it enrolls against a new "shed"
    Then it is answered exactly as before, with the proof as one extra field it ignores
    And the answer's shape is otherwise byte-compatible with the old one

  @security
  Scenario: a new member renews against an old authority with no proof needed
    Given this machine runs the new roger, is enrolled, and holds "shed"'s root
    And "shed" runs a roger from before root proofs
    When this machine renews
    Then the answer's root equals the one it holds and it is accepted

  @security
  Scenario: members enrolled before this change keep working with no action
    Given members that enrolled before root proofs existed
    When the authority and the members update in any order
    Then every member still verifies its peers against the root it holds
    And no member is told to re-enroll because of this change

  @security @ios
  Scenario: an older phone app still claims from a new authority
    Given the phone runs an app from before root proofs
    When it is adopted and claims from a new "shed"
    Then "shed" issues to it as before
    And the adopt output carried the warning to update the app first

  @security
  Scenario Outline: every pairing of versions is either safe or refused, never silently unsafe on a new joiner
    Given a joiner running <joiner> and an authority running <authority>
    When the joiner enrolls for the first time with <proof>
    Then the result is <result>

    Examples:
      | joiner | authority | proof     | result                                        |
      | new    | new       | a code    | kept, proof verified                          |
      | new    | new       | the pin   | kept, pin matched                             |
      | new    | new       | nothing   | refused before sending, or held and asked for |
      | new    | old       | a code    | refused: the authority sends no proof         |
      | new    | old       | the pin   | kept, pin matched                             |
      | new    | old       | nothing   | refused before sending, or held and asked for |
      | old    | new       | nothing   | kept unchecked, as before (warned at allow)   |
      | old    | old       | nothing   | kept unchecked, as before                     |

  # =========================================================================
  # 13. A CORE AUTHORITY - TLS already settles it
  # =========================================================================

  # Evidence: the default broker is https://broker.rogerai.fm (cmd/rogerai/main.go defaultBroker);
  # enrollhttp dials it with Go's default client, which verifies the server certificate against the
  # system roots, and nothing on the enroll path sets InsecureSkipVerify. So under the Core authority
  # the root arrives over a channel already authenticated as Core, and a LAN device cannot substitute
  # one. Two caveats the spec closes: the broker address is owner-configurable and may be plain HTTP,
  # and no Core-side /edge/enroll handler exists in this tree yet, so the Core path is pinned here
  # before it goes live.

  @security
  Scenario: a Core-authority enrollment needs no code, because TLS authenticated Core
    Given this Edge uses the Core authority and the broker address is https
    When this machine enrolls
    Then the root is accepted on the strength of the verified TLS connection to Core
    And no code is minted, asked for or checked

  @security
  Scenario: a device on the LAN answering for Core cannot complete the TLS handshake
    Given this Edge uses the Core authority
    And a device on the network intercepts the connection to the broker
    When this machine enrolls
    Then the TLS certificate does not verify
    And enrollment fails naming connectivity, keeping nothing, as enrollment.feature requires

  # RULED 2026-10-06: D10 - refuse a plain-HTTP broker for Edge enrollment unless it is loopback.
  # A plain-HTTP broker would put the Core path back where the LAN path was; loopback stays
  # allowed for development and the test harness.
  @security
  Scenario Outline: the Core path refuses a broker address that is not authenticated
    Given the broker address is "<broker>"
    When this machine enrolls under the Core authority
    Then the result is <result>

    Examples:
      | broker                       | result                                                     |
      | https://broker.rogerai.fm    | enrolled over verified TLS                                 |
      | http://127.0.0.1:8080        | enrolled (loopback, for development)                       |
      | http://localhost:8080        | enrolled (loopback, for development)                       |
      | http://[::1]:8080            | enrolled (loopback, for development)                       |
      | http://192.168.1.5:8080      | refused before sending: Core enrollment needs https        |
      | http://broker.example:80     | refused before sending: Core enrollment needs https        |

  @security
  Scenario: a Core-authority refusal for plain HTTP names the local authority as the offline path
    Given the broker address is plain HTTP on the LAN
    When this machine enrolls under the Core authority
    Then it says Core enrollment needs an https broker
    And it says that a local authority with a code is the way to enroll on this network instead

  # =========================================================================
  # 14. THE SURFACES - CLI, [3] EDGE, the console, the phone
  # =========================================================================

  @tui
  Scenario: the join wizard asks for the code after the authority answers
    Given the wizard is on the join path, named "bench", authority "http://192.168.1.10:8791"
    And that authority allows this machine
    When the owner confirms
    Then the handshake sweeps, and stops at a step that asks for the code shown on the authority
    And it shows the root's short fingerprint the authority offered
    When the owner enters the code
    Then the sweep locks and the wizard reaches the done step

  @tui
  Scenario: a wrong code in the wizard says so in place and lets the owner retype
    Given the wizard is asking for the code
    When the owner enters a code that does not verify
    Then it says the code does not match the Edge that answered, with attempts left
    And it stays at the code step

  @tui
  Scenario: an answer the wizard cannot verify is never shown as a success
    Given the wizard is asking for the code
    When the owner leaves with esc
    Then nothing is kept and the machine is exactly as it was
    And the Edge screen is unchanged

  @tui
  Scenario: adopting a device on [3] shows its code
    Given a phone candidate "pocket" on the [3] EDGE screen of the authority
    When the owner adopts it
    Then the confirmation names "pocket" and shows its code and how long it is good for
    And the adopting panel for "pocket" keeps showing the code until it is spent or expires
    And once it expires the panel says so and offers a fresh code

  @tui
  Scenario: the code holds up at every width and with NO_COLOR
    Given an adopting panel showing a code
    When it renders at 120, 100, 80 and 60 columns and under NO_COLOR
    Then the code is shown whole, never wrapped or truncated, at every width
    And it carries no ANSI under NO_COLOR

  @tui
  Scenario: the done step of a new Edge says the next machine needs allow or adopt for its code
    Given a new Edge was just created as "shed"
    Then the done step says another machine joins with the code that allow or adopt prints here
    And it shows the root's full fingerprint for the pin path

  @console
  Scenario: the console's adopt returns the code only to the owner's token-gated request
    Given a console over the authority with a phone candidate "pocket"
    When the owner adopts "pocket" through the console's POST, with the token
    Then the response carries the code and how long it is good for
    And the console shows it in the adopting row
    And a read without the token never carries a code

  @console
  Scenario: the console's join takes a code, and a wrong one is said plainly
    Given a console on a machine that is not enrolled
    When the owner joins an authority through the console with a code that does not verify
    Then the console says the code does not match the Edge that answered
    And nothing was kept

  @cli
  Scenario: roger edge setup on the join path asks for the code after the authority answers
    Given an owner whose machine is allowed on the authority
    When they run "roger edge setup", choose to join, name it "bench" and give the authority address
    Then it asks for the code the authority showed
    And with the right code it prints that it joined, with the root's short fingerprint

  @cli
  Scenario: roger edge setup that is not yet allowed says the allow command also gives the code
    Given an owner whose machine is not yet allowed on the authority
    When they run "roger edge setup" and choose to join
    Then it prints this machine's user key and the allow command to run on the authority
    And it says that command prints the code to bring back here

  @ios
  Scenario: the phone's Edge tab asks for the code once an authority has answered
    Given the phone "pocket" holds an unverified answer to its claim
    Then the Edge tab says an Edge answered and asks for the code shown where "pocket" was adopted
    And it shows the short fingerprint of each root offered
    And it does not show "pocket" as a member until a code verifies

  @ios
  Scenario: the phone's typed join takes the code beside the address
    Given the owner is joining from the phone by typing the authority's address
    Then the join form has a field for the code as well
    And a join with a code that does not verify keeps nothing and says why

  @ios
  Scenario: the phone also checks the certificate against the root before keeping it
    Given an answer whose root proof verifies
    When the certificate in it does not chain to that root
    Then the phone refuses it, keeping nothing
    And the check uses the same P-256 chain rules the Go side applies

  @ios
  Scenario: the proof derivation is pinned by golden vectors both implementations run
    Given the shared golden-vector file of codes, nonces, roots, accounts, node ids and proofs
    Then the Go implementation reproduces every proof
    And the phone's implementation reproduces every proof
    And a vector with any one field changed fails in both

  # =========================================================================
  # 15. NEVER HALF-APPLIED
  # =========================================================================

  # corrected 2026-10-06 (founder-approved): the Then said "no root ... remain", which cannot hold for
  # a declined root change, where the old membership rightly stays. What every row means is this.
  @security
  Scenario Outline: every refusal in this file leaves the machine exactly as it was
    Given a first enrollment refused because <reason>
    Then nothing from the refused answer is kept: the machine's Edge files are exactly as before the attempt
    And retrying later behaves as a first attempt

    Examples:
      | reason                                  |
      | no code and no pin                      |
      | the proof did not verify                |
      | the answer carried no proof             |
      | the pin did not match                   |
      | the code was spent                      |
      | the code had expired                    |
      | the held answer outlived its window     |
      | the wrong-code cap was reached          |
      | the proof version is unknown            |
      | the owner declined a root change        |

  @security
  Scenario: the root is verified before anything else from the answer is used
    Given an answer from an unverified authority
    Then no address, name, account or revocation list from it is used or stored
    And the revocation list is fetched only after the root is kept
