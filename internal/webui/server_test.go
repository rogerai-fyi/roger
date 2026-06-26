package webui

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/node"
)

func testCtrl() *node.Controller {
	c := node.New(node.Config{Station: "amber-fox", Upstream: "http://127.0.0.1:1234/v1", UpstreamKey: "sk-secret-leak-canary"})
	c.SetRows([]node.ShareRow{{Model: "m1", Ctx: 8192}, {Model: "m2", Ctx: 8192}})
	return c
}

func TestStateRequiresToken(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// No token -> forbidden.
	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("state without token = %d, want 403", resp.StatusCode)
	}

	// Wrong token -> forbidden.
	resp, _ = http.Get(srv.URL + "/api/state?t=nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("state with wrong token = %d, want 403", resp.StatusCode)
	}

	// Right token -> ok.
	resp, err = http.Get(srv.URL + "/api/state?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state with token = %d, want 200", resp.StatusCode)
	}
	var snap node.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Station != "amber-fox" || len(snap.Rows) != 2 {
		t.Fatalf("snapshot = %+v, want station amber-fox + 2 rows", snap)
	}
}

func TestTokenViaHeader(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Header.Set("X-Roger-Token", s.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state with header token = %d, want 200", resp.StatusCode)
	}
}

func TestStateNeverLeaksUpstreamKey(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/state?t=" + s.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)
	if strings.Contains(string(buf[:n]), "sk-secret-leak-canary") {
		t.Fatalf("state response leaked the upstream key:\n%s", buf[:n])
	}
}

func TestStaticShellNoTokenNeeded(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200 (static shell loads without a token)", resp.StatusCode)
	}
}

func TestEventsStream(t *testing.T) {
	old := eventInterval
	eventInterval = 20 * time.Millisecond
	defer func() { eventInterval = old }()

	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/events?t="+s.Token(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("events content-type = %q, want text/event-stream", ct)
	}
	// Read the first SSE frame and assert it's a JSON snapshot.
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			var snap node.Snapshot
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snap); err != nil {
				t.Fatalf("SSE frame not a snapshot: %v (%q)", err, line)
			}
			if snap.Station != "amber-fox" {
				t.Fatalf("SSE snapshot station = %q, want amber-fox", snap.Station)
			}
			return
		}
	}
	t.Fatal("no SSE data frame received")
}

func TestEventsRequiresToken(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("events without token = %d, want 403", resp.StatusCode)
	}
}

func TestAssetsServe(t *testing.T) {
	s := New(testCtrl(), Options{})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	// The shell HTML references these; both must serve without a token (static, no data).
	for _, path := range []string{"/", "/assets/console.css", "/assets/console.js"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if n == 0 {
			t.Fatalf("GET %s served an empty body", path)
		}
	}
	// The shell references the css + js by the paths above.
	resp, _ := http.Get(srv.URL + "/")
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, ref := range []string{"/assets/console.css", "/assets/console.js"} {
		if !strings.Contains(string(html), ref) {
			t.Errorf("shell HTML does not reference %s", ref)
		}
	}
}

func TestListenReturnsLocalhostURLWithToken(t *testing.T) {
	s := New(testCtrl(), Options{})
	ln, url, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(url, "?t="+s.Token()) {
		t.Fatalf("Listen url = %q, want a 127.0.0.1 URL carrying the token", url)
	}
}
