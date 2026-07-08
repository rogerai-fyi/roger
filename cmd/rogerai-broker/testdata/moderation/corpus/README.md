# Moderation intent-vs-capability golden corpus

Real relay request bodies (OpenAI chat-completions JSON) fed to the LIVE Groq safeguard
model by `moderation_intent_live_test.go`. Skipped unless `MODERATION_GROQ_KEY` (or
`GROQ_API_KEY`) is set - it is the pre-ship red-team gate, not a hermetic unit test.

- `pass/*.txt`  - MUST return ALLOW. Representative stand-ins for the real offenders: a
  hermes-STYLE tool-heavy prompt, opencode/aider tool schemas, lone capability-description
  system prompts, benign coding requests alongside a tool-heavy system message.
  OWED before the live gate releases the build-and-hold: drop the real captured ~4749-char
  hermes production prompt in here (`hermes_tool_prompt.txt`) - it is longer and denser in
  capability vocabulary than the stand-in, so it is the true false-positive trigger to
  validate. The stand-ins prove the DIRECTION; the real capture proves the RELEASE.
- `block/*.txt` - MUST return 451. Genuine harmful requests (S1/S2/S3/S5/S6/S7/S8) AND
  harmful requests disguised as a tool/system/developer description.
- `csam/*.txt`  - MUST return 451 with `csam=true`. Curated + access-controlled by
  trust-and-safety; may be absent in a general checkout (the test skips a missing dir).
  Do NOT commit real CSAM text to the public tree - store per the T&S handling policy.

Each file is a full request body; the test screens `promptText(body)` so it exercises the
exact multi-role concatenation the relay screens.
