# STATUS: APPROVED 2026-10-11 (founder)
# BUILD STATUS: BUILT 2026-10-11 (internal/look, internal/tui/welcome.go, cmd/rogerai/onboard.go, internal/webui/welcome.go + console-welcome.js;
#   runners: internal/look/look_test.go, internal/tui/welcome_card_bdd_test.go, cmd/rogerai/welcome_card_bdd_test.go, internal/webui/welcome_console_bdd_test.go).
#
# WELCOME CARD: the pre-TUI welcome picker (cmd/rogerai/onboard.go runWizard: "Welcome to
# RogerAI - a two-way radio for Local Models ... Just use models / Share FREE / Share earn")
# is replaced by a ONE-TIME card drawn INSIDE the TUI, the first time it opens. Each line names
# one thing roger does, in one line, and choosing it lands on that part of the app. Nothing is
# configured by the card itself: it only takes you somewhere.
#
# The founder picked this preview (2026-10-11). Its numbers become the TUI's real keys (see
# DECISION 1), so the card teaches the keys the user keeps using:
#
#   ROGER·AI  first time here? pick one (esc to skip)
#
#   1  Chat with a model     pick a model and talk
#   0  Run an agent          let a model read, write, run code
#   2  Share a model         put a model on air (free or paid)
#   3  Edge network          join your own devices together
#   w  Web console           open the same app in a browser
#   l  Log in                starter credit for new accounts; earn by sharing
#
#   you can always get here again with ?
#
# GROUND TRUTH (read 2026-10-11):
#   cmd/rogerai/main.go: no-args launch plays the boot once per version, then maybeOnboard(cfg)
#     runs the huh wizard before tuiHooks / NewController. cfg.Onboarded marks "done".
#   cmd/rogerai/onboard.go: runWizard (Keep/Modify/Reset rerun, consume/free/earn intent),
#     applyIntent, finishShare (detectFull, guidedUpstream fallback, netbind.SuggestPort, earn
#     path discloses login + payout terms, collects prices), cmdOnboard (--free/--earn/--yes/
#     --reset), interactive() = stdin+stdout are TTYs.
#   internal/tui: sections [0] AGENT, [1] TUNE IN (the dial), [2] SHARE, [3] EDGE, [4] CONFIG,
#     [?] help (modeHelp; `?` opens it from the dial and SHARE, esc/q/enter/? close it); `w`
#     opens the web console (the same helper /webui uses); `l` runs login (doLogin);
#     modeShareSetup is the guided "no local model detected" fallback; `m` toggles the
#     windowshade (compact); NO_COLOR folds glyphs.
#   Starter credit: the broker seeds new wallets ROGERAI_SEED_CREDITS (currently $1) once, and
#     only the first ROGERAI_SEED_LIMIT (default 1000) wallets are seeded (cmd/rogerai-broker
#     main.go runServe; store SeedOnce). So "$1, always" is not a promise the code keeps.
#
# RULED 2026-10-11 (founder approved every recommendation):
#   1. Card keys. The preview numbered 1-6, but 1/2/3 already switch sections and the agent is
#      [0]. A card that says "2 Run an agent" while [2] is SHARE everywhere else would teach the
#      wrong keys. RULED 2026-10-11: the card shows the REAL keys (1 dial, 0 agent, 2 share, 3 edge,
#      w console, l login) and arrows + enter also work.
#   2. Reopening. `?` is already the help screen. RULED 2026-10-11: `?` keeps opening help, and the
#      help screen gains the card as its first section ("start here"), so "get here again with
#      ?" stays true without a second key. (Alternative: a new key such as `W`; not recommended.)
#   3. Starter-credit wording. RULED 2026-10-11 (amended by R3 below): the card line carries NO
#      amount, never a fixed "$1".
#   4. Who sees it. RULED 2026-10-11: only a config with neither onboarded=true nor a saved share, and
#      a new welcome_seen flag. Existing users (onboarded or sharing) never see it: no
#      migration prompt, the flag is simply set on their next save.
#   5. `roger onboard`. RULED 2026-10-11: keep the Keep/Modify/Reset share setup there (it is the
#      scripted/explicit share setup), and add `roger onboard --welcome` to show the card on the
#      next TUI launch. --free/--earn/--yes/--reset keep working unchanged.
#   6. Web console. RULED 2026-10-11: the console shows the same one-time card (same six lines, same
#      destinations), sharing the same welcome_seen flag, so seeing it in either front-end
#      counts for both.
#
#   7. Look. RULED 2026-10-11: the card is built from internal/fleetview's components and palette, the
#      limoni look the founder approved for the Edge Health and Fleet screens (2026-09-26): a
#      muted eyebrow, paper key caps, a red selection rail, one glyph per line in the one red,
#      prose in ink, descriptions muted, and the fleetview action bar at the foot. (Amended by the
#      plan review below: the palette, glyphs and key-cap / eyebrow / rule / action-bar helpers
#      live in a shared internal/look package; internal/fleetview uses it for its limoni spans and
#      internal/tui renders the card natively with lipgloss, under the big ROGER·AI lockup (kept,
#      founder rule). limoni is not embedded in the TUI.) A rendered mockup with the real
#      palette: docs/welcome-and-empty-edge-mockups.md and docs/welcome-card-mockup.png.
#   8. Glyphs. RULED 2026-10-11: ◉ chat, ▸ agent, ((•)) share, ▟•▙ edge, ◇ console, ◈ login, each in the
#      one red; under NO_COLOR they fold the way the TUI already folds its glyphs.
#
# RULED 2026-10-11 (founder, on the fresh-context plan review):
#   R1. The enrolled [3] EDGE view is NOT restyled in this slice; only the empty state and this
#       card get the new look (follow-up outlined in docs/welcome-and-empty-edge-mockups.md).
#   R2. The card MAY use the Health/Fleet palette: the [3] colour exception extends to the card.
#   R3. The login line carries no amount: "starter credit for new accounts; earn by sharing". The
#       login panel says a starter credit is offered only while the broker's public GET /promo
#       reports it active, and never names an amount.
#   Review fixes: eyebrows are muted (amber only where fleetview uses it); l on the card is the
#   dial's [L] panel (a logout confirm when logged in), and a logged-in first launch shows
#   "Log out" on that line; welcome_seen is written only through a SaveWelcomeSeen hook that
#   writes only that field; w on the card is exactly the dial's w (openConsole); `?` help's
#   "start here" is the same six lines; under 70 columns the description column is dropped;
#   the card never pushes its action bar off a 20-row terminal; NO_COLOR folds every new glyph
#   through glyphs.Fold (▌ to >, key caps to [k]).
#
# SUPERSEDES (approved, not edited here; proposed text):
#   features/onboarding/onboarding.feature:39 "First run onboards, but the app still works with
#     defaults": "Then a first-run setup is offered" ->
#     "Then the TUI opens with a one-time welcome card (features/onboarding/welcome_card.feature)"
#     and "And declining still leaves a working app on sensible defaults" ->
#     "And skipping the card with esc leaves a working app on sensible defaults".
#   onboarding.feature header lines 20-29 (scenario 1 is "the first-run CLI wizard"; applyIntent
#     drives "consume vs share"): scenario 1 becomes the in-TUI card; applyIntent is retired;
#     applyRerunChoice (scenario 6, `roger onboard`) stays.
#   web/src/manual.html: the first-run section quoting the old picker is rewritten to the card.

