package main

// modeldisk.go: what the owner typed, as a model already on this disk (short_names_nearby.feature,
// founder ruling 2026-10-11). Two quantisations of one repository are not a question: the
// quantisation rule picks the best fit here and --quant names another. A name that two different
// repositories share is the owner's pick at a terminal, and never roger's.

import (
	"errors"
	"fmt"
	"strings"

	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/hfhub"
	"rogerai.fm/roger/v7/internal/localmodel"
	"rogerai.fm/roger/v7/internal/provision"
)

// diskPick is the model on this disk a name resolved to. others are the other quantisations of its
// repository that are also here; byRule says the quantisation rule chose among them. fetch, when
// set, is the exact reference to fetch instead: a --quant this disk does not have, of a repository
// it does.
type diskPick struct {
	m      localmodel.Model
	others []localmodel.Model
	byRule bool
	fetch  string
}

// modelOnDisk resolves ref (and --quant) against this disk. A name that is not here returns an
// error wrapping localmodel.ErrUnknownModel, so the caller goes on to nearby and the hub.
func modelOnDisk(h localmodel.Home, ref, quant string, ctxLen int) (diskPick, error) {
	name := ref
	if quant == "" && !strings.Contains(ref, "/") {
		if n, q, ok := strings.Cut(ref, ":"); ok {
			name, quant = n, q
		}
	}
	m, err := h.Find(name)
	var amb *localmodel.Ambiguous
	switch {
	case err == nil:
		if quant == "" || strings.EqualFold(m.Quant, quant) {
			return diskPick{m: m}, nil
		}
		if m.Repo != "" {
			return diskPick{fetch: m.Repo + ":" + quant}, nil
		}
		return diskPick{}, fmt.Errorf("%w: %q at %s - only %s is here", localmodel.ErrUnknownModel, name, quant, m.ID)
	case !errors.As(err, &amb):
		return diskPick{}, err
	}
	if quant != "" {
		var at []localmodel.Model
		for _, x := range amb.Hits {
			if strings.EqualFold(x.Quant, quant) {
				at = append(at, x)
			}
		}
		if len(at) == 1 {
			return diskPick{m: at[0]}, nil
		}
		if len(at) == 0 && amb.SameRepo() {
			return diskPick{fetch: amb.Hits[0].Repo + ":" + quant}, nil
		}
		if len(at) > 1 {
			amb = &localmodel.Ambiguous{Query: amb.Query, Hits: at}
		}
	}
	if amb.SameRepo() {
		if i := diskRule(amb.Hits, ctxLen); i >= 0 {
			p := diskPick{m: amb.Hits[i], byRule: true}
			for j, x := range amb.Hits {
				if j != i {
					p.others = append(p.others, x)
				}
			}
			return p, nil
		}
		return diskPick{}, fmt.Errorf("%w; none fits this machine comfortably - name one with --quant", amb)
	}
	return diskPickOwner(amb)
}

// diskRule is the quantisation rule (localmodel.Suggest, the same one hub picks use) over files of
// one repository already on this disk, judged against this machine. -1 when nothing fits.
func diskRule(hits []localmodel.Model, ctxLen int) int {
	files := make([]hfhub.File, len(hits))
	for i, m := range hits {
		files[i] = hfhub.File{Path: m.File, Bytes: m.Bytes, Quant: m.Quant}
	}
	shape, _ := detect.ReadShape(hits[0].Path) // one architecture for every quantisation
	return localmodel.Suggest(localmodel.Rank(files, shape, modelBudget(), ctxLen))
}

// diskPickOwner has the owner pick between different repositories that share a name: numbered, at
// a terminal. A scripted run gets the list and picks nothing.
func diskPickOwner(amb *localmodel.Ambiguous) (diskPick, error) {
	if !edgeIsInteractive() {
		return diskPick{}, amb
	}
	fmt.Fprintf(modelOut, "%q is on this disk from %d repositories; roger does not pick between them:\n", amb.Query, len(amb.Hits))
	for i, m := range amb.Hits {
		fmt.Fprintf(modelOut, "  %d) %s  %s\n", i+1, m.ID, provision.BytesWord(m.Bytes))
	}
	fmt.Fprintf(modelOut, "pick 1-%d, or n: ", len(amb.Hits))
	i := modelReadPick(len(amb.Hits))
	if i < 0 {
		fmt.Fprintln(modelOut, "nothing picked. Nothing changed.")
		return diskPick{}, errModelDeclined
	}
	return diskPick{m: amb.Hits[i]}, nil
}

// diskOthers is the one line naming the other quantisations of the pick that are here too.
func diskOthers(p diskPick) string {
	if len(p.others) == 0 {
		return ""
	}
	qs := make([]string, len(p.others))
	for i, x := range p.others {
		qs[i] = modelDash(x.Quant)
	}
	return "also on disk: " + strings.Join(qs, ", ")
}

// diskUsing is what serving says when the quantisation rule chose among files on this disk.
func diskUsing(p diskPick) string {
	how := "pick it with --quant " + p.others[0].Quant
	if len(p.others) > 1 {
		how = "pick another with --quant <quant>"
	}
	return fmt.Sprintf("using %s (best fit here); %s; %s", p.m.ID, diskOthers(p), how)
}
