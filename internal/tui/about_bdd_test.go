package tui

// about_bdd_test.go - the godog harness for the @tui scenarios of features/details: the ABOUT
// block at the top of the [1] TUNE IN station log (i), and the details fields of the SHARE g
// editor. It drives the REAL dial through Update (offersMsg, keys) and reads the REAL views.
// The broker's half (curated notes stamped onto offers) is played by catalog.Notes.Apply,
// the function the broker calls.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/protocol"
)

var aboutDemoNotes = []catalog.Note{
	{ID: "demo-1b-chat", Summary: "General chat on the demo 1B base.", BestAt: "Conversation, short technical writing.",
		NotFor: "Fire decisions or reading sensors.", BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html"},
	{ID: "demo-1b-fire-decision", Summary: "Picks one fire-defense action from site state.", BestAt: "Choosing among a fixed list of actions.",
		NotFor: "Chat. It answers with one action only.", BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html#fire"},
	{ID: "demo-1b-fire-vl", Summary: "Confirms or rejects smoke or flame in a frame.", BestAt: "Single camera frames, yes or no.",
		NotFor: "Video, or deciding what to do about it.", BaseModel: "demo-1b", License: "Apache-2.0", Page: "/research-demo-1b.html#vl"},
}

type aboutBDD struct {
	offers   []offer
	raw      map[string]bool // stations whose text skips canonicalization (an old broker)
	notes    catalog.Notes
	w, h     int
	compact  bool
	m        model
	rendered string // the last render a width/byte assertion reads
	share    shareTaxBDD
}

func unescAbout(s string) string {
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return s
}

func (s *aboutBDD) setNotes(ns []catalog.Note) error {
	b, _ := json.Marshal(ns)
	n, err := catalog.ParseNotes(b)
	s.notes = n
	return err
}

func (s *aboutBDD) station(id string) *offer {
	for i := range s.offers {
		if s.offers[i].NodeID == id {
			return &s.offers[i]
		}
	}
	return nil
}

func (s *aboutBDD) table(t *godog.Table) error {
	head := t.Rows[0].Cells
	for _, row := range t.Rows[1:] {
		v := map[string]string{}
		for i, c := range row.Cells {
			v[strings.TrimSpace(head[i].Value)] = strings.TrimSpace(c.Value)
		}
		o := offer{NodeID: v["station"], Region: "home", Model: v["model"], Modality: v["modality"],
			Online: v["online"] != "no", Curated: v["curated"] == "yes", TPS: 40,
			Taxonomy: protocol.Taxonomy{Tuning: v["tuning"], BaseModel: v["base_model"], License: v["license"],
				Summary: v["summary"], BestAt: v["best_at"], NotFor: v["not_for"]}}
		if v["vision"] == "yes" {
			o.Capabilities = []string{protocol.CapVision}
		}
		s.offers = append(s.offers, o)
	}
	return nil
}

func (s *aboutBDD) notesTable(t *godog.Table) error {
	head := t.Rows[0].Cells
	var ns []catalog.Note
	for _, row := range t.Rows[1:] {
		v := map[string]string{}
		for i, c := range row.Cells {
			v[head[i].Value] = c.Value
		}
		ns = append(ns, catalog.Note{ID: v["id"], Summary: v["summary"], BestAt: v["best_at"], NotFor: v["not_for"],
			BaseModel: v["base_model"], License: v["license"], Page: v["page"]})
	}
	return s.setNotes(ns)
}
func (s *aboutBDD) notesDescribe(string) error { return s.setNotes(aboutDemoNotes) }

// build is the broker publishing (canonicalize, stamp notes) and the dial receiving it.
func (s *aboutBDD) build() {
	pub := make([]offer, len(s.offers))
	for i, o := range s.offers {
		if !s.raw[o.NodeID] {
			o.Taxonomy = s.notes.Apply(o.Model, o.Taxonomy.Canonical("chat"))
		}
		pub[i] = o
	}
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
	tm, _ = tm.Update(offersMsg(pub))
	tm, _ = tm.Update(tickMsg{})
	m := asModel(tm)
	m.compact = s.compact
	s.m = m
}

func (s *aboutBDD) send(k tea.KeyMsg) {
	tm, _ := tea.Model(s.m).Update(k)
	s.m = asModel(tm)
}

func (s *aboutBDD) cursorOn(model string) error {
	s.build()
	for i, b := range s.m.visibleBands() {
		if b.model == model {
			s.m.cursor = i
			return nil
		}
	}
	return fmt.Errorf("no band %q on the dial", model)
}
func (s *aboutBDD) openLog(model string) error {
	if err := s.cursorOn(model); err != nil {
		return err
	}
	s.send(keyMsg("i"))
	if s.m.mode != modeBandDetail {
		return fmt.Errorf("i did not open the station log (mode %v)", s.m.mode)
	}
	s.rendered = stripANSI(s.m.bandDetailView(s.m.effWidth()))
	return nil
}
func (s *aboutBDD) press(k string) error {
	switch k {
	case "i":
		s.send(keyMsg("i"))
		s.rendered = stripANSI(s.m.bandDetailView(s.m.effWidth()))
	default:
		s.send(mtKey(k))
	}
	return nil
}
func (s *aboutBDD) isLogFor(model string) error {
	if s.m.mode != modeBandDetail || s.m.detailBand.model != model {
		return fmt.Errorf("not the station log for %s (mode %v, band %q)", model, s.m.mode, s.m.detailBand.model)
	}
	return nil
}

// logLines is the station log, without styling.
func (s *aboutBDD) logLines() []string { return strings.Split(s.rendered, "\n") }

// aboutRows is the ABOUT block: from the ABOUT heading up to the blank line after it.
func (s *aboutBDD) aboutRows() []string {
	var out []string
	in := false
	for _, l := range s.logLines() {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "ABOUT") {
			in = true
		}
		if in {
			if t == "" {
				break
			}
			out = append(out, t)
		}
	}
	return out
}
func (s *aboutBDD) firstIsAbout() error {
	ls := s.logLines()
	if len(ls) < 2 || !strings.Contains(ls[0], "STATION LOG") || !strings.HasPrefix(strings.TrimSpace(ls[1]), "ABOUT") {
		return fmt.Errorf("ABOUT is not the first block under the heading:\n%q", ls[:min(3, len(ls))])
	}
	return nil
}
func (s *aboutBDD) tableAfter() error {
	a, t := strings.Index(s.rendered, "ABOUT"), strings.Index(s.rendered, "callsign")
	if a < 0 || t < a {
		return fmt.Errorf("the station table does not follow ABOUT:\n%s", s.rendered)
	}
	return nil
}
func (s *aboutBDD) outcome(res string) error {
	switch {
	case res == "the screen is the TUNE IN list":
		if s.m.mode != modeBrowse {
			return fmt.Errorf("mode %v, want the TUNE IN list", s.m.mode)
		}
		return nil
	case strings.HasPrefix(res, "the TUI tunes in to "):
		want := strings.Trim(strings.TrimPrefix(res, "the TUI tunes in to "), `"`)
		if s.m.mode != modeConnectConfirm || s.m.q.b.model != want {
			return fmt.Errorf("mode %v band %q, want the tune-in confirm for %s", s.m.mode, s.m.q.b.model, want)
		}
		return nil
	}
	return fmt.Errorf("unknown outcome %q", res)
}
func (s *aboutBDD) logOpen(model string) error { return s.openLog(model) }
func (s *aboutBDD) statusReads(v string) error {
	m := s.m
	if s.share.m.ctrl != nil {
		m = s.share.m
	}
	if got := strings.TrimSpace(stripANSI(m.status)); got != v {
		return fmt.Errorf("status %q, want %q", got, v)
	}
	return nil
}
func (s *aboutBDD) openHelp() error {
	s.build()
	s.send(keyMsg("?"))
	s.rendered = stripANSI(s.m.helpView())
	return nil
}
func (s *aboutBDD) helpLists(key, what string) error {
	for _, l := range strings.Split(s.rendered, "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && f[0] == key && strings.Contains(strings.ToLower(l), what) {
			return nil
		}
	}
	return fmt.Errorf("help has no %q key cell saying %q", key, what)
}

