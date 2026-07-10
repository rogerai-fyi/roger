package detect

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// osaurusServer mimics an Osaurus node: an OpenAI-compatible GET /v1/models PLUS the
// distinctive root banner Osaurus returns at GET / — the fingerprint that disambiguates
// it from Jan, which shares the default :1337 port.
func osaurusServer(models ...string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		var data []map[string]string
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("Osaurus Server is running! 🦕"))
	})
	return httptest.NewServer(mux)
}

// TestIsOsaurusFingerprint: the root-banner fingerprint is true ONLY for a real Osaurus
// banner. A Jan-style server (no matching banner), a look-alike with a different root
// body, and a dead endpoint all read as not-Osaurus.
func TestIsOsaurusFingerprint(t *testing.T) {
	osa := osaurusServer("gpt-oss-20b")
	defer osa.Close()
	jan := fakeServer("jan-model") // ServeMux 404s GET / — no banner
	defer jan.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Jan API is running"))
	}))
	defer other.Close()

	cases := []struct {
		name string
		base string
		want bool
	}{
		{"osaurus banner", osa.URL + "/v1", true},
		{"jan no banner", jan.URL + "/v1", false},
		{"different banner", other.URL + "/v1", false},
		{"dead endpoint", "http://127.0.0.1:1/v1", false},
	}
	for _, c := range cases {
		if got := isOsaurus(c.base); got != c.want {
			t.Errorf("%s: isOsaurus(%q) = %v, want %v", c.name, c.base, got, c.want)
		}
	}
}

// TestIsOsaurusExported: the exported wrapper `roger share` uses to set Config.Osaurus once at
// share time - a thin wrapper over isOsaurus, tested directly so the earning/hardening entry point
// is covered (not only via the OSAURUS_E2E-gated live test).
func TestIsOsaurusExported(t *testing.T) {
	osa := osaurusServer("gpt-oss-20b")
	defer osa.Close()
	jan := fakeServer("jan-model")
	defer jan.Close()
	if !IsOsaurus(osa.URL + "/v1") {
		t.Error("IsOsaurus should be true for a real Osaurus banner")
	}
	if IsOsaurus(jan.URL + "/v1") {
		t.Error("IsOsaurus should be false for a non-Osaurus server")
	}
	if IsOsaurus("http://127.0.0.1:1/v1") {
		t.Error("IsOsaurus should be false for a dead endpoint")
	}
}

// TestDetectBrandsOsaurus: a server sitting on the :1337 slot (labeled "jan" by port)
// that fingerprints as Osaurus is re-branded "osaurus" so `roger share` labels the offer
// with the true backend.
func TestDetectBrandsOsaurus(t *testing.T) {
	defer quietSources(t)()
	srv := osaurusServer("gpt-oss-20b")
	defer srv.Close()

	old := probes
	probes = []struct{ name, base string }{{"jan", srv.URL + "/v1"}}
	defer func() { probes = old }()

	found, _ := DetectFull()
	if len(found) != 1 {
		t.Fatalf("found %d servers, want 1: %+v", len(found), found)
	}
	if found[0].Name != "osaurus" {
		t.Errorf("Osaurus on the jan/:1337 slot should re-brand to osaurus, got %q", found[0].Name)
	}
}

// TestDetectKeepsJanName: a genuine Jan server (no Osaurus banner) on the same slot keeps
// its "jan" label — the fingerprint must not re-brand every :1337 server.
func TestDetectKeepsJanName(t *testing.T) {
	defer quietSources(t)()
	srv := fakeServer("jan-model") // no banner at GET /
	defer srv.Close()

	old := probes
	probes = []struct{ name, base string }{{"jan", srv.URL + "/v1"}}
	defer func() { probes = old }()

	found, _ := DetectFull()
	if len(found) != 1 || found[0].Name != "jan" {
		t.Fatalf("a non-Osaurus server must keep its jan label, got %+v", found)
	}
}

// TestProbeKeyBrandsOsaurus: the guided-fallback "paste a URL" path also re-brands an
// Osaurus upstream, so a manually-configured Osaurus offer is labeled correctly.
func TestProbeKeyBrandsOsaurus(t *testing.T) {
	srv := osaurusServer("gpt-oss-20b")
	defer srv.Close()

	f, st := ProbeKey(srv.URL, "")
	if st != Reachable {
		t.Fatalf("ProbeKey status = %v, want Reachable", st)
	}
	if f.Name != "osaurus" {
		t.Errorf("ProbeKey should brand an Osaurus upstream, got name %q", f.Name)
	}
}
