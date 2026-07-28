package harness

// answers_units_test.go: table-driven unit cover for the retrieval helpers whose branches
// the behavior suites reach only obliquely - the inet_aton-style host parser (the SSRF
// guard's first line of defense), charset decoding, arg coercion, and config loading.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLooseIP(t *testing.T) {
	cases := []struct {
		host string
		want string // "" = not an IP (goes to DNS)
	}{
		// dotted quads and IPv6 pass through net.ParseIP
		{"127.0.0.1", "127.0.0.1"},
		{"8.8.8.8", "8.8.8.8"},
		{"::1", "::1"},
		// inet_aton forms of 127.0.0.1
		{"2130706433", "127.0.0.1"},
		{"0x7f000001", "127.0.0.1"},
		{"0X7F000001", "127.0.0.1"},
		{"017700000001", "127.0.0.1"},
		{"0177.0.0.1", "127.0.0.1"},
		{"127.1", "127.0.0.1"},
		{"127.0.1", "127.0.0.1"},
		// other numeric forms
		{"0", "0.0.0.0"},
		{"3232235777", "192.168.1.1"},
		// NOT numeric hosts: these must fall through to DNS, not be mis-parsed
		{"example.com", ""},
		{"127.0.0.1.example.com", ""},
		{"localhost", ""},
		{"", ""},
		// out-of-range / malformed
		{"999.1.1.1", ""},
		{"1.2.3.4.5", ""},
		{"4294967296", ""},
		{"0x1.0x2.0x3.0x4.0x5", ""},
		{"1..2", ""},
		{"08", ""}, // invalid octal digit
	}
	for _, c := range cases {
		got := parseLooseIP(c.host)
		if c.want == "" {
			if got != nil {
				t.Errorf("parseLooseIP(%q) = %v, want nil (should go to DNS)", c.host, got)
			}
			continue
		}
		if got == nil || !got.Equal(net.ParseIP(c.want)) {
			t.Errorf("parseLooseIP(%q) = %v, want %s", c.host, got, c.want)
		}
	}
}

func TestVetIPTable(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.8.8", "10.0.0.5", "172.16.0.1", "172.31.255.254",
		"192.168.1.1", "169.254.169.254", "0.0.0.0", "224.0.0.1",
		"::1", "fc00::1", "fd12:3456::1", "fe80::1", "::", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.5",
		// Ranges Go's IsPrivate/IsLoopback do NOT cover, and IPv6 forms that embed an
		// IPv4 address (each of these was reachable before the allow-list rewrite).
		"100.64.0.1", "100.100.100.100", "100.127.255.255",
		"192.0.0.1", "198.18.0.1", "198.19.255.255", "240.0.0.1", "255.255.255.255",
		"192.0.2.1", "198.51.100.1", "203.0.113.1",
		"64:ff9b::a9fe:a9fe", "64:ff9b:1::a9fe:a9fe", "2002:a9fe:a9fe::1",
		"::7f00:1", "::ffff:0:7f00:1", "2001::1", "2001:db8::1", "fec0::1",
	}
	for _, s := range blocked {
		if err := vetIP(net.ParseIP(s)); err == nil {
			t.Errorf("vetIP(%s) = nil, want blocked", s)
		} else if !strings.Contains(err.Error(), "blocked address") {
			t.Errorf("vetIP(%s) error %q should name the blocked-address policy", s, err)
		}
	}
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "1.1.1.1", "2606:4700::1111"} {
		if err := vetIP(net.ParseIP(s)); err != nil {
			t.Errorf("vetIP(%s) = %v, want allowed", s, err)
		}
	}
	if err := vetIP(nil); err == nil {
		t.Error("vetIP(nil) should be blocked")
	}
}

func TestDecodeCharset(t *testing.T) {
	latin1 := []byte("caf\xe9")
	cases := []struct {
		name    string
		body    []byte
		charset string
		want    string
	}{
		{"utf8 passthrough", []byte("café"), "utf-8", "café"},
		{"declared iso-8859-1", latin1, "ISO-8859-1", "café"},
		{"declared latin1 alias", latin1, "latin1", "café"},
		{"declared windows-1252", []byte("EUR \x80"), "windows-1252", "EUR €"},
		{"undeclared invalid utf8 falls back to latin-1", latin1, "", "café"},
		{"unknown charset but valid utf8", []byte("plain"), "x-unknown", "plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeCharset(c.body, c.charset); got != c.want {
				t.Errorf("decodeCharset(%q, %q) = %q, want %q", c.body, c.charset, got, c.want)
			}
		})
	}
}