func (s *aboutBDD) headingReads(v string) error {
	r := s.aboutRows()
	if len(r) == 0 || r[0] != v {
		return fmt.Errorf("ABOUT heading %q, want %q\n%s", firstOr(r), v, s.rendered)
	}
	return nil
}
func firstOr(r []string) string {
	if len(r) == 0 {
		return ""
	}
	return r[0]
}
func (s *aboutBDD) shows(v string) error {
	for _, r := range s.aboutRows() {
		if strings.Contains(r, v) {
			return nil
		}
	}
	return fmt.Errorf("the ABOUT block does not show %q:\n%s", v, s.rendered)
}
func (s *aboutBDD) notShows(v string) error {
	if strings.Contains(strings.Join(s.aboutRows(), "\n"), v) {
		return fmt.Errorf("the ABOUT block shows %q", v)
	}
	return nil
}
func (s *aboutBDD) hasLine(label string) bool {
	for _, r := range s.aboutRows() {
		if strings.HasPrefix(r, label+" ") {
			return true
		}
	}
	return false
}
func (s *aboutBDD) noLine(label string) error {
	if s.hasLine(label) {
		return fmt.Errorf("the ABOUT block has a %q line:\n%s", label, s.rendered)
	}
	return nil
}
func (s *aboutBDD) noLines(a, b string) error {
	if err := s.noLine(a); err != nil {
		return err
	}
	return s.noLine(b)
}
func (s *aboutBDD) sameBase() string {
	for _, r := range s.aboutRows() {
		if v, ok := strings.CutPrefix(r, "same base "); ok {
			return v
		}
	}
	return ""
}
func (s *aboutBDD) siblingsCap(n int, more string) error {
	v := s.sameBase()
	names, tail, _ := strings.Cut(v, " +")
	if got := len(strings.Split(names, ", ")); got != n || "+"+tail != more {
		return fmt.Errorf("same base %q: want %d names then %q", v, n, more)
	}
	return nil
}
func (s *aboutBDD) siblingsLack(name string) error {
	for _, n := range strings.Split(strings.Split(s.sameBase(), " +")[0], ", ") {
		if n == name {
			return fmt.Errorf("same base %q names %s", s.sameBase(), name)
		}
	}
	return nil
}
func (s *aboutBDD) siblingsHave(name string) error {
	if !strings.Contains(", "+s.sameBase()+",", ", "+name+",") {
		return fmt.Errorf("same base %q does not name %s", s.sameBase(), name)
	}
	return nil
}

