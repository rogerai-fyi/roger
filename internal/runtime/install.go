package runtime

// GETTING THE RUNTIME ONTO THIS MACHINE: pick, fetch, verify, unpack - once.
//
// Every step is someone else's: Pick chooses, provision.Fetcher downloads and verifies, Unpack
// refuses to be tricked. This file only orders them and makes the whole thing IDEMPOTENT, because
// it runs every time somebody serves a model and the second run must cost nothing.
//
// Two details that are easy to get wrong and are the reason this is its own file:
//   - The tree is unpacked into a STAGING directory and renamed into place only when complete. An
//     interrupted unpack must never leave a half tree at the path a later run trusts.
//   - Completion is recorded with the archive's DIGEST, not a bare "done" file, so a runtime from
//     an older pin is never mistaken for the current one after the manifest moves on.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"rogerai.fm/roger/v6/internal/provision"
)

const verifiedMarker = ".verified-sha256"

// Install makes sure this machine's runtime is on disk under dir and returns the path of the
// server binary inside it. A machine with no runtime gets ErrNoRuntime with its reason.
func Install(ctx context.Context, h Host, dir string, f *provision.Fetcher) (string, Asset, error) {
	a, err := Pick(h)
	if err != nil {
		return "", Asset{}, err
	}
	exe, err := installAsset(ctx, a, dir, f)
	return exe, a, err
}

// Installed reports the server binary for this machine if it is already in place, without
// fetching anything. A screen uses it to say "ready" or "will download N MB first".
func Installed(h Host, dir string) (string, Asset, bool) {
	a, err := Pick(h)
	if err != nil {
		return "", Asset{}, false
	}
	root := treeFor(dir, a)
	exe := filepath.Join(root, a.Exe)
	if isVerified(root, a) && exists(exe) {
		return exe, a, true
	}
	return "", a, false
}

func installAsset(ctx context.Context, a Asset, dir string, f *provision.Fetcher) (string, error) {
	root := treeFor(dir, a)
	exe := filepath.Join(root, a.Exe)
	if isVerified(root, a) && exists(exe) {
		return exe, nil // already here: the second run costs nothing
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	archive := filepath.Join(dir, a.Name)
	if err := f.Fetch(ctx, provision.Request{URL: a.URL, Dest: archive, Bytes: a.Bytes, SHA256: a.SHA256}); err != nil {
		return "", err
	}
	staging := root + ".staging"
	_ = os.RemoveAll(staging)
	if err := Unpack(archive, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	_ = os.RemoveAll(root)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(staging, root); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, verifiedMarker), []byte(a.SHA256), 0o644); err != nil {
		return "", err
	}
	// The archive has served its purpose; tens of megabytes on a Pi's card are worth giving back.
	_ = os.Remove(archive)
	if !exists(exe) {
		return "", ErrNoRuntime
	}
	return exe, nil
}

func treeFor(dir string, a Asset) string { return filepath.Join(dir, a.Tag, a.Platform) }

func isVerified(root string, a Asset) bool {
	b, err := os.ReadFile(filepath.Join(root, verifiedMarker))
	return err == nil && strings.TrimSpace(string(b)) == a.SHA256
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
