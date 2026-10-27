package edge

// DIAL-OUT PRESENCE (features/edge/presence.feature). A member that cannot serve an inbound face -
// a phone, most of all - dials the authority and says "I am here". The authority records it as the
// SAME verified member discovery would have observed by dialing it, so a pushed node and a pulled
// node are indistinguishable on the map.
//
// The trust is identical to the pull side (verify.go): the presence is authenticated by the node's
// authority-ISSUED certificate. Only the direction of the dial changes; nothing new is trusted.

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"rogerai.fm/roger/v6/internal/store"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// PresencePath is where a node pushes its presence.
const PresencePath = "/edge/present"

// presencePrefix domain-separates a presence signature from every other thing a node key signs.
const presencePrefix = "rogerai-edge-present/v1"

// presenceFreshness bounds how far from now a presence timestamp may be - the same window
// enrollment uses. A signed presence that never goes stale is one somebody can keep and replay.
const presenceFreshness = 5 * time.Minute

// presenceMaxBody bounds what an unauthenticated caller may make the authority hold: a presence is
// a certificate (a few KB) plus a few small fields.
const presenceMaxBody = 64 << 10

// PresenceRequest is one node telling the authority it is present. Everything here is public: the
// certificate is public by nature, and the signature binds the claim to one node key and one
// moment - a captured request is useless after its freshness window and its nonce is spent.
type PresenceRequest struct {
	NodeID string   `json:"node_id"`
	Cert   string   `json:"cert"`  // PEM leaf certificate the authority issued this node
	Name   string   `json:"name"`  // the node's chosen name, used only when it is new to the fleet
	Kind   string   `json:"kind"`  // host | board | mobile
	Caps   []string `json:"caps"`  // the capabilities the node reports (self-reported, like describe)
	Addr   string   `json:"addr"`  // its LAN address, or "" for a node that only ever dials out
	Nonce  string   `json:"nonce"` // hex, 16 bytes
	TS     int64    `json:"ts"`
	Sig    string   `json:"sig"` // hex ed25519 signature over canonical(), by the node key
	// V is the presence version: absent or 1 is the original form; 2 adds the phone's vitals INSIDE
	// the signature; 3 (ruled 2026-10-13) adds Power, for ANY kind of node. Vitals or power outside the
	// version that signs them are unsigned, and are refused.
	V      int          `json:"v,omitempty"`
	Vitals *PhoneVitals `json:"vitals,omitempty"`
	Power  *PowerVitals `json:"power,omitempty"`
}

// PhoneVitals is what a phone can honestly report about itself (features/edge/fleet_window.feature).
// In v3 a phone's battery and charging travel in Power, like every other node's, and these two
// fields are refused there; thermal and foreground stay phone-only.
type PhoneVitals struct {
	BatteryPct *int   `json:"battery_pct,omitempty"`
	Charging   *bool  `json:"charging,omitempty"`
	Thermal    string `json:"thermal,omitempty"`
	Foreground *bool  `json:"foreground,omitempty"`
}

// PowerVitals is a node's power, any kind (presence v3; store.EdgePower once recorded).
type PowerVitals struct {
	BatteryPct  *int   `json:"battery_pct,omitempty"`  // 0..100, absent = no battery or unknown
	Charging    *bool  `json:"charging,omitempty"`     // charging, or full on external power
	Source      string `json:"source,omitempty"`       // "ac" | "battery" | "unknown"; absent = not reported
	LowPower    *bool  `json:"low_power,omitempty"`    // energy saver / Low Power Mode on
	MinutesLeft *int   `json:"minutes_left,omitempty"` // OS estimate of time remaining, minutes
}

// powerSources is the closed vocabulary of a power source. "solar" and "ups" are not in it: no
// operating system this reads reports them as a source, and a word nobody can report is a guess.
// MaxMinutesLeft is the most a time-remaining reading may say: a year. Past it an estimate is no
// estimate (a discharge rate near zero), and a duration that large would overflow when printed.
const MaxMinutesLeft = 365 * 24 * 60

var powerSources = map[string]bool{"": true, "ac": true, "battery": true, "unknown": true}

// presenceVersionRefusal is the exact words an authority uses for a version it does not read; a
// sender detects them to fall back (v3 to v2 to v1). An authority from before v3 says the same.
const presenceVersionRefusal = "that presence version is not one this authority reads"

// presencePrefixV3 domain-separates the v3 form, which signs the vitals and the power.
const presencePrefixV3 = "rogerai-edge-present/v3"

// presencePrefixV2 domain-separates the v2 form, which signs the vitals too.
const presencePrefixV2 = "rogerai-edge-present/v2"

