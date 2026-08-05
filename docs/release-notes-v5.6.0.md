# v5.6.0 — a login of our own, and Towers that can be admitted

Two bodies of work: RogerAI stops borrowing every identity it has, and a community-run
Tower can be registered onto the public network.

**Read the two "before you deploy" items first.** Both change what a deployment must do.

---

## Before you deploy

### 1. The `rogerai` schema must exist before the broker starts

Migrations create tables inside it and never the schema itself, because the app's database
user is expected to have no DB-level CREATE. Provision it once, as an admin:

```sql
CREATE SCHEMA rogerai AUTHORIZATION <app_user>;
GRANT USAGE, CREATE ON SCHEMA rogerai TO <app_user>;
```

Deployments that already run the money schema have this. It is called out because the Tower
migration is new and would otherwise be the first thing to notice its absence.

### 2. A configured subsystem that cannot start now stops the deploy

Previously a subsystem that failed to initialise turned itself off and logged a line. That
is right for something you never configured and wrong for something you did: the deployment
comes up healthy with a feature missing, and the first person to find out is a user it did
not work for.

Now: **not configured** is quiet and safe; **configured but broken** exits at startup.

If a deploy fails after this upgrade, that is the intended behaviour surfacing a
misconfiguration that was previously silent. The log line names the subsystem and the cause.

---

## Sign-in

**A RogerAI account of our own.** Identity was entirely borrowed - an owner row keyed on a
GitHub id or an Apple sub - so anyone holding neither could not sign in, two third parties
could lock a customer out of an account holding a wallet balance, and provider outage was
total sign-in outage. There is now a first-party account keyed on a verified email, entered
with a mailed code. GitHub and Apple are unchanged and still offered.

The sign-in flow never consults an account store, so it cannot reveal whether an address is
known - there is no branch to leak. The mail carries no one-click link: a followed link
authenticates whoever followed it, including a mail scanner, so the code is typed back into
the session that asked for it.

Auto-linking an email to an existing provider account requires **both** sides verified.
Linking on a provider's unverified address would be full takeover of an account holding a
balance.

**Device login survives restarts and works across instances.** It previously kept pending
logins in process memory, so a restart dropped them - reported to the CLI as "that code is
not valid", which is the rejection meant for a guesser - and behind more than one instance
the flow could not complete at all, because the approval landed on one process and the poll
read another. Both fixed; single-instance deployments are unchanged and need no new
configuration.

**Multi-instance now needs `ROGERAI_REDIS_URL`.** Without it, device login and email sign-in
cannot complete across instances. Single-instance is unaffected.

**Mail** is provider-swappable (ZeptoMail or Resend) by configuration. A separate fix: the
delivery log carried the full recipient address, and the sign-in subject carried the code -
so an ordinary log line held a live credential next to the address it belonged to. Addresses
are masked and the code never enters a subject.

## Joined Towers

**Registration works end to end.** `roger-tower login` then `roger-tower register` admits a
Tower, issues it a certificate, and leaves it in quarantine. A new Tower is never trusted on
arrival.

Three independent proofs, and holding any one is not enough: the enrollment token proves an
operator was approved, a challenge signature proves the machine holds the identity key it
claims, and a CSR proves it holds a **separate** channel key. The two keys stay separate so
rotating a certificate never renames a Tower and a stolen channel key proves nothing about
its identity.

**Certificates renew on the link the Tower already holds**, at two thirds of lifetime, with
no operator involvement - which is a security property as much as a convenience: an operator
who is never asked to re-authenticate a Tower has no habit for a phishing mail to exploit.
The old certificate is not revoked on renewal, so a Tower mid-rotation keeps its live
connection.

**The admission registry and the CA root are durable.** Both previously lived in process
memory, which meant a deploy forgot which Towers were admitted, **undid revocations** - and
un-burned the identity keys revocation burns - and erased accumulated false-claim evidence.
The CA root now has explicit custody: injected from a secret store, persisted, or generated
once and logged loudly. A half-configured root is refused rather than generated, because
issuing under a root nobody chose makes every certificate on the network unverifiable.

**Not shipped yet:** `roger-tower serve`, `drain`, and `revoke` need the joined relay link.
A registered Tower is admitted, holds its certificate, and waits in quarantine; it does not
carry traffic. Standalone Towers serve today and need none of this.

## Fixes worth naming

- **Rolling deploys.** `CREATE TABLE`/`CREATE INDEX` with `IF NOT EXISTS` are not atomic
  against a concurrent create, so two pods starting together could have one fail on a
  system-catalog unique violation. In the money store that was fatal - the broker refused to
  start. Every migration now retries once; a second failure still stops the deploy.
- **`CREATE SCHEMA IF NOT EXISTS` removed from the Tower migration.** PostgreSQL checks
  CREATE-on-database before the IF-NOT-EXISTS short-circuit, so it failed with "permission
  denied" on any least-privilege deployment *even though the schema existed* - and, because
  a failed setup used to disable itself quietly, it would have shipped as "Tower
  registration doesn't work" with no error anyone was watching.
- **An enrollment token was usable by anyone who obtained it.** Nothing bound a request to
  the authenticated account, so a leaked token could enroll a Tower onto somebody else's
  account. Requests now carry the operator the broker authenticated.
- **Open-redirect** closed on the post-sign-in destination.

## Upgrading

Additive and idempotent throughout; no destructive migration and no downtime step.
Rollback to the previous binary is safe - the new columns and tables are simply unused by
it. Sessions minted before this release keep working.

Full operational detail, including what to check after the deploy:
`docs/auth-and-tower-operations.md`.
