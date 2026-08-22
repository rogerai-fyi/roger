package main

import (
	"regexp"
	"testing"
)

// The OpenAPI document is EMBEDDED and SERVED at /openapi.yaml, so its info.version is a
// public claim about which broker a client is talking to - sitting beside /version, which
// makes the same claim from a different source.
//
// Nothing kept the two in step. At the v6.0.0 bump the spec still said 5.7.1 while the
// endpoint said 6.0.0, and every gate stayed green: the spec is data, the test that fetches
// it only checks the status code and content type, and no reader compares the number to
// anything. An integrator diffing the two would have found the contradiction before we did.
//
// This pins the document to the binary it ships inside. It deliberately does NOT name a
// version - a literal here is the same trap that made version_test.go and
// release_security_test.go fail the bump for no reason.
func TestOpenAPIVersionMatchesTheBinary(t *testing.T) {
	m := regexp.MustCompile(`(?m)^info:\n(?:.*\n)*?  version: (\S+)$`).FindStringSubmatch(openapiSpec)
	if m == nil {
		t.Fatal("no info.version in the embedded OpenAPI document: the spec is served publicly " +
			"and has to say which release it describes")
	}
	if m[1] != version {
		t.Errorf("openapi.yaml info.version = %q but the broker reports %q at /version. "+
			"Both are public and served by the same process, so a mismatch is a contradiction "+
			"an integrator sees before we do.", m[1], version)
	}
}
