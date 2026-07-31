package harness

// brief_marker_drift_test.go guards the ONE piece of duplication between this package and
// internal/brief: the brief MIRRORS the retrieved-page marker rather than importing it, so
// that the dependency stays one-way (brief -> harness, never the reverse). A test file may
// import freely, so the mirror is checked here against the REAL wrapper webFetch writes.
//
// If fetch.go ever rewords the marker, this fails loudly - instead of every handed-off page
// silently losing its "retrieved from <url>, untrusted" provenance on the way to a guest.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rogerai.fm/roger/v5/internal/brief"
	"rogerai.fm/roger/v5/internal/capsule"
)

func TestBriefStillRecognisesTheRetrievedMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>page text</p></body></html>"))
	}))
	defer srv.Close()

	defer func(v func(net.IP) error) { fetchVetIP = v }(fetchVetIP)
	fetchVetIP = allowLoopbackVet

	wrapped, err := webFetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	c := capsule.Capsule{Capsule: capsule.Version, Messages: []capsule.Message{{
		Role: "assistant", Content: "read it",
		ToolCalls: capsule.ToolCallsRaw([]capsule.ToolCall{{
			ID: "c1", Name: "web_fetch", Arguments: `{"url":"` + srv.URL + `/"}`, Result: &wrapped,
		}}),
	}}}
	out := brief.Render(c)

	if !strings.Contains(out, "retrieved from "+srv.URL+"/") {
		t.Errorf("the brief no longer parses the harness marker - provenance is lost on handoff.\nwrapped=%q\nbrief=%s", wrapped, out)
	}
	if !strings.Contains(strings.ToUpper(out), "UNTRUSTED") {
		t.Errorf("the brief dropped the untrusted warning:\n%s", out)
	}
	if !strings.Contains(out, "page text") {
		t.Errorf("the page text was lost:\n%s", out)
	}
}
