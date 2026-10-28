package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Live E2E 2026-10-28: `roger use <model> --node X` previewed the cheapest station overall as
// "the strongest match" while routing was pinned to X and X served. The preview names the station
// the session's routing tries first.
func TestRoutedStation(t *testing.T) {
	const none = "(none on air)"
	offers := []Offer{
		{NodeID: "house", Model: "m", PriceOut: 0.5, PriceIn: 0.1, TPS: 90, Online: true},
		{NodeID: "lab", Model: "m", PriceOut: 1.5, PriceIn: 0.3, TPS: 40, Online: true},
		{NodeID: "attic", Model: "m", PriceOut: 1.0, PriceIn: 0.2, TPS: 20, Online: true},
		{NodeID: "asleep", Model: "m", PriceOut: 0.1, Online: false},
		{NodeID: "other-band", Model: "n", PriceOut: 0.1, Online: true},
	}
	for _, tc := range []struct {
		name    string
		maxOut  float64
		opt     UseOptions
		want    string // "" = nothing pins the choice; none = the routing allows no station on air
		wantWhy string
	}{
		{name: "no routing: nothing pins", opt: UseOptions{}},
		{name: "--node pins a station on air", opt: UseOptions{Prefer: []string{"lab"}, NoFallbacks: true}, want: "lab", wantWhy: "pinned by your routing"},
		{name: "--order: the first on air wins", opt: UseOptions{Prefer: []string{"asleep", "attic", "lab"}}, want: "attic", wantWhy: "pinned by your routing"},
		{name: "--order naming a station of another band skips it", opt: UseOptions{Prefer: []string{"other-band", "lab"}}, want: "lab", wantWhy: "pinned by your routing"},
		{name: "--only: the cheapest it allows", opt: UseOptions{Only: []string{"lab", "attic"}}, want: "attic", wantWhy: "the cheapest your routing allows"},
		{name: "--node inside --only", opt: UseOptions{Only: []string{"lab", "attic"}, Prefer: []string{"lab"}, NoFallbacks: true}, want: "lab", wantWhy: "pinned by your routing"},
		{name: "--order outside --only is not tried", opt: UseOptions{Only: []string{"attic"}, Prefer: []string{"lab"}}, want: "attic", wantWhy: "the cheapest your routing allows"},
		{name: "--exclude the cheapest", opt: UseOptions{ExcludeNodes: []string{"house"}}, want: "attic", wantWhy: "the cheapest your routing allows"},
		{name: "--order excluded is not tried", opt: UseOptions{Prefer: []string{"lab"}, ExcludeNodes: []string{"lab"}}, want: "house", wantWhy: "the cheapest your routing allows"},
		{name: "--node excluded: nothing it allows", opt: UseOptions{Prefer: []string{"lab"}, ExcludeNodes: []string{"lab"}, NoFallbacks: true}, want: none},
		{name: "--order off air: the router falls back to the cheapest", opt: UseOptions{Prefer: []string{"asleep"}}, want: "house", wantWhy: "the cheapest your routing allows"},
		{name: "--node off air: no fallback, nothing it allows", opt: UseOptions{Prefer: []string{"asleep"}, NoFallbacks: true}, want: none},
		{name: "--node of another band: nothing it allows", opt: UseOptions{Prefer: []string{"other-band"}, NoFallbacks: true}, want: none},
		{name: "--only with nothing on air: nothing it allows", opt: UseOptions{Only: []string{"asleep"}}, want: none},
		{name: "--exclude every station: nothing it allows", opt: UseOptions{ExcludeNodes: []string{"house", "lab", "attic"}}, want: none},
		{name: "--order above the out-price cap is not tried", maxOut: 1.2, opt: UseOptions{Prefer: []string{"lab"}}, want: "house", wantWhy: "the cheapest your routing allows"},
		{name: "--node above the out-price cap: nothing it allows", maxOut: 1.2, opt: UseOptions{Prefer: []string{"lab"}, NoFallbacks: true}, want: none},
		{name: "--node at the out-price cap is tried", maxOut: 1.5, opt: UseOptions{Prefer: []string{"lab"}, NoFallbacks: true}, want: "lab", wantWhy: "pinned by your routing"},
		{name: "--only, all above the cap: nothing it allows", maxOut: 0.9, opt: UseOptions{Only: []string{"lab", "attic"}}, want: none},
		{name: "--no-fallbacks alone pins nothing", opt: UseOptions{NoFallbacks: true}},
		{name: "a cap alone pins nothing", maxOut: 0.9, opt: UseOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, why, ok := routedStation(offers, "m", tc.maxOut, tc.opt)
			if tc.want == "" {
				require.False(t, ok, "picked %+v", o)
				return
			}
			require.True(t, ok)
			if tc.want == none {
				require.Empty(t, o.NodeID, "nothing the routing allows is on air, so no station is named")
				return
			}
			require.Equal(t, tc.want, o.NodeID)
			require.Equal(t, tc.wantWhy, why)
		})
	}
}

