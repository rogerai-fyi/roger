# Playbox v2 - THE CASSETTE DECK (founder direction, 2026-08-01). Supersedes the
# tabbed layout of playbox_bench.feature: no navigation between chat/capabilities/
# edge - ONE near-fullscreen view shaped like a wide tape deck:
#
#   [ INPUT console ]  [ CASSETTE bay + shelf ]  [ OUTPUT monitor ]
#
# The model IS the cassette. The visitor picks a tape from the shelf, it loads
# into the bay with a mechanical animation, the reels spin while it plays. The
# left console selects ONE input kind at a time on a rotary selector (text,
# voice, image, tool, embed, guard); the right monitor shows the result.
# Honesty doctrine unchanged: chat output is REAL (Tower relay / Ping), demo
# kinds replay certified contracts and recorded outputs, always labelled.
Feature: The Playbox cassette deck
  In order to see what kind of input goes to what kind of model over our network
  As a Playbox visitor
  I want one deck where I load a model like a tape and play inputs through it

  Scenario: one view, no tabs
    Then the deck presents input, cassette, and output side by side
    And no tab navigation is required to reach any capability

  Scenario: the shelf is the live band plus labelled demo tapes
    Given the broker is reachable
    Then each chatable model on air is a cassette on the shelf marked LIVE
    And the Wave demo tape is on the shelf marked RECORDED
    And a quiet band leaves only the demo tapes, honestly labelled

  Scenario: loading a tape
    When the visitor picks a cassette
    Then it loads into the bay with the load animation and its reels idle
    And the label shows the model name and its capability badges
    And input kinds the model does not carry are dimmed on the selector

  Scenario: the rotary input selector allows one input kind at a time
    Then the selector offers text, voice, image, tool, embed, and guard positions
    And exactly one position is active, and its input surface is shown

  Scenario: text input plays for real
    Given a live cassette is loaded and the text input holds a message
    When the visitor presses play
    Then the reels spin while the reply streams from that station via the Tower
    And the output monitor shows the streamed reply labelled LIVE

  Scenario: voice input is preset cards that speak into the tape
    Then the voice surface offers preset utterance cards showing their words
    And playing one sends its transcript through the same live path
    And the output labels it as a spoken input's transcript

  Scenario: image input is preselected scenes
    Then the image surface offers preselected scene cards by category
    And playing one produces a clearly labelled recorded demonstration

  Scenario: tool, embed, and guard inputs replay certified contracts
    Given the tool, embed, or guard position is selected
    When the visitor plays a preset input card
    Then the monitor animates the certified contract output (JSON, tool call,
      alert, or verdict) labelled RECORDED, never presented as live
    And embed presents the device the model rides in (pump, sensor, ESP32)

  # Founder ask, 2026-08-02: the deck should feel like an operator console under the
  # hands, not a web page you click at.
  Scenario: the deck is playable from the keyboard
    Given a tape is loaded and an input is ready
    Then space plays, escape stops, and the arrow keys change tape
    And the number keys select the input positions in order
    And a key pressed while typing a message reaches the message, never the transport

  Scenario: the console shows its own keys
    Then the deck prints its shortcuts on the faceplate, engraved like key caps
    And each printed key is one the deck actually honours

  Scenario: the deck remembers the tape you left in it
    Given a visitor loaded a tape and chose an input position
    When they come back to the page later
    Then the same tape is loaded and the same position is selected
    And a tape that has since gone off air is not silently swapped for another

  # THE SIGNED-IN OPERATOR (founder ask 2026-08-02, reshaped by a validation pass).
  # Signing in only ADDS capability and visibility - it never removes, hides, or
  # degrades anything a signed-out visitor can see or do.
  #
  # Deliberately NOT here, and why:
  #   - No wallet or balance on the deck. It already lives in the site header.
  #   - No "these are your own stations" marking. /discover is a public, shared,
  #     cached read with no owner field, so ownership is not knowable in the
  #     browser; and a browser session is a github-scoped wallet, which can never
  #     satisfy the pubkey-derived self-use test - so playing your own station from
  #     the web bills at market price. Marking it "yours, free" would be a false
  #     claim about money.
  Scenario: the plate carries the operator's handle, and only the handle
    Given a visitor is signed in with a handle
    Then the maker's plate shows that handle beside the serial number
    And it never shows the wallet identifier, balance, spend, or request history
    And an account with no handle leaves the plate exactly as it is signed out

  Scenario: an expired session stops asserting an identity
    Given the deck is showing a signed-in handle
    When the relay or the audio path refuses the session as expired
    Then the deck stops claiming that identity
    And any surface it had unlocked is locked again

  Scenario: a price is stated as the floor it is, never as a per-turn total
    Given a tape whose stations charge for output
    Then the card states the price it is charged in, per million units
    And it is drawn from the cheapest station carrying the tape, marked "from"
    And a voice tape says its unit is characters, not tokens
    And a scheduled price says that it varies by time of day
    And no per-turn total is ever shown, because the output length is unknowable
      before the turn runs

  Scenario: the free path is unchanged by signing in
    Then every free tape, recorded surface, and honesty label is identical
      whether or not the visitor is signed in

  Scenario: reduced motion and honesty pins carry over
    Then reduced motion stops reels and load animation with the deck legible
    And the quiet-band, unreachable-broker, and relay-error states remain distinct
    And ESCALATE still renders as a first-class good outcome
