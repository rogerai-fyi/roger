# CONFIG DURABILITY — the ~/.config/rogerai/config.json read-modify-write is lossy (founder-hit:
# an old binary silently dropped share_voices). Two mechanisms, both live at launch:
#   1. FORWARD-COMPAT DROP: loadConfig (cmd/rogerai/main.go:194) unmarshals the file into the
#      typed `config` struct; saveConfig (main.go:208) marshals THAT struct back. Any JSON key not
#      in the struct is dropped on the next save. A binary that predates a key (a downgrade, or a
#      mixed toolchain during the v5.0.0 rollout) erases it.
#   2. MULTI-WRITER RACE: 13 call sites do loadConfig -> mutate -> saveConfig on the WHOLE struct,
#      across SEPARATE processes (the TUI's SaveVoice/SavePrice/SaveStation/SaveCompact hooks, plus
#      one-off CLI commands: login, config set, drphil, onboard). Two overlapping cycles => last
#      writer wins => the other process's field is lost. saveConfig also writes NON-ATOMICALLY
#      (os.WriteFile), so a crash mid-write can truncate/corrupt the file.
#
# APPROVED by the founder 2026-07-02. First slice SHIPPED: the isChatShare guard
# (shouldPersistShareUpstream in cmd/rogerai/main.go + share_upstream_persist_test.go) stops a
# voice/stt share from clobbering the saved CHAT share.upstream (the acute founder-hit incident,
# audit #15). The full C1-C5 durability of saveConfig below is the remaining, separately-tracked
# implementation; this file is the founder-approved source-of-truth spec for it.
#
# DECISIONS PINNED BY THIS SPEC:
#   C1  saveConfig PRESERVES unknown keys: a key present on disk but absent from the struct
#       survives a save. (Impl: a `map[string]json.RawMessage` catch-all merged on write, or a
#       re-read-and-overlay-changed-fields merge — the reviewer picks the leaner one.)
#   C2  saveConfig writes ATOMICALLY: write a temp file in the same dir, fsync, rename over the
#       target — a crash mid-write never leaves a truncated/corrupt config.
#   C3  a concurrent writer does not clobber another process's field: the save re-reads the
#       current on-disk JSON and overlays ONLY the fields this process changed (merge), rather
#       than blindly rewriting a stale in-memory whole-struct snapshot. (A cross-process advisory
#       lock around read-modify-write is an acceptable alternative if simpler.)
#   C4  a corrupt/half-written config.json on load degrades to defaults + a preserved backup,
#       never a crash or a silent wipe of the user's real settings.
#   C5  no behavior change for the common single-writer path: an ordinary save round-trips every
#       known field byte-identically to today.

Feature: config.json durability
  As an operator running the TUI, headless shares, and one-off CLI commands together
  I want config saves to preserve every setting across versions and concurrent writers
  So that an upgrade, a downgrade, or two commands at once never lose my configuration

  # ── C1: forward-compat — unknown keys survive ──────────────────────────────────────────

  Scenario: a key this binary does not know is preserved across a save
    Given a config.json containing a "future_feature" key this binary has no field for
    When the binary loads the config, changes the broker url, and saves
    Then the written config still contains "future_feature" with its original value
    And the broker url reflects the change

  Scenario: the exact founder regression - a newer key survives an unrelated save
    Given a config.json with a populated "share_voices" section
    When a command that only edits "share_prices" loads and saves the config
    Then "share_voices" is still present and unchanged

  # ── C2: atomic write ───────────────────────────────────────────────────────────────────

  Scenario: a save is atomic
    When the config is saved
    Then the target file is replaced by an atomic rename from a temp file in the same directory
    And no reader ever observes a partially written config

  # ── C3: concurrent writers do not clobber each other ───────────────────────────────────

  Scenario: two processes editing different fields both persist
    Given process A loads the config to change compact mode
    And process B loads the same config to change a per-model price
    When both save (B after A)
    Then the final config has BOTH A's compact-mode change AND B's price change

  # ── C4: corrupt file degrades safely ───────────────────────────────────────────────────

  Scenario: a truncated config.json does not crash or wipe settings
    Given a config.json that is truncated mid-object (invalid JSON)
    When the binary loads the config
    Then it falls back to defaults without crashing
    And the unreadable file is preserved as a backup rather than overwritten blindly

  # ── C5: single-writer round-trip is unchanged ──────────────────────────────────────────

  Scenario: an ordinary save round-trips every known field
    Given a config with broker, user, station, share, share_prices, share_voices, and compact set
    When it is loaded and saved with no change
    Then every field round-trips byte-identically to before
