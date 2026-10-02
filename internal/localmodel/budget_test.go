package localmodel

// MODELS ARE GIGABYTES AND THEY ACCUMULATE (local_models.feature §6): a budget can be set so a
// board's card is not filled silently, and nothing is ever evicted without the owner having asked
// for one. With a budget, room is made by removing the models served least recently that roger
// fetched itself; a model being served and a file the owner placed by hand are never removed.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fetchedModel(t *testing.T, h Home, repo, file string, size int64, used time.Time) Model {
	t.Helper()
	p, err := h.PathFor(repo, file)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, make([]byte, size), 0o644))
	m := Model{ID: repo + ":" + file, Repo: repo, File: file, Path: p, Bytes: size, SHA256: "ab", Band: BandFor(repo), Fetched: used}
	require.NoError(t, h.record(m))
	return m
}

func TestWithoutABudgetNothingIsEverEvicted(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	fetchedModel(t, h, "a/one", "one.gguf", 900, time.Now().Add(-90*24*time.Hour))
	out, err := h.RoomFor(1 << 40)
	require.NoError(t, err)
	require.Empty(t, out, "no budget, no eviction - however full")
}

func TestABudgetMakesRoomFromTheLeastRecentlyServed(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	now := time.Now()
	old := fetchedModel(t, h, "a/old", "old.gguf", 400, now.Add(-30*24*time.Hour))
	mid := fetchedModel(t, h, "a/mid", "mid.gguf", 400, now.Add(-10*24*time.Hour))
	fresh := fetchedModel(t, h, "a/fresh", "fresh.gguf", 400, now.Add(-time.Hour))
	// Serving is use: the old one was served yesterday, so it is no longer the least recent.
	require.NoError(t, h.MarkServed(old.ID, now.Add(-24*time.Hour)))
	require.NoError(t, h.SetSettings(Settings{DiskBudget: 1500}))

	out, err := h.RoomFor(200)
	require.NoError(t, err)
	require.Empty(t, out, "1200 + 200 is inside 1500")

	out, err = h.RoomFor(500)
	require.NoError(t, err)
	require.Equal(t, []string{mid.ID}, ids(out), "the one served least recently goes first")

	defer func(f func(int) bool) { alive = f }(alive)
	alive = func(int) bool { return true }
	require.NoError(t, h.writeServing(Serving{Supervisor: 1, Port: 6001, ID: fresh.ID, File: fresh.File, Band: "fresh"}))
	out, err = h.RoomFor(900)
	require.NoError(t, err)
	require.Equal(t, []string{mid.ID, old.ID}, ids(out), "the one being served is never on the list")

	_, err = h.RoomFor(1400)
	require.ErrorContains(t, err, "is being served", "and when nothing else can go, it says what is holding the room")

	_, err = h.RoomFor(1600)
	require.ErrorContains(t, err, "larger than the 1.5 KiB you set for models")
}

func TestAFileTheOwnerPlacedIsNeverRemovedToMakeRoom(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	require.NoError(t, os.MkdirAll(h.Models(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(h.Models(), "mine-Q4_K_M.gguf"), make([]byte, 1000), 0o644))
	require.NoError(t, h.SetSettings(Settings{DiskBudget: 1200}))
	_, err := h.RoomFor(500)
	require.ErrorContains(t, err, "placed by hand")
}

func TestMakingRoomRemovesExactlyThePlannedModels(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	now := time.Now()
	old := fetchedModel(t, h, "a/old", "old.gguf", 600, now.Add(-30*24*time.Hour))
	keep := fetchedModel(t, h, "a/keep", "keep.gguf", 600, now)
	require.NoError(t, h.SetSettings(Settings{DiskBudget: 1500}))
	gone, err := h.MakeRoom(600)
	require.NoError(t, err)
	require.Equal(t, []string{old.ID}, ids(gone))
	_, err = os.Stat(old.Path)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(keep.Path)
	require.NoError(t, err)
}

func ids(ms []Model) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// Review 2026-10-01: with two on the air, EVERY one is protected - not only the one served last - and
// so is a model still loading, which has no record yet.
func TestNothingOnTheAirOrLoadingIsEverRemoved(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	now := time.Now()
	a := fetchedModel(t, h, "a/a", "a.gguf", 400, now.Add(-30*24*time.Hour))
	b := fetchedModel(t, h, "a/b", "b.gguf", 400, now.Add(-20*24*time.Hour))
	c := fetchedModel(t, h, "a/c", "c.gguf", 400, now.Add(-10*24*time.Hour))
	require.NoError(t, h.SetSettings(Settings{DiskBudget: 1300}))
	defer func(f func(int) bool) { alive = f }(alive)
	alive = func(int) bool { return true }
	require.NoError(t, h.writeServing(Serving{Supervisor: 1, Port: 6001, ID: a.ID, File: a.File, Band: "a", Started: now}))
	require.NoError(t, h.writeServing(Serving{Supervisor: 1, Port: 6002, ID: b.ID, File: b.File, Band: "b", Started: now.Add(time.Second)}))
	out, err := h.RoomFor(400)
	require.NoError(t, err)
	require.Equal(t, []string{c.ID}, ids(out), "a, served first and least recently used, is on the air and stays")

	done, err := h.claimPort(6003, c)
	require.NoError(t, err)
	_, err = h.RoomFor(400)
	require.ErrorContains(t, err, c.ID+" is being loaded")
	done()
	out, err = h.RoomFor(400)
	require.NoError(t, err)
	require.Equal(t, []string{c.ID}, ids(out), "once loading ends it is an ordinary model again")
}
