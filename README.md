# RogerAI

[![coverage](https://rogerai-fyi.github.io/roger/coverage/badge.svg)](https://rogerai-fyi.github.io/roger/coverage/packages.html)

```
             ·   ˙   ·
           ˙ ((  •  )) ˙                   ┌──────────────┐
              \(   )/                      │ ((•)) ON AIR │
               │ R │         ▟█▙           └──────────────┘
   ┌───────────┴───┴──────────█───────────────────────────┐
   │  ◉ ch 01 · on band gpt-oss-20b · 42 tok/s   ▂ ▄ ▆ █  │
   ╰┬────────────────────────────────────────────────────┬╯
    ▔                                                    ▔

        █▀▀█ █▀▀█ █▀▀▀ █▀▀▀ █▀▀█     █▀▀█ ▀█▀  ▄
        █▀▀▄ █  █ █ ▀█ █▀▀  █▀▀▄  ▄  █▀▀█  █   █
        ▀  ▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀  ▀     ▀  ▀ ▀▀▀  ▀
             borrow a GPU, pay by the token · roger that.
```

**A two-way radio for GPUs.** RogerAI is a marketplace for crowd-sourced, self-hosted LLMs:
people run open models on their own GPUs and go "on air"; you tune in and pay per token. Every
token carries a **model-lineage record** - a receipt signed by the provider and counter-signed
by the broker, tracing the response back to the model that produced it. Owners monetize idle
hardware; users get cheap, diverse access.

```
curl -fsSL https://rogerai.fyi/install.sh | sh
```

Then just run `roger` for the interactive radio (browse stations, tune in, test, copy the
endpoint; `rogerai` is the legacy alias). Already have Go?
`go install github.com/rogerai-fyi/roger/cmd/rogerai@latest`.

## Use it

```
roger                         # interactive TUI: browse → connect → test → copy endpoint
roger search                  # list models (cheapest now first; shows tok/s, ◆ confidential, FREE)
roger use <model>             # open a local OpenAI-compatible endpoint that relays via the broker
roger voices                  # browse voices on air (TTS/STT stations, sample previews)
roger say --voice <v> "hi"    # text-to-speech through a voice station, played locally
roger balance                 # wallet credits
roger topup 10                # buy credits (Stripe)
roger remote                  # your private BASE STATION: continue an agent session from anywhere
```

Inside the agent (`[0] AGENT`), `/remote-control` puts the live session on your private **Base
Station** so you can pick it up from another terminal (`roger remote attach <code>`), the web
console, or the app - private to your account, one-time link codes for a phone. Tools keep
running on the host and still ask before anything mutating.

`roger use` (or the TUI's "tune in") exposes `http://127.0.0.1:4141/v1` with an API key - point
any OpenAI-compatible tool at it.

## Share your GPU (become a provider)

The node **dials OUT** and long-polls the broker for jobs - no inbound ports, no tunnel
dependency. Behind any NAT, one command:

```
roger share                                    # auto-detects your local model, starts earning
# options: --price-in/--price-out, --free-window 03:00-03:30, --schedule '<time-of-use JSON>',
#          --confidential (TEE-attested), --upstream <your OpenAI endpoint>
```

## How it works

```
rogerai ──► broker (broker.rogerai.fyi) ──► your node ──► your local model
 discover     registry · wallet · relay      dials out      (Ollama/vLLM/llama.cpp/LM Studio)
 use/topup    match · meter · co-sign         serve+sign
```

The broker is the only public component and is **content-blind** (it stores token counts and
signed receipts, never prompts). It's an OpenAI-compatible relay - see the served spec at
`/openapi.yaml`.

- **Per-token pricing** with a 24h price-lock, **free** and **time-of-use** windows.
- **Voice bands** - TTS/STT stations alongside chat: share a Kokoro-style speech server with
  `roger share`, browse with `roger voices`, attributed on air as `@station/slug`.
- **Lineage receipts** - hash-chained, dual-signed (`internal/protocol`).
- **Privacy** - identity pseudonymized to providers; a confidential (TEE) tier for sensitive work.
- **Routing constraints** - price, measured throughput (tok/s), confidential-only.

## Run it locally

```
make build
make demo     # broker + a node + a request, end to end (needs a local OpenAI endpoint)
go test ./...
```

## Docs

- [BROKER-SPEC.md](BROKER-SPEC.md) - the open broker spec (anyone can self-host/federate)
- `cmd/rogerai-broker/openapi.yaml` - OpenAPI 3.1 (also served at `/openapi.yaml`)
- [VERIFICATION.md](VERIFICATION.md) · [PRIVACY.md](PRIVACY.md) · [DEPLOY.md](DEPLOY.md) · [STRIPE.md](STRIPE.md)

## License

[PolyForm Perimeter 1.0.0](LICENSE) - source-available, free for any non-competing use. You can
self-host your own broker for your own community; you can't run it as a competing commercial
marketplace service. See the LICENSE for the exact grant.
