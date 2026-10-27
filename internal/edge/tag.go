package edge

// TAGS (features/edge/tags.feature): the owner's own words for what a unit IS, kept on the node
// record beside the name they chose. A tag names and groups; it is never a capability, a permission
// or a grant, and it is never read from a peer - a node cannot label itself into a group whose
// rules are looser.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"rogerai.fm/roger/v7/internal/store"
)

// MaxTags bounds an owner's words on one node: enough to group a fleet, few enough to read on a row.
const MaxTags = 12

// MaxTagLen bounds one tag.
const MaxTagLen = 32

// TagScopePrefix marks an alert rule scoped to a tag rather than a node: "tag:production".
const TagScopePrefix = "tag:"

// NormalizeTag is the owner's word as it is kept: trimmed and lowercased.
func NormalizeTag(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidTag reports why a word cannot be a tag, or nil. The rule is narrow on purpose: a tag goes in
// a rule scope, a column and a URL query, so anything that would need quoting is refused here.
func ValidTag(s string) error {
	if s == "" {
		return errors.New("a tag needs a word")
	}
	if strings.Contains(s, TagScopePrefix) || strings.Contains(s, ":") {
		return fmt.Errorf("a tag cannot contain a colon: %q", s)
	}
	if len([]rune(s)) > MaxTagLen {
		return fmt.Errorf("a tag is at most %d characters: %q", MaxTagLen, s)
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return fmt.Errorf("a tag is lowercase letters, digits, dot, dash or underscore, starting with a letter or a digit: %q", s)
		}
	}
	return nil
}

// CleanTags normalises, validates, de-duplicates and sorts a set of words, refusing the first that
// cannot be a tag and refusing more than MaxTags.
func CleanTags(words []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		t := NormalizeTag(w)
		if err := ValidTag(t); err != nil {
			return nil, err
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("a node carries at most %d tags; that is %d", MaxTags, len(out))
	}
	sort.Strings(out)
	return out, nil
}

// TagScope is the scope string for a rule that covers a tag.
func TagScope(tag string) string { return TagScopePrefix + tag }

// ScopeTag reads a rule scope as a tag; ok is false when the scope is not a tag scope.
func ScopeTag(scope string) (string, bool) {
	t, ok := strings.CutPrefix(scope, TagScopePrefix)
	return t, ok
}

// HasTag reports whether a node's tags include tag.
func HasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// SetTags replaces a node's tags with the owner's words. The words are cleaned first, so an invalid
// one changes nothing.
func (f *Fleet) SetTags(id string, words []string) error {
	clean, err := CleanTags(words)
	if err != nil {
		return err
	}
	return f.mutate(id, func(n *store.EdgeNode) error {
		if tagsEqual(n.Tags, clean) {
			return nil // nothing changed: no history line for a no-op
		}
		n.Tags = clean
		n.History = append(n.History, store.EdgeEvent{At: f.now().Unix(), What: "tagged", Detail: tagsDetail(clean)})
		return nil
	})
}

// EditTags adds and removes words on a node, leaving the rest. Removing a tag the node does not
// carry is not an error: the owner asked for an end state, and it is already so.
func (f *Fleet) EditTags(id string, add, remove []string) error {
	addClean, err := CleanTags(add)
	if err != nil {
		return err
	}
	rmClean := make([]string, 0, len(remove))
	for _, w := range remove {
		rmClean = append(rmClean, NormalizeTag(w)) // a word that is not a tag simply matches nothing
	}
	return f.mutate(id, func(n *store.EdgeNode) error {
		next := append([]string(nil), n.Tags...)
		for _, t := range addClean {
			if !HasTag(next, t) {
				next = append(next, t)
			}
		}
		kept := next[:0]
		for _, t := range next {
			if !HasTag(rmClean, t) {
				kept = append(kept, t)
			}
		}
		clean, err := CleanTags(kept)
		if err != nil {
			return err
		}
		if tagsEqual(n.Tags, clean) {
			return nil
		}
		n.Tags = clean
		n.History = append(n.History, store.EdgeEvent{At: f.now().Unix(), What: "tagged", Detail: tagsDetail(clean)})
		return nil
	})
}

func tagsDetail(tags []string) string {
	if len(tags) == 0 {
		return "none"
	}
	return strings.Join(tags, " ")
}

func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
