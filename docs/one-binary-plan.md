# One binary: refactor plan

Status: APPROVED 2026-10-07 (founder), with features/packaging/one_binary.feature and all ten
recommended decisions.

Goal: `roger` is the only user binary (`roger tower ...` replaces `roger-tower` and
`roger-tower-local`), `rogerai-broker` absorbs the tokenizer sidecar, and every concern has
one home. Each step below is shippable on its own: the suite and `make cover-gate` are green
after it, and no step depends on a later one.

## Order

### 0. Before starting
- Merge main into the working line first. Main has the broker job-ack change
  (`internal/agent/agent.go`: `ackJob`, `/agent/ack`, features/multinode/node_ack.feature),
  which touches the agent loops step 4 restructures. Doing it later means resolving it
  inside a refactor.
- Land the LAN nonce fix (every LAN Tower request carries a nonce) on wt/lan-mode; step 2
  generalizes it.

### 1. `internal/netpriv` (leaf): one private-address rule
- New `internal/netpriv`: `Classify(ip) Class` (loopback, private, ULA, link-local, CGNAT,
  public), `IsLocal(ip, allowCGNAT bool)`, and the CIDR table. Standard library only.
- Move callers: `localplane/bind.go` (privateBindCIDRs), `tower/egress.go`
  (defaultPrivateCIDRs + mustCIDRs), `lanmode.IsLocalIP`, `client.targetsLANTower`,
  `edge/wire` (PrivateInterfaces, AdvertiseAddrs), `cmd/rogerai/edgebind.go`,
  `tower/doctor.go` (isLoopback string prefix), `cmd/roger-tower/serve.go`
  (classifyAdvertised).
- `lanmode` stops importing `localplane` (it only needed IsPrivateAddr).
- Structural test: no CIDR tables or `IsPrivate` calls outside netpriv, except
  `protocol.TrustedBase` (a different question: loopback-only key trust).
- Behavior changes: doctor's loopback check parses addresses; joined serve's link-local
  tier matches lanmode.

### 2. One nonce decision
- `netpriv`-based `protocol.WantsNonce(base)` (or in `lanmode`), used by
  `client.signWithKey` and `agent.signLocal`.
- Behavior change: loopback standalone Towers get a nonce too.

### 3. `internal/netbind`: one bind helper
- Fold `localplane.ResolveBind`, `edgeListen` (per-private-interface wildcard), the
  web console's loopback bind, the TUI and webui `listenFreePort` copies, and onboarding's
  port scan into `netbind`: `Resolve(bind, posture)`, `ListenEach(privateIfaces, port)`,
  `ListenFree(host)` (binds once, returns the listener; never probe-close-rebind).
- The joined hub (`towerhub/hub.go`) gets a logged posture and refuses hostname binds.

### 4. One share loop and one Tower probe
- `lanmode.IsTower(base)` is the only probe (absorbs `isLocalTowerBroker` and
  `ProbeTower`), with one short cache.
- `agent.StartLAN` is the only standalone loop; `serveLocalTowerShare` and `shareLAN`'s
  goroutines become calls to it.
- One-time migration: a broker setting that IsTower recognizes is copied into
  `lan_towers`; Tower discovery then reads `lan_towers` only.
- `client.localTowerReceipt` replaces the two local-receipt synthesizers
  (client.go:945, lan_relay.go:82).

### 5. One key loader and one live-share registry
- `internal/keyfile.LoadOrCreate(path)` for node.key and user.key (same bytes, same 0600).
- Merge `lan-shares/share-<pid>.json` into `internal/onair` (one pid registry, one
  staleness rule); migrate old records on start.

### 6. `internal/localplane/serve` and the Core-free proof
- Move roger-tower-local's main body (bind, `tower.Open`, `RequireMode`, `Lock`, listen,
  `http.Server`, advertise) into `internal/localplane/serve.Prepare/Run`, reading the YAML
  config like the other tower commands.
- Shared structural helper `internal/structural` (or a testutil) used by
  localplane/serve, edgeauth and later Core-free packages: forbidden-prefix list, toolchain
  required, minimum dependency count.
- Runtime guard: in standalone mode, replace `net.DefaultResolver`'s dial,
  `http.DefaultTransport` and a package-level dialer with a netpriv-guarded dialer, and
  assert no Core client was constructed (a process-wide flag Core clients set when built).
- `cmd/roger-tower-local` becomes a ~30-line main calling `serve.Run` (still green, still
  shipping), so step 7 is only a move.

### 7. `internal/towercli` and `roger tower`
- Move every `cmd/roger-tower` command into `internal/towercli` (exported `Run(args,
  stdout, stderr) int`), resolving the `package main` symbol clashes (`cmdConfig`,
  `cmdResume`, `cmdStatus`, `loadConfig`, `usage`, `run`, `newCoreStub`).
- `roger tower ...` dispatches to towercli; `serve` dispatches on the directory's mode to
  `localplane/serve` (standalone) or the joined path. Wrong-mode flags are refused.
