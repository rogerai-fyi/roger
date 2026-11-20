# Details text is written by one person and read by strangers, in a terminal, in a browser
# console and on the public website. It is the most hostile string we display.
#
# Two layers, as for base_model and license today (protocol.Taxonomy.Validate vs normalize):
#
#   STRICT (Declare): the operator is told, at `roger share`, in the TUI g editor, in the local
#     console and in the web owner editor. Over-length is REFUSED, never cut, because text
#     arriving shortened is the same surprise as a category arriving dropped.
#   LENIENT (broker normalize, and every reader re-canonicalizes): whatever a node or an old
#     broker sends is passed through protocol.boundedText with the field's cap, which drops
#     C0/C1 controls (so ESC, and with it every ANSI sequence, cannot survive), DEL, bidi
#     embeddings/overrides/isolates, zero-width characters and the line/paragraph separators,
#     trims, and caps by CHARACTERS. Details text differs from a name in ONE way: tab, CR,
#     LF and the line/paragraph separators become a space and runs of spaces collapse to one,
#     because a sentence typed across two lines should read as a sentence, not as "oneline".
#     Details are always ONE line per field on the wire; the surfaces do the wrapping.
#
# Rendering rules (every surface):
#   TUI       re-canonicalized on read, rendered as plain styled text, wrapped to the width.
#   console   textContent only. No innerHTML, insertAdjacentHTML or outerHTML in the details code.
#   website   textContent (bands.js) or Svelte text interpolation; never {@html}.
#   /v1/models and CLI  JSON-encoded / printed after the same canonicalization.
#
# Notation: in this file "\uXXXX", "\n", "\t", "\r" and "\xNN" inside quoted strings and
# table cells are ESCAPES that the step definitions decode before use. The spec file itself
# holds no raw control, bidi or zero-width character (a test checks that too).
#
# Reserved intended uses: a tag is refused for words like "diagnosis" or "targeting"
# (offer_taxonomy.feature, healthcare/defense markets rule). summary and best_at are also
# claims about intended use, so the same reserved words are refused there as whole words.
# not_for is exempt: "not for diagnosis" is exactly the warning we want people to write.

