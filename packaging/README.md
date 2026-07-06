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
   fetches), and pushes the **Homebrew cask** and **Scoop manifest** to their repos.
3. **winget** - opens a PR to `microsoft/winget-pkgs` (only when enabled; see below).

Local dry-run (builds everything into `dist/`, publishes nothing):

```sh
TAP_GITHUB_TOKEN=dummy goreleaser release --snapshot --clean --skip=publish
```

## One-time setup (owner action required)

### 1. Homebrew + Scoop repos + token
- [x] `rogerai-fyi/homebrew-tap` created (public, `main`) - cask lands in `Casks/roger.rb`.
- [x] `rogerai-fyi/scoop-bucket` created (public, `main`) - manifest lands in `bucket/roger.json`.
- [ ] Create a token with `contents:write` on **both** repos and add it as secret
  **`TAP_GITHUB_TOKEN`** on the `roger` repo. The default `GITHUB_TOKEN` cannot push
  cross-repo, so this is required. **Owner action** (GitHub won't mint a PAT via CLI):
  1. Fine-grained PAT: https://github.com/settings/tokens?type=beta -> Resource owner
     `rogerai-fyi`, Repository access = only `homebrew-tap` + `scoop-bucket`,
     Repository permissions -> Contents: **Read and write**. (Or a classic PAT with `repo`.)
  2. Store it: `gh secret set TAP_GITHUB_TOKEN --repo rogerai-fyi/roger --body <TOKEN>`

Then users install with:

```sh
brew install --cask rogerai-fyi/tap/roger          # macOS
scoop bucket add rogerai https://github.com/rogerai-fyi/scoop-bucket && scoop install roger  # Windows
```

> Note: it's a Homebrew **cask** (not a formula) because the release ships a prebuilt
> binary - the cask install strips the macOS quarantine xattr so the unsigned binary runs
> without a Gatekeeper prompt. `homebrew-core` (`brew install roger`, no tap prefix) is not
> an option: it requires an OSI-approved license and builds from source; PolyForm Perimeter
> is source-available, not open-source.

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
