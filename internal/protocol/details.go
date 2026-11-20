package protocol

// The details text (features/details): summary, best at, not for. One stranger writes it and
// everyone else reads it in a terminal and a browser, so it is the most hostile string the
// product displays. The rules are text_sanitization.feature.

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// SourceRogerAI labels details that come from RogerAI's curated notes. Only the broker sets
// it; Normalize strips whatever a node sent.
const SourceRogerAI = "rogerai"

// Field caps, in characters.
const (
	SummaryMax = 140
	DetailMax  = 200
	pageMax    = 200
)

// detailsClean drops every control (C0, DEL, C1, so ESC and with it any ANSI sequence),
// bidi and zero-width character, turns tab, CR, LF and the line/paragraph separators into
// one space, collapses runs of spaces and trims. Invalid UTF-8 decodes to U+FFFD.
func detailsClean(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029:
			space = true
			continue
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || spoofing(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// DetailsText is one details field as it may be shown: clean, one line, at most max
// characters. Every reader calls it again, so a broker that skipped it changes nothing.
func DetailsText(s string, max int) string {
	s = detailsClean(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return strings.TrimSpace(s[:i])
		}
		n++
	}
	return s
}

// reservedUse returns the first reserved word (in reservedOrder) that appears in text as a
// whole word, "" when none does. A word is a run of a-z, 0-9 and "-", so "outpatient" and
// "weaponsmith" are not matches and "target-acquisition" is.
func reservedUse(text string) string {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	}) {
		words[w] = true
	}
	for _, w := range reservedOrder {
		if words[w] {
			return w
		}
	}
	return ""
}

// SitePath reports whether p is a same-site path a page link may carry: "/" then letters,
// digits and . _ - / #, never "//" (another host), at most pageMax bytes.
func SitePath(p string) bool {
	if len(p) < 2 || len(p) > pageMax || p[0] != '/' || p[1] == '/' {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/#", r)) {
			return false
		}
	}
	return true
}

// detailsField is one details field as both layers walk it.
type detailsField struct {
	name     string
	v        *string
	max      int
	reserved bool // a reserved use is refused here (summary, best at; never not for)
}

// detailsFields is the one list both layers walk.
func (t *Taxonomy) detailsFields() []detailsField {
	return []detailsField{
		{"summary", &t.Summary, SummaryMax, true},
		{"best at", &t.BestAt, DetailMax, true},
		{"not for", &t.NotFor, DetailMax, false}, // "not for diagnosis" is the warning we want
	}
}

// normalizeDetails is the lenient layer: cut to the cap, drop a field that names a reserved
// use, then label who wrote what survived.
func (t *Taxonomy) normalizeDetails() {
	for _, f := range t.detailsFields() {
		if *f.v = DetailsText(*f.v, f.max); f.reserved && reservedUse(*f.v) != "" {
			*f.v = ""
		}
	}
	switch {
	case !t.DetailsDeclared():
		t.DetailsSource = ""
	case normWord(t.DetailsSource) == SourceRogerAI:
		t.DetailsSource = SourceRogerAI
	default:
		t.DetailsSource = SourceDeclared
	}
	if t.DetailsSource != SourceRogerAI || !SitePath(t.Page) {
		t.Page = ""
	}
}

// validateDetails is the strict layer: over-length is refused, never cut.
func (t Taxonomy) validateDetails() error {
	for _, f := range t.detailsFields() {
		c := detailsClean(*f.v)
		if n := utf8.RuneCountInString(c); n > f.max {
			return fmt.Errorf("%s: at most %d characters (got %d)", f.name, f.max, n)
		}
		if w := reservedUse(c); f.reserved && w != "" {
			return fmt.Errorf("%s: reserved use %q", f.name, w)
		}
	}
	return nil
}

// DetailsDeclared reports whether any details text is set.
func (t Taxonomy) DetailsDeclared() bool {
	return t.Summary != "" || t.BestAt != "" || t.NotFor != ""
}

// WithDetailsOf returns t carrying d's details (text, source, page) in place of its own.
func (t Taxonomy) WithDetailsOf(d Taxonomy) Taxonomy {
	t.Summary, t.BestAt, t.NotFor, t.DetailsSource, t.Page = d.Summary, d.BestAt, d.NotFor, d.DetailsSource, d.Page
	return t
}

// Ballot is one station's say in what a band's details are.
type Ballot struct {
	T               Taxonomy
	Online, Curated bool // Curated: a commercial-API proxy station, not RogerAI's notes
}

// Details is what a band of stations says about its model, by FoldDetails.
type Details struct {
	Summary, BestAt, NotFor, Source, Page string
	DeclaredBy, Stations                  int // stations that sent this text, of those that voted
}

// Empty reports whether there is no text.
func (d Details) Empty() bool { return d.Summary == "" && d.BestAt == "" && d.NotFor == "" }

// FoldDetails picks a band's details by the /market rule its category already follows:
// RogerAI's curated text when any station carries it; otherwise the (summary, best at, not
// for) triple MOST online human stations declared, ties to the alphabetically first summary;
// curated proxies only when no online human has text; offline stations only when nothing is
// on air. Fields are never mixed across stations. Every text is re-canonicalized here.
func FoldDetails(ballots []Ballot) Details {
	var human, proxy, offline []Taxonomy
	humans, proxies := 0, 0
	for _, b := range ballots {
		t := b.T
		for _, f := range t.detailsFields() {
			*f.v = DetailsText(*f.v, f.max)
		}
		if normWord(t.DetailsSource) == SourceRogerAI && t.DetailsDeclared() {
			page := ""
			if SitePath(t.Page) {
				page = t.Page
			}
			return Details{Summary: t.Summary, BestAt: t.BestAt, NotFor: t.NotFor, Source: SourceRogerAI, Page: page}
		}
		switch {
		case !b.Online:
			offline = append(offline, t)
		case b.Curated:
			proxies++
			proxy = append(proxy, t)
		default:
			humans++
			human = append(human, t)
		}
	}
	pool, voters := human, humans
	if !anyDetails(pool) {
		pool, voters = proxy, proxies
	}
	if humans+proxies == 0 {
		pool, voters = offline, len(offline)
	}
	type key struct{ s, b, n string }
	count := map[key]int{}
	for _, t := range pool {
		if t.DetailsDeclared() {
			count[key{t.Summary, t.BestAt, t.NotFor}]++
		}
	}
	keys := make([]key, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return Details{}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if count[a] != count[b] {
			return count[a] > count[b]
		}
		return a.s+"\x00"+a.b+"\x00"+a.n < b.s+"\x00"+b.b+"\x00"+b.n
	})
	k := keys[0]
	return Details{Summary: k.s, BestAt: k.b, NotFor: k.n, Source: SourceDeclared, DeclaredBy: count[k], Stations: voters}
}

func anyDetails(ts []Taxonomy) bool {
	for _, t := range ts {
		if t.DetailsDeclared() {
			return true
		}
	}
	return false
}
