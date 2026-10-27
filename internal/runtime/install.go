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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"rogerai.fm/roger/v7/internal/provision"
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
	if exe, ok := installedAt(dir, a); ok {
		return exe, nil // already here: the second run costs nothing
	}
	root := treeFor(dir, a)
	exe := filepath.Join(root, a.Exe)
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
	if a.Libs != nil {
		// The second archive is verified like the first and its files placed beside the server.
		libArchive := filepath.Join(dir, a.Libs.Name)
		if err := f.Fetch(ctx, provision.Request{URL: a.Libs.URL, Dest: libArchive, Bytes: a.Libs.Bytes, SHA256: a.Libs.SHA256}); err != nil {
			_ = os.RemoveAll(staging)
			return "", err
		}
		err := placeBeside(libArchive, staging, filepath.Dir(filepath.Join(staging, a.Exe)))
		_ = os.Remove(libArchive)
		if err != nil {
			_ = os.RemoveAll(staging)
			return "", err
		}
	}
	_ = os.RemoveAll(root)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(staging, root); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, verifiedMarker), []byte(a.pin()), 0o644); err != nil {
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
	return err == nil && strings.TrimSpace(string(b)) == a.pin()
}

// pin is what the verified marker records: the build's digest, and its runtime's when it has one, so
// a change to either pin is never taken as already installed.
func (a Asset) pin() string {
	if a.Libs != nil {
		return a.SHA256 + "+" + a.Libs.SHA256
	}
	return a.SHA256
}

// installedAt is the server binary of build a under dir, when it is in place and verified.
func installedAt(dir string, a Asset) (string, bool) {
	root := treeFor(dir, a)
	exe := filepath.Join(root, a.Exe)
	return exe, isVerified(root, a) && exists(exe)
}

// InstallBuild fetches, verifies and unpacks one chosen build (see Plan), or finds it in place.
func InstallBuild(ctx context.Context, a Asset, dir string, f *provision.Fetcher) (string, error) {
	return installAsset(ctx, a, dir, f)
}

// InstalledBuild is the server binary of build a when it is already in place, fetching nothing.
func InstalledBuild(a Asset, dir string) (string, bool) { return installedAt(dir, a) }

// placeBeside unpacks archive under root and moves every file it holds into dest, flat.
func placeBeside(archive, root, dest string) error {
	tmp := filepath.Join(root, ".libs-staging")
	if err := Unpack(archive, tmp); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// Flattening is where an archive could reach past its own tree: it may never replace a file
	// already there (the server, a library it loads), and never carries a link.
	return filepath.WalkDir(tmp, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a link, and a runtime's libraries are files", ErrUnsafeArchive, d.Name())
		}
		to := filepath.Join(dest, d.Name())
		if _, err := os.Lstat(to); err == nil {
			return fmt.Errorf("%w: %s would replace a file the build already has", ErrUnsafeArchive, d.Name())
		}
		return os.Rename(p, to)
	})
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