// The preview's station line, its price and the channel line name the pinned station.
func TestUsePreviewNamesThePinnedStation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"offers":[` +
			`{"node_id":"house-or-gpt-oss-20b","model":"m","price_out":0.5,"price_in":0.1,"tps":90,"online":true},` +
			`{"node_id":"lab","model":"m","price_out":1.5,"price_in":0.3,"tps":40,"online":true}]}`))
	}))
	t.Cleanup(srv.Close)
	var addr string
	captureServe(t, &addr)
	for _, tc := range []struct {
		name, station, why, price string
		opt                       UseOptions
	}{
		{name: "pinned with --node", station: "@lab", why: "(pinned by your routing)", price: "1.50 $/1M out", opt: UseOptions{Prefer: []string{"lab"}}},
		{name: "no pin", station: "@house-or-gpt-oss-20b", why: "(the strongest match)", price: "0.50 $/1M out", opt: UseOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opt.Yes, tc.opt.MaxOut, tc.opt.Port = true, 5, freeTestPort(t)
			out := captureOut(t, func() { require.NoError(t, Use(srv.URL, "u_gh_1", "m", tc.opt)) })
			var line string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "station        ") {
					line = l
				}
			}
			require.Contains(t, line, tc.station, out)
			require.Contains(t, line, tc.why, out)
			require.Contains(t, out, "price now      "+tc.price, out)
			require.Contains(t, out, "CHANNEL OPEN m via "+tc.station, out)
			require.Contains(t, out, "locking strongest "+tc.station, out)
		})
	}
}

// When nothing the routing allows is on air (a --node station off air, which never falls back, or
// --only / --exclude leaving none), the preview says so and names no other station; the channel
// still opens, since a station may come on air while the session runs.
func TestUsePreviewWhenTheRoutingAllowsNothingOnAir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"offers":[{"node_id":"house","model":"m","price_out":0.5,"online":true},` +
			`{"node_id":"attic","model":"m","price_out":1.0,"online":true},` +
			`{"node_id":"lab","model":"m","price_out":1.5,"online":false}]}`))
	}))
	t.Cleanup(srv.Close)
	for _, tc := range []struct {
		name string
		opt  UseOptions
	}{
		{name: "--node off air", opt: UseOptions{Prefer: []string{"lab"}, NoFallbacks: true}},
		{name: "--node off air, the cheapest excluded", opt: UseOptions{Prefer: []string{"lab"}, NoFallbacks: true, ExcludeNodes: []string{"house"}}},
		{name: "--only off air", opt: UseOptions{Only: []string{"lab"}}},
		{name: "--exclude every station on air", opt: UseOptions{ExcludeNodes: []string{"house", "attic"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var addr string
			captureServe(t, &addr)
			tc.opt.Yes, tc.opt.MaxOut, tc.opt.Port = true, 5, freeTestPort(t)
			out := captureOut(t, func() { require.NoError(t, Use(srv.URL, "u_gh_1", "m", tc.opt)) })
			require.Contains(t, out, "station        none your routing allows is on air right now", out)
			require.Contains(t, out, "CHANNEL OPEN m via your routing", out)
			require.NotContains(t, out, "@house", out)
			require.NotContains(t, out, "@attic", out)
			require.NotContains(t, out, "price now", "no station's price is quoted as the session's")
			require.NotEmpty(t, addr, "the channel still opens: a station may come on air while it runs")
		})
	}
}
