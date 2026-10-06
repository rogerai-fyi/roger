# PROPOSED SPEC - 2026-09-30, awaiting founder approval. Part of the routing-expression set;
# the vocabulary is features/routing/ROUTING-EXPRESSION-CONTRACT.md §11 (plus §1b, §2).
#
# ACCOUNT KEYS: guardrailed bearer credentials a logged-in account mints for ITSELF.
#
# WHAT A "KEY" IS TODAY (origin/main 518c698b). There is no consumer API-key object. A
# consumer authenticates one of three ways:
#   - a signed request: Ed25519 device keypair, X-Roger-Pubkey / X-Roger-TS / X-Roger-Sig
#     (internal/protocol/auth.go:25-27, verified in identityOf, cmd/rogerai-broker/main.go:1219),
#     whose pubkey-derived id maps to the account wallet once bound by login (walletOf,
#     main.go:1281);
#   - a Playbox web-session cookie behind an allowlisted Origin (tunnel.go:1660-1672);
#   - a grant, `Bearer rog-grant_<secret>`, an OWNER-minted credential for SOMEONE ELSE that
#     routes only to the owner's nodes and is governed by grant caps (internal/store/grant.go,
#     cmd/rogerai-broker/grant.go:78-100; features/grants/grants.feature).
# A bare `Bearer <anything else>` is the legacy unsigned id and can never spend
# (main.go:1262-1267; features/relay/auth.feature).
#
# So today an account that wants "a key for my CI box with a $5/week ceiling that can only
# use two models" has nothing: the monthly cap (/account/limit, dashboards.go:~75-100;
# monthlycap.go) is one number for the whole account, and a grant is the wrong tool (it is
# scoped to the issuer's OWN nodes, not the network, and pays from the grant's own wallet).
# NOTE: internal/keypurpose is NOT this. It is the broker's own signing-key ring (purpose
# separation for receipts/attestation/etc.). Nothing here touches it.
#
# THE SHAPE (PROPOSED, mirrors the grant record where the fields mean the same thing):
#   - id `key_<rand>`; secret `rog-key_<secret>` shown ONCE at mint; only sha256(secret) is
#     stored (exactly the grant discipline, features/grants/grants.feature:17).
#   - a key authenticates on the relay as `Authorization: Bearer rog-key_...`, resolved FIRST
#     (before the signature path) like a grant is; it resolves to the MINTING ACCOUNT's wallet
#     as PAYER and routes the whole public network (unlike a grant).
#   - fields: name, limit_usd, reset (daily|weekly|monthly|none), expires_at, allowed_models[],
#     allowed_nodes[], disabled. Enforcement is in features/relay/key_limits.feature; this
#     file is the management surface, validation, windows, expiry, privacy, and audit.
#   - management needs the SIGNED ACCOUNT IDENTITY (dashIdentityBody, the same gate as
#     /account/limit) or the web session; a key can never mint, read, or modify keys.
#   - storage is the shared store (Postgres, Redis-cached like the grant secret-hash lookup),
#     so revocation and PATCHes bind on every broker instance.
#   - every mint / patch / delete writes a $0 audit row of kind `key_event` in the ledger's
#     audit stream (the same stream `adjust` / `void` use, internal/store/ledger.go:29-30)
#     [PROPOSED kind].
#
# Enforced by: cmd/rogerai-broker/key_guardrails_bdd_test.go (godog, strict; to be written
# at RED) driving the real handlers against the real store (testcontainers Postgres).

