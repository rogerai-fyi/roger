package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rogerai-fyi/roger/internal/agent"
)

// okBroker is an httptest server that 200s everything (incl. heartbeats), so any
// agent.Start session it backs goes truthfully ON AIR.
func okBroker(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
}

// startShares spins up n on-air sessions (models m0..m(n-1), distinct node ids) and
// registers them in mm.shares, then repoints the headline. Each carries a non-zero
// out price so the panel renders the priced (not FREE) form for it.
func startShares(t *testing.T, mm *model, broker string, n int) {
	t.Helper()
	mm.shares = map[string]*agent.Session{}
	for i := 0; i < n; i++ {
		model := fmt.Sprintf("model-%02d", i)
		sess, err := agent.Start(agent.Config{
			Broker: broker, Upstream: "http://127.0.0.1:0",
			NodeID: fmt.Sprintf("node-%02d", i), Model: model,
			Ctx: 8192, Parallel: 1, PriceOut: 1.5,
		})
		if err != nil {
			t.Fatalf("agent.Start %s: %v", model, err)
		}
		t.Cleanup(sess.Stop)
		mm.shares[model] = sess
	}
	mm.refreshShareHeadline()
}

// TestOnAirPanelListsAllBands: the on-air panel renders ONE row per live band plus a
// single TOTALS line - the founder bug was that 3+ shared models showed only the
// headline. Here 3 sessions must yield 3 model rows + a TOTALS line.
func TestOnAirPanelListsAllBands(t *testing.T) {
	srv := okBroker(t)
	defer srv.Close()

	mm := New(srv.URL, "tester")
	startShares(t, &mm, srv.URL, 3)
	waitBadge(t, &mm, "ON AIR")

	plain := stripANSI(mm.onAirPanel(120))
	for _, model := range []string{"model-00", "model-01", "model-02"} {
		if !strings.Contains(plain, model) {
			t.Errorf("on-air panel must list every band; missing %q:\n%s", model, plain)
		}
	}
	for _, node := range []string{"node-00", "node-01", "node-02"} {
		if !strings.Contains(plain, node) {
			t.Errorf("on-air panel should show each band's node %q:\n%s", node, plain)
		}
	}
	if c := strings.Count(plain, "TOTALS"); c != 1 {
		t.Errorf("on-air panel should have exactly one TOTALS line, got %d:\n%s", c, plain)
	}
	if !strings.Contains(plain, "sharing 3 bands") {
		t.Errorf("header should announce the band count:\n%s", plain)
	}
	if !strings.Contains(plain, "/share off") {
		t.Errorf("panel must keep the /share off footer (stops all):\n%s", plain)
	}
	// One band -> singular "band".
	mm1 := New(srv.URL, "tester")
	startShares(t, &mm1, srv.URL, 1)
	waitBadge(t, &mm1, "ON AIR")
	if p := stripANSI(mm1.onAirPanel(120)); !strings.Contains(p, "sharing 1 band") {
		t.Errorf("single band should read 'sharing 1 band':\n%s", p)
	}
}

// TestOnAirPanelManyBandsFold: past onAirMaxRows, the extra bands fold into a single
// "+K more on air" line while the TOTALS line still sums EVERY band.
func TestOnAirPanelManyBandsFold(t *testing.T) {
	srv := okBroker(t)
	defer srv.Close()

	const n = onAirMaxRows + 3
	mm := New(srv.URL, "tester")
	startShares(t, &mm, srv.URL, n)
	waitBadge(t, &mm, "ON AIR")

	plain := stripANSI(mm.onAirPanel(120))
	if !strings.Contains(plain, fmt.Sprintf("+%d more on air", n-onAirMaxRows)) {
		t.Errorf("expected a '+%d more' fold line:\n%s", n-onAirMaxRows, plain)
	}
	// The folded models must NOT each get a full row (only the first onAirMaxRows do).
	if strings.Contains(plain, fmt.Sprintf("model-%02d", n-1)) {
		t.Errorf("the last folded band should not have its own row:\n%s", plain)
	}
	if !strings.Contains(plain, fmt.Sprintf("sharing %d bands", n)) {
		t.Errorf("header should still count every band:\n%s", plain)
	}
}

// TestOnAirCompactLine: the windowshade compact one-liner summarizes ALL bands (count
// + served + earnings + /share off), never just the headline.
func TestOnAirCompactLine(t *testing.T) {
	srv := okBroker(t)
	defer srv.Close()

	mm := New(srv.URL, "tester")
	mm.width, mm.height = 120, 30
	startShares(t, &mm, srv.URL, 3)
	waitBadge(t, &mm, "ON AIR")

	line := stripANSI(mm.compactOnAirLine(120))
	if !strings.Contains(line, "sharing 3") {
		t.Errorf("compact line should summarize the band count:\n%s", line)
	}
	if !strings.Contains(line, "served") || !strings.Contains(line, "/share off") {
		t.Errorf("compact line should carry served + /share off:\n%s", line)
	}
	if !strings.Contains(line, "ON AIR") {
		t.Errorf("compact line should carry the ON AIR beacon:\n%s", line)
	}
}

// TestOnAirPanelEmpty: with no live shares, the panel renders nothing (off air).
func TestOnAirPanelEmpty(t *testing.T) {
	mm := New("http://broker.local", "tester")
	if got := mm.onAirPanel(120); got != "" {
		t.Errorf("empty shares should render no panel, got:\n%s", got)
	}
	if got := mm.compactOnAirLine(120); got != "" {
		t.Errorf("empty shares should render no compact line, got:\n%s", got)
	}
	// A nil entry in the map is also off-air (no live session).
	mm.shares = map[string]*agent.Session{"m": nil}
	if got := mm.onAirPanel(120); got != "" {
		t.Errorf("nil-only shares should render no panel, got:\n%s", got)
	}
}

// TestOnAirPanelNarrowSafe: the full panel + compact line must never overflow the
// width at narrow terminals, even with many bands and long node ids.
func TestOnAirPanelNarrowSafe(t *testing.T) {
	srv := okBroker(t)
	defer srv.Close()

	mm := New(srv.URL, "tester")
	startShares(t, &mm, srv.URL, onAirMaxRows+2)
	waitBadge(t, &mm, "ON AIR")

	for _, w := range []int{40, 64, 80, 120} {
		for _, line := range strings.Split(mm.onAirPanel(w), "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: on-air panel line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
		if vis := utf8.RuneCountInString(stripANSI(mm.compactOnAirLine(w))); vis > w {
			t.Errorf("width %d: compact line overflows (%d cols)", w, vis)
		}
	}
}
