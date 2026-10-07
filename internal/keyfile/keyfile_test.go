package keyfile

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateCreatesOnceAndLoadsAfter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "node.key")
	require.NoError(t, Check(p), "a key that does not exist yet is fine")
	a, err := LoadOrCreate(p)
	require.NoError(t, err)
	b, err := LoadOrCreate(p)
	require.NoError(t, err)
	require.Equal(t, a, b, "the second load reads the key the first created")
	fi, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(filepath.Dir(p))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	require.NoError(t, Check(p))
}

func TestDamagedKeysAreRefused(t *testing.T) {
	for name, content := range map[string]string{
		"not hex":  "zz-not-hex",
		"too long": hex.EncodeToString(make([]byte, ed25519.PrivateKeySize+1)),
		"empty":    "",
	} {
		p := filepath.Join(t.TempDir(), "user.key")
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		_, err := LoadOrCreate(p)
		require.ErrorContains(t, err, p, name)
		require.ErrorContains(t, err, "will not overwrite it", name)
		got, _ := os.ReadFile(p)
		require.Equal(t, content, string(got), name+": never overwritten")
	}
}

func TestLoadOrCreateUnreadableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err := LoadOrCreate(filepath.Join(file, "node.key")) // its parent is a file, not a directory
	require.Error(t, err)
}

func TestMustLoadOrCreateRefusesADamagedKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.key")
	require.NoError(t, os.WriteFile(p, []byte("damaged"), 0o600))
	var code int
	var msg bytes.Buffer
	oldExit, oldErr := exit, stderr
	exit, stderr = func(c int) { code = c }, &msg
	defer func() { exit, stderr = oldExit, oldErr }()
	require.Nil(t, MustLoadOrCreate(p))
	require.Equal(t, 1, code)
	require.Contains(t, msg.String(), p)

	ok := filepath.Join(t.TempDir(), "user.key")
	require.NotNil(t, MustLoadOrCreate(ok))
}

// Two processes creating the same key at once: the one that loses the race reads the winner's
// key rather than replacing it.
func TestLoadOrCreateLosingTheCreationRaceReadsTheWinner(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.key")
	_, winner, _ := ed25519.GenerateKey(nil)
	old := createExcl
	createExcl = func(path string) (*os.File, error) {
		// the other process creates it first
		require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(winner)), 0o600))
		return old(path)
	}
	defer func() { createExcl = old }()
	got, err := LoadOrCreate(p)
	require.NoError(t, err)
	require.Equal(t, ed25519.PrivateKey(winner), got)
}

func TestLoadOrCreateWriteFailure(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.key")
	old := createExcl
	createExcl = func(path string) (*os.File, error) {
		f, err := old(path)
		if err != nil {
			return nil, err
		}
		f.Close()
		return os.Open(path) // read-only: the write fails
	}
	defer func() { createExcl = old }()
	_, err := LoadOrCreate(p)
	require.Error(t, err)
}

func TestLoadOfADirectoryIsAnError(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadOrCreate(dir)
	require.Error(t, err, "a path that cannot be read as a file is not a key")
	require.Error(t, Check(dir))
}

func TestLoadOrCreateCreateFailure(t *testing.T) {
	old := createExcl
	createExcl = func(string) (*os.File, error) { return nil, os.ErrPermission }
	defer func() { createExcl = old }()
	_, err := LoadOrCreate(filepath.Join(t.TempDir(), "node.key"))
	require.ErrorIs(t, err, os.ErrPermission)
}
