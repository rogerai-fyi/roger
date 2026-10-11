package tower

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
)

func pairedKey(t *testing.T) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return protocol.UserIDFromPubkey(hex.EncodeToString(pub))
}

// features/join/approve.feature: an approved request admits the client's key (or attaches the
// Station with its self-reported models), once, with no code.
func TestAdmitPairedAdmitsAClientOnce(t *testing.T) {
	st, err := Init(t.TempDir()+"/lab", ModeStandalone)
	require.NoError(t, err)
	kh := pairedKey(t)
	res, err := st.AdmitPaired(JoinClient, "laptop-x", kh, nil)
	require.NoError(t, err)
	require.False(t, res.AlreadyMember)
	require.True(t, st.IsAdmitted(kh))
	require.Equal(t, "laptop-x", st.ClientName(kh))
	again, err := st.AdmitPaired(JoinClient, "laptop-x", kh, nil)
	require.NoError(t, err)
	require.True(t, again.AlreadyMember)
}

func TestAdmitPairedAttachesAStation(t *testing.T) {
	st, err := Init(t.TempDir()+"/lab", ModeStandalone)
	require.NoError(t, err)
	kh := pairedKey(t)
	res, err := st.AdmitPaired(JoinStation, "kitchen-pi", kh, []string{"qwen3-8b"})
	require.NoError(t, err)
	require.Equal(t, []string{"qwen3-8b"}, res.Station.Models)
}

func TestAdmitPairedNeedsAStandaloneTowerAndAKey(t *testing.T) {
	st, err := Init(t.TempDir()+"/lab", ModeStandalone)
	require.NoError(t, err)
	_, err = st.AdmitPaired(JoinClient, "x", "", nil)
	require.Error(t, err)
}

// An approval on a Tower that is not standalone, or naming no key, admits nothing.
func TestAdmitPairedRefusesWhatItCannotAdmit(t *testing.T) {
	joined, err := Init(t.TempDir()+"/hub", ModeJoined)
	require.NoError(t, err)
	_, err = joined.AdmitPaired(JoinClient, "laptop-x", pairedKey(t), nil)
	require.ErrorIs(t, err, ErrNotStandalone)

	st, err := Init(t.TempDir()+"/lab", ModeStandalone)
	require.NoError(t, err)
	_, err = st.AdmitPaired(JoinClient, "laptop-x", "", nil)
	require.Error(t, err)
	any, err := st.HasAnyAdmittedClient()
	require.NoError(t, err)
	require.False(t, any)
}

// A second approval for an admitted key says it is already a member and changes nothing.
func TestAdmitPairedTwiceIsAlreadyAMember(t *testing.T) {
	st, err := Init(t.TempDir()+"/lab", ModeStandalone)
	require.NoError(t, err)
	kh := pairedKey(t)
	_, err = st.AdmitPaired(JoinClient, "laptop-x", kh, nil)
	require.NoError(t, err)
	res, err := st.AdmitPaired(JoinClient, "laptop-x", kh, nil)
	require.NoError(t, err)
	require.True(t, res.AlreadyMember)
}
