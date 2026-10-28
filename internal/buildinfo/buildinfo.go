// Package buildinfo holds the one version every roger binary reports (features/packaging/
// one_binary.feature, "one version"). A release stamps it with
//
//	-ldflags "-X rogerai.fm/roger/v7/internal/buildinfo.Version=<version>"
//
// for roger (and so `roger tower` and the one-release shims, which hand off to roger) and for
// rogerai-broker alike. An unstamped build (go install, go build) reports the release this
// source is, so it still compares correctly against the newest release.
package buildinfo

// Version is the build's version; the release pipeline overrides it at link time.
var Version = "7.0.0"
