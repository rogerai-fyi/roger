package client

// Named routing profiles (ROUTING-EXPRESSION-CONTRACT.md §9). A profile is client-side config
// in config.json under "profiles": {"<name>": {model?, models?, provider?, roger?}}. Every
// first-party client resolves it into the §1 body object; the broker never sees "@profile/".
//
// Load is lenient where the config loader is (a typo must never brick `roger use`): an unknown
// key inside a profile is ignored and reported, a key outside the routing contract (max_tokens,
// system) is ignored and reported, a profiles section that is not an object is ignored with one
// warning. A value of the wrong type or out of the contract's range is an error for THAT
// profile only, naming the profile and the key.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"rogerai.fm/roger/v6/internal/routingrules"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProfileRef is the prefix a model id (or roger.profile) uses to name a profile.
const ProfileRef = "@profile/"

// DefaultProfileName is reserved: the default profile is built from limits.*.
const DefaultProfileName = "default"

var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidProfileName reports whether name is a lowercase slug (1-64 chars, a-z 0-9 - _).
func ValidProfileName(name string) bool { return profileNameRE.MatchString(name) }

// Profile is one validated profile: Body holds only routing keys (model, models, provider,
// roger), each value exactly as written. Unknown / NonRouting name the keys load ignored.
type Profile struct {
	Name       string
	Body       map[string]any
	Unknown    []string // e.g. "roger.prefer"
	NonRouting []string // e.g. "max_tokens", "system"
}

// Profiles is the parsed "profiles" section: valid profiles by name, per-profile load errors,
// invalid names and a section-level warning (the section is not an object).
type Profiles struct {
	ByName       map[string]*Profile
	Errs         map[string]error
	BadNames     []string
	ReservedSeen bool // a profile named "default" was defined (and ignored)
	SectionWarn  string
}

var (
	profileProviderKeys = routingrules.ProviderKeys
	profileRogerKeys    = routingrules.RogerKeys
	profileMaxPriceKeys = routingrules.MaxPriceKeys
)

// KnownRoutingKey reports whether sub (dotted, below top "provider" or "roger") is a routing
// key a profile may hold, e.g. ("provider", "max_price.request").
func KnownRoutingKey(top, sub string) bool {
	head, rest, nested := strings.Cut(sub, ".")
	switch top {
	case "provider":
		if head == "max_price" && nested {
			return profileMaxPriceKeys[rest]
		}
		return !nested && profileProviderKeys[head]
	case "roger":
		return !nested && profileRogerKeys[head]
	}
	return false
}

// ParseProfiles reads the "profiles" section of a config.json document. A missing section is
// an empty set; a corrupt document is an error (the caller keeps its own fallback).
func ParseProfiles(doc []byte) (*Profiles, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return nil, err
	}
	out := &Profiles{ByName: map[string]*Profile{}, Errs: map[string]error{}}
	raw, ok := top["profiles"]
	if !ok || string(raw) == "null" {
		return out, nil
	}
	var section map[string]json.RawMessage
	if json.Unmarshal(raw, &section) != nil {
		out.SectionWarn = "profiles: ignored (not an object) - run roger profile list"
		return out, nil
	}
	for name, body := range section {
		if name == DefaultProfileName {
			out.ReservedSeen = true
			continue
		}
		if !ValidProfileName(name) {
			out.BadNames = append(out.BadNames, name)
			continue
		}
		p, err := parseProfile(name, body)
		if err != nil {
			out.Errs[name] = err
			continue
		}
		out.ByName[name] = p
	}
	sort.Strings(out.BadNames)
	return out, nil
}

