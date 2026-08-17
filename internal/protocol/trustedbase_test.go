package protocol

import "testing"

// The guard exists so a MITM on a plaintext broker link can never hand back a forged
// grant-signing key (audit M2). https always passes; http passes only to loopback or with
// the explicit env opt-in.
func TestTrustedBase(t *testing.T) {
	ok := []string{
		"https://broker.rogerai.fm",
		"http://127.0.0.1:8080",
		"http://localhost:9999",
		"http://dev.localhost:9999",
		"http://[::1]:8080",
	}
	for _, base := range ok {
		if err := TrustedBase(base); err != nil {
			t.Errorf("TrustedBase(%q) = %v, want nil", base, err)
		}
	}
	bad := []string{
		"http://broker.rogerai.fm",
		"http://10.0.0.5:8080",
		"ftp://broker.rogerai.fm",
		"://not-a-url",
	}
	for _, base := range bad {
		if err := TrustedBase(base); err == nil {
			t.Errorf("TrustedBase(%q) = nil, want refusal", base)
		}
	}
	// The explicit opt-in unlocks plaintext to anywhere - the operator truly meant it.
	t.Setenv(InsecureHTTPEnv, "1")
	if err := TrustedBase("http://broker.internal:8080"); err != nil {
		t.Errorf("with %s=1, TrustedBase(http) = %v, want nil", InsecureHTTPEnv, err)
	}
}
