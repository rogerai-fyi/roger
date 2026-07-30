Feature: RogerAI adopts rogerai.fm without breaking rogerai.fyi compatibility
  RogerAI's public brand moves to rogerai.fm, while previously shipped clients,
  shared links, authentication callbacks, and email addresses using rogerai.fyi
  remain valid. The migration is additive: a cosmetic rename must never strand a
  machine client, invalidate a login, or lose a message.

  Scenario: The public website declares rogerai.fm as canonical
    Given an indexable public page is built
    Then its canonical URL uses "https://rogerai.fm"
    And its Open Graph URL and image use "https://rogerai.fm"
    And its Twitter image uses "https://rogerai.fm"
    And its structured-data website and organization identifiers use "https://rogerai.fm"
    And its sitemap URL uses "https://rogerai.fm"
    And no canonical or sitemap entry uses "https://rogerai.fyi"

  Scenario: Public product links prefer rogerai.fm
    Given a new public document, CLI message, release artifact, or package manifest is produced
    Then human-facing website, support, install, dashboard, console, billing, security, and policy links use "rogerai.fm"
    And new remote-control links use "https://rogerai.fm/r.html"
    And historical records are not rewritten merely to replace their original domain

  Scenario Outline: The legacy website redirects to the matching canonical URL
    Given a visitor requests "<old>"
    Then the edge returns one permanent redirect to "<new>"
    And the query string is preserved

    Examples:
      | old                                      | new                                  |
      | https://rogerai.fyi/                     | https://rogerai.fm/                   |
      | https://www.rogerai.fyi/manual.html      | https://rogerai.fm/manual.html        |
      | https://rogerai.fyi/r.html?source=legacy | https://rogerai.fm/r.html?source=legacy |

  Scenario: A legacy remote-control fragment survives navigation
    Given a user opens "https://rogerai.fyi/r.html#8FK3-9MQ2"
    When the browser follows the legacy website redirect
    Then the final browser URL is "https://rogerai.fm/r.html#8FK3-9MQ2"
    And the remote-control page receives code "8FK3-9MQ2"

  Scenario Outline: Machine and administrative legacy hosts are not website redirects
    Given a request is sent to "<host>"
    Then the request is not redirected by the legacy website redirect rule

    Examples:
      | host                 |
      | broker.rogerai.fyi   |
      | control.rogerai.fyi  |

  Scenario: Both broker hostnames reach the same compatible service
    Given the production broker is healthy
    Then "https://broker.rogerai.fm/health" returns success
    And "https://broker.rogerai.fyi/health" returns success
    And neither hostname redirects API, WebSocket, streaming, or callback requests
    And both hostnames present valid TLS certificates

  Scenario: Previously saved client configuration remains usable
    Given an installed client has saved broker "https://broker.rogerai.fyi"
    When it connects after the brand migration
    Then it reaches the production broker without changing its configuration
    And repair tooling does not replace the saved hostname solely because it is ".fyi"

  Scenario: New client configuration prefers the branded broker
    Given a newly installed client has no saved broker configuration
    Then its default broker is "https://broker.rogerai.fm"
    And the legacy broker remains accepted as a valid production broker

  Scenario Outline: Browser authentication accepts both migration origins
    Given a credentialed browser request has Origin "<origin>"
    When it calls a credentialed broker endpoint
    Then the broker allows that exact origin
    And it allows credentials
    And it does not return a wildcard origin

    Examples:
      | origin               |
      | https://rogerai.fm   |
      | https://rogerai.fyi  |

  Scenario: An unrecognized credentialed origin remains forbidden
    Given a credentialed browser request has Origin "https://attacker.example"
    When it calls a credentialed broker endpoint
    Then the broker does not grant credentialed cross-origin access

  Scenario: OAuth callbacks migrate only after provider registration
    Given an OAuth provider has not registered a rogerai.fm callback
    Then production continues using its registered rogerai.fyi callback
    And no deployment emits an unregistered callback
    When the provider registers the exact rogerai.fm callback
    Then production may switch to it without removing the working rogerai.fyi path

  Scenario: Federated login keeps a stable token audience
    Given existing web sessions were issued for the current production client identifier
    Then the domain migration does not silently change the expected token audience
    And a new client identifier is adopted only as an explicit authentication migration

  Scenario: Both associated domains remain valid for the mobile app
    Then rogerai.fm serves its platform association file directly without a redirect
    And rogerai.fyi continues serving its platform association file directly without a redirect
    And both files identify the same production app
    And both files authorize the remote-control path

  Scenario: The canonical site security policy permits only the intended broker
    Given a page is served from rogerai.fm
    Then its Content-Security-Policy permits the selected RogerAI broker origin
    And it does not weaken script, frame, object, or base restrictions
    And HSTS and the existing security headers remain present

  Scenario: Email delivery is additive
    Given a role address exists at rogerai.fyi
    Then the corresponding rogerai.fm role address reaches the intended existing mailbox or group
    And the rogerai.fyi address continues receiving mail
    And changing the public address does not create an unintended separate inbox

  Scenario: Transactional mail switches only after authentication passes
    Given the transactional-mail service has not verified rogerai.fm
    Then production continues sending from the verified rogerai.fyi identity
    When rogerai.fm has verified DKIM, SPF, and bounce-domain records
    Then production may send from "noreply@rogerai.fm"
    And the rogerai.fyi sending identity remains available during rollback

  Scenario: Each mail domain publishes one coherent authentication policy
    Then rogerai.fm publishes the configured inbound-mail MX records
    And rogerai.fm publishes exactly one SPF policy at its apex
    And rogerai.fm publishes a domain-specific DKIM key
    And rogerai.fm publishes a DMARC policy
    And rogerai.fyi gains a DMARC policy without disrupting its existing mail-authentication records

  Scenario: Search engines receive a one-to-one migration
    Given an indexable rogerai.fyi URL existed
    Then it has one corresponding indexable rogerai.fm URL
    And the old URL permanently redirects directly to that URL
    And the new URL has a self-referential canonical
    And the new sitemap contains the new URL but not the old URL

  Scenario: Rollback does not require a client release
    Given the rogerai.fm website or broker alias becomes unavailable
    Then broker.rogerai.fyi remains independently routable
    And existing installed clients continue operating
    And transactional mail may revert to the verified rogerai.fyi sender
    And the rogerai.fyi registration and DNS zone are retained
