# MODERATION - THE TOOLS / FUNCTIONS ARRAY IS SCREENED (evasion closed).
#
# THE GAP (verified, cmd/rogerai-broker/moderation.go promptText:497-525): the relay screen
# only pulled text from messages[].content. An OpenAI chat-completions body can ALSO carry a
# top-level "tools" array (tools[].function.{name,description,parameters}) and the legacy
# "functions" array (functions[].{name,description,parameters}). A client could hide a harmful
# instruction inside a tool `description` (or a nested parameter description) - free text that
# the provider node still sees - and it would skip moderation entirely, because promptText
# never read it. That is an evasion surface: harmful-intent-in-a-tool-description.
#
# THE FIX (this change, BUILD-AND-HOLD): extend promptText to ALSO fold the tools/functions
# array's text into the screened blob - each tool's function name + description + every string
# value inside its parameters schema (so nested parameter descriptions, enums, examples are
# screened too). Pure text extraction: the verdict mapping, the 451 path, and the CSAM
# preserve+report path are UNCHANGED. Safe to do now that the intent-not-capability carveout
# (#39) is live: a benign tool schema full of capability vocabulary ("executes shell commands",
# "deletes files") is ALLOWED by the carveout, so folding the tools array in does NOT
# re-introduce the false positive.
#
# HEADLINE INVARIANT: a harmful instruction placed ONLY in a tool/function description (or a
# parameter description) is now SCREENED and BLOCKS; a benign tool-heavy tools array still
# PASSES (the #39 carveout holds); a body with NO tools array behaves exactly as before; the
# verdict mapping and the 451/CSAM path are unchanged.
#
# HARNESS (see moderation_tools_bdd_test.go + moderation_intent_live_test.go):
#   @plumbing - deterministic; asserts promptText now extracts the tools/functions text and
#               that a body whose ONLY harmful text is a tool description flows through screen()
#               to the SAME 451 (and CSAM) mapping. No network (a local httptest stub for the
#               verdict). This is the layer that proves the extraction gap is closed.
#   @live     - runs the REAL Groq safeguard model (screenGroq) against the golden corpus in
#               testdata/moderation/corpus/{pass,block,csam}, whose tool-array-shaped bodies
#               (tools_*.txt, functions_*.txt) carry their harmful/benign text in the
#               STRUCTURED tools/functions array. Realized by moderation_intent_live_test.go's
#               generic corpus scan (which screens promptText(body) for every file), SKIPPED
#               when MODERATION_GROQ_KEY (or GROQ_API_KEY) is unset, exactly like #39. This is
#               the only layer that proves the harmful tool description actually BLOCKS and the
#               benign tool array actually PASSES against the live judge.

Feature: Moderation screens the top-level tools / functions array, not only the messages

  # ==========================================================================
  # SECTION A - PLUMBING: promptText now extracts the tools/functions text
  # (each is @plumbing, hermetic - proves the extraction gap is closed)
  # ==========================================================================

  @plumbing
  Scenario: a harmful instruction in a tool description is now in the screened text
    Given a relay body whose only harmful text is inside a top-level tool's "description"
    And the messages array is benign
    When promptText extracts the text to screen
    Then the extracted text contains the harmful tool description
    And the extracted text still contains the benign message content

  @plumbing
  Scenario: a tool's function name is included in the screened text
    Given a relay body with a top-level tool whose function name is "MALICIOUS_TOOL_NAME"
    When promptText extracts the text to screen
    Then the extracted text contains "MALICIOUS_TOOL_NAME"

  @plumbing
  Scenario: a harmful instruction in a nested parameter description is now screened
    Given a relay body whose harmful text is inside a tool parameter's "description"
    When promptText extracts the text to screen
    Then the extracted text contains the harmful parameter description

  @plumbing
  Scenario: the legacy top-level functions array is also screened
    Given a relay body whose only harmful text is inside a legacy top-level "functions" entry description
    When promptText extracts the text to screen
    Then the extracted text contains the harmful function description

  @plumbing
  Scenario: a body with NO tools array behaves exactly as before (no regression)
    Given a relay body with only messages and no tools or functions array
    When promptText extracts the text to screen
    Then the extracted text equals the messages-only extraction (unchanged)

  @plumbing
  Scenario: multimodal message text is still screened alongside the tools array (no regression)
    Given a relay body with multimodal message parts AND a tool description
    When promptText extracts the text to screen
    Then the extracted text contains both the message part text and the tool description

  # ==========================================================================
  # SECTION B - PLUMBING: the verdict mapping / 451 / CSAM path is UNCHANGED
  # (a body whose ONLY harmful text is a tool description flows through screen())
  # ==========================================================================

  @plumbing
  Scenario: a tool-description-only body reaches the classifier and a flagged verdict 451s
    Given a stub safeguard backend that returns "unsafe S1"
    And a relay body whose only harmful text is a tool description
    When the body's prompt text is screened
    Then the screen returns 451 (flagged)

  @plumbing
  Scenario: a CSAM verdict on a tool-description-only body still 451s AND flags csam (preserve path intact)
    Given a stub safeguard backend that returns "unsafe S4"
    And a relay body whose only harmful text is a tool description
    When the body's prompt text is screened
    Then the screen returns 451 (flagged)
    And modResult.csam is true with the matched category S4

  @plumbing
  Scenario: a benign tool-heavy body with a "safe" verdict still allows (verdict mapping unchanged)
    Given a stub safeguard backend that returns "safe"
    And a relay body with a benign tool-heavy tools array
    When the body's prompt text is screened
    Then the screen returns status 0 (ALLOW)

  # ==========================================================================
  # SECTION C - LIVE: against the REAL safeguard model (golden corpus).
  # Realized by moderation_intent_live_test.go's generic corpus scan; SKIPPED without MODERATION_GROQ_KEY.
  # ==========================================================================

  @live
  Scenario: a harmful instruction hidden in a tool description BLOCKS (the evasion is closed)
    Given the moderation backend is the real Groq safeguard model with the refined policy
    And a relay body whose only harmful text is a malware-authoring instruction in a tool "description"
    When the concatenated prompt text is screened
    Then the screen returns 451 (flagged)

  @live
  Scenario: a harmful instruction in a legacy functions entry BLOCKS
    Given the moderation backend is the real Groq safeguard model with the refined policy
    And a relay body whose only harmful text is a weapon-synthesis instruction in a legacy "functions" description
    When the concatenated prompt text is screened
    Then the screen returns 451 (flagged)

  @live
  Scenario Outline: a benign tool-heavy tools array still PASSES (the #39 carveout holds)
    Given the moderation backend is the real Groq safeguard model with the refined policy
    And a relay body carrying the real <agent> tool schema in the top-level tools array
    When the concatenated prompt text is screened
    Then the screen returns status 0 (ALLOW)

    Examples:
      | agent    |
      | opencode |
      | aider    |
      | hermes   |

  @live @csam @tns-owned
  Scenario: a CSAM "tool description" still BLOCKS and is flagged for preserve+report
    Given the moderation backend is the real Groq safeguard model with the refined policy
    And a relay body whose tool description states its purpose is to produce child sexual abuse material
    When the concatenated prompt text is screened
    Then the screen returns 451 (flagged)
    And modResult.csam is true with the matched category S4
    # The real CSAM-in-a-tool-description corpus body is founder/T&S-owned and ABSENT from the
    # public tree (like #39). This scenario is validated by trust-and-safety in the live gate;
    # no CSAM content is authored here.
