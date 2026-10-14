package main

// SHORT NAMES COME FROM WHAT IS SERVED NEARBY, AND FALL BACK TO THE MODEL HUB
// (features/models/short_names_nearby.feature, founder rulings 2026-10-11).
//
// A name the owner types that is not a path, not a hub reference and not on this disk is looked up
// in what is served nearby: this machine's own serves, the Edge's members and the pinned Towers'
// stations. Each of those may say which exact file a band is (internal/servedfile), and that is the
// file roger offers, from the model hub, checked against the digest the source reported. A market
// band is a name only: it never chooses a file. A name served nowhere nearby is looked up on the hub
// and the owner picks. Nothing is fetched before the owner says yes to exactly one file.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/catalog"
	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/hfhub"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/modelview"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/servedfile"
)

// nearbyMap is one file a name is served as, and every nearby source that says so.
type nearbyMap struct {
	File servedfile.File
	By   []string // whose word, e.g. "your Edge member atlas"
}

func (m nearbyMap) by() string { return strings.Join(m.By, " and ") }

// nearbyName is one name served nearby: the files it is served as, the sources serving it that do
// not say which file (an older peer), and whether the market also has it (a name only).
type nearbyName struct {
	Name   string
	Maps   []nearbyMap
	Plain  []string
	Market bool
}

// nearbyMarketWait bounds the market read: a slow broker never holds up a name served nearby.
var nearbyMarketWait = 4 * time.Second

