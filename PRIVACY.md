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
1. **Encrypted transport, no inbound.** Every consumer-facing surface is TLS/HTTPS, and providers
   dial OUT (no open ports), so your prompt is never in cleartext on the wire. **One internal link
   is not yet TLS, and it is not the one carrying your prompt.** When a node serves through a
   *relay* (below), it long-polls that relay's hub over plain HTTP, because the endpoint format
   relays advertise cannot currently express a scheme. What rides in the clear there is the
   *node's own polling credential*, not your content: the job and its answer are sealed
   end-to-end to keys the relay does not hold, so the relay - and anything on the path - carries
   ciphertext either way. The exposure is the node operator's, not yours, and the node says so out
   loud when it joins. This is a known defect with a fix in design, not an accepted design.
2. **The broker is content-blind.** It relays request bytes but **persists only token counts +
   hashes** in receipts - never prompt or response text. No prompt logging at the broker, ever.
   The one honest exception is a **transient, pre-dispatch safety screen**, and it applies to
   **the broker-relayed path only**. See item 2b for the path where it does not.

   On the relayed path, before a prompt is dispatched its text is checked against a moderation
   model (today **Groq Llama Guard**) so we can refuse content we do not allow on the network (see
   the AUP in the ToS). That screen runs *in transit* - the prompt is not stored for it - but it
   does mean the text is read by the screen at dispatch time and sent to **Groq, a third-party
   processor**, for the check. The same applies to the public **Ping** site concierge: a Ping
   message may be served by a free on-air community station or fall back to **Groq**. Treat Ping
   as a public, unauthenticated demo, not a private channel. Disclosed in the site privacy policy
   and ToS; no contradiction with content-blindness - we do not *store* or *read for the
   marketplace*, the screen is a narrow in-transit exception.

2b. **Relayed-through-a-Tower traffic is NOT pre-screened, and we will not pretend otherwise.**
   A second serving path exists: a request can travel from you to the serving node through a
   *relay* (a "Tower"), which may be run by us or by another operator. On that path the broker
   never sees your prompt at all - it issues an authorization naming a model and some ceilings,
   and the prompt and the completion go around it, encrypted end-to-end to the node. That is
   better for confidentiality and it has a cost we are naming rather than burying: **there is
   nothing to screen, so nothing is screened before dispatch.** Policy enforcement on that path is
   entirely **post-hoc, on a sampled fraction**, from signed transcripts the node retains. Content
   we do not allow can therefore be served and only found afterwards.

   This used to be an opt-in plane a provider had to select with a flag. It is now the default:
   any signed-in `roger share` offers itself to the relay fabric, and a consumer using the relay
   API reaches it. So the honest statement is: **a prompt is pre-screened when it is dispatched by
   the broker, and not when it is relayed around it.** (Same rule, stated for operators, in
   `docs/tower.md` §8: no user-facing surface may describe edge traffic as pre-screened.)
3. **Identity pseudonymization (shipped).** Providers never receive your real identity - only a
   per-`(user, node)` pseudonym. A host can count repeat customers but cannot tie usage to a
   person, and two colluding hosts get *different* pseudonyms for the same user, so they can't
   re-identify you by joining logs.
4. **The node exposes only `/v1` inference.** A shared node serves chat completions and nothing
   else - never shell, files, or the owner's data, and never the consumer's data beyond the one
   request it must run.

## The real guarantee for sensitive work: the Confidential tier
Providers can run inference inside a **TEE / confidential VM** where host memory is encrypted and
**the machine's owner cannot read it.** The node generates a **hardware remote-attestation quote**
that the broker **cryptographically verifies** before listing it as `confidential ◆`. Verification
has three gates, ALL of which must pass: (1) the quote's signature chains to the silicon vendor's
root, (2) the quote's `report_data` binds the node's key to a fresh broker-issued nonce (so a quote
cannot be replayed by another node or reused once stale), and (3) the launch measurement is on a
pinned allowlist of approved RogerAI serving stacks. The broker re-checks attestation on a cadence
and drops the badge if it lapses. **Implemented today for AMD SEV-SNP** (via
github.com/google/go-sev-guest); Intel TDX and NVIDIA Confidential Computing GPU attestation are
pluggable backends on the same interface. A node with no TEE produces no quote, makes no claim, and
is plainly "standard" - the `◆` is never granted without a verified quote. Privacy-sensitive users
(and bots handling PII/tax/financial data) set a filter to **route only to verified confidential
nodes.** This is the cryptographic answer to "the owner could spy."

Honest status: the SEV-SNP verification pipeline is wired and unit-tested against synthetic signed
quotes (valid quote verifies; forged signature, replayed/rebound quote, non-allowlisted measurement,
and lapsed re-attestation are all rejected). It still needs validation against real SEV-SNP hardware
and an independent security review before it is leaned on for high-stakes secrets, and the
measurement allowlist must be populated with the real, reproducibly-built RogerAI stack measurements.

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
- Validate the SEV-SNP attestation pipeline on real confidential-VM hardware + an external security review.
- Additional TEE backends on the same verifier interface: Intel TDX, NVIDIA Confidential Computing GPU attestation.
- Per-request `no-store` flag echoed into the signed receipt (auditable promise).
- Canary-prompt auditing service + automated slashing.
- Optional client→enclave encryption envelope for confidential nodes (broker stays content-blind).

**Positioning:** be transparent. "Open tier hosts technically could log - we deter it and strip
your identity; for true privacy, use Confidential or your own nodes." Honesty is the trust moat.