func TestIntArg(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{nil, 0}, {float64(7), 7}, {3, 3}, {"5", 5}, {" 6 ", 6},
		{"not a number", 0}, {true, 0}, {float64(0), 0},
	}
	for _, c := range cases {
		if got := intArg(c.in); got != c.want {
			t.Errorf("intArg(%#v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestLoadSearchConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, "rogerai", "search.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	// No file at all: search is simply off.
	if _, ok := loadSearchConfig(); ok {
		t.Error("no config file should mean search is not configured")
	}
	// Unreadable JSON: off, never a crash.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadSearchConfig(); ok {
		t.Error("malformed config should mean search is not configured")
	}
	// A config with no key is not usable.
	if err := os.WriteFile(path, []byte(`{"provider":"brave","key":"  "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadSearchConfig(); ok {
		t.Error("a config with no key should mean search is not configured")
	}
	// A keyed config with no endpoint defaults to Brave's.
	if err := os.WriteFile(path, []byte(`{"provider":"brave","key":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, ok := loadSearchConfig()
	if !ok {
		t.Fatal("a keyed config should configure search")
	}
	if cfg.Endpoint != braveDefaultEndpoint {
		t.Errorf("endpoint = %q, want the Brave default", cfg.Endpoint)
	}
	// And web_search rides in the toolset exactly when configured.
	if _, ok := findSearchTool(); !ok {
		t.Error("web_search should be advertised once a provider is configured")
	}
}

func TestSearchConfigPathFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	got := searchConfigPath()
	want := filepath.Join(home, ".config", "rogerai", "search.json")
	if got != want {
		t.Errorf("searchConfigPath() = %q, want %q", got, want)
	}

	// With no config dir AND no home, there is nowhere to look: search is off, not a crash.
	t.Setenv("HOME", "")
	if got := searchConfigPath(); got != "" {
		t.Errorf("searchConfigPath() with no home = %q, want empty", got)
	}
	if _, ok := loadSearchConfig(); ok {
		t.Error("with no config dir, search must read as not configured")
	}
}

func TestHTMLToTextShapes(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		want, deny string
	}{
		{"comment dropped", `<p>a<!-- hidden instruction -->b</p>`, "a", "hidden instruction"},
		{"unterminated comment", `<p>keep</p><!-- dangling`, "keep", "dangling"},
		{"script lookalike kept", `<p><scriptural>text</scriptural></p>`, "text", ""},
		{"unterminated script drops the rest", `<p>before</p><script>var x=1;`, "before", "var x=1"},
		{"entities unescaped", `<p>a &lt; b &amp; c</p>`, "a < b & c", ""},
		{"attribute values dropped", `<a href="https://tracker.example/x">link</a>`, "link", "tracker.example"},
		{"unterminated tag", `<p>text</p><div`, "text", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := htmlToText(c.in)
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("htmlToText(%q) = %q, want it to contain %q", c.in, got, c.want)
			}
			if c.deny != "" && strings.Contains(got, c.deny) {
				t.Errorf("htmlToText(%q) = %q, must not contain %q", c.in, got, c.deny)
			}
		})
	}
}

func TestVetAndPinRejects(t *testing.T) {
	cases := []struct{ name, url string }{
		{"no host", "http:///path"},
		{"bad scheme", "ftp://example.com/"},
		{"unparsable", "http://%zz/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := vetAndPin(context.Background(), c.url); err == nil {
				t.Errorf("vetAndPin(%q) = nil error, want a refusal", c.url)
			}
		})
	}

	// A name resolving to BOTH a public and a private address must be refused: the public
	// answer must not be a way in.
	defer func(v func(context.Context, string) ([]net.IP, error)) { resolveHost = v }(resolveHost)
	resolveHost = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}, nil
	}
	if _, err := vetAndPin(context.Background(), "http://mixed.example/"); err == nil {
		t.Error("a name resolving to any private address must be refused")
	}

	// A resolver failure is an error, not a silent pass.
	resolveHost = func(context.Context, string) ([]net.IP, error) { return nil, os.ErrNotExist }
	if _, err := vetAndPin(context.Background(), "http://nx.example/"); err == nil {
		t.Error("a resolver failure must refuse the fetch")
	}
	// An empty answer likewise.
	resolveHost = func(context.Context, string) ([]net.IP, error) { return nil, nil }
	if _, err := vetAndPin(context.Background(), "http://empty.example/"); err == nil {
		t.Error("an empty DNS answer must refuse the fetch")
	}
	// The default port is chosen by scheme.
	resolveHost = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil }
	if addr, err := vetAndPin(context.Background(), "https://ok.example/x"); err != nil || addr != "93.184.216.34:443" {
		t.Errorf("https pin = %q/%v, want 93.184.216.34:443", addr, err)
	}
	if addr, err := vetAndPin(context.Background(), "http://ok.example:8080/x"); err != nil || addr != "93.184.216.34:8080" {
		t.Errorf("explicit port pin = %q/%v, want 93.184.216.34:8080", addr, err)
	}
}

