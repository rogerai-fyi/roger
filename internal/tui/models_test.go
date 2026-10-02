package tui

// features/serving/local_models.feature §7 (@tui): the MODELS screen, driven by key against the same
// hook shapes the console takes. Hooks run off the event loop in the app; here each command is run
// and its message fed back, so the test sees exactly the frames an owner would.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/modelview"
)

type modelsFake struct {
	view    modelview.View
	fetched []string
	served  []string
	added   []string
	removed []string
	stopped int
	stopIDs []string
}

func (f *modelsFake) hooks() modelview.Hooks {
	return modelview.Hooks{
		View: func() (modelview.View, error) { return f.view, nil },
		Search: func(ctx context.Context, q string) ([]modelview.RepoRow, error) {
			if q == "boom" {
				return nil, errors.New("the source did not answer - check this machine's network")
			}
			return []modelview.RepoRow{{Repo: "acme/Tiny-GGUF", Downloads: 51234, License: "mit"}, {Repo: "acme/Locked-GGUF", Gated: true}}, nil
		},
		Files: func(ctx context.Context, repo string, ctxLen int) (modelview.FilesView, error) {
			if ctxLen == 0 {
				ctxLen = 8192
			}
			return modelview.FilesView{Repo: repo, License: "mit", Budget: 4 << 30, Pool: "system memory", Ctx: ctxLen,
				Choices: []modelview.FileChoice{
					{Label: "Q2_K", File: "Tiny-Q2_K.gguf", Bytes: 1 << 30, Fit: "fits", Short: "fits  at 8K - needs 2.1 GiB"},
					{Label: "Q4_K_M", File: "Tiny-Q4_K_M.gguf", Bytes: 2 << 30, Fit: "fits", Short: "fits  at 8K - needs 3.2 GiB",
						Line: "fits at 8K context: needs 3.2 GiB of 4.0 GiB system memory", Sum: "weights 2.0 GiB + cache 1.1 GiB = 3.2 GiB", Suggested: true,
						Pace: "~45 tok/s usable", PaceLine: "writes about 45 tok/s - usable (2.0 GiB per token at measured 100 GB/s)",
						Room: "to stay within the budget you set for models, roger removes first, least recently served: a/old:Q4 (1.0 GiB)"},
					{Label: "F16", File: "Tiny-F16.gguf", Bytes: 8 << 30, Fit: "will not fit", Short: "will not fit - needs 9.4 GiB"},
					{Label: "mmproj-F16", File: "mmproj-F16.gguf", Bytes: 1 << 20, Unservable: "a vision projector"},
				}}, nil
		},
		Fetch: func(repo, file string) error { f.fetched = append(f.fetched, repo+"|"+file); return nil },
		Serve: func(id string, ctx int, add bool) (string, error) {
			if add {
				f.added = append(f.added, id)
				return "starting " + id + " beside", nil
			}
			f.served = append(f.served, id)
			return "starting " + id, nil
		},
		Stop: func(id string) (string, error) {
			f.stopped++
			f.stopIDs = append(f.stopIDs, id)
			return "stopped serving tiny.", nil
		},
		Remove: func(id string) (string, error) { f.removed = append(f.removed, id); return "removed " + id, nil },
	}
}

func modelsModel(t *testing.T, f *modelsFake) model {
	t.Helper()
	var tm tea.Model = NewWithHooks("http://b", "t", nil, Hooks{Models: f.hooks()})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 116, Height: 44})
	m := tm.(model)
	m, _ = m.enterModels()
	m = feed(t, m, m.modelsLoad())
	return m
}

// feed runs one command and folds its message back in, the way the runtime would.
func feed(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	nm, _, ok := m.onModelsMsg(msg)
	if !ok {
		t.Fatalf("unexpected message %T", msg)
	}
	return nm
}

func press(t *testing.T, m model, keys ...string) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case " ":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		tm, c := m.onModelsKey(msg)
		switch v := tm.(type) {
		case model:
			m = v
		case *model:
			m = *v
		}
		cmd = c
	}
	return m, cmd
}

func screen(m model) string { return stripANSI(m.modelsView(m.effWidth())) }

