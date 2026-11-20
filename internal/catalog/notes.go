package catalog

// RogerAI's curated notes (features/details/curated_catalog.feature): RogerAI's own words for
// RogerAI's own models, keyed by model id. The broker stamps them onto the offers it
// publishes, so every surface shows one reviewed description and a wording fix ships with
// one broker deploy. A sibling of Entry rather than a field of it: Entry is the acquisition
// manifest and refuses an entry with no download, and an adapter served beside its base has
// a description but no download of its own.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"rogerai.fm/roger/v7/internal/protocol"
)

//go:embed notes.json
var notesJSON []byte

// SiteOrigin is the public site a note's page path belongs to, for surfaces that cannot ask
// the broker (the TUI and the CLI). The broker resolves against its own configured origin.
const SiteOrigin = "https://rogerai.fm"

// Note is RogerAI's description of one model id.
type Note struct {
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	BestAt    string `json:"best_at,omitempty"`
	NotFor    string `json:"not_for,omitempty"`
	BaseModel string `json:"base_model,omitempty"`
	License   string `json:"license,omitempty"`
	Page      string `json:"page,omitempty"` // a site path, e.g. /research-wave-nano.html
}

// Notes is a validated set, keyed by canonical model id.
type Notes map[string]Note

// NoteID is the canonical form an id is matched in: cleaned and lower-cased.
func NoteID(id string) string { return strings.ToLower(protocol.DetailsText(id, 256)) }

// ParseNotes validates a notes file and rejects it whole if any note fails: a note that
// ships malformed would be shown to everyone.
func ParseNotes(b []byte) (Notes, error) {
	var list []Note
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("notes: invalid JSON: %w", err)
	}
	out := make(Notes, len(list))
	for _, n := range list {
		if err := n.validate(); err != nil {
			return nil, err
		}
		id := NoteID(n.ID)
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("notes: duplicate note id %q", n.ID)
		}
		out[id] = n
	}
	return out, nil
}

func (n Note) validate() error {
	if NoteID(n.ID) == "" {
		return fmt.Errorf("notes: a note has no id")
	}
	fail := func(format string, a ...any) error {
		return fmt.Errorf("notes: %q "+format, append([]any{n.ID}, a...)...)
	}
	if strings.TrimSpace(n.Summary) == "" {
		return fail("has no summary")
	}
	// The operator's strict rules (lengths, reserved uses) hold for our own words too.
	if err := (protocol.Taxonomy{Summary: n.Summary, BestAt: n.BestAt, NotFor: n.NotFor}).Validate(""); err != nil {
		return fail("%v", err)
	}
	for _, v := range []string{n.Summary, n.BestAt, n.NotFor, n.BaseModel, n.License} {
		if protocol.DetailsText(v, 1<<16) != v {
			return fail("has a control character or stray whitespace in %q", v)
		}
	}
	if n.Page != "" && !protocol.SitePath(n.Page) {
		return fail("page must be a site path, got %q", n.Page)
	}
	return nil
}

// Builtin returns the notes shipped in this binary.
func Builtin() (Notes, error) { return ParseNotes(notesJSON) }

// Apply returns t as published for model: RogerAI's note in place of any station text, with
// its base model and license when it has them; or, for a model with no note, t with any
// "rogerai" label it should not carry demoted to "declared" and no page.
func (ns Notes) Apply(model string, t protocol.Taxonomy) protocol.Taxonomy {
	n, ok := ns[NoteID(model)]
	if !ok {
		if t.DetailsSource == protocol.SourceRogerAI {
			t.DetailsSource = protocol.SourceDeclared
		}
		t.Page = ""
		return t
	}
	t.Summary, t.BestAt, t.NotFor = n.Summary, n.BestAt, n.NotFor
	t.DetailsSource, t.Page = protocol.SourceRogerAI, n.Page
	if n.BaseModel != "" {
		t.BaseModel = n.BaseModel
	}
	if n.License != "" {
		t.License = n.License
	}
	return t
}

// Has reports whether a note describes model.
func (ns Notes) Has(model string) bool { _, ok := ns[NoteID(model)]; return ok }

// PageURL resolves a note's site path against an origin; "" stays "".
func PageURL(origin, page string) string {
	if page == "" {
		return ""
	}
	return strings.TrimRight(origin, "/") + page
}

// PageDisplay is how a terminal shows a page: the URL on SiteOrigin without its scheme.
func PageDisplay(page string) string {
	return strings.TrimPrefix(PageURL(SiteOrigin, page), "https://")
}
