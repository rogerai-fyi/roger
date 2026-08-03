# Running a RogerAI Tower

A **Tower** is a relay. It routes requests to **Stations** — the machines that actually
serve models. There are two ways to run one, and the difference matters:

| | **Standalone** | **Joined** |
|---|---|---|
| What it is | Your own private network | A child relay of the public RogerAI network |
| Talks to RogerAI | Never | Yes |
| Account needed | **No, none, ever** | Yes |
| Who it serves | You and your machines | Other people's traffic |
| Status | Available now | Not yet — Phase 2 |

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

Joined mode adds `login`, `logout` and `register`. They exist today only to tell you the
joined protocol has not shipped - see below.

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

## Run it as a service

**Not yet.** The unit runs `roger-tower serve`, which lands with the joined protocol
(Phase 2); enabling it today gives you a crash loop. Drive a standalone Tower with the
one-shot commands above.

The unit ships now so its hardening can be reviewed alongside the code it will protect.
When `serve` exists, the install is:

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

## What is not built yet

Joined mode. `serve`, `drain`, `revoke`, `login` and `register` all exist today only to
tell you so, rather than failing obscurely. Joining the public network needs the Phase 2
protocol: admission, short-lived certificates, signed dispatch leases, and the receipt
contract. Until that ships, a Tower is a local tool — a genuinely useful one, but a local
one.

**Do not enable the systemd unit yet.** It runs `roger-tower serve`, which does not
exist, so it would crash-loop. The unit ships now so its hardening can be reviewed
alongside the code it will protect.

Durable PostgreSQL storage is also pending; today state lives in the data directory,
which is fine for a single node and is not yet the fail-closed durable profile the spec
describes.

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
