package edge

// Executable pins for features/edge/logs.feature: the ring, and the redaction that is the whole risk.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogRingKeepsTheNewestLinesAndBoundsThem(t *testing.T) {
	r := NewLogRing(5)
	for _, ln := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		_, err := r.Write([]byte(ln + "\n"))
		require.NoError(t, err)
	}
	lines, held := r.Tail(0)
	require.Equal(t, 5, held, "the cap holds")
	require.Equal(t, []string{"three", "four", "five", "six", "seven"}, lines, "oldest first, newest last")

	// Several lines in one write, blanks dropped.
	r = NewLogRing(0)
	_, err := r.Write([]byte("a\n\nb\n"))
	require.NoError(t, err)
	lines, held = r.Tail(0)
	require.Equal(t, []string{"a", "b"}, lines)
	require.Equal(t, 2, held)

	// One absurd line is cut rather than held whole.
	_, err = r.Write([]byte(strings.Repeat("x", MaxLogLineLen+500) + "\n"))
	require.NoError(t, err)
	lines, _ = r.Tail(1)
	require.Contains(t, lines[0], "line cut")
	require.Less(t, len([]rune(lines[0])), MaxLogLineLen+40)

	// A ring with nothing in it answers emptily.
	empty, held := NewLogRing(0).Tail(10)
	require.Empty(t, empty)
	require.Zero(t, held)
}

func TestLogRingTailClampsWhatIsAsked(t *testing.T) {
	r := NewLogRing(0)
	for i := 0; i < 10; i++ {
		_, _ = r.Write([]byte("line\n"))
	}
	lines, held := r.Tail(3)
	require.Len(t, lines, 3)
	require.Equal(t, 10, held)
	lines, _ = r.Tail(1000)
	require.Len(t, lines, 10, "never more than it holds")
	lines, _ = r.Tail(-5)
	require.Len(t, lines, 10, "a nonsense count is the default, clamped")
}

func TestLogRingIsSafeUnderConcurrentWritesAndReads(t *testing.T) {
	r := NewLogRing(50)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = r.Write([]byte("a line from a goroutine\n"))
				_, _ = r.Tail(10)
			}
		}()
	}
	wg.Wait()
	lines, held := r.Tail(0)
	require.Equal(t, 50, held)
	for _, ln := range lines {
		require.Equal(t, "a line from a goroutine", ln, "no read tore a line")
	}
}

// The secrets this fleet really carries must not leave the process.
func TestRedactionRemovesEverySecretShapeAndKeepsTheLine(t *testing.T) {
	secrets := map[string]string{
		"openai":     "sk-abcdefghijklmnopqrstuvwxyz012345",
		"openrouter": "sk-or-v1-2cc8198d15ed3647c7b3457bf77829e2",
		"cerebras":   "csk-eyxertx4fr8t9nevh2ke6cnf493de6hc",
		"xai":        "xai-abcdefghijklmnopqrstuvwxyz0123",
		"groq":       "gsk_abcdefghijklmnopqrstuvwxyz0123",
		"github":     "ghp_abcdefghijklmnopqrstuvwxyz0123",
		"grant":      "rgr_abcdefghijklmnopqrstuvwxyz0123",
		"entropy":    "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5YWJjZGVm",
	}
	for name, secret := range secrets {
		t.Run(name, func(t *testing.T) {
			line := "2026-09-28 upstream refused for band qwen: " + secret + " (retrying)"
			got := RedactSecrets(line)
			require.NotContains(t, got, secret, "the secret left the process")
			require.Contains(t, got, redactMarker)
			require.Contains(t, got, "upstream refused for band qwen", "the rest of the line survives")
			require.Contains(t, got, "(retrying)")
		})
	}

	// The labelled forms keep their label, so the line still says what was removed. The label may
	// carry a prefix, be quoted, or be a flag - the cases the fourth audit named as leaking.
	for _, c := range []struct{ in, keep string }{
		{`starting share with key=hunter2 and ctx=90112`, "key="},
		{`share --upstream-key hunter2 --ctx 90112`, "upstream-key"},
		{`share --api-key=hunter2`, "api-key"},
		{`config {"api_key": "hunter2", "ctx": 90112}`, "api_key"},
		{`env UPSTREAM_KEY=hunter2 loaded`, "UPSTREAM_KEY"},
		{`env upstream_token: hunter2`, "upstream_token"},
		{`-secret hunter2 given`, "secret"},
		{`token = abc123def456 was rejected`, "token"},
		{`authorization: Basic YWxhZGRpbjpvcGVuc2VzYW1l`, "authorization:"},
		{`sent Bearer abc123def456ghi`, "Bearer"},
		{`API_KEY="s3cr3t-value" loaded`, "API_KEY"},
		{`password: letmein`, "password"},
	} {
		got := RedactSecrets(c.in)
		require.Contains(t, got, redactMarker, c.in)
		require.Contains(t, got, c.keep, c.in)
		for _, bad := range []string{"hunter2", "abc123def456", "YWxhZGRpbjpvcGVuc2VzYW1l", "s3cr3t-value", "letmein"} {
			require.NotContains(t, got, bad, c.in)
		}
	}

	// An ordinary line is left alone: redaction that eats the log is no use.
	for _, ordinary := range []string{
		"edge discovery: refused n_abc at 10.0.0.9:44121: unreachable",
		"registered with broker as roggentooedged, 3 models on air",
		"cpu_temp 95.5C over 85C on RogGentooEdged",
		"probe: serve -> VERIFIED for gpt-oss-20b",
	} {
		require.Equal(t, ordinary, RedactSecrets(ordinary), "an ordinary line must survive whole")
	}
}

