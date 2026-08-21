# Running a RogerAI Tower

A **Tower** is a relay. It routes requests to **Stations** — the machines that actually
serve models. There are two ways to run one, and the difference matters:

| | **Standalone** | **Joined** |
|---|---|---|
| What it is | Your own private network | A child relay of the public RogerAI network |
| Talks to RogerAI | Never | Yes |
| Account needed | **No, none, ever** | Yes |
| Who it serves | You and your machines | Other people's traffic |
| Earns | Nothing (it is your own traffic) | 10% of gross on every request it carries |
| Status | Available now | Available now |

Standalone needs no account because nothing leaves your machine. Joined needs one
because the moment you relay strangers' traffic you become infrastructure, and a Tower
that misbehaves has to be revocable — which only means something if an identity costs
more than a few seconds to replace.

## Install

```sh
curl -fsSL https://rogerai.fm/install.sh | ROGERAI_COMPONENT=tower sh
```

Linux only, on purpose: a Tower is a long-running server process.

The installer checks the download against the release `checksums.txt` and refuses to
install on a **mismatch**. Be aware of what that does and does not cover: if no sha256
tool is present, or the asset is absent from `checksums.txt`, it says so and continues
rather than stopping. The checksums are also served from the same origin as the binary,
so this detects corruption and truncation, not a compromised release host. Signed
artifacts with provenance are specified in `features/tower/packaging.feature` and are not
built yet.

## Standalone in five commands

```sh
# 1. Create the data directory. This fixes the mode for the life of the directory.
roger-tower init --dir /var/lib/roger-tower --mode standalone

# 2. Check what it will do before it does anything. (doctor reads a CONFIG FILE, not the
#    data directory - write the YAML below first, or skip this step for now.)
roger-tower doctor --config /etc/roger-tower/tower.yaml

# 3. Mint a one-time code for your first client. Shown ONCE - it is never stored.
roger-tower invite --dir /var/lib/roger-tower --client <your-client-key-hash>

# 4. Redeem it. That client becomes this network's operator.
roger-tower admit --dir /var/lib/roger-tower --client <hash> --id <id> --code <code>

# 5. Attach a machine that serves a model, then route to it.
roger-tower attach --dir /var/lib/roger-tower --station st-1 --key <hash> --models llama-8b
roger-tower route  --dir /var/lib/roger-tower --client <hash> --model llama-8b
```

`roger-tower stations` lists what is attached; `roger-tower status` shows the mode and
network id; `roger-tower version` prints the build.

Two kinds of argument, which the commands do not mix: `--dir` names the **data
directory** (identity, admission state, attached Stations); `--config` names a **YAML
file** (listeners, limits). `init`, `invite`, `admit`, `attach`, `stations`, `route` and
`status` take `--dir`. `doctor` and `config` take `--config`.

Joined mode adds `login`, `logout`, `register`, `serve`, `earnings`, `drain`, `resume` and
`revoke`. They are real: a joined Tower holds a link to Roger Core and hosts the sealed data
plane - see below.

## Configuration

```yaml
apiVersion: tower.rogerai.fm/v1alpha1
kind: Tower
mode: standalone

identity:
  dir: /var/lib/roger-tower

stationListener:
  address: 127.0.0.1:7070
adminListener:
  address: 127.0.0.1:7071
```

Three things the config will not let you do, by design:

- **Put a secret in a scalar.** Enrollment tokens, keys, and database URLs with passwords
  are supplied as owner-only files. A scalar ends up in shell history and backups.
- **Mix the modes.** A standalone config cannot name a public authority, enrollment
  token, or payout setting; a joined config cannot name a local trust root. These are
  rejected, not defaulted off, so a later edit cannot quietly flip one on.
- **Typo a control into silence.** Unknown fields are errors. A misspelled setting fails
  the config rather than being ignored.

`roger-tower config print --config FILE` shows every effective value including defaults,
with secret *paths* shown and their contents never read. There is no unredacted mode.

### Durability

```yaml
storage:
  profile: durable        # or "development" (the default)
```

**development** is the default and says so on every `ready` and `status`: identity,
admission state and attached Stations may be lost on restart. Fine for trying it out,
never quiet about what it is.

**durable** promises the state survives a restart, so `roger-tower ready` checks every
dependency that promise rests on - the identity volume, the pinned offline root, the local
signing key, and the database secret if one is configured - and **exits non-zero** when any
is missing, naming the repair for each. Put it in an `ExecStartPre` or a readiness probe:
a Tower that cannot keep its state should refuse to serve rather than serve and lose it.

One thing the durable profile does *not* yet mean: Tower state still lives in the data
directory rather than in PostgreSQL. The profile verifies the dependencies; moving the
state itself is separate work, and claiming otherwise would be exactly the silent loss the
preflight exists to prevent.

## Run it as a service

The unit runs `roger-tower serve`, which holds the link to Roger Core and hosts the sealed
hub - the data plane consumers submit encrypted work to and `roger share` nodes poll.
Give it a hub listener and the public address consumers reach it at, in the config file the
unit passes:

