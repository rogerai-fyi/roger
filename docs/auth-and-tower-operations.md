# Running sign-in and joined-Tower admission

What a deployment needs to configure, what happens when it configures nothing, and what to
check before and after a release. Provider-neutral by design: everything here is portable,
and nothing describes any particular deployment.

## The rule everything follows

**Not configured and misconfigured are different, and they end differently.**

A subsystem you did not configure degrades to something safe and says so. A single-instance
broker with no database and no cache runs sign-in and refuses joined Towers; it never issues
a credential it cannot remember.

A subsystem you DID configure and that cannot start takes the process down at startup. That
is deliberate, and it is new in this release. Turning a configured feature off and carrying
on means the deployment comes up healthy with something missing, and the first person to
find out is a user it did not work for - which is exactly how a migration bug that failed on
every least-privilege deployment stayed hidden here.

## Prerequisite: the `rogerai` schema must already exist

The broker's migrations create TABLES inside `rogerai` and never the schema itself, because
the app's database user is expected to have no DB-level CREATE. Provision it once, as an
admin, before the first start:

```sql
CREATE SCHEMA rogerai AUTHORIZATION <app_user>;
GRANT USAGE, CREATE ON SCHEMA rogerai TO <app_user>;
```

If the schema is missing, startup fails with a clear error rather than coming up without
Tower admission. `CREATE SCHEMA IF NOT EXISTS` is deliberately NOT in the migration:
PostgreSQL checks CREATE-on-database before the IF-NOT-EXISTS short-circuit, so it fails
with "permission denied for database" even when the schema is already there.

## Configuration

### Sign-in (device login and first-party email)

| Variable | Required | Without it |
|---|---|---|
| `ROGERAI_REDIS_URL` | no | Sign-in works on ONE instance. Pending device logins and outstanding email codes live in process memory, so a restart drops them and a second instance cannot complete a flow the first started. **Required for any multi-instance deployment.** |
| `ZEPTOMAIL_API_KEY` or `RESEND_API_KEY` | for email sign-in | Emailed codes are unavailable and the route says so plainly. GitHub and Apple sign-in are unaffected. |
| `MAIL_FROM` | no | Defaults to a sender on the selected provider's domain. Set it to a domain **this** deployment has verified with **that** provider - the provider that verified a domain is the only one that can sign for it. |
| `DATABASE_URL` | for accounts | Accounts live in memory and do not survive a restart. Fine for a demo, never for a deployment. |

### Joined-Tower admission

| Variable | Required | Without it |
|---|---|---|
| `DATABASE_URL` | **yes** | Joined-Tower admission is **OFF** and the routes refuse with a plain message. Standalone Towers are unaffected - they need nothing from us. If `DATABASE_URL` IS set and admission cannot start, the broker exits instead: you configured it, so a silent absence is not an outcome we may choose for you. |
| `ROGERAI_TOWER_CA_KEY_PEM` + `ROGERAI_TOWER_CA_CERT_PEM` | recommended | A root is generated on first start and stored in the database, with a warning in the log. Supply both to keep the root in your secret store instead. **Supply both or neither**: half a root is refused rather than quietly generating one, because issuing under a root nobody chose makes every certificate on the network unverifiable. |
| `ROGERAI_TOWER_CERT_TTL` | no | Issued Tower certificates live 24h. The lease in the registry is the long-lived grant; the certificate is deliberately short because it cannot be recalled once handed out. |

## The CA root

This root can mint a certificate for **any** Tower ID, which means whoever holds it can
speak as any Tower on the network. Treat it accordingly.

Three ways a deployment gets one, in the order the code tries them:

1. **Injected** - both halves supplied as PEM. The process neither generates nor stores the
   root. This is what a production deployment should do: custody stays outside the
   application database and the root can be rotated without a code change.
2. **Persisted** - a root this deployment generated earlier and kept.
3. **Generated once**, stored, and logged loudly. A self-hoster should not have to run a PKI
   ceremony before their first Tower, but they are told plainly where their root ended up.

To move a generated root into a secret store, read it out of `rogerai.tower_ca_root`, put
both halves in your secret manager, set the two variables, and restart. The root does not
change, so no certificate is invalidated.

**Rotating the root invalidates every certificate issued under it.** Every Tower must
re-enroll. There is no partial path; plan it as a network event.

## Schema

All schema is applied on startup and is **additive and idempotent** - `CREATE TABLE IF NOT
EXISTS`, `ADD COLUMN IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`. There is no destructive
migration and no downtime step. A rollback to the previous binary is safe: the added
columns and tables are simply unused by it.

Tables added for this work, all under the `rogerai` schema:

