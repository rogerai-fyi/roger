package main

// stationoffer.go decides what a station offers a Tower (features/join/invite.feature section 6,
// founder ruling 2026-10-11). By default a station offers only the models roger serves itself.
// Another detected local server is offered when the station's owner picks it at a terminal or
// names it with --models. A local proxy to hosted accounts is offered only when named with
// --models: never by default, never by a pick. The choice made at join is saved, so the
// station's later polls stay inside it too.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/detect"
)

// StationOffer is what this machine chose to offer Towers as a station: the explicit --models
// list (it replaces the default when set) and the other local servers' models picked at the
// prompt. Models roger serves itself are always offered, so they are not stored.
type StationOffer struct {
	Explicit []string `json:"explicit,omitempty"`
	Picked   []string `json:"picked,omitempty"`
}

// rogerServedBases are the base URLs of the runtimes roger serves on this machine (`roger model
// serve`). A server counts as roger-served by WHERE it is, never by what its models are called.
var rogerServedBases = func() []string {
	var out []string
	for _, s := range modelHome().LiveAll() {
		out = append(out, s.BaseURL)
	}
	return out
}

// stationInteractive reports whether the join can ask the station's owner; a test replaces it.
var stationInteractive = func() bool { return edgeIsInteractive() }

// stationStdin is where the pick is read from; a test replaces it.
var stationStdin io.Reader = os.Stdin

// hostedFamilies are model-id prefixes of hosted, account-backed models. A detected server that
// lists any of them is a local proxy to someone's paid or third-party account.
var hostedFamilies = []string{
	"claude", "codex", "grok", "gemini", "chatgpt", "gpt-3", "gpt-4", "gpt-5",
	"o1", "o3", "o4", "copilot", "github", "duck-", "perplexity", "sonar",
}