Feature: Account keys - guardrailed credentials an account mints for itself

  Background:
    Given a broker with the money store and the shared store wired
    And account "acct-a" is logged in (wallet "u_gh_a") with balance $20.00
    And account "acct-b" is logged in (wallet "u_gh_b") with balance $20.00
    And "acct-a" signs its management requests with its bound device key

  # --- minting -------------------------------------------------------------------
  Scenario: Minting a key shows the secret once and stores only its hash
    When "acct-a" POSTs /account/keys with {"name":"ci-box"}
    Then the response is 201 with "id" starting "key_" and "secret" starting "rog-key_"
    And the store holds sha256(secret) and never the secret
    And a later GET /account/keys shows "ci-box" with no "secret" field

  Scenario: A mint with no fields at all is a valid unlimited key
    When "acct-a" POSTs /account/keys with {}
    Then the response is 201
    And the key has limit_usd 0 (unlimited), reset "none", no expiry, empty allow-lists, disabled false

  Scenario: Every guardrail field can be set at mint
    When "acct-a" POSTs /account/keys with:
      """
      {"name":"weekly-five","limit_usd":5,"reset":"weekly","expires_at":"2027-01-01T00:00:00Z",
       "allowed_models":["qwen3-32b","llama-3.3-70b"],"allowed_nodes":["node-a"],"disabled":false}
      """
    Then the response is 201 and GET /account/keys echoes every field as sent

  Scenario: The secret's prefix is the only recognizer the list ever shows
    Given "acct-a" minted key "k1" whose secret ends in "7f3a"
    When "acct-a" GETs /account/keys
    Then "k1" shows "hint":"...7f3a" and no other part of the secret

  Scenario: The account owner is the payer of every key it mints
    Given "acct-a" minted key "k1"
    When a relay authenticates with "k1"
    Then the payer wallet is "u_gh_a"

  Scenario: A key cannot mint a key
    Given "acct-a" minted key "k1"
    When a request bearing "k1" POSTs /account/keys
    Then the response is 403 with code "key_cannot_manage_keys"
    And no key was created

  Scenario: A key cannot list, patch, or delete keys
    Given "acct-a" minted keys "k1" and "k2"
    When a request bearing "k1" GETs /account/keys
    Then the response is 403 with code "key_cannot_manage_keys"
    When a request bearing "k1" PATCHes /account/keys/k2 with {"disabled":true}
    Then the response is 403 with code "key_cannot_manage_keys" and "k2" is unchanged
    When a request bearing "k1" DELETEs /account/keys/k2
    Then the response is 403 with code "key_cannot_manage_keys" and "k2" still exists

  Scenario: An anonymous (unbound) keypair cannot mint
    Given a device keypair that never logged in
    When it POSTs /account/keys with {"name":"x"}
    Then the response is 401 asking to log in (the same gate as /account/limit)

  Scenario: An unsigned request cannot mint
    When an unsigned POST /account/keys arrives with an X-Roger-User header
    Then the response is 401 "invalid request signature"

  Scenario: A Playbox web session behind an allowlisted Origin can mint
    Given "acct-a" holds a valid web session cookie
    When the browser POSTs /account/keys with {"name":"browser"} from an allowlisted Origin
    Then the response is 201

  Scenario: A web session behind a foreign Origin cannot mint
    Given "acct-a" holds a valid web session cookie
    When a POST /account/keys arrives with that cookie from an unlisted Origin
    Then the response is 401 and no key was created

  Scenario: A grant bearer cannot mint
    Given owner "op-1" minted grant "g1"
    When a request bearing "g1" POSTs /account/keys
    Then the response is 403 with code "key_cannot_manage_keys"

  Scenario: A mint replayed with the same Idempotency-Key does not mint twice
    Given "acct-a" POSTs /account/keys with header "Idempotency-Key: abc" and {"name":"ci"}
    When the same request is replayed within 10 minutes
    Then the response is 409 with code "already_minted" naming the first key's id
    And no second key exists and no secret is returned (the secret is shown once, ever)

  Scenario: A mint replayed with a different Idempotency-Key is a new key
    Given "acct-a" minted "ci" with "Idempotency-Key: abc"
    When "acct-a" POSTs the same body with "Idempotency-Key: def"
    Then the response is 201 with a different id

  Scenario: A mint without an Idempotency-Key is never de-duplicated
    When "acct-a" POSTs {"name":"ci"} twice with no Idempotency-Key
    Then two keys named "ci" exist (names are labels, not identifiers)

  Scenario: An account may hold at most 32 live keys
    Given "acct-a" holds 32 non-deleted keys
    When "acct-a" POSTs /account/keys with {"name":"one-more"}
    Then the response is 400 with code "key_limit_count" and message "at most 32 keys per account - delete one first"

  Scenario: A deleted key does not count toward the 32
    Given "acct-a" holds 32 keys and deletes one
    When "acct-a" POSTs /account/keys with {"name":"replacement"}
    Then the response is 201

  # --- validation at mint and patch --------------------------------------------
  Scenario Outline: A malformed guardrail is a 400 naming the field
    When "acct-a" POSTs /account/keys with <body>
    Then the response is 400 with code "invalid_key_field" and the message names "<field>"
    And no key was created

    Examples:
      | body                                              | field          |
      | {"limit_usd":-1}                                  | limit_usd      |
      | {"limit_usd":"NaN"}                               | limit_usd      |
      | {"limit_usd":"Inf"}                               | limit_usd      |
      | {"limit_usd":"5"}                                 | limit_usd      |
      | {"reset":"hourly"}                                | reset          |
      | {"reset":"Weekly"}                                | reset          |
      | {"reset":""}                                      | reset          |
      | {"expires_at":"2020-01-01T00:00:00Z"}             | expires_at     |
      | {"expires_at":"2027-01-01"}                       | expires_at     |
      | {"expires_at":"2027-01-01T00:00:00+02:00"}        | expires_at     |
      | {"expires_at":1800000000}                         | expires_at     |
      | {"allowed_models":"qwen3-32b"}                    | allowed_models |
      | {"allowed_models":[""]}                           | allowed_models |
      | {"allowed_models":[" qwen3-32b"]}                 | allowed_models |
      | {"allowed_models":[1]}                            | allowed_models |
      | {"allowed_nodes":[""]}                            | allowed_nodes  |
      | {"allowed_nodes":"node-a"}                        | allowed_nodes  |
      | {"disabled":"yes"}                                | disabled       |
      | {"name":"<a string of 129 chars>"}                | name           |
      | {"name":"line\nbreak"}                            | name           |

  Scenario: A limit of exactly 0 means unlimited, not zero spend
    When "acct-a" POSTs /account/keys with {"limit_usd":0,"reset":"daily"}
    Then the response is 201 and the key is unlimited (limit_usd 0)

  Scenario: The smallest positive limit is accepted at cent precision and stored at 6 places
    When "acct-a" POSTs /account/keys with {"limit_usd":0.01}
    Then the response is 201 and limit_usd reads back 0.01

  Scenario: A limit is rounded to 6 decimal places like every other dollar amount
    When "acct-a" POSTs /account/keys with {"limit_usd":1.23456789}
    Then limit_usd reads back 1.234568

  Scenario: A reset without a limit is accepted and inert
    When "acct-a" POSTs /account/keys with {"reset":"daily"}
    Then the response is 201 and the key is unlimited with reset "daily" (nothing to reset)

  Scenario: Allow-lists accept up to 64 exact ids each
    When "acct-a" POSTs /account/keys with 64 allowed_models and 64 allowed_nodes
    Then the response is 201

  Scenario Outline: A 65th allow-list entry is refused
    When "acct-a" POSTs /account/keys with 65 entries in "<list>"
    Then the response is 400 with code "invalid_key_field" naming "<list>"

    Examples:
      | list           |
      | allowed_models |
      | allowed_nodes  |

  Scenario: Duplicate allow-list entries are de-duplicated, not refused
    When "acct-a" POSTs /account/keys with {"allowed_models":["qwen3-32b","qwen3-32b"]}
    Then the response is 201 and allowed_models reads back ["qwen3-32b"]

  Scenario: Allow-list ids are exact-match and case-sensitive
    Given "acct-a" minted key "k1" with allowed_models ["Qwen3-32B"]
    When a relay bearing "k1" asks for "qwen3-32b"
    Then the response is 403 with code "key_model_denied" (no case folding, no prefix match)

  Scenario: An allow-list entry that names nothing on air is accepted (it may come on air later)
    When "acct-a" POSTs /account/keys with {"allowed_nodes":["node-not-yet-registered"]}
    Then the response is 201

  Scenario: An empty allow-list means all
    Given "acct-a" minted key "k1" with allowed_models [] and allowed_nodes []
    When a relay bearing "k1" asks for any on-air model
    Then no key_model_denied or key_node_denied is possible for it

  Scenario: Unknown fields in a mint or patch are a 400
    When "acct-a" POSTs /account/keys with {"limit":5}
    Then the response is 400 with code "unknown_key_field" naming "limit"

  Scenario: expires_at must be UTC ISO-8601 with a Z suffix
    When "acct-a" POSTs /account/keys with {"expires_at":"2027-06-01T12:00:00Z"}
    Then the response is 201 and expires_at reads back "2027-06-01T12:00:00Z"

  Scenario: expires_at one second in the future is accepted
    Given the clock reads 2026-10-01T00:00:00Z
    When "acct-a" POSTs /account/keys with {"expires_at":"2026-10-01T00:00:01Z"}
    Then the response is 201

  Scenario: expires_at equal to now is refused as past
    Given the clock reads 2026-10-01T00:00:00Z
    When "acct-a" POSTs /account/keys with {"expires_at":"2026-10-01T00:00:00Z"}
    Then the response is 400 naming "expires_at"

  Scenario: A mint body over 4 KiB is refused before parsing
    When "acct-a" POSTs /account/keys with a 5 KiB body
    Then the response is 413

  # --- patching ------------------------------------------------------------------
  Scenario: A nil field in a PATCH leaves that field unchanged
    Given "acct-a" minted key "k1" with limit_usd 5, reset "weekly", allowed_models ["qwen3-32b"]
    When "acct-a" PATCHes /account/keys/k1 with {"name":"renamed"}
    Then "k1" has name "renamed", limit_usd 5, reset "weekly", allowed_models ["qwen3-32b"]

  Scenario Outline: Each field can be patched independently
    Given "acct-a" minted key "k1" with defaults
    When "acct-a" PATCHes /account/keys/k1 with <patch>
    Then the response is 200 and "k1" reads back <patch> with every other field at its default

    Examples:
      | patch                                    |
      | {"name":"x"}                             |
      | {"limit_usd":2.5}                        |
      | {"reset":"monthly"}                      |
      | {"expires_at":"2027-01-01T00:00:00Z"}    |
      | {"allowed_models":["qwen3-32b"]}         |
      | {"allowed_nodes":["node-a"]}             |
      | {"disabled":true}                        |

  Scenario: Clearing an allow-list is an explicit empty array, not a missing field
    Given "acct-a" minted key "k1" with allowed_models ["qwen3-32b"]
    When "acct-a" PATCHes /account/keys/k1 with {"allowed_models":[]}
    Then "k1" allows all models
    When "acct-a" PATCHes /account/keys/k1 with {"name":"n"}
    Then allowed_models is still [] (a missing field never resets anything)

  Scenario: Clearing an expiry is an explicit null
    Given "acct-a" minted key "k1" with expires_at "2027-01-01T00:00:00Z"
    When "acct-a" PATCHes /account/keys/k1 with {"expires_at":null}
    Then "k1" never expires

  Scenario: Lowering a limit below current window usage binds on the next request
    Given "acct-a" minted key "k1" with limit_usd 10 and it has spent $6.00 this window
    When "acct-a" PATCHes /account/keys/k1 with {"limit_usd":5}
    Then the response is 200
    And the next priced relay bearing "k1" is 402 with limit_source "key_limit"
    And the $6.00 already settled is not reversed

  Scenario: Lowering a limit does not cut a stream already in flight
    Given a stream bearing "k1" is mid-flight under a $10 limit
    When "acct-a" PATCHes "k1" to limit_usd 0.01
    Then the stream completes and settles under its original hold
    And the next request bearing "k1" is 402

  Scenario: Raising a limit needs the signed account identity, never the key itself
    Given "acct-a" minted key "k1" with limit_usd 5
    When a request bearing "k1" PATCHes /account/keys/k1 with {"limit_usd":500}
    Then the response is 403 with code "key_cannot_manage_keys" and the limit is still 5

  Scenario: A raised limit takes effect on the next request
    Given "acct-a" minted key "k1" with limit_usd 1 and it has spent $1.00 this window
    And a relay bearing "k1" is 402 with limit_source "key_limit"
    When "acct-a" PATCHes "k1" to limit_usd 3
    Then the next relay bearing "k1" is served

  Scenario: Changing the reset interval re-anchors the window at the change
    Given "acct-a" minted key "k1" with limit_usd 5, reset "monthly", $4.00 spent this month
    When "acct-a" PATCHes "k1" to reset "daily"
    Then the window usage shown is the spend since the PATCH ($0.00), and the $4.00 stays in lifetime usage

  Scenario: Changing reset to "none" makes the limit lifetime including past spend
    Given "acct-a" minted key "k1" with limit_usd 5, reset "daily", $4.00 lifetime spend across past days
    When "acct-a" PATCHes "k1" to reset "none"
    Then limit_remaining reads $1.00 (lifetime spend counts)

  Scenario: A PATCH on an unknown key id is a uniform 404
    When "acct-a" PATCHes /account/keys/key_doesnotexist with {"name":"x"}
    Then the response is 404 with the uniform "no such key" body

  Scenario: A PATCH on another account's key is the same uniform 404
    Given "acct-b" minted key "kb"
    When "acct-a" PATCHes /account/keys/kb with {"disabled":true}
    Then the response is 404 with the uniform "no such key" body, byte-identical to the unknown-id case
    And "kb" is unchanged

  Scenario: A PATCH with an empty body is a 400
    When "acct-a" PATCHes /account/keys/k1 with {}
    Then the response is 400 with code "empty_patch"

  Scenario: A PATCH cannot change the id, hash, owner, created_at, or usage
    When "acct-a" PATCHes /account/keys/k1 with {"id":"key_other"}
    Then the response is 400 with code "unknown_key_field" naming "id"
    When "acct-a" PATCHes /account/keys/k1 with {"usage":0}
    Then the response is 400 with code "unknown_key_field" naming "usage"

  Scenario: Disabling then re-enabling keeps the window usage
    Given "acct-a" minted key "k1" with limit_usd 5 and $2.00 spent this window
    When "acct-a" PATCHes "k1" to disabled true, then disabled false
    Then limit_remaining reads $3.00

  # --- listing -------------------------------------------------------------------
  Scenario: The list shows every live key of the caller with usage and remaining, never secrets
    Given "acct-a" minted keys "k1" (limit 5 weekly, $1.25 spent this week) and "k2" (unlimited)
    When "acct-a" GETs /account/keys
    Then each entry has id, name, hint, limit_usd, reset, limit_remaining, usage, usage_daily, usage_weekly, usage_monthly, last_used, expires_at, disabled, created_at, allowed_models, allowed_nodes
    And "k1" shows limit_remaining 3.75 and "k2" shows limit_remaining null (unlimited)
    And no entry has a "secret" or "secret_hash" field

  Scenario: usage_daily / usage_weekly / usage_monthly are UTC-window sums of settled spend
    Given key "k1" settled $1.00 yesterday (UTC), $2.00 today, $4.00 last week, $8.00 last month
    When "acct-a" GETs /account/keys on a day in the same week and month as "today"
    Then "k1" shows usage_daily 2.00, usage_weekly 3.00, usage_monthly 3.00, usage 15.00

  Scenario: A reversed spend row does not count toward key usage
    Given key "k1" settled $3.00 and one $1.00 row was later reversed by a chargeback
    When "acct-a" GETs /account/keys
    Then "k1" shows usage 2.00 (the same rule as month spend, features/money/caps.feature)

  Scenario: last_used is the time of the last authenticated request, served or not
    Given key "k1" last made a request that was refused 402 at T
    When "acct-a" GETs /account/keys
    Then "k1" shows last_used T

  Scenario: The list is scoped to the caller's account only
    Given "acct-a" minted "k1" and "acct-b" minted "kb"
    When "acct-a" GETs /account/keys
    Then only "k1" is listed

  Scenario: A single key can be read by id
    When "acct-a" GETs /account/keys/k1
    Then the response is the same entry the list shows

  Scenario: Reading another account's key by id is the uniform 404
    Given "acct-b" minted "kb"
    When "acct-a" GETs /account/keys/kb
    Then the response is 404, byte-identical to GET /account/keys/key_doesnotexist

  Scenario: Enumerating key ids yields nothing
    When "acct-a" GETs /account/keys/<id> for 1000 guessed ids
    Then every miss is the same 404 body, byte-identical whether the id exists under another account or not at all
    And both cases run the same lookup-then-owner-compare path (constant-work structure, as the band-code resolve; no timing claim is made)

  Scenario: Deleted keys are not listed
    Given "acct-a" minted "k1" and deleted it
    When "acct-a" GETs /account/keys
    Then "k1" is absent

  Scenario: The list is paginated newest-first with a hard page of 100
    Given "acct-a" minted 32 keys
    When "acct-a" GETs /account/keys
    Then all 32 come back newest-first in one page (the per-account cap is below the page size)

  # --- deleting ------------------------------------------------------------------
  Scenario: Deleting a key revokes it immediately
    Given "acct-a" minted "k1"
    When "acct-a" DELETEs /account/keys/k1
    Then the response is 204
    And the next request bearing "k1" is 401 with code "key_revoked"

  Scenario: Revocation binds on every instance within one request
    Given two broker instances share the store
    And "acct-a" DELETEs "k1" on instance A
    When a request bearing "k1" reaches instance B immediately after
    Then it is 401 with code "key_revoked" (the hash lookup is shared; any cache is invalidated on delete)

  Scenario: A revoked key's in-flight stream completes and settles
    Given a stream bearing "k1" is mid-flight
    When "acct-a" DELETEs "k1"
    Then the stream runs to completion, settles against "u_gh_a", writes its receipt
    And a new request bearing "k1" is 401 "key_revoked"

  Scenario: A revoked key's in-flight non-stream relay completes and settles
    Given a non-stream relay bearing "k1" has been dispatched
    When "acct-a" DELETEs "k1" before the response returns
    Then the relay settles normally and the response is 200

  Scenario: Deleting twice is idempotent
    When "acct-a" DELETEs "k1" twice
    Then the first is 204 and the second is 404 uniform (a deleted key is indistinguishable from a never-minted id)

  Scenario: Deleting another account's key is the uniform 404 and changes nothing
    Given "acct-b" minted "kb"
    When "acct-a" DELETEs /account/keys/kb
    Then the response is 404 and "kb" still authenticates

  Scenario: A deleted key's usage stays in the account's spend history
    Given "k1" settled $3.00 then was deleted
    When "acct-a" GETs /usage
    Then the $3.00 is still attributed to key id "k1"

  Scenario: Deleting a key does not touch the account balance or monthly cap
    Given "acct-a" has balance $20.00 and monthly cap $50.00
    When "acct-a" DELETEs "k1"
    Then balance is $20.00 and the monthly cap is $50.00

  # --- reset windows (all UTC) ---------------------------------------------------
  Scenario Outline: A window boundary in UTC resets the window usage
    Given "acct-a" minted key "k1" with limit_usd 5 and reset "<reset>"
    And "k1" spent $5.00 at <last_in_window>
    When a priced relay bearing "k1" arrives at <just_before>
    Then it is 402 with limit_source "key_limit"
    When a priced relay bearing "k1" arrives at <just_after>
    Then it is served (window usage is $0.00)

    Examples:
      | reset   | last_in_window       | just_before          | just_after           |
      | daily   | 2026-10-01T12:00:00Z | 2026-10-01T23:59:59Z | 2026-10-02T00:00:00Z |
      | weekly  | 2026-10-01T12:00:00Z | 2026-10-04T23:59:59Z | 2026-10-05T00:00:00Z |
      | monthly | 2026-10-01T12:00:00Z | 2026-10-31T23:59:59Z | 2026-11-01T00:00:00Z |

  Scenario: The week starts on Monday 00:00:00 UTC
    Given key "k1" with reset "weekly" spent $5.00 on Sunday 2026-10-04T23:00:00Z
    When a priced relay arrives Monday 2026-10-05T00:00:00Z
    Then the window usage is $0.00

  Scenario: A month boundary from a 31-day month into a 30-day month
    Given key "k1" with reset "monthly" spent $5.00 on 2026-10-31T23:59:59Z
    When a priced relay arrives 2026-11-01T00:00:00Z
    Then the window usage is $0.00
    And on 2026-11-30T23:59:59Z the window still holds November's spend
    And on 2026-12-01T00:00:00Z it is $0.00 again

  Scenario: February and leap years follow the calendar
    Given key "k1" with reset "monthly"
    When the clock crosses 2028-02-29T23:59:59Z to 2028-03-01T00:00:00Z
    Then the window resets exactly once, at the crossing

  Scenario: Daylight-saving changes do not move a window (UTC has none)
    Given key "k1" with reset "daily" and the broker host in a DST zone
    When the local clock skips or repeats an hour
    Then the window boundary is still 00:00:00Z

  Scenario: reset "none" never resets
    Given key "k1" with limit_usd 5 and reset "none" spent $5.00 in 2026
    When a priced relay arrives in 2027
    Then it is 402 with limit_source "key_limit"

  Scenario: The window is computed from settled spend, not from a counter that could drift
    Given two instances each settled $2.00 for key "k1" this window
    When either instance computes the window usage
    Then it reads $4.00 (a sum over the shared ledger, like monthSpend)

  # --- expiry and disabled --------------------------------------------------------
  Scenario: A key exactly at its expiry is expired
    Given key "k1" with expires_at 2026-12-01T00:00:00Z
    When a request bearing "k1" arrives at 2026-12-01T00:00:00Z
    Then it is 401 with code "key_expired"
    When a request bearing "k1" arrives at 2026-11-30T23:59:59Z
    Then it authenticates

  Scenario: An expired key can be given a new future expiry by the account
    Given key "k1" expired yesterday
    When "acct-a" PATCHes "k1" with {"expires_at":"2027-01-01T00:00:00Z"}
    Then "k1" authenticates again

  Scenario: An expired key is still listed, marked expired
    Given key "k1" expired yesterday
    When "acct-a" GETs /account/keys
    Then "k1" is listed with expires_at in the past (the account decides whether to delete it)

  Scenario: A disabled key is refused with its own code
    Given key "k1" with disabled true
    When a request bearing "k1" arrives
    Then it is 401 with code "key_disabled"

  Scenario: A disabled key's in-flight stream completes
    Given a stream bearing "k1" is mid-flight
    When "acct-a" PATCHes "k1" to disabled true
    Then the stream completes and settles; the next request is 401 "key_disabled"

  Scenario Outline: Refusal precedence when several conditions hold
    Given key "k1" is <revoked>, <expired>, <disabled>
    When a request bearing "k1" arrives
    Then it is 401 with code "<code>"

    Examples:
      | revoked | expired | disabled | code         |
      | revoked | expired | disabled | key_revoked  |
      | live    | expired | disabled | key_expired  |
      | live    | live    | disabled | key_disabled |

  Scenario: A bearer that looks like a key but matches no hash is a 401 key_invalid
    # An unknown secret gets its own code; revoked / expired / disabled keep theirs (the holder
    # may learn why). The secret is 32 random bytes, so distinct codes are no enumeration
    # oracle (CONTRACT §11). What IS uniform is the work: the same sha256-then-lookup path runs
    # for a hit and a miss.
    When a request bearing "rog-key_notarealsecret" arrives
    Then it is 401 with code "key_invalid" and message "key invalid"
    And the sha256-then-lookup path is the same one a live key takes

  Scenario: A key bearer with a device signature alongside is authenticated by the key alone
    Given a request bearing "k1" that also carries a valid X-Roger-Pubkey/TS/Sig of "acct-b"
    When the broker resolves identity
    Then the request is "k1" of "acct-a"; the signature is ignored (one credential per request, the bearer wins as it does for grants)

  Scenario: A key never grants self-use pricing
    Given "acct-a" owns node "node-a"
    When a relay bearing "k1" is served by "node-a"
    Then it is billed at the offer price (self-use $0 requires the signed device path, not a key)

  # --- multi-instance -------------------------------------------------------------
  Scenario: A PATCH on instance A binds on instance B within one request
    Given two instances share the store
    When "acct-a" PATCHes "k1" to allowed_models ["qwen3-32b"] on A
    Then the next relay bearing "k1" on B for "llama-3.3-70b" is 403 "key_model_denied"

  Scenario: A mint on instance A authenticates on instance B immediately
    When "acct-a" mints "k1" on A
    Then a relay bearing "k1" on B authenticates

  # state audit 2026-10-05: the per-account rules hold across instances, not per instance.
  Scenario: Two instances minting at once cannot take an account past 32 live keys
    Given "acct-a" holds 31 non-deleted keys
    When "acct-a" mints on A and on B at the same moment
    Then exactly one mint is 201 and the other is 400 "key_limit_count"
    And "acct-a" holds exactly 32 live keys

  # state audit 2026-10-05
  Scenario: A mint replayed on another instance at the same moment does not mint twice
    When "acct-a" mints with "Idempotency-Key: abc" on A and on B at the same moment
    Then exactly one mint is 201 and the other is 409 "already_minted" naming the first key's id
    And "acct-a" holds exactly 1 live key

  # state audit 2026-10-05
  Scenario: The management limiter is one limit across instances
    When "acct-a" POSTs /account/keys 14 times in one minute, alternating between A and B
    Then the requests past the management limiter are 429 with Retry-After and no key is created for them
    And at most 10 keys were created across both instances

  # state audit 2026-10-05
  Scenario: A key revoked elsewhere stops working even while the shared store is down
    Given "acct-a" mints "k1" on A
    And a relay bearing "k1" on B authenticates
    And "acct-a" DELETEs "k1" on instance A
    When the shared store goes down
    Then the next relay bearing "k1" on B is 401 "key_revoked" (never served from a stale cache)

  Scenario: The shared store being unreachable fails closed for key auth
    Given the shared store is down and the key is not in the local cache
    When a request bearing "k1" arrives
    Then it is 503 "key lookup failed - try again shortly" (never a silent allow, never a 401 that reads as revoked)

  # slice-5 review 2026-10-05: an edit never writes back a revoked or disabled flag it read earlier.
  Scenario: A rename racing a delete on another instance never brings the key back
    Given "acct-a" mints "k1" on A
    And a relay bearing "k1" on B authenticates
    When "acct-a" renames "k1" on A while "acct-a" DELETEs it on B between A's read and write
    Then the rename is 404 "not_found"
    And the next relay bearing "k1" on B is 401 "key_revoked" (never served from a stale cache)

  # slice-5 review 2026-10-05
  Scenario: A rename racing a disable on another instance keeps the key disabled
    Given "acct-a" mints "k1" on A
    When "acct-a" renames "k1" to "renamed" on A while "acct-a" disables it on B between A's read and write
    Then "k1" is disabled and named "renamed"

  # slice-5 review 2026-10-05: a reversal nets out of the window the money was spent in.
  Scenario: A chargeback in a later window frees no budget in the window it lands in
    Given "acct-a" minted "k1" with a $5.00 daily limit and spent all of it yesterday (UTC)
    When that request is charged back $5.00 today
    Then "k1" shows usage_daily 0.00, limit_remaining 5.00 and usage 0.00

  # slice-5 review 2026-10-05: a request is reversed on its key at most once, in total.
  Scenario: A request refunded and then charged back is reversed on the key at most once
    Given key "k1" settled $3.00 in one request
    When that request is refunded $3.00 and then charged back $3.00
    Then "k1" shows usage 0.00 (never below zero)

  # slice-5 review 2026-10-05: an invalidation that failed during an outage is not lost.
  Scenario: A key deleted during a shared-store outage stops working on peers once the store answers again
    Given "acct-a" mints "k1" on A
    And a relay bearing "k1" on B authenticates
    And "acct-a" reads "k1" on A
    And the shared store refuses every command
    And "acct-a" DELETEs "k1" on instance A
    When the shared store answers again
    Then within 2 seconds a relay bearing "k1" on B is 401 "key_revoked"

  # --- audit ------------------------------------------------------------------------
  Scenario Outline: Every management action writes a $0 audit row
    When "acct-a" performs <action>
    Then a ledger audit row of kind "key_event" exists with account "u_gh_a", key id, action "<name>", and the changed field names
    And the row carries $0 money and no secret, hash, or field values

    Examples:
      | action                                    | name    |
      | POST /account/keys                        | mint    |
      | PATCH /account/keys/k1 {"limit_usd":2}    | patch   |
      | DELETE /account/keys/k1                   | delete  |

  Scenario: An audit row is written even when the action was refused for a validation reason
    When "acct-a" POSTs /account/keys with {"limit_usd":-1}
    Then no key exists and no audit row is written (nothing changed)
    When a request bearing "k1" PATCHes /account/keys/k1
    Then an audit row "denied" is written with the key id (a key trying to manage keys is worth seeing)

  # corrected 2026-10-02 (founder-approved): the account export is a POST behind a web session
  Scenario: Audit rows are visible in the account export
    Given "acct-a" minted and deleted keys
    When "acct-a" POSTs /account/export
    Then the export lists the key ids, names, and key_event rows, and no secrets or hashes

  # corrected 2026-10-02 (founder-approved): approved account deletion refuses a positive balance, so the account starts at $0
  Scenario: Account deletion revokes every key and anonymizes their audit rows
    Given "acct-a" has balance $0.00 and monthly cap $0.00
    When "acct-a" POSTs /account/delete
    Then every key of "acct-a" is revoked immediately and its key_event rows are anonymized like the rest of the account

  # --- privacy and logging ----------------------------------------------------------
  Scenario: Logs name key ids, never secrets or hashes
    When "acct-a" mints "k1" and a relay bearing "k1" is served
    Then every log line about it names "key_..." and contains neither "rog-key_" nor the sha256

  Scenario: An error body never echoes the bearer
    When a request bearing "rog-key_typo" is refused
    Then the 401 body does not contain "rog-key_typo"

  Scenario: The key id appears in the relay lineage and /generation
    When a relay bearing "k1" is served
    Then the console lineage row and GET /generation?id= carry "key_id":"k1"

  Scenario: A key id is not a wallet id
    Then "key_<rand>" is outside the reserved id namespaces (u_, u_gh_, g_) and can never be presented as an X-Roger-User

  # --- clients ------------------------------------------------------------------------
  # corrected 2026-10-06 (founder-approved): account keys live on /keys.html beside grant keys
  Scenario: The web keys page lists, mints, edits, and deletes keys through the same endpoints
    Given "acct-a" is on /keys.html with a web session
    Then the "Account keys" section shows the same entries as GET /account/keys
    And minting shows the secret once with a copy control and a warning that it is never shown again
    And the page sends body JSON, never the secret in a URL

  # corrected 2026-10-06 (founder-approved): account keys live on /keys.html beside grant keys
  Scenario: The keys page follows the design system
    Then the account keys section uses the existing keys-page type scale, one red, no grid, no glow, no pinned bar, and passes phone width and dark-mode contrast

  Scenario Outline: `roger keys` covers the same surface (PROPOSED command set)
    When "acct-a" runs `<command>`
    Then it performs <effect> via the same endpoint and prints <output>

    Examples:
      | command                                                        | effect                   | output                                     |
      | roger keys list                                                | GET /account/keys        | a mono table: id, name, hint, limit, used, resets, expires, state |
      | roger keys mint --name ci --limit 5 --reset weekly              | POST /account/keys       | the secret ONCE, then "store it now - it is not shown again" |
      | roger keys set key_x --limit 10 --models qwen3-32b,llama-3.3-70b | PATCH /account/keys/key_x | the updated row                            |
      | roger keys set key_x --disable                                  | PATCH disabled true      | the updated row                            |
      | roger keys rm key_x                                             | DELETE /account/keys/key_x | "revoked key_x"                           |

  Scenario: `roger keys mint` refuses to print the secret into a pipe silently
    When `roger keys mint` runs with stdout not a TTY
    Then it prints only the secret (machine-readable) and the warning goes to stderr

  Scenario: `roger keys` needs a logged-in device key
    Given the device keypair never logged in
    When `roger keys list` runs
    Then it exits non-zero with "log in first - run `roger login`"

  Scenario: A key can be used as the bearer by `roger use --key`
    When `roger use qwen3-32b --key rog-key_...` runs
    Then the local proxy authenticates every relay with the key bearer instead of the device signature
    And the key is never written to the config file unless `--save-key` is passed

  # --- adversarial ---------------------------------------------------------------------
  Scenario: An allow-listed private-band node still needs the band code
    Given key "k1" with allowed_nodes ["node-private"] where "node-private" is band-only
    When a relay bearing "k1" arrives with no freq
    Then it is 503 no_match (an allow-list is scope, not admission; the band stays hidden)
    When the same relay carries the band's code
    Then it is served on "node-private"

  Scenario: An allow-listed model a grant denies is still denied on the grant path
    Given owner "op-1" minted grant "g1" that denies "qwen3-32b"
    Then a request bearing "g1" for "qwen3-32b" is 403 "this grant does not allow model qwen3-32b" (a key's allow-list is irrelevant to a grant request)

  Scenario: A key cannot widen its own allow-lists through routing keys
    Given key "k1" with allowed_nodes ["node-a"]
    When a relay bearing "k1" sends provider.only ["node-a","node-b"]
    Then the effective allow set is {node-a} (intersection), never {node-a,node-b}

  Scenario: A key cannot name another account's wallet as payer
    When a relay bearing "k1" also sends X-Roger-User "u_gh_b"
    Then the payer is "u_gh_a" and the header is ignored (features/relay/spend.feature)

  Scenario: Minting is rate-limited per account
    When "acct-a" POSTs /account/keys 20 times in one minute
    Then the requests past the management limiter are 429 with Retry-After and no key is created for them

  Scenario: A stolen secret is useless once deleted, and the audit trail shows the deletion
    Given "k1" leaked and was used from a new IP
    When "acct-a" DELETEs "k1"
    Then the next use is 401 "key_revoked" and the audit stream shows mint, uses (in lineage), and delete

  Scenario: A key cannot be used to mint a grant either
    When a request bearing "k1" POSTs /grants
    Then it is 401 (grants need the signed owner identity; a key is a consumer credential)

  Scenario: A hash miss and a hash hit on a revoked key take the same code path (constant-work structure)
    # No timing measurement is asserted (CONTRACT §11): the guarantee is structural, like the
    # band-code resolve - always hash, always look up, decide afterwards.
    When a request bearing an unknown secret and one bearing a revoked secret arrive
    Then both run sha256 over the bearer and one shared-store lookup before any decision
    And neither returns before the lookup completes
