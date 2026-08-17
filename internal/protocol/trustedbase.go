package protocol

// trustedbase.go guards KEY-TRUST fetches (audit M2). A node or tower that pins Roger Core's
// grant-signing key - or ships its keys and receives a hub bearer token - trusts the transport
// that delivered it. Over https that trust is WebPKI; over plaintext http it is nothing: an
// on-path attacker hands back a forged grant key and every attacker-signed grant verifies,
// which on a serving node means unbounded free compute burn. So a plaintext broker base is
// refused unless it is loopback (local dev, tests) or the operator explicitly opts in with
// ROGERAI_INSECURE_HTTP=1.

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// InsecureHTTPEnv is the explicit opt-in for a plaintext, non-loopback broker base.
const InsecureHTTPEnv = "ROGERAI_INSECURE_HTTP"

// TrustedBase reports whether base is an acceptable transport for key-trust traffic:
// https always; http only to loopback or with the explicit env opt-in.
func TrustedBase(base string) error {
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("unparseable broker base %q: %w", base, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		if os.Getenv(InsecureHTTPEnv) == "1" {
			return nil
		}
		return fmt.Errorf("refusing plaintext http broker base %q for key-trust traffic: an on-path attacker could hand back a forged signing key; use https, or set %s=1 if you truly mean it", base, InsecureHTTPEnv)
	default:
		return fmt.Errorf("broker base %q: unsupported scheme %q", base, u.Scheme)
	}
}
