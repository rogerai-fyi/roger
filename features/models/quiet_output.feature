# STATUS: APPROVED 2026-10-13 (founder: Q1-Q5 as recommended, local_models.feature:363 re-approved)
#
# QUIET MODEL OUTPUT: `roger model get` and `roger model serve` say about four lines by default
# (what, its size, whether it fits, where it is serving), and the full account is one flag away
# (--verbose). Option O3 of docs/simple-first-serve-research.md, which recommended it ("four lines by
# default with the total kept, full sum behind --verbose").
#
# This RE-OPENS one approved scenario, features/serving/local_models.feature:363 ("the sum is stated,
# not just the verdict"). Its proposed new text is at the end of this header; nothing in that file is
# edited here.
#
# GROUND TRUTH: a real run of the 915f5039 test build of roger
# (v6.10.0-test.915f5039) on 2026-10-13, with a scratch XDG_CONFIG_HOME holding one tiny model already in
# the local cache (bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M, 100.6 MiB) and the pinned runtime. Nothing
# was downloaded: `get` of another quantisation was declined at the prompt. Code read at 915f5039:
# cmd/rogerai/modelcmd.go (modelFetchChoice, modelBudgetLine, modelServeModel, modelServeDetached,
# modelPlan, modelProgress, parseModelArgv), internal/localmodel/judge.go (Verdict.Line, Sum, Short).
#
# In the examples below, two things in the real output are written generically, by house rule for
# public specs: the model hub's own name is "the model hub", and the spec-sheet GPU list on the memory
# speed line is "<the cards and their speeds>". Paths are shortened to "<config>". Everything else is
# verbatim from the run.
#
# TODAY, `roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0` (11 lines before the prompt):
#
#   bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 is not on this machine yet.
#   this machine: 191.2 GiB of video memory across 4 GPUs
#   memory speed: spec sheet: <the cards and their speeds>
#
#   bartowski/SmolLM2-135M-Instruct-GGUF  SmolLM2-135M-Instruct-Q8_0.gguf  138.1 MiB
#     fits at 8K context: needs 1.4 GiB of 191.2 GiB video memory across 4 GPUs
#     weights 138.1 MiB + cache 180.0 MiB + output 192.0 KiB + working 128.0 MiB + reserve 1.0 GiB = 1.4 GiB
#     licence: apache-2.0 (as its author published it; roger records it and does not referee it)
#     source: the model hub - the author's word, checked against the digest the index publishes
#     free on this disk: 3.2 GiB
#   fetch it? [y/N]: not fetched. Nothing changed.
#
# PROPOSED default:
#
#   bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 · 138.1 MiB · licence apache-2.0 (the author's word, from the model hub)
#   fits at 8K context: needs 1.4 GiB of 191.2 GiB video memory across 4 GPUs · 3.2 GiB free on this disk
#   fetch it? [y/N]: not fetched. Nothing changed.
#
# TODAY, `roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M --port 18931`, then ctrl-c:
#
#   bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M: fits at 8K context by llama.cpp's own check: needs 630.0 MiB, leaving its margin free
#   starting llama.cpp b11146, the cuda build on SmolLM2-135M-Instruct-Q4_K_M.gguf at 8192 context...
#     on one card, CUDA0: it fits there, and one card answers faster than a split
#   on the air locally: "smollm2-135m-instruct" - bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M, Q4_K_M, sha256 2e8040ceae78, 8192 context
#     http://127.0.0.1:18931/v1 (key in <config>/rogerai/local/serving-18931.json)
#     roger share picks it up like any local server. ctrl-c or `roger model stop` ends it.
#   stopping smollm2-135m-instruct.
#
# PROPOSED default:
#
#   "smollm2-135m-instruct" · bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M · 100.6 MiB · licence apache-2.0 (the author's word)
#   fits at 8K context by llama.cpp's own check: needs 630.0 MiB · the cuda build, on CUDA0
#   on the air locally at http://127.0.0.1:18931/v1 (key in <config>/rogerai/local/serving-18931.json) · sha256 2e8040ceae78
#   ctrl-c or `roger model stop` ends it · --verbose shows the full account
#   stopping smollm2-135m-instruct.
#
# TODAY, the same with --detach (4 lines), PROPOSED the same four facts:
#
#   bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M: fits at 8K context by llama.cpp's own check: needs 630.0 MiB, leaving its margin free
#   starting bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M in the background...
#   on the air locally: "smollm2-135m-instruct" - bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M, 8192 context, at http://127.0.0.1:18932/v1
#     it keeps serving after this terminal closes. `roger model status` · `roger model stop`
#
#   "smollm2-135m-instruct" · bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M · 100.6 MiB · licence apache-2.0 (the author's word)
#   fits at 8K context by llama.cpp's own check: needs 630.0 MiB · the cuda build
#   on the air locally at http://127.0.0.1:18932/v1, in the background · sha256 2e8040ceae78
#   it keeps serving after this terminal closes · `roger model status` · `roger model stop`
#
# The output is plain text today in a terminal and in a pipe (a run under a pseudo-terminal printed
# the same bytes with no colour), so NO_COLOR changes nothing, today or proposed.
#
# FOUND DURING THE RUN (a defect today, independent of this spec; see the scenario in §5):
#   `roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M --ctx 9999999` printed
#     "...: fits at 9999999 context by llama.cpp's own check: needs 405.6 GiB, leaving its margin free"
#   on a machine with 191.2 GiB of video memory, then the cuda build failed to load it, the CPU build was
#   tried, and the command ended with
#     "error: will not fit at 9999999 context by llama.cpp's own check: was unable to fit model into
#      system memory by reducing context; fits at 8K"
#   The same command said "fits" and "will not fit" by the same check. In the quiet output the fit line is
#   the only fit claim on screen, so it must be the one the start actually ran under.
#
# DECISIONS for the founder (each with a recommendation):
#
# DECISION Q1. The flag. Recommendation: `--verbose` and `-v`, the same pair `roger detect` already takes,
#   on `roger model get` and `serve` (and the same flag reaches a member with --on). `files`, `status`, `list`,
#   `search` and `stop` are unchanged. No environment switch (YAGNI). Alternative: `--verbose` only.
# DECISION Q2. When the sum is shown by default. Recommendation: the total only, on the fit line, when the
#   verdict is "fits"; the full sum by default whenever the verdict is "tight", "will not fit" or "cannot
#   say", because that is exactly when the owner needs the terms. Alternative: the total only, always.
# DECISION Q3. Output to a pipe or a file (not a terminal). Recommendation: the same quiet lines (stable to
#   read and grep), and the fetch progress as one final line, never a stream of carriage-return redraws in
#   a log. Alternative: the full account whenever stdout is not a terminal.
# DECISION Q4. What leaves the default and stays behind --verbose: the "is not on this machine yet" line,
#   the machine's memory line and its note, the memory speed line, the term-by-term sum (Q2 aside), the
#   long licence and source sentences, the "starting llama.cpp ..." line, the "roger share picks it up"
#   line. Recommendation: all of these, with their facts kept in the short form on the four lines.
# DECISION Q5. The FOUND contradiction. Recommendation: fix it in the same slice, because the quiet fit line
#   is the only fit claim on screen: the fit line printed is the verdict of the build that actually starts,
#   and a GPU start that fails is followed by the fallback line and the CPU build's own verdict (the
#   approved "not a silent fallback" rule), never by an earlier "fits" left standing.
#
# RE-APPROVAL NEEDED, features/serving/local_models.feature:363. Today:
#
#   @fit
#   Scenario: the sum is stated, not just the verdict
#     Given a machine with measured memory and a model with a known size and context
#     When its fit is judged
#     Then the sum accounts for the weights, the KV cache at the context it would run, the
#       runtime's working set, and a reserve
#     And the owner can see each term, not only the answer
#
# Proposed:
#
#   @fit
#   Scenario: the sum is stated, not just the verdict
#     Given a machine with measured memory and a model with a known size and context
#     When its fit is judged
#     Then the verdict always states the total it rests on and the memory it was judged against
#     And the sum accounts for the weights, the KV cache at the context it would run, the
#       runtime's working set, and a reserve
#     And the owner can see each term with `--verbose`, in `roger model files`, and by default whenever
#       the verdict is tight, will not fit or cannot say (features/models/quiet_output.feature)
#
# Other approved scenarios this spec keeps, and the short form that keeps them:
#   local_models.feature:221 (ours vs the author's word, without colour): "(the author's word)" on line 1;
#     an entry from our own catalogue says "(RogerAI catalogue)" instead.
#   local_models.feature:350 (a verdict names its context): every fit line names the context.
#   local_models.feature:356 (fitting and usable are two claims): no speed is folded into the fit line;
#     the speed stays in `roger model status` and behind --verbose, as today.
#   local_models.feature:431 (what is running is sayable in full): `roger model status` is unchanged.
#   local_models.feature:465 (the licence is shown before the fetch): the licence is on line 1, before the prompt.
#   local_models.feature:663 (a GPU build that cannot start is not a silent fallback): the fallback line stays.
#   local_models.feature:493 (a failure says which failure it was): errors are never shortened (§4).
#
# No em dashes and no provider names in this file by house rule.

