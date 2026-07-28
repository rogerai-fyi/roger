package harness

// fetch_hardening_bdd_test.go makes features/answers/fetch_hardening.feature EXECUTABLE
// against the REAL webFetch. No mocks: every scenario drives the actual fetch path over
// real HTTP against real local listeners, with two narrow SEAMS (the internal/audio Env /
// tools.go shellTimeout idiom) so the adversarial cases are reachable at all:
//
//   resolveHost  - the DNS seam. Lets a scenario make a public-looking hostname resolve to
//                  a private address (DNS-based SSRF) and flip its answer between calls
//                  (rebinding) without depending on real DNS.
//   fetchVetIP   - the address-vetting seam. A scenario that needs a reachable "public"
//                  server (every httptest listener is loopback) swaps in a vet that
//                  DELEGATES TO THE REAL vetIP and only permits 127.0.0.1 - so the
//                  refusal being asserted (169.254.169.254 etc) is still the real logic.
//                  Production never assigns either seam.
//
// fetchTimeout is a var for the same reason shellTimeout is: a test shortens it to prove
// the deadline without a 20s wall clock.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

type fetchState struct {
	t *testing.T

	srv *httptest.Server

	result string
	err    error

	srvURL   string
	resolves int

	wroteBytes atomic.Int64
	elapsed    time.Duration

	origResolve func(context.Context, string) ([]net.IP, error)
	origVet     func(net.IP) error
	origTimeout time.Duration
}

func (s *fetchState) reset() {
	s.srv = nil
	s.result, s.err = "", nil
	s.srvURL, s.resolves = "", 0
	s.wroteBytes.Store(0)
	s.elapsed = 0
	s.origResolve, s.origVet, s.origTimeout = resolveHost, fetchVetIP, fetchTimeout
}

func (s *fetchState) restore() {
	resolveHost, fetchVetIP, fetchTimeout = s.origResolve, s.origVet, s.origTimeout
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

// allowLoopbackVet delegates to the REAL vet, permitting only 127.0.0.1 so a local
// httptest server can stand in for a "public" host while every other refusal stays real.
func allowLoopbackVet(ip net.IP) error {
	if ip.IsLoopback() {
		return nil
	}
	return vetIP(ip)
}

// serve starts a server with h and returns its URL.
func (s *fetchState) serve(h http.HandlerFunc) string {
	s.srv = httptest.NewServer(h)
	return s.srv.URL
}

// --- refusal by address --------------------------------------------------------

func (s *fetchState) fetchURL(u string) error {
	s.result, s.err = webFetch(context.Background(), u)
	return nil
}

func (s *fetchState) refusedBeforeConnect() error {
	if s.err == nil {
		return fmt.Errorf("the fetch was NOT refused (returned %q) - a model-supplied internal address must be blocked", tail(s.result))
	}
	if !strings.Contains(strings.ToLower(s.err.Error()), "blocked address") {
		return fmt.Errorf("refusal must be the guard's blocked-address policy (pre-dial), got a different failure: %v", s.err)
	}
	return nil
}

func (s *fetchState) namesBlockedPolicy() error {
	low := strings.ToLower(s.err.Error())
	if !strings.Contains(low, "blocked address") {
		return fmt.Errorf("the tool result must name the blocked-address policy, got %v", s.err)
	}
	return nil
}

func (s *fetchState) refusedNonHTTP() error {
	if s.err == nil {
		return fmt.Errorf("a non-http(s) scheme was not refused (returned %q)", tail(s.result))
	}
	return nil
}

func (s *fetchState) hostnameResolvesTo(host, ip string) error {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return fmt.Errorf("bad test IP %q", ip)
	}
	prev := resolveHost
	resolveHost = func(ctx context.Context, h string) ([]net.IP, error) {
		if h == host {
			return []net.IP{addr}, nil
		}
		return prev(ctx, h)
	}
	return nil
}

func (s *fetchState) fetchThatHostname() error {
	s.result, s.err = webFetch(context.Background(), "http://internal.attacker.example/")
	return nil
}

func (s *fetchState) resolvedVettedAndRefused() error { return s.refusedBeforeConnect() }

// --- rebinding / pinning --------------------------------------------------------

