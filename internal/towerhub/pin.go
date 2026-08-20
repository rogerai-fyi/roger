package towerhub

// pin.go is how the two parties that dial a tower's hub - a serving NODE and an edge
// CONSUMER - reach it over TLS and VERIFY what answers, without a publicly-trusted
// certificate, without a domain name, and without a byte of new key material.
//
// # THE PROBLEM THE WEB PKI CANNOT SOLVE HERE
//
// A tower is a volunteer's box. Very often it is a home connection behind a dynamic address
// with no domain at all, which is precisely the operator the relay programme exists for.
// Requiring a publicly-trusted certificate would not have made those towers secure; it would
// have made them ineligible, and quietly restricted the fabric to operators who already run
// infrastructure. So "get a real certificate" is not a policy this system can adopt.
//
// It is also the wrong question. What a node needs to know before it polls is not "am I
// talking to relay.example?" - it never chose that name and cannot tell a good one from a
// bad one - but "am I talking to THE TOWER ROGER CORE ASSIGNED ME?". The Web PKI answers the
// first question, which is only ever a proxy for the second, and it answers it by trusting
// every certificate authority on Earth to be honest about a name the tower itself asserted.
//
// # WHAT IS PINNED, AND WHO SAYS SO
//
// The tower tells Core, on the link Core already authenticates it over, the SHA-256 of the
// SubjectPublicKeyInfo of the certificate its hub presents. Core relays that fingerprint to
// the node in the attach response and to the consumer in the authorize response - beside the
// endpoint, the tower id, the grant key and the Station's session key, every one of which
// those parties already take from Core and could not function without. The dialer then
// accepts exactly one certificate: the one whose public key hashes to that string.
//
// So the trust root is Core, which was already the trust root for WHERE to connect. Adding
// WHAT WILL ANSWER to a list that already contains the address is not a new dependency; a
// party who could forge the fingerprint could forge the address and stand up the whole hub.
//
// # ONE FIELD, SO "TLS BUT UNVERIFIED" CANNOT BE SPELLED
//
// There is deliberately no separate boolean. The pin IS the advertisement: a hub that speaks
// TLS is one Core holds a fingerprint for, and an empty fingerprint means plaintext, which is
// exactly today's behaviour. It is therefore impossible to configure a tower into the state
// this whole file exists to prevent - a TLS listener whose clients cannot check it - because
// there is no way to say "I speak TLS" without also saying what to verify.
//
// # WHAT THE PIN DOES NOT CHECK, SAID OUT LOUD
//
// Not the hostname, not the expiry, not a chain: a pinned public key makes all three
// meaningless. An expired self-signed certificate with the pinned key is ACCEPTED, and that
// is correct rather than sloppy - expiry exists so a compromised key stops being believed by
// parties who cannot be told otherwise, and here they can be told: Core stops advertising the
// fingerprint and the tower is unreachable on the next attach. The revocation channel is the
// same one that distributes the pin.

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// PinLen is the length of a hub certificate pin in hex characters: sha256, hex-encoded.
const PinLen = 2 * sha256.Size

// CertPin is the pin of one certificate: hex sha256 over its SubjectPublicKeyInfo.
//
// THE PUBLIC KEY RATHER THAN THE WHOLE CERTIFICATE, which is the difference between a pin an
// operator can live with and one they will turn off. Fingerprinting the DER would break on
// every reissue - a renewal that keeps the same key, a re-mint with a longer validity, a
// changed subject line - and each break is a fleet-wide outage for a cosmetic edit. The SPKI
// changes when, and only when, the key changes, which is the event the pin is actually about.
func CertPin(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// ValidPin reports whether a string is shaped like a pin. Shape only - whether it is the
// RIGHT pin is decided by a handshake, and this exists so a malformed one is refused at the
// door (Core's link ingress) rather than as a connection failure hours later on somebody
// else's machine.
func ValidPin(pin string) bool {
	if len(pin) != PinLen {
		return false
	}
	_, err := hex.DecodeString(pin)
	return err == nil
}

// ErrEndpointCarriesScheme refuses an endpoint that names its own scheme.
//
// It cannot happen - both ingress points validate an endpoint with net.SplitHostPort, which
// rejects anything containing "://" - and it is an error rather than a silently honoured
// special case because the LAST version of this code honoured it. That branch was unreachable
// for the whole life of the system while its comment advertised it as "how a TLS-fronted hub
// is reached", which is how the plaintext default came to look deliberate. A scheme in an
// endpoint now means somebody has changed the wire format without changing this, and the
// useful answer to that is a loud stop.
var ErrEndpointCarriesScheme = errors.New(
	"a tower hub endpoint is host:port and carries no scheme: TLS is expressed by the " +
		"certificate pin that travels beside it, not by the address")

// HubURL is the base URL for one hub: https when there is a pin to verify it with, http when
// there is not.
//
// THE SCHEME IS DERIVED FROM THE PIN AND FROM NOTHING ELSE. Every party that dials a hub goes
// through this one function - the node, the consumer, and Core's own canary - so the three
// cannot drift into disagreeing about whether a given tower speaks TLS. They did before: each
// held its own copy of `"http://" + endpoint`, and a change to one of them would have left the
// other two plaintext against a TLS listener, which is not a degraded mode but a total outage
// for half the traffic.
func HubURL(endpoint, pin string) (string, error) {
	if strings.Contains(endpoint, "://") {
		return "", fmt.Errorf("%w (got %q)", ErrEndpointCarriesScheme, endpoint)
	}
	if endpoint == "" {
		return "", errors.New("this tower advertises no hub endpoint")
	}
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		return "", fmt.Errorf("a tower hub endpoint must be host:port, got %q: %w", endpoint, err)
	}
	if pin == "" {
		return "http://" + endpoint, nil
	}
	if !ValidPin(pin) {
		// A malformed pin is refused rather than dropped back to plaintext. Dropping back is
		// the downgrade this whole mechanism exists to prevent, and it would be reachable by
		// anyone who could corrupt one field.
		return "", fmt.Errorf("this tower's hub certificate pin is malformed (%q): it must be "+
			"%d hex characters of sha256 over the certificate's public key", pin, PinLen)
	}
	return "https://" + endpoint, nil
}

