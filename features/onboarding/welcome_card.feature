# STATUS: APPROVED 2026-10-11 (founder)
# BUILD STATUS: NOT BUILT.
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
#   1  Chat with a model     pick a band on the dial and talk
#   0  Run an agent          let a model read, write, run code
#   2  Share a model         put a model on air (free or paid)
#   3  Edge network          join your own devices together
#   w  Web console           open the same app in a browser
#   l  Log in                starter credit for new accounts, earn from shares
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
#   3. Starter-credit wording. RULED 2026-10-11: "a starter credit for new accounts" with the amount
#      shown only when the broker reports it is still offered (a read the client already makes
#      at login); never a fixed "$1" on the card.
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
#      limoni look the founder approved for the Edge Health and Fleet screens (2026-09-26): an
#      amber eyebrow, paper key caps, a red selection rail, one glyph per line in the one red,
#      prose in ink, descriptions muted, and the fleetview action bar at the foot. The radio TUI
#      is bubbletea, so the card renders through limoni's bubbletea compat inside the existing
#      view, under the big ROGER·AI lockup (kept, founder rule). A rendered mockup with the real
#      palette: docs/welcome-and-empty-edge-mockups.md and docs/welcome-card-mockup.png.
#   8. Glyphs. RULED 2026-10-11: ◉ chat, ▸ agent, ((•)) share, ▟•▙ edge, ◇ console, ◈ login, each in the
#      one red; under NO_COLOR they fold the way the TUI already folds its glyphs.
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

  Scenario: the card lists six things, one line each
    When the welcome card is open
    Then it shows exactly these lines, in this order, each with its key:
      | key | label           | what it does                                   |
      | 1   | Chat with a model | pick a band on the dial and talk             |
      | 0   | Run an agent      | let a model read, write, run code            |
      | 2   | Share a model     | put a model on air (free or paid)            |
      | 3   | Edge network      | join your own devices together               |
      | w   | Web console       | open the same app in a browser               |
      | l   | Log in            | starter credit for new accounts, earn from shares |
    And it ends with "you can always get here again with ?"
    And no line is longer than one terminal line

  Scenario: the card's keys are the TUI's own keys
    When the welcome card is open
    Then every key it shows does the same thing anywhere else on the dial
    And the section bar's [0] [1] [2] [3] keys match the card's agent, dial, share and edge lines

  Scenario: the starter credit is never promised as a fixed amount
    Given the broker no longer seeds new wallets
    When the welcome card is open
    Then the login line does not mention an amount
    And it never says "$1"

  Scenario: the card states the starter credit amount only when the broker offers it
    Given the broker reports new wallets are seeded with a starter credit
    When the user picks Log in
    Then the login flow names the amount the broker reported

  # 3. What each line does

  Scenario: 1 opens the dial
    When the user presses 1 on the welcome card
    Then the card closes and the dial is shown
    And the dial's hint line says how to tune in

  Scenario: 0 opens the agent with a model tuned
    Given a band is tuned in
    When the user presses 0 on the welcome card
    Then the card closes and [0] AGENT opens on that band

  Scenario: 0 with nothing tuned says what to do next
    Given no band is tuned in
    When the user presses 0 on the welcome card
    Then [0] AGENT opens
    And it says in one line to tune a band first, and names the key that opens the dial

  Scenario: 2 opens SHARE with what was found on this machine
    Given a local model server is detected
    When the user presses 2 on the welcome card
    Then the card closes and [2] SHARE opens with the detected models as rows
    And SHARE says in one line: free needs no login, earning needs a login and a price

  Scenario: 2 with no local model opens the guided setup
    Given no local model server is detected
    When the user presses 2 on the welcome card
    Then SHARE opens its guided setup (start a tool, paste an address, or get a model that fits)
    And nothing is put on air until the user chooses a row and turns it on

  Scenario: earning from SHARE still discloses login and payout terms before a price
    Given SHARE is open from the welcome card
    When the user chooses to earn on a row
    Then the login requirement and the payout terms are shown before any price is asked
    And the same disclosure the old earn wizard showed is used

  Scenario: 3 opens the Edge screen and starts scanning there
    Given this machine has not joined an Edge
    When the user presses 3 on the welcome card
    Then [3] EDGE opens
    And the LAN scan runs only while [3] EDGE is open (features/edge/scan_on_request.feature)

  Scenario: w starts the web console and shows its address
    When the user presses w on the welcome card
    Then the web console starts the same way the w key starts it from the dial
    And the status line shows the console's local address
    And the console is not opened in a browser unless the user's console setting says so

  Scenario: w when the console is off says why
    Given roger was started with --no-webui
    When the user presses w on the welcome card
    Then the status line says the web console is off for this run, and how to turn it on

  Scenario: l starts login
    When the user presses l on the welcome card
    Then the same login flow as the l key on the dial starts
    And the card is closed and marked seen

  Scenario: l when already logged in says so
    Given the user is logged in
    When the user presses l on the welcome card
    Then the status line says who is logged in and nothing else changes

  # 4. Keys and layout

  Scenario: arrows and enter pick a line
    Given the welcome card is open with the first line selected
    When the user presses down twice and enter
    Then the third line (Share a model) is picked

  Scenario: keys that are not on the card do nothing while it is open
    Given the welcome card is open
    When the user presses a key that is not on the card
    Then the card stays open and nothing else happens

  Scenario: ctrl+p keeps its meaning
    Given the welcome card is open
    When the user presses ctrl+p
    Then the card stays open and nothing else happens
    And ctrl+p never opens a palette or history from the card

  Scenario: the card fits a narrow terminal
    Given a terminal 60 columns wide
    When the welcome card is open
    Then no line overflows the width
    And the "what it does" column is dropped before any label is cut

  Scenario: the card in the windowshade
    Given the compact windowshade mode is on
    When the TUI opens for the first time
    Then the card is drawn as one line per option under the compact header

  Scenario: the card reads with NO_COLOR
    Given NO_COLOR is set
    When the welcome card is open
    Then every key is still marked as a key without color
    And the brand and the card use only ASCII-folded glyphs where the TUI already folds them

  Scenario: the card uses the TUI's palette
    When the welcome card is open
    Then it uses only the TUI's existing colors (mono plus the one red)
    And the big ROGER·AI brand stays visible above it

  # 5. Getting back to it

  Scenario: ? opens help with the welcome card at the top
    Given the welcome card was closed
    When the user presses ? on the dial
    Then the help screen opens with a "start here" section listing the same six lines and keys

  Scenario: `roger onboard --welcome` shows the card on the next launch
    Given welcome_seen is true
    When the user runs `roger onboard --welcome`
    Then the next TUI launch shows the welcome card once

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

  Scenario: the card never changes configuration by itself
    When the user picks any line on the welcome card
    Then only welcome_seen is written by the card
    And sharing, prices, the Edge and login change only through the screen the card opened

  Scenario: the card never puts anything on air
    When the user picks Share a model
    Then no model is on air until the user turns a SHARE row on
    And nothing is offered on the market without the market confirmation

  # 9. Look

  Scenario: the card is drawn with the Health and Fleet look
    When the welcome card is open
    Then it uses internal/fleetview's palette (paper, ink, muted, the amber eyebrow, the one red)
    And each key is a paper key cap and each line leads with its glyph in the one red
    And the selected line has the red rail at its left
    And the foot is the fleetview action bar: "↑↓ move", "enter open", "1 0 2 3 w l jump", "esc skip"

  Scenario: the big ROGER·AI lockup stays above the card
    When the welcome card is open
    Then the preset bar and the ▟▄▙ R O G E R · A I lockup are drawn above it, unchanged

  Scenario Outline: the card reads well at every width it will meet
    When the welcome card is open at <width> columns
    Then no line overflows
    And <what folds>

    Examples:
      | width | what folds                                                     |
      | 100   | nothing is cut, with a blank line between options              |
      | 80    | nothing is cut                                                  |
      | 60    | the description column is dropped and options are one per line |

  Scenario: a golden frame pins the look
    When the welcome card is rendered at 100x24 through the limoni uitest harness
    Then the frame matches the committed golden snapshot for the welcome card
    And a rendered frame at 80 columns contains every label and key

