# OPS - ALERT DELIVERY ("page once, page everyone, never lose the page").
#
# THE INCIDENT (verified, prod broker logs 2026-09-07/08): 109 "email: resend error 429 ...
# rate_limit_exceeded ... 10 requests per second" lines. Cause: adminAlert() fans out one
# sendEmail per recipient, each an independent goroutine POST (email.go sendEmail -> go deliver),
# with no queue, no pacing, no retry. When the house rig flapped, checkProviderGapAlerts fired
# for several models in the SAME tick on BOTH instances: 6 models x 3 recipients x 2 instances =
# 36 POSTs inside a second against a 10/s provider cap. Resend dropped the excess; those pages
# were lost, not delayed. Separately (features memory 2026-07-08): the onset dedup (alertFiring)
# is per-process, so two instances page twice for one event; a deploy's 45-105s heartbeat gap
# pages "0 providers" for every model; and a flapping station re-pages on every recovery.
# Meanwhile sign-in codes and top-up receipts share the same sendEmail path and can be starved by
# an alert burst.
#
# THE POSTURE THIS SPEC PINS:
#   1. ONE SEND QUEUE PER INSTANCE, PACED. All outbound email goes through a bounded FIFO drained
#      by a single sender at <= ROGERAI_EMAIL_RATE per second (default 4, so two instances stay
#      under a 10/s provider cap). Enqueue never blocks the caller (sendEmail keeps its contract).
#   2. TWO LANES. Transactional mail (sign-in code, top-up receipt, cap notice, ban/warn notices to
#      operators) is sent ahead of ops alerts. An alert burst can delay alerts, never a login.
#   3. RETRY WITH BACKOFF. A provider 429/5xx/transport error is retried up to 3 times honoring
#      Retry-After (else 1s, 2s, 4s + jitter); then dropped with a loud line and a counter. Never
#      retried inline on the caller.
#   4. COALESCE THE BURST. Alerts raised within one coalescing window (default 5s) are sent as ONE
#      digest email per recipient ("[RogerAI ALERT] 6 conditions" with a table), not N emails.
#   5. DEDUP ACROSS INSTANCES. The onset dedup lives in the shared store (SETNX rogerai:alert:<key>
#      with a TTL) with the per-process map as fallback, so one condition pages once per onset
#      regardless of instance count. CLEARED deletes the key.
#   6. DEPLOY GRACE + DEBOUNCE. No noproviders page during the first ROGERAI_ALERT_GRACE (120s)
#      after boot, and a model must be absent for 2 consecutive ticks before it pages.
#   7. FLAP SUPPRESSION. A key that fires 3 times within an hour is muted to a single "flapping"
#      page and a summary when it has stayed clear 30 minutes.
#   8. ALERTING REMAINS BEST-EFFORT AND NEVER BLOCKS SERVING (unchanged: no alert path touches a
#      relay's latency or outcome).
#
# GROUND TRUTH (origin/main e7d492d5): cmd/rogerai-broker/alerts.go adminAlert/alertClear (per-
# process alertFiring, alertMu), checkProviderGapAlerts (per-tick drop/restore), alertCheckerLoop
# (1m ticker), checkHealthAlerts, checkCSAMSLAAlert; email.go sendEmail (go deliver, swallow) /
# deliver (provider payloads, "email: resend error %d" log); emaillogin.go sendSignInCode;
# emailstore.go (Valkey INCR/PEXPIRE allow script - a fixed-window primitive to reuse for the
# flap counter); sharedstore.go counters (rogerai:ctr:*). The 2026-07-08 alert-burst diagnosis
# (deploy churn, per-process dedup, double-page) informs 5-7. Step definitions: a real httptest
# email provider that records POST timestamps and can script 429/5xx with Retry-After, miniredis
# for the shared store, godog, no mocks.
#
# KNOBS: ROGERAI_EMAIL_RATE=4/s  ROGERAI_EMAIL_QUEUE=1000  ROGERAI_EMAIL_RETRIES=3
#        ROGERAI_ALERT_COALESCE=5s  ROGERAI_ALERT_GRACE=120s  ROGERAI_ALERT_DEBOUNCE_TICKS=2
#        ROGERAI_ALERT_FLAP_COUNT=3  ROGERAI_ALERT_FLAP_WINDOW=1h  ROGERAI_ALERT_FLAP_QUIET=30m
#        ROGERAI_ALERT_DEDUP_TTL=24h

