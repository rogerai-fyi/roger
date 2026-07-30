# RogerAI TUI restart handoff — 2026-07-29

This is the durable resume point for the RogerAI TUI redesign after a machine
restart. Read this document, the repository `CLAUDE.md`, and
`docs/tube-ping-mascot.md` before changing code.

## Repository and release state

- Repository: `/home/luis/ai/RogerAI`
- Branch: `release/v5.3.9-prep`
- HEAD when this handoff was written: `5352497`
- HEAD is tagged `v5.4.6` and matches `origin/release/v5.3.9-prep`.
- The TUI work described below is **uncommitted post-release work** in a very
  dirty shared worktree.
- Do not reset, checkout, clean, or broadly rewrite the worktree.
- Do not commit, tag, or publish unless Luis explicitly asks.
- Preserve the unrelated untracked subtitle:
  `web/src/assets/broadcasts/independence-v5.srt`.

At the time of writing, another substantial domain-migration/web/broker change
was being made concurrently. It touches many files outside the TUI and added:

- `cmd/rogerai/domain_migration_test.go`
- `cmd/rogerai-broker/domain_migration_test.go`
- `features/domain/`
- `web/test/domain-migration.test.mjs`

Treat all of that as someone else's active work. The last coverage run failed
because `cmd/rogerai/domain_migration_test.go` referenced an undefined
`remoteLinkURL`. This appeared after the mascot smoke gate passed. That compile
blocker is now resolved: the symbol became `remoteLinkCode`, and the full suite
and coverage gate are green. Do not remove or broadly rewrite the concurrent
domain-migration work as part of the TUI task without first resolving
ownership/current intent.

## Founder-approved direction

The goal is a clean, crisp, spacious TUI influenced by OpenCode and Codex:

- Roger red remains the house accent, but additional cool/warm semantic colors
  distinguish roles, code, diffs, state, usage, and selection.
- Prompt text must wrap without disappearing in both AGENT and TUNE IN.
- Terminal-native drag selection/copy remains available by default.
- User prompts, Roger answers, metadata, tools, approvals, and the composer must
  have obvious structural separation.
- Coding responses need readable fenced code and green/red diff rendering.
- Usage, steps, tokens, and spend should be truthful, compact, and graphical.
- Agent calls are unlimited by default; `agent-timeout` remains an optional
  configured cap.
- Codex CLI belongs in `/operator` as a detected context-only guest.
- The mascot family is Tube Ping, while classic Ping remains a compatibility
  character and constrained/ASCII fallback.

Founder explicitly approved:

- Codex guest support.
- Unlimited-by-default agent duration.
- The committed-spec direction changes needed for those features.
- The conversation hierarchy and actionable visual-polish direction.
- Tube Ping, provided it uses the exact pixel design below.

## Canonical Tube Ping — do not redesign

Luis rejected the later line-art experiment. The approved canonical form is
the original pixel receiver:

```text
   ▄██████▄
(  █    • █▓  )
   █  ROG █▓
    ▀█▄▄█▀▒
     ▀  ▀
```

Important rendering lesson: the first implementation styled nearly every block
glyph independently. Repeated ANSI start/reset spans made the body look
fragmented in Luis's terminal. `styleTubePingRow` now emits contiguous body
spans and only breaks for the red eye and depth edge. Preserve that approach.

Current mascot forms:

- Hero/title card: the exact five-row sprite above.
- Ping World walker: a scene-sized derivative with alternating feet.
- Header station bug: `▟•▙▓`.
- AGENT corner: the same stable five-row pixel Tube Ping family for waiting,
  thinking, streaming, and tool states.
- Classic Ping: retained in Ping World and for narrow/ASCII compatibility.

Tube Ping is meant to walk inside the `z` ASCII world after its short debut,
not exist only as a splash. On supported layouts (`>=72x20`), Tube Ping moves
across the rim with alternating feet and keeps distance from classic Ping.
Small worlds omit Tube Ping cleanly.

Source and contracts:

- `internal/tui/tube_ping.go`
- `internal/tui/tube_ping_test.go`
- `internal/tui/pingworld.go`
- `internal/tui/ping.go`
- `features/tui/tube_ping_mascot.feature`
- `docs/tube-ping-mascot.md`

