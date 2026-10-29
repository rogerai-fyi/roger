# Serving a model the first time: today's flows, how other tools do it, and simpler options

Status: RESEARCH ONLY (2026-10-11). No code or spec has changed. Founder question, after
running `roger model serve bartowski/SmolLM2-135M-Instruct-GGUF:Q4_K_M` and `roger join '<station
invite>'`: "is this only complicated because we are reusing cache? will it be easier for the end
user ... seems hard", then "please just do research if this can be done first in a more easier flow
or pattern".

Short answer: about half of what looked hard came from the test setup, not from roger. The other
half is real and fixable:
1. You have to name a model by its HuggingFace repo path.
2. The default output is about ten lines of fit arithmetic and provenance.
3. Getting a model and offering it are two commands where one intent would do.

The cheapest big improvement is to build the RogerAI catalogue we already approved
(`features/share/model_catalog.feature`, with `internal/catalog` slice 1 already written) and give
its entries short names. Every other simplification hangs off that.

---

## 1. Today's flows, counted

Counts are for a real user, not the runbook's test harness. A "decision" is something the user must
know or choose. "Output" counts the lines `roger model serve` prints for the founder's run.

### What the test setup added that real users never face

- `XDG_CONFIG_HOME=/tmp/rbk-station`, `PATH=$B:$PATH` and `ROGERAI_NO_UPDATE_CHECK=1`: these keep the
  test away from the real config and binary.
- The two `ln -sfn` symlinks into `~/.cache/gotmp/modeltry`: these were meant to skip a download.
  They didn't work. `roger model status` and `roger model list` don't see a symlinked models
  directory (a known minor bug), so roger downloaded the 100 MiB file anyway.
- Lines broken by the terminal on paste (`--` then `detach --yes`). That is a runbook formatting
  problem, not roger's.

Without those, the station side was two commands. The model fetch, fit check, digest check and serve
all worked first try.

### (a) A first-time user who wants to chat with a local model on this machine

| Step | Command / action | Decisions |
|---|---|---|
| 1 | `roger model search <words>` (or already know a repo) | which repo, out of HuggingFace results |
| 2 | `roger model serve <owner/repo>[:QUANT]` | the quant is optional (roger picks Q4_K_M when it fits, `internal/localmodel/judge.go` `Suggest`); `fetch it? [y/N]` |
| 3 | it blocks the terminal unless `--detach` | keep it running, or detach |
| 4 | chat: `roger --lan use <band>` or the TUI | the band name is derived (`smollm2-135m-instruct`); outside LAN mode `roger use` means the market |

