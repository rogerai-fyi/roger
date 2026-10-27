package tui

// features/serving/local_models.feature §9: models are rows of SHARE - what roger serves itself, what
// is on this disk, a download in progress, and always last, the way to get a model.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/modelview"
)

func shareWithModels(t *testing.T, f *modelsFake) model {
	t.Helper()
	var tm tea.Model = NewWithHooks("http://b", "t", nil, Hooks{Models: f.hooks()})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 130, Height: 44})
	m := tm.(model)
	m.mode = modeShare
	m.shareRows = []shareRow{{model: "gpt-oss-20b", upstream: "http://127.0.0.1:11434/v1/chat/completions"},
		{model: "tiny", upstream: "http://127.0.0.1:36277/v1/chat/completions"}}
	return feed(t, m, m.modelsLoad())
}

func shareKey(t *testing.T, m model, k string) (model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	tm, cmd := m.onShareKey(msg)
	switch v := tm.(type) {
	case model:
		return v, cmd
	case *model:
		return *v, cmd
	}
	return m, cmd
}

func TestModelsAreRowsOfShare(t *testing.T) {
	f := &modelsFake{view: baseView()}
	f.view.Serving = &modelview.Serving{Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Quant: "Q4_K_M", Ctx: 8192, Address: "http://127.0.0.1:36277/v1"}
	f.view.Models[0].Serving = true
	f.view.Job = &modelview.FetchJob{Repo: "acme/Big-GGUF", File: "Big-Q4_K_M.gguf", Total: 4 << 30, Done: 1 << 30, Rate: 40 << 20, RemainingS: 77, State: "fetching"}
	m := shareWithModels(t, f)
	out := stripANSI(m.shareView(130))
	for _, want := range []string{"gpt-oss-20b", "ON DISK", "mine", "⏎ serve here", "↓ Big-Q4_K_M", "1.0 GiB/4.0 GiB", "40.0 MiB/s",
		"+ Get a model that fits this machine", "4.0 GiB system memory"} {
		if !strings.Contains(out, want) {
			t.Errorf("SHARE is missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "acme/Tiny-GGUF:Q4_K_M") > 0 && strings.Contains(out, "tiny      ON DISK") {
		t.Errorf("a model roger serves is drawn again as on disk:\n%s", out)
	}
	if i, j := strings.Index(out, "↓ Big"), strings.Index(out, "+ Get a model"); i < 0 || j < i {
		t.Errorf("getting a model is not the last row:\n%s", out)
	}
	// The served row says it is roger's own, with its quantisation and context.
	m.shareCursor = 1
	if out = stripANSI(m.shareView(130)); !strings.Contains(out, "served by roger itself · acme/Tiny-GGUF:Q4_K_M · Q4_K_M · 8192 context") {
		t.Errorf("roger's own row does not say so:\n%s", out)
	}
	if got, want := m.shareRowCount(), 2+4; got != want { // §11 adds the serving-settings row
		t.Errorf("rows = %d, want %d (2 detected + on disk + download + settings + get)", got, want)
	}
}

func TestAModelOnDiskIsServedFromItsShareRowAskedFirst(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := shareWithModels(t, f)
	m, _ = shareKey(t, m, "down")
	m, _ = shareKey(t, m, "down")
	if e, ok := m.shareExtraAt(m.shareCursor); !ok || e.kind != extraDisk {
		t.Fatalf("the cursor did not reach the on-disk row")
	}
	if out := stripANSI(m.shareView(130)); !strings.Contains(out, "nothing serves it - ⏎ serves it here") {
		t.Errorf("the on-disk row has no detail:\n%s", out)
	}
	m, _ = shareKey(t, m, "enter")
	if out := stripANSI(m.shareView(130)); !strings.Contains(out, "Serve acme/Tiny-GGUF:Q4_K_M?") || !strings.Contains(out, "(⏎ alone is no)") {
		t.Fatalf("the serve was not asked in SHARE:\n%s", out)
	}
	// Enter alone is no; the detected-row keys never act on roger's rows.
	m, _ = shareKey(t, m, "enter")
	if len(f.served) != 0 {
		t.Fatalf("Enter alone served: %v", f.served)
	}
	m, _ = shareKey(t, m, "p")
	if m.mode != modeShare {
		t.Errorf("p on roger's row opened another screen")
	}
	m, _ = shareKey(t, m, "enter")
	m, cmd := shareKey(t, m, "y")
	msg := cmd()
	nm, _, _ := m.onModelsMsg(msg)
	if len(f.served) != 1 || !strings.Contains(stripANSI(nm.status), "starting acme/Tiny-GGUF:Q4_K_M") {
		t.Errorf("y did not serve, or SHARE was not told: %v %q", f.served, stripANSI(nm.status))
	}
}

func TestGettingAModelIsTheLastRowAndOpensInsideShare(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := shareWithModels(t, f)
	for i := 0; i < m.shareRowCount(); i++ {
		m, _ = shareKey(t, m, "down")
	}
	if e, _ := m.shareExtraAt(m.shareCursor); e.kind != extraGet {
		t.Fatalf("the last row is not the way to get a model")
	}
	m, _ = shareKey(t, m, "enter")
	if m.mode != modeModels || !m.mdl.typing || !m.inShareSection() {
		t.Errorf("getting a model did not open the search inside SHARE")
	}
	if !strings.Contains(stripANSI(m.modeName()), "GET A MODEL") {
		t.Errorf("the header does not say GET A MODEL: %q", m.modeName())
	}
}

func TestNothingDetectedStillOffersRogersOwnRowsAndTheWizardLeadsWithIt(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := shareWithModels(t, f)
	m.shareRows = nil
	out := stripANSI(m.shareView(130))
	if !strings.Contains(out, "no local models detected") || !strings.Contains(out, "+ Get a model that fits this machine") {
		t.Errorf("an empty SHARE does not offer roger's own rows:\n%s", out)
	}
	if setupOptions[0].key != "roger" {
		t.Fatalf("the wizard's first option is %q", setupOptions[0].key)
	}
	w := m.enterShareSetup()
	tm, _ := w.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if modeOf(tm) != modeModels {
		t.Errorf("the wizard's first option did not open getting a model")
	}
	// A build that cannot serve models says so rather than offering it.
	none := NewWithHooks("http://b", "t", nil, Hooks{})
	none = none.enterShareSetup()
	tm, _ = none.onShareSetupKey(tea.KeyMsg{Type: tea.KeyEnter})
	if modeOf(tm) == modeModels || !strings.Contains(asModel(tm).setupErr, "roger model") {
		t.Errorf("a build without models offered them")
	}
	if len(none.shareExtras()) != 0 {
		t.Errorf("a build without models draws roger's rows")
	}
}

// Pre-push audit (2026-10-26): every message re-syncs the SHARE cache first, and the sync clamped the
// cursor to the detected rows, so the 160ms tick threw it back to the top the moment it reached one of
// roger's own rows, and Enter acted on the wrong row. Tests that called the key handler directly never
// saw the tick.
func TestRogersOwnShareRowsStayUnderTheCursorAcrossATick(t *testing.T) {
	f := &modelsFake{view: baseView()}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, Hooks{Models: f.hooks()})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 130, Height: 44})
	m := tm.(model)
	m.mode = modeShare
	m.setShareRows([]shareRow{{model: "gpt-oss-20b", upstream: "http://127.0.0.1:11434/v1/chat/completions"}})
	m = feed(t, m, m.modelsLoad())
	require.Greater(t, m.shareRowCount(), len(m.shareRows), "roger's own rows follow the detected ones")
	m.shareCursor = len(m.shareRows) + 1
	want, ok := m.shareExtraAt(m.shareCursor)
	require.True(t, ok)

	tm, _ = m.Update(tickMsg{})
	m = tm.(model)
	require.Equal(t, len(m.shareRows)+1, m.shareCursor, "the tick leaves the cursor where it was")
	got, ok := m.shareExtraAt(m.shareCursor)
	require.True(t, ok)
	require.Equal(t, want.kind, got.kind)
}
