# Founder-selected Tube Ping direction, 2026-07-29.
#
# Tube Ping is the hero-scale evolution of classic Ping, not a replacement.
# Classic Ping remains the compact animated creature and narrow/ASCII fallback.

Feature: Tube Ping makes a terminal-native 3D debut
  RogerAI should introduce a rounder, dimensional hero mascot that is unmistakably
  descended from Ping, obeys the one-red-glint law, and remains elegant in real terminals.

  Rule: The canonical silhouette is stable and recognizable

    Scenario: Full Tube Ping uses the founder-approved pixel receiver silhouette
      When the canonical Tube Ping hero renders
      Then it has a rounded block face, one live eye centred over the ROG wordmark, two feet, and a right-side depth plane
      And its visible silhouette matches:
        """
              ▄███████▄
           (  █   •   █▓  )
              █  ROG  █▓
               ▀█▄▄▄█▀▒
                ▀   ▀
        """
      And trailing whitespace is not significant
      And each body plane is styled as a contiguous span rather than one ANSI span per glyph

    Scenario: The eye remains the only saturated Roger-red cell
      Given full color rendering
      Then the Tube Ping eye "•" uses the live red role
      And the body uses bright, warm terminal ink
      And the right face uses a middle shadow plane
      And the lower edge uses a dim shadow plane
      And no body, wordmark, wave, or shadow cell uses the live red role

    Scenario: Depth reads without color
      Given NO_COLOR or the mono palette is active
      Then the body, right plane, and lower shadow still use distinct block densities
      And the centred eye and ROG wordmark remain visible
      And no ANSI color escapes are emitted under NO_COLOR

    Scenario: Legacy ASCII keeps classic Ping instead of a broken approximation
      Given ROGERAI_ASCII is active or Unicode block support is unavailable
      Then Tube Ping folds to the existing classic ASCII-safe Ping
      And no replacement character or half-rendered block appears

  Rule: Tube Ping scales down deliberately

    Scenario Outline: The hero fits its supported terminal widths
      When Tube Ping renders in a terminal <width> columns wide
      Then no mascot row exceeds <width> cells
      And the mascot remains horizontally balanced

      Examples:
        | width |
        | 40    |
        | 80    |
        | 120   |
        | 190   |

    Scenario: Tiny layouts retain classic Ping
      Given the available mascot region is narrower than the Tube Ping hero
      Then the existing compact classic Ping is rendered
      And no Tube Ping row is clipped

    Scenario: Wide hero placement does not steal the work surface
      Given Tube Ping appears outside Ping World
      Then it is limited to an intentional landing, help, boot, or empty-state region
      And it never appears beside an active transcript and composer at the same time
      And only one Roger mascot is visible on that screen

    Scenario: Main TUI chrome uses the compact Tube Ping station bug
      Given AGENT, TUNE IN, SHARE, or CONFIG renders the Roger header
      Then the top-left identity uses a compact pixel Tube Ping mark
      And the mark keeps one live eye and a visible depth edge
      And it does not increase the existing header height
      And narrow or ASCII layouts retain a clean compact fallback

    Scenario: AGENT uses the compact Tube Ping reaction poses
      Given the AGENT corner mascot is visible
      Then waiting, thinking, streaming, and tool states use this roomier Tube Ping silhouette:
        """
              ▄███████▄
           (  █   •   █▓  )
              █  ROG  █▓
               ▀█▄▄▄█▀▒
                ▀   ▀
        """
      And the eye, face, right depth plane, lower bevel, and feet remain visually distinct
      And each pose preserves the same bounding box
      And transcript height does not jump when the pose changes

  Rule: The z screensaver gives Tube Ping an unmistakable debut

    Scenario: Entering Ping World opens with a short Tube Ping title card
      Given the user presses "z" from a normal TUI view
      Then Ping World initially shows Tube Ping with "ROGER·AI" and an "ON AIR" cue
      And the title card lasts long enough to be seen but no longer than three seconds
      And it transitions into the existing living Ping World without requiring input

    Scenario: The world keeps classic Ping and adds a walking Tube Ping cameo
      Given the Tube Ping title card has completed
      Then classic Ping still walks, looks, transmits, sleeps, and leads ducklings in Ping World
      And Tube Ping walks through the scene on supported wide and tall layouts
      And Tube Ping uses alternating feet and changes horizontal position over time
      And Tube Ping does not replace classic Ping's animation banks

    Scenario: Small worlds omit the Tube Ping walker cleanly
      Given Ping World is too narrow or short for the full Tube Ping walker
      Then classic Ping remains visible
      And no clipped Tube Ping fragment appears

    Scenario: Re-entering Ping World remains calm
      Given the user has already seen the Tube Ping debut during this process
      When the user leaves and re-enters Ping World
      Then the title card may use a shorter beat or transition directly into the world
      And the choice is deterministic, not random flicker

    Scenario: Any key exits during the debut or the world
      Given Tube Ping's title card or the existing Ping World is visible
      When the user presses any key
      Then the prior TUI mode is restored
      And no stale debut animation continues repainting the restored view

    Scenario: Reduced motion renders a static debut
      Given quiet mode, reduced motion, or a non-TTY output
      Then the Tube Ping debut uses one static frame
      And no timed fade, bob, blink, or signal-wave animation runs

    Scenario Outline: The debut is safe at responsive screen sizes
      Given Ping World opens at <width> by <height>
      Then Tube Ping, ROGER·AI, and the exit hint fit without clipping
      And undersized screens use classic Ping's minimal world fallback

      Examples:
        | width | height |
        | 40    | 12     |
        | 80    | 24     |
        | 120   | 32     |
        | 190   | 50     |

  Rule: Tube Ping has subtle terminal-native life

    Scenario: Idle depth animation changes light, not identity
      Given Tube Ping is animating in a capable terminal
      Then a slow two-frame breathe may shift one highlight or shadow plane
      And the silhouette, centred eye, wordmark, and occupied bounding box remain stable

    Scenario: Transmit animation grows radio waves symmetrically
      Given Tube Ping enters its transmit pose
      Then the left and right wave marks grow in matched stages
      And the eye may widen from "•" to "O" without adding a second red light
      And the depth planes do not shimmer or crawl

    Scenario: Blink is brief and never looks powered off
      Given Tube Ping blinks
      Then the eye closes for one brief frame and returns to "•"
      And the ON AIR text or surrounding live cue remains truthful during the blink

    Scenario: Animation freezes while terminal text is natively selected
      Given native terminal selection is active and Roger is otherwise idle
      Then Tube Ping does not repaint and erase the user's selection

  Rule: The asset is reusable for a future rogerai.fm debut

    Scenario: Mascot data is independent from one TUI screen
      Then the canonical glyph rows and semantic color roles live in one reusable mascot component
      And main TUI chrome, Ping World, boot/help previews, and future exporters consume that component
      And no screen owns a divergent hand-copied Tube Ping silhouette

    Scenario: The mascot system is documented
      Then project documentation names the hero, walker, and compact station-bug forms
      And it records responsive fallbacks, semantic color roles, and the classic Ping compatibility promise

    Scenario: A plain-text export preserves the canonical form
      When Tube Ping is exported without ANSI styling
      Then the output is the approved canonical silhouette
      And it can be embedded in rogerai.fm documentation or launch material

    Scenario: Existing classic Ping contracts remain green
      Then classic Ping's canonical frames, Ping Walk, one-red law, and Ping World behavior tests remain unchanged
      And Tube Ping tests are additive rather than deleting classic mascot coverage
