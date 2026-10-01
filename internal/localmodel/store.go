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

	"rogerai.fm/roger/v6/internal/detect"
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
	Quant   string    `json:"quant,omitempty"`   //
	License string    `json:"license,omitempty"` // shown and recorded, never a gate (ruling 2026-09-30)
	Band    string    `json:"band"`              // the name it is served under
	Fetched time.Time `json:"fetched,omitempty"` //
}

// Verified reports whether roger checked this file's digest itself. A file dropped into the
// directory by hand is served on the owner's word, and the screens say so.
func (m Model) Verified() bool { return m.SHA256 != "" }

// ErrUnknownModel is a name that matches nothing on this disk.
var ErrUnknownModel = errors.New("no model by that name on this machine")

const recordExt = ".roger.json"

// List is every servable model on this disk, fetched or dropped in by hand.
func (h Home) List() ([]Model, error) {
	root := h.Models()
	var out []Model
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".gguf") {
			return nil
		}
		if why := Unservable(filepath.Base(p)); why != "" {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		m := found(p, fi.Size())
		if b, err := os.ReadFile(p + recordExt); err == nil {
			var rec Model
			if json.Unmarshal(b, &rec) == nil && rec.ID != "" {
				rec.Path = p
				m = rec
			}
		}
		out = append(out, m)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
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
	ids := make([]string, len(hits))
	for i, m := range hits {
		ids[i] = m.ID
	}
	return Model{}, fmt.Errorf("%q matches %d models, name one: %s", q, len(hits), strings.Join(ids, ", "))
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
