package tui

// The model taxonomy in the TUI (MODEL-CATEGORIES-DESIGN-2026-10-05):
// features/categories/tui_dial.feature - a band's category label, grade badge, detail and the
//   G filter;
// features/categories/tui_share_editor.feature - the SHARE tab's CATEGORY column and the g
//   editor (modeShareMeta), a front-end on node.Controller.SetMeta/ClearMeta.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/protocol"
)

// guessMark marks a taxonomy no station declared - the dial's honesty mark for a guess, as
// ⌁~ is for an inferred lineage.
const guessMark = "~"

// bandTaxonomy is what a band's stations say it is for, by the /market rule (founder ruling
// 2026-10-05, slice-4 review): the pair MOST of its online HUMAN stations carry (ties
// alphabetical), curated proxies labelling only a band no human station labels, and a band
// with nothing on air falling back to every station so it still reads. declared counts the
// voters that DECLARED that pair; tags and grades come from every on-air station (all of
// them when none is on air), so one station's "aligned" never hides another's grade.
type bandTaxonomy struct {
	cat, sub string
	declared int
	tags     []string
	policies []string
}

func foldTaxonomy(all []offer) bandTaxonomy {
	// Canonicalized on read: the broker already does it, but the dial may be pointed at any
	// broker, and these strings are printed into a terminal.
	var human, curated, online []protocol.Taxonomy
	for _, o := range all {
		t := o.Taxonomy.Canonical(canonModality(o.Modality))
		if !o.Online {
			continue
		}
		online = append(online, t)
		if o.Curated {
			curated = append(curated, t)
		} else {
			human = append(human, t)
		}
	}
	voters, pool := human, online
	if !hasCategory(voters) {
		voters = curated
	}
	if len(online) == 0 {
		for _, o := range all {
			pool = append(pool, o.Taxonomy.Canonical(canonModality(o.Modality)))
		}
		voters = pool
	}
	pairs, declared, tags := map[string]int{}, map[string]int{}, map[string]int{}
	pols := map[string]bool{}
	for _, t := range voters {
		if t.Category != "" {
			k := t.Category + "/" + t.Subcategory
			pairs[k]++
			if t.TaxonomySource == protocol.SourceDeclared {
				declared[k]++
			}
		}
	}
	for _, t := range pool {
		for _, tag := range t.Tags {
			tags[tag]++
		}
		if t.ContentPolicy != "" {
			pols[t.ContentPolicy] = true
		}
	}
	var bt bandTaxonomy
	best, bestKey := 0, ""
	for k, n := range pairs {
		if n > best || (n == best && k < bestKey) {
			best, bestKey = n, k
		}
	}
	if bestKey != "" {
		bt.cat, bt.sub = protocol.SplitCategory(bestKey)
		bt.declared = declared[bestKey]
	}
	for t := range tags {
		bt.tags = append(bt.tags, t)
	}
	sort.Slice(bt.tags, func(i, j int) bool {
		a, b := bt.tags[i], bt.tags[j]
		return tags[a] > tags[b] || (tags[a] == tags[b] && a < b)
	})
	for p := range pols {
		bt.policies = append(bt.policies, p)
	}
	sort.Strings(bt.policies)
	return bt
}

func hasCategory(ts []protocol.Taxonomy) bool {
	for _, t := range ts {
		if t.Category != "" {
			return true
		}
	}
	return false
}

func taxPair(cat, sub string) string {
	if sub == "" {
		return cat
	}
	return cat + "/" + sub
}

// bandCategoryLabel is the band's category, marked as a guess when no station declared it;
// "" when its stations carry no category (absent claims nothing).
func bandCategoryLabel(bd band) string {
	if bd.tax.cat == "" {
		return ""
	}
	if bd.tax.declared == 0 {
		return taxPair(bd.tax.cat, bd.tax.sub) + guessMark
	}
	return taxPair(bd.tax.cat, bd.tax.sub)
}

// bandGrade is the content-policy grade worth a badge: the least-refusing one any station
// declared, so one station's "aligned" never hides another's "uncensored". Aligned is the
// default reading and goes unbadged (it shows in the detail).
func bandGrade(bd band) string {
	for _, g := range []string{"uncensored", "reduced-refusal"} {
		if slices.Contains(bd.tax.policies, g) {
			return g
		}
	}
	return ""
}

// screeningNote is the one sentence every surface shows next to a grade: a grade describes
// the weights, never the network (policy_honesty.feature).
const screeningNote = "RogerAI still screens every request"

// bandTaxonomyDetail is the band detail's taxonomy line(s), or "" when there is nothing.
func bandTaxonomyDetail(bd band) []string {
	var parts []string
	if l := taxPair(bd.tax.cat, bd.tax.sub); bd.tax.cat != "" {
		parts = append(parts, fmt.Sprintf("category %s · declared by %d of %d stations", l, bd.tax.declared, len(bd.all)))
	}
	if len(bd.tax.tags) > 0 {
		parts = append(parts, "tags "+strings.Join(bd.tax.tags, ", "))
	}
	if len(bd.tax.policies) > 0 {
		parts = append(parts, "content policies "+strings.Join(bd.tax.policies, ", "))
	}
	if len(parts) == 0 {
		return nil
	}
	lines := []string{strings.Join(parts, "  ·  ")}
	if g := bandGrade(bd); g != "" {
		lines = append(lines, g+": the station says its weights refuse less - "+screeningNote)
	}
	return lines
}