Feature: A one-time welcome card inside the TUI

  Background:
    Given roger is launched with no arguments on an interactive terminal

  # 1. When it shows

  Scenario: a brand-new install opens the TUI with the welcome card
    Given a config with no onboarded flag, no saved share and no welcome_seen flag
    When the TUI opens
    Then the welcome card is drawn over the dial, under the ROGER·AI brand
    And no pre-TUI picker runs before it

  Scenario: the card is shown once
    Given the welcome card was shown and closed
    When roger is launched again
    Then the TUI opens on the dial with no card

  Scenario Outline: closing the card in any way marks it seen
    Given the welcome card is open
    When the user <closes it>
    Then welcome_seen is saved as true
    And the card is not shown on the next launch

    Examples:
      | closes it                    |
      | presses esc                  |
      | picks a line with its key    |
      | picks a line with enter      |
      | presses q                    |

  Scenario: a user who already onboarded never sees the card
    Given a config with onboarded true
    When the TUI opens
    Then no welcome card is drawn
    And welcome_seen is saved as true on the next config save

  Scenario: a user who already shares never sees the card
    Given a config with a saved share and no onboarded flag
    When the TUI opens
    Then no welcome card is drawn

  Scenario: a non-interactive run never shows the card
    Given stdout is not a terminal
    When roger is launched with no arguments
    Then no welcome card is drawn and nothing waits for a key

  Scenario: a scripted onboarding never shows the card
    Given the user ran `roger onboard --free --yes` before the first TUI launch
    When the TUI opens
    Then no welcome card is drawn

  Scenario: the boot animation plays before the card, not over it
    Given a first-ever launch where the version boot plays
    When the boot finishes
    Then the TUI opens and only then is the welcome card drawn

  Scenario: the card never blocks the app if the config cannot be saved
    Given the config file cannot be written
    When the user closes the card
    Then the TUI keeps working on the dial
    And the status line says the welcome choice could not be remembered

  # 2. What it says

  @tui
  # corrected 2026-10-11 (founder-approved, plan review): line 1 and the login line's copy
  Scenario: the card lists six things, one line each
    When the welcome card is open
    Then it shows exactly these lines, in this order, each with its key:
      | key | label           | what it does                                   |
      | 1   | Chat with a model | pick a model and talk                        |
      | 0   | Run an agent      | let a model read, write, run code            |
      | 2   | Share a model     | put a model on air (free or paid)            |
      | 3   | Edge network      | join your own devices together               |
      | w   | Web console       | open the same app in a browser               |
      | l   | Log in            | starter credit for new accounts; earn by sharing |
    And it ends with "you can always get here again with ?"
    And no line is longer than one terminal line

  @tui
  Scenario: the card's keys are the TUI's own keys
    When the welcome card is open
    Then every key it shows does the same thing anywhere else on the dial
    And the section bar's [0] [1] [2] [3] keys match the card's agent, dial, share and edge lines

  @tui
  Scenario: the starter credit is never promised as a fixed amount
    Given the broker no longer seeds new wallets
    When the welcome card is open
    Then the login line does not mention an amount
    And it never says "$1"

  @tui
  # corrected 2026-10-11 (founder-approved, plan review)
  Scenario: the login panel mentions the starter credit only while the broker offers it
    Given the broker reports new wallets are seeded with a starter credit
    When the user picks Log in
    Then the login panel says new accounts get a starter credit
    And it names no amount

  @tui
  # corrected 2026-10-11 (founder-approved, plan review)
  Scenario: the login panel says nothing about a starter credit the broker no longer offers
    Given the broker no longer seeds new wallets
    When the user picks Log in
    Then the login panel does not mention a starter credit

  # 3. What each line does

  @tui
  Scenario: 1 opens the dial
    When the user presses 1 on the welcome card
    Then the card closes and the dial is shown
    And the dial's hint line says how to tune in

  @tui
  Scenario: 0 opens the agent with a model tuned
    Given a band is tuned in
    When the user presses 0 on the welcome card
    Then the card closes and [0] AGENT opens on that band

  @tui
  Scenario: 0 with nothing tuned says what to do next
    Given no band is tuned in
    When the user presses 0 on the welcome card
    Then [0] AGENT opens
    And it says in one line to tune a band first, and names the key that opens the dial

  @tui
  Scenario: 2 opens SHARE with what was found on this machine
    Given a local model server is detected
    When the user presses 2 on the welcome card
    Then the card closes and [2] SHARE opens with the detected models as rows
    And SHARE says in one line: free needs no login, earning needs a login and a price

  @tui
  Scenario: 2 with no local model opens the guided setup
    Given no local model server is detected
    When the user presses 2 on the welcome card
    Then SHARE opens its guided setup (start a tool, paste an address, or get a model that fits)
    And nothing is put on air until the user chooses a row and turns it on

  @tui
  Scenario: earning from SHARE still discloses login and payout terms before a price
    Given SHARE is open from the welcome card
    When the user chooses to earn on a row
    Then the login requirement and the payout terms are shown before any price is asked
    And the same disclosure the old earn wizard showed is used

  @tui
  Scenario: 3 opens the Edge screen and starts scanning there
    Given this machine has not joined an Edge
    When the user presses 3 on the welcome card
    Then [3] EDGE opens
    And the LAN scan runs only while [3] EDGE is open (features/edge/scan_on_request.feature)

  @tui
  # corrected 2026-10-11 (founder-approved, plan review): w is exactly the dial's w
  Scenario: w starts the web console and shows its address
    When the user presses w on the welcome card
    Then the web console opens the same way the w key opens it from the dial
    And the status line shows the console's local address
    And a browser is opened only on an interactive terminal, as the dial's w does

  @tui
  Scenario: w when the console is off says why
    Given roger was started with --no-webui
    When the user presses w on the welcome card
    Then the status line says the web console is off for this run, and how to turn it on

  @tui
  Scenario: l starts login
    When the user presses l on the welcome card
    Then the same login flow as the l key on the dial starts
    And the card is closed and marked seen

  @tui
  # corrected 2026-10-11 (founder-approved, plan review): l is the dial's [L] panel
  Scenario: l when already logged in opens the same logout confirm as the dial
    Given the user is logged in
    When the user presses l on the welcome card
    Then the [L] panel opens with the logout confirm, as the l key does on the dial
    And nothing is logged out until the user confirms

  @tui
  Scenario: a logged-in first launch says so on the login line
    Given the user is logged in
    When the welcome card is open
    Then the l line reads "Log out" and says who is logged in

  # 4. Keys and layout

  @tui
  Scenario: arrows and enter pick a line
    Given the welcome card is open with the first line selected
    When the user presses down twice and enter
    Then the third line (Share a model) is picked

  @tui
  Scenario: keys that are not on the card do nothing while it is open
    Given the welcome card is open
    When the user presses a key that is not on the card
    Then the card stays open and nothing else happens

  @tui
  Scenario: ctrl+p keeps its meaning
    Given the welcome card is open
    When the user presses ctrl+p
    Then the card stays open and nothing else happens
    And ctrl+p never opens a palette or history from the card

  @tui
  Scenario: the card fits a narrow terminal
    Given a terminal 60 columns wide
    When the welcome card is open
    Then no line overflows the width
    And the "what it does" column is dropped before any label is cut

  @tui
  Scenario: the card in the windowshade
    Given the compact windowshade mode is on
    When the TUI opens for the first time
    Then the card is drawn as one line per option under the compact header

  @tui
  Scenario: the card reads with NO_COLOR
    Given NO_COLOR is set
    When the welcome card is open
    Then every key is still marked as a key without color
    And the brand and the card use only ASCII-folded glyphs where the TUI already folds them

  @tui
  Scenario: the card uses the TUI's palette
    When the welcome card is open
    # corrected 2026-10-11 (founder-approved, plan review, R2)
    Then it uses only the Health and Fleet palette the [3] screens use
    And the big ROGER·AI brand stays visible above it

  # 5. Getting back to it

  @tui
  Scenario: ? opens help with the welcome card at the top
    Given the welcome card was closed
    When the user presses ? on the dial
    # corrected 2026-10-11 (founder-approved, plan review): start here IS the six lines
    Then the help screen opens with a "start here" section listing exactly the same six lines and keys

  Scenario: `roger onboard --welcome` shows the card on the next launch
    Given welcome_seen is true
    When the user runs `roger onboard --welcome`
    Then the next TUI launch shows the welcome card once

  # corrected 2026-10-11 (founder-approved, plan review)
  Scenario: `roger onboard --reset` brings the card back
    Given welcome_seen is true and a saved share
    When the user runs `roger onboard --reset --free --yes`
    Then welcome_seen is cleared, so the next TUI launch shows the card

  # 6. Scripted paths that stay

  Scenario Outline: the scripted onboarding flags keep working
    When the user runs `roger onboard <flags>`
    Then the share setup completes as before without opening the TUI

    Examples:
      | flags        |
      | --free --yes |
      | --earn       |
      | --reset      |

  Scenario: `roger onboard` on a set-up machine keeps Keep, Modify, Reset
    Given a config with onboarded true
    When the user runs `roger onboard` on a terminal
    Then it offers Keep it as is, Modify my setup, Reset and start over
    And a re-run touches only the local share setup, never the linked account, prices or earnings

  Scenario: the old pre-TUI picker is gone
    Given a brand-new install
    When roger is launched with no arguments
    Then nothing asks "Are you here to use models, or to share your own?" before the TUI

  # 7. The web console

  @console
  Scenario: the console shows the same card once
    Given welcome_seen is false
    When the web console is opened for the first time
    Then it shows the same six lines with the same destinations
    And closing it saves welcome_seen so the TUI will not show the card either

  @console
  Scenario: seeing the card in the TUI counts for the console
    Given the welcome card was closed in the TUI
    When the web console is opened
    Then no welcome card is shown there

  @console
  Scenario: the console's Log in and Web console lines make sense in a browser
    When the console's welcome card is open
    Then "Web console" is replaced by "Terminal app" with the command to run roger
    And "Log in" starts the console's own login

  # 8. Safety

  @tui
  Scenario: the card never changes configuration by itself
    When the user picks any line on the welcome card
    Then only welcome_seen is written by the card
    And sharing, prices, the Edge and login change only through the screen the card opened

  @tui
  Scenario: the card never puts anything on air
    When the user picks Share a model
    Then no model is on air until the user turns a SHARE row on
    And nothing is offered on the market without the market confirmation

  # 9. Look

  @tui
  Scenario: the card is drawn with the Health and Fleet look
    When the welcome card is open
    # corrected 2026-10-11 (founder-approved, plan review): the eyebrow is muted, as in Health/Fleet
    Then it uses the Health/Fleet palette (paper, ink, a muted eyebrow, the one red)
    And each key is a paper key cap and each line leads with its glyph in the one red
    And the selected line has the red rail at its left
    And the foot is the fleetview action bar: "↑↓ move", "enter open", "1 0 2 3 w l jump", "esc skip"

  @tui
  Scenario: the big ROGER·AI lockup stays above the card
    When the welcome card is open
    Then the preset bar and the ▟▄▙ R O G E R · A I lockup are drawn above it, unchanged

  @tui
  Scenario Outline: the card reads well at every width it will meet
    When the welcome card is open at <width> columns
    Then no line overflows
    And <what folds>

    Examples:
      | width | what folds                                                     |
      | 100   | nothing is cut, with a blank line between options              |
      | 80    | nothing is cut                                                  |
      | 60    | the description column is dropped and options are one per line |
      | 69    | the description column is dropped and options are one per line |

  @tui
  # corrected 2026-10-11 (founder-approved, plan review)
  Scenario: a short terminal never loses the action bar
    Given a terminal 100 columns wide and 20 rows tall
    When the welcome card is open
    Then the frame is no taller than 20 rows
    And the action bar and all six lines are still on screen

  @tui
  # corrected 2026-10-11 (founder-approved, plan review)
  Scenario: the card sits under the owner-join banner without hiding it
    Given an owner-join request is waiting
    When the welcome card is open
    Then the owner-join banner is still drawn above the card
    And the card's action bar is still on screen

  @tui
  # corrected 2026-10-11 (founder-approved, plan review): goldens come from the TUI's own View()
  Scenario: a golden frame pins the look
    When the welcome card is rendered by the TUI at 100, 80 and 60 columns
    Then each frame contains every key and label, and no line overflows
    And the 100-column frame matches the committed golden file for the welcome card

