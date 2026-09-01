#!/usr/bin/env bash
# =====================================================================
# RogerAI on rented hardware - paste this into Vast.ai's ONSTART box:
#
#   curl -fsSL https://rogerai.fm/vast-onstart.sh | bash
#
#   Rent a GPU  ->  serve an open model on it  ->  put it on the band.
#
# It installs `roger`, brings up vLLM on a loopback port, waits for the
# model to actually load, and hands that endpoint to `roger share`.
# Everything is env-configurable and it is FREE by default, because a
# free share needs no account and therefore no secret on a machine you
# do not own.
#
#   ROGER_MODEL       HF model id to serve      (default Qwen/Qwen3-8B)
#   ROGER_PORT        loopback port for vLLM    (default 8000)
#   ROGER_PRICE_OUT   $/1M output tokens        (default 0 = free)
#   ROGER_PRICE_IN    $/1M input tokens         (default 0 = free)
#   ROGER_NODE        station callsign          (default: auto)
#   ROGER_MAX_LEN     vLLM --max-model-len      (default 8192)
#   ROGER_GPU_UTIL    vLLM memory fraction      (default 0.90)
#   ROGER_HF_TOKEN    HuggingFace token for GATED models (never logged)
#   ROGER_WAIT_SECS   how long to wait for load (default 900)
#   ROGER_SKIP_GPU_CHECK=1  serve anyway on a box with no visible NVIDIA GPU
#   ROGER_DRY_RUN=1   print the plan and do nothing
#
# EARNING needs a signed-in owner. `roger login` is a DEVICE FLOW: it
# prints a code, you approve it in the browser on your own laptop, and
# the rented box never holds your GitHub token. Do that once in the
# instance shell BEFORE setting a price. This script refuses to price a
# share on a box with no owner rather than quietly going on air free -
# rented hardware bills by the hour either way.
# =====================================================================
set -euo pipefail

MODEL="${ROGER_MODEL:-Qwen/Qwen3-8B}"
PORT="${ROGER_PORT:-8000}"
PRICE_IN="${ROGER_PRICE_IN:-0}"
PRICE_OUT="${ROGER_PRICE_OUT:-0}"
NODE="${ROGER_NODE:-}"
MAX_LEN="${ROGER_MAX_LEN:-8192}"
GPU_UTIL="${ROGER_GPU_UTIL:-0.90}"
WAIT_SECS="${ROGER_WAIT_SECS:-900}"
# HF_TOKEN is the name the HuggingFace libraries already read, so accept either and let an
# existing environment win nothing over an explicit setting.
HF_TOKEN_IN="${ROGER_HF_TOKEN:-${HF_TOKEN:-}}"
DRY="${ROGER_DRY_RUN:-0}"

CONF_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/rogerai"
AUTH="$CONF_DIR/auth.json"
UPSTREAM="http://127.0.0.1:${PORT}/v1"
LOG="${ROGER_LOG_DIR:-/var/log}/roger-vllm.log"
[ -w "$(dirname "$LOG")" ] 2>/dev/null || LOG="/tmp/roger-vllm.log"

say() { printf '[roger] %s\n' "$*"; }
die() { printf '[roger] %s\n' "$*" >&2; exit 1; }

# a price is anything that is not zero, in float terms
priced() { awk -v a="$PRICE_IN" -v b="$PRICE_OUT" 'BEGIN{exit !(a+0>0 || b+0>0)}'; }

# ---- the share command, assembled once so the plan and the run agree ----
SHARE_ARGS=(share --upstream "$UPSTREAM" --model "$MODEL")
if priced; then
  # only pass what was actually asked for: a 0 here would still be "a price"
  awk -v a="$PRICE_IN"  'BEGIN{exit !(a+0>0)}' && SHARE_ARGS+=(--price-in "$PRICE_IN")
  awk -v b="$PRICE_OUT" 'BEGIN{exit !(b+0>0)}' && SHARE_ARGS+=(--price-out "$PRICE_OUT")
fi
[ -n "$NODE" ] && SHARE_ARGS+=(--node "$NODE")

# What the box actually has, BEFORE anything is downloaded. vLLM will discover a missing
# GPU too - several gigabytes and several billed minutes later, with a stack trace.
GPU_DESC="none visible (nvidia-smi not found)"
GPU_OK=0
if command -v nvidia-smi >/dev/null 2>&1; then
  if GPU_LIST="$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null)" && [ -n "$GPU_LIST" ]; then
    # `paste -sd', '` CYCLES the two delimiters rather than using both, which produced
    # "A,B C,D". Count them instead - a rented box is usually N of one card, and "4x <name>"
    # is what an operator actually wants to read.
    GPU_N="$(printf '%s\n' "$GPU_LIST" | grep -c .)"
    GPU_DESC="$(printf '%s\n' "$GPU_LIST" | sort -u | sed "s/^/  /" | paste -sd';' - | sed 's/^  //; s/;  */; /g')"
    GPU_DESC="${GPU_N}x ${GPU_DESC}"
    GPU_OK=1
  else
    GPU_DESC="nvidia-smi present but reports no GPU"
  fi
