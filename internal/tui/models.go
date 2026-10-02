package tui

// MODELS (features/serving/local_models.feature §7): `roger model` as a screen. Reached with g from
// SHARE and from the "no local model found" wizard - the moment an owner has nothing to share is the
// moment roger can serve something itself.
//
// Every judgement, fetch and serve goes through Hooks.Models, which cmd/rogerai fills from the CLI's
// own code (internal/modelview): this file draws and asks, nothing more. Hooks are called off the
// event loop, so a slow index never freezes the app. The bar moves only while a fetch moves; when
// nothing is happening nothing animates.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/modelview"
	"rogerai.fm/roger/v6/internal/provision"
)

type modelsPane int

const (
	modelsDisk   modelsPane = iota // what is on this machine
	modelsSearch                   // search results from the index
	modelsRepo                     // one repo's files, judged here
)

// modelsAsk is a question awaiting y/N. Enter alone is No.
type modelsAsk struct {
	title string
	lines []string
	do    func() tea.Cmd
}

type modelsState struct {
	view     modelview.View
	loaded   bool
	err      string
	pane     modelsPane
	sel      int
	typing   bool // the search box has the keys
	query    string
	busy     string // what is in flight off the loop ("searching", "judging")
	results  []modelview.RepoRow
	files    modelview.FilesView
	fitsOnly bool
	ctx      int
	ask      *modelsAsk
	says     string
	frame    int
}

type (
	modelsViewMsg struct {
		v   modelview.View
		err error
	}
	modelsSearchMsg struct {
		rows []modelview.RepoRow
		err  error
	}
	modelsFilesMsg struct {
		fv  modelview.FilesView
		err error
	}
	modelsDidMsg struct {
		says string
		err  error
	}
	modelsTickMsg struct{}
)

const modelsPollEvery = 2 * time.Second

func (m model) modelsHooks() modelview.Hooks { return m.hooks.Models }

// enterModels opens the screen and reads this machine.
func (m model) enterModels() (model, tea.Cmd) {
	if m.hooks.Models.View == nil {
		m.status = stDim.Render("this build cannot serve models itself - `roger model` in a terminal")
		return m, nil
	}
	m.mode = modeModels
	m.mdl.pane, m.mdl.sel, m.mdl.typing, m.mdl.ask, m.mdl.says = modelsDisk, 0, false, nil, ""
	m.status = stDim.Render("MODELS - serve a model here with nothing else installed")
	return m, tea.Batch(m.modelsLoad(), modelsTick())
}

// shareModelsCmd loads roger's own rows for SHARE and starts their clock, when this build can serve.
func (m model) shareModelsCmd() tea.Cmd {
	if m.hooks.Models.View == nil {
		return nil
	}
	return tea.Batch(m.modelsLoad(), modelsTick())
}

func (m model) modelsLoad() tea.Cmd {
	view := m.hooks.Models.View
	return func() tea.Msg {
		v, err := view()
		return modelsViewMsg{v, err}
	}
}

// modelsTick paces the screen: fast while a fetch moves (the bar and the carrier), slow otherwise.
func modelsTick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return modelsTickMsg{} })
}

func (m model) modelsFetching() bool {
	return m.mdl.view.Job != nil && m.mdl.view.Job.State == "fetching"
}

// onModelsMsg handles the screen's own messages; ok is false for anything else.
func (m model) onModelsMsg(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case modelsViewMsg:
		if msg.err != nil {
			m.mdl.err = msg.err.Error()
		} else {
			m.mdl.view, m.mdl.loaded, m.mdl.err = msg.v, true, ""
		}
		return m, nil, true
	case modelsSearchMsg:
		m.mdl.busy = ""
		if msg.err != nil {
			m.mdl.says = msg.err.Error()
			return m, nil, true
		}
		m.mdl.results, m.mdl.pane, m.mdl.sel = msg.rows, modelsSearch, 0
		return m, nil, true
	case modelsFilesMsg:
		m.mdl.busy = ""
		if msg.err != nil {
			m.mdl.says = msg.err.Error()
			return m, nil, true
		}
		m.mdl.files, m.mdl.pane, m.mdl.sel = msg.fv, modelsRepo, 0
		if i := m.modelsSuggested(); i >= 0 {
			m.mdl.sel = i
		}
		return m, nil, true
	case modelsDidMsg:
		if msg.err != nil {
			m.mdl.says = msg.err.Error()
		} else {
			m.mdl.says = msg.says
		}
		if m.mode == modeShare { // asked from a SHARE row: the answer goes where the owner is
			if msg.err != nil {
				m.status = stEmber.Render(msg.err.Error())
			} else {
				m.status = stLive.Render(msg.says)
			}
		}
		return m, m.modelsLoad(), true
	case modelsTickMsg:
		if m.mode != modeModels && m.mode != modeShare {
			return m, nil, true // neither screen is up: the clock stops with them
		}
		m.mdl.frame++
		var cmd tea.Cmd
		// Fetching: re-read every tick so the bar moves as the fetch moves. Otherwise every
		// couple of seconds, which catches a serve coming on the air without animating anything.
		if m.modelsFetching() || m.mdl.frame%int(modelsPollEvery/(250*time.Millisecond)) == 0 {
			cmd = m.modelsLoad()
		}
		return m, tea.Batch(cmd, modelsTick()), true
	}
	return m, nil, false
}

