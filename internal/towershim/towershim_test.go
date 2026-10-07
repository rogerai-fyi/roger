package towershim

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// seams points the package at a fake install: this executable in dir/self, a PATH lookup, and a
// hand-off that records instead of replacing the test process.
func seams(t *testing.T, self string, path func(string) (string, error)) (*bytes.Buffer, *[]string) {
	t.Helper()
	oldExe, oldLook, oldHand, oldErr, oldOS := executable, lookPath, handOff, stderr, goos
	t.Cleanup(func() { executable, lookPath, handOff, stderr, goos = oldExe, oldLook, oldHand, oldErr, oldOS })
	var errb bytes.Buffer
	var got []string
	executable = func() (string, error) { return self, nil }
	lookPath = path
	handOff = func(p string, args, env []string) error { got = append([]string{p}, args...); return nil }
	stderr = &errb
	return &errb, &got
}

func touch(t *testing.T, p string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o755))
	return p
}

func noPath(string) (string, error) { return "", errors.New("not found") }

func TestMainHandsOffWithTheMappedArguments(t *testing.T) {
	dir := t.TempDir()
	self := touch(t, filepath.Join(dir, "roger-tower"))
	roger := touch(t, filepath.Join(dir, "roger"))
	for _, tc := range []struct {
		old  string
		args []string
		want []string
		now  string
	}{
		{"roger-tower", []string{"status", "--dir", "D"}, []string{"tower", "status", "--dir", "D"}, "roger tower"},
		{"roger-tower-local", []string{"--dir", "D"}, []string{"tower", "serve", "--dir", "D"}, "roger tower serve"},
		{"roger-tower-local", []string{"version"}, []string{"tower", "version"}, "roger tower serve"},
		{"roger-tower-local", []string{"--version"}, []string{"tower", "version"}, "roger tower serve"},
	} {
		errb, got := seams(t, self, noPath)
		require.Equal(t, 0, Main(tc.old, tc.args))
		require.Equal(t, append([]string{roger}, tc.want...), *got)
		require.Equal(t, tc.old+" is now `"+tc.now+"`; this name is removed in the next release\n", errb.String())
	}
}

func TestMainFindsRogerOnPathAndNeverItself(t *testing.T) {
	dir := t.TempDir()
	self := touch(t, filepath.Join(dir, "roger-tower"))
	elsewhere := touch(t, filepath.Join(t.TempDir(), "roger"))
	_, got := seams(t, self, func(string) (string, error) { return elsewhere, nil })
	require.Equal(t, 0, Main("roger-tower", []string{"status"}))
	require.Equal(t, elsewhere, (*got)[0], "roger on PATH when none sits beside the shim")

	// A "roger" that is this very file (a copy installed under both names via a hard link) is
	// never chosen.
	link := filepath.Join(dir, "roger")
	require.NoError(t, os.Link(self, link))
	errb, _ := seams(t, self, func(string) (string, error) { return link, nil })
	require.Equal(t, 127, Main("roger-tower", []string{"status"}))
	require.Contains(t, errb.String(), "roger is not installed; install roger, then run `roger tower status`")
}

func TestMainRefusals(t *testing.T) {
	dir := t.TempDir()
	self := touch(t, filepath.Join(dir, "roger-tower"))

	errb, _ := seams(t, self, noPath)
	require.Equal(t, 127, Main("roger-tower-local", []string{"--dir", "D"}))
	require.Contains(t, errb.String(), "then run `roger tower serve`")
	require.NotContains(t, errb.String(), "is now", "no notice when nothing will run")

	errb, _ = seams(t, self, noPath)
	require.Equal(t, 2, Main("roger-towr", nil))
	require.Contains(t, errb.String(), "not a known Tower shim")

	t.Setenv(hopEnv, "1")
	errb, got := seams(t, self, noPath)
	require.Equal(t, 1, Main("roger-tower", []string{"status"}))
	require.Contains(t, errb.String(), "(a loop)")
	require.Empty(t, *got, "a loop never hands off")
}

func TestMainReportsAFailedHandOff(t *testing.T) {
	dir := t.TempDir()
	self := touch(t, filepath.Join(dir, "roger-tower"))
	touch(t, filepath.Join(dir, "roger"))
	errb, _ := seams(t, self, noPath)
	handOff = func(string, []string, []string) error { return errors.New("exec format error") }
	require.Equal(t, 1, Main("roger-tower", []string{"status"}))
	require.Contains(t, errb.String(), "cannot run")
}

func TestWindowsNamesCarryExe(t *testing.T) {
	dir := t.TempDir()
	self := touch(t, filepath.Join(dir, "roger-tower.exe"))
	roger := touch(t, filepath.Join(dir, "roger.exe"))
	_, got := seams(t, self, noPath)
	goos = "windows"
	require.Equal(t, 0, Main("roger-tower.exe", []string{"status"}))
	require.Equal(t, roger, (*got)[0])
	require.True(t, strings.HasSuffix((*got)[0], ".exe"))
}

func TestABesideCandidateThatIsADirectoryIsSkipped(t *testing.T) {
	dir := t.TempDir()
	self := touch(t, filepath.Join(dir, "roger-tower"))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "roger"), 0o755))
	errb, _ := seams(t, self, noPath)
	require.Equal(t, 127, Main("roger-tower", []string{"status"}))
	require.Contains(t, errb.String(), "roger is not installed")
}

// TestMissingRogerWithNoArgumentsNamesTheCommand: `roger-tower` run bare, with roger not
// installed, names `roger tower` instead of indexing past the end of its arguments.
func TestMissingRogerWithNoArgumentsNamesTheCommand(t *testing.T) {
	dir := t.TempDir()
	errb, _ := seams(t, touch(t, filepath.Join(dir, "roger-tower")), noPath)
	require.Equal(t, 127, Main("roger-tower", nil))
	require.Contains(t, errb.String(), "run `roger tower`")
}
