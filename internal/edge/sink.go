package edge

// EXTERNAL SINKS (features/edge/health.feature §5): where an alert goes when the owner wants it to
// leave this machine.
//
// PRIVATE BY DEFAULT. An Edge sends nothing anywhere until its owner configures a sink. With none,
// an alert is still an alert - it shows on every fleet surface and in the log - it simply does not
// travel. That is the default because a fleet's metrics are the owner's business and an airgapped
// Edge must be a first-class citizen, not a degraded one.
//
// THE SECRET GOES IN AND NEVER COMES BACK OUT. A Discord webhook URL is a bearer credential:
// anybody holding it can post as that integration, for ever, with no further authentication. So
// the URL is stored 0600 beside the rest of the owner's operational state and is stripped from
// EVERY read. Sinks() hands back records whose URL field is empty; only Deliver, inside this file,
// ever reads the stored secret, and it is the only function that can. A screen, a log line, a
// receipt and a browser payload are all built from Words(), which carries the host and an id and
// nothing else. This is enforced by construction rather than by remembering.
//
// EMAIL IS NOT BUILT. It needs an SMTP account, a from-address, TLS settings and a credential of
// its own, which is a configuration surface that wants designing rather than guessing at. It is
// refused by name - "not built" - rather than accepted and silently dropped.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SinkKind is what sort of thing is on the other end.
type SinkKind string

const (
	SinkWebhook  SinkKind = "webhook"  // any URL that accepts a JSON POST, including one on the LAN
	SinkDiscord  SinkKind = "discord"  // a Discord webhook: the same POST with Discord's own field
	SinkTelegram SinkKind = "telegram" // a Telegram bot sendMessage URL
	SinkEmail    SinkKind = "email"    // NOT BUILT: refused by name rather than silently dropped
)

// SinkKinds are the kinds an owner may configure, for a refusal that names them.
func SinkKinds() []SinkKind { return []SinkKind{SinkWebhook, SinkDiscord, SinkTelegram} }

// Sink is one destination. URL is the SECRET and is empty on every record that leaves the store.
type Sink struct {
	ID    string   `json:"id"`
	Kind  SinkKind `json:"kind"`
	URL   string   `json:"url,omitempty"`
	Host  string   `json:"host,omitempty"` // the host alone, kept so a read can say WHERE without the secret
	Added int64    `json:"added,omitempty"`
}

// Words is how a sink is named anywhere a person can see it: the kind, the host it posts to, and
// its id. Never the path, never a token, never a query string - the parts that are the credential.
func (s Sink) Words() string {
	where := s.Host
	if where == "" {
		where = "somewhere"
	}
	return string(s.Kind) + " · " + where + " · " + s.ID
}

// redacted is the sink as it leaves the store: everything except the secret.
func (s Sink) redacted() Sink { s.URL = ""; return s }

// NewSinkID mints an id an owner can type back.
func NewSinkID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "sk_" + hex.EncodeToString(b)
}

