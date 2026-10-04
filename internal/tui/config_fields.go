package tui

// THE [3] CONFIG ROUTING FIELDS (features/tui/routing_profiles.feature). The table keeps its
// two headline columns; every other routing key of a band's rule lives on the plate under the
// table, walked with tab / shift+tab in one fixed order. Numbers and text are typed; the four
// choice fields cycle with space (or enter); require toggles with t / v. Each field writes
// one Limit key, which the host persists as limits.models.<m>.<key> (or limits.default).

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"rogerai.fm/roger/v6/internal/client"
)

type limFieldKind int

const (
	fkNumber limFieldKind = iota // typed digits (prices, t/s)
	fkText                       // typed free text, parsed on commit (params, ctx, ttft, region)
	fkChoice                     // cycled with space / enter
	fkToggle                     // require: t / v
)

// limField is one routing field of a band's rule.
type limField struct {
	label string
	kind  limFieldKind
}

// limFieldDefs is the tab order (routing_profiles.feature "tab walks every routing field").
var limFieldDefs = []limField{
	{"max $/1M out", fkNumber}, {"min t/s", fkNumber}, {"max $/1M in", fkNumber}, {"max $/request", fkNumber},
	{"quant", fkChoice}, {"pref", fkChoice}, {"require", fkToggle}, {"params (B)", fkText},
	{"min ctx", fkText}, {"max ttft", fkText}, {"trust", fkChoice}, {"self-hosted", fkChoice}, {"region", fkText},
}

const (
	lfMaxOut = iota
	lfMinTPS
	lfMaxIn
	lfMaxCost
	lfQuant
	lfPref
	lfRequire
	lfParams
	lfMinCtx
	lfMaxTTFT
	lfTrust
	lfSelfHosted
	lfRegion
)

// defaultLimitRow is the [3] CONFIG row that edits limits.default.
const defaultLimitRow = "default"

// limFocusLabel names the field the CONFIG cursor is on ("" off the band table).
func (m model) limFocusLabel() string {
	if m.mode != modeLimits || m.limOnBudget || m.limField < 0 || m.limField >= len(limFieldDefs) {
		return ""
	}
	return limFieldDefs[m.limField].label
}

// rowLimit / putRowLimit read and write the rule a CONFIG row edits: limits.default for the
// default row, the band's own entry otherwise.
func (m model) rowLimit(row string) Limit {
	if m.limits == nil {
		return Limit{}
	}
	if row == defaultLimitRow {
		m.limits.mu.Lock()
		defer m.limits.mu.Unlock()
		return m.limits.Default
	}
	return m.limits.resolve(row)
}

func (m model) putRowLimit(row string, l Limit) {
	if m.limits == nil {
		return
	}
	if row == defaultLimitRow {
		m.limits.mu.Lock()
		defer m.limits.mu.Unlock()
		m.limits.Default = l
		if m.limits.Save != nil {
			m.limits.Save(m.limits.Models, m.limits.Default)
		}
		return
	}
	m.limits.Set(row, l)
}

// fieldBuf is a field's current value as the edit buffer starts it.
func fieldBuf(l Limit, f int) string {
	switch f {
	case lfMaxOut:
		return trimZero(l.MaxOut)
	case lfMinTPS:
		return trimZero(l.MinTPS)
	case lfMaxIn:
		return trimZero(l.MaxIn)
	case lfMaxCost:
		return trimZero(l.MaxCost)
	case lfParams:
		if len(l.ParamsB) == 2 {
			return fmt.Sprintf("%g-%g", l.ParamsB[0], l.ParamsB[1])
		}
	case lfMinCtx:
		if l.MinCtx > 0 {
			return strconv.Itoa(l.MinCtx)
		}
	case lfMaxTTFT:
		if l.MaxTTFTMs > 0 {
			return strconv.Itoa(l.MaxTTFTMs)
		}
	case lfRegion:
		return strings.Join(l.Region, ",")
	}
	return ""
}

// applyField parses a typed value into field f of l. An empty value leaves the field as it was
// (clearing a rule is d, or typing 0); a value the contract refuses is an error for the plate.
func applyField(l Limit, f int, raw string) (Limit, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return l, nil
	}
	num := func() (float64, error) {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil || x < 0 {
			return 0, fmt.Errorf("%q is not a number ≥ 0", v)
		}
		return x, nil
	}
	var err error
	switch f {
	case lfMaxOut:
		l.MaxOut, err = num()
	case lfMinTPS:
		l.MinTPS, err = num()
	case lfMaxIn:
		l.MaxIn, err = num()
	case lfMaxCost:
		l.MaxCost, err = num()
	case lfParams:
		var r []float64
		if r, err = client.ParseParams(v); err == nil {
			l.ParamsB = r
		}
	case lfMinCtx:
		var n int
		if n, err = client.ParseCtx(v); err == nil {
			l.MinCtx = n
		}
	case lfMaxTTFT:
		var n int
		if n, err = client.ParseTTFT(v); err == nil {
			l.MaxTTFTMs = n
		}
	case lfRegion:
		var regs []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if _, rerr := client.NormRegion(p); rerr != nil {
				return l, rerr
			}
			if !containsFold(regs, p) {
				regs = append(regs, p)
			}
		}
		l.Region = regs
	}
	return l, err
}

func containsFold(xs []string, x string) bool {
	for _, y := range xs {
		if strings.EqualFold(y, x) {
			return true
		}
	}
	return false
}

// prefRing / trustRing are the choice cycles; "" is the unset default (balanced / any).
var (
	prefRing  = []string{"", "cheap", "fast", "reliable"}
	trustRing = []string{"", "verified", "confidential"}
)

func nextIn(ring []string, cur string) string {
	for i, v := range ring {
		if v == cur {
			return ring[(i+1)%len(ring)]
		}
	}
	return ring[0]
}