// categoriesOnAir lists the categories the dial's bands carry, in canonical order.
func (m model) categoriesOnAir() []string {
	have := map[string]bool{}
	for _, b := range m.bands {
		// Voice bands are not on the dial's list (they live in the DJ BOOTH), so a category
		// only they carry would be a filter that shows nothing.
		if b.tax.cat != "" && !b.isVoice() {
			have[b.tax.cat] = true
		}
	}
	var out []string
	for _, c := range protocol.Categories() {
		if have[c.Name] {
			out = append(out, c.Name)
		}
	}
	return out
}

// cycleCategoryFilter advances the category filter: off -> each category on the dial -> off,
// the same one-keypress family as Q.
func (m model) cycleCategoryFilter() model {
	cats := m.categoriesOnAir()
	if len(cats) == 0 {
		m.fCat = ""
		m.status = stDim.Render("no categorized bands on the dial")
		return m
	}
	next := cats[0]
	if i := slices.Index(cats, m.fCat); m.fCat != "" {
		next = ""
		if i >= 0 && i+1 < len(cats) {
			next = cats[i+1]
		}
	}
	m.fCat = next
	if next == "" {
		m.status = stDim.Render("category filter off")
	} else {
		m.status = stDim.Render("showing ") + stKey.Render(next) + stDim.Render(" bands · G next")
	}
	m.clampBrowse()
	return m
}

// --- the SHARE tab ----------------------------------------------------------------------

// shareCategoryCell is the CATEGORY cell: the declaration, else the guess marked "~", else "-".
func shareCategoryCell(r shareRow) string {
	if r.declared.Declared() && r.declared.Category != "" {
		return taxPair(r.declared.Category, r.declared.Subcategory)
	}
	if r.declared.Declared() {
		return "(no category)"
	}
	if r.inferred.Category != "" {
		return taxPair(r.inferred.Category, r.inferred.Subcategory) + guessMark
	}
	return "-"
}

// The editor's fields, in tab order.
const (
	mtFieldCat = iota
	mtFieldSub
	mtFieldTags
	mtFieldTuning
	mtFieldPolicy
	mtFieldLicense
	mtFieldBase
	mtFieldCount
)

// metaEditor is the g editor's state (modeShareMeta), embedded in model.
type metaEditor struct {
	mtModel, mtModality                                                string
	mtCat, mtSub, mtTags, mtTuning, mtPolicy, mtLicense, mtBase, mtErr string
	mtField                                                            int
	mtGuess                                                            bool // prefilled from detection, not a declaration
}

var (
	mtTunings  = []string{"", "base", "instruct", "finetune", "merge", "quantized", "adapter"}
	mtPolicies = []string{"", "aligned", "reduced-refusal", "uncensored"}
)

func mtCategories() []string {
	out := []string{""}
	for _, c := range protocol.Categories() {
		out = append(out, c.Name)
	}
	return out
}

func mtSubcategories(cat string) []string {
	out := []string{""}
	for _, c := range protocol.Categories() {
		if c.Name == cat {
			out = append(out, c.Subcategories...)
		}
	}
	return out
}

func cycleIn(list []string, cur string, step int) string {
	i := slices.Index(list, cur)
	if i < 0 {
		i = 0
	}
	return list[(i+step+len(list))%len(list)]
}

func (m model) isVoiceMeta() bool {
	return m.mtModality == protocol.ModalityTTS || m.mtModality == protocol.ModalitySTT
}

// enterShareMeta opens the category editor for the row at the cursor, prefilled with the
// declaration, else the guess (flagged). Not login-gated: describing a model is not earning.
func (m model) enterShareMeta() (tea.Model, tea.Cmd) {
	if len(m.shareRows) == 0 || m.ctrl == nil {
		return m, nil
	}
	row := m.shareRows[m.shareCursor]
	d, inf, _ := m.ctrl.MetaFor(row.model)
	src, guess := d, false
	if !d.Declared() {
		src, guess = inf, true
	}
	m.metaEditor = metaEditor{
		mtModel: row.model, mtModality: row.modality, mtGuess: guess,
		mtCat: src.Category, mtSub: src.Subcategory, mtTags: strings.Join(src.Tags, ", "),
		mtTuning: src.Tuning, mtPolicy: src.ContentPolicy, mtLicense: src.License, mtBase: src.BaseModel,
	}
	if m.isVoiceMeta() {
		m.mtCat, m.mtSub = "audio", row.modality
	}
	m.mode = modeShareMeta
	m.status = stDim.Render("tab field · ←→ choose · type tags/license/base · ⏎ save · ctrl+x clear · esc")
	return m, nil
}

