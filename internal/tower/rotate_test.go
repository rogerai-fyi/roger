package tower

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tower_identity.feature section 3: rotating the Tower's key changes the fingerprint it serves
// under, and every outstanding invite stops working (it names the old fingerprint).
func TestRotateTLSKeyChangesTheFingerprintAndVoidsInvites(t *testing.T) {
	st, err := Init(t.TempDir()+"/lab", ModeStandalone)
	require.NoError(t, err)
	before, err := st.JoinFingerprint()
	require.NoError(t, err)
	id1, _ := st.TowerID, ""
	_, _, err = st.CreateJoinInvitation(JoinClient, "", nil, time.Hour)
	require.NoError(t, err)
	require.Len(t, st.LiveInvitations(), 1)

	after, voided, err := st.RotateTLSKey()
	require.NoError(t, err)
	require.NotEqual(t, before, after, "a rotated key serves under a new fingerprint")
	got, err := st.JoinFingerprint()
	require.NoError(t, err)
	require.Equal(t, after, got)
	require.Equal(t, 1, voided)
	require.Empty(t, st.LiveInvitations(), "an invite naming the old fingerprint never works again")
	require.Equal(t, id1, st.TowerID, "the Tower's identity key is untouched")

	reopened, err := Open(st.Dir())
	require.NoError(t, err)
	got, err = reopened.JoinFingerprint()
	require.NoError(t, err)
	require.Equal(t, after, got, "the new key is on disk")
}

func TestJoinedClientIsKnownByName(t *testing.T) {
	st, err := Init(t.TempDir()+"/lab", ModeStandalone)
	require.NoError(t, err)
	require.Equal(t, "", st.ClientName("nobody"))
	adm := admitByJoin(t, st, "laptop-x")
	require.Equal(t, "laptop-x", st.ClientName(adm))
}

// admitByJoin joins a fresh client key under name through a bearer invite and returns its hash.
func admitByJoin(t *testing.T, st *State, name string) string {
	t.Helper()
	_, code, err := st.CreateJoinInvitation(JoinClient, "", nil, time.Hour)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Proof: joinProof(st, code, "n", pub), Nonce: "n", PubHex: pub, KeyHash: kh, Name: name})
	require.NoError(t, err)
	return kh
}
