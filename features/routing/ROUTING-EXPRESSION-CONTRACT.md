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
- Node ids and Tower relay ids are distinct namespaces; each `only`/`order`/`ignore` entry is
  matched in both independently (a collision is logged once on /admin/live, never a 400).
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
