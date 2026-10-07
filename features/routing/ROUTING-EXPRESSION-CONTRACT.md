# Routing expression contract (the shared vocabulary for the routing-expression spec set)

**Status:** PROPOSED 2026-09-30, awaiting founder approval. Every `.feature` in this set cites
this file. Nothing here is implemented; the GROUND TRUTH sections in each feature describe the
code as it is at origin/main `518c698b`, and the scenarios describe the behavior after the change.

**Scope of the set:** the CONSUMER side of routing: how a request states what it wants
(model, nodes, price, speed, capability, privacy, size), how the broker honors it, what the
consumer learns back, and how every first-party client exposes it. Station-side offers change
only where a new declared attribute is needed (`params_b`). Money invariants (hold, fee, receipt,
price lock, ceilings) are NOT relaxed anywhere in this set; where a new knob touches money, the
scenario states the invariant it keeps.

---

## 1. The request shape

`POST /v1/chat/completions` (and every relay path that shares its body: agent turns, Playbox,
guest proxy, Tower bridge) accepts three routing carriers. All are optional.

```jsonc
{
  "model":  "qwen3-32b",                       // primary; may carry variant sugar (§4)
  "models": ["qwen3-30b-a3b", "llama-3.3-70b"],// ordered fallbacks AFTER model (§3)

  "provider": {                                // OpenRouter-compatible keys, node ids as "providers"
    "order":           ["node-a", "node-b"],   // strict priority among listed nodes
    "only":            ["node-a", "node-b"],   // allow-list
    "ignore":          ["node-z"],             // deny-list
    "allow_fallbacks": true,                   // false = single attempt, no station failover
    "sort":            "price",                // price | throughput | latency (strict order, no P2C)
    "quantizations":   ["Q8_0", "BF16"],       // verbatim quant labels, case-insensitive
    "max_price":       { "prompt": 0.20, "completion": 0.60, "request": 0.02 }, // $/1M, $/1M, $ per request
    "require_parameters": false                // true = implicit require for tools/json/vision (§5)
  },

  "roger": {                                   // network-specific knobs
    "pref":             "balanced",            // cheap | balanced | fast | reliable (weighted knob)
    "require":          ["tools", "vision"],   // closed set = protocol.knownCapabilities
    "params_b":         [7, 70],               // declared parameter count range, billions, inclusive
    "min_ctx":          32768,                 // declared context window floor
    "min_tps":          20,                    // measured tok/s floor (unmeasured passes)
    "max_ttft_ms":      1500,                  // measured TTFT ceiling (unmeasured passes)
    "trust_min":        "any",                 // any | verified | confidential
    "self_hosted_only": false,                 // true excludes curated (commercial-proxy) stations
    "confidential":     false,                 // TEE-attested only (same as trust_min=confidential)
    "region":           ["eu", "us"],          // node.region membership, unknown region ineligible
    "freq":             "<band code>",         // private band admission (body form of X-Roger-Freq)
    "profile":          "@profile/coding"      // client-side profile reference (§9); broker rejects
  }
}
```

### 1a. Precedence and validation

