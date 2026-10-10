package towercli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// features/join/invite.feature: the owner undoes a join with revoke-client, by the name the client
// joined under (or its key hash).
func TestRevokeClientByName(t *testing.T) {
	dir := t.TempDir() + "/lab"
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	join := func(name string) string {
		_, code, err := st.CreateJoinInvitation(tower.JoinClient, "", nil, time.Hour)
		require.NoError(t, err)
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		ph := hex.EncodeToString(pub)
		fp, _ := st.JoinFingerprint()
		kh := protocol.UserIDFromPubkey(ph)
		_, err = st.ConsumeJoin(tower.JoinRequest{Proof: joincode.Tower.Proof(code, "n", ph, fp), Nonce: "n", PubHex: ph, KeyHash: kh, Name: name})
		require.NoError(t, err)
		return kh
	}
	owner := join("owner-box")
	laptop := join("laptop-x")
	var out bytes.Buffer
	require.NoError(t, run([]string{"revoke-client", "--dir", dir, "laptop-x"}, &out))
	require.Contains(t, out.String(), "laptop-x can no longer use this Tower")
	require.False(t, st.IsAdmitted(laptop))
	require.True(t, st.IsAdmitted(owner))
	require.Error(t, run([]string{"revoke-client", "--dir", dir, "nobody"}, &out), "an unknown client is named, not ignored")
	require.Error(t, run([]string{"revoke-client", "--dir", dir}, &out), "a client is required")
}