// dnsAnswerChanges stands up a REAL server, points a hostname at it, and arms the
// resolver to flip to a private address on every later call. The fetch below must reach
// the server it vetted and must not consult DNS again mid-flight.
func (s *fetchState) dnsAnswerChanges() error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "served to Host %s", r.Host)
	})
	served := net.ParseIP(strings.Split(strings.TrimPrefix(s.srvURL, "http://"), ":")[0])
	resolveHost = func(context.Context, string) ([]net.IP, error) {
		s.resolves++
		if s.resolves == 1 {
			return []net.IP{served}, nil // the vetted answer
		}
		return []net.IP{net.ParseIP("10.0.0.5")}, nil // the rebind attempt
	}
	return nil
}

func (s *fetchState) fetchThatChangingHostname() error {
	port := strings.Split(s.srvURL, ":")[2]
	s.result, s.err = webFetch(context.Background(), "http://rebind.example:"+port+"/x")
	return nil
}

func (s *fetchState) responseCameFromVettedAddress() error {
	if s.err != nil {
		return fmt.Errorf("the fetch failed: %v", s.err)
	}
	// The body proves the connection landed on the server that was vetted, and the Host
	// header proves the ORIGINAL hostname was preserved (the dial is pinned, not rewritten).
	if !strings.Contains(s.result, "served to Host rebind.example") {
		return fmt.Errorf("response %q did not come from the vetted server with the original Host", s.result)
	}
	return nil
}

func (s *fetchState) resolvedExactlyOnce() error {
	if s.resolves != 1 {
		return fmt.Errorf("the hostname was resolved %d times, want exactly 1 (a re-resolution is a rebinding window)", s.resolves)
	}
	return nil
}

func (s *fetchState) reresolutionCannotRedirect() error {
	// The armed resolver now answers 10.0.0.5: a FRESH fetch must be refused outright,
	// proving later answers are vetted rather than trusted from the earlier pin.
	if _, err := vetAndPin(context.Background(), "http://rebind.example/x"); err == nil {
		return fmt.Errorf("the rebound private answer was accepted")
	}
	return nil
}

// --- redirects -------------------------------------------------------------------

func (s *fetchState) serverRedirectsTo(target string) error {
	fetchVetIP = allowLoopbackVet
	url := s.serve(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	})
	s.srvURL = url
	return nil
}

func (s *fetchState) fetchPublicURL() error {
	start := time.Now()
	s.result, s.err = webFetch(context.Background(), s.srvURL+"/")
	s.elapsed = time.Since(start)
	return nil
}

func (s *fetchState) redirectVettedAndRefused() error { return s.refusedBeforeConnect() }

func (s *fetchState) noRequestToInternal() error {
	// The refusal is the guard's policy error, which is raised BEFORE any dial. A fetch
	// that had actually tried to reach an unroutable internal address would instead have
	// burned a connect timeout, so a fast policy refusal is the evidence no dial happened.
	if s.err == nil || !strings.Contains(s.err.Error(), "blocked address") {
		return fmt.Errorf("expected a pre-dial policy refusal, got result %q err %v", tail(s.result), s.err)
	}
	if s.elapsed > 2*time.Second {
		return fmt.Errorf("the refusal took %s - long enough to have attempted a connection", s.elapsed)
	}
	return nil
}

func (s *fetchState) redirectWithoutLocation() error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusFound) // 302 with NO Location
		_, _ = w.Write([]byte("<html><body><p>readable but going nowhere</p></body></html>"))
	})
	return nil
}

func (s *fetchState) errorNamesUnusableRedirect() error {
	if s.err == nil {
		return fmt.Errorf("a redirect with no Location was treated as a page: %q", tail(s.result))
	}
	if !strings.Contains(strings.ToLower(s.err.Error()), "redirect") {
		return fmt.Errorf("the error should name the unusable redirect, got %v", s.err)
	}
	return nil
}

func (s *fetchState) nothingCitableFromIt() error {
	if retrievedURL(s.result) != "" {
		return fmt.Errorf("a redirect stub was marked as a citable retrieval: %q", tail(s.result))
	}
	return nil
}

func (s *fetchState) serverRedirectsNTimes(n int) error {
	fetchVetIP = allowLoopbackVet
	var base string
	hops := 0
	base = s.serve(func(w http.ResponseWriter, r *http.Request) {
		hops++
		if hops > n {
			_, _ = w.Write([]byte("arrived"))
			return
		}
		http.Redirect(w, r, fmt.Sprintf("%s/hop%d", base, hops), http.StatusFound)
	})
	s.srvURL = base
	return nil
}

