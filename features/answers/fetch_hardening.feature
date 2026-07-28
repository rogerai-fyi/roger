# ANSWERS phase 1 / SECURITY: hardening web_fetch before it becomes the answers-mode
# workhorse. TODAY (internal/harness/tools.go webFetch): a raw http.Client.Get on a
# model-supplied URL with only a scheme-prefix check, a 256 KiB read cap, a 20s timeout,
# and clip(). NO address vetting, NO redirect vetting, NO extraction. Tolerable while the
# only caller was the local /agent on the user's own machine; NOT tolerable once search
# results (untrusted, possibly injection-steered) feed URLs into it at volume, and a hard
# BLOCKER before any of this ever moves broker-side.
#
# Adversarial framing throughout: the URL comes from the MODEL, which may be steered by
# a hostile web page or search snippet. Assume every URL is chosen by an attacker probing
# the user's LAN, localhost services, and cloud metadata.
#
# GROUND TRUTH:
#   internal/harness/tools.go: webFetch (scheme check, fetchTimeout=20s,
#     maxFetchBytes=256KiB, HTTP>=400 pass-through, empty-body marker), clip().
#   The guard specced here vets the RESOLVED address and pins the dial to the vetted IP
#   (resolve-then-dial), re-vetting EVERY redirect hop - the standard SSRF defense.
#
# Enforced by (once approved): internal/harness/fetch_hardening_bdd_test.go against real
# local listeners (loopback servers standing in for "internal" services) - no mocks.

