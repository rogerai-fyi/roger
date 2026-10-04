#!/usr/bin/env bash
# scripts/localplane-routing-smoke.sh - standalone Tower routing smoke check (loopback only).
#
# Stands up a fresh Core-free Tower (roger-tower-local) with two stations ("node1", "node2")
# serving the same model through fake upstreams that RECORD every job body, opens a client
# channel with `roger use`, and asserts the routing contract of the local plane (§5a):
#
#   1. a "models" request is served, X-RogerAI-Model names the served model, X-Roger-Cost: 0
#   2. an "only" request (provider.only = node2) is served by node2 alone, every time
#   3. an ignored-key request (roger.min_tps) answers X-Roger-Routing-Ignored naming the key
#   4. no station's job body carries the "provider" or "roger" carrier, or "models"
#
# Usage: make build && scripts/localplane-routing-smoke.sh      (BIN=dir overrides ./bin)
# Exits non-zero on any failure. Touches nothing outside a temp dir.
set -u

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BIN="${BIN:-$ROOT/bin}"
for b in roger roger-tower roger-tower-local; do
  [ -x "$BIN/$b" ] || { echo "missing $BIN/$b (run: make build)"; exit 2; }
done

W="$(mktemp -d)"
cleanup() { kill $(jobs -p) 2>/dev/null; wait 2>/dev/null; rm -rf "$W"; }
trap cleanup EXIT
FAILS=0
fail() { echo "FAIL: $*"; FAILS=$((FAILS + 1)); }
pass() { echo "ok:   $*"; }

port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'; }
TOWER_PORT=$(port); UP1=$(port); UP2=$(port)
MODEL=test-model

# --- fake upstreams: answer, and record each job body the station forwards ---------------------
cat > "$W/up.py" <<'PY'
import http.server, json, sys
port, name, log = int(sys.argv[1]), sys.argv[2], sys.argv[3]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('content-length', 0)))
        with open(log, 'ab') as f:
            f.write(body.replace(b'\n', b' ') + b'\n')
        b = json.dumps({"id": "cmpl-1", "object": "chat.completion", "model": "test-model",
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "served by " + name},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 3, "completion_tokens": 3, "total_tokens": 6}}).encode()
        self.send_response(200); self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
python3 "$W/up.py" "$UP1" node1 "$W/jobs-node1.log" &
python3 "$W/up.py" "$UP2" node2 "$W/jobs-node2.log" &
sleep 1

# --- identities: one consumer, two station node keys (separate config homes) ------------------
export HOME="$W/home"; mkdir -p "$HOME"
CFG_C="$W/cfg-client"; CFG_1="$W/cfg-node1"; CFG_2="$W/cfg-node2"
mkdir -p "$CFG_C" "$CFG_1" "$CFG_2"
"$BIN/roger-tower" init --dir "$W/tower" --mode standalone >/dev/null
UUSER=$(XDG_CONFIG_HOME="$CFG_C" "$BIN/roger" account | sed -n 's/.*tower client id: //p' | head -1)
nodeid() { # $1 = config home, $2 = upstream port; a short share run creates node.key
  XDG_CONFIG_HOME="$1" timeout 2 "$BIN/roger" share --broker "http://127.0.0.1:$TOWER_PORT" \
    --upstream "http://127.0.0.1:$2/v1/chat/completions" --model "$MODEL" >/dev/null 2>&1
  local nk; nk=$(cat "$1/rogerai/node.key")
  echo "u_$(printf '%s' "${nk: -64}" | sha256sum | cut -c1-16)"
}
UNODE1=$(nodeid "$CFG_1" "$UP1"); UNODE2=$(nodeid "$CFG_2" "$UP2")
[ -n "$UUSER" ] && [ -n "$UNODE1" ] && [ -n "$UNODE2" ] || { echo "could not derive identities"; exit 1; }

# --- offline ceremony, then serve -------------------------------------------------------------
INV=$("$BIN/roger-tower" invite --dir "$W/tower" --client "$UUSER")
ID=$(echo "$INV" | sed -n 's/^invitation: //p'); CODE=$(echo "$INV" | sed -n 's/^code: //p')
"$BIN/roger-tower" admit  --dir "$W/tower" --client "$UUSER" --id "$ID" --code "$CODE" >/dev/null
"$BIN/roger-tower" attach --dir "$W/tower" --station node1 --key "$UNODE1" --models "$MODEL" >/dev/null
"$BIN/roger-tower" attach --dir "$W/tower" --station node2 --key "$UNODE2" --models "$MODEL" >/dev/null
"$BIN/roger-tower-local" --dir "$W/tower" --bind "127.0.0.1:$TOWER_PORT" >"$W/tower.log" 2>&1 &
sleep 1
for n in 1 2; do
  cfg="$W/cfg-node$n"; up=UP$n
  XDG_CONFIG_HOME="$cfg" "$BIN/roger" share --broker "http://127.0.0.1:$TOWER_PORT" \
    --upstream "http://127.0.0.1:${!up}/v1/chat/completions" --model "$MODEL" >"$W/share$n.log" 2>&1 &
