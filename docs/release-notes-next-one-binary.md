# Draft - one program: `roger tower` replaces the Tower binaries

Draft. Not yet numbered; fold into the next release's notes when it is cut.

## One download

`roger` is the only program. A Tower operator runs `roger tower ...`: the same commands,
flags and behavior `roger-tower` had (`init`, `config`, `doctor`, `ready`, `status`, `invite`,
`admit`, `attach`, `stations`, `route`, `serve`, `login`, `logout`, `register`, `earnings`,
`probe`, `station`, `drain`, `resume`, `revoke`, `version`). `roger tower help` lists them.

`roger tower serve` serves what the data directory's mode says. On a standalone directory it
runs the consumer plane `roger-tower-local` used to run, with the same flags (`--bind`,
`--allow-public`, `--advertise`, `--label`), and it now also reads `--config`
(`standalone.bind`, `standalone.label`, `standalone.advertise`, and a configured durable store,
so the plane reads what `admit` and `attach` wrote). `--check` reports what would be served
without listening. On a joined directory it runs the link and the sealed hub. A flag that belongs
to the other mode is refused with exit 2, and with only `--config` the directory is the
config's `identity.dir`.

A standalone Tower now runs on macOS and Windows too. Joined Towers stay on Linux, where the
hub, the systemd unit and the non-root runtime were proven.

## The old names, for one release

`roger-tower` and `roger-tower-local` still ship on Linux, as tiny stand-ins: each prints one
line on stderr ("roger-tower is now roger tower; this name is removed in the next release")
and hands the process to `roger tower`, so output, exit codes and a supervisor's signals are
roger's own. A unit or script that runs the old names keeps working; the next release removes
them. `install.sh` with `ROGERAI_COMPONENT=tower` or `=tower-local` installs roger and, on
Linux, the matching stand-in. The packaged systemd unit runs `roger tower serve --dir ...
--config ...`.

## The standalone guarantee, restated

The promise that a standalone Tower cannot reach Roger Core used to rest on a separate binary
that did not contain Core code. It now rests on two things, stated as exactly that: the
standalone serve package imports no Core code (a dependency test enforces it), and in
standalone mode the process refuses every non-local connection through its default HTTP
client, transport and resolver, and refuses to start if a Core client was built.

## Smaller

- `roger tower login` reuses this machine's `roger login`; a standalone directory refuses it.
- `roger --version` joins `roger version`; every binary reports one stamped version.
- A bad `roger tower` or `roger lan` flag is a usage error (exit 2) naming the flag and the
  command's usage line, like the rest of roger.
- Hints name the commands that exist: the not-admitted LAN hint says the Tower's owner runs
  `roger tower invite --client <id>` and gives the code to this machine; `roger tower ready`
  points at `roger tower doctor`.
