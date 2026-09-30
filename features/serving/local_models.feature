# ROGER SERVES THE MODEL ITSELF: browse, fit, fetch, serve - with nothing else installed.
#
# STATUS: PROPOSED, awaiting founder approval. NOT IMPLEMENTED.
#
# WHY THIS EXISTS
#
# FOUNDER DIRECTION 2026-09-29: "lets say i don't have vllm or llama.cpp or osaurus etc, but i
# downloaded some models from huggingface locally, or even the tui provides an option to download
# more models from hugging face that are adequate for the system, similarly to how we do in ios we
# can filter and browse and so on then serve them directly ... this will come more in hand when we
# are working with the edge devices, lets say we have roger installed on jetson nano or nx and we
# want it to serve a different model, we should be able to support that entire flow and switch
# remotely through the edge".
#
# THE GAP, EXACTLY. features/edge/local_inference.feature calls rung 1 of the ladder "this
# instance's own loaded model". It is not. harness.LocalCompleter posts to a chat-completions URL
# that internal/detect FOUND - vLLM, llama.cpp, Ollama, LM Studio, Osaurus. Roger has never loaded
# a model in its life. On a machine with none of those installed, rung 1 is empty, rung 2 needs a
# peer that is not there, and an airgapped box can do nothing at all. The iOS app does not have
# this problem: it browses, filters, downloads and serves on the device. The terminal is behind its
# own phone.
#
# WHAT THIS ADDS. Roger gains the whole flow: SEE what it could run, KNOW what fits this machine,
# FETCH it, SERVE it, and be told to switch by its owner from across the Edge. The user installs
# nothing.
#
# =============================================================================================
# THE ARCHITECTURE RULING THIS SPEC NEEDS (the one question that must be answered first)
# =============================================================================================
#
# "Serve it directly" can mean three very different things and they are not close:
#
#   (a) WRITE THE INFERENCE. Implement transformer inference in Go. Months of work to be slower
#       than llama.cpp at every step, then a permanent obligation to keep up with every new
#       architecture. No.
#   (b) BIND llama.cpp INTO ROGER with cgo. One binary, no child process - and it ends our
#       CGO_ENABLED=0 static builds, multiplies the release matrix by every CUDA/Metal/CPU
#       variant, and puts a C++ build toolchain in the path of every contributor.
#   (c) ROGER OWNS A RUNTIME IT DID NOT WRITE. It fetches a prebuilt inference server for this
#       machine, verifies it, and supervises it as a child process the owner never has to know
#       about. Roger keeps the parts that are ours: the catalogue, the fitness rule, the download,
#       the lifecycle, the Edge integration, the screen.
#
# THIS SPEC IS WRITTEN FOR (c), and every scenario below assumes it. The reasoning: roger is a
# static Go binary that ships to five platforms through one GoReleaser run, and (b) would end that
# on day one. (c) also degrades honestly - on a machine where no runtime can be had, roger says so
# instead of failing to build. If the founder rules for (b), most of this spec survives unchanged
# and only "THE RUNTIME" below is rewritten.
#
# =============================================================================================
# THE RULES THIS WHOLE FILE OBEYS
# =============================================================================================
#
# 1. NOTHING IS FETCHED WITHOUT BEING ASKED. Not a model, not a runtime, not a catalogue refresh.
#    An Edge that is airgapped by choice must never reach for the network because a screen opened.
#
# 2. A RUNTIME IS CODE. A model file is data that a runtime interprets; the runtime is a program
#    this machine will execute. They get different treatment and this spec never blurs them: the
#    runtime is fetched only from a pinned source, verified against a pinned hash BEFORE it is run,
#    and never taken from a URL that arrived over the wire.
#
# 3. FIT IS ARITHMETIC, AND IT IS SHOWN. "Adequate for this system" is a calculation over facts
#    this machine measured - total RAM, VRAM, free disk, the file's own size and quantisation - and
#    the owner can see the sum. A model that will not fit is drawn with the reason, never hidden:
#    hiding it is how an owner concludes the feature is broken.
#
# 4. IT NEVER CLAIMS WHAT IT HAS NOT MEASURED. A machine whose VRAM could not be read does not get
#    a guess. It gets "cannot tell", which is a different answer from "will not run".
#
# 5. THE DISK IS NOT OURS TO FILL. A download that would not leave room is refused before it
#    starts, not discovered at 94%.
#
# 6. WHAT IS RUNNING IS SAYABLE. At any moment a node can say which model it serves, from which
#    file, at which quantisation, with which hash. "qwen3-8b" is not an answer; two people running
#    "qwen3-8b" may be running very different weights (MODEL-VARIANTS-DESIGN-2026-08-22).
#
# GROUND TRUTH: internal/detect/localhw.go (ParseNvidiaGPUs, ParseROCmVRAMMiB, ParseMemTotalMiB,
# ParseSysctlBytesMiB - the measurements the fitness rule reads), internal/detect/gguf.go (the
# header reader that already names a quant and who made it), internal/detect/detect.go (how an
# EXISTING server is found - this feature is what happens when it finds none),
# internal/harness/local.go (LocalCompleter - what a served model is finally used by),
# features/edge/local_inference.feature (the ladder this feeds rung 1 of),
# features/edge/serving_probe.feature (how a served band is PROVEN rather than claimed).
#
# Tags: @catalogue (the list and the filter), @fit (the arithmetic), @fetch (downloads),
# @runtime (the thing that executes a model), @serve (putting one on air), @cli, @tui.

