# STATUS: PROPOSED 2026-10-07 (awaiting founder approval)
#
# ONE USER BINARY. Founder rulings 2026-10-07 (memory: one-binary):
#   - `roger` is the only binary a user downloads. `roger tower ...` replaces both
#     `roger-tower` and `roger-tower-local`. The old names stay for ONE release as thin
#     shims, then are removed.
#   - The standalone Tower's "cannot reach Roger Core" promise moves from binary linkage
#     to (a) a package-level dependency proof on the standalone serve package and (b) a
#     runtime guard in standalone mode. The approved "by linkage" wording is superseded.
#   - `rogerai-broker` stays the separate SERVER binary. The tokenizer sidecar becomes
#     `rogerai-broker tokenizer`, still its own process.
#   - Functionality is added INTO its existing home, never beside it: one rule, one helper,
#     one loop per concern (section 7).
#
# Every DRY merge below is stated as behavior a user or operator can observe, so each one
# is testable. A merge that CHANGES behavior is marked `# BEHAVIOR CHANGE:` with a
# `# DECISION:` and a recommendation.
#
# Plan and order: docs/one-binary-plan.md.

Feature: One user binary with modes
  A user installs `roger` and nothing else. A Tower operator runs `roger tower ...`. The
  standalone Tower keeps its promise that it cannot reach Roger Core, and that promise is
  stated honestly for what it now is: a package that imports no Core code, and a process
  that refuses every non-local connection in standalone mode.

  # =====================================================================================
  # 1. THE COMMAND MAP
  # =====================================================================================

  Scenario Outline: Every roger-tower command has the same behavior under roger tower
    Given a data directory initialized for "<mode>" mode
    When the operator runs "roger tower <command>" with "<flags>"
    Then the result is identical to "roger-tower <command>" with "<flags>" from the previous release
    And the exit code is the same
    And the files written to the data directory are byte-identical

    Examples:
      | command          | flags                                                  | mode       |
      | init             | --dir D --mode standalone                              | none       |
      | init             | --dir D --mode joined                                  | none       |
      | config validate  | --config C                                             | standalone |
      | config print     | --config C                                             | standalone |
      | doctor           | --config C --offline                                   | standalone |
      | doctor           | --config C --clock-check --ntp N                       | standalone |
      | ready            | --config C                                             | joined     |
      | status           | --dir D                                                | standalone |
      | status           | --dir D                                                | joined     |
      | invite           | --dir D --config C --client K --ttl 15m --attempts 5   | standalone |
      | admit            | --dir D --config C --client K --id I --code X          | standalone |
      | attach           | --dir D --config C --station S --key K --models m      | standalone |
      | attach           | --dir D --config C --station S --key K --curated       | standalone |
      | stations         | --dir D --config C                                     | standalone |
      | route            | --dir D --config C --client K --model m                | standalone |
      | register         | --dir D                                                | joined     |
      | earnings         |                                                        | joined     |
      | probe            | --model m --broker B --body F                          | joined     |
      | station revoke   | --dir D --station-id S                                 | standalone |
      | drain            | --dir D                                                | joined     |
      | resume           | --dir D                                                | joined     |
      | revoke           | --dir D --yes                                          | joined     |

  Scenario: config print still refuses an unredacted print
    When the operator runs "roger tower config print --config C --redact=false"
    Then it exits non-zero
    And it explains that an unredacted print is refused, exactly as before

  Scenario: roger tower serve on a standalone directory serves the consumer plane
    Given a data directory initialized for standalone mode
    When the operator runs "roger tower serve --dir D --bind 192.168.1.10:8787"
    Then the standalone consumer plane listens on 192.168.1.10:8787
    And it serves /discover, /v1/chat/completions, /local/poll and /local/complete as before
    And it opens no listener for the joined hub

  Scenario: roger tower serve on a joined directory serves the joined link and hub
    Given a data directory initialized for joined mode
    When the operator runs "roger tower serve --dir D --hub :8443"
    Then the joined link and the sealed hub start exactly as "roger-tower serve" did

  Scenario Outline: A flag that belongs to the other mode is refused, naming the mode
    Given a data directory initialized for "<mode>" mode
    When the operator runs "roger tower serve --dir D <flag>"
    Then it exits 2 before opening any listener
    And the message says "<flag> is a <other> flag; this directory is <mode>"

    Examples:
      | mode       | flag                  | other      |
      | standalone | --hub :8443           | joined     |
      | standalone | --relay-public        | joined     |
      | standalone | --hub-tls             | joined     |
      | standalone | --hub-legacy-bearer   | joined     |
      | joined     | --allow-public        | standalone |
      | joined     | --advertise           | standalone |
      | joined     | --label shed          | standalone |
      | joined     | --bind 0.0.0.0:8787   | standalone |

  Scenario: The standalone serve flags keep their defaults and posture
    Given a data directory initialized for standalone mode
    When the operator runs "roger tower serve --dir D" with no bind
    Then it binds 127.0.0.1:8787
    And a wildcard or public bind is refused without --allow-public, as before
    And a hostname bind is refused, as before

  Scenario: standalone serve now reads the YAML config like every other tower command
    # BEHAVIOR CHANGE: roger-tower-local ignored --config, so its bind could never come
    # from the operator's YAML.
    # DECISION: honor --config for standalone serve (bind, label, advertise), with flags
    # winning over the file. Recommended: yes.
    Given a standalone config file that sets the bind to 192.168.1.10:8787
    When the operator runs "roger tower serve --dir D --config C"
    Then it binds 192.168.1.10:8787
    And a --bind flag on the same command wins over the file

  Scenario: An uninitialized directory is refused with the init command named
    Given an empty directory D
    When the operator runs "roger tower serve --dir D"
    Then it exits non-zero
    And the message names "roger tower init --dir D --mode standalone|joined"

  Scenario Outline: Near-twin commands stay distinct and their help says which is which
    When the user runs "roger <top>" and "roger tower <sub>"
    Then each does only its own job
    And "roger help" describes "<top>" as "<top_help>"
    And "roger tower help" describes "<sub>" as "<sub_help>"

    Examples:
      | top    | sub    | top_help                                   | sub_help                                     |
      | status | status | this machine's roger mode and connection   | a Tower data directory's state               |
      | doctor | doctor | this machine's roger health (clock vs broker) | a Tower's readiness and clock (NTP)       |
      | resume | resume | re-open your last roger session            | lift a drain on a joined Tower               |
      | login  | login  | sign this machine in to RogerAI            | mark a Tower directory as signed in          |

  Scenario: roger tower with no subcommand prints the tower help, not an error
    When the user runs "roger tower"
    Then it prints the tower command list
    And it exits 0

  Scenario: The old "tower is a separate binary" error is gone
    When the user runs "roger tower status --dir D" on a standalone directory
    Then it prints the directory's status
    And it never says that tower is a separate binary

  Scenario: roger share --tower keeps its current explanation
    When the user runs "roger share --tower"
    Then the explanation now names "roger tower attach" instead of "roger-tower attach"

  # =====================================================================================
  # 2. TOWER LOGIN
  # =====================================================================================
  # DECISION: `roger tower login` and `roger tower logout` stay as commands but are THIN:
  # login runs the same device flow as `roger login` (it already signs with roger's own
  # user.key) and then writes the data-directory marker; logout removes only the marker and
  # never signs the machine out of roger. Recommended: keep both commands (operators'
  # scripts and the approved operator_login spec use them), implemented on roger login.

  Scenario: roger tower login is roger login plus the directory marker
    Given a joined data directory D and a machine not signed in to roger
    When the operator runs "roger tower login --dir D" and completes the device flow
    Then the machine is signed in to roger, the same as "roger login"
    And D carries the tower-account marker
    And no second key or credential file is created

  Scenario: roger tower login on a machine already signed in only writes the marker
    Given a machine signed in to roger
    When the operator runs "roger tower login --dir D"
    Then no device flow runs
    And D carries the tower-account marker

  Scenario: roger tower logout removes only the marker
    Given a machine signed in to roger and a directory D with the marker
    When the operator runs "roger tower logout --dir D"
    Then the marker is gone
    And "roger status" still shows the machine signed in

  Scenario: tower login on a standalone directory is refused
    Given a standalone data directory D
    When the operator runs "roger tower login --dir D"
    Then it exits non-zero
    And it says a standalone Tower has no RogerAI account by design

  # =====================================================================================
  # 3. THE OLD NAMES FOR ONE RELEASE
  # =====================================================================================
  # DECISION: the shims are tiny separate executables (not symlinks) that exec
  # `roger tower ...`. A symlink dispatching on argv[0] would make the shim depend on
  # being installed next to roger under a fixed name, which Windows and some package
  # managers do not preserve. Recommended: tiny exec shims, built only for the one release.

  Scenario Outline: An old name still works and says what replaced it
    Given roger and the "<old>" shim are installed
    When the operator runs "<old> <args>"
    Then "roger tower <new_args>" runs with the same arguments
    And stderr starts with one line: "<old> is now `<now>`; this name is removed in the next release"
    And the exit code is the exit code of "roger tower <new_args>"
    And stdout is exactly what "roger tower <new_args>" printed

    Examples:
      | old               | args                    | new_args                      | now               |
      | roger-tower       | status --dir D          | status --dir D                | roger tower       |
      | roger-tower       | serve --dir D --hub :1  | serve --dir D --hub :1        | roger tower       |
      | roger-tower       | version                 | version                       | roger tower       |
      | roger-tower-local | --dir D --bind A        | serve --dir D --bind A        | roger tower serve |
      | roger-tower-local | --dir D --advertise     | serve --dir D --advertise     | roger tower serve |
      | roger-tower-local | version                 | version                       | roger tower serve |

  Scenario: The deprecation notice never goes to stdout
    When the operator runs "roger-tower stations --dir D | wc -l"
    Then the count is the same as "roger tower stations --dir D | wc -l"

  Scenario: A shim that cannot find roger fails clearly
    Given the roger-tower shim is installed but roger is not on PATH nor beside the shim
    When the operator runs "roger-tower status --dir D"
    Then it exits 127
    And it says "roger is not installed; install roger, then run `roger tower status`"

  Scenario: A shim never loops
    Given a roger binary that is itself the roger-tower shim
    When the shim execs roger
    Then it detects the loop and exits non-zero instead of exec-ing itself again

  Scenario: A systemd unit that still runs roger-tower serve keeps working for the release
    Given the previous release's roger-tower.service unit
    When it starts after the upgrade
    Then the Tower serves as before
    And the journal shows the one-line deprecation notice

  # =====================================================================================
  # 4. THE CORE-FREE GUARANTEE, RE-EXPRESSED
  # =====================================================================================
  # Supersedes standalone_consumer_plane.feature lines 1-9 (header) and the scenario
  # "The consumer process cannot reach Roger Core, by linkage not by policy" (line 52),
  # and the Feature description's "the process that serves consumers does not link a
  # single line that can reach Roger Core". roger links towerhub and towercore/* today
  # (agent, station, edge), so "no code in this binary" is no longer true and is not
  # claimed anywhere.

  Scenario: The standalone serve package imports no Core code
    Given the package internal/localplane/serve
    When its full dependency graph is inspected
    Then it imports none of internal/towerjoin, internal/towercore and its subpackages, or internal/towerhub
    And it imports neither internal/client nor internal/agent
    And the check fails the build if any is added

  Scenario: The handler package keeps its no-egress source gate, and serve gets its own
    Given internal/localplane and internal/localplane/serve
    When the source-scan gates run
    Then internal/localplane forbids every outbound call and listener, as before
    And internal/localplane/serve forbids every outbound call (it may listen)

  Scenario: One helper backs every Core-free structural test
    Given the structural tests for localplane/serve, edgeauth and any later Core-free package
    When they run
    Then each calls the same shared helper with its forbidden list
    And the helper fails on a forbidden prefix, on a missing go toolchain, and on a suspiciously tiny dependency list

  Scenario: Standalone mode dials only local addresses
    Given roger tower serve running a standalone directory
    When any code in the process tries to connect to 93.184.216.34:443
    Then the connection is refused before a packet is sent
    And the refusal is logged with the destination and the words "standalone: non-local connection refused"
    And the serve keeps running

  Scenario: Standalone mode refuses a hostname that resolves to a public address
    Given roger tower serve running a standalone directory
    When any code in the process connects by a name that resolves to a public address
    Then the connection is refused using the resolved address, not the name

  Scenario Outline: The standalone guard allows only the local address classes
    Given roger tower serve running a standalone directory
    When the process connects to "<addr>"
    Then the connection is "<result>"

    Examples:
      | addr                 | result  |
      | 127.0.0.1:8787       | allowed |
      | [::1]:8787           | allowed |
      | 10.0.0.5:9000        | allowed |
      | 172.16.4.4:9000      | allowed |
      | 192.168.1.20:9000    | allowed |
      | [fd00::5]:9000       | allowed |
      | 169.254.10.10:9000   | allowed |
      | [fe80::1%eth0]:9000  | allowed |
      | 100.64.0.9:9000      | refused |
      | 8.8.8.8:53           | refused |
      | [2001:db8::1]:443    | refused |

  Scenario: A Core client constructed in standalone mode fails closed at startup
    Given a build in which standalone startup constructs a Core client (a bug)
    When roger tower serve starts on a standalone directory
    Then it exits non-zero before opening the consumer listener
    And the message says a Core client was constructed in standalone mode

  Scenario: The guard is the process's default transport, not an opt-in
    Given roger tower serve running a standalone directory
    When code uses http.DefaultClient, http.DefaultTransport or net.Dial directly
    Then those paths are refused too
    # The guard replaces the default dialer and transport at startup in standalone mode,
    # so a forgotten client cannot bypass it.

  Scenario: The guard does not apply to joined mode or to plain roger
    Given roger tower serve running a joined directory
    Then it may connect to Roger Core as before
    And "roger use", "roger share" and the TUI are unaffected by the standalone guard

  Scenario: Docs and help state the guarantee honestly
    When the user reads "roger tower serve --help" and the manual's standalone section
    Then they say the standalone serve package imports no Core code and standalone mode refuses every non-local connection
    And they never say the binary contains no code that can reach Roger Core

  # =====================================================================================
  # 5. PACKAGING
  # =====================================================================================
  # DECISION: `roger tower` on macOS and Windows. Tower code now ships in roger on every
  # platform. Recommended: standalone is SUPPORTED everywhere (it is plain Go and the plane
  # is local); joined mode refuses on macOS and Windows with "joined Towers run on Linux"
  # until a non-Linux joined build is tested, because the hub, the systemd unit and the
  # non-root runtime were only proven on Linux.

  Scenario: A release builds one user binary per platform
    When a release is built
    Then the archives contain roger for linux, darwin and windows on every built architecture
    And no roger-tower or roger-tower-local archive exists except the one-release shims
    And the checksum file, signature bundle, SBOM and provenance cover roger and the shims

  Scenario: The one-release shims ship only on Linux, where the old binaries shipped
    When a release is built
    Then roger-tower and roger-tower-local shims exist for linux only

  Scenario: Standalone tower works on macOS and Windows
    Given roger on macOS or Windows
    When the operator runs "roger tower init --mode standalone" and "roger tower serve"
    Then the standalone plane serves on the local network

  Scenario: Joined tower refuses on macOS and Windows with a clear message
    Given roger on macOS or Windows
    When the operator runs "roger tower init --mode joined"
    Then it exits non-zero
    And it says "joined Towers run on Linux"

  Scenario Outline: install.sh's tower components install roger
    When install.sh runs with ROGERAI_COMPONENT="<component>"
    Then roger is installed
    And for this release the "<shim>" shim is installed beside it
    And the script prints "use `roger tower ...`"

    Examples:
      | component  | shim              |
      | tower       | roger-tower       |
      | tower-local| roger-tower-local |

  Scenario: The systemd unit runs roger tower serve
    Given the packaged Tower unit
    Then its ExecStart runs "roger tower serve --dir ... --config ..."
    And it keeps the non-root user and the existing hardening

  Scenario: The tower runs as a non-root user (carried over)
    Given the host or OCI package is installed with defaults
    When "roger tower serve" starts
    Then its runtime user is non-root
    And private identity files remain readable only by that identity

  Scenario: Package managers ship roger as before
    Then the Homebrew, Scoop, winget and Gentoo packages install only roger, unchanged
    And their docs mention "roger tower" for operators

  Scenario: The version is stamped once and reported the same everywhere
    When a release is built
    Then one build-time variable stamps the version
    And "roger version", "roger tower version", "roger --version" and each shim's version report the same string

  # =====================================================================================
  # 6. SERVER SIDE: THE TOKENIZER BECOMES A BROKER MODE
  # =====================================================================================

  Scenario: rogerai-broker tokenizer serves the same contract as the sidecar
    When the operator runs "rogerai-broker tokenizer"
    Then it listens on 127.0.0.1:$TOKENIZER_PORT (default 9099)
    And POST /count with {"model","text"} answers {"tokens","exact","method"} as before
    And GET /health answers "ok"
    And TOKENIZER_DIR is honored as before

  Scenario: The broker's recount is unchanged
    Given TOKENIZER_URL points at "rogerai-broker tokenizer"
    When the broker recounts tokens
    Then the counts and the fail-open behavior are identical to the sidecar's

  Scenario: The tokenizer mode does not start the broker
    When the operator runs "rogerai-broker tokenizer"
    Then no database, shared store or HTTP broker listener is opened

  Scenario: The tokenizer stays off the broker's hot path
    Given a broker and its tokenizer running as two processes from the same binary
    Then a tokenizer outage never fails or delays a relay

  # =====================================================================================
  # 7. DRY: ONE HOME PER CONCERN
  # =====================================================================================

  # --- 7.1 one private-address rule (internal/netpriv) ---------------------------------

  Scenario Outline: One rule decides what is local, everywhere
    When "<site>" classifies "<addr>"
    Then the answer is "<class>"

    Examples:
      | site                         | addr            | class            |
      | tower bind                   | 192.168.1.10    | private          |
      | tower egress allow-list      | 192.168.1.10    | private          |
      | lan add                      | 192.168.1.10    | private          |
      | client nonce decision        | 192.168.1.10    | private          |
      | edge advert                  | 192.168.1.10    | private          |
      | edge listener                | 192.168.1.10    | private          |
      | tower advertise              | 192.168.1.10    | private          |
      | joined serve advertised addr | 192.168.1.10    | private          |
      | tower doctor                 | 127.0.0.1       | loopback         |
      | lan add                      | 169.254.3.3     | link-local       |
      | client nonce decision        | 169.254.3.3     | link-local       |
      | tower egress allow-list      | 169.254.3.3     | link-local       |
      | lan add                      | 100.64.0.9      | cgnat            |
      | tower bind                   | 8.8.8.8         | public           |

  Scenario: Every site that decides "local" calls the same function
    Given the source tree
    When a structural test searches for private-address CIDR tables and IsPrivate calls outside internal/netpriv
    Then it finds none, except protocol.TrustedBase's loopback-only key-trust rule, which is a different question and is named in the test's allow-list

  Scenario: tower/egress no longer keeps a copy of the private CIDR table
    Then internal/tower/egress.go uses internal/netpriv
    And its allow-list behavior is unchanged for loopback, RFC1918 and ULA

  Scenario: tower doctor's loopback check is no longer a string prefix
    # BEHAVIOR CHANGE: tower/doctor.go:143 matched loopback by string prefix.
    # DECISION: use the netpriv rule (any 127.0.0.0/8 and ::1, parsed). Recommended: yes.
    When doctor checks a config whose bind is "127.1.2.3:8787" or "[::1]:8787"
    Then both are reported as loopback

  Scenario: The joined serve's advertised-address tiers follow the one rule
    # BEHAVIOR CHANGE: roger-tower serve.go classifyAdvertised used stdlib IsPrivate, which
    # treats link-local differently from lanmode.
    # DECISION: link-local is "local, not routable" in both. Recommended: yes.
    When joined serve classifies an advertised fe80:: address
    Then it uses the same class as lan add

  Scenario: lanmode no longer drags localplane and tower into every client
    Given internal/lanmode
    When its dependency graph is inspected
    Then it does not import internal/localplane or internal/tower
    # It imported localplane only for IsPrivateAddr; netpriv is a leaf package.

  Scenario: netpriv is a leaf
    When internal/netpriv's dependency graph is inspected
    Then it imports only the standard library

  # --- 7.2 one Tower detection ---------------------------------------------------------

  Scenario: One function decides whether an address is a standalone Tower
    Given the old isLocalTowerBroker, lanmode.ProbeTower and the X-Roger-Local reader
    Then lanmode.IsTower(base) is the only probe
    And roger share, roger lan add, the dial, the TUI and console SHARE all call it

  Scenario: A broker setting that is a local Tower is folded into lan_towers
    # BEHAVIOR CHANGE: "point broker at a Tower" and "lan add" were two ways to name a Tower.
    # DECISION: on first run after the upgrade, a broker setting that IsTower recognizes is
    # copied into lan_towers (once, with a one-line note), and the broker setting is left as
    # is. Thereafter Tower discovery reads lan_towers only. Recommended: yes.
    Given a config whose broker is "http://192.168.1.10:8787", a standalone Tower
    When roger starts after the upgrade
    Then lan_towers contains "192.168.1.10:8787"
    And roger prints once "added 192.168.1.10:8787 to your LAN Towers (from your broker setting)"
    And roger share serves that Tower exactly as before

  Scenario: The probe result is cached briefly, the same for every caller
    When the TUI renders repeatedly and lan list runs in the same second
    Then the Tower is probed at most once per cache window

  # --- 7.3 one standalone share loop --------------------------------------------------

  Scenario: Every way of sharing to standalone Towers uses agent.StartLAN
    Given headless roger share to one Tower, roger --lan share to several, and TUI or console SHARE
    Then all of them run agent.StartLAN
    And the single-Tower case is StartLAN with one Tower
    And serveLocalTowerShare and shareLAN's own goroutine loop no longer exist

  Scenario: The share loop's behavior is unchanged by the merge
    When a share serves one or several Towers
    Then polls, backoff (doubling to 30s), the 401 attach hint and stop-on-cancel behave as today

  # --- 7.4 one bind helper (internal/netbind) -------------------------------------------

  Scenario Outline: Every listener binds through netbind with its posture
    When "<listener>" starts with bind "<bind>"
    Then the result is "<result>"

    Examples:
      | listener          | bind              | result                                          |
      | standalone serve  | 127.0.0.1:8787    | listens                                         |
      | standalone serve  | 0.0.0.0:8787      | refused without --allow-public                  |
      | edge face         | wildcard          | one listener per private interface, same port   |
      | web console       | 127.0.0.1:0       | listens on loopback, free port                  |
      | joined hub        | 0.0.0.0:8443      | listens, with the posture logged                |
      | tui proxy         | 127.0.0.1:0       | listens on loopback, free port                  |

  Scenario: The joined hub states its bind posture
    # BEHAVIOR CHANGE: the hub called net.Listen with no posture check at all.
    # DECISION: the hub keeps binding any address (a joined Tower is public by design), but
    # netbind logs "hub listening on <addr> (public|private|loopback)" at start, and refuses
    # a hostname bind like every other site. Recommended: yes; no refusal of public binds.
    When the joined hub starts on "0.0.0.0:8443"
    Then it listens
    And the log states the posture

  Scenario: One free-port helper replaces three
    Then the TUI, the web console and onboarding pick a free port through netbind
    And none of them closes a probe listener and binds the port again

  # --- 7.5 one key loader -------------------------------------------------------------

  Scenario: node.key and user.key are loaded by the same function
    Given agent.loadOrCreateKey and client.LoadOrCreateUserKey
    Then both call keyfile.LoadOrCreate(path)
    And files written before the upgrade load unchanged, with the same permissions (0600)

  Scenario: A damaged key file is refused the same way at every site
    Given a truncated key file
    When roger, roger share or roger tower loads it
    Then each refuses with the same message naming the path, and never overwrites it

  # --- 7.6 one nonce decision ---------------------------------------------------------

  Scenario Outline: Every signed request to a local Tower carries a nonce
    When roger or a share station signs a request to a Tower at "<addr>"
    Then the request carries a signed X-Roger-Nonce: "<nonce>"

    Examples:
      | addr                   | nonce |
      | 192.168.1.10:8787      | yes   |
      | [fd00::5]:8787         | yes   |
      | 169.254.3.3:8787       | yes   |
      | [fe80::1%eth0]:8787    | yes   |
      | 100.64.0.9:8787        | yes   |
      | 127.0.0.1:8787         | yes   |
      | broker.example:443     | no    |

  Scenario: The nonce decision lives in one function beside the address rule
    Then client.signWithKey and agent.signLocal both ask the same function whether to add a nonce

  Scenario: Loopback Towers now get a nonce too
    # BEHAVIOR CHANGE: the standalone spec said loopback "needs none" (no wire to eavesdrop),
    # and the station side always sent one.
    # DECISION: always send a nonce to any standalone Tower, loopback included. It costs
    # nothing, the Tower already accepts it, and one rule is simpler than two.
    # Recommended: yes.
    When roger signs a request to 127.0.0.1:8787
    Then it carries a nonce

  # --- 7.7 one local-receipt helper ---------------------------------------------------

  Scenario: Every free local answer is recorded by one helper
    Given a free answer from a standalone Tower, through the market path or the LAN ladder
    Then the receipt is written by client.localTowerReceipt
    And both paths produce the same fields, with LAN set only on the LAN ladder

  # --- 7.8 one version ----------------------------------------------------------------

  Scenario: One version variable for both binaries
    Then roger and rogerai-broker stamp their version from one build-time variable each, set by the release pipeline
    And the broker no longer hardcodes its version in source

  # --- 7.9 one live-share registry ----------------------------------------------------

  Scenario: lan-shares records live in the onair registry
    # DECISION: merge the lan-shares pid records into internal/onair (one pid-keyed registry
    # with one staleness rule), with the LAN fields (band, upstream, key, modality) carried
    # in an owner-only side record the registry owns. Recommended: merge.
    Given a share on air in another process
    Then the LAN relay, the Edge face and the TUI read it through internal/onair
    And a stale record (dead pid) is ignored and removed by one rule
    And the registry files stay 0600 in a 0700 directory

  Scenario: Records written before the upgrade are read once and migrated
    Given lan-shares/share-<pid>.json from the previous release
    When roger starts
    Then a live one is read; a stale one is removed

  # --- 7.10 every hint names the new commands -----------------------------------------

  Scenario Outline: Hints name roger tower
    When "<situation>"
    Then the hint names "<command>"
    And no output names roger-tower or roger-tower-local except the one-release shims' notice

    Examples:
      | situation                                         | command                                             |
      | a LAN Tower refuses this machine (not admitted)   | roger tower invite --client <keyhash> on the Tower  |
      | a station's poll gets the uniform 401             | roger tower attach --station ... --key ...          |
      | a joined login needs an invite                    | roger tower invite                                  |
      | the standalone serve starts                       | roger lan add <addr> on each client                 |
      | tower readiness fails                             | roger tower doctor                                  |

  Scenario: The not-admitted hint names the command the owner actually runs
    # Fixes internal/client/lan.go's "roger-tower admit --client" (admit also needs --id and
    # --code; the owner first runs invite).
    Given a LAN Tower that answers this machine with the uniform 401
    When the user runs roger --lan use against it
    Then the hint says the Tower's owner runs "roger tower invite --client <keyhash>" and gives the code to this machine

  Scenario: Shared usage helpers print the house style
    When any tower or lan command is given a bad flag
    Then it prints the same usage format as other roger commands and exits 2

  # =====================================================================================
  # 8. SPEC DRIFT FIXED BY THIS CHANGE (approved lines to supersede; see the report)
  # =====================================================================================

  Scenario: No approved scenario names a command that does not exist
    Given every features/ file
    When a structural test scans for "roger-tower", "roger-tower-local", "roger-tower release audit" and "roger-tower station invite"
    Then only this file and the one-release shim scenarios mention the old names
    And every scenario that names a tower command names one that "roger tower help" lists