That's 3 to 4 commands, about 4 decisions, and around 45 typed characters for the model reference.
`serve` prints about 10 lines before the server starts (the founder's paste):
1. the machine's memory and speed;
2. the file and its size;
3. the fit verdict;
4. the memory sum (weights, cache, output, working, reserve);
5. the licence;
6. the source;
7. a progress bar;
8. "fetched and verified" with the digest;
9. the runtime's fit verdict;
10. "starting".

### (b) Sharing a model on the market

1. `roger model serve <owner/repo> --detach` (as in (a)).
2. `roger share`. This detects local servers and offers the FIRST detected server's FIRST model
   (`cmd/rogerai/main.go` around 1593-1617).
3. Logging in, if you want to earn.

If nothing is detected, share stops with "no local LLM detected ... Start one". It names Ollama and
LM Studio, but not `roger model serve`, which is roger's own way to start one.

Side note, out of this doc's scope: on a machine that also runs local proxies to paid accounts, the
"first detected server" can be one of those proxies. The station-offer work found the same class of
issue.

### (c) A station joining a LAN Tower

| Who | Command | Decisions |
|---|---|---|
| Owner | `roger tower invite --dir D --station --models <band>` | the owner must know the band name the station will serve |
| Station | `roger model serve <owner/repo>[:Q] --detach` | as in (a) |
| Station | `roger join '<station invite>'` | paste |

That's 3 commands across 2 machines, and the band name has to match on both sides. The invite
doesn't carry "which model to serve", only which models are allowed.

### (d) An Edge member serving a model to the owner's devices

- On the member: `roger model serve <owner/repo:Q>` as in (a).
- Or, already one command from the authority: `roger model serve <owner/repo:QUANT> --on <node>`.
  The member fetches only an exact `owner/repo:QUANT`, by an earlier ruling
  (`cmd/rogerai/modelcmd.go` `modelOnNode`).

So the remote path exists, but it requires the full repo and the exact quant.

### (e) The TUI

SHARE always ends with the row "+ Get a model that fits this machine", which is the first option when
nothing is detected (`features/serving/local_models.feature:581`). Choosing it:
1. opens the browser inside SHARE;
2. you type a search, since searching the wider world is an explicit action (`:229`);
3. you pick a result;
4. you see its quants and fits;
5. you confirm the fetch;
6. the row turns "on disk";
7. Enter serves it, asked first (`:569`).

That's about 7 interactions and 3 decisions. The screen opens empty except for what's already on
disk, because the RogerAI catalogue that would fill it isn't published yet: `internal/catalog` has
the manifest types but no importer.

### What is mandatory and what is incidental

| Item | Mandatory? | Source |
|---|---|---|
| Consent before a third-party fetch | Yes, "made once per model, on a screen that says whose word it is taking" | local_models.feature header ruling, 2026-09-30 |
| Licence shown and recorded (never gating) | Yes, it must be shown; it need not take its own line | local_models.feature:465 |
| Ours vs the author's word never drawn as equal | Yes | local_models.feature:221 |
| The memory sum stated, not only the verdict | Yes, as approved | local_models.feature:363 |
| Fit checked before serving (llama-fit-params) | Yes, a safety property | local_models.feature:342 |
| Digest verified | Yes | model_catalog.feature |
| Typing `owner/repo:QUANT` | No: the quant is already optional, and short names are only missing a catalogue | |
| Blocking the terminal by default | No: `--detach` exists | |
| Ten lines of output | No: the facts are required, the layout is not | |
| Two commands for "serve this to that Tower" | No | |

---

## 2. How comparable tools handle the first run

| Tool | First command | Naming | Default and quant | Output | Sharing to other machines |
|---|---|---|---|---|---|
| Ollama | `ollama run gemma4` pulls on first use, then serves and chats in one command | short library tags; `hf.co/{user}/{repo}[:QUANT]` for any HuggingFace GGUF | Q4_K_M when the repo has it, else "one reasonable quant"; quant is optional | `pulling manifest`, one bar per layer, `verifying sha256 digest`, `success` | set `OLLAMA_HOST` |
| LM Studio (`lms`) | `lms get llama-3.1-8b`, then `lms load`, then `lms server start` | short names, optional `@q4_k_m` | quant auto-picked to match installed runtimes; no name shows staff picks | progress per download | "Serve on Local Network" switches the bind to 0.0.0.0 (one setting) |
| Jan | Hub, then one click "Download" | browses HuggingFace | quants grouped Small / Balanced / Large with a recommended default; labels Fits / May be slow / Won't fit | live progress in the top bar | local API server |
| GPT4All | install, add a model, download | curated list | recommends one starter model ("we recommend starting with Llama 3") | in-app | not the focus |
| llamafile | download one file, `chmod +x`, run it | the file is the model | baked in | starts a local server | the server binds locally |
| LocalAI | `local-ai models install <name>`, then `local-ai run qwen3-4b` | gallery short names, or `huggingface://`, `ollama://`, `oci://` | from the gallery entry | install is a separate step (no pull on first use) | an API server |
| exo | `uv run exo`, or the macOS app | HuggingFace repo ids picked in a dashboard | download on demand | dashboard | devices "automatically discover each other - no manual configuration" |
| Apple Foundation Models | none: `SystemLanguageModel.default` | one system model | shipped with the OS; the app only checks availability | none | none (on-device only) |

Patterns that recur:
1. **Short names from a curated list**, with a full-path escape hatch for anything else (Ollama,
   LM Studio, LocalAI, GPT4All).
2. **One command that pulls on first use and runs** (`ollama run`). Only LocalAI makes install a
   separate step.
3. **The quant is hidden by default and picked by a stated rule** (Ollama's Q4_K_M, LM Studio's
   runtime match, Jan's recommended tier), but can always be overridden.
4. **A recommended default when no name is given** (LM Studio staff picks, GPT4All's starter, Jan's
   recommended tier, Apple's single model).
5. **Quiet, phase-based output**: one line per phase plus a bar; details live elsewhere.
6. **Fit labels in words**: Jan's Fits / May be slow / Won't fit matches roger's verdicts.
7. **Sharing is one switch or zero config** (LM Studio's toggle, exo's auto-discovery). None of them
   has roger's admission and trust model, so their simplicity is partly the absence of it.

Roger already does several things better than most of these: the fit is judged by the runtime itself
before serving, the licence is recorded, the digest is checked, and the ours-vs-author's distinction
is drawn. The gap is presentation and naming, not substance.

Sources:
- Ollama README: https://github.com/ollama/ollama
- Ollama with HuggingFace GGUFs (default Q4_K_M, `hf.co/...:QUANT`): https://huggingface.co/docs/hub/ollama
- Ollama pull output stages: https://geshan.com.np/blog/2025/02/ollama-commands/ and https://apxml.com/courses/getting-started-local-llms/chapter-4-running-first-local-llm/downloading-model-ollama
- LM Studio CLI: https://lmstudio.ai/docs/cli and `lms get`: https://lmstudio.ai/docs/cli/get
- LM Studio "Serve on Local Network": https://lmstudio.ai/docs/developer/core/server/serve-on-network
- Jan model hub and fit labels: https://www.jan.ai/docs/desktop/manage-models
- GPT4All quickstart: https://docs.gpt4all.io/gpt4all_desktop/quickstart.html
- llamafile: https://github.com/Mozilla-Ocho/llamafile
- LocalAI getting started: https://localai.io/docs/basics/getting_started/
- exo: https://github.com/exo-explore/exo
- Apple Foundation Models, SystemLanguageModel: https://developer.apple.com/documentation/foundationmodels/systemlanguagemodel

---

## 3. Options for roger

Step counts are for persona (a), plus (c) where it applies. Every option keeps consent, the fit
check, the digest check and the ours-vs-author's distinction.

### O1. A curated catalogue with short names (build the approved catalogue)

- **What:**
  - A small signed manifest of models RogerAI has tested: `smollm2`, `qwen3-0.6b`, `qwen3-8b`,
    `gpt-oss-20b`, and so on.
  - Each entry has a short name, a repo, per-quant files with sha256, the licence, and the serving
    memory we measured.
  - It is cached locally so it works offline.
  - `roger model serve qwen3-8b` resolves through it.
  - The quant is chosen by the existing `Suggest` rule against this machine.
- **Honesty:** better than today. Catalogue entries carry our measured numbers and are drawn as
  "RogerAI's catalogue", while HuggingFace repos stay "the author's word". This is exactly
  local_models.feature:221.
- **Steps:** (a) becomes `roger model serve qwen3-8b`, one decision (y). No search, no repo path.
- **Effort:** medium.
  - The manifest types, Validate, Fit and Merge exist in `internal/catalog`.
  - Missing: the importer, the signing and publishing, the cache, and the resolution in
    `modelResolve`.
  - Someone also has to curate and measure the entries, which is ongoing work: updating the list.
- **Specs:** builds `features/share/model_catalog.feature` (approved, not built). Adds a short-name
  field and a "short name resolves" scenario.

### O2. `roger model serve` with no argument picks a fitting default

- **What:** with no name, roger offers the best catalogue model that fits this machine comfortably,
  by a rule it states, for example "the largest general chat model that fits with room to spare".
  It names the model, its size and why, then asks y/N.
- **Steps:** (a) becomes `roger model serve`, then y.
- **Trade-off:** a default is a recommendation, so the rule must be stated in the output.
- **Effort:** small once O1 exists. It reuses `Judge` and `Suggest` over catalogue entries.

### O3. Quiet output by default, `--verbose` for the full account

- **What:** the default prints the same facts on fewer lines:
  ```
  qwen3-8b (RogerAI catalogue) · 4.9 GiB · fits: 6.1 of 191 GiB at 8K · licence apache-2.0
  fetch it? [y/N] y
  fetching  4.9 GiB  ████████████░░░  62%  48 MB/s  0:38 left
  serving qwen3-8b: verified sha256 2e80..., llama.cpp cuda, 8K context
  ```
  `--verbose`, or `roger model files <name>`, prints today's full account, including the full sum.
- **Honesty:** the licence and source stay on the first line, and fits plus need stays as one
  number. local_models.feature:363 ("the sum is stated, not just the verdict") would need either
  re-approval (the sum behind `--verbose`, the total kept on the line) or the compact form kept as
  "the sum".
- **Steps:** none removed, but the screen goes from about 10 lines to 4.
- **Effort:** small.

### O4. One-command station: the invite says which model, the station offers to serve it

- **What:**
  - The owner's station invite names the model (`roger tower invite --station --serve qwen3-8b`, or
    the existing `--models` reused).
  - On the station, `roger join '<invite>'` notices nothing is serving it. It offers: "the Tower asks
    for qwen3-8b (4.9 GiB, fits); fetch and serve it here? [Y/n]". Then it serves detached and
    joins.
- **Honesty:** the station's owner still consents once, on a line that names the model and whose
  word it is.
- **Steps:** (c) becomes 1 owner command plus 1 paste plus 1 y. The band-name matching problem goes
  away.
- **Effort:** medium. It needs O1 for names. Without O1 it works with full repo references, but less
  nicely.
- **Specs:** new scenarios in `features/join/invite.feature`. This fits the 2026-10-11 station-offer
  ruling: the offered model is roger-served by construction.

### O5. `roger run <name>` (or `roger --lan use <name>` that offers to serve)

- **What:** like `ollama run`: fetch if missing (asked), serve detached, open a local chat.
- **Naming decision:**
  - `roger use` means the market today, so overloading it would blur that.
  - A new verb (`roger run qwen3-8b` or `roger chat qwen3-8b`) keeps "market" and "local" apart.
  - Or `roger --lan use qwen3-8b` could offer "nothing local serves qwen3-8b: fetch and serve it
    here? [y/N]".
- **Steps:** (a) becomes one command and one y.
- **Effort:** small to medium. It composes O1 and O3 with the existing serve, LAN relay and chat.

### O6. TUI first run shows suggestions, not an empty search

- **What:** the "+ Get a model that fits this machine" row opens on two or three catalogue
  suggestions that fit, already judged. Enter fetches and serves, asked once. Search stays available
  for the wider world.
- **Honesty:** this is the cached catalogue and what's on disk, which `:229` already allows.
  Searching HuggingFace stays an explicit action.
- **Steps:** (e) goes from about 7 interactions to 2 or 3.
- **Effort:** small to medium once O1 exists.

### Effects on other flows

- **`roger share` with nothing detected:** it can suggest the O2 or O5 command instead of only
  naming third-party servers. That's a one-line change in the message.
- **Edge `--on <node>`:** it can accept a catalogue short name. The ruling that a member fetches
  only an exact reference still holds, because a short name resolves to an exact file before it is
  sent.

---

## 4. Recommendation

Do O1 first; it is the foundation. Then O3, O2 and O6, then O4 and O5.

| Persona | Today | After |
|---|---|---|
| (a) first chat | search, `serve owner/repo:Q`, y, detach or second terminal, `--lan use band`: 3-4 commands, ~4 decisions, ~10 lines | `roger run` (or `roger model serve`), y: 1 command, 1 decision, ~4 lines |
| (b) market share | serve, share (picks the first detected) | `roger model serve`, y, `roger share` |
| (c) station | owner `invite --station --models <band>`; station `serve owner/repo:Q`, `join`: 3 commands, band must match | owner `invite --station --serve qwen3-8b`; station `join`, y: 2 actions |
| (d) Edge member | `serve owner/repo:QUANT --on node` | `serve qwen3-8b --on node` |
| (e) TUI | ~7 interactions, empty browser | 2-3 interactions, suggestions shown |

All of the following are kept: consent once per model, the licence shown, the source drawn as ours
or the author's, the runtime's fit check before serving, digest verification, the disk budget, and
"serving means it answers".

### Founder decisions

1. **The catalogue's first entries and who curates them.**
   - Options: (i) a small fixed list we measure on this machine and the Pis, shipped in the binary
     and refreshable; (ii) a signed list fetched from rogerai.fm and cached; (iii) both, with the
     binary's copy as the offline fallback.
   - Recommend (iii).
2. **The no-argument default rule (O2).**
   - Options: (i) the largest general chat model that fits comfortably; (ii) a fixed "starter" per
     memory tier, for example under 8 GiB, 8 to 24 GiB, and over 24 GiB; (iii) always the smallest,
     for speed.
   - Recommend (ii): stable, explainable, testable.
3. **The local "run" verb (O5).**
   - Options: (i) a new `roger run <name>`; (ii) `roger chat <name>`; (iii) `roger --lan use <name>`
     offering to serve; (iv) none.
   - Recommend (i), mirroring `ollama run`. It keeps `roger use` meaning the market.
4. **Quiet output (O3).**
   - Options: (i) four lines by default with the total kept, full sum behind `--verbose`
     (re-approves local_models.feature:363); (ii) keep the full sum, just tighter.
   - Recommend (i).
5. **Station invite carries the model (O4).**
   - Options: (i) a new `--serve <name>` on `roger tower invite --station`; (ii) reuse `--models`,
     so the first entry is the one to serve.
   - Recommend (i): offering and requesting are different things.

### Approved specs that would change

- `features/share/model_catalog.feature`: built (O1), plus a short-name field and resolution
  scenarios.
- `features/serving/local_models.feature`:
  - `:363` the sum is stated (O3, if decision 4 is (i));
  - `:581` getting a model is the last row (O6 suggestions);
  - `:229` searching is an action (unchanged; suggestions come from the cached catalogue).
- `features/join/invite.feature`: new station scenarios (O4).
- `features/share/hosting_compatibility.feature`: the "no local LLM detected" message suggests roger's
  own serve.
- New: a short spec for `roger run` (O5), if chosen.

Nothing here has been built.
