#!/usr/bin/env bash
# run-cvm.sh - launch the built RogerAI confidential image on a REAL AMD SEV-SNP host via
# QEMU, for the verify step (docs/tee-runbook.md §4): boot it, pull a quote, and confirm
# report.measurement equals the digest tee/measure.sh precomputed for the SAME --vcpus.
#
# Requires: an EPYC SEV-SNP host, a SEV-SNP-capable QEMU + the built artifacts under
# ./result. This is NOT for the hot path (providers run the image directly); it is the
# operator's one-time "does the silicon agree with the math" check.
set -euo pipefail

OUT="${1:-./result}"
VCPUS="${TEE_VCPUS:-4}"
MEM="${TEE_MEM:-16384}"   # MiB

for f in OVMF.fd vmlinuz initrd rootfs.img cmdline; do
  [ -f "$OUT/$f" ] || { echo "missing $OUT/$f - run: nix build .#cvm" >&2; exit 1; }
done

# IMPORTANT: vCPU count is part of the launch measurement - it MUST match the --vcpus you
# passed to tee/measure.sh, or the digest will (correctly) differ.
exec qemu-system-x86_64 \
  -enable-kvm -cpu EPYC-v4 -smp "$VCPUS" -m "$MEM" \
  -machine q35,confidential-guest-support=sev0,memory-backend=ram1 \
  -object memory-backend-memfd,id=ram1,size="${MEM}M",share=true,prealloc=false \
  -object sev-snp-guest,id=sev0,cbitpos=51,reduced-phys-bits=1 \
  -bios "$OUT/OVMF.fd" \
  -kernel "$OUT/vmlinuz" -initrd "$OUT/initrd" \
  -append "$(cat "$OUT/cmdline")" \
  -drive file="$OUT/rootfs.img",if=virtio,format=raw,readonly=on \
  -nographic -serial mon:stdio