## Implemented post-v5.4.6 TUI work

### Prompt/composer correctness

- AGENT and TUNE IN use multiline textareas.
- One-to-two-row wrapping no longer scrolls the first row away.
- Drafts over the six-row cap render a cursor-following window.
- Explicit newlines, paste, CJK, emoji, and narrow widths are preserved.
- Up/down arrows edit inside multiline drafts before recalling history.
- Multiline history persists as JSON-per-line with legacy plain-line fallback.
- Render-time textarea copies no longer mutate the live shared viewport.

### Conversation hierarchy

- TUNE IN user turns render as a warm authored `YOU ›` band.
- Roger answers begin with a cool `ROGER › model` marker.
- Answer rows use a continuous `▏` gutter.
- Prose precedes quieter answer/session metadata.
- The prompt/answer relationship is structurally visible without boxing every
  message.
- Syntax-aware answer rendering supports headings, fenced code, and explicit
  `diff` fences with green/red lines.

Primary files:

- `internal/tui/tui.go`
- `internal/tui/agent.go`
- `internal/tui/conversation_hierarchy_test.go`
- `features/tui/conversation_hierarchy_and_selection.feature`

### Prompt and approval polish

- Always-on transcript/composer seam.
- Context-derived next-action placeholder:
  - error → retry/fix;
  - file write → run tests/review;
  - question → answer the agent;
  - tool-bearing turn → continue.
- Right Arrow accepts a grounded ghost suggestion into an empty composer
  without sending it or overwriting authored text.
- `→ accept` advertises the interaction.
- Approval state uses a prominent red `● APPROVAL REQUIRED` block with the
  complete soft-wrapped command and `[y/N] deny=default`.
- A tool invocation owns one stateful running/approved/result card instead of
  producing four near-duplicate transcript lines.
- Fallback approval recovery now creates bookkeeping that the eventual result
  updates in place.

Primary contracts:

- `features/tui/agent_prompt_fixes.feature`
- `features/tui/actionable_visual_polish.feature`
- `internal/tui/actionable_visual_polish_test.go`
- existing AGENT Godog tests.

### Mouse and copy behavior

Implemented:

- Mouse capture is **off by default**, preserving ordinary terminal
  drag-to-select/copy.
- `ctrl+o` and `/mouse` toggle captured wheel scrolling from AGENT and TUNE IN.
- Returning from `/operator` respects the current mouse ownership instead of
  unconditionally re-enabling capture.
- `ctrl+y` and `/copy` still use the app clipboard path and show copy feedback.
- Idle animation/ticks are frozen where needed so repainting does not erase
  native terminal selection.

Not yet implemented:

- App-owned smart drag selection in opt-in mouse mode.
- Exact visible selection extraction across wrapped rows and message blocks.
- Selection highlighting, Unicode character-count toast, honest clipboard
  success/failure acknowledgement, reverse drag, and cancellation behavior.

The desired smart-selection behavior is fully described under the “Smart mouse
mode” rule in:

`features/tui/conversation_hierarchy_and_selection.feature`

Native selection must remain the default. Do not claim auto-copy/character-count
feedback for native terminal selection because the application cannot observe
that selection.

### Agent duration and usage rail

- The old 300-second default cap was removed.
- Calls are unlimited unless `agent-timeout` is configured.
- Time extension/grant messaging uses the real configured duration.
- The SESSION / STEPS / SPENT rail hides when all values are zero.
- Steps use `·/8`, not a misleading em dash.
- Token/spend/status colors are semantic rather than monochromatic red.

### Codex `/operator` guest

- Codex is registered as a context-only guest like Claude/OpenCode/Hermes/Aider.
- Detection uses PATH first.
- GUI-launch fallback probes Codex-specific common paths such as
  `~/.npm-global/bin` and `~/.local/bin` only when PATH lookup fails.
- Codex has shipped brand art and detection/registry tests.

Primary files:

- `internal/operator/registry.go`
- `internal/operator/detect.go`
- `internal/operator/brand.go`
- `internal/operator/codex_resolution_test.go`
- `features/operator/detection.feature`
- `features/handoff/claude_guest.feature`

## Local review binary

Current local command:

```bash
/home/luis/.local/bin/roger version
# rogerai 5.4.6
```

`rogerai` continues to resolve to `roger`.