```yaml
hub:
  address: :8444          # what the process binds
  tlsCert: /etc/roger-tower/hub.crt   # optional, and recommended: the payload is sealed
  tlsKey:  /etc/roger-tower/hub.key   # end-to-end regardless, but node tokens ride TLS
relay:
  public: tower.example.com:8444      # what Core advertises to consumers and nodes
```

The install is:

```sh
sudo useradd --system --home /var/lib/roger-tower --shell /usr/sbin/nologin roger-tower
sudo install -m0755 roger-tower /usr/local/bin/
sudo install -m0644 roger-tower.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now roger-tower
```

The unit runs unprivileged with a read-only filesystem apart from its data directory, no
capabilities, and no ability to gain privilege. If you run **standalone**, uncomment the
`IPAddressDeny=any` block — it turns "this Tower does not phone home" from a property of
the code into one the kernel enforces.

## What is and is not built

**Joined mode is built.** Admission, short-lived certificates, the link (session, heartbeat,
clean drain, renewal), the sealed hub data plane, self-attaching `roger share` nodes,
signed receipts, one-use settlement, and compensation: every settled request pays the serving
node 70% of its own listed price, **your Tower 10%**, the platform 20%. Relay earnings are
ordinary earnings — `roger-tower earnings` reads them, and they cash out on the same rail as
serving (120-day hold, $25 minimum, Stripe Connect onboarding).

**Not built:** the wider revenue-share *program* around those earnings (eligibility tiers,
maturity, payout authority, program-level clawback). Traffic is also early — the figure your
Tower shows is $0 until consumers route work through your hub.

Durable PostgreSQL storage for the Tower's own state is pending; today it lives in the data
directory, which is fine for a single node and is not yet the fail-closed durable profile the
spec describes.

## Encrypting the hub link

One flag:

```bash
roger-tower serve --hub :8444 --relay-public YOUR.HOST:8444 --hub-tls
```

With no certificate files this Tower mints a self-signed certificate, keeps it, and
advertises its public-key fingerprint to Roger Core on the link. Core hands that fingerprint
to every node and every consumer it routes here, and each of them accepts that certificate
and no other.

**No domain name, no publicly-trusted certificate, no renewal.** That is deliberate rather
than a shortcut: a volunteer relay on a home connection with a dynamic address cannot obtain
a public certificate at any price, so requiring one would not make Towers secure, it would
make most operators ineligible. And it answers a sharper question than the Web PKI does — a
node does not care whether it is talking to `relay.example`, it cares whether it is talking
to the Tower Core assigned it, which is exactly what the pinned fingerprint proves.

If you already have a real certificate, `--hub-tls-cert` and `--hub-tls-key` still work and
the fingerprint is computed from it. Nothing is lost by having one; nothing is required.

**What it protects.** Without TLS the hub link is plaintext, and anyone on the path — the
node's ISP, a shared host, your own hosting network — can read the traffic shape and forge
the status codes a node uses to reason about its own pay. They cannot read the work: every
request and answer is sealed end to end to the serving node's key, and a Tower never holds
that key with or without TLS. What TLS adds is that nobody can impersonate your hub to a
node, and the Station's long-term public identity stops riding every poll in the clear.

**One thing to know before you turn it on.** A node picks up its Tower's certificate when it
attaches. Turning TLS on, or later rotating the certificate, changes the fingerprint — nodes
already serving through you will fail against the new listener until they re-attach. They do
recover on their own (a node that keeps failing against its hub asks Core again and picks up
the new details), but it is not instant. Prefer to enable it before you have nodes, and treat
a rotation as a brief interruption rather than a transparent one.

## Your clock is load-bearing

```bash
roger-tower doctor --config /etc/roger-tower/config.yaml
```

Doctor reports two things about time: whether the kernel believes its clock is disciplined —
the half that tells you there is a repair to make — and the measured offset against an NTP
server.

This is not hygiene. Nodes authenticate to your hub by signing each request, and a signature
carries the signer's timestamp. Past the accepted skew your Tower refuses **every honest
node's poll** and relays nothing, while looking perfectly healthy from the outside: the
process is up, the port answers, the link to Core is fine, and no work moves. Doctor is where
you find that out, and the offset is reported as a problem rather than a note precisely
because of how quiet the failure is.

A standalone Tower measures nothing unless you ask. It promises no outbound connection at
all, and that promise is not ours to break to check a clock — pass `--clock-check` if you
want the measurement, and `--ntp HOST:PORT` to name your own server. A joined Tower measures
by default; `--offline` opts out.

## Security posture, stated plainly

- A standalone Tower makes **no outbound network call at all**. That is enforced by a test
  that reads the source of the whole package and fails if any file gains the ability to
  reach the network, so it cannot rot quietly.
- Outbound destinations, when they exist, must be literal IPs inside a declared private
  range. Hostnames are refused rather than resolved — resolving one is already a DNS
  lookup, and a name that resolves somewhere allowed today can resolve elsewhere tomorrow.
- A Tower never fetches a URL supplied by a request. With the caller choosing the
  destination, no allowlist can stop a fetcher from becoming an open proxy.
- Bootstrap codes carry 128 bits of OS randomness, are shown once, and are stored only as
  an HMAC verifier. Every rejection reads identically, so a failed attempt tells an
  attacker nothing about which part was wrong.
