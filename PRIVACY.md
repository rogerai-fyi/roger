# RogerAI privacy - what's protected, and the honest limits

## The hard truth (say it plainly)
To run your prompt, **a model has to read it in cleartext on the machine doing inference.**
No routing trick changes that - whoever runs the GPU can, in principle, see what their GPU
processes. This is true of *every* third-party inference service (OpenAI included). Anyone who
claims "fully private on someone else's GPU" without confidential hardware is mistaken or lying.

So RogerAI's job is: (1) leak nothing *beyond* the GPU that must run the work, (2) make the
open tier economically/legally hostile to logging, and (3) offer a tier where the host
*cryptographically cannot* read your data.

## What RogerAI does today
1. **Encrypted transport, no inbound.** Everything is TLS/HTTPS; providers dial OUT (no open
   ports). Prompts are never in cleartext on the wire.
2. **The broker is content-blind.** It relays request bytes but **persists only token counts +
   hashes** in receipts - never prompt or response text. No prompt logging at the broker, ever.
3. **Identity pseudonymization (shipped).** Providers never receive your real identity - only a
   per-`(user, node)` pseudonym. A host can count repeat customers but cannot tie usage to a
   person, and two colluding hosts get *different* pseudonyms for the same user, so they can't
   re-identify you by joining logs.
4. **The node exposes only `/v1` inference.** A shared node serves chat completions and nothing
   else - never shell, files, or the owner's data, and never the consumer's data beyond the one
   request it must run.

## The real guarantee for sensitive work: the Confidential tier
Providers can run inference inside a **TEE / confidential VM** - NVIDIA Confidential Computing
(Hopper/Blackwell), AMD SEV-SNP, Intel TDX - where GPU/host memory is encrypted and **the
machine's owner cannot read it.** The node presents a **remote attestation** the broker
verifies before listing it as `confidential ◆`. Privacy-sensitive users (and bots handling
PII/tax/financial data) set a filter to **route only to attested confidential nodes.** This is
the cryptographic answer to "the owner could spy," and a real product differentiator.

## The open tier: deterrence, not magic
The cheapest providers run on ordinary GPUs where the host *technically could* log. We make that
a bad idea, not an impossible one:
- **Signed no-log ToS** bound to the node's Ed25519 identity (the same key = portable, *losable*
  reputation).
- **Stake / escrow** that is **slashed** on a proven violation; honeypot/canary prompts detect
  loggers.
- **Pseudonymity** (above) means even a logging host captures content with no identity attached.

## What the user controls (privacy as a routing constraint)
In the CLI/criteria you choose your floor:
- **Open** - cheapest; trust = ToS + stake + reputation + pseudonymity. *Don't send secrets here.*
- **Confidential ◆** - TEE-attested; the host cannot read your data.
- **Private** - route only to your own nodes or a trust-listed friend's; or use the local tier.

## Roadmap
- Attestation verification + `confidential ◆` badge and a `--confidential` route filter.
- Per-request `no-store` flag echoed into the signed receipt (auditable promise).
- Canary-prompt auditing service + automated slashing.
- Optional client→enclave encryption envelope for confidential nodes (broker stays content-blind).

**Positioning:** be transparent. "Open tier hosts technically could log - we deter it and strip
your identity; for true privacy, use Confidential or your own nodes." Honesty is the trust moat.