func (s *fetchState) fetchIt() error {
	s.result, s.err = webFetch(context.Background(), s.srvURL+"/")
	return nil
}

func (s *fetchState) chainAbandonedAfter(n int) error {
	if s.err == nil && !strings.Contains(strings.ToLower(s.result), "redirect") {
		return fmt.Errorf("a %d-hop chain was followed to completion (got %q), want it abandoned after %d hops", n+1, tail(s.result), n)
	}
	msg := s.result
	if s.err != nil {
		msg = s.err.Error()
	}
	if !strings.Contains(strings.ToLower(msg), "redirect") {
		return fmt.Errorf("the abandoned-chain result must say why, got %q", msg)
	}
	return nil
}

func (s *fetchState) vettedHostIsLoopback() error { return s.refusedBeforeConnect() }

// --- extraction -------------------------------------------------------------------

const htmlPage = `<!doctype html><html><head><title>T</title>
<style>.x{color:#ff0000}</style>
<script>var secretTracker = 1; alert("xss-marker");</script>
</head><body><nav><a href="/">home</a></nav>
<h1>Backoff explained</h1><p>Retry with &amp; jitter.</p>
<script>var another = "second-marker";</script>
</body></html>`

func (s *fetchState) pageIsHTML() error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(htmlPage))
	})
	return nil
}

func (s *fetchState) fetchOnIt() error {
	s.result, s.err = webFetch(context.Background(), s.srvURL+"/")
	return nil
}

func (s *fetchState) resultIsReadableText() error {
	if s.err != nil {
		return fmt.Errorf("fetch errored: %v", s.err)
	}
	for _, want := range []string{"Backoff explained", "Retry with & jitter."} {
		if !strings.Contains(s.result, want) {
			return fmt.Errorf("readable text is missing %q, got %q", want, tail(s.result))
		}
	}
	if strings.Contains(s.result, "<h1>") || strings.Contains(s.result, "<p>") {
		return fmt.Errorf("raw markup survived extraction: %q", tail(s.result))
	}
	return nil
}

func (s *fetchState) scriptAndStyleAbsent() error {
	for _, bad := range []string{"xss-marker", "second-marker", "secretTracker", "color:#ff0000"} {
		if strings.Contains(s.result, bad) {
			return fmt.Errorf("script/style content leaked into the model's context: %q", bad)
		}
	}
	return nil
}

func (s *fetchState) serveContentType(ctype string) error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write([]byte("\x00\x01\x02binary-marker\x00"))
	})
	return nil
}

func (s *fetchState) bodyNotReturned() error {
	if strings.Contains(s.result, "binary-marker") {
		return fmt.Errorf("binary content was fed to the model: %q", tail(s.result))
	}
	return nil
}

func (s *fetchState) namesUnsupportedType() error {
	msg := s.result
	if s.err != nil {
		msg = s.err.Error()
	}
	if !strings.Contains(strings.ToLower(msg), "unsupported content type") {
		return fmt.Errorf("the result must name the unsupported content type, got %q", msg)
	}
	return nil
}

func (s *fetchState) serveJSON(ctype string) error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write([]byte(`{"ok":true,"note":"<b>not markup</b>"}`))
	})
	return nil
}

func (s *fetchState) bodyReturnedAsText() error {
	if s.err != nil {
		return fmt.Errorf("fetch errored: %v", s.err)
	}
	if !strings.Contains(s.result, `{"ok":true`) || !strings.Contains(s.result, "<b>not markup</b>") {
		return fmt.Errorf("JSON must pass through verbatim (no extraction), got %q", tail(s.result))
	}
	return nil
}

func (s *fetchState) serveMislabeledBinary(ctype string) error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write([]byte("\x00\x00\x00\x01\x02\x03binary-marker\x00\x00\x7f\x00"))
	})
	return nil
}

func (s *fetchState) refusedAsBinary() error {
	if strings.Contains(s.result, "binary-marker") {
		return fmt.Errorf("a mislabeled binary body reached the model: %q", tail(s.result))
	}
	msg := s.result
	if s.err != nil {
		msg = s.err.Error()
	}
	if !strings.Contains(strings.ToLower(msg), "binary") {
		return fmt.Errorf("the sniffed refusal must say the body is binary, got %q", msg)
	}
	return nil
}

