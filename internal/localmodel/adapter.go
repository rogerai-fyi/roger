package localmodel

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"rogerai.fm/roger/v6/internal/detect"
)

// Adapter is a LoRA the runtime serves beside a base model (`llama-server --lora`). It is never
// a model row: edge_device_runtime.feature §2. What is known of it is what its own header says.
type Adapter struct {
	Path  string // where it is on this disk
	File  string // its file name
	Bytes int64
	Arch  string // general.architecture: the base family it was trained for
	Base  string // general.base_model.0.name, when the header records it
}

// Name is the adapter's short name: the file's stem without its precision suffix, the way a
// model's band is spelled from its file.
func (a Adapter) Name() string { return BandFor(strings.TrimSuffix(a.File, filepath.Ext(a.File))) }

// AdapterAt is the adapter at a path, refused when the header does not say it is one: a base
// model or a projector handed to --lora would be taken by the runtime and answer nonsense.
func AdapterAt(p string) (Adapter, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return Adapter{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return Adapter{}, fmt.Errorf("no adapter file at %s", p)
	}
	shape, _ := detect.ReadShape(abs)
	if shape.Kind != "adapter" {
		says := "its header does not say what it is"
		if shape.Kind != "" {
			says = "its header says it is a " + shape.Kind
		}
		return Adapter{}, fmt.Errorf("%s is not a LoRA adapter: %s", filepath.Base(abs), says)
	}
	if shape.AdapterType != "lora" {
		says := "its header does not say what kind of adapter it is"
		if shape.AdapterType != "" {
			says = "its header says it is a " + shape.AdapterType + " adapter, which --lora cannot take"
		}
		return Adapter{}, fmt.Errorf("%s is not a LoRA adapter: %s", filepath.Base(abs), says)
	}
	return Adapter{Path: abs, File: filepath.Base(abs), Bytes: fi.Size(), Arch: shape.Arch, Base: shape.BaseModel}, nil
}

// ErrUnknownAdapter is a name that matches no adapter file on this disk.
var ErrUnknownAdapter = errors.New("no adapter by that name on this machine")

// FindAdapter is the one adapter file in the models directory whose name (or short name) is q.
// Two that match is an error naming both, never a silent pick.
func (h Home) FindAdapter(q string) (Adapter, error) {
	q = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(q)), ".gguf")
	var hits []Adapter
	root := h.Models()
	walk := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		walk = real
	}
	_ = filepath.WalkDir(walk, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".gguf") {
			return nil
		}
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		if stem != q && BandFor(stem) != q {
			return nil
		}
		if a, err := AdapterAt(p); err == nil {
			hits = append(hits, a)
		}
		return nil
	})
	switch len(hits) {
	case 0:
		return Adapter{}, ErrUnknownAdapter
	case 1:
		return hits[0], nil
	}
	names := make([]string, 0, len(hits))
	for _, a := range hits {
		names = append(names, a.Path)
	}
	return Adapter{}, fmt.Errorf("%q names %d adapter files, give the path: %s", q, len(hits), strings.Join(names, ", "))
}
