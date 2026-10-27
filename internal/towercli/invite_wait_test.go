package towercli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/joincode"
	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
)

// lockedBuf is a writer the waiter and the test read concurrently.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// features/join/invite.feature, "The owner sees the join as it happens": while the invite is still
// on screen, a client's join prints who joined, its short key, and how to undo it.
func TestAWaitingInvitePrintsTheJoinAsItHappens(t *testing.T) {
	dir := t.TempDir() + "/lab"
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	inv, code, err := st.CreateJoinInvitation(tower.JoinClient, "", nil, time.Hour)
	require.NoError(t, err)

	var out lockedBuf
	done := make(chan struct{})
	go func() {
		waitForJoin(dir, inv.ID, &out, 10*time.Millisecond, time.Now().Add(10*time.Second))
		close(done)
	}()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	ph := hex.EncodeToString(pub)
	fp, _ := st.JoinFingerprint()
	kh := protocol.UserIDFromPubkey(ph)
	_, err = st.ConsumeJoin(tower.JoinRequest{Proof: joincode.Tower.Proof(code, "n", ph, fp), Nonce: "n", PubHex: ph, KeyHash: kh, Name: "laptop-x"})
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting invite never noticed the join")
	}
	got := out.String()
	require.Contains(t, got, "laptop-x joined (key "+strings.TrimPrefix(kh, "u_")[:4]+"..."+kh[len(kh)-2:]+")")
	require.Contains(t, got, "revoke-client laptop-x")
}

// An invite that expires or is voided unused ends the wait and says so.
func TestAWaitingInviteThatEndsUnusedSaysSo(t *testing.T) {
	dir := t.TempDir() + "/lab"
	st, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	inv, _, err := st.CreateJoinInvitation(tower.JoinClient, "", nil, time.Hour)
	require.NoError(t, err)
	require.NoError(t, st.VoidInvitation(inv.ID))
	var out lockedBuf
	waitForJoin(dir, inv.ID, &out, 10*time.Millisecond, time.Now().Add(5*time.Second))
	require.Contains(t, out.String(), "no one used this invite")
}
