package client

// LAN discovery (features/lan/discovery_and_dial.feature): the one merged list LAN mode shows
// on every surface. Three sources and nothing else: this machine, VERIFIED LAN-direct Edge
// members, and standalone Towers at a local address. A Tower's address is checked again at
// every refresh (a name is never cached as local), its /discover is read signed with a capped
// body and a capped band count, and a slow Tower never holds the other sources hostage.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"rogerai.fm/roger/v6/internal/towerpin"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/servedfile"
)

// LANSourceKind is where a band is served on the LAN.
type LANSourceKind string

const (
	LANSelf  LANSourceKind = "self"
	LANEdge  LANSourceKind = "edge"
	LANTower LANSourceKind = "tower"
)

// LANSource is one place a band is served. Verified is true for this machine, for Edge members
// (certificate-verified) and for a Tower pinned to its TLS key; an unpinned Tower is plain http,
// trusted only because it is on the LAN.
type LANSource struct {
	Kind     LANSourceKind `json:"kind"`
	ID       string        `json:"id"`             // node name, or the Tower's host:port; "" for this machine
	Addr     string        `json:"addr,omitempty"` // a Tower's host:port
	Verified bool          `json:"verified"`
	// File is which exact file this source serves the band from, as it says
	// (features/models/short_names_nearby.feature): this machine's and an Edge member's own record,
	// or a pinned Tower's station report. nil: this source names no file.
	File *servedfile.File `json:"file,omitempty"`
}

// Label is the dial's source text: "this machine", "edge: jetson", "tower: 192.168.1.20".
func (s LANSource) Label() string {
	switch s.Kind {
	case LANSelf:
		return "this machine"
	case LANEdge:
		return "edge: " + s.ID
	}
	host := s.ID
	if h, _, err := net.SplitHostPort(s.ID); err == nil {
		host = h
	}
	return "tower: " + host
}

// LANBand is one dial row: a model and every local source serving it.
type LANBand struct {
	Model   string      `json:"model"`
	Sources []LANSource `json:"sources"`
	// Voice marks a text-to-speech band a local source serves as a voice (features/lan/voice.feature):
	// the dial opens its voice preview, never a chat channel.
	Voice bool `json:"voice,omitempty"`
}

// SourceLabel joins the row's source labels, this machine first.
func (b LANBand) SourceLabel() string {
	var out []string
	for _, s := range b.Sources {
		out = append(out, s.Label())
	}
	return strings.Join(out, ", ")
}

// LANEdgeBand is one Edge member's band, as the fleet record has it.
type LANEdgeBand struct {
	Node      string
	Band      string
	Verified  bool // the serving probe answered (a CLAIM is not a source)
	LANDirect bool // reachable on the LAN, not only through a relay
	Dark      bool
	File      *servedfile.File // which file the member says the band is; nil on an older member
}

// NotAdmittedHint is what a Tower that has not let this machine in yet means, and the simple way in
// (features/join/surfaces.feature): no key hash or id to carry between machines.
const NotAdmittedHint = "this Tower hasn't let you in yet. Ask its owner for an invite (roger tower invite), then run: roger join <invite>"

// LANTowerState is what a configured Tower is right now.
type LANTowerState string

const (
	LANTowerAnswering    LANTowerState = "answering"
	LANTowerNotAdmitted  LANTowerState = "not admitted"
	LANTowerNotAnswering LANTowerState = "not answering"
	LANTowerBadAnswer    LANTowerState = "bad answer"
	LANTowerWaiting      LANTowerState = "waiting"
	LANTowerRefused      LANTowerState = "refused"
	LANTowerSameAs       LANTowerState = "same Tower"
	// LANTowerCandidate is a Tower heard advertising on the LAN that is not in lan_towers: shown
	// with how to add it, and never used until it is added.
	LANTowerCandidate LANTowerState = "candidate"
	// LANTowerNotStandalone is an address that answered a turn without the local marker.
	LANTowerNotStandalone LANTowerState = "not a standalone Tower"
)

// LANTowerStatus is one configured Tower's line.
type LANTowerStatus struct {
	Addr   string        `json:"addr"`
	State  LANTowerState `json:"state"`
	Detail string        `json:"detail,omitempty"`
	Bands  int           `json:"bands"`
	More   bool          `json:"more,omitempty"` // it listed more than LAN mode shows
}