// modelsVisible is the rows of the current pane, after the fits-only filter.
func (m model) modelsFilesVisible() []int {
	var out []int
	for i, c := range m.mdl.files.Choices {
		if m.mdl.fitsOnly && (c.Unservable != "" || (c.Fit != "fits" && c.Fit != "tight")) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (m model) modelsSuggested() int {
	for j, i := range m.modelsFilesVisible() {
		if m.mdl.files.Choices[i].Suggested {
			return j
		}
	}
	return -1
}

func (m model) modelsRows() int {
	switch m.mdl.pane {
	case modelsSearch:
		return len(m.mdl.results)
	case modelsRepo:
		return len(m.modelsFilesVisible())
	}
	return len(m.mdl.view.Models)
}

func (m *model) onModelsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	// A pending question owns the keys: y is yes, anything else - Enter included - is no.
	if a := m.mdl.ask; a != nil {
		m.mdl.ask = nil
		if key == "y" || key == "Y" {
			return m, a.do()
		}
		m.mdl.says = "nothing changed"
		return m, nil
	}
	if m.mdl.typing {
		switch k.Type {
		case tea.KeyEsc:
			m.mdl.typing = false
		case tea.KeyEnter:
			m.mdl.typing = false
			return m, m.modelsDoSearch()
		case tea.KeyBackspace, tea.KeyDelete:
			if r := []rune(m.mdl.query); len(r) > 0 {
				m.mdl.query = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			if len(m.mdl.query) < 120 {
				if k.Type == tea.KeySpace {
					m.mdl.query += " " // a space arrives with or without its rune; it is one space
				} else {
					m.mdl.query += string(k.Runes)
				}
			}
		}
		return m, nil
	}
	switch key {
	case "esc", "q":
		if m.mdl.pane != modelsDisk {
			if m.mdl.pane == modelsRepo && len(m.mdl.results) > 0 {
				m.mdl.pane = modelsSearch
			} else {
				m.mdl.pane = modelsDisk
			}
			m.mdl.sel = 0
			return m, nil
		}
		nm, cmd := m.doShare(nil)
		return nm, cmd
	case "up", "k":
		if m.mdl.sel > 0 {
			m.mdl.sel--
		}
	case "down", "j":
		if m.mdl.sel < m.modelsRows()-1 {
			m.mdl.sel++
		}
	case "/":
		m.mdl.typing, m.mdl.says, m.mdl.query = true, "", "" // a new search starts empty
	case "r":
		return m, m.modelsLoad()
	case "f":
		if m.mdl.pane == modelsRepo {
			m.mdl.fitsOnly = !m.mdl.fitsOnly
			m.mdl.sel = 0
		}
	case "+", "=", "-":
		if m.mdl.pane == modelsRepo {
			cur := m.mdl.files.Ctx
			if cur <= 0 {
				cur = 8192
			}
			if key == "-" {
				cur /= 2
			} else {
				cur *= 2
			}
			m.mdl.ctx = max(512, min(cur, 1<<20))
			return m, m.modelsOpenRepo(m.mdl.files.Repo)
		}
	case "S":
		if sv := m.mdl.view.Serving; sv != nil {
			stop := m.hooks.Models.Stop
			m.mdl.ask = &modelsAsk{title: "stop serving \"" + sv.Band + "\"?",
				lines: []string{sv.ID + " · anything routed to it stops being answered"},
				do:    func() tea.Cmd { return modelsDo(func() (string, error) { return stop() }) }}
		}
	case "x":
		if m.mdl.pane == modelsDisk && m.mdl.sel < len(m.mdl.view.Models) {
			r := m.mdl.view.Models[m.mdl.sel]
			if r.Serving {
				m.mdl.says = r.ID + " is being served right now - S stops it first"
				return m, nil
			}
			rm := m.hooks.Models.Remove
			m.mdl.ask = &modelsAsk{title: "remove " + r.ID + "?",
				lines: []string{modelsBytes(r.Bytes) + " back · it can be fetched again later"},
				do:    func() tea.Cmd { return modelsDo(func() (string, error) { return rm(r.ID) }) }}
		}
	case "enter":
		return m, m.modelsEnter()
	}
	return m, nil
}

func modelsDo(f func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		says, err := f()
		return modelsDidMsg{says, err}
	}
}

func (m *model) modelsDoSearch() tea.Cmd {
	search := m.hooks.Models.Search
	if search == nil {
		return nil
	}
	q := strings.TrimSpace(m.mdl.query)
	m.mdl.busy = "searching the index"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rows, err := search(ctx, q)
		return modelsSearchMsg{rows, err}
	}
}

