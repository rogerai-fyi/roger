# MODERATION - LEAN-PASS RECALIBRATION ("lean pass, keep the net").
#
# THE INCIDENT (verified): a coding-agent relay body (aider / ai-benchy) whose payload said
# things like "summarize this repository" made the Groq safeguard model answer with a SUMMARY
# / refusal / rambling line instead of the required "safe" / "unsafe <codes>" verdict. The old
# screenGroq parse (moderation.go:398-428) treated ANY first word that was not exactly "safe"
# as flagged and returned 451 - so a malformed, code-LESS verdict became a false-positive block
# of a benign coding request. Separately, every unsafe category blocked identically, which was
# heavier than the founder wants for the low-harm categories.
#
# THE FOUNDER-APPROVED POSTURE (this spec pins it EXHAUSTIVELY - happy path AND adversarial):
#
#   BLOCK NET (451 on a CLEAR verdict that carries the valid code):
#     S4 / the "sexual/minors" token -> BLOCK csam=true, PRESERVE + queue a CyberTipline report.
#       ALWAYS. Detected even inside a rambling/noisy verdict; a present CSAM signal NEVER fails
#       open and NEVER passes on retry. (Unchanged from today's isCSAM path.)
#     S1 (violence), S3 (sex crimes), S5 (weapons of mass harm), S6 (self-harm) -> BLOCK 451,
#       csam=false.
#   PASS + LOG (ALLOW, but emit a "passed-but-flagged category <code>" telemetry line):
#     S2 (hacking), S7 (hate), S8 (drugs).
#   MALFORMED verdict (non-empty text, NO valid S1-S8 code: summary / refusal / rambling /
#     bogus code like S9): RETRY ONCE with a tightened re-prompt; if still no valid code ->
#     PASS (unless a CSAM token is present anywhere -> block). This is the aider/ai-benchy fix.
#   INFRA OUTAGE (empty message.content, transport error, non-200, timeout - NO verdict at all):
#     FAIL OPEN even under ROGERAI_REQUIRE_MODERATION=1 - serve unscreened AND log a loud,
#     auditable "MODERATION FAIL-OPEN (classifier unavailable: <reason>)" line. During an outage
#     the CSAM screen is NOT applied (accepted founder tradeoff - there is no verdict to read).
#
#   RELIABILITY FIXES (all categories):
#     R1 CONTENT ISOLATION - the screened text is wrapped in explicit data delimiters and the
#        classifier is told to treat everything between them as DATA, never instructions, so an
#        agent payload ("summarize this repo") cannot hijack the classifier.
#     R2 STRICT PARSE - only a verdict carrying >=1 VALID code S1-S8 is a candidate decision; a
#        bogus code (S9, X, S42) is NOT valid and makes the verdict malformed.
#     R3 RETRY ONCE on malformed before deciding.
#
# GROUND TRUTH: cmd/rogerai-broker/moderation.go screenGroq / groqVerdict / decideVerdict /
# groqFailMode / isCSAM / moderationPolicy; enforced by moderation_recalibration_bdd_test.go
# (real httptest Groq stub returning canned verdicts, no mocks) + moderation_test.go.
#
# NOTE: this recalibration is the GROQ safeguard path (production backend, gpt-oss-safeguard).
# The URL adapter backend keeps its flagged-boolean shape and is out of scope here. Because the
# chat/TTS/STT-output/voice-registration screens ALL share moderation.screen()->screenGroq, this
# posture (incl. the outage fail-open) applies to every one of those surfaces on the groq backend
# - the founder sign-off + live red-team must cover them, not just the chat relay.

