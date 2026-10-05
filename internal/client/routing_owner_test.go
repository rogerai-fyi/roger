package client

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplyOwnerRoutingKeys pins the owner's remaining routing keys: each is a default or a
// ceiling a guest may only tighten.
func TestApplyOwnerRoutingKeys(t *testing.T) {
	owner := Routing{Sort: "price", Prefer: []string{"n1"}, NoFallbacks: true, Require: []string{"tools"},
		ParamsB: []float64{7, 70}, MinCtx: 8192, MaxTTFT: 1500, RequireParams: true}
	cases := []struct{ name, guest, want, err string }{
		{"absent keys take the owner's", `{"model":"m"}`,
			`{"model":"m","provider":{"sort":"price","order":["n1"],"allow_fallbacks":false,"require_parameters":true},
			  "roger":{"require":["tools"],"params_b":[7,70],"min_ctx":8192,"max_ttft_ms":1500}}`, ""},
		{"a guest may only tighten", `{"model":"m","provider":{"order":["n1"],"allow_fallbacks":true,"require_parameters":false},
			"roger":{"require":["vision"],"params_b":[30,100],"min_ctx":32768,"max_ttft_ms":900}}`,
			`{"model":"m","provider":{"sort":"price","order":["n1"],"allow_fallbacks":false,"require_parameters":true},
			  "roger":{"require":["tools","vision"],"params_b":[30,70],"min_ctx":32768,"max_ttft_ms":900}}`, ""},
		{"a guest loosening is raised back", `{"model":"m","roger":{"params_b":[1,7],"min_ctx":1024,"max_ttft_ms":5000}}`,
			`{"model":"m","provider":{"sort":"price","order":["n1"],"allow_fallbacks":false,"require_parameters":true},
			  "roger":{"require":["tools"],"params_b":[7,7],"min_ctx":8192,"max_ttft_ms":1500}}`, ""},
		{"a guest pref keeps the owner's sort out", `{"model":"m","roger":{"pref":"fast"}}`,
			`{"model":"m","provider":{"order":["n1"],"allow_fallbacks":false,"require_parameters":true},
			  "roger":{"pref":"fast","require":["tools"],"params_b":[7,70],"min_ctx":8192,"max_ttft_ms":1500}}`, ""},
		// With no fallbacks the owner's order is the ceiling (contract §9): a guest order or
		// only outside it never replaces the owner's pin.
		{"a guest order outside a no-fallback pin is refused", `{"model":"m","provider":{"order":["n2"]}}`, "",
			"order names n2, outside this session's pinned stations"},
		{"a guest only outside a no-fallback pin is refused", `{"model":"m","provider":{"only":["n2"]}}`, "",
			"only names no station inside this session's pinned stations"},
		{"a disjoint params range is refused", `{"model":"m","roger":{"params_b":[100,200]}}`, "",
			"params_b is outside this session's allowed range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := owner.Apply([]byte(c.guest))
			if c.err != "" {
				var rr *RoutingRefusal
				require.ErrorAs(t, err, &rr)
				require.Equal(t, c.err, rr.Msg)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, c.want, string(out))
		})
	}
	// the failover's re-pick hint wins over the owner's preferred order
	out, err := Routing{Prefer: []string{"n1"}, Order: []string{"n7"}}.Apply([]byte(`{"model":"m"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"m","provider":{"order":["n7"]}}`, string(out))
}

func TestDroppedNamesEveryHeaderlessOwnerKey(t *testing.T) {
	r := Routing{Models: []string{"a"}, Only: []string{"n"}, Prefer: []string{"n"}, NoFallbacks: true, Sort: "price",
		RequireParams: true, MaxReq: 1, Require: []string{"tools"}, ParamsB: []float64{1, 2}, MinCtx: 1, MaxTTFT: 1,
		TrustMin: "verified", Region: []string{"eu"}, SelfHostedOnly: true, Quantizations: []string{"Q8_0"}}
	d := r.Dropped()
	require.Len(t, d, 15)
	for _, k := range d {
		if k != "provider.require_parameters" { // a profile-only key: no flag sets it
			require.NotEmpty(t, RoutingFlag(k), k)
		}
	}
	require.Empty(t, RoutingFlag("roger.nope"))
}