- `tower_admissions`, `tower_enrollment_tokens` - who is admitted, and unspent tokens
- `tower_ca_root`, `tower_ca_revoked` - the issuing root (when not injected) and revocations
- `tower_enroll_challenges`, `tower_enroll_committed` - in-flight enrollment
- `owners.email_verified_at` plus a partial unique index on a verified address

The unique index is partial - it covers only verified, non-anonymized rows - so unverified
addresses stay unconstrained and a deleted account's address becomes reusable rather than
held forever.

## Pre-deploy checks

- `make cover-gate` passes.
- `DATABASE_URL` points at a database this binary can migrate.
- For more than one instance: `ROGERAI_REDIS_URL` is set. Without it, device login and email
  sign-in cannot complete across instances - a request lands on one and its follow-up on
  another.
- If injecting a CA root, **both** variables are set and the halves belong together. A
  mismatch is refused at startup with an explicit message rather than at the first handshake.

## Standalone Towers and PostgreSQL 15+

A standalone Tower with a durable store creates its table in whatever schema its DSN
resolves to - `public` by default. **PostgreSQL 15 and later no longer let a non-owner
create tables in `public`**, so a Tower running as a dedicated least-privilege role fails at
startup with "permission denied for schema public". Either grant it:

```sql
GRANT CREATE ON SCHEMA public TO <tower_user>;
```

or point the DSN at a schema that user owns. The startup error says this too - it is called
out here because it is a trap a self-hoster meets on a fresh modern PostgreSQL and nowhere
else.

## Rolling deploys

Migrations are safe to run concurrently. `CREATE TABLE`/`CREATE INDEX` with `IF NOT EXISTS`
are not atomic against a concurrent create, so two pods starting together can have one lose
on a system-catalog unique violation; every migration retries once, which settles it. A
second failure is surfaced rather than retried, so a real problem still stops the deploy.

## Post-deploy checks

The startup log states what is on, in these words:

- `tower: joined-Tower admission is ON (protocol vN-N)` - or `OFF` with the reason.
- `tower CA: generated a new issuing root...` - if you see this on a production start, the
  root is in your database and should be moved to your secret store.
- `device login: pending logins are shared across instances`
- `email login: outstanding codes and their budgets are shared across instances`

The last two are absent on a single-instance deployment, which is correct there and a
problem behind a load balancer.

Then, end to end: sign in with an emailed code; run `roger login` and approve it from the
browser; run `roger-tower login` and `roger-tower register` and confirm the Tower reports
`quarantine`. Quarantine is the expected state - a new Tower is never trusted on arrival,
and promotion is a separate, deliberate decision.

## The relay link

`roger-tower serve` holds it. A joined Tower opens a session, pushes a signed inventory,
heartbeats, and drains on shutdown so Core drops its inventory at once rather than letting it
age out over the freshness window.

It relays the offers its **Stations** signed, byte for byte. It cannot make one up: a Station
signs with an assertion key the Tower does not hold and must never hold, because a relay that
could sign would make "signed by the Station" mean "signed by whoever is relaying". A Tower
with nothing in its offers directory pushes a valid inventory of zero leaves — "I am here and
I have nothing."

> **HISTORICAL:** the `roger-station` runbook below describes the retired leaf-station
> generation. A provider now runs plain `roger share` - no flag, no second binary - and the
> share offers itself to the relay fabric on its own (self-attach at their own listed price,
> blind-serve sealed submits, audits answered automatically) on top of going on air the
> ordinary way; a tower operator runs `roger-tower serve --hub :8444 --relay-public HOST:PORT`.

### Serving work

Given `--station ID=URL`, `serve` also collects work for its Stations:

```
roger-tower serve --dir DIR --station st-abc123=http://127.0.0.1:8730/execute
```

and on the Station:

```
KEYS=$(curl -s https://BROKER/tower/dispatch/key)
roger-station serve --dir DIR --upstream http://127.0.0.1:11434/v1/chat/completions \
    --core-key          $(jq -r .dispatch_key <<<"$KEYS") \
    --core-envelope-key $(jq -r .envelope_key <<<"$KEYS")
```

Two keys, pinned out of band, because a Station only ever talks to its Tower — which is
exactly the party a forged grant would come from, so fetching them over that channel would
prove nothing. `dispatch_key` is what proves a grant came from Core. `envelope_key` is what
results are sealed to.

The Station opens the sealed request, verifies that Core signed the grant, that it names
**this** Station, and that the request inside is the one the grant commits to — then executes
and signs for exactly what it returns, sealed back to Core.