func (m *model) modelsOpenRepo(repo string) tea.Cmd {
	files := m.hooks.Models.Files
	if files == nil {
		return nil
	}
	m.mdl.busy = "judging every file of " + repo + " against this machine"
	ctxLen := m.mdl.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		fv, err := files(ctx, repo, ctxLen)
		return modelsFilesMsg{fv, err}
	}
}

// modelsEnter is the one obvious action on the selected row.
func (m *model) modelsEnter() tea.Cmd {
	switch m.mdl.pane {
	case modelsSearch:
		if m.mdl.sel < len(m.mdl.results) {
			m.mdl.ctx = 0
			return m.modelsOpenRepo(m.mdl.results[m.mdl.sel].Repo)
		}
	case modelsRepo:
		vis := m.modelsFilesVisible()
		if m.mdl.sel >= len(vis) {
			return nil
		}
		fv, c := m.mdl.files, m.mdl.files.Choices[vis[m.mdl.sel]]
		switch {
		case c.Unservable != "":
			m.mdl.says = c.File + " is not servable: " + c.Unservable
		case c.Have:
			m.mdl.says = c.File + " is already on this machine"
		default:
			fetch := m.hooks.Models.Fetch
			lic := fv.License
			if lic == "" {
				lic = "not stated"
			}
			lines := []string{fv.Repo + "  " + c.File + "  " + modelsBytes(c.Bytes), c.Line}
			if c.Sum != "" {
				lines = append(lines, c.Sum)
			}
			if c.PaceLine != "" {
				lines = append(lines, c.PaceLine)
			}
			lines = append(lines, "licence: "+lic+" (as its author published it; recorded, never a gate)",
				"source: HuggingFace - the author's word, checked against the digest the index publishes")
			m.mdl.ask = &modelsAsk{title: "fetch this model?", lines: lines,
				do: func() tea.Cmd {
					return modelsDo(func() (string, error) {
						if err := fetch(fv.Repo, c.File); err != nil {
							return "", err
						}
						return "fetching " + c.File + " - it keeps going if you leave this screen", nil
					})
				}}
		}
	default:
		if m.mdl.sel >= len(m.mdl.view.Models) {
			return nil
		}
		r := m.mdl.view.Models[m.mdl.sel]
		if r.Serving {
			m.mdl.says = r.ID + " is already on the air"
			return nil
		}
		if !m.mdl.view.CanServe {
			m.mdl.says = m.mdl.view.Runtime
			return nil
		}
		serve, cur := m.hooks.Models.Serve, m.mdl.view.Serving
		title, lines := "serve "+r.ID+"?", []string{"on the air as \"" + r.Band + "\", in the background: leaving this screen does not stop it"}
		if cur != nil {
			title = "switch to " + r.ID + "?"
			lines = append(lines, "it is started and proven FIRST; only then is "+cur.ID+" stopped")
		}
		m.mdl.ask = &modelsAsk{title: title, lines: lines,
			do: func() tea.Cmd { return modelsDo(func() (string, error) { return serve(r.ID, 0) }) }}
	}
	return nil
}

// modelsBytes is a size the way every roger screen says sizes.
func modelsBytes(n int64) string { return provision.BytesWord(n) }

func modelsSecs(s int64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
}