// ---- station edits
func (s *aboutBDD) add(o offer) error {
	if o.Region == "" {
		o.Region, o.TPS = "home", 40
	}
	s.offers = append(s.offers, o)
	return nil
}
func (s *aboutBDD) sixMore(base string) error {
	for i := 1; i <= 6; i++ {
		_ = s.add(offer{NodeID: fmt.Sprintf("x%d", i), Model: fmt.Sprintf("demo-1b-extra-%d", i), Online: true,
			Taxonomy: protocol.Taxonomy{BaseModel: base}})
	}
	return nil
}
func (s *aboutBDD) offAirBand(model, base string) error {
	return s.add(offer{NodeID: "old", Model: model, Taxonomy: protocol.Taxonomy{BaseModel: base}})
}
func (s *aboutBDD) secondQuant(model, quant, base string) error {
	return s.add(offer{NodeID: "q8", Model: model, Quant: quant, Online: true, Taxonomy: protocol.Taxonomy{BaseModel: base}})
}
func (s *aboutBDD) onAirBand(model, base string) error {
	return s.add(offer{NodeID: "bx", Model: model, Online: true, Taxonomy: protocol.Taxonomy{BaseModel: base}})
}
func (s *aboutBDD) goesOff(id string) error { s.station(id).Online = false; return nil }
func (s *aboutBDD) summaryNoBest(id, sum string) error {
	o := s.station(id)
	o.Summary, o.BestAt, o.NotFor = sum, "", ""
	return nil
}
func (s *aboutBDD) summaryBest(id, sum, best string) error {
	o := s.station(id)
	o.Summary, o.BestAt, o.NotFor = sum, best, ""
	return nil
}
func (s *aboutBDD) catLicense(id, pair, lic string) error {
	o := s.station(id)
	o.Category, o.Subcategory = protocol.SplitCategory(pair)
	o.License, o.TaxonomySource = lic, protocol.SourceDeclared
	return nil
}
func (s *aboutBDD) catTagsPolicy(id, pair, tags, pol string) error {
	o := s.station(id)
	o.Category, o.Subcategory = protocol.SplitCategory(pair)
	o.Tags, o.ContentPolicy, o.TaxonomySource = strings.Split(tags, ","), pol, protocol.SourceDeclared
	return nil
}
func (s *aboutBDD) policy(id, pol string) error {
	o := s.station(id)
	o.ContentPolicy, o.TaxonomySource = pol, protocol.SourceDeclared
	return nil
}
func words(n int) string { // whole 8-letter words, at most n characters
	var b strings.Builder
	for b.Len()+9 <= n {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("abcdefgh")
	}
	return b.String()
}

