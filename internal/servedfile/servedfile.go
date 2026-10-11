// Package servedfile is which exact file a band is, as the source serving it says
// (features/models/short_names_nearby.feature). A source's report is its word: it is bounded and
// checked here before anyone may use it to pick a file, and a report that cannot name exactly one
// file on the model hub is dropped rather than half-trusted. It imports nothing of roger's, so the
// standalone Tower and the Edge fabric can carry it.
package servedfile

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

// File is one served file: the repository on the model hub, the file in it, its quantisation and
// the sha256 of its bytes.
type File struct {
	Repo   string `json:"repo"`
	File   string `json:"file"`
	Quant  string `json:"quant,omitempty"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes,omitempty"`
}

var (
	repoPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
	digest   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	maxFile  = 256
	maxQuant = 32
)

// Clean returns the report if it names exactly one file, or nil.
func Clean(f File) *File {
	owner, name, ok := strings.Cut(f.Repo, "/")
	if !ok || !repoPart.MatchString(owner) || !repoPart.MatchString(name) || strings.Contains(f.Repo, "..") {
		return nil
	}
	if f.File == "" || len(f.File) > maxFile || !strings.HasSuffix(strings.ToLower(f.File), ".gguf") ||
		strings.HasPrefix(f.File, "/") || path.Clean(f.File) != f.File || strings.Contains(f.File, "..") ||
		hasControl(f.File) {
		return nil
	}
	if len(f.Quant) > maxQuant || hasControl(f.Quant) || !digest.MatchString(f.SHA256) || f.Bytes < 0 {
		return nil
	}
	return &f
}

// FromID builds a report from a roger model id ("owner/repo:QUANT" or "owner/repo:file"), or nil
// when the id names no repository (a model the owner placed by hand).
func FromID(id, file, quant, sha256 string, bytes int64) *File {
	repo, _, _ := strings.Cut(id, ":")
	return Clean(File{Repo: repo, File: file, Quant: quant, SHA256: sha256, Bytes: bytes})
}

// Ref is how roger names the file on the model hub: repo:QUANT, or repo:file with no quant label.
func (f File) Ref() string {
	if f.Quant != "" {
		return f.Repo + ":" + f.Quant
	}
	return f.Repo + ":" + f.File
}

// Same reports whether two reports name the same bytes. Repository names on the hub are not case
// sensitive; the file and the digest are compared exactly.
func (f File) Same(o File) bool {
	return strings.EqualFold(f.Repo, o.Repo) && f.File == o.File && f.SHA256 == o.SHA256
}

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }
