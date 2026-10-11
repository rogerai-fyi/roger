# STATUS: PROPOSED 2026-10-11 (awaiting founder approval)
# BUILD STATUS: NOT BUILT.
#
# ROGERAI'S MODEL CATALOGUE, AND SHORT NAMES FOR WHAT IT HOLDS - slice 1 of the first-run work.
#
# WHY. The founder, after serving a model by hand: "is this only complicated because we are reusing
# cache? will it be easier for the end user client that is trying this for the first time, seems
# hard", then "please just do research if this can be done first in a more easier flow or pattern".
# The research (docs/simple-first-serve-research.md) found the genuinely hard part is the NAME: a
# first-time user has to type `bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M`. Every comparable tool
# answers with a short name from a curated list. Founder approved 2026-10-11: build the catalogue and
# short names FIRST, then quiet output, a no-argument default, TUI suggestions, the station invite
# naming the model, and a local run verb, in that order.
#
# WHAT ALREADY EXISTS, so none of it is redesigned here:
#   - features/share/model_catalog.feature (APPROVED, unbuilt): a signed, cached catalogue of
#     offerable artifacts; consented, checksum-verified acquisition; fit against real memory; honest
#     degradation when the catalogue cannot be fetched. This file BUILDS that catalogue and adds the
#     one thing it never said: an entry has a short name a person can type.
#   - features/serving/local_models.feature (APPROVED, built): HuggingFace search and fetch, the
#     arithmetic (`localmodel.Judge`, four verdicts, the sum), the quant pick (`localmodel.Suggest`:
#     Q4_K_M when it fits comfortably, else the largest that fits comfortably, else the largest that
#     fits at all), the runtime's own fit check winning once the file is on disk, the disk budget,
#     the licence shown and recorded and never gating, ours-vs-the-author's-word (:221).
#   - internal/catalog (slice 1 of model_catalog, written, no importer): Entry{ID, Repo, File, Bytes,
#     SHA256, ServeMem, License, Runtime, Parent}, Validate, AssessFit, Merge.
#   - cmd/rogerai/modelcmd.go `modelResolve`: a .gguf path, then a name already on disk
#     (`Home.Find`), then a `owner/repo[:QUANT]` reference fetched from HuggingFace.
#
# WHAT THIS ADDS (and nothing else):
#   1. The catalogue as data roger can read: entries with a short name, aliases, one file per
#      quantisation (each with its byte count and sha256), the serving memory we measured, the
#      licence, the runtime, and the repo it comes from.
#   2. Where the catalogue comes from, how it is verified, and how it behaves offline.
#   3. `roger model serve <short name>` (and `get`, `files`, `--on <node>`) resolving through it,
#      choosing the quantisation by the existing Suggest rule against THIS machine.
#   4. `roger model serve` with no argument offering one fitting default, by a stated rule.
#   5. The model browser (TUI and console) opening on two or three catalogue models that fit.
#
# NOT IN THIS SLICE (each is its own later slice with its own spec; noted so nobody assumes it):
#   quiet output (re-approves local_models.feature:363), a station invite naming the model to serve,
#   a local run verb, and `roger share` suggesting roger's own serve when nothing is detected.
#
# GROUND TRUTH: internal/catalog/catalog.go, internal/localmodel/judge.go (Judge, Rank, Suggest),
# internal/localmodel/get.go and store.go (Home.Find, the disk budget), internal/hfhub (the index),
# internal/provision (fetch + verify), cmd/rogerai/modelcmd.go (parseModelArgv, modelResolve,
# modelServe), internal/modelview (the shared screen hooks), internal/tui (SHARE, the browser),
# internal/webui (the console SHARE panel).
#
# =============================================================================================
# DECISIONS FOR THE FOUNDER
# =============================================================================================
#
# DECISION 1 (catalogue source): (i) a list shipped inside the binary, refreshable with a new
#   release; (ii) a signed list fetched from rogerai.fm and cached; (iii) both: the signed fetched
#   list when it verifies, the binary's copy as the offline and first-run fallback.
#   RECOMMEND (iii). It works airgapped from the first run, and new models do not wait for a release.
#
# DECISION 2 (no-argument default rule): (i) the largest general chat model that fits comfortably;
#   (ii) a fixed starter per memory tier, stated in the output, e.g. under 8 GiB usable, 8 to 24 GiB,
#   over 24 GiB (each tier names one catalogue entry); (iii) always the smallest.
#   RECOMMEND (ii): stable, explainable in one line, testable, and it does not change every time a
#   bigger model is added to the list.
#
# DECISION 3 (run verb, LATER slice, noted only): (i) `roger run <name>`; (ii) `roger chat <name>`;
#   (iii) `roger --lan use <name>` offering to serve; (iv) none.
#   RECOMMEND (i), mirroring the most familiar tool; `roger use` keeps meaning the market.
#   Nothing in this file depends on it.
#
# DECISION 4 (quiet output, NEXT slice, noted only): four lines by default with the total need kept
#   on the line and the full sum behind --verbose, which re-approves local_models.feature:363; or keep
#   the full sum, tighter. RECOMMEND the four lines. This file's scenarios assert FACTS (name, source,
#   size, verdict, licence), never line counts, so they hold under either answer.
#
# DECISION 5 (station invite carries the model, LATER slice, noted only): a new `--serve <name>` on
#   `roger tower invite --station`, or reuse `--models`. RECOMMEND `--serve`: offering and requesting
#   are different acts. Short names from this file are what make that slice pleasant.
#
# DECISION 6 (new, this slice): what a short name may look like, so it never collides with a repo
#   reference or a path. RECOMMEND: lower-case letters, digits, '.', '-', no '/', no ':' unless a
#   quantisation follows (`qwen3-8b:Q8_0`), 2 to 40 characters.
#
# =============================================================================================
# APPROVED SCENARIOS THIS WOULD SUPERSEDE (not edited here; proposed text in brackets)
# =============================================================================================
#
#   features/serving/local_models.feature:581 "getting a model is always the last row of SHARE":
#     its line "choosing it opens the search and the fit view inside SHARE" becomes [choosing it
#     opens two or three catalogue models that fit this machine, already judged, with the search one
#     keystroke away inside SHARE]. The rest of the scenario stands.
#   Nothing else is contradicted. :221 (ours vs the author's word) and :229 (searching is an action,
#   only the cached catalogue and what is on disk on opening) are what this file builds on.
#
# Tags: @catalog (the data and its trust), @names (resolution), @default (no argument), @tui, @web,
# @offline, @adversarial.