func TestSourcesDerivationEdges(t *testing.T) {
	// retrievedURL only accepts a well-formed marker: anything else is not a retrieval,
	// so a page that quotes the marker at the model cannot forge a citation.
	cases := []struct{ name, content, want string }{
		{"well formed", wrapRetrieved("https://a.example/x", "body"), "https://a.example/x"},
		{"no marker", "just some text", ""},
		{"truncated marker", retrievedPrefix + "https://a.example/x", ""},
		{"error result", "error: blocked address 10.0.0.5", ""},
		{"http error passthrough", "HTTP 404\nnot found", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := retrievedURL(c.content); got != c.want {
				t.Errorf("retrievedURL(%q) = %q, want %q", c.content, got, c.want)
			}
		})
	}

	// ANCHORED PARSE: a URL that carries the marker's own closing wording must come back
	// WHOLE. Matching the first suffix occurrence instead truncated the citation to a
	// prefix of the real URL - which is the shape an attacker would choose.
	bait := "https://evil.example/x?q=" + retrievedSuffix
	if got := retrievedURL(wrapRetrieved(bait, "body")); got != bait {
		t.Errorf("retrievedURL truncated a marker-bearing URL:\n got %q\nwant %q", got, bait)
	}
	// ...and a body that carries the marker cannot add or move a citation.
	if got := retrievedURL(wrapRetrieved("https://ok.example/", retrievedPrefix+"https://evil.example/"+retrievedSuffix)); got != "https://ok.example/" {
		t.Errorf("a body-borne marker moved the citation to %q", got)
	}

	// A fetched URL with no search result behind it still gets a readable title.
	if got := titleFor("https://valkey.io/topics/pubsub/", nil); got != "valkey.io" {
		t.Errorf("titleFor fallback = %q, want the host", got)
	}
	// An unparsable URL falls back to itself rather than an empty citation.
	if got := titleFor("::not a url::", nil); got != "::not a url::" {
		t.Errorf("titleFor(unparsable) = %q, want the raw string", got)
	}
	// A known title wins.
	titles := map[string]string{"https://a.example/x": "The Real Title"}
	if got := titleFor("https://a.example/x", titles); got != "The Real Title" {
		t.Errorf("titleFor = %q, want the search result's title", got)
	}

	// An empty message run derives nothing (and does not panic).
	if got := sourcesFrom(nil); len(got) != 0 {
		t.Errorf("sourcesFrom(nil) = %+v, want none", got)
	}
	// A fresh loop has no turn yet.
	l := NewLoop(t.TempDir(), "sys", stubCompleter(Message{Role: "assistant", Content: "hi"}), nil)
	if got := l.sources(); len(got) != 0 {
		t.Errorf("a loop with no turn derived %+v, want none", got)
	}
	// A turnStart past the transcript (a Reset mid-flight) is handled, not panicked on.
	l.turnStart = 999
	if got := l.sources(); got != nil {
		t.Errorf("out-of-range turnStart derived %+v, want nil", got)
	}
}

func TestChargeRetrievalBudget(t *testing.T) {
	l := NewLoop(t.TempDir(), "", nil, nil)
	for i := 0; i < maxSearchesPerTurn; i++ {
		if msg := l.chargeRetrieval("web_search"); msg != "" {
			t.Fatalf("search %d of %d refused early: %q", i+1, maxSearchesPerTurn, msg)
		}
	}
	if msg := l.chargeRetrieval("web_search"); !strings.Contains(msg, "budget") {
		t.Errorf("the over-budget search returned %q, want a budget refusal", msg)
	}
	// The two budgets are independent: spending the searches must not block fetches.
	if msg := l.chargeRetrieval("web_fetch"); msg != "" {
		t.Errorf("a fetch was refused by the SEARCH budget: %q", msg)
	}
	// A non-retrieval tool is never charged.
	for i := 0; i < 50; i++ {
		if msg := l.chargeRetrieval("read_file"); msg != "" {
			t.Fatalf("read_file was charged against the retrieval budget: %q", msg)
		}
	}
}