// ValidateSink refuses a sink that cannot work, before anything is stored.
func ValidateSink(kind SinkKind, raw string) error {
	if kind == SinkEmail {
		return fmt.Errorf("an email sink is not built yet: it needs an SMTP account, a from-address and a credential of its own, and that wants designing rather than guessing. Use a %s for now", SinkWebhook)
	}
	known := false
	for _, k := range SinkKinds() {
		known = known || kind == k
	}
	if !known {
		return fmt.Errorf("%q is not a kind of sink; there is: %s", string(kind), sinkKindList())
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("a %s sink needs a URL to post to", kind)
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("that is not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("a sink posts over http or https, not %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("that URL names no host to post to (http://box.local/hook)")
	}
	return nil
}

func sinkKindList() string {
	out := make([]string, 0, len(SinkKinds()))
	for _, k := range SinkKinds() {
		out = append(out, string(k))
	}
	return joinWords(out)
}

// Sinks is the configured destinations, WITHOUT their secrets. There is deliberately no method
// that returns one with its URL: nothing outside this file needs it, and a getter that could would
// be the hole every rule above is written to prevent.
func (l AlertLog) Sinks() ([]Sink, error) {
	all, err := l.sinksWithSecrets()
	if err != nil {
		return nil, err
	}
	out := make([]Sink, 0, len(all))
	for _, s := range all {
		out = append(out, s.redacted())
	}
	return out, nil
}

func (l AlertLog) sinksWithSecrets() ([]Sink, error) {
	var out []Sink
	if err := l.readJSON("sinks.json", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddSink stores one, and answers with the redacted record so a caller can name what it just made
// without ever holding the secret again.
func (l AlertLog) AddSink(kind SinkKind, raw string, now time.Time) (Sink, error) {
	if err := ValidateSink(kind, raw); err != nil {
		return Sink{}, err
	}
	raw = strings.TrimSpace(raw)
	u, _ := url.Parse(raw)
	s := Sink{ID: NewSinkID(), Kind: kind, URL: raw, Host: u.Host, Added: now.Unix()}
	err := l.withLock(func() error {
		all, err := l.sinksWithSecrets()
		if err != nil {
			return err
		}
		return l.writeJSON("sinks.json", append(all, s))
	})
	return s.redacted(), err
}

// RemoveSink drops one by id and says whether there was one.
func (l AlertLog) RemoveSink(id string) (bool, error) {
	had := false
	err := l.withLock(func() error {
		all, err := l.sinksWithSecrets()
		if err != nil {
			return err
		}
		out := make([]Sink, 0, len(all))
		for _, s := range all {
			if s.ID == id {
				had = true
				continue
			}
			out = append(out, s)
		}
		if !had {
			return nil
		}
		return l.writeJSON("sinks.json", out)
	})
	return had, err
}

// Delivery is what happened when one alert was sent to one sink. It names the sink by ID and never
// by URL, so a delivery record is safe to log, print, store and show.
type Delivery struct {
	Sink  string `json:"sink"`  // the sink id
	Words string `json:"words"` // the sink as a person reads it (kind · host · id)
	OK    bool   `json:"ok"`
	Says  string `json:"says"`            // the outcome, or why it failed
	Tries int    `json:"tries,omitempty"` // how many attempts it took
}

// sinkTries is how many attempts one alert gets per sink, and sinkBackoff the wait before the
// second. The wait doubles. Bounded on purpose: an unreachable sink must not hold up the pass that
// is trying to tell somebody about a fleet that is on fire.
const sinkTries = 3

const sinkBackoff = 400 * time.Millisecond

// sinkTimeout bounds ONE attempt. A sink that hangs is a sink that failed.
const sinkTimeout = 5 * time.Second

// Deliver sends one alert to every configured sink and returns what happened to each. An Edge with
// no sink configured sends nothing and says so by returning nothing, which is not an error: it is
// the default and the private case.
//
// It never returns an error for a sink that would not take the alert - that is a Delivery with OK
// false and a reason, because the alert must stay visible as undelivered rather than vanish into a
// returned error the caller might drop.
func (l AlertLog) Deliver(ctx context.Context, a Alert, now time.Time) ([]Delivery, error) {
	all, err := l.sinksWithSecrets()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	out := make([]Delivery, 0, len(all))
	for _, s := range all {
		out = append(out, l.deliverOne(ctx, s, a))
	}
	return out, nil
}

// TestSink sends a CLEARLY MARKED test to one sink, so an owner can prove it works without waiting
// for something to go wrong. It changes no rule state and raises no alarm: the message says it is
// a test, and nothing is recorded against any rule.
func (l AlertLog) TestSink(ctx context.Context, id string, now time.Time) (Delivery, error) {
	all, err := l.sinksWithSecrets()
	if err != nil {
		return Delivery{}, err
	}
	for _, s := range all {
		if s.ID != id {
			continue
		}
		a := Alert{At: now, RuleID: "test", Node: "this Edge", Metric: "test", Firing: true, Test: true}
		return l.deliverOne(ctx, s, a), nil
	}
	return Delivery{}, fmt.Errorf("no sink has the id %q - `roger edge sink` lists them", id)
}

// deliverOne posts to one sink, retrying with a bounded, doubling backoff. It is the ONLY place the
// stored URL is read, and the only place it is passed to anything.
func (l AlertLog) deliverOne(ctx context.Context, s Sink, a Alert) Delivery {
	d := Delivery{Sink: s.ID, Words: s.Words()}
	body, err := sinkBody(s.Kind, a)
	if err != nil {
		d.Says = err.Error()
		return d
	}
	wait := l.SinkBackoff
	if wait <= 0 {
		wait = sinkBackoff
	}
	for try := 1; try <= sinkTries; try++ {
		d.Tries = try
		code, err := sinkPost(ctx, s.URL, body)
		switch {
		case err == nil && code >= 200 && code < 300:
			d.OK, d.Says = true, "delivered"
			return d
		case err != nil:
			// The error may carry the URL (net/http puts it in), and the URL is the secret.
			d.Says = "could not reach it: " + sinkScrub(err.Error(), s.URL)
		default:
			d.Says = "it answered " + strconv.Itoa(code)
		}
		if try < sinkTries {
			select {
			case <-ctx.Done():
				d.Says = "gave up: " + ctx.Err().Error()
				return d
			case <-time.After(wait):
			}
			wait *= 2
		}
	}
	d.Says += " · undelivered after " + strconv.Itoa(sinkTries) + " tries"
	return d
}

// sinkScrub removes a secret from text that is about to be stored or shown. Belt and braces: the
// callers are careful, and this is what makes carelessness impossible rather than unlikely.
func sinkScrub(text, secret string) string {
	if secret == "" {
		return text
	}
	out := strings.ReplaceAll(text, secret, "the sink's URL")
	if u, err := url.Parse(secret); err == nil && u.Path != "" && len(u.Path) > 1 {
		out = strings.ReplaceAll(out, u.Path, "/…")
	}
	return out
}

// sinkBody is the message, in the shape the far end expects. All three are a JSON POST; they differ
// only in which field carries the text, which is why they are one code path and not three.
func sinkBody(kind SinkKind, a Alert) ([]byte, error) {
	text := AlertWords(a)
	switch kind {
	case SinkDiscord:
		return json.Marshal(map[string]string{"content": text})
	case SinkTelegram:
		return json.Marshal(map[string]string{"text": text})
	case SinkWebhook:
		body := map[string]any{
			"text": text, "node": a.Node, "metric": string(a.Metric),
			"value": a.Value, "threshold": a.Threshold, "firing": a.Firing, "test": a.Test,
			"at": a.At.UTC().Format(time.RFC3339),
		}
		if a.Unreported {
			delete(body, "value") // there is no reading, and a zero would be read as one
			body["unreported"] = true
		}
		return json.Marshal(body)
	}
	return nil, fmt.Errorf("%q is not a kind of sink", string(kind))
}

// AlertWords is one alert as a person reads it, wherever it is delivered. A test says so FIRST, so
// nobody is woken by a message they have to read to the end to discover is a drill.
func AlertWords(a Alert) string {
	if a.Test {
		return "ROGER EDGE TEST · this is a test alert from your Edge, sent because you asked. No rule is firing and nothing is wrong."
	}
	state := "FIRING"
	if !a.Firing {
		state = "CLEARED"
	}
	if a.Unreported {
		return "ROGER EDGE " + state + " · " + a.Node + " · " + string(a.Metric) + " · " + a.Node + " no longer reports " + unreportedReading(a.Metric) + ", so this call is closed"
	}
	out := "ROGER EDGE " + state + " · " + a.Node + " · " + string(a.Metric)
	if a.Metric != "" && a.Threshold != 0 {
		out += " " + strconv.FormatFloat(a.Value, 'f', -1, 64) +
			" (" + string(a.Op) + " " + strconv.FormatFloat(a.Threshold, 'f', -1, 64) + ")"
	}
	return out
}

// sinkPost is one attempt. No redirects are followed: a redirect would send the owner's alert, and
// with a webhook the credential in the URL, somewhere they did not configure.
func sinkPost(ctx context.Context, rawURL string, body []byte) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, sinkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{
		Timeout: sinkTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}
