#!/usr/bin/env bash
# tee/measure.sh - compute the AMD SEV-SNP launch measurement for the RogerAI confidential
# CVM image, by wrapping `sev-snp-measure` (https://github.com/virtee/sev-snp-measure).
#
# The launch measurement is a 48-byte (96-hex) digest of the guest's initial state:
# OVMF + (direct boot) kernel/initrd/cmdline + per-vCPU VMSA + guest policy. It depends on
# vCPU count, so pass the same --vcpus you will boot with. The hex it prints is exactly an
# allowlist line for tee/measurements/current.hex and ROGERAI_TEE_MEASUREMENTS_FILE.
#
#   tee/measure.sh --ovmf OVMF.fd --kernel vmlinuz --initrd initrd \
#                  --append "<cmdline>" --vcpus 4 --vcpu-type EPYC-v4
set -euo pipefail

OVMF="" KERNEL="" INITRD="" APPEND="" VCPUS=4 VCPU_TYPE="EPYC-v4"
while [ $# -gt 0 ]; do
  case "$1" in
    --ovmf)      OVMF="$2"; shift 2 ;;
    --kernel)    KERNEL="$2"; shift 2 ;;
    --initrd)    INITRD="$2"; shift 2 ;;
    --append)    APPEND="$2"; shift 2 ;;
    --vcpus)     VCPUS="$2"; shift 2 ;;
    --vcpu-type) VCPU_TYPE="$2"; shift 2 ;;
    -h|--help)   sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if ! command -v sev-snp-measure >/dev/null 2>&1; then
  echo "tee/measure.sh: sev-snp-measure not found - install it:" >&2
  echo "  pip install sev-snp-measure   # or: pipx install sev-snp-measure" >&2
  exit 3
fi
[ -n "$OVMF" ]   || { echo "missing --ovmf" >&2; exit 2; }
[ -n "$KERNEL" ] || { echo "missing --kernel" >&2; exit 2; }
[ -n "$INITRD" ] || { echo "missing --initrd" >&2; exit 2; }

# --mode snp = SEV-SNP launch digest; --output-format hex prints just the digest.
exec sev-snp-measure --mode snp \
  --vcpus "$VCPUS" --vcpu-type "$VCPU_TYPE" \
  --ovmf "$OVMF" --kernel "$KERNEL" --initrd "$INITRD" \
  --append "$APPEND" \
  --output-format hex