// isHostedModel reports whether a model id names a hosted, account-backed model. Open-weight
// names such as gpt-oss never do.
func isHostedModel(m string) bool {
	id := strings.ToLower(m)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if strings.HasPrefix(id, "gpt-oss") {
		return false
	}
	for _, p := range hostedFamilies {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// isAccountProxy reports whether a server is a proxy to hosted accounts: it lists any hosted model.
func isAccountProxy(models []string) bool {
	for _, m := range models {
		if isHostedModel(m) {
			return true
		}
	}
	return false
}

// orZero is the saved offer, or the default (nil: never chosen).
func (o *StationOffer) orZero() StationOffer {
	if o == nil {
		return StationOffer{}
	}
	return *o
}

// sameBase reports whether two server URLs name the same server.
func sameBase(a, b string) bool {
	norm := func(u string) string {
		u = strings.TrimSuffix(strings.TrimSuffix(u, "/chat/completions"), "/")
		return strings.TrimSuffix(u, "/v1")
	}
	return a != "" && b != "" && norm(a) == norm(b)
}

func isRogerServed(base string, served []string) bool {
	for _, s := range served {
		if sameBase(base, s) {
			return true
		}
	}
	return false
}

// stationChoose works out what to offer from what is on air (found), the explicit --models list,
// and, at a terminal, the owner's pick. It writes the list and what was left out to out.
func stationChoose(found []detect.Found, explicit []string, out io.Writer) (offer []string, saved StationOffer) {
	served := rogerServedBases()
	var detected []string
	for _, f := range found {
		for _, m := range f.Models {
			if !contains(detected, m) {
				detected = append(detected, m)
			}
		}
	}
	if len(explicit) > 0 {
		var notHere []string
		for _, m := range explicit {
			if contains(detected, m) {
				offer = append(offer, m)
			} else {
				notHere = append(notHere, m)
			}
		}
		if len(notHere) > 0 {
			fmt.Fprintf(out, "not on air here, not offered: %s\n", strings.Join(notHere, ", "))
		}
		return offer, StationOffer{Explicit: offer}
	}
	type cand struct {
		model string
		proxy bool
	}
	var others []cand
	for _, f := range found {
		rogers, proxy := isRogerServed(f.BaseURL, served), isAccountProxy(f.Models)
		for _, m := range f.Models {
			switch {
			case rogers && !proxy:
				if !contains(offer, m) {
					offer = append(offer, m)
				}
			default:
				others = append(others, cand{m, proxy})
			}
		}
	}
	// One entry per model: several servers can list the same id, and it counts as an account
	// proxy's if any of them is one.
	var dropped []cand
	at := map[string]int{}
	for _, c := range others {
		if contains(offer, c.model) {
			continue
		}
		if i, seen := at[c.model]; seen {
			dropped[i].proxy = dropped[i].proxy || c.proxy
			continue
		}
		at[c.model] = len(dropped)
		dropped = append(dropped, c)
	}
	others = dropped
	if len(others) == 0 {
		return offer, StationOffer{}
	}
	if stationInteractive() {
		fmt.Fprintln(out, "other models on this machine (none is offered unless you pick it):")
		for i, c := range others {
			if c.proxy {
				fmt.Fprintf(out, "  %d) %s (account proxy: name it with --models to offer)\n", i+1, c.model)
			} else {
				fmt.Fprintf(out, "  %d) %s\n", i+1, c.model)
			}
		}
		fmt.Fprint(out, "add which? numbers separated by spaces, Enter for none: ")
		line, _ := bufio.NewReader(stationStdin).ReadString('\n')
		var picked []string
		for _, f := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r' }) {
			n, err := strconv.Atoi(f)
			if err != nil || n < 1 || n > len(others) || others[n-1].proxy {
				continue
			}
			if m := others[n-1].model; !contains(picked, m) {
				picked = append(picked, m)
				offer = append(offer, m)
			}
		}
		return offer, StationOffer{Picked: picked}
	}
	fmt.Fprintln(out, skippedLine(len(others)))
	return offer, StationOffer{}
}

// skippedLine is the one line a station prints for the models it did not offer: a count, never
// the list (a machine can carry dozens, account proxies among them).
func skippedLine(n int) string {
	noun := "models"
	if n == 1 {
		noun = "model"
	}
	return fmt.Sprintf("skipped %d other %s on this machine; name one with --models to offer it", n, noun)
}

// refusedLine is the one line a station prints for the models the Tower did not take: their names
// when there are a few, a count when there are many.
func refusedLine(refused []string) string {
	if len(refused) <= 3 {
		return "the Tower does not accept: " + strings.Join(refused, ", ")
	}
	return fmt.Sprintf("the Tower does not accept %d of the offered models (its invite names the ones it takes)", len(refused))
}

// stationKeep narrows what a station's poll reports to what it chose: the explicit list when one
// was given; otherwise the ones picked at join plus a roger-served upstream's models. A hosted
// model, or any model of a proxy upstream, is reported only when named explicitly.
func stationKeep(offer StationOffer, upstream string, models []string) []string {
	out := []string{}
	if len(offer.Explicit) > 0 {
		for _, m := range models {
			if contains(offer.Explicit, m) {
				out = append(out, m)
			}
		}
		return out
	}
	rogers := isRogerServed(upstream, rogerServedBases()) && !isAccountProxy(models)
	for _, m := range models {
		if contains(offer.Picked, m) || (rogers && !isHostedModel(m)) {
			out = append(out, m)
		}
	}
	return out
}

// parseModelsFlag pulls --models a,b (or --models=a,b) out of args and returns the list and the
// remaining args.
func parseModelsFlag(args []string) (models, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		v, ok := strings.CutPrefix(a, "--models=")
		if !ok && (a == "--models" || a == "-models") && i+1 < len(args) {
			v, ok = args[i+1], true
			i++
		}
		if !ok {
			rest = append(rest, a)
			continue
		}
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" && !contains(models, m) {
				models = append(models, m)
			}
		}
	}
	return models, rest
}

// stationRoute is where a station sends jobs for one model it offers from another local server.
type stationRoute struct{ url, key string }

// stationRoutes are the offered models served by local servers other than the share's own
// upstream (picked at join, named with --models, or another roger runtime), so one station serves
// everything its owner chose and each job reaches the server that has its model.
func stationRoutes(offer StationOffer, upstream string) map[string]stationRoute {
	routes := map[string]stationRoute{}
	found, _ := detectFull()
	for _, f := range found {
		if sameBase(f.BaseURL, upstream) || f.Chat == "" {
			continue
		}
		for _, m := range stationKeep(offer, f.BaseURL, f.Models) {
			if _, ok := routes[m]; !ok {
				routes[m] = stationRoute{f.Chat, f.Key}
			}
		}
	}
	return routes
}

// withStationRoutes adds the other servers' offered models to a station's poll and routes their
// jobs (agent.Config.StationUpstream).
func withStationRoutes(run *agent.Config, routes map[string]stationRoute) {
	if len(routes) == 0 {
		return
	}
	own := run.StationModels
	run.StationModels = func() []string {
		var out []string
		if own != nil {
			out = own()
		}
		for m := range routes {
			if !contains(out, m) {
				out = append(out, m)
			}
		}
		sort.Strings(out[min(1, len(out)):]) // the shared model leads; the rest in a stable order
		return out
	}
	run.StationUpstream = func(model string) (string, string, bool) {
		r, ok := routes[model]
		return r.url, r.key, ok
	}
}
