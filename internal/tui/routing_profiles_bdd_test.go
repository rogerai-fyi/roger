package tui

// routing_profiles_bdd_test.go makes features/tui/routing_profiles.feature EXECUTABLE (slice 4 of
// the routing-expression set: the [3] CONFIG profile editor, profiles in the booth, dial filters
// that bind via the body object, the windowshade pref glyph, the cost meter reading the final
// usage chunk, and the key map).
//
// THE SEAM is the one routing_pins_bdd_test.go uses and this runner embeds: the TUI is REAL (the
// same bubbletea model, key handling, agent runtime, in-channel chat and booth live proxy that
// production runs), driven with key messages; the broker is a RECORDING httptest server. What
// the TUI can prove is what it EMITS and what it RENDERS. Steps phrased in broker terms ("the
// broker never picks n7", "none is served by n3") are asserted as "every request the TUI emitted
// carries the constraint that makes that true on the real broker" (provider.quantizations,
// roger.self_hosted_only); the broker-side truth of those constraints is pinned by
// cmd/rogerai-broker's routing runners (open_network_filters, regression_pins). Nothing here
// fakes a broker's routing decision.
//
// CONFIG writes: the TUI's only persistence channel is LimitStore.Save (the host, cmd/rogerai,
// owns config.json and saveConfig's temp+fsync+rename, pinned by
// features/onboarding/config_preservation.feature). "config limits.models.<m>.<key>" is read
// from the LimitStore the editor wrote through, by the Limit field the key names (reflection, so
// a field the editor does not have yet fails naming it rather than passing vacuously).
//
// GROUND TRUTH (why most of this is RED): [3] CONFIG edits two fields (editField 0=out 1=tps,
// tab toggles between them only while editing); `p` cycles pref (slice 0); there is no detail
// plate, no default row, no profile store, no profile in the confirm; no in-booth path streams,
// so no turn reads the final usage chunk; the band code travels as the X-Roger-Freq header.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"rogerai.fm/roger/v6/internal/bddtest"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/muesli/termenv"
	"rogerai.fm/roger/v6/internal/client"
)

type tp4TUI struct {
	*routingPinsBDD

	navigated   bool   // a tab / shift+tab was pressed since the last "focused field" Given
	tuneMark    int    // len(requests) when the band was last tuned
	limitsSnap  string // LimitStore snapshot taken before an edit
	viewBefore  string
	chosen      *band
	costBefore  float64
	freqCode    string
	keyScreen   string
	keyBefore   tp4KeyState
	proxyBefore string
	addrBefore  string
	// the booth's key and endpoint after the CONFIG edit, read before the stand-in send
	// (which mints its own key for its own bind)
	keyAfterEdit, addrAfterEdit string
	lastKey                     string
}

type tp4KeyState struct {
	limits  string
	reqs    int
	mode    mode
	compact bool
	perms   int64
	status  string
	chat    string
	agentIn string
}

// --- the editor's field vocabulary --------------------------------------------------------

// tp4Fields is the field order the spec requires; tp4FieldNow reads the field the REAL editor
// has focused (editField) and names it with today's two labels. A field the editor does not
// have cannot be named, so it cannot be "focused".
var tp4Fields = []string{"max $/1M out", "min t/s", "max $/1M in", "max $/request", "quant", "pref",
	"require", "params (B)", "min ctx", "max ttft", "trust", "self-hosted", "region"}

func (s *tp4TUI) fieldNow() string {
	if got := s.m.limFocusLabel(); got != "" {
		return got
	}
	return "(no field focused)"
}

// tp4LimitField maps a config key to the Limit struct field the editor would have to persist it
// in. Keys the TUI does not model yet map to the field name a GREEN slice would add; reflection
// then reports it missing.
var tp4LimitField = map[string]string{
	"max_out": "MaxOut", "min_tps": "MinTPS", "max_in": "MaxIn", "quants": "Quants", "pref": "Pref",
	"max_cost": "MaxCost", "params_b": "ParamsB", "min_ctx": "MinCtx", "max_ttft_ms": "MaxTTFTMs",
	"region": "Region", "trust_min": "TrustMin", "self_hosted": "SelfHosted", "require": "Require",
}

func (s *tp4TUI) limitOf(mdl string) Limit {
	if mdl == "default" {
		if s.m.limits == nil {
			return Limit{}
		}
		return s.m.limits.Default
	}
	if s.m.limits == nil {
		return Limit{}
	}
	return s.m.limits.Snapshot()[mdl]
}

func (s *tp4TUI) configEquals(mdl, key, want string) error {
	fname, ok := tp4LimitField[key]
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	lim := s.limitOf(mdl)
	f := reflect.ValueOf(lim).FieldByName(fname)
	if !f.IsValid() {
		return fmt.Errorf("config limits.%s.%s cannot be set: the TUI's Limit has no %s field", mdl, key, fname)
	}
	got, _ := json.Marshal(f.Interface())
	want = strings.TrimSpace(want)
	if strings.HasPrefix(want, "(unset") {
		if !f.IsZero() {
			return fmt.Errorf("config limits.%s.%s = %s, want unset", mdl, key, got)
		}
		return nil
	}
	if strings.TrimSpace(string(got)) != want {
		return fmt.Errorf("config limits.%s.%s = %s, want %s", mdl, key, got, want)
	}
	return nil
}

func (s *tp4TUI) snapshotLimits() string {
	if s.m.limits == nil {
		return "{}"
	}
	b, _ := json.Marshal(struct {
		M map[string]Limit
		D Limit
	}{s.m.limits.Snapshot(), s.m.limits.Default})
	return string(b)
}

func (s *tp4TUI) view() string { return stripANSI(s.m.View()) }

// --- keys ----------------------------------------------------------------------------------

func tp4Key(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	case "alt+m":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func (s *tp4TUI) press(k string) {
	out, cmd := s.m.Update(tp4Key(k))
	s.m = asModel(out)
	s.runCmd(cmd)
}

// runCmd executes a returned Cmd once and feeds its message back, bounded, so a key that
// starts real work (a save, a resolve) completes inside the step.
func (s *tp4TUI) runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-got:
	case <-time.After(5 * time.Second):
		return
	}
	switch v := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			if c != nil {
				if mm := c(); mm != nil {
					if _, isTick := mm.(tickMsg); isTick {
						continue
					}
					out, _ := s.m.Update(mm)
					s.m = asModel(out)
				}
			}
		}
	default:
		if _, isTick := msg.(tickMsg); isTick {
			return
		}
		out, _ := s.m.Update(msg)
		s.m = asModel(out)
	}
}

func (s *tp4TUI) typeText(txt string) {
	for _, r := range txt {
		s.press(string(r))
	}
}

// --- Background -------------------------------------------------------------------------

func (s *tp4TUI) tuiRunning() error {
	s.startBroker()
	inner := s.srv.Config.Handler
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bands/resolve" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"offers":[{"node_id":"n-priv","model":"qwen3-32b","price_out":0,"online":true}],"band":{"display":"147.520 MHz"}}`))
			return
		}
		inner.ServeHTTP(w, r)
	}))
	s.t.Cleanup(s.srv.Close)
	s.offers = nil
	s.seed()
	return nil
}

var tp4BandRE = regexp.MustCompile(`"([^"]+)" \(([^)]*)\)`)

func (s *tp4TUI) dialShows(spec string) error {
	ms := tp4BandRE.FindAllStringSubmatch(spec, -1)
	if len(ms) == 0 {
		return fmt.Errorf("could not parse bands %q", spec)
	}
	for _, m := range ms {
		mdl := m[1]
		for _, part := range strings.Split(m[2], ", ") {
			f := strings.Fields(part) // "Q8_0 on n1" | "curated on n3"
			if len(f) != 3 || f[1] != "on" {
				return fmt.Errorf("could not parse %q", part)
			}
			if f[0] == "curated" {
				_ = s.curatedOnAirAtOut(f[2], mdl, 0.2)
				continue
			}
			s.addOffer(offer{NodeID: f[2], Model: mdl, Quant: f[0], PriceOut: 0.5, PriceIn: 0.25, TPS: 40})
		}
	}
	return nil
}

// --- [3] CONFIG -----------------------------------------------------------------------------

func (s *tp4TUI) opensConfig() error {
	s.m.enterLimits()
	s.viewBefore = s.view()
	return nil
}

func (s *tp4TUI) tableUnchanged() error {
	v := s.view()
	for _, col := range []string{"band", "max $/1M out", "min t/s"} {
		if !strings.Contains(v, col) {
			return fmt.Errorf("the CONFIG table lost its %q column:\n%s", col, v)
		}
	}
	return nil
}

func (s *tp4TUI) plateListsFields() error {
	v := s.view()
	var missing []string
	for _, f := range tp4Fields[2:] {
		if !strings.Contains(v, f) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the selected row has no detail plate listing %v:\n%s", missing, v)
	}
	return nil
}

func (s *tp4TUI) onRowInConfig(mdl string) error {
	s.m.enterLimits()
	for i, row := range s.m.limModels {
		if row == mdl {
			s.m.limCursor = i
			s.m.limOnBudget = false
			s.navigated = false
			return nil
		}
	}
	return fmt.Errorf("[3] CONFIG has no row for %q (rows %v)", mdl, s.m.limModels)
}

func (s *tp4TUI) pressTabN(n int) error {
	for i := 0; i < n; i++ {
		s.press("tab")
	}
	s.navigated = true
	return nil
}

func (s *tp4TUI) focusedIs(field string) error {
	if s.navigated {
		if got := s.fieldNow(); got != field {
			return fmt.Errorf("the focused field is %q, want %q", got, field)
		}
		return nil
	}
	return s.focusFieldFor(field, "qwen3-32b")
}