// Names lists the valid profile names, sorted.
func (ps *Profiles) Names() []string {
	out := make([]string, 0, len(ps.ByName))
	for n := range ps.ByName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Get returns the named profile, or the load error naming it, or ok=false when undefined.
func (ps *Profiles) Get(name string) (*Profile, error, bool) {
	if ps == nil {
		return nil, nil, false
	}
	if err, bad := ps.Errs[name]; bad {
		return nil, err, true
	}
	p, ok := ps.ByName[name]
	return p, nil, ok
}

func parseProfile(name string, raw json.RawMessage) (*Profile, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, fmt.Errorf("profile %s: not an object", name)
	}
	p := &Profile{Name: name, Body: map[string]any{}}
	for k, v := range m {
		switch k {
		case "model", "models":
			p.Body[k] = v
		case "provider", "roger":
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("profile %s: %s must be an object", name, k)
			}
			known := profileProviderKeys
			if k == "roger" {
				known = profileRogerKeys
			}
			kept := map[string]any{}
			for sk, sv := range obj {
				if !known[sk] {
					p.Unknown = append(p.Unknown, k+"."+sk)
					continue
				}
				kept[sk] = sv
			}
			if len(kept) > 0 {
				p.Body[k] = kept
			}
		default:
			p.NonRouting = append(p.NonRouting, k)
		}
	}
	sort.Strings(p.Unknown)
	sort.Strings(p.NonRouting)
	if r, ok := p.Body["roger"].(map[string]any); ok {
		if ref, has := r["profile"]; has && ref != nil {
			return nil, fmt.Errorf("profile %s: profiles cannot reference profiles", name)
		}
	}
	if err := ValidateRoutingBody(p.Body); err != nil {
		return nil, fmt.Errorf("profile %s: %v", name, err)
	}
	return p, nil
}

// ValidateRoutingBody checks the routing keys of a body object with the contract's types and
// ranges (§1a), naming the first offending key. It validates only what is present.
func ValidateRoutingBody(b map[string]any) error {
	if v, ok := b["model"]; ok && v != nil {
		if s, isStr := v.(string); !isStr || strings.TrimSpace(s) == "" {
			return fmt.Errorf("model must be a model id")
		} else if len(s) > ModelIDMax {
			return fmt.Errorf("model: a model id longer than %d characters", ModelIDMax)
		}
	}
	if v, ok := b["models"]; ok && v != nil {
		arr, isArr := v.([]any)
		if !isArr {
			return fmt.Errorf("models must be a list of model ids")
		}
		if len(arr) > RoutingListMax { // the broker bounds raw entries before counting models
			return fmt.Errorf("models has more than %d entries", RoutingListMax)
		}
		// The effective list is [model] ++ models (contract §3): the model counts too.
		seen := map[string]bool{}
		if m, ok := b["model"].(string); ok && strings.TrimSpace(m) != "" {
			seen[sugarless(m)] = true
		}
		for _, e := range arr {
			s, isStr := e.(string)
			if !isStr || strings.TrimSpace(s) == "" || s != strings.TrimSpace(s) { // padded: refused, as on the broker
				return fmt.Errorf("models must be a list of model ids")
			}
			if len(s) > ModelIDMax {
				return fmt.Errorf("models: a model id longer than %d characters", ModelIDMax)
			}
			seen[sugarless(s)] = true
		}
		if len(seen) > ModelsMax {
			return fmt.Errorf("models has more than %d distinct models", ModelsMax)
		}
	}
	if err := ValidateRoutingValues(b); err != nil {
		return err
	}
	p, _ := b["provider"].(map[string]any)
	r, _ := b["roger"].(map[string]any)
	if p != nil && r != nil && p["sort"] != nil && r["pref"] != nil {
		return fmt.Errorf("provider.sort and roger.pref are exclusive")
	}
	return nil
}

// The broker's size bounds on a routing object, shared through internal/routingrules.
const (
	RoutingListMax = routingrules.RoutingListMax
	QuantLabelMax  = routingrules.QuantLabelMax
	RoutingIntMax  = routingrules.RoutingIntMax
	ModelsMax      = routingrules.ModelsMax
	ModelIDMax     = routingrules.ModelIDMax
)

// ValidateRoutingValues checks the type and range of each provider and roger key present
// (§1a); the rules live in internal/routingrules so the local plane shares them Core-free.
func ValidateRoutingValues(b map[string]any) error { return routingrules.Values(b) }