The Tower is a courier and **cannot read either direction**: it holds an ephemeral key, a
nonce and ciphertext. It cannot alter the request (the grant commits to a digest of the
plaintext, and the Station checks after opening) and cannot alter the answer (the receipt
commits to a digest, and Core checks after opening).

Two things worth knowing before you wonder why a healthy Station is idle:

- **Tower-backed work is a fallback.** It is routed only when no directly-registered node
  offers the model.
- **It is uncompensated.** Nothing is charged for it and nothing is earned. Responses carry
  `X-RogerAI-Cost: 0`. Paid Tower work waits on the compensation ledger.

The `--core-key` is pinned out of band on purpose. A Station only ever talks to its Tower —
which is exactly the party a forged grant would come from — so fetching the key over that
channel would prove nothing.

## Attaching a Station

Three machines, three steps, and the key never moves.

**On the Station** — `roger-station` is a separate binary for exactly this reason; running it
on the Tower would put a Station's private keys on the relay.

```
roger-station init --dir /var/lib/roger-station
# prints: station id, assertion key, session key
```

**On your workstation, signed in** (`roger-tower login`):

```
roger-tower station invite --dir DIR --assertion-key HEX --session-key HEX [--station-id ID]
# prints an invitation id and a ONE-TIME secret, and the exact attach command
```

**On the Tower**, redeeming as the Tower:

```
roger-tower station attach --dir DIR --invitation ID --secret S \
    --assertion-key HEX --session-key HEX --station-id ID
```

The two calls are signed by different keys deliberately. `invite` is your **account** —
authorizing a machine to serve under it is an account decision, and the account is what a
suspension acts on. `attach` is the **Tower** — Core takes the Station's origin from whoever
signed, so a relay cannot attach a Station behind somebody else's.

Then, back on the Station, publish an offer and copy it across:

```
roger-station offer --dir DIR --tower TOWER-ID --model NAME \
    --price-in N --price-out N --earn-in N --earn-out N --capacity N \
    --out offer.json
# copy offer.json into the Tower's `offers` directory
```

`roger-tower status` reports what **Core** believes: state, whether the link is live, and
which Stations are routable. That is the only trustworthy answer — the Tower's own files
record what it was told at enrollment and go stale the moment anything changes.

`roger-tower station revoke --dir DIR --station-id ID` retires a Station. It is signed by the
account, not the Tower, so it still works when the Tower is the thing that has gone wrong.

## Pausing, resuming and retiring your own Tower

```
roger-tower drain  --dir DIR        # stop taking new work; keep the link up
roger-tower resume --dir DIR        # take work again
roger-tower revoke --dir DIR --yes  # retire this Tower permanently
```

Draining is not the same as stopping `serve`. Stopping drops the inventory and goes;
draining leaves the Tower connected and visible while Roger Core stops routing new work to
it, so in-flight work finishes on its own deadlines. That is what you want before a disk
swap or an upgrade, and it is reversible.

These are signed by your **account**, not by the Tower, so retiring hardware still works when
the Tower itself is the thing that has gone wrong.

`resume` can only return a Tower to a state it already held. Leaving **quarantine** is an
administrator's decision and none of these commands can make it — the permission check is
keyed on the state the Tower is in, not only the state you are asking for.

## Quarantine

Both Towers and Stations are admitted **into quarantine**. That is not a missing feature and
not a fault: admission (proving who you are) and eligibility (being trusted with customer
traffic) are separate decisions on purpose.

Opening either gate is an administrator's call, not an operator's — the party asking to be
trusted cannot also be the one granting it:

```
POST /tower/lifecycle          {"tower_id":"...","state":"active"}      # admin
POST /tower/station/promote    {"station_id":"..."}                     # admin
POST /tower/lease/expire       {"tower_id":"..."}                       # admin
```

Each takes the admin credential. `/tower/lifecycle` applies the approved transition table, so
a suspended Tower returns through quarantine rather than straight to active.

Promotion is manual and deliberately the only path today. The approved design promotes on
evidence Core gathered itself — session uptime it held, probes it dispatched, signatures it
verified — and none of that is built; an automatic ladder that does not exist would be worse
than a switch a human has to throw.

## What is not shipped yet

- **Compensation.** Tower-backed work earns nothing yet; the funding-reservation and
  attempt-ledger machinery the signed grant contract binds to does not exist.
- **Streaming** through a Tower. A streamed answer needs the inner Station session, so a
  `stream: true` request is never routed to a Tower.
- **Multi-instance dispatch.** The pending-work queue is in-process, so a Tower must poll the
  broker instance that issued its work.
- Nothing else on this list. Draining, resuming and retiring shipped — see below.

Standalone Towers serve today and need none of this.
