# ROGER SERVES A MODEL ITSELF - THE PART THE APPROVED CATALOGUE SPEC DOES NOT COVER.
#
# STATUS: APPROVED by the founder 2026-09-30, including the ruling below; §8 (console) 2026-10-01.
# BUILD STATUS (2026-10-01, wt/edge-limoni): PARTIAL. BUILT: the index (search, files, gated, quant
# choice, licence shown + recorded), the arithmetic (named context, the sum, four verdicts, refusal
# with the context that fits), the runtime (pinned manifest, digest before run, measured glibc floor,
# platforms with no runtime say so, loopback + key file, cache/slot flags, cleanup), serving (switch
# proves the new before stopping the old, said in full, the band detection already routes on), removal,
# the TUI screen (§7) and the console (§8). NOT BUILT: "fitting and being usable" (no speed estimate),
# "the runtime's own fitness answer wins once the file is on disk" (our estimate is still used),
# "two at once", an idle timeout (nothing unloads by surprise holds trivially), a cache budget, and
# "the terminal and the phone agree" (same formula, not cross-checked against iOS).
#
# READ features/share/model_catalog.feature FIRST. That spec is APPROVED and it already owns most
# of this ground: one list of detected and offerable models, fit assessed against real detected
# memory, consented and resumable and checksum-verified acquisition, a supervised runtime that is
# probed before it is announced and stopped when sharing stops, and honest degradation when the
# catalogue cannot be fetched. `internal/catalog` is slice 1 of it and is already written -
# Entry, Validate, AssessFit, Fit, Merge - with no importer yet, by design. None of that is
# repeated here.
#
# THIS FILE IS THE DELTA between that spec and what the founder asked for on 2026-09-29:
#
#   "lets say i don't have vllm or llama.cpp or osaurus etc, but i downloaded some models from
#    huggingface locally, or even the tui provides an option to download more models from hugging
#    face that are adequate for the system, similarly to how we do in ios we can filter and browse
#    and so on then serve them directly ... this will come more in hand when we are working with
#    the edge devices."
#
# =============================================================================================
# THE SCOPE CONFLICT THE FOUNDER MUST RULE ON (one sentence, one decision)
# =============================================================================================
#
# features/share/model_catalog.feature puts this OUT OF SCOPE, in as many words:
#
#     "acquiring or launching arbitrary third-party models the catalogue does not describe
#      (an operator can still point --upstream at anything)"
#
# The new direction asks for exactly that: browse HuggingFace, filter it, fetch from it. The two
# cannot both stand. RULED 2026-09-30: YES, roger acquires models it did not publish a manifest
# for. The older exclusion in model_catalog.feature is superseded for that clause alone; every
# other line of that spec stands. What the answer still requires:
#
#   - OUR CATALOGUE AND THE WIDE WORLD ARE DRAWN DIFFERENTLY. An entry we publish carries a
#     manifest we signed: a hash, a licence, a measured serving memory. A repo from HuggingFace
#     carries what its author happened to upload. Those are not the same evidence and the screen
#     must not pretend they are.
#   - A THIRD-PARTY FETCH IS THE OWNER'S DECISION, made once per model, on a screen that says
#     whose word it is taking.
#
# AND A SECOND RULING, SAME DAY: "lets not block our behavior based on licenses, we care about
# functionality". So nothing here refuses to fetch, serve or broadcast because of what a weight
# licence says. That is the owner's call to make and the owner's risk to carry, and roger is not
# the referee.
#
# The licence is still READ AND SHOWN, because that costs nothing and blocks nothing: an owner who
# wants to know what terms came with their weights can see them, and the answer is recorded
# against the file so the question can be asked later. Displaying a fact is not gating on it.
#
# ONE THING THAT LOOKS LIKE LICENCE GATING AND IS NOT. A GATED HuggingFace repo answers a download
# with 401 and will not serve the bytes to anybody without an accepted access request and a token.
# That is the far end refusing, not roger refusing, and detecting it early is the difference
# between a clear message and a failed download. It stays.
#
# =============================================================================================
# WHAT ELSE IS MISSING FROM THE APPROVED SPEC, AND WHY EACH MATTERS
# =============================================================================================
#
# A. WHERE DOES THE RUNTIME COME FROM? model_catalog names `internal/runtime` (serve + health) and
#    says a runtime is supervised - but never says where the PROGRAM comes from on a machine that
#    has none. That is the whole of the founder's "i don't have vllm or llama.cpp". A model file is
#    data; a runtime is code this machine will execute, and the two get different treatment.
# B. THE ARITHMETIC. "Fit is assessed against real detected memory" is the right rule with no
#    numbers behind it. The iOS app has the numbers, calibrated against real crashes, and a
#    terminal that guesses differently will disagree with the phone about the same model.
# C. GATED MODELS, QUANTISATION CHOICE, CONTEXT LENGTH, TWO AT ONCE, IDLE UNLOAD, AND THE BAND
#    NAME. Each is something an owner meets in the first hour.
# D. THE SCREEN. model_catalog integrates with SHARE. The founder asked for a browser with filters
#    and "beautiful animated visuals", which is a screen of its own.
#
# WHAT IOS ALREADY PROVES, AND WHERE THE TERMINAL MUST DIFFER. The iOS app does this end to end on
# MLX, an Apple-only runtime, so the terminal cannot reuse the engine - a Pi and a Jetson need
# GGUF. What it CAN reuse is the arithmetic, which was calibrated against real out-of-memory kills
# rather than guessed, and the honesty of its fit line. Its own worst gaps are named below and
# this spec fixes them rather than inheriting them: fraction-only progress with no bytes or rate,
# NO integrity check at all even though the expected byte count was already fetched, one generic
# failure string for every error, a dead end on gated repos with nowhere to put a token, and no
# filter for "what actually fits".
#
# =============================================================================================
# THE ARCHITECTURE QUESTION IS ANSWERED, WITH EVIDENCE (researched 2026-09-30)
# =============================================================================================
#
# The runtime is llama.cpp's `llama-server`, fetched and supervised as a child process. Not
# written by us, not linked into us. The evidence, each item checked rather than recalled:
#
#   - OLLAMA ALREADY MADE THIS EXACT MOVE. The most heavily invested Go inference project there
#     is used to bind llama.cpp through cgo and no longer does: its tree now carries
#     llm/llama_binary.go and llm/llama_server.go, whose header says "wraps the llama-server
#     binary as a subprocess". When the project with the most reason to keep cgo abandons it,
#     that is the strongest available signal about where the maintenance cost really lives.
#   - CGO WOULD COST US THE PRODUCT'S SHAPE. All nine GoReleaser targets are CGO_ENABLED=0 today
#     and macOS notarisation happens on the Linux runner. cgo means nine toolchains, a libc
#     floor on a binary that is currently one static file, and roger growing from 28 MB to about
#     50 MB for CPU-only - before CUDA, whose runtime alone is a 440 MB download.
#   - WRITING IT IS NOT A PROJECT, IT IS A TREADMILL. llama.cpp ships around twenty tagged builds
#     A DAY, fourteen x86 CPU variants and seven arm variants as separate objects in one release,
#     and adds whole model architectures in single releases. The pure-Go alternatives are all
#     one-or-two-person repos younger than four months.
#   - THE LICENCE IS CLEAN. llama.cpp and ggml are MIT. Nothing GPL, nothing awkward beside
#     PolyForm, and we fetch at runtime rather than redistributing.
#
# AND IT TURNS OUT THE RUNTIME ALREADY DOES OUR HARDEST SUM. llama-server has a fitness pass of
# its own, on by default: `-fit`, `--fit-target` (1024 MiB headroom), `--fit-ctx`, `-ngl auto`,
# and a standalone `llama-fit-params` that prints the settings it would choose. So the rule below
# is a PRE-DOWNLOAD estimate whose only job is to never say "fits" when it does not; once the
# file is on disk the runtime's own answer is authoritative and we take it rather than
# re-deriving one. That is a large piece of code we now do not write.
#
# THREE FACTS THAT CHANGE WHAT THE SPEC MUST SAY:
#
#   1. THE CONTEXT KILLS EDGE BOXES, NOT THE WEIGHTS. Qwen3-4B-Q4_K_M is a 2.33 GiB file with
#      5.63 GiB of KV cache at its full 40960 context. A check that asks "does the 2.5 GB file
#      fit in 8 GB?" says yes and the machine then dies. Any verdict that does not name the
#      context it was computed at is not a verdict.
#   2. "IT FITS" AND "IT IS USABLE" ARE DIFFERENT CLAIMS. CPU decoding is bandwidth-bound; a
#      Pi 4 has roughly 4-6 GB/s, so a 2.3 GB model is about 2 tokens a second at best. Roger
#      should make both claims, separately, and let the owner decide.
#   3. SOME PLATFORMS SIMPLY CANNOT. There is no llama.cpp build for 32-bit arm anywhere, so a
#      Pi Zero 2 or a 32-bit Pi OS can relay and control and never serve. The official arm64
#      build needs GLIBC 2.38 and Raspberry Pi OS Bookworm has 2.36, so today's most common Pi
#      install cannot load it either. An original Jetson Nano is below every prebuilt binary in
#      existence. Each of those is a sentence roger must be able to say plainly, which is what
#      the "a platform with no runtime says so" scenario below is for.
#
# =============================================================================================
# WHAT IS ALREADY BUILT, so none of it is written twice (surveyed 2026-09-29)
# =============================================================================================
#
# THE DATA HALF OF THE CATALOGUE. `internal/catalog` has Entry{ID, Repo, File, Bytes, SHA256,
# ServeMem, License, Runtime, Parent}, Validate (every consent-bearing field mandatory, Repo must
# be an absolute URL), AssessFit with tightFraction 0.8, and - the useful surprise - Merge, which
# presents detected and offerable models as ONE list with detected first. That is the screen's
# data model, already written and already tested, with zero importers.
#
# THE SUPERVISOR. roger already spawns and reaps a long-running detached child correctly, and the
# hard parts are the ones you only learn by getting them wrong: `edgeSpawnRoger`
# (cmd/rogerai/edgeagentcmd.go) sanitises the environment so the child does not inherit a pinned
# port and die instantly, detaches through a per-platform seam, and keeps a goroutine on Wait so
# the child is not a zombie for the parent's life. `cmd/rogerai/edgestop.go` is the other half:
# SIGTERM and never SIGKILL, a bounded wait, an honest report if it had not gone, and a pid-safety
# check that returns "cannot tell" on a platform where it cannot read the process, rather than
# guessing yes. An inference runtime is another long-running child. It reuses all of this.
#
# THE MEASUREMENTS. `detect.LocalHW` already carries VRAMTotalMiB, RAMTotalMiB, DiskFreeMiB and
# CPUCores, each with a Known flag so a zero is never confused with an unread value, and SetGPUs
# marks the VRAM total unknown if ANY GPU did not report rather than under-reporting.
# `internal/detect/preflight.go` already states bars with their reasoning, including "the smallest
# pool that holds a 7-8B model at 4-bit with a KV cache".
#
# WHAT IS MISSING FROM THE MEASUREMENTS, and each one changes an answer:
#   - FREE VRAM. Only the total is read. A model fits a 24 GiB card that already has 20 GiB
#     resident, and we would say yes.
#   - PER-GPU. VRAMTotalMiB is a SUM. 20 GiB does not fit on two 12 GiB cards without a
#     tensor-parallel the runtime may not have.
#   - THE COMPUTE CAPABILITY. No CUDA arch, no Metal family, no AVX512 or NEON. That is precisely
#     what decides WHICH prebuilt runtime binary to fetch, so it is a blocker for section 3.
#   - THE DISK PATH IS THE HOME DIRECTORY, not wherever weights would live. Free space on the
#     wrong filesystem is not an answer.
#   - NO APPLE METAL CEILING. Unified memory is checked against total system RAM; macOS reserves a
#     fraction and iogpu.wired_limit_mb caps what Metal can actually address.
#
# THE SCREEN'S IDIOMS. A browse screen should reuse rather than invent: the SHARE table's shape
# and selected-row detail banner (internal/tui/view_share.go), the voice picker's heading that
# states provenance and count ("N voices (local)" becomes "N models (cached, 3d old)"), the band
# browser's typed live filter over a derived visible slice with a virtualised window, and the
# explicit y/N gate before a costly irreversible action. None of these need designing.
#
# GROUND TRUTH: features/share/model_catalog.feature (APPROVED, the base), internal/catalog
# (slice 1, written, no importer), features/share/hosting_compatibility.feature (the boundary
# model_catalog deliberately moved), internal/detect/localhw.go (ParseNvidiaGPUs,
# ParseROCmVRAMMiB, ParseMemTotalMiB, ParseSysctlBytesMiB - the measurements), internal/detect/
# gguf.go (the header reader: quant, who quantised it, finetune), internal/harness/local.go
# (LocalCompleter - what finally uses a served model), features/edge/serving_probe.feature (how a
# band is proven), features/edge/remote_model.feature (switching a member's model from afar).
#
# WHAT STILL NEEDS HARDWARE TO SETTLE, recorded so nobody assumes it was checked:
#   - whether the llama-server inside ollama's JetPack 6 tarball runs standalone, separated from
#     their daemon (the only prebuilt Jetson-CUDA server that exists);
#   - whether upstream's CUDA arm64 build actually loads and JITs on a JetPack 7 Orin;
#   - the compute-buffer term, which has no clean closed form and is currently a budgeted
#     approximation that stage 2 corrects;
#   - MLX against llama.cpp Metal on Apple Silicon, which decides whether the Mac ever needs a
#     second runtime;
#   - whether HuggingFace's model-search endpoint stays as it is, since it is absent from their
#     published API spec. A contract test in CI, not an assumption.
#
# Tags: @hf (the wide world), @runtime (the program, not the weights), @fit (the arithmetic),
# @serve, @cli, @tui.

