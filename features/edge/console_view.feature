# ROGER EDGE - THE CONSOLE'S EDGE TAB ("what is on my Edge, and how is it connected?" in a browser).
#
# WHY THIS EXISTS
#
# FOUNDER RULING 2026-09-11: Roger Edge is shown on the TUI AND on the web console for now, the
# Apple platforms later. 2026-09-13: "we need to do the same changes on the webui". The TUI's
# [3] EDGE screen exists (features/edge/topology_view.feature) and its session layer exists
# (features/edge/sessions.feature). This is the SAME screen, drawn in the console.
#
# THE CONSOLE IS A LIVE TWIN, NOT A SECOND IMPLEMENTATION. It drives the same fleet, the same
# candidate list, the same adopt path and the same session ledger the TUI holds, through the
# same hooks. Where the TUI arranges the graph, the console asks for the SAME arrangement over
# the API - one function, two windows - so the two can never disagree about which relay a node
# is drawn under. Everything the TUI spec forbids the console forbids too, and every rule below
# that has a TUI twin names it.
#
# THE RULE THE WHOLE TAB OBEYS
#
# IT NEVER DRAWS A LINK IT HAS NOT SEEN. A solid edge is a LAN-direct transport this instance
# verified; a dashed edge is a path through a relay, and the relay is drawn as its own node so
# the hop is visible rather than implied; a dark node is drawn dark, its edge broken, and it is
# KEPT. A pulse travels an edge only when that node was really heard from - the browser has no
# heartbeat channel, so "heard from" is a node's last-seen advancing between two reads, and
# nothing else starts one. Sessions are a WINDOW over receipts the relay already wrote: the tab
# can look, it cannot dial, and it cannot spend.
#
# BROWSER HONESTY. The console binds localhost and gates every /api call on the per-run token,
# and this tab adds no exception. It loads no third-party script for its drawing (the console
# ships everything it runs), it stays legible when colour is taken away (state is carried by
# stroke pattern and glyph, the accessibility twin of NO_COLOR), and it honours
# prefers-reduced-motion, because a pulse the viewer asked not to see is not information.
#
# GROUND TRUTH: internal/webui/server.go (routes, auth, action), internal/webui/assets/
# console.{html,js,css} (the tabs and the panel idiom), internal/tui/edge.go + edge_view.go
# (the arrangement, the empty state, the marks, the graph bound), internal/tui/
# edge_session_view.go (grouping, the outcome words), internal/edge (Fleet, Sessions),
# features/edge/topology_view.feature and features/edge/sessions.feature (the twins).
#
# Step definitions run the REAL console server (httptest) over a REAL internal/edge.Fleet on
# the REAL in-memory store, read the REAL JSON, and assert on the shipped console.html /
# console.js / console.css text for the browser rules. No mocks.

