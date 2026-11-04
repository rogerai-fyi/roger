# Which address a request came from, and when a forwarding header is believed.
#
# The caller's address keys every per-address control: the anonymous and free-tier rate
# limits, the limit on guessing API keys, the email sign-in code flow, /report, the
# concierge, free registration and its network bucket, the region cross-check, and the
# abuse-preservation record, where a forged address would poison a legal report. So it has to
# be an address the caller cannot choose.
#
# A caller writes every header it sends. A forwarding header is worth something only when a
# proxy the operator named wrote it, so the broker reads one only when the TCP peer is such a
# proxy, and then only the part that proxy added:
#   - X-Forwarded-For is read from the RIGHT. Each proxy appends the address it saw, so the
#     right-most entry that is not itself a trusted proxy is the caller. The entries to its left
#     came from the caller and are never read. Every trusted peer is taken to be such a proxy,
#     so the list should name the proxies and nothing else.
#   - Single-address headers (CF-Connecting-IP, X-Real-IP, True-Client-IP, Forwarded) are not
#     read. Whether something in front overwrote them is a fact about the deployment that the
#     broker cannot check, and common proxies pass them through untouched.
#   - ROGERAI_TRUSTED_PROXIES names the proxies (addresses or CIDRs). Unset, blank or "none", no
#     peer is trusted. A broker behind a proxy it was not told about then keys every caller on
#     that proxy: wrong, but visible at once, where trusting an unnamed peer would let it name
#     any caller. A setting the broker cannot read stops it at startup.
#   - The coarse country (the Tower origin tally) is read only from the header the operator
#     names in ROGERAI_COUNTRY_HEADER, for example CF-IPCountry when an edge proxy sets it,
#     only from a trusted proxy, only when it has one line, and only as a 2-character code.
#
# STATUS: proposed 2026-11-04, awaiting founder approval. Executable:
# cmd/rogerai-broker/client_address_bdd_test.go.