// modelNearby reads every nearby source, by name. The sources are the dial's own (lanDial): the
// trust in each is decided there, and only a source that may define a mapping carries a file.
func modelNearby(cfg config) []nearbyName {
	byName := map[string]*nearbyName{}
	var order []string
	get := func(name string) *nearbyName {
		k := strings.ToLower(name)
		n := byName[k]
		if n == nil {
			n = &nearbyName{Name: name}
			byName[k] = n
			order = append(order, k)
		}
		return n
	}
	add := func(name, by string, f *servedfile.File) {
		if name == "" {
			return
		}
		n := get(name)
		if f == nil {
			if !contains(n.Plain, by) {
				n.Plain = append(n.Plain, by)
			}
			return
		}
		for i := range n.Maps {
			if n.Maps[i].File.Same(*f) {
				if !contains(n.Maps[i].By, by) {
					n.Maps[i].By = append(n.Maps[i].By, by)
				}
				return
			}
		}
		n.Maps = append(n.Maps, nearbyMap{File: *f, By: []string{by}})
	}
	h := modelHome()
	for _, s := range h.LiveAll() {
		add(s.Band, "this machine's own serve", servedfile.FromID(s.ID, s.File, s.Quant, s.SHA256, modelBytesOf(h, s)))
	}
	if bands, err := lanEdgeBands(); err == nil {
		for _, b := range bands {
			if b.Verified && !b.Dark { // a band a member only claims is not served
				add(b.Band, "your Edge member "+b.Node, b.File)
			}
		}
	}
	if towers := lanTowerAddrs(cfg); len(towers) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		bands, _ := client.LANDiscover(ctx, client.LANInputs{Towers: towers, HTTP: lanProbeHTTP, Resolve: lanResolve})
		cancel()
		for _, b := range bands {
			for _, src := range b.Sources {
				// Only a Tower read over its pin is nearby: an unpinned one is plain http on the LAN,
				// and one whose key changed was refused before it said anything.
				if src.Kind == client.LANTower && src.Verified {
					add(b.Model, "a station on the Tower "+nearbyHost(src.ID)+" (its word)", src.File)
				}
			}
		}
	}
	for _, name := range modelMarketNames(cfg) {
		get(name).Market = true
	}
	out := make([]nearbyName, 0, len(order))
	for _, k := range order {
		out = append(out, *byName[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func nearbyHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// modelBytesOf is the size of the file a serve record names, from the record of it on this disk.
func modelBytesOf(h localmodel.Home, s localmodel.Serving) int64 {
	all, _ := h.List()
	for _, m := range all {
		if m.ID == s.ID && m.File == s.File {
			return m.Bytes
		}
	}
	return 0
}

// modelMarketNames is the bands the market lists: names only, for discovery. LAN mode never asks
// the market, and neither does a configuration without one.
func modelMarketNames(cfg config) []string {
	if cfg.lan.LAN || cfg.Broker == "" {
		return nil
	}
	got := make(chan []client.Offer, 1)
	go func() {
		offers, _ := client.Discover(cfg.Broker)
		got <- offers
	}()
	var offers []client.Offer
	select {
	case offers = <-got:
	case <-time.After(nearbyMarketWait):
	}
	var out []string
	for _, o := range offers {
		if !o.Online {
			continue
		}
		if name := strings.TrimSpace(o.Model); name != "" && !contains(out, name) {
			out = append(out, name) // its File, if it sent one, is a stranger's claim: never read
		}
	}
	return out
}

// nearbyFind is the name served nearby, if it is.
func nearbyFind(names []nearbyName, name string) (nearbyName, bool) {
	for _, n := range names {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return nearbyName{}, false
}

// errNearbyChanged is a mapping that moved between the owner's yes and the fetch.
var errNearbyChanged = errors.New("the mapping changed")

// nearbyResolved is one nearby name settled to one file of the model hub: the ranked choice, the
// digest it is checked against, and whose word that digest is.
type nearbyResolved struct {
	name   string
	mp     nearbyMap
	c      localmodel.Choice
	b      localmodel.Budget
	listed bool // the file is not the one the source serves, so its digest is the hub listing's
}

func (r nearbyResolved) ref() string { return r.mp.File.Repo + ":" + modelDash(r.c.File.Quant) }

// ask is the consent question, word for word on every surface.
func (r nearbyResolved) ask() string {
	return fmt.Sprintf("%s; fetch %s?", r.line(), provision.BytesWord(r.c.File.Bytes))
}

// mapLine is the source's mapping, as the source says it.
func (r nearbyResolved) mapLine() string {
	return fmt.Sprintf("%s = %s as served by %s", r.name, r.mp.File.Ref(), r.mp.by())
}

// line is the mapping as the owner reads it before saying yes.
func (r nearbyResolved) line() string {
	if r.listed {
		return fmt.Sprintf("%s = %s from the model hub's listing", r.name, r.ref())
	}
	return fmt.Sprintf("%s = %s as served by %s", r.name, r.ref(), r.mp.by())
}

// nearbyPickMap chooses one of a name's mappings: the only one, or the owner's pick by number. Two
// sources that disagree are never resolved for the owner, and never without a terminal.
func nearbyPickMap(ctx context.Context, n nearbyName, ctxLen int) (nearbyMap, error) {
	if len(n.Maps) == 1 {
		return n.Maps[0], nil
	}
	fmt.Fprintf(modelOut, "%s is served nearby as %d different files:\n", n.Name, len(n.Maps))
	for i, mp := range n.Maps {
		size, verdict := "", ""
		if cs, _, err := modelRank(ctx, mp.File.Repo, ctxLen); err == nil {
			for _, c := range cs {
				if c.File.Path == mp.File.File {
					size, verdict = provision.BytesWord(c.File.Bytes), c.Verdict.Short()
				}
			}
		}
		fmt.Fprintf(modelOut, "  %d) %s  %s  %s  - as served by %s\n", i+1, mp.File.Ref(), modelDash(size), modelDash(verdict), mp.by())
	}
	if !edgeIsInteractive() {
		return nearbyMap{}, fmt.Errorf("%s is served nearby as %d different files and there is no terminal to choose on, so nothing was fetched; name one exactly: roger model serve <owner/repo>:<quant>", n.Name, len(n.Maps))
	}
	fmt.Fprintf(modelOut, "pick 1-%d, or n: ", len(n.Maps))
	i := modelReadPick(len(n.Maps))
	if i < 0 {
		fmt.Fprintln(modelOut, "not fetched. Nothing changed.")
		return nearbyMap{}, errModelDeclined
	}
	return n.Maps[i], nil
}

// nearbyResolve settles a nearby name to one file: the source's own, or the quantisation the owner
// named, or, when the source's will not fit here, the one the existing rule picks.
func nearbyResolve(ctx context.Context, n nearbyName, quant string, ctxLen int) (nearbyResolved, error) {
	mp, err := nearbyPickMap(ctx, n, ctxLen)
	if err != nil {
		return nearbyResolved{}, err
	}
	r := nearbyResolved{name: n.Name, mp: mp}
	cs, b, err := modelRank(ctx, mp.File.Repo, ctxLen)
	if err != nil {
		return r, fmt.Errorf("%s is %s as served by %s, and the file must come from the model hub, but %s: %w", n.Name, mp.File.Ref(), mp.by(), hubTrouble(err), err)
	}
	r.b = b
	if quant != "" {
		if r.c, err = modelPickQuant(cs, quant); err != nil {
			return r, err
		}
		r.listed = r.c.File.Path != mp.File.File
	} else {
		for _, c := range cs {
			if c.File.Path == mp.File.File {
				r.c = c
			}
		}
		if r.c.File.Path == "" {
			return r, fmt.Errorf("%s says %s is %s, which the model hub does not list in %s", mp.by(), n.Name, mp.File.File, mp.File.Repo)
		}
		if r.c.Verdict.Fit == catalog.FitWontFit {
			fmt.Fprintf(modelOut, "%s's %s will not fit here: %s\n", mp.by(), modelDash(r.c.File.Quant), r.c.Verdict.Line())
			if s := r.c.Verdict.Sum(); s != "" {
				fmt.Fprintf(modelOut, "  %s\n", s)
			}
			i := localmodel.Suggest(cs)
			if i < 0 {
				return r, fmt.Errorf("nothing in %s fits this machine - `roger model files %s` shows each", mp.File.Repo, mp.File.Repo)
			}
			r.c, r.listed = cs[i], true
			fmt.Fprintf(modelOut, "offering %s of the same repository: that file is not the one %s serves, so its digest comes from the model hub's listing\n",
				modelDash(r.c.File.Quant), mp.by())
		}
	}
	if !r.listed {
		if r.c.File.SHA256 != mp.File.SHA256 {
			return r, fmt.Errorf("%s reported sha256 %s for %s, but the model hub lists %s: not fetched, and never served",
				mp.by(), mp.File.SHA256, mp.File.File, modelDash(r.c.File.SHA256))
		}
	}
	return r, nil
}

// modelFromNearby fetches the file a nearby name maps to, after the owner says yes to exactly it. A
// mapping that changes between the yes and the fetch is shown and asked again.
func modelFromNearby(ctx context.Context, cfg config, n nearbyName, quant string, ctxLen int, yes bool) (localmodel.Model, error) {
	for range 3 {
		r, err := nearbyResolve(ctx, n, quant, ctxLen)
		if err != nil {
			return localmodel.Model{}, err
		}
		if m, ok := modelOnDiskBy(r.c.File.SHA256); ok {
			fmt.Fprintf(modelOut, "%s; it is already on this disk under the same digest, so nothing is fetched\n", r.line())
			return m, nil
		}
		source := "the bytes come from the model hub, checked against the digest " + r.mp.by() + " reported"
		whose := "the author's word, from the model hub; checked against the digest " + r.mp.by() + " reported"
		if r.listed {
			source = "source: the model hub - its author's word, checked against the digest the listing gave"
			whose = "the author's word, from the model hub"
		}
		var now nearbyName
		m, err := modelFetchChoice(ctx, r.mp.File.Repo, r.c, r.b, modelFetchAsk{
			ask:    r.ask() + " [y/N]: ",
			source: source,
			whose:  whose,
			before: func() error {
				var ok bool
				if now, ok = nearbyFind(modelNearby(cfg), n.Name); !ok || len(now.Maps) == 0 {
					return fmt.Errorf("%s is no longer served nearby; nothing was fetched", n.Name)
				}
				for _, mp := range now.Maps {
					if mp.File.Same(r.mp.File) {
						return nil
					}
				}
				return errNearbyChanged
			},
		}, yes)
		if errors.Is(err, errNearbyChanged) {
			fmt.Fprintf(modelOut, "the file %s maps to changed since you said yes: now %s as served by %s; asking again\n",
				n.Name, now.Maps[0].File.Ref(), now.Maps[0].by())
			n = now
			continue
		}
		if errors.Is(err, provision.ErrCorrupt) && !r.listed {
			return m, fmt.Errorf("%w; it was checked against sha256 %s as %s reported, and nothing was served", err, provision.ShortDigest(r.mp.File.SHA256), r.mp.by())
		}
		return m, err
	}
	return localmodel.Model{}, fmt.Errorf("the file %s maps to keeps changing; nothing was fetched", n.Name)
}

// modelOnDiskBy is a model on this disk whose verified digest is sha.
func modelOnDiskBy(sha string) (localmodel.Model, bool) {
	all, _ := modelHome().List()
	for _, m := range all {
		if sha != "" && m.SHA256 == sha {
			return m, true
		}
	}
	return localmodel.Model{}, false
}

// nearbyNotes says why a name with no usable mapping goes to the hub: the market never chooses a
// file, and an older source does not say which file it serves.
func nearbyNotes(n nearbyName) {
	if n.Market {
		fmt.Fprintf(modelOut, "%s is on the market, but a market station never chooses a file, so roger looks the name up instead\n", n.Name)
	}
	if len(n.Plain) > 0 {
		fmt.Fprintf(modelOut, "%s is served by %s, which does not say which file it is\n", n.Name, strings.Join(n.Plain, " and "))
	}
}

// nearbyAlso is the note on a model the owner placed by hand: their file keeps its name, and a
// nearby source serving that name is said, with how to serve that one instead.
func nearbyAlso(cfg config, m localmodel.Model, name string) {
	n, ok := nearbyFind(modelNearby(cfg), name)
	if !ok {
		return
	}
	for _, mp := range n.Maps {
		fmt.Fprintf(modelOut, "a nearby source also serves a %q: %s as served by %s. roger model serve %s serves that one instead\n",
			name, mp.File.Ref(), mp.by(), mp.File.Ref())
	}
}

// nearbyRepo is the repository a nearby name is served from, for the commands that look at a
// repository; anything else is passed through as typed.
func nearbyRepo(ctx context.Context, cfg config, ref string, a modelArgv) (string, error) {
	if strings.Contains(ref, "/") || a.hub {
		return ref, nil
	}
	n, ok := nearbyFind(modelNearby(cfg), ref)
	if !ok || len(n.Maps) == 0 {
		return ref, nil
	}
	mp, err := nearbyPickMap(ctx, n, a.ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(modelOut, "%s is %s as served by %s\n\n", n.Name, mp.File.Ref(), mp.by())
	return mp.File.Repo, nil
}

// nearbyForMember settles a nearby name for `--on <node>`: the member is told the exact file and its
// digest, never the name, and only after the owner says yes to it here. ok is false for anything
// that is not a nearby name, which goes on as typed (an id on that machine).
func nearbyForMember(cfg config, a modelArgv) (nearbyResolved, bool, error) {
	ref := a.pos[0]
	if strings.Contains(ref, "/") || a.hub {
		return nearbyResolved{}, false, nil
	}
	name, quant, _ := strings.Cut(ref, ":")
	n, ok := nearbyFind(modelNearby(cfg), name)
	if !ok || len(n.Maps) == 0 {
		return nearbyResolved{}, false, nil
	}
	ctx := context.Background()
	r, err := nearbyResolve(ctx, n, quant, a.ctx)
	if err != nil {
		return r, false, err
	}
	if !a.yes {
		fmt.Fprintf(modelOut, "%s; tell %s to serve exactly that file? [y/N]: ", r.line(), a.on)
		if !edgeConfirmed() {
			fmt.Fprintln(modelOut, "not sent. Nothing changed.")
			return r, false, errModelDeclined
		}
	}
	return r, true, nil
}

// modelNearbyList is `roger model nearby`: every name served nearby, its file and whose word it is.
func modelNearbyList(cfg config) error {
	names := modelNearby(cfg)
	if len(names) == 0 {
		fmt.Fprintln(modelOut, "nothing is served nearby (on this machine, by your Edge members or on a pinned Tower).")
		fmt.Fprintln(modelOut, "find a model: roger model search <words>, or roger model serve <name> to look a name up on the model hub")
		return nil
	}
	fmt.Fprintln(modelOut, "served nearby - serve one by its name; which file a name is, is its source's word:")
	w := 8
	for _, n := range names {
		w = max(w, len(n.Name))
	}
	for _, n := range names {
		for _, mp := range n.Maps {
			size := ""
			if mp.File.Bytes > 0 {
				size = "  " + provision.BytesWord(mp.File.Bytes)
			}
			fmt.Fprintf(modelOut, "  %-*s  %s%s  - %s\n", w, n.Name, mp.File.Ref(), size, mp.by())
		}
		if len(n.Plain) > 0 {
			fmt.Fprintf(modelOut, "  %-*s  served by %s, which does not say which file it is\n", w, n.Name, strings.Join(n.Plain, " and "))
		}
	}
	// The market's names last: strangers' bands, names only, never a file.
	for _, n := range names {
		if n.Market && len(n.Maps) == 0 && len(n.Plain) == 0 {
			fmt.Fprintf(modelOut, "  %-*s  on the market (a name only: serving it looks it up on the model hub)\n", w, n.Name)
		}
	}
	return nil
}

// The hub fallback's rule (ruling 2026-10-11, decision 4): GGUF repositories matching the words that
// have a file which fits this machine by the existing rule, by the hub's own download count.
const (
	hubShown   = 3  // offered
	hubInspect = 10 // looked into: one listing and one header read each
)

// hubPick is one repository the fallback offers, at the quantisation the existing rule picks.
type hubPick struct {
	m hfhub.Model
	c localmodel.Choice
	b localmodel.Budget
}

// modelHubLookup is a name served nowhere nearby, looked up on the model hub because the owner asked
// to serve it. The owner picks; nothing is fetched on a guess, and never without a terminal.
func modelHubLookup(ctx context.Context, cfg config, name string, ctxLen int, yes bool) (localmodel.Model, error) {
	fmt.Fprintf(modelOut, "%s is not served nearby; looking it up on the model hub, because you asked to serve it\n", name)
	ms, err := modelHub().Search(ctx, hfhub.Query{Text: name, Limit: hubInspect})
	if err != nil {
		return localmodel.Model{}, fmt.Errorf("%s is not served nearby, and %s (%v); %s", name, hubTrouble(err), err, nearbyInstead(cfg))
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Downloads > ms[j].Downloads })
	var picks []hubPick
	var more int
	var skipped []string
	for _, m := range ms {
		cs, b, err := modelRank(ctx, m.Repo, ctxLen)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("  -  %s  cannot be served: %v", m.Repo, err))
			continue
		}
		i := localmodel.Suggest(cs)
		switch {
		case i >= 0 && len(picks) < hubShown:
			picks = append(picks, hubPick{m, cs[i], b})
		case i >= 0:
			more++
		case len(cs) > 0 && hubAllUnservable(cs):
			skipped = append(skipped, fmt.Sprintf("  -  %s  cannot be served: %s is %s", m.Repo, cs[0].File.Path, cs[0].Unservable))
		case len(cs) > 0:
			skipped = append(skipped, fmt.Sprintf("  -  %s  not offered: no file in it fits this machine", m.Repo))
		default:
			skipped = append(skipped, fmt.Sprintf("  -  %s  cannot be served: it has no GGUF weights", m.Repo))
		}
	}
	if len(picks) == 0 && len(skipped) == 0 {
		return localmodel.Model{}, fmt.Errorf("nothing nearby and nothing on the model hub matches %q - try roger model search <other words>", name)
	}
	fmt.Fprintf(modelOut, "ranked by the model hub's download count, among GGUF repositories with a file that fits this machine; the top %d are shown\n", hubShown)
	hubList(picks)
	if more > 0 {
		verb := "fit"
		if more == 1 {
			verb = "fits"
		}
		fmt.Fprintf(modelOut, "  %d more that %s: roger model search %s shows every match\n", more, verb, name)
	}
	for _, l := range skipped {
		fmt.Fprintln(modelOut, l)
	}
	if len(picks) == 0 {
		return localmodel.Model{}, fmt.Errorf("nothing nearby, and nothing on the model hub that matches %q fits this machine - try roger model search <other words>", name)
	}
	if !edgeIsInteractive() {
		return localmodel.Model{}, errors.New("no terminal to pick on, so nothing was fetched; name one exactly: roger model serve <owner/repo>:<quant>")
	}
	return hubFetchPick(ctx, picks, yes)
}

// hubList draws the hub's offers, numbered, as their authors' word.
func hubList(picks []hubPick) {
	fmt.Fprintln(modelOut, "each is its author's word, not RogerAI's and not a nearby source's")
	for i, p := range picks {
		fmt.Fprintf(modelOut, "  %d) %s:%s  %s  licence %s  %s downloads  %s\n", i+1, p.m.Repo, modelDash(p.c.File.Quant),
			provision.BytesWord(p.c.File.Bytes), modelDash(p.m.License), countWord(p.m.Downloads), p.c.Verdict.Short())
	}
}

// hubFetchPick has the owner pick one offer (a single one is not asked to be picked), shows it in
// full and fetches it only on a yes, against the digest the listing gave.
func hubFetchPick(ctx context.Context, picks []hubPick, yes bool) (localmodel.Model, error) {
	i := 0
	if len(picks) > 1 {
		fmt.Fprintf(modelOut, "pick 1-%d to see it before anything is fetched, or n: ", len(picks))
		if i = modelReadPick(len(picks)); i < 0 {
			fmt.Fprintln(modelOut, "not fetched. Nothing changed.")
			return localmodel.Model{}, errModelDeclined
		}
	}
	p := picks[i]
	return modelFetchChoice(ctx, p.m.Repo, p.c, p.b, modelFetchAsk{
		ask:    "fetch it? [y/N]: ",
		source: "source: the model hub - its author's word, checked against the digest the listing gave",
		whose:  "the author's word, from the model hub",
	}, yes)
}

// noTerminalSuggest is what a run with no terminal says after its suggestions: nothing moved.
const noTerminalSuggest = "no terminal, so nothing is fetched: roger model serve <name> serves one"

// modelSuggest is `roger model serve` with no name (short_names_nearby.feature section 6): what is
// served nearby that fits this machine, else a few hub models that fit comfortably, by a stated
// rule. ok is false when nothing was chosen; nothing is ever fetched without a pick at a terminal.
func modelSuggest(ctx context.Context, cfg config, a modelArgv) (localmodel.Model, bool, error) {
	b := modelBudget()
	mapped := nearbyMapped(cfg)
	if !b.Known() {
		// No guess: each nearby one with what it needs, and the owner decides on their own word.
		fmt.Fprintf(modelOut, "roger cannot judge what fits this machine: %s\n", b.Why)
		if len(mapped) == 0 {
			fmt.Fprintln(modelOut, "nothing is served nearby.")
			return localmodel.Model{}, false, nil
		}
		for _, n := range mapped {
			line := nearbyResolved{name: n.Name, mp: n.Maps[0]}.mapLine()
			if c, ok := nearbyChoice(ctx, n.Maps[0], a.ctx); ok && c.Verdict.Need.Total > 0 {
				fmt.Fprintf(modelOut, "  -  %s  %s  needs %s of memory at %d context\n", line, provision.BytesWord(c.File.Bytes),
					provision.BytesWord(c.Verdict.Need.Total), c.Verdict.Need.Ctx)
			} else {
				fmt.Fprintf(modelOut, "  -  %s\n", line)
			}
		}
		fmt.Fprintln(modelOut, "roger model serve <name> serves one on your word")
		return localmodel.Model{}, false, nil
	}
	if len(mapped) > 0 {
		sug := nearbySuggestions(ctx, mapped, a.ctx)
		var fit []nearbyName
		var fitLines, other []string
		for _, g := range sug {
			if g.why != "" {
				size := ""
				if g.ok {
					size = "  " + provision.BytesWord(g.c.File.Bytes)
				}
				other = append(other, fmt.Sprintf("  -  %s%s  %s", g.r.mapLine(), size, g.why))
				continue
			}
			fit = append(fit, g.n)
			fitLines = append(fitLines, fmt.Sprintf("  %d) %s  %s  %s", len(fit), g.r.mapLine(), provision.BytesWord(g.c.File.Bytes), g.c.Verdict.Short()))
		}
		if len(fit) > 0 {
			fmt.Fprintln(modelOut, "served nearby, and fits this machine:")
			for _, l := range append(fitLines, other...) {
				fmt.Fprintln(modelOut, l)
			}
			if !edgeIsInteractive() {
				fmt.Fprintln(modelOut, noTerminalSuggest)
				return localmodel.Model{}, false, nil
			}
			if len(fit) == 1 {
				fmt.Fprint(modelOut, "pick 1 to serve it, or n: ")
			} else {
				fmt.Fprintf(modelOut, "pick 1-%d to serve one, or n: ", len(fit))
			}
			i := modelReadPick(len(fit))
			if i < 0 {
				fmt.Fprintln(modelOut, "not fetched. Nothing changed.")
				return localmodel.Model{}, false, nil
			}
			m, err := modelFromNearby(ctx, cfg, fit[i], "", a.ctx, a.yes)
			return m, err == nil, err
		}
		fmt.Fprintln(modelOut, "nothing served nearby fits this machine:")
		for _, l := range other {
			fmt.Fprintln(modelOut, l)
		}
	} else {
		fmt.Fprintln(modelOut, "nothing is served nearby.")
	}
	ms, err := modelHub().Search(ctx, hfhub.Query{Limit: hubInspect})
	if err != nil {
		return localmodel.Model{}, false, fmt.Errorf("nothing is served nearby, and %s (%v), so roger suggests nothing", hubTrouble(err), err)
	}
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Downloads > ms[j].Downloads })
	var picks []hubPick
	for _, m := range ms {
		if m.Pipeline != "text-generation" || len(picks) == hubShown {
			continue // a chat model, the rule says
		}
		cs, b, err := modelRank(ctx, m.Repo, a.ctx)
		if err != nil {
			continue
		}
		if i := localmodel.Suggest(cs); i >= 0 && cs[i].Verdict.Fit == catalog.FitFits {
			picks = append(picks, hubPick{m, cs[i], b})
		}
	}
	fmt.Fprintf(modelOut, "from the model hub: the most-downloaded chat GGUFs on the model hub with a file that fits this machine comfortably; up to %d\n", hubShown)
	if len(picks) == 0 {
		fmt.Fprintln(modelOut, "none of them fits this machine comfortably; roger model search <words> looks further")
		return localmodel.Model{}, false, nil
	}
	hubList(picks)
	if !edgeIsInteractive() {
		fmt.Fprintln(modelOut, noTerminalSuggest)
		return localmodel.Model{}, false, nil
	}
	m, err := hubFetchPick(ctx, picks, a.yes)
	if errors.Is(err, errModelDeclined) {
		return m, false, nil
	}
	return m, err == nil, err
}