func (s *fetchState) serveHugeBody() error {
	fetchVetIP = allowLoopbackVet
	chunk := strings.Repeat("readable words here ", 3200) // ~64 KiB
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for i := 0; i < 64; i++ { // ~4 MiB offered
			n, err := w.Write([]byte(chunk))
			s.wroteBytes.Add(int64(n))
			if err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	})
	return nil
}

func (s *fetchState) atMostMaxFetchBytesRead() error {
	// The client must stop reading at the cap; the server therefore never gets to push
	// the whole 4 MiB (it sees a short write / broken pipe, or simply is never drained).
	if got := s.wroteBytes.Load(); got >= 4<<20 {
		return fmt.Errorf("the client drained the whole %d-byte body - maxFetchBytes (%d) is not enforced", got, maxFetchBytes)
	}
	return nil
}

func (s *fetchState) extractedClippedAndMarked() error {
	if len(s.result) > maxToolOutput+64 {
		return fmt.Errorf("result is %d bytes, want clipped to ~%d", len(s.result), maxToolOutput)
	}
	if !strings.Contains(s.result, "truncated") {
		return fmt.Errorf("a clipped fetch must be marked truncated, got %q", tail(s.result))
	}
	return nil
}

// serveDNSStall arms a resolver that never answers: the fetch deadline, not the resolver,
// must end the call.
func (s *fetchState) serveDNSStall() error {
	fetchTimeout = 300 * time.Millisecond
	resolveHost = func(ctx context.Context, _ string) ([]net.IP, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s.srvURL = "http://never-answers.example"
	return nil
}

func (s *fetchState) serveANSI() error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("safe text\x1b[2J\x1b]0;pwned\x07 and \x1emarker\x08 more"))
	})
	return nil
}

func (s *fetchState) readableTextRemains() error {
	if s.err != nil {
		return fmt.Errorf("fetch errored: %v", s.err)
	}
	for _, want := range []string{"safe text", "more"} {
		if !strings.Contains(s.result, want) {
			return fmt.Errorf("readable text %q was lost: %q", want, s.result)
		}
	}
	return nil
}

func (s *fetchState) noControlBytesSurvive() error {
	for _, r := range s.result {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return fmt.Errorf("control byte %#x survived into the model's context: %q", r, s.result)
		}
	}
	return nil
}

func (s *fetchState) serveControlRidden(ctype string) error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		// No NUL anywhere: the ratio sniff is what must catch this.
		_, _ = w.Write([]byte(strings.Repeat("\x01\x02\x03\x04\x05binary-marker", 40)))
	})
	return nil
}

func (s *fetchState) serveStall() error {
	fetchVetIP = allowLoopbackVet
	fetchTimeout = 300 * time.Millisecond
	s.srvURL = s.serve(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	return nil
}

func (s *fetchState) endsAtTimeout() error {
	if s.err == nil {
		return fmt.Errorf("a stalled fetch must end at the deadline, got %q", tail(s.result))
	}
	return nil
}

// loopContinuesAfterFetch proves the recoverability claim against the REAL loop: a failing
// web_fetch comes back as a tool result the model answers around, not as a dead turn.
func (s *fetchState) loopContinuesAfterFetch() error {
	calls := 0
	target := s.srvURL + "/"
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			return toolCall("f1", "web_fetch", fmt.Sprintf(`{"url":%q}`, target)), nil
		}
		for _, m := range msgs {
			if m.Role == "tool" && strings.TrimSpace(m.Content) == "" {
				return Message{}, fmt.Errorf("the failed fetch fed back an empty tool result")
			}
		}
		return Message{Role: "assistant", Content: "answered despite the failed fetch"}, nil
	}
	loop := NewLoop(s.t.TempDir(), "sys", complete, nil)
	final, err := loop.Send(context.Background(), "read that page", nil)
	if err != nil {
		return fmt.Errorf("a failing web_fetch aborted the turn: %v", err)
	}
	if final == "" {
		return fmt.Errorf("the turn produced no answer after a failed fetch")
	}
	return nil
}

