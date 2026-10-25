# STATUS: PROPOSED 2026-10-24. Not approved. No step definitions, no code. Waiting on founder rulings
# marked RULING NEEDED. Context: ~/ai/rogerai-internal-docs/LOCAL-MODELS-CLI-STATUS-2026-10-24.md and
# JETSON-EDGE-HANDOFF-2026-10-24.md (private repo).
#
# WHY THIS EXISTS
#
# features/serving/local_models.feature gives a machine the whole flow: find, judge, fetch, serve.
# features/edge/remote_model.feature does it to a machine you are not sitting at. Both assume two
# things that are not true of a Jetson Orin Nano running a RogerAI Wave model:
#
#   1. that the file lives on the model hub. Wave models are not published there (founder ruling
#      2026-10-20): they are served free on the RogerAI CLI and share.
#   2. that the pinned runtime loads. The pinned arm64 builds need glibc 2.38 (measured, see
#      internal/runtime/manifest.go). JetPack 6 is Ubuntu 22.04, glibc 2.35. roger would refuse.
#
# And a serve that works is still not the one `roger use` picks unless roger is in LAN mode.
#
# This file closes those gaps. It changes nothing about what local_models.feature already promises:
# loopback plus a key, consent before every fetch, the fit arithmetic, the quiet output.

Feature: A Wave model on an edge device, from the RogerAI channel, on a runtime that loads there

  # 1. THE SOURCE IS THE ROGERAI CHANNEL, NOT THE HUB
  #
  # RULING NEEDED (gap 1): where the bytes come from. Options, in the order the lab proposed:
  #   (a) a RogerAI release URL per entry (object store or the share's static files), with bytes and
  #       sha256 in the entry, fetched the way hub files are fetched today;
  #   (b) a nearby serve hands the file over itself (this machine, an Edge member, a pinned Tower),
  #       on the authority-signed path the Edge already has for fetch-by-exact-id.
  # The scenarios below are written for (a) with (b) as a later addition. They can be rewritten.

  Scenario: a Wave entry names a RogerAI release as its source
    Given the catalog entry "wave-nano-1.45b" whose source is a RogerAI release URL
    And the entry carries the file name, bytes and sha256 of each quant it offers
    When the owner runs "roger model get wave-nano-1.45b"
    Then roger shows the entry's size, memory need and licence and asks for consent, as for a hub file
    And the model hub is never asked about "wave-nano-1.45b"

  Scenario: a RogerAI-sourced file is checked against its recorded digest
    Given the owner consented to fetch "wave-nano-1.45b" at Q4_K_M
    When the bytes arrive and their sha256 is not the entry's
    Then the file is removed and roger says the digest did not match, naming the source
    And nothing is served

  Scenario: a Wave name served nearby still comes from the RogerAI channel
    Given the workstation serves "wave-nano-1.45b" as a station this machine can see
    And the station reports the exact file and digest it serves (internal/servedfile)
    When the owner runs "roger model get wave-nano-1.45b"
    Then roger offers that exact file from the RogerAI channel, not the hub
    And the digest offered is the one the station reported

  Scenario: a Wave name is refused plainly when the channel is unreachable
    Given the RogerAI channel cannot be reached
    When the owner runs "roger model get wave-nano-1.45b"
    Then roger says the RogerAI channel did not answer, in one sentence
    And it does not fall back to the model hub

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

  Scenario: an adapter is part of the entry and served with its base
    Given the entry "wave-nano-1.45b" offers the adapter "fire-decision" as a GGUF LoRA
    When the owner runs "roger model serve wave-nano-1.45b --adapter fire-decision"
    Then the adapter file is fetched with the same consent and digest rules as the base
    And llama-server is started with "--lora" naming that file
    And the band is named "wave-nano-1.45b+fire-decision" so a reader cannot mistake it for the base

  Scenario: an entry that needs a build roger does not pin is refused honestly
    Given the entry "wave-nano-vl" records that it needs a runtime with multimodal projection support
    And no pinned runtime for this machine has it
    When the owner runs "roger model serve wave-nano-vl"
    Then roger says which runtime feature is missing and that the text model "wave-nano-1.45b" serves here
    And nothing is fetched

  # 3. THIS MACHINE'S OWN SERVE COMES FIRST, IN EVERY MODE
  #
  # LAN mode already lists the own serve as a band (cmd/rogerai/lan.go). Outside LAN mode roger use
  # asks the market first. An owner who just ran "roger model serve" and then "roger use" of the
  # same name should not be sent to the market for the model under their hands.

  Scenario: roger use prefers the model this machine serves
    Given this machine serves "wave-nano-1.45b" with "roger model serve"
    And the market also lists "wave-nano-1.45b" from another station
    When the owner runs "roger use wave-nano-1.45b"
    Then the local proxy is pointed at this machine's own serve
    And the first line says it is local, at no cost, and names the port

  Scenario: the order is own serve, then Edge, then market
    Given this machine does not serve "wave-nano-1.45b"
    And an Edge member this machine belongs to serves it
    When the owner runs "roger use wave-nano-1.45b"
    Then the Edge member is chosen before any market station
    And the market is asked only when neither this machine nor the Edge serves the name

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