// MergeProfile layers a request (or flag set) over a profile: a request key replaces the
// profile's value for that key only (provider.* and roger.* per sub-key; provider.max_price per
// sub-key); a null in the request clears the profile's value. An exclusive pair produced by the
// merge (provider.sort with roger.pref) is an error naming both and which came from the profile.
func MergeProfile(prof, req map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range prof {
		out[k] = deepCopy(v)
	}
	for k, v := range req {
		switch k {
		case "provider", "roger":
			rv, isObj := v.(map[string]any)
			if v == nil {
				delete(out, k)
				continue
			}
			if !isObj {
				out[k] = v // left for the validator / broker to refuse
				continue
			}
			base, _ := out[k].(map[string]any)
			if base == nil {
				base = map[string]any{}
			}
			for sk, sv := range rv {
				if sv == nil {
					delete(base, sk)
					continue
				}
				if k == "provider" && sk == "max_price" {
					bm, _ := base["max_price"].(map[string]any)
					rm, rIsObj := sv.(map[string]any)
					if bm != nil && rIsObj {
						merged := map[string]any{}
						for a, b := range bm {
							merged[a] = b
						}
						for a, b := range rm {
							if b == nil {
								delete(merged, a)
							} else {
								merged[a] = b
							}
						}
						base["max_price"] = merged
						continue
					}
				}
				base[sk] = sv
			}
			if len(base) == 0 {
				delete(out, k)
			} else {
				out[k] = base
			}
		default:
			if v == nil {
				delete(out, k)
			} else {
				out[k] = v
			}
		}
	}
	p, _ := out["provider"].(map[string]any)
	r, _ := out["roger"].(map[string]any)
	if p != nil && r != nil && p["sort"] != nil && r["pref"] != nil {
		pp, _ := prof["provider"].(map[string]any)
		from := "provider.sort from the profile, roger.pref from the request"
		if pp == nil || pp["sort"] == nil {
			from = "roger.pref from the profile, provider.sort from the request"
		}
		return nil, fmt.Errorf("provider.sort and roger.pref are exclusive (%s)", from)
	}
	return out, nil
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = deepCopy(x)
		}
		return m
	case []any:
		a := make([]any, len(t))
		for i, x := range t {
			a[i] = deepCopy(x)
		}
		return a
	}
	return v
}

// guestProfileOf is the profile a request body names ("" for none, or for a body the
// resolver refuses on its own: two profiles, sugar on the reference, not JSON).
func guestProfileOf(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil || m == nil {
		return ""
	}
	name, _, err := profileRefOf(m)
	if err != nil {
		return ""
	}
	return name
}

// profileRefOf reports the profile a body names, from model "@profile/<n>" (a guest provider
// prefix such as roger/ is stripped first) or roger.profile. Case-sensitive: "@Profile/x" is
// not a reference. A sugar suffix on the reference is an error.
func profileRefOf(m map[string]any) (name string, fromModel bool, err error) {
	model, _ := m["model"].(string)
	model = guestModelID(model)
	var a, b string
	if strings.HasPrefix(model, ProfileRef) {
		a = strings.TrimPrefix(model, ProfileRef)
	}
	if r, ok := m["roger"].(map[string]any); ok {
		if s, isStr := r["profile"].(string); isStr && strings.HasPrefix(s, ProfileRef) {
			b = strings.TrimPrefix(s, ProfileRef)
		}
	}
	for _, n := range []string{a, b} {
		if n != "" && sugarless(n) != n {
			return "", false, &RoutingRefusal{Msg: "sugar on a profile reference; put " + n[len(sugarless(n)):] + " on the profile's models"}
		}
	}
	switch {
	case a != "" && b != "" && a != b:
		return "", false, &RoutingRefusal{Msg: "two profiles named: @profile/" + a + " and @profile/" + b}
	case a != "":
		return a, true, nil
	case b != "":
		return b, false, nil
	}
	return "", false, nil
}

