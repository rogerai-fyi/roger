# RogerAI confidential tier — CVM image + measurement-pinning runbook

The `confidential ◆` tier lets a provider prove, with hardware attestation, that
inference runs inside a TEE whose memory the host cannot read. Today the backend is
**AMD SEV-SNP**. A node earns the badge ONLY after the broker cryptographically verifies
its quote against a **pinned launch-measurement allowlist**. This runbook is how an
operator stands that allowlist up and how a provider becomes eligible.

> Status: the verification + routing path is implemented and tested (see
> `features/trust/confidential_attestation.feature` and the Go tests it cites). What this
> runbook scaffolds — the reproducible image and the pinned measurement — is what makes
> the tier *available* to outside providers. Until a real measurement is pinned the tier
> is **fail-closed**: nobody is granted the badge.
>
> **Gated, data-center-only tier.** This is NOT a self-serve feature. A real confidential
> node needs BOTH an AMD EPYC Milan+ host with SEV-SNP **and** an H100-class confidential
> GPU — because the model runs in **VRAM**, which SEV-SNP does not protect, so a CPU TEE
> alone is not enough. No consumer CPU/GPU qualifies. Eligibility + the apply path:
> `docs/tee-eligibility.md` / https://rogerai.fyi/confidential. This runbook is for an
> approved operator standing the tier up.

---

## 0. What you're pinning

The SEV-SNP **launch measurement** is a 48-byte (96-hex) digest of the guest's initial
state before it executes: the OVMF firmware, and — for direct boot — the kernel, initrd,
and kernel cmdline, plus the per-vCPU initial CPU state (VMSA) and the guest policy. One
allowlist entry = one blessed `(image, config, vCPU-count)` tuple.

- **Gotcha:** the measurement depends on **vCPU count** and SEV policy/features. Fix the
  vCPU count for the program, or precompute and pin one measurement per supported count.

Where it lands in the code:

- Broker allowlist: `ROGERAI_TEE_MEASUREMENTS` (comma/newline hex) and/or
  `ROGERAI_TEE_MEASUREMENTS_FILE` (one hex/line, `#` comments) →
  `cmd/rogerai-broker/attest.go` `parseMeasurements` / `loadAttestRegistry`.
- Verification gates: `cmd/rogerai-broker/attest.go` `sevSNPVerifier.Verify`
  (signature chain → `report_data` binding → measurement allowlist → TCB floor).
- Empty allowlist ⇒ tier UNAVAILABLE (fail-closed).

---

## 1. Image architecture

A single-purpose, locked-down guest on an **EPYC SEV-SNP host with an H100-class
confidential GPU** (CC mode; the GPU is attested alongside the CPU TEE so VRAM is
protected too): measured boot → the RogerAI node
(`roger share --confidential`) → a local model server (vLLM / llama.cpp) → outbound to
the broker. Read-only **dm-verity** rootfs, no SSH, no shell, a minimal init
(`image/cvm/node-init.sh`) that just launches the server + `roger share`, ephemeral tmpfs
for runtime. The node's Ed25519 key is generated *inside* the guest and bound into
`report_data = SHA-512(pubkey ‖ nonce)`.

## 2. Reproducible build

Use **direct boot** (OVMF + kernel + initrd + cmdline) and a bit-reproducible builder
(**Nix**, scaffolded in `image/cvm/flake.nix`) so identical inputs always yield identical
artifacts. Pin: OVMF build+flags, kernel version+config (SEV guest drivers,
`/dev/sev-guest`), initramfs, the dm-verity root hash (baked into the cmdline). Output:
`OVMF.fd`, `vmlinuz`, `initrd`, `rootfs.img`, `verity-roothash`.

```sh
cd image/cvm
nix build .#cvm        # produces ./result/{OVMF.fd,vmlinuz,initrd,rootfs.img,cmdline}
```

## 3. Precompute the measurement (before any deploy)

```sh
tee/measure.sh \
  --ovmf  image/cvm/result/OVMF.fd \
  --kernel image/cvm/result/vmlinuz \
  --initrd image/cvm/result/initrd \
  --append "$(cat image/cvm/result/cmdline)" \
  --vcpus 4 --vcpu-type EPYC-v4
# -> prints the 96-hex launch digest (wraps sev-snp-measure)
```

Commit the result to `tee/measurements/current.hex` (one hex/line; keep one line per
supported vCPU count). That file IS the allowlist — config-as-code.

## 4. Verify against real silicon

Don't trust the math alone for go-live: launch the image on a real SEV-SNP host
(`image/cvm/run-cvm.sh`), pull a quote, and assert `report.measurement == precomputed`.
Then end-to-end against a staging broker that has the measurement pinned:

```sh
ROGER_BROKER=https://staging-broker roger share --confidential
# expect: "confidential: ◆ VERIFIED by the broker ..."
```

## 5. Pin into the broker (turn the tier on)

```sh
ROGERAI_TEE_MEASUREMENTS_FILE=/etc/rogerai/tee-measurements.hex  # = tee/measurements/current.hex
ROGERAI_TEE_REQUIRE=1                  # reject a failed claim, never silently downgrade
ROGERAI_TEE_CHECK_REVOCATION=1         # pull the AMD CRL, reject revoked VCEK/ASK
ROGERAI_TEE_MIN_BL_SPL / _TEE_SPL / _SNP_SPL / _UCODE_SPL   # TCB floor you build against
# defaults kept: ROGERAI_TEE_REATTEST=1h, ROGERAI_TEE_NONCE_TTL=5m
```

Broker log flips from `TEE: confidential tier UNAVAILABLE …` to
`TEE: confidential tier ON — N approved measurement(s) …`.

## 6. CI drift gate

`.github/workflows/tee-measurement.yml` rebuilds the image, recomputes the measurement
(`tee/verify-drift.sh`), and fails on any mismatch against the committed
`tee/measurements/current.hex`. That is the reproducibility + supply-chain guarantee: an
image change can't silently invalidate every provider's badge, and a measurement can't be
edited without a matching, reproducible image.

## 7. Rollover & revocation

- **New version:** add its line, deploy, let providers migrate; both verify during the
  overlap. After the re-attest TTL (≤ 1h) old verified status lapses — then remove the old
  line.
- **Emergency revoke:** delete the line → within ≤ re-attest TTL every node on that image
  loses `◆` and drops out of confidential routing (`reattestSweep` in `tunnel.go`).

## 8. Provider UX & the cloud caveat

Publish the image artifact + launch recipe + the expected measurement(s) so a provider can
self-verify before trusting you. Local diagnosis is built in: `roger share --confidential`
distinguishes **no `/dev/sev-guest`** (wrong hardware — aborts via `ErrNoTEEDevice`) from
**right hardware, unblessed image** (the broker grants standard and `roger share` prints
the "NOT granted … not on the broker's allowlist" warning).

- **Self-managed QEMU/KVM on bare-metal SEV-SNP** → you control OVMF/boot and can pin your
  own measurement. The clean path.
- **Managed cloud CVMs (Azure CC, GCP Confidential VM)** → the platform often controls
  OVMF/boot, so you frequently can't pre-compute and pin your own measurement. Decide
  whether the program is bare-metal-only or pins the cloud image's measurement (weaker
  guarantee).