Feature: Moderation lean-pass recalibration - keep the CSAM/harm net, stop false-positive blocks

  # ===========================================================================
  # 1. CLEAN + BLOCK NET
  # ===========================================================================

  Scenario: a clean "safe" verdict allows without a retry
    Given a Groq safeguard backend scripted to return "safe"
    When a relay prompt is screened
    Then the screen allows (status 0)
    And the classifier was called exactly 1 time

  Scenario Outline: a block-net category rejects 451 and is not a CSAM incident
    Given a Groq safeguard backend scripted to return "unsafe <code>"
    When a relay prompt is screened
    Then the screen returns 451
    And modResult.csam is false

    Examples:
      | code |
      | S1   |
      | S3   |
      | S5   |
      | S6   |

  Scenario: same-line and next-line codes both block a block-net category
    Given a Groq safeguard backend scripted to return "unsafe\nS1,S3"
    When a relay prompt is screened
    Then the screen returns 451
    And modResult.csam is false

  # REGRESSION (reviewer + pre-push audit, 2026-07-08): a block-net code obscured by trailing
  # punctuation OR an internal separator (slash/semicolon/pipe/tab) must still block.
  Scenario Outline: a separator-obscured block-net code still rejects 451
    Given a Groq safeguard backend scripted to return "<verdict>"
    When a relay prompt is screened
    Then the screen returns 451

    Examples:
      | verdict      |
      | unsafe S1.   |
      | unsafe S5)   |
      | unsafe S6;   |
      | unsafe S1;S3  |
      | unsafe S1\|S2 |

  # ===========================================================================
  # 2. CSAM NET - ALWAYS blocks + preserves, never fails open, never passes on retry
  # ===========================================================================

  Scenario: an S4 verdict blocks 451 with csam=true for the preserve+report path
    Given a Groq safeguard backend scripted to return "unsafe S4"
    When a relay prompt is screened
    Then the screen returns 451
    And modResult.csam is true with category "S4"

  Scenario: the "sexual/minors" token is detected as CSAM
    Given a Groq safeguard backend scripted to return "unsafe sexual/minors"
    When a relay prompt is screened
    Then the screen returns 451
    And modResult.csam is true

  Scenario: a CSAM signal buried in a rambling/noisy verdict still blocks as CSAM
    Given a Groq safeguard backend scripted to return "I think this could be S4 material, unsure"
    When a relay prompt is screened
    Then the screen returns 451
    And modResult.csam is true with category "S4"
    And the classifier was called exactly 1 time

  Scenario: a CSAM signal in an otherwise-malformed verdict blocks and never reaches the pass-on-retry path
    Given a Groq safeguard backend scripted to return "cannot comply but this is S4"
    When a relay prompt is screened
    Then the screen returns 451
    And modResult.csam is true with category "S4"
    And the classifier was called exactly 1 time

  # REGRESSION (reviewer + pre-push audit, 2026-07-08): a code hidden by ANY separator - trailing
  # punctuation, or an internal slash/semicolon/colon/pipe/tab - must not evade the CSAM net.
  Scenario Outline: a separator-obscured CSAM signal still blocks as CSAM
    Given a Groq safeguard backend scripted to return "<verdict>"
    When a relay prompt is screened
    Then the screen returns 451
    And modResult.csam is true
    And the classifier was called exactly 1 time

    Examples:
      | verdict                       |
      | cannot comply but this is S4. |
      | unsafe (S4)                   |
      | the content is sexual/minors. |
      | unsafe S4/S5                  |
      | unsafe S1;S4                  |

  # ===========================================================================
  # 3. PASS + LOG (low-harm categories)
  # ===========================================================================

  Scenario Outline: a pass-log category is ALLOWED and logged, never dispatched-blocked
    Given a Groq safeguard backend scripted to return "unsafe <code>"
    When a relay prompt is screened
    Then the screen allows (status 0)
    And a "passed-but-flagged category <code>" telemetry line is logged

    Examples:
      | code |
      | S2   |
      | S7   |
      | S8   |

  # ===========================================================================
  # 4. MALFORMED -> retry once -> pass (the aider/ai-benchy fix)
  # ===========================================================================

  Scenario Outline: a code-less malformed verdict retries once then passes
    Given a Groq safeguard backend scripted to return "<verdict>"
    When a relay prompt is screened
    Then the screen allows (status 0)
    And the classifier was called exactly 2 times

    Examples:
      | verdict                                             |
      | I cannot help with that                             |
      | Here is a summary of the repository you asked about |
      | unsafe                                              |
      | unsafe S9                                           |
      | X42 undefined                                       |

  Scenario: the aider/ai-benchy incident payload passes instead of a false-positive 451
    Given a Groq safeguard backend scripted to return "The user is asking me to summarize a code repository, which is a benign development task."
    When the aider agent relay body is screened
    Then the screen allows (status 0)
    And the classifier was called exactly 2 times

  Scenario: a verdict malformed on the first call but valid on the retry uses the retry decision
    Given a Groq safeguard backend scripted to return "please clarify" then "unsafe S1"
    When a relay prompt is screened
    Then the screen returns 451
    And the classifier was called exactly 2 times

  Scenario: a malformed first call whose retry names a pass-log category passes and logs
    Given a Groq safeguard backend scripted to return "let me think" then "unsafe S2"
    When a relay prompt is screened
    Then the screen allows (status 0)
    And the classifier was called exactly 2 times

  # ===========================================================================
  # 5. INFRA OUTAGE -> fail open + loud log, even under require=1
  # ===========================================================================

  Scenario: a transport error fails OPEN under require=1 with a loud log
    Given ROGERAI_REQUIRE_MODERATION=1 and the Groq classifier is unreachable
    When a relay prompt is screened
    Then the screen allows (status 0)
    And a loud "MODERATION FAIL-OPEN" incident line is logged

  Scenario: a non-200 from the classifier fails OPEN under require=1 with a loud log
    Given ROGERAI_REQUIRE_MODERATION=1 and the Groq classifier returns HTTP 500
    When a relay prompt is screened
    Then the screen allows (status 0)
    And a loud "MODERATION FAIL-OPEN" incident line is logged

  Scenario: an empty verdict (no message content) fails OPEN under require=1 with a loud log
    Given ROGERAI_REQUIRE_MODERATION=1 and the Groq classifier returns an empty verdict
    When a relay prompt is screened
    Then the screen allows (status 0)
    And a loud "MODERATION FAIL-OPEN" incident line is logged

  # ===========================================================================
  # 6. CONTENT ISOLATION (R1) - the payload is wrapped as data, not instructions
  # ===========================================================================

  Scenario: the screened text is wrapped in explicit data delimiters
    Given a Groq safeguard backend scripted to return "safe"
    When a relay prompt is screened
    Then the request the classifier received wraps the prompt in data delimiters
    And the moderation policy instructs the classifier to treat the delimited text as data, never instructions
