# v6.5.0 - Curated providers

The dial gets fuller without getting dishonest. A station may now relay a commercial
API as a **curated** proxy - declared, labeled, and priced by one network rule - and
towers can serve those providers on both of their planes.

## The deal, in one paragraph

A curated operator declares the upstream's list price. The network posts **list + 30%**:
the list goes back to the operator as pass-through (reimbursement, not margin), and the
30% is the network's routing fee - anonymized routing through the broker/tower fabric,
receipts and usage history, failover, and best-connection selection. Nobody is ever
underwater: a posted price below the declared list is refused at the door, and the
derivation is the only price a curated offer can carry.

## What you can do

- `roger share --curated <provider> --upstream <url> --upstream-price-in/out ...`
  puts a curated station on the public band (requires an explicit upstream; a free
  upstream stays free).
- `roger-tower attach --curated <provider> ...` admits a labeled proxy on a
  standalone Tower's own network - free, like everything on the local plane, with
  the label on discovery, answers (`X-Roger-Curated`) and receipts.
- A joined tower's inventory may declare `curated_provider` per offer; the curated
  pricing rule is enforced at Core admission.
- On the dial, curated stations wear their own mark (`»provider`) and the `U` key
  hides them; the station count always reads humans and curated apart.
- The web surfaces follow suit: the model directory counts curated apart under its
  own heading, your usage history names the provider and the split on every curated
  request, and a curated operator's dashboard shows pass-through, never "earnings".

## The honesty rules (enforced, not promised)

- A human station cannot wear a provider name; a curated station cannot claim TEE,
  a region, or a time-of-use schedule; a node id can never flip between human and
  curated (broker and tower alike).
- Curated supply never inflates the human-supply story: market rows, signal quality,
  and the dial count it apart - while the 0-provider pager counts it in, because its
  question is "will a request fail?".
- Routing is neutral: at equal terms a curated station is picked by the same rules,
  struck by the same strikes, and failed over like any other station.
- Consumers are indistinguishable to the upstream: the request leaves through the
  station's own key with no consumer identity attached.

## Also in this release

- The spend-limits screen edits the monthly budget in place, and the share table
  refreshes in the background instead of clearing on re-entry.
- The `»` mark joins the shared glyph set with an ASCII fallback (`>>`).

The full spec set (7 feature files, all executable under godog + the web suite) lives
in `features/curated/`.
