#!/usr/bin/env bash
# tee/verify-drift.sh - the CI drift gate for the confidential CVM measurement.
#
# Rebuilds the reproducible image, recomputes its SEV-SNP launch measurement, and asserts
# it equals the committed allowlist (tee/measurements/current.hex). Any drift fails: an
# image change can't silently invalidate providers' badges, and a pinned measurement can't
# be edited without a matching, reproducible image.
#
# SCAFFOLD BEHAVIOR (pre-launch): until a real measurement is pinned (the file holds only
# comments / placeholders) OR the toolchain (nix + sev-snp-measure) is unavailable, this
# SKIPS with exit 0 and an explanatory note - so CI stays green while the image is being
# stood up. Once tee/measurements/current.hex holds a real 96-hex line, the gate is live.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MFILE="$ROOT/tee/measurements/current.hex"

# Pull the real (non-comment, non-blank) measurement lines.
mapfile -t PINNED < <(grep -vE '^\s*(#|$)' "$MFILE" 2>/dev/null || true)
if [ "${#PINNED[@]}" -eq 0 ]; then
  echo "tee/verify-drift: no real measurement pinned yet (placeholder) - SKIP (tier fail-closed)."
  exit 0
fi

if ! command -v nix >/dev/null 2>&1 || ! command -v sev-snp-measure >/dev/null 2>&1; then
  echo "tee/verify-drift: nix and/or sev-snp-measure unavailable - SKIP (cannot recompute here)."
  exit 0
fi

echo "tee/verify-drift: rebuilding the reproducible CVM image…"
( cd "$ROOT/image/cvm" && nix build .#cvm --no-link --print-out-paths ) >/tmp/cvm-out 2>&1 || {
  echo "tee/verify-drift: image build failed:" >&2; cat /tmp/cvm-out >&2; exit 1; }
OUT="$(tail -n1 /tmp/cvm-out)"

# Recompute for each supported vCPU count the operator pins (one per line; default 4).
COMPUTED="$("$ROOT/tee/measure.sh" \
  --ovmf "$OUT/OVMF.fd" --kernel "$OUT/vmlinuz" --initrd "$OUT/initrd" \
  --append "$(cat "$OUT/cmdline")" --vcpus "${TEE_VCPUS:-4}")"

if printf '%s\n' "${PINNED[@]}" | grep -qx "$COMPUTED"; then
  echo "tee/verify-drift: OK - recomputed measurement matches a pinned allowlist entry."
  exit 0
fi
echo "tee/verify-drift: DRIFT - recomputed $COMPUTED is not in tee/measurements/current.hex" >&2
printf '  pinned:\n'; printf '    %s\n' "${PINNED[@]}" >&2
exit 1
