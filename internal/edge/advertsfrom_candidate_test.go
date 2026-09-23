package edge

import (
	"net"
	"testing"
)

// A phone advertises as a CANDIDATE with NO certificate fingerprint and (with the iOS port bug) no
// servable port. It must still be discovered - a candidate is adopted/claimed, never dialed from
// here (features/edge/claim.feature). It must, however, have identified itself (id= in its TXT); a
// bare PTR is noise. A MEMBER (account set) with no fingerprint is still dropped.
func TestAdvertsFromKeepsCandidateWithoutFingerprint(t *testing.T) {
	svc := DefaultService + "." + mdnsDomain
	inst := "n_phone." + svc
	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.17"), Port: 5353}

	// A phone: PTR + TXT (id, empty acct), no SRV, no A, no fp.
	candidate := &message{answers: []record{
		{typ: typePTR, name: svc, ptr: inst},
		{typ: typeTXT, name: inst, txt: []string{"id=n_phone", "acct=", "kind=mobile", "caps=classify", "nm=gentle-ibex-14"}},
	}}
	got := advertsFrom(candidate, DefaultService, from)
	if len(got) != 1 {
		t.Fatalf("a candidate without a fingerprint should be discovered, got %d", len(got))
	}
	if got[0].NodeID != "n_phone" || got[0].Account != "" || got[0].Kind != "mobile" {
		t.Errorf("candidate parsed wrong: %+v", got[0])
	}
	if got[0].Name != "gentle-ibex-14" {
		t.Errorf("candidate should carry its advertised friendly name, got %q", got[0].Name)
	}

	// A bare PTR with no TXT is NOT a candidate (no self-identification).
	bare := &message{answers: []record{{typ: typePTR, name: svc, ptr: inst}}}
	if got := advertsFrom(bare, DefaultService, from); len(got) != 0 {
		t.Errorf("a bare PTR with no id must be ignored, got %d", len(got))
	}
}
