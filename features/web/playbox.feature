# The Playground becomes the PLAYBOX - the operator's bench. Same honest-surfaces
# doctrine (nothing faked, every state real), but per-station chat goes LIVE through
# the Tower via the browser-session relay path (features/relay/browser_session.feature)
# instead of handing the visitor a CLI command.
#
# Scope of THIS spec: the rename, the live chat wiring, and the honest states.
# The bench visual overhaul (dial, S-meter, logbook) and the Crystal Set (in-browser
# Wave Nano) are separate specs, pending the models-agent answer on artifacts.
Feature: The Playbox
  In order to try RogerAI's network and models without installing anything
  As a visitor to rogerai.fm
  I want a console page where tuning a station starts a real conversation

  # ---------- the rename -------------------------------------------------------

  Scenario: the page is named Playbox everywhere a visitor can see
    Then the page title, hero, nav entry, footer entry, models-hero button,
      and manual section all say "Playbox" and never "Playground"
    And the page's social card and meta description use the Playbox name

  Scenario: the old address keeps working
    When a visitor opens the old playground address
    Then they land on the Playbox without a broken link
    # (redirect or same-file rename - implementation's choice, the link contract holds)

  # ---------- live station chat ------------------------------------------------

  Scenario: tuning a free station and keying up gets a real reply
    Given the band shows a free chat station on air
    When the visitor selects it and sends a message
    Then the reply comes from that station through the Tower, streamed as it arrives
    And the transcript labels the reply with the station's model name, not "PING"

  Scenario: a paid station asks for sign-in before the first send
    Given the visitor is signed out and selects a paid station
    Then the composer is replaced by the sign-in invitation before anything is sent

  Scenario: a signed-in visitor sees the spend honestly
    Given a signed-in visitor with a balance chats on a paid station
    Then the account panel's balance reflects the spend after the reply settles

  Scenario: Ping remains the default operator
    Given no station is selected
    Then the composer talks to Ping via the concierge, exactly as before

  # ---------- honest states (regression pins from the Playground) ---------------

  Scenario: the quiet band is reported, never simulated
    Given the broker is reachable but no stations are on air
    Then the page says the band is quiet and offers no fake stations

  Scenario: an unreachable broker is its own state
    Given the broker cannot be reached
    Then the page says it could not reach the broker and that it is retrying
    And this state is distinct from the quiet band

  Scenario: a relay error surfaces as itself
    Given a station drops mid-conversation or the relay returns an error
    Then the transcript shows the error plainly and never invents a reply

  Scenario: rate limiting is explained, not retried into
    Given the relay answers 429
    Then the page tells the visitor to slow down and does not hammer the endpoint