// LiveHousehold is what a stop reads before it signals anything: only registrations that are still
// beating, each with its heartbeat (features/edge/maintenance.feature).
func TestLiveHouseholdHoldsOnlyWhatIsStillBeating(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// Both are written by a running process (this one): a pid that is not running is a dead roger
	// whatever its heartbeat says, and that is TestAGoneRogersRecordGivesItsNameBack's case.
	fresh, err := Register(dir, "n_desk", Instance{Name: "alive", PID: os.Getpid()}, now)
	require.NoError(t, err)
	stale, err := Register(dir, "n_desk", Instance{Name: "wedged", PID: os.Getpid()}, now)
	require.NoError(t, err)

	live := LiveHousehold(dir, now)
	require.Len(t, live, 2)
	require.Equal(t, "alive", live[0].Name, "by name")
	require.Equal(t, os.Getpid(), live[0].PID, "the pid it registered")
	require.False(t, live[0].Beat.IsZero(), "with its heartbeat")

	// Age one past the reap window: it is not live, and LiveHousehold does not delete it either -
	// reaping is Household's job, and a reader that deleted what it read would be a surprise.
	old := now.Add(-2 * InstanceLife)
	for _, p := range mustGlob(t, dir) {
		if strings.Contains(readFileString(t, p), "wedged") {
			require.NoError(t, os.Chtimes(p, old, old))
		}
	}
	live = LiveHousehold(dir, now)
	require.Len(t, live, 1)
	require.Equal(t, "alive", live[0].Name)
	require.FileExists(t, mustGlob(t, dir)[0])

	// A deregistered one is gone at once, which is what a clean exit does.
	require.NoError(t, fresh.Deregister())
	require.Empty(t, LiveHousehold(dir, now))
	_ = stale

	// An unreadable file is skipped rather than failing the read.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{not json"), 0o600))
	require.Empty(t, LiveHousehold(dir, now))
	require.Empty(t, LiveHousehold(filepath.Join(dir, "nope"), now), "a directory that is not there is empty, not an error")
}

// A roger killed without deregistering leaves a fresh record behind. Its pid has exited, so the
// record is not a running roger: Household reaps it at once and its name is free for the next roger
// to start, rather than that roger taking "<node>-2" for the two minutes the heartbeat would allow.
// A record from a roger too old to carry its pid is judged by its heartbeat alone.
func TestAGoneRogersRecordGivesItsNameBack(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	gone := exec.Command(os.Args[0], "-test.run=^$")
	require.NoError(t, gone.Run())
	_, err := Register(dir, "n_hub", Instance{Name: "hub", PID: gone.Process.Pid}, now)
	require.NoError(t, err)
	_, err = Register(dir, "n_hub", Instance{Name: "legacy"}, now)
	require.NoError(t, err)

	require.Len(t, LiveHousehold(dir, now), 1, "the gone roger is not live")
	require.Equal(t, "legacy", LiveHousehold(dir, now)[0].Name, "a pid-less record lives by its heartbeat")
	got := []string{}
	for _, in := range Household(dir, now) {
		got = append(got, in.Name)
	}
	require.Equal(t, []string{"legacy"}, got)
	require.Len(t, mustGlob(t, dir), 1, "the gone roger's record was reaped")

	next, err := Register(dir, "n_hub", Instance{Name: DefaultInstanceName("hub", []string{"legacy"}), PID: os.Getpid()}, now)
	require.NoError(t, err)
	require.Equal(t, "hub", next.Instance().Name, "the next roger gets the name back")
}

func mustGlob(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, m)
	return m
}

func readFileString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}
