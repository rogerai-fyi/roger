// Package catalog describes the models RogerAI can OFFER to put on air, as
// distinct from the models internal/detect finds already running.
//
// SHARE has only ever been able to list a model that is already being served by
// a local OpenAI-compatible endpoint. Putting a RogerAI model on air therefore
// meant leaving the tool: find the repo, download weights, install a runtime,
// work out the serve flags, come back. Every step is a place to give up.
//
// This package is the data half of closing that gap: the manifest of offerable
// artifacts, the honesty rules an entry must satisfy before it can be shown, and
// the merge that presents offerable and detected models as one list. It performs
// NO I/O - no fetching, no downloading, no process launching - so the rules stay
// cheap to test and impossible to get wrong by accident. Acquisition and serving
// belong to internal/provision and internal/runtime.
//
// Contract: features/share/model_catalog.feature.
package catalog

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Entry is one artifact RogerAI publishes and can offer to acquire and serve.
//
// Every field an operator needs in order to CONSENT is mandatory: where the bytes
// come from, how many there are, what it will cost in memory, and under what
// licence. An entry missing any of those cannot be offered, because offering it
// would be asking someone to accept an unknown.
type Entry struct {
	ID       string // model id as it will appear on air
	Repo     string // absolute URL of the publishing repository
	File     string // the artifact file within that repository
	Bytes    int64  // download size; what consent is given against
	SHA256   string // digest of the artifact file, hex
	ServeMem int64  // memory needed to serve it
	License  string // the artifact's licence
	Runtime  string // runtime that serves it, e.g. "llama.cpp"
	Parent   string // upstream lineage; empty when RogerAI is the origin
}

// Validate reports why an entry may not be offered, naming the field at fault.
func (e Entry) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("catalog: entry has no id")
	}
	fail := func(format string, a ...any) error {
		return fmt.Errorf("catalog: entry %q "+format, append([]any{e.ID}, a...)...)
	}
	if strings.TrimSpace(e.Repo) == "" {
		return fail("has no repo")
	}
	// An operator must be able to see WHERE bytes come from before consenting, so
	// a bare "owner/name" shorthand is not enough - it names no host.
	if u, err := url.Parse(e.Repo); err != nil || u.Scheme == "" || u.Host == "" {
		return fail("repo is not an absolute URL: %q", e.Repo)
	}
	if strings.TrimSpace(e.File) == "" {
		return fail("has no file")
	}
	if e.Bytes <= 0 {
		return fail("has no download size in bytes")
	}
	if e.ServeMem <= 0 {
		return fail("has no serving memory requirement")
	}
	if !isSHA256(e.SHA256) {
		return fail("has no usable sha256 digest: %q", e.SHA256)
	}
	if strings.TrimSpace(e.License) == "" {
		return fail("has no license")
	}
	if strings.TrimSpace(e.Runtime) == "" {
		return fail("has no runtime")
	}
	return nil
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// Manifest is a validated, de-duplicated, stably ordered set of entries.
type Manifest struct {
	Entries []Entry
}

// NewManifest validates every entry and rejects the whole manifest if any one of
// them fails. A partially-valid catalogue is worse than none: it would silently
// drop a model an operator went looking for.
func NewManifest(entries []Entry) (Manifest, error) {
	seen := make(map[string]bool, len(entries))
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return Manifest{}, err
		}
		if seen[e.ID] {
			return Manifest{}, fmt.Errorf("catalog: duplicate entry id %q", e.ID)
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return Manifest{Entries: out}, nil
}

// Fit is how an offerable model relates to the memory actually detected here.
type Fit uint8

const (
	FitUnknown Fit = iota // memory or requirement not known - never claim a fit
	FitFits
	FitTight
	FitWontFit
)

func (f Fit) String() string {
	switch f {
	case FitFits:
		return "fits"
	case FitTight:
		return "tight"
	case FitWontFit:
		return "will not fit"
	default:
		return "unknown"
	}
}

// tightFraction is where "fits" becomes "tight": needing most of what exists
// leaves nothing for the OS or anything else the operator is running.
const tightFraction = 0.8

// AssessFit compares a requirement against REAL detected memory.
//
// Unknown inputs report FitUnknown rather than optimism - telling an operator a
// model fits when we cannot know invites them to download gigabytes onto a
// machine that cannot serve them.
func AssessFit(availableBytes, needBytes int64) Fit {
	if availableBytes <= 0 || needBytes <= 0 {
		return FitUnknown
	}
	if needBytes > availableBytes {
		return FitWontFit
	}
	if float64(needBytes) > float64(availableBytes)*tightFraction {
		return FitTight
	}
	return FitFits
}

// State is where a model stands on the SHARE list.
type State uint8

const (
	StateOffered  State = iota // published by RogerAI, not on this machine yet
	StateDetected              // already served by a local endpoint
)

// Shareable is one row of the SHARE list, from either source.
type Shareable struct {
	ID    string
	State State
	Entry *Entry // catalogue facts when RogerAI publishes it; nil otherwise
}

// ReadyToBroadcast reports whether this row can go on air as it stands. An
// offered model cannot: it does not exist locally, so there is nothing to serve.
func (s Shareable) ReadyToBroadcast() bool { return s.State == StateDetected }

// Merge presents detected and offerable models as ONE list.
//
// A catalogue model that is already running is DETECTED, not offered - there is
// nothing to acquire - but it keeps its catalogue facts so the row can still show
// licence and lineage. Detected models sort ahead of offered ones: a model that is
// ready now outranks one that needs gigabytes downloading first.
func Merge(detected []string, entries []Entry) []Shareable {
	byID := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}

	seen := make(map[string]bool, len(detected))
	ready := make([]Shareable, 0, len(detected))
	for _, name := range detected {
		id := strings.TrimSpace(name)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		row := Shareable{ID: id, State: StateDetected}
		if e, ok := byID[id]; ok {
			row.Entry = &e
		}
		ready = append(ready, row)
	}

	offered := make([]Shareable, 0, len(entries))
	for _, e := range entries {
		if seen[e.ID] {
			continue
		}
		offered = append(offered, Shareable{ID: e.ID, State: StateOffered, Entry: &e})
	}

	sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
	sort.Slice(offered, func(i, j int) bool { return offered[i].ID < offered[j].ID })
	return append(ready, offered...)
}
