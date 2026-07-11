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

  version "5.2.2"
  sha256 arm:          "326d73388762bb9de4d714ac3c96f940741be030d828c61a1512acdafd137c70",
         x86_64:       "6cfdcfd5162d6890ffa28b2560a255e338a1a5070474470759df32c6366e6a3f",
         arm64_linux:  "a3511fd8ad9a71a18f4b4510307b601441b5fb67897262940849ea8b395e9795",
         x86_64_linux: "dfaca814a924bc758ec6006122af993d53580b5d2c2bfdecd6acc82113c18713"

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