done
sleep 2

# --- a client channel (the stations long-poll /local/poll for its jobs) --------------------------
mkdir -p "$CFG_C/rogerai"
printf '{"broker":"http://127.0.0.1:%s"}\n' "$TOWER_PORT" > "$CFG_C/rogerai/config.json"
XDG_CONFIG_HOME="$CFG_C" "$BIN/roger" use "$MODEL" --yes >"$W/use.log" 2>&1 &
for _ in $(seq 1 40); do grep -q OPENAI_API_BASE= "$W/use.log" && break; sleep 0.25; done
BASE=$(sed -n 's#.*OPENAI_API_BASE=\(http://[^ ]*\).*#\1#p' "$W/use.log" | head -1)
KEY=$(sed -n 's#.*OPENAI_API_KEY=\([a-f0-9]*\).*#\1#p' "$W/use.log" | head -1)
[ -n "$BASE" ] && [ -n "$KEY" ] || { echo "roger use did not open a channel:"; cat "$W/use.log"; exit 1; }

post() { # $1 = name, $2 = body; writes $W/$1.h and $W/$1.b, prints the status
  curl -s -o "$W/$1.b" -D "$W/$1.h" -w '%{http_code}' -X POST "$BASE/chat/completions" \
    -H "Authorization: Bearer $KEY" -H 'content-type: application/json' -d "$2"
}
hdr() { grep -i "^$2:" "$W/$1.h" | head -1 | cut -d: -f2- | tr -d ' \r'; }

# 1. models[]
code=$(post models '{"model":"'"$MODEL"'","models":["'"$MODEL"'"],"messages":[{"role":"user","content":"hi"}]}')
[ "$code" = 200 ] && pass "models[] request served" || fail "models[] request answered $code: $(cat "$W/models.b")"
[ "$(hdr models X-RogerAI-Model)" = "$MODEL" ] && pass "X-RogerAI-Model: $MODEL" \
  || fail "X-RogerAI-Model is '$(hdr models X-RogerAI-Model)', want $MODEL"
[ "$(hdr models X-Roger-Cost)" = 0 ] && pass "X-Roger-Cost: 0" \
  || fail "X-Roger-Cost is '$(hdr models X-Roger-Cost)', want 0"

# 2. provider.only, repeated: a random claimant passes once by luck, not six times
bad=0
for i in 1 2 3 4 5 6; do
  code=$(post only '{"model":"'"$MODEL"'","provider":{"only":["node2"]},"messages":[{"role":"user","content":"only '"$i"'"}]}')
  { [ "$code" = 200 ] && grep -q 'served by node2' "$W/only.b"; } || bad=$((bad + 1))
done
[ "$bad" = 0 ] && pass "provider.only node2: served by node2 six times" \
  || fail "provider.only node2: $bad of 6 not served by node2 (last: $(cat "$W/only.b"))"

# 3. an ignored key is named, never silently dropped
code=$(post ignored '{"model":"'"$MODEL"'","roger":{"min_tps":10},"messages":[{"role":"user","content":"hi"}]}')
[ "$code" = 200 ] || fail "ignored-key request answered $code: $(cat "$W/ignored.b")"
case ",$(hdr ignored X-Roger-Routing-Ignored)," in
  *,roger.min_tps,*) pass "X-Roger-Routing-Ignored names roger.min_tps" ;;
  *) fail "X-Roger-Routing-Ignored is '$(hdr ignored X-Roger-Routing-Ignored)', want roger.min_tps" ;;
esac

# 4. the carriers never reach a station
cat "$W"/jobs-node*.log > "$W/jobs.log" 2>/dev/null
[ -s "$W/jobs.log" ] || fail "no station recorded a job body"
if python3 - "$W/jobs.log" <<'PY'
import json, sys
bad = [l for l in open(sys.argv[1]) if l.strip() and
       any(k in json.loads(l) for k in ("provider", "roger", "models"))]
sys.exit(1 if bad else 0)
PY
then pass "no job body carries provider / roger / models"
else fail "a station's job body still carries provider, roger or models: $(cat "$W/jobs.log")"
fi

echo
[ "$FAILS" = 0 ] && { echo "PASS: local plane routing smoke"; exit 0; }
echo "FAIL: $FAILS check(s)"; exit 1