func baseView() modelview.View {
	return modelview.View{Budget: 4 << 30, Pool: "system memory", Runtime: "llama.cpp b11146 installed", CanServe: true,
		Dir: "/home/x/.config/rogerai/local/models", DiskUsed: 2 << 30,
		Models: []modelview.Row{
			{ID: "acme/Tiny-GGUF:Q4_K_M", File: "Tiny-Q4_K_M.gguf", Bytes: 2 << 30, Quant: "Q4_K_M", Band: "tiny", Verified: true},
			{ID: "mine-Q8_0", File: "mine-Q8_0.gguf", Bytes: 1 << 30, Quant: "Q8_0", Band: "mine"},
		}}
}

func TestModelsScreenAnswersWhatThisMachineCanRunFirst(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := modelsModel(t, f)
	out := screen(m)
	for _, want := range []string{"THIS MACHINE  4.0 GiB of system memory", "RUNTIME       llama.cpp b11146 installed",
		"ON THE AIR    nothing", "ON THIS MACHINE", "acme/Tiny-GGUF:Q4_K_M", "checked", "your word"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "THIS MACHINE"), strings.Index(out, "ON THIS MACHINE"); i < 0 || i > j {
		t.Errorf("the machine's memory is not stated before the list")
	}
	if !strings.Contains(m.modelsKeys(), "⏎ serve") || strings.Contains(m.modelsKeys(), "S stop") {
		t.Errorf("the keys do not match what can be done now: %q", m.modelsKeys())
	}
}

// local_models.feature §4: fitting and being usable are two different claims, each in its own place.
func TestModelsFitAndSpeedAreSaidSeparately(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := modelsModel(t, f)
	m, _ = press(t, m, "/", "t", "i", "n", "y")
	m, cmd := press(t, m, "enter")
	m = feed(t, m, cmd)
	m, cmd = press(t, m, "enter")
	m = feed(t, m, cmd)
	out := screen(m)
	if !strings.Contains(out, "fits  at 8K - needs 3.2 GiB  ~45 tok/s usable") {
		t.Errorf("the speed is not beside the fit in its own words:\n%s", out)
	}
	if !strings.Contains(out, "writes about 45 tok/s - usable") {
		t.Errorf("the selected file's speed and its working are not shown:\n%s", out)
	}
	m, _ = press(t, m, "enter")
	if out = screen(m); !strings.Contains(out, "writes about 45 tok/s - usable") {
		t.Errorf("the fetch is asked without the speed:\n%s", out)
	}
	if !strings.Contains(out, "roger removes first, least recently served: a/old:Q4") {
		t.Errorf("the fetch is asked without saying what the budget removes:\n%s", out)
	}
}

func TestModelsScreenNotOfferedWithoutHooks(t *testing.T) {
	m := NewWithHooks("http://b", "t", nil, Hooks{})
	m2, cmd := m.enterModels()
	if m2.mode == modeModels || cmd != nil || !strings.Contains(stripANSI(m2.status), "roger model") {
		t.Errorf("a build with no model hooks opened the screen anyway")
	}
}