func (m *model) mtText() *string {
	switch m.mtField {
	case mtFieldTags:
		return &m.mtTags
	case mtFieldLicense:
		return &m.mtLicense
	case mtFieldBase:
		return &m.mtBase
	}
	return nil
}

// onShareMetaKey drives the category editor.
func (m *model) onShareMetaKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeShare
		m.status = stDim.Render("cancelled - category unchanged")
		return m, nil
	case "enter":
		m.commitShareMeta()
		return m, nil
	case "ctrl+x":
		if err := m.ctrl.ClearMeta(m.mtModel); err != nil {
			m.mtErr = err.Error()
			return m, nil
		}
		m.syncShareCache()
		m.mode = modeShare
		m.status = stDim.Render("category cleared - the guess is back")
		return m, nil
	case "tab", "down":
		m.mtField = (m.mtField + 1) % mtFieldCount
		return m, nil
	case "shift+tab", "up":
		m.mtField = (m.mtField - 1 + mtFieldCount) % mtFieldCount
		return m, nil
	case "right", "left":
		step := 1
		if k.String() == "left" {
			step = -1
		}
		switch m.mtField {
		case mtFieldCat:
			if !m.isVoiceMeta() {
				m.mtCat, m.mtSub = cycleIn(mtCategories(), m.mtCat, step), ""
			}
		case mtFieldSub:
			if !m.isVoiceMeta() {
				m.mtSub = cycleIn(mtSubcategories(m.mtCat), m.mtSub, step)
			}
		case mtFieldTuning:
			m.mtTuning = cycleIn(mtTunings, m.mtTuning, step)
		case mtFieldPolicy:
			m.mtPolicy = cycleIn(mtPolicies, m.mtPolicy, step)
		}
		return m, nil
	case "backspace":
		if p := m.mtText(); p != nil && *p != "" {
			r := []rune(*p)
			*p = string(r[:len(r)-1])
		}
		return m, nil
	}
	// A terminal sends a space as its own key (tea.KeySpace), not as a rune.
	if p := m.mtText(); p != nil && (k.Type == tea.KeyRunes || k.Type == tea.KeySpace) {
		if k.Type == tea.KeySpace {
			*p += " "
		} else {
			*p += string(k.Runes)
		}
	}
	return m, nil
}

// commitShareMeta saves through the controller, which validates exactly as the console and
// `roger share` do. A refusal keeps the editor open with the reason.
func (m *model) commitShareMeta() {
	var tags []string
	for _, t := range strings.Split(m.mtTags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	onAir := m.shares[m.mtModel] != nil
	err := m.ctrl.SetMeta(m.mtModel, protocol.Taxonomy{Category: m.mtCat, Subcategory: m.mtSub, Tags: tags,
		Tuning: m.mtTuning, ContentPolicy: m.mtPolicy, License: m.mtLicense, BaseModel: m.mtBase})
	if err != nil {
		m.mtErr = err.Error()
		return
	}
	m.syncShareCache()
	m.mode = modeShare
	m.status = stDim.Render("category saved for ") + stKey.Render(m.mtModel)
	if onAir {
		m.status += stDim.Render(" - applies the next time it goes on air")
	}
}

// shareMetaView renders the category editor.
func (m model) shareMetaView(w int) string {
	var b strings.Builder
	head := stBrand.Render("CATEGORY") + stDim.Render(" · ") + stKey.Render(m.mtModel)
	if m.mtGuess {
		head += stDim.Render("   guess until saved")
	}
	b.WriteString("  " + truncVisible(stSelBar.Render("▌")+" "+head, w-2) + "\n\n")
	none := func(v string) string {
		if v == "" {
			return "(none)"
		}
		return v
	}
	row := func(field int, label, val string, choice bool) {
		mark, v := "  ", val
		if field == m.mtField {
			mark = stSelBar.Render("▸ ")
			if choice {
				v = "◂ " + val + " ▸"
			} else {
				v = val + "_"
			}
		}
		b.WriteString("  " + mark + stDim.Render(pad(label, 13)) + stKey.Render(v) + "\n")
	}
	row(mtFieldCat, "category", none(m.mtCat), true)
	row(mtFieldSub, "subcategory", none(m.mtSub), true)
	row(mtFieldTags, "tags", m.mtTags, false)
	row(mtFieldTuning, "tuning", none(m.mtTuning), true)
	row(mtFieldPolicy, "policy", none(m.mtPolicy), true)
	row(mtFieldLicense, "license", m.mtLicense, false)
	row(mtFieldBase, "base model", m.mtBase, false)
	if m.isVoiceMeta() {
		b.WriteString("\n  " + stDim.Render("a "+m.mtModality+" model is always audio/"+m.mtModality) + "\n")
	}
	if m.mtPolicy == "uncensored" || m.mtPolicy == "reduced-refusal" {
		b.WriteString("\n  " + stDim.Render("the grade describes the weights - "+screeningNote) + "\n")
	}
	if m.mtErr != "" {
		b.WriteString("\n  " + stEmber.Render(m.mtErr) + "\n")
	}
	b.WriteString("\n  " + stDim.Render("a category is your own description - consumers see whether it was declared or guessed") + "\n")
	return b.String()
}