Feature: Roger can browse, fetch and serve a local model itself, with no other inference software installed

  # =========================================================================
  # 1. THE MACHINE WITH NOTHING ON IT
  # =========================================================================

  @cli
  Scenario: a machine with no inference server says what it can do, not just what it lacks
    Given a machine with no vLLM, llama.cpp, Ollama, LM Studio or Osaurus running
    When the owner asks what roger can serve
    Then it says no inference server was found
    And it offers to serve a model itself, naming the command
    And it does not pretend a model is loaded

  @cli
  Scenario: a model already downloaded by hand is found and offered
    Given the owner has a GGUF file under their models directory that roger did not fetch
    When roger lists what it could serve
    Then that file is listed, with the name, quantisation and size read from the file itself
    And it is marked as one the owner provided, not one roger fetched
    And roger can serve it without downloading anything

  # =========================================================================
  # 2. BROWSING - what could this machine run
  # =========================================================================

  @catalogue
  Scenario: the catalogue lists models with the facts a decision needs
    When the owner browses the models
    Then each one names its parameters, its quantisation, its file size and its context length
    And each says whether it fits THIS machine, with the reason
    And the list is ordered with what fits first

  @catalogue
  Scenario: browsing filters by what the owner is looking for
    When the owner filters by name, by size, or by what fits
    Then only the matching models are drawn
    And the count says how many of how many are shown

  @catalogue
  Scenario: the catalogue is a cache, so an airgapped machine still browses
    Given a machine with no internet and a catalogue fetched earlier
    When the owner browses the models
    Then the cached catalogue is drawn, with its age
    And nothing is fetched
    And a model already on disk is still marked as ready to serve

  @catalogue
  Scenario: a catalogue that has never been fetched says so rather than showing an empty list
    Given a machine that has never fetched the catalogue and has no internet
    When the owner browses the models
    Then it says the catalogue has never been fetched and what would fetch it
    And any model already on disk is still listed, because that needs no catalogue

  @catalogue
  Scenario: the catalogue refresh is an action, never a side effect
    Given a machine with a stale catalogue
    When the owner opens the browser
    Then nothing is fetched
    When the owner asks to refresh
    Then the catalogue is fetched, and the new age is shown

  # =========================================================================
  # 3. FIT - the arithmetic, shown
  # =========================================================================

  @fit
  Scenario: fit is computed from what this machine measured
    Given a machine with a known total RAM, VRAM and free disk
    When a model's fit is judged
    Then the judgement uses the model's file size, its quantisation and its context length
    And it accounts for the KV cache at the context length it would run
    And it accounts for the runtime's own overhead
    And the owner can see the sum, not only the verdict

  @fit
  Scenario Outline: the verdict is one of four, and each means something different
    Given a machine and a model whose <situation>
    Then the verdict is <verdict>
    And the reason says <reason>

    Examples:
      | situation                                | verdict    | reason                                    |
      | weights and cache fit in VRAM            | runs fast  | it fits on the GPU                        |
      | weights fit in RAM but not VRAM          | runs       | it runs on the CPU, slower                |
      | weights exceed RAM                       | too big    | how much it needs against how much exists |
      | VRAM could not be read                   | cannot say | what could not be measured                |

  @fit
  Scenario: a machine that cannot be measured is never told a comforting lie
    Given a machine whose total RAM could not be read
    When a model's fit is judged
    Then the verdict is that it cannot say
    And it names the measurement it is missing
    And the model can still be fetched and tried, on the owner's word

  @fit
  Scenario: the disk is checked before a fetch, with a margin
    Given a machine with less free disk than a model needs plus a margin
    When the owner asks to fetch it
    Then it is refused before anything is downloaded
    And the refusal names the space needed and the space free

  # =========================================================================
  # 4. FETCHING
  # =========================================================================

  @fetch
  Scenario: a fetch shows progress and can be stopped
    When the owner fetches a model
    Then the progress shows the bytes, the rate and the time remaining
    And the owner can cancel it
    And a cancelled fetch leaves no half file that could be served

  @fetch
  Scenario: an interrupted fetch resumes rather than starting again
    Given a fetch that was interrupted with most of the file downloaded
    When the owner fetches it again
    Then it resumes from where it stopped
    And the finished file is identical to one fetched in a single run

  @fetch
  Scenario: a fetched file is verified before it is ever served
    Given a model whose index entry carries a size and a hash
    When the fetch completes
    Then the file is checked against both
    And a file that does not match is not served, and says why
    And a partial file is never served under any circumstances

  @fetch
  Scenario: a fetch is refused when the machine is not the owner's to fill
    Given a model larger than the free disk allows
    Then the fetch is refused with the arithmetic
    And nothing was written

  # =========================================================================
  # 5. THE RUNTIME - the part that is code, not data
  # =========================================================================

  @runtime
  Scenario: the runtime is fetched once, verified, and only then executed
    Given a machine with no runtime yet
    When the owner serves a model for the first time
    Then roger says it needs a runtime and what it is
    And the runtime is fetched from the pinned source for this platform
    And its hash is checked against the pinned hash BEFORE it is executed
    And a hash that does not match means it is never executed, and the owner is told

  @runtime
  Scenario: the runtime is never taken from somewhere that arrived over the wire
    Given an instruction that names a runtime URL
    Then it is refused
    And the refusal says a runtime comes from the pinned source alone

  @runtime
  Scenario: a platform with no runtime available says so plainly
    Given a machine whose platform has no prebuilt runtime
    When the owner tries to serve a model
    Then it says this platform cannot serve locally yet, and names the platform
    And it does not fall back to something that will not work

  @runtime
  Scenario: the runtime is roger's child, and dies with it
    Given roger is serving a model
    When roger exits
    Then the runtime is stopped
    And no orphaned server is left holding the port or the memory

  # =========================================================================
  # 6. SERVING
  # =========================================================================

  @serve
  Scenario: serving a model makes it a band on the Edge, proven rather than claimed
    Given a fetched model
    When the owner serves it
    Then roger starts the runtime and waits until it answers
    And the band it serves is advertised on this node
    And the serving probe proves it before anything routes to it
    And a runtime that never answers is reported as failed to start, never as serving

  @serve
  Scenario: what is being served is sayable in full
    Given roger is serving a model
    When the owner asks what it is serving
    Then it names the model, the quantisation, the file and its hash
    And it says which runtime is running it, and for how long

  @serve
  Scenario: stopping gives the memory back
    Given roger is serving a model
    When the owner stops it
    Then the runtime is stopped and the memory released
    And the band is withdrawn from this node
    And the Edge stops routing to it

  @serve
  Scenario: switching models is one action, not a stop and a start the owner must sequence
    Given roger is serving one model
    When the owner serves a different one
    Then the new one is fetched if needed, started, and proven
    And only then is the old one stopped
    And if the new one fails to start, the old one is still serving

  @serve
  Scenario: a served model is what rung 1 of the ladder finally means
    Given roger is serving a model
    When a turn is dispatched on this machine
    Then it goes to the local model, with no broker and no peer
    And the receipt says local, at zero cost

  # =========================================================================
  # 7. THE TERMINAL
  # =========================================================================

  @tui
  Scenario: the browser is a screen, not a wall of text
    When the owner opens the model browser
    Then each model is a row with its facts and its fit verdict
    And what fits is distinguishable from what does not WITHOUT colour
    And the selected row's full detail is shown beside or below the list

  @tui
  Scenario: a fetch in progress is visible and alive
    Given a fetch is running
    Then the screen shows the bytes, the rate, the remaining time and a progress bar
    And the bar moves as the fetch moves, and stops when it stops
    And the owner can leave the screen and come back to it still running
    And nothing animates when there is nothing happening

  @tui
  Scenario: the screen degrades honestly
    When the browser is drawn at 80 columns and under NO_COLOR
    Then every fact still reads, in text alone
    And nothing overflows the width
