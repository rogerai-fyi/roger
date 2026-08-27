# v6.3.0 - your rig comes back on air by itself, and its numbers are true

A minor, not a patch: sharing gains a real memory across restarts. Nothing breaks. No API
change, no migration, no configuration change, and the module path is unchanged at
`rogerai.fm/roger/v6`. Existing config is read as-is; a rig that has never expressed a
preference behaves exactly as it did.

## Auto-start: the models you share stay shared

Until now, closing `roger` took your rig off the market and only you would notice - by not
noticing, which is worse. Every restart was a silent un-sharing, and the only evidence was an
empty SHARE table nobody had reason to look at.

Putting a model on air now arms it to come back on air the next time `roger` launches. There
is nothing to configure: the models you already share are the models that return.

```
ON AIR qwen3.8-27b llama-3.1-8b
```

The default is opt-out, and the band card's new `s` key is how you opt out (or back in):

```
  on air      ON AIR                                  a  toggle
  at launch   on · goes on air when roger starts      s  auto-start this model
```

A model you have not decided about renders its absence rather than guessing at it
(`- · arms itself when you put it on air`), which is what keeps the opt-out default honest.

That default is only safe because the decision is genuinely three-state, not two. "Never
decided" and "decided no" are different things, kept apart by presence rather than by a
boolean - in the controller and in `config.json`, where `auto_start` is written only once you
have actually chosen. A model you turn off stays off, even if you put it on air by hand for a
single session. Turning something on for one evening never silently re-arms it.

### Several copies of roger, no fight

Launching a second `roger` with the same armed models does not fight the first. The
per-node-id on-air lock already guaranteed that - it exists because two broadcasters on one
node id once made the broker see a station flapping between upstreams, scrambling earnings
attribution. What was missing was the *report*: a second instance finding its models already
on air is the system working, so it is now stated plainly rather than raised as an error.

Every model that did not start is named, with its reason:

```
ON AIR llama-3.1-8b · already on air in another roger: qwen3.8-27b · needs login:
mixtral-8x7b · over the on-air cap: phi-4
```

A count would tell you something was skipped without telling you what, which is the worse
half of saying nothing.

## A probe is not traffic

The broker sends every shared node a small unbilled canary - a liveness check, and a
tool-call check - so the market can report which stations are reachable and how fast they
are. That work is real, but it is not *your* traffic, and it was being counted as though it
were: served requests, output tokens, and the node's running value all included it.

On one real station that was 2,738 of the requests it reported, against 48,001 output tokens
- 17.5 tokens a request, which is a canary, not a conversation. The number an operator uses
to judge whether sharing is worth it was inflated by work nobody paid for.

Probes are now tallied separately and kept out of every operator-facing figure. They are
hidden, not discarded: a rig that looks busy can still say what it is busy with, and the
split travels in the node snapshot as `probes` / `probe_tokens`, so the terminal and the
browser console cannot disagree about what the same machine did. A station with no canary
traffic reports no probe fields at all, rather than zeros that would read as measurements.

The broker's own figures were never affected. Receipts are written only on settlement, and a
probe never settles - `roger payout` and the account pages have always been right. This
closes the gap between them and the terminal.

## We stopped probing hardest where we already knew the most

Measuring the above turned up a scheduling bug worth more than the accounting fix.

The probe cadence is adaptive: 30 seconds when a node is interesting, doubling while idle up
to a 15-minute ceiling. When a node serves a real request, the broker records that as a free
measurement - the node did actual work, and the reading it produced is better than any canary
could give. But it *also* reset the backoff to the floor, which is the opposite of what the
code's own comment promised ("an actively-used node is barely probed"). The busiest nodes
were probed hardest.

Measured on the shipped arithmetic, for a node sharing one model:

| your traffic | before | after |
|---|---|---|
| idle | 200 | 200 |
| used hourly | 384 | 200 |
| used every 10 minutes | 1,212 | 200 |
| used every 2 minutes | 2,880 | 196 |

(unbilled requests landing on your machine per day)

Operators were paying GPU time for being useful, up to 14x over. Real traffic now defers the
next probe instead of pulling it in, so probe cost is flat in traffic rather than growing
with it. The deferral is deliberately bounded - never past one ceiling since the last real
probe - because the tool-call verdict refreshes only inside a probe round and real traffic
does not assert it.

Liveness never rode on this and is unchanged: a node that goes away is dropped by the
heartbeat within 45 seconds, twenty times tighter than the probe ceiling and entirely
separate from it.

Separately, the tool-call canary fired once per shared model on *every* round, so probe cost
grew with how many models you share - the behaviour the product exists to encourage. Earning
the capability is untouched and just as fast; only re-proving a verdict that is already
settled slows down, to once per 20 minutes. A node sharing four models drops from 480 to 384
unbilled requests a day on top of the change above.

## AT LIST is not earnings

The SHARE table had a column headed EARNINGS that was not earnings. It is a node-local tally
the node computes from its own price card - the node never learns what the broker actually
charged - and serving your own traffic is free by design. So a rig serving its owner accrued
a number there while the ledger stayed at zero, which is exactly what happened on a real
machine: `$0.27` on screen, `$0.00` payable and `$0.00` held.

The column is now headed AT LIST, and says what it is beside the number rather than in
documentation:

```
AT LIST is this work priced at your card - not settled money.
Serving your OWN traffic is $0. Real earnings: roger payout
```

The price cell was wrong in a second way, and the two compounded. It showed only the output
price, while billing runs on both axes - and on that same machine the hidden input price was
`$0.20/1M`, twenty times the `$0.01/1M` on display. Chat workloads are mostly prompt, so the
term doing most of the billing was the one you could not see. Both axes are shown now.

## You share a local model, not a GPU

The TUI, the onboarding wizard, and `--help` all described sharing as putting your *GPU* on
air. You share a model. The machine underneath it might be a GPU, an Apple Silicon laptop, or
a CPU box, and the distinction matters to anyone deciding whether they qualify. The copy now
says local model throughout.

## Upgrading

```sh
curl -fsSL https://rogerai.fyi/install.sh | sh
```

Nothing to migrate. The first launch after upgrading behaves exactly as before, because no
model has an auto-start decision recorded yet; the models you put on air from then on will
come back on their own.