Feature: A person can serve a model by a short name from RogerAI's catalogue
  A first-time user should be able to type a name they can remember, see whose word they are taking
  and what it costs on this machine, say yes once, and be serving. Nothing about consent, the fit
  check, the digest check or the licence line gets weaker to make that short.

  Background:
    Given a machine with usable memory that roger can measure
    And RogerAI's catalogue lists "smollm2" and "qwen3-8b" with files in several quantisations

  # =========================================================================
  # 1. THE CATALOGUE ITSELF
  # =========================================================================

  @catalog
  Scenario: a catalogue entry carries everything a person needs to consent
    Given a catalogue entry for "qwen3-8b"
    Then it names the repository its files come from as an absolute address
    And each quantisation it lists has its own file name, byte count and sha256 digest
    And it states the serving memory RogerAI measured, the licence and the runtime that serves it
    And it states its upstream parent when it has one
    And an entry missing any of those is not offered, naming the missing field

  @catalog
  Scenario: every entry has one short name and may have aliases
    Then "qwen3-8b" is the entry's short name
    And an alias such as "qwen3:8b" resolves to the same entry
    And no two entries share a short name or an alias
    And a catalogue in which two entries collide is refused as a whole, naming both

  @catalog
  Scenario Outline: a short name is shaped so it can never be mistaken for a repo or a path
    When a catalogue entry declares the short name "<name>"
    Then the entry is <outcome>

    Examples:
      | name              | outcome                                   |
      | qwen3-8b          | accepted                                  |
      | smollm2           | accepted                                  |
      | gpt-oss-20b       | accepted                                  |
      | Qwen3-8B          | refused: a short name is lower case       |
      | owner/qwen3-8b    | refused: a short name has no "/"          |
      | qwen3-8b.gguf     | refused: a short name is not a file name  |
      | q                 | refused: a short name has 2 to 40 characters |

  @catalog
  Scenario: the catalogue is drawn as RogerAI's word, the wide world as the author's
    Given "qwen3-8b" from the catalogue and a repository found by searching HuggingFace
    When both are listed
    Then the catalogue entry is marked as RogerAI's catalogue with the serving memory we measured
    And the searched repository is marked as its author's word
    And the two are distinguishable without colour

  # =========================================================================
  # 2. WHERE IT COMES FROM, AND WHY IT IS TRUSTED
  # =========================================================================

  @catalog
  Scenario: the binary carries a catalogue, so the first run works with no network
    Given a machine that has never fetched the catalogue and has no network
    When the owner runs "roger model serve smollm2"
    Then the name resolves from the catalogue built into this release
    And the owner is told the name came from the catalogue this release shipped with

  @catalog
  Scenario: a newer signed catalogue replaces the built-in one once it verifies
    Given the published catalogue is newer than the one this release shipped with
    And its signature verifies against RogerAI's catalogue key built into roger
    When roger refreshes the catalogue
    Then the newer catalogue is cached and used
    And the cache records when it was fetched

  @catalog @adversarial
  Scenario Outline: a catalogue that does not verify is never used
    Given a fetched catalogue that <problem>
    When roger refreshes the catalogue
    Then it is refused and not cached
    And roger keeps using the last catalogue that verified
    And the refusal names the reason

    Examples:
      | problem                                              |
      | carries no signature                                 |
      | is signed by a key that is not RogerAI's catalogue key |
      | was altered after it was signed                      |
      | is older than the catalogue already in use           |
      | is larger than the size roger will read              |

  @catalog
  Scenario: refreshing the catalogue is never something a command does silently in the way
    When the owner runs "roger model serve smollm2"
    Then a catalogue refresh, if one is due, never delays resolution beyond a short bound
    And a refresh that fails falls back to the cached catalogue without failing the command

  @catalog
  Scenario: the owner can see which catalogue is in use
    When the owner runs "roger model catalog"
    Then it states whether the catalogue in use is the built-in one or a fetched one
    And when it was published, and when it was fetched
    And how many entries it lists

  @offline
  Scenario: an airgapped machine never tries to fetch the catalogue
    Given roger is told it has no internet, or the catalogue address cannot be reached
    Then the built-in or cached catalogue is used
    And the model browser says the catalogue is the cached one and how old it is
    And nothing waits on the network to show it

  # =========================================================================
  # 3. A SHORT NAME RESOLVES TO EXACTLY ONE FILE
  # =========================================================================

  @names
  Scenario: a short name resolves to an exact file before anything happens
    When the owner runs "roger model serve qwen3-8b"
    Then roger names the repository, the file and its sha256 it resolved the name to
    And it states the quantisation it chose and the rule it chose it by
    And it asks before fetching, naming the byte count, as for any other fetch

  @names
  Scenario: the quantisation is chosen by the same stated rule as a repository's files
    Given "qwen3-8b" lists Q4_K_M, Q5_K_M and Q8_0 files
    When the owner runs "roger model serve qwen3-8b"
    Then roger chooses by the rule it already states for a repository
    And the rule is Q4_K_M when it fits comfortably, else the largest that fits comfortably, else the largest that fits
    And the choice is judged against this machine's measured memory at the context it would run

  @names
  Scenario: the owner can name the quantisation after the short name
    When the owner runs "roger model serve qwen3-8b:Q8_0"
    Then roger resolves to the Q8_0 file of that entry
    And a quantisation the entry does not list is refused, naming the ones it does

  @names
  Scenario: a short name for a model already on this disk serves it without fetching
    Given the file "qwen3-8b" resolves to is already on disk and its digest matches
    When the owner runs "roger model serve qwen3-8b"
    Then nothing is fetched
    And roger serves the file on disk

  @names
  Scenario: a file on disk that no longer matches its catalogue digest is not served under the name
    Given the file "qwen3-8b" resolves to is on disk but its digest differs from the catalogue's
    When the owner runs "roger model serve qwen3-8b"
    Then roger does not serve it under the catalogue name
    And it names the expected and the actual digest and offers to fetch the file again

  @names
  Scenario Outline: every command that takes a model accepts a short name
    When the owner runs "<command>"
    Then "qwen3-8b" resolves through the catalogue exactly as it does for serve

    Examples:
      | command                              |
      | roger model get qwen3-8b             |
      | roger model files qwen3-8b           |
      | roger model serve qwen3-8b --add     |
      | roger model serve qwen3-8b --on pi-1 |
      | roger model rm qwen3-8b              |

  @names
  Scenario: a member told to serve a short name receives the exact file, never the name
    When the owner runs "roger model serve qwen3-8b --on pi-1"
    Then the instruction sent to pi-1 names the exact repository, file and sha256
    And pi-1 fetches only that exact reference, as the approved rule for remote serving requires

  # =========================================================================
  # 4. FULL REFERENCES STILL WORK, AND NAMES NEVER SHADOW THEM
  # =========================================================================

  @names
  Scenario Outline: what the owner typed decides how it is resolved
    When the owner runs "roger model serve <ref>"
    Then it is resolved as <kind>

    Examples:
      | ref                                              | kind                                   |
      | ./models/my-model.gguf                           | a file path                            |
      | bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M      | a HuggingFace reference, the author's word |
      | bartowski/SmolLM2-135M-Instruct-GGUF             | a HuggingFace reference, quant by rule |
      | smollm2                                          | a catalogue short name                 |
      | smollm2:Q8_0                                     | a catalogue short name with a quantisation |

  @names
  Scenario: a model on disk the owner placed by hand keeps its own name
    Given a model the owner placed on disk is listed as "smollm2" by "roger model list"
    When the owner runs "roger model serve smollm2"
    Then roger serves the owner's own file
    And it says the catalogue also has a "smollm2" and how to serve that one instead

  @names
  Scenario: a full reference that matches a catalogue entry is still drawn as the author's word
    When the owner runs "roger model serve" with the exact repository and file a catalogue entry names
    Then roger serves it
    And it says this file is also in RogerAI's catalogue, so the measured memory is shown

  # =========================================================================
  # 5. NAMES THAT DO NOT RESOLVE
  # =========================================================================

  @names
  Scenario: an unknown name suggests the closest catalogue names
    When the owner runs "roger model serve qwen-8b"
    Then roger says no catalogue entry is called "qwen-8b"
    And it suggests up to three close names such as "qwen3-8b"
    And it says how to search the wider world: roger model search qwen-8b
    And nothing is fetched

  @names
  Scenario: a name that matches several entries is never guessed
    Given the catalogue lists "qwen3-8b" and "qwen3-8b-instruct"
    When the owner runs "roger model serve qwen3"
    Then roger lists both with their sizes and verdicts
    And asks the owner to name one
    And nothing is fetched

  @names
  Scenario: an unknown name is never sent to HuggingFace as a search on its own
    When the owner runs "roger model serve something-unknown"
    Then no request leaves the machine for that name
    And searching stays an explicit action the owner takes

  # =========================================================================
  # 6. NO ARGUMENT: ONE FITTING DEFAULT, BY A STATED RULE
  # =========================================================================

  @default
  Scenario Outline: with no name roger offers the starter for this machine's memory tier
    Given this machine has <usable> of usable memory
    When the owner runs "roger model serve" with no name
    Then roger offers the catalogue's starter for the <tier> tier
    And it states the rule: one starter per memory tier, and which tier this machine is in
    And it shows the model's size, its verdict on this machine and its licence
    And it asks before fetching or serving

    Examples:
      | usable  | tier             |
      | 6 GiB   | under 8 GiB      |
      | 16 GiB  | 8 to 24 GiB      |
      | 191 GiB | over 24 GiB      |

  @default
  Scenario: a starter that does not fit this machine is not offered as the default
    Given the starter for this machine's tier would not fit at the context it would run
    When the owner runs "roger model serve" with no name
    Then roger offers the next smaller tier's starter that fits, and says why
    And when none fits, it says so with the arithmetic and offers nothing

  @default
  Scenario: a machine whose memory cannot be read gets no guessed default
    Given roger cannot read this machine's memory
    When the owner runs "roger model serve" with no name
    Then roger says it cannot judge what fits, naming the missing reading
    And it lists the catalogue's starters with their needs instead of picking one

  @default
  Scenario: declining the default changes nothing
    When the owner runs "roger model serve" with no name and answers no
    Then nothing is fetched and nothing is served
    And roger says how to see the catalogue: roger model catalog

  @default
  Scenario: a non-interactive run with no name never fetches on its own
    Given no terminal is attached
    When roger runs "roger model serve" with no name and no --yes
    Then it prints the default it would choose and exits without fetching
    And with --yes it fetches and serves that default, saying so

  # =========================================================================
  # 7. THE BROWSER OPENS ON SUGGESTIONS, NOT AN EMPTY SEARCH
  # =========================================================================

  @tui
  Scenario: the model browser opens on catalogue models that fit this machine
    When the owner chooses "Get a model that fits this machine" in SHARE
    Then two or three catalogue models that fit are shown first, already judged
    And this machine's usable memory is stated above them
    And the starter for this machine's tier is marked as the suggestion
    And searching the wider world is one keystroke away and is not sent until chosen

  @tui
  Scenario: choosing a suggestion is the same consented fetch as the command
    When the owner chooses a suggested catalogue model
    Then the same consent line is shown as for "roger model serve <name>"
    And nothing is fetched until the owner confirms

  @tui
  Scenario: models that do not fit are never suggested, and the browser says when none fit
    Given no catalogue model fits this machine
    When the owner opens the model browser
    Then no suggestion is shown
    And the browser says none fit, with this machine's memory, and offers the search

  @tui
  Scenario: suggestions read without colour and fit the width
    When the browser is drawn at 80 columns under NO_COLOR
    Then each suggestion's name, source, size and verdict read in text alone
    And nothing overflows the width

  @web
  Scenario: the console's SHARE panel suggests the same models by the same rule
    When the owner opens the get-a-model view in the console
    Then it shows the same suggestions the terminal does, from the same code
    And choosing one is the same consented fetch

  # =========================================================================
  # 8. WHAT DOES NOT CHANGE
  # =========================================================================

  @catalog
  Scenario: the licence of a catalogue model is shown and recorded and stops nothing
    When the owner serves "qwen3-8b"
    Then its licence is shown before the fetch and recorded against the file
    And the licence never prevents fetching, serving or going on air

  @catalog
  Scenario: the runtime's own fit check still has the last word for a catalogue model
    Given the catalogue's measured memory says "qwen3-8b" fits
    And the runtime's own fit check on this machine says it does not
    When the owner serves "qwen3-8b"
    Then the runtime's answer wins and is said, as for any other model

  @catalog
  Scenario: the disk budget applies to catalogue models like any other fetch
    Given a disk budget is set
    When fetching "qwen3-8b" would exceed it
    Then roger says what it would remove first, before the fetch, as it does today
    And it never removes a model being served or one the owner placed by hand

  @catalog
  Scenario: a fetched catalogue file is verified against the catalogue's digest before it is served
    When the file for "qwen3-8b" finishes downloading
    Then its sha256 is checked against the digest the catalogue lists
    And a mismatch is never served, naming the expected and actual digest

  # =========================================================================
  # 9. ADVERSARIAL
  # =========================================================================

  @adversarial
  Scenario: a catalogue entry cannot point a short name at a file somewhere else
    Given a catalogue entry whose repository is not an absolute https address
    Then the entry is refused and the name does not resolve

  @adversarial
  Scenario: a cached catalogue altered on disk is not used
    Given the cached catalogue file was altered after it was fetched
    When roger reads the cache
    Then its signature no longer verifies and it is discarded
    And roger falls back to the built-in catalogue, saying so

  @adversarial
  Scenario: a repository that changes the file behind a catalogue name cannot change what is served
    Given the repository now serves different bytes under the file name the catalogue lists
    When the owner serves "qwen3-8b"
    Then the download fails the catalogue's digest and is not served

  @adversarial
  Scenario: a remote instruction cannot introduce a name the member's catalogue does not know
    Given an Edge instruction arrives naming a model only by a short name
    Then the member refuses it, because members fetch only an exact reference