func TestModelsSearchJudgeAndFetchWithTheOwnersYes(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := modelsModel(t, f)
	m, _ = press(t, m, "/", "a", " ", "b")
	if m.mdl.query != "a b" {
		t.Errorf("a space was not typed as one space: %q", m.mdl.query)
	}
	m, _ = press(t, m, "esc", "/", "t", "i", "n", "y")
	if !strings.Contains(screen(m), "/ tiny█") {
		t.Fatalf("typing is not shown:\n%s", screen(m))
	}
	m, cmd := press(t, m, "enter")
	m = feed(t, m, cmd)
	out := screen(m)
	if !strings.Contains(out, "what their authors published, not RogerAI's word") || !strings.Contains(out, "GATED") {
		t.Errorf("search results are not marked as the wider world:\n%s", out)
	}
	m, cmd = press(t, m, "enter")
	m = feed(t, m, cmd)
	out = screen(m)
	for _, want := range []string{"acme/Tiny-GGUF", "licence mit", "judged at 8192 context", "* Q4_K_M", "will not fit",
		"not servable: a vision projector", "weights 2.0 GiB + cache 1.1 GiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("the repo view is missing %q:\n%s", want, out)
		}
	}
	// The suggestion is selected; f shows only what fits, and the verdict is a word, not a colour.
	m, _ = press(t, m, "f")
	if out = screen(m); strings.Contains(out, "F16") || strings.Contains(out, "mmproj") || !strings.Contains(out, "only what fits") {
		t.Errorf("the fits-only filter did not filter:\n%s", out)
	}
	m, _ = press(t, m, "f", "down")
	// Enter asks; Enter alone is No.
	m, _ = press(t, m, "enter")
	if out = screen(m); !strings.Contains(out, "Fetch this model?") || !strings.Contains(out, "licence: mit") || !strings.Contains(out, "(⏎ alone is no)") {
		t.Fatalf("the fetch was not asked with its facts:\n%s", out)
	}
	m, _ = press(t, m, "enter")
	if len(f.fetched) != 0 || !strings.Contains(screen(m), "nothing changed") {
		t.Fatalf("Enter alone fetched: %v", f.fetched)
	}
	m, _ = press(t, m, "enter")
	m, cmd = press(t, m, "y")
	m = feed(t, m, cmd)
	if len(f.fetched) != 1 || f.fetched[0] != "acme/Tiny-GGUF|Tiny-Q4_K_M.gguf" {
		t.Fatalf("y did not fetch the selected file: %v", f.fetched)
	}
	if !strings.Contains(screen(m), "it keeps going if you leave this screen") {
		t.Errorf("the owner is not told the fetch survives leaving")
	}
	// The context is the owner's: - halves it and the files are judged again at that length.
	m, cmd = press(t, m, "-")
	m = feed(t, m, cmd)
	if !strings.Contains(screen(m), "judged at 4096 context") {
		t.Errorf("changing the context did not re-judge:\n%s", screen(m))
	}
	// esc walks back: repo -> results -> disk.
	m, _ = press(t, m, "esc")
	if m.mdl.pane != modelsSearch {
		t.Errorf("esc from a repo did not return to the results")
	}
	m, _ = press(t, m, "esc")
	if m.mdl.pane != modelsDisk {
		t.Errorf("esc from the results did not return to the disk")
	}
	// A failed search names which failure.
	m, _ = press(t, m, "/", "b", "o", "o", "m")
	m, cmd = press(t, m, "enter")
	m = feed(t, m, cmd)
	if !strings.Contains(screen(m), "check this machine's network") {
		t.Errorf("a failed search does not say why:\n%s", screen(m))
	}
	// A new search starts empty, typing edits it, and esc leaves the box.
	m, _ = press(t, m, "/", "x", "y", "backspace")
	if m.mdl.query != "x" {
		t.Errorf("the search box did not start empty or edit (%q)", m.mdl.query)
	}
	m, _ = press(t, m, "esc")
	if m.mdl.typing {
		t.Errorf("esc did not leave the search box")
	}
}

func TestModelsAFetchIsVisibleAliveAndOnlyAnimatesWhileItMoves(t *testing.T) {
	f := &modelsFake{view: baseView()}
	f.view.Job = &modelview.FetchJob{Repo: "acme/Tiny-GGUF", File: "Tiny-Q8_0.gguf", Total: 4 << 30, Done: 1 << 30,
		Rate: 40 << 20, RemainingS: 77, State: "fetching"}
	m := modelsModel(t, f)
	out := screen(m)
	for _, want := range []string{"FETCHING", "Tiny-Q8_0.gguf", "1.0 GiB of 4.0 GiB", "40.0 MiB/s", "1m17s left", "█", "░"} {
		if !strings.Contains(out, want) {
			t.Errorf("the fetch is missing %q:\n%s", want, out)
		}
	}
	a := screen(m)
	m, cmd, _ := m.onModelsMsg(modelsTickMsg{})
	if cmd == nil {
		t.Fatalf("a running fetch did not keep the clock going")
	}
	if screen(m) == a {
		t.Errorf("nothing moved while bytes were moving")
	}

	// Nothing happening: ticks change nothing on screen.
	f.view.Job = nil
	m = feed(t, m, m.modelsLoad())
	b := screen(m)
	for i := 0; i < 5; i++ {
		m, _, _ = m.onModelsMsg(modelsTickMsg{})
	}
	if screen(m) != b {
		t.Errorf("the screen animated with nothing happening")
	}

	// Left and come back: the clock stops with the screen, the fetch is shown again on return. (SHARE
	// keeps it running - roger's own rows live there - so "left" is somewhere else.)
	m.mode = modeBrowse
	if _, cmd, _ := m.onModelsMsg(modelsTickMsg{}); cmd != nil {
		t.Errorf("a screen nobody is looking at kept ticking")
	}
	f.view.Job = &modelview.FetchJob{Repo: "acme/Tiny-GGUF", File: "Tiny-Q8_0.gguf", Total: 10, Done: 5, State: "fetching"}
	m, _ = m.enterModels()
	m = feed(t, m, m.modelsLoad())
	if !strings.Contains(screen(m), "FETCHING") {
		t.Errorf("coming back did not show the fetch still running")
	}
}

