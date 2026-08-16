Feature: The Cookie Line factory game
  The Playbox offers a live factory game beside, not inside, the Console and
  the Wave Mesh engineering workbench. It exists to make one argument
  playable: reading raw sensor numbers unaided is hard, and each rung of the
  Wave ladder takes a layer of that work off the player.

  Scenario: Enter a separate factory deck
    Given the Playbox deck selector is visible
    Then Console, Wave Mesh, and Factory are three sibling choices
    And choosing Factory opens the cookie line
    And Wave Mesh remains the full recorded engineering sandbox

  Scenario: The line runs live, and only while I am watching
    Given the cookie line has a mixer, an oven, and a packager
    Then each machine shows one sensor reading and one dial I can turn
    And dough, baked cookies and boxes move between the machines as it runs
    And nothing advances while the browser tab is hidden
    And I can pause the whole line at any time

  Scenario: Phase zero - no models, just numbers
    Given I have bought no models
    When a sensor develops a fault
    Then the machine's lamp still reports what its own instrument claims
    And a stuck sensor keeps showing a comfortable number
    And the only honest clue is that the line stops producing
    And I may service any machine at any time, whether or not it is faulty
    And servicing a healthy sensor simply costs me the money

  Scenario: Wave Pico names the fault on one machine
    Given I install Wave Pico on the mixer
    When the mixer's sensor develops a recorded condition
    Then Pico shows a colour and one word for that machine
    And the word is the prediction from a real record in the committed export
    And a record whose margin sits below the floor makes Pico say it is unsure
    And a record whose prediction was wrong makes Pico wrong here too

  Scenario: Wave Nano explains why and what to change
    Given Wave Nano is installed at the desk
    When Pico is unsure or wrong about a fault
    Then Nano states what the reading is doing and tells me to service it
    And it adds a dial instruction when the machine is outside its band
    And Nano only resolves conditions the recorded senior actually got right

  Scenario: Wave Micro and Wave Giga read the whole site
    Given Wave Micro is installed
    Then I see every machine's band, headroom and state in one view
    And Wave Giga names the bottleneck and what upgrading it would free
    And both are labelled as arithmetic over the game's own numbers
    And neither is presented as a recorded model answer

  Scenario: The ladder is a handover of work
    Given Wave Micro is installed
    Then I may hand one machine's dial to the models
    And Wave Giga lets me hand over every dial
    And I can take any dial back at any time
    When the models hold a dial
    Then the dial visibly moves by itself and says which tier moved it
    And the automation acts on what the sensor claims, so a lie still fools it

  Scenario: A self-running plant still reports its misses
    Given every dial has been handed to the models
    Then the results panel leads the desk
    And it shows cookies per second, uptime, and incidents caught versus missed
    And an incident that stopped the line before anyone acted counts as missed
    And the panel states that automation does not make the recorded models infallible

  Scenario: The honesty line is stated on the surface
    Given I am playing the cookie line
    Then the deck says model behaviour is recorded replay
    And it says the plant itself - cookies, coins, bands, wear - is game simulation
    And a fault drawn from another kind of instrument says so on the card
    And no tier without a recorded run is ever given an invented answer
