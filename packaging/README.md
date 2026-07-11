# Packaging `roger`

`roger` ships as a single static binary (`CGO_ENABLED=0`, cross-compiled for
`{linux,darwin,windows}/{amd64,arm64}`). Every channel below consumes the GitHub
Release assets that GoReleaser publishes on each `v*` tag.

## How a release flows

`git tag vX.Y.Z && git push origin vX.Y.Z` triggers `.github/workflows/release.yml`:

1. **gate** - the full test suite + `cover-gate` must be green (a release never ships over
   a red gate).
2. **release** - GoReleaser (`.goreleaser.yaml`) cross-compiles, publishes the GitHub
   Release with the raw binaries + `checksums.txt` (the exact names `web/install.sh`
   fetches), and pushes the **Scoop manifest** to its repo.
3. **brew** - renders `Formula/roger.rb` from that release's `checksums.txt` (via
   `scripts/gen-brew-formula.sh`) and pushes it to `rogerai-fyi/homebrew-tap`. This is a
   **formula, not a cask**, and it's NOT done by GoReleaser - see the Homebrew section below.
4. **winget** - opens a PR to `microsoft/winget-pkgs` (only when enabled; see below).

Local dry-run (builds everything into `dist/`, publishes nothing):

```sh
TAP_GITHUB_TOKEN=dummy goreleaser release --snapshot --clean --skip=publish
```

## One-time setup (owner action required)

### 1. Homebrew + Scoop repos + token
- [x] `rogerai-fyi/homebrew-tap` created (public, `main`) - **formula** lands in `Formula/roger.rb`.
- [x] `rogerai-fyi/scoop-bucket` created (public, `main`) - manifest lands in `bucket/roger.json`.
- [x] `TAP_GITHUB_TOKEN` secret set on the `roger` repo (`contents:write` on both tap repos).
  The default `GITHUB_TOKEN` can't push cross-repo, so this is required. To rotate it:
  1. Fine-grained PAT: https://github.com/settings/tokens?type=beta -> Resource owner
     `rogerai-fyi`, Repository access = only `homebrew-tap` + `scoop-bucket`,
     Repository permissions -> Contents: **Read and write**. (Or a classic PAT with `repo`.)
  2. Store it: `gh secret set TAP_GITHUB_TOKEN --repo rogerai-fyi/roger --body <TOKEN>`

Then users install with:

```sh
brew trust rogerai-fyi/homebrew-tap && brew install rogerai-fyi/homebrew-tap/roger   # macOS + Linux
scoop bucket add rogerai https://github.com/rogerai-fyi/scoop-bucket && scoop install roger  # Windows
```

#### Formula, not cask — and why we generate it ourselves
It's a Homebrew **formula** (not a cask): a formula installs the same prebuilt per-arch
binary, **works on Linux Homebrew too** (casks are macOS-only, and providers run on Linux
GPU boxes), and needs no `--cask` flag. Gatekeeper is a non-issue - Homebrew's formula
downloader doesn't set the macOS quarantine xattr and Go ad-hoc-signs darwin binaries, so
the unsigned CLI runs clean (verified: formula install -> `roger version` exits 0).

