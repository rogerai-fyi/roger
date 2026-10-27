package main

// The Roger app's Edge wire as its newest built version speaks it (the app source of
// 2026-10-03, not yet in the App Store), replayed byte for byte against this release's
// authority. The App Store build (1.2) has no Edge code at all; this pins that the next app
// keeps working on v7 without an update: allow-key enrollment, adopt-and-claim, presence v1
// and v2 (battery under vitals), all over the plain listener the app dials, and the plain
// listener's new TLS hints do not change a body the app reads.
//
// The requests are built here the way the Swift code builds them (EdgeEnroll.swift,
// EdgeClaim.swift, EdgePresence.swift): its JSON keys, its canonical lines, a 16-byte
// lowercase-hex nonce, account "" on enrollment. Nothing below calls a Go wire type to build
// a request, so a change to the Go side that an old app would trip over fails here.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// oldAppKey is the app's fixed golden seed (RogerAITests/EdgeTests.swift), so a run here and the
// Swift vectors describe the same device.
func oldAppKey(first byte) ed25519.PrivateKey {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = first + byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func oldAppNonce(t *testing.T) string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func oldAppSign(k ed25519.PrivateKey, lines ...string) string {
	return hex.EncodeToString(ed25519.Sign(k, []byte(strings.Join(lines, "\n"))))
}

func oldAppPost(t *testing.T, addr, path string, body map[string]any) (int, http.Header, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post("http://"+addr+path, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, out
}

// oldAppPresence is EdgePresence.makeRequest: v1 for no vitals, v2 with the four vitals appended.
func oldAppPresence(t *testing.T, node ed25519.PrivateKey, id, certPEM, name string, vitals map[string]any) map[string]any {
	nonce, ts := oldAppNonce(t), strconv.FormatInt(time.Now().Unix(), 10)
	caps := []string{"chat"}
	body := map[string]any{"node_id": id, "cert": certPEM, "name": name, "kind": "mobile", "caps": caps,
		"addr": "", "nonce": nonce, "sig": ""}
	tsN, _ := strconv.ParseInt(ts, 10, 64)
	body["ts"] = tsN
	if vitals == nil {
		body["sig"] = oldAppSign(node, "rogerai-edge-present/v1", id, name, "mobile", "chat", "", nonce, ts)
		return body
	}
	opt := func(k string) string {
		switch v := vitals[k].(type) {
		case int:
			return strconv.Itoa(v)
		case bool:
			if v {
				return "1"
			}
			return "0"
		}
		return ""
	}
	thermal, _ := vitals["thermal"].(string)
	body["v"] = 2
	body["vitals"] = vitals
	body["sig"] = oldAppSign(node, "rogerai-edge-present/v2", id, name, "mobile", "chat", "", nonce, ts,
		opt("battery_pct"), opt("charging"), thermal, opt("foreground"))
	return body
}

// servingAuthorityWithTLS is servingAuthority on a machine that enrolled itself, as a real authority
// has: it holds a node identity, so its TLS listener is up and the plain listener answers with the
// TLS hints and refuses the phone's consume paths. This is the state an older app meets.
func servingAuthorityWithTLS(t *testing.T) (*edgeHost, string) {
	t.Helper()
	offline(t)
	t.Setenv("ROGERAI_EDGE_DISCOVERY", "0")
	t.Setenv(envAuthorityTLSBind, "127.0.0.1:0")
	_, code := edgeRun(t, "edge", "authority", "local", "shed")
	require.Equal(t, 0, code)
	_, code = edgeRun(t, "edge", "enroll", "shed")
	require.Equal(t, 0, code)
	h, err := newEdgeHost("shed")
	require.NoError(t, err)
	h.startAuthority()
	t.Cleanup(h.closeListeners)
	require.NotNil(t, h.authTLSLn, "an enrolled authority serves its TLS listener")
	_, port, err := net.SplitHostPort(h.authorityAddr())
	require.NoError(t, err)
	return h, "127.0.0.1:" + port
}

func TestTheNextAppsEdgeWireIsStillHeardByThisAuthority(t *testing.T) {
	h, addr := servingAuthorityWithTLS(t)
	node, user := oldAppKey(0x00), oldAppKey(0x20)
	nodePub, userPub := hex.EncodeToString(node.Public().(ed25519.PublicKey)), hex.EncodeToString(user.Public().(ed25519.PublicKey))
	id := edgeauth.NodeID(node.Public().(ed25519.PublicKey))
	require.Equal(t, "n_56475aa75463474c0285df5dbf2bcab73da651358839e9b7", id, "the app's golden node id")

	// Allow-key enrollment (EdgeEnroll.makeRequest), the owner having run `roger edge authority allow`.
	_, code := edgeRun(t, "edge", "authority", "allow", userPub)
	require.Equal(t, 0, code)
	nonce, ts := oldAppNonce(t), time.Now().Unix()
	enroll := map[string]any{"account": "", "node_id": id, "node_key": nodePub, "name": "pixel-8", "kind": "mobile",
		"nonce": nonce, "ts": ts, "user_key": userPub,
		"sig": oldAppSign(user, "rogerai-edge-enroll/v1", "", id, nodePub, "pixel-8", "mobile", nonce, strconv.FormatInt(ts, 10))}
	status, hdr, raw := oldAppPost(t, addr, "/edge/enroll", enroll)
	require.Equal(t, http.StatusOK, status, string(raw))
	var resp struct {
		NodeID  string `json:"node_id"`
		Account string `json:"account"`
		Cert    string `json:"cert"`
		Root    string `json:"root"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp), "the body EdgeEnroll.Response decodes")
	require.Equal(t, id, resp.NodeID)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")
	require.Contains(t, resp.Root, "BEGIN CERTIFICATE")
	require.NotEmpty(t, hdr.Get("Roger-Edge-TLS"), "the new hint headers ride along; the old app ignores them")
	require.NotEmpty(t, hdr.Get("Roger-Edge-Authority"))

	// Presence v1 (a Catalyst Mac, or a phone with no readings) and v2 with the battery under vitals.
	for _, v := range []map[string]any{nil, {"battery_pct": 81, "charging": true, "thermal": "nominal", "foreground": true}} {
		status, _, raw = oldAppPost(t, addr, "/edge/present", oldAppPresence(t, node, id, resp.Cert, "pixel-8", v))
		require.Equal(t, http.StatusOK, status, "vitals %v: %s", v, raw)
	}
	m, ok, err := h.st.fleet.Get(id)
	require.NoError(t, err)
	require.True(t, ok, "the app's presence made it a verified member")
	require.Equal(t, "mobile", m.Kind)

	// The same presence twice is a replay, refused (the nonce the app sends is the 32-hex form the cache keys).
	beat := oldAppPresence(t, node, id, resp.Cert, "pixel-8", nil)
	status, _, _ = oldAppPost(t, addr, "/edge/present", beat)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = oldAppPost(t, addr, "/edge/present", beat)
	require.Equal(t, http.StatusForbidden, status)
}

func TestTheNextAppsAdoptAndClaimIsStillHeardByThisAuthority(t *testing.T) {
	_, addr := servingAuthorityWithTLS(t)
	node := oldAppKey(0x00)
	nodePub := hex.EncodeToString(node.Public().(ed25519.PublicKey))
	id := edgeauth.NodeID(node.Public().(ed25519.PublicKey))

	claim := func() (int, []byte) {
		nonce, ts := oldAppNonce(t), time.Now().Unix()
		body := map[string]any{"node_id": id, "node_key": nodePub, "name": "pixel-8", "kind": "mobile", "nonce": nonce, "ts": ts,
			"sig": oldAppSign(node, "rogerai-edge-claim/v1", id, nodePub, "pixel-8", "mobile", nonce, strconv.FormatInt(ts, 10))}
		s, _, raw := oldAppPost(t, addr, "/edge/claim", body)
		return s, raw
	}
	status, raw := claim()
	require.NotEqual(t, http.StatusOK, status, "not adopted yet: EdgeClaim.poll reads this as notYet: %s", raw)

	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, local.GrantClaim(id, "pixel-8"), "the owner adopts the phone")
	status, raw = claim()
	require.Equal(t, http.StatusOK, status, string(raw))
	var resp struct {
		NodeID string `json:"node_id"`
		Cert   string `json:"cert"`
		Root   string `json:"root"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Equal(t, id, resp.NodeID)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")

	status, _, raw = oldAppPost(t, addr, "/edge/present", oldAppPresence(t, node, id, resp.Cert, "pixel-8", nil))
	require.Equal(t, http.StatusOK, status, string(raw))
}