Feature: Roger can find, judge, fetch and serve a model from the wider world, on a machine with no inference software at all

  # =========================================================================
  # 1. THE WIDE WORLD - only if the founder rules yes
  # =========================================================================

  @hf
  Scenario: our own catalogue and a third-party repo are never drawn as equal evidence
    Given a model from RogerAI's published catalogue and a model from HuggingFace
    When both are listed
    Then ours shows the hash, the licence and the serving memory we measured
    And the third-party one shows what its author published, marked as their word not ours
    And the two are distinguishable without colour

  @hf
  Scenario: searching the wider world is an action, never something a screen does on opening
    When the owner opens the browser
    Then only the cached catalogue and what is already on disk are shown
    When the owner searches
    Then the search is sent, and the results are marked as coming from outside our catalogue

  @hf
  Scenario: a repo that cannot be served is rejected with the reason, before any bytes move
    Given a search result with no servable weights, or an architecture this runtime cannot load
    Then it is listed as unservable with the specific reason
    And the fetch action is not offered for it
    And the reason is the actionable one first, when there are several

  @hf
  Scenario: a model that needs a licence accepted says so, and a token is used only for that
    Given a gated repo
    When the owner asks to fetch it
    Then it is refused before any bytes move, naming the gate and what to do
    And a token the owner configured is used if there is one
    And browsing never requires a token, only fetching what is gated

  # =========================================================================
  # 2. ONE REPO IS NOT ONE MODEL
  # =========================================================================

  @hf @fit
  Scenario: several quantisations of one model do not fit alike
    Given a repo published in several quantisations
    Then each is listed with its own size and its own fit verdict
    And the one that fits best on THIS machine is suggested
    And the owner may choose another anyway
    And what is finally served records which one it was

  # =========================================================================
  # 3. THE RUNTIME - the part that is code
  #
  # The runtime tracks a STABLE channel, never the nightly firehose upstream publishes about
  # twenty times a day. A pinned tag is resolved into our own manifest, and the manifest is what
  # ships.
  # =========================================================================

  @runtime
  Scenario: the runtime is fetched once, verified, and only then executed
    Given a machine with no inference runtime at all
    When the owner serves a model for the first time
    Then roger says what runtime it needs and that it will fetch it
    And it is fetched from the pinned source for this platform
    And its hash is checked against the pinned hash BEFORE it is executed
    And a hash that does not match is never executed, and the owner is told

  @runtime
  Scenario: a runtime is never taken from an address that arrived from outside
    Given any input naming a runtime URL, path or command
    Then it is refused
    And the refusal says a runtime comes from the pinned source alone
    And this holds for a catalogue entry, a search result and an Edge instruction alike

  @runtime
  Scenario Outline: a platform that cannot serve says so plainly, and says what it CAN do
    Given a machine that is <machine>
    When the owner tries to serve a model
    Then it says local serving is not available there, and why in one sentence
    And it says what that machine can still do on the Edge
    And it does not offer a fetch that cannot end in serving

    Examples:
      | machine                                        |
      | a 32-bit arm board, for which no build exists  |
      | an older OS whose system libraries are too old |
      | a board below every published build            |

  @runtime
  Scenario: the supervisor sets the flags a small machine cannot survive without
    Given a machine with little memory
    When roger starts the runtime
    Then it sets the prompt-cache size explicitly rather than taking a default sized for a server
    And it pins the parallel slot count rather than letting it be chosen for a larger machine
    And it binds to loopback or a socket, with a key, and does not expose a second web UI

  @runtime
  Scenario: a runtime roger started is roger's to clean up
    Given roger started a runtime
    When roger exits, crashes or is killed
    Then no orphaned runtime is left holding the port or the memory
    And a server the operator started themselves is never touched

  @runtime
  Scenario: the runtime we fetch is one we resolved in advance, never a name we built at run time
    Given a platform that needs a runtime
    Then the asset is taken from a manifest shipped inside roger, carrying its tag, its exact
      asset name, its URL, its size and its digest
    And no asset name is ever composed from a template at run time
    And the manifest is updated deliberately, the way roger's own release assets are

  @runtime
  Scenario: an upstream that quietly raises its floor is caught before an owner is
    Given a manifest naming a runtime asset
    Then the build checks each asset against the oldest system libraries we claim to support
    And an asset that exceeds that floor fails the check rather than shipping
    And the declared floor is written down, so raising it is a decision rather than an accident

  @runtime @fit
  Scenario: a board with one pool of memory is measured as one pool
    Given a machine whose CPU and GPU share a single memory pool
    Then the fitness rule treats that pool as the budget for the weights AND the cache together
    And it does not look for a separate video memory figure that does not exist
    And it does not report a figure from a tool that cannot see that hardware

  # =========================================================================
  # 4. THE ARITHMETIC - the numbers behind "fit"
  # =========================================================================

  @fit
  Scenario: the runtime's own fitness answer wins once the file is on disk
    Given a model already downloaded
    When roger judges whether it will run
    Then it asks the runtime, which has a fitness pass of its own
    And it takes that answer rather than re-deriving one
    And our own estimate is used only BEFORE the file exists, where the runtime cannot be asked

  @fit
  Scenario: a verdict that does not name its context is not a verdict
    Given a model whose weights are small and whose KV cache at full context is not
    Then the verdict states the context it was computed at
    And a verdict computed at one context is never shown against another

  @fit
  Scenario: fitting and being usable are two different claims
    Given a model that fits a machine with little memory bandwidth
    Then roger says it fits
    And it separately estimates how fast it would answer
    And it does not let one claim stand in for the other

  @fit
  Scenario: the sum is stated, not just the verdict
    Given a machine with measured memory and a model with a known size and context
    When its fit is judged
    Then the sum accounts for the weights, the KV cache at the context it would run, the
      runtime's working set, and a reserve
    And the owner can see each term, not only the answer

  @fit
  Scenario Outline: four verdicts, and each means something different
    Given a machine where a model's need is <situation>
    Then the verdict is <verdict>

    Examples:
      | situation                              | verdict     |
      | comfortably inside what is free        | fits        |
      | inside what is free but close to it    | tight       |
      | beyond what is free                    | will not fit |
      | not computable, because a reading failed | cannot say |

  @fit
  Scenario: an unmeasurable machine is told the truth, not a comfortable default
    Given a machine whose memory could not be read
    Then the verdict is that it cannot say, naming the missing reading
    And the model may still be fetched on the owner's explicit word

  @fit
  Scenario: the context length is the owner's choice and it changes the sum
    Given a model that fits at a short context and not at a long one
    When the owner sets the context length
    Then the verdict is recomputed for that length
    And serving uses the length they chose
    And a length that cannot be held is refused with the arithmetic, never silently reduced

  @fit
  Scenario: the terminal and the phone agree about the same model
    Given a model judged on iOS and on a terminal with the same usable memory
    Then the two verdicts agree
    And where they cannot, the difference is a measured platform fact, not a different formula

  # =========================================================================
  # 5. SERVING, BEYOND WHAT THE APPROVED SPEC ALREADY SAYS
  # =========================================================================

  @serve
  Scenario: switching models is one action, and the old one survives a failed switch
    Given roger is serving one model
    When the owner serves a different one
    Then the new one is fetched if needed, started and proven
    And only then is the old one stopped
    And a new one that fails to start leaves the old one serving, with the reason reported

  @serve
  Scenario: two at once only when they really fit together
    Given roger is serving a model
    When the owner serves a second on the same machine
    Then it is allowed only if both fit together by the same arithmetic
    And otherwise it offers to switch rather than add
    And a machine serving two says what each of them is

  @serve
  Scenario: nothing is unloaded by surprise
    Given roger is serving a model that has had no turn for a long time
    Then it keeps serving by default, because an Edge device exists to answer
    And an idle timeout may be set, off by default
    And when one is set and fires, the band is withdrawn and the event recorded, so a peer that
      stops finding it can see why

  @serve
  Scenario: what is running is sayable in full
    When the owner asks what is being served
    Then it names the model, the quantisation, the file, its hash and the context length
    And it says which runtime is running it and for how long
    And "the model name" alone is never the whole answer, because two people running one name may
      be running very different weights

  @serve
  Scenario: a locally served model advertises the band a peer already asks for
    Given roger serves a model locally
    Then the band follows the vocabulary the dispatch ladder already routes on
    And the file and quantisation stay visible behind the band
    And the serving probe proves it before anything routes to it

  # =========================================================================
  # 6. THE THINGS THAT BITE LATER
  # =========================================================================

  @fetch
  Scenario: models are gigabytes and they accumulate
    Given an owner who has fetched several models
    Then roger says how much disk they are using and where
    And a model can be removed, and removing it returns it to being merely offerable
    And a cache budget can be set, so a board's card is not filled silently
    And nothing is ever evicted without the owner having asked for a budget

  @serve
  Scenario: "roger says it is serving" must mean the model answers, not that we started something
    Given a runtime that started and then died
    Then the node does not report that band as served
    And the check is the serving probe the fabric already uses, not our own bookkeeping about a
      process we launched

  @hf
  Scenario: the licence is shown and recorded, and it stops nothing
    Given a model whose licence restricts how it may be used
    Then the licence is shown before it is fetched, so the owner sees what came with the weights
    And it is recorded against the model on disk, so the question can be answered later
    And it does not prevent fetching, serving or going on air
    And roger states the terms and leaves the decision where it belongs

  # =========================================================================
  # 7. THE SCREEN
  # =========================================================================

  @tui
  Scenario: the browser answers "what can this machine run" before anything else
    When the owner opens the model browser
    Then the machine's own usable memory is stated at the top
    And each model is a row with its facts and its verdict
    And what fits is distinguishable from what does not WITHOUT colour
    And the list can be filtered to only what fits

  @tui
  Scenario: a fetch is visible, alive, and leaveable
    Given a fetch is running
    Then the screen shows the bytes, the rate and the time remaining, not a bare percentage
    And the progress moves as the fetch moves and stops when it stops
    And the owner can leave the screen and come back to it still running
    And nothing animates when nothing is happening

  @tui
  Scenario: a failure says which failure it was
    Given a fetch that fails
    Then the reason distinguishes no space, no network, refused, gated and corrupt
    And the action that would fix it is named
    And one generic failure string for everything is never enough

  @tui
  Scenario: the screen degrades honestly
    When the browser is drawn at 80 columns and under NO_COLOR
    Then every fact still reads in text alone
    And nothing overflows the width

  # =========================================================================
  # 8. THE BROWSER CONSOLE - APPROVED by the founder 2026-10-01 (placement superseded by §9: inside
  # the console's SHARE panel, not a tab of its own)
  #
  # FOUNDER DIRECTION 2026-10-01: "i want to add the same for the webui so we need to make sure
  # that works from there as well". The console is the same owner on the same machine, so it does
  # exactly what `roger model` does through the same code, and nothing more. Every rule above
  # applies unchanged; these scenarios are only what a browser adds.
  # =========================================================================

  @web
  Scenario: the console's model page answers "what can this machine run" like the terminal does
    When the owner opens the console's model page
    Then it states the machine's usable memory and which pool it is
    And it lists the models on disk with their size, quantisation and whether roger checked them
    And it says what is being served now, in full, or that nothing is

  @web
  Scenario: searching from the console is an action and its results are the authors' word
    When the owner searches from the console
    Then the results are marked as coming from outside RogerAI's catalogue
    And a gated repo is marked before anyone tries to fetch it
    And opening a repo judges every quantisation against THIS machine at a named context, with the
      suggested one marked

  @web @security
  Scenario: a fetch or a serve from the console is the same consented act as at the terminal
    When the owner fetches or serves from the console
    Then the console shows the verdict, the sum and the licence first and asks for a yes
    And only the page served by this machine, to its own owner, can ask; the request carries a model
      id or a repo and file from the index, never a URL, a path outside the models directory or a
      runtime
    And the work happens in roger, not in the browser, so closing the tab does not stop it

  @web
  Scenario: a fetch from the console is visible with bytes, rate and time left, and survives a reload
    Given a fetch started from the console
    Then the page shows its bytes, rate and time remaining as it moves
    And reloading the page shows the same fetch still running
    And a failure names which failure it was and the fix

  @web
  Scenario: the console stops, switches and removes with the terminal's rules
    Then stopping, switching and removing behave exactly as `roger model stop`, `serve` and `rm` do
    And removing a model that is being served is refused with the reason
    And a destructive action asks once and the safe answer is the default

  # =========================================================================
  # 9. WHERE IT LIVES - FOUNDER RULING 2026-10-01: rows in the SHARE list
  #
  # "no new tab, it should be something under [2] SHARE that is visually obvious not just a
  # keybinding/shortcut, something more integrated, and as a row". Researched against LM Studio,
  # Jan and GPT4All (models you have and getting more live in one list, each variant marked for fit)
  # and empty-state guidance (the next action sits inside the list). Ruled: rows in the SHARE list.
  # The console MODELS tab of §8 is withdrawn; the console's SHARE panel carries the same rows.
  # =========================================================================

  @tui @web
  Scenario: models roger serves itself are rows of SHARE like any other
    Given roger is serving a model itself
    Then it is a row of the SHARE list, marked as served by roger with its quantisation and context
    And it goes on and off air exactly as any other row does

  @tui @web
  Scenario: a model on this disk that is not served is a row you serve from SHARE
    Given a model on this disk that nothing is serving
    Then it is a row of the SHARE list marked as on disk
    And choosing it serves it here, asked first

  @tui @web
  Scenario: a download in progress is a row with a live bar
    Given a model being fetched
    Then it is a row of the SHARE list with its bytes, rate and time left
    And it becomes an on-disk row when it is fetched and checked

  @tui @web
  Scenario: getting a model is always the last row of SHARE, and visibly so
    Then the SHARE list always ends with a row to get a model that fits this machine, stating the
      machine's memory
    And choosing it opens the search and the fit view inside SHARE, not a separate tab
    And when nothing at all was detected, getting a model is the first option offered

  @web
  Scenario: the console has no separate models tab
    Then the console's tabs are the ones the console spec approved
    And everything §8 describes is reached from the console's SHARE panel