// nearbySuggestion is one nearby mapping judged against this machine: why is "" when it fits.
type nearbySuggestion struct {
	n   nearbyName // the name, with this one mapping
	r   nearbyResolved
	c   localmodel.Choice
	ok  bool // the hub lists the file, so c is its judged entry
	why string
}

// nearbySuggestions judges every mapping against this machine: the one rule `roger model serve`
// with no name, the TUI and the console all suggest by.
func nearbySuggestions(ctx context.Context, mapped []nearbyName, ctxLen int) []nearbySuggestion {
	out := make([]nearbySuggestion, 0, len(mapped))
	for _, n := range mapped {
		g := nearbySuggestion{n: n}
		g.c, g.ok = nearbyChoice(ctx, n.Maps[0], ctxLen)
		g.r = nearbyResolved{name: n.Name, mp: n.Maps[0], c: g.c}
		switch {
		case !g.ok:
			g.why = "cannot be judged: the model hub does not list that file now"
		case g.c.Verdict.Fit == catalog.FitWontFit:
			g.why = "would not fit here: " + g.c.Verdict.Line()
		}
		out = append(out, g)
	}
	return out
}

// nearbyMapped is every mapping served nearby, one name per mapping.
func nearbyMapped(cfg config) []nearbyName {
	var out []nearbyName
	for _, n := range modelNearby(cfg) {
		for _, mp := range n.Maps {
			out = append(out, nearbyName{Name: n.Name, Maps: []nearbyMap{mp}})
		}
	}
	return out
}