func TestModelsAFailureSaysWhichFailureAndTheFix(t *testing.T) {
	f := &modelsFake{view: baseView()}
	f.view.Job = &modelview.FetchJob{Repo: "acme/Tiny-GGUF", File: "x.gguf", Total: 10, Done: 3, State: "failed",
		Err: "not enough free disk", Fix: "free some space, or remove a model you no longer use"}
	m := modelsModel(t, f)
	out := screen(m)
	if !strings.Contains(out, "FETCH FAILED") || !strings.Contains(out, "not enough free disk") || !strings.Contains(out, "fix: free some space") {
		t.Errorf("the failure is not named with its fix:\n%s", out)
	}
	f.view.Job.State = "interrupted"
	m = feed(t, m, m.modelsLoad())
	if !strings.Contains(screen(m), "resumes from here") {
		t.Errorf("an interrupted fetch does not say it resumes")
	}
	f.view.Job = nil
	f.view.ServeLog = []string{"error: out of memory"}
	m = feed(t, m, m.modelsLoad())
	if !strings.Contains(screen(m), "DID NOT COME ON THE AIR") || !strings.Contains(screen(m), "out of memory") {
		t.Errorf("a serve that failed in the background is silent:\n%s", screen(m))
	}
}

func TestModelsServeSwitchStopAndRemoveAreAskedFirst(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := modelsModel(t, f)
	m, _ = press(t, m, "enter")
	if !strings.Contains(screen(m), "Serve acme/Tiny-GGUF:Q4_K_M?") {
		t.Fatalf("serve was not asked:\n%s", screen(m))
	}
	m, cmd := press(t, m, "y")
	m = feed(t, m, cmd)
	if len(f.served) != 1 || !strings.Contains(screen(m), "starting acme/Tiny-GGUF:Q4_K_M") {
		t.Fatalf("y did not serve: %v", f.served)
	}

	f.view.Serving = &modelview.Serving{Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Quant: "Q4_K_M", Ctx: 8192, Since: time.Now().Add(-90 * time.Second)}
	f.view.Models[0].Serving = true
	m = feed(t, m, m.modelsLoad())
	out := screen(m)
	if !strings.Contains(out, "▶ ON THE AIR  \"tiny\"") || !strings.Contains(out, "8192 context") || !strings.Contains(out, "for 1m30s") {
		t.Errorf("what is on the air is not said in full:\n%s", out)
	}
	m, _ = press(t, m, "x")
	if !strings.Contains(screen(m), "being served right now") {
		t.Errorf("removing the served model was not refused")
	}
	m, _ = press(t, m, "down", "enter")
	if !strings.Contains(screen(m), "Switch to mine-Q8_0?") || !strings.Contains(screen(m), "proven FIRST") {
		t.Errorf("a switch does not say the old one keeps serving until the new one answers:\n%s", screen(m))
	}
	m, _ = press(t, m, "n")
	m, _ = press(t, m, "S")
	m, cmd = press(t, m, "y")
	m = feed(t, m, cmd)
	if f.stopped != 1 {
		t.Errorf("S then y did not stop")
	}
	m, _ = press(t, m, "x")
	m, cmd = press(t, m, "y")
	_ = feed(t, m, cmd)
	if len(f.removed) != 1 || f.removed[0] != "mine-Q8_0" {
		t.Errorf("x then y did not remove the selected model: %v", f.removed)
	}

	// A machine with no runtime says why rather than offering a serve that cannot happen.
	f.view = baseView()
	f.view.CanServe, f.view.Runtime = false, "local serving is not available here: needs glibc 2.38, this has 2.36"
	m = feed(t, m, m.modelsLoad())
	m.mdl.sel = 0
	m, _ = press(t, m, "enter")
	if m.mdl.ask != nil || !strings.Contains(screen(m), "needs glibc 2.38") {
		t.Errorf("serve was offered on a machine that cannot serve:\n%s", screen(m))
	}
}

