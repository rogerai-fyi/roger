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

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/hfhub"
	"rogerai.fm/roger/v6/internal/localmodel"
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

// modelFromNearby fetches the file a nearby name maps to, after the owner says yes to exactly it.
func modelFromNearby(ctx context.Context, cfg config, n nearbyName, quant string, ctxLen int, yes bool) (localmodel.Model, error) {
	mp := n.Maps[0]
	cs, b, err := modelRank(ctx, mp.File.Repo, ctxLen)
	if err != nil {
		return localmodel.Model{}, err
	}
	var c localmodel.Choice
	for _, x := range cs {
		if x.File.Path == mp.File.File {
			c = x
		}
	}
	if c.File.Path == "" {
		return localmodel.Model{}, fmt.Errorf("%s says %s is %s, which the model hub does not list in %s", mp.by(), n.Name, mp.File.File, mp.File.Repo)
	}
	c.File.SHA256 = mp.File.SHA256
	ref := mp.File.Repo + ":" + modelDash(c.File.Quant)
	return modelFetchChoice(ctx, mp.File.Repo, c, b, modelFetchAsk{
		ask:    fmt.Sprintf("%s = %s as served by %s; fetch %s? [y/N]: ", n.Name, ref, mp.by(), provision.BytesWord(c.File.Bytes)),
		source: "the bytes come from the model hub, checked against the digest " + mp.by() + " reported",
	}, yes)
}

// modelHubLookup is a name served nowhere nearby, looked up on the model hub because the owner asked
// to serve it. The owner picks; nothing is fetched on a guess.
func modelHubLookup(ctx context.Context, name string, ctxLen int, yes bool) (localmodel.Model, error) {
	fmt.Fprintf(modelOut, "%s is not served nearby; looking it up on the model hub, because you asked to serve it\n", name)
	ms, err := modelHub().Search(ctx, hfhub.Query{Text: name, Limit: 20})
	if err != nil {
		return localmodel.Model{}, modelSourceErr(err)
	}
	type pick struct {
		repo string
		c    localmodel.Choice
		b    localmodel.Budget
		m    hfhub.Model
	}
	var picks []pick
	for _, m := range ms {
		cs, b, err := modelRank(ctx, m.Repo, ctxLen)
		if err != nil {
			continue
		}
		if i := localmodel.Suggest(cs); i >= 0 {
			picks = append(picks, pick{m.Repo, cs[i], b, m})
		}
	}
	if len(picks) == 0 {
		return localmodel.Model{}, fmt.Errorf("nothing nearby and nothing on the model hub matches %q - try roger model search <other words>", name)
	}
	for i, p := range picks {
		fmt.Fprintf(modelOut, "  %d) %s:%s  %s  licence %s  %s downloads  %s\n", i+1, p.repo, modelDash(p.c.File.Quant),
			provision.BytesWord(p.c.File.Bytes), modelDash(p.m.License), countWord(p.m.Downloads), p.c.Verdict.Short())
	}
	i := 0
	if len(picks) > 1 {
		fmt.Fprintf(modelOut, "pick 1-%d to see it before anything is fetched, or n: ", len(picks))
		i = modelReadPick(len(picks))
		if i < 0 {
			fmt.Fprintln(modelOut, "not fetched. Nothing changed.")
			return localmodel.Model{}, errModelDeclined
		}
	}
	p := picks[i]
	return modelFetchChoice(ctx, p.repo, p.c, p.b, modelFetchAsk{
		ask:    "fetch it? [y/N]: ",
		source: "source: the model hub - its author's word, checked against the digest the listing gave",
	}, yes)
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
