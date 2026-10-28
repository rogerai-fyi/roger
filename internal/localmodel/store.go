// Package localmodel is roger serving a model by itself: the models on this disk, whether one
// will run here, fetching one, and what is being served right now.
//
// Every hard part lives elsewhere and is reused rather than repeated: the index is internal/hfhub,
// the arithmetic is internal/catalog, the download is internal/provision, the program is
// internal/runtime. This package is the ORDER those go in, and the record of what happened, so
// the CLI, the TUI and an Edge request all do exactly the same thing.
package localmodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/detect"
)

// Home is where all of it lives: <Dir>/models, <Dir>/runtime and the serving record.
type Home struct{ Dir string }

func (h Home) Models() string  { return filepath.Join(h.Dir, "models") }
func (h Home) Runtime() string { return filepath.Join(h.Dir, "runtime") }

// Model is one weight file on this disk and what is known about where it came from.
type Model struct {
	ID      string    `json:"id"`                // repo:QUANT when fetched, the file name when found
	Repo    string    `json:"repo,omitempty"`    // the repository it was fetched from
	File    string    `json:"file"`              // its path inside that repository
	Path    string    `json:"-"`                 // where it is on this disk
	Bytes   int64     `json:"bytes"`             //
	SHA256  string    `json:"sha256,omitempty"`  // checked on arrival; empty for a file roger found
	Digest  string    `json:"digest,omitempty"`  // what roger computed of a file placed by hand, after the fact; never a check
	Quant   string    `json:"quant,omitempty"`   //
	License string    `json:"license,omitempty"` // shown and recorded, never a gate (ruling 2026-09-30)
	Band    string    `json:"band"`              // the name it is served under
	Fetched time.Time `json:"fetched,omitempty"` //
	Served  time.Time `json:"served,omitempty"`  // last went on the air: what a budget's "least recently used" counts
	ModTime time.Time `json:"modtime,omitempty"` // the file's modification time when this record was written
}

// Verified reports whether roger checked this file's digest itself. A file dropped into the
// directory by hand is served on the owner's word, and the screens say so.
func (m Model) Verified() bool { return m.SHA256 != "" }

// Sum is the file's sha256 as best roger knows it: the check on arrival, else the digest it
// computed later, else "". It says what the bytes are; it does not say roger fetched them.
func (m Model) Sum() string {
	if m.SHA256 != "" {
		return m.SHA256
	}
	return m.Digest
}

// ErrUnknownModel is a name that matches nothing on this disk.
var ErrUnknownModel = errors.New("no model by that name on this machine")

const recordExt = ".roger.json"

