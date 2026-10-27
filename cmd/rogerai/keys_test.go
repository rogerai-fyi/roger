package main

// keys_test.go: `roger keys` in-process against a broker stand-in that records what it was
// sent, and `roger use --key / --save-key`. The full path against the REAL broker binary is
// pinned by cmd/rogerai-broker/key_cli_bdd_test.go (key_guardrails.feature); this covers the
// branches a subprocess cannot show, notably the TTY output order of a mint.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type keysStandIn struct {
	srv   *httptest.Server
	calls []string
	last  map[string]any
}

func newKeysStandIn(t *testing.T, status int) *keysStandIn {
	s := &keysStandIn{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		s.last = nil
		_ = json.Unmarshal(b, &s.last)
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
			return
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"keys":[{"id":"key_a","name":"ci","hint":"...abcd","limit_usd":5,"reset":"weekly"}]}`))
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"key_new","name":"ci","secret":"rog-key_s3cret"}`))
		case http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"key_a","name":"ci","limit_usd":10,"reset":"weekly","disabled":true}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func runKeysOut(t *testing.T, broker string, tty bool, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := runKeys(broker, args, &out, &errb, tty)
	return out.String(), errb.String(), err
}

func TestKeysMintTTYOrder(t *testing.T) {
	s := newKeysStandIn(t, 0)
	out, errOut, err := runKeysOut(t, s.srv.URL, true, "mint", "--name", "ci", "--limit", "5", "--reset", "weekly")
	require.NoError(t, err)
	require.Empty(t, errOut)
	i, j := strings.Index(out, "rog-key_s3cret"), strings.Index(out, keysSecretWarning)
	require.True(t, i >= 0 && j > i, "a TTY gets the secret, then the warning: %q", out)
	require.Equal(t, 1, strings.Count(out, "rog-key_s3cret"))
	require.Equal(t, map[string]any{"name": "ci", "limit_usd": 5.0, "reset": "weekly"}, s.last)
}

func TestKeysMintPipe(t *testing.T) {
	s := newKeysStandIn(t, 0)
	out, errOut, err := runKeysOut(t, s.srv.URL, false, "mint")
	require.NoError(t, err)
	require.Equal(t, "rog-key_s3cret\n", out)
	require.Contains(t, errOut, keysSecretWarning)
	require.Empty(t, s.last, "a mint with no flags names no fields")
}

func TestKeysVerbs(t *testing.T) {
	s := newKeysStandIn(t, 0)
	out, _, err := runKeysOut(t, s.srv.URL, false, "list")
	require.NoError(t, err)
	require.Contains(t, out, "key_a")

	out, _, err = runKeysOut(t, s.srv.URL, false, "set", "key_a", "--limit", "10", "--models", "qwen3-32b,llama-3.3-70b", "--nodes", "", "--disable", "--name", "ci", "--expires", "never")
	require.NoError(t, err)
	require.Contains(t, out, "disabled")
	require.Equal(t, map[string]any{"limit_usd": 10.0, "allowed_models": []any{"qwen3-32b", "llama-3.3-70b"}, "allowed_nodes": []any{},
		"disabled": true, "name": "ci", "expires_at": nil}, s.last)

	_, _, err = runKeysOut(t, s.srv.URL, false, "set", "key_a", "--enable", "--expires", "30d")
	require.NoError(t, err)
	require.Equal(t, false, s.last["disabled"])
	exp, _ := s.last["expires_at"].(string)
	require.True(t, strings.HasSuffix(exp, "Z"), "expiry is RFC 3339 UTC: %q", exp)

	out, _, err = runKeysOut(t, s.srv.URL, false, "rm", "key_a")
	require.NoError(t, err)
	require.Equal(t, "revoked key_a\n", out)
	require.Equal(t, "DELETE /account/keys/key_a", s.calls[len(s.calls)-1])

	for _, args := range [][]string{nil, {"help"}} {
		out, _, err = runKeysOut(t, s.srv.URL, false, args...)
		require.NoError(t, err)
		require.Contains(t, out, "roger keys list")
	}
}