// modelsBar is the fetch as a bar of real proportion. cells is its width.
func modelsBar(done, total int64, cells int) string {
	if total <= 0 || cells <= 0 {
		return strings.Repeat("░", max(cells, 0))
	}
	if done > total {
		done = total
	}
	n := int(float64(cells) * float64(done) / float64(total))
	return strings.Repeat("█", n) + strings.Repeat("░", cells-n)
}

// carrier is the one moving thing, and it moves only while bytes do.
var carrierFrames = []string{"·  ", "·· ", "···", " ··", "  ·", "   "}

func (m model) modelsView(w int) string {
	s := m.mdl
	var b strings.Builder
	line := func(t string) { b.WriteString(truncVisible(t, w) + "\n") }
	line("")
	line("  " + stKey.Render("MODELS") + stDim.Render("  serve a model here with nothing else installed"))
	if s.err != "" {
		line("  " + stEmber.Render("could not read this machine: ") + s.err)
	}
	if !s.loaded {
		line("  " + stDim.Render("reading this machine…"))
		return b.String()
	}
	v := s.view
	machine := "cannot say - " + v.BudgetWhy
	if v.Budget > 0 {
		machine = modelsBytes(v.Budget) + " of " + v.Pool
	}
	line("  " + stDim.Render("THIS MACHINE  ") + machine)
	line("  " + stDim.Render("RUNTIME       ") + v.Runtime)
	if sv := v.Serving; sv != nil {
		since := ""
		if !sv.Since.IsZero() {
			since = " · for " + modelsSecs(int64(time.Since(sv.Since)/time.Second))
		}
		line("  " + stLive.Render("▶ ON THE AIR  ") + stKey.Render("\""+sv.Band+"\"") +
			stDim.Render(fmt.Sprintf(" · %s · %s · %d context%s", sv.ID, modelsDash(sv.Quant), sv.Ctx, since)))
	} else {
		line("  " + stDim.Render("ON THE AIR    nothing - pick a model below and press ⏎"))
	}
	if j := v.Job; j != nil {
		head := map[string]string{"fetching": "FETCHING", "done": "FETCHED + CHECKED", "failed": "FETCH FAILED",
			"interrupted": "INTERRUPTED"}[j.State]
		cells := max(10, min(30, w-60))
		info := modelsBytes(j.Done) + " of " + modelsBytes(j.Total)
		anim := ""
		if j.State == "fetching" {
			if j.Rate > 0 {
				info += " · " + modelsBytes(int64(j.Rate)) + "/s"
			}
			if j.RemainingS > 0 && j.Done*50 >= j.Total {
				info += " · " + modelsSecs(j.RemainingS) + " left"
			}
			anim = " " + carrierFrames[s.frame%len(carrierFrames)]
		}
		line("  " + stKey.Render(pad(head, 18)) + stDim.Render(j.File) + anim)
		line("  " + pad("", 18) + modelsBar(j.Done, j.Total, cells) + "  " + info)
		switch j.State {
		case "failed":
			line("  " + pad("", 18) + stEmber.Render(j.Err))
			if j.Fix != "" {
				line("  " + pad("", 18) + "fix: " + j.Fix)
			}
		case "interrupted":
			line("  " + pad("", 18) + stDim.Render("fetch it again and it resumes from here"))
		}
	}
	if v.Serving == nil && len(v.ServeLog) > 0 {
		line("  " + stEmber.Render("THE LAST SERVE DID NOT COME ON THE AIR") + stDim.Render(" - its last words:"))
		for _, l := range v.ServeLog {
			line("    " + stDim.Render(l))
		}
	}
	line("")
	switch s.pane {
	case modelsSearch:
		m.modelsSearchView(w, line)
	case modelsRepo:
		m.modelsRepoView(w, line)
	default:
		m.modelsDiskView(w, line)
	}
	line("")
	if s.typing {
		line("  " + stKey.Render("/ ") + s.query + "█" + stDim.Render("   ⏎ search · esc cancel"))
	} else if s.query != "" && s.pane != modelsDisk {
		line("  " + stDim.Render("search: "+s.query))
	}
	if s.busy != "" {
		line("  " + stDim.Render(s.busy+"…"))
	}
	if a := s.ask; a != nil {
		line("  " + stKey.Render(strings.ToUpper(a.title[:1])+a.title[1:]))
		for _, l := range a.lines {
			line("    " + l)
		}
		line("  " + stKey.Render("y") + " yes   " + stKey.Render("any other key") + " no " + stDim.Render("(⏎ alone is no)"))
	} else if s.says != "" {
		line("  " + s.says)
	}
	return b.String()
}

func modelsDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (m model) modelsDiskView(w int, line func(string)) {
	v := m.mdl.view
	line("  " + stKey.Render("ON THIS MACHINE") + stDim.Render(fmt.Sprintf("  %d models · %s", len(v.Models), modelsBytes(v.DiskUsed))))
	if len(v.Models) == 0 {
		line("  " + stDim.Render("nothing yet - press / to look for one, or drop a .gguf into"))
		line("  " + stDim.Render(v.Dir))
		return
	}
	idw := min(48, max(20, w-36))
	for i, r := range v.Models {
		cur, mark := "  ", " "
		if i == m.mdl.sel {
			cur = stKey.Render("▸ ")
		}
		if r.Serving {
			mark = "▶"
		}
		check := "checked"
		if !r.Verified {
			check = "your word"
		}
		line("  " + cur + mark + " " + pad(truncVisible(r.ID, idw), idw) + " " + pad(modelsDash(r.Quant), 8) +
			pad(modelsBytes(r.Bytes), 11) + stDim.Render(check))
	}
}

func (m model) modelsSearchView(w int, line func(string)) {
	line("  " + stKey.Render("FROM HUGGINGFACE") + stDim.Render(fmt.Sprintf("  %d repos - what their authors published, not RogerAI's word", len(m.mdl.results))))
	if len(m.mdl.results) == 0 {
		line("  " + stDim.Render("nothing with servable weights matched"))
	}
	rw := min(52, max(20, w-30))
	for i, r := range m.mdl.results {
		cur := "  "
		if i == m.mdl.sel {
			cur = stKey.Render("▸ ")
		}
		gate := ""
		if r.Gated {
			gate = "  GATED"
		}
		line("  " + cur + pad(truncVisible(r.Repo, rw), rw) + " " + pad(strconv.Itoa(r.Downloads), 9) + stDim.Render(modelsDash(r.License)) + gate)
	}
}

func (m model) modelsRepoView(w int, line func(string)) {
	fv := m.mdl.files
	lic := fv.License
	if lic == "" {
		lic = "not stated"
	}
	filter := "all files"
	if m.mdl.fitsOnly {
		filter = "only what fits"
	}
	line("  " + stKey.Render(fv.Repo) + stDim.Render("  licence "+lic+" · judged at "+strconv.Itoa(fv.Ctx)+" context · "+filter))
	vis := m.modelsFilesVisible()
	if len(vis) == 0 {
		line("  " + stDim.Render("nothing here fits this machine at this context - f shows all, - halves the context"))
	}
	lw := 8
	for _, i := range vis {
		lw = max(lw, len(fv.Choices[i].Label))
	}
	lw = min(lw, max(8, w/3))
	for j, i := range vis {
		c := fv.Choices[i]
		cur := "  "
		if j == m.mdl.sel {
			cur = stKey.Render("▸ ")
		}
		// The verdict is a WORD in its own column, so what fits reads without colour.
		mark, verdict := " ", c.Short
		switch {
		case c.Unservable != "":
			verdict = "not servable: " + c.Unservable
		case c.Suggested:
			mark = "*"
		}
		if c.Pace != "" && c.Unservable == "" {
			verdict += "  " + c.Pace
		}
		if c.Have {
			verdict += "  · on this machine"
		}
		line("  " + cur + mark + " " + pad(truncVisible(c.Label, lw), lw) + " " + pad(modelsBytes(c.Bytes), 11) + verdict)
	}
	if j := m.mdl.sel; j < len(vis) {
		if c := fv.Choices[vis[j]]; c.Sum != "" || c.PaceLine != "" {
			line("")
			if c.Sum != "" {
				line("  " + stDim.Render(c.Sum))
			}
			if c.PaceLine != "" {
				line("  " + stDim.Render(c.PaceLine))
			}
		}
	}
}

// modelsKeys is the footer for this screen: only the keys that do something here, now.
func (m model) modelsKeys() string {
	s := m.mdl
	switch {
	case s.ask != nil:
		return "y yes · any other key no"
	case s.typing:
		return "type to search · ⏎ search · esc cancel"
	case s.pane == modelsSearch:
		return "↑↓ pick · ⏎ see what fits · / search · esc back"
	case s.pane == modelsRepo:
		return "↑↓ pick · ⏎ fetch · f only what fits · +/- context · esc back"
	}
	k := "↑↓ pick · ⏎ serve · x remove · / find one"
	if s.view.Serving != nil {
		k += " · S stop"
	}
	return k + " · esc SHARE"
}