// quantChoices are the labels on air for a model, verbatim and in dial order, after "any".
func (m model) quantChoices(row string) []string {
	out := []string{"any"}
	for _, b := range m.bands {
		if b.model == row && b.quant != "" && !containsFold(out, b.quant) {
			out = append(out, b.quant)
		}
	}
	return out
}

// cycleField advances a choice field one step and returns the new rule.
func (m model) cycleField(row string, l Limit, f int) Limit {
	switch f {
	case lfPref:
		l.Pref = nextIn(prefRing, l.Pref)
	case lfTrust:
		l.TrustMin = nextIn(trustRing, l.TrustMin)
	case lfSelfHosted:
		l.SelfHosted = !l.SelfHosted
	case lfQuant:
		cur := "any"
		if len(l.Quants) == 1 {
			cur = l.Quants[0]
		}
		next := nextIn(m.quantChoices(row), cur)
		l.Quants = nil
		if next != "any" {
			l.Quants = []string{next}
		}
	}
	return l
}

// toggleRequire flips one capability in the rule's require list, keeping tools before vision.
func toggleRequire(l Limit, capName string) Limit {
	has := map[string]bool{}
	for _, c := range l.Require {
		has[c] = true
	}
	has[capName] = !has[capName]
	l.Require = nil
	for _, c := range []string{"tools", "vision"} {
		if has[c] {
			l.Require = append(l.Require, c)
		}
	}
	return l
}

// fieldShown is a field's value on the plate ("-" when unset).
func fieldShown(l Limit, f int) string {
	dash := "-"
	switch f {
	case lfMaxOut:
		if l.MaxOut > 0 {
			return fmt.Sprintf("out ≤ $%.2f/1M", l.MaxOut)
		}
	case lfMinTPS:
		if l.MinTPS > 0 {
			return fmt.Sprintf("≥%g t/s", l.MinTPS)
		}
	case lfMaxIn:
		if l.MaxIn > 0 {
			return fmt.Sprintf("in ≤ $%.2f/1M", l.MaxIn)
		}
	case lfMaxCost:
		if l.MaxCost > 0 {
			return fmt.Sprintf("≤ $%g/req", l.MaxCost)
		}
	case lfQuant:
		if len(l.Quants) > 0 {
			return strings.Join(l.Quants, ",")
		}
		return "any"
	case lfPref:
		if l.Pref != "" {
			return l.Pref
		}
		return "balanced"
	case lfRequire:
		if len(l.Require) > 0 {
			return strings.Join(l.Require, ",")
		}
	case lfParams:
		if len(l.ParamsB) == 2 {
			return fmt.Sprintf("%g-%gB", l.ParamsB[0], l.ParamsB[1])
		}
	case lfMinCtx:
		if l.MinCtx > 0 {
			if l.MinCtx%1024 == 0 {
				return fmt.Sprintf("ctx ≥ %dk", l.MinCtx/1024)
			}
			return fmt.Sprintf("ctx ≥ %d", l.MinCtx)
		}
	case lfMaxTTFT:
		if l.MaxTTFTMs > 0 {
			if l.MaxTTFTMs >= 1000 {
				return fmt.Sprintf("ttft ≤ %gs", float64(l.MaxTTFTMs)/1000)
			}
			return fmt.Sprintf("ttft ≤ %dms", l.MaxTTFTMs)
		}
	case lfTrust:
		if l.TrustMin != "" {
			return l.TrustMin
		}
		return "any"
	case lfSelfHosted:
		if l.SelfHosted {
			return "self-hosted"
		}
		return "no"
	case lfRegion:
		if len(l.Region) > 0 {
			return strings.Join(l.Region, ",")
		}
	}
	return dash
}

// limPlate is the selected row's detail plate: every field after the two table columns as
// "label value", the focused one in the selection style, and the choices of a focused choice
// field. Each line is clipped to the terminal (truncVisible ends a cut with "…").
func (m model) limPlate(row string, w int) []string {
	l := m.rowLimit(row)
	var cells []string
	for f := lfMaxIn; f < len(limFieldDefs); f++ {
		cell := limFieldDefs[f].label + " " + fieldShown(l, f)
		if f == m.limField {
			cells = append(cells, stSelText.Render(cell))
		} else {
			cells = append(cells, stDim.Render(cell))
		}
	}
	var lines []string
	line := "    "
	for _, c := range cells {
		if line != "    " && lipgloss.Width(line)+3+lipgloss.Width(c) > w-2 {
			lines = append(lines, line)
			line = "    "
		}
		if line != "    " {
			line += stDim.Render(" · ")
		}
		line += c
	}
	lines = append(lines, line)
	if m.limField == lfQuant || m.limField == lfPref || m.limField == lfTrust || m.limField == lfSelfHosted {
		lines = append(lines, "    "+stDim.Render(limFieldDefs[m.limField].label+": "+strings.Join(m.fieldChoices(row), " · ")+"   space cycle"))
	}
	if m.limField == lfRequire {
		lines = append(lines, "    "+stDim.Render("require: t tools · v vision"))
	}
	if m.limField < lfMaxIn {
		lines = append(lines, "    "+stDim.Render(limFieldDefs[m.limField].label+" "+fieldShown(l, m.limField)))
	}
	for i := range lines {
		lines[i] = truncVisibleTail(lines[i], w)
	}
	return lines
}

func (m model) fieldChoices(row string) []string {
	switch m.limField {
	case lfQuant:
		return m.quantChoices(row)
	case lfPref:
		return []string{"balanced", "cheap", "fast", "reliable"}
	case lfTrust:
		return []string{"any", "verified", "confidential"}
	}
	return []string{"no", "self-hosted"}
}