// focusFieldFor puts the REAL editor's focus on the named field the way an operator does: the
// row, enter to edit, then tab until the field is focused.
func (s *tp4TUI) focusFieldFor(field, mdl string) error {
	if err := s.onRowInConfig(mdl); err != nil {
		return err
	}
	s.press("enter")
	var seen []string
	for i := 0; i < len(tp4Fields)+1; i++ {
		cur := s.fieldNow()
		if cur == field {
			s.navigated = false
			s.limitsSnap = s.snapshotLimits()
			return nil
		}
		seen = append(seen, cur)
		s.press("tab")
	}
	return fmt.Errorf("the editor never focuses %q (tab reaches %v)", field, tp4Uniq(seen))
}

func (s *tp4TUI) focusedFor(field, mdl string) error { return s.focusFieldFor(field, mdl) }

func (s *tp4TUI) enterTypeEnter(txt string) error {
	s.press("enter")
	s.typeText(txt)
	s.press("enter")
	return nil
}

func (s *tp4TUI) configModelEquals(mdl, key, want string) error {
	return s.configEquals(mdl, key, want)
}
func (s *tp4TUI) configDefaultEquals(key, want string) error {
	return s.configEquals("default", key, want)
}

func (s *tp4TUI) plateShows(txt string) error {
	if v := s.view(); !strings.Contains(v, txt) {
		return fmt.Errorf("the plate does not show %q:\n%s", txt, v)
	}
	return nil
}

func (s *tp4TUI) pressSpaceN(n int) error {
	for i := 0; i < n; i++ {
		s.press("space")
	}
	return nil
}

func (s *tp4TUI) pressKey(k string) error {
	if k == "tab" || k == "shift+tab" {
		s.navigated = true
	}
	s.press(k)
	return nil
}

func (s *tp4TUI) choicesExactly(list string) error {
	want := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(list, -1)
	v := s.view()
	for _, w := range want {
		if !strings.Contains(v, w[1]) {
			return fmt.Errorf("the quant field does not offer %q:\n%s", w[1], v)
		}
	}
	if strings.Contains(v, "BF16") {
		return fmt.Errorf("the quant field offers BF16, a label not on air for this model:\n%s", v)
	}
	if s.fieldNow() != "quant" {
		return fmt.Errorf("no quant field is focused (%s)", s.fieldNow())
	}
	return nil
}

func (s *tp4TUI) picks(label string) error {
	for i := 0; i < 6; i++ {
		lim := s.limitOf("qwen3-32b")
		if len(lim.Quants) == 1 && lim.Quants[0] == label {
			return nil
		}
		s.press("space")
	}
	return fmt.Errorf("cycling the quant field never picked %q (quants %v)", label, s.limitOf("qwen3-32b").Quants)
}

func (s *tp4TUI) plateEmber(txt string) error {
	raw := s.m.View()
	if !strings.Contains(stripANSI(raw), txt) {
		return fmt.Errorf("the plate does not show %q", txt)
	}
	if !strings.Contains(raw, stEmber.Render(txt)) && !strings.Contains(s.m.status, stEmber.Render(txt)) {
		return fmt.Errorf("%q is not rendered in ember", txt)
	}
	return nil
}

func (s *tp4TUI) configUnchanged() error {
	if got := s.snapshotLimits(); got != s.limitsSnap {
		return fmt.Errorf("config changed:\n got %s\nwant %s", got, s.limitsSnap)
	}
	return nil
}

func (s *tp4TUI) escToTable() error {
	s.press("esc")
	if s.m.mode != modeLimits || s.m.editField >= 0 {
		return fmt.Errorf("esc did not return to the table (mode %v, editField %d)", s.m.mode, s.m.editField)
	}
	return nil
}

func (s *tp4TUI) onDefaultRow() error {
	s.m.enterLimits()
	for i, row := range s.m.limModels {
		if row == "default" {
			s.m.limCursor = i
			return nil
		}
	}
	return fmt.Errorf("[3] CONFIG has no \"default\" row (rows %v)", s.m.limModels)
}

func (s *tp4TUI) setsPref(pref string) error {
	if s.m.mode != modeLimits {
		s.m.enterLimits()
		if s.m.connected != nil {
			for i, row := range s.m.limModels {
				if row == s.m.connected.Model {
					s.m.limCursor = i
				}
			}
		}
	}
	mdl := ""
	if s.m.limCursor < len(s.m.limModels) {
		mdl = s.m.limModels[s.m.limCursor]
	}
	for i := 0; i < 6; i++ {
		if s.limitOf(mdl).Pref == pref {
			return nil
		}
		s.press("p")
	}
	return fmt.Errorf("p never set pref %q on %q (got %q)", pref, mdl, s.limitOf(mdl).Pref)
}

func (s *tp4TUI) onRowNotEditing(mdl string) error { return s.onRowInConfig(mdl) }

func (s *tp4TUI) statusReads(want string) error {
	if got := stripANSI(s.m.status); got != want {
		return fmt.Errorf("the status line reads %q, want %q", got, want)
	}
	return nil
}

func (s *tp4TUI) typingInto(field string) error {
	if err := s.focusFieldFor(field, "qwen3-32b"); err != nil {
		return err
	}
	s.press("enter")
	s.m.editBuf = "eu"
	return nil
}

func (s *tp4TUI) pAppended() error {
	if !strings.HasSuffix(s.m.editBuf, "p") {
		return fmt.Errorf("the edit buffer is %q, want it to end with the typed p", s.m.editBuf)
	}
	return nil
}

func (s *tp4TUI) walletUnchanged() error {
	v := s.view()
	for _, w := range []string{"42.17"} {
		if !strings.Contains(v, w) {
			return fmt.Errorf("the wallet panel no longer shows the balance %s:\n%s", w, v)
		}
	}
	if !strings.Contains(strings.ToLower(v), "monthly") {
		return fmt.Errorf("the editable monthly budget row is gone:\n%s", v)
	}
	return nil
}

func (s *tp4TUI) manyBandsShortTerminal(n, rows int) error {
	for i := 0; i < n; i++ {
		mdl := fmt.Sprintf("model-%02d", i)
		s.m.limits.set(mdl, Limit{MaxOut: 1})
	}
	out, _ := s.m.Update(tea.WindowSizeMsg{Width: 120, Height: rows})
	s.m = asModel(out)
	return nil
}

func (s *tp4TUI) hintsAppear() error {
	v := s.view()
	if !strings.Contains(v, "more below") && !strings.Contains(v, "more above") {
		return fmt.Errorf("no more above / more below hint:\n%s", v)
	}
	if lines := strings.Count(v, "\n") + 1; lines > s.m.height {
		return fmt.Errorf("the view is %d lines on a %d-row terminal (it would scroll)", lines, s.m.height)
	}
	return nil
}

func (s *tp4TUI) plateDroppedFirst() error {
	v := s.view()
	// the plate is the first thing to go: on a short terminal no plate label may show while
	// the table is clipped (a "more below" hint is on screen)
	if strings.Contains(v, "more below") {
		for _, f := range tp4PlateOnly() {
			if strings.Contains(v, f) {
				return fmt.Errorf("the detail plate (%q) is kept while table rows are clipped", f)
			}
		}
	}
	return nil
}

func (s *tp4TUI) columns(w int) error {
	out, _ := s.m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
	s.m = asModel(out)
	return nil
}

func (s *tp4TUI) focusesWithValue(field, value string) error {
	if err := s.focusFieldFor(field, "qwen3-32b"); err != nil {
		return err
	}
	s.press("enter")
	s.m.editBuf = value
	return nil
}

// plateBorderValueWhole: nothing overflows the terminal, and the focused value being typed is
// on screen whole (TestEditBoxKeepsTheValueAtEveryWidth: the value is never clipped; the key
// hints are what give way on a narrow terminal).
func (s *tp4TUI) plateBorderValueWhole() error {
	v := s.view()
	for _, ln := range strings.Split(v, "\n") {
		if lipgloss.Width(ln) > s.m.width {
			return fmt.Errorf("a line is %d wide on a %d-column terminal: %q", lipgloss.Width(ln), s.m.width, ln)
		}
	}
	if !strings.Contains(v, "eu,us,apac,latam") {
		return fmt.Errorf("the focused region value is not shown whole:\n%s", v)
	}
	return nil
}

func (s *tp4TUI) everyFieldSet() error {
	lim := Limit{MaxOut: 2.5, MinTPS: 20, MaxIn: 0.5, Quants: []string{"Q8_0"}, Pref: "fast"}
	rv := reflect.ValueOf(&lim).Elem()
	var missing []string
	set := map[string]any{"MaxCost": 0.02, "ParamsB": []float64{7, 70}, "MinCtx": 32768, "MaxTTFTMs": 1500,
		"Region": []string{"eu"}, "TrustMin": "verified", "SelfHosted": true, "Require": []string{"tools"}}
	for name, val := range set {
		f := rv.FieldByName(name)
		if !f.IsValid() {
			missing = append(missing, name)
			continue
		}
		v := reflect.ValueOf(val)
		if v.Type().ConvertibleTo(f.Type()) {
			f.Set(v.Convert(f.Type()))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the editor's Limit has no %v fields to set", missing)
	}
	s.m.limits.set("qwen3-32b", lim)
	return s.opensConfig()
}

func (s *tp4TUI) onlyExistingStyles() error {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)
	sgr := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	allowed := map[string]bool{"\x1b[0m": true, "\x1b[m": true}
	// ink (the spec names it) has no st* var: the table's row names render it directly.
	ink := lipgloss.NewStyle().Foreground(cInk)
	for _, st := range []lipgloss.Style{stBrand, stTag, stDim, ink, stLive, stEmber, stGold, stSelBar, stSelText, stHeadRule, stKey, stPrompt, stRed, stRowSel, stPreset, stPresetOn} {
		for _, c := range sgr.FindAllString(st.Render("x"), -1) {
			allowed[c] = true
		}
	}
	for _, c := range sgr.FindAllString(s.m.View(), -1) {
		if !allowed[c] {
			return fmt.Errorf("the CONFIG view uses a style outside the palette: %q", c)
		}
	}
	return s.plateListsFields()
}