// ErrHubCertificateUnpinned is a hub whose certificate is not the one Core named.
//
// It is deliberately not retried and not softened anywhere: a certificate that does not match
// is either a misconfigured tower or the exact on-path attacker the pin exists to stop, and
// there is no third case in which continuing is the right answer.
var ErrHubCertificateUnpinned = errors.New(
	"the tower hub presented a TLS certificate that is not the one Roger Core named for this " +
		"relay: refusing the connection rather than talking to whoever answered")

// PinnedTLSConfig is the only way this package produces a TLS client configuration, and it
// CANNOT produce one without a pin.
//
// # ABOUT InsecureSkipVerify, WHICH IS SET HERE
//
// The name is a lie in this context and the code below is the reason it is safe to set. It
// switches off Go's built-in verification: chain-to-a-public-root, and hostname. Both are
// meaningless for a self-signed certificate on a volunteer's dynamic address, and NEITHER is
// what this connection needs proved. What replaces them is stricter, not weaker: exactly one
// public key is acceptable, named by Core, and any other certificate - including a perfectly
// valid one from a public authority for the very name we dialled - is refused.
//
// The important property is structural rather than textual. A tls.Config with
// InsecureSkipVerify and no VerifyPeerCertificate is theatre, so this function refuses to
// return one: there is no pin-less path through it, and no other constructor in the tree.
// Callers cannot reach the unsafe configuration by forgetting an argument, because the
// argument they would have to forget is the one that makes the function work at all.
//
// TLS 1.3 IS THE FLOOR, for a reason specific to what leaks here. Under 1.2 the server's
// certificate crosses the wire in the clear, so a passive observer learns which tower a node
// is attached to even though it can read nothing else; under 1.3 the certificate is inside
// the encrypted handshake. Both ends of this connection are this codebase, so there is no
// compatibility to trade away.
func PinnedTLSConfig(pin string) (*tls.Config, error) {
	if !ValidPin(pin) {
		return nil, fmt.Errorf("a pinned TLS connection needs a %d-character hex certificate "+
			"pin from Roger Core, got %q - refusing to build an unverified TLS client", PinLen, pin)
	}
	want := []byte(strings.ToLower(pin))
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced, not omitted - see the doc comment
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("%w: it presented no certificate at all", ErrHubCertificateUnpinned)
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("%w: its certificate could not be parsed (%v)", ErrHubCertificateUnpinned, err)
			}
			got := []byte(CertPin(leaf))
			if subtle.ConstantTimeCompare(got, want) != 1 {
				return fmt.Errorf("%w (expected %s, presented %s)", ErrHubCertificateUnpinned, want, got)
			}
			return nil
		},
	}, nil
}

// Reach turns Core's advertisement of one tower's data plane - an endpoint, and a certificate
// pin that may be empty - into the two things a caller needs to talk to it: the base URL, and
// an HTTP client that will verify whatever answers.
//
// hc is the caller's own client, because the three callers want genuinely different things
// from it: a node needs a timeout longer than the hub's poll TTL, a consumer needs no timeout
// at all (a submit is legitimately held while the node generates) and Core's canary is happy
// with a default. What none of them should be doing is deciding the SCHEME or the VERIFICATION,
// which is why those two are here and not there.
//
// It returns a COPY: a caller's client is often shared between goroutines (the node's poll
// workers and its audit loop hold one between them), and installing a transport into it from
// under them is a data race.
func Reach(endpoint, pin string, hc *http.Client) (string, *http.Client, error) {
	base, err := HubURL(endpoint, pin)
	if err != nil {
		return "", nil, err
	}
	var out http.Client
	if hc != nil {
		out = *hc
	}
	if pin == "" {
		return base, &out, nil
	}
	if out.Transport != nil {
		// REFUSED RATHER THAN OVERWRITTEN. A caller who has installed their own transport has
		// their own dialing arrangements, and silently replacing them would either break those
		// arrangements or - far worse - keep them and lose the pin, which is the one failure
		// this function exists to make unrepresentable.
		return "", nil, errors.New("a pinned tower hub connection cannot be built on a " +
			"caller-supplied http.Transport: the pin lives in the transport's TLS configuration")
	}
	cfg, err := PinnedTLSConfig(pin)
	if err != nil {
		return "", nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg
	// A hub speaks HTTP/1.1 and long-polls. ForceAttemptHTTP2 on a cloned DefaultTransport
	// would negotiate h2 whenever the tower's certificate advertises it, which is a protocol
	// change smuggled in by a TLS change; keep the transport this connection already had.
	tr.ForceAttemptHTTP2 = false
	out.Transport = tr
	return base, &out, nil
}
