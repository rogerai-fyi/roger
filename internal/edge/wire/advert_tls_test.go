package wire

import "testing"

// An authority advertises its TLS listener's port as `tls=<port>` (ruled 2026-10-13); anything that is
// not an authority emits no such key, and a reader from before it ignores it.
func TestAnAuthorityAdvertisesItsTLSPort(t *testing.T) {
	txt := Advert{NodeID: "n", AuthPort: 39009, TLSPort: 39443}.TXT()
	found := false
	for _, kv := range txt {
		if kv == "tls=39443" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no tls key in %v", txt)
	}
	if got := advertFromTXT(txt).TLSPort; got != 39443 {
		t.Fatalf("tls port read back as %d", got)
	}
	for _, kv := range (Advert{NodeID: "n"}).TXT() {
		if len(kv) > 4 && kv[:4] == "tls=" {
			t.Fatal("a node that is not an authority advertised a TLS port")
		}
	}
}
