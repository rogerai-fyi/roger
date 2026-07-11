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
brew install --cask roger      # no tap, no trust — official cask
```

Keep the tap formula around for people who want it, and for any platform the cask doesn't cover.

## Gates — run the exact check Homebrew runs on a new submission

```sh
brew audit --cask --new roger
```

Current status (re-run before submitting — these move over time):

- ✅ **Notability** — bar is **≥75 stars OR ≥30 forks OR ≥30 watchers**. roger has **193
  stars**, so this passes.
- ⏳ **Repo age** — *"GitHub repository too new (<30 days old)"*. homebrew-cask wants the
  upstream repo ≥30 days old. roger was created 2026-06-23, so it clears on **~2026-07-23**.
  Nothing to do but wait.
- ❌ **macOS code signing** — *"Signature verification failed"*. homebrew-cask requires the
  macOS binary to be **Developer-ID signed and notarized**; ours are only ad-hoc signed.
  The release pipeline is now **wired for this** — `.goreleaser.yaml` has a `notarize:` block
  (GoReleaser's cross-platform, Quill-backed signer; runs on the Linux runner) that stays off
  until the signing secrets exist. So clearing this gate is **config, not code**: add the five
  `MACOS_*` repo secrets per [Code signing in `../README.md`](../README.md#code-signing), cut a
  release, and the darwin binaries ship signed + notarized. Then re-run
  `brew audit --cask --new roger` — the signature error clears.

   (Signing is *only* needed for the cask. The formula runs the ad-hoc-signed binary fine —
   Homebrew doesn't quarantine formula downloads — which is why the tap works today.)

## Submitting (once signing lands and the repo is ≥30 days old)

1. Refresh the four sha256s + version in [`roger.rb`](roger.rb) for the target release. The
   generator prints the same SHAs the cask needs:
   ```sh
   scripts/gen-brew-formula.sh <version>     # read the sha256 values from its output
   ```
2. `brew style roger.rb` and `brew audit --cask --new roger` — both must be clean.
3. Fork `Homebrew/homebrew-cask`, drop the file at `Casks/r/roger.rb`, open a PR. Homebrew's
   CI re-runs the audit; a maintainer reviews.
4. After merge: `brew install --cask roger` works with no tap and no trust. Version bumps are
   then handled by Homebrew's autobump bot (the `livecheck` block drives it), so this stub is
   a one-time submission — not something the release workflow pushes.
