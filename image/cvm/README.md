# RogerAI confidential CVM image

A reproducible, single-purpose **AMD SEV-SNP** guest image that runs the RogerAI provider
node (`roger share --confidential`) plus a local model server, and nothing else — so its
**launch measurement** is stable, meaningful, and pinnable. See `docs/tee-runbook.md` for
the full operator runbook; this directory holds the image build + launch.

## Layout

- `flake.nix` — the reproducible build (direct boot: OVMF + kernel + initrd + dm-verity
  rootfs). `nix build .#cvm` → `result/{OVMF.fd,vmlinuz,initrd,rootfs.img,cmdline}`.
- `node-init.sh` — the in-guest payload (PID 1's job): start the model server, then
  `roger share --confidential`. Read-only rootfs, no shell, no SSH.
- `run-cvm.sh` — launch the built image on a real SEV-SNP host via QEMU (for the verify
  step: pull a quote and confirm `report.measurement` equals the precomputed digest).

## Build → measure → pin (summary)

```sh
nix build .#cvm
../../tee/measure.sh --ovmf result/OVMF.fd --kernel result/vmlinuz \
  --initrd result/initrd --append "$(cat result/cmdline)" --vcpus 4 --vcpu-type EPYC-v4
# commit the printed hex to ../../tee/measurements/current.hex, then point the broker's
# ROGERAI_TEE_MEASUREMENTS_FILE at it (docs/tee-runbook.md §5).
```

## Status

This is a **scaffold**. The `flake.nix` pins are marked `TODO` — fill in the exact OVMF
build, kernel version+config, and rootfs contents for your fleet, then run the
build→measure→verify loop on real SEV-SNP silicon. The drift gate
(`.github/workflows/tee-measurement.yml`) stays green until a real measurement is pinned.

> Reproducibility is the whole point: anyone must be able to rebuild this image and get
> the byte-identical artifacts → the same launch measurement → independently verify what a
> confidential node is running. Keep every input pinned by hash.