// modelWebNearby is the screens' view of what is served nearby, from nearbySuggestions.
func modelWebNearby(cfg config) func(ctx context.Context) (modelview.NearbyView, error) {
	return func(ctx context.Context) (modelview.NearbyView, error) {
		b := modelBudgetNow()
		nv := modelview.NearbyView{Budget: b.Bytes, Pool: b.Pool, BudgetWhy: b.Why, Fits: []modelview.NearbyRow{}, Other: []modelview.NearbyRow{}}
		for _, g := range nearbySuggestions(ctx, nearbyMapped(cfg), 0) {
			f := g.r.mp.File
			row := modelview.NearbyRow{Name: g.n.Name, Repo: f.Repo, File: f.File, Quant: f.Quant, SHA256: f.SHA256, Bytes: f.Bytes,
				By: g.r.mp.by(), Why: g.why}
			if g.ok {
				row.Quant, row.Bytes, row.Ask = g.c.File.Quant, g.c.File.Bytes, g.r.ask()
				row.Fit, row.Short, row.Line = g.c.Verdict.Fit.String(), g.c.Verdict.Short(), g.c.Verdict.Line()
			}
			if g.why == "" {
				nv.Fits = append(nv.Fits, row)
			} else {
				nv.Other = append(nv.Other, row)
			}
		}
		return nv, nil
	}
}