GoReleaser can't generate it: it **dropped** Homebrew formula support (`brews`) in favour of
casks (goreleaser.com/deprecations#brews). So `scripts/gen-brew-formula.sh` renders the
formula from the release `checksums.txt`, and the `brew` job in `release.yml` pushes it to
the tap on every non-prerelease `v*` tag. To (re)generate by hand for the current release:

```sh
scripts/gen-brew-formula.sh 5.2.1 > Formula/roger.rb   # or pass a local checksums.txt as $2
```

> **The `brew trust` step is unavoidable from a third-party tap.** Homebrew 6+ requires a
> one-time `brew trust` for **any** non-official tap - formula or cask (it's the default via
> `HOMEBREW_REQUIRE_TAP_TRUST`). Only the **official** taps are trusted by default. (After the
> one-liner, bare `brew upgrade roger` / `brew uninstall roger` work, since the tap is then
> trusted.)

The zero-trust upgrade path (`brew install --cask roger`, no tap) is to get into the official
`Homebrew/homebrew-cask` - the same tap `claude-code` uses for a proprietary CLI binary. Not
`homebrew-core` (it needs an OSI license + build-from-source; PolyForm is neither). A ready-to-
submit cask + the live gate status live in [`homebrew-cask/`](homebrew-cask/). Notability is
already cleared (193★ vs a 75★ bar); what's left is the repo hitting 30 days old (~2026-07-23)
and **signing + notarizing the darwin binaries**. Sign the builds, submit once - then keep the
tap formula for Linux / anyone who wants it.

### 2. winget (optional, Windows 11's built-in manager)
winget needs a **first manual submission**, then the workflow keeps it updated:

- [x] `bownux/winget-pkgs` fork created (winget-releaser opens PRs from it).
- [ ] Add secret **`WINGET_TOKEN`** = a classic PAT on the **bownux** account with
  `public_repo` scope: `gh secret set WINGET_TOKEN --repo rogerai-fyi/roger --body <TOKEN>`.
  (The fork owner and the PAT owner must match - both `bownux`.)
- [ ] Bootstrap the package id `RogerAI.Roger` once (after a real release exists):
  ```sh
  wingetcreate new https://github.com/rogerai-fyi/roger/releases/download/vX.Y.Z/roger-windows-amd64.exe
  ```
  Set `PackageIdentifier: RogerAI.Roger`, installer type **portable**. Submit the PR.
- Flip repo **variable** `PUBLISH_WINGET=true`. From then on each release auto-opens an
  update PR (Microsoft moderation runs before it merges; pre-release tags with a `-` are
  skipped).

Users then install with `winget install RogerAI.Roger`.

### 3. Gentoo (Portage)
Hand-maintained (GoReleaser has no Portage support). A `-bin` ebuild lives in
`packaging/gentoo/net-misc/roger-bin/`. It fetches the release binaries directly, so it
works against the live release today (v5.0.1).

A Gentoo overlay is just a directory tree with a few metadata files. The steps below build
one and install `roger` on this machine; the same tree, pushed to git, is what others add.

**A. Build the overlay tree** (from the repo root; `$OV` is where the overlay lives):

```sh
OV=/var/db/repos/roger
sudo mkdir -p "$OV"/{metadata,profiles} "$OV"/net-misc/roger-bin "$OV"/licenses
echo roger        | sudo tee "$OV"/profiles/repo_name
printf 'masters = gentoo\nauto-sync = false\n' | sudo tee "$OV"/metadata/layout.conf
# the ebuild + its metadata
sudo cp packaging/gentoo/net-misc/roger-bin/roger-bin-5.0.1.ebuild \
        packaging/gentoo/net-misc/roger-bin/metadata.xml           "$OV"/net-misc/roger-bin/
# PolyForm isn't in ::gentoo's licenses/, so the overlay must ship the text itself:
sudo cp LICENSE "$OV"/licenses/PolyForm-Perimeter-1.0.0
```

**B. Register the overlay** with Portage:

```sh
sudo tee /etc/portage/repos.conf/roger.conf <<'EOF'
[roger]
location = /var/db/repos/roger
masters = gentoo
auto-sync = no
EOF
```

**C. Generate the Manifest** (downloads the release binaries, records their sha512):

```sh
cd /var/db/repos/roger/net-misc/roger-bin && sudo ebuild roger-bin-5.0.1.ebuild manifest
```

**D. Accept the ~arch keyword + the non-free license, then install:**

```sh
echo 'net-misc/roger-bin ~amd64'                     | sudo tee /etc/portage/package.accept_keywords/roger-bin
echo 'net-misc/roger-bin PolyForm-Perimeter-1.0.0'   | sudo tee /etc/portage/package.license/roger-bin
sudo emerge -av net-misc/roger-bin
roger version   # smoke test
```

**Publish it for others** (optional): put the same `$OV` tree in a git repo (e.g.
`rogerai-fyi/gentoo-overlay`), commit, push. Consumers then run:

```sh
eselect repository add roger git https://github.com/rogerai-fyi/gentoo-overlay.git
emerge --sync roger && emerge net-misc/roger-bin
```

**Bump on each release**: `git mv` the ebuild to `roger-bin-<newver>.ebuild` and re-run
`ebuild ... manifest`.

> **GURU / main `::gentoo` tree are off the table** for the same reason as homebrew-core:
> PolyForm Perimeter is not a free license, so a public community overlay won't accept it.
> A personal overlay is fine. A build-from-source `go-module.eclass` ebuild is possible
> later if desired, but doesn't change the license situation.

## Code signing (deferred, quality-of-life)
Binaries are currently unsigned. Not a blocker for any channel above, but:
- **Windows**: unsigned `.exe` triggers SmartScreen. Fix later with an Authenticode cert or
  Azure Trusted Signing, wired into the GoReleaser build as a `signs`/post-build hook.
- **macOS**: the cask strips quarantine, so a CLI runs fine unsigned. Notarization only
  matters if we ever ship a `.pkg`/`.dmg`/`.app`.
