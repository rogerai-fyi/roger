package tui

// features/models/short_names_nearby.feature section 7: the model browser opens on the bands served
// nearby that fit this machine, each with its source, size and verdict; the hub search is one
// keystroke away and is never sent on its own; choosing a band is the same consented fetch as
// `roger model serve <name>`.

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v7/internal/modelview"
)

type nearbyFake struct {
	*modelsFake
	nv       modelview.NearbyView
	searched []string
	picked   []modelview.NearbyPick
}

func (f *nearbyFake) hooks() modelview.Hooks {
	h := f.modelsFake.hooks()
	search := h.Search
	h.Search = func(ctx context.Context, q string) ([]modelview.RepoRow, error) {
		f.searched = append(f.searched, q)
		return search(ctx, q)
	}
	h.Nearby = func(context.Context) (modelview.NearbyView, error) { return f.nv, nil }
	h.FetchNearby = func(p modelview.NearbyPick) (string, error) {
		f.picked = append(f.picked, p)
		return "fetching " + p.File + " - it keeps going if you leave this screen", nil
	}
	return h
}

func nearbyRows() modelview.NearbyView {
	return modelview.NearbyView{Budget: 16 << 30, Pool: "system memory",
		Fits: []modelview.NearbyRow{{Name: "qwen3-8b", Repo: "Qwen/Qwen3-8B-GGUF", File: "Qwen3-8B-Q4_K_M.gguf", Quant: "Q4_K_M",
			SHA256: strings.Repeat("ab", 32), Bytes: 5 << 30, By: "your Edge member atlas", Fit: "fits",
			Short: "fits  at 8K - needs 6.4 GiB", Line: "fits at 8K context: needs 6.4 GiB of 16.0 GiB system memory",
			Ask: "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch 5.0 GiB?"}},
		Other: []modelview.NearbyRow{{Name: "gpt-oss-120b", Repo: "big/GPT-GGUF", File: "GPT-Q8_0.gguf", Quant: "Q8_0",
			Bytes: 120 << 30, By: "a station on the Tower 192.168.1.20 (its word)", Fit: "will not fit",
			Why: "would not fit here: will not fit: needs 130.0 GiB, system memory has 16.0 GiB"}}}
}

// openGet puts the SHARE cursor on the get-a-model row and presses Enter, folding in what it loads.
func openGet(t *testing.T, f *nearbyFake, width int) model {
	t.Helper()
	var tm tea.Model = NewWithHooks("http://b", "t", nil, Hooks{Models: f.hooks()})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: width, Height: 44})
	m := tm.(model)
	m.mode = modeShare
	m = feed(t, m, m.modelsLoad())
	for i := 0; i < m.shareRowCount(); i++ {
		m, _ = shareKey(t, m, "down")
	}
	if e, _ := m.shareExtraAt(m.shareCursor); e.kind != extraGet {
		t.Fatalf("the last row is not the way to get a model")
	}
	m, cmd := shareKey(t, m, "enter")
	for cmd != nil {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				if c != nil {
					if nm, _, ok := m.onModelsMsg(c()); ok {
						m = nm
					}
				}
			}
			break
		}
		nm, _, ok := m.onModelsMsg(msg)
		if !ok {
			break
		}
		m, cmd = nm, nil
	}
	return m
}

func TestTheModelBrowserOpensOnNearbyBandsThatFit(t *testing.T) {
	f := &nearbyFake{modelsFake: &modelsFake{view: baseView()}, nv: nearbyRows()}
	m := openGet(t, f, 130)
	out := stripANSI(m.modelsView(130))
	for _, want := range []string{
		"this machine: 16.0 GiB of system memory",
		"served nearby, and fits this machine",
		"qwen3-8b", "Qwen/Qwen3-8B-GGUF:Q4_K_M", "5.0 GiB", "fits  at 8K", "your Edge member atlas",
		"gpt-oss-120b", "would not fit here",
		"/ search the model hub",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the browser is missing %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "qwen3-8b"), strings.Index(out, "gpt-oss-120b"); i < 0 || j < i {
		t.Errorf("the bands that fit are not shown first:\n%s", out)
	}
	if m.mdl.typing {
		t.Errorf("with bands nearby the browser opens on them, not on the search box")
	}
	if len(f.searched) != 0 {
		t.Errorf("opening the browser sent a search: %v", f.searched)
	}

	// The search is one keystroke away, and is not sent until chosen.
	m, _ = press(t, m, "/")
	if !m.mdl.typing || len(f.searched) != 0 {
		t.Errorf("/ did not open the search box, or sent something: typing=%v %v", m.mdl.typing, f.searched)
	}
}

func TestChoosingANearbyBandIsTheSameConsentedFetch(t *testing.T) {
	f := &nearbyFake{modelsFake: &modelsFake{view: baseView()}, nv: nearbyRows()}
	m := openGet(t, f, 130)
	m, _ = press(t, m, "enter")
	if m.mdl.ask == nil {
		t.Fatalf("choosing a band did not ask")
	}
	ask := stripANSI(m.modelsView(130))
	if !strings.Contains(ask, "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch 5.0 GiB?") {
		t.Errorf("the consent line is not the terminal's:\n%s", ask)
	}
	if !strings.Contains(ask, "checked against the digest your Edge member atlas reported") {
		t.Errorf("the ask does not say what the bytes are checked against:\n%s", ask)
	}
	m, _ = press(t, m, "n")
	if len(f.picked) != 0 {
		t.Fatalf("a no fetched: %v", f.picked)
	}
	m, _ = press(t, m, "enter")
	_, cmd := press(t, m, "y")
	nm := feed(t, m, cmd)
	want := modelview.NearbyPick{Name: "qwen3-8b", Repo: "Qwen/Qwen3-8B-GGUF", File: "Qwen3-8B-Q4_K_M.gguf", SHA256: strings.Repeat("ab", 32)}
	if len(f.picked) != 1 || f.picked[0] != want {
		t.Fatalf("y did not fetch exactly the file shown: %+v", f.picked)
	}
	if !strings.Contains(nm.mdl.says, "fetching Qwen3-8B-Q4_K_M.gguf") {
		t.Errorf("the owner was not told: %q", nm.mdl.says)
	}
}

func TestWithNothingNearbyTheBrowserOpensOnTheSearchNotYetSent(t *testing.T) {
	f := &nearbyFake{modelsFake: &modelsFake{view: baseView()}}
	m := openGet(t, f, 130)
	if !m.mdl.typing {
		t.Errorf("with nothing nearby the search box is not ready to type into")
	}
	out := stripANSI(m.modelsView(130))
	if !strings.Contains(out, "nothing is served nearby") {
		t.Errorf("the browser does not say nothing is served nearby:\n%s", out)
	}
	if len(f.searched) != 0 {
		t.Errorf("a search was sent on its own: %v", f.searched)
	}
}

func TestNearbySuggestionsReadWithoutColourAndFitTheWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	f := &nearbyFake{modelsFake: &modelsFake{view: baseView()}, nv: nearbyRows()}
	m := openGet(t, f, 80)
	out := m.View()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("colour escapes under NO_COLOR")
	}
	for _, l := range strings.Split(out, "\n") {
		if w := utf8.RuneCountInString(stripANSI(l)); w > 80 {
			t.Errorf("a line overflows 80 columns (%d): %q", w, stripANSI(l))
		}
	}
	plain := stripANSI(out)
	for _, want := range []string{"qwen3-8b", "atlas", "5.0 GiB", "fits"} {
		if !strings.Contains(plain, want) {
			t.Errorf("at 80 columns the suggestion lost %q:\n%s", want, plain)
		}
	}
}