Feature: Details text is bounded, stripped of control and spoofing characters, and inert everywhere

  # ---------------------------------------------------------------- length, strict
  Scenario Outline: The strict layer refuses over-length text and accepts the boundary
    When the operator declares <field> as a string of <n> "<char>" characters
    Then the declaration is <result>

    Examples:
      | field   | n   | char | result                                               |
      | summary | 140 | a    | accepted                                             |
      | summary | 141 | a    | refused with "summary: at most 140 characters (got 141)" |
      | summary | 140 | é    | accepted                                             |
      | summary | 140 | 火   | accepted                                             |
      | summary | 141 | 火   | refused with "summary: at most 140 characters (got 141)" |
      | summary | 140 | 🔥   | accepted                                             |
      | best_at | 200 | a    | accepted                                             |
      | best_at | 201 | a    | refused with "best at: at most 200 characters (got 201)" |
      | not_for | 200 | a    | accepted                                             |
      | not_for | 201 | a    | refused with "not for: at most 200 characters (got 201)" |

  Scenario: Length is measured after trimming surrounding whitespace
    When the operator declares summary as "   " followed by 140 "a" characters followed by "   "
    Then the declaration is accepted
    And the saved summary has 140 characters

  Scenario: Whitespace-only text is no text
    When the operator declares summary "      "
    Then the declaration is accepted
    And the saved declaration has no summary

  # ---------------------------------------------------------------- length, lenient
  Scenario Outline: The broker cuts over-length text from a node to the cap
    Given an offer whose <field> is a string of <n> "a" characters
    When the broker normalizes the offer
    Then the offer's <field> has <kept> characters

    Examples:
      | field   | n     | kept |
      | summary | 141   | 140  |
      | summary | 10000 | 140  |
      | best_at | 201   | 200  |
      | not_for | 65536 | 200  |

  Scenario: A cut never splits a multi-byte character
    Given an offer whose summary is 200 "火" characters
    When the broker normalizes the offer
    Then the offer's summary is valid UTF-8 with 140 characters

  Scenario: Invalid UTF-8 from a node is replaced, not passed through
    Given an offer whose summary bytes are "ok \xff\xfe ok"
    When the broker normalizes the offer
    Then the offer's summary is valid UTF-8
    And the offer's summary starts with "ok" and ends with "ok"

  # ---------------------------------------------------------------- control and spoofing characters
  Scenario Outline: Control and spoofing characters are dropped at both layers
    Given a summary "<raw>"
    When it is declared by the operator
    Then the saved summary is "<clean>"
    When it arrives from a node and the broker normalizes it
    Then the offer's summary is "<clean>"

    Examples:
      | raw                                   | clean                      |
      | fire\u001b[31m RED\u001b[0m model     | fire[31m RED[0m model      |
      | a\u001b]8;;https://x.invalid\u0007b   | a]8;;https://x.invalidb    |
      | line one\nline two                    | line one line two          |
      | tab\there                             | tab here                   |
      | cr\r\n\n\nreturn                      | cr return                  |
      | nul\u0000byte                         | nulbyte                    |
      | del\u007fete                          | delete                     |
      | c1\u009bcsi                           | c1csi                      |
      | bidi \u202egnp.exe                    | bidi gnp.exe               |
      | iso \u2066late\u2069                  | iso late                   |
      | zero\u200bwidth\u200dhere\ufeff       | zerowidthhere              |
      | sep\u2028line\u2029para               | sep line para              |
      | many     spaces   inside              | many spaces inside         |

  Scenario: ESC is dropped, so no ANSI sequence can reach a terminal
    Given an offer whose best_at contains "\u001b[2J\u001b[H" and "\u001b]52;c;aGk=\u0007"
    When the TUI renders the band's about block
    Then the rendered block contains no byte 0x1b
    And the rendered block contains no byte 0x07

  Scenario: Text is re-canonicalized on read, not only at the broker
    Given a broker that does not normalize details (an older or third-party broker)
    And its /discover offer for "demo-1b-chat" has summary "x\u001b[31my"
    When the TUI, the local console and the CLI read that offer
    Then each shows the summary "x[31my"

  # ---------------------------------------------------------------- markup is text
  Scenario Outline: Markup in details is shown as literal text in every web surface
    Given an on-air offer for "demo-1b-chat" with summary "<raw>"
    When the local console BROWSE details and the website QSL card show it
    Then each shows exactly the characters "<raw>"
    And neither page gained an element, attribute or script from it

    Examples:
      | raw                                        |
      | <script>alert(1)</script>                  |
      | <img src=x onerror=alert(1)>               |
      | <a href="javascript:alert(1)">docs</a>     |
      | "><svg onload=alert(1)>                    |
      | {@html '<b>x</b>'}                         |
      | {{card:nano.size}}                         |
      | &lt;b&gt;already escaped&lt;/b&gt;         |
      | ${alert(1)}                                |

  Scenario: The console details code never assigns markup
    When the console's details source is inspected
    Then it contains no "innerHTML =", "insertAdjacentHTML" or "outerHTML"

  Scenario: The website details code never renders raw HTML
    When the website's QSL details source is inspected
    Then it contains no "{@html" and assigns details only through textContent

  Scenario: A URL in operator text is never a link
    Given an on-air offer for "demo-1b-chat" with best_at "see https://example.invalid/free-credits"
    When the console and the website show the details
    Then the text "https://example.invalid/free-credits" is shown as plain text
    And no anchor element points at "example.invalid"

  # ---------------------------------------------------------------- reserved intended use
  Scenario Outline: A reserved use in summary or best_at is refused at declare
    When the operator declares <field> "<text>"
    Then the declaration is refused with "reserved use \"<word>\""

    Examples:
      | field   | text                                   | word      |
      | summary | Patient triage from photos.            | triage    |
      | summary | Fast DIAGNOSIS of skin conditions.     | diagnosis |
      | best_at | dosing advice                          | dosing    |
      | best_at | target-acquisition for drones          | target-acquisition |
      | best_at | weapons guidance                       | weapons   |
      | summary | Radiology report drafting.             | radiology |

  Scenario Outline: A reserved word inside a longer word is not a match
    When the operator declares summary "<text>"
    Then the declaration is accepted

    Examples:
      | text                                   |
      | Reads outpatient-building HVAC alarms. |
      | Weaponsmith is a game character name.  |

  Scenario: A reserved use in not_for is allowed
    When the operator declares not_for "Not for diagnosis, triage or dosing."
    Then the declaration is accepted

  Scenario: A reserved use in summary from a node is dropped by the broker
    Given an offer whose summary is "Patient triage from photos." and best_at is "Reads gauges."
    When the broker normalizes the offer
    Then the offer has no summary
    And the offer's best_at is "Reads gauges."

  # ---------------------------------------------------------------- other limits
  Scenario: The details add at most 540 characters to an offer
    Given an offer whose summary, best_at and not_for are each 100000 characters
    When the broker normalizes the offer
    Then the three fields together hold at most 540 characters

  Scenario: An oversized registration body is still refused by the existing body limit
    Given a registration body larger than the broker's registration limit because of details text
    When the broker reads it
    Then it is refused exactly as any oversized registration is
