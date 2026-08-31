#!/usr/bin/env bash
# Prove the pre-push gate would ACTUALLY run, from wherever you are standing.
#
# The gate is two hooks in a chain. A global hook (core.hooksPath, shared by every repo on
# the machine) runs the claude-audit, and it explicitly invokes this repo's own hook first,
# because a global core.hooksPath silently overrides .git/hooks. Neither hook is in this
# repository, so nothing here notices when the chain breaks - and when it breaks, it breaks
# QUIETLY: the audit banner still prints, so a push looks gated while the coverage gate,
# the web suite and the clean-checkout build never ran at all.
#
# That is not hypothetical. On 2026-08-30 the global hook located this repo's hook with
# `git rev-parse --git-dir`, which inside a linked WORKTREE is .git/worktrees/<name> - a
# directory with no hooks/ in it. The chain was skipped for every push made from a
# worktree, which the working agreement asks for by default, so the real gate had been off
# for most work while still printing its banner.
#
# So: check the chain rather than trusting it. Everything here is read-only and fast - the
# heavy gates are stubbed out, because what is under test is whether they are REACHED.
#
#   make verify-gate     (also runs automatically after `make hooks`)
set -uo pipefail

ok=0; bad=0
pass() { printf '  \033[32mok\033[0m   %s\n' "$1"; ok=$((ok+1)); }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '       %s\n' "$2"; bad=$((bad+1)); }
skip() { printf '  --   %s\n' "$1"; }

echo "[verify-push-gate] checking the chain from $(pwd)"

# ---- 1. this repo's own hook is installed ------------------------------------------
hooks_root="$(git rev-parse --git-common-dir 2>/dev/null || echo .git)"
local_hook="$hooks_root/hooks/pre-push"
if [ -x "$local_hook" ]; then
	if cmp -s scripts/hooks/pre-push "$local_hook"; then
		pass "repo hook installed and matches scripts/hooks/pre-push"
	else
		fail "installed repo hook differs from its tracked source" "run: make hooks"
	fi
else
	fail "no repo hook at $local_hook" "run: make hooks"
fi

# ---- 2. the global hook actually reaches it ----------------------------------------
# The ONLY honest test is to run the chain and look for a line that only the repo hook
# can print. COVER_GATE_SKIP makes it say so and stop, so this costs nothing.
global_hooks="$(git config --get core.hooksPath 2>/dev/null || true)"
global_hook="${global_hooks:+$global_hooks/pre-push}"
if [ -z "${global_hook:-}" ] || [ ! -x "$global_hook" ]; then
	skip "no global pre-push hook configured (nothing to chain from)"
else
	head_sha="$(git rev-parse HEAD 2>/dev/null || echo)"
	base_sha="$(git rev-parse origin/main 2>/dev/null || git rev-parse "HEAD^" 2>/dev/null || echo)"
	if [ -z "$head_sha" ] || [ -z "$base_sha" ]; then
		skip "cannot resolve a push range to probe with"
	else
		out="$(printf 'refs/heads/probe %s refs/heads/main %s\n' "$head_sha" "$base_sha" \
			| COVER_GATE_SKIP=1 CLAUDE_AUDIT_SKIP=1 "$global_hook" origin probe 2>&1)"
		if printf '%s' "$out" | grep -q 'gates SKIPPED'; then
			pass "global hook chains to the repo hook (works from a worktree)"
		else
			fail "the global hook did NOT reach this repo's hook - the gate is OFF" \
			     "it must locate the hook with 'git rev-parse --git-common-dir', not --git-dir"
		fi
	fi
fi

# ---- 3. the audit is handed the right tree -----------------------------------------
# git EXPORTS GIT_DIR to its hooks. A hook that assigns to that name overwrites what every
# child inherits, and claude-audit then resolves HEAD in the wrong checkout - reviewing
# another branch's diff while reporting on yours.
if [ -n "${global_hook:-}" ] && [ -x "$global_hook" ] && [ -n "${head_sha:-}" ]; then
	probe_dir="$(mktemp -d)"
	cat > "$probe_dir/claude-audit" <<'PROBE'
#!/bin/sh
echo "AUDIT_SEES_HEAD=$(git rev-parse --short HEAD 2>/dev/null)"
exit 0
PROBE
	chmod +x "$probe_dir/claude-audit"
	want="$(git rev-parse --short HEAD)"
	seen="$(printf 'refs/heads/probe %s refs/heads/main %s\n' "$head_sha" "$base_sha" \
		| COVER_GATE_SKIP=1 PATH="$probe_dir:$PATH" "$global_hook" origin probe 2>&1 \
		| sed -n 's/^AUDIT_SEES_HEAD=//p' | head -1)"
	rm -rf "$probe_dir"
	if [ -z "$seen" ]; then
		skip "audit not reached: HEAD is already on the remote, so there is nothing to audit (this check runs when you have unpushed work)"
	elif [ "$seen" = "$want" ]; then
		pass "audit sees this tree's HEAD ($want)"
	else
		fail "audit sees HEAD=$seen but this tree is at $want" \
		     "the hook is clobbering the exported GIT_DIR; assign to a local name instead"
	fi
fi

# ---- 4. the connection survives a long gate ----------------------------------------
# git opens the SSH connection BEFORE running pre-push. The gate then runs for many minutes
# with no traffic on that socket, the far end drops it as idle, and the pack write lands on
# a dead connection: the audit says PASS and the push does not land. TCPKeepAlive does not
# help - the OS waits ~2h before its first probe.
if command -v ssh >/dev/null 2>&1; then
	interval="$(ssh -G github.com 2>/dev/null | sed -n 's/^serveraliveinterval //p' | head -1)"
	countmax="$(ssh -G github.com 2>/dev/null | sed -n 's/^serveralivecountmax //p' | head -1)"
	if [ -n "$interval" ] && [ "$interval" -gt 0 ] 2>/dev/null; then
		budget=$(( interval * ${countmax:-3} ))
		if [ "$budget" -ge 1500 ]; then
			pass "ssh keepalive to github.com: ${interval}s x ${countmax} = ${budget}s of gate"
		else
			fail "ssh keepalive only covers ${budget}s; the full gate runs longer" \
			     "raise ServerAliveInterval/ServerAliveCountMax for github.com in ~/.ssh/config"
		fi
	else
		fail "ssh keepalive to github.com is OFF (ServerAliveInterval=0)" \
		     "a gated push will SIGPIPE (141) after the audit passes, and will NOT land"
	fi
else
	skip "no ssh on PATH"
fi

echo "[verify-push-gate] $ok ok, $bad failed"
[ "$bad" -eq 0 ] || {
	echo "[verify-push-gate] THE GATE IS NOT FULLY ARMED - fix the above before trusting a push." >&2
	exit 1
}