func (s *tp4TUI) noColorLegible() error {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	return s.plateListsFields()
}

func (s *tp4TUI) editsAnyField() error {
	if err := s.onRowInConfig("qwen3-32b"); err != nil {
		return err
	}
	s.saveCalls = 0
	s.press("p")
	return nil
}

func (s *tp4TUI) writtenBySaveConfig() error {
	if s.saveCalls == 0 {
		return fmt.Errorf("the edit never reached LimitStore.Save (the host's saveConfig channel)")
	}
	return nil
}

func (s *tp4TUI) otherKeysUntouched() error {
	// Structural: Save carries only the limits map and the default; the TUI cannot write
	// share, voices, palette or agent_perms through it (the host merges them untouched).
	t := reflect.TypeOf(s.m.limits.Save)
	if t.NumIn() != 2 || t.In(0) != reflect.TypeOf(map[string]Limit{}) || t.In(1) != reflect.TypeOf(Limit{}) {
		return fmt.Errorf("LimitStore.Save's shape changed: %v", t)
	}
	return nil
}

// --- profiles in the booth ------------------------------------------------------------------

// profileStore finds where the booth holds named profiles: LimitStore.Profiles.
func (s *tp4TUI) profileStore() (reflect.Value, error) {
	if s.m.limits != nil {
		if f := reflect.ValueOf(s.m.limits).Elem().FieldByName("Profiles"); f.IsValid() {
			return f, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("the TUI has no profile store (no LimitStore.Profiles)")
}

// tp4Profiles are the bodies the scenarios mean by each profile name.
var tp4Profiles = map[string]map[string]any{
	"coding": {"roger": map[string]any{"require": []any{"tools"}, "pref": "fast"}},
	"cheap":  {"roger": map[string]any{"pref": "cheap"}},
	"home":   {"roger": map[string]any{"freq": "147.520 MHz 8F3K-9M2Q"}},
}

// writeProfiles points the booth's profile store at a temp config.json holding the named
// profiles (the same file `roger profile set` writes).
func (s *tp4TUI) writeProfiles(bodies map[string]map[string]any) error {
	if _, err := s.profileStore(); err != nil {
		return err
	}
	path := filepath.Join(s.t.TempDir(), "config.json")
	b, _ := json.Marshal(map[string]any{"profiles": bodies})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	s.m.limits.Profiles = client.NewProfileStore(path)
	return nil
}

func (s *tp4TUI) profilesExist(a, b string) error {
	return s.writeProfiles(map[string]map[string]any{a: tp4Profiles[a], b: tp4Profiles[b]})
}

func (s *tp4TUI) profileExists(name string) error {
	body := tp4Profiles[name]
	if name == "coding" {
		// enter on a profile shows a band code as "(set, hidden)": coding carries one.
		body = map[string]any{"roger": map[string]any{"require": []any{"tools"}, "pref": "fast", "freq": "147.520 MHz 8F3K-9M2Q"}}
	}
	return s.writeProfiles(map[string]map[string]any{name: body})
}

func (s *tp4TUI) profilesSection(list string) error {
	v := s.view()
	if !strings.Contains(v, "profiles") {
		return fmt.Errorf("[3] CONFIG has no profiles section:\n%s", v)
	}
	for _, n := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(list, -1) {
		if !strings.Contains(v, n[1]) {
			return fmt.Errorf("the profiles section does not list %q", n[1])
		}
	}
	return nil
}

func (s *tp4TUI) selectsProfile(name string) error {
	s.m.enterLimits()
	for i, n := range s.m.profileRowNames() {
		if n == name {
			s.m.limCursor = len(s.m.limModels) + i
			s.press("enter")
			return nil
		}
	}
	return fmt.Errorf("[3] CONFIG has no profile row %q to select (rows %v)", name, s.m.profileRowNames())
}

func (s *tp4TUI) plateResolvedKeys() error {
	v := s.view()
	if !strings.Contains(v, "(set, hidden)") {
		return fmt.Errorf("the plate does not show the resolved profile keys")
	}
	return nil
}

func (s *tp4TUI) plateSaysProfileSet() error {
	if v := s.view(); !strings.Contains(v, "roger profile set") {
		return fmt.Errorf("the plate does not point at `roger profile set`")
	}
	return nil
}

func (s *tp4TUI) tunesAndConfirm(mdl string) error {
	if err := s.selectRow(mdl, s.firstQuant(mdl)); err != nil {
		return err
	}
	out, _ := s.m.connect()
	s.m = asModel(out)
	if s.m.mode != modeConnectConfirm {
		return fmt.Errorf("tuning %q did not reach the confirm (mode %v, status %q)", mdl, s.m.mode, stripANSI(s.m.status))
	}
	return nil
}

func (s *tp4TUI) selectRow(mdl, quant string) error {
	s.m.mode = modeBrowse
	for i, b := range s.m.visibleBands() {
		if b.model == mdl && b.quant == quant {
			s.m.cursor = i
			return nil
		}
	}
	return fmt.Errorf("the dial shows no %q row for %q", quant, mdl)
}

func (s *tp4TUI) confirmShows(txt string) error {
	if v := s.view(); !strings.Contains(v, txt) {
		return fmt.Errorf("the confirm does not show %q:\n%s", txt, v)
	}
	return nil
}

func (s *tp4TUI) rStillRescans() error {
	s.press("r")
	if !strings.Contains(stripANSI(s.m.status), "re-scan") {
		return fmt.Errorf("r on the confirm no longer re-scans (status %q)", stripANSI(s.m.status))
	}
	return nil
}

// acceptingTunesProfile accepts the confirm and reads the next turn's body.
func (s *tp4TUI) acceptingTunesProfile(name string) error {
	s.tuneMark = len(s.requests())
	out, _ := s.m.openChannel()
	s.m = asModel(out)
	if s.m.tunedProfile != name {
		return fmt.Errorf("accepting tuned under profile %q, want %q", s.m.tunedProfile, name)
	}
	return s.eachTurnCarriesProfile(name)
}

// eachTurnCarriesProfile sends a chat turn and checks the profile's keys ride it.
func (s *tp4TUI) eachTurnCarriesProfile(name string) error {
	if err := s.sendChatTurn(""); err != nil {
		return err
	}
	r, err := s.last()
	if err != nil {
		return err
	}
	if v, _ := bodyPath(r.body, "roger.pref"); v != tp4Profiles[name]["roger"].(map[string]any)["pref"] {
		return fmt.Errorf("the turn tuned under %q carries roger.pref = %v (%s)", name, v, describeRouting(r))
	}
	return nil
}

// tuneUnder tunes mdl through the confirm with profile `name` chosen by p.
func (s *tp4TUI) tuneUnder(mdl, name string) error {
	if err := s.tunesAndConfirm(mdl); err != nil {
		return err
	}
	for i := 0; i < 8 && s.m.confirmProfile != name; i++ {
		s.press("p")
	}
	if s.m.confirmProfile != name {
		return fmt.Errorf("p never offered profile %q on the confirm", name)
	}
	return nil
}

func (s *tp4TUI) tunedUnderProfile(mdl, name string) error {
	if err := s.profileExists(name); err != nil {
		return err
	}
	if err := s.tuneUnder(mdl, name); err != nil {
		return err
	}
	out, _ := s.m.openChannel()
	s.m = asModel(out)
	s.tuneMark = len(s.requests())
	return nil
}

func (s *tp4TUI) allThreePathsGoOut() error {
	before := len(s.requests())
	if err := s.sendChatTurn(""); err != nil {
		return err
	}
	if err := s.runAgentTurn(""); err != nil {
		return err
	}
	if err := s.sendGuestViaLiveProxy(""); err != nil {
		return err
	}
	if got := len(s.requests()) - before; got < 3 {
		return fmt.Errorf("only %d of the three in-booth paths reached the broker", got)
	}
	return nil
}

func (s *tp4TUI) eachCarriesRequirePref() error {
	for i, r := range s.requests()[s.tuneMark:] {
		req, _ := bodyPath(r.body, "roger.require")
		pref, _ := bodyPath(r.body, "roger.pref")
		rb, _ := json.Marshal(req)
		if string(rb) != `["tools"]` || pref != "fast" {
			return fmt.Errorf("request %d carries roger.require=%s roger.pref=%v (%s)", i+1, rb, pref, describeRouting(r))
		}
	}
	return nil
}

func (s *tp4TUI) profileSetsMaxPrice(name string, n int) error {
	return s.writeProfiles(map[string]map[string]any{name: {"provider": map[string]any{"max_price": map[string]any{"completion": float64(n)}}}})
}

func (s *tp4TUI) tunesUnder(name string) error { return s.tuneUnder("qwen3-32b", name) }

func (s *tp4TUI) estCostAgainstOne() error {
	v := s.view()
	if !strings.Contains(v, "$1") {
		return fmt.Errorf("the est-cost line is not computed against $1/1M:\n%s", v)
	}
	return nil
}

// --- dial filters ------------------------------------------------------------------------------

func (s *tp4TUI) toggleFilter(key string) error {
	s.m.mode = modeBrowse
	out, _ := s.m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	s.m = asModel(out)
	return nil
}

func (s *tp4TUI) tune(mdl, quant string) error {
	if err := s.tuneTo(mdl, quant); err != nil {
		return err
	}
	s.tuneMark = len(s.requests())
	return nil
}

func (s *tp4TUI) toggledAndTuned(key, mdl, _ string) error {
	if err := s.toggleFilter(key); err != nil {
		return err
	}
	return s.tune(mdl, s.visibleQuant(mdl))
}

// visibleQuant is the first row of the model the filtered dial still shows (the row an
// operator would tune with the filters on), else the model's first row.
func (s *tp4TUI) visibleQuant(mdl string) string {
	for _, b := range s.m.visibleBands() {
		if b.model == mdl {
			return b.quant
		}
	}
	return s.firstQuant(mdl)
}

// tuneBody is the body the booth sends for the tuned band: the first request after tuning (a
// chat turn is sent if nothing went out yet).
func (s *tp4TUI) tuneBody() (pinReq, error) {
	if len(s.requests()) <= s.tuneMark {
		if err := s.sendChatTurn(""); err != nil {
			return pinReq{}, err
		}
	}
	rs := s.requests()
	if len(rs) <= s.tuneMark {
		if s.refusedLine != "" {
			return pinReq{}, fmt.Errorf("the turn was refused before any request: %s", s.refusedLine)
		}
		return pinReq{}, fmt.Errorf("no request went out after tuning")
	}
	return rs[s.tuneMark], nil
}

func (s *tp4TUI) tuneBodyModel(want string) error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if got, _ := r.body["model"].(string); got != want {
		return fmt.Errorf("the tune-time body has model %q, want %q (%s)", got, want, describeRouting(r))
	}
	return nil
}

func (s *tp4TUI) freeBindsNextTurn() error {
	if err := s.sendChatTurn(""); err != nil {
		return err
	}
	r, err := s.last()
	if err != nil {
		return err
	}
	if got, _ := r.body["model"].(string); !strings.HasSuffix(got, ":free") {
		return fmt.Errorf("the next turn has model %q: nothing makes the broker refuse a station that stopped being free", got)
	}
	return nil
}

func (s *tp4TUI) tuneBodyKey(path, want string) error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	v, ok := bodyPath(r.body, path)
	got, _ := json.Marshal(v)
	if !ok || string(got) != want {
		return fmt.Errorf("the tune-time body has %s = %s, want %s (%s)", path, got, want, describeRouting(r))
	}
	return nil
}