func (s *fetchState) serve404() error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body><p>no such page</p></body></html>"))
	})
	return nil
}

func (s *fetchState) carries404AndBody() error {
	if !strings.Contains(s.result, "HTTP 404") {
		return fmt.Errorf("an error status must surface as HTTP 404, got %q", tail(s.result))
	}
	if !strings.Contains(s.result, "no such page") {
		return fmt.Errorf("the (extracted) error body must surface, got %q", tail(s.result))
	}
	return nil
}

func (s *fetchState) serveEmpty() error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
	})
	return nil
}

func (s *fetchState) saysEmptyBody() error {
	if !strings.Contains(strings.ToLower(s.result), "empty body") {
		return fmt.Errorf("an empty body needs its explicit marker, got %q", s.result)
	}
	return nil
}

func (s *fetchState) serveLatin1() error {
	fetchVetIP = allowLoopbackVet
	s.srvURL = s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=ISO-8859-1")
		// "café résumé" in ISO-8859-1 (0xE9 = é).
		_, _ = w.Write([]byte("<html><body><p>caf\xe9 r\xe9sum\xe9</p></body></html>"))
	})
	return nil
}

func (s *fetchState) validUTF8() error {
	if s.err != nil {
		return fmt.Errorf("fetch errored: %v", s.err)
	}
	if !strings.Contains(s.result, "café résumé") {
		return fmt.Errorf("ISO-8859-1 text was not transcoded, got %q", tail(s.result))
	}
	return nil
}

// --- one guard, every caller ------------------------------------------------------

// Adding a file here is the thing a reviewer must object to: it exempts that file from
// the "no network around the guard" rule.
// guardedFetchFiles are the ONLY package files allowed to open a socket for a
// MODEL-SUPPLIED URL. broker.go (the metered relay to the broker) and search.go (the
// operator-configured provider endpoint) reach the network too, but neither ever dials an
// address the model chose, so they are not part of this guard's surface.
var guardedFetchFiles = map[string]bool{"fetch.go": true, "broker.go": true, "search.go": true}

func (s *fetchState) oneFetchImplementation() error {
	files, err := filepath.Glob("*.go")
	if err != nil {
		return err
	}
	defs := 0
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		src := string(b)
		defs += strings.Count(src, "func webFetch(")
		if guardedFetchFiles[f] {
			continue
		}
		for _, bad := range []string{
			"http.Get(", "http.Post(", "http.Head(", "http.Client{",
			"http.DefaultClient", "http.DefaultTransport", "http.NewRequest",
			"net.Dial", "httputil.",
		} {
			if strings.Contains(src, bad) {
				offenders = append(offenders, f+" uses "+bad)
			}
		}
	}
	if defs != 1 {
		return fmt.Errorf("found %d webFetch implementations, want exactly 1 carrying the guard", defs)
	}
	if len(offenders) > 0 {
		return fmt.Errorf("network access outside the guarded fetch path: %v", offenders)
	}
	return nil
}

func (s *fetchState) noCallerBypasses() error { return s.oneFetchImplementation() }

