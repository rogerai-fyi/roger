# v6.3.1 - a station that says "too big" in bytes is still saying "too big"

A patch. One fix, no API change, no migration, and the module path is unchanged at
`rogerai.fm/roger/v6`.

## The agent stopped on an error it could have fixed itself

The AGENT already compacts automatically when a conversation outgrows a model's context
window: it drops the raw tool output it no longer needs and sends the turn again. That only
happened when the station said so in words it recognised - "context length exceeded",
"maximum context", "too many tokens", "kv cache".

But a station can refuse an oversized conversation at the HTTP layer instead of the
tokenizer, and then it never says "context" at all:

```
Maximum request body size 1048576 exceeded, actual body size 1050714 (status 413)
```

That is llama.cpp and Ollama. A proxy in front says `Request Entity Too Large`. None of it
matched, so the turn stopped and offered:

> retry the turn or fix the error

which cannot work. Retrying sends the same oversized body again, and there is nothing for a
human to fix. An error message that looks actionable and is not is worse than one that
admits it is stuck.

It is the same wall measured in bytes rather than tokens, and it has the same remedy:
compaction frees bytes exactly as it frees tokens. The agent now recognises it and compacts
by itself. On the session that prompted this the request was 2KB over a 1MiB cap - one prune
clears that comfortably.

The message it shows if compaction is not enough now names the right limit too:

> the conversation outgrew what gpt-oss-120b's station accepts in one request

rather than blaming the context window. The model's window was fine; the station's
per-request byte cap was not, and naming the wrong one sends you to the wrong knob.

The match is deliberately narrow: request-size shapes only, never a bare `413`, which can
appear in a model's own answer or in a tool result being relayed back. Compacting on that
would silently destroy your context over a number that merely looks like a status code.

## Upgrading

```sh
curl -fsSL https://rogerai.fm/install.sh | sh
```

Nothing to migrate.