Feature: Alert and transactional email delivery is paced, prioritized, retried, coalesced, and deduplicated across instances

  Background:
    Given a broker with ADMIN_EMAIL set to three recipients
    And an httptest email provider that records every POST with its timestamp

  # ===========================================================================
  # 1. PACING - never faster than the provider allows
  # ===========================================================================

  Scenario: sends are paced at the configured rate
    Given the email rate is 4 per second
    When 20 emails are enqueued in the same millisecond
    Then the provider receives all 20
    And no 1-second window contains more than 4 POSTs

  Scenario: enqueue never blocks the caller
    Given the provider hangs on every POST
    When 50 emails are enqueued from a request goroutine
    Then each enqueue returns in under 1 millisecond
    And the request goroutine is never blocked on delivery

  Scenario: a full queue drops the newest ALERT and counts it, never a transactional mail
    Given the email queue capacity is 5 and the sender is paused
    And 5 ops alerts are queued
    When a sign-in code is enqueued
    Then the sign-in code is queued and the oldest ops alert is evicted with reason "queue-full"
    When a 6th ops alert is enqueued
    Then it is dropped with reason "queue-full" and the counter email_dropped{queue-full} reads 2

  Scenario: two instances together stay under the provider cap
    Given two broker instances each at 4 per second
    When both burst 20 alerts at once
    Then no 1-second window across both providers' logs contains more than 8 POSTs

  Scenario: a disabled mailer is still a no-op that logs once
    Given no email provider key is set
    When 10 alerts fire
    Then nothing is queued and one "transactional email disabled" line is logged

  # ===========================================================================
  # 2. LANES - a login never waits behind an alert burst
  # ===========================================================================

  Scenario: transactional mail jumps the queue
    Given the email rate is 1 per second
    And 10 ops alerts are queued
    When a sign-in code is enqueued
    Then the sign-in code is the next POST the provider receives

  Scenario Outline: every transactional kind rides the priority lane
    Given 10 ops alerts are queued
    When a <kind> email is enqueued
    Then it is delivered before any of the alerts

    Examples:
      | kind                    |
      | sign-in code            |
      | top-up receipt          |
      | cap notice              |
      | operator warning        |
      | operator ban notice     |
      | payout notice           |

  Scenario: within a lane, order is FIFO
    When alerts A, B, C are enqueued in that order
    Then the provider receives A, B, C in that order

  # ===========================================================================
  # 3. RETRY - a provider hiccup delays a page, it does not lose it
  # ===========================================================================

  Scenario: a 429 with Retry-After is retried after that many seconds and delivered
    Given the provider returns 429 with "Retry-After: 2" once, then 200
    When an alert is sent
    Then the provider received 2 POSTs at least 2 seconds apart
    And the alert was delivered
    And the counter email_retries reads 1

  Scenario: a 429 without Retry-After backs off 1s, 2s, 4s with jitter
    Given the provider returns 429 with no Retry-After three times, then 200
    When an alert is sent
    Then the gaps between POSTs are about 1s, 2s, 4s (+-25%)
    And the alert was delivered on the 4th POST

  Scenario: after the retry budget the email is dropped loudly
    Given the provider returns 500 forever
    When an alert is sent
    Then the provider received 4 POSTs (1 + 3 retries)
    And one "email: DROPPED after 3 retries (to=<masked> subj=...)" line is logged
    And the counter email_dropped{retries} reads 1

  Scenario Outline: which statuses retry and which do not
    Given the provider returns <status> once, then 200
    When an alert is sent
    Then the provider received <posts> POSTs

    Examples:
      | status | posts |
      | 429    | 2     |
      | 500    | 2     |
      | 502    | 2     |
      | 503    | 2     |
      | 400    | 1     |
      | 401    | 1     |
      | 403    | 1     |
      | 422    | 1     |

  Scenario: a transport error retries like a 5xx
    Given the provider closes the connection once, then 200
    When an alert is sent
    Then the alert was delivered on the 2nd POST

  Scenario: retries do not break pacing
    Given the email rate is 4 per second
    And the provider returns 429 for the first 10 POSTs
    When 20 alerts are sent
    Then no 1-second window contains more than 4 POSTs including retries

  Scenario: retries never happen on the caller's goroutine
    Given the provider returns 429 with "Retry-After: 5"
    When an alert fires from the alert checker
    Then the checker tick completes in under 100 milliseconds

  # ===========================================================================
  # 4. COALESCING - one event, one email per recipient
  # ===========================================================================

  Scenario: alerts raised within the coalescing window become one digest per recipient
    Given the coalescing window is 5 seconds
    When 6 "noproviders:<model>" conditions fire in the same checker tick
    Then each recipient receives exactly ONE email
    And its subject is "[RogerAI ALERT] 6 conditions: model gpt-oss-120b has 0 providers (+5 more)"
    And its body lists all 6 conditions with their facts
    And the provider received 3 POSTs total, not 18

  Scenario: a lone alert is not delayed by the coalescing window more than the window
    When one alert fires
    Then it is delivered within the coalescing window plus pacing

  Scenario: a CSAM SLA alert is never coalesced or delayed
    When a "csam_sla" alert fires alongside 6 noproviders alerts
    Then the CSAM alert is sent immediately as its own email on the priority lane
    And the 6 others become one digest

  Scenario: a digest's CLEARED lines are logged per condition (audit unchanged)
    Given 6 conditions fired as one digest
    When all 6 clear
    Then 6 "alert: CLEARED" log lines exist

  # ===========================================================================
  # 5. CROSS-INSTANCE DEDUP - one onset, one page
  # ===========================================================================

  Scenario: two instances page once for the same onset
    Given two broker instances share the store
    When both detect "noproviders:m" in the same minute
    Then exactly one digest email per recipient is sent
    And the store holds rogerai:alert:noproviders:m with a TTL

  Scenario: CLEARED removes the shared key so a re-onset re-pages
    Given "noproviders:m" fired and the key exists
    When the model returns on air and the checker clears it
    Then the key is deleted
    When the model drops again after the debounce
    Then it pages again

  Scenario: the dedup key expires so a stuck condition is re-paged daily
    Given "noproviders:m" fired 25 hours ago and is still firing
    Then it pages once more (TTL 24h elapsed)

  Scenario: the shared store being unreachable falls back to the per-process map
    Given the shared store is unreachable
    When "noproviders:m" fires on both instances
    Then each instance pages once (today's behavior) and logs the fallback once

  Scenario: dedup keys are namespaced and never collide with rate-limit keys
    Then the alert key prefix is "rogerai:alert:" and no other subsystem writes under it

  # ===========================================================================
  # 6. DEPLOY GRACE + DEBOUNCE - a deploy is not an outage
  # ===========================================================================

  Scenario: no noproviders page in the first 120 seconds after boot
    Given the broker booted 30 seconds ago and re-hydrated 41 registrations
    When the checker sees "m" with 0 providers
    Then no alert fires and one "alerts: in startup grace" line is logged

  Scenario: a model must be absent for two consecutive ticks before it pages
    Given the grace has elapsed and "m" was on air
    When the checker sees "m" absent on one tick and present on the next
    Then no alert fires
    When the checker sees "m" absent on two consecutive ticks
    Then "noproviders:m" fires once

  Scenario: health alerts (db down, valkey down) are not debounced
    When the checker sees the durable store unreachable on one tick
    Then "db_down" fires immediately

  Scenario: the CSAM SLA alert is not subject to grace
    Given the broker booted 10 seconds ago
    And a CSAM incident has been queued longer than the SLA
    When the checker runs
    Then "csam_sla" fires immediately

  # ===========================================================================
  # 7. FLAP SUPPRESSION - a bouncing station pages once, then summarizes
  # ===========================================================================

  Scenario: the third onset within an hour mutes the key
    When "noproviders:m" fires, clears, fires, clears, and fires again within 40 minutes
    Then three pages were sent
    And the third page's subject ends with "(flapping - further onsets muted until 30m quiet)"
    When it clears and fires a fourth time 10 minutes later
    Then no email is sent and "alert: MUTED (flapping) noproviders:m" is logged

  Scenario: a muted key summarizes when it has been quiet for 30 minutes
    Given "noproviders:m" is muted after flapping 7 times
    When it stays clear for 30 minutes
    Then one "[RogerAI ALERT] noproviders:m stabilized after 7 onsets" email is sent per recipient
    And the mute is lifted

  Scenario: muting is per key
    Given "noproviders:m" is muted
    When "noproviders:other" fires for the first time
    Then it pages normally

  Scenario: the flap counter lives in the shared store with the window as TTL
    Given two instances
    When "noproviders:m" flaps across both instances
    Then they count toward one flap total

  # ===========================================================================
  # 8. OBSERVABILITY + ISOLATION
  # ===========================================================================

  Scenario: /admin/live exposes the email queue counters
    When the founder reads /admin/live
    Then it shows email_queued, email_sent, email_retries, email_dropped by reason, alerts_coalesced, alerts_deduped, alerts_muted, and the queue depth per lane

  Scenario: an alert storm has no effect on relay latency
    Given the provider returns 429 forever and 500 alerts fire
    When a funded consumer relays a prompt
    Then the relay completes at the station's speed

  Scenario: a broker shutdown flushes the transactional lane within the drain budget
    Given 3 sign-in codes and 50 alerts are queued
    When the broker receives a stop signal with a 3 second drain budget
    Then the 3 sign-in codes are delivered first
    And undelivered alerts are counted as dropped{shutdown}

  # ===========================================================================
  # 9. REGRESSION - the 2026-09-07 rig flap replayed
  # ===========================================================================

  Scenario: the house rig dropping 6 models on 2 instances pages each recipient once, loses nothing
    Given two instances, three recipients, a 10/s provider, and 6 models that drop in one tick
    When the checker runs on both instances after the debounce
    Then the provider received exactly 3 POSTs, all 200
    And zero "resend error 429" lines were logged
    And each recipient received one digest naming all 6 models

  # ===========================================================================
  # 10. REGRESSIONS - the 76642e33 review (each a bug the reviewer found, pinned)
  # ===========================================================================

  Scenario: a failed DEL on clear does not silence the next real onset
    Given "noproviders:m" fired and the key exists
    And the shared store fails every command
    When the model returns on air and the checker clears it
    Then the clear is pending and the key still exists
    When the shared store recovers
    And the condition re-onsets before the checker retries the clear
    Then it pages again
    And the key is claimed again and no clear is pending

  Scenario: a pending clear is retried on the next checker tick
    Given "noproviders:m" fired and the key exists
    And the shared store fails every command
    When the model returns on air and the checker clears it
    And the shared store recovers
    And the checker runs
    Then the key is deleted
    And no clear is pending

  Scenario: an absurd ROGERAI_EMAIL_RETRIES does not panic the sender
    Given the email retries knob is 40
    And the provider returns 500 forever
    When an alert is sent
    Then the email is eventually dropped after 40 retries
    And the sender is still alive

  Scenario: an alert onset never blocks its caller while the shared store hangs
    Given the shared store hangs on every command
    When the /billing drift check fires an alert
    Then the check returns in under 50 milliseconds
    And the page is still sent once the store answers

  Scenario: a never-muted key is forgotten after the flap window
    Given "noproviders:m" fires once and clears
    When the flap window elapses and the checker runs
    Then the flap table no longer holds "noproviders:m"

  # ===========================================================================
  # 11. REGRESSIONS - the claude-audit on the merged branch (2026-09-08), each pinned
  # ===========================================================================
  # A. The coalescing flush ran on an untracked goroutine: onsets raised inside the window
  #    right before SIGTERM were lost silently (no send, no dropped{shutdown}, no log line).
  # B. The shared 24h onset claim was DEL'd only when the LOCAL mirror was firing. After a
  #    restart during which the model recovered inside the startup grace, no instance ever
  #    cleared the claim, so the next real drop within 24h was DEDUPED on every instance (a
  #    regression vs per-process dedup). Now: a model seen on air for the first time by a
  #    process releases any claim a previous process left, and the claim is a short lease
  #    (a few checker intervals) that only a firing instance keeps alive, so an orphan
  #    expires within minutes. The daily re-page of a stuck condition is a local timer plus
  #    a separate cross-instance claim (rogerai:alert:repage:<key>).
  # C. first_* milestone keys re-paged after the 24h TTL with a "first" subject.
  # D. csam:first-report rode the coalesced alert lane behind transactional mail; only
  #    csam_sla bypassed coalescing.

  Scenario: onsets raised inside the coalescing window before shutdown are flushed, not lost
    Given the coalescing window is 5 seconds
    When 3 "noproviders:<model>" conditions fire inside the coalescing window
    And the broker receives a stop signal 1 second later with a 3 second drain budget
    Then all 3 conditions reach the provider as one digest per recipient, or are counted dropped{shutdown} with a log line
    And every queued email is accounted for: email_queued equals email_sent plus email_dropped

  Scenario: a claim orphaned by a restart does not silence the next real onset
    Given "noproviders:m" fired and the key exists
    When the instance restarts
    And the model returns on air during the startup grace
    And the model drops again after the grace and the debounce
    Then it pages exactly once

  Scenario: a firing instance keeps its shared claim alive across checker ticks
    Given "noproviders:m" fired and the key exists
    When 10 checker ticks pass a minute apart with the model still absent
    Then the store still holds rogerai:alert:noproviders:m with a TTL

  Scenario: a claim no instance is firing expires within minutes
    Given "noproviders:m" fired and the key exists
    When the instance restarts
    And 5 minutes of checker ticks pass on the restarted instance
    Then the store no longer holds rogerai:alert:noproviders:m

  Scenario: the daily re-page of a stuck condition is claimed once across instances
    Given two broker instances share the store
    And both detect "noproviders:m" in the same minute
    When 25 hours pass with the model still absent on both instances
    Then it pages once more (TTL 24h elapsed)

  Scenario Outline: a milestone never re-pages as "first"
    Given the "<milestone>" milestone fired
    When 25 hours pass and the same milestone is raised again
    Then no further email is sent

    Examples:
      | milestone         |
      | first_ban         |
      | first_dispute     |
      | first_report      |
      | first_live_topup  |
      | csam:first-report |

  Scenario: every csam-prefixed alert bypasses coalescing
    When a "csam_sla" alert and a "csam:first-report" alert fire alongside 6 noproviders alerts
    Then each CSAM alert is sent immediately as its own email on the priority lane
    And the 6 others become one digest