@cli
Feature: roger model get and serve say four lines by default, and the full account with --verbose
  As someone serving a model on their own machine
  I want to see what it is, its size, whether it fits and where it is serving, in a few lines
  So that the facts I act on are not lost in the arithmetic, which is still there when I ask

  Background:
    Given a machine with 191.2 GiB of video memory across 4 GPUs and 3.2 GiB free on the model disk
    And "bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M" (100.6 MiB, licence apache-2.0) is on this machine
    And "bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0" (138.1 MiB) is not
    And the pinned runtime is installed and resolves to the cuda build

  # =====================================================================================
  # 1. get
  # =====================================================================================

  Scenario: get says what it would fetch in two lines, then asks
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0" and answers n
    Then the output is exactly:
      """
      bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 · 138.1 MiB · licence apache-2.0 (the author's word, from the model hub)
      fits at 8K context: needs 1.4 GiB of 191.2 GiB video memory across 4 GPUs · 3.2 GiB free on this disk
      fetch it? [y/N]: not fetched. Nothing changed.
      """
    And nothing is written to the model disk

  Scenario: the licence and whose word it is come before the question
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0"
    Then "licence apache-2.0" and "the author's word" are printed before "fetch it? [y/N]"

  # 2026-10-13 review: pending, because `roger model get` has no catalogue source yet to tell apart.
  @pending
  Scenario: a model from our own catalogue is told apart from the author's word without colour
    Given a catalogue entry "qwen3-8b" with a hash and a licence we published
    When the owner runs "roger model get qwen3-8b" and answers n
    Then line 1 says "(RogerAI catalogue)" where a hub model says "(the author's word, from the model hub)"

  Scenario: a licence the index does not state is said as unknown, not left out
    Given a hub model whose index entry carries no licence
    When the owner runs "roger model get" for it and answers n
    Then line 1 says "licence - (the author's word, from the model hub)"

  Scenario: an accepted fetch ends with the verified digest and the next command
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 --yes" in a terminal
    Then the two lines are followed by one progress line that redraws in place
    And the last line is "fetched and verified: bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 (sha256 <first 12 hex>). roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0"

  Scenario: the progress line keeps bytes, rate and time left
    Given a fetch that has moved more than a fiftieth of the file
    Then the progress line says the bytes done of the total, the rate and the time left, as today

  Scenario: get of a model already here says so in one line
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"
    Then the output is exactly "already on this machine: bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"

  Scenario: a disk that must be cleared first says what would go, by default
    Given the disk budget means 1 model must be removed to make room
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0" and answers n
    Then the room line naming what would be removed is printed before the question, as today

  # =====================================================================================
  # 2. serve
  # =====================================================================================

  Scenario: serve in the foreground says four lines, then the stop line on exit
    When the owner runs "roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M --port 18931" and presses ctrl-c once it is on the air
    Then the output is exactly:
      """
      "smollm2-135m-instruct" · bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M · 100.6 MiB · licence apache-2.0 (the author's word)
      fits at 8K context by llama.cpp's own check: needs 630.0 MiB · the cuda build, on CUDA0
      on the air locally at http://127.0.0.1:18931/v1 (key in <config>/rogerai/local/serving-18931.json) · sha256 2e8040ceae78
      ctrl-c or `roger model stop` ends it · --verbose shows the full account
      stopping smollm2-135m-instruct.
      """

  Scenario: serve --detach says the same four facts and how to reach it later
    When the owner runs "roger model serve smollm2-135m-instruct --detach --port 18932"
    Then the output is exactly:
      """
      "smollm2-135m-instruct" · bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M · 100.6 MiB · licence apache-2.0 (the author's word)
      fits at 8K context by llama.cpp's own check: needs 630.0 MiB · the cuda build
      on the air locally at http://127.0.0.1:18932/v1, in the background · sha256 2e8040ceae78
      it keeps serving after this terminal closes · `roger model status` · `roger model stop`
      """

  Scenario: a model loaded from a path with no recorded licence says so
    When the owner runs "roger model serve ./SmolLM2-135M-Instruct-Q4_K_M.gguf"
    Then line 1 says "licence - (not recorded: served from a file)"

  Scenario: serve of a model not on this machine is get's lines, the question, then serve's lines
    When the owner runs "roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0" and answers y
    Then get's two lines and the question come first, then the progress line, then serve's four lines
    And the "fetched and verified" line is not printed separately, because the sha256 is on serve's line 3

  Scenario: switching from a model already on the air says what was switched, in one line
    Given "qwen3-0.6b" is being served
    When the owner serves "smollm2-135m-instruct" without --add
    Then one line "switched from <the old id>: <what the stop said>" follows line 3
    And the old one is stopped only after the new one answers, as today

  Scenario: serving beside another says beside which
    Given "qwen3-0.6b" is being served
    When the owner serves "smollm2-135m-instruct" with --add
    Then line 3 reads "on the air locally beside qwen3-0.6b at http://..."

  Scenario: an idle timeout that ends the serve says so
    Given an idle timeout of 10 minutes is set
    When the serve has had no turn for 10 minutes
    Then the last line is "stopped: <why>", as today

  Scenario: a GPU build that falls back to the CPU build says so, by default
    Given the cuda build cannot start the model and the runtime choice is auto
    When the owner runs "roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"
    Then a line "the cuda build could not start it: <its last words> - serving with the CPU build instead" is printed
    And the fit line that follows is the CPU build's own verdict
    And the four lines name "the cpu build"

  Scenario: runtime notes are not hidden
    Given the runtime resolves with a note (for example a build that was settled earlier)
    When the owner runs "roger model serve smollm2-135m-instruct"
    Then each note is printed before line 1, as today

  # =====================================================================================
  # 3. Honesty, kept in the short form
  # =====================================================================================

  Scenario Outline: every fit line names the context it was judged at
    When the owner runs "<command>"
    Then the fit line contains "at <ctx> context"

    Examples:
      | command                                                                  | ctx  |
      | roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0                | 8K   |
      | roger model serve smollm2-135m-instruct                                  | 8K   |
      | roger model serve smollm2-135m-instruct --ctx 4096                       | 4K   |

  Scenario: the fit line says whose check it is
    When the owner serves a model already on this machine
    Then the fit line says "by llama.cpp's own check" when the runtime answered
    And says "(roger's estimate - the runtime's own check could not run: <why>)" when it did not

  Scenario: the fit line always carries the total
    When the owner runs get or serve with the verdict "fits"
    Then the fit line states "needs <total>", and for roger's own estimate also "of <budget> <pool>"

  # DECISION Q2
  Scenario Outline: the full sum is shown by default exactly when the verdict is not a comfortable fit
    Given a model whose verdict here is <verdict>
    When the owner runs "roger model get" for it and answers n
    Then the term-by-term sum line <sum>

    Examples:
      | verdict      | sum                    |
      | fits         | is not printed         |
      | tight        | is printed             |
      | will not fit | is printed             |
      | cannot say   | is replaced by the missing reading, named |

  # 2026-10-13 review: DECISION Q2 holds on serve too (local_models.feature:363 as re-approved). A
  # serve that will not fit is refused with the whole sum (section 4).
  Scenario Outline: serve shows the full sum by default exactly when the verdict is not a comfortable fit
    Given a model whose verdict here is <verdict>
    And the runtime's own check cannot run, so roger's estimate stands
    When the owner serves a model already on this machine without --verbose
    Then the term-by-term sum line <sum>

    Examples:
      | verdict      | sum                    |
      | fits         | is not printed         |
      | tight        | is printed             |
      | cannot say   | is replaced by the missing reading, named |

  Scenario: the speed claim is never folded into the fit line
    When the owner runs get or serve
    Then neither of the four lines states a speed
    And `roger model status` still states the speed, as today

  Scenario: what is running is still sayable in full
    Given "smollm2-135m-instruct" is being served
    When the owner runs "roger model status"
    Then the output names the model, file, quant, full sha256, context, runtime with build and pid, address, speed and idle timeout, as today

  # =====================================================================================
  # 4. Errors stay verbose
  # =====================================================================================

  Scenario: a model that will not fit at the context asked for is refused with the whole arithmetic
    When the owner runs "roger model serve smollm2-135m-instruct --ctx 9999999"
    Then the error names the context, says it will not fit by the check that refused it, and says the context that fits
    And the next line is the term-by-term sum
    And the next line is the exact command that would serve it: "roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M --ctx 8192"
    And --verbose is not needed to see any of it

  Scenario: a name that matches nothing says where it looked
    When the owner runs "roger model serve no-such-model-xyz"
    Then the output is exactly:
      """
      no-such-model-xyz is not served nearby; looking it up on the model hub, because you asked to serve it
      error: nothing nearby and nothing on the model hub matches "no-such-model-xyz" - try roger model search <other words>
      """

  Scenario Outline: every failure keeps today's full wording
    Given <failure>
    When the owner runs get or serve
    Then the error is byte for byte today's, with every line it has today

    Examples:
      | failure                                                       |
      | no room on the disk                                           |
      | no network                                                    |
      | the index refuses the request                                 |
      | a gated model with no token                                   |
      | a file whose digest does not match                            |
      | an explicit gpu choice whose build cannot start the model     |
      | a new model that fails to start while another is serving      |
      | two models that do not fit together                           |
      | an embedding model asked to serve chat                        |
      | a runtime that cannot be fetched or verified                  |
      | a background serve that never comes on the air                |

  Scenario: a failed command exits non-zero, a declined fetch exits zero
    When the owner declines a fetch
    Then the exit status is 0 and the output ends "not fetched. Nothing changed."
    When a serve fails
    Then the exit status is 1

  # =====================================================================================
  # 5. The FOUND contradiction (regression)
  # =====================================================================================

  # DECISION Q5
  Scenario: the fit line printed is the verdict of the build that actually starts
    Given the cuda build's own check says a model fits at 9999999 context
    And the cuda build then fails to load it and the CPU build's check says it will not fit
    When the owner runs "roger model serve smollm2-135m-instruct --ctx 9999999"
    Then no line says "fits at 9999999 context" without a later line saying the cuda build could not start it
    And the command does not end with a "fits" line and a "will not fit" error from checks it does not tell apart
    And each verdict printed names the build whose check it is

  # =====================================================================================
  # 6. --verbose restores everything
  # =====================================================================================

  # DECISION Q1
  Scenario Outline: --verbose prints today's full account, byte for byte
    When the owner runs "<command> --verbose"
    Then the output is byte for byte what the 915f5039 build prints for "<command>"

    Examples:
      | command                                                                         |
      | roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0                       |
      | roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M --port 18931      |
      | roger model serve smollm2-135m-instruct --detach --port 18932                   |
      | roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0                     |

  Scenario Outline: --verbose and -v are a flag with no value, anywhere among the arguments
    When the owner runs "<command>"
    Then it is the same as "roger model serve smollm2-135m-instruct --port 18931 --verbose"
    And it never says "--verbose needs a value"

    Examples:
      | command                                                          |
      | roger model serve --verbose smollm2-135m-instruct --port 18931   |
      | roger model serve -v smollm2-135m-instruct --port 18931          |
      | roger model serve smollm2-135m-instruct --port 18931 -v          |

  Scenario: --verbose reaches a member of the Edge
    When the owner runs "roger model serve smollm2-135m-instruct --on garage --verbose"
    Then the member's answer is its full account

  Scenario: the default output tells the owner the full account exists
    When the owner runs a foreground serve
    Then line 4 ends "--verbose shows the full account"

  # =====================================================================================
  # 7. Terminals, pipes and NO_COLOR
  # =====================================================================================

  # DECISION Q3
  Scenario: output to a pipe is the same quiet lines
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 < /dev/null | cat"
    Then the output is the same two lines and the question as in a terminal
    And the question is answered no, because there is no one to answer it, as today

  Scenario: a fetch into a pipe or a file writes one progress line, not a stream of redraws
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 --yes > fetch.log"
    Then fetch.log contains no carriage return
    And it contains one line with the bytes fetched, the rate and the time it took

  Scenario: a fetch in a terminal redraws its one progress line in place
    When the owner runs "roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0 --yes" in a terminal
    Then the progress is one line redrawn in place, and it stays on screen when the fetch ends

  Scenario: NO_COLOR changes nothing, because the output carries no colour
    When the owner runs get, serve and serve --detach with NO_COLOR set and without it
    Then each pair of outputs is byte for byte the same
    And neither contains an ANSI escape

  Scenario Outline: no default line is wider than a line needs to be read
    When the owner runs "<command>" in a terminal <cols> columns wide
    Then every quiet line is printed whole, wrapping as the terminal wraps it, never cut by roger

    Examples:
      | command                                                      | cols |
      | roger model get bartowski/SmolLM2-135M-Instruct-GGUF:Q8_0    | 80   |
      | roger model serve smollm2-135m-instruct                      | 60   |

  # =====================================================================================
  # 8. Where the quiet output is NOT applied
  # =====================================================================================

  Scenario Outline: the commands that are already the full account do not change
    When the owner runs "<command>"
    Then its output is byte for byte today's

    Examples:
      | command                                                       |
      | roger model files bartowski/SmolLM2-135M-Instruct-GGUF        |
      | roger model status                                            |
      | roger model list                                              |
      | roger model search smollm2                                    |
      | roger model stop smollm2-135m-instruct                        |

  Scenario: the TUI and the console are not changed by this spec
    When the owner fetches or serves from SHARE in the TUI or from the console
    Then what those screens show is unchanged