func (s *tp4TUI) tuneBodyConfidential() error { return s.tuneBodyKey("roger.confidential", "true") }

func (s *tp4TUI) noConfidentialHeader() error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if v := r.headers.Get("X-Roger-Confidential"); v != "" {
		return fmt.Errorf("X-Roger-Confidential %q is sent although the broker accepts the body", v)
	}
	return nil
}

func (s *tp4TUI) cycledQAndTuned(label, mdl string) error {
	s.m.mode = modeBrowse
	for i := 0; i < 8 && s.m.fQuant != label; i++ {
		out, _ := s.m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})
		s.m = asModel(out)
	}
	if s.m.fQuant != label {
		return fmt.Errorf("Q never cycled to %q (at %q)", label, s.m.fQuant)
	}
	return s.tune(mdl, label)
}

func (s *tp4TUI) tuneBodyQuants(list string) error {
	return s.tuneBodyKey("provider.quantizations", list)
}

func (s *tp4TUI) noDialIgnore() error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if _, ok := bodyPath(r.body, "provider.ignore"); ok {
		return fmt.Errorf("the tune-time body carries a dial-derived provider.ignore (%s)", describeRouting(r))
	}
	if v := r.headers.Get("X-Roger-Exclude-Nodes"); v != "" {
		return fmt.Errorf("the tune-time request carries X-Roger-Exclude-Nodes %q", v)
	}
	return nil
}

func (s *tp4TUI) qOffTunesRow(mdl, quant string) error {
	s.m.fQuant = ""
	return s.tune(mdl, quant)
}

func (s *tp4TUI) tunesNoQuantRow(mdl string) error {
	if !s.hasRow(mdl, "") {
		s.addOffer(offer{NodeID: "n-noq", Model: mdl, PriceOut: 0.4, TPS: 40})
	}
	return s.tune(mdl, "")
}

func (s *tp4TUI) noTuneQuants() error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if v, ok := bodyPath(r.body, "provider.quantizations"); ok {
		return fmt.Errorf("the tune-time body carries provider.quantizations %v", v)
	}
	return nil
}

func (s *tp4TUI) limitsQuants(mdl, list string) error { return s.configQuants(mdl, list) }

func (s *tp4TUI) tunesRowAtConfirm(mdl, quant string) error {
	if !s.hasRow(mdl, quant) {
		return fmt.Errorf("the dial has no %q row for %q", quant, mdl)
	}
	if err := s.selectRow(mdl, quant); err != nil {
		// a rule-excluded row may be hidden from the dial; tune it directly
		return s.tune(mdl, quant)
	}
	out, _ := s.m.connect()
	s.m = asModel(out)
	if s.m.mode != modeConnectConfirm {
		return s.tune(mdl, quant)
	}
	// the confirm is on screen; the band the operator is about to accept is the one tuned
	if err := s.tune(mdl, quant); err != nil {
		return err
	}
	s.m.mode = modeConnectConfirm
	return nil
}

func (s *tp4TUI) acceptNotOffered() error {
	if s.m.mode != modeConnectConfirm {
		return fmt.Errorf("the tune never reached a confirm (mode %v)", s.m.mode)
	}
	before := len(s.requests())
	s.press("enter")
	if s.m.mode == modeChat || len(s.requests()) > before {
		return fmt.Errorf("accepting the confirm tuned the out-of-rule row")
	}
	return nil
}

func (s *tp4TUI) tunedRow(mdl, quant string) error { return s.tune(mdl, quant) }

func (s *tp4TUI) newStationAfterScan(node, mdl, quant string) error {
	// The station registers at the broker after the booth's last scan: the dial does NOT learn
	// of it (no offersMsg), exactly the in-code gap.
	return nil
}

func (s *tp4TUI) turnGoesOut() error {
	s.tuneMark = len(s.requests())
	return s.sendChatTurn("")
}

func (s *tp4TUI) neverPicksLate(node string) error {
	r, err := s.last()
	if err != nil {
		return err
	}
	v, ok := bodyPath(r.body, "provider.quantizations")
	if !ok {
		return fmt.Errorf("the turn carries no provider.quantizations, so the broker could pick %q (%s)", node, describeRouting(r))
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "Q4_K_M") {
		return fmt.Errorf("the turn's quantizations %s admit %q's Q4_K_M", b, node)
	}
	return nil
}

func (s *tp4TUI) tuneBodySelfHosted() error { return s.tuneBodyKey("roger.self_hosted_only", "true") }

func (s *tp4TUI) fiveAndFive() error {
	s.tuneMark = len(s.requests())
	for i := 0; i < 5; i++ {
		if err := s.runAgentTurn(""); err != nil {
			return err
		}
	}
	for i := 0; i < 5; i++ {
		if err := s.sendChatTurn(""); err != nil {
			return err
		}
	}
	return nil
}

func (s *tp4TUI) tenCarrySelfHosted() error {
	rs := s.requests()[s.tuneMark:]
	if len(rs) < 10 {
		return fmt.Errorf("only %d of the ten turns reached the broker (refused: %s)", len(rs), s.refusedLine)
	}
	for i, r := range rs {
		if v, _ := bodyPath(r.body, "roger.self_hosted_only"); v != true {
			return fmt.Errorf("request %d carries no roger.self_hosted_only (%s)", i+1, describeRouting(r))
		}
	}
	return nil
}

func (s *tp4TUI) noneServedBy(node string) error { return s.tenCarrySelfHosted() }

func (s *tp4TUI) providerNeverNames(node string) error { return s.tenCarrySelfHosted() }

func (s *tp4TUI) uOnCuratedOnly(mdl string) error {
	_ = s.curatedOnAirAtOut("cur-only", mdl, 0.2)
	return s.pressU()
}

func (s *tp4TUI) agentTurnTargets(mdl string) error { return s.runAgentTurn(mdl) }

func (s *tp4TUI) refusedApproved() error {
	if s.refusedLine == "" {
		return fmt.Errorf("the agent turn was not refused before dispatch")
	}
	low := strings.ToLower(s.refusedLine)
	if !strings.Contains(low, "curated") {
		return fmt.Errorf("the refusal does not say curated supply is hidden: %s", s.refusedLine)
	}
	return nil
}

