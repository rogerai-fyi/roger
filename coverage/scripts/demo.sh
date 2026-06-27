#!/usr/bin/env bash
# RogerAI demo: rogerai (CLI) -> broker -> rogerai share (node) -> local LiteLLM.
set -uo pipefail
cd "$(dirname "$0")/.."
UPSTREAM="${UPSTREAM:-http://127.0.0.1:8060/v1/chat/completions}"
MODEL="${MODEL:-gpt-oss-20b}"
export ROGER_BROKER="${ROGER_BROKER:-http://127.0.0.1:7070}"  # local demo (prod default is broker.rogerai.fyi)

for p in 7070 7072; do pid=$(ss -tlnpH 2>/dev/null | grep "127.0.0.1:$p" | grep -oP 'pid=\K[0-9]+' | head -1); [ -n "$pid" ] && kill "$pid" 2>/dev/null; done
sleep 1
./bin/rogerai-broker > /tmp/roger-broker.log 2>&1 & BPID=$!
sleep 1
./bin/rogerai share -upstream "$UPSTREAM" -model "$MODEL" -node demo-node > /tmp/roger-agent.log 2>&1 & APID=$!
sleep 2

echo "== rogerai search =="; ./bin/rogerai search
echo "== balance before =="; ./bin/rogerai balance
echo "== request through the chain =="
curl -s -m60 -D /tmp/roger-hdr.txt http://127.0.0.1:7070/v1/chat/completions \
  -H "X-Roger-User: demo-user" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"In 4 words: what is RogerAI?\"}],\"max_tokens\":120}" >/dev/null
grep -iE 'X-RogerAI-(Cost|Balance)' /tmp/roger-hdr.txt
echo "lineage receipt:"; grep -i 'X-RogerAI-Receipt' /tmp/roger-hdr.txt | sed 's/^[^:]*: //'
echo "== balance after =="; ./bin/rogerai balance

kill $BPID $APID 2>/dev/null
echo "(demo done)"