- **Header and body together: LIMITS compose to the STRICTER, PREFERENCES are body-wins.**
  Every header has a body form (`X-Roger-Node` ≡ `provider.order:[n]` +
  `allow_fallbacks:false`; `X-Roger-Exclude-Nodes` ≡ `provider.ignore`; `X-Roger-Min-TPS` ≡
  `roger.min_tps`; `X-Roger-Max-Price` ≡ `max_price.prompt`; `X-Roger-Max-Price-Out` ≡
  `max_price.completion`; `X-Roger-Pref` ≡ `roger.pref`; `X-Roger-Confidential` ≡
  `roger.confidential`; `X-Roger-Freq` ≡ `roger.freq`). Headers remain supported indefinitely.
  When BOTH forms of one knob are present (founder ruling 2026-10-01, replacing the earlier
  blanket "body wins"):
  - **Limiting knobs compose; neither form can loosen the other.**
    - out-price cap: the LOWER of `X-Roger-Max-Price-Out` and `max_price.completion`. A side
      that is absent or 0 states no cap, so the other side's value applies; both absent or 0
      is the $10 default; the result is still clamped to the register ceiling.
    - in-price cap: the LOWER of `X-Roger-Max-Price` and `max_price.prompt` (absent or 0 on
      one side = no cap on that side).
    - `min_tps`: the HIGHER of `X-Roger-Min-TPS` and `roger.min_tps`.
    - confidential: OR. `X-Roger-Confidential` set with body `confidential:false` stays
      confidential; the stricter of header / body / `trust_min` wins.
    - exclusions: `provider.ignore` and `X-Roger-Exclude-Nodes` are UNIONED.
    - band code: a present `X-Roger-Freq` is the session's band. A body `roger.freq` naming a
      DIFFERENT code is a 400 `conflicting_routing_keys` (the message names the keys, never a
      code); the same code, or no body freq, is fine; a body freq alone works as the header.
    None of these is an error except the differing band code.
  - **Preference knobs are body-wins.** `roger.pref` replaces `X-Roger-Pref`; a body
    `provider.order` replaces the `X-Roger-Node` pin (and drops the pin's implied
    `allow_fallbacks:false` unless the body says false); a body `allow_fallbacks:true` turns
    the header pin into an order of one with fallbacks. A preference changes who is tried
    first among stations the limits already admit, so it cannot widen spend or exposure.
  - Why: a local proxy's OWNER states limits in headers (every already-installed client does)
    while a GUEST application controls the body. Body-wins let a guest body raise the owner's
    price cap, and no client release can repair clients already in the field, so the broker
    itself guarantees a header limit is never loosened by a body.
  - Legacy header leniency is kept: an unknown `X-Roger-Pref` value still means balanced and a
    non-numeric `X-Roger-Min-TPS` still means 0 (today's parsers; a counter is bumped). The
    same values in the BODY are a 400. Old clients keep working; new clients get told.
  - Duplicate JSON keys: last occurrence wins (Go `encoding/json`, as `model` behaves today).
- **Unknown keys under `provider` or `roger` are a 400** naming the key
  (`{"error":{"code":"unknown_routing_key","message":"unknown routing key provider.foo"}}`).
  No silent drop. Top-level OpenAI keys the broker does not understand keep passing through to
  the station untouched, as today.
- **Type and range errors are a 400** with `code:"invalid_routing_value"` naming the key:
  negative or non-finite prices, `params_b` not a 2-array of positive numbers with min ≤ max,
  `min_ctx` ≤ 0, `max_ttft_ms` ≤ 0, empty strings in node lists, more than 32 entries in any
  list, an unknown `sort` / `pref` / `trust_min` / `require` value, `region` entries that are
  not lowercase 2-8 char tokens.
- **Exclusive pairs are a 400** with `code:"conflicting_routing_keys"`: `provider.sort` with
  `roger.pref` (a sort variant suffix IS a sort); `provider.only` with `provider.order` naming
  a node not in `only`; a sort suffix with a different explicit `provider.sort`.
  `allow_fallbacks:false` with an `order` of more than one node is NOT a conflict (it means:
  try in order, but never leave the list). `only` ∩ `ignore` (or `order` ∩ `ignore`) is NOT a
  conflict: deny wins, the node is simply out.
- **`provider.max_price.request`** (per-request USD cap): the hold is sized to the cap, and the
  `max_tokens` sent to the station is LOWERED to what the cap buys at that station's prices
  after the prompt's input cost, so generation stops where the money stops rather than the
  operator eating the overrun (FOUNDER RULING requested; the alternative is bill-clamp only, as
  the hold does today). It is NOT a plan filter, with one exception: a station whose INPUT cost
  alone (measured prompt tokens × in price) meets or exceeds the cap can buy no output and is
  dropped from the plan; every station dropped → 503 `no_match`.
- **`null` means absent** for every routing key at every level (`"ignore": null`,
  `"max_price": {"prompt": null}`, `"allow_fallbacks": null` all fall back to the header or
  the default). Never a 400.
- **Enumerated values are exact lowercase** (`sort`, `pref`, `trust_min`, `require`, `region`
  entries, sugar suffixes): `"Tools"`, `"Verified"`, `"Price"` are 400 `invalid_routing_value`.
  Quant labels are the one case-insensitive comparison (verbatim labels vary by tool).
- **Header pin + body `provider` without `order`:** the pin is kept; a kept pin that is not in
  a body `only` is a 400 `conflicting_routing_keys` (same rule as `order` ∉ `only`).
- **Anonymous callers** whose constraints leave only PAID stations get today's 401 "log in to
  spend" (`anonCannotPay`), never 503: the stations exist, the caller cannot pay them.
- **The routing carriers are stripped before the body reaches a station.** The station sees a
  plain OpenAI body whose `model` is the served model. `models`, `provider`, `roger` never leave
  the broker. (Also on the edge/Tower bridge.)
- **Body size:** the routing object counts toward the existing body limit; no new limit.
- **`provider.max_price.image`** is a 400 (`unsupported_routing_key`) until image pricing exists.

### 1b. Money and admission invariants that every knob keeps

- The consumer out-cap default ($10/1M when absent or 0) still applies (`pricesafety.go`).
  `max_price.completion` above the register ceiling is clamped to the ceiling, not an error.
- A grant's node/model allow-lists, a private band's node set, and the anonymous/free rules
  intersect every consumer constraint. A constraint can only NARROW what a grant or band admits.
- The hold is placed once per request and covers the priciest (model, station) pair in the
  plan; a pair the hold cannot cover is dropped from the plan, never attempted.
- Failover never changes the payer, never crosses free → paid, never double-charges, never
  leaks one station's error body into another's success.
- Moderation runs once on the prompt, before any pick, regardless of any routing knob.

---

## 2. Error codes (relay)

| Situation | Status | `error.code` | Retry-After |
|---|---|---|---|
| malformed / unknown / conflicting routing input | 400 | as in §1a | no |
| grant denies every model in the list | 403 | `grant_model_denied` | no |
| nothing on air satisfies the constraints (any model in the list) | 503 | `no_match` | no |
| matches exist but every one is in 429 cooldown | 503 | `band_cooling` | yes, soonest expiry |
| `allow_fallbacks:false` and the single/listed station(s) unavailable | 503 | `no_match` or `band_cooling` as above | as above |
| private band code unresolvable or band denies every model | 503 | (uniform message, no code that distinguishes) | no |
| every attempt failed upstream | last upstream status | pass-through | yes when 429/503 |

The message text for "no node offers" stays; a `code` is added. 404 is NOT introduced (existing
clients key on 503). EVERY routing refusal carries `X-RogerAI-Cost: 0` and no receipt: the 400
routing errors (`unknown_routing_key`, `invalid_routing_value`, `conflicting_routing_keys`,
`unsupported_routing_key`, `unknown_profile`), the 503 `no_match` / `band_cooling` (and the
uniform band message), and the anonymous 401 "log in to spend". A consumer never has to guess
whether a refused request cost anything.

Further codes used by this set: 400 `unknown_profile`, `unsupported_routing_key`; discovery 400
`unknown_query_param` / `invalid_query_param`; 402 `key_limit_reached` / `monthly_cap_reached` /
`insufficient_balance` with `error.metadata.limit_source` + `remedy_hint`; 403
`key_model_denied` / `key_node_denied` / `key_cannot_manage_keys`; 401 `key_disabled` (and the
existing revoked/expired texts); 409 `already_minted`. A 400 routing error is answered BEFORE
moderation (nothing is screened for a request that cannot route); moderation stays BEFORE the
money gates (monthly cap, key limit) as today.

---

## 3. `models[]` ordered fallback

- Effective list = `[model] ++ models`, de-duplicated preserving first occurrence, max **5**
  entries after de-dup (6th+ → 400 `invalid_routing_value`). `model` absent and `models`
  present → the first entry is primary. Both absent → 400 `invalid_routing_value` whose
  message contains "model is required" (NEW behavior: today a body with no model answers 503
  "no node offers ").
- A model id longer than **256 characters**, in `model` or in any `models[]` entry, is a 400
  `invalid_routing_value` naming the limit; exactly 256 is accepted. (No length rule existed
  before; the bound is explicit so an id can never be used to carry a payload.)
- A grant's `price_in` / `price_out` is the price the GRANT BILLS AT, never a cap on station
  offers: a priced grant bills the served (model, station) pair at the grant's price whichever
  model serves. The consumer's own `max_price` caps still filter station offers under a grant.
- Each entry may carry variant sugar (§4); sugar applies to that entry only, except a `sort`
  variant, which applies to the whole request (last one wins, as OpenRouter).
- The plan is built per model in order: up to `ROGERAI_RELAY_ATTEMPTS` stations for the model,
  under every constraint, then the next model. The request deadline (≥ 10 s left for a new
  attempt) bounds the whole plan.
- **Move to the next model when:** the current model has no eligible station (silently, no
  error); an attempt fails on a station-failover trigger (429, 5xx, station-reported 502,
  2xx-empty) and the current model has no more stations in its plan; the upstream returns the
  context-window 400 (the model is too small for the prompt; recognized by a new
  `context-window` void reason using the same overflow vocabulary the client compacts on,
  `harness.IsContextOverflow`; every other 400 stays terminal). The broker's own declared-ctx
  gate skips a too-small model BEFORE dispatch; the "exceeds the context window" 400 is
  answered only when EVERY listed model on air is too small. **Do NOT move on:** any other
  4xx (the request itself is bad); a moderation verdict (prompt-level, model-independent);
  after the first streamed content frame (same rule as station failover).
- `allow_fallbacks:false` governs STATION failover only: one station per model, and the list
  is still walked (the consumer named those models on purpose). OpenRouter keeps the two
  independent as well.
- The broker rewrites `model` in the forwarded body per attempt (today the body is forwarded
  verbatim); nothing else in the body changes byte-for-byte. Recount and settle are keyed on
  the DISPATCHED model, not the requested one.
- A grant that denies some models: they are skipped; all denied → 403 `grant_model_denied`.
  A band that denies some models: skipped; all denied → the uniform band message.
- Money: billed at the (model, station) that served, at that station's locked price for that
  model; the hold covers the priciest pair in the plan; a free relay (anonymous / free band /
  $0 grant) never plans a paid pair; a pair over the consumer's caps is never planned.
- Transparency: `X-RogerAI-Model` (served model id, no sugar) on every response path; the
  receipt names it; the stream `usage` chunk names it (§7).

---

## 4. Variant sugar on a model id

Recognized suffixes, applied right-to-left, only from this closed set: `:free`, `:floor`,
`:nitro`. Any other `:tag` is part of the model id (Ollama-style ids like `llama3:8b` stay
intact). Meaning: `:free` = only offers that cost THIS CALLER nothing right now (priced 0/0,
`free_now`, or $0 for the caller: self-use of an owned station, a free grant); `:floor` =
`provider.sort:"price"`; `:nitro` = `provider.sort:"throughput"`. Stacking is allowed
(`model:free:nitro`). Sugar plus an explicit conflicting `provider.sort` → 400
`conflicting_routing_keys`. The served model in `X-RogerAI-Model` and the receipt is the bare id.
A station may not REGISTER a model id ending in a sugar suffix (`foo:free` → 400 at register;
persisted legacy rows dropped on re-hydrate, as negative prices are). The `models[]` 5-entry
limit counts bare ids after de-dup of suffixed spellings.

---

## 5. Node selection semantics

- `provider.only` → the `allow` set already in `pickFor`, intersected with grant/band sets.
- `provider.ignore` → the `exclude` set (union with the header).
- `provider.order` → strict priority: attempts go to listed nodes in list order (eligible ones),
  then, if `allow_fallbacks` is true (default), to the remaining eligible nodes by score. Listed
  nodes that are ineligible are skipped without error. `order` disables power-of-two-choices for
  the listed portion only.
- `allow_fallbacks:false` → the plan is exactly the listed eligible nodes (`order` or `only`),
  or a single attempt when neither is given. The broker never adds a station the consumer did
  not name.
- `provider.sort` → strict ordering of the eligible Tier-A set by one metric: `price` = out
  price asc, then in price asc, then score; `throughput` = tps desc, unmeasured last;
  `latency` = ttft asc, unmeasured last. Disables power-of-two-choices. Tier-B is still only used
  when Tier-A is empty. `roger.pref` remains the weighted knob and is exclusive with `sort`.
  `order` + `sort` together: `order` governs the listed portion, `sort` ranks the remainder.
  Under `sort` or `order` (and the sugar that means them) the edge/Tower 50 % coin is REPLACED by the ranking: a Tower offer is
  one more candidate ranked by its own declared price / measured tps / ttft, never preferred or
  excluded by the coin. `pref` keeps the coin.
- `provider.quantizations` → offer.quant ∈ set (case-insensitive, verbatim labels, no
  bucketing); the value `unknown` (as in OpenRouter's list) matches an offer with NO quant
  label. Without `unknown` in the set, an unlabeled offer is ineligible. The TUI's standing
  quant rule (which lets unlabeled rows pass by design, `Limit.acceptsQuant`) maps to
  `quantizations: [<chosen>..., "unknown"]`, so the approved unit test keeps its meaning.
  `CanonicalQuant` is not a closed set, so an unrecognized requested label is a valid request
  that matches nothing (503 `no_match`), not a 400.
- Node ids and Tower ids are distinct namespaces. `only`, `order` and a pin match a Tower by its
  TOWER id only (a row's node id is what the Tower published for the machine behind it, so
  matching it would let a Tower pose as a direct station the consumer named); `ignore` and
  `X-Roger-Exclude-Nodes`, the narrowing direction, match both the Tower id and that node id.
  An id that names both a direct node and a Tower is matched in its own namespace on each side,
  and /admin/live warns once (`namespace_warning`), never a 400.
- Capability gating (`roger.require`, `provider.require_parameters`, and the implicit rule):
  - explicit `require` values come from `protocol.knownCapabilities` (`tools`, `vision` today);
    anything else → 400.
  - implicit: a body with a non-empty `tools` array requires `tools`; a body with any
    `image_url` content part requires `vision`. This is on by default and cannot be turned off;
    it closes the "tools request lands on a no-tools node" defect. `tools: []` and a
    `tool_choice` without `tools` add nothing (the broker is not a schema validator; the body
    is forwarded and the upstream's 400 stays terminal).
  - the tools verdict is keyed per (node, model) (`toolKey`, `toolcall.go:115`): a canary pass
    on model A never verifies model B on the same node; a definitive fail clears it, transient
    failures never touch it, the verdict has a TTL. `/market`'s id-guessed vision
    (`detect.VisionFromID`) is DISPLAY only and never eligibility.
  - `require_parameters:true` additionally requires `tools` when `tool_choice` is set and
    treats `response_format` json as requiring `tools`-verified nodes (the only structured-output
    signal we verify today); false (default) adds nothing beyond the implicit rule.
  - a capability counts only as the broker records it: `tools` is probe-verified (a self-
    declared `tools` is already stripped at register), `vision` is node-declared.
- Trust: `trust_min:"verified"` → node.verified (canary-passed) required; `"confidential"` →
  TEE-attested required (same as `confidential:true`); `"any"` default. Tier-A/B health gating
  is unchanged and independent.
- `self_hosted_only:true` → curated stations ineligible; the TUI "hide curated" filter sends
  this instead of exclude lists. `curated` is a self-declared, signature-covered registration
  flag (no approval record exists, `tunnel.go:295-320`); the approved rule "human → curated
  re-registration is a NEW station identity" (`curated_identity.feature`) is made SYMMETRIC by
  this set: curated → non-curated is also a new identity, so a proxy cannot carry its reputation
  into the self-hosted lane. NEW requirement, not a pin. Residual, stated honestly: a fresh
  identity that lies about being self-hosted is caught by the curated honesty rules and
  probes, not by this filter.
- `trust_min:"confidential"` on the bridge declines the bridge outright (a Tower is a third
  party), consistent with today's confidential gate. An anonymous caller naming a Tower in
  `only`/`order` gets the uniform 503 `no_match` (cost 0, no hold), never a 403 that discloses
  the id is a Tower.
- `verified` (for `trust_min:"verified"`) is canary-only (`verifiedServing`); a recent completed
  real relay does NOT count (approved `verified_serving.feature`).
- A settle the ledger rejects: consumer refunded and the body still returned; operator side: no
  earn row, receipt voided with `settle-failed`, no strike.
- `region` → node.region (self-declared) ∈ set; nodes with no region are ineligible under the
  filter.
- `params_b` → offer.params_b within [min, max]; an ESTIMATED params_b (broker-filled from the
  known-model table, flagged `params_estimated`) is a value and counts; only an offer with no
  params_b and no table match is ineligible. A station-declared params_b that contradicts the
  table is ineligible and flagged (anti-spoof). `min_ctx` → offer.ctx ≥ min AND ctx is
  DECLARED (an estimated ctx is unknown, ineligible under the filter). The existing
  measured-prompt-vs-declared-ctx gate is unchanged.
- `min_tps` / `max_ttft_ms` → measurement-based: unmeasured (0) passes, as `X-Roger-Min-TPS`
  does today.

### 5a. The Core-free Tower local plane

The plane honors `models[]`, `only`, `ignore`, `order`, `allow_fallbacks` over local station
ids (the attach registry knows only station id + model list, `localplane/completion.go:203-214`);
every key it cannot evaluate (`quantizations`, `params_b`, `min_ctx`, `require`, `min_tps`,
`max_ttft_ms`, money caps, region, self_hosted_only, freq, sort, pref) is ignored and NAMED in
`X-Roger-Routing-Ignored` (names only, never values). The approved 404 for a MODEL nobody
offers locally stays and runs first. `confidential` / `trust_min` are evaluated, not ignored:
no attested local station → 503 `no_match`. `@profile/` in either carrier → 400
`unknown_profile`. Constraint misses are 503 `no_match` (never 404), as on the broker.

## 6. Edge / Tower bridge parity

Every constraint in §5 applies on the bridge path exactly as on the direct path. Where a
Tower's offer cannot be evaluated (no quant, no params_b, no region, ...) it is ineligible
under that filter, same as a direct node. The 50 % coin can only choose between two eligible
servers, never widen eligibility. `provider.order` / `only` / `ignore` name node ids; a Tower
is ineligible under `only`/`order` unless its relay id is listed, and ineligible under
`allow_fallbacks:false` unless listed. Closes the min-tps/exclude/pref bridge defect.

The two-tier health gate holds ACROSS fabrics: a Tier-B (probationary) Tower row is used only
when no Tier-A candidate exists on EITHER fabric. A healthy direct station is never passed over
for a probationary Tower, and a healthy Tower is never passed over for a probationary direct
station.

## 7. Transparency

- `X-RogerAI-Model` on every 2xx (direct, bridge, stream, non-stream).
- Streams: the response ends with an OpenAI-shaped final chunk `{"choices":[],"usage":{
  prompt_tokens, completion_tokens, total_tokens, cost, rogerai:{receipt, node, model, relay,
  tokens_in, tokens_out, tps, price_in, price_out, locked_until, balance, void_reason,
  key_*}}}` before `data: [DONE]`, ALWAYS (as OpenRouter; `stream_options.include_usage:false`
  does not suppress it). Because the station's receipt arrives after its final chunk
  (`tunnel.go:2875-2889`), the BROKER owns `[DONE]`: the station's `[DONE]` is swallowed, the
  settle runs, the usage chunk is written with post-recount numbers, then the broker's `[DONE]`
  (a missing receipt: the idle window, then a voided chunk, then `[DONE]`). The `rogerai`
  object appears only in the broker's chunk; a station-emitted `usage` passes through as
  content and never replaces it. The trailing `: rogerai-cost=` comment stays for one release
  for old clients and equals `usage.cost`. Headers known before commit (`Provider`, `Model`,
  `Price`, `Monthly-*`, `Key-*`) are set before the first frame and not re-sent. `X-RogerAI-Model`
  and the other response headers are added to CORS `Access-Control-Expose-Headers`.
- Bridged (Tower) answers carry the receipt too; today they carry none at all
  (`edgebridge.go:126-130`), which is a pin in this set. The node signature on a bridged receipt
  is the Tower's relay key (the party the broker dispatched to); the broker co-signs exactly as
  on the direct path, so `VerifyBroker` covers both.
- `GET /generation?id=<request id>`: two views, mirroring `/console`: the CONSUMER view for the
  signed wallet / browser session / the exact grant token that made the request; the OWNER
  view (no balance, no wallet, no pseudonym) for the payout owner whose station served or was
  tried and for a grant's minting owner; everyone else, an unknown id, and an expired record
  get the same uniform 404. Returns `{id, model_requested, models, served:{node, model, relay},
  cost, tokens_in, tokens_out, tps, ttft_ms, latency_ms, streamed, cancelled, key_id,
  moderation:{mode, verdict, latency_ms}, attempts:[{n, node, model, status, error_code,
  duration_ms, retry_after_s}], receipt, created_at}`. Non-served outcomes (503 no_match /
  band_cooling, 400 routing errors, 402 pre-dispatch, moderation rejection) get a record with
  `attempts: []` (a model skipped for having no eligible station is NOT a synthetic attempt;
  `models` already shows the list); a moderation rejection exposes only `verdict: "rejected"`.
  The OWNER view carries no `receipt` (its `user` is the per-(user, node) pseudonym, and a TRIED
  owner must not see the served owner's node or pseudonym): an owner sees only their own
  attempt(s). Never the prompt, completion, band code or station IP. Void reasons use the
  approved vocabulary (`upstream-throttled`, `empty-output`, ...), plus the new `context-window`
  and `settle-failed`. `key_id` is present; key limit/spend state only in the consumer view. Readable while the console lineage holds it (no new
  retention number is invented); 404 after account deletion. Rate-limited like `/console`;
  works across instances (shared store). CLI: `roger generation <id>` with `--last`, `--json`.

## 8. Discovery

- `/discover` and `/market` honor filter params: `model`, `min_tps`, `max_ttft_ms`,
  `max_price_in`, `max_price_out`, `params_min`, `params_max`, `min_ctx`, `region`, `quant`,
  `capability` (repeatable), `self_hosted=1`, `confidential=1`, `free=1`, `trust_min`. Unknown
  params → 400 (`unknown_query_param`); malformed values → 400 (`invalid_query_param`);
  `freq` as a query param → 400 (private offers stay behind `POST /bands/resolve`). Filtering
  happens after the cache, per request; the cache key becomes the unfiltered feed, so a hundred
  filter combinations are one compute and `/discover` keeps its deliberate no-per-IP-gate
  posture (`market.go:256-263`). `/market` aggregates are computed over the FILTERED providers.
  An empty result is 200 with an empty list.
- `/discover` offers gain `params_b` (number, omitted when undeclared) and `params_estimated`
  (bool, true when the broker filled it from a known-model table rather than the station).
- `GET /v1/models` on the broker: OpenAI shape, one entry per distinct model id on air, sorted
  by id, `created` = first-on-air time, with `rogerai:{providers, min_price_in, min_price_out,
  best_tps, ctx_max (omitted when every ctx is estimated), params_b, params_b_min/max when
  offers disagree, params_estimated, quants, capabilities, verified, free_now, confidential,
  curated, cooling, cooling_until}`; `GET /v1/models/{id}` one entry or 404; same rate limit
  as `/market`; honors the same filters. The LOCAL PROXY's `/v1/models` stays the approved
  one-entry list (`features/proxy/models.feature`) and does not forward to the broker; a guest
  can still send `@profile/<name>` without it being listed. (Listing profiles there is a
  re-ruling candidate, see §12.)

## 9. Profiles (client-side in v1)

- Config `profiles.<name>` holds any subset of §1 (`models`, `provider`, `roger`, plus
  `max_tokens`, `system` are out of scope). `@profile/<name>` in `model` (or `roger.profile`) is
  resolved by the FIRST-PARTY CLIENT (CLI/TUI/local proxy/Playbox) into the body; the broker
  rejects any unresolved `@profile/` with 400 `unknown_profile`. Request fields override profile
  fields shallowly (`provider.only` in the request replaces the profile's whole `provider.only`).
- `limits.default` / `limits.models.<m>` keep working and are folded into the default profile.
  The standing `quants` RULE is sent as `quantizations: [<labels>..., "unknown"]`; a tuned ROW
  is sent without `unknown` (§5).
- `@profile/<name>` whose profile names no model, sent through a tuned proxy: the band's model
  is the primary (no local 400).
- **Old-broker negotiation** (the local proxy): at tune time the proxy GETs the broker's
  `/v1/models`; 200 = body mode, 404 = header mode (old broker), remembered per proxy instance,
  reset on re-tune, never persisted. In header mode keys with no header form are dropped and
  named in `X-Roger-Routing-Dropped`; `models[]` is an honest local 400. Detection is a positive
  signal, never a 400 from the broker (an old broker forwards unknown top-level keys).
- Guest constraints outside the owner's ceiling (region, order, quant outside the owner's rule)
  are refused LOCALLY with an OpenAI-shaped 400; the proxy never widens a filter to express a
  refusal (`quantizations: []` means no filter and is never synthesized).
- Guest operators (opencode / hermes / aider) get routing by naming `@profile/<name>` as the
  model or by sending the body object themselves; the local proxy stops overwriting `model`
  when the body carries `models`, `provider`, `roger` or a profile reference, and only rewrites
  a bare foreign id (today's behavior) otherwise.

## 10. CLI flags (roger use / roger agent / roger ask)

`--models a,b,c`, `--only n1,n2`, `--order n1,n2`, `--exclude n1`, `--node n1` (= order + no
fallbacks), `--no-fallbacks`, `--sort price|throughput|latency`, `--pref
cheap|balanced|fast|reliable`, `--quant Q8_0,BF16`, `--require tools,vision`, `--params 7-70`,
`--min-ctx 32k`, `--max-ttft 1500ms`, `--trust verified|confidential`, `--self-hosted`,
`--region eu,us`, `--max-cost 0.02` (per request), `--profile name`, and the existing
`--max-in --max-out --min-tps --confidential --freq`. `--max-out 0` keeps today's meaning (the
$10 default) and the help text says so; `--max-out unlimited` sends the register ceiling.

## 11. Key guardrails (non-grant keys)

No consumer API-key object exists today (only signed device keys, web sessions and grants), so
this is greenfield, modeled on the grant record. A key is a `rog-key_<secret>` bearer with id
`key_<rand>`, stored sha256-only, resolved first like a grant (a device signature alongside is
ignored; keys never get self-use $0); the payer is the minting account's wallet; it routes the
whole network. `POST /account/keys`, `PATCH /account/keys/{id}`, `GET`, `DELETE` with `name`,
`limit_usd`, `reset: daily|weekly|monthly|none` (UTC windows, week starts Monday, anchored at
the interval change), `expires_at` (ISO-8601 UTC), `allowed_models[]`, `allowed_nodes[]` (≤ 64,
exact ids), `disabled`; 32 live keys per account; `Idempotency-Key` replay → 409; the secret is
shown once. Management needs the signed account identity; a key cannot manage keys (403).
Enforcement: an ATOMIC key-scoped reserve next to the wallet hold (captured / released on the
same exit paths, swept with orphan holds) gives ZERO overshoot, same floor(remaining/hold) law
as wallet holds; the hold is NOT shrunk to the remaining limit (mirrors the monthly cap: the
first pick must fit or 402; a pricier failover pair is trimmed from the plan). Precedence when
several bind: key_limit > monthly_cap > credits. Headers `X-RogerAI-Key-Limit/-Spend/-Pct/
-Notice` mirror the monthly ones (absent when unlimited; pre-request state at the gate, post-
settle state in the usage chunk with `key_reset_at`). allowed_models/nodes are checked where
grant allow-lists are (after moderation, before pick): 403, a pinned disallowed node is 403 not
503, sugar stripped first, allow-list is scope not admission (a private band still needs its
code), applies on the bridge. $0 (free/self) requests count usage, not spend. A grant bearer is
governed by the grant, never by a key. Revoked/expired/disabled: precedence revoked > expired >
disabled; an in-flight stream completes and settles; fail-closed 503 when the shared store is
down. Audit: `$0` ledger rows kind `key_event`; in `/account/export`; anonymized on delete.
Clients: `roger keys list|mint|set|rm`, `roger use --key`, `/usage?by=key`, the web account
page; `/generation` shows `key_id`. Errors: an unknown secret is 401 `key_invalid`; a revoked /
expired / disabled key gets its own code (the holder may learn why; the secret is unguessable so
this is no oracle). Shared store unreachable at key auth OR at the reserve step: 503 fail-closed.
Non-disclosure claims are about constant-work STRUCTURE (same hash-compare path on hit and
miss), never about timing measurements.

## 12. Rulings assumed (founder to confirm)

1. Accept OpenRouter's `provider{}` shape verbatim, plus `roger{}`. (Q1 → yes)
2. Constraint mismatch stays 503 (`no_match`), cooling stays 503 + Retry-After; no 404. (Q2)
3. Declared attributes (params, ctx, quant, region) unknown → ineligible under a filter;
   measured attributes (tps, ttft) unknown → pass. (Q3)
4. Implicit `require` for tools/vision is on by default, not switchable. (Q4)
5. `self_hosted_only` is a plain filter; curated stations get no visibility guarantee. (Q5)
6. `--max-out 0` keeps the $10 default; help text fixed; `unlimited` added. (Q6)
7. Broker-side `auto` is NOT in this set (waits for the multi-model pool work). (Q7)

## 13. Implementation status and rulings made while building

**Slice 0 (regression pins) is implemented.** Honored by the broker today: `roger.pref`,
`roger.confidential`, `roger.min_tps`, `roger.self_hosted_only`, `roger.freq`,
`provider.quantizations`, `provider.max_price.prompt` / `.completion`, `provider.ignore`,
`provider.order`, `provider.allow_fallbacks`, plus implicit tools/vision gating, the stream
usage chunk, `X-RogerAI-Model`, `503 no_match` / `band_cooling` codes, a minimal
`GET /v1/models`, and the old-broker probe in every first-party client path. Every other key in
§1 is RECOGNIZED and REFUSED with 400 `unsupported_routing_key` until its slice ships (no
silent drop); OpenAPI says which is which.

Rulings (founder, 2026-09-30 / 10-01):
- The band-cooling 503 carries `code:"band_cooling"` (approved `upstream_failover.feature`
  literal updated).
- First-party clients carry confidential in the body; `live_options.feature` reworded.
- A private-band request is band-scoped from the FIRST pick, not only in the failover plan (a
  pre-existing gap: a `--freq` request could be served by a public station at the public
  price). A band code means that station at that station's price.
- A guest may only TIGHTEN the proxy owner's routing: `min_tps` = max, `quantizations` must be
  a subset of the owner's rule (else a local 400 `routing_outside_session`), `freq` is never
  taken from a guest; `confidential` / `self_hosted_only` are OR'd, `ignore` unioned; a guest
  `max_price.completion` above the owner's EFFECTIVE out cap (the owner's `--max-out`, else
  the $10 default) is clamped to it, and a guest `max_price.prompt` above the owner's
  `--max-in` (when set) likewise, each with one proxy log line. The proxy always writes the
  effective `max_price.completion` into the body it sends and keeps the
  `X-Roger-Max-Price-Out` header for one release.
- **Header and body limits compose to the stricter at the broker (2026-10-01)**, §1a: price
  caps = the lower, `min_tps` = the higher, confidential = OR, exclusions = union, a header
  band code cannot be replaced by a different body code (400). Only `pref` and
  `order`/`allow_fallbacks` versus the pin header stay body-wins. Found by the pre-push audit:
  under the earlier body-wins rule a proxy guest's body could raise the owner's header cap,
  on every client version already installed.
- An ordered station dropped as unpayable (anonymous caller, or a balance that cannot cover
  it) is excluded from the remaining scored pick as well, so it cannot be re-picked into a
  401 / 402.
- Until the bridge evaluates them itself, a request carrying `quantizations`, an implicit
  tools/vision need, `self_hosted_only`, `order`, or `allow_fallbacks:false` DECLINES the
  Tower bridge (narrow, never widen). Slice 1 replaces the decline with real evaluation (§6).
- `provider.order: []` is a 400; an empty `ignore` / `quantizations` is "no filter".

Spec corrections approved 2026-10-01 (thirteen, found by the slice-1 RED runners; each
changed scenario carries a `# corrected 2026-10-01 (founder-approved)` line):
1. `node_preference`: the four scenarios that assume a private band with several stations are
   tagged `@later`; a band is one node id today and multi-station bands are not in this set.
   The slice-1 runners exclude `@later`.
2. `node_preference`: the two "picked more often" pref scenarios use tps 90 vs 60 (was 90 vs
   20, where the faster station already took every pick under every pref).
3. `request_shape`: the three min-tps scenarios measure `n-c` below the floor (it was
   unmeasured, passed the floor and outscored the expected station).
4. `request_shape`: the grant no-station message literal carries the model name, as the broker
   prints it.
5. `request_shape` + `model_fallback_list`: an over-limit SIGNED body is a 401 "invalid request
   signature" (the reader truncates at 4 MiB and the signature check fails first); no 413
   exists.
6. `variant_sugar`: the near-miss outline row that was literally `qwen3-32b:free` is dropped.
7. `capability_gating`: the `require_parameters: null` row is dropped (null = absent).
8. `request_shape`: an empty `roger.freq` is absent, not a 400 (row moved to the null/empty
   outline).
9. `model_fallback_list`: "neither model nor models" is a NEW 400 `invalid_routing_value`
   ("model is required"), §3.
10. `model_fallback_list`: the model id limit is explicit, 256 characters, §3.
11. `model_fallback_list`: a grant's price is what the grant bills at, not a cap on offers, §3.
12. Every routing refusal carries `X-RogerAI-Cost: 0`, §2.
13. Tier A before Tier B holds across fabrics, §6.

Rulings from the slice-1 fresh-context review (founder, 2026-10-02):
- **Deferral tags approved as-is**: scenarios tagged `@slice2` / `@slice3` / `@slice4` run when
  their slice lands (the tag is removed then); `@later` (multi-station private bands, a
  Tower-signed bridged receipt) is known open work; `@cli` / `@tui` / `@proxy` / `@harness` /
  `@docs` / `@unit` belong to another runner.
- **A guest may only tighten, including `models[]`**: the local proxy refuses (local 400
  `routing_outside_session`) any `models[]` entry whose bare id is not the tuned band's model;
  a list naming only that model (sugar included) passes. The proxy does not add the owner's
  `pref` default when the guest states a sort (it would conflict).
- **`:free` under a PRICED grant is `no_match`**: nothing the grant can reach costs the caller
  nothing (§4). Under a FREE grant `:free` admits the grant's stations.
- **A bridged pair's hold** is the larger of its context-window estimate and the bridge's own
  grant ceiling (`edgeGrantCeiling`), so a Tower operator is never underpaid; the consumer's
  caps still bound it. In practice a bridged follower needs a balance of about the ceiling
  (~$2 at $1/1M), as the bridge's own hold always did.
- **A receipt keeps what the node signed**: when an attempt was dispatched for a different
  model, the broker records `dispatched_model` (outside the node signature, inside the
  broker's); billing, the tokenizer key, the lineage row and `X-RogerAI-Model` follow it.
- **`models[]` is bounded at 32 raw entries** like every list (§1a), and the effective list
  stops at the 6th distinct id.
- **The per-request cap binds every output limit** (`max_tokens` and `max_completion_tokens`);
  a station the cap buys no output at is dropped, never sent `max_tokens: 1`.

Added during the spec pass (each is pinned by scenarios; flip the scenario if you rule otherwise):

8. `provider.max_price.request` also lowers the station's `max_tokens` to what the cap buys
   (§1a). The alternative is bill-clamp only.
9. `allow_fallbacks:false` still walks an explicit `models[]` (§3).
10. `quantizations` gains `unknown`; unlabeled offers are ineligible unless it is listed (§5).
11. Under `sort`/`order` the Tower coin is replaced by ranking (§5).
12. curated → non-curated re-registration is a new identity (§5), a NEW requirement.
13. The broker owns the stream's `[DONE]` so the usage chunk can carry settled numbers (§7).
14. `/generation` keeps records for non-served outcomes, and has an owner view (§7).
15. Keys: zero-overshoot reserve, hold not shrunk, 32 keys per account, `key_event` audit rows
    (§11).
16. The local proxy's `/v1/models` stays one entry; listing `@profile/…` there would contradict
    the approved `features/proxy/models.feature` and is left for a separate re-ruling (§8).
17. A defect-6 consequence: the TUI quant rule's "unlabeled passes" is preserved via `unknown`,
    so `TestAbsenceIsReadDifferentlyByRowAndRule` keeps its meaning.

Slice 1 part C (2026-10-01):
18. Tower rows are candidates IN the plan: ranked by the consumer's order (a Tower id ranks
    where it was named), by a strict sort across both fabrics (direct first on a tie), else by
    the tier gate across fabrics, and when the two heads tie on tier by the fan-out coin,
    consulted once per request for the head model and counted on /admin/live
    (`edge_coin_flips`). The request's one hold covers the priciest pair including a Tower pair
    and follows a bridged attempt by rekey to the attempt id the Tower's settlement captures.
19. `only` / `order` / pin match the Tower id only; `ignore` matches both ids (above).
20. A bridged attempt's failure is a failover trigger like a direct one (the plan continues);
    a Tower station's own 429 surfaces with its Retry-After when the bridge is the last word.
21. Tower rows a consumer constraint declined are counted once per request and constraint
    (`edge_bridge_declined{<constraint>}`) and logged once per constraint and Tower, naming
    the request, never a band code.
22. The bridged receipt carries the broker signature only; a Tower-key node signature needs a
    hub-side protocol change (tagged `@later`).

Rulings on the slice-6 open items (founder, 2026-10-05):
23. Job ids (§14.B7 #13): the keyed per-attempt id, with the key derived from the broker signing
    key under its own label, so every instance agrees and no mapping table exists. A receipt
    names the attempt, not the request: responses carry `X-RogerAI-Attempt-Id` (the attempt the
    receipt names), and the consumer's own ledger rows and /console consumer events carry
    `relay_request_id` beside it (Postgres: a `relay_request_id` column on receipts). A
    Tower-relayed attempt carries it too: the attempt record keeps it, so whichever instance
    settles the attempt stamps it. A station owner's rows never carry it. No station protocol
    change.
24. Region (§14.B7 #21): the network-to-continent table is operator-supplied by configuration;
    the public code ships only the loader and names no dataset. With no table configured, region
    stays declared only (today's behavior) and `attribute_sources.region` says "declared".
25. Probe shape (§14.B7 #2): canaries take the organic stream share of the model's recent
    traffic; a streamed canary is read through a stream sink like a customer's, waits on the
    stream idle window, and is graded on the rebuilt answer.
26. Latency (§14.B7 #19): `sort: latency` means TOTAL latency (time to the whole answer) for
    every row, direct and bridged, measured per node as an EWMA at settle and kept in the shared
    store; where no total is measured yet it falls back to TTFT; unmeasured ranks last.
27. Route explain: the `self_hosted_only` exclusion is pinned with a curated station of its own
    (the outline row that named a never-curated station is replaced).

Rulings on the slice-6 review (founder, 2026-10-06):
28. Canary identity (§14.B7 #2): the pseudonym a canary carries rotates PER PROBE: each is
    derived from the broker secret plus a per-probe nonce, shaped like a customer's, so a station
    sees no stable probe user. Grading and verified status are unchanged.
29. Idempotency replays (§14.B2): only a success or a client error (a 4xx other than 429) is
    replayed. A retryable outcome (429, any 5xx) releases the claim and the retry runs fresh,
    still with at most one hold and one charge overall. A request that reached nobody (the
    consumer left before any answer) releases its claim too, and a stored status below 100 is
    never replayed.
30. Idempotency, saved reply gone (§14.B2): when the claim exists but its saved reply is gone,
    the answer stays a 409 (never a second job under one key) with the code
    `response_unavailable`.
31. Upstream credential refusals (§14.B6): a station's upstream 401 or 403 reaches the consumer
    with its status, its error code and a plain message, never `error.metadata.raw` (a commercial
    upstream's refusal can echo a fragment of a credential). Every other status keeps the capped
    raw body for diagnosis. A 403's classification is unchanged (upstream_error, no failover).
32. Idempotency after a stream that died before its first byte (§14.B2, §14.10): the stream still
    bills its prompt, so its key is kept; a retry gets 409 `response_unavailable` and the consumer
    is charged exactly once. A non-stream disconnect bills $0, releases its key, and its retry is
    served fresh. Generally a key is released only when nothing was charged.

---

## 14. Hardening (slice 6, PROPOSED 2026-10-02, awaiting founder approval)

From the product / security / fairness audit of 2026-10-02 and the founder rulings made on it
(recorded in memory and below). Part A: fairness, abuse, money. Part B: user value, integrity,
privacy. Reconciliation of the two parts:
- The pair cooldown key is **(station, payer, model)** everywhere (part B's "(station, payer)"
  means the same key).
- The error envelope of part B §14.B6 is the one envelope; part A's consumer-caused 4xx uses it
  with `error.code "consumer_rejected"` (type `invalid_request_error`), `metadata.station` (omitted
  under the no-oracle rules), `metadata.raw` (4 KiB cap).
- `X-RogerAI-Attempts` is defined once (part B, idempotency) and is set on every relay response.
- "Distinct payer" (part A) is the billing wallet; anonymous and unbound callers count by their
  per-IP bucket key. Affinity (part B) and idempotency are keyed by the same payer identity.

### 14.A Fairness, abuse and money

#11, #14, #15, #16, #23, #24) and the founder rulings of the same day.

## 14.1 Consumer-caused upstream errors (#1)

- An upstream 400, 401, 404, 413 or 422 is **consumer-caused**: the attempt is voided at $0 with
  `void_reason: "consumer-rejected"`, recorded as a $0 lineage receipt, answered to the consumer
  (wrapped: `error.code "consumer_rejected"`, `error.metadata.station`, `error.metadata.raw`),
  and is never a strike, never counts toward the warn/payout hold, and is never a failover or
  model-fallback trigger.
- Exceptions keep their own rules: the recognized context-window 400 (`context-window`, §3) and
  429 (`upstream-throttled`).
- Empty-output strikes (2xx-empty, 5xx) are still recorded, each carrying the payer, but count
  toward the warn step only once at least `ROGERAI_STRIKE_MIN_PAYERS` (default **3**) distinct
  payers produced them within the strike decay window. A payer is the billing wallet; an
  anonymous or unbound caller is identified by its per-IP bucket key. `1` restores the old
  behavior. The zero-doubt impossible-input ban is unaffected.

## 14.2 Cooldowns are per (station, payer) first (#4)

- A 429 cools the **(station, payer, model)** pair for its Retry-After (default and cap as
  today: `ROGERAI_STATION_COOLDOWN_DEFAULT`, `ROGERAI_STATION_COOLDOWN_MAX`), shared across
  instances, extend-never-stack.
- The station cools for **every** payer (today's node-wide, all-models cooldown) only once
  `ROGERAI_COOLDOWN_MIN_PAYERS` (default **3**) distinct payers got a 429 from it within
  `ROGERAI_COOLDOWN_PAYER_WINDOW` (default **60 s**).
- A pair cooldown answers the affected payer with 503 `band_cooling` and that pair's
  Retry-After when no other candidate exists; it never pages and never counts the station off
  air. Cooling never touches trust (unchanged).
- A curated station may declare `tpm` (tokens per minute) on its offer; a human station may
  not (400 naming `tpm (curated stations only)`). A request whose measured prompt exceeds
  `ROGERAI_TPM_REQUEST_SHARE` (default **50%**) of that budget is not dispatched to that
  station: if no other candidate exists, 429 `request_exceeds_station_tpm` with a Retry-After,
  $0, no cooldown for anyone.

## 14.3 Rate-limit identity (#7)

- An unbound keypair (no account bound) shares the per-IP bucket with anonymous callers
  (`ROGERAI_ANON_RATE_RPM` / `_BURST`). A keypair bound to an account keeps the account bucket.
- Free traffic (a `:free` request, or a pick that lands on an offer free right now, excluding
  self-use and free grants, which have their own limits) is additionally limited per client IP
  (`ROGERAI_FREE_RATE_RPM`, default **20**) and per (client IP, station)
  (`ROGERAI_FREE_STATION_RPM`, default **10**); pinning one free station via order/only/pin is
  capped per caller (`ROGERAI_FREE_PIN_RPM`, default **10**). Refusals: 429 `free_rate_limited`
  / `free_pin_limited` with Retry-After. Shared across instances.

## 14.4 Sort picks within a band (#8)

- `provider.sort` orders the eligible pool by its metric, then picks among candidates within a
  band of the best: `ROGERAI_SORT_BAND_PRICE` (default **5%** of the best estimated request cost)
  for `price`, `ROGERAI_SORT_BAND_SPEED` (default **10%**) for `throughput` and `latency`.
  Within the band the pick is weighted by spare capacity (1 - in_flight / effective capacity,
  where effective capacity is the lower of declared capacity and measured concurrency), and
  ties are broken by the request seed, never by node id. Unmeasured stations stay last.
- The failover plan is: the band (in seeded order), then the rest in sort order, up to the
  attempt limit. `allow_fallbacks:false` is one attempt at a band pick. The variant suffixes
  `:floor` / `:nitro` inherit the band.

## 14.5 Tower share (#15)

- When both heads are Tier A and no sort/order applies, the fabric that goes first is chosen
  by the request seed with probability proportional to **eligible capacity** on each side (sum
  of effective capacity of eligible direct stations vs eligible Tower rows, each Tower station
  counted individually). Sort and order still replace the coin; free and self-use are never
  diverted to a billed Tower.

## 14.6 Home first, curated as overflow (#16)

- By default home (non-curated) stations rank ahead of curated stations, including Tower rows
  whose node is curated. Curated stations are used when no eligible home station can take the
  request (none, all cooling, all busy) and after the home attempts in the failover plan.
- A consumer opts in to curated on equal terms with `roger.pref "fast"`, any explicit
  `provider.sort` (or `:floor` / `:nitro`), or naming a curated station in `order`, `only` or
  the pin header. `cheap`, `balanced`, `reliable` keep home-first. `self_hosted_only` still
  excludes curated entirely. Among curated stations the ordinary score decides.
- Supersedes `features/curated/curated_routing.feature:26`.

## 14.7 Price means estimated request cost; default input cap (#3)

- `sort: price`, `:floor` and the default score's price term rank on **estimated request
  cost** = measured prompt tokens × in price + expected output × out price, where expected
  output = `max_completion_tokens`, else `max_tokens`, else `ROGERAI_DEFAULT_OUTPUT_TOKENS`
  (default **4096**), never above the declared window minus the prompt. Free offers still
  price-tie and never move the scoring range.
- A server-side default input cap `ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_IN` (default **$5/1M**)
  applies when no input cap is stated and composes to the stricter with `X-Roger-Max-Price` and
  `provider.max_price.prompt` exactly as the out cap does; an explicit cap above the register
  ceiling is clamped to it.
- `/v1/models` adds `rogerai.blended_price_per_1m`: the lowest blended price of a single station
  at `ROGERAI_BLEND_INPUT_RATIO` (default **3:1** input:output), labeled with the ratio.

## 14.8 Dispatch failures fail over (#5)

- A dispatch failure before any work (no poller free, off air, handoff lost, dispatch bus error)
  is a failover and model-fallback trigger on both stream and non-stream, under the existing
  hold, deadline (>= 10 s left) and `allow_fallbacks` rules; never a strike.
- When the whole plan fails this way: 503 `station_busy` (or `station_off_air` when every
  station was off air) with Retry-After; on a stream, nothing is committed (no empty 200).
- Supersedes `model_fallback_list.feature:437`. The non-stream 504 timeout is unchanged.

## 14.9 Usage reported once, as billed (#9)

- Non-stream: the response body's `usage` is rewritten to the billed counts (min(claim,
  recount)), plus `usage.cost` and `usage.rogerai{...}` (as the stream chunk carries).
- Stream: exactly one chunk with a usage object reaches the consumer, the broker's, whether the
  broker injected `include_usage` or the consumer asked for it; a station chunk that carries
  both content and usage is forwarded with its usage object removed.

## 14.10 Bill what was delivered (#11, #14)

- **Client disconnect** (stream or non-stream): the broker sends the station a cancel for the
  job; the settle bills the prompt plus the completion tokens forwarded to the client before
  the cancel, recounted by the broker (the station's claim is capped at that recount), clamped
  to the hold; the operator earns its share; never a strike. A non-stream disconnect before
  the result bills $0 (nothing was delivered).
- **Stall after content**: the delivered tokens settle the same way; the chunk says
  `usage.rogerai.partial: "stall"`; no empty-output strike; the health stall counter
  increments as today. A stall before any content is a void as today.
- **Late non-stream result after a 504** within `ROGERAI_LATE_RECEIPT_GRACE` (default **30 s**):
  DECISION, default Option A: recorded as a $0 lineage receipt (`late-after-timeout`), consumer
  billed $0, operator unpaid, no strike. After the grace window: discarded as today.
- Supersedes `stream_receipt_parity.feature:292`, the settle lines of
  `stream_receipt_parity.feature:312` and `model_fallback_list.feature:431`.

## 14.11 Holds sized to the request (#23)

- The hold reads `max_completion_tokens`, else `max_tokens`; with neither it uses
  `ROGERAI_DEFAULT_OUTPUT_TOKENS` and the forwarded body carries `max_tokens` set to that
  budget (bounded by the window left after the prompt).
- A plan whose Tower pair's hold does not fit the wallet drops the Tower pair and proceeds
  Tower-free when a direct pair fits; 402 only when nothing fits.

## 14.12 Price lock protects against hikes only (#24)

- Within a lock, billing is min(lock, current) (unchanged). A lock minted under a price that
  had been posted for less than `ROGERAI_LOCK_MIN_POSTED` (default **1 h**) expires when that
  price stops being in effect (or at 24 h, whichever is first). The posted-since time is shared
  across instances. Scheduled (time-of-use) prices are never locked; voided attempts mint no
  lock (unchanged).

## Knob summary

| Knob | Default |
|---|---|
| `ROGERAI_STRIKE_MIN_PAYERS` | 3 |
| `ROGERAI_COOLDOWN_MIN_PAYERS` | 3 |
| `ROGERAI_COOLDOWN_PAYER_WINDOW` | 60 s |
| `ROGERAI_TPM_REQUEST_SHARE` | 50% |
| `ROGERAI_FREE_RATE_RPM` | 20 |
| `ROGERAI_FREE_STATION_RPM` | 10 |
| `ROGERAI_FREE_PIN_RPM` | 10 |
| `ROGERAI_SORT_BAND_PRICE` | 5% |
| `ROGERAI_SORT_BAND_SPEED` | 10% |
| `ROGERAI_CONSUMER_DEFAULT_MAX_PRICE_IN` | $5/1M |
| `ROGERAI_DEFAULT_OUTPUT_TOKENS` | 4096 |
| `ROGERAI_BLEND_INPUT_RATIO` | 3 (3:1) |
| `ROGERAI_LATE_RECEIPT_GRACE` | 30 s |
| `ROGERAI_LOCK_MIN_POSTED` | 1 h |


### 14.B User value, integrity and privacy

#18, #20, #25 (founder ruling: class aliases + explain + affinity), #2, #12, #13, #19, #21.
Draft A (the sibling file) covers the fairness, abuse and money items. Each rule below is pinned
by the feature file named beside it.

## §14.B1 Session affinity (features/routing/session_affinity.feature)

- Carriers: `roger.session` (string) or top-level `session_id` (string); equal = fine; different
  = 400 conflicting_routing_keys; null = absent. 1..256 printable ASCII bytes, else 400
  invalid_routing_value.
- Entry key = (payer, HMAC(broker secret, session id), served bare model); value = the station id
  or Tower relay id that served the last SERVED turn. Written on serve only (failures and voids do
  not move it). TTL `ROGERAI_AFFINITY_TTL` default 10m of inactivity, refreshed per served turn.
- Use: the affine server is tried first when, for THIS request, it is eligible under every
  constraint, Tier-A, not cooling (station or (station, payer) pair) and in_flight < capacity.
  Otherwise normal routing, silently, and the entry is re-written to the new server on serve.
- Explicit order / pin / sort / sort sugar ignore affinity for the head; pref keeps it; `only`
  keeps it only when the affine server is inside `only`.
- Head only: the failover plan behind it is built as today.
- Never crosses payers; never forwarded to stations (both carriers stripped); never logged raw;
  shared store with a bounded local fallback; counters affinity_hits,
  affinity_misses{ineligible,cooling,busy,expired,explicit}.

## §14.B2 Idempotency (features/routing/idempotency.feature)

- `Idempotency-Key` header, 1..128 printable ASCII bytes, else 400 invalid_idempotency_key.
  Scope (payer, key); window `ROGERAI_IDEMPOTENCY_TTL` default 10m from the first request.
- Fingerprint = sha256(exact body bytes + the X-Roger-* routing headers read).
- Same scope + fingerprint: finished non-stream with a success or client-error status (§13 ruling
  29; a 429 or 5xx releases the key and the retry runs fresh) → replay: same status, body bytes,
  X-RogerAI-* headers, same X-RogerAI-Request-Id, plus `X-RogerAI-Idempotent-Replay: true`; no
  dispatch, hold, settle, moderation, or relay rate token. In flight → 409 request_in_flight with
  Retry-After >= 1. A stream that committed its first frame → 409 stream_not_replayable; a stream
  that failed before any frame is treated like a non-stream outcome of the same status.
- Same scope, different fingerprint → 422 idempotency_key_reused.
- Bounds: stored body <= 1 MiB (else retries get 409 response_too_large_to_replay); <= 1000 live
  keys per payer (the oldest FINISHED keys are evicted early, never one still in flight; the
  request is still served).
- The claim lives in the store; the saved reply in the shared store (per instance without it).
  A claim whose saved reply is gone answers 409 response_unavailable (§13 ruling 30).
- Every relay response carries `X-RogerAI-Attempts: <n>` (station attempts made). The local proxy
  mints one key per client request (or forwards the client's), reuses it on every retry, and does
  not re-pick when n > 1.

## §14.B3 Route explain (features/routing/route_explain.feature)

- `roger.dry_run: true` or POST /v1/route/explain (same body). No dispatch, hold, key reserve,
  moderation, price lock, receipt, /generation record or capacity use. Non-boolean = 400; false /
  null = absent.
- Same validation errors as a real request (400/401/403). Money or supply refusals are reported
  as `would: {status, code, retry_after_s?}` inside a 200.
- Document: request_id ("dry_" prefix), models, plan [{model, station|tower, price_in, price_out,
  tier, reason: order|sort:<metric>|score|affinity}], excluded [{station, model, reasons}], hold,
  cost_estimate {min = prompt tokens × head in price, max = hold}, would.
- Exclusion reasons: the noMatchFilters vocabulary plus cooling, ignore, only, context_window,
  capability <name>.
- Visibility = what a real request by the same caller could reach (bands need their code;
  anonymous sees /discover stations and no Towers; grants see their owner's stations; another
  payer's pair-cooling or affinity is never visible).
- Headers X-RogerAI-Cost: 0, X-RogerAI-Dry-Run: true; own rate bucket, same limits as /market.

## §14.B4 /v1/models OpenRouter-compatible fields (features/discovery/models_openrouter_fields.feature)

- context_length = max DECLARED window over eligible public offers (omitted if all estimated).
- pricing {prompt, completion} = per-token USD decimal strings from ONE offer: lowest blended
  cost (in × 3 + out), ties by /discover order; free (incl. an active free window) = "0".
- supported_parameters = max_tokens, temperature, top_p, stop, seed, stream, plus tools,
  tool_choice, response_format when some eligible offer is tools-verified.
- architecture = {input_modalities: text (+ image if some offer declares vision),
  output_modalities: text}. Voice offers not listed.
- Same on /v1/models/{id} and filtered reads; rogerai block unchanged; class aliases listed with
  rogerai.expands_to (omitted when empty).

## §14.B5 Class aliases (features/routing/class_aliases.feature)

- Data table: coding-30b = params_b in [24, 40] and some tools-verified offer; small = params_b
  <= 9; large = params_b >= 60. params_b = declared or id estimate; neither = never in a size
  class.
- Expansion: matching on-air public chat models under the caller's visibility, ordered by
  cheapest coherent blended cost, ties by bare id, max 5; then exactly a models[] request.
- Errors: unknown class 400 unknown_model_class (names are exact lowercase); class + models →
  400 conflicting_routing_keys; class inside models → 400 invalid_routing_value; empty expansion
  → 503 no_match naming the class. Stations may not register ids starting `@class/`.
- Sugar on a class: sort sugar request-wide, `:free` per expanded model.
- X-RogerAI-Class header; /generation lists the expansion; counter class_requests{<name>}.

## §14.B6 Error envelope (features/errors/error_envelope.feature)

- {"error": {code, message, type, metadata}}; code always set; type from OpenAI's set
  (invalid_request_error, authentication_error, permission_error, not_found_error,
  rate_limit_error, insufficient_quota, server_error, overloaded_error, timeout_error);
  metadata only request_id, retry_after_s, station, model, attempts, filters, raw.
- New codes: station_off_air, station_busy, no_poller (503); station_timeout (504);
  rate_limited, grant_rate_limited (429); invalid_signature, signature_required,
  session_expired, login_required (401); band_unavailable (503, one code and one message for
  every private-band refusal); grant_unavailable (503); content_refused (451, no category);
  moderation_unavailable (503); upstream_error (upstream status; raw body under metadata.raw,
  <= 4 KiB, station/model named except on band requests and anonymous Tower naming);
  invalid_request_id (/generation 400).
- Every error: X-RogerAI-Cost: 0, X-RogerAI-Request-Id, Retry-After == metadata.retry_after_s
  when set. Post-commit stream errors: one data frame in the envelope, then usage chunk, then
  [DONE].

## §14.B7 Integrity (features/security/routing_integrity.feature)

- Probes: pseudonym from the real derivation (no "probe" user), rotated per probe (§13 ruling 28);
  rotating realistic prompts (no fixed sentinel: the challenge wording, the padding prose and the
  shadow tools are drawn from pools, the body is written model-first like a client SDK, and
  temperature and max_tokens follow a recent organic request); same request shape as the model's traffic; shadow canaries mirror organic
  shape. `verified` withdrawn when organic evidence contradicts the probe: K=3 recount strikes or
  organic success below the Tier-A bar within 1h (knobs), restored after a clean window and a
  passing canary.
- Pick budget: `ROGERAI_PICK_BUDGET` default 64 pickFor calls per relay; over → 503
  routing_budget_exceeded (no hold, no dispatch). Cap drops evaluated inside the pick; body parsed
  once.
- Unlinkable job ids: "att_" + hex(HMAC-SHA256(broker secret, request id + ":" + n))[:24];
  receipts bind to it and name it; `X-RogerAI-Attempt-Id` names the served attempt; the
  consumer's own ledger rows carry `relay_request_id` (§13 ruling 23).
- /generation owner view drops key_id, models, moderation; /generation has its own rate bucket
  (same per-identity limits as /console).
- Tower streams: keepalive comment every 10s after commit while waiting (knob, not content for
  failover); sort:latency ranks EVERY row (direct and bridged) on total latency, falling back to
  TTFT where none is measured (§13 ruling 26); bridged stream attempts get the direct stream
  deadline.
- attribute_sources on /discover offers and the /v1/models rogerai block (region/quant declared;
  params_b and ctx declared|estimated; tools verified; vision declared; tps/ttft measured).
  Region contradicted by the station's network (when the operator's network-to-continent table,
  `ROGERAI_NET_CONTINENTS`, maps its connecting address to a continent other than the declared
  region's) → ineligible under roger.region, /admin/live region_mismatch, /discover
  attribute_sources.region "contradicted"; no table configured = declared only (§13 ruling 24);
  the continent is stamped on the registration and travels with the shared registry; curated
  regions (provider names) are never contradicted; addresses never exposed. Docs: region is not
  data residency.

## Design choices made in this draft (for the founder)

1. Affinity is per (payer, session, served model) and is a HEAD preference only; explicit sort
   and order override it; `pref` does not.
2. Idempotent stream retries after commit get 409 (no byte replay); retries of refusals replay the
   refusal; routing headers are part of the fingerprint.
3. A dry run does not run moderation and reports money/supply refusals inside a 200 (`would`).
4. Blended cost uses a 3:1 prompt:completion weighting for /v1/models pricing and class ordering.
5. Class names: coding-30b [24, 40] + verified tools, small <= 9, large >= 60 (inclusive).
6. The private-band refusal gets ONE code (band_unavailable) for all three causes.
7. Upstream bodies are wrapped (metadata.raw, 4 KiB cap) instead of passed through raw: a
   behavior change for clients that parse a station's own error JSON.
8. Unlinkable job ids change the job id stations see (receipts bind to it); old stations keep
   working because they echo whatever id they are given.
9. Region contradiction only when the network bucket maps to a known continent; otherwise no
   contradiction is inferred.
