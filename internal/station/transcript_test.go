package station

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func tr(id string) Transcript {
	return Transcript{AttemptID: id, Request: []byte("q-" + id), Response: []byte("a-" + id)}
}

func TestKeepAndGetRoundTrip(t *testing.T) {
	s := NewTranscripts(10, 1)
	s.Keep(tr("att-1"))
	got, ok := s.Get("att-1")
	require.True(t, ok)
	require.Equal(t, []byte("q-att-1"), got.Request)
	require.Equal(t, []byte("a-att-1"), got.Response)

	_, ok = s.Get("nope")
	require.False(t, ok, "an attempt never kept cannot be produced")
}

// Bounded, dropping the oldest - an attempt old enough to age out has almost certainly
// aged out of its audit window too.
func TestAFullTranscriptStoreDropsTheOldest(t *testing.T) {
	s := NewTranscripts(3, 1)
	for i := 0; i < 5; i++ {
		s.Keep(tr(fmt.Sprintf("att-%d", i)))
	}
	require.Equal(t, 3, s.Len())
	_, ok := s.Get("att-0")
	require.False(t, ok)
	_, ok = s.Get("att-4")
	require.True(t, ok)
}

// Sampling is DETERMINISTIC on the attempt id: the same attempt is kept or dropped
// identically no matter which process runs, and an attacker cannot retry to land outside the
// sample because the attempt id is Core's to mint.
func TestSamplingIsDeterministic(t *testing.T) {
	a := NewTranscripts(1000, 4)
	b := NewTranscripts(1000, 4)
	kept := 0
	for i := 0; i < 400; i++ {
		id := fmt.Sprintf("att-%d", i)
		a.Keep(tr(id))
		b.Keep(tr(id))
		_, ka := a.Get(id)
		_, kb := b.Get(id)
		require.Equal(t, ka, kb, "the same id must be kept or dropped identically")
		if ka {
			kept++
		}
	}
	// Roughly a quarter, and never all or none - a sample that kept everything or nothing
	// would be no sample.
	require.Greater(t, kept, 50)
	require.Less(t, kept, 200)
}

func TestSampleOfOneKeepsEverything(t *testing.T) {
	for _, n := range []uint32{0, 1} {
		s := NewTranscripts(1000, n)
		for i := 0; i < 50; i++ {
			s.Keep(tr(fmt.Sprintf("att-%d", i)))
		}
		require.Equal(t, 50, s.Len())
	}
}

func TestKeepIgnoresEmptyAttempts(t *testing.T) {
	s := NewTranscripts(10, 1)
	s.Keep(Transcript{Request: []byte("x")})
	require.Zero(t, s.Len())
}

func TestKeepIsIdempotent(t *testing.T) {
	s := NewTranscripts(10, 1)
	s.Keep(tr("att-1"))
	s.Keep(Transcript{AttemptID: "att-1", Request: []byte("different")})
	got, _ := s.Get("att-1")
	require.Equal(t, []byte("q-att-1"), got.Request, "the first kept wins")
	require.Equal(t, 1, s.Len())
}