// List is every servable model on this disk, fetched or dropped in by hand.
func (h Home) List() ([]Model, error) {
	root := h.Models()
	// A models directory that is itself a symlink (a shared cache linked in) is walked through its
	// target; paths are still reported under root, so records and budgets see one stable path.
	walk := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		walk = real
	}
	var out []Model
	err := filepath.WalkDir(walk, func(p string, d fs.DirEntry, err error) error {
		if rel, rerr := filepath.Rel(walk, p); rerr == nil {
			p = filepath.Join(root, rel)
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".gguf") {
			return nil
		}
		if why := UnservableAt(p); why != "" {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, recorded(found(p, fi.Size()), fi.ModTime()))
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

// recorded is the model as its record beside the file says, when there is one, else as found.
// The path is always where the file IS, never what the record says. A record is roger's own
// writing (a fetch, or a digest it computed) and is trusted only while it still describes the
// file on disk: the size it remembers and, when it kept one, the modification time. The bytes
// are not re-read. A different file copied in under the same name is a different file, and
// the old record's digest or check is not said of it.
func recorded(m Model, mtime time.Time) Model {
	if b, err := os.ReadFile(m.Path + recordExt); err == nil {
		var rec Model
		if json.Unmarshal(b, &rec) == nil && rec.ID != "" {
			if (rec.Bytes > 0 && rec.Bytes != m.Bytes) || (!rec.ModTime.IsZero() && !rec.ModTime.Equal(mtime)) {
				return m
			}
			rec.Path = m.Path
			return rec
		}
	}
	return m
}

// found describes a file nobody recorded: what its name says, and nothing it does not.
func found(p string, size int64) Model {
	name := filepath.Base(p)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	return Model{ID: stem, File: name, Path: p, Bytes: size, Quant: detect.QuantInName(name), Band: BandFor(stem)}
}

// Find resolves what an owner typed: an exact id first, then a band, file or repo name when that
// names exactly one model. Ambiguity is an error that lists the choices, never a silent pick.
func (h Home) Find(q string) (Model, error) {
	all, err := h.List()
	if err != nil {
		return Model{}, err
	}
	for _, m := range all {
		if m.ID == q {
			return m, nil
		}
	}
	lq := strings.ToLower(q)
	var hits []Model
	for _, m := range all {
		stem := strings.TrimSuffix(m.File, filepath.Ext(m.File))
		for _, c := range []string{m.ID, m.Band, m.Repo, filepath.Base(stem)} {
			if c != "" && strings.ToLower(c) == lq {
				hits = append(hits, m)
				break
			}
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Model{}, fmt.Errorf("%w: %q - `roger model list` shows what is here", ErrUnknownModel, q)
	}
	return Model{}, &Ambiguous{Query: q, Hits: hits}
}

// Ambiguous is a name that matches more than one model on this disk. Find never picks; a caller
// that knows a rule for the case (two quantisations of one repository) can.
type Ambiguous struct {
	Query string
	Hits  []Model
}

func (e *Ambiguous) Error() string {
	ids := make([]string, len(e.Hits))
	for i, m := range e.Hits {
		ids[i] = m.ID
	}
	return fmt.Sprintf("%q matches %d models, name one: %s", e.Query, len(e.Hits), strings.Join(ids, ", "))
}

// SameRepo reports whether every match is a quantisation of one recorded repository.
func (e *Ambiguous) SameRepo() bool {
	for _, m := range e.Hits {
		if m.Repo == "" || !strings.EqualFold(m.Repo, e.Hits[0].Repo) {
			return false
		}
	}
	return len(e.Hits) > 0
}

// Usage is how much disk the models take, which an owner of a board's SD card needs to see.
func (h Home) Usage() (int64, int, error) {
	all, err := h.List()
	var n int64
	for _, m := range all {
		n += m.Bytes
	}
	return n, len(all), err
}

// Remove deletes a model from disk. It returns to being merely something that could be fetched.
func (h Home) Remove(m Model) error {
	if m.Path == "" || !within(h.Models(), m.Path) {
		return fmt.Errorf("%s is not in roger's models directory, so roger does not delete it", m.ID)
	}
	if err := os.Remove(m.Path); err != nil {
		return err
	}
	_ = os.Remove(m.Path + recordExt)
	_ = os.Remove(m.Path + ".part")
	// Tidy the repo directories it leaves empty; Remove fails harmlessly on one that is not.
	for d := filepath.Dir(m.Path); within(h.Models(), d) && d != h.Models(); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}

func (h Home) record(m Model) error {
	if fi, err := os.Stat(m.Path); err == nil {
		m.ModTime = fi.ModTime()
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.Path+recordExt, b, 0o644)
}

// PathFor is where a repository's file lands. Both halves arrived from outside, so both are
// checked: a name that climbs out of the models directory is refused, not cleaned up.
func (h Home) PathFor(repo, file string) (string, error) {
	if !repoRE.MatchString(repo) {
		return "", fmt.Errorf("%q is not a repository name (owner/name)", repo)
	}
	p := filepath.Join(h.Models(), filepath.FromSlash(repo), filepath.FromSlash(file))
	if strings.Contains(file, "..") || filepath.IsAbs(file) || !within(h.Models(), p) {
		return "", fmt.Errorf("%q is not a file name roger will write", file)
	}
	return p, nil
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// BandFor is the name a model is served under: the repository's own name, lower case, without
// the packaging suffix every GGUF repo carries. The quantisation stays visible behind it on every
// screen rather than inside it, so two quants of one model route as one band.
func BandFor(name string) string {
	n := strings.ToLower(filepath.Base(name))
	n = strings.TrimSuffix(n, "-gguf")
	n = strings.TrimSuffix(n, ".gguf")
	if q := strings.ToLower(detect.QuantInName(n)); q != "" {
		n = strings.TrimSuffix(strings.TrimSuffix(n, q), "-")
		n = strings.TrimSuffix(n, ".")
	}
	return n
}

// UnservableAt is Unservable for a file on this disk: its name first, then what its own header
// says it is. A LoRA adapter or a projector is named like a model more often than not (the Wave
// fire-decision adapter is "fire-decision-adapter-f16.gguf"), and offering one as a model row
// ends in a runtime that refuses it after the owner said yes.
func UnservableAt(path string) string {
	if why := Unservable(path); why != "" {
		return why
	}
	shape, _ := detect.ReadShape(path) // Kind is carried even when the shape is incomplete
	switch shape.Kind {
	case "adapter":
		why := "a LoRA adapter, which is served beside its base model rather than on its own"
		if shape.BaseModel != "" {
			why = "a LoRA adapter for " + shape.BaseModel + ", which is served beside that base model rather than on its own"
		}
		return why
	case "mmproj":
		return "a vision projector, which goes beside a model rather than being one"
	}
	return ""
}

var splitRE = regexp.MustCompile(`(?i)-\d{5}-of-\d{5}\.gguf$`)

// Unservable is why a file cannot be served ON ITS OWN, or "" when it can. These are files every
// GGUF repo carries beside the weights, and offering a fetch of one would end in a runtime that
// refuses it.
func Unservable(file string) string {
	base := strings.ToLower(filepath.Base(file))
	switch {
	case strings.HasPrefix(base, "mmproj"):
		return "a vision projector, which goes beside a model rather than being one"
	case splitRE.MatchString(base):
		return "one part of a model split across several files, which this does not fetch yet"
	case strings.Contains(base, "imatrix"):
		return "calibration data, not weights"
	}
	return ""
}

// FromPath is a model file named by its path, wherever it is: the owner who already downloaded
// something can serve it without moving it. It is served on their word, unverified.
func FromPath(p string) (Model, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return Model{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return Model{}, fmt.Errorf("no model file at %s", p)
	}
	if why := UnservableAt(abs); why != "" {
		return Model{}, fmt.Errorf("%s is %s", filepath.Base(abs), why)
	}
	return recorded(found(abs, fi.Size()), fi.ModTime()), nil
}
