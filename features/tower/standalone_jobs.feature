# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# Scope: standalone/private client lifecycle, Station routing, local policy, request replay,
# grant/result integrity, free accounting, receipt verification, retry, and failure. It has no
# RogerAI public membership, credits, holds, settlement, or compensation.

Feature: A standalone Tower is a complete local broker without becoming RogerAI public authority
  Its local administrator governs one private trust root and durable free-routing ledger.
  Public RogerAI identities and money surfaces are structurally absent.

  Background:
    Given a durable standalone Tower with a unique local network ID and Tower ID, pinned offline local root, purpose-separated local trust-document, trust-publication, policy, client-admission, client-certificate, bootstrap-verifier-authority, local-operator-set, Station-admission, Station-certificate, Station-bridge-authority, Station-bridge-certificate, grant, receipt, key-escrow-authorization, and key-escrow-result signers, and PostgreSQL

  # --- pinned local trust and purpose directory ----------------------------

  Scenario: LocalTrustDocumentV1 has one closed root-scoped key directory
    Given LocalTrustDocumentV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | local network ID |
      | protocol version |
      | fixed standalone-local trust kind |
      | pinned local root algorithm, key ID, canonical public-key bytes, and fingerprint |
      | local trust-document signer key ID and closed root_genesis, delegated, or root_recovery signer kind |
      | deterministic stable trust-document series ID |
      | positive trust-document revision |
      | previous LocalTrustDocumentV1 complete hash or canonical first-revision absence |
      | consumed LocalAdminAuthorizationV1 ID and complete hash or canonical root-genesis/root-recovery absence |
      | LocalPurposeKeyRecoverySetV1 complete hash or canonical non-root-recovery absence |
      | canonical positive LocalPurposeKeyEntryV1 member count |
      | complete ordered LocalPurposeKeyEntryV1 member array |
      | local issue-time claim |
      | effective local authority time and global sequence |
      | finite expiry local authority time and global sequence |
    And every LocalPurposeKeyEntryV1 member contains only unique key ID, closed signature algorithm, canonical public-key bytes and hash, exactly one closed local purpose, finite not-before/not-after local authority tuples, active or retired or revoked state, state-effective local authority tuple, and compromise-effective local authority tuple or canonical non-compromise absence
    And the only directory purposes in v1 are local_trust_document, local_trust_publication, local_policy, local_client_admission, local_client_certificate, local_bootstrap_verifier_authority, local_operator_set, local_station_admission, local_station_certificate, local_station_bridge_authority, local_station_bridge_certificate, local_grant, local_receipt_ledger, local_service_tls, local_administrator_audit, local_key_escrow_authorization, and local_key_escrow_result
    And those purposes have fixed enum ordinals zero through sixteen in that listed order; directory members sort by purpose ordinal then unsigned bytewise key-ID order, contain no position-dependent ordinal field, and declared member count equals the complete nonempty array length
    When each document, entry, or nested field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, reordered, or retyped while retaining the signature
    Then strict UTF-8 JCS decoding, complete-object hashing, deterministic-series derivation, purpose-signature verification, or exact directory verification fails
    And unknown fields, unknown purposes/states/algorithms, duplicate key IDs, duplicate active keys for one purpose, reused public-key bytes across purposes, invalid key encodings, nonfinite validity, and any RogerAI/public/money/discovery role or endpoint are rejected rather than ignored

  Scenario: Local trust has one pinned genesis and one independently committed publication chain
    Given the local root fingerprint was displayed and pinned through the trusted initialization channel
    When LocalTrustDocumentV1 revision D is accepted through LocalTrustPublicationV1 revision P
    Then stable trust-document series ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over UTF-8 strict JCS [LocalTrustDocumentV1-series-v1,local-network-ID,pinned-local-root-fingerprint]
    And LocalTrustPublicationV1 contains only schema/network/protocol, standalone-local kind, pinned-root fingerprint, closed root_genesis or delegated or root_recovery publication kind, publication signer key ID, deterministic publication-series ID, positive publication revision, prior LocalTrustPublicationV1 complete hash or canonical first absence, exact LocalTrustDocumentV1 series/revision/complete hash and signer kind/key ID, consumed LocalAdminAuthorizationV1 ID/complete hash or canonical genesis/recovery absence and LocalPurposeKeyRecoverySetV1 complete hash or canonical non-recovery absence copied from that document, issue-time claim, effective/expiry tuples copied from that document, independently assigned local trust-ledger commit time/global sequence, and closed lost_key, compromised_key, expired_key, or missed_renewal recovery reason or canonical non-recovery absence
    And publication-series ID derives from strict JCS [LocalTrustPublicationV1-series-v1,local-network-ID,pinned-local-root-fingerprint], revision 1 has both prior absences and root_genesis signatures by the pinned root, and its document installs the first delegated trust-document and trust-publication keys
    And every ordinary successor is exactly P plus one with immediate prior publication hash and document revision D plus one with immediate prior document hash, consumes one trust_key_change LocalAdminAuthorizationV1 that binds both prior heads and the exact acyclic target-semantic digest, both signers are active for their respective purposes under the prior accepted document at the independently assigned commit tuple, and one serializable current-head CAS advances both chains atomically
    And LocalPurposeKeyRecoverySetV1 is a strict unsigned child containing only schema/network/protocol, publication series/exact next revision, prior document/publication hashes, recovery reason, canonical positive member count, and members sorted by purpose then old key ID, each containing exact old purpose/key ID/public-key hash/state/not-after, replacement key ID/algorithm/canonical public-key bytes/hash/not-before/not-after, and replacement state active
    And root_recovery is signed directly by the pinned local root, names the exact prior publication/document heads and recovery set, advances both chains by exactly one without resetting network or series IDs, makes every named old entry terminal revoked at the commit and every distinct replacement active there, and leaves every unlisted directory entry byte-identical
    And lost_key or compromised_key requires an exact affected key and fail-closed operator ceremony, while expired_key requires old not-after no later than commit and missed_renewal requires no still-active unexpired key for that exact purpose; recovery may replace any exact online purpose needed to restore administration but grants no object authority by itself
    And root_recovery remains valid under the pinned offline root even when the prior document or online trust/policy signer has expired: it must first replace the exact expired local_trust_document, local_trust_publication, or local_policy entry, then a separate break-glass policy recovery may renew byte-identical policy rules; fresh work remains failed closed throughout and no expired cached signer or policy is reused
    And exact replay is idempotent, effective is no earlier than the publication commit, expiry is later than effective and no later than every signing key's not-after, and conflicting bytes, a gap, overflow, stale prior, second genesis, unpinned root, or signer-controlled commit tuple is rejected

  Scenario: Local key retirement, revocation, and compromise are time-qualified
    Given an accepted LocalTrustPublicationV1 changes one LocalPurposeKeyEntryV1 state
    When an object signature under that key is checked
    Then a newly active key may sign only at or after its state-effective tuple and before both key not-after and trust-document expiry
    And retired forbids new signatures at or after state-effective while preserving signatures anchored before it, revoked forbids new authority at or after state-effective, and revoked for compromise additionally requires compromise-effective equal to the independently assigned publication commit tuple
    And non-compromise active or retired entries require canonical compromise absence, while compromise cannot be backdated by a signer-controlled claim or used to validate an object lacking its independent local authority anchor
    And trust-management compromise makes fresh authority fail closed until the pinned-root recovery publication commits; no cached key or restored database can bypass that publication head

  Scenario: Every local purpose-key directory transition is monotonic and complete
    Given one accepted LocalTrustDocumentV1 directory is replaced by its exact successor
    When the successor's complete LocalPurposeKeyEntryV1 array is compared at the publication commit tuple
    Then every prior key entry remains present, ordered, and byte-identical unless that publication changes its state, and every new key ID/public key is unique and starts active with not-before/state-effective equal to that commit
    And an active key may become retired or revoked, a retired key may only remain retired or become revoked, a revoked key is terminal, and every changed state-effective tuple equals that publication commit
    And a replacement for one purpose is a new entry rather than reactivation; exactly one current active key exists for each required purpose after commit and every not-before is earlier than not-after and bounded by document expiry
    And deletion, purpose/key-byte change in place, state rollback, reactivation, duplicate active entry, unbounded validity, or hidden history fails the publication CAS and fresh authority remains fail closed

  Scenario: The pinned local root is not an online certificate or service authority
    Given standalone initialization completed the root-genesis trust publication
    When ordinary client admission, Station attachment, TLS serving, policy changes, grants, receipts, and trust publication run
    Then the pinned local root private key is absent from the serving process, environment, ordinary secret mounts, APIs, and automatic rotation paths
    And client certificates, Station secure-session certificates, and service TLS certificates are signed only by the distinct online local_client_certificate, local_station_certificate, and local_service_tls keys currently delegated by LocalTrustDocumentV1
    And no online issuer can sign LocalTrustDocumentV1, LocalTrustPublicationV1, an admission authority, policy, grant, or receipt, while the pinned root can act only in the explicit offline root_genesis, root_recovery, LocalBreakGlassRecoveryAuthorizationV1, or LocalOfflineRootEscrowApprovalV1 ceremony
    And clients pin the offline root fingerprint plus monotonic publication history and verify the online issuer's exact purpose/state there; an ambiguous online-CA fingerprint or a certificate chain without that history grants no authority

  Scenario: Local trust verifiers reject rollback but retain historical verification
    Given a local verifier pinned the root fingerprint and greatest accepted LocalTrustPublicationV1 sequence and complete hash
    When startup, restore, reconnect, or receipt verification presents trust history
    Then every publication from genesis through the requested historical authority anchor must form one signature-valid immediate-prior chain under that pinned root
    And fresh admission, policy selection, Station routing, and grant issuance require the database's unique current publication/document heads and reject a lower sequence even when its signatures and expiry still verify
    And the verifier durably retains its greatest accepted head, accepts a higher contiguous head, and reports an unverifiable gap or fork without replacing that head
    And historical credentials, grants, assertions, and receipts resolve their exact retained document/publication versions and time-qualified key states without treating later ordinary retirement as retroactive revocation
    And deleting old trust documents, resetting a sequence, changing the root/network, or substituting a RogerAI trust chain makes readiness or verification fail closed

  # --- exact local policy ---------------------------------------------------

  Scenario: LocalPolicyV1 is a closed deterministic local-only rule object
    Given LocalPolicyV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | local network ID |
      | protocol version |
      | fixed standalone-local policy kind |
      | local-policy signer key ID |
      | deterministic stable policy-series ID |
      | positive policy revision |
      | previous LocalPolicyV1 complete hash or canonical first-revision absence |
      | closed initialization, administrator_update, or break_glass_recovery source kind |
      | consumed LocalAdminAuthorizationV1 or LocalBreakGlassRecoveryAuthorizationV1 ID/complete hash, or canonical initialization absence prescribed by source kind |
      | exact authorizing LocalTrustPublicationV1 revision and complete hash |
      | fixed local-policy-evaluator-v1 language version |
      | closed required or disabled moderation mode |
      | canonical moderation-rule member count and ordered array |
      | canonical route-and-bound member count and ordered array |
      | closed deny_all or explicit_allowlist tool mode and canonical tool-rule member count and ordered array |
      | fixed no_request_controlled_fetch network mode and LocalPrivateDependencyAllowlistV1 complete hash |
      | canonical fixed result-disposition member count and complete ordered array |
      | bounded retry count, attempt duration, request bytes, result bytes, input tokens, output tokens, and streams |
      | issue-time claim |
      | effective local authority time and global sequence |
      | finite expiry local authority time and global sequence |
      | independently assigned local policy-ledger commit time and global sequence |
    And each moderation rule contains only unique rule ID, request or output stage, deterministic local evaluator ID/version/module hash/configuration hash, maximum evaluated bytes/runtime, and exact pass/block/error dispositions; stage ordinals are request 0 and output 1 and rules sort by stage ordinal then unsigned UTF-8 rule-ID bytes, required has fixed count 2 with exactly one request-stage and one output-stage rule, while disabled requires count zero, an empty array, and visible no_moderation disposition
    And each route-and-bound entry contains only a unique fixed-inference-role/model/offer/modality tuple, maximum request/result bytes, input/output tokens, streams, attempt duration, retry count, and allow or reject disposition; modality ordinals are chat 0, chat_streaming 1, speech_to_text 2, and text_to_speech 3, entries sort by unsigned UTF-8 model bytes, offer bytes, then modality ordinal, tuples cannot overlap, and unmatched requests reject
    And each explicit tool rule contains only unique tool ID, canonical input-schema hash, maximum calls and bytes, local deterministic handler hash, and an explicit allowed-inference-model member count plus typed unique model array; tool rules and each model array sort by unsigned UTF-8 identifier bytes, unmatched tools/models reject, deny_all requires count zero and an empty array, and no tool may name a status/local_operator role, URL, host, socket, or public-network capability
    And the result-disposition array has fixed count 7 and contains complete, request_blocked, output_blocked, evaluator_error, provider_error, cancelled, and deadline exactly once in enum ordinals zero through six in that order, each with deliver, withhold, or fail disposition plus the local-free accounting disposition
    And every declared count equals its array length; duplicate keys/tuples, omitted required result, unknown enum, alternate order, or differing representation is rejected before the policy hash is accepted
    When any common or nested field is mutated while retaining the signature
    Then strict UTF-8 JCS decoding, exact enum/array coverage, nonoverlap, numeric-bound, relationship, complete-hash, or purpose-signature verification fails before evaluation or routing
    And floating values, implicit defaults, regex or executable text, remote evaluator endpoints, request-controlled destinations, public policy claims, unknown fields, and a disabled policy with moderation rules are rejected

  Scenario: LocalPrivateDependencyAllowlistV1 has a closed private destination language
    Given LocalPolicyV1 names one LocalPrivateDependencyAllowlistV1 complete hash
    Then that strict unsigned child contains only schema/network/protocol, standalone-local dependency kind, owning LocalPolicyV1 series/revision, canonical member count, and an ordered unique member array
    And each member contains only dependency ID, closed postgresql, valkey, Station, local_moderation_module, or local_tool_handler purpose, closed unix_socket, loopback_ip, private_cidr, or cluster_dns destination kind, exact transport protocol, exact single port or bounded port interval or kind-prescribed canonical absence, TLS required/disabled state, expected local TLS identity hash or canonical disabled absence, and exactly the kind-specific fields below
    And unix_socket contains one normalized absolute path beneath an administrator-fixed local socket root with canonical host/address/port absence and rejects symlinks or inode changes; loopback_ip contains one canonical 127.0.0.0/8 or ::1 literal with path/CIDR/DNS absence
    And private_cidr contains one canonical RFC1918 or unique-local unicast CIDR plus optional exact literal address with path/DNS absence; cluster_dns contains one lowercase A-label absolute service name, pinned private cluster-DNS resolver identity, and exact permitted private CIDR result set with path/literal absence
    And members sort by canonical JCS bytes of purpose, dependency ID, destination kind, and kind-specific identity; duplicates, overlapping network/port authority for different dependencies, empty required identity, wildcard/glob, userinfo, query, fragment, redirect, proxy, or implicit default is forbidden
    And every DNS answer and connection is rechecked against the exact member on every attempt, all returned addresses must be inside its permitted private CIDRs, redirects are never followed, proxy environment variables are rejected, and request/Station content cannot select or alter a member
    And public/global, unspecified, multicast, link-local, cloud-metadata, private address outside the member, mixed allowed/forbidden answer, DNS rebinding, alternate-address fallback, Unix path escape, and every RogerAI/public-Internet destination are rejected before resolution or connection
    And the child has no signer or authority alone: only its exact one-way complete hash inside the purpose-signed current LocalPolicyV1 grants private dependency authority, and unknown fields/kinds/transports or noncanonical ordering fail strict JCS decoding

  Scenario: Local policy has one current revision and non-signer-controlled applicability
    Given the current LocalPolicyV1 revision is R with complete hash H
    When initialization or authorized local administration commits a successor
    Then policy-series ID derives from strict JCS [LocalPolicyV1-series-v1,local-network-ID]
    And creation is revision 1/prior/authorization absence with initialization source, every ordinary successor is exactly R plus one with prior H and consumes one policy_update LocalAdminAuthorizationV1 binding H and the exact acyclic target-semantic digest, and a break-glass successor instead consumes one policy_head_recovery LocalBreakGlassRecoveryAuthorizationV1 with the same prior/digest relationship
    And one serializable current-head CAS at the independently assigned policy-ledger tuple wins; the target's semantic projection must equal the authorization digest before its one-way authorization-hash reference and resulting complete hash are accepted
    And the bound LocalTrustPublicationV1 is the unique current head at commit, the policy signer is active for local_policy at that commit, effective is no earlier than commit, expiry is later than effective and no later than the signer not-after and trust-document expiry, and exact replay is idempotent
    And request admission and grant issuance independently require the greatest current unexpired policy head; a stale, future, expired, forked, skipped, overflowed, differently encoded, or wrong-network revision fails closed
    And break-glass recovery regenerates LocalPrivateDependencyAllowlistV1 with the next owning policy revision and a new child hash while requiring its complete ordered member business projection, excluding only owner series/revision, to be byte-identical to the expired predecessor; every other evaluator/rule/mode/bound business field remains byte-identical and the recovery target-semantic digest binds the regenerated child hash
    And LocalExecutionGrantV1, LocalProviderAssertionV1, and LocalSettlementReceiptV1 copy the selected policy series/revision/complete hash and dispositions byte-identically, so a later policy revision never rewrites an existing attempt or historical receipt

  Scenario: Every local authority object uses a non-signer-controlled anchor
    Given a standalone verifier checks "<object>"
    Then purpose-key validity and compromise status use "<anchor>"
    And an absent, unverifiable, rolled-back, or signer-controlled anchor grants no admission, routing, result, or historical-verification authority

    Examples:
      | object | anchor |
      | LocalTrustDocumentV1 | independently assigned LocalTrustPublicationV1 trust-ledger commit tuple under the pinned-root publication chain |
      | LocalTrustPublicationV1 | its independently assigned local trust-ledger commit tuple plus the pinned root or exact prior current trust-document key state prescribed by its closed publication kind |
      | LocalPolicyV1 | independently assigned local policy-ledger commit tuple and exact current trust-publication head |
      | LocalClientInvitationV1 | independently assigned local invitation-ledger commit tuple and exact current trust-publication head |
      | LocalClientCredentialAuthorityV1 | independently assigned local credential-ledger commit tuple and exact invitation or prior-current-head relationship |
      | LocalBootstrapVerifierHeadV1 | independently assigned bootstrap-verifier-ledger commit tuple plus exact current trust/policy/head and source-authorization relationship |
      | LocalOperatorAuthorityHeadSetV1 | independently assigned local operator-set-ledger commit tuple plus exact complete current local_operator credential-head predicate |
      | LocalAdminAuthorizationV1 | independently assigned local administration-ledger commit tuple plus exact current trust/policy/administrator-credential heads and one-use mutation relationship |
      | LocalBreakGlassRecoveryAuthorizationV1 | independently assigned local recovery-ledger commit tuple under the pinned offline root plus exact current trust/policy/client heads and one-use recovery relationship |
      | LocalKeyEscrowExportAuthorizationV1 | independently assigned local key-escrow-authorization-ledger commit tuple plus exact current trust/policy/operator-set/admin-proof and owner-only local ceremony relationship |
      | LocalKeyEscrowReservationV1 | independently assigned reservation-ledger commit tuple plus exact unconsumed export authorization/current-head/key-manifest/destination predicate |
      | LocalKeyEscrowExportResultV1 | independently assigned local key-escrow-result-ledger commit tuple plus exact one-use authorization/reservation/destination/archive relationship |
      | LocalStationAttachAuthorizationV1 | independently assigned local attachment-authorization-ledger commit tuple and exact current trust/publication/policy heads |
      | LocalStationOriginAuthorityV1 | independently assigned local origin-ledger commit tuple and exact consumed authorization or prior-current-head relationship |
      | TowerLocalStationBridgeCredentialV1 | independently assigned local bridge-ledger commit tuple plus exact current LocalTrustPublicationV1/LocalTrustDocumentV1, LocalStationOriginAuthorityV1, and bridge-authority heads; bridge authority signer is current at every transition and the distinct bridge-certificate issuer is required only for certificate-producing kinds, while restriction-only revocation preserves the historical certificate without issuer availability |
      | LocalRequestAuthorizationV1 | durable authenticated-receive local tuple checked against the current credential head and bounded client issue/expiry claims |
      | LocalCancellationAuthorizationV1 | independently assigned authenticated-cancellation receive/cutoff tuple checked against the exact current client credential and nonterminal attempt/grant heads |
      | LocalExecutionGrantV1 | independently assigned local grant-ledger commit tuple plus exact current trust, policy, client, and Station heads |
      | LocalProviderAssertionV1 | independently assigned observed-assertion commit tuple for chain authority plus the distinct evidence-complete tuple only when accepted, never Station start/end claims |
      | LocalRejectedAssertionAuditV1 | independently assigned local audit-ledger tuple plus exact observed assertion head, attempt terminal authority, and rejected_late or rejected_cancelled relationship |
      | LocalSettlementReceiptV1 | atomic local receipt-ledger commit tuple and exact grant/evidence relationships |

  # --- local client lifecycle ----------------------------------------------

  Scenario: LocalClientInvitationV1 is a strict one-use admission authority
    Given standalone admission prepares a client invitation for public key C and closed inference, status, or local_operator role R
    When the local client-admission signer issues LocalClientInvitationV1
    Then its strict signed object contains only schema/network/protocol, standalone-local invitation kind, local client-admission signer key ID, invitation ID, unique 256-bit nonce, positive bootstrap generation and prior LocalClientInvitationV1 ID/complete hash for bootstrap sources or canonical absence of both for administrator_created, exact issuing LocalBootstrapVerifierHeadV1 series/revision/complete hash and positive HMAC-key generation, closed first_bootstrap, root_bootstrap_reissue, or administrator_created source kind, consumed LocalAdminAuthorizationV1 or LocalBreakGlassRecoveryAuthorizationV1 ID/complete hash or canonical first-bootstrap absence prescribed by source, client key ID/algorithm/canonical public-key bytes/hash, role, pinned-root fingerprint, exact current LocalTrustPublicationV1 revision/complete hash, exact current LocalPolicyV1 series/revision/complete hash, bootstrap-verifier-record hash, positive attempt budget, issue time, finite expiry time, and independently assigned local invitation-ledger commit time/global sequence
    And invitation ID derives from strict JCS [LocalClientInvitationV1-id-v1,local-network-ID,unique-nonce], administrator_created consumes client_invite authorization and root_bootstrap_reissue consumes bootstrap_reissue_recovery whose target-semantic digest equals the invitation's acyclic semantic projection, commit is no earlier than issue and strictly before expiry, and the signer is active for local_client_admission under the bound current trust publication at commit
    And the invitation's acyclic semantic projection recursively excludes only its issuing verifier-head revision/hash, source authorization or recovery ID/hash backreference, signatures, complete hash, verifier-record hash, and independently assigned ledger tuples while retaining the HMAC-key generation and every admission business field; its stored bootstrap-verifier record is exactly HMAC-SHA-256 under that generation's secret over the length-delimited concatenation of canonical bootstrap-code secret octets and UTF-8 strict JCS [LocalInvitationVerifierRecordV1-v1,local-network-ID,invitation-ID,acyclic-semantic-projection-digest,attempt-budget,expiry]
    And first_bootstrap and root_bootstrap_reissue require exactly local_operator, while administrator_created permits only inference or status; no administrator authorization, invitation, role change, or certificate can create a second local_operator or promote another client into that role in standalone v1
    And strict decoding rejects an unknown/duplicate/null field, unsupported key/role, re-encoded key, wrong root/network, public identity, public credential, money field, URL, or expiry beyond signer/trust/policy validity

  Scenario: LocalBootstrapVerifierHeadV1 is the signed current bootstrap-verifier authority
    Given standalone initialization, ordinary administrator rotation, or pristine offline-root bootstrap reissue installs bootstrap verifier generation G
    When LocalBootstrapVerifierHeadV1 is signed and committed
    Then its strict object contains only schema/network/protocol, standalone-local bootstrap-verifier-head kind, local_bootstrap_verifier_authority signer key ID, deterministic verifier-series ID, positive revision, previous LocalBootstrapVerifierHeadV1 complete hash or canonical first-revision absence, positive HMAC-key generation, fixed HMAC-SHA-256 algorithm, SHA-256 verifier-secret commitment, immutable finite HMAC-generation not-after local authority tuple, LocalOutstandingInvitationSetV1 complete hash, LocalInvitationSetTransitionV1 complete hash or canonical initialization absence, exact current LocalTrustPublicationV1 and LocalTrustDocumentV1 series/revision/complete hashes, exact current LocalPolicyV1 series/revision/complete hash, closed initialization, first_bootstrap_create, administrator_invitation_create, invitation_failed_attempt, invitation_consume, invitation_expire, invitation_attempt_lock, administrator_rotation, or root_bootstrap_reissue source kind, consumed LocalAdminAuthorizationV1 or LocalBreakGlassRecoveryAuthorizationV1 ID/complete hash or source-prescribed absence, issue-time claim, effective local authority tuple, and independently assigned bootstrap-verifier-ledger commit time/global sequence
    And verifier-series ID derives from strict JCS [LocalBootstrapVerifierHeadV1-series-v1,local-network-ID], revision 1/generation 1 has prior/transition/authorization absence, an empty invitation set, and initialization source; every successor is exactly prior revision plus one with immediate-prior hash
    And every first_bootstrap_create, administrator_invitation_create, invitation_failed_attempt, invitation_consume, invitation_expire, or invitation_attempt_lock preserves the prior positive HMAC-key generation, algorithm, secret commitment, secret, and generation not-after while applying only its exact invitation-set transition; administrator_rotation and root_bootstrap_reissue advance generation by exactly one and install a distinct fresh secret with a new finite not-after bounded by the current signer/trust validity, while any unchanged, skipped, or overflowed required counter is rejected
    And LocalOutstandingInvitationSetV1 is a strict unsigned child containing only schema/network/protocol, verifier-series/revision/generation, canonical member count, and the complete ordered set of every nonterminal invitation under that generation; each member contains only invitation ID, acyclic semantic-projection digest, verifier-record hash, source kind, expiry, attempt budget, and attempts consumed, members sort by unsigned bytewise invitation ID, and declared count equals array length
    And LocalInvitationSetTransitionV1 is a strict unsigned child containing only schema/network/protocol, verifier-series ID, exact prior/result verifier revisions and HMAC-key generations, exact prior/result invitation-set hashes, closed create, failed_attempt, consume, expire, attempt_lock, administrator_rotate, or root_reissue action, canonical nonnegative member-delta count, complete ordered member-delta array, and exact LocalAdminAuthorizationV1, LocalBreakGlassRecoveryAuthorizationV1, resulting LocalClientCredentialAuthorityV1, durable failed-attempt event, independent expiry tuple, or action-prescribed source absence; each delta contains one invitation ID plus its prior/result member or canonical action-prescribed absence and closed issued, failed_attempt, consumed, expired, attempt_locked, verifier_rotated, or root_reissued result
    And deltas sort by unsigned bytewise invitation ID; create has one absent-to-issued delta, failed_attempt has one issued-to-issued delta whose attempts-consumed is exactly prior plus one, consume/expire/attempt_lock has one present-to-terminal-absence delta, administrator_rotate has exactly every prior member and no result member with verifier_rotated result and produces an empty set, and root_reissue terminalizes every prior member with root_reissued result plus exactly one absent-to-issued replacement delta in its resulting set
    And only administrator_rotate with an already empty prior set may have count zero and an empty delta array; every other action has at least one exact delta and declared count always equals array length
    And first_bootstrap_create has source absence, administrator_invitation_create binds and consumes client_invite authorization, invitation_failed_attempt binds an independently committed bounded attempt event, invitation_consume binds the resulting revision-1 credential complete hash, invitation_expire binds the first independent local authority tuple no earlier than expiry, invitation_attempt_lock follows the exact failed attempt that exhausts budget, administrator_rotation consumes bootstrap_verifier_rotate authorization, and root_bootstrap_reissue consumes its recovery authorization
    And an invitation creation, every failed attempt, successful use, expiry, attempt lock, or verifier rotation advances the verifier head, transition, complete set, and invitation state atomically; a newly created invitation binds the resulting head, while that head binds only its acyclic semantic digest and verifier record so no circular hash exists
    And the HMAC secret is exactly 256 fresh bits from the operating-system cryptographic random source, is never stored in a signed object, log, metric, trace, state backup, API response, or public metadata, and its commitment or HMAC record cannot be used as the secret
    And the verifier-head signer is current, active, valid, and uncompromised for local_bootstrap_verifier_authority under the bound current trust head at commit, head effective equals that commit, and every invitation expiry is no later than its immutable generation not-after; unknown fields, a signer-controlled commit tuple, noncanonical set/transition, stale trust/policy, absent or improperly reused secret, wrong counter transition, head fork, malformed source authorization, or a secret commitment mismatch fails closed

  Scenario: An active local operator may rotate the bootstrap verifier after any history
    Given a current usable local_operator and LocalBootstrapVerifierHeadV1 revision V with generation G
    When one bootstrap_verifier_rotate LocalAdminAuthorizationV1 commits generation G plus one
    Then the authorization binds V, the exact complete current LocalOutstandingInvitationSetV1 hash, current trust/policy/operator-set heads, and the target semantic digest for the next empty-set verifier head
    And one serializable transaction compare-and-swaps those heads, allocates a fresh operating-system-random HMAC secret, signs and commits verifier revision V plus one/generation G plus one, terminalizes every previously outstanding invitation as verifier_rotated, and destroys or cryptographically fences the old secret before the new head becomes current
    And the transaction consumes the authorization and advances the verifier head, complete invitation-set predicate, terminal invitation records, and administrator audit result atomically; exact retry returns that result, while a stale head, changed invitation set, partial terminalization, concurrent invitation, secret reuse, or any path that leaves an old verifier usable aborts without changing authority
    And this ordinary rotation remains available after client, Station, request, grant, or receipt history; the offline-root bootstrap_reissue_recovery path remains restricted to a pristine no-client/no-job network

  Scenario: Exactly one root-genesis bootstrap invitation can exist without an administrator
    Given a fresh standalone network has accepted only LocalTrustPublicationV1 revision 1 and LocalPolicyV1 revision 1 and has no invitation, client credential, administrator, request, Station, or local receipt
    When initialization issues the first LocalClientInvitationV1
    Then source is first_bootstrap, bootstrap and HMAC-key generations are 1 with prior/authorization absence, role is exactly local_operator, and network/root/trust/policy/client-key/bootstrap-verifier fields match the locally displayed one-time bootstrap ceremony; its creation advances initialized empty verifier head revision 1 to revision 2 without changing HMAC-key generation
    And one serializable bootstrap-state CAS changes never_issued to issued_generation_1 for that network at the invitation-ledger tuple, and consuming it atomically changes that exact generation to consumed while creating the one initial local_operator credential, removing the invitation from the verifier set through its next verifier head, and advancing the singleton LocalOperatorAuthorityHeadSetV1
    And administrator_created requires canonical bootstrap-generation/prior absence and the exact current bootstrap-verifier key generation; a second first_bootstrap source, any other bootstrap role, non-genesis first-bootstrap trust/policy head, restored never_issued flag with existing history, or ordinary invitation with absent administration authorization is rejected

  Scenario: An unusable unconsumed bootstrap can be reissued only by the offline root
    Given bootstrap generation G is terminal expired, attempt_locked, verifier_lost, or verifier_compromised, was never consumed, and the standalone network still has no client credential, administrator, request, Station, grant, or receipt
    When one bootstrap_reissue_recovery LocalBreakGlassRecoveryAuthorizationV1 and replacement invitation commit
    Then the recovery binds the exact terminal invitation ID/complete hash/state, current verifier-head revision/complete hash/HMAC-key generation/invitation-set hash, current trust/policy heads, replacement client key, next HMAC-key generation, replacement bootstrap-verifier-record hash, and complete LocalMutationTargetSemanticV1 digest
    And LocalClientInvitationV1 source is root_bootstrap_reissue, bootstrap and HMAC-key generations are exactly G plus one with exact prior ID/hash, role remains local_operator, and the recovery is consumed in the same transaction that CASes bootstrap state, verifier head/set, pristine operator-set head, and terminal invitation record to issued_generation_G_plus_1 with the replacement as the sole outstanding member
    And verifier_lost or verifier_compromised makes the current invitation fail closed immediately; the offline recovery transaction installs a fresh operating-system-random bootstrap-verifier HMAC key, commits its next signed head/transition and replacement verifier record, terminalizes and fences the old invitation/key generation, and issues the replacement atomically
    And only that newest generation can be consumed; zero, skipped, overflowed, concurrent, nonterminal-prior, already-consumed, history-bearing, wrong-role, or online reset/reissue is rejected

  Scenario: LocalClientInvitationV1 consumption and credential revision one are atomic
    Given one valid unconsumed LocalClientInvitationV1 and proof of its exact client key
    When concurrent consumers or a response-loss retry reaches the serializable credential transaction
    Then one uniqueness transaction compare-and-swaps the exact issuing/current verifier heads, current complete invitation set, current trust/policy and operator-set heads, consumes that invitation ID exactly once, commits LocalClientCredentialAuthorityV1 revision 1, and advances the verifier head through its invitation_consume transition
    And revision 1 copies invitation/client/key/role/root/trust/policy fields byte-identically, has initial_invitation source with the consumed invitation ID/complete hash, and has credential-ledger commit tuple later than invitation commit and strictly before invitation expiry
    And if and only if its source-prescribed role is the first pristine local_operator, that same transaction advances the empty operator set to its singleton head; inference or status consumption proves the singleton is unchanged
    And exact replay after fresh key proof returns the one existing authority outcome, while divergent reuse, another key/role/network, expired use, or a second credential series fails without consuming or issuing anything else

  Scenario Outline: LocalClientCredentialAuthorityV1 has one exact revision source
    Given a local client credential change has kind "<kind>"
    Then LocalClientCredentialAuthorityV1 contains only schema/network/protocol, standalone-local credential kind, local client-admission signer key ID, local client-certificate issuer key ID, deterministic stable credential-series ID, positive revision, previous LocalClientCredentialAuthorityV1 complete hash or canonical first-revision absence, stable client ID, client key ID/algorithm/canonical public-key bytes/hash, closed role, unique credential serial, credential-certificate complete hash, immutable certificate-issuance credential revision and credential-ledger commit tuple, active or revoked state, state-effective tuple, compromise-effective tuple or canonical non-compromise absence, not-before/expiry local tuples, pinned-root fingerprint, exact LocalTrustPublicationV1 revision/complete hash, issuance LocalPolicyV1 series/revision/complete hash, issue-time claim, independently assigned local credential-ledger commit time/global sequence, closed change kind, and exactly "<source>"
    And every foreign source field is canonically absent

    Examples:
      | kind | source |
      | initial_invitation | consumed LocalClientInvitationV1 ID/complete hash; revision is 1 with prior absence, active state, no compromise, and every client/key/role/root/trust/policy field copied byte-identically |
      | same_key_renewal | fresh local-administrator audit authorization ID/complete hash/commit tuple, current client-key proof hash, unchanged client/key/role, replacement serial/certificate, active state, and no compromise |
      | key_rotation | fresh local-administrator audit authorization ID/complete hash/commit tuple, replacement-key proof hash, current-key proof hash or exact compromise_recovery absence, unchanged client/role, replacement key/serial/certificate, active state, and optional old-key compromise tuple |
      | role_change | fresh local-administrator audit authorization ID/complete hash/commit tuple, current client-key proof hash, unchanged client/key, exact prior/new role, replacement serial/certificate, active state, and no compromise |
      | revoke | fresh local-administrator audit authorization ID/complete hash/commit tuple, unchanged client/key/role/serial/certificate, revoked state, closed ordinary or compromise reason, and compromise tuple present exactly for compromise |
      | break_glass_recovery | consumed sole_admin_credential_recovery LocalBreakGlassRecoveryAuthorizationV1 ID/complete hash/commit tuple, exact current sole local_operator authority head made terminal/superseded at target commit T, replacement-key proof, unchanged stable client ID/local_operator role, replacement key/serial/certificate and active uncompromised state; compromised_key records old-key compromise-effective exactly T in successor history, while expired or lost_key requires canonical old-key compromise absence |

  Scenario: Local client credentials have one current head and immediate revocation
    Given a client's current LocalClientCredentialAuthorityV1 is revision R with complete hash H
    When a renewal, key rotation, role change, or revocation commits
    Then stable client ID derives from strict JCS [LocalClientV1-id-v1,local-network-ID,initial-client-public-key-hash] and stable credential-series ID derives from [LocalClientCredentialAuthorityV1-series-v1,local-network-ID,stable-client-ID]
    And every successor is exactly R plus one with prior H, one current-head CAS at the independently assigned credential-ledger tuple wins, not-before/state-effective are no earlier than commit, expiry is finite and bounded by signer/trust expiry, and exact replay is idempotent
    And every authority signer is active for local_client_admission under the byte-identical current trust-publication head at commit; initial_invitation, same_key_renewal, key_rotation, role_change, and break_glass_recovery additionally require a distinct active local_client_certificate signer, set certificate-issuance revision/tuple to that current authority revision/commit, and issue a certificate containing only the exact local network/client/key/role/serial/validity identity scope, never the containing authority series/revision/hash or commit tuple, while the authority one-way binds that certificate complete hash
    And revoke is restriction-only: it copies the historical certificate issuer ID, serial, complete hash, immutable issuance revision/tuple, key/role/validity fields byte-identically and requires no available certificate issuer; the current authority state always gates use, and the retained authority chain maps each historical certificate hash and issuance tuple to every exact authority revision that referenced it
    And the selected issuance policy is current at commit for every revision
    And new request acceptance, retry, response retrieval, stream resume, and grant issuance require the exact current active authority head, credential serial, key proof, role, validity, and non-compromised state at their independent local tuples
    And a revoked head takes effect at its commit, a stale certificate or prior head cannot authorize new work, and historical grant/receipt verification retains the exact prior authority and time-qualified trust history
    And each ordinary successor consumes a matching LocalAdminAuthorizationV1 whose target-semantic digest equals the authority's acyclic semantic projection, while break_glass_recovery consumes only its exact recovery authority; a zero/gap/overflow/fork, stale prior, changed series/client, duplicate serial, malformed source, wrong-purpose signer, unknown field, or public-network field is rejected before the head changes

  Scenario: LocalOperatorAuthorityHeadSetV1 serializes the exact singleton operator authority
    Given a standalone v1 network initializes or changes the current local_operator credential
    When its local operator-set head is signed and committed
    Then LocalOperatorAuthorityHeadSetV1 contains only schema/network/protocol, standalone-local operator-set kind, local_operator_set signer key ID, deterministic operator-set series ID, positive revision, previous LocalOperatorAuthorityHeadSetV1 complete hash or canonical first absence, exact current LocalTrustPublicationV1 and LocalTrustDocumentV1 series/revision/complete hashes, canonical member count, complete ordered LocalOperatorAuthorityMemberV1 array, closed initialization, first_bootstrap, ordinary_credential_successor, or sole_admin_root_recovery source kind, exact source credential/recovery/authorization IDs and complete hashes or source-prescribed absence, issue-time claim, effective local authority tuple, and independently assigned local operator-set-ledger commit time/global sequence
    And each strict LocalOperatorAuthorityMemberV1 contains only stable client ID, LocalClientCredentialAuthorityV1 series/revision/complete hash, credential serial, client key ID/public-key hash, fixed local_operator role, current authority state, validity tuples, and compromise-effective tuple or canonical absence; members sort by unsigned bytewise stable client ID and declared count equals array length
    And operator-set series ID derives from strict JCS [LocalOperatorAuthorityHeadSetV1-series-v1,local-network-ID], initialization is revision 1 with prior absence/count zero/empty array, first bootstrap advances to revision 2/count one, and every later successor is exactly prior revision plus one with immediate-prior hash and count exactly one
    And every local_operator credential creation, renewal, key rotation, terminalization, supersession, or replacement advances its credential head and this complete set head in the same serializable transaction; the set member copies the resulting credential head byte-identically and no operator-affecting transition may commit against an unchanged, incomplete, stale, or signer-selected set
    And the set signer is current, active, valid, and uncompromised for local_operator_set under the transaction's current trust head, effective equals its independently assigned commit tuple, exact replay is idempotent, and zero/gap/overflow/fork, duplicate/member-order defect, wrong role, inconsistent credential, unknown field, or signer-controlled anchor is rejected; the durable set head has no independent expiry and use instead verifies its exact current member credential plus current trust/policy authority

  Scenario: Standalone v1 always has exactly one post-bootstrap local operator
    Given LocalOperatorAuthorityHeadSetV1 has reached its first_bootstrap singleton state
    When an ordinary client invitation or credential transition is authorized
    Then administrator_created invitations permit only inference or status, a role change may not enter or leave local_operator, and an ordinary revocation/demotion/removal of the singleton local_operator is rejected
    And only same-key renewal or key rotation of that same stable operator may ordinarily update the singleton member; a stale/current proof requirement still applies and an unusable or expired singleton fails administration closed until offline-root sole-admin recovery
    And sole_admin_credential_recovery compare-and-swaps the exact singleton set head and member, makes the old credential authority terminal/superseded at target tuple T, proves the replacement key, commits its prescribed successor, and installs that successor as the sole member atomically
    And compromised_key sets the old key's compromise cutoff exactly T so it cannot authorize an overlapping transaction, while expired or lost_key preserves canonical compromise absence but the prior key has no authority at or after T; no transient zero-member/two-member state, attacker-added member, ordinary self-removal, or second operator can block or dilute this recovery predicate

  Scenario: LocalRequestAuthorizationV1 binds the exact current local credential
    Given an authenticated local client creates LocalRequestAuthorizationV1
    Then its strict client-signed object contains only schema/network/protocol, standalone-local request kind, stable client ID, LocalClientCredentialAuthorityV1 series/revision/complete hash, credential serial, client key ID, role, method/path, request-body digest, model, modality, requested bounds, issue/expiry, unique 256-bit request nonce, and high-entropy idempotency key
    And the durable receive transaction verifies strict JCS, signature, body and transport binding, bounded freshness, exact current active inference-role credential head, current trust head, and current applicable LocalPolicyV1 before atomically recording its independently assigned authenticated-receive local time/global sequence and idempotency result
    And status and local_operator credentials are structurally ineligible for this model/tool request path even if a malformed policy or route claims otherwise
    And unknown/public/money fields, a stale credential revision, signer-time authority, divergent idempotency reuse, or a request exceeding credential/policy bounds is rejected before an attempt or mapping exists

  Scenario: LocalCancellationAuthorizationV1 is a strict client-signed one-attempt cutoff
    Given the exact current inference client that created a nonterminal local request wants to cancel its granted attempt
    When the Tower receives LocalCancellationAuthorizationV1
    Then its strict client-signed object contains only schema/network/protocol, standalone-local cancellation kind, fixed cancel_attempt action, deterministic cancellation ID, stable client ID, exact current LocalClientCredentialAuthorityV1 series/revision/complete hash/serial/key ID/inference role, job/request/attempt IDs, LocalRequestAuthorizationV1 complete hash and authenticated-receive tuple, LocalExecutionGrantV1 complete hash and grant-ledger tuple, unique 256-bit cancellation nonce, high-entropy cancellation idempotency key, issue time, and finite expiry no later than the signed grant deadline and credential expiry
    And cancellation ID derives from strict JCS [LocalCancellationAuthorizationV1-id-v1,local-network-ID,stable-client-ID,request-ID,attempt-ID,cancellation-nonce], and the client signature verifies under the exact current active, unexpired, uncompromised credential key after strict UTF-8 JCS decoding and byte-identical request/grant relationship verification
    And its authority interval is exactly half-open: client issue time is no later than the independently assigned Tower authenticated-receive/cutoff tuple, that tuple is strictly earlier than cancellation expiry, and cancellation expiry is no later than the signed execution deadline and credential expiry; equality at expiry or deadline grants no cancellation authority
    And one serializable receive transaction compare-and-swaps that current credential, exact request/grant/attempt nonterminal state, cancellation idempotency absence, and no evidence-complete result before assigning the durable authenticated-cancellation receive/cutoff local time/global sequence and committing LocalAttemptTerminalStateV1 cancelled atomically
    And exact authenticated replay returns the same cancellation/terminal outcome, while divergent idempotency reuse, another client/request/attempt/grant, stale or non-inference credential, cancelled/deadline/evidence-complete prior state, issue/expiry misuse, or signer-controlled time cannot move the cutoff
    When any common field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while retaining the client signature
    Then strict decoding, ID/signature/relationship/current-head verification, one-use CAS, or bounded-time validation rejects it without changing the attempt
    And unknown fields, free-text reason, Station/Tower-selected client identity, public-network identity, money, URL, tool, policy mutation, or authority over another attempt is rejected rather than ignored

  Scenario: A bootstrap invitation admits one scoped local client
    Given a one-time bootstrap invitation binds client key C and local role R
    When the client proves C and atomically consumes the invitation
    Then the local client-admission authority signs one bounded LocalClientCredentialAuthorityV1 for this network, C, and R and authorizes only the distinct current local_client_certificate issuer to issue its byte-identical credential certificate
    And client ID, key, role, credential serial, issue/expiry, LocalPolicyV1 series/revision/complete hash, LocalTrustPublicationV1 revision/complete hash, and invitation ID are recorded
    And no RogerAI account, session, user ID, credit, or credential is created

  Scenario Outline: Local client authorization is role-scoped
    Given a valid local client has role "<role>"
    When it requests "<action>"
    Then the result is "<result>"

    Examples:
      | role | action | result |
      | inference | invoke an admitted model within bounds | evaluated by local policy and routing |
      | inference | administer clients, Stations, CA, policy, backup, or mode | rejected |
      | status | read bounded non-content health | allowed |
      | status | invoke a model or mutate configuration | rejected |
      | local operator | manage invitations and ordinary routing policy | allowed after fresh admin authorization |
      | local operator | export root/private keys without the key-escrow ceremony | rejected |

  Scenario: Local client revocation is immediate for new work
    Given a local client credential is active
    When the local administrator commits its revocation
    Then no new request, retry, response retrieval, or stream resume is authorized by that credential
    And nonterminal requests follow the signed local cancel/drain disposition
    And historical local receipts remain verifiable under their authority anchors

  Scenario: LocalAdminAuthorizationV1 closes every post-bootstrap administration source
    Given fresh local administrator authentication authorizes one client, Station, policy, bootstrap-verifier, key-escrow, or trust-safe mutation
    When LocalAdminAuthorizationV1 is signed and durably committed
    Then its strict object contains only these fields:
      | field                                                                                       |
      | schema/network/protocol                                                                     |
      | standalone-local administration kind                                                        |
      | local-administrator-audit signer key ID                                                     |
      | deterministic authorization ID                                                              |
      | unique 256-bit nonce                                                                        |
      | closed action from the LocalAdminAuthorizationV1 action set below                           |
      | exact subject type/stable ID/current series/revision/complete hash or creation absence      |
      | LocalMutationTargetSemanticV1 digest                                                        |
      | exact current LocalTrustPublicationV1 and LocalTrustDocumentV1 series/revision/complete hashes |
      | exact current LocalPolicyV1 series/revision/complete hash                                   |
      | exact current LocalBootstrapVerifierHeadV1 series/revision/complete hash/HMAC-key generation/LocalOutstandingInvitationSetV1 hash |
      | exact current LocalOperatorAuthorityHeadSetV1 series/revision/complete hash and singleton member |
      | administrator stable client ID                                                              |
      | exact current LocalClientCredentialAuthorityV1 series/revision/complete hash/serial/key ID/local_operator role |
      | fresh administrator proof and audit-session binding hash                                    |
      | issue/expiry                                                                                |
      | independently assigned local administration-ledger commit time/global sequence              |
    And the closed action set is exactly:
      | action                    |
      | client_invite             |
      | client_renew              |
      | client_key_rotate         |
      | client_role_change        |
      | client_revoke             |
      | Station_attach            |
      | Station_renew             |
      | Station_secure_key_rotate |
      | Station_capability_replace|
      | Station_revoke            |
      | policy_update             |
      | trust_key_change          |
      | bootstrap_verifier_rotate |
      | key_escrow_export         |
    And LocalMutationTargetSemanticV1 digest is SHA-256 over strict JCS [LocalMutationTargetSemanticV1-v1,local-network-ID,target schema/kind/stable ID or series/next revision/immediate-prior hash or action-prescribed creation absence,target purpose-signer key ID,every authorization-prescribed immutable business field,current trust,policy,bootstrap-verifier,and operator-set heads] and recursively excludes only the target's direct or nested source authorization/recovery ID/complete-hash backreferences, target signature/complete hash, independently allocated target-ledger tuples/group indexes, issue-time claim, and certificate complete hash/signature while retaining every other prescribed child/object hash and certificate issuer/subject/key/serial/validity field
    And authorization ID derives from strict JCS [LocalAdminAuthorizationV1-id-v1,local-network-ID,unique-nonce], its action/subject/resulting-field shape is exact, and one-use consumption is CASed with the prescribed target creation or next authority revision
    And the signer must be active for local_administrator_audit at commit, the transaction compare-and-swaps the administrator's byte-identical current active credential head and its byte-identical singleton operator-set member, verifies fresh key proof and local_operator role then, and expiry is finite and bounded by trust, policy, signer, and administrator-credential expiry
    And target commit T must satisfy authorization-ledger commit tuple less than or equal to T and T strictly before authorization expiry; at T the target transaction atomically compare-and-swaps the exact unconsumed authorization, bound current trust publication/document, policy, bootstrap-verifier, complete operator-set, administrator-credential, and action-specific target prior heads, and verifies the administrator key, audit signer, target purpose signer, and every certificate issuer actually used are active, valid, and uncompromised
    And a retirement, revocation, compromise, expiry, current-head change, prior consumption, target-digest mismatch, or commit outside that interval makes the target CAS lose without a partial target or consumed authorization
    And mutation, replay for a second revision, a wrong subject/action, an unlisted root export/public-network/money action, an unknown field, or a signer-controlled commit tuple is rejected

  Scenario: LocalBreakGlassRecoveryAuthorizationV1 is offline, narrow, and one-use
    Given the exact current trust history is valid but the pristine bootstrap, current local policy, or sole usable local_operator credential can no longer authorize its required successor
    When the operator invokes the explicit offline pinned-root recovery ceremony
    Then LocalBreakGlassRecoveryAuthorizationV1 contains only schema/network/protocol, standalone-local recovery kind, pinned-root key ID/fingerprint, deterministic recovery-series ID, positive recovery revision, previous recovery complete hash or canonical first absence, deterministic recovery authorization ID, unique 256-bit recovery nonce, closed bootstrap_reissue_recovery, policy_head_recovery, or sole_admin_credential_recovery action, exact current LocalTrustPublicationV1 and LocalTrustDocumentV1 series/revision/complete hashes, exact current LocalPolicyV1 series/revision/complete hash and expired/current state, exact current LocalBootstrapVerifierHeadV1 series/revision/complete hash/HMAC-key generation/invitation-set hash, exact current LocalOperatorAuthorityHeadSetV1 series/revision/complete hash/count and complete singleton member or action-prescribed pristine empty set, exact prior bootstrap generation/invitation ID/complete hash/terminal state plus replacement client key ID/public-key hash/next verifier-key generation/bootstrap-verifier-record hash or action-prescribed absence, exact target client credential series/revision/complete hash or action-prescribed absence, closed expired, attempt_locked, lost_key, or compromised_key reason, LocalMutationTargetSemanticV1 digest for the complete one prescribed next invitation/verifier-head, policy, or local_operator-credential/operator-set transition, issue/expiry, and independently assigned local recovery-ledger commit time/global sequence
    And recovery-series ID derives from strict JCS [LocalBreakGlassRecoveryAuthorizationV1-series-v1,local-network-ID], recovery authorization ID derives from strict JCS [LocalBreakGlassRecoveryAuthorizationV1-id-v1,local-network-ID,recovery-series-ID,recovery-revision,recovery-nonce], every revision is immediate-prior/current-head CAS, the pinned offline root signs the object, commit is inside its finite issue/expiry window, and one uniqueness CAS consumes it with exactly that next target revision or bootstrap generation
    And bootstrap_reissue_recovery is allowed only for the exact newest never-consumed expired, attempt_locked, verifier_lost under lost_key, or verifier_compromised under compromised_key bootstrap generation before any client or job history and atomically advances the verifier head/generation with its replacement invitation as the sole resulting set member; policy_head_recovery is allowed only when the named current policy is expired and creates the exact recovery successor defined above
    And sole_admin_credential_recovery requires the exact current operator-set count one/member and target local_operator head to be expired or lack fresh proof under the root-signed lost_key or compromised_key reason, then atomically makes that head terminal/superseded at T after replacement-key proof and installs only its prescribed successor in the next singleton set head, recording old-key compromise-effective T only for compromised_key and canonical compromise absence for expired/lost_key
    And target commit T must satisfy recovery-ledger commit tuple less than or equal to T and T strictly before recovery expiry; at T one transaction compare-and-swaps the unconsumed recovery/current recovery head, exact current trust publication/document, policy, bootstrap-verifier, complete operator-set, bootstrap-state/prior-invitation, and action-specific target-client heads, and verifies the pinned-root signature plus every online target purpose/certificate signer actually used as current, active, valid, and uncompromised
    And it atomically consumes the recovery with exactly one matching target creation, next revision, or next bootstrap generation; any head change, stale signer, expired use, target-digest mismatch, or competing consumption fails without either side committing
    And it grants no request, Station, trust-key, routing, grant, receipt, public-network, money, root-export, or arbitrary administration authority; wrong target digest, concurrent head change, exact replay after consumption, a second target, or online-root access is rejected and audited

  Scenario: LocalKeyEscrowExportAuthorizationV1 is exact, owner-local, and one-use
    Given the singleton local_operator requests one encrypted server-key export through the owner-local recovery command
    When LocalKeyEscrowExportAuthorizationV1 is signed and durably committed
    Then its strict object contains only schema/network/protocol, standalone-local key-escrow-authorization kind, local_key_escrow_authorization signer key ID, deterministic export-authorization ID, unique 256-bit nonce, consumed key_escrow_export LocalAdminAuthorizationV1 ID/complete hash/commit tuple, exact current LocalTrustPublicationV1 and LocalTrustDocumentV1 series/revision/complete hashes, exact current LocalPolicyV1 series/revision/complete hash, exact current LocalBootstrapVerifierHeadV1 series/revision/complete hash/HMAC-key generation, exact current singleton LocalOperatorAuthorityHeadSetV1 series/revision/complete hash/member and administrator credential authority/hash/key, owner-local ceremony binding, fixed X25519 recovery public-key algorithm/canonical 32-byte value/fingerprint, explicit include_offline_root boolean, LocalOfflineRootEscrowApprovalV1 complete hash or canonical false-flag absence, LocalEscrowServerKeyManifestV1 complete hash, fixed roger-standalone-key-escrow-v1 archive format, fixed X25519-HKDF-SHA256 envelope and AES-256-GCM chunk algorithms plus fixed salt/info domain labels, random 64-bit nonce prefix, and positive chunk-size parameter, exact destination identity, issue/finite-expiry, and independently assigned local key-escrow-authorization-ledger commit time/global sequence
    And authorization ID derives from strict JCS [LocalKeyEscrowExportAuthorizationV1-id-v1,local-network-ID,unique-nonce], and the owner-local ceremony binding contains only a unique ceremony ID, normalized Unix-socket device/inode/path under the configured local administration root, operating-system peer UID equal to the configured owner UID, controlling-TTY device/inode, local command executable hash, fresh administrator audit-session binding hash, and independently observed local challenge tuple
    And recovery and sender-ephemeral X25519 fingerprints are fixed-length unpadded base64url SHA-256 digests over UTF-8 strict JCS [LocalEscrowX25519PublicKeyV1,recovery or sender_ephemeral role,canonical 32-byte public key], respectively
    And no TCP/HTTP route, remote-admin session, forwarded socket, container ingress, browser API, bearer credential, unattended job, or caller without that exact OS peer plus TTY and fresh singleton-operator key proof can create or consume the authorization
    And LocalEscrowServerKeyManifestV1 is a strict unsigned child containing only schema/network/protocol, deterministic export-authorization ID, canonical positive member count, and members sorted by fixed escrow-purpose ordinal then unsigned bytewise key ID, each containing exact current server-side purpose, key ID, algorithm, canonical public-key hash or symmetric-key commitment, protected-store identity, and export-required true
    And escrow-purpose ordinals zero through sixteen are the LocalTrustDocumentV1 directory purposes in their exact listed order and backup_encryption is ordinal seventeen; an unknown, duplicate, reordered, missing required, nonexportable-as-exportable, or differently identified member rejects authorization
    And that manifest contains every and only the current server-side online trust, publication, policy, admission, certificate, verifier-head-authority, operator-set, Station-bridge, grant, receipt-ledger, service-TLS, administrator-audit, escrow-authorization, escrow-result, and backup-encryption private-key identity required by the selected standalone restore profile; local client, local_operator, Station assertion/session/bridge, invitation-code, bootstrap-verifier HMAC secret, public RogerAI, retired unneeded, undeclared, and foreign-network private material is structurally forbidden
    And exact destination identity contains only a normalized absolute parent path beneath the configured owner-only export root, parent device/inode, final filename, no-overwrite disposition, required regular-file type, and mode 0600; symlink traversal, an existing target, wildcard, relative path, changed parent identity, group/world access, or another filesystem destination is rejected
    And include_offline_root false requires canonical approval absence and forbids root material; true requires strict LocalOfflineRootEscrowApprovalV1 containing only schema/network/protocol, standalone-local offline-root-escrow-approval kind, pinned-root key ID/fingerprint, export-authorization ID, authorization acyclic semantic digest, recovery-key fingerprint, exact key-manifest hash, destination identity, archive/encryption parameters, authorization nonce, true root-inclusion flag, issue/finite-expiry, and offline-root signature
    And that approval is created and signed outside the serving process by the pinned offline root, its expiry is no later than authorization expiry, and any false flag, absent/wrong root signature, changed semantic field, or use for another authorization is rejected
    And each reservation generates a fresh operating-system-CSPRNG X25519 sender-ephemeral keypair; it rejects a noncanonical recovery or ephemeral public key and an all-zero X25519 shared secret, and derives the 32-byte content key by HKDF-SHA256 extract/expand where salt is SHA-256 over UTF-8 strict JCS [RogerEscrowSaltV1,local-network-ID,export-ID,authorization-nonce] and info is UTF-8 strict JCS [RogerEscrowKeyV1,X25519-HKDF-SHA256,AES-256-GCM,protocol-version,local-network-ID,export-ID,authorization-complete-hash,key-manifest-hash,recovery-key-fingerprint,recovery-public-key-bytes,sender-ephemeral-public-key-bytes]
    And every AES-256-GCM chunk nonce is the authorized random 64-bit prefix concatenated with its unsigned big-endian 32-bit zero-based chunk index, the positive chunk count cannot exceed 4294967295, and associated data binds the complete envelope-header hash, export ID, manifest hash, chunk index, and exact plaintext length
    And the authorization's acyclic semantic digest excludes only its consumed administration backreference, offline-root approval hash/signature, purpose signature, complete hash, and independently assigned ledger tuple while retaining every export choice, so neither signed object depends on its own hash
    And one serializable commit consumes the exact key_escrow_export administration authorization and compare-and-swaps the bound trust, policy, verifier, complete singleton operator-set, administrator-credential, signer, manifest-key, and destination-absence predicates; its signer is current for local_key_escrow_authorization, expiry is bounded by every bound authority, and replay, mutation, stale state, signer-controlled time, or a second authorization result is rejected

  Scenario: LocalKeyEscrowExportResultV1 makes one ciphertext outcome permanent and retry-safe
    Given one unconsumed LocalKeyEscrowExportAuthorizationV1 passed its owner-local ceremony and current-head checks
    When the isolated local export command reserves, writes, and finalizes the export
    Then the isolated command first generates its fresh sender-ephemeral X25519 keypair and a serializable reservation compare-and-swaps the exact unconsumed authorization, all bound current heads and manifest key identities, owner-local OS peer/TTY, authorization expiry, destination absence, and canonical sender-ephemeral public key into one deterministic export ID with closed reserved state before any server or offline-root private key is read
    And export ID derives from strict JCS [LocalKeyEscrowExportV1-id-v1,local-network-ID,authorization-ID], exact replay resumes only that reservation, and another reservation, destination, recovery key, manifest, root flag, or authorization cannot share it
    And strict purpose-signed LocalKeyEscrowReservationV1 contains only schema/network/protocol, standalone-local reservation kind, local_key_escrow_authorization signer key ID, export ID, exact authorization ID/complete hash/commit tuple, exact copied trust/policy/verifier/operator-set/administrator-credential/key-manifest/recovery-key/root-approval/destination/algorithm fields, fixed X25519 sender-ephemeral algorithm/canonical 32-byte public key/fingerprint, fixed reserved state, issue/authorization-expiry, and independently assigned reservation-ledger commit time/global sequence; its signer and every copied head/key are current at that tuple
    And the sender-ephemeral private key exists only in locked isolated-command memory for that reservation and is zeroized at completion, abort, or process loss; it is never persisted, exported, logged, or accepted from a caller, so a prepublication restart without it terminally aborts rather than generating a second keypair under the consumed authorization
    And the command runs outside the serving daemon/API, streams the exact manifest directly from the protected key store into an authenticated-encryption writer, writes only a same-directory random-name mode-0600 ciphertext temporary file, verifies its complete envelope, fsyncs file and parent directory, and atomically renames with no-replace semantics to the authorized destination
    And include_offline_root true additionally requires direct local offline-root media input to that isolated command, proof that the supplied private key matches the pinned root and LocalOfflineRootEscrowApprovalV1, streaming it only into the authenticated ciphertext, and immediate memory zeroization; the serving daemon, RPC/API, ordinary secret mount, database, logs, and temporary plaintext files never receive it
    And LocalEscrowPlaintextArchiveV1 has exactly one encoding: UTF-8 strict JCS containing only schema/network/protocol, standalone-local escrow-plaintext kind, export/authorization IDs, root-inclusion flag, canonical positive member count, and the exact manifest-ordered members, each containing material kind, purpose/ordinal or root-prescribed absence, key ID, algorithm, public-key hash or symmetric commitment, protected-store identity, private-material encoding, and canonical unpadded base64url private bytes; true root inclusion prepends exactly one pinned-root member and false inclusion forbids it
    And LocalEscrowEnvelopeHeaderV1 is public UTF-8 strict JCS containing only schema/network/protocol, archive format, export and authorization/manifest hashes, recovery-key algorithm/bytes/fingerprint, sender-ephemeral algorithm/bytes/fingerprint, root-inclusion flag, KDF/AEAD algorithms and exact salt/info labels, nonce prefix, chunk size/count, and plaintext byte length; the on-disk archive is exactly 8 ASCII octets RTKESC01, unsigned big-endian 32-bit header length, those header bytes, then for each ascending chunk an unsigned big-endian 32-bit ciphertext length followed by ciphertext and its 16-byte GCM tag, with no padding, compression, alternate member framing, or trailing bytes
    And LocalEscrowCiphertextManifestV1 is a strict unsigned child containing only schema/network/protocol, export ID, authorization/manifest hashes, archive format and encryption parameters copied byte-identically, recovery-key fingerprint, sender-ephemeral algorithm/canonical public key/fingerprint copied from the reservation and envelope header, root-inclusion result, ciphertext byte length, canonical positive chunk count, ordered zero-based chunk index/length/SHA-256 digest members, complete ciphertext SHA-256 digest, authenticated envelope-header complete hash, destination identity/mode, and durable file/parent-fsync plus atomic-rename results
    And LocalKeyEscrowExportResultV1 contains only schema/network/protocol, standalone-local key-escrow-result kind, local_key_escrow_result signer key ID, deterministic export ID, exact authorization ID/complete hash/commit tuple, exact LocalKeyEscrowReservationV1 complete hash/commit tuple, closed completed or aborted result, copied recovery-key, sender-ephemeral-public-key, root-flag, key-manifest, destination, and algorithm fields, LocalEscrowCiphertextManifestV1 complete hash or canonical aborted absence, closed failure reason or canonical completed absence, independently assigned result-ledger commit time/global sequence, and permanent administrator-audit sequence/prior hash
    And completed commits only after recomputing every selected key identity and ciphertext-manifest field, rechecking the reservation, authorization, bound current heads, unchanged manifest-key identities, destination, and result signer, and proving the durable final file is byte-identical to the manifest; it atomically consumes the reservation and authorization into one signed terminal result
    And an error or crash before durable rename leaves no plaintext and at most one owner-only unpublished and unaccepted mode-0600 temporary ciphertext, which may already be cryptographically complete but restart must reconcile only through the exact reservation or remove before signing one aborted result; only the authorized durable no-replace final path plus completed signed result is accepted as an archive
    And a crash after durable rename but before result commit resumes the same reservation, verifies the existing ciphertext, and commits that one completed result without rewriting or duplicating the archive; a completion-time head/key mismatch removes or quarantines the final path and signs only the prescribed aborted result
    And local client/operator/Station private material, bootstrap-verifier HMAC secret, offline-root bytes without the true flag and approval, extra/missing/reordered manifest keys, plaintext archive bytes, a usable partial archive, overwrite, changed permissions/path/inode, stale current head, expired authorization, divergent replay, unknown field, or remote initiation rejects or aborts the export and remains permanently auditable

  Scenario: Every key-escrow authority and result field is mutation-closed
    Given a valid LocalKeyEscrowExportAuthorizationV1, optional LocalOfflineRootEscrowApprovalV1, LocalEscrowServerKeyManifestV1, LocalKeyEscrowReservationV1, LocalEscrowCiphertextManifestV1, and LocalKeyEscrowExportResultV1
    When any common, variant, nested-member, absence, count, order, authority, OS-ceremony, key, algorithm, destination, state, digest, tuple, or signature field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, reordered, or retyped
    Then strict UTF-8 JCS decoding, canonical hashing, purpose/root signature verification, current-head CAS, one-use reservation, ciphertext verification, or exact relationship validation fails before a private-key read or accepted archive result
    And no permissive parser, generic administration authorization alone, filesystem side effect, or unsigned log entry grants export authority or completion

  # --- exact local Station admission and origin ----------------------------

  Scenario: LocalStationAttachAuthorizationV1 has one strict one-use shape
    Given a local administrator authorizes a new private Station and verifies both Station key proofs
    When the local Station-admission signer issues LocalStationAttachAuthorizationV1
    Then its strict signed object contains only schema/network/protocol, standalone-local attachment kind, local Station-admission signer key ID, authorization ID, unique 256-bit nonce, consumed Station_attach LocalAdminAuthorizationV1 ID/complete hash, owner ID, exact preallocated Station ID, assertion key ID/algorithm/canonical public-key bytes/hash, secure-session key ID/algorithm/canonical public-key bytes/hash, LocalStationCapabilityCeilingV1 complete hash, minimum software/protocol versions, pinned-root fingerprint, exact current LocalTrustPublicationV1 revision/complete hash, exact current LocalPolicyV1 series/revision/complete hash, issue/expiry, and independently assigned local attachment-authorization-ledger commit time/global sequence
    And LocalStationCapabilityCeilingV1 is a strict unsigned child containing only schema/network/protocol, standalone-local capability kind, owner/Station IDs, policy series/revision/complete hash, canonical nonempty member count, and ordered unique members containing zero-based ordinal, offer ID, model, LocalModalitySetV1 complete hash, maximum input/output tokens, request/result bytes, streams, concurrency, and attempt duration
    And capability members sort by unsigned UTF-8 offer-ID bytes then model bytes, each ordinal equals its array index, declared count equals array length, and offer ID is unique
    And each LocalModalitySetV1 is a strict unsigned child containing only schema/network/protocol, standalone-local modality-set kind, owner/Station/offer IDs, canonical positive member count, and a canonical ordered unique array whose only v1 values have fixed ordinals chat 0, chat_streaming 1, speech_to_text 2, and text_to_speech 3 and appear in ascending ordinal order
    And every referenced modality set copies the enclosing ceiling owner/Station and member offer IDs byte-identically, its declared count equals its array length, and duplicate, omitted-from-count, unknown, empty, reordered, wrong-owner, wrong-offer, or differently hashed membership is rejected
    And authorization ID derives from strict JCS [LocalStationAttachAuthorizationV1-id-v1,local-network-ID,unique-nonce], the administration authorization's target-semantic digest equals this authorization's acyclic semantic projection and is consumed in the same commit, the capability child is owned by the same owner/Station and no member exceeds policy bounds, commit is no earlier than issue and strictly before finite expiry, and the signer is active for local_station_admission under the bound current trust head at commit
    And unknown/duplicate/null fields, duplicate or overlapping offers, empty capability, invalid key encoding, reused assertion/secure-session keys, a public Station/Tower/origin field, money, URL, wrong root/network/policy, or expiry beyond trust/policy/signer validity is rejected before attachment

  Scenario: Local Station attachment consumes one authorization into origin revision one
    Given one valid unconsumed LocalStationAttachAuthorizationV1 and fresh possession proofs for both bound Station keys
    When concurrent attachment or an exact response-loss retry reaches the serializable origin transaction
    Then one uniqueness CAS consumes the authorization exactly once and commits LocalStationOriginAuthorityV1 revision 1 plus the bound local secure-session certificate atomically
    And revision 1 has initial_attach source containing the consumed authorization ID/complete hash, copies owner/Station/keys/capability/root/trust/policy byte-identically, uses active state, origin epoch 1, fixed assertion epoch 1, no retiring certificate, no_overlap with canonical cutoff absence, and origin-ledger commit strictly before authorization expiry
    And exact retry after both key proofs returns the existing origin/certificate outcome, while divergent reuse, another Station/owner/key/capability, expired use, or a second origin series fails without another identity or certificate

  Scenario Outline: LocalStationOriginAuthorityV1 has one exact revision source
    Given a standalone local Station origin change has kind "<kind>"
    Then LocalStationOriginAuthorityV1 contains only schema/network/protocol, standalone-local origin kind, local Station-admission signer key ID, local Station-certificate issuer key ID, deterministic stable origin-series ID, positive revision, previous LocalStationOriginAuthorityV1 complete hash or canonical first-revision absence, Station/owner IDs, active, draining, or revoked state, positive Station origin epoch, fixed Station assertion epoch 1, immutable assertion key ID/algorithm/canonical public-key bytes/hash, secure-session key ID/algorithm/canonical public-key bytes/hash, secure-session certificate serial and complete hash, immutable certificate-issuance origin revision and origin-ledger commit tuple, retiring certificate serial or canonical absence, closed no_overlap or finish_authenticated overlap disposition, already-authenticated-session finish-cutoff local tuple or canonical no-overlap absence, LocalStationCapabilityCeilingV1 complete hash, pinned-root fingerprint, exact LocalTrustPublicationV1 revision/complete hash, exact LocalPolicyV1 series/revision/complete hash, issue/not-before/expiry tuples, independently assigned local origin-ledger commit time/global sequence, closed change kind, and exactly "<source>"
    And every foreign source field is canonically absent

    Examples:
      | kind | source |
      | initial_attach | consumed LocalStationAttachAuthorizationV1 ID/complete hash; revision/origin epoch are 1 with prior absence, active state, and every owner/Station/key/capability/root/trust/policy field copied byte-identically |
      | same_key_renewal | consumed LocalAdminAuthorizationV1 ID/complete hash/commit tuple for Station_renew, current assertion and secure-session key proof hashes, unchanged origin epoch/keys/capability, replacement certificate serial/hash, retiring prior serial, and bounded finish_authenticated cutoff |
      | secure_key_rotation | consumed LocalAdminAuthorizationV1 ID/complete hash/commit tuple for Station_secure_key_rotate, current assertion-key proof and replacement secure-session-key proof hashes, exact next origin epoch, immutable assertion key/capability, replacement secure-session key/certificate, retiring prior serial, and planned finish_authenticated or compromise no_overlap disposition |
      | capability_replace | consumed LocalAdminAuthorizationV1 ID/complete hash/commit tuple for Station_capability_replace, both current key proofs, unchanged origin epoch/keys/certificate, exact replacement LocalStationCapabilityCeilingV1 complete hash under the current policy, and active or draining state |
      | revoke | consumed LocalAdminAuthorizationV1 ID/complete hash/commit tuple for Station_revoke, unchanged owner/Station/origin/assertion/key/certificate/capability fields, revoked state, closed ordinary or compromise reason, no_overlap, and canonical cutoff absence |

  Scenario: Local Station origin has one current head with bounded secure-session overlap
    Given a Station's current LocalStationOriginAuthorityV1 is revision R with complete hash H
    When renewal, secure-key rotation, capability replacement, or revocation commits
    Then origin-series ID derives from strict JCS [LocalStationOriginAuthorityV1-series-v1,local-network-ID,Station-ID]
    And every successor is exactly R plus one with prior H, one current-head CAS at the independently assigned origin-ledger tuple wins, not-before is no earlier than commit, expiry is finite and bounded by authority-signer/trust/policy expiry, certificate-issuing variants are additionally bounded by the replacement certificate expiry, and exact replay is idempotent
    And every authority signer is active for local_station_admission under the byte-identical current trust-publication head at commit; initial_attach, same_key_renewal, and secure_key_rotation additionally require a distinct active local_station_certificate signer, set certificate-issuance revision/tuple to that current origin revision/commit, and issue a certificate containing only the exact local network/Station/owner/secure-key/serial/validity identity scope, never the containing origin series/revision/hash or commit tuple, while the origin authority one-way binds that certificate complete hash
    And capability_replace and revoke are restriction-only: they copy the historical certificate issuer ID, serial, complete hash, immutable issuance revision/tuple, secure key and validity byte-identically, may commit after that certificate expires, and require no available certificate issuer; current origin state gates use, an expired retained certificate cannot open or continue a session, and retained origin history maps each certificate hash and issuance tuple to every exact origin revision that referenced it
    And the bound policy is current, every capability member remains within it, and every noninitial source consumes the exact current-head LocalAdminAuthorizationV1 once
    And finish_authenticated requires a changed replacement serial, a cutoff after commit and no later than retiring-certificate/policy expiry, and permits only sessions authenticated under the retiring serial before head replacement to finish; no_overlap requires canonical cutoff absence and immediately rejects the retiring serial
    And assertion key and assertion epoch remain immutable for the Station ID in v1; assertion-key loss/compromise or assertion-chain loss requires revocation and a newly authorized Station ID without copying the old assertion head
    And new sessions, offers, routing, and grants require the exact current active origin head, current certificate, current policy/trust heads, and an equal-or-narrower offer; draining permits only already granted work and revoked permits none
    And a zero/gap/overflow/fork, stale prior, changed series/owner/assertion key, skipped origin epoch, expanded capability, malformed overlap/source, duplicate serial, wrong-purpose signer, unknown field, or public-network field is rejected before the head changes

  Scenario: Local grant issue compare-and-swaps every current local authority
    Given a current accepted LocalRequestAuthorizationV1 and one eligible current Station offer are selected
    When the serializable LocalExecutionGrantV1 issue transaction commits
    Then it compare-and-swaps the unique current LocalTrustPublicationV1/LocalTrustDocumentV1, LocalPolicyV1, LocalClientCredentialAuthorityV1, and LocalStationOriginAuthorityV1 heads and rechecks every signer/key/status/validity/compromise relationship at the independently assigned grant-ledger tuple
    And it copies exact trust publication/document, policy, client authority, Station origin authority, capability ceiling, credential/role/key, Station/owner/epoch/assertion/secure-session/certificate, offer/bounds, authenticated-receive tuple, and request digest fields byte-identically
    And it verifies the actual canonical request bytes produce the signed request digest; required moderation loads the exact grant-bound evaluator ID/version/module hash/configuration hash and request-stage rule, deterministically executes it, and may continue only when both computed and signed request-policy dispositions are pass, while disabled moderation proves the exact signed disabled mode, zero moderation-rule count/empty array, and canonical signed no_moderation disposition without loading or executing an evaluator
    And a deterministic required-mode block or error commits only the exact policy-prescribed request_blocked or evaluator_error pre-dispatch terminal/retry outcome; missing or changed evaluator bytes, unavailable artifact/runtime, nondeterministic execution failure, mismatch, or ambiguity fails closed, and neither case creates a grant, advances the grant-sequence head, or dispatches to a Station
    And the local grant signer is active for local_grant at commit, the credential role is exactly inference, the selected offer is an equal-or-narrower inference-only ceiling member whose exact matched policy route disposition is allow, the credential and Station are active, and grant deadline is no later than every bound authority expiry
    And the same transaction compare-and-swaps the durable grant-sequence head keyed by local network and Station ID: an absent head requires sequence 1 and canonical prior-grant-hash absence, while head sequence S/hash H requires bounded sequence S plus one and exact previous LocalExecutionGrantV1 complete hash H
    And exact replay for the same attempt/grant nonce returns the byte-identical committed grant and head without advancing, while zero, gap, overflow, duplicate-conflicting sequence, wrong prior, changed Station, or divergent replay is rejected; restart, restore, certificate/origin rotation, or administrator action cannot reset the head and only a new Station ID starts at 1
    And any concurrent trust/policy/client/Station/grant-sequence head change, ineligible request-policy result, evaluator availability/determinism failure, content/digest mismatch, or disposition mismatch makes that issue CAS lose without a grant, sequence allocation, Station dispatch, or partial attempt

  Scenario: Local Station rotation and revocation preserve historical receipts without authorizing stale work
    Given a receipt binds historical LocalStationOriginAuthorityV1 revision R and LocalClientCredentialAuthorityV1 revision C
    When either current authority rotates, drains, retires, or revokes after the receipt's independent evidence/receipt tuples
    Then historical verification resolves R, C, their exact trust/policy heads, and time-qualified key states at those tuples
    And ordinary later rotation/retirement/revocation does not rewrite the grant, assertion, receipt, or assertion-chain head
    And a compromise effective at or before a required anchor reports that object invalid, while a signer-controlled backdate, current-head rollback, or stale authority cannot validate new work

  # --- policy and moderation ------------------------------------------------

  Scenario: Standalone policy is explicit local policy rather than RogerAI policy
    Given the operator selects a versioned local request/output policy at initialization or through authorized administration
    When a client request is evaluated
    Then the local grant and receipt bind that exact LocalPolicyV1 series, revision, complete hash, and result
    And no RogerAI moderation endpoint, policy claim, badge, telemetry, or network call is used
    And status and receipts label the decision local/private

  Scenario Outline: Local policy availability has deterministic behavior
    Given standalone policy mode is "<mode>"
    When the local policy evaluator is "<condition>"
    Then the request result is "<result>"

    Examples:
      | mode | condition | result |
      | required | healthy and request/output passes | proceed under the exact signed current LocalPolicyV1 head |
      | required | healthy and request is blocked | reject before Station execution |
      | required | healthy and output is blocked | withhold output and apply the configured local free-accounting disposition |
      | required | unavailable, stale, or invalid | fail closed without Station dispatch or verified output |
      | disabled | exactly disabled by signed local policy | route without a moderation claim and record policy-disabled visibly |

  # --- request, grant, and replay -------------------------------------------

  Scenario: A local request has fresh authentication and durable idempotency
    Given a local client signature binds client/network, method/path, body digest, model, modality, issue/expiry, and high-entropy idempotency key
    When the standalone Tower authenticates and atomically consumes it
    Then exactly one local request and attempt are created
    And an exact authenticated retry resolves to that request while divergent reuse is a conflict
    And invalid authentication cannot consume or discover another client's idempotency mapping

  Scenario: Captured local creation authorization cannot retrieve a response
    Given an attacker has a consumed local request signature but lacks fresh client/session and response-retrieval proof
    When it replays or queries the idempotency key
    Then it receives one generic unauthorized result without existence, status, content, receipt, or timing disclosure
    And no new attempt or retrieval credential is created

  Scenario: Local replay state survives every restart and validity window
    Given a local idempotency key reached a terminal request
    When response retention ends or the Tower restarts/restores
    Then durable uniqueness remains through every admitted signature/retry window and then as a permanent scoped tombstone
    And the same local network, client, and key never create another request

  Scenario: The local grant binds one exact free-routing attempt
    Given local policy selected one eligible Station and exact offer
    When the standalone Tower issues LocalExecutionGrantV1
    Then its local grant signature binds schema/network/protocol and standalone-local kind, local signer, current trust document/publication, job/request/attempt, exact client credential authority/key/role/nonce and authenticated-receive tuple, grant nonce, exact Station origin authority/capability/assertion/secure-session identities and epoch/session binding, model/offer, request digest, bounds, exact policy series/revision/hash and disposition, issue/deadline and independent grant-ledger commit tuple, modality, and local-free accounting kind
    And every public Tower, Roger Core, price, credit, hold, payout, and compensation field is canonically absent

  Scenario: Every local grant field is tamper-evident
    Given LocalExecutionGrantV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | local network ID |
      | protocol version |
      | fixed standalone-local grant kind |
      | local grant signer key ID |
      | LocalTrustPublicationV1 revision and complete hash |
      | LocalTrustDocumentV1 series, revision, and complete hash |
      | job ID |
      | request ID |
      | attempt ID |
      | stable client ID |
      | LocalClientCredentialAuthorityV1 series, revision, and complete hash |
      | client credential serial and role |
      | client key ID |
      | client nonce/idempotency key |
      | authenticated-receive local authority time and global sequence |
      | grant nonce |
      | Station ID |
      | Station owner ID |
      | LocalStationOriginAuthorityV1 series, revision, and complete hash |
      | LocalStationCapabilityCeilingV1 complete hash |
      | Station assertion key ID |
      | Station origin epoch |
      | fixed Station assertion epoch 1 |
      | Station secure-session key ID |
      | Station secure-session certificate serial |
      | Station inner-session epoch |
      | Station inner-channel-binding hash |
      | local Station grant sequence |
      | previous LocalExecutionGrantV1 complete hash or canonical first-grant absence |
      | model |
      | offer ID |
      | request digest |
      | maximum input tokens |
      | maximum output tokens |
      | maximum request bytes |
      | maximum result bytes |
      | maximum streams |
      | modality |
      | LocalPolicyV1 series, revision, and complete hash |
      | local request-policy disposition |
      | issue time |
      | execution deadline |
      | independently assigned local grant-ledger commit time and global sequence |
      | local-free accounting kind |
    When each listed field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while the signature remains
    Then every field and mutation pair is rejected before Station execution
    And unknown fields, wrong local network, wrong signer purpose, and public-object downgrade are rejected

  Scenario: A local Station claims one grant once
    Given a valid local grant, matching plaintext request digest, and authenticated Station session
    When duplicate or concurrent deliveries arrive
    Then the Station executes at most once under its durable grant nonce/sequence claim
    And it verifies sequence 1/prior absence or exact prior accepted grant hash and next sequence for that Station ID before claiming
    And at most one result can become the local terminal receipt

  # --- exact result and receipt ---------------------------------------------

  Scenario Outline: Every standalone modality requires exact Station evidence
    Given a local "<modality>" attempt
    When its result and LocalProviderAssertionV1 arrive
    Then Station signature, local network, job/request/attempt, grant, client credential authority/nonce, Station origin authority/keys/epoch/session, model/offer, exact policy series/revision/hash, bounds, request digest, selected-variant response fields, result status, and sequence chain all match
    And a mismatch fails the attempt before verified output or receipt finalization

    Examples:
      | modality |
      | chat |
      | chat_streaming |
      | speech_to_text |
      | text_to_speech |

  Scenario: Every local provider assertion field is tamper-evident
    Given LocalProviderAssertionV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | local network ID |
      | protocol version |
      | fixed standalone-local provider-assertion kind |
      | Station assertion signer key ID |
      | LocalTrustPublicationV1 revision and complete hash copied from the grant |
      | LocalTrustDocumentV1 series, revision, and complete hash copied from the grant |
      | job ID |
      | request ID |
      | attempt ID |
      | LocalExecutionGrantV1 complete-object hash |
      | independently assigned local grant-ledger commit time and global sequence copied from the grant |
      | stable client ID |
      | LocalClientCredentialAuthorityV1 series, revision, and complete hash |
      | client credential serial and role |
      | client key ID |
      | client nonce/idempotency key |
      | grant nonce |
      | Station ID |
      | Station owner ID |
      | LocalStationOriginAuthorityV1 series, revision, and complete hash |
      | LocalStationCapabilityCeilingV1 complete hash |
      | Station assertion key ID |
      | Station origin epoch |
      | fixed Station assertion epoch 1 |
      | Station secure-session key ID |
      | Station secure-session certificate serial |
      | Station inner-session epoch |
      | Station inner-channel-binding hash |
      | local Station grant sequence |
      | model |
      | offer ID |
      | LocalPolicyV1 series, revision, and complete hash |
      | local request-policy disposition |
      | request digest |
      | response digest for complete or canonical absence for provider_error |
      | execution deadline |
      | maximum input tokens |
      | maximum output tokens |
      | maximum request bytes |
      | maximum result bytes |
      | maximum streams |
      | modality |
      | provider input claim for complete or canonical absence for provider_error |
      | provider output claim for complete or canonical absence for provider_error |
      | closed complete or provider_error provider result status |
      | Station start timestamp claim |
      | Station end timestamp claim |
      | Station assertion sequence |
      | previous LocalProviderAssertionV1 complete hash or canonical first-sequence-in-epoch absence |
    When each common or selected-variant required/present field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while the Station signature remains
    Then every applicable field and mutation pair is rejected before verified output or local finalization
    And complete requires exact response bytes/digest and provider input/output usage claims, while provider_error requires canonical absence of response bytes/digest and every provider usage claim; inserting a complete-only field into provider_error or omitting it from complete is rejected
    And no Station may claim cancelled, deadline, output_blocked, evaluator_error, or another Tower-local terminal decision
    And any Tower, Roger Core, dispatch, public money, compensation, payout, unknown, or wrong-object field is rejected rather than normalized

  Scenario: A local provider assertion chain cannot reset for one local Station ID in v1
    Given LocalProviderAssertionV1 has fixed assertion epoch 1, bounded monotonic sequence Q, and one previous-complete-hash field under its stable local Station assertion key, and the Tower retains a distinct per-Station observed assertion head
    When the local assertion-chain relationship and attempt cutoff are checked
    Then sequence 1 is permitted exactly once for that local Station ID and requires canonical previous-hash absence
    And every later sequence is exactly the observed head plus one and binds that exact prior LocalProviderAssertionV1 complete hash across local sessions, certificates, process restarts, and bridge rotation
    And every strictly decoded, signature-valid, byte-identical grant/Station relationship and next-sequence-valid assertion atomically advances the observed head exactly once; if its attempt is still eligible before cutoff, that transaction also wins evidence-complete, while valid evidence after deadline or cancellation advances only the observed head plus an exact durable rejected_late or rejected_cancelled audit and creates no receipt
    And LocalRejectedAssertionAuditV1 is a strict local_administrator_audit-signed object containing only schema/network/protocol, standalone-local rejected-assertion-audit kind, audit signer key ID, assertion complete hash/sequence, Station ID, attempt/grant IDs and grant hash, observed prior/result assertion-head sequence/hashes, exact LocalAttemptTerminalStateV1 complete hash/status/cutoff-or-deadline tuple, closed rejected_late or rejected_cancelled reason, issue-time claim, and independently assigned local audit-ledger commit time/global sequence; it grants no evidence-complete or result authority
    And a valid late/cancelled assertion advances the observed head and commits that audit atomically only while the audit signer is current, active, valid, and uncompromised; if it is unavailable the unchanged assertion waits and retries without head advance, while a divergent assertion remains a fork
    And exact replay returns the existing observed/evidence-or-rejection outcome without advancing, while malformed bytes/signature/relationship or different bytes at the same sequence are a losing fork, do not advance the observed head, and cannot become evidence-complete or a receipt
    And no reconnect, local administrator action, restore, or key replacement can reset/skip the chain; key/head loss, detach/reattach, or identity recovery requires a new local Station ID in v1
    And epoch other than 1, zero, skipped, duplicate-conflicting, overflowed, cross-Station/key, wrong-prior, present-at-first, or absent-after-first shape is rejected before local finalization

  Scenario: Local provider evidence is anchored by durable receipt at the Tower
    Given one LocalProviderAssertionV1 arrives over its exact grant-bound authenticated Station session
    When the standalone Tower commits the evidence-complete local authority time/global sequence
    Then it verifies strict JCS and Station signature, the complete current assertion-chain relationship, byte-identical grant-bound trust/policy/client/Station authority fields, request/result digests and bounds, and the Station assertion key's time-qualified origin/trust state at that independent tuple
    And ordinary draining may finish only an already granted/authenticated attempt through its grant deadline, while revocation or compromise effective no later than evidence-complete rejects evidence not already durably committed before that cutoff
    And Station start/end claims, local receipt-signing time, a stale current head, an uncommitted packet, or a signer-controlled backdate cannot replace the evidence-complete tuple

  Scenario: Cancellation and deadline are Tower-local terminal authorities with no receipt
    Given one granted standalone attempt has no evidence-complete LocalProviderAssertionV1
    When authenticated cancellation or the signed grant deadline races Station evidence
    Then LocalAttemptTerminalStateV1 is a strict durable local record containing only schema/network/protocol, standalone-local attempt-terminal kind, job/request/attempt IDs, LocalExecutionGrantV1 complete hash and grant-ledger tuple, closed cancelled or deadline status, exact LocalCancellationAuthorizationV1 ID/complete hash/authenticated-receive cutoff tuple or canonical deadline absence, signed execution deadline plus first independently observed local authority tuple no earlier than that deadline or canonical cancellation absence, and independently assigned terminal-state commit time/global sequence
    And cancelled derives only from a durable cancellation authorized by the exact current client/credential/request relationship and its Tower-assigned cutoff, while deadline derives only from the grant's signed deadline and an independent Tower authority observation; Station timestamps, status strings, disconnects, or assertions grant neither status
    And one serializable attempt-state CAS permits either evidence-complete before the applicable cutoff/deadline or the Tower-local cancelled/deadline state, never both; once the latter wins, a later strict next assertion may advance only the distinct observed assertion head with its rejection audit and produces no LocalProviderAssertion-backed LocalSettlementReceiptV1
    And retry, restart, restore, clock rollback, duplicate cancel, or Station-signed cancelled/deadline cannot reset, postpone, or overwrite that terminal state

  Scenario: LocalRejectedAssertionAuditV1 is exact and grants no result authority
    Given a strictly valid next LocalProviderAssertionV1 was observed only after Tower-authoritative cancellation or deadline won
    When its observed-head transaction commits
    Then strict purpose-signed LocalRejectedAssertionAuditV1 contains only schema/network/protocol, standalone-local rejected-assertion-audit kind, local_administrator_audit signer key ID, Station ID/assertion epoch/sequence/prior hash/assertion complete hash, job/request/attempt IDs, LocalExecutionGrantV1 complete hash, observed prior/result head hashes, assertion-observation tuple, exact LocalAttemptTerminalStateV1 complete hash/status/cutoff/commit tuple, closed rejected_cancelled or rejected_late reason, independently assigned local audit-ledger commit time/global sequence, and audit sequence/previous hash
    And the audit signer is current under the transaction's current trust head, the reason is recomputed from the independent observation versus terminal cutoff tuples, and observed-head advance, audit, and no-evidence attempt disposition commit atomically
    And mutation, replay under another sequence/attempt, signer time, Station status/timestamps, malformed or forked assertion bytes, or an audit without its exact terminal authority cannot advance the head, reopen the attempt, authorize output, or create a receipt

  Scenario: LocalSettlementReceiptV1 is immutable free accounting
    Given exact valid local evidence and policy disposition
    When the local transaction finalizes it
    Then it commits one local receipt with complete LocalProviderAssertionV1 and LocalExecutionGrantV1 hashes, exact grant-bound and receipt-commit-current trust relationships, policy/client/Station authority relationships, session/model/variant-aware digests/counts/bounds/dispositions, distinct provider and local-terminal result statuses, evidence-complete authority tuple, independent receipt-ledger commit tuple, ledger sequence/previous hash, purpose signer key, and local-free accounting kind
    And before signing it always verifies the actual canonical request bytes/digest and deterministically recomputes the exact grant-bound route/bounds/result-accounting and request-policy decision; required moderation always reloads and runs the exact request evaluator/module/configuration/rule, while disabled moderation proves the signed disabled mode, zero rule count/empty array, and request no_moderation without executing an evaluator
    And only complete provider status supplies and verifies canonical response/result bytes, response digest, provider usage claims, and local input/output recounts and runs the required output evaluator; block maps to local-terminal output_blocked, deterministic evaluator error maps to evaluator_error, and pass or disabled output no_moderation maps to complete
    And provider_error requires canonical absence of response/result bytes/digest, provider usage claims, local recounts, and output evaluation fields, uses exactly not_evaluated_provider_terminal output-policy disposition, and maps only to local-terminal provider_error
    And unavailable/changed required artifacts or bytes, evaluator runtime/nondeterminism failure, digest mismatch, non-allow route, rule ambiguity, impossible provider/local status pair, or claimed-disposition mismatch rejects finalization, while a deterministic signed evaluator error follows its exact evaluator_error disposition rather than being confused with unavailability
    And consumer debit, hold, Station earning, Tower candidate/share, external-cash lineage, processor fee, payout, and RogerAI fields are absent
    And receipt plus terminal attempt state commit atomically

  Scenario: Every local receipt field and absence is tamper-evident
    Given LocalSettlementReceiptV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | local network ID |
      | protocol version |
      | standalone-local origin kind |
      | local receipt signer key ID |
      | grant-bound LocalTrustPublicationV1 revision and complete hash |
      | grant-bound LocalTrustDocumentV1 series, revision, and complete hash |
      | receipt-commit-current LocalTrustPublicationV1 revision and complete hash |
      | receipt-commit-current LocalTrustDocumentV1 series, revision, and complete hash |
      | local receipt ID |
      | job ID |
      | request ID |
      | attempt ID |
      | LocalExecutionGrantV1 complete-object hash |
      | independently assigned local grant-ledger commit time and global sequence |
      | LocalProviderAssertionV1 complete-object hash |
      | stable client ID |
      | LocalClientCredentialAuthorityV1 series, revision, and complete hash |
      | client credential serial and role |
      | client key ID |
      | client nonce/idempotency key |
      | grant nonce |
      | Station ID |
      | Station owner ID |
      | LocalStationOriginAuthorityV1 series, revision, and complete hash |
      | LocalStationCapabilityCeilingV1 complete hash |
      | Station assertion key ID |
      | Station origin epoch |
      | fixed Station assertion epoch 1 |
      | Station secure-session key ID |
      | Station secure-session certificate serial |
      | Station inner-session epoch |
      | Station inner-channel-binding hash |
      | model |
      | offer ID |
      | LocalPolicyV1 series, revision, and complete hash |
      | local request-policy disposition |
      | local output-policy disposition |
      | request digest |
      | response digest for complete or canonical absence for provider_error |
      | execution deadline |
      | maximum input tokens |
      | maximum output tokens |
      | maximum request bytes |
      | maximum result bytes |
      | maximum streams |
      | modality |
      | provider input claim for complete or canonical absence for provider_error |
      | provider output claim for complete or canonical absence for provider_error |
      | local input recount for complete or canonical absence for provider_error |
      | local output recount for complete or canonical absence for provider_error |
      | provider result status copied from LocalProviderAssertionV1 |
      | local terminal result status |
      | evidence-complete local authority time |
      | evidence-complete local authority sequence |
      | local-free accounting kind |
      | local ledger sequence |
      | previous local ledger hash or canonical first-sequence absence |
      | independently assigned local receipt-ledger commit time and global authority sequence |
    When each common or selected-variant required/present field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while the local receipt signature remains
    Then every applicable field and mutation pair fails strict decoding, exact variant/absence validation, or the local receipt signature
    And complete requires every complete-only response/usage/recount field, provider_error forbids all of them, provider status permits only complete or provider_error, and local terminal status permits only complete, output_blocked, evaluator_error, or provider_error with the exact mapping above
    And inserting any RogerAI public identity, Tower admission/dispatch, Core observation, price, currency, credit, hold, debit, earning, funding, compensation, payout, unknown, or wrong-object field is rejected
    And no RogerAI verifier treats the object as a public SettlementReceiptV2

  Scenario: The standalone local receipt ledger has one canonical first link
    Given LocalSettlementReceiptV1 has local ledger sequence Q and its previous-ledger-hash field
    When the local serializable receipt commit verifies the chain
    Then sequence 1 requires canonical previous-hash absence and every later sequence is exactly the accepted head plus one with that exact prior LocalSettlementReceiptV1 complete hash
    And exact replay returns the existing receipt, while zero, a gap, overflow, prior presence at sequence 1, absence later, cross-network prior, wrong prior, or conflicting bytes at one sequence fails without local finalization
    And local receipt ID derives from strict JCS [LocalSettlementReceiptV1-id-v1,local-network-ID,attempt-ID], and the independently assigned receipt-ledger tuple is no earlier than evidence-complete and is allocated inside the same serializable transaction
    And that transaction compare-and-swaps the unique current LocalTrustPublicationV1/LocalTrustDocumentV1 heads and copies them into the receipt as receipt-commit-current trust; the current publication is either byte-identical to the grant-bound publication or its strict retained immediate-prior descendant, and the complete chain from the grant head has no gap, fork, rollback, or root/network change
    And the local_receipt_ledger signer must be current, active, valid, and uncompromised under that receipt-commit-current trust at the receipt tuple, while the grant-bound trust/policy/client/Station fields remain byte-identical historical grant relationships verified at their own independent anchors
    And a concurrent trust-head change, retired/revoked receipt signer, missing ancestor proof, or substitution of the receipt-current head with the older grant head makes the atomic terminal receipt commit fail

  Scenario: A standalone receipt verifies only under its pinned local trust root
    Given a local client later verifies a local receipt
    When it resolves the signer and Station history
    Then the local network ID, pinned offline root, monotonic LocalTrustPublicationV1 history, purpose and time-qualified key states, exact policy/client/Station/grant/assertion relationships, independent evidence/receipt tuples, signatures, and ledger chain must all verify
    And a RogerAI public root, another standalone root, or signer-controlled timestamp cannot make it valid

  # --- retry, failure, and durable state ------------------------------------

  Scenario Outline: A failed local attempt has one outcome
    Given a standalone attempt encounters "<failure>"
    When its authoritative deadline/retry policy runs
    Then "<outcome>"
    And no public or local money entry is created

    Examples:
      | failure | outcome |
      | no eligible Station | request fails locally without a Station grant |
      | Station rejects or disconnects before complete evidence | attempt fails and a retry may use a new attempt/grant/Station |
      | local policy required but unavailable | fail closed without dispatch |
      | result or assertion invalid | attempt fails without verified output |
      | complete evidence reaches exact deadline or local cancellation cutoff | attempt is late/cancelled and cannot finalize |
      | database, idempotency, sequence, signer, or policy state unavailable | readiness false and no guessed terminal receipt |

  Scenario: A local retry never reuses authority
    Given a local attempt failed before verified terminal output and retry policy permits another
    When the Tower retries
    Then it creates a new attempt ID, nonce, grant, Station/session binding, deadline, and sequence context
    And late evidence from the prior attempt remains unable to finalize either attempt

  Scenario: Standalone no-egress remains true through every job path
    Given local client, Station, policy, database, DNS, proxy variables, redirects, errors, retries, and restore inputs are adversarially exercised
    When initialization, admission, routing, streaming, receipt verification, status, and failure run
    Then every connection remains inside the explicit private dependency allowlist
    And no RogerAI or public-Internet DNS lookup, update, telemetry, discovery, moderation, settlement, or trust request occurs

  Scenario: Local durable-state failure cannot silently fall back to memory
    Given standalone durable mode loses PostgreSQL, required current local purpose signer or publication history, online purpose-scoped certificate issuer, idempotency state, sequence head, or policy state
    When a new or active request needs that authority
    Then readiness is false and no request finalizes under an in-memory substitute
    And recovery resumes only from the durable local network history
