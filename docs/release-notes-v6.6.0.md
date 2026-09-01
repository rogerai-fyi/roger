# v6.6.0 - the ten-percent ruling

One platform fee, one number, both planes: **10%**.

- **Operators keep 90%** of every token they serve (was 70%). Same prices, same
  dial - a bigger share of the same request.
- **Curated bands post list + 10%** (was +30%). The operator pass-through is
  unchanged: the declared upstream list, whole. The consumer's posted price drops
  on every curated band the moment its station re-registers.
- **Relay through a Tower splits 90/5/5** (station / tower operator / platform,
  was 70/10/20). The tower's share still comes out of the platform's margin, so
  "90% to the station, always" holds with or without a relay.

Why: we researched the visible routing-fee market before setting the number -
aggregators cluster at ~5% and 0% exists; 30% was roughly six times market. 10%
is the honest, defensible tenth, and it makes the operator pitch one sentence.

Every surface moved together: the money specs and their executable scenarios, the
settlement and edge-billing tests, the pricing page and its calculator, the FAQ,
the ToS (fee changes apply to future usage, as it has always said), the operator
broadcasts with their arithmetic recomputed, and the manual. The fee remains a
deployment setting (--fee / ROGERAI_FEE); what changed is the default and the
promise around it, now pinned by its own scenario.