fi

say "model    $MODEL"
say "upstream $UPSTREAM"
say "gpu      $GPU_DESC"
# The VALUE is never printed - this script's stdout is the instance console on a machine
# somebody else administers, and a token in a log is a leaked token.
if [ -n "$HF_TOKEN_IN" ]; then
  say "hf token set (Hugging Face; value not shown)"
else
  say "hf token none - fine for open weights, but a GATED model will 401 on download"
fi
if priced; then
  say "pricing  in=\$$PRICE_IN out=\$$PRICE_OUT per 1M tokens"
else
  say "pricing  free (no account needed)"
fi

# ---- the money guard, before anything is installed or downloaded -------
# A priced share on an unowned box is refused here rather than 403'd by the broker ten
# minutes and one model download later, and it is NEVER downgraded to a free share: the
# operator asked to earn, and the instance is billing by the hour regardless.
if priced && [ ! -f "$AUTH" ]; then
  cat >&2 <<MSG
[roger] refusing to price this share: no signed-in owner on this box.

  Earning is attributed to a GitHub-linked owner, and this machine has none
  ($AUTH is missing).

  Fix it once, from the instance shell:

      roger login

  That is a device flow - it prints a code, you approve it in a browser on
  YOUR machine, and this rented box never sees your GitHub token. Then
  re-run this script.

  Or drop ROGER_PRICE_IN / ROGER_PRICE_OUT to share for free, which needs
  no account at all.
MSG
  exit 2
fi

# Only a problem if WE are the ones about to start a server. If something is already
# serving the port, whatever it runs on is its own business.
if [ "$GPU_OK" != 1 ] && [ "${ROGER_SKIP_GPU_CHECK:-0}" != "1" ]; then
  if ! curl -fsS --max-time 3 "$UPSTREAM/models" >/dev/null 2>&1; then
    cat >&2 <<MSG
[roger] no GPU visible on this box: $GPU_DESC

  vLLM would find this out too, after downloading the weights and several billed
  minutes of an instance that cannot serve. Stopping first instead.

  If the instance does have a GPU, the container probably was not started with it
  attached. If you meant to serve on something else - a non-NVIDIA accelerator, or a
  server you will start yourself - re-run with:

      ROGER_SKIP_GPU_CHECK=1
MSG
    exit 3
  fi
fi

if [ "$DRY" = "1" ]; then
  say "dry run: nothing will be installed, downloaded or served."
  say "would serve : vllm serve $MODEL --host 127.0.0.1 --port $PORT --max-model-len $MAX_LEN --gpu-memory-utilization $GPU_UTIL"
  say "would share : roger ${SHARE_ARGS[*]}"
  exit 0
fi

# ---- 1. the client -----------------------------------------------------
if ! command -v roger >/dev/null 2>&1; then
  export PATH="$HOME/.local/bin:$PATH"
fi
if ! command -v roger >/dev/null 2>&1; then
  say "installing roger"
  curl -fsSL https://rogerai.fm/install.sh | sh
  export PATH="$HOME/.local/bin:$PATH"
fi
command -v roger >/dev/null 2>&1 || die "roger is still not on PATH after install."

# ---- 2. the model server ----------------------------------------------
if curl -fsS --max-time 3 "$UPSTREAM/models" >/dev/null 2>&1; then
  say "something already serves $UPSTREAM - using it"
elif command -v vllm >/dev/null 2>&1; then
  say "starting vllm on 127.0.0.1:$PORT (log: $LOG)"
  # exported for the child only, and never echoed
  [ -n "$HF_TOKEN_IN" ] && export HF_TOKEN="$HF_TOKEN_IN" HUGGING_FACE_HUB_TOKEN="$HF_TOKEN_IN"
  nohup vllm serve "$MODEL" \
    --host 127.0.0.1 --port "$PORT" \
    --max-model-len "$MAX_LEN" \
    --gpu-memory-utilization "$GPU_UTIL" >>"$LOG" 2>&1 &
else
  die "no vLLM on this image and nothing serving $UPSTREAM.
  Pick a vLLM image for the instance (vllm/vllm-openai works), or start any
  OpenAI-compatible server on port $PORT yourself before this runs."
fi

# ---- 3. wait for the weights to actually load -------------------------
say "waiting up to ${WAIT_SECS}s for the model to load"
waited=0
until curl -fsS --max-time 5 "$UPSTREAM/models" >/dev/null 2>&1; do
  sleep 5
  waited=$((waited + 5))
  if [ "$waited" -ge "$WAIT_SECS" ]; then
    say "the server never came up. Last 40 lines of $LOG:"
    tail -n 40 "$LOG" >&2 2>/dev/null || true
    die "giving up after ${WAIT_SECS}s."
  fi
  [ $((waited % 60)) -eq 0 ] && say "  still loading (${waited}s)"
done
say "model is up after ${waited}s"

# ---- 4. on air ---------------------------------------------------------
# exec, so the share IS the container's foreground process: when it stops, the
# instance stops, and Vast stops billing for a box that is no longer serving.
say "going on air"
exec roger "${SHARE_ARGS[@]}"
