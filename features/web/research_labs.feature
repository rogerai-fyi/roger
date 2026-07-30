Feature: RogerAI Research presents real model science without confusing it with the broker directory
  RogerAI needs a durable public research surface that distinguishes its own
  training, adaptations, optimizations, and verification work. Every statement
  must be supported by a released artifact or an explicitly labeled active
  project, and local model use must not be represented as broker-mandatory.

  Background:
    Given a visitor opens the RogerAI marketing website

  Scenario: Research is a first-class destination
    Then the primary marketing navigation includes "Research"
    And the link resolves to "/research.html"
    And the page identifies the research organization as "RogerAI Labs"

  Scenario: The live model directory remains distinct
    Given the visitor reads the Research page
    Then the page explains that "/models.html" lists live broker stations
    And the Research page lists RogerAI research artifacts and projects
    And it does not imply every research artifact is currently served
    And it does not imply every broker station is a RogerAI model

  Scenario Outline: Every artifact has honest lineage
    Given a research artifact was <work>
    Then its visible lineage badge is "<badge>"
    And its upstream parent and license are shown when an upstream parent exists

    Examples:
      | work                                                   | badge                  |
      | pretrained by RogerAI from an initialized architecture | Trained by RogerAI     |
      | continually pretrained or post-trained by RogerAI      | Adapted by RogerAI     |
      | pruned, quantized, converted, or runtime-modified       | Optimized by RogerAI   |
      | tested unchanged in the RogerAI harness                 | Verified by RogerAI    |

  Scenario: DeepSeek MTP is not presented as a Roger-trained model
    Given the DeepSeek-V4-Flash MTP project is shown
    Then it is labeled "Optimized by RogerAI"
    And the upstream DeepSeek model is named
    And the page describes the converter, runtime, MTP, benchmark, and artifact work
    And the page does not say RogerAI pretrained DeepSeek

  Scenario: Kimi-K3 REAP is not presented as a Roger-trained model
    Given the Kimi-K3 256 GB project is shown
    Then it is labeled "Optimized by RogerAI"
    And the upstream Kimi model is named
    And estimates are visually distinguished from measured results
    And unresolved A_log and quality gates remain visible until resolved
    And the page does not say RogerAI pretrained Kimi

  Scenario: An unreleased Wave model is presented as a project
    Given no Wave checkpoint has passed release gates
    Then Wave Nano is labeled "Research" or "In progress"
    And no download count, benchmark victory, or device compatibility is invented
    And future modalities are shown as roadmap items rather than available models

  Scenario: A released model card exposes its artifact contract
    Given a RogerAI model has passed release gates
    Then its card shows model ID and version
    And its card shows total and active parameters when applicable
    And its card shows lineage and parent
    And its card shows license
    And its card shows available formats
    And its card shows tested hardware and peak memory
    And its card links weights, source, recipe, raw evaluations, and limitations
    And its card shows stable, release-candidate, research, deprecated, or superseded status

  Scenario: Open-source claims are component-specific
    Then the page does not call a restricted component open source
    And the page does not call the PolyForm broker open source
    And an open-weight-only model is labeled "open weights"
    And "Open Source AI" is used only when the release satisfies the published RogerAI reproducibility checklist

  Scenario: Local use is not conditioned on RogerAI
    Then the page says users may download and run qualifying open models locally
    And publishing to the RogerAI broker is described as optional
    And the broker value proposition is discovery, routing, payments, failover, and signed receipts
    And no model license claim says remote inference must traverse RogerAI

  Scenario: Device claims are exact
    Given a model is described as running on a device
    Then the page identifies the tested device class
    And the page identifies the runtime and model format
    And the page identifies the precision or quantization
    And the page links the measurement record
    And generic NPU support is not claimed from CPU or GPU evidence

  Scenario: Raspberry Pi support identifies the tested operating envelope
    Given a model is described as supporting Raspberry Pi
    Then the page identifies the exact Pi model and RAM
    And the page identifies OS, runtime, artifact, quantization, and context
    And the page reports cold load, peak memory, prompt speed, and decode speed
    And the page does not generalize one Pi result to every ARM device

  Scenario: ESP32 is not represented as running Wave Nano
    Given the visitor reads the embedded device support
    Then the page does not say an ESP32 runs a 350M Roger Wave model
    And ESP32 artifacts are labeled task-specific Roger Edge models
    And supported local tasks may include wake, VAD, fixed commands, sensing, or vision
    And general speech recognition or open-ended generation is not implied

  Scenario: ESP32 escalation preserves local control
    Given a Roger Edge device escalates a request
    Then local sensing and safe allowlisted commands remain available offline
    And network transmission occurs only under the configured consent policy
    And a nearby trusted Pi or phone may be preferred before internet routing
    And remote results are structured data rather than arbitrary executable code
    And the local device validates action, arguments, nonce, expiry, and issuer

  Scenario: ESP32-P4 wireless requirements are explicit
    Given a Roger Edge Vision P4 artifact uses wireless networking
    Then the page identifies the companion connectivity chip or board
    And it does not imply ESP32-P4 includes native Wi-Fi or Bluetooth

  Scenario: NPU support proves delegation
    Given an artifact claims NPU acceleration
    Then the page identifies the exact SoC and accelerator
    And the artifact was compiled for that backend
    And the evidence reports full delegation, partial delegation, and CPU fallback
    And a successful model load alone is not presented as NPU acceleration

  Scenario: Benchmarks retain their provenance
    Given a benchmark result is displayed
    Then it identifies the artifact version
    And it identifies the evaluation harness
    And it distinguishes RogerAI measurements from upstream vendor measurements
    And it links raw results
    And it does not claim superiority from a different harness

  Scenario: Negative and superseded results remain discoverable
    Given a project has a failed hypothesis or superseded artifact
    Then the project page retains the result
    And it identifies why the result failed or was superseded
    And the model catalog may mark it deprecated without deleting its evidence

  Scenario: Team language is factual and approved
    Then the page may describe computer-science and systems-engineering experience
    And named biographies and employer details appear only after founder approval
    And the page does not inflate degrees, affiliations, publications, or employment

  Scenario: The page works without client-side JavaScript
    When client-side JavaScript is unavailable
    Then the research thesis, projects, lineage, licenses, statuses, and primary links remain readable

  Scenario: Research has a useful empty state
    Given the dynamic research manifest cannot be loaded
    Then the page retains its static research thesis and project summaries
    And no fake metric or placeholder artifact is displayed as live data
    And source and Broadcast links remain available

  Scenario: Research navigation is accessible on narrow screens
    Given the viewport uses the mobile navigation
    Then "Research" remains keyboard and touch accessible
    And the current-page state is announced
    And no artifact data requires pointer hover

  Scenario: Search and sharing describe the page accurately
    Then the document title identifies RogerAI Research
    And the meta description mentions open edge-model and inference research
    And canonical and social metadata resolve to the Research page
    And the metadata does not claim an unreleased model is available

  Scenario: The Wave family is presented as programs, not products
    Given no Wave checkpoint has passed release gates
    Then Wave Nano and Wave Micro are labeled programs in design or pending bake-off
    And Wave Tool, Wave Vision, and Wave Audio are shown as roadmap tiers gated on Nano
    And no Wave tier claims a release date, download, benchmark victory, or device support
    And microcontroller-class artifacts remain Roger Edge task models below the Wave line

  Scenario: Industry use cases are deployment patterns, not case studies
    Given the visitor reads the industry section
    Then manufacturing, warehouses and logistics, energy and heavy assets, and defense and public sector are described
    And each use case describes on-premises or air-gapped local inference
    And the page states these are patterns and does not name or imply customers RogerAI does not have
    And advisory models are kept out of real-time closed-loop control

  Scenario: Services are offered without conditioning model use on them
    Given the visitor reads the services section
    Then optimization engineering, edge deployment, benchmarking, and industrial pilots are offered
    And the page states model use never requires buying services
    And services never require the broker
    And a contact channel is provided

  Scenario: Developers get standard formats and reproduction paths
    Given the visitor reads the developers section
    Then artifacts are described as GGUF served by llama.cpp
    And weights link to the RogerAI Hugging Face organization
    And source links to the RogerAI GitHub organization
    And published numbers are described as reproducible with the serve command and settings
    And network publishing remains optional
