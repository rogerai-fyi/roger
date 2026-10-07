package lanmode

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/netpriv"
)

func TestParseEnv(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
		ok   bool
	}{
		{"1", true, true}, {"true", true, true}, {"TRUE", true, true}, {"yes", true, true}, {"on", true, true},
		{"0", false, true}, {"false", false, true}, {"no", false, true}, {"off", false, true}, {" On ", true, true},
		{"sometimes", false, false}, {"", false, false}, {"2", false, false},
	} {
		got, err := ParseEnv(tc.in)
		if !tc.ok {
			require.Error(t, err, tc.in)
			require.Contains(t, err.Error(), "ROGER_LAN")
			continue
		}
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}
}

func TestDecide(t *testing.T) {
	type in struct {
		cfg, env string
		envSet   bool
		lan, no  bool
	}
	for _, tc := range []struct {
		in   in
		lan  bool
		src  Source
		fail bool
	}{
		{in{}, false, SrcDefault, false},
		{in{cfg: "lan"}, true, SrcConfig, false},
		{in{env: "1", envSet: true}, true, SrcEnv, false},
		{in{cfg: "lan", env: "0", envSet: true}, false, SrcEnv, false},
		{in{lan: true}, true, SrcFlag, false},
		{in{cfg: "lan", no: true}, false, SrcFlag, false},
		{in{env: "0", envSet: true, lan: true}, true, SrcFlag, false},
		{in{cfg: "lan", env: "1", envSet: true, no: true}, false, SrcFlag, false},
		{in{lan: true, no: true}, false, "", true},
		{in{env: "sometimes", envSet: true}, false, "", true},
		{in{env: "sometimes", envSet: true, lan: true}, true, SrcFlag, false}, // the flag decides; env is never read
		{in{cfg: "bogus"}, false, "", true},
		{in{cfg: "bogus", env: "1", envSet: true}, true, SrcEnv, false}, // the env decides; config is never read
	} {
		m, err := Decide(Inputs{FlagLAN: tc.in.lan, FlagNoLAN: tc.in.no, EnvLAN: tc.in.env, EnvLANSet: tc.in.envSet, Config: tc.in.cfg})
		if tc.fail {
			require.Error(t, err, "%+v", tc.in)
			continue
		}
		require.NoError(t, err, "%+v", tc.in)
		require.Equal(t, Mode{LAN: tc.lan, Source: tc.src}, m, "%+v", tc.in)
	}
}

func TestConfigMode(t *testing.T) {
	v, err := ConfigMode("lan")
	require.NoError(t, err)
	require.Equal(t, "lan", v)
	v, err = ConfigMode("default")
	require.NoError(t, err)
	require.Equal(t, "", v)
	_, err = ConfigMode("offline")
	require.Error(t, err)
	require.Contains(t, err.Error(), "lan")
	require.Contains(t, err.Error(), "default")
}

func TestParseAddrAndClassifyLiteral(t *testing.T) {
	for _, tc := range []struct {
		in    string
		local bool
	}{
		{"127.0.0.1:8787", true}, {"[::1]:8787", true}, {"10.0.0.5:8787", true},
		{"172.16.0.9:8787", true}, {"172.31.255.254:8787", true}, {"172.32.0.1:8787", false},
		{"192.168.1.69:8787", true}, {"[fd12:3456::1]:8787", true}, {"169.254.10.10:8787", true},
		{"[fe80::1%eth0]:8787", true}, {"100.64.0.1:8787", false}, {"8.8.8.8:8787", false},
		{"0.0.0.0:8787", false}, {"[::]:8787", false}, {"255.255.255.255:8787", false},
		{"224.0.0.251:5353", false}, {"[::ffff:203.0.113.7]:8787", false}, {"[::ffff:192.168.1.20]:8787", true},
	} {
		v := ClassifyLiteral(tc.in)
		require.Equal(t, tc.local, v.Local, "%s: %s", tc.in, v.Reason)
		if !tc.local {
			require.NotEmpty(t, v.Reason, tc.in)
		}
	}
	require.True(t, ClassifyLiteral("100.64.0.1:8787").CGNAT)
	require.False(t, ClassifyLiteral("8.8.8.8:8787").CGNAT)
}

