#!/usr/bin/env bash
# node-init.sh - the in-guest payload (PID 1's job) for the RogerAI confidential CVM.
#
# Single purpose: bring up a local model server, then go on air as a confidential provider.
# The rootfs is read-only (dm-verity), there is no shell and no SSH, and runtime state lives
# in tmpfs. The node's Ed25519 key is generated INSIDE the guest on first boot and bound
# into the attestation quote's report_data - it never leaves the encrypted VM.
#
# `roger share --confidential` runs ConfidentialPreflight() first (needs /dev/sev-guest),
# then generates a real SEV-SNP quote at registration; the broker grants the ◆ badge only
# if this image's launch measurement is on its allowlist (docs/tee-runbook.md).
set -euo pipefail

# Broker + model come from the kernel cmdline (measured) with sane fallbacks.
BROKER="$(sed -n 's/.*roger\.broker=\([^ ]*\).*/\1/p' /proc/cmdline)"
BROKER="${BROKER:-https://broker.rogerai.fyi}"
MODEL="${ROGER_MODEL:-}"          # TODO: pin the served model for the fleet
UPSTREAM="${ROGER_UPSTREAM:-http://127.0.0.1:8000/v1}"

echo "rogerai-cvm: confidential node booting (broker=$BROKER)"

# 1) Local model server (vLLM shown; swap for your fleet's server). Must expose an
#    OpenAI-compatible endpoint at $UPSTREAM. Runs in the background; the box serves nothing
#    else.
# TODO: launch the real server, e.g.:
#   vllm serve "$MODEL" --host 127.0.0.1 --port 8000 &
#   wait-for "$UPSTREAM/models"

# 2) Go on air as a CONFIDENTIAL provider. --confidential makes registration produce a real
#    TEE quote; without /dev/sev-guest this aborts (so a misbuilt image fails loudly).
exec roger share \
  --confidential \
  --broker "$BROKER" \
  --upstream "$UPSTREAM" \
  ${MODEL:+--model "$MODEL"}
