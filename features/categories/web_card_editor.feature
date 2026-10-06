# Slice 5 - the website console's model cards edit the category (web/src/console.html +
# js/console.js, "Models & pricing"). A front-end on POST /provider/meta
# (owner_override.feature): it offers the broker's closed category list (GET /categories),
# sends the owner's credentials, shows the server's refusal reason in place, and renders
# every station-supplied value as text. Checked against the built site (web/test).

Feature: The website console's model cards edit the category

  Scenario: Each model card offers an Edit category control
    When the console script is built
    Then each model card has an "Edit category" button

  Scenario: The editor offers the broker's category list
    When the console script is built
    Then the editor reads its categories from "/categories"

  Scenario: Saving and clearing go through /provider/meta with the owner's credentials
    When the console script is built
    Then saving POSTs to "/provider/meta" with credentials
    And clearing POSTs "clear": true to "/provider/meta"

  Scenario: A refused save shows the server's reason in the card
    When the console script is built
    Then a refused save writes the server's message into the card

  Scenario: An override set on the web is labelled as such
    When the console script is built
    Then a card whose category was set on the web says "set on the web"

  Scenario: The console says where the category is changed, now including the web
    When the console page is built
    Then it says the category can be changed here, with "roger share --category", in the TUI SHARE tab, or in the local console

  Scenario: Station-supplied text is never markup
    When the console script is built
    Then the category editor builds its fields with textContent and value, never innerHTML

  # Regression (slice-5 review): with no category list loaded, a Save would post an empty
  # declaration - which the broker treats as a clear - and silently remove the override.
  Scenario: Save is disabled until the category list has loaded
    When the console script is built
    Then the editor's Save button starts disabled and is enabled only once the list loads
    And a failed category-list response is not cached
