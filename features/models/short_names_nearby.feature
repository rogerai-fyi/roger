# STATUS: APPROVED 2026-10-11 (founder)
# BUILD STATUS: BUILT 2026-10-11 on wt/simple-join. Every scenario is executable: the @tui ones by
#   internal/tui/short_names_nearby_bdd_test.go against the real SHARE screen, every other one by
#   cmd/rogerai/short_names_nearby_bdd_test.go with real Edge fleet records, a real pinned Tower and
#   the real console server, against a stand-in for the model hub's API (never the real hub).
#   How the runner reads three scenarios, said here so nobody assumes more: "a spoofed Edge member"
#   is a band on record that the serving probe never proved (an advertisement without a member
#   certificate never reaches the fleet record at all); "the Tower lab" is named by its address on
#   screen, as every Tower is; and "--on" sends the member the exact owner/repo:QUANT and its sha256,
#   which a member older than this build refuses as a malformed task rather than serving a name.
#
# SHORT NAMES COME FROM WHAT IS SERVED NEARBY, AND FALL BACK TO THE MODEL HUB - slice 1 of the
# first-run work.
#
# WHY. After serving a model by hand the founder asked: "is this only complicated because we are
# reusing cache? will it be easier for the end user client that is trying this for the first time,
# seems hard". The research (docs/simple-first-serve-research.md) found the hard part is the NAME: a
# first-time user has to type `bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M`.
#
# FOUNDER RULINGS 2026-10-11, which shape this whole file:
#   1. "suggest whatever is already on your LAN/Edge/market instead of a list; short names only for
#      those. A fresh machine with nothing nearby gets no suggestion."
#   2. "and fall back to hugging face": a name not served nearby is looked up on the model hub, and
#      the owner picks the exact repository; nothing guessed is ever fetched.
#
# WHAT THIS SUPERSEDES. A first draft of this file proposed a RogerAI-curated catalogue with a
# signing key and a starter list per memory tier. The approved-but-never-built catalogue in
# features/share/model_catalog.feature is SUPERSEDED BY RULING 1 BEFORE IT WAS BUILT, for its
# "signed, cached catalogue of offerable artifacts" clause only: there is no curated list and no
# catalogue key. Its other rules (consent before any byte moves, fit against real memory, checksum-
# verified and resumable acquisition, probe before announcing, stop what roger started, honest
# degradation) all stand and are what this file relies on. internal/catalog's Entry/Validate/Fit
# types stay useful as the shape of a resolved file; its Merge has no importer and stays unused.
#
# WHAT ALREADY EXISTS AND IS REUSED, never redesigned:
#   - features/serving/local_models.feature (APPROVED, built): hub search and fetch, the arithmetic
#     (`localmodel.Judge`, four verdicts, the sum), the quant rule (`localmodel.Suggest`: Q4_K_M when
#     it fits comfortably, else the largest that fits comfortably, else the largest that fits at
#     all), the runtime's own fit check winning once the file is on disk, the disk budget, the
#     licence shown and recorded and never gating, ours-vs-the-author's-word.
#   - The serving records that already say WHICH file is behind a band:
#       * this machine: `localmodel.Serving` {Band, ID (repo), File, SHA256, Quant, Ctx, Runtime};
#       * an Edge member: its fleet/instance record (`store.EdgeServed` {Band, ID, Quant, Ctx}) plus
#         the serve record it reports; the digest is added to what a member reports (this slice);
#       * a pinned LAN Tower: its /discover offers carry the band only today; a station's self-
#         reported repo, file and digest are added to its offer (this slice), labelled as that
#         station's word.
#       * a market station: /discover carries the band; no file mapping (see DECISION 1).
#   - The LAN dial (`client.LANDial`) already merges this machine, Edge members and pinned Towers,
#     with the source named per band. That is where nearby names come from.
#   - cmd/rogerai/modelcmd.go `modelResolve`: a .gguf path, then a name on disk (`Home.Find`), then a
#     hub reference `owner/repo[:QUANT]`. This file inserts "a name served nearby" and "the hub
#     fallback" into that order (section 4).
#
# NOT IN THIS SLICE (each is its own later slice; noted so nobody assumes it was done): quiet output
# (re-approves local_models.feature:363), a station invite naming the model to serve, a local run
# verb, `roger share` suggesting roger's own serve, and an Edge member handing a file over directly.
#
# =============================================================================================
# DECISIONS FOR THE FOUNDER
# =============================================================================================
#
# RULED 2026-10-11, DECISION 1 (as recommended) (which sources may DEFINE a name-to-file mapping). A mapping decides which bytes get
#   fetched, so it is a trust question:
#     - this machine's own serve records: yes, they are the owner's;
#     - Edge members: yes, they are proven the owner's (member certificates under the owner's root);
#     - pinned LAN Towers: a pinned Tower's stations may not be the owner's, but the Tower itself was
#       chosen and pinned by the owner, and the file is still checked against a digest;
#     - market stations: strangers.
#   RECOMMEND: this machine, Edge members and pinned Towers may define a mapping; market bands are
#   SHOWN for discovery ("also on the market") but NEVER used to pick a file. A Tower-sourced
#   mapping is labelled as that station's word, never as the owner's.
#
# RULED 2026-10-11, DECISION 2 (as recommended) (do market bands count as "nearby" names at all). RECOMMEND: they appear in suggestions
#   only as names, marked "on the market", and choosing one goes to the hub fallback (section 6) to
#   find the file, never to the market station's claim.
#
# RULED 2026-10-11, DECISION 3 (as recommended) (the order when a name is both nearby and on the hub). RECOMMEND: nearby first (it is
#   what the owner's own machines already run), the hub only when nothing nearby serves the name or
#   the owner asks for it (`--hub`).
#
# RULED 2026-10-11, DECISION 4 (as recommended) (the hub fallback's ranking, for a name and for no name). RECOMMEND: GGUF repositories
#   matching the words, that have a file which fits this machine by the existing rule, ordered by the
#   hub's own download count, top 3 shown. The rule is stated in the output. For no name: the 3
#   most-downloaded chat GGUFs with a file that fits comfortably.
#
# RULED 2026-10-11, DECISION 5 (as recommended) (an Edge member handing the file over directly, LATER slice). Today every fetch goes to
#   the hub, so an airgapped LAN can discover a name but not fetch the file. A member could serve its
#   verified file to another member over the existing mTLS face. RECOMMEND a later slice: digest-
#   verified, members only, never from a Tower or the market.
#
# RULED 2026-10-11, DECISION 6 (as recommended) (the run verb, LATER slice, noted only): `roger run <name>` recommended; nothing here
#   depends on it.
#
# RULED 2026-10-11, DECISION 7 (as recommended) (quiet output, NEXT slice, noted only): four lines with the total kept, full sum behind
#   --verbose, re-approving local_models.feature:363. This file asserts FACTS, never line counts.
#
# RULED 2026-10-11, DECISION 8 (as recommended) (station invite names the model, LATER slice, noted only): `--serve <name>` recommended.
#
# =============================================================================================
# APPROVED SCENARIOS THIS WOULD SUPERSEDE (not edited here; proposed text in brackets)
# =============================================================================================
#
#   features/serving/local_models.feature:581 "getting a model is always the last row of SHARE": its
#     line "choosing it opens the search and the fit view inside SHARE" becomes [choosing it opens
#     the bands served nearby that fit this machine, each with its source, or the search when nothing
#     nearby fits; the search is one keystroke away inside SHARE]. The rest stands.
#   features/share/model_catalog.feature: the "signed, cached catalogue of offerable artifacts" scope
#     clause, and the scenarios that need a RogerAI-published entry ("A catalogue entry whose artifact
#     is not public is never offered", "The catalogue never invents a measurement", "A stale cached
#     catalogue is used but labelled", "A catalogue that cannot be fetched never breaks discovery").
#     [superseded 2026-10-11 by founder ruling: names come from what is served nearby and the model
#     hub; there is no RogerAI catalogue]. Every other scenario there stands.
#
# Tags: @nearby, @trust, @hub, @names, @default, @tui, @web, @offline, @adversarial.