func TestKeysRefusals(t *testing.T) {
	s := newKeysStandIn(t, 0)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"set"}, "usage: roger keys set"},
		{[]string{"set", "--limit", "1"}, "usage: roger keys set"},
		{[]string{"set", "key_a"}, "nothing to change"},
		{[]string{"set", "key_a", "--disable", "--enable"}, "--disable and --enable together"},
		{[]string{"set", "key_a", "--expires", "soon"}, ""},
		{[]string{"mint", "--disable"}, "applies to `roger keys set`"},
		{[]string{"rm"}, "usage: roger keys rm"},
		{[]string{"frob"}, `unknown keys command "frob"`},
	} {
		n := len(s.calls)
		_, _, err := runKeysOut(t, s.srv.URL, false, tc.args...)
		require.Error(t, err, "%v", tc.args)
		require.Contains(t, err.Error(), tc.want, "%v", tc.args)
		require.Equal(t, n, len(s.calls), "%v must refuse before calling the broker", tc.args)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"keys":[]}`)) }))
	defer empty.Close()
	out, _, err := runKeysOut(t, empty.URL, false, "list")
	require.NoError(t, err)
	require.Contains(t, out, "no keys yet")

	denied := newKeysStandIn(t, http.StatusUnauthorized)
	for _, args := range [][]string{{"list"}, {"mint"}, {"set", "key_a", "--limit", "1"}, {"rm", "key_a"}} {
		_, _, err := runKeysOut(t, denied.srv.URL, false, args...)
		require.ErrorContains(t, err, "log in first - run `roger login`", "%v", args)
	}
}

func TestUseKeyFlags(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBrokerEmpty(t), User: "u"}
	require.ErrorContains(t, cmdUse(cfg, []string{"m1", "--key", "sk-not-ours"}), "--key takes an account key")
	require.ErrorContains(t, cmdUse(cfg, []string{"m1", "--save-key"}), "--save-key needs --key")

	require.NoError(t, cmdUse(cfg, []string{"m1", "--key", "rog-key_abc"}))
	require.Empty(t, loadConfig().UseKey, "a --key is never written without --save-key")

	require.NoError(t, cmdUse(cfg, []string{"m1", "--key", "rog-key_abc", "--save-key"}))
	require.Equal(t, "rog-key_abc", loadConfig().UseKey)
	require.NoError(t, cmdUse(loadConfig(), []string{"m1"}), "a saved key is used by a later run")
}

func TestUseKeySources(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config{Broker: fakeBrokerEmpty(t), User: "u"}
	withStdin := func(s string) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		_, _ = w.WriteString(s)
		w.Close()
		prev := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = prev })
	}
	withStdin("rog-key_fromstdin\n")
	require.NoError(t, cmdUse(cfg, []string{"m1", "--key", "-", "--save-key"}))
	require.Equal(t, "rog-key_fromstdin", loadConfig().UseKey, "--key - reads the key from stdin")
	withStdin("")
	require.ErrorContains(t, cmdUse(cfg, []string{"m1", "--key", "-"}), "read no key from stdin")

	t.Setenv("ROGER_KEY", "not-a-key")
	require.ErrorContains(t, cmdUse(cfg, []string{"m1"}), "--key takes an account key")
	t.Setenv("ROGER_KEY", "rog-key_fromenv")
	require.NoError(t, cmdUse(cfg, []string{"m1", "--save-key"}))
	require.Equal(t, "rog-key_fromenv", loadConfig().UseKey, "--save-key keeps a ROGER_KEY key")
	t.Setenv("ROGER_KEY", "")

	require.NoError(t, cmdUse(loadConfig(), []string{"--forget-key"}), "--forget-key needs no model")
	require.Empty(t, loadConfig().UseKey)
	require.NoError(t, cmdUse(loadConfig(), []string{"--forget-key"}), "forgetting with nothing saved is fine")
	require.NoError(t, cmdUse(loadConfig(), []string{"m1", "--forget-key"}), "--forget-key with a model then tunes in")
}

func TestUseKeyNoteAndTransport(t *testing.T) {
	require.Equal(t, "saved key ...wxyz in use (roger use --forget-key to clear)", useKeyNote("rog-key_wxyz", "rog-key_wxyz", false))
	require.Equal(t, "account key ...wxyz from --key", useKeyNote("rog-key_wxyz", "", true))
	require.Equal(t, "account key ...wxyz from ROGER_KEY", useKeyNote("rog-key_wxyz", "rog-key_other", false))
	for _, n := range []string{useKeyNote("rog-key_secretwxyz", "", true)} {
		require.NotContains(t, n, "secret", "the note shows the hint, never the key")
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := cmdUse(config{Broker: "http://broker.example", User: "u"}, []string{"m1", "--key", "rog-key_abc"})
	require.ErrorContains(t, err, "only over https")
}

func TestSaveKeyOnlyAfterTransportAndForgetWording(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := cmdUse(config{Broker: "http://broker.example", User: "u"}, []string{"m1", "--key", "rog-key_abc", "--save-key"})
	require.ErrorContains(t, err, "only over https")
	require.Empty(t, loadConfig().UseKey, "a key refused for its transport is never saved")

	stdout := func(f func()) string {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		prev := os.Stdout
		os.Stdout = w
		f()
		os.Stdout = prev
		w.Close()
		b, _ := io.ReadAll(r)
		return string(b)
	}
	cfg := config{Broker: fakeBrokerEmpty(t), User: "u"}
	out := stdout(func() { require.NoError(t, cmdUse(cfg, []string{"--forget-key"})) })
	require.Contains(t, out, "no key was saved")
	require.NoError(t, cmdUse(cfg, []string{"m1", "--key", "rog-key_abc", "--save-key"}))
	out = stdout(func() { require.NoError(t, cmdUse(loadConfig(), []string{"--forget-key"})) })
	require.Contains(t, out, "forgot the saved key")
}
