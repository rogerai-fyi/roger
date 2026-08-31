# The push gate, and the two ways it has gone quiet

`git push` here runs a gate: the full coverage run, the web suite, a clean-checkout build,
and an independent Claude audit. It is worth having - it has caught real regressions at the
last minute, including several introduced by the very changes that were fixing something
else.

It is also, twice now, been **off without saying so**. Both times the banner still printed,
so a push looked gated while nothing was checked. This page is how to tell.

```sh
make verify-gate     # proves the chain end to end; also runs after `make hooks`
```

## How the chain is wired

Two hooks, and neither is in this repository:

1. **`~/.config/git/hooks/pre-push`** - machine-level, shared by every repo, set by
   `git config --global core.hooksPath`. It runs the Claude audit. Because a global
   `core.hooksPath` *overrides* `.git/hooks`, it must explicitly invoke this repo's hook
   first, and it does.
2. **`.git/hooks/pre-push`** - this repo's gate. Not version controlled, so it is installed
   from its tracked source with `make hooks`. Source: `scripts/hooks/pre-push`.

Nothing inside the repo notices when that chain breaks, which is why the verifier exists.

## Failure 1: the gate never ran from a worktree

The global hook located this repo's hook with `git rev-parse --git-dir`. Inside a linked
worktree that is `.git/worktrees/<name>`, which has no `hooks/` directory - so the test for
an executable hook failed and the chain was skipped in silence. No coverage gate, no web
suite, no clean-checkout build; only the audit ran.

The working agreement asks for one worktree per session, so this was the normal case. The
gate had been off for most work while still printing its banner.

**Fix:** locate it with `git rev-parse --git-common-dir`, and assign the result to a
*local* variable name. Not `GIT_DIR`: git EXPORTS `GIT_DIR` to its hooks, and overwriting it
hands the auditor the wrong tree - it reviewed another branch's diff and reported a phantom
1513-file change for a one-file push.

## Failure 2: the push SIGPIPEd after everything passed

git opens the SSH connection to the remote *before* running the hook. The gate then runs
for fifteen to twenty-five minutes with no traffic on that socket, the far end drops it as
idle, and the pack write lands on a dead connection. Exit 141. Every gate reports success
and the push does not land, which is the worst failure shape available: it looks like a
green ship.

`TCPKeepAlive yes` does not cover it - the OS waits about two hours before its first probe.

**Fix**, in `~/.ssh/config`:

```
Host github.com
    ServerAliveInterval 30
    ServerAliveCountMax 120
```

`scripts/hooks/pre-push` now checks this itself and refuses to start the long gate on a
connection that will not survive it, so the cost is one second instead of twenty minutes.
It only applies to ssh remotes: an https push re-authenticates per request and holds no
socket.

## If a push fails

Read the verdict before reaching for `--no-verify`. The audit blocks on `NEEDS_WORK`, and
in this repo it has been right about it. `--no-verify` skips the coverage gate and the web
suite as well as the audit, so it is not "skip the opinion" - it is "skip everything".

And whatever the output said, confirm the push actually landed:

```sh
git fetch -q origin && git merge-base --is-ancestor HEAD origin/main && echo LANDED
```

Both failures above printed success. Neither had shipped anything.