// declaration is a station and every station carrying the same words (in the Background s4
// and s5 are one operator's declaration on two stations), so a layout scenario that edits
// "s4's text" edits the text the band shows rather than starting a new vote.
func (s *aboutBDD) declaration(id string, f func(o *offer)) {
	o := s.station(id)
	key := o.Summary + "\x00" + o.BestAt + "\x00" + o.NotFor
	for i := range s.offers {
		if p := &s.offers[i]; p.Model == o.Model && p.Summary+"\x00"+p.BestAt+"\x00"+p.NotFor == key {
			f(p)
		}
	}
}
func (s *aboutBDD) bestWords(id string) error {
	s.declaration(id, func(o *offer) { o.BestAt = strings.TrimSpace(words(200)) })
	return nil
}
func (s *aboutBDD) oneWord(id string) error {
	s.declaration(id, func(o *offer) { o.Summary = strings.Repeat("w", 140) })
	return nil
}
func (s *aboutBDD) wide(id, ch string) error {
	s.declaration(id, func(o *offer) { o.Summary = strings.Repeat(ch, 140) })
	return nil
}
func (s *aboutBDD) maxFields() error {
	s.declaration("s4", func(o *offer) {
		o.Summary, o.BestAt, o.NotFor = strings.TrimSpace(words(140)), strings.TrimSpace(words(200)), strings.TrimSpace(words(200))
	})
	return nil
}
func (s *aboutBDD) rawArrives(id, sum string) error {
	if s.raw == nil {
		s.raw = map[string]bool{}
	}
	s.declaration(id, func(o *offer) { o.Summary = unescAbout(sum); s.raw[o.NodeID] = true })
	return nil
}
func (s *aboutBDD) proxy(model, sum string) error {
	return s.add(offer{NodeID: "proxy", Model: model, Online: true, Curated: true, CuratedProvider: "upstream",
		Taxonomy: protocol.Taxonomy{Summary: sum}})
}

// ---- the empty and category cases
func (s *aboutBDD) noAbout() error {
	if len(s.aboutRows()) != 0 {
		return fmt.Errorf("the station log has an ABOUT block:\n%s", s.rendered)
	}
	return nil
}
func (s *aboutBDD) logShows(v string) error {
	if !strings.Contains(s.rendered, v) {
		return fmt.Errorf("the station log does not show %q:\n%s", v, s.rendered)
	}
	return nil
}
func (s *aboutBDD) categoryInAbout() error {
	want := bandTaxonomyDetail(s.m.detailBand)
	if len(want) == 0 {
		return fmt.Errorf("the band has no category detail")
	}
	return s.shows(want[0])
}
func (s *aboutBDD) categoryOnce() error {
	want := bandTaxonomyDetail(s.m.detailBand)[0]
	if n := strings.Count(s.rendered, want); n != 1 {
		return fmt.Errorf("the category detail appears %d times", n)
	}
	return nil
}

// ---- layout
func (s *aboutBDD) width(w int) error { s.w = w; return nil }
func (s *aboutBDD) size(w, h int) error {
	s.w, s.h = w, h
	return nil
}
func (s *aboutBDD) notWider(w int) error {
	for _, l := range strings.Split(s.rendered, "\n") {
		if lipgloss.Width(l) > w {
			return fmt.Errorf("a line is %d columns wide (max %d): %q", lipgloss.Width(l), w, l)
		}
	}
	return nil
}
func (s *aboutBDD) bestRows() []string {
	var out []string
	in := false
	for _, r := range s.aboutRows() {
		switch {
		case strings.HasPrefix(r, "best at "):
			in = true
			r = strings.TrimSpace(strings.TrimPrefix(r, "best at"))
		case in && (strings.HasPrefix(r, "not for ") || strings.HasPrefix(r, "kind ")):
			return out
		}
		if in {
			out = append(out, r)
		}
	}
	return out
}
func (s *aboutBDD) splitAtSpaces() error {
	rows := s.bestRows()
	if len(rows) < 2 {
		return fmt.Errorf("best at did not wrap: %q", rows)
	}
	for _, r := range rows {
		for _, w := range strings.Fields(strings.TrimSuffix(r, "…")) {
			if w != "abcdefgh" {
				return fmt.Errorf("a word was split: %q in %q", w, r)
			}
		}
	}
	return nil
}
func (s *aboutBDD) indented() error {
	lines := s.logLines()
	for i, l := range lines {
		if strings.Contains(l, "best at ") && i+1 < len(lines) {
			col := strings.Index(l, "best at") + about10
			next := lines[i+1]
			if len(next) <= col || strings.TrimSpace(next[:col]) != "" || next[col] == ' ' {
				return fmt.Errorf("continuation not under the value column %d: %q", col, next)
			}
			return nil
		}
	}
	return fmt.Errorf("no best at line")
}