Latest installed review binary includes the approved pixel Tube Ping. It was
built from the current dirty worktree, so after restart verify the concurrent
domain-migration work has not moved before treating the binary as a clean
release artifact.

Useful backups:

```text
/home/luis/.local/bin/roger.pre-approved-tube-ping-20260729.bak
/home/luis/.local/bin/roger.pre-tube-ping-v2-20260729.bak
/home/luis/.local/bin/roger.pre-tube-ping-20260729.bak
/home/luis/.local/bin/roger.pre-codex-visual-review-20260729.bak
```

To test after reboot:

```bash
cd /home/luis/ai/RogerAI
roger
```

- Inspect the top-left `▟•▙▓` mark.
- Enter AGENT and inspect the three-row reactive pixel mascot.
- Press `z`.
- Confirm the five-row approved hero appears briefly.
- Wait for Ping World and confirm Tube Ping actually walks in the scene while
  classic Ping remains present.
- Resize below `72x20` and confirm the Tube Ping walker disappears cleanly,
  leaving classic Ping.

## Validation snapshot — 2026-07-29

Everything is validated and green in the current dirty worktree:

- `go vet ./...` plus full `go test ./... -count=1`: **PASS**, zero failures.
  The earlier `remoteLinkURL` compile blocker is resolved; the symbol became
  `remoteLinkCode`.
- `make smoke`: **PASS, 30/30**. One `gofmt` drift was fixed first in
  `cmd/rogerai-broker/session_hint_test.go`: comment alignment left over from
  the `.fm` rename.
- `make cover-gate`: **PASS**. Every package is at or above 90%; total coverage
  is 92.1%, and `internal/tui` is 91.8%.
- Web domain tests: **PASS, 4/4 green**.
- Earlier focused Tube Ping/header/AGENT/Ping World tests and
  `go test ./internal/tui -count=1` also passed.

Do not spend the first post-reboot session re-running all of these unless the
worktree has changed. Start with the visual check below, then continue the two
in-progress implementation tracks.

## Resume queue after reboot

Status after restart continuation: **4 tasks — 3 done, 1 in progress**.

- **DONE:** Validate the full Go suite, smoke suite, and coverage gate on the
  dirty worktree.
- **DONE:** Visually verified the approved five-row pixel Tube Ping in the
  installed `roger` binary. The header station bug, title card, and wide-world
  walker match the approved pixel family; classic Ping remains in the world.
- **DONE:** Implemented app-owned smart drag selection in opt-in mouse mode,
  following RED-to-GREEN. Native terminal selection remains the default.
  The implementation and focused tests live in untracked
  `internal/tui/smartselect.go` and `internal/tui/smartselect_test.go`; preserve
  them before editing.
- **IN PROGRESS:** Bind the four new TUI feature contracts to executable Godog
  steps:
  - `features/tui/agent_prompt_fixes.feature`
  - `features/tui/actionable_visual_polish.feature`
  - `features/tui/conversation_hierarchy_and_selection.feature`
  - `features/tui/tube_ping_mascot.feature`

  `agent_prompt_fixes.feature` is now bound and green. The other three remain
  unbound. Do not use catch-all or no-op definitions to make them appear green:
  each scenario must exercise the real model/render path. In particular, the
  Tube Ping breathe/transmit/blink scenarios describe behavior not yet exposed
  by the canonical hero component and must remain honest RED implementation
  work until production behavior and focused tests exist.

## Required resume procedure

1. Read `/home/luis/ai/RogerAI/CLAUDE.md`.
2. Read this handoff and `docs/tube-ping-mascot.md`.
3. Run `git status -sb`; expect a very dirty worktree.
4. Treat the domain-migration work as concurrent work even though its former
   `remoteLinkURL` compile blocker is resolved.
5. Do not revert or overwrite unrelated files.
6. Launch `roger`, visually review the approved mascot on the header, AGENT,
   splash, and walking world.
7. If the mascot still differs from the canonical five lines, inspect the raw
   output and ANSI span count before changing the silhouette.
8. Continue RED-to-GREEN with the opt-in smart-selection work.
9. Wire the newer approved Gherkin files to executable Godog suites where they
   are still only backed by unit tests.
10. Rerun:

