# Homebrew cask — the zero-trust upgrade path

Our tap ([`rogerai-fyi/homebrew-tap`](https://github.com/rogerai-fyi/homebrew-tap)) ships a
**formula** that works today on macOS + Linux. Its one cost is a one-time `brew trust`, which
Homebrew 6 requires for **any** third-party tap.

The only way to drop that trust step is to live in an **official** Homebrew tap, which is
trusted by default. `homebrew-core` is out (it needs an OSI license + build-from-source;
PolyForm Perimeter is neither). But **`Homebrew/homebrew-cask`** accepts prebuilt, proprietary
/ source-available binaries — it's exactly where `claude-code` lives — and a modern cask can
cover macOS **and** Linux (see the `os macos:/linux:` + `*_linux` shas in [`roger.rb`](roger.rb)).

So when roger clears the two gates below, submit [`roger.rb`](roger.rb) to homebrew-cask and
users get:

```sh
brew install roger      # no tap, no trust - official cask
```

Keep the tap formula around for people who want it, and for any platform the cask doesn't cover.

## Gates - run the exact check Homebrew runs on a new submission

```sh
brew audit --cask --new roger
```

You do not need a Mac for this: the `cask-audit` workflow (`.github/workflows/cask-audit.yml`)
stages this stub in a throwaway tap on a macOS runner and runs `brew style`, the audit, and
a real `brew install` + `roger version`. Run it from the Actions tab (workflow_dispatch); it
also runs on any PR that touches this directory. Its job summary lists the gate results.

Status as of 2026-10-23, from the workflow's first run against the v6.13.0 stub (`brew style`
clean, `brew install` + `roger version` worked, and the audit reported exactly one problem,
the signature). Re-run before submitting - these move over time:

- ✅ **Notability** - bar is **≥75 stars OR ≥30 forks OR ≥30 watchers**. roger had **176
  stars** on 2026-10-23, so this passes.
- ✅ **Repo age** - homebrew-cask wants the upstream repo ≥30 days old. roger was created
  2026-06-23; cleared 2026-07-23.
- ❌ **macOS code signing** - *"Signature verification failed"*. homebrew-cask requires the
  macOS binary to be **Developer-ID signed and notarized**; ours are only ad-hoc signed.
  The release pipeline is **wired for this** - `.goreleaser.yaml` has a `notarize:` block
  (GoReleaser's cross-platform, Quill-backed signer; runs on the Linux runner) that stays off
  until the signing secrets exist. So clearing this gate is **config, not code**: add the five
  `MACOS_*` repo secrets per [Code signing in `../README.md`](../README.md#code-signing), cut a
  release, and the darwin binaries ship signed + notarized. Then re-run the audit - the
  signature error clears.

   (Signing is *only* needed for the cask. The formula runs the ad-hoc-signed binary fine -
   Homebrew doesn't quarantine formula downloads - which is why the tap works today.)

## Submitting (once signing lands and the repo is ≥30 days old)

1. Regenerate [`roger.rb`](roger.rb) for the target release. It is generated, never hand-edited:
   ```sh
   scripts/gen-brew-formula.sh --cask <version> "" packaging/homebrew-cask/roger.rb
   ```
2. `brew style roger.rb` and `brew audit --cask --new roger` — both must be clean.
3. Fork `Homebrew/homebrew-cask`, drop the file at `Casks/r/roger.rb`, open a PR. Homebrew's
   CI re-runs the audit; a maintainer reviews.
4. After merge: `brew install roger` works with no tap and no trust (no `--cask` flag needed:
   with no homebrew-core formula named `roger`, `brew install` falls through to the cask). Version bumps are
   then handled by Homebrew's autobump bot (the `livecheck` block drives it), so this stub is
   a one-time submission — not something the release workflow pushes.
