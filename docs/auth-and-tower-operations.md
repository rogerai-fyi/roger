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

## What is not shipped yet

`roger-tower serve`, `drain`, and `revoke` need the joined relay link, which has not shipped.
Registration is complete: a Tower is admitted, holds its certificate, and sits in quarantine
until the link lands. Standalone Towers serve today and need none of this.
