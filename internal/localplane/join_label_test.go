package localplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// features/join/invite.feature: the join answer names the Tower ("joined the Tower lab at ..."),
// and the owner sees the join as it happens, with how to undo it.
func TestAJoinNamesTheTowerAndTheOwnerSeesIt(t *testing.T) {
	st := standaloneState(t)
	srv := New(st)
	srv.SetLabel("lab")
	var logged []string
	srv.logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	_, code, err := st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	n := protocol.NewNonce()
	rec := doJoin(srv, priv, joinBody(t, st, priv, code, n, "client", "laptop-x", nil), n)
	require.Equal(t, http.StatusOK, rec.Code)
	var ans JoinAnswer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ans))
	require.Equal(t, "lab", ans.Tower)

	kh := protocol.UserIDFromPubkey(hexPub(priv.Public().(ed25519.PublicKey)))
	out := strings.Join(logged, "\n")
	require.Contains(t, out, "laptop-x joined (key "+kh[:4]+"..."+kh[len(kh)-2:]+")")
	require.Contains(t, out, "undo with: roger tower revoke-client laptop-x")
}
