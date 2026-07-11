Feature: Content-blind broker rendezvous (Stage 3, broker half)
  The broker is a content-blind one-time-code rendezvous for the encrypted stranger capsule.
  It stores ONLY {lookup, ciphertext}: never the code, never the key, never the plaintext.
  A mint is owner-signed (attribution / rate-limiting); a resolve is authed by possession of
  the lookup and returns the blob exactly ONCE. Every miss/expired/garbage resolve is a
  byte-identical 404 (no existence oracle). The store is SHARED so a mint on one instance
  resolves on another, and a blob is consumed atomically exactly once across instances.

  Scenario: Mint then resolve round-trips the opaque blob once
    Given a signed capsule mint of an opaque blob under a lookup
    When I resolve that lookup
    Then the resolve returns the exact blob
    And a second resolve of the same lookup is a uniform 404

  Scenario: The broker cannot decrypt what it stores
    Given a stranger capsule sealed under a code and minted under its lookup
    Then the broker holds only the lookup and the ciphertext
    And the broker has neither the code nor the key
    And the broker cannot recover the plaintext from what it stores

  Scenario: An unknown lookup and an empty lookup are the identical 404
    When I resolve an unknown lookup
    And I resolve an empty lookup
    Then both are a 404 with byte-identical bodies

  Scenario: An expired blob resolves to a 404
    Given a blob minted with an already-expired TTL
    When I resolve its lookup
    Then the resolve is a uniform 404

  Scenario: An oversize blob is refused at mint
    Given a signed capsule mint of a blob larger than the cap
    Then the mint is refused as too-large
    And nothing is stored under its lookup

  Scenario: An unsigned mint is rejected and stores nothing
    Given an UNSIGNED capsule mint under a lookup
    Then the mint is rejected as unauthorized
    And resolving that lookup is a uniform 404

  Scenario: Multi-instance - mint on A resolves on B
    Given two broker instances sharing one store
    When I mint a blob on instance A under a lookup
    And I resolve that lookup on instance B
    Then the resolve on B returns the exact blob

  Scenario: Multi-instance single-use - two concurrent resolves, exactly one wins
    Given two broker instances sharing one store
    And a blob minted under a lookup
    When both instances resolve the same lookup concurrently
    Then exactly one resolve returns the blob and the other is a 404

  Scenario: The code and plaintext are never logged or persisted
    Given a stranger capsule sealed under a code and minted under its lookup
    Then no stored value and no log line contains the code or the plaintext
