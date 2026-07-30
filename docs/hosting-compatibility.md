# Model-host compatibility

RogerAI is a network and relay, not a model runtime. You can share from your
preferred model host when it exposes the HTTP contract below. The software used
to train, fine-tune, optimize, quantize, or download the model does not otherwise
matter.

This means both of these Unsloth workflows work:

- Load and serve a model in Unsloth Studio, then let `roger share` detect its
  authenticated API on `http://127.0.0.1:8888/v1`.
- Train, optimize, or export a model with Unsloth, serve the resulting artifact
  through another compatible host such as vLLM, llama.cpp, Ollama, or LM Studio,
  and share that endpoint.

## Support levels

These terms describe different things:

- **Verified:** exercised against that host's real release or an equivalent
  conformance fixture.
- **Auto-detected:** RogerAI knows the host's default endpoint or environment
  variables, or finds its listening localhost port.
- **Compatible by protocol:** the host is not named in RogerAI but implements the
  contract; connect it with `roger share --upstream <url>`.

A named host is not guaranteed to support every model or modality, and an
unnamed host is not unsupported merely because its brand is absent.

## Chat upstream contract

A shareable chat host must provide:

- `GET /v1/models`, returning model IDs in the OpenAI list shape;
- `POST /v1/chat/completions`, accepting the offered model ID;
- OpenAI-style SSE when `stream: true`;
- token usage in non-streaming responses and, for streaming, support for
  `stream_options.include_usage`.

RogerAI accepts a server root, `/v1` base, or full Chat Completions URL. Custom
and remote endpoints may need:

```sh
roger share --upstream http://127.0.0.1:9999/v1 \
  --upstream-key "$YOUR_HOST_API_KEY"
```

The key stays in the provider's local configuration and is sent only to that
upstream. RogerAI does not send harvested keys to arbitrary services found
during listening-port discovery.

Unsloth Studio serves its authenticated OpenAI-compatible API on
`http://127.0.0.1:8888` with an `sk-unsloth-...` key, and documents
`UNSLOTH_STUDIO_AUTH_TOKEN` as the variable holding that key. RogerAI reads it,
so a Studio set up by Unsloth's own instructions needs no extra configuration.

Two further variables are RogerAI's own, not exported by Unsloth: set
`UNSLOTH_STUDIO_URL` when the Studio runs on a non-default port
(`unsloth studio -p ...`), and `UNSLOTH_API_KEY` as an alias for the key if you
prefer the naming used by the other hosts in the detector table.
`UNSLOTH_STUDIO_AUTH_TOKEN` takes precedence when both are set.

## Optional modality contracts

Speech and transcription are separate optional upstream surfaces:

- `POST /v1/audio/speech`
- `POST /v1/audio/transcriptions`

A host implementing chat is not automatically assumed to implement audio.

## Current gaps

RogerAI does not currently relay these upstream APIs:

- OpenAI Responses;
- Anthropic Messages;
- embeddings or reranking;
- image generation;
- MCP, code-execution, web-search, model-management, or other host admin routes.

Those routes remain outside the node allowlist even when a host, including
Unsloth Studio, provides them. This deliberately prevents broker traffic from
reaching privileged local control surfaces.

Compatibility can also fail when a nominally OpenAI-compatible host omits model
listing, returns a different response shape, uses non-SSE streaming, rejects
`stream_options`, or does not report usage needed for metering. Such a host may
need a compatibility proxy or a focused RogerAI adapter.

## Why RogerAI does not launch models

RogerAI currently leaves model download, quantization, GPU placement, inference
tuning, and process lifecycle to dedicated hosts. That keeps one stable sharing
contract across llama.cpp, vLLM, Unsloth, desktop applications, and future
servers instead of making RogerAI another wrapper around one engine.

## Compatibility audit record

Checked 2026-07-30:

| Claim | Class | Evidence | Result |
|---|---|---|---|
| Unsloth exposes Models and Chat Completions | Source-backed | [Unsloth README](https://github.com/unslothai/unsloth#inference) and its API implementation | Supported |
| Unsloth Studio defaults to port 8888 | Source-backed | [Unsloth launch documentation](https://github.com/unslothai/unsloth#launch) | Supported |
| RogerAI accepts an authenticated Unsloth endpoint | Measured | `TestDetectUnslothWithItsEnvKey` against an authenticated HTTP conformance fixture | Passed |
| Unknown listening ports do not receive harvested keys | Measured | `TestDetectDoesNotSprayEnvKeysToPortScans` | Passed |
| Every nominally OpenAI-compatible host works without qualification | Unresolved and too broad | Requires per-host streaming and usage conformance tests | Do not claim |
