# Tower specification: implementation guide

Status: **working guide for turning `features/tower/*.feature` into an executable suite.**
It changes no approved behavior. Where it proposes reshaping a spec, the behavior asserted
must stay identical.

Last updated: 2026-08-02

## Why this document exists

The Tower proposal is behaviorally strong and, as written, not yet executable. That is a
shape problem, not a rigor problem, and it is fixable mechanically. This guide records the
measurements, names the three structural fixes, and defines the canonical vocabulary that
step definitions will implement.

Read this before writing a single step definition. Writing them against the current prose
would produce roughly 50,000 lines of glue that nobody will maintain.

## Measurements (2026-08-02 tree)

| Measure | Value |
|---|---|
| Feature files | 18 (22 after the tamper split) |
| Lines | ~10,270 |
| Scenarios | 810 |
| Steps | 4,140 |
| Distinct step texts | 4,041 (97.6%) |
| Distinct after normalizing quotes, `<placeholders>`, integers | 4,030 (97.3%) |
| Steps over 250 characters | 498 |
| Share of all step text in those 498 steps | 36% |
| Of those long steps, schema-shape declarations | ~46% |
| Distinct signed object names | 203 |
| ...of which typed-set registry rows | 70 |
| Distinct object **schemas** | 133 |

The headline: **effectively every step is its own step definition.** Only 99 steps repeat
verbatim across 10,270 lines. Everything else in this guide follows from that number.

## What is already right - do not "fix" these

Three patterns in the set are exemplary and must survive any restructuring.

**The Cartesian tamper matrix.** A `| field |` table crossed with a `| mutation |` table and
a short flat `Then` covers thousands of cases with three step definitions. This is the single
best thing in the spec set. Every field enumeration should migrate *toward* this shape.

**The generic typed set.** `CanonicalTypedSetV1` plus 158 registry rows is exactly right.
The 70 `*SetV1` names are registry rows, not 70 schemas - an earlier reading of the object
count as 203 distinct schemas was wrong, and the real figure is 133.

**Signed policy instead of magic numbers.** Thresholds, deadlines, and rates are signed,
versioned policy objects read at a named stage. There are zero uses of "appropriate",
"reasonable", "promptly", or "eventually" as criteria anywhere in the set. Keep that bar.

## Fix 1: unify the step vocabulary (the gate)

About 1,000 steps fall into six families that differ only in prose. Unified, they are roughly
twenty parameterized step definitions.

| Family | Steps | Canonical replacement |
|---|---|---|
| Field / preimage enumeration | 221 | `Then the strict signed preimage of <Object> is exactly:` + `\| field \|` table |
| Hash and canonicalization | 532 | `Then the complete hash of <Object> is stable`; `Then <Object> re-encodes byte-identically`; `Then <field> is canonically absent` |
| Rejection and fail-closed | 244 | `Then it is rejected before <stage>` |
| Transaction and CAS | 181 | `Then exactly one serializable transaction commits:` + table of committed facts |
| Mutation application | 137 | already table-driven in the tamper matrix; other files should reference it rather than restate it |
| Replay and idempotence | 69 | `Then exact replay returns the identical <object>` |

### The canonical step vocabulary

Step definitions implement these and only these shapes. A new scenario that needs a phrasing
outside this list is a signal to extend the list deliberately, not to invent prose.

```gherkin
# Structure and encoding
Then the strict signed preimage of <Object> is exactly:            # + | field | table
Then the complete hash of <Object> is stable
Then <Object> re-encodes byte-identically
Then <field> of <Object> is canonically absent
Then <Object> is rejected before <stage>

# Authority and lifecycle
Given <Object> is signed by the <role> key
Given <Party> is in state <state>
When <actor> presents <Object> at <stage>
Then verification fails for <reason>
Then exactly one serializable transaction commits:                 # + | fact | table
Then no partial state is created

# Money
Then <amount> atoms move from <state> to <state>
Then the ledger conserves: <expression>
Then exact replay returns the identical <object>
Then the operation fails closed

# Evidence
Then one <evidence kind> record binds:                             # + | field | table
```

`<stage>` is a closed set: `decode`, `signature`, `authority`, `relationship`, `commit`,
`send fence`, `rail call`.

### Rule for new scenarios

Prefer a Scenario Outline over an Examples table to a new prose sentence. If a `Then` names
more than three independent facts, it is a table, not a sentence.

## Fix 2: schemas belong in tables, not prose

498 steps carry 36% of all spec text and nearly half of those are object field lists written
as one sentence. A 1,500-character `Then` cannot be implemented by one step definition and,
when it fails, does not say which of forty clauses broke.

Converted so far, as worked examples of the target shape:

- `key_separation.feature` - the 57-role key inventory is now a `| role |` table.
- `standalone_jobs.feature` - `LocalAdminAuthorizationV1` is now a field table plus a closed
  action table.
- `job_and_settlement.feature` - the duplicated `ExecutionGrantV1` field list now references
  the tamper matrix, which is already declared the source of truth. This also removed a real
  drift: the two enumerations had already diverged.

Remaining: roughly 67 steps of the same shape, concentrated in the `features/tower/tamper/`
matrices (78 long schema steps), `standalone_jobs.feature` (59),
`control_plane_tamper_matrix.feature` and `compensation_state_machines.feature` (20 each).

**Rule: a field list appears in exactly one place.** Every other mention references it.
Duplicating a field list into prose is how the two `ExecutionGrantV1` enumerations drifted
apart within a day.

