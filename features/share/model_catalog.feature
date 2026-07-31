# ROGERAI MODEL CATALOGUE — offerable models beside detected ones.
#
# Today SHARE can only list models that are ALREADY running behind a local
# OpenAI-compatible server. To put a RogerAI model on air an operator must leave
# the tool entirely: find the repo, download weights, install a runtime, work out
# the serve flags, then come back and run `roger share`. Every one of those steps
# is a place to give up.
#
# This adds a second class of row: a model RogerAI can OFFER but which is not on
# this machine yet. It is visually distinct, it is never silently acquired, and it
# carries the same evidence a released model card does.
#
# Scope:
#   - a signed, cached catalogue of offerable artifacts;
#   - SHARE rendering detected and offerable models in ONE list, distinguishable
#     at a glance;
#   - hardware fit assessment from real detected memory, not a guess;
#   - consented, resumable, checksum-verified acquisition;
#   - supervised local serving of an acquired artifact, then going on air.
#
# Interfaces: internal/catalog (manifest + validation), internal/provision
#   (acquire + verify), internal/runtime (serve + health), internal/detect
#   (unchanged discovery), internal/tui (SHARE), cmd/rogerai.
#
# Deliberately extends features/share/hosting_compatibility.feature, which put
# "launching/stopping model hosts" out of scope. That boundary is moved HERE, on
# purpose, and only for artifacts RogerAI itself publishes a manifest for.
#
# Out of scope:
#   - acquiring or launching arbitrary third-party models the catalogue does not
#     describe (an operator can still point --upstream at anything);
#   - replacing llama.cpp or embedding an inference engine in the binary;
#   - training, fine-tuning, or converting artifacts on the operator's machine.

Feature: RogerAI can offer models it publishes, not only models already running
  An operator should be able to see what RogerAI could put on air on this machine,
  understand the cost in bytes and memory before anything is fetched, and never be
  surprised by a download, a background process, or a dead artifact.

  Background:
    Given an operator opens the SHARE screen

  # ---- the list -------------------------------------------------------------

  Scenario: Detected and offerable models share one list
    Given a local server is already serving "qwen3-vl-8b"
    And the catalogue offers a model that is not on this machine
    Then both appear in the SHARE model list
    And the detected model is shown as ready to go on air
    And the offerable model is shown as not installed
    And the two states are distinguishable without colour alone

  Scenario: An offerable model states what it would cost before anything happens
    Given the operator highlights an offerable model
    Then the download size is shown
    And the memory the model needs to serve is shown
    And its licence is shown
    And its upstream lineage is shown when an upstream parent exists

  Scenario: An offerable model cannot go on air until it exists locally
    Given the operator selects an offerable model
    Then RogerAI does not register it with the broker
    And the operator is offered acquisition instead of a broadcast

  # ---- honesty --------------------------------------------------------------

  Scenario: A catalogue entry whose artifact is not public is never offered
    Given a catalogue entry names an artifact that is not publicly reachable
    Then the entry does not appear in the SHARE list
    And the operator is not shown a download action for it

  Scenario: The catalogue never invents a measurement
    Given a catalogue entry has no published measurement on this device class
    Then the entry does not display a speed or a benchmark figure
    And it states that the envelope for this device is unmeasured

  # ---- hardware fit ---------------------------------------------------------

  Scenario Outline: Fit is assessed against real detected memory
    Given the machine has <available> of usable memory
    And an offerable model needs <needed> to serve
    Then the entry is marked "<verdict>"

    Examples:
      | available | needed | verdict     |
      | 32 GB     | 4 GB   | fits        |
      | 8 GB      | 7 GB   | tight       |
      | 8 GB      | 24 GB  | will not fit |

  Scenario: A model that cannot fit is not silently offered anyway
    Given an offerable model cannot fit in detected memory
    Then acquisition requires an explicit override
    And the reason it cannot fit is stated

  # ---- acquisition ----------------------------------------------------------

  Scenario: Nothing is downloaded without explicit consent
    Given an offerable model is highlighted
    Then no bytes are fetched until the operator confirms
    And the confirmation names the exact source and the byte count

  Scenario: An interrupted download resumes rather than restarting
    Given an acquisition was interrupted partway
    When the operator retries
    Then the transfer resumes from what is already on disk

  Scenario: A corrupt artifact is never served
    Given an acquired artifact fails its published checksum
    Then it is not served
    And it is not registered with the broker
    And the failure names the expected and actual digest

  Scenario: Acquired artifacts live in a documented, reclaimable place
    Then the storage location is shown to the operator
    And the operator can remove an acquired artifact
    And removing it returns the entry to the offerable state

  # ---- serving --------------------------------------------------------------

  Scenario: A model is only announced once it actually answers
    Given an acquired artifact has been started locally
    Then RogerAI probes its endpoint before going on air
    And the model is registered only after the endpoint answers
    And a runtime that never becomes healthy is reported, not left pending

  Scenario: A supervised runtime is stopped when sharing stops
    Given RogerAI started a local runtime for an acquired model
    When the operator takes that model off air
    Then the runtime RogerAI started is stopped
    And a server the operator started themselves is left running

  # ---- degradation ----------------------------------------------------------

  Scenario: A catalogue that cannot be fetched never breaks discovery
    Given the catalogue cannot be reached
    Then previously detected local models are still listed
    And SHARE remains usable
    And the catalogue section states it is unavailable rather than empty

  Scenario: A stale cached catalogue is used but labelled
    Given the catalogue was last fetched some time ago
    And it cannot be refreshed now
    Then the cached entries are still offered
    And their age is visible

  Scenario: Local use is never conditioned on the catalogue
    Then an operator can still share a model they run themselves
    And an operator can still point RogerAI at an explicit endpoint
    And no acquisition is required to use RogerAI locally
