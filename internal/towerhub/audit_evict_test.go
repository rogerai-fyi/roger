package towerhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
)

// evictingSource retains nothing and reports a rising young-eviction count.
type evictingSource struct {
	mu sync.Mutex
	n  int
}

func (e *evictingSource) SignedTranscript(string) ([]byte, []byte, []byte, bool, error) {
	return nil, nil, nil, false, nil
}
func (e *evictingSource) EvictedYoung() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	return e.n
}

// A station dropping evidence inside its audit window says so OUT LOUD - the operator needs
// the cause, not the effect (audits failing at Core weeks later). Counting it silently was
// the half-fix an audit flagged.
func TestYoungEvictionsAreReportedToTheOperator(t *testing.T) {
	id := newTestNode(t)
	s := NewServer(New(), stubCheck, time.Second, 50*time.Millisecond)
	s.RegisterNode("st1", id.auth())
	mux := http.NewServeMux()
	mux.HandleFunc(PathAuditWanted, s.AuditWanted)
	mux.HandleFunc(PathAuditTranscript, s.AuditTranscript)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.SetWanted("st1", []string{"att-gone"})

	said := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pub, _, err := envelope.NewKey()
	require.NoError(t, err)
	go AnswerAudits(ctx, id.client(srv.URL, 0), "st1", &evictingSource{},
		pub, 20*time.Millisecond, func(e error) {
			select {
			case said <- e.Error():
			default:
			}
		})

	select {
	case msg := <-said:
		require.True(t, strings.Contains(msg, "before their audit window"),
			"the operator is told WHY audits will start failing, got: %s", msg)
		require.Contains(t, msg, "memory", "and what to do about it")
	case <-time.After(5 * time.Second):
		t.Fatal("a young eviction was never reported")
	}
}