```bash
gofmt -w <changed-go-files>
go test ./internal/tui -count=1
go vet ./...
go test ./... -count=1
make smoke
make cover-gate
```

11. Only after all relevant failures are understood, build and install a new
    review binary:

```bash
review_bin=$(mktemp /tmp/roger-v5.4.6-review.XXXXXX)
go build -ldflags '-X main.Version=5.4.6' -o "$review_bin" ./cmd/rogerai
backup=/home/luis/.local/bin/roger.pre-next-tui-review.bak
cp /home/luis/.local/bin/roger "$backup"
install -m 0755 "$review_bin" /home/luis/.local/bin/roger
/home/luis/.local/bin/roger version
```

## Remaining prioritized work

### P1

1. Visually verify the restored approved pixel Tube Ping after reboot.
2. Implement app-owned smart selection in opt-in mouse mode, RED-to-GREEN.
3. Keep native drag-copy as the default and make clipboard failure honest.
4. Bind the four approved TUI Gherkin contracts to executable Godog steps.

### P2

1. Continue cleaning responsive hierarchy at 40/80/120/190 columns.
2. Review AGENT and TUNE IN metadata density after real long coding sessions.
3. Ensure Tube Ping walking never obscures classic Ping, towers, or critical
   world information at supported sizes.
4. Consider a reusable plain-text/export surface for the future rogerai.fm
   launch using the canonical `tubePingRows`.
5. Revisit the `/operator` resident DJ plate so it uses the canonical pixel
   family consistently without increasing layout height.

### Website/company positioning

Address this reviewer risk explicitly:

> 3. Website doesn't read as a company. Parked domain, vague landing page, or
> no clear AI product visible to the reviewer.

Add a clear **Company** section that immediately explains:

- What RogerAI is as a company.
- What product RogerAI provides, who it serves, and the practical problems it
  solves.
- How the product can be evaluated or used, with unmistakable navigation to
  the product, models, documentation, and company information.

Give **RogerAI Labs Research** greater prominence rather than presenting it as
an isolated or secondary page. The research story should explain that RogerAI
Labs makes frontier-capability models easier to access and develops open-source
models for edge, manufacturing, industrial, and personal use cases.

Use **Open Air Waves** and **Wave Models** as concrete parts of that research
and model story. The website should connect the company, research, models, and
real deployments so a reviewer can quickly see an active AI company with a
specific product and mission—not a parked domain or vague landing page.

Continuation status: **DONE and green.** The homepage now has a responsive
Company section connecting the product, audience, evaluation paths, RogerAI
Labs, Open Air Waves, and Wave models. The footer links directly to it.
Contract and executable coverage:

- `features/web/company_positioning.feature`
- `web/test/company-positioning.test.mjs`

The complete web suite passes with 103 tests.

### Splash follow-up — 2026-07-29

The installed Tube Ping debut was reviewed from a real terminal capture and
tightened:

- the detached two-line wordmark/title became one compact
  `ROGER·AI · TUBE PING · ON AIR` lockup;
- the return hint now says `press any key to return`;
- carrier-wave parentheses use the quiet plane instead of competing with the
  bright receiver body;
- blink and symmetric transmit poses are real reusable renderer states, while
  quiet/non-TTY output remains static.

The reviewed binary is installed at `/home/luis/.local/bin/roger` as 5.4.6.
Backup: `/home/luis/.local/bin/roger.pre-splash-lockup-20260729.bak`.

Post-change gates: full Go suite and vet pass; web 103/103; smoke 30/30;
coverage gate passes at 92.0% total and 91.6% for `internal/tui`.

## Guardrails

- The five-line Tube Ping silhouette is a founder choice, not an open design
  prompt.
- Never reintroduce per-glyph ANSI styling for its block body.
- Never remove classic Ping or its existing animation banks.
- Never let the mascot take rows from an active transcript/composer.
- Smart select owns transcript drags by default so release can copy and report
  the character count. `ctrl+o` or `/mouse` immediately restores native terminal
  selection when the operator prefers terminal-owned selection.
- Never report native terminal selection as copied by the app.
- Never restore the 300-second agent timeout as the default.
- Never fabricate operator capabilities: Codex remains context-only unless the
  registry contract is deliberately changed.
- Preserve user and concurrent-agent work in the dirty tree.
