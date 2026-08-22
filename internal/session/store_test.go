package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/capsule"
)

func snapshot(id, cwd, title string, created, updated time.Time) Snapshot {
	model := "test-model"
	return Snapshot{
		Version:   CurrentVersion,
		ID:        id,
		Title:     title,
		Workdir:   cwd,
		CreatedAt: created,
		UpdatedAt: updated,
		Model:     model,
		Messages: []capsule.Message{
			{Role: "user", Content: title, XRoger: capsule.XRoger{Turn: 0, Agent: "user:agent"}},
			{Role: "assistant", Content: "answer", XRoger: capsule.XRoger{Turn: 1, Agent: "roger-agent:" + model}},
		},
	}
}

func TestStoreSaveListAndPermissions(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	s := NewStore(dir)
	s.Now = func() time.Time { return now }

	in := snapshot("th_alpha", filepath.Join(dir, "work", "alpha"), "Fix the radio", now.Add(-time.Hour), now)
	require.NoError(t, s.Save(in))

	got, warnings, err := s.List()
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, []Snapshot{in}, got)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "th_alpha.json"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		require.Equal(t, os.FileMode(0o700), mustStat(t, dir).Mode().Perm())
	}
	require.Empty(t, mustGlob(t, filepath.Join(dir, "*.tmp-*")), "atomic temp files must not remain visible")
}

func TestStoreListNewestFirstSkipsInvalidWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	require.NoError(t, s.Save(snapshot("th_old", filepath.Join(dir, "work", "a"), "old", base, base)))
	require.NoError(t, s.Save(snapshot("th_new", filepath.Join(dir, "work", "b"), "new", base, base.Add(time.Hour))))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "future.json"), []byte(`{"version":999,"id":"th_future"}`), 0o600))

	got, warnings, err := s.List()
	require.NoError(t, err)
	require.Len(t, warnings, 2)
	require.Equal(t, []string{"th_new", "th_old"}, []string{got[0].ID, got[1].ID})
	require.Equal(t, []byte("{not json"), mustRead(t, filepath.Join(dir, "broken.json")))
}

func TestStoreResolveFullUniqueAmbiguousAndMissing(t *testing.T) {
	items := []Snapshot{
		snapshot("th_example123", "/a", "one", time.Time{}, time.Time{}),
		snapshot("th_example999", "/b", "two", time.Time{}, time.Time{}),
		snapshot("th_other", "/c", "three", time.Time{}, time.Time{}),
	}

	got, err := Resolve(items, "th_example123")
	require.NoError(t, err)
	require.Equal(t, "th_example123", got.ID)

	got, err = Resolve(items, "th_oth")
	require.NoError(t, err)
	require.Equal(t, "th_other", got.ID)

	_, err = Resolve(items, "th_exam")
	require.ErrorIs(t, err, ErrAmbiguous)
	require.Contains(t, err.Error(), "th_example123")
	require.Contains(t, err.Error(), "th_example999")
	require.NotContains(t, err.Error(), "/a", "errors expose safe IDs and titles, not full paths")

	_, err = Resolve(items, "th_missing")
	require.ErrorIs(t, err, ErrNotFound)
	require.Contains(t, err.Error(), "roger resume")
}

func TestStoreConcurrentSavesAlwaysLeaveAWholeSnapshot(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	a := snapshot("th_same", filepath.Join(dir, "work"), "a", base, base.Add(time.Second))
	b := snapshot("th_same", filepath.Join(dir, "work"), "b", base, base.Add(2*time.Second))

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); require.NoError(t, s.Save(a)) }()
		go func() { defer wg.Done(); require.NoError(t, s.Save(b)) }()
	}
	wg.Wait()

	got, warnings, err := s.List()
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, got, 1)
	require.Contains(t, []string{"a", "b"}, got[0].Title)
	require.Len(t, got[0].Messages, 2)
}

func TestStoreDefaultsValidationAndStaleWriteOrdering(t *testing.T) {
	require.NotEmpty(t, DefaultDir())
	dir := t.TempDir()
	s := NewStore(dir)
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	for _, id := range []string{"", ".", "..", "../escape", "bad/id", "bad id"} {
		err := s.Save(Snapshot{ID: id})
		require.Error(t, err, id)
	}
	require.Error(t, (*Store)(nil).Save(Snapshot{ID: "th_ok"}))
	require.Error(t, NewStore("").Save(Snapshot{ID: "th_ok"}))
	require.Error(t, s.Save(Snapshot{Version: CurrentVersion + 1, ID: "th_future"}))
	require.Error(t, s.Save(Snapshot{ID: "th_relative", Workdir: "relative/path"}))

	require.NoError(t, s.Save(Snapshot{ID: "th_defaults", Title: "new", Workdir: filepath.Join(dir, "work")}))
	got, warnings, err := s.List()
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, CurrentVersion, got[0].Version)
	require.Equal(t, now, got[0].CreatedAt)
	require.Equal(t, now, got[0].UpdatedAt)
	require.Equal(t, byte('\n'), mustRead(t, filepath.Join(dir, "th_defaults.json"))[len(mustRead(t, filepath.Join(dir, "th_defaults.json")))-1])

	older := got[0]
	older.Title = "must not win"
	older.UpdatedAt = now.Add(-time.Hour)
	require.NoError(t, s.Save(older))
	again, _, err := s.List()
	require.NoError(t, err)
	require.Equal(t, "new", again[0].Title)
}

func TestStoreMissingAndUnreadableDirectories(t *testing.T) {
	missing := NewStore(filepath.Join(t.TempDir(), "missing"))
	got, warnings, err := missing.List()
	require.NoError(t, err)
	require.Nil(t, got)
	require.Nil(t, warnings)

	notDir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notDir, []byte("x"), 0o600))
	_, _, err = NewStore(notDir).List()
	require.Error(t, err)
	require.Error(t, NewStore(notDir).Save(Snapshot{ID: "th_x"}))

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "ignored.json"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad-id.json"), []byte(`{"version":1,"id":"../bad"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "relative.json"), []byte(`{"version":1,"id":"th_relative","workdir":"relative"}`), 0o600))
	items, warnings, err := NewStore(dir).List()
	require.NoError(t, err)
	require.Empty(t, items)
	require.Len(t, warnings, 2)
	_, err = readFile(filepath.Join(dir, "missing.json"))
	require.Error(t, err)

	conflict := filepath.Join(dir, "th_conflict.json")
	require.NoError(t, os.Mkdir(conflict, 0o700))
	err = NewStore(dir).Save(Snapshot{ID: "th_conflict", Workdir: dir})
	require.Error(t, err)
}

func TestSafeLabelStripsControlsTrimsAndBounds(t *testing.T) {
	require.Equal(t, "hello", SafeLabel("\x1b[31m\n hello \x7f"))
	require.Len(t, SafeLabel(strings.Repeat("x", 100)), 80)
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	require.NoError(t, err)
	return m
}