func TestModelsScreenDegradesHonestlyAt80ColumnsAndWithoutColour(t *testing.T) {
	f := &modelsFake{view: baseView()}
	f.view.Models[0].ID = "someone-with-a-long-name/An-Extremely-Long-Repository-Name-GGUF:Q4_K_M"
	f.view.Job = &modelview.FetchJob{Repo: "acme/Tiny-GGUF", File: "Tiny-Q8_0.gguf", Total: 4 << 30, Done: 3 << 30,
		Rate: 40 << 20, RemainingS: 30, State: "fetching"}
	m := modelsModel(t, f)
	tm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = tm.(model)
	for _, ln := range strings.Split(m.modelsView(80), "\n") {
		if lipgloss.Width(ln) > 80 {
			t.Errorf("a line overflows 80 columns (%d): %q", lipgloss.Width(ln), stripANSI(ln))
		}
	}
	out := stripANSI(m.modelsView(80))
	for _, want := range []string{"THIS MACHINE", "FETCHING", "3.0 GiB of 4.0 GiB", "checked"} {
		if !strings.Contains(out, want) {
			t.Errorf("without colour the screen loses %q:\n%s", want, out)
		}
	}
}

func TestModelsAreReachedFromShareAndFromTheNothingFoundWizard(t *testing.T) {
	f := &modelsFake{view: baseView()}
	m := NewWithHooks("http://b", "t", nil, Hooks{Models: f.hooks()})
	m.mode = modeShare
	tm, cmd := m.onShareKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if tm.(model).mode != modeModels || cmd == nil {
		t.Errorf("g on SHARE did not open MODELS")
	}
	m2 := m.enterShareSetup()
	tm, _ = m2.onShareSetupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if got := modeOf(tm); got != modeModels {
		t.Errorf("g in the nothing-found wizard did not open MODELS")
	}
	// ...but not while a URL is being pasted: that g is part of the URL.
	m3 := m.enterShareSetup()
	m3.setupCursor = len(setupOptions) - 1
	tm, _ = m3.onShareSetupKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if modeOf(tm) == modeModels {
		t.Errorf("a g typed into a pasted URL opened MODELS")
	}
	if !strings.Contains(stripANSI(m2.footer(116)), "g serve one with roger") {
		t.Errorf("the wizard does not say g exists")
	}
	_ = context.Background
}

func modeOf(tm tea.Model) mode {
	switch v := tm.(type) {
	case model:
		return v.mode
	case *model:
		return v.mode
	}
	return -1
}

// local_models.feature §5 "two at once only when they really fit together": with one on the air, the
// serve question offers adding beside it as well as switching, and a machine serving two shows both.
func TestModelsASecondModelCanBeAddedBesideTheFirst(t *testing.T) {
	f := &modelsFake{view: baseView()}
	f.view.Serving = &modelview.Serving{Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Quant: "Q4_K_M", Ctx: 8192, Since: time.Now()}
	f.view.Models[0].Serving = true
	m := modelsModel(t, f)
	m, _ = press(t, m, "down", "enter")
	out := screen(m)
	if !strings.Contains(out, "a add beside") || !strings.Contains(out, "only if both fit together") {
		t.Fatalf("adding beside is not offered with its condition:\n%s", out)
	}
	m, cmd := press(t, m, "a")
	m = feed(t, m, cmd)
	if len(f.added) != 1 || f.added[0] != "mine-Q8_0" || len(f.served) != 0 {
		t.Fatalf("a did not add beside: added %v served %v", f.added, f.served)
	}

	f.view.Also = []modelview.Serving{{Band: "older", ID: "x/older:Q4", Quant: "Q4_K_M", Ctx: 4096, Since: time.Now().Add(-time.Hour)}}
	m = feed(t, m, m.modelsLoad())
	out = screen(m)
	if !strings.Contains(out, "\"older\"") || !strings.Contains(out, "\"tiny\"") {
		t.Errorf("a machine serving two does not say each:\n%s", out)
	}
	m, _ = press(t, m, "S")
	if !strings.Contains(screen(m), "Stop serving all 2?") {
		t.Fatalf("stopping with two on the air does not say it stops both:\n%s", screen(m))
	}
	m, cmd = press(t, m, "y")
	_ = feed(t, m, cmd)
	if len(f.stopIDs) != 1 || f.stopIDs[0] != "" {
		t.Errorf("S with two should stop all: %v", f.stopIDs)
	}
}
