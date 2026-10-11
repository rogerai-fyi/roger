package localplane

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/tower"
)

// features/join/invite.feature, "A station's model list follows what it actually serves": a
// station's poll carries what it serves now, and the Tower routes that from this poll on, within
// what the owner allowed.
func TestAStationPollReportsWhatItServes(t *testing.T) {
	st, err := tower.Init(t.TempDir(), tower.ModeStandalone)
	require.NoError(t, err)
	inv, code, err := st.CreateInvitation("u_op", time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, "u_op")
	require.NoError(t, err)
	priv := newKeyPair()
	_, err = st.AttachStation("kitchen-pi", keyHashOf(priv), []string{"qwen3-8b", "gpt-oss-20b"})
	require.NoError(t, err)

	srv := New(st)
	srv.pollTimeout = 50 * time.Millisecond
	h := httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	c := &http.Client{Timeout: 5 * time.Second}

	poll := func(body string) int {
		req, err := signedPost(h.URL, "/local/poll", []byte(body), priv)
		require.NoError(t, err)
		code, _, err := doBody(c, req)
		require.NoError(t, err)
		return code
	}
	models := func() []string {
		sts, err := st.Stations()
		require.NoError(t, err)
		require.Len(t, sts, 1)
		return sts[0].Models
	}

	// It stopped serving qwen3-8b: the Tower routes only gpt-oss-20b from this poll on.
	require.Equal(t, http.StatusNoContent, poll(`{"streams":true,"models":["gpt-oss-20b"]}`))
	require.Equal(t, []string{"gpt-oss-20b"}, models())

	// A report never widens past the models the owner attached it with.
	require.Equal(t, http.StatusNoContent, poll(`{"streams":true,"models":["gpt-oss-20b","llama-70b"]}`))
	require.Equal(t, []string{"gpt-oss-20b"}, models())

	// An older station sends no list: what the Tower routes is unchanged.
	require.Equal(t, http.StatusNoContent, poll(`{"streams":true}`))
	require.Equal(t, []string{"gpt-oss-20b"}, models())
}
