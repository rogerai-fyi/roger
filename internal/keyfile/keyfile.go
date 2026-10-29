// Package keyfile is the one loader for roger's ed25519 identity keys, node.key and user.key
// (features/packaging/one_binary.feature section 7.5): hex text, 0600, in a 0700 directory.
//
// A damaged key is REFUSED, never replaced. Overwriting it would silently become someone else:
// a different node id, a different signing identity, every admission and earning tied to the
// old key lost. The owner decides - move it aside for a new identity, or restore a backup.
package keyfile

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// damaged is the one refusal every site gives for a key file that is not a key.
func damaged(path string) error {
	return fmt.Errorf("the key at %s is damaged (not a hex-encoded ed25519 private key); roger will not overwrite it: move it aside to create a new identity, or restore it from a backup", path)
}

// Load reads the key at path. A missing file is an error matching fs.ErrNotExist.
func Load(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, damaged(path)
	}
	return ed25519.PrivateKey(raw), nil
}

// Check reports whether the key at path can be used: nil when it is a key or does not exist yet
// (LoadOrCreate will make one), the same refusal LoadOrCreate gives when it is damaged.
func Check(path string) error {
	_, err := Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// LoadOrCreate loads the key at path, or creates and saves a new one when there is none. It
// never overwrites a file that exists.
func LoadOrCreate(path string) (ed25519.PrivateKey, error) {
	priv, err := Load(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return priv, err
	}
	_, priv, err = ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// O_EXCL: a key another process created a moment ago is read, never replaced.
	f, err := createExcl(path)
	if errors.Is(err, fs.ErrExist) {
		return Load(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write([]byte(hex.EncodeToString(priv))); err != nil {
		f.Close()
		return nil, err
	}
	return priv, f.Close()
}

// createExcl creates path for writing only if it does not exist (a seam so a test can lose the
// creation race on purpose).
var createExcl = func(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// stderr and exit are how MustLoadOrCreate refuses (seams for tests).
var (
	stderr io.Writer = os.Stderr
	exit             = os.Exit
)

// MustLoadOrCreate is LoadOrCreate for callers that cannot carry an error: it refuses (prints
// the one message and exits) rather than hand back a key that is not this identity. roger
// checks both keys at startup (Check), so a running process reaches this only if a key is
// damaged mid-run.
func MustLoadOrCreate(path string) ed25519.PrivateKey {
	priv, err := LoadOrCreate(path)
	if err != nil {
		fmt.Fprintln(stderr, "roger: "+err.Error())
		exit(1)
		return nil
	}
	return priv
}