## Fix 3: scope the object surface to the delivery phase

133 schemas is more than any single release needs, and a spec that must be implemented all at
once will not ship. Tiering them makes the first release finite. This changes no behavior; it
orders the work.

| Tier | Gate | Objects | Rough count |
|---|---|---|---|
| **0** | Phase 0 - repair the existing production money path | `SettlementReceiptV2`, `ProviderAssertionV2`, `EntityKeyV1`, `RogerTrustDocumentV1`, `RogerTrustPublicationV1`, `HoldReferenceV1`, `AttemptEventV1`, `AttemptIssueCommitmentV1` | 8 |
| **1** | Phase 1 - standalone MVP | the `Local*` family | 35 |
| **2** | Phase 2 - joined pilot, no money | `TowerEnrollmentProofV1`, `TowerAdmissionLeaseV1`, `TowerLifecycleEventV1`, `TowerInventoryV1`, `DispatchLeaseV1`, `ExecutionGrantV1`, `CoreTransitObservationV1`, `TowerTransitStatementV1`, `StationOfferV1`, `StationLifecycleEventV1`, `StationOriginLeaseV1`, `DirectStationOriginAuthorityV1`, `StationAttachAuthorizationV1`, `StationEpochResetV1`, `TowerLocalStationBridgeCredentialV1`, `ClientRequestAuthorizationV1`, `PublicDirectorySnapshotV1`, `RootDelegationV1`, `RogerLedgerGenesisV1`, `RogerLedgerCheckpointV1` | ~20 |
| **3** | Phase 3 - compensation beta | compensation, payout, debt, eligibility, funding, and policy families | ~50 |
| **4** | Deferred until measured need | all `*ValueProjectionV1`, `ControlValueProjectionV1`, `CompensationControlTotalLeafV1`, dust family, escrow family, writeoff decision family, tax correction incident family | ~20 |

Tier 4 is the one worth arguing about before approval. Those objects add auditability rather
than safety, and every one of them is a schema, a tamper matrix, a set of step definitions,
and a migration. Deferring them does not weaken any money invariant that Tier 0 through 3
already assert; it defers a class of *proof about* those invariants. The founder should decide
whether Phase 3 needs them or whether the append-only SQL ledger plus signed checkpoints is
sufficient at beta scale.

## Fix 4: split `tamper_matrix.feature` - DONE 2026-08-02

2,395 lines and 128 scenarios sat in one file with no section comments. A failure there
handed the implementer a 2,395-line bisect, and it violated the project convention of
organizing by sub-domain into many files.

Split under `features/tower/tamper/`, verified content-identical (2,359 non-blank body lines
before and after) with all 128 scenarios conserved and all files parsing:

| File | Contents |
|---|---|
| `tamper_job_authority.feature` | request authorization, dispatch lease, execution grant, grant compensation snapshot, capability, funding lots and reservations |
| `tamper_transit_and_receipt.feature` | provider assertion, Tower transit statement, Core transit observation, settlement receipt, compensation receipt |
| `tamper_typed_sets.feature` | `CanonicalTypedSetV1` and the registry rows |
| `tamper_compensation_variants.feature` | compensation envelope, affected-state projections, variant mutation outlines, dust, hold and classification references |
| `tamper_policy_and_ledger.feature` | every `*PolicyV1`, `*DecisionV1`, `*IncidentV1`, control projections, ledger head, payout instruction, role-signature interchange |

Both split conditions were met: the source-of-truth header comment and the universal-mutation
note are replicated verbatim into all five files, each carries the original `Background:`, and
every cross-reference elsewhere in the set was repointed from the deleted filename to the
specific matrix that now owns the contract.

## Fix 5: the remaining untestable assertions

Few in number, and each has a mechanical repair.

| Kind | Where | Repair |
|---|---|---|
| Asserting the absence of a claim in prose | `packaging.feature`, `trust_tiers.feature`, `modes.feature` | either drop, or make it a real test that greps the shipped documentation corpus |
| Unbounded "bounded" retention | `trust_tiers.feature` abuse tips | name the retention bound as signed policy |
| Undefined "excessive" | `control_plane_tamper_matrix.feature` threshold validation | state the rule, presumably threshold greater than signer-set size |
| Unfalsifiable holds | `job_and_settlement.feature` v1 holds that never release | assert instead that no v1 signer role can transition the hold - a Scenario Outline over the key-role table |
| Review with no terminal state | resolved for self-dealing on 2026-08-02 | apply the same signed-deadline-plus-default-disposition pattern to every remaining hold |

## Order of work

1. **Vocabulary unification** across all 22 files. Nothing else matters until the step count
   collapses. Target: under 400 distinct step phrasings.
2. **Field-table conversion** of the remaining ~67 schema steps.
3. ~~Split the tamper matrix five ways.~~ Done 2026-08-02.
4. **Founder decision on Tier 4**, then freeze the Phase 2 object set.
5. **Then, and only then**, write step definitions - starting with Tier 0, against real
   PostgreSQL, as red tests for the existing production money-path repairs.

Steps 1 through 3 are mechanical and behavior-preserving. They can proceed in parallel with
the founder's approval of the behavior itself, because they change how the specification is
written and not what it asserts.

## What this guide does not change

No approved scenario's meaning. No founder decision. No production code. The specification
remains proposed and unapproved, and `features/tower/glossary.feature` remains the normative
source for vocabulary that the other files inherit.
