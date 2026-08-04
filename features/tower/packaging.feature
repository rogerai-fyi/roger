# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# Scope: Tower release artifacts, signature/provenance verification, installer behavior,
# host/container/Kubernetes profiles, configuration assets, and public download claims.

Feature: Operators can install the same verified Tower safely on Linux or Kubernetes
  A Tower release is independently verifiable, mode-explicit, least privilege, reproducible,
  and unable to become public merely through a packaging or configuration default.

  # --- release identity and artifact set -----------------------------------

  Scenario Outline: Each supported host architecture gets one versioned static archive
    Given a released Tower version V built from commit C
    When the release assets are enumerated for "<os>" "<arch>"
    Then a `roger-tower` archive, checksum entry, signature bundle, SBOM, provenance predicate, and versioned TUF target metadata exist
    And the binary reports version V, commit C, protocol range, and build identity

    Examples:
      | os    | arch  |
      | linux | amd64 |
      | linux | arm64 |

  Scenario: One multi-architecture OCI release resolves to matching immutable images
    Given Tower version V is released
    When the OCI manifest is inspected by digest
    Then it contains linux/amd64 and linux/arm64 images
    And every image reports version V and the same source commit
    And each platform image and the manifest list have verifiable signatures and provenance

  Scenario: Independent clean builders reproduce every binary and image payload
    Given two isolated builders use the signed source commit, locked dependencies, declared toolchain, and release flags
    When they build the same Tower platform artifact
    Then the uncompressed binary or image filesystem payload digest is identical
    And any intentionally variable envelope metadata is excluded, documented, and independently provenance-bound

  Scenario: Conflicting release views are detectable when an auditor compares them
    Given two official mirrors or clients retain the same claimed release-metadata version
    When `roger-tower release audit` compares their root, targets, snapshot, timestamp, target digest, and recorded transparency-entry bytes
    Then a byte conflict under one role/version or an invalid inclusion/consistency proof is reported as a split-view incident
    And neither view is promoted until the incident is resolved
    And documentation does not claim two isolated clients detect a malicious split view without comparison, gossip, or a trusted witness

  Scenario: TUF roles bound update authority and freshness independently
    Given the release channel publishes root, targets, snapshot, and timestamp metadata
    When a client resolves a Tower update
    Then each role meets its configured signature threshold, version monotonicity, expiry, delegated path, and hash/length bindings
    And an online timestamp or snapshot key alone cannot authorize another target or root
    And root rotation requires the old-root and new-root thresholds during the bounded transition

  Scenario Outline: Cross-artifact version drift blocks release
    Given a candidate release version V
    When "<artifact>" identifies another version, commit, protocol range, or source tree
    Then the release gate fails and no Tower download is promoted

    Examples:
      | artifact                  |
      | amd64 binary              |
      | arm64 binary              |
      | OCI amd64 image           |
      | OCI arm64 image           |
      | checksum manifest         |
      | TUF root metadata         |
      | TUF targets metadata      |
      | TUF snapshot metadata     |
      | TUF timestamp metadata    |
      | signature bundle          |
      | SBOM                      |
      | provenance predicate      |
      | Compose files             |
      | Helm chart                |
      | JSON Schema               |

  # --- fail-closed verification -------------------------------------------

  Scenario: A valid archive installation verifies identity before execution
    Given an official Tower archive and its release metadata
    When the installer runs
    Then it verifies the TUF root, targets, snapshot, timestamp, version, expiry, target length and digest
    And it verifies the signed manifest identity and expected repository/workflow
    And verifies the archive digest against that signed manifest
    And verifies provenance and selected platform before installing
    And records the installed version and verification result

  Scenario Outline: Installer verification failure leaves the active install unchanged
    Given a working installed Tower
    When an update has "<defect>"
    Then the installer fails before replacing or executing the binary
    And the prior binary, configuration, identity, and service remain recoverable
    And no verification step is silently skipped

    Examples:
      | defect                                      |
      | missing checksum manifest                   |
      | missing signature bundle                    |
      | invalid signature                           |
      | unexpected signer identity                  |
      | unexpected source repository                |
      | digest mismatch                             |
      | wrong operating system                      |
      | wrong CPU architecture                      |
      | missing provenance                          |
      | provenance for another commit               |
      | malformed SBOM                              |
      | unavailable verification tool               |
      | incomplete or truncated download            |
      | metadata older than the rollback floor      |
      | expired update metadata                     |
      | a snapshot mixing files from release versions |
      | release version below Core's security floor |

  Scenario: Offline verification uses the recorded transparency bundle
    Given an operator has the release archive, signed manifest, and verification bundle
    When network access is unavailable
    Then verification can validate the bundled signature, certificate identity, digest, and transparency evidence under the documented trust root
    And it never substitutes an unsigned checksum from the same mirror as sufficient proof

  Scenario: A mirror cannot replace an artifact and its unsigned checksum together
    Given an attacker controls the archive and checksum download origin but not the release signer
    When both files are replaced consistently
    Then signature or provenance verification fails

  Scenario Outline: Archive extraction cannot write outside the staged install
    Given a cryptographically valid-looking but structurally hostile archive
    When it contains "<entry>"
    Then installation rejects the archive before activating any file

    Examples:
      | entry                                      |
      | an absolute path                           |
      | a parent-directory traversal               |
      | a symlink escaping the staging directory   |
      | a hard link escaping the staging directory |
      | two entries resolving to one target        |
      | a device or special file                   |

  Scenario: Host update preserves operator-owned state and configuration
    Given a verified newer Tower binary and an existing installation
    When the host installer stages and activates the update
    Then it does not overwrite identity, secrets, configuration, database, backup, or operator-owned files
    And activation is atomic or rolls back to the prior verified binary

  Scenario: Rollback metadata distinguishes an intentional supported rollback from an attack
    Given an installed Tower version and a signed minimum-version policy
    When an older version is offered
    Then it is rejected if below the security floor
    And an allowed operational rollback requires explicit operator action and compatible state
    And no rollback restores an expired or revoked admission lease

  # --- binary and container least privilege --------------------------------

  Scenario: The Tower process runs without root privileges
    Given the host or OCI package is installed with defaults
    When `roger-tower serve` starts
    Then its runtime user is non-root
    And private identity files remain readable only by that identity
    And it requests no privileged port, device, host namespace, or Linux capability

  Scenario Outline: Hardened OCI execution remains functional
    Given a valid Tower mode configuration
    When the container runs with "<hardening>"
    Then initialization or serving succeeds using only declared writable mounts

    Examples:
      | hardening                         |
      | read-only root filesystem         |
      | all Linux capabilities dropped    |
      | no-new-privileges                 |
      | default seccomp profile           |
      | a non-root numeric user           |
      | a temporary directory size limit  |

  Scenario: All container hardening controls work together
    Given a valid Tower mode configuration and declared writable identity volume
    When the container runs simultaneously as a non-root numeric user with read-only root, no-new-privileges, all capabilities dropped, RuntimeDefault seccomp, and bounded temporary storage
    Then initialization, readiness, serving, drain, and restart succeed without weakening any control

  Scenario: Missing writable identity storage fails explicitly
    Given an OCI Tower has a read-only root filesystem and no identity volume
    When initialization or certificate persistence is required
    Then it fails readiness with an identity-storage error
    And it does not fall back to an ephemeral identity or write secrets elsewhere

  Scenario: Build images contain only intended runtime material
    Given a released Tower OCI image
    When its filesystem and SBOM are inspected
    Then it contains the Tower runtime and declared trust/config assets
    And it contains no source credentials, build cache, package-manager cache, test fixtures, Roger Core secret, database dump, or client history

  # --- host and Compose profiles ------------------------------------------

  Scenario: The systemd unit is safe by default
    Given the host installer creates a systemd service
    When its unit is inspected and started
    Then it uses a dedicated unprivileged account, explicit state and config directories, restart limits, hardening, readiness, and graceful drain
    And secrets are not embedded in the unit or process arguments
    And standalone listeners are loopback unless explicitly changed

  Scenario: Joined Compose contains no public-network state service
    Given an operator selects the joined Compose profile
    When the rendered services and networks are inspected
    Then they contain only the Tower and its scoped identity/config mounts
    And no PostgreSQL, Valkey, payment, Roger Core admin, or public ingress dependency is implied
    And the parent connection is outbound-only

  Scenario: Standalone Compose is locally complete and visibly private
    Given an operator selects the standalone durable Compose profile
    When the stack starts
    Then Tower and PostgreSQL become healthy with durable named volumes
    And any optional local Valkey is used only for trusted local HA
    And no RogerAI authority or public advertisement endpoint is configured
    And the generated local bootstrap fingerprint is shown without exposing its private key

  Scenario: Compose secrets do not appear in environment inspection
    Given enrollment, local auth, or database secrets are needed
    When the Compose process environment and rendered config are inspected
    Then secret values are absent and referenced through protected files or secret mounts

  # --- Kubernetes -----------------------------------------------------------

  Scenario: The joined Helm default is one fenced Tower identity
    Given default joined Helm values
    When the chart renders
    Then exactly one Tower replica owns one persistent identity volume
    And it has no public client ingress or Core database/message-bus credential
    And NetworkPolicy permits only required DNS, parent egress, local Station traffic, and declared observability
    And the workload references the OCI image by verified immutable digest rather than a mutable tag

  Scenario: Cluster admission rejects an unverified Tower image
    Given the chart's documented signature policy is installed in the cluster
    When an unsigned image, wrong signer, wrong provenance source, mutable tag, or digest absent from the signed release is deployed
    Then admission rejects the pod before any Tower process runs

  Scenario: Unsafe joined replica count is rejected
    Given session fencing and per-replica identities are not enabled in v1
    When an operator sets joined replicas above one or enables an HPA
    Then chart validation fails with an identity/session-fencing explanation

  Scenario: Standalone Helm resources remain local authority resources
    Given standalone Helm values
    When the chart renders
    Then its Service, pinned offline-root/publication history, online local purpose keys, database, secrets, backups, and optional ingress belong only to the standalone network
    And no joined certificate, enrollment token, public directory route, or RogerAI credit config is rendered
    And its NetworkPolicy permits only declared private CIDRs, DNS needed for those local names, database, local Stations/clients, and observability
    And it denies RogerAI and public-Internet egress by default

  Scenario Outline: The chart applies a safe pod control
    Given valid Helm values
    When the chart renders
    Then "<control>" is present and effective

    Examples:
      | control                                      |
      | runAsNonRoot                                 |
      | readOnlyRootFilesystem                       |
      | allowPrivilegeEscalation false               |
      | all capabilities dropped                     |
      | seccomp RuntimeDefault                       |
      | resource requests and limits                 |
      | startup, liveness, and readiness probes      |
      | identity or database persistent storage      |
      | graceful termination and drain budget        |
      | a disruption budget where replicas permit it |

  Scenario: A Kubernetes Secret update does not print or log the secret
    Given a mounted secret rotates
    When the Tower reloads or restarts
    Then logs identify the secret purpose and version without its value
    And the old value is removed after the documented overlap

  # --- schema, samples, and download page ----------------------------------

  Scenario: Every shipped sample validates against the exact shipped schema
    Given the release contains joined, standalone, host, Compose, and Helm examples
    When strict validation runs
    Then every sample passes the same versioned schema used by the runtime
    And every unknown field and invalid cross-mode field in negative fixtures fails

  Scenario: The download page explains Tower and broker terminology once
    Given the public Tower download page is enabled for a released beta
    When a visitor reads it
    Then it calls Tower the downloadable broker-like service
    And distinguishes joined public-network mode from standalone/private mode and existing private bands
    And states that Roger Core controls public admission, routing, and settlement

  Scenario: The download page makes no unmeasured claim
    Given resource or performance gates have not yet produced release evidence
    When the public Tower page is built
    Then estimates are labeled as pilot targets rather than minimums or guarantees
    And no latency, throughput, uptime, privacy, hardware-verification, or decentralization claim exceeds measured evidence

  Scenario: Licensing and operator obligations accompany every distribution
    Given any official archive, image, Compose profile, or chart is published
    When an operator obtains it
    Then the applicable software license, public-network operator terms, privacy disclosure, security contact, and update policy are reachable before enrollment
