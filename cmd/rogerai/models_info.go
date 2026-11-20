package main

// `roger models <id>`: what one model on the market is for (features/details/api_cli.feature).
// The same ABOUT lines the TUI station log shows (internal/about), from the same /discover
// offers, plain text; --json is the /v1/models rogerai.details block plus the id.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/about"
	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/protocol"
)

// lanOffersFor reads every configured LAN Tower's offers (a seam: a test points it at its own).
var lanOffersFor = func(cfg config) []client.Offer {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.LANOffers(ctx, client.LANInputs{Towers: lanTowerAddrs(cfg), HTTP: lanProbeHTTP, Resolve: lanResolve})
}

func cmdModelsInfo(cfg config, args []string) error {
	var id string
	asJSON := false
	for _, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case id == "":
			id = a
		}
	}
	var offers []client.Offer
	if cfg.lan.LAN {
		defer edgeScans.Begin(scanLAN)() // this run is the request (scan_on_request.feature)
		// A Tower is not the broker: it can never claim RogerAI's words or a page. Our notes
		// apply to our model ids here exactly as the broker applies them.
		notes, _ := catalog.Builtin()
		offers = lanOffersFor(cfg)
		for i := range offers {
			offers[i].DetailsSource, offers[i].Page = "", ""
			offers[i].Taxonomy = notes.Apply(offers[i].Model, offers[i].Taxonomy.Canonical(offers[i].Modality))
		}
	} else {
		var err error
		if offers, err = client.Discover(cfg.Broker); err != nil {
			return err
		}
	}
	for i := range offers { // read again whatever answered: this text is printed into a terminal
		offers[i].Taxonomy = offers[i].Taxonomy.Canonical(offers[i].Modality)
	}
	var mine []client.Offer
	for _, o := range offers {
		if o.Model == id {
			mine = append(mine, o)
		}
	}
	if len(mine) == 0 {
		return notOnAir(id, offers)
	}
	ballots := make([]protocol.Ballot, len(mine))
	online := 0
	for i, o := range mine {
		ballots[i] = protocol.Ballot{T: o.Taxonomy, Online: o.Online, Curated: o.Curated}
		if o.Online {
			online++
		}
	}
	d := protocol.FoldDetails(ballots)
	if asJSON {
		blk := about.Block(d, ballots, catalog.SiteOrigin)
		blk["id"] = id
		b, err := json.Marshal(blk)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	tuning, base, license := about.Facts(ballots, d)
	vision := false
	for _, o := range mine {
		for _, c := range o.Capabilities {
			vision = vision || c == protocol.CapVision
		}
	}
	fmt.Println(id)
	if h := about.Heading(d); h != "" {
		fmt.Println("  " + h)
	} else {
		fmt.Println("  no description yet - the station has not said what this model is for")
	}
	for _, l := range about.Lines(about.Model{ID: id, Modality: mine[0].Modality, Vision: vision, Tuning: tuning,
		BaseModel: base, License: license, Details: d, Siblings: siblingsOf(id, base, offers)},
		func(p string) string { return catalog.PageURL(catalog.SiteOrigin, p) }) {
		fmt.Println("  " + about.Pad(l.Label) + l.Value)
	}
	n := "stations"
	if online == 1 {
		n = "station"
	}
	fmt.Printf("  %s%d %s   ·   roger use %s\n", about.Pad("on air"), online, n, id)
	return nil
}

// siblingsOf is every other on-air model on the same base.
func siblingsOf(id, base string, offers []client.Offer) []string {
	key := about.SameBase(base)
	if key == "" {
		return nil
	}
	seen := map[string]bool{id: true}
	var out []string
	for _, o := range offers {
		if o.Online && !seen[o.Model] && about.SameBase(o.BaseModel) == key {
			seen[o.Model] = true
			out = append(out, o.Model)
		}
	}
	sort.Strings(out)
	return out
}

// notOnAir is the error for an id with no offer, naming the on-air ids that contain it.
func notOnAir(id string, offers []client.Offer) error {
	msg := id + " is not on air - roger models lists what is"
	seen := map[string]bool{}
	var close []string
	for _, o := range offers {
		if id != "" && o.Online && !seen[o.Model] && strings.Contains(o.Model, id) {
			seen[o.Model] = true
			close = append(close, o.Model)
		}
	}
	if len(close) > 0 {
		sort.Strings(close)
		msg += "\nclose matches: " + strings.Join(close, ", ")
	}
	return fmt.Errorf("%s", msg)
}
