package localmodel

// FETCHING one file of one repository, verified, and recorded only once it is.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/hfhub"
	"rogerai.fm/roger/v7/internal/provision"
)

// Pick finds the file an owner named in a repository's list: a quantisation ("Q4_K_M") or a file
// path. Nothing is guessed: a name that matches nothing, or several, is an error with the choices.
func Pick(files []hfhub.File, want string) (hfhub.File, error) {
	var hits []hfhub.File
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f.Path), ".gguf")
		if strings.EqualFold(f.Path, want) || strings.EqualFold(f.Quant, want) || strings.EqualFold(stem, want) {
			if Unservable(f.Path) == "" {
				hits = append(hits, f)
			}
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	var names []string
	for _, f := range files {
		if Unservable(f.Path) == "" {
			names = append(names, f.Path)
		}
	}
	if len(hits) == 0 {
		return hfhub.File{}, fmt.Errorf("no servable file %q here; there is: %s", want, strings.Join(names, ", "))
	}
	return hfhub.File{}, fmt.Errorf("%q names %d files, name one: %s", want, len(hits), strings.Join(names, ", "))
}

// Get fetches one file into the models directory and records where it came from. The record is
// written only after the digest checked, so a model on this disk with a record is one roger
// verified, and a partial download is never listed as a model.
func (h Home) Get(ctx context.Context, hub *hfhub.Client, f *provision.Fetcher, repo string, file hfhub.File, license string) (Model, error) {
	if why := Unservable(file.Path); why != "" {
		return Model{}, fmt.Errorf("%s is %s", file.Path, why)
	}
	dest, err := h.PathFor(repo, file.Path)
	if err != nil {
		return Model{}, err
	}
	// The owner's budget, if they set one: room is made before a byte moves, and never without one.
	// Before the directory is made, because removing a repo's last file tidies its directory away.
	if _, err := h.MakeRoom(file.Bytes); err != nil {
		return Model{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Model{}, err
	}
	err = f.Fetch(ctx, provision.Request{
		URL: hub.DownloadURL(repo, file.Path), Dest: dest, Bytes: file.Bytes, SHA256: file.SHA256,
		Header: hub.AuthHeader(),
	})
	if err != nil {
		return Model{}, err
	}
	id := repo + ":" + file.Quant
	if file.Quant == "" {
		id = repo + ":" + file.Path
	}
	// Two files of one quantisation from one repo must not share an id: the second is named by its path.
	if all, err := h.List(); err == nil {
		for _, m := range all {
			if m.ID == id && m.Path != dest {
				id = repo + ":" + file.Path
				break
			}
		}
	}
	m := Model{ID: id, Repo: repo, File: file.Path, Path: dest, Bytes: file.Bytes, SHA256: file.SHA256,
		Quant: file.Quant, License: license, Band: BandFor(repo), Fetched: time.Now().UTC()}
	if err := h.record(m); err != nil {
		return Model{}, err
	}
	return m, nil
}