const about10 = 10

func (s *aboutBDD) showsSummary() error {
	b := s.m.detailBand
	if b.about.Summary == "" {
		return fmt.Errorf("the band has no summary")
	}
	return s.shows(strings.Fields(b.about.Summary)[0])
}
func (s *aboutBDD) atMostRows(n int) error {
	if got := len(s.aboutRows()); got > n {
		return fmt.Errorf("the ABOUT block is %d rows (max %d)", got, n)
	}
	return nil
}
func (s *aboutBDD) firstStationVisible() error {
	v := stripANSI(s.m.View())
	if lines := strings.Split(v, "\n"); len(lines) > s.h {
		return fmt.Errorf("the frame is %d rows on a %d-row terminal", len(lines), s.h)
	}
	if !strings.Contains(v, "s4") {
		return fmt.Errorf("no station row visible:\n%s", v)
	}
	return nil
}
func (s *aboutBDD) compactOn() error { s.compact = true; return nil }
func (s *aboutBDD) summaryShown() error {
	if v := stripANSI(s.m.View()); !strings.Contains(v, "General chat on the demo 1B base.") {
		return fmt.Errorf("the compact station log does not show the summary:\n%s", v)
	}
	return nil
}
func (s *aboutBDD) screenHas(v string) error {
	if !strings.Contains(stripANSI(s.m.View()), unescAbout(v)) {
		return fmt.Errorf("the screen does not show %q", v)
	}
	return nil
}
func (s *aboutBDD) noStationESC() error {
	if strings.Contains(s.m.View(), "a\x1b[31mb") {
		return fmt.Errorf("the station's ESC reached the screen")
	}
	return nil
}
func (s *aboutBDD) pure() error {
	a, b := s.m.bandDetailView(s.m.effWidth()), s.m.bandDetailView(s.m.effWidth())
	if a != b {
		return fmt.Errorf("two renders differ")
	}
	return nil
}

// ---- curated label, lineage and the cross-surface reads
func (s *aboutBDD) curatedStation(model, lineage string) error {
	return s.add(offer{NodeID: "c1", Model: model, Online: true, Confidential: lineage == "with"})
}
func (s *aboutBDD) labels(v string) error { return s.shows(v) }
func (s *aboutBDD) notVerified() error    { return s.shows("not verified") }
func (s *aboutBDD) lineageBeside(v string) error {
	r := s.aboutRows()
	if len(r) == 0 || !strings.Contains(r[0], v) || !strings.Contains(r[0], "◆") {
		return fmt.Errorf("no ◆ beside %q: %q", v, firstOr(r))
	}
	return nil
}
func (s *aboutBDD) bestEsc(a, b string) error {
	return s.add(offer{NodeID: "e1", Model: "esc-model", Online: true, Taxonomy: protocol.Taxonomy{BestAt: unescAbout(a) + " " + unescAbout(b)}})
}
func (s *aboutBDD) rendersBlock() error {
	if err := s.openLog("esc-model"); err != nil {
		return err
	}
	s.rendered = s.m.bandDetailView(s.m.effWidth()) // raw: styling escapes are ours; the check below reads the text
	return nil
}
func (s *aboutBDD) noByte(b string) error {
	n, _ := strconv.ParseUint(strings.TrimPrefix(b, "0x"), 16, 8)
	for _, r := range s.m.aboutRows(s.m.detailBand, s.m.effWidth()) {
		if strings.ContainsRune(r, rune(n)) {
			return fmt.Errorf("byte %s in %q", b, r)
		}
	}
	return nil
}
func (s *aboutBDD) oldBroker() error { s.raw = map[string]bool{}; return nil }
func (s *aboutBDD) itsOffer(model, sum string) error {
	_ = s.add(offer{NodeID: "o1", Model: model, Online: true, Taxonomy: protocol.Taxonomy{Summary: unescAbout(sum)}})
	s.raw["o1"] = true
	return nil
}
func (s *aboutBDD) readsOffer() error { return s.openLog("demo-1b-chat") }

