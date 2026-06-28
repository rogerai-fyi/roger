# Confidential ◆ tier — hardware eligibility

The confidential tier is a **gated, data-center-only** add-on. This page is the honest
answer to "can I run a confidential node?" — and for almost everyone running a home GPU,
the answer is no, by physics, not policy. The **standard** tier is the path for everyone
else, and it still earns the same way with verifiable, co-signed lineage receipts.

> Apply (if you qualify): **https://rogerai.fyi/confidential**
> Operator setup (once approved): `docs/tee-runbook.md`

## What "confidential" actually requires

A real confidential node has to protect the data *where it is processed*. For LLM serving
that means **two** TEEs, not one:

1. **CPU TEE — AMD SEV-SNP.** Encrypts + integrity-protects VM memory so the host can't
   read it, and produces the VCEK-signed attestation quote the broker verifies.
   - Requires **AMD EPYC Milan (7003) or newer** (Genoa, Turin). Server silicon.
2. **Confidential GPU — NVIDIA Hopper+ (H100/H200) in CC mode.** This is the part people
   miss: the model weights and KV-cache live in **VRAM**, which SEV-SNP does **not**
   protect. Without a confidential GPU, the host can read the GPU memory and the
   "confidential" guarantee is hollow. Confidential GPU compute exists only on
   **H100-class (and newer) data-center GPUs**.

Both are data-center parts. There is currently **no consumer path to attested confidential
GPU inference** — from anyone.

## Hardware that does NOT qualify (and why)

| Hardware | Confidential? | Why |
|---|---|---|
| Ryzen / Ryzen PRO | ❌ | No SEV-SNP (consumer silicon) |
| Threadripper (3000/7000) | ❌ | No SEV at all — server/workstation feature, fused off |
| Threadripper PRO (5000WX/7000WX) | ❌ | SEV/SEV-ES at best; **not SEV-SNP**, which the quote needs |
| Any consumer GPU (RTX, etc.) | ❌ | No confidential-compute mode — VRAM is readable by the host |
| Intel consumer (SGX/TDX) | ❌ | SGX deprecated on consumer + too small for LLMs; TDX is Xeon-only |
| AMD EPYC Milan+ **+** NVIDIA H100 CC | ✅ | The supported combination |

## If you don't qualify — use the standard tier

`roger share` (no `--confidential`) is the default and earns identically. RogerAI's
consumer-grade trust guarantee is **not** "the host can't see your data" — it's
**verifiable, attributable serving** via per-request **co-signed lineage receipts** (signed
by the serving node, counter-signed by the broker, hash-chained). That is the honest
guarantee a home GPU can actually make. See `features/trust/lineage_receipts.feature`.

## If you do qualify

`roger share --confidential` runs a local preflight (needs `/dev/sev-guest`), generates a
real SEV-SNP quote at registration, and the broker grants the ◆ badge only if your image's
launch measurement is on its pinned allowlist. The cloud caveat still applies: a managed
confidential VM that controls its own firmware may not let you pin your own measurement —
bare-metal EPYC (or a cloud that accepts custom firmware) is the clean path. Operator
runbook: `docs/tee-runbook.md`.
