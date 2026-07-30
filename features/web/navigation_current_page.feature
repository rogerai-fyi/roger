Feature: First-class destinations show a stable current-page marker
  The shared RogerAI navigation should orient visitors without adding another
  label, moving the layout, or weakening keyboard and mobile behavior.

  Scenario Outline: A destination marks exactly its matching link
    Given the visitor opens "<page>"
    Then exactly one primary navigation link has aria-current "page"
    And that link points to "<href>"
    And it uses a persistent Roger-red two-pixel underline
    And hover and keyboard focus remain distinct
    And the marker remains visible in the mobile menu

    Examples:
      | page          | href           |
      | models.html   | /models.html   |
      | research.html | /research.html |
      | voices.html   | /voices.html   |
      | app.html      | /app.html      |
      | company.html  | /company.html  |
      | manual.html   | /manual.html   |

  Scenario: Homepage marks no destination falsely
    Given the visitor opens "index.html"
    Then no primary navigation link has aria-current "page"

  Scenario: Reduced motion does not animate the persistent marker
    Given reduced motion is requested
    Then the current-page underline has no transition