// LANDial is one read of the LAN dial: the merged rows, each Tower's state, and, when this
// Edge's record could not be read, why (an unread Edge is never shown as an empty one).
type LANDial struct {
	Bands      []LANBand
	Towers     []LANTowerStatus
	EdgeUnread string
}

// LANInputs are the sources to merge.
type LANInputs struct {
	Self     []string
	Voices   []string // the bands above that a local source serves as a voice
	Edge     []LANEdgeBand
	Towers   []string
	Key      ed25519.PrivateKey // nil = this machine's identity key
	HTTP     *http.Client       // nil = a plain client
	Resolve  lanmode.Resolver   // nil = the system resolver
	Timeout  time.Duration      // per Tower; 0 = 5 s
	MaxBands int                // per Tower; 0 = lanDefaultMaxBands
}

const (
	lanDefaultMaxBands = 256
	lanMaxBody         = 1 << 20
	lanMaxModelName    = 128
)

// LANDiscover merges the three sources into dial rows (sorted by model) and reports each
// configured Tower's state, in the order given.
func LANDiscover(ctx context.Context, in LANInputs) ([]LANBand, []LANTowerStatus) {
	rows := map[string]*LANBand{}
	add := func(model string, src LANSource) {
		model = cleanModelName(model)
		if model == "" {
			return
		}
		b := rows[model]
		if b == nil {
			b = &LANBand{Model: model}
			rows[model] = b
		}
		for _, have := range b.Sources {
			if have.Kind == src.Kind && have.ID == src.ID {
				return
			}
		}
		b.Sources = append(b.Sources, src)
	}
	for _, m := range in.Self {
		add(m, LANSource{Kind: LANSelf, Verified: true})
	}
	for _, e := range in.Edge {
		if e.Verified && e.LANDirect && !e.Dark {
			add(e.Band, LANSource{Kind: LANEdge, ID: e.Node, Verified: true, File: e.File})
		}
	}
	status := lanTowers(ctx, in)
	for _, st := range status {
		// A pinned Tower was read over TLS to exactly its key (features/join/tower_identity.feature),
		// so its rows are verified, like an Edge member's. An unpinned one is plain http, unverified.
		_, pinned := lanmode.PinFor(st.Addr)
		for _, m := range st.models {
			src := LANSource{Kind: LANTower, ID: st.Addr, Addr: st.Addr, Verified: pinned}
			if f, ok := st.files[m]; ok && pinned { // an unpinned Tower's report never defines a file
				src.File = &f
			}
			add(m, src)
		}
	}
	voice := map[string]bool{}
	for _, v := range in.Voices {
		voice[v] = true
	}
	out := make([]LANBand, 0, len(rows))
	for _, b := range rows {
		b.Voice = voice[b.Model]
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	res := make([]LANTowerStatus, len(status))
	for i, st := range status {
		res[i] = st.LANTowerStatus
	}
	return out, res
}

type towerResult struct {
	LANTowerStatus
	models []string
	files  map[string]servedfile.File // a station's report of which file a model is
}

// lanTowers reads every configured Tower in parallel, each bounded by its own timeout.
func lanTowers(ctx context.Context, in LANInputs) []towerResult {
	out := make([]towerResult, len(in.Towers))
	seen := map[string]int{} // resolved ip:port -> first index
	var wg sync.WaitGroup
	for i, entry := range in.Towers {
		out[i].Addr = entry
		a, err := lanmode.ParseAddr(entry)
		if err != nil {
			out[i].State, out[i].Detail = LANTowerRefused, err.Error()
			continue
		}
		// One lookup decides both: the connection goes to the address the check approved, never to
		// the name again, so a name cannot flip to a public address in between.
		key, v := lanmode.Resolve(ctx, a, in.Resolve)
		if !v.Local && !(v.CGNAT && a.Literal()) {
			out[i].State, out[i].Detail = LANTowerRefused, v.Reason
			continue
		}
		if first, dup := seen[key]; dup {
			out[i].State, out[i].Detail = LANTowerSameAs, "the same Tower as "+in.Towers[first]
			continue
		}
		seen[key] = i
		base := "http://" + key
		if a.Literal() {
			base = a.Base() // escapes an IPv6 zone
		}
		wg.Add(1)
		go func(i int, base string) {
			defer wg.Done()
			out[i].State, out[i].Detail, out[i].models, out[i].files, out[i].More = readTower(ctx, in, base)
			out[i].Bands = len(out[i].models)
		}(i, base)
	}
	wg.Wait()
	return out
}

// readTower reads one Tower's /discover, signed (the plane admits signed clients only).
func readTower(ctx context.Context, in LANInputs, base string) (LANTowerState, string, []string, map[string]servedfile.File, bool) {
	timeout := in.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/discover", nil)
	if err != nil {
		return LANTowerRefused, err.Error(), nil, nil, false
	}
	key := in.Key
	if key == nil {
		key = LoadOrCreateUserKey()
	}
	hc := in.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	// A pinned Tower is read over TLS, trusting exactly its key (features/join/tower_identity.feature).
	if routed, phc, pinned := lanmode.Route(base, hc); pinned {
		req.URL, _ = url.Parse(routed + "/discover")
		hc = phc
	}
	signWithKey(req, nil, key)
	hc = noRedirects(hc)
	resp, err := hc.Do(req)
	if errors.Is(err, towerpin.ErrPinMismatch) {
		return LANTowerRefused, TowerKeyChanged(req.URL.Host), nil, nil, false
	}
	if err != nil {
		var ue *url.Error
		if ctx.Err() != nil || (errors.As(err, &ue) && ue.Timeout()) {
			return LANTowerWaiting, "did not answer in time", nil, nil, false
		}
		return LANTowerNotAnswering, err.Error(), nil, nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return LANTowerNotAdmitted, NotAdmittedHint, nil, nil, false
	}
	if resp.StatusCode != http.StatusOK {
		return LANTowerBadAnswer, fmt.Sprintf("status %d", resp.StatusCode), nil, nil, false
	}
	limit := in.MaxBands
	if limit == 0 {
		limit = lanDefaultMaxBands
	}
	models, files, more, err := streamOffers(io.LimitReader(resp.Body, lanMaxBody), limit)
	if err != nil && len(models) == 0 {
		return LANTowerBadAnswer, "not a /discover answer", nil, nil, false
	}
	return LANTowerAnswering, "", models, files, more
}

// streamOffers reads {"offers":[...]} one offer at a time and stops once limit distinct online
// models are read, so a Tower listing a hundred thousand offers costs a bounded read. A body
// cut off by the size cap after the limit was reached is not an error.
func streamOffers(r io.Reader, limit int) ([]string, map[string]servedfile.File, bool, error) {
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, false, fmt.Errorf("not an object")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, nil, false, err
		}
		if key != "offers" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return nil, nil, false, fmt.Errorf("offers is not a list")
		}
		var models []string
		files := map[string]servedfile.File{}
		seen := map[string]bool{}
		for dec.More() {
			var o Offer
			if err := dec.Decode(&o); err != nil {
				return models, files, false, err
			}
			m := cleanModelName(o.Model)
			if m == "" || seen[m] || !o.Online {
				continue
			}
			if len(models) == limit {
				return models, files, true, nil
			}
			seen[m] = true
			models = append(models, m)
			if o.File != nil && o.FileBy == "station" {
				if c := servedfile.Clean(*o.File); c != nil {
					files[m] = *c
				}
			}
		}
		return models, files, false, nil
	}
	return nil, nil, false, nil
}

// noRedirects returns a copy of hc that never follows a redirect: a LAN source is an address
// the owner chose, and a redirect is not an addition (features/lan/adversarial.feature).
func noRedirects(hc *http.Client) *http.Client {
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// cleanModelName strips control characters and caps the length, so a hostile /discover
// cannot break the dial.
func cleanModelName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > lanMaxModelName {
		s = s[:lanMaxModelName]
	}
	return s
}

// TowerKeyChanged is the refusal for a pinned Tower answering with another key
// (features/join/tower_identity.feature).
func TowerKeyChanged(addr string) string {
	return "the Tower at " + addr + " is not the one you joined (its key changed). If the owner reset it, re-join with a new invite: roger join <invite>"
}