func TestWarnOldBrokerAndSugarMark(t *testing.T) {
	out := captureOut(t, func() { warnOldBroker(ProxyOptions{HeaderRouting: true, Require: []string{"tools"}, MinTPS: 20}) })
	require.Equal(t, "  old broker: routing flags with no header form are dropped: --require\n", out)
	require.Empty(t, captureOut(t, func() { warnOldBroker(ProxyOptions{Require: []string{"tools"}}) }), "body mode drops nothing")
	require.Empty(t, captureOut(t, func() { warnOldBroker(ProxyOptions{HeaderRouting: true, MinTPS: 20}) }), "a header form exists")
	require.Equal(t, "  (free only)", sugarMark("m:free"))
	require.Equal(t, "  (cheapest first)", sugarMark("m:floor"))
	require.Equal(t, "  (fastest first)", sugarMark("m:nitro"))
	require.Equal(t, "", sugarMark("m"))
	require.Equal(t, "  old broker: routing flags with no header form are dropped: provider.require_parameters\n",
		captureOut(t, func() { warnOldBroker(ProxyOptions{HeaderRouting: true, RequireParams: true}) }))
}

// TestUseOnFreqKeepsConfidential is the regression pin for `roger use --freq X --confidential`:
// the private-band path built its proxy options without the confidential requirement, so the
// session silently routed to any station on the band.
func TestUseOnFreqKeepsConfidential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var got ProxyOptions
	old := newProxyHandler
	newProxyHandler = func(o ProxyOptions) http.Handler { got = o; return http.NotFoundHandler() }
	t.Cleanup(func() { newProxyHandler = old })
	var addr string
	captureServe(t, &addr)
	captureOut(t, func() {
		require.NoError(t, Use(fakeBroker(t), "u_gh_1", "m1", UseOptions{Freq: "147.520 MHz 8F3K", Confidential: true, Yes: true,
			Require: []string{"tools"}, RoutingLine: "routing: private freq"}))
	})
	require.NotEmpty(t, addr, "the channel opened")
	require.True(t, got.Confidential, "the confidential requirement reaches the proxy on the private-band path")
	require.Equal(t, []string{"tools"}, got.Require)
}

// captureOut runs fn with os.Stdout redirected and returns what it printed.
func captureOut(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = orig }()
	fn()
	_ = w.Close()
	return <-done
}

// TestOwnerRoutingCriteriaKeepsTheStricterTrust pins that the re-pick never runs below the
// owner's trust floor: a caller's lower trust_min ("any") is raised to the owner's, and a
// caller's higher one stands, so failover never prefers a station the broker will refuse.
func TestOwnerRoutingCriteriaKeepsTheStricterTrust(t *testing.T) {
	for _, tc := range []struct{ caller, owner, want string }{
		{"any", "verified", "verified"},
		{"", "verified", "verified"},
		{"confidential", "verified", "confidential"},
		{"verified", "", "verified"},
	} {
		c := Criteria{TrustMin: tc.caller}
		ownerRoutingCriteria(ProxyOptions{TrustMin: tc.owner}, &c)
		require.Equal(t, tc.want, c.TrustMin, "caller %q, owner %q", tc.caller, tc.owner)
	}
}

// TestFreeOnlyCoversEveryModelsEntry: the booth's F filter binds free-only to the whole
// request, so every models[] fallback (the owner's list or a guest's) carries :free too; the
// broker applies the variant per entry, and a bare entry could reach a paid station.
func TestFreeOnlyCoversEveryModelsEntry(t *testing.T) {
	for _, tc := range []struct{ name, guest, want string }{
		{"the owner's list", `{"model":"a"}`, `["a:free","b:free"]`},
		{"a guest list inside it", `{"model":"a","models":["b","a:free"]}`, `["b:free","a:free"]`},
	} {
		out, err := Routing{FreeOnly: true, Models: []string{"a", "b"}}.Apply([]byte(tc.guest))
		require.NoError(t, err, tc.name)
		var got struct {
			Model  string          `json:"model"`
			Models json.RawMessage `json:"models"`
		}
		require.NoError(t, json.Unmarshal(out, &got))
		require.Equal(t, "a:free", got.Model, tc.name)
		require.JSONEq(t, tc.want, string(got.Models), tc.name)
	}
}

// TestGuestModelsMayNameTheBandModel: the band's own model is always routable, so a guest
// models[] naming it (sugar included) is never refused when the owner's list omits it.
func TestGuestModelsMayNameTheBandModel(t *testing.T) {
	out, err := Routing{Models: []string{"x"}}.Apply([]byte(`{"model":"band","models":["band:nitro","x","y"]}`))
	require.NoError(t, err)
	var got struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, []string{"band:nitro", "x"}, got.Models, "the band model and the owner's are kept, y is dropped")

	_, err = Routing{Models: []string{"x"}}.Apply([]byte(`{"model":"band","models":["y"]}`))
	var rr *RoutingRefusal
	require.ErrorAs(t, err, &rr, "a list with neither is still refused")
}