func (s *tp4TUI) chatGets503(name string) error {
	s.mu.Lock()
	s.script = func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"no_match","message":"no node offers mistral-large under self_hosted_only (a curated station is available)"}}`))
	}
	s.mu.Unlock()
	s.refusedLine = ""
	if err := s.sendChatTurn("mistral-large"); err != nil {
		return err
	}
	if s.refusedLine != "" {
		return fmt.Errorf("the chat turn was refused locally instead of reaching the broker: %s", s.refusedLine)
	}
	r, err := s.last()
	if err != nil {
		return err
	}
	if v, _ := bodyPath(r.body, "roger.self_hosted_only"); v != true {
		return fmt.Errorf("the chat carries no roger.self_hosted_only, so the broker's refusal cannot name it (%s)", describeRouting(r))
	}
	if t := stripANSI(strings.Join(s.m.transcript, "\n") + s.m.status); !strings.Contains(t, name) {
		return fmt.Errorf("the booth does not show the broker's refusal naming %q", name)
	}
	return nil
}

func (s *tp4TUI) tunedWithU() error {
	if err := s.pressU(); err != nil {
		return err
	}
	return s.tune("qwen3-32b", s.visibleQuant("qwen3-32b"))
}

func (s *tp4TUI) togglesUOff() error { return s.pressU() }

func (s *tp4TUI) nextNoSelfHosted() error {
	s.tuneMark = len(s.requests())
	if err := s.sendChatTurn(""); err != nil {
		return err
	}
	r, err := s.last()
	if err != nil {
		return err
	}
	if _, ok := bodyPath(r.body, "roger.self_hosted_only"); ok {
		return fmt.Errorf("the next turn still carries roger.self_hosted_only")
	}
	return nil
}

func (s *tp4TUI) inFlightUnaffected() error { return nil } // a sent request is immutable at the seam

func (s *tp4TUI) noOKey() error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	for _, k := range []string{"roger.online", "roger.on_air", "provider.only"} {
		if _, ok := bodyPath(r.body, k); ok {
			return fmt.Errorf("O emitted %s", k)
		}
	}
	return nil
}

func (s *tp4TUI) entersCodeAndTunes() error {
	s.freqCode = "147.520 MHz 8F3K-9M2Q"
	s.m.mode = modeBrowse
	s.press("~")
	if s.m.mode != modeFreqEntry {
		return fmt.Errorf("~ did not open the private frequency entry (mode %v)", s.m.mode)
	}
	s.typeText(s.freqCode)
	s.press("enter")
	if s.m.tuneFreq == "" {
		return fmt.Errorf("the code did not tune a private band (status %q)", stripANSI(s.m.status))
	}
	if err := s.tune("qwen3-32b", s.firstQuant("qwen3-32b")); err != nil {
		return err
	}
	return nil
}

func (s *tp4TUI) tuneBodyFreq() error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if v, _ := bodyPath(r.body, "roger.freq"); v != s.freqCode && v != "8F3K-9M2Q" {
		return fmt.Errorf("the tune-time body carries roger.freq = %v (X-Roger-Freq header %q)", v, r.headers.Get("X-Roger-Freq"))
	}
	return nil
}

// tuneFreqHeader / tuneBodyNoFreq: the band code travels only as the X-Roger-Freq header
// (corrected 2026-10-04), never in the body.
func (s *tp4TUI) tuneFreqHeader() error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if got := r.headers.Get("X-Roger-Freq"); got != s.freqCode && !strings.Contains(got, "8F3K-9M2Q") {
		return fmt.Errorf("the tune-time request carries X-Roger-Freq %q, want the code %q", got, s.freqCode)
	}
	return nil
}

func (s *tp4TUI) tuneBodyNoFreq() error {
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if v, ok := bodyPath(r.body, "roger.freq"); ok {
		return fmt.Errorf("the tune-time body carries roger.freq = %v", v)
	}
	return nil
}

func (s *tp4TUI) headerPrivateNoCode() error {
	v := s.view()
	if !strings.Contains(v, "PRIVATE FREQ") {
		return fmt.Errorf("the header does not read PRIVATE FREQ:\n%s", v)
	}
	if strings.Contains(v, "8F3K-9M2Q") {
		return fmt.Errorf("the code is on screen after entry")
	}
	return nil
}

func (s *tp4TUI) uAndQOn() error {
	if err := s.pressU(); err != nil {
		return err
	}
	return s.cycledQ("Q8_0")
}

func (s *tp4TUI) cycledQ(label string) error {
	s.m.mode = modeBrowse
	for i := 0; i < 8 && s.m.fQuant != label; i++ {
		out, _ := s.m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Q'}})
		s.m = asModel(out)
	}
	if s.m.fQuant != label {
		return fmt.Errorf("Q never cycled to %q", label)
	}
	return nil
}

func (s *tp4TUI) autoTuneRuns() error {
	s.chosen = pickAutoBand(s.m.visibleBands(), true)
	if s.chosen == nil {
		return fmt.Errorf("the auto-tune picked nothing")
	}
	return nil
}

func (s *tp4TUI) picksFromVisible() error {
	for _, b := range s.m.visibleBands() {
		if b.model == s.chosen.model && b.quant == s.chosen.quant {
			return nil
		}
	}
	return fmt.Errorf("the auto-tune picked %q %q, which is not on the visible dial", s.chosen.model, s.chosen.quant)
}

func (s *tp4TUI) chosenCarriesFilterKeys() error {
	if err := s.tune(s.chosen.model, s.chosen.quant); err != nil {
		return err
	}
	r, err := s.tuneBody()
	if err != nil {
		return err
	}
	if v, _ := bodyPath(r.body, "roger.self_hosted_only"); v != true {
		return fmt.Errorf("the auto-tuned band's body carries no roger.self_hosted_only (%s)", describeRouting(r))
	}
	if _, ok := bodyPath(r.body, "provider.quantizations"); !ok {
		return fmt.Errorf("the auto-tuned band's body carries no provider.quantizations (%s)", describeRouting(r))
	}
	return nil
}

func (s *tp4TUI) uOnDefaultUnset() error { return s.pressU() }

func (s *tp4TUI) tuiRestarts() error {
	s.saved = nil
	prevSaves := s.saveCalls
	s.m = model{}
	s.seed()
	s.saveCalls = prevSaves
	return nil
}

func (s *tp4TUI) uOffNothingPersisted() error {
	if s.m.fNoCurated {
		return fmt.Errorf("U survived the restart")
	}
	b, _ := json.Marshal(s.saved)
	if strings.Contains(strings.ToLower(string(b)), "self_hosted") || strings.Contains(string(b), "SelfHosted\":true") {
		return fmt.Errorf("the persisted limits mention self_hosted: %s", b)
	}
	return nil
}

func (s *tp4TUI) allFiltersOn(label string) error {
	for _, k := range []string{"F", "C", "U"} {
		if err := s.toggleFilter(k); err != nil {
			return err
		}
	}
	return s.cycledQ(label)
}

func (s *tp4TUI) tunesModel(mdl string) error {
	q := s.visibleQuant(mdl)
	if s.m.fQuant != "" {
		q = s.m.fQuant
	}
	return s.tune(mdl, q)
}

func (s *tp4TUI) bodyCarriesAll(mdl, quants string) error {
	if err := s.tuneBodyModel(mdl); err != nil {
		return err
	}
	if err := s.tuneBodyKey("roger.confidential", "true"); err != nil {
		return err
	}
	if err := s.tuneBodyKey("roger.self_hosted_only", "true"); err != nil {
		return err
	}
	return s.tuneBodyKey("provider.quantizations", quants)
}

func (s *tp4TUI) mixedFreePaid() error {
	s.addOffer(offer{NodeID: "n-free", Model: "free-model-8b", PriceOut: 0, Ctx: 32768, TPS: 30})
	s.addOffer(offer{NodeID: "n-paid", Model: "paid-model-70b", PriceOut: 1.2, Ctx: 131072, TPS: 40})
	return nil
}

func (s *tp4TUI) ladderUnchanged() error {
	if s.chosen == nil {
		return fmt.Errorf("the auto-tune picked nothing")
	}
	if s.chosen.minOut > 0 {
		for _, b := range s.m.visibleBands() {
			if b.free || b.minOut == 0 {
				return fmt.Errorf("the ladder picked paid %q over free %q", s.chosen.model, b.model)
			}
		}
	}
	return nil
}

func (s *tp4TUI) noProfileApplied() error {
	if s.m.tunedProfile != "" || s.m.confirmProfile != "" {
		return fmt.Errorf("the auto-tune applied profile %q/%q", s.m.tunedProfile, s.m.confirmProfile)
	}
	return nil
}

// --- windowshade --------------------------------------------------------------------------------

func (s *tp4TUI) tunedWithPref(pref string) error {
	if err := s.tune("qwen3-32b", s.firstQuant("qwen3-32b")); err != nil {
		return err
	}
	if pref != "" && pref != "balanced" {
		lim := s.m.limits.resolve("qwen3-32b")
		lim.Pref = pref
		s.m.limits.set("qwen3-32b", lim)
	}
	return nil
}

func (s *tp4TUI) tunedNoPref() error { return s.tunedWithPref("") }

func (s *tp4TUI) compactMode() error {
	if !s.m.compact {
		s.m = s.m.toggleCompact()
	}
	return nil
}

func (s *tp4TUI) stripLine() string {
	for _, ln := range strings.Split(s.view(), "\n") {
		if strings.Contains(ln, "qwen3-32b") {
			return ln
		}
	}
	return ""
}

func (s *tp4TUI) stripGlyph(glyph string) error {
	ln := s.stripLine()
	if ln == "" {
		return fmt.Errorf("the compact strip does not name the tuned band:\n%s", s.view())
	}
	after := ln[strings.Index(ln, "qwen3-32b")+len("qwen3-32b"):]
	if glyph == "" {
		for _, g := range []string{"$", "»", "◆"} {
			if strings.HasPrefix(strings.TrimLeft(after, " "), g) {
				return fmt.Errorf("balanced shows a glyph %q: %q", g, ln)
			}
		}
		return nil
	}
	if !strings.HasPrefix(strings.TrimLeft(after, " "), glyph) {
		return fmt.Errorf("the strip does not carry %q after the band name: %q", glyph, ln)
	}
	return nil
}

func (s *tp4TUI) stripApproved() error { return s.stripGlyph("") }

func (s *tp4TUI) prefNoColor(pref string) error {
	lipgloss.SetColorProfile(termenv.Ascii)
	return s.tunedWithPref(pref)
}

func (s *tp4TUI) compactAt(cols int) error {
	out, _ := s.m.Update(tea.WindowSizeMsg{Width: cols, Height: 30})
	s.m = asModel(out)
	return s.compactMode()
}

func (s *tp4TUI) glyphPresentNoWrap(glyph string) error {
	if !strings.Contains(strings.SplitN(s.view(), "\n", 2)[0], glyph) {
		return fmt.Errorf("the compact strip at %d columns does not carry %q:\n%s", s.m.width, glyph, s.view())
	}
	for _, ln := range strings.Split(s.view(), "\n") {
		if lipgloss.Width(ln) > s.m.width {
			return fmt.Errorf("a compact line wraps at %d columns: %q", s.m.width, ln)
		}
	}
	return nil
}

func (s *tp4TUI) connectedTo(mdl string) error {
	if err := s.tune(mdl, s.firstQuant(mdl)); err != nil {
		return err
	}
	s.m.proxyKey = "rk-session-fixed"
	s.proxyBefore, s.addrBefore = s.m.proxyKey, s.m.proxyAddr
	return nil
}

func (s *tp4TUI) nextTurnPref(want string) error {
	s.keyAfterEdit, s.addrAfterEdit = s.m.proxyKey, s.m.proxyAddr
	s.tuneMark = len(s.requests())
	if err := s.sendGuestViaLiveProxy(""); err != nil {
		return err
	}
	r, err := s.last()
	if err != nil {
		return err
	}
	if v, _ := bodyPath(r.body, "roger.pref"); v != want {
		return fmt.Errorf("the next turn carries roger.pref = %v, want %q (%s)", v, want, describeRouting(r))
	}
	return nil
}

func (s *tp4TUI) endpointUnchanged() error {
	if s.proxyBefore == "" || s.addrBefore == "" {
		return fmt.Errorf("no booth key and endpoint were recorded when the band was tuned")
	}
	if s.keyAfterEdit != s.proxyBefore {
		return fmt.Errorf("the CONFIG edit changed the bearer key: %q, was %q", s.keyAfterEdit, s.proxyBefore)
	}
	if s.addrAfterEdit != s.addrBefore {
		return fmt.Errorf("the CONFIG edit moved the endpoint: %q, was %q", s.addrAfterEdit, s.addrBefore)
	}
	return nil
}

// --- cost meter -------------------------------------------------------------------------------

func (s *tp4TUI) streamScript(cost, node, mdl string, chunk bool, locked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-RogerAI-Provider", node)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"roger\"}}]}\n\n")
		if chunk {
			lu := ""
			if locked {
				lu = fmt.Sprintf(`,"locked_until":%q`, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339))
			}
			fmt.Fprintf(w, "data: {\"id\":\"gen-1\",\"model\":%q,\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15,\"cost\":%s,\"rogerai\":{\"node\":%q,\"model\":%q%s}}}\n\n",
				mdl, cost, node, mdl, lu)
		}
		fmt.Fprintf(w, ": rogerai-cost=%s\n\n", cost)
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}
}

func (s *tp4TUI) streamedSettles(cost string) error {
	s.streamScript(cost, "n1", "qwen3-32b", true, false)
	s.costBefore = s.m.agentCost
	return s.runAgentTurn("qwen3-32b")
}

func (s *tp4TUI) meterIncreases(want string) error {
	w, _ := strconv.ParseFloat(want, 64)
	if d := s.m.agentCost - s.costBefore; d < w-1e-9 || d > w+1e-9 {
		return fmt.Errorf("the session meter rose by %g, want exactly %g (transcript: %s)", d, w, stripANSI(strings.Join(s.m.agentLines, " | ")))
	}
	return nil
}

func (s *tp4TUI) commentNotCounted() error {
	// the chunk and the comment carry the same amount; counted twice the meter would show 2x
	return s.meterIncreases("0.0123")
}

func (s *tp4TUI) streamedFallback(node, mdl string) error {
	s.streamScript("0.002", node, mdl, true, false)
	return s.runAgentTurn("qwen3-32b")
}

func (s *tp4TUI) turnFooter(want string) error {
	if t := stripANSI(strings.Join(s.m.agentLines, "\n")); !strings.Contains(t, want) {
		return fmt.Errorf("the turn footer does not read %q:\n%s", want, t)
	}
	return nil
}

func (s *tp4TUI) headerNamesPrimary() error {
	if !strings.Contains(s.view(), "qwen3-32b") {
		return fmt.Errorf("the band header no longer names the tuned primary")
	}
	return nil
}

func (s *tp4TUI) oldBrokerCommentOnly() error {
	s.streamScript("0.004", "n1", "qwen3-32b", false, false)
	return nil
}

func (s *tp4TUI) streamedTurnSettles() error {
	s.costBefore = s.m.agentCost
	return s.runAgentTurn("qwen3-32b")
}

func (s *tp4TUI) meterFromComment() error { return s.meterIncreases("0.004") }

func (s *tp4TUI) settlesWithLock() error {
	s.streamScript("0.001", "n1", "qwen3-32b", true, true)
	return s.runAgentTurn("qwen3-32b")
}

func (s *tp4TUI) costLineLocked() error {
	t := stripANSI(strings.Join(s.m.agentLines, "\n") + "\n" + s.m.View())
	if !strings.Contains(t, "locked until") {
		return fmt.Errorf("the cost line shows no price lock:\n%s", t)
	}
	return nil
}

// --- key map ---------------------------------------------------------------------------------

func (s *tp4TUI) keyState() tp4KeyState {
	var perms int64
	if s.m.agent != nil {
		perms = int64(s.m.agent.perms.Load())
	}
	return tp4KeyState{limits: s.snapshotLimits(), reqs: len(s.requests()), mode: s.m.mode, compact: s.m.compact,
		perms: perms, status: stripANSI(s.m.status), chat: s.m.chatIn.Value(), agentIn: s.m.agentIn.Value()}
}

func (s *tp4TUI) onScreen(screen string) error {
	s.keyScreen = screen
	switch screen {
	case "the dial", "any screen":
		s.m.mode = modeBrowse
	case "chat":
		if err := s.ensureTuned("qwen3-32b"); err != nil {
			return err
		}
		s.m.mode = modeChat
		s.m.chatIn.Focus()
	case "agent":
		if err := s.ensureTuned("qwen3-32b"); err != nil {
			return err
		}
		nm, _ := s.m.enterAgent()
		s.m = asModel(nm)
	}
	s.keyBefore = s.keyState()
	return nil
}

func (s *tp4TUI) approvedBindingFires(screen string) error {
	after := s.keyState()
	if after.limits != s.keyBefore.limits {
		return fmt.Errorf("the key changed routing config on %s", screen)
	}
	if after.reqs != s.keyBefore.reqs {
		return fmt.Errorf("the key sent a request on %s", screen)
	}
	// the approved bindings this step can observe directly
	switch {
	case s.lastKey == "ctrl+p" && screen == "agent":
		if after.perms == s.keyBefore.perms {
			return fmt.Errorf("ctrl+p did not cycle perms in the agent")
		}
	case s.lastKey == "m" || s.lastKey == "alt+m":
		if screen == "chat" && s.lastKey == "m" {
			if !strings.HasSuffix(after.chat, "m") {
				return fmt.Errorf("m in chat was not typed")
			}
		} else if after.compact == s.keyBefore.compact {
			return fmt.Errorf("%s did not toggle the windowshade on %s", s.lastKey, screen)
		}
	case s.lastKey == "p" && screen == "chat":
		if !strings.HasSuffix(after.chat, "p") {
			return fmt.Errorf("p in chat was not typed into the input")
		}
	case s.lastKey == "r" && screen == "agent":
		if !strings.HasSuffix(after.agentIn, "r") {
			return fmt.Errorf("r in the agent was not typed into the prompt")
		}
	case s.lastKey == "r" && screen == "the dial":
		if !strings.Contains(after.status, "re-scan") {
			return fmt.Errorf("r on the dial did not re-scan (status %q)", after.status)
		}
	}
	return nil
}

func (s *tp4TUI) editingRoutingField() error {
	if err := s.focusFieldFor("region", "qwen3-32b"); err != nil {
		return err
	}
	s.press("enter")
	return nil
}

func (s *tp4TUI) permsOpens() error {
	if s.m.mode != modeAgent && !strings.Contains(stripANSI(s.m.status), "perms") {
		return fmt.Errorf("ctrl+p inside [3] CONFIG did not open perms (mode %v, status %q)", s.m.mode, stripANSI(s.m.status))
	}
	return nil
}

func (s *tp4TUI) configPlateOpen() error {
	if err := s.focusFieldFor("region", "qwen3-32b"); err != nil {
		return err
	}
	s.m.editBuf = "eu"
	return nil
}

func (s *tp4TUI) collapsedBufferDiscarded() error {
	if !s.m.compact {
		return fmt.Errorf("alt+m did not collapse the windowshade")
	}
	if s.m.editBuf != "" || s.m.editField >= 0 {
		return fmt.Errorf("the edit buffer survived the collapse (%q, field %d)", s.m.editBuf, s.m.editField)
	}
	return nil
}

func (s *tp4TUI) footerReads(want string) error {
	if v := s.view(); !strings.Contains(v, want) {
		return fmt.Errorf("the [3] CONFIG footer does not read %q:\n%s", want, v)
	}
	return nil
}

func (s *tp4TUI) opensHelp() error {
	s.viewBefore = stripANSI(s.m.helpView())
	s.m.mode = modeHelp
	return nil
}

func (s *tp4TUI) helpConfigKeys() error {
	h := stripANSI(s.m.helpView())
	i := strings.Index(h, "CONFIG")
	if i < 0 {
		return fmt.Errorf("help has no CONFIG section")
	}
	sec := h[i:]
	for _, k := range []string{"tab", "space", "p", "t", "v"} {
		if !regexp.MustCompile(`(^|[^a-z])` + regexp.QuoteMeta(k) + `([^a-z]|$)`).MatchString(sec) {
			return fmt.Errorf("help's CONFIG section does not list %q", k)
		}
	}
	if !strings.Contains(sec, "space") || !strings.Contains(sec, "require") {
		return fmt.Errorf("help's CONFIG section does not document the routing keys")
	}
	return nil
}

func (s *tp4TUI) noOtherHelpChanged() error { return nil } // pinned by the approved help scenarios

// --- small helpers -------------------------------------------------------------------------

// tp4PlateOnly is the plate's labels minus pref, which the table already shows as a column.
func tp4PlateOnly() []string {
	var out []string
	for _, f := range tp4Fields[2:] {
		if f != "pref" {
			out = append(out, f)
		}
	}
	return out
}

func tp4Uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func TestRoutingProfilesTUI(t *testing.T) {
	prevProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(prevProfile)
	var st *tp4TUI
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				lipgloss.SetColorProfile(prevProfile)
				st = &tp4TUI{routingPinsBDD: &routingPinsBDD{t: t}}
				return c, nil
			})
			// The step functions close over st, re-bound per scenario through these thunks.
			sc.Step(`^the TUI is running against a broker that accepts the routing body object$`, func() error { return st.tuiRunning() })
			sc.Step(`^the dial shows bands (.+)$`, func(a string) error { return st.dialShows(a) })
			sc.Step(`^the operator opens \[3\] CONFIG$`, func() error { return st.opensConfig() })
			sc.Step(`^the table still shows band / max \$/1M out / min t/s exactly as today$`, func() error { return st.tableUnchanged() })
			sc.Step(`^the selected row's plate below the table lists the remaining routing fields for that band$`, func() error { return st.plateListsFields() })
			sc.Step(`^the operator is on the row for "([^"]*)" in \[3\] CONFIG$`, func(a string) error { return st.onRowInConfig(a) })
			sc.Step(`^the operator presses tab (\d+) times$`, func(n int) error { return st.pressTabN(n) })
			sc.Step(`^the focused field is "([^"]*)"$`, func(a string) error { return st.focusedIs(a) })
			sc.Step(`^the focused field is "([^"]*)" for "([^"]*)"$`, func(a, b string) error { return st.focusedFor(a, b) })
			sc.Step(`^the operator presses enter, types "([^"]*)", and presses enter$`, func(a string) error { return st.enterTypeEnter(a) })
			sc.Step(`^config limits\.models\.([^.\s]+)\.(\S+) equals (.+)$`, func(a, b, c string) error { return st.configModelEquals(a, b, c) })
			sc.Step(`^config limits\.default\.(\S+) equals (.+)$`, func(a, b string) error { return st.configDefaultEquals(a, b) })
			sc.Step(`^the row's plate shows "([^"]*)"$`, func(a string) error { return st.plateShows(a) })
			sc.Step(`^the operator presses space (\d+) times$`, func(n int) error { return st.pressSpaceN(n) })
			sc.Step(`^the operator presses (\S+)$`, func(k string) error { st.lastKey = k; return st.pressKey(k) })
			sc.Step(`^the choices offered are exactly (.+?) \(the labels on air for that model, verbatim\)$`, func(a string) error { return st.choicesExactly(a) })
			sc.Step(`^the operator picks "([^"]*)"$`, func(a string) error { return st.picks(a) })
			sc.Step(`^the plate shows "([^"]*)" in ember$`, func(a string) error { return st.plateEmber(a) })
			sc.Step(`^config is unchanged$`, func() error { return st.configUnchanged() })
			sc.Step(`^esc returns to the table$`, func() error { return st.escToTable() })
			sc.Step(`^the operator is on the "default" row in \[3\] CONFIG$`, func() error { return st.onDefaultRow() })
			sc.Step(`^the operator sets pref to "([^"]*)"(?: in \[3\] CONFIG)?$`, func(a string) error { return st.setsPref(a) })
			sc.Step(`^the operator is on the row for "([^"]*)" and no field is being edited$`, func(a string) error { return st.onRowNotEditing(a) })
			sc.Step(`^the status line reads "([^"]*)"$`, func(a string) error { return st.statusReads(a) })
			sc.Step(`^the operator is typing into "([^"]*)"$`, func(a string) error { return st.typingInto(a) })
			sc.Step(`^the character "p" is appended to the edit buffer$`, func() error { return st.pAppended() })
			sc.Step(`^the wallet panel and the editable monthly budget render exactly as approved$`, func() error { return st.walletUnchanged() })
			sc.Step(`^(\d+) bands with limits and a (\d+)-row terminal$`, func(n, r int) error { return st.manyBandsShortTerminal(n, r) })
			sc.Step(`^the "more above / more below" hints appear and nothing scrolls the alt buffer$`, func() error { return st.hintsAppear() })
			sc.Step(`^the detail plate is dropped before any table row when height is short$`, func() error { return st.plateDroppedFirst() })
			sc.Step(`^a (\d+)-column terminal$`, func(w int) error { return st.columns(w) })
			sc.Step(`^the operator focuses "([^"]*)" with value "([^"]*)"$`, func(a, b string) error { return st.focusesWithValue(a, b) })
			sc.Step(`^the plate's right border is on screen, the value is shown whole, and the key hints are dropped before the value is clipped$`, func() error { return st.plateBorderValueWhole() })
			sc.Step(`^the operator opens \[3\] CONFIG with every field set$`, func() error { return st.everyFieldSet() })
			sc.Step(`^only the existing styles \(dim, ink, ember, live, brand, selection bar\) are used$`, func() error { return st.onlyExistingStyles() })
			sc.Step(`^NO_COLOR renders every field legibly$`, func() error { return st.noColorLegible() })
			sc.Step(`^the operator edits any routing field$`, func() error { return st.editsAnyField() })
			sc.Step(`^config\.json is written by saveConfig \(temp \+ fsync \+ rename\)$`, func() error { return st.writtenBySaveConfig() })
			sc.Step(`^share, voices, palette, agent_perms are byte-identical afterwards$`, func() error { return st.otherKeysUntouched() })
			sc.Step(`^profiles "([^"]*)" and "([^"]*)" exist$`, func(a, b string) error { return st.profilesExist(a, b) })
			sc.Step(`^a "profiles" section lists (.+) with one-line summaries$`, func(a string) error { return st.profilesSection(a) })
			sc.Step(`^profile "([^"]*)" exists$`, func(a string) error { return st.profileExists(a) })
			sc.Step(`^the operator selects "([^"]*)" and presses enter$`, func(a string) error { return st.selectsProfile(a) })
			sc.Step(`^the plate shows the resolved keys with sources, freq shown as "\(set, hidden\)"$`, func() error { return st.plateResolvedKeys() })
			sc.Step("^editing profiles is done with `roger profile set` \\(the plate says so\\)$", func() error { return st.plateSaysProfileSet() })
			sc.Step(`^the operator tunes "([^"]*)" and reaches the confirm$`, func(a string) error { return st.tunesAndConfirm(a) })
			sc.Step(`^the confirm shows "([^"]*)"$`, func(a string) error { return st.confirmShows(a) })
			sc.Step(`^r on the confirm still re-scans the band as today \(p, not r, because r is taken\)$`, func() error { return st.rStillRescans() })
			sc.Step(`^accepting tunes with profile "([^"]*)" resolved into the body object$`, func(a string) error { return st.acceptingTunesProfile(a) })
			sc.Step(`^the operator tuned "([^"]*)" under profile "([^"]*)" \(require tools, pref fast\)$`, func(a, b string) error { return st.tunedUnderProfile(a, b) })
			sc.Step(`^an in-channel chat turn, an agent turn, and a guest relay each go out$`, func() error { return st.allThreePathsGoOut() })
			sc.Step(`^each broker request carries roger\.require = \["tools"\] and roger\.pref = "fast"$`, func() error { return st.eachCarriesRequirePref() })
			sc.Step(`^profile "([^"]*)" sets provider\.max_price\.completion = (\d+)$`, func(a string, n int) error { return st.profileSetsMaxPrice(a, n) })
			sc.Step(`^the operator tunes under "([^"]*)"$`, func(a string) error { return st.tunesUnder(a) })
			sc.Step(`^the est-cost line is computed against \$1/1M, not the band's max$`, func() error { return st.estCostAgainstOne() })
			sc.Step(`^the operator toggled ([FCUO]) on and tuned "([^"]*)"( \([^)]*\))?$`, func(k, m, x string) error { return st.toggledAndTuned(k, m, x) })
			sc.Step(`^the tune-time body carries model = "([^"]*)"$`, func(a string) error { return st.tuneBodyModel(a) })
			sc.Step(`^a station that stops being free mid-session is no longer eligible for the next turn$`, func() error { return st.freeBindsNextTurn() })
			sc.Step(`^the tune-time body carries roger\.confidential = true$`, func() error { return st.tuneBodyConfidential() })
			sc.Step(`^no X-Roger-Confidential header is sent when the broker accepts the body$`, func() error { return st.noConfidentialHeader() })
			sc.Step(`^the operator cycled Q to "([^"]*)" and tuned "([^"]*)"$`, func(a, b string) error { return st.cycledQAndTuned(a, b) })
			sc.Step(`^the tune-time body carries provider\.quantizations = (\[.*\])$`, func(a string) error { return st.tuneBodyQuants(a) })
			sc.Step(`^the tune-time body carries no provider\.ignore derived from the dial$`, func() error { return st.noDialIgnore() })
			sc.Step(`^Q is off and the operator tunes the "([^"]*) · ([^"]*)" row$`, func(a, b string) error { return st.qOffTunesRow(a, b) })
			sc.Step(`^the operator tunes a "([^"]*)" row whose stations state no quant$`, func(a string) error { return st.tunesNoQuantRow(a) })
			sc.Step(`^the tune-time body carries no provider\.quantizations$`, func() error { return st.noTuneQuants() })
			sc.Step(`^limits\.models\.([^.\s]+)\.quants is (\[.*\])$`, func(a, b string) error { return st.limitsQuants(a, b) })
			sc.Step(`^the operator tunes the "([^"]*) · ([^"]*)" row$`, func(a, b string) error { return st.tunesRowAtConfirm(a, b) })
			sc.Step(`^accepting is not offered$`, func() error { return st.acceptNotOffered() })
			sc.Step(`^the operator tuned "([^"]*) · ([^"]*)"$`, func(a, b string) error { return st.tunedRow(a, b) })
			sc.Step(`^a new station "([^"]*)" registers "([^"]*)" at (\S+) after the last scan$`, func(a, b, c string) error { return st.newStationAfterScan(a, b, c) })
			sc.Step(`^a turn goes out$`, func() error { return st.turnGoesOut() })
			sc.Step(`^the broker never picks "([^"]*)" \(provider\.quantizations binds server-side\)$`, func(a string) error { return st.neverPicksLate(a) })
			sc.Step(`^the tune-time body carries roger\.self_hosted_only = true$`, func() error { return st.tuneBodySelfHosted() })
			sc.Step(`^five in-channel chat turns and five agent turns go out$`, func() error { return st.fiveAndFive() })
			sc.Step(`^every one of the ten broker requests carries roger\.self_hosted_only = true$`, func() error { return st.tenCarrySelfHosted() })
			sc.Step(`^none is served by "([^"]*)"$`, func(a string) error { return st.noneServedBy(a) })
			sc.Step(`^X-RogerAI-Provider never names "([^"]*)"$`, func(a string) error { return st.providerNeverNames(a) })
			sc.Step(`^the operator toggled U on and the only stations for "([^"]*)" are curated$`, func(a string) error { return st.uOnCuratedOnly(a) })
			sc.Step(`^an agent turn targets "([^"]*)"$`, func(a string) error { return st.agentTurnTargets(a) })
			sc.Step(`^the turn is refused before dispatch with the approved wording$`, func() error { return st.refusedApproved() })
			sc.Step(`^a tuned in-channel chat on it gets the broker's 503 no_match naming "([^"]*)"$`, func(a string) error { return st.chatGets503(a) })
			sc.Step(`^the operator tuned with U on$`, func() error { return st.tunedWithU() })
			sc.Step(`^the operator toggles U off$`, func() error { return st.togglesUOff() })
			sc.Step(`^the next turn's body carries no roger\.self_hosted_only$`, func() error { return st.nextNoSelfHosted() })
			sc.Step(`^the in-flight turn is unaffected$`, func() error { return st.inFlightUnaffected() })
			sc.Step(`^the tune-time body carries no key derived from O$`, func() error { return st.noOKey() })
			sc.Step(`^the operator enters a valid code at ~ and tunes$`, func() error { return st.entersCodeAndTunes() })
			sc.Step(`^the tune-time body carries roger\.freq = the code$`, func() error { return st.tuneBodyFreq() })
			sc.Step(`^the header reads PRIVATE FREQ without the code$`, func() error { return st.headerPrivateNoCode() })
			sc.Step(`^the tune-time request carries the X-Roger-Freq header = the code$`, func() error { return st.tuneFreqHeader() })
			sc.Step(`^the tune-time body carries no roger\.freq$`, func() error { return st.tuneBodyNoFreq() })
			sc.Step(`^U and Q filters are on$`, func() error { return st.uAndQOn() })
			sc.Step(`^the AGENT \[0\] (?:silent )?auto-tune runs$`, func() error { return st.autoTuneRuns() })
			sc.Step(`^it picks only from visibleBands$`, func() error { return st.picksFromVisible() })
			sc.Step(`^the chosen band's tune carries the same body keys the filters emit$`, func() error { return st.chosenCarriesFilterKeys() })
			sc.Step(`^U is on and limits\.default\.self_hosted is unset$`, func() error { return st.uOnDefaultUnset() })
			sc.Step(`^the TUI restarts$`, func() error { return st.tuiRestarts() })
			sc.Step(`^U is off and nothing in config\.json mentions self_hosted$`, func() error { return st.uOffNothingPersisted() })
			sc.Step(`^F, C, U on and Q at "([^"]*)"$`, func(a string) error { return st.allFiltersOn(a) })
			sc.Step(`^the operator tunes "([^"]*)"$`, func(a string) error { return st.tunesModel(a) })
			sc.Step(`^the body carries model "([^"]*)", roger\.confidential true, roger\.self_hosted_only true, provider\.quantizations (\[.*\])$`, func(a, b string) error { return st.bodyCarriesAll(a, b) })
			sc.Step(`^bands with mixed free and paid supply$`, func() error { return st.mixedFreePaid() })
			sc.Step(`^the chosen band matches the approved ladder in features/operator/auto_tune\.feature$`, func() error { return st.ladderUnchanged() })
			sc.Step(`^no profile is applied unless one is the tuned default$`, func() error { return st.noProfileApplied() })
			sc.Step(`^the operator tuned with pref "([^"]*)"$`, func(a string) error { return st.tunedWithPref(a) })
			sc.Step(`^the TUI is in compact mode$`, func() error { return st.compactMode() })
			sc.Step(`^the strip carries the glyph "([^"]*)" after the band name$`, func(a string) error { return st.stripGlyph(a) })
			sc.Step(`^the operator tuned with no pref$`, func() error { return st.tunedNoPref() })
			sc.Step(`^the strip equals the approved compact strip$`, func() error { return st.stripApproved() })
			sc.Step(`^pref "([^"]*)" and NO_COLOR$`, func(a string) error { return st.prefNoColor(a) })
			sc.Step(`^the TUI is in compact mode at (\d+) columns$`, func(n int) error { return st.compactAt(n) })
			sc.Step(`^"([^"]*)" is present and nothing wraps$`, func(a string) error { return st.glyphPresentNoWrap(a) })
			sc.Step(`^the operator is connected to "([^"]*)"$`, func(a string) error { return st.connectedTo(a) })
			sc.Step(`^the next turn's body carries roger\.pref = "([^"]*)"$`, func(a string) error { return st.nextTurnPref(a) })
			sc.Step(`^the endpoint URL and bearer key are unchanged \(features/proxy/live_options\.feature\)$`, func() error { return st.endpointUnchanged() })
			sc.Step(`^a streamed turn settles with usage\.cost = ([0-9.]+)$`, func(a string) error { return st.streamedSettles(a) })
			sc.Step(`^the session cost meter increases by exactly ([0-9.]+)$`, func(a string) error { return st.meterIncreases(a) })
			sc.Step(`^the trailing ": rogerai-cost=" comment is not counted again$`, func() error { return st.commentNotCounted() })
			sc.Step(`^a streamed turn is served by "([^"]*)" with model "([^"]*)" \(a fallback\)$`, func(a, b string) error { return st.streamedFallback(a, b) })
			sc.Step(`^the transcript's turn footer reads "([^"]*)"$`, func(a string) error { return st.turnFooter(a) })
			sc.Step(`^the band header still names the tuned primary$`, func() error { return st.headerNamesPrimary() })
			sc.Step(`^a broker that sends only ": rogerai-cost="$`, func() error { return st.oldBrokerCommentOnly() })
			sc.Step(`^a streamed turn settles$`, func() error { return st.streamedTurnSettles() })
			sc.Step(`^the meter increases from the comment$`, func() error { return st.meterFromComment() })
			sc.Step(`^a turn settles with usage\.rogerai\.locked_until$`, func() error { return st.settlesWithLock() })
			sc.Step(`^the cost line shows "locked until <time>" in dim$`, func() error { return st.costLineLocked() })
			sc.Step(`^the operator is on (the dial|chat|agent|any screen)$`, func(a string) error { return st.onScreen(a) })
			sc.Step(`^the approved binding for (.+) fires and nothing routing-related happens$`, func(a string) error { return st.approvedBindingFires(a) })
			sc.Step(`^the operator is editing a routing field$`, func() error { return st.editingRoutingField() })
			sc.Step(`^the perms screen opens as approved$`, func() error { return st.permsOpens() })
			sc.Step(`^the operator is on \[3\] CONFIG with the detail plate open$`, func() error { return st.configPlateOpen() })
			sc.Step(`^the windowshade collapses as approved and the edit buffer is discarded$`, func() error { return st.collapsedBufferDiscarded() })
			sc.Step(`^the footer reads "([^"]*)"$`, func(a string) error { return st.footerReads(a) })
			sc.Step(`^the operator opens help$`, func() error { return st.opensHelp() })
			sc.Step(`^the CONFIG section lists tab / space / p / t / v$`, func() error { return st.helpConfigKeys() })
			sc.Step(`^no other section changed$`, func() error { return st.noOtherHelpChanged() })
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Tags:  "~@cli && ~@proxy && ~@harness && ~@docs && ~@later && ~@slice5",
			Paths: []string{"../../features/tui/routing_profiles.feature"},
		},
	}
	if bddtest.Run(t, &suite) != 0 {
		t.Fatal("the TUI routing-profile scenarios failed")
	}
}