// modelWebFetchNearby fetches, in the background, exactly the file the owner said yes to on a
// screen: refused when the name no longer maps to it, or when the hub lists other bytes.
func modelWebFetchNearby(cfg config) func(p modelview.NearbyPick) (string, error) {
	return func(p modelview.NearbyPick) (string, error) {
		n, ok := nearbyFind(modelNearby(cfg), p.Name)
		if !ok || len(n.Maps) == 0 {
			return "", fmt.Errorf("%s is not served nearby now; nothing was fetched", p.Name)
		}
		want := servedfile.File{Repo: p.Repo, File: p.File, SHA256: p.SHA256}
		for _, mp := range n.Maps {
			if !mp.File.Same(want) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			r, err := nearbyResolve(ctx, nearbyName{Name: n.Name, Maps: []nearbyMap{mp}}, "", 0)
			if err != nil {
				return "", err
			}
			if r.listed {
				return "", fmt.Errorf("%s will not fit here; nothing was fetched", r.mapLine())
			}
			r.c.File.SHA256 = mp.File.SHA256
			if err := modelHome().StartFetch(modelHub(), provision.Fetcher{}, mp.File.Repo, r.c.File, modelLicense(ctx, mp.File.Repo), nil); err != nil {
				return "", err
			}
			return "fetching " + r.c.File.Path + " - it keeps going if you leave this screen", nil
		}
		return "", fmt.Errorf("%s no longer maps to that file; look again before saying yes", p.Name)
	}
}