func TestFetchHardeningBDD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &fetchState{t: t}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				st.restore()
				return ctx, err
			})

			sc.Step(`^the model calls web_fetch with url "([^"]*)"$`, st.fetchURL)
			sc.Step(`^the fetch is refused before any connection is made$`, st.refusedBeforeConnect)
			sc.Step(`^the tool result names the blocked-address policy$`, st.namesBlockedPolicy)
			sc.Step(`^the fetch is refused$`, st.refusedNonHTTP)
			sc.Step(`^the vetted host is 127\.0\.0\.1 and the fetch is refused$`, st.vettedHostIsLoopback)

			sc.Step(`^hostname "([^"]*)" resolves to (\S+)$`, st.hostnameResolvesTo)
			sc.Step(`^the model calls web_fetch with that hostname$`, st.fetchThatHostname)
			sc.Step(`^the resolved address is vetted and the fetch is refused$`, st.resolvedVettedAndRefused)

			sc.Step(`^a hostname whose DNS answer changes between requests$`, st.dnsAnswerChanges)
			sc.Step(`^the model calls web_fetch on that hostname$`, st.fetchThatChangingHostname)
			sc.Step(`^the response comes from the address that was vetted$`, st.responseCameFromVettedAddress)
			sc.Step(`^the hostname was resolved exactly once$`, st.resolvedExactlyOnce)
			sc.Step(`^a post-vet re-resolution can never redirect the dial to a private address$`, st.reresolutionCannotRedirect)

			sc.Step(`^a public server that 302-redirects to "([^"]*)"$`, st.serverRedirectsTo)
			sc.Step(`^the model calls web_fetch on the public URL$`, st.fetchPublicURL)
			sc.Step(`^the redirect target is vetted like a fresh URL and refused$`, st.redirectVettedAndRefused)
			sc.Step(`^no request is sent to the internal address$`, st.noRequestToInternal)
			sc.Step(`^a server that answers 302 with no Location header$`, st.redirectWithoutLocation)
			sc.Step(`^the tool result is an error naming the unusable redirect$`, st.errorNamesUnusableRedirect)
			sc.Step(`^nothing is citable from it$`, st.nothingCitableFromIt)
			sc.Step(`^a public server that redirects (\d+) times$`, st.serverRedirectsNTimes)
			sc.Step(`^the model calls web_fetch$`, st.fetchIt)
			sc.Step(`^the chain is abandoned after (\d+) hops with a clear tool result$`, st.chainAbandonedAfter)

			sc.Step(`^a page whose body is HTML with script, style, and nav markup$`, st.pageIsHTML)
			sc.Step(`^the model calls web_fetch on it$`, st.fetchOnIt)
			sc.Step(`^the tool result is the page's readable text$`, st.resultIsReadableText)
			sc.Step(`^script and style content is absent$`, st.scriptAndStyleAbsent)

			sc.Step(`^a URL whose response Content-Type is "([^"]*)"$`, st.serveContentType)
			sc.Step(`^the body is not returned$`, st.bodyNotReturned)
			sc.Step(`^the tool result names the unsupported content type$`, st.namesUnsupportedType)
			sc.Step(`^a URL serving "([^"]*)"$`, st.serveJSON)
			sc.Step(`^the body is returned as text \(no extraction applied\)$`, st.bodyReturnedAsText)
			sc.Step(`^a URL serving NUL-ridden bytes labeled "([^"]*)"$`, st.serveMislabeledBinary)
			sc.Step(`^the body is refused as binary \(content sniff, not just the header\)$`, st.refusedAsBinary)

			sc.Step(`^a URL serving a body larger than maxFetchBytes$`, st.serveHugeBody)
			sc.Step(`^at most maxFetchBytes are read from the wire$`, st.atMostMaxFetchBytesRead)
			sc.Step(`^the extracted result is clipped and marked truncated$`, st.extractedClippedAndMarked)

			sc.Step(`^a hostname whose resolution stalls beyond fetchTimeout$`, st.serveDNSStall)
			sc.Step(`^a URL serving text carrying ANSI escape sequences$`, st.serveANSI)
			sc.Step(`^the readable text remains$`, st.readableTextRemains)
			sc.Step(`^no escape or control bytes survive$`, st.noControlBytesSurvive)
			sc.Step(`^a URL serving control-byte-ridden text labeled "([^"]*)"$`, st.serveControlRidden)
			sc.Step(`^a URL that stalls beyond fetchTimeout$`, st.serveStall)
			sc.Step(`^the call ends at the timeout with an error tool result$`, st.endsAtTimeout)
			sc.Step(`^the loop continues$`, st.loopContinuesAfterFetch)

			sc.Step(`^a URL responding 404 with a short body$`, st.serve404)
			sc.Step(`^the tool result carries "HTTP 404" and the \(extracted\) body$`, st.carries404AndBody)
			sc.Step(`^a URL responding 200 with an empty body$`, st.serveEmpty)
			sc.Step(`^the tool result says the body was empty$`, st.saysEmptyBody)
			sc.Step(`^a URL serving ISO-8859-1 text$`, st.serveLatin1)
			sc.Step(`^the tool result is valid UTF-8 with no mojibake control garbage$`, st.validUTF8)

			sc.Step(`^there is exactly one fetch implementation carrying the guard$`, st.oneFetchImplementation)
			sc.Step(`^no caller in internal/harness can reach the network around it$`, st.noCallerBypasses)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/answers/fetch_hardening.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("fetch hardening behavior scenarios failed (see godog output above)")
	}
}