// ---- the g editor (declaration.feature), on the REAL controller via shareTaxBDD
func (s *aboutBDD) controller(model, inferred string) error { return s.share.lists(model, inferred) }
func (s *aboutBDD) selected(model string) error             { return s.share.cursor(model) }
func (s *aboutBDD) pressG() error                           { s.share.send(keyMsg("g")); return nil }
func (s *aboutBDD) editorRows() []string {
	var out []string
	for _, l := range strings.Split(stripANSI(s.share.m.shareMetaView(160)), "\n")[1:] {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "▸"))
		for _, lab := range []string{"category", "subcategory", "tags", "tuning", "policy", "license", "base model", "summary", "best at", "not for"} {
			if strings.HasPrefix(t, lab+" ") || t == lab {
				out = append(out, lab)
				break
			}
		}
	}
	return out
}
func (s *aboutBDD) rowsAre(v string) error {
	if got := strings.Join(s.editorRows(), ", "); got != v {
		return fmt.Errorf("editor rows %q, want %q", got, v)
	}
	return nil
}
func (s *aboutBDD) rowsInclude(v string) error {
	if !strings.Contains(strings.Join(s.editorRows(), ", "), v) {
		return fmt.Errorf("editor rows %q do not include %q", s.editorRows(), v)
	}
	return nil
}
func (s *aboutBDD) editorOpen(model string) error { return s.share.open(model) }
func (s *aboutBDD) focus(name string) error {
	f := map[string]int{"summary": mtFieldSummary, "best at": mtFieldBestAt, "not for": mtFieldNotFor}[name]
	s.share.focus(f)
	if s.share.m.mtField != f {
		return fmt.Errorf("could not reach the %s field", name)
	}
	return nil
}
func (s *aboutBDD) typeText(text string) {
	for _, r := range text {
		if r == ' ' {
			s.share.send(tea.KeyMsg{Type: tea.KeySpace})
		} else {
			s.share.send(keyMsgRunes(r))
		}
	}
}
func (s *aboutBDD) typeAndSave(field, text string) error {
	if err := s.focus(field); err != nil {
		return err
	}
	s.typeText(text)
	s.share.send(mtKey("enter"))
	return nil
}
func (s *aboutBDD) savedSummary(model, v string) error {
	d, _, _ := s.share.m.ctrl.MetaFor(model)
	if d.Summary != v {
		return fmt.Errorf("saved summary %q, want %q", d.Summary, v)
	}
	return nil
}
func (s *aboutBDD) typeN(n int, field string) error {
	if err := s.focus(field); err != nil {
		return err
	}
	s.typeText(strings.Repeat("a", n))
	return nil
}
func (s *aboutBDD) rowShows(v string) error {
	if !strings.Contains(stripANSI(s.share.m.shareMetaView(160)), v) {
		return fmt.Errorf("the editor does not show %q", v)
	}
	return nil
}
func (s *aboutBDD) holdsN(n int) error {
	if got := utf8.RuneCountInString(s.share.m.mtSummary); got != n {
		return fmt.Errorf("summary holds %d characters, want %d", got, n)
	}
	return nil
}
func (s *aboutBDD) paste(text, field string) error {
	if err := s.focus(field); err != nil {
		return err
	}
	s.share.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(unescAbout(strings.ReplaceAll(text, "\n", `\n`))), Paste: true})
	return nil
}
func (s *aboutBDD) holds(v string) error {
	if s.share.m.mtSummary != v {
		return fmt.Errorf("summary holds %q, want %q", s.share.m.mtSummary, v)
	}
	return nil
}
func (s *aboutBDD) setAndSave(text string) error { return s.typeAndSave("summary", text) }
func (s *aboutBDD) stillOpen() error {
	if s.share.m.mode != modeShareMeta {
		return fmt.Errorf("the editor closed")
	}
	return nil
}
func (s *aboutBDD) editorShows(v string) error {
	return s.rowShows(strings.ReplaceAll(v, `\"`, `"`))
}
func (s *aboutBDD) voice(model, _ string) error { return s.share.listsTTS(model) }
func (s *aboutBDD) openFor(model string) error  { return s.share.open(model) }
func (s *aboutBDD) longBest(w, n int) error {
	if err := s.share.open("demo-1b-fire-decision"); err != nil {
		return err
	}
	s.share.m.mtBestAt = strings.Repeat("b", n)
	s.share.m.mtField = mtFieldBestAt
	s.rendered = stripANSI(s.share.m.shareMetaView(w))
	return nil
}

func TestAboutTUIBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &aboutBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = aboutBDD{w: 120, h: 60}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^the dial shows these stations:$`, st.table)
			sc.Step(`^RogerAI's curated notes describe (.*) as in curated_catalog\.feature$`, st.notesDescribe)
			sc.Step(`^RogerAI's curated notes contain:$`, st.notesTable)
			sc.Step(`^the cursor is on the band `+q+`$`, st.cursorOn)
			sc.Step(`^the operator presses "(i|esc|left|h|q|enter)"$`, st.press)
			sc.Step(`^the screen is the station log for `+q+`$`, st.isLogFor)
			sc.Step(`^the first block under the heading is ABOUT$`, st.firstIsAbout)
			sc.Step(`^the station table follows the ABOUT block$`, st.tableAfter)
			sc.Step(`^the station log for `+q+` is open$`, st.logOpen)
			sc.Step(`^(the screen is the TUNE IN list|the TUI tunes in to ".*")$`, st.outcome)
			sc.Step(`^the status line reads `+q+`$`, st.statusReads)
			sc.Step(`^the operator opens help with "\?"$`, st.openHelp)
			sc.Step(`^the TUNE IN keys list `+q+` as `+q+`$`, st.helpLists)
			sc.Step(`^the operator opens the station log for `+q+`$`, st.openLog)
			sc.Step(`^the ABOUT heading reads `+q+`$`, st.headingReads)
			sc.Step(`^the ABOUT block shows `+q+`$`, st.shows)
			sc.Step(`^the ABOUT block does not show `+q+`$`, st.notShows)
			sc.Step(`^the ABOUT block has no `+q+` line$`, st.noLine)
			sc.Step(`^the ABOUT block has no `+q+` or `+q+` line$`, st.noLines)
			sc.Step(`^the "same base" line names (\d+) bands then `+q+`$`, st.siblingsCap)
			sc.Step(`^the "same base" line does not name `+q+`$`, st.siblingsLack)
			sc.Step(`^the "same base" line names `+q+`$`, st.siblingsHave)
			sc.Step(`^six more on-air bands with base model `+q+`$`, st.sixMore)
			sc.Step(`^an off-air band `+q+` with base model `+q+`$`, st.offAirBand)
			sc.Step(`^a second band `+q+` with quant `+q+` and base model `+q+`$`, st.secondQuant)
			sc.Step(`^an on-air band `+q+` with base model `+q+`$`, st.onAirBand)
			sc.Step(`^station `+q+` goes off air$`, st.goesOff)
			sc.Step(`^station `+q+` is off air$`, st.goesOff)
			sc.Step(`^station `+q+` has summary `+q+` and no best_at$`, st.summaryNoBest)
			sc.Step(`^station `+q+` has summary `+q+` and best_at `+q+`$`, st.summaryBest)
			sc.Step(`^a curated proxy station on `+q+` with summary `+q+`$`, st.proxy)
			sc.Step(`^the station log has no ABOUT block$`, st.noAbout)
			sc.Step(`^the station log shows `+q+`$`, st.logShows)
			sc.Step(`^station `+q+` declares category `+q+` and license `+q+`$`, st.catLicense)
			sc.Step(`^station `+q+` declares category `+q+`, tags `+q+` and policy `+q+`$`, st.catTagsPolicy)
			sc.Step(`^station `+q+` declares policy `+q+`$`, st.policy)
			sc.Step(`^the ABOUT block contains the line produced by the existing category detail$`, st.categoryInAbout)
			sc.Step(`^the station log shows the category detail exactly once$`, st.categoryOnce)
			sc.Step(`^station `+q+` has best_at of 200 characters made of 8-letter words$`, st.bestWords)
			sc.Step(`^the terminal is (\d+) columns wide$`, st.width)
			sc.Step(`^the terminal is (\d+) columns by (\d+) rows$`, st.size)
			sc.Step(`^no rendered line is wider than (\d+) (?:display )?columns$`, st.notWider)
			sc.Step(`^the best_at text is split only at spaces$`, st.splitAtSpaces)
			sc.Step(`^continuation lines are indented under the value column$`, st.indented)
			sc.Step(`^station `+q+` has summary of one 140-character word$`, st.oneWord)
			sc.Step(`^station `+q+` has summary of 140 `+q+` characters$`, st.wide)
			sc.Step(`^the ABOUT block shows the summary$`, st.showsSummary)
			sc.Step(`^every text field is at its maximum length$`, st.maxFields)
			sc.Step(`^the ABOUT block is at most (\d+) rows$`, st.atMostRows)
			sc.Step(`^the first station row is visible$`, st.firstStationVisible)
			sc.Step(`^the TUI is in its compact layout$`, st.compactOn)
			sc.Step(`^the summary line is shown$`, st.summaryShown)
			sc.Step(`^station `+q+` arrives with summary "(.*)" from a broker that did not normalize it$`, st.rawArrives)
			sc.Step(`^the rendered screen contains "(.*)"$`, st.screenHas)
			sc.Step(`^the rendered screen contains no byte 0x1b from station text$`, st.noStationESC)
			sc.Step(`^the view is rendered twice with the same model$`, func() error { return nil })
			sc.Step(`^both renders are identical$`, st.pure)

			// curated_catalog.feature
			sc.Step(`^a station on air with model `+q+` (with|without) verified lineage$`, st.curatedStation)
			sc.Step(`^the TUI, the console and the website show the details for `+q+`$`, st.openLog)
			sc.Step(`^the TUI shows the details for `+q+`$`, st.openLog)
			sc.Step(`^each labels the text `+q+`$`, st.labels)
			sc.Step(`^each shows that the station's weights are not verified$`, st.notVerified)
			sc.Step(`^the about block shows the ◆ verified lineage mark beside `+q+`$`, st.lineageBeside)
			// text_sanitization.feature
			sc.Step(`^an offer whose best_at contains "(.*)" and "(.*)"$`, st.bestEsc)
			sc.Step(`^the TUI renders the band's about block$`, st.rendersBlock)
			sc.Step(`^the rendered block contains no byte (0x[0-9a-f]+)$`, st.noByte)
			sc.Step(`^a broker that does not normalize details \(an older or third-party broker\)$`, st.oldBroker)
			sc.Step(`^its /discover offer for `+q+` has summary "(.*)"$`, st.itsOffer)
			sc.Step(`^the TUI, the local console and the CLI read that offer$`, st.readsOffer)
			sc.Step(`^each shows the summary "(.*)"$`, st.shows)
			// declaration.feature (the g editor)
			sc.Step(`^a share controller with a detected model `+q+` inferred as `+q+`$`, st.controller)
			sc.Step(`^the SHARE tab with `+q+` selected$`, st.selected)
			sc.Step(`^the operator presses "g"$`, st.pressG)
			sc.Step(`^the editor rows are `+q+`$`, st.rowsAre)
			sc.Step(`^the editor rows include `+q+`$`, st.rowsInclude)
			sc.Step(`^the g editor is open for `+q+`$`, st.editorOpen)
			sc.Step(`^the operator moves to `+q+`, types `+q+` and saves$`, st.typeAndSave)
			sc.Step(`^the saved declaration for `+q+` has summary `+q+`$`, st.savedSummary)
			sc.Step(`^the operator types (\d+) characters into `+q+`$`, st.typeN)
			sc.Step(`^the summary row shows `+q+`$`, st.rowShows)
			sc.Step(`^the summary field holds (\d+) characters$`, st.holdsN)
			sc.Step(`^the operator pastes "(.*)" into `+q+`$`, st.paste)
			sc.Step(`^the summary field holds `+q+`$`, st.holds)
			sc.Step(`^the operator sets summary `+q+` and saves$`, st.setAndSave)
			sc.Step(`^the editor is still open$`, st.stillOpen)
			sc.Step(`^the editor shows "(.*)"$`, st.editorShows)
			sc.Step(`^a detected voice model `+q+` with modality `+q+`$`, st.voice)
			sc.Step(`^the operator opens the g editor for `+q+`$`, st.openFor)
			sc.Step(`^the g editor is open at (\d+) columns with a (\d+)-character best_at$`, st.longBest)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths: []string{"../../features/details/tui_about.feature", "../../features/details/curated_catalog.feature",
				"../../features/details/text_sanitization.feature", "../../features/details/declaration.feature"},
			Tags:     "@tui",
			TestingT: t,
			Strict:   true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("details @tui behavior scenarios failed (see godog output above)")
	}
}