// nearbyChoice is the hub's ranked entry for the exact file a mapping names, judged here.
func nearbyChoice(ctx context.Context, mp nearbyMap, ctxLen int) (localmodel.Choice, bool) {
	cs, _, err := modelRank(ctx, mp.File.Repo, ctxLen)
	if err != nil {
		return localmodel.Choice{}, false
	}
	for _, c := range cs {
		if c.File.Path == mp.File.File {
			return c, true
		}
	}
	return localmodel.Choice{}, false
}

func hubAllUnservable(cs []localmodel.Choice) bool {
	for _, c := range cs {
		if c.Unservable == "" {
			return false
		}
	}
	return true
}

// hubTrouble says why the model hub gave no answer, in the owner's words.
func hubTrouble(err error) string {
	if errors.Is(err, hfhub.ErrUnreachable) {
		return "the model hub cannot be reached now"
	}
	return "the model hub did not answer"
}

// nearbyInstead is what is served nearby, said when the hub cannot be asked.
func nearbyInstead(cfg config) string {
	var names []string
	for _, n := range modelNearby(cfg) {
		if len(n.Maps) > 0 {
			names = append(names, n.Name)
		}
	}
	if len(names) == 0 {
		return "nothing is served nearby either"
	}
	return "served nearby instead: " + strings.Join(names, ", ")
}

// modelReadPick reads a number from 1 to n; anything else is no (-1).
func modelReadPick(n int) int {
	line := edgeReadLine()
	var k int
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &k); err != nil || k < 1 || k > n {
		return -1
	}
	return k - 1
}