- `roger tower login|logout` call roger's login plus the data-directory marker.
- `cmd/roger-tower` and `cmd/roger-tower-local` become exec shims for one release.
- macOS and Windows: standalone supported; joined `init` refuses with "joined Towers run
  on Linux".

### 8. Version, hints and usage
- One `-X .../internal/version.Version` for roger (and the shims) and one for the broker;
  remove the broker's hardcoded `var version`.
- Replace every hint string naming the old binaries (list below) and fix
  `internal/client/lan.go:279` (admit needs --id/--code; the owner runs invite).
- Shared usage/flag helpers for tower and lan commands.

### 9. Packaging and docs
- `.goreleaser.yaml`: one roger build per platform; Linux-only shims for one release; drop
  the `tower` and `tower-local` archives after that release.
- `web/src/install.sh`: tower/tower-local components install roger (+ shims for one
  release).
- `packaging/tower/roger-tower.service`: `ExecStart=roger tower serve ...`; README and
  example configs.
- Makefile (`TOWER_VERSION_LDFLAGS`), `.gitignore`, `scripts/hooks/pre-push:102`.
- Web: manual.html (776-800, 824-825, 841-846, 885-895, 935, 1755), tower.html (287, 291,
  308), broadcasts-run-a-tower.html, js/site.js:226, web/test/tower.test.mjs (158, 162),
  pinned page-text fixtures.
- Docs: tower.md, tower-architecture.md, auth-and-tower-operations.md (incl. 210, 226, 234:
  restate the Core-free guarantee as package-level plus runtime guard),
  relay-selection-design.md, roger-edge-design.md:90, minimum-hardware-requirement.md.
  Release notes stay historical.

### 10. Server side
- `rogerai-broker tokenizer` runs the sidecar's `newMux`/`run` (same `/count`, `/health`,
  `TOKENIZER_PORT`, `TOKENIZER_DIR`); it opens no database or shared store. Remove
  `cmd/tokenizer-sidecar`. Update Makefile:23, .env.example:274-295, recount.go comments,
  features/money/recount_billing.feature:75. Cost: the broker binary gains the tiktoken
  tables (lazy-initialized; size and build time only).

### 11. Remove the shims (the release after)
- Delete `cmd/roger-tower` and `cmd/roger-tower-local` and their release entries.

## Risks
- **Merge order.** The job-ack change on main touches agent loops (step 0).
- **ldflags.** The tower binaries stamp `main.version`; roger stamps `Version`. Moving to
  one `internal/version` package changes the ldflags path in goreleaser and the Makefile
  together (step 8), or the shipped binaries report "dev".
- **Symbol clashes** in `package main` (step 7): solved by moving tower commands into
  `internal/towercli`, not into `cmd/rogerai`.
- **The Core-free claim weakens** from "no code in this binary" to "a package that imports
  no Core code, plus a runtime guard". The approved wording is superseded; docs and help
  must not overclaim (step 6, step 9).
- **Default-transport guard** must be installed before any goroutine can dial; it runs at
  the very start of `roger tower serve` in standalone mode, and a test proves
  http.DefaultClient is refused.
- **Tower code on macOS/Windows** is new surface: standalone is plain Go and local;
  joined refuses there until tested.
- **Coverage.** Moving code between packages shifts coverage; each step keeps every
  package at or above 90%.

## Approved spec lines that name the old binaries (proposed new text)

| File:line | Today | Proposed |
|---|---|---|
| auth/email_code_login.feature:36 | `roger login` and `roger-tower login` | `roger login` and `roger tower login` |
| auth/email_code_login.feature:310-311 | roger-tower login works through the email route | roger tower login works through the email route |
| auth/broker_mediated_login.feature:3, 14 | `roger-tower login`; roger-tower's egress story | `roger tower login`; a Tower's egress story |
| edge/enrollment.feature:42-44 | roger-tower-local links no Core (binary) | the standalone serve package imports no Core code, plus the runtime guard |
| tower/approval_visibility.feature:1 | the roger-tower cli | the `roger tower` commands |
| tower/edge_dispatch.feature:11 | `roger-tower probe` | `roger tower probe` |
| tower/standalone_consumer_plane.feature:1-9, 41-58 | Core-free binary; "by linkage not by policy" | superseded by one_binary.feature section 4 |
| tower/packaging.feature:20 | a `roger-tower` archive | a `roger` archive (shims for one release) |
| tower/packaging.feature:43 | `roger-tower release audit` (does not exist) | name the real audit step, or mark it unbuilt |
| tower/packaging.feature:158 | `roger-tower serve` starts | `roger tower serve` starts |
| tower/station_attachment.feature:13, 51 | `roger-tower station invite` (does not exist) | `roger tower attach` (the real command), or mark unbuilt |
| tower/operator_login.feature:8 | `roger-tower` | `roger tower` |
| lan/discovery_and_dial.feature:10, 38-39, 51 | `roger-tower-local serve --advertise` (no serve subcommand) | `roger tower serve --advertise` |
| lan (approved 2026-10-07) standalone_streaming.feature | roger-tower-local | roger tower serve |
