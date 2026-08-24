package tower

// receipts_test.go covers the standalone plane's persisted local receipts - free, locally
// accounted bookkeeping (features/tower/standalone_consumer_plane.feature), never billing.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
)

func TestRecordAndReadReceipts(t *testing.T) {
	st, dir := newBootstrapStore(t)

	r1, err := st.RecordReceipt("client-a", "st-1", "llama-8b")
	require.NoError(t, err)
	require.Equal(t, "client-a", r1.ClientKeyHash)
	require.Equal(t, "st-1", r1.StationID)
	require.Equal(t, "llama-8b", r1.Model)
	require.Equal(t, st.LocalNetworkID, r1.NetworkID)
	require.Zero(t, r1.Cost, "a local receipt is free")
	require.NotEmpty(t, r1.RequestID)
	require.NotEmpty(t, r1.RootFingerprint, "the receipt pins the offline root")

	_, err = st.RecordReceipt("client-b", "st-2", "qwen")
	require.NoError(t, err)

	got, err := st.Receipts(0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "client-a", got[0].ClientKeyHash, "receipts are returned in the order they were recorded")
	require.Equal(t, "client-b", got[1].ClientKeyHash)

	// Persisted across a restart.
	st2, err := Open(dir)
	require.NoError(t, err)
	got2, err := st2.Receipts(0)
	require.NoError(t, err)
	require.Len(t, got2, 2)
}

func TestReceiptsLimitReturnsTheMostRecent(t *testing.T) {
	st, _ := newBootstrapStore(t)
	for _, m := range []string{"m1", "m2", "m3"} {
		_, err := st.RecordReceipt("c", "st", m)
		require.NoError(t, err)
	}
	got, err := st.Receipts(2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "m2", got[0].Model, "a limit returns the most recent N in order")
	require.Equal(t, "m3", got[1].Model)
}

func TestReceiptsIsStandaloneOnly(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir, ModeJoined)
	require.NoError(t, err)
	st, err := Open(dir)
	require.NoError(t, err)
	_, err = st.RecordReceipt("c", "s", "m")
	require.ErrorIs(t, err, ErrNotStandalone)
}

func TestReceiptsOnAFreshNetworkIsEmpty(t *testing.T) {
	st, _ := newBootstrapStore(t)
	got, err := st.Receipts(0)
	require.NoError(t, err, "a network that has served nothing has no receipts, not an error")
	require.Empty(t, got)
}

func TestReceiptsSkipsBlankAndCorruptLines(t *testing.T) {
	st, dir := newBootstrapStore(t)
	_, err := st.RecordReceipt("c", "st", "m") // one valid line
	require.NoError(t, err)
	// Append a blank line and a corrupt line; both must be skipped, not fatal.
	f, err := os.OpenFile(filepath.Join(dir, receiptsFile), os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, _ = f.WriteString("\n{not valid json\n")
	require.NoError(t, f.Close())

	got, err := st.Receipts(0)
	require.NoError(t, err)
	require.Len(t, got, 1, "the one valid receipt survives; blank and corrupt lines are skipped")
}

func TestRecordReceiptFailsWithoutTheOfflineRoot(t *testing.T) {
	st, dir := newBootstrapStore(t)
	require.NoError(t, os.Remove(filepath.Join(dir, offlineRoot)))
	_, err := st.RecordReceipt("c", "st", "m")
	require.Error(t, err, "a receipt pins the offline root; without it, none is written")
}

func TestRecordReceiptFailsWhenTheLogPathIsUnwritable(t *testing.T) {
	st, dir := newBootstrapStore(t)
	// A directory where the receipts file should be: OpenFile for append fails.
	require.NoError(t, os.Mkdir(filepath.Join(dir, receiptsFile), 0o755))
	_, err := st.RecordReceipt("c", "st", "m")
	require.Error(t, err, "an unwritable receipt log is surfaced, not swallowed")
}
