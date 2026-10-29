package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeviceFlowRequestsEmailScope pins the CLI half of the 2026-10-05 founder ruling: the
// device sign-in asks GitHub for the account's email addresses, the same scopes as the web
// sign-in (features/ops/cap_notice_emails.feature).
func TestDeviceFlowRequestsEmailScope(t *testing.T) {
	var scope string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		scope = r.PostForm.Get("scope")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"AAAA-1111","verification_uri":"https://gh/device","interval":5}`))
	}))
	t.Cleanup(srv.Close)
	old := ghDeviceCodeURL
	ghDeviceCodeURL = srv.URL
	t.Cleanup(func() { ghDeviceCodeURL = old })

	_, err := startDeviceFlow("cid")
	require.NoError(t, err)
	require.Equal(t, "read:user user:email", scope)
}