Feature: The broker decides who sent a request from what its own proxies saw

  Rule: With no proxy named, every request is the address it connected from

    Scenario: A caller is keyed on the address it connected from
      Given no proxy is named
      When a request arrives from "203.0.113.5" with no forwarding headers
      Then the broker takes the caller to be "203.0.113.5"

    Scenario Outline: Forwarding headers are ignored, from any peer, until a proxy is named
      Given no proxy is named
      When a request arrives from "<peer>" with header "<header>" set to "<value>"
      Then the broker takes the caller to be "<peer>"

      Examples:
        | peer        | header           | value                      |
        | 203.0.113.5 | X-Forwarded-For  | 198.51.100.7               |
        | 203.0.113.5 | X-Forwarded-For  | 198.51.100.7, 198.51.100.8 |
        | 203.0.113.5 | X-Forwarded-For  | 10.0.0.9                   |
        | 203.0.113.5 | CF-Connecting-IP | 198.51.100.7               |
        | 203.0.113.5 | X-Real-IP        | 198.51.100.7               |
        | 203.0.113.5 | True-Client-IP   | 198.51.100.7               |
        | 203.0.113.5 | Forwarded        | for=198.51.100.7           |
        | 10.0.0.1    | X-Forwarded-For  | 198.51.100.7               |
        | 127.0.0.1   | X-Forwarded-For  | 198.51.100.7               |
        | ::1         | X-Forwarded-For  | 198.51.100.7               |
        | fd00::1     | CF-Connecting-IP | 198.51.100.7               |

    Scenario Outline: The address is written one way, whatever form the connection gives it
      Given no proxy is named
      When a request arrives from "<peer>" with no forwarding headers
      Then the broker takes the caller to be "<caller>"

      Examples:
        | peer               | caller      |
        | ::ffff:203.0.113.5 | 203.0.113.5 |
        | 2001:db8::5        | 2001:db8::5 |
        | 2001:DB8:0:0::5    | 2001:db8::5 |

    Scenario: A connection that is not an IP address is used as it is and is never a proxy
      Given the broker's trusted proxies are "10.0.0.1"
      When a request arrives from "@local-socket" with header "X-Forwarded-For" set to "198.51.100.7"
      Then the broker takes the caller to be "@local-socket"

  Rule: Behind a named proxy, the caller is the right-most entry that is not a trusted proxy

    Scenario Outline: What the proxy appended counts; what the caller wrote does not
      Given the broker's trusted proxies are "10.0.0.1, 10.0.0.2, 192.168.1.0/24, 127.0.0.1, ::1, fd00::/8"
      When a request arrives from "10.0.0.1" with X-Forwarded-For "<xff>"
      Then the broker takes the caller to be "<caller>"

      Examples: one proxy, which appends the address it saw
        | xff                                     | caller      |
        | 203.0.113.7                             | 203.0.113.7 |
        | 198.51.100.1, 203.0.113.7               | 203.0.113.7 |
        | 198.51.100.1, 198.51.100.2, 203.0.113.7 | 203.0.113.7 |
        | not-an-address, 203.0.113.7             | 203.0.113.7 |
        | 10.0.0.2, 203.0.113.7                   | 203.0.113.7 |
        | 127.0.0.1, 203.0.113.7                  | 203.0.113.7 |

      Examples: a chain of trusted proxies, each appending the one before it
        | xff                                              | caller      |
        | 203.0.113.7, 10.0.0.2                            | 203.0.113.7 |
        | 198.51.100.1, 203.0.113.7, 10.0.0.2, 192.168.1.3 | 203.0.113.7 |
        | 203.0.113.7, fd00::2                             | 203.0.113.7 |
        | 203.0.113.7, 127.0.0.1                           | 203.0.113.7 |
        | 203.0.113.7, ::1                                 | 203.0.113.7 |

      Examples: the forms a proxy may write an address in
        | xff                 | caller      |
        | 203.0.113.7:51000   | 203.0.113.7 |
        | [2001:db8::7]:51000 | 2001:db8::7 |
        | [2001:db8::7]       | 2001:db8::7 |
        | 2001:db8::7         | 2001:db8::7 |
        | 2001:DB8::7         | 2001:db8::7 |
        | ::ffff:203.0.113.7  | 203.0.113.7 |

    Scenario: Several X-Forwarded-For lines are one list, read in order
      Given the broker's trusted proxies are "10.0.0.1"
      When a request arrives from "10.0.0.1" with X-Forwarded-For lines "203.0.113.7" and "198.51.100.1"
      Then the broker takes the caller to be "198.51.100.1"

    Scenario Outline: A caller among the trusted proxies is the entry the nearest proxy wrote
      Given the broker's trusted proxies are "10.0.0.1, 10.0.0.2, 192.168.1.0/24"
      When a request arrives from "10.0.0.1" with X-Forwarded-For "<xff>"
      Then the broker takes the caller to be "<caller>"

      Examples:
        | xff                    | caller       |
        | 192.168.1.20           | 192.168.1.20 |
        | 10.0.0.2, 192.168.1.20 | 192.168.1.20 |
        | garbage, 192.168.1.20  | 192.168.1.20 |

    Scenario: A trusted host is believed as a proxy, so the list names proxies and nothing else
      Given the broker's trusted proxies are "10.0.0.1, 192.168.1.0/24"
      When a request arrives from "10.0.0.1" with X-Forwarded-For "198.51.100.66, 192.168.1.20"
      Then the broker takes the caller to be "198.51.100.66"
      # 192.168.1.20 is inside a trusted network, so the broker takes it for a proxy that saw
      # 198.51.100.66. Listing a whole network trusts every host on it to append honestly.

    Scenario Outline: When the proxy's own entry is missing or broken, the request is keyed on the proxy
      Given the broker's trusted proxies are "10.0.0.1"
      When a request arrives from "10.0.0.1" with X-Forwarded-For "<xff>"
      Then the broker takes the caller to be "10.0.0.1"

      Examples:
        | xff                         |
        | not-an-address              |
        | 203.0.113.7, not-an-address |
        | 203.0.113.7,                |
        | unknown                     |
        | []                          |

    Scenario: A trusted proxy that sends no X-Forwarded-For is keyed as itself
      Given the broker's trusted proxies are "10.0.0.1"
      When a request arrives from "10.0.0.1" with no forwarding headers
      Then the broker takes the caller to be "10.0.0.1"

    Scenario Outline: Single-address headers are not read, even from a trusted proxy
      Given the broker's trusted proxies are "10.0.0.1"
      When a request arrives from "10.0.0.1" with X-Forwarded-For "203.0.113.7" and header "<header>" set to "<value>"
      Then the broker takes the caller to be "203.0.113.7"

      Examples:
        | header           | value             |
        | CF-Connecting-IP | 198.51.100.66     |
        | X-Real-IP        | 198.51.100.66     |
        | True-Client-IP   | 198.51.100.66     |
        | Forwarded        | for=198.51.100.66 |

  Rule: The operator names the proxies

    Scenario Outline: A list of addresses and networks
      Given the broker's trusted proxies are "<setting>"
      When a request arrives from "<peer>" with X-Forwarded-For "<xff>"
      Then the broker takes the caller to be "<caller>"

      Examples:
        | setting                   | peer          | xff                   | caller      |
        | 10.0.0.1                  | 10.0.0.1      | 203.0.113.7           | 203.0.113.7 |
        | 10.0.0.1                  | 10.0.0.2      | 203.0.113.7           | 10.0.0.2    |
        | 10.0.0.1                  | 10.0.0.1      | 203.0.113.7, 10.0.0.9 | 10.0.0.9    |
        | 10.0.0.1                  | 10.0.0.1      | 10.0.0.1              | 10.0.0.1    |
        | 10.0.0.0/24               | 10.0.0.77     | 203.0.113.7           | 203.0.113.7 |
        | 10.0.0.1, 192.168.50.0/24 | 192.168.50.9  | 203.0.113.7           | 203.0.113.7 |
        | 2001:db8:1::/48           | 2001:db8:1::9 | 203.0.113.7           | 203.0.113.7 |
        | 198.51.100.0/24           | 198.51.100.4  | 203.0.113.7           | 203.0.113.7 |
        | ::ffff:10.0.0.1           | 10.0.0.1      | 203.0.113.7           | 203.0.113.7 |
        | 10.0.0.1,,10.0.0.2        | 10.0.0.2      | 203.0.113.7           | 203.0.113.7 |
        | none                      | 10.0.0.1      | 203.0.113.7           | 10.0.0.1    |
        | NONE                      | 127.0.0.1     | 203.0.113.7           | 127.0.0.1   |

    Scenario: A blank setting trusts no peer
      Given the broker's trusted proxies are ""
      When a request arrives from "10.0.0.1" with X-Forwarded-For "203.0.113.7"
      Then the broker takes the caller to be "10.0.0.1"

    Scenario Outline: A proxy setting the broker cannot read stops it at startup
      When the broker starts with trusted proxies "<setting>"
      Then it refuses to start, naming "<bad>"

      Examples:
        | setting             | bad                 |
        | 10.0.0.300          | 10.0.0.300          |
        | 10.0.0.0/33         | 10.0.0.0/33         |
        | proxy.internal      | proxy.internal      |
        | 10.0.0.1, nope      | nope                |
        | none, 10.0.0.1      | none                |
        | ::ffff:10.0.0.0/104 | ::ffff:10.0.0.0/104 |
        | 0.0.0.0/0           | 0.0.0.0/0           |
        | ::/0                | ::/0                |
        | 10.0.0.1/24         | 10.0.0.1/24         |
        | ,                   | ,                   |

    Scenario Outline: A country header the broker cannot use stops it at startup
      When the broker starts with country header "<setting>"
      Then it refuses to start, naming "<bad>"

      Examples:
        | setting          | bad              |
        | CF IPCountry     | CF IPCountry     |
        | CF-IPCountry:    | CF-IPCountry:    |
        | X-Forwarded-For  | X-Forwarded-For  |
        | CF-Connecting-IP | CF-Connecting-IP |
        | x-real-ip        | x-real-ip        |
        | Forwarded        | Forwarded        |

    Scenario: The broker says what it trusts when it starts
      When the broker starts with trusted proxies "10.0.0.1, 192.168.50.0/24"
      Then its startup log reads "forwarding headers believed from 10.0.0.1/32, 192.168.50.0/24"
      When the broker starts with no trusted-proxy setting
      Then its startup log reads "no proxy trusted, every request is keyed on its TCP peer"
      When the broker starts with trusted proxies "none"
      Then its startup log reads "no proxy trusted, every request is keyed on its TCP peer"
      When the broker starts with country header "CF-IPCountry"
      Then its startup log reads "country from CF-IPCountry"

  Rule: Every per-address control sees the same caller

    Scenario: A direct caller cannot reset its report allowance by forging headers
      Given no proxy is named
      And a caller at "203.0.113.5" has used up its report allowance
      When it reports again with CF-Connecting-IP "198.51.100.7" and X-Forwarded-For "198.51.100.8"
      Then the report is refused with 429

    Scenario: Behind a named proxy, two callers keep separate allowances
      Given the broker's trusted proxies are "10.0.0.1"
      And a caller at "203.0.113.5" behind the proxy "10.0.0.1" has used up its report allowance
      When a caller at "203.0.113.6" reports through the same proxy
      Then the report is not refused with 429

    Scenario: Behind a proxy the broker was not told about, every caller shares the proxy's allowance
      Given no proxy is named
      And a caller at "203.0.113.5" behind the proxy "10.0.0.1" has used up its report allowance
      When a caller at "203.0.113.6" reports through the same proxy
      Then the report is refused with 429

    Scenario: No other broker code reads a forwarding header or the connection's address
      When the broker's source is scanned for forwarding and country header names and RemoteAddr
      Then only the client-address code names them

  Rule: The country is read only from the header the operator names, from a trusted proxy

    Scenario Outline: The origin tally's country
      Given the broker's trusted proxies are "10.0.0.1"
      And the operator names "<country header>" as the country header
      When a request arrives from "<peer>" with header "<sent>" set to "<value>"
      Then the request's country is counted as "<country>"

      Examples:
        | country header | peer        | sent          | value | country |
        |                | 10.0.0.1    | CF-IPCountry  | US    | unknown |
        | CF-IPCountry   | 10.0.0.1    | CF-IPCountry  | US    | US      |
        | CF-IPCountry   | 10.0.0.1    | CF-IPCountry  | de    | DE      |
        | CF-IPCountry   | 203.0.113.5 | CF-IPCountry  | US    | unknown |
        | CF-IPCountry   | 10.0.0.1    | X-Country     | CN    | unknown |
        | CF-IPCountry   | 10.0.0.1    | CF-IPCountry  | XX    | XX      |
        | CF-IPCountry   | 10.0.0.1    | CF-IPCountry  | T1    | T1      |
        | CF-IPCountry   | 10.0.0.1    | CF-IPCountry  | USA   | unknown |
        | CF-IPCountry   | 10.0.0.1    | CF-IPCountry  | <b>   | unknown |
        | CF-IPCountry   | 10.0.0.1    | CF-IPCountry  | U$    | unknown |
        | cf-ipcountry   | 10.0.0.1    | CF-IPCountry  | FR    | FR      |
        | X-Geo-Country  | 10.0.0.1    | X-Geo-Country | BR    | BR      |

    Scenario: Two country lines are no country
      Given the broker's trusted proxies are "10.0.0.1"
      And the operator names "CF-IPCountry" as the country header
      When a request arrives from "10.0.0.1" with header "CF-IPCountry" lines "US" and "DE"
      Then the request's country is counted as "unknown"