var thermalStates = map[string]bool{"": true, "nominal": true, "fair": true, "serious": true, "critical": true}

// vitalsRefusal is why a presence's version, vitals or power cannot be believed, or "".
func (p PresenceRequest) vitalsRefusal() string {
	switch p.V {
	case 0, 1:
		if p.Vitals != nil {
			return "vitals arrive only in a v2 presence, where they are signed"
		}
		if p.Power != nil {
			return "power arrives only in a v3 presence, where it is signed"
		}
		return ""
	case 2:
		if p.Power != nil {
			return "power arrives only in a v3 presence, where it is signed"
		}
	case 3:
		if v := p.Vitals; v != nil && (v.BatteryPct != nil || v.Charging != nil) {
			return "in a v3 presence the battery is reported under power"
		}
		if w := p.Power; w != nil {
			if w.BatteryPct != nil && (*w.BatteryPct < 0 || *w.BatteryPct > 100) {
				return "a battery reading outside 0-100"
			}
			if !powerSources[w.Source] {
				return "a power source that does not exist"
			}
			if w.MinutesLeft != nil && *w.MinutesLeft < 0 {
				return "a negative time remaining"
			}
			if w.MinutesLeft != nil && *w.MinutesLeft > MaxMinutesLeft {
				return "a time remaining over a year"
			}
		}
	default:
		return presenceVersionRefusal
	}
	if v := p.Vitals; v != nil {
		if Kind(p.Kind) != Mobile {
			return "only a phone reports phone vitals"
		}
		if v.BatteryPct != nil && (*v.BatteryPct < 0 || *v.BatteryPct > 100) {
			return "a battery reading outside 0-100"
		}
		if !thermalStates[v.Thermal] {
			return "a thermal state that does not exist"
		}
	}
	return ""
}

