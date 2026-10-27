# STATUS: PROPOSED 2026-10-24. Scenarios marked BUILT were built on the founder's word of that day,
# each pinned by a unit test named at the scenario. The rest wait. Still RULING NEEDED: section 4
# (the device runtime). Not yet executable under godog. The rulings behind this file and the
# device test notes live in the private docs repository.
#
# WHY THIS EXISTS
#
# features/serving/local_models.feature gives a machine the whole flow: find, judge, fetch, serve.
# features/edge/remote_model.feature does it to a machine you are not sitting at. Both assume two
# things that are not true of a small edge device running a model the owner has as a FILE:
#
#   1. that the file lives on the model hub. A model an owner carries over by hand (a USB stick, a
#      copy over the network) is not there, and must never be looked up there.
#   2. that the pinned runtime loads. The pinned arm64 builds need glibc 2.38 (measured, see
#      internal/runtime/manifest.go). A device on an older Ubuntu has 2.35. roger would refuse.
#
# And a serve that works is still not the one `roger use` picks unless roger is in LAN mode.
#
# This file closes those gaps. It changes nothing about what local_models.feature already promises:
# loopback plus a key, consent before every fetch, the fit arithmetic, the quiet output.

Feature: A Wave model on an edge device, from the RogerAI channel, on a runtime that loads there

  # 1. THE SOURCE IS A FILE THE OWNER PUTS THERE, NOT THE HUB
  #
  # FOUNDER RULING 2026-10-24: some models reach a machine as a file the owner copies over (USB, a
  # copy over the network). roger must serve a file it is pointed at, or one dropped into its
  # models directory, and never ask the hub for it.
  #
  # VERIFIED 2026-10-24 on the Edge line: "roger model serve <path>.gguf"
  # and a file copied into <config>/local/models/ served by its name both work, on loopback, key
  # gated, answering completions. What was missing is below: the file's name is the only thing
  # roger knows about it (licence "-", sha256 "-"), an adapter file beside it is listed as a model,
  # and LAN mode did not see the serve at all (fixed, section 6).

  # BUILT before this spec (verified live 2026-10-24); "served from a file" wording: partly, status says licence "-"
  Scenario: a file the owner points at is served as it is
    Given the file "/media/usb/wave-nano-1p45b-Q4_K_M.gguf" the owner copied over
    When the owner runs "roger model serve /media/usb/wave-nano-1p45b-Q4_K_M.gguf"
    Then it is served on loopback with a key, under the band "wave-nano-1p45b"
    And the model hub is never asked about it
    And "roger model status" says it was served from a file and names the path

  # BUILT before this spec (verified live 2026-10-24)
  Scenario: a file dropped into the models directory is a row by its name
    Given the owner copied "wave-nano-1p45b-Q4_K_M.gguf" into the models directory
    When the owner runs "roger model list"
    Then the row "wave-nano-1p45b-Q4_K_M" is marked "found", with its size and quant from its name
    And "roger model serve wave-nano-1p45b" serves it without a fetch

  # NOT BUILT: the record format (roger-model.json) is a new contract; needs approval
  Scenario: a release folder beside the file is read for what the name cannot say
    Given the owner copied a folder holding "wave-nano-1p45b-Q4_K_M.gguf" and a "roger-model.json" beside it
    And that record names the model, its licence, its sha256, its chat template, its add_bos override and its adapters
    When the owner runs "roger model list"
    Then the row carries the record's name and licence, and the digest is checked against the file
    And a record whose digest does not match the file is said so, and the row is still served on the owner's word

  # BUILT 2026-10-24 (d37d3efb): TestAHandPlacedFileIsDigestedOnceItIsOnTheAir, TestADigestForAServeThatLeftTheAirWritesNoServeRecord
  Scenario: the digest of a hand-placed file is computed once and remembered
    Given a hand-placed file with no record beside it
    When the owner runs "roger model serve" on it
    Then roger computes the file's sha256 in the background and records it beside the file
    And a later "roger model status" shows the digest instead of "-"

  # BUILT 2026-10-27 (pre-push audit note): TestAFileReplacedUnderTheSameNameForgetsTheOldDigest
  Scenario: a different file copied in under the same name does not inherit the old digest
    Given a hand-placed file whose digest roger recorded beside it
    When the owner copies a different file over it under the same name
    Then the record is trusted only while the file's size and modification time are the ones it remembers
    And "roger model status" and the list show no digest until the new file is digested in its turn
    And a record written before modification times were kept is trusted while the size matches

  # BUILT 2026-10-27 (pre-push audit note): TestADigestNeverBringsBackTheRecordOfAServeThatStopped
  Scenario: a digest that finishes after the serve stopped brings no record back
    Given a hand-placed file on the air whose digest is still computing
    When the owner stops the serve before the digest is known
    Then the serve's record stays cleared, because the digest's write and the stop are serialised by one lock
    And the file's own record is still written, since the digest is true of the file

  # NOT BUILT: needs the catalog to mark RogerAI-channel entries
  Scenario: a Wave name that is not on this machine is not looked up on the hub
    Given no file for "wave-nano-1.45b" is on this machine
    And the catalog marks "wave-nano-1.45b" as RogerAI-channel only
    When the owner runs "roger model get wave-nano-1.45b"
    Then roger says the file is not on this machine and how to put it there
    And the model hub is never asked

  # NOT BUILT
  Scenario: a nearby serve names the exact file, and the file still comes from the owner
    Given the workstation serves "wave-nano-1p45b" as a station this machine can see
    And the station reports the exact file and digest it serves (internal/servedfile)
    When the owner runs "roger model get wave-nano-1p45b"
    Then roger names the file and digest to look for and where to copy it
    And it does not try the model hub for a RogerAI-channel model

  # 2. THE ENTRY CARRIES HOW THE MODEL MUST BE SERVED
  #
  # The lab's rule (2026-10-22): a served check is valid only with the serve script's exact flags.
  # Wave Nano needs its chat template and an add_bos_token override; the fire-decision adapter is a
  # --lora line; Nano-VL needs an mmproj file and a fork build. Those belong to the entry, not to the
  # owner's memory.

  Scenario: the entry's serve flags are used, and shown
    Given the catalog entry "wave-nano-1.45b" records a chat template and an add_bos_token override
    When the owner runs "roger model serve wave-nano-1.45b"
    Then llama-server is started with exactly those flags in addition to roger's own
    And "roger model status" names the template and override in use

  # BUILT 2026-10-24 for a file-based adapter (--adapter name|path; no catalog entry yet): TestAnAdapterIsServedBesideItsBaseUnderACombinedBand, TestServeAdapterIsResolvedAndCheckedAgainstItsBase; verified live with the fire-decision LoRA
  Scenario: an adapter is part of the entry and served with its base
    Given the entry "wave-nano-1.45b" offers the adapter "fire-decision" as a GGUF LoRA
    When the owner runs "roger model serve wave-nano-1.45b --adapter fire-decision"
    Then the adapter file is fetched with the same consent and digest rules as the base
    And llama-server is started with "--lora" naming that file
    And the band is named "wave-nano-1.45b+fire-decision" so a reader cannot mistake it for the base

  # BUILT 2026-10-24 (f0267daf), minus "listed under its base model": it is not listed; TestAnAdapterOrProjectorByHeaderIsNotAModelRow
  Scenario: an adapter file is not offered as a model
    Given the file "fire-decision-adapter-f16.gguf" whose GGUF header says general.type is "adapter"
    When the owner runs "roger model list"
    Then it is listed under its base model as an adapter, not as a model row
    And "roger model serve fire-decision-adapter-f16" is refused in one sentence naming its base model

  # BUILT 2026-10-24 (f0267daf): not listed; TestAnAdapterOrProjectorByHeaderIsNotAModelRow
  Scenario: a projector file is not offered as a model
    Given the file "nano-vl-v7cropcpen-mmproj-f16.gguf" whose GGUF header says general.type is "mmproj"
    When the owner runs "roger model list"
    Then it is listed beside its text model, not as a model row

  Scenario: an entry that needs a build roger does not pin is refused honestly
    Given the entry "wave-nano-vl" records that it needs a runtime with multimodal projection support
    And no pinned runtime for this machine has it
    When the owner runs "roger model serve wave-nano-vl"
    Then roger says which runtime feature is missing and that the text model "wave-nano-1.45b" serves here
    And nothing is fetched

  # 3. THIS MACHINE'S OWN SERVE COMES FIRST FOR `roger use`
  #
  # LAN mode already lists the own serve as a band (cmd/rogerai/lan.go). Outside LAN mode roger use
  # asked the market first. An owner who just ran "roger model serve" and then "roger use" of the
  # same name should not be sent to the market for the model under their hands. BUILT for `roger
  # use` (the session and the relay's first rung); the TUI chat, the console and the agent outside
  # LAN mode still take the Edge-then-market ladder and are a later slice.

  # BUILT 2026-10-24: TestRogerUsePrefersWhatThisMachineServes (cmd/rogerai), verified live
  Scenario: roger use prefers the model this machine serves
    Given this machine serves "wave-nano-1.45b" with "roger model serve"
    And the market also lists "wave-nano-1.45b" from another station
    When the owner runs "roger use wave-nano-1.45b"
    Then the local proxy is pointed at this machine's own serve
    And the first line says it is local, at no cost, and names the port

  # BUILT 2026-10-24 for `roger use` (relaySelfFirst before relayEdgeFirst); a session OPENED on the own serve is pinned to this machine and its Edge and does not fall to the market mid-session; a dead own serve with no Edge is refused in this machine's words (TestOwnServeIsTheFirstRungAndItsFailureIsNamed)
  Scenario: the order is own serve, then Edge, then market
    Given this machine does not serve "wave-nano-1.45b"
    And an Edge member this machine belongs to serves it
    When the owner runs "roger use wave-nano-1.45b"
    Then the Edge member is chosen before any market station
    And the market is asked only when neither this machine nor the Edge serves the name

  # BUILT 2026-10-24: TestRogerUsePrefersWhatThisMachineServes
  Scenario: --market asks the market on purpose
    Given this machine serves "wave-nano-1.45b"
    When the owner runs "roger use wave-nano-1.45b --market"
    Then the market station is used and roger says the local serve was skipped on request

  # 4. A RUNTIME THAT LOADS ON THE DEVICE
  #
  # RULING NEEDED (gap 4): how a Jetson gets a runtime. Options:
  #   (a) RogerAI builds and pins its own arm64 llama-server against glibc 2.35, CPU and a Jetson
  #       CUDA build (upstream ships neither), released and digested like roger's own assets;
  #   (b) roger accepts a hand-placed llama-server (built on the device) after checking it answers
  #       --version and --list-devices, and records that the owner supplied it;
  #   (c) both: (b) now, (a) when the build exists.
  # The scenarios cover (b) and the refusal that precedes it; (a) adds manifest rows, not scenarios.

  Scenario: a pinned runtime that cannot load here is refused with the reason and the way out
    Given this machine is linux/arm64 with glibc 2.35
    And every pinned arm64 runtime needs glibc 2.38
    When the owner runs "roger model serve wave-nano-1.45b"
    Then roger says the pinned runtime needs a newer system library than this machine has, naming both versions
    And it names the way out: "roger model runtime use <path to llama-server>"
    And nothing is downloaded

  Scenario: the owner points roger at a llama-server they built
    Given a llama-server binary the owner built on this machine
    When the owner runs "roger model runtime use /opt/llama/bin/llama-server"
    Then roger runs it with --version and --list-devices and records the build, the devices and the path
    And "roger model runtime" says "owner-supplied" with the path and the version it reported
    And a later "roger model serve" uses it

  Scenario: a supplied binary that is not llama-server is refused
    Given a file that does not answer "--version" like llama-server
    When the owner runs "roger model runtime use <that file>"
    Then roger refuses, says what it expected to see, and keeps the previous runtime setting

  Scenario: a supplied runtime that offloads is treated as a GPU build
    Given the owner-supplied llama-server lists a CUDA device
    When the owner runs "roger model serve wave-nano-1.45b"
    Then the fit check counts the device's memory as the runtime's device memory
    And the serve places the model on that device as a pinned CUDA build would

  Scenario: a Jetson's GPU is not reported by nvidia-smi, and roger says so without claiming no GPU
    Given this machine has a Tegra GPU and no nvidia-smi
    When the owner runs "roger detect"
    Then the hardware line says no GPU tool answered, not that there is no GPU
    And "roger model runtime auto" still asks each build --list-devices before choosing

  # 5. THE EDGE DOES IT FROM ACROSS THE ROOM (unchanged, pinned here)
  #
  # remote_model.feature already covers "roger model serve <name> --on <node>" with an authority-
  # signed grant and fetch-by-exact-id. Nothing here loosens it. A Wave entry fetched on a member
  # follows sections 1, 2 and 4 on that member.

  Scenario: a member fetches a Wave model from the RogerAI channel on the authority's say
    Given an Edge member on a Jetson with an owner-supplied runtime
    And the authority runs "roger model serve wave-nano-1.45b --on <member>"
    When the member fetches
    Then it fetches from the RogerAI channel under the grant, with the digest the entry records
    And the authority's fleet window shows the fetch and then the serve, as for any member

  # 6. REGRESSIONS FOUND LIVE ON 2026-10-24 (fixed on wt/jetson-local, pinned by unit tests)

  # BUILT 2026-10-24 (15082fd3): TestLANSelfBandsListWhatRogerModelServeHasOnTheAir
  Scenario: the LAN dial lists what roger model serve has on the air
    Given this machine serves "wave-nano-1p45b" with "roger model serve"
    And nothing is shared and no Edge is joined
    When the owner runs "roger --lan search"
    Then "wave-nano-1p45b" is listed from this machine
    And "roger --lan use wave-nano-1p45b" opens a channel to this machine

  # BUILT 2026-10-24 (15082fd3): TestAnUnjudgeableFileServedOnYourWordGetsTheDefaultContext
  Scenario: a file whose header cannot be judged serves at the default context in the background
    Given a file whose GGUF header carries none of the fields the fit arithmetic needs
    When the owner runs "roger model serve <that file> --detach"
    Then the background serve is started at the default context, never at zero
    And the owner is not left waiting out the detach window

