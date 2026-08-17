package station

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// THE MEMORY BOUND IS BYTES. Retention protects young transcripts from the count limit, but
// not from the byte budget - otherwise a consumer driving large requests through a hub fills
// the node's memory inside the retention window. Young evictions are COUNTED, because each
// one becomes an audit this station cannot answer.
func TestBigTranscriptsAreBoundedByBytesNotCount(t *testing.T) {
	s := NewTranscripts(4, 0)
	clock := time.Now()
	s.now = func() time.Time { return clock }

	// 8 MiB each: 64 of them would be 512 MiB, twice the budget.
	big := make([]byte, 8<<20)
	for i := 0; i < 64; i++ {
		s.Keep(Transcript{AttemptID: fmt.Sprintf("att-%d", i), Response: big})
	}
	require.LessOrEqual(t, s.bytes, transcriptMaxBytes, "the byte budget is never exceeded")
	require.Less(t, s.Len(), 64, "the oldest were evicted to stay inside it")
	require.Positive(t, s.EvictedYoung(),
		"dropping a transcript inside its audit window is counted, not silent")
}

// Small transcripts still enjoy the full retention protection: the byte budget never binds.
func TestSmallTranscriptsKeepTheirRetentionProtection(t *testing.T) {
	s := NewTranscripts(4, 0)
	clock := time.Now()
	s.now = func() time.Time { return clock }
	for i := 0; i < 200; i++ {
		s.Keep(Transcript{AttemptID: fmt.Sprintf("att-%d", i), Response: []byte("small")})
	}
	require.Equal(t, 200, s.Len(), "young and tiny: nothing is evicted")
	require.Zero(t, s.EvictedYoung())
}