func optInt(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func optBool(p *bool) string {
	switch {
	case p == nil:
		return ""
	case *p:
		return "1"
	}
	return "0"
}

// canonical is the exact byte string a presence is signed over. Every field that decides what is
// recorded is in it; the certificate is bound separately, by Authenticate deriving the same id and
// the signature being checked against the certificate's own key.
func (p PresenceRequest) canonical() []byte {
	if p.V == 3 {
		v := p.Vitals
		if v == nil {
			v = &PhoneVitals{}
		}
		w := p.Power
		if w == nil {
			w = &PowerVitals{}
		}
		// Every reading is a line; an absent one is an empty line, never a zero. Golden vectors:
		// internal/edge/testdata/presence_v3_vectors.json.
		return []byte(strings.Join([]string{
			presencePrefixV3, p.NodeID, p.Name, p.Kind, strings.Join(p.Caps, ","), p.Addr, p.Nonce,
			timeString(p.TS), v.Thermal, optBool(v.Foreground),
			optInt(w.BatteryPct), optBool(w.Charging), w.Source, optBool(w.LowPower), optInt(w.MinutesLeft),
		}, "\n"))
	}
	if p.V == 2 {
		v := p.Vitals
		if v == nil {
			v = &PhoneVitals{}
		}
		// The vitals are part of what is signed: an absent reading is an empty field, never a zero.
		return []byte(strings.Join([]string{
			presencePrefixV2, p.NodeID, p.Name, p.Kind, strings.Join(p.Caps, ","), p.Addr, p.Nonce,
			timeString(p.TS), optInt(v.BatteryPct), optBool(v.Charging), v.Thermal, optBool(v.Foreground),
		}, "\n"))
	}
	return []byte(strings.Join([]string{
		presencePrefix, p.NodeID, p.Name, p.Kind, strings.Join(p.Caps, ","), p.Addr, p.Nonce,
		timeString(p.TS),
	}, "\n"))
}

func timeString(ts int64) string { return strconv.FormatInt(ts, 10) }

// PresenceHandler serves /edge/present for one authority. It writes to the SAME fleet discovery
// writes to, so a pushed member and a dialed member are one kind of thing. `now` is injected for
// tests. Every refusal is a 403 with a short reason, the same uniform "no" enrollment gives.
func PresenceHandler(fleet *Fleet, auth *cert.Authority, now func() time.Time) http.Handler {
	return PresenceHandlerFunc(fleet, func() *cert.Authority { return auth }, now)
}

// PresenceHandlerFunc is PresenceHandler with the authority read FRESH per request, so a node
// revoked while this endpoint runs is refused at once rather than only after a restart (audit
// 2026-09-23). trust returns nil when the authority cannot be loaded, and a nil authority refuses
// the presence (fail closed).
func PresenceHandlerFunc(fleet *Fleet, trust func() *cert.Authority, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	seen := newNonceCache()
	mux := http.NewServeMux()
	mux.HandleFunc(PresencePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, presenceMaxBody))
		if err != nil {
			http.Error(w, "could not read the presence", http.StatusBadRequest)
			return
		}
		var req PresenceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "that presence could not be read", http.StatusBadRequest)
			return
		}

		// 0. The version and the vitals must be readable before anything is believed of them.
		if why := req.vitalsRefusal(); why != "" {
			refusePresence(w, why)
			return
		}
		// 1. The certificate must parse.
		leaf, err := parseLeafPEM(req.Cert)
		if err != nil {
			refusePresence(w, "that presence carries no usable certificate")
			return
		}
		// 2. It must chain to THIS authority's root, be in date, and not be revoked. Authenticate
		//    is the one check the pull side trusts too (verify.go), so push and pull agree. The
		//    authority is read fresh per request so a serial revoked since startup is honoured now.
		auth := trust()
		if auth == nil {
			refusePresence(w, "this node cannot verify presence right now")
			return
		}
		id, err := auth.Authenticate(leaf)
		if err != nil {
			refusePresence(w, "that certificate did not come from this authority")
			return
		}
		// 3. The id it names must be the one it claims, and the one its KEY derives - the misissue
		//    check the pull side makes (verify.go step 5).
		pub, ok := leaf.PublicKey.(ed25519.PublicKey)
		if !ok || id != req.NodeID || NodeID(pub) != req.NodeID {
			refusePresence(w, "that presence's identity does not match its certificate")
			return
		}
		// 4. Fresh: not far from the authority's own clock.
		if d := now().Sub(time.Unix(req.TS, 0)); d > presenceFreshness || d < -presenceFreshness {
			refusePresence(w, "that presence is stale")
			return
		}
		// 5. Signed by the certificate's key, over exactly these fields.
		sig, err := hex.DecodeString(req.Sig)
		if err != nil || !ed25519.Verify(pub, req.canonical(), sig) {
			refusePresence(w, "that presence's signature does not check out")
			return
		}
		// 6. Not a replay of one already accepted. The nonce is remembered until this request could
		//    no longer be FRESH - TS + presenceFreshness - not merely now + presenceFreshness. A
		//    request with a near-future TS stays acceptable until TS+window; expiring its nonce at
		//    now+window would prune it while the very same bytes are still fresh, opening a replay
		//    window (audit 2026-09-24).
		if !isLowerHex(req.Nonce, 32) {
			refusePresence(w, malformedNonce)
			return
		}
		if !seen.admit(req.NodeID, req.Nonce, time.Unix(req.TS, 0).Add(presenceFreshness).Unix(), now().Unix()) {
			refusePresence(w, "that presence was already seen")
			return
		}

		// Record it as a verified member - the same Observe discovery calls after a successful dial.
		caps := make([]Capability, 0, len(req.Caps))
		for _, c := range req.Caps {
			if cap := Capability(c); cap.Valid() {
				caps = append(caps, cap)
			}
		}
		obs := Observation{
			Name: req.Name, Kind: req.Kind, Caps: caps,
			Addr: req.Addr, Fingerprint: Fingerprint(leaf.Raw),
		}
		if v := req.Vitals; v != nil {
			obs.Phone = &store.EdgePhone{BatteryPct: v.BatteryPct, Charging: v.Charging, Thermal: v.Thermal,
				Foreground: v.Foreground, ReadAt: now().Unix()}
		}
		if w := req.Power; w != nil {
			pw := &store.EdgePower{BatteryPct: w.BatteryPct, Charging: w.Charging, Source: w.Source,
				LowPower: w.LowPower, MinutesLeft: w.MinutesLeft, ReadAt: now().Unix()}
			if !pw.Empty() {
				obs.Power = pw
			}
			// A phone's battery stays on its phone line too, so every surface that reads it still does.
			if Kind(req.Kind) == Mobile && obs.Phone != nil {
				obs.Phone.BatteryPct, obs.Phone.Charging = w.BatteryPct, w.Charging
			}
		}
		if _, err := fleet.Observe(req.NodeID, obs); err != nil {
			http.Error(w, "the fleet could not be written", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return mux
}

// parseLeafPEM decodes a single PEM CERTIFICATE block to an x509 certificate.
func parseLeafPEM(pemStr string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errNoCert
	}
	return x509.ParseCertificate(block.Bytes)
}

var errNoCert = &presenceError{"no certificate"}

type presenceError struct{ s string }

func (e *presenceError) Error() string { return e.s }

func refusePresence(w http.ResponseWriter, reason string) {
	http.Error(w, reason, http.StatusForbidden)
}
