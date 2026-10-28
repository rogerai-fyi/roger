package localmodel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTheWordsAModelIsSaidIn(t *testing.T) {
	require.Equal(t, "8K", ctxWord(8192))
	require.Equal(t, "1000", ctxWord(1000), "a context that is not a whole K is said exactly")
	require.Equal(t, "4.3", tokWord(4.3))
	require.Equal(t, "45", tokWord(45.4))

	require.Empty(t, Pace{}.Short(), "no speed, no row text")
	require.Equal(t, "~45 tok/s usable", Pace{TokPerSec: 45, Word: "usable"}.Short())
	require.Equal(t, "92+ tok/s usable", Pace{TokPerSec: 92, Word: "usable", AtLeast: true}.Short(), "a floor reads as one")

	require.Empty(t, RoomLine(nil))
	line := RoomLine([]Model{{ID: "a/old:Q4", Bytes: 1 << 30}, {ID: "a/mid:Q8", Bytes: 2 << 30}})
	require.Contains(t, line, "least recently served: a/old:Q4 (1.0 GiB), a/mid:Q8 (2.0 GiB)")
}

func TestWhereThingsLive(t *testing.T) {
	h := Home{Dir: "/x"}
	require.Equal(t, filepath.Join("/x", "runtime"), h.Runtime())
	require.Equal(t, filepath.Join("/x", "serving-4001.json"), h.RecordOf(Serving{Port: 4001}))
}

func TestAFileTheOwnerNamesIsAModelOnlyIfItIsOne(t *testing.T) {
	dir := t.TempDir()
	_, err := FromPath(filepath.Join(dir, "absent.gguf"))
	require.ErrorContains(t, err, "no model file at")
	_, err = FromPath(dir)
	require.ErrorContains(t, err, "no model file at", "a directory is not a model")
	proj := filepath.Join(dir, "mmproj-F16.gguf")
	require.NoError(t, os.WriteFile(proj, []byte("x"), 0o644))
	_, err = FromPath(proj)
	require.ErrorContains(t, err, "vision projector")
	ok := filepath.Join(dir, "mine-Q4_K_M.gguf")
	require.NoError(t, os.WriteFile(ok, []byte("weights"), 0o644))
	m, err := FromPath(ok)
	require.NoError(t, err)
	require.Equal(t, "Q4_K_M", m.Quant)
	require.False(t, m.Verified(), "a file nobody checked is served on the owner's word")
}

func TestMarkingAServeTouchesOnlyRogersOwnRecords(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	require.ErrorIs(t, h.MarkServed("nobody/here:Q4", time.Now()), ErrUnknownModel)
	require.NoError(t, os.MkdirAll(h.Models(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(h.Models(), "mine-Q4_K_M.gguf"), []byte("w"), 0o644))
	require.NoError(t, h.MarkServed("mine-Q4_K_M", time.Now()), "a hand-placed file has no record to write, and that is fine")
	_, err := os.Stat(filepath.Join(h.Models(), "mine-Q4_K_M.gguf"+recordExt))
	require.True(t, os.IsNotExist(err), "and none is invented for it")
}

func TestMakingRoomThatCannotBeMadeRemovesNothing(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	old := fetchedModel(t, h, "a/old", "old.gguf", 600, time.Now().Add(-time.Hour))
	require.NoError(t, h.SetSettings(Settings{DiskBudget: 1000}))
	_, err := h.MakeRoom(2000)
	require.ErrorContains(t, err, "larger than")
	_, err = os.Stat(old.Path)
	require.NoError(t, err, "a refused plan removes nothing")
}

func TestStopsSurviveOnlyWhatIsReadable(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	require.NoError(t, h.writeStops(nil), "writing no stops is clearing them")
	require.Empty(t, h.Stops())
	require.NoError(t, os.MkdirAll(h.Dir, 0o700))
	require.NoError(t, os.WriteFile(h.stopPath(), []byte("{damaged"), 0o600))
	require.Empty(t, h.Stops(), "a damaged record says nothing rather than something wrong")
	_, ok := h.LastStop()
	require.False(t, ok)
}

func TestAServePinnedToOneCardSaysSoToTheRuntime(t *testing.T) {
	args := strings.Join(Args(ServeOpts{Model: Model{Path: "/m", Band: "b"}, Ctx: 4096, Device: "CUDA2"}, 9, "/k"), " ")
	require.Contains(t, args, "--device CUDA2")
	args = strings.Join(Args(ServeOpts{Model: Model{Path: "/m", Band: "b"}, Ctx: 4096}, 9, "/k"), " ")
	require.NotContains(t, args, "--device", "no pin: the runtime's own placement")
}
