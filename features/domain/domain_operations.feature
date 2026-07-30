Feature: The rogerai.fm cutover is observable and avoids partial activation
  External configuration is activated in dependency order so DNS, certificates,
  authentication, email, and redirects are never assumed ready before verification.

  Scenario: Nameservers change only after the authoritative zone is prepared
    Given the new authoritative zone is not populated with required website and mail records
    Then the registrar nameservers are not changed
    When the required records are staged
    Then the registrar may delegate rogerai.fm to the assigned authoritative nameservers

  Scenario: Website redirect waits for the canonical site
    Given rogerai.fm does not return the expected production page with a valid certificate
    Then rogerai.fyi does not redirect visitors to it
    When the canonical site, critical paths, and security headers pass verification
    Then the rogerai.fyi website redirect may be enabled

  Scenario: Broker defaults change only after compatibility verification
    Given broker.rogerai.fm has not passed API, streaming, WebSocket, callback, and health checks
    Then released clients continue defaulting to broker.rogerai.fyi
    When the alias passes those checks through the production edge
    Then a later client release may default to broker.rogerai.fm

  Scenario: Hosting custom domains retain legacy bindings
    When rogerai.fm domains are attached to the production apps
    Then rogerai.fyi website, broker, and administrative domains are not removed
    And each new hostname reaches the intended existing app rather than a duplicate deployment

  Scenario: Edge rules are scoped by hostname
    Then canonical-site header rules match only rogerai.fm and www.rogerai.fm
    And the www redirect matches only www.rogerai.fm
    And the legacy-site redirect matches only rogerai.fyi and www.rogerai.fyi
    And broker and control subdomains are excluded

  Scenario: Production verification covers every critical surface
    Then HTTPS succeeds for rogerai.fm and www.rogerai.fm
    And the www hostname redirects to the apex
    And a legacy content path redirects to the matching canonical path
    And both broker health endpoints succeed
    And the install scripts download successfully
    And the sitemap and security metadata use rogerai.fm
    And both associated-domain files are served directly
    And inbound and outbound mail pass SPF, DKIM, and DMARC inspection
    And configured browser-login methods complete
    And hosted Checkout returns to rogerai.fm

  Scenario: Secrets never enter migration artifacts
    Then registrar, DNS, hosting, payment, mail, source-control, and identity credentials remain outside version control
    And generated app specifications do not expose decrypted secret values
    And logs and handoff documentation redact tokens and signing material
