# Submission stub for the OFFICIAL Homebrew cask tap (Homebrew/homebrew-cask).
# This is NOT our tap's file and NOT installed from here — our tap ships a FORMULA
# (rogerai-fyi/homebrew-tap, Formula/roger.rb). This cask is the artifact you PR into
# Homebrew/homebrew-cask once roger is notable enough (see this dir's README.md), so macOS +
# Linux users get a true zero-trust `brew install --cask roger` with no third-party tap.
#
# Modeled on Homebrew/homebrew-cask Casks/c/claude-code.rb (a proprietary CLI binary cask
# with Linux variants). Refresh the four sha256s from a release's checksums.txt:
#   scripts/gen-brew-formula.sh <version>        # prints the same SHAs this cask needs
# `version` is Ruby interpolation; the URL resolves to v<version>/roger-<os>-<arch>.
cask "roger" do
  arch arm: "arm64", intel: "amd64"
  os macos: "darwin", linux: "linux"

  version "5.2.1"
  sha256 arm:          "a2b5a7f9647d0131b4d4e193ac28dcd7e0c5e3a93a65e9cf0d4c9ec134ced27e",
         x86_64:       "5ca0d5d027e33e23d33ac36a5d87e9d4f3771210c605a5b3e1a556e5ffc5d757",
         arm64_linux:  "f22771c61b8b352eda1c9e86bc61bffcd23f0ff2f68e65cb05bd50c5645a691a",
         x86_64_linux: "a0f63dc5df6443066cfff798c0f25aeb386e73fd35c56df04de3dc61abbd671b"

  url "https://github.com/rogerai-fyi/roger/releases/download/v#{version}/roger-#{os}-#{arch}",
      verified: "github.com/rogerai-fyi/roger/"
  name "RogerAI"
  desc "Two-way radio for GPUs: consume and share LLM/voice models over the broker"
  homepage "https://rogerai.fyi/"

  livecheck do
    url :url
    strategy :github_latest
  end

  binary "roger-#{os}-#{arch}", target: "roger"

  # roger persists to <UserConfigDir>/rogerai and <UserCacheDir>/rogerai — clean both on
  # each supported OS. Confirm against the running build before submitting.
  zap trash: [
    "~/.cache/rogerai",                        # Linux (XDG cache)
    "~/.config/rogerai",                       # Linux (XDG config)
    "~/Library/Application Support/rogerai",   # macOS config
    "~/Library/Caches/rogerai",                # macOS cache
  ]
end