Feature: The console's EDGE tab draws the owner's fleet and its live sessions, and claims nothing the TUI would not

  Background:
    Given a console over a fleet of nodes

  # =========================================================================
  # 1. THE TAB
  # =========================================================================

  Scenario: EDGE is a tab of the console, between BROWSE and SETTINGS
    When the console shell is served
    Then the tab strip reads CHAT, SHARE, ACCOUNT, BROWSE, EDGE, SETTINGS in that order
    And there is a panel for EDGE hidden until its tab is chosen
    And the hash #edge opens it, like every other tab's hash

  Scenario: the shell still loads without a token, and the Edge data does not
    When the console shell is fetched with no token
    Then it is served, because it carries no node data
    When the Edge data is fetched with no token
    Then it is refused with 403
    When the Edge data is fetched with the wrong token
    Then it is refused with 403

  Scenario: the console serves its own drawing
    When the console shell is served
    Then it loads no script from outside the console
    And the Edge graph is drawn as inline SVG the console ships

  # =========================================================================
  # 2. THE READ IS ONE SNAPSHOT
  # =========================================================================

  Scenario: the Edge read is a single snapshot with self, the fleet, the candidates and the sessions
    Given nodes "shed" and "bench" on the Edge and a candidate "attic"
    When the Edge data is read
    Then it names this instance as self
    And it lists exactly "shed" and "bench" as nodes
    And it lists exactly "attic" as a candidate, outside the nodes
    And it carries the moment the snapshot was taken, so every age is measured from one clock
    And no node's private key material appears anywhere in it

  Scenario: the Edge read is read-only
    When the Edge data is requested with POST
    Then it is refused with 405
    And nothing about the fleet changed

  Scenario: a console with no Edge host says so instead of drawing an empty fleet
    Given a console built with no Edge wired
    When the Edge data is read
    Then it reports the Edge as not configured
    And the panel explains that this build has no Edge host, rather than showing an empty graph

  Scenario: a fleet that cannot be read is an error on screen, never an empty Edge
    Given the fleet store fails on read
    When the Edge data is read
    Then it is refused with 502 and the store's own words
    And the panel shows that message, not "the only node"

  Scenario: the arrangement is the TUI's, not a second one
    Given "bench" is reached only through the relay "shed", which is on the Edge
    When the Edge data is read
    Then "bench" is listed immediately under "shed" and names "shed" as the relay it is reached through
    And "shed" is flagged as a relay
    And that is the same order the TUI's Edge screen draws them in

  Scenario: a node reachable both ways is reported once, on its preferred transport
    Given "bench" has a LAN transport and a relay transport
    When the Edge data is read
    Then "bench" appears once
    And it is reported as LAN-direct, with no relay named

  Scenario: a relay chain and a relay cycle still list every member exactly once
    Given "a" names "b" as its relay and "b" names "a" as its relay
    When the Edge data is read
    Then both "a" and "b" are listed
    And each is listed exactly once

  Scenario: a dark node is reported dark and kept
    Given "bench" has not been heard from past the dark threshold
    When the Edge data is read
    Then "bench" is still listed
    And its presence is DARK and its last-seen age is carried

  Scenario: capability states travel with each node
    Given "bench" declares serve verified, sense declared and actuate unconfirmed
    When the Edge data is read
    Then "bench" carries all three capabilities with their states
    And an unverified capability names how it will be verified, as the TUI's detail does

  Scenario: a fleet too large to draw is flagged so the browser lists it instead
    Given 25 nodes on the Edge
    When the Edge data is read
    Then it says the graph is too large to draw
    And it still lists all 25 nodes
    And the panel renders the list, not a broken picture

  Scenario: the snapshot never names a relay it did not list
    Given "bench" names a relay "gone" that is not on the Edge
    When the Edge data is read
    Then "bench" is listed on its own relayed edge with no relay named
    And no node in the snapshot is reached through a name that is not in the snapshot

  # =========================================================================
  # 3. THE DRAWING (the browser's half, pinned on the shipped assets)
  # =========================================================================

  Scenario: this instance is drawn as the centre of the graph
    When the console shell is served
    Then the Edge graph places self at the centre and every node in relation to it

  Scenario: a LAN-direct peer is a solid edge, a relayed peer is a dashed edge through the relay, a dark node a broken one
    When the console shell is served
    Then a LAN-direct node is drawn with a solid stroke
    And a relayed node is drawn with a dashed stroke to its relay, and the relay is drawn as its own node
    And a dark node is drawn dim with a broken stroke, and it is kept

  Scenario: state is never carried by colour alone
    When the console shell is served
    Then live, relayed and dark differ by stroke pattern, not only by colour
    And a verified capability differs from a declared one by case, as it does in the terminal

  Scenario: an empty Edge explains itself instead of drawing an empty box
    Given this instance is the only node
    When the console shell is served
    Then the panel says this instance is the only node on the Edge
    And it says the screen draws only what it has seen
    And it says how to add a node: run RogerAI on another machine on this network, and adopt it

  Scenario: a long node name never breaks the drawing
    When the console shell is served
    Then a node label is clipped to its cell
    And the full name is still available on the node

  # =========================================================================
  # 4. THE ANIMATION IS EVIDENCE, NOT DECORATION
  # =========================================================================

  Scenario: a pulse starts only when a node was really heard from
    When the console shell is served
    Then a pulse is started only when a node's last-seen advanced since the previous read
    And nothing starts a pulse on a bare timer

  Scenario: a relayed node's pulse travels through the relay
    When the console shell is served
    Then a pulse for a relayed node runs along its edge to the relay and then the relay's edge to self

  Scenario: the animation stops when the tab is not visible
    When the console shell is served
    Then the Edge tab polls and animates only while it is the shown tab and the page is visible
    And leaving the tab stops both

  Scenario: the pulse honours prefers-reduced-motion
    When the console shell is served
    Then under prefers-reduced-motion a heartbeat is shown as a still mark on the node, not a travelling pulse
    And the mark still appears only on a real heartbeat

  # =========================================================================
  # 5. SELECTION AND DETAIL
  # =========================================================================

  Scenario: a node can be selected and described without leaving the tab
    When the console shell is served
    Then choosing a node opens its detail beside the graph
    And the detail carries id, kind, capabilities with their states, transports, presence with last-seen, pin and history

  Scenario: the detail of a relayed node names the relay it is reached through
    When the console shell is served
    Then a relayed node's detail names its relay

  Scenario: selection survives a fleet change
    When the console shell is served
    Then the selection is kept by node id across reads
    And a node arriving, going dark or being forgotten does not move it onto a different node

  Scenario: a candidate is drawn outside the fleet and its detail says it is not on your Edge
    When the console shell is served
    Then candidates are drawn in their own block, with no edge to self
    And a candidate's detail says it is not on your Edge and offers adopt

  # =========================================================================
  # 6. ADOPT IS THE TUI'S ADOPT
  # =========================================================================

  Scenario: adopting a candidate takes the same path as the TUI's a and roger edge adopt
    Given a candidate "attic"
    When the owner adopts "attic" from the console
    Then the adopt hook is called with the candidate's id and name
    And the next Edge read lists "attic" as a node and no longer as a candidate

  Scenario: adopt is a write and is gated like every write
    When adopt is requested with GET
    Then it is refused with 405
    When adopt is requested with no token
    Then it is refused with 403
    And no adopt hook was called

  Scenario: adopt refuses what is not a candidate
    When the owner adopts an id that is not a candidate
    Then it is refused with 404
    And no adopt hook was called

  Scenario: a failed adopt is shown in the fleet's own words
    Given adopting "attic" fails with "certificate does not match the advertised pin"
    When the owner adopts "attic" from the console
    Then it is refused with 502 and that message
    And "attic" is still a candidate

  Scenario: a console with no adopt wired says so
    Given a console built with a fleet but no adopt hook
    When the owner adopts "attic" from the console
    Then it is refused with 501 and says this build cannot adopt

  Scenario: the console never adopts on its own
    When the console shell is served
    Then adopt happens only on the owner's click
    And no read, poll or timer calls adopt

  # =========================================================================
  # 7. SESSIONS ARE A WINDOW OVER RECEIPTS
  # =========================================================================

  Scenario: an idle Edge carries no sessions
    When the Edge data is read
    Then the sessions list is empty
    And the panel says the Edge is quiet rather than drawing an empty table

  Scenario: a session appears only once traffic really flowed
    Given a receipted turn from a guest "opencode" against "gpt-oss-120b" served by "house-or-1"
    When the Edge data is read
    Then one session is listed, attributed to "opencode", band "gpt-oss-120b", station "house-or-1", outcome "served"

  Scenario: a session fades rather than accumulating
    Given a receipted turn 91 seconds ago
    When the Edge data is read
    Then no session is listed

  Scenario: a failed-over session shows both the station it left and the one that served
    Given a turn that left "house-cb-2" and was served by "house-cb-1"
    When the Edge data is read
    Then the session names "house-cb-2" as left and "house-cb-1" as station

  Scenario: a session on a relayed path names the relay, in order
    Given a turn relayed through "shed" and served by "bench"
    When the Edge data is read
    Then the session names "shed" as via and "bench" as station
    And the panel draws the path participant, relay, station in that order

  Scenario: an escalation is a good outcome in the browser too
    Given a board "gate-cam" that escalated a reading under contract "gate" with labels "open, closed, blocked"
    When the Edge data is read
    Then the session is marked escalate with outcome "escalate · right call"
    And it carries the contract's class and labels
    And the panel styles an escalation as a positive outcome, never as an error

  Scenario: a refusal says why
    Given a turn refused for "over-limit"
    When the Edge data is read
    Then the session's outcome reads "REFUSED · over-limit"

  Scenario: identical sessions are counted on one row, busiest first
    Given 5 identical receipted turns from "opencode" and 1 from "aider"
    When the Edge data is read
    Then the sessions are grouped into two rows
    And the "opencode" row comes first with a count of 5

  Scenario: a session of another account is never in the snapshot
    Given traffic belonging to a different account
    When the Edge data is read
    Then no session for it is listed
    And nothing about it is inferable from the snapshot

  Scenario: the console's own chat turn is a session attributed to the console
    Given the console relays a chat turn that returns a receipt
    Then a session attributed to "console" is recorded from that receipt
    And it names the band and the station the receipt names

  Scenario: a console turn that returned no receipt records no session
    Given the console relays a chat turn that fails
    Then no session is recorded

  Scenario: the Edge read cannot start a session
    Given the Edge data is read many times
    Then no turn was dispatched
    And the session ledger is unchanged

  # =========================================================================
  # 8. THE NODE SNAPSHOT IS UNTOUCHED
  # =========================================================================

  Scenario: the live node stream does not grow an Edge
    When the node state and the event stream are read
    Then neither carries fleet, candidate or session data
    And the Edge tab reads its own endpoint, while shown, at the stream's cadence