func TestParseAddr(t *testing.T) {
	a, err := ParseAddr("192.168.1.20")
	require.NoError(t, err)
	require.Equal(t, "192.168.1.20:8787", a.String(), "the standalone Tower's default port")
	a, err = ParseAddr("http://192.168.1.20:9000/")
	require.NoError(t, err)
	require.Equal(t, "192.168.1.20:9000", a.String())
	a, err = ParseAddr("tower.lan:8787")
	require.NoError(t, err)
	require.False(t, a.Literal())
	_, err = ParseAddr("[fe80::1]:8787")
	require.Error(t, err)
	require.Contains(t, err.Error(), "%eth0")
	_, err = ParseAddr("")
	require.Error(t, err)
	_, err = ParseAddr("192.168.1.20:notaport")
	require.Error(t, err)
	require.Equal(t, "http://192.168.1.20:8787", mustAddr(t, "192.168.1.20").Base())
	require.Equal(t, "http://[fe80::1%25eth0]:8787", mustAddr(t, "[fe80::1%eth0]:8787").Base())
}

func mustAddr(t *testing.T, s string) Addr {
	a, err := ParseAddr(s)
	require.NoError(t, err)
	return a
}

func TestClassifyHostnameEveryAddressMustBeLocal(t *testing.T) {
	ctx := context.Background()
	res := func(ips ...string) Resolver {
		return func(context.Context, string) ([]net.IP, error) {
			var out []net.IP
			for _, s := range ips {
				out = append(out, net.ParseIP(s))
			}
			return out, nil
		}
	}
	a := mustAddr(t, "tower.lan:8787")
	require.True(t, Classify(ctx, a, res("192.168.1.20", "10.0.0.4")).Local)
	v := Classify(ctx, a, res("192.168.1.20", "203.0.113.7"))
	require.False(t, v.Local)
	require.Contains(t, v.Reason, "resolves to a public address")
	v = Classify(ctx, a, func(context.Context, string) ([]net.IP, error) { return nil, errors.New("nxdomain") })
	require.False(t, v.Local)
	require.Contains(t, v.Reason, "nxdomain")
	v = Classify(ctx, a, res())
	require.False(t, v.Local)
	// A literal never consults the resolver.
	require.True(t, Classify(ctx, mustAddr(t, "192.168.1.20:8787"), nil).Local)
}

func TestIsLocalIPIsTheHomeSetPlusLinkLocal(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.20.0.1", "192.168.0.1", "fd00::1", "8.8.8.8", "100.64.0.1"} {
		ip := net.ParseIP(s)
		require.Equal(t, netpriv.IsHome(ip), IsLocalIP(ip), s)
	}
	require.True(t, IsLocalIP(net.ParseIP("169.254.1.1")))
	require.True(t, IsLocalIP(net.ParseIP("fe80::1")))
	require.False(t, IsLocalIP(nil))
}

func TestCheckAndIsTower(t *testing.T) {
	ResetTowers()
	t.Cleanup(ResetTowers)
	tower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/local/poll" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`)) // the Tower's uniform 401 body
			return
		}
		http.NotFound(w, r)
	}))
	defer tower.Close()
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer broker.Close()
	catchAll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer catchAll.Close()
	other401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_error"}`))
	}))
	defer other401.Close()
	ctx := context.Background()

	require.Equal(t, ProbeTowerYes, Check(ctx, nil, tower.URL))
	require.Equal(t, ProbeNotTower, Check(ctx, nil, broker.URL), "a broker 404s /local/poll")
	require.Equal(t, ProbeNotTower, Check(ctx, nil, catchAll.URL), "a catch-all 200 is not a Tower")
	require.Equal(t, ProbeNotTower, Check(ctx, nil, other401.URL), "a 401 without the uniform body is not a Tower")
	require.Equal(t, ProbeNoAnswer, Check(ctx, nil, "http://127.0.0.1:1"))
	require.Equal(t, ProbeNoAnswer, Check(ctx, nil, "://bad"))

	require.True(t, IsTower(ctx, nil, tower.URL))
	require.False(t, IsTower(ctx, nil, broker.URL))
	// "localhost" is the loopback host, case-insensitively (RFC 4343).
	u, _ := url.Parse(tower.URL)
	require.True(t, IsTower(ctx, nil, "http://localhost:"+u.Port()))
	require.True(t, IsTower(ctx, nil, "http://LOCALHOST:"+u.Port()))
	for _, bad := range []string{"https://broker.example", "http://203.0.113.5:8787", "http://roggentoo:8787", "://bad", ""} {
		require.False(t, IsTower(ctx, nil, bad), bad+" is never probed as a Tower")
	}
	// A confirmed Tower is remembered for the nonce decision.
	tu, _ := url.Parse(tower.URL + "/v1/chat/completions")
	require.True(t, WantsNonce(tu, nil), "a Tower this process confirmed gets the nonce, loopback included")
	ResetTowers()
	require.False(t, WantsNonce(tu, nil), "a loopback address nobody confirmed does not")
}

