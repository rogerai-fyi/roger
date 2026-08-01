# LET'S TALK - the site contact dialog.
#
# Founder direction 2026-08-01: a clean contact affordance (reference: Liquid.ai's
# "Let's talk" modal) reachable from the company and models surfaces. The site is
# static behind a strict CSP with NO form backend, so the honest implementation is
# an accessible dialog that composes a prefilled email to the labs mailbox: the
# reader picks a topic, gets a starter note they can edit, and Send opens their
# mail client via mailto. Nothing is collected or transmitted by us.
#
# Interfaces: web/src/_partials/lets-talk.html, web/src/js/lets-talk.js,
#   web/src/company.html, web/src/research-models.html, web/src/styles/base.css.
#
# Out of scope: any server endpoint, any analytics on the dialog, any required
# field beyond what a mail client itself needs.

Feature: A reader can start a conversation without hunting for an address
  The dialog lowers the cost of writing a good first email; it never pretends
  to be a form that submits anywhere.

  Background:
    Given a visitor on the company or the models research page

  Scenario: The trigger is honest without JavaScript
    Then the "Let's talk" trigger is a real mailto link to the labs mailbox
    And with scripting disabled it still opens the visitor's mail client

  Scenario: The dialog is an accessible modal
    Then the dialog carries role dialog, aria-modal, and a visible title
    And focus moves in on open, is trapped inside, and restores on close
    And Escape and the backdrop close it

  Scenario: A topic prefills a starter note
    Then choosing a topic fills the note with that topic's starter lines
    And the visitor can edit or clear the note freely
    And no starter note contains an em dash

  Scenario: Send composes an email, never a POST
    Then the form has no action and no submit endpoint
    And Send opens a mailto to the labs mailbox with the subject and note
    And the visitor's details ride in the note body, not in any request of ours

  Scenario: The dialog ships styled on every page that offers it
    Then the defining stylesheet is in the bundle of every page using the dialog
