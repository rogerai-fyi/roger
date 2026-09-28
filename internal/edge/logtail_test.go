package edge

// Executable pins for features/edge/logs.feature: the ring, and the redaction that is the whole risk.

import (
	"strings"
	"sync"
	"testing"

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

	// The labelled forms keep their label, so the line still says what was removed.
	for _, c := range []struct{ in, keep string }{
		{`starting share with key=hunter2 and ctx=90112`, "key="},
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