// TestDecideEdgeOnly pins the one precedence for LAN mode and edge-only together
// (features/lan/edge_only.feature section 1): flag, then environment, then config.
func TestDecideEdgeOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Inputs
		want Mode
		err  string
	}{
		{"--edge-only implies LAN", Inputs{FlagEdgeOnly: true}, Mode{LAN: true, EdgeOnly: true, Source: SrcFlag}, ""},
		{"env edge-only", Inputs{EnvEdgeOnly: "1", EnvEdgeOnlySet: true}, Mode{LAN: true, EdgeOnly: true, Source: SrcEnv}, ""},
		{"config edge-only", Inputs{Config: "lan-edge-only"}, Mode{LAN: true, EdgeOnly: true, Source: SrcConfig}, ""},
		{"env 0 over config edge-only keeps LAN", Inputs{Config: "lan-edge-only", EnvEdgeOnly: "0", EnvEdgeOnlySet: true}, Mode{LAN: true, Source: SrcEnv}, ""},
		{"env 0 with no LAN anywhere", Inputs{EnvEdgeOnly: "0", EnvEdgeOnlySet: true}, Mode{Source: SrcEnv}, ""},
		{"--lan turns edge-only off", Inputs{FlagLAN: true, Config: "lan-edge-only"}, Mode{LAN: true, Source: SrcFlag}, ""},
		{"--no-lan turns both off", Inputs{FlagNoLAN: true, Config: "lan-edge-only"}, Mode{Source: SrcFlag}, ""},
		{"flag beats env", Inputs{FlagEdgeOnly: true, EnvEdgeOnly: "0", EnvEdgeOnlySet: true}, Mode{LAN: true, EdgeOnly: true, Source: SrcFlag}, ""},
		{"ROGER_LAN=1 is LAN without edge-only", Inputs{EnvLAN: "1", EnvLANSet: true, Config: "lan-edge-only"}, Mode{LAN: true, Source: SrcEnv}, ""},
		{"--edge-only --no-lan conflict", Inputs{FlagEdgeOnly: true, FlagNoLAN: true}, Mode{}, "--edge-only and --no-lan conflict"},
		{"--lan --no-lan conflict", Inputs{FlagLAN: true, FlagNoLAN: true}, Mode{}, "--lan and --no-lan conflict"},
		{"env edge-only on, ROGER_LAN off", Inputs{EnvEdgeOnly: "1", EnvEdgeOnlySet: true, EnvLAN: "0", EnvLANSet: true}, Mode{}, "ROGER_LAN_EDGE_ONLY=1 and ROGER_LAN=0 conflict"},
		{"unknown edge-only value", Inputs{EnvEdgeOnly: "maybe", EnvEdgeOnlySet: true}, Mode{}, "ROGER_LAN_EDGE_ONLY"},
		{"unknown ROGER_LAN value", Inputs{EnvLAN: "2", EnvLANSet: true}, Mode{}, "ROGER_LAN"},
		{"unknown config", Inputs{Config: "lan-only"}, Mode{}, "lan-edge-only"},
		{"nothing set", Inputs{}, Mode{Source: SrcDefault}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decide(tc.in)
			if tc.err != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestConfigModeEdgeOnly(t *testing.T) {
	m, err := ConfigMode(" LAN-EDGE-ONLY ")
	require.NoError(t, err)
	require.Equal(t, "lan-edge-only", m)
	_, err = ConfigMode("edge")
	require.ErrorContains(t, err, "lan, lan-edge-only or default")
}

func TestParseEdgeOnlyEnvNamesAcceptedValues(t *testing.T) {
	_, err := ParseEdgeOnlyEnv("2")
	require.ErrorContains(t, err, "ROGER_LAN_EDGE_ONLY")
	require.ErrorContains(t, err, "1, true, yes or on")
}