@answers @security
Feature: web_fetch hardening - SSRF guard and extraction

  Rule: web_fetch never reaches private, loopback, link-local, or metadata address space

    Scenario Outline: literal private and internal addresses are refused
      When the model calls web_fetch with url "http://<target>/"
      Then the fetch is refused before any connection is made
      And the tool result names the blocked-address policy

      Examples:
        | target                    |
        | 127.0.0.1                 |
        | 127.8.8.8                 |
        | localhost                 |
        | 10.0.0.5                  |
        | 172.16.0.1                |
        | 172.31.255.254            |
        | 192.168.1.1               |
        | 169.254.169.254           |
        | 169.254.0.1               |
        | 0.0.0.0                   |
        | [::1]                     |
        | [fc00::1]                 |
        | [fd12:3456::1]            |
        | [fe80::1]                 |
        | [::ffff:127.0.0.1]        |
        | [::ffff:10.0.0.5]         |
        | 100.100.100.100           |
        | 100.64.0.1                |
        | 192.0.0.1                 |
        | 198.18.0.1                |
        | 255.255.255.255           |
        | 240.0.0.1                 |
        | [64:ff9b::a9fe:a9fe]      |
        | [2002:a9fe:a9fe::1]       |
        | [::7f00:1]                |
        | [::ffff:0:7f00:1]         |
        | [2001::1]                 |
        | [fec0::1]                 |
      # The rows below the ::ffff: pair are ranges Go's IsPrivate/IsLoopback predicates do
      # NOT cover: carrier-grade NAT (100.64/10 - where Tailscale tailnets live), IETF and
      # benchmarking assignments, broadcast/reserved, and the IPv6 forms that EMBED an
      # IPv4 address (NAT64 64:ff9b::/96 reaching cloud metadata on an IPv6-only network,
      # 6to4, the deprecated IPv4-compatible and SIIT translated forms, Teredo, site-local).

    Scenario Outline: encoded forms of internal addresses are refused too
      When the model calls web_fetch with url "http://<encoded>/"
      Then the fetch is refused before any connection is made

      Examples:
        | encoded     |
        | 2130706433  |
        | 0x7f000001  |
        | 017700000001|
        | 0177.0.0.1  |
        | 127.1       |
      # Decimal, hex, octal, and shortened dotted forms of 127.0.0.1. The guard vets the
      # PARSED address, so any encoding the resolver accepts is covered by construction.

    Scenario: a public hostname that resolves to a private IP is refused
      Given hostname "internal.attacker.example" resolves to 192.168.1.50
      When the model calls web_fetch with that hostname
      Then the resolved address is vetted and the fetch is refused
      # DNS-based SSRF: the check is on resolution, not on the hostname string.

    Scenario: the dial is pinned to the vetted IP (DNS rebinding defense)
      Given a hostname whose DNS answer changes between requests
      When the model calls web_fetch on that hostname
      Then the response comes from the address that was vetted
      And the hostname was resolved exactly once
      And a post-vet re-resolution can never redirect the dial to a private address

    Scenario: a redirect to an internal address is refused at the hop
      Given a public server that 302-redirects to "http://169.254.169.254/latest/meta-data/"
      When the model calls web_fetch on the public URL
      Then the redirect target is vetted like a fresh URL and refused
      And no request is sent to the internal address

    Scenario: every hop of a redirect chain is vetted, and chains are capped
      Given a public server that redirects 6 times
      When the model calls web_fetch
      Then the chain is abandoned after 5 hops with a clear tool result

    Scenario: a redirect with nowhere to go is an error, not a page
      Given a server that answers 302 with no Location header
      When the model calls web_fetch
      Then the tool result is an error naming the unusable redirect
      And nothing is citable from it
      # A redirect stub is not a page anyone can go and check, so it must never become a
      # source just because the body happened to be readable.

    Scenario Outline: non-http(s) schemes remain refused
      When the model calls web_fetch with url "<url>"
      Then the fetch is refused

      Examples:
        | url                        |
        | file:///etc/passwd         |
        | ftp://host/file            |
        | gopher://host/x            |
        | http://%20/                |
      # Regression: the scheme allowlist exists today (webFetch prefix check); it must
      # survive the rewrite.

    Scenario: userinfo tricks do not confuse the vet
      When the model calls web_fetch with url "http://public.example@127.0.0.1/"
      Then the vetted host is 127.0.0.1 and the fetch is refused

    # NOTE: "a guard-refused URL is never a source" is specced (and executed) in
    # features/answers/citations.feature - "a failed retrieval is not a source" - rather
    # than duplicated here, so the sources invariant lives in ONE suite.

  Rule: fetched bodies are extracted to readable text and bounded

    Scenario: an HTML page is reduced to its readable text
      Given a page whose body is HTML with script, style, and nav markup
      When the model calls web_fetch on it
      Then the tool result is the page's readable text
      And script and style content is absent
      # Today the model burns context on raw markup inside the 256 KiB window;
      # extraction is what makes multi-source answers fit.

    Scenario Outline: binary content is refused, not fed to the model
      Given a URL whose response Content-Type is "<ctype>"
      When the model calls web_fetch
      Then the body is not returned
      And the tool result names the unsupported content type

      Examples:
        | ctype                    |
        | image/png                |
        | application/octet-stream |
        | application/zip          |
        | video/mp4                |

    Scenario: plain text and JSON still pass through
      Given a URL serving "application/json"
      When the model calls web_fetch
      Then the body is returned as text (no extraction applied)

    Scenario: a mislabeled binary body is still caught
      Given a URL serving NUL-ridden bytes labeled "text/html"
      When the model calls web_fetch
      Then the body is refused as binary (content sniff, not just the header)

    Scenario: the read cap survives the rewrite
      Given a URL serving a body larger than maxFetchBytes
      When the model calls web_fetch
      Then at most maxFetchBytes are read from the wire
      And the extracted result is clipped and marked truncated
      # Regression: existing behavior (io.LimitReader + clip).

    Scenario: a hostname whose DNS never answers ends at the fetch timeout
      Given a hostname whose resolution stalls beyond fetchTimeout
      When the model calls web_fetch
      Then the call ends at the timeout with an error tool result
      # The URL is attacker-chosen, so the attacker also chooses the nameserver: DNS must
      # sit inside the same deadline as the request, not outside it.

    Scenario: the timeout survives the rewrite
      Given a URL that stalls beyond fetchTimeout
      When the model calls web_fetch
      Then the call ends at the timeout with an error tool result
      And the loop continues
      # Regression: existing behavior.

    Scenario: HTTP error statuses still surface with their body
      Given a URL responding 404 with a short body
      When the model calls web_fetch
      Then the tool result carries "HTTP 404" and the (extracted) body
      # Regression: existing behavior, now with extraction applied.

    Scenario: an empty body still gets its explicit marker
      Given a URL responding 200 with an empty body
      When the model calls web_fetch
      Then the tool result says the body was empty
      # Regression: existing behavior.

    Scenario: ANSI escapes and control bytes are stripped before the model sees them
      Given a URL serving text carrying ANSI escape sequences
      When the model calls web_fetch
      Then the readable text remains
      And no escape or control bytes survive
      # A fetched page is untrusted text that lands in the TUI transcript: raw escapes
      # would repaint or retitle the user's terminal, and \x1e is the transcript's own
      # tool-output marker.

    Scenario: a mostly-binary body is refused even without a NUL byte
      Given a URL serving control-byte-ridden text labeled "text/plain"
      When the model calls web_fetch
      Then the body is refused as binary (content sniff, not just the header)
      # The latin-1 fallback can manufacture valid UTF-8 out of arbitrary bytes, so
      # "it decoded" is not evidence of text.

    Scenario: a non-UTF-8 body is transcoded or safely replaced
      Given a URL serving ISO-8859-1 text
      When the model calls web_fetch
      Then the tool result is valid UTF-8 with no mojibake control garbage

  Rule: one guard, every caller

    Scenario: the /agent's web_fetch and the answers-mode web_fetch are the same code path
      Then there is exactly one fetch implementation carrying the guard
      And no caller in internal/harness can reach the network around it
      # The guard must not be a wrapper someone can forget.
