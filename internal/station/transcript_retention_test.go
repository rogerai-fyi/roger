package station

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The audit-retention guarantee: a transcript younger than the retention window survives the
// count limit (the store grows instead), an aged one is evicted normally, and the hard cap
// bounds the growth absolutely.
func TestYoungTranscriptsSurviveTheCountLimit(t *testing.T) {
	s := NewTranscripts(4, 0)
	clock := time.Now()
	s.now = func() time.Time { return clock }

	for i := 0; i < 10; i++ {
		s.Keep(Transcript{AttemptID: fmt.Sprintf("att-%d", i), Request: []byte("q"), Response: []byte("a")})
	}
	require.Equal(t, 10, s.Len(), "young entries grow past the limit - an audit may still want each")
	_, ok := s.Get("att-0")
	require.True(t, ok, "the oldest young entry is protected")

	// Time passes beyond the retention window: the count limit resumes evicting oldest-first.
	clock = clock.Add(auditRetention + time.Minute)
	s.Keep(Transcript{AttemptID: "att-new", Request: []byte("q"), Response: []byte("a")})
	require.LessOrEqual(t, s.Len(), 4+1, "aged entries yield to the count limit again")
	_, ok = s.Get("att-0")
	require.False(t, ok, "the aged oldest went first")
	_, ok = s.Get("att-new")
	require.True(t, ok)
}

// The hard cap holds even inside the retention window.
func TestTheHardCapBoundsRetentionGrowth(t *testing.T) {
	s := NewTranscripts(4, 0)
	clock := time.Now()
	s.now = func() time.Time { return clock }
	for i := 0; i < transcriptHardCap+8; i++ {
		s.Keep(Transcript{AttemptID: fmt.Sprintf("att-%d", i)})
	}
	require.Equal(t, transcriptHardCap, s.Len(), "memory is bounded absolutely")
}