// ResolveProfileBody resolves a "@profile/" reference in a request body against profs: the
// profile is merged under the request's own keys, the reference is removed, and the model is
// the profile's model, else its first models[] entry (the rest stay in models), else left empty
// for the caller (the proxy's tuned band). A body naming no profile is returned unchanged with
// resolved=false. An unknown profile, two profiles, sugar on the reference or a merge conflict
// is a *RoutingRefusal.
func ResolveProfileBody(body []byte, profs *Profiles) (out []byte, resolved bool, err error) {
	// UseNumber: the request is re-encoded after the merge, and float64 would round its
	// large integers (a seed above 2^53).
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if !json.Valid(body) || dec.Decode(&m) != nil || m == nil {
		return body, false, nil
	}
	name, fromModel, err := profileRefOf(m)
	if err != nil {
		return nil, false, err
	}
	if name == "" {
		return body, false, nil
	}
	p, perr, ok := profs.Get(name)
	if perr != nil {
		return nil, false, &RoutingRefusal{Msg: perr.Error()}
	}
	if !ok {
		return nil, false, &RoutingRefusal{Msg: "unknown profile " + name}
	}
	req := map[string]any{}
	for k, v := range m {
		req[k] = v
	}
	if fromModel {
		delete(req, "model")
	}
	if r, isObj := req["roger"].(map[string]any); isObj {
		r2 := map[string]any{}
		for k, v := range r {
			if k != "profile" {
				r2[k] = v
			}
		}
		if len(r2) == 0 {
			delete(req, "roger")
		} else {
			req["roger"] = r2
		}
	}
	merged, merr := MergeProfile(p.Body, req)
	if merr != nil {
		return nil, false, &RoutingRefusal{Msg: "profile " + name + ": " + merr.Error()}
	}
	if _, has := merged["model"]; !has {
		if list, isArr := merged["models"].([]any); isArr && len(list) > 0 {
			merged["model"] = list[0]
			if len(list) == 1 {
				delete(merged, "models")
			} else {
				merged["models"] = list[1:]
			}
		} else {
			merged["model"] = ""
		}
	}
	enc, _ := json.Marshal(merged)
	return enc, true, nil
}

// ConfigPath is the config.json every first-party client reads (os.UserConfigDir()/rogerai).
func ConfigPath() string {
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "rogerai", "config.json")
}

// ProfileStore serves the profiles of one config.json, re-reading it only when its mtime or
// size changes, and keeping the last good set (with one warning naming config.json) when the
// file becomes unreadable.
type ProfileStore struct {
	path string

	mu      sync.Mutex
	mtime   time.Time
	size    int64
	last    *Profiles
	reads   int
	warned  bool
	statErr bool
}

// NewProfileStore watches path ("" = ConfigPath()).
func NewProfileStore(path string) *ProfileStore {
	if path == "" {
		path = ConfigPath()
	}
	return &ProfileStore{path: path}
}

// Reads is how many times the file was read (for the "re-read on change, not on every
// request" pin).
func (s *ProfileStore) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// Get returns the current profiles (never nil).
func (s *ProfileStore) Get() *Profiles {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Stat(s.path)
	if err != nil {
		if s.last == nil {
			s.last = &Profiles{ByName: map[string]*Profile{}, Errs: map[string]error{}}
		}
		return s.last
	}
	if s.last != nil && fi.ModTime().Equal(s.mtime) && fi.Size() == s.size {
		return s.last
	}
	s.mtime, s.size = fi.ModTime(), fi.Size()
	s.reads++
	doc, rerr := os.ReadFile(s.path)
	ps, perr := ParseProfiles(doc)
	if rerr != nil || perr != nil {
		if !s.warned {
			log.Printf("profiles: %s is unreadable - keeping the last good profiles", s.path)
			s.warned = true
		}
		if s.last == nil {
			s.last = &Profiles{ByName: map[string]*Profile{}, Errs: map[string]error{}}
		}
		return s.last
	}
	s.warned = false
	s.last = ps
	return ps
}

// dropGuestFreq removes a caller's own roger.freq (a guest cannot choose the band) when the
// body names a profile; any other body is returned unchanged.
func dropGuestFreq(body []byte) []byte {
	var m map[string]any
	if decodeNumbers(body, &m) != nil || m == nil {
		return body
	}
	if name, _, err := profileRefOf(m); err != nil || name == "" {
		return body
	}
	r, ok := m["roger"].(map[string]any)
	if !ok {
		return body
	}
	if _, has := r["freq"]; !has {
		return body
	}
	delete(r, "freq")
	if len(r) == 0 {
		delete(m, "roger")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// decodeNumbers decodes a JSON object keeping numbers as json.Number, so a re-encode never
// rounds a large integer.
func decodeNumbers(body []byte, m *map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	return dec.Decode(m)
}

// takeFreq removes roger.freq from a resolved body and returns it: a profile's band code is
// sent as the X-Roger-Freq header, never in the body.
func takeFreq(body []byte) ([]byte, string) {
	var m map[string]any
	if decodeNumbers(body, &m) != nil || m == nil {
		return body, ""
	}
	r, ok := m["roger"].(map[string]any)
	if !ok {
		return body, ""
	}
	f, _ := r["freq"].(string)
	if f == "" {
		return body, ""
	}
	delete(r, "freq")
	if len(r) == 0 {
		delete(m, "roger")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body, ""
	}
	return out, f
}
