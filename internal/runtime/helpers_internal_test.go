package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/provision"
)

// A machine whose disk cannot hold the runtime is told so before a byte moves.
func TestInstallRefusesWhenTheRuntimeWillNotFitOnDisk(t *testing.T) {
	f := &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 1, true }}
	_, a, err := Install(context.Background(), Host{OS: "linux", Arch: "amd64", GlibcMajor: 2, GlibcMinor: 42}, t.TempDir(), f)
	require.ErrorIs(t, err, provision.ErrNoSpace)
	require.Equal(t, "ubuntu-x64", a.Platform, "and it says which runtime it was")
}

func TestTheBaseAddressLosesOnlyATrailingV1(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:9/v1": "http://127.0.0.1:9", "http://127.0.0.1:9": "http://127.0.0.1:9", "/v1": "/v1", "": "",
	} {
		require.Equal(t, want, trimV1(in), in)
	}
}

func TestAFailureCarriesTheRuntimesLastWords(t *testing.T) {
	s := &Server{}
	require.Empty(t, s.tailForError(), "nothing said, nothing quoted")
	s.Note("loading model")
	s.Note("error: out of memory")
	require.Equal(t, " - it said: error: out of memory", s.tailForError())
}

// The readiness check asks what the runtime serves, with the key, and a bad answer is an error.
func TestWhatTheRuntimeServesIsAskedWithItsKey(t *testing.T) {
	var mode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch mode {
		case "broken":
			fmt.Fprint(w, "{not json")
		default:
			fmt.Fprint(w, `{"data":[{"id":"tiny"},{"id":"other"}]}`)
		}
	}))
	defer srv.Close()
	ids, err := modelsListed(context.Background(), srv.URL+"/v1", "k")
	require.NoError(t, err)
	require.Equal(t, []string{"tiny", "other"}, ids)
	_, err = modelsListed(context.Background(), srv.URL+"/v1", "wrong")
	require.ErrorContains(t, err, "status 401")
	mode = "broken"
	_, err = modelsListed(context.Background(), srv.URL+"/v1", "k")
	require.Error(t, err)
	_, err = modelsListed(context.Background(), "http://127.0.0.1:1/v1", "k")
	require.Error(t, err, "nobody listening is an error, not an empty list")
}

func TestAFileThatCannotBeWrittenSaysSo(t *testing.T) {
	dir := t.TempDir()
	block := filepath.Join(dir, "afile")
	require.NoError(t, os.WriteFile(block, []byte("x"), 0o644))
	require.Error(t, writeFile(filepath.Join(block, "under", "f"), strings.NewReader("w"), 0o644),
		"a parent that is a file cannot hold a directory")
	require.Error(t, writeFile(dir, strings.NewReader("w"), 0o644), "a directory is not overwritten as a file")
	p := filepath.Join(dir, "sub", "ok")
	require.NoError(t, writeFile(p, strings.NewReader("weights"), 0o600))
	b, _ := os.ReadFile(p)
	require.Equal(t, "weights", string(b))
}

func TestTheFittedContextIsReadOnlyFromAWholeFlag(t *testing.T) {
	require.Equal(t, 8192, fittedCtx("-c 8192 -ngl -1"))
	require.Equal(t, 4096, fittedCtx("-ngl -1 -c 4096\n"))
	require.Zero(t, fittedCtx("-ngl -1"), "no -c, no context")
	require.Zero(t, fittedCtx("-ngl -1 -c"), "a dangling flag is not a number")
	require.Zero(t, fittedCtx("-c lots"))
}