Feature: A person can serve a model by the name it already has nearby, or find it on the model hub
  A first-time user should be able to type a name they already see on their own machines, learn
  exactly which file that name is and whose word it is, say yes once, and be serving. When nothing
  nearby has the name, roger looks it up on the model hub and lets them pick. Nothing about consent,
  the fit check, the digest check or the licence line gets weaker to make that short.

  Background:
    Given a machine whose usable memory roger can measure

  # =========================================================================
  # 1. WHAT COUNTS AS NEARBY, AND WHAT A NEARBY NAME CARRIES
  # =========================================================================

  @nearby
  Scenario: nearby names are the bands served on this machine, Edge members and pinned Towers
    Given this machine serves "qwen3-8b"
    And Edge member "atlas" serves "gpt-oss-20b"
    And the pinned LAN Tower "lab" has a station serving "smollm2-135m-instruct"
    When the owner runs "roger model nearby"
    Then all three names are listed, each with its source and whose word the mapping is
    And each shows the repository, quantisation and size its source says it is serving

  @nearby @trust
  Scenario: a market band is shown for discovery but never defines a file
    Given a market station advertises "llama-3.1-8b"
    When the owner runs "roger model nearby"
    Then "llama-3.1-8b" is listed as "on the market"
    And no repository or file is shown for it
    And choosing it looks the name up on the model hub instead

  @nearby
  Scenario: a fresh machine with nothing nearby gets no nearby suggestion
    Given no band is served on this machine, by an Edge member or by a pinned Tower
    When the owner runs "roger model nearby"
    Then roger says nothing is served nearby
    And it says how to find a model: roger model search <words>, or roger model serve <name>

  @nearby @trust
  Scenario Outline: whose word a mapping is depends on its source
    Given the name "qwen3-8b" is served by <source>
    When the owner asks which file "qwen3-8b" is
    Then the mapping is labelled as <label>

    Examples:
      | source                       | label                                   |
      | this machine                 | this machine's own serve                |
      | Edge member "atlas"          | your Edge member atlas                  |
      | a station on pinned Tower "lab" | a station on the Tower lab (its word)  |

  @nearby
  Scenario: an Edge member reports the digest of the file it serves
    Given Edge member "atlas" serves "qwen3-8b"
    Then the record atlas reports names the repository, file, quantisation and sha256 it serves

  @nearby
  Scenario: a station on a pinned Tower reports what it serves, as its own word
    Given a station joined to the pinned Tower "lab" serves "smollm2-135m-instruct"
    Then the Tower's offer names the repository, file, quantisation and sha256 the station reported
    And the Tower marks those fields as the station's report

  # =========================================================================
  # 2. SERVING A NAME THAT IS SERVED NEARBY
  # =========================================================================

  @names
  Scenario: the mapping is shown before anything is fetched
    Given Edge member "atlas" serves "qwen3-8b" from a known repository, quantisation and digest
    When the owner runs "roger model serve qwen3-8b"
    Then roger says which file "qwen3-8b" is, as served by atlas
    And it states the byte count and this machine's fit verdict for that file
    And it asks before fetching, naming the source of the bytes as the model hub

  @names
  Scenario: the file named by the mapping is fetched from the hub and checked against the source's digest
    Given the owner confirms serving "qwen3-8b" as served by atlas
    When the file finishes downloading
    Then its sha256 is checked against the digest atlas reported
    And a mismatch is never served, naming the expected and actual digest

  @names
  Scenario: the quantisation is the source's, unless it does not fit here
    Given atlas serves "qwen3-8b" at Q8_0
    And Q8_0 of that repository does not fit this machine
    When the owner runs "roger model serve qwen3-8b"
    Then roger says atlas's Q8_0 will not fit here, with the arithmetic
    And it offers the quantisation of the same repository that the existing rule picks for this machine
    And it says that file is not the one atlas serves, so its digest comes from the hub listing

  @names
  Scenario: the owner can name the quantisation after the short name
    When the owner runs "roger model serve qwen3-8b:Q4_K_M"
    Then roger resolves to that quantisation of the repository nearby sources name for "qwen3-8b"
    And a quantisation the repository does not publish is refused, naming the ones it does

  @names
  Scenario: a name already on this disk under the same digest serves without fetching
    Given the file a nearby source maps "qwen3-8b" to is already on this disk with a matching digest
    When the owner runs "roger model serve qwen3-8b"
    Then nothing is fetched
    And roger serves the file on disk

  @names
  Scenario: a model the owner placed by hand keeps its own name
    Given a model the owner placed on disk is listed as "qwen3-8b" by "roger model list"
    When the owner runs "roger model serve qwen3-8b"
    Then roger serves the owner's own file
    And it says a nearby source also serves a "qwen3-8b" and how to serve that one instead

  @names
  Scenario Outline: every command that takes a model accepts a nearby name
    When the owner runs "<command>"
    Then "qwen3-8b" resolves through the nearby mapping exactly as it does for serve

    Examples:
      | command                              |
      | roger model get qwen3-8b             |
      | roger model files qwen3-8b           |
      | roger model serve qwen3-8b --add     |
      | roger model serve qwen3-8b --on pi-1 |

  @names
  Scenario: a member told to serve a nearby name receives the exact file, never the name
    When the owner runs "roger model serve qwen3-8b --on pi-1"
    Then the instruction sent to pi-1 names the exact repository, file and sha256
    And pi-1 fetches only that exact reference, as the approved rule for remote serving requires

  # =========================================================================
  # 3. CONFLICTS ARE NEVER GUESSED
  # =========================================================================

  @names
  Scenario: two nearby sources mapping one name to different files are both shown
    Given atlas maps "qwen3-8b" to one repository and file
    And a station on the Tower "lab" maps "qwen3-8b" to a different repository or file
    When the owner runs "roger model serve qwen3-8b"
    Then roger lists both mappings with their sources, sizes and verdicts
    And it asks the owner to choose one by number
    And nothing is fetched until the owner chooses

  @names
  Scenario: two sources that agree on the same file are one choice
    Given atlas and pi-1 both serve "qwen3-8b" from the same repository, file and digest
    When the owner runs "roger model serve qwen3-8b"
    Then roger shows one mapping, naming both sources

  @names
  Scenario: a conflict in a non-interactive run is refused, never resolved
    Given two nearby sources map "qwen3-8b" to different files
    And no terminal is attached
    When roger runs "roger model serve qwen3-8b --yes"
    Then it refuses, listing both mappings
    And it says how to name one exactly: the full repository reference

  # =========================================================================
  # 4. HOW WHAT THE OWNER TYPED IS RESOLVED
  # =========================================================================

  @names
  Scenario Outline: the order roger resolves a name in
    When the owner runs "roger model serve <ref>"
    Then it is resolved as <kind>

    Examples:
      | ref                                         | kind                                          |
      | ./models/my-model.gguf                      | a file path                                   |
      | bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M | a hub reference, the author's word            |
      | qwen3-8b, already on this disk              | the file on disk                              |
      | qwen3-8b, served nearby                     | the nearby mapping, shown before fetching     |
      | qwen3-8b, nowhere nearby                    | a hub lookup the owner picks from (section 5) |

  @names
  Scenario: the owner can skip nearby sources and go straight to the hub
    When the owner runs "roger model serve qwen3-8b --hub"
    Then roger does not use any nearby mapping
    And it looks "qwen3-8b" up on the model hub as in section 5

  # =========================================================================
  # 5. THE HUB FALLBACK: A NAME NOT SERVED NEARBY
  # =========================================================================

  @hub
  Scenario: a name not served nearby is looked up on the model hub and the owner picks
    Given nothing nearby serves "qwen3-8b"
    When the owner runs "roger model serve qwen3-8b"
    Then roger says "qwen3-8b" is not served nearby and it is looking on the model hub
    And it lists up to three matching repositories with their size, licence and download count
    And each shows the quantisation the existing rule would pick for this machine and its verdict
    And it states the ranking rule
    And it asks the owner to pick one by number or answer no
    And nothing is fetched until the owner picks

  @hub
  Scenario: typing the name is the explicit action that sends it to the hub
    When the owner runs "roger model serve qwen3-8b" and nothing nearby serves it
    Then the name is sent to the model hub because the owner asked to serve it
    And opening a screen never sends a search on its own, as the approved rule requires

  @hub
  Scenario: a single clear match is still confirmed, never fetched on its own
    Given the hub returns exactly one repository matching "qwen3-8b"
    When the owner runs "roger model serve qwen3-8b"
    Then roger shows that repository with its size, licence, quantisation and verdict
    And it asks yes or no
    And nothing is fetched until the owner says yes

  @hub
  Scenario: the hub pick is fetched under the digest the listing gave
    Given the owner picked a repository and file from the hub listing
    Then the sha256 the listing gave for that file is pinned
    And the downloaded file is checked against that pinned digest before it is served

  @hub
  Scenario: the hub listing is the author's word, and is drawn that way
    When roger lists hub matches
    Then each is marked as its author's word, not RogerAI's and not a nearby source's

  @hub
  Scenario: a hub repository that cannot be served is listed with the reason and never picked
    Given a matching repository has no servable weights, or an architecture this runtime cannot load
    Then it is listed as unservable with the specific reason
    And it cannot be chosen

  @hub
  Scenario: no match on the hub is said plainly
    Given the hub returns no repository matching "qwen3-8b"
    When the owner runs "roger model serve qwen3-8b"
    Then roger says nothing nearby and nothing on the model hub matches
    And it suggests: roger model search <other words>

  @hub
  Scenario: a non-interactive run never picks from the hub
    Given nothing nearby serves "qwen3-8b"
    And no terminal is attached
    When roger runs "roger model serve qwen3-8b --yes"
    Then it refuses, listing the matches with their exact references
    And it says how to name one exactly: roger model serve <owner/repo>:<quant>

  @hub @offline
  Scenario: with no internet, the hub fallback says so instead of failing vaguely
    Given nothing nearby serves "qwen3-8b"
    And the model hub cannot be reached
    When the owner runs "roger model serve qwen3-8b"
    Then roger says the name is not served nearby and the model hub cannot be reached
    And it lists what is served nearby instead

  # =========================================================================
  # 6. NO NAME: SUGGESTIONS FROM NEARBY, THEN THE HUB
  # =========================================================================

  @default
  Scenario: with no name roger suggests what is served nearby that fits this machine
    Given Edge member "atlas" serves "qwen3-8b" and "gpt-oss-20b"
    And only "qwen3-8b" fits this machine
    When the owner runs "roger model serve" with no name
    Then roger suggests "qwen3-8b" as served by atlas, with its size and verdict
    And it does not suggest "gpt-oss-20b", and says it would not fit here
    And it asks the owner to pick or answer no

  @default
  Scenario: with nothing nearby, roger suggests a few hub models that fit, by a stated rule
    Given nothing is served nearby
    When the owner runs "roger model serve" with no name
    Then roger says nothing is served nearby
    And it lists up to three hub models that fit this machine comfortably
    And it states the rule: the most-downloaded chat GGUFs on the model hub with a file that fits
    And each is marked as its author's word
    And it asks the owner to pick or answer no

  @default
  Scenario: with nothing nearby and no internet, roger suggests nothing
    Given nothing is served nearby
    And the model hub cannot be reached
    When the owner runs "roger model serve" with no name
    Then roger says nothing is served nearby and the model hub cannot be reached
    And it suggests nothing

  @default
  Scenario: a machine whose memory cannot be read gets no guessed suggestion
    Given roger cannot read this machine's memory
    When the owner runs "roger model serve" with no name
    Then roger says it cannot judge what fits, naming the missing reading
    And it lists what is nearby with each one's needs instead of picking one

  @default
  Scenario: a non-interactive run with no name never fetches
    Given no terminal is attached
    When roger runs "roger model serve" with no name, with or without --yes
    Then it prints what it would suggest and exits without fetching

  # =========================================================================
  # 7. THE BROWSER OPENS ON NEARBY BANDS
  # =========================================================================

  @tui
  Scenario: the model browser opens on nearby bands that fit this machine
    Given nearby sources serve bands that fit this machine
    When the owner chooses "Get a model that fits this machine" in SHARE
    Then those bands are shown first, each with its source, size and verdict
    And this machine's usable memory is stated above them
    And searching the model hub is one keystroke away and is not sent until chosen

  @tui
  Scenario: with nothing nearby the browser opens on the search
    Given nothing is served nearby
    When the owner opens the model browser
    Then it says nothing is served nearby
    And the search is ready to type into, not yet sent

  @tui
  Scenario: choosing a nearby band is the same consented fetch as the command
    When the owner chooses a nearby band in the browser
    Then the same mapping and consent line is shown as for "roger model serve <name>"
    And nothing is fetched until the owner confirms

  @tui
  Scenario: suggestions read without colour and fit the width
    When the browser is drawn at 80 columns under NO_COLOR
    Then each suggestion's name, source, size and verdict read in text alone
    And nothing overflows the width

  @web
  Scenario: the console's SHARE panel suggests the same bands by the same rule
    When the owner opens the get-a-model view in the console
    Then it shows the same nearby suggestions the terminal does, from the same code
    And choosing one is the same consented fetch

  # =========================================================================
  # 8. WHAT DOES NOT CHANGE
  # =========================================================================

  Scenario: the licence is shown and recorded and stops nothing
    When the owner serves a model resolved from a nearby name or the hub
    Then its licence is shown before the fetch and recorded against the file
    And the licence never prevents fetching, serving or going on air

  Scenario: the runtime's own fit check still has the last word
    Given roger's estimate says the resolved file fits
    And the runtime's own fit check on this machine says it does not
    When the owner serves it
    Then the runtime's answer wins and is said, as for any other model

  Scenario: the disk budget applies to every fetch
    Given a disk budget is set
    When fetching the resolved file would exceed it
    Then roger says what it would remove first, before the fetch, as it does today
    And it never removes a model being served or one the owner placed by hand

  @offline
  Scenario: nearby discovery works on a LAN with no internet
    Given the machine has no internet but Edge members and a pinned Tower are reachable
    When the owner runs "roger model nearby"
    Then the nearby bands are listed
    And serving one says the file must be fetched from the model hub, which cannot be reached now

  # =========================================================================
  # 9. ADVERSARIAL
  # =========================================================================

  @adversarial @trust
  Scenario: a market station claiming a popular name can never choose the file
    Given a market station advertises "qwen3-8b" and claims a repository of its choosing
    And nothing else nearby serves "qwen3-8b"
    When the owner runs "roger model serve qwen3-8b"
    Then the market station's claim is ignored for choosing a file
    And roger goes to the hub fallback and the owner picks from the hub listing

  @adversarial @trust
  Scenario: a spoofed Edge member cannot define a mapping
    Given an advertisement on the LAN claims to be an Edge member serving "qwen3-8b"
    But it does not present a member certificate under the owner's root
    Then its mapping is not used and its band is not listed as nearby

  @adversarial @trust
  Scenario: a Tower whose pin no longer matches cannot define a mapping
    Given the Tower "lab" answers with a key that does not match its pin
    Then its stations' bands are not listed as nearby and their mappings are not used

  @adversarial
  Scenario: a name whose mapping changes between suggestion and fetch is asked again
    Given the owner was shown "qwen3-8b" as one file
    And before the fetch starts, the source now maps "qwen3-8b" to a different file
    Then roger does not fetch the new file under the old consent
    And it shows the new mapping and asks again

  @adversarial
  Scenario: a source whose reported digest does not match the hub's file is never served
    Given atlas reports a digest for "qwen3-8b" that the downloaded file does not match
    Then the file is not served
    And roger names the expected and actual digest and which source reported the expected one

  @adversarial @hub
  Scenario: a lookalike repository is never preferred for looking like the name
    Given the hub lists "qwen3-8b" from its original publisher and a lookalike name from another author
    Then both are shown with their authors, download counts and licences
    And roger never picks one for the owner
    And the ranking rule is applied as stated, not by name similarity alone

  @adversarial @hub
  Scenario: a hub file that changes between listing and fetch is not served
    Given the owner picked a file whose sha256 the listing gave
    And the repository now serves different bytes under that file name
    When the download finishes
    Then it fails the pinned digest and is not served

  @adversarial @hub
  Scenario: a huge download is confirmed by size before any byte moves
    Given the picked file is larger than this machine's free disk or much larger than the others listed
    Then roger states the byte count and the free space before asking
    And a file that cannot fit on disk is refused before the fetch, with the numbers

  @adversarial
  Scenario: a remote instruction carrying only a name is refused
    Given an Edge instruction arrives naming a model only by a short name
    Then the member refuses it, because members fetch only an exact reference

  # founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule
  Scenario: two quantisations of one repository on disk are picked by the quantisation rule
    Given "smollm2-135m-instruct" is on this disk at Q4_K_M and at Q8_0 of one repository
    When the owner runs "roger model serve smollm2-135m-instruct"
    Then roger serves the Q4_K_M file, the one the quantisation rule picks for this machine
    And it says it chose Q4_K_M as the best fit here, that Q8_0 is also on disk, and how to pick it
    And nothing is downloaded from the model hub

  # founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule
  Scenario: --quant picks another quantisation already on disk
    Given "smollm2-135m-instruct" is on this disk at Q4_K_M and at Q8_0 of one repository
    When the owner runs "roger model serve smollm2-135m-instruct --quant Q8_0"
    Then roger serves the Q8_0 file
    And nothing is downloaded from the model hub

  # founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule
  Scenario: get of a name with two quantisations on disk fetches nothing
    Given "smollm2-135m-instruct" is on this disk at Q4_K_M and at Q8_0 of one repository
    When the owner runs "roger model get smollm2-135m-instruct"
    Then roger says it is already on this machine, naming the file the rule picks and the other quantisation
    And the command succeeds
    And nothing is downloaded from the model hub

  # founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule
  Scenario: get of a name already on disk once fetches nothing
    Given "smollm2-135m-instruct" is on this disk at Q4_K_M only
    When the owner runs "roger model get smollm2-135m-instruct"
    Then roger says "already on this machine: bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M"
    And the command succeeds
    And nothing is downloaded from the model hub

  # founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule
  Scenario: --quant naming a quantisation not on disk fetches it from that repository
    Given "smollm2-135m-instruct" is on this disk at Q4_K_M only
    When the owner runs "roger model get smollm2-135m-instruct --quant Q8_0 --yes"
    Then the Q8_0 file of that repository is fetched and on this disk

  # founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule
  Scenario: one name from two different repositories on disk is never picked for the owner
    Given "smollm2-135m-instruct" is on this disk from two different repositories
    When the owner runs "roger model serve smollm2-135m-instruct" and picks 2
    Then both repositories are listed, numbered
    And roger serves the file from the second repository listed

  # founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule
  Scenario: a scripted run never picks between two repositories on disk
    Given "smollm2-135m-instruct" is on this disk from two different repositories
    And no terminal is attached
    When the owner runs "roger model serve smollm2-135m-instruct"
    Then roger refuses, listing both, and serves nothing
