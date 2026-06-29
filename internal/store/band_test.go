package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestBandStoreCRUD covers the band store on BOTH backends: create, the three
// lookups (by code hash / by node / by owner), owner-scoped revoke, the active-count
// cap helper, the model allow-list round-trip, and cross-owner isolation.
func TestBandStoreCRUD(t *testing.T) {
	for name, m := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			b := Band{ID: "band_1", CodeHash: "h1", CodeDisplay: "147.520 MHz · 8F3K-9M2Q",
				Owner: "owner1", NodeID: "n1", Models: []string{"qwen", "llama"}}
			if err := m.CreateBand(b); err != nil {
				t.Fatalf("CreateBand: %v", err)
			}
			// A second owner's band, to prove owner-scoping below.
			if err := m.CreateBand(Band{ID: "band_2", CodeHash: "h2", Owner: "owner2", NodeID: "n2"}); err != nil {
				t.Fatalf("CreateBand(2): %v", err)
			}

			// by code hash (+ JSONB model allow-list round-trip)
			got, ok, _ := m.BandByCodeHash("h1")
			if !ok || got.ID != "band_1" {
				t.Fatalf("BandByCodeHash = %+v ok=%v, want band_1", got, ok)
			}
			if len(got.Models) != 2 || got.Models[0] != "qwen" || got.Models[1] != "llama" {
				t.Errorf("model allow-list round-trip = %v, want [qwen llama]", got.Models)
			}
			if _, ok, _ := m.BandByCodeHash("nope"); ok {
				t.Errorf("BandByCodeHash(unknown) ok=true, want false")
			}
			// by node
			if got, ok, _ := m.BandByNode("n1"); !ok || got.ID != "band_1" {
				t.Errorf("BandByNode = %+v ok=%v, want band_1", got, ok)
			}
			if _, ok, _ := m.BandByNode("ghost"); ok {
				t.Errorf("BandByNode(unknown) ok=true, want false")
			}
			// by owner (scoped: owner1 sees only its own)
			list, _ := m.BandsByOwner("owner1")
			if len(list) != 1 || list[0].ID != "band_1" {
				t.Errorf("BandsByOwner(owner1) = %+v, want only band_1", list)
			}

			// active count (cap enforcement point) = 1 while live.
			if n, _ := m.CountActiveBands("owner1", now); n != 1 {
				t.Errorf("CountActiveBands = %d, want 1", n)
			}

			// owner-scoped revoke: another owner can't touch it.
			if ok, _ := m.SetBandRevoked("band_1", "other", true); ok {
				t.Errorf("SetBandRevoked by wrong owner succeeded - must be owner-scoped")
			}
			if ok, _ := m.SetBandRevoked("band_1", "owner1", true); !ok {
				t.Errorf("SetBandRevoked by owner failed")
			}
			// Revoked band drops out of the active count (so a freed slot lets a new band mint).
			if n, _ := m.CountActiveBands("owner1", now); n != 0 {
				t.Errorf("CountActiveBands after revoke = %d, want 0", n)
			}
			// ... but is still revoked when looked up (resolve treats it as a uniform miss).
			if got, _, _ := m.BandByCodeHash("h1"); !got.Revoked {
				t.Errorf("revoked band not marked revoked on lookup")
			}
		})
	}
}

// TestRemaskBandDisplays is the SECURITY MIGRATION pin (Mem + Postgres): a band persisted
// with the OLD recoverable display ("freq · TAIL", which CanonicalBandTail/BandCodeHash
// resolve straight out of stored state) is re-masked so its stored display can no longer
// reconstruct or resolve the band - while the code_hash (the owner's saved-code lookup
// key) is left UNCHANGED, so the one-time full code still resolves. IDEMPOTENT: a second
// run re-masks nothing. An already-masked band is never touched.
func TestRemaskBandDisplays(t *testing.T) {
	for name, m := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			const tail = "8F3K9M2Q"
			const legacy = "147.520 MHz · 8F3K-9M2Q" // pre-fix the display WAS the resolvable code
			hash := protocol.BandCodeHash(tail)
			// Premise: the legacy display resolves the band (the vulnerability being fixed).
			if protocol.BandCodeHash(legacy) != hash {
				t.Fatalf("legacy display %q must hash to the band - test premise wrong", legacy)
			}
			if err := m.CreateBand(Band{ID: "band_legacy", CodeHash: hash,
				CodeDisplay: legacy, Owner: "o", NodeID: "n1"}); err != nil {
				t.Fatalf("CreateBand(legacy): %v", err)
			}
			// A band already in the masked form must be LEFT UNTOUCHED (so the count is honest
			// and a re-run is a no-op).
			_, freshDisplay, freshTail := protocol.NewBandCode()
			if err := m.CreateBand(Band{ID: "band_fresh", CodeHash: protocol.BandCodeHash(freshTail),
				CodeDisplay: freshDisplay, Owner: "o", NodeID: "n2"}); err != nil {
				t.Fatalf("CreateBand(fresh): %v", err)
			}

			n, err := m.RemaskBandDisplays()
			if err != nil {
				t.Fatalf("RemaskBandDisplays: %v", err)
			}
			if n != 1 { // only the legacy row changes; the already-masked one is skipped
				t.Errorf("re-masked %d bands, want 1 (only the legacy display)", n)
			}

			got, ok, _ := m.BandByCodeHash(hash)
			if !ok {
				t.Fatalf("band lost after migration - the code_hash must be unchanged")
			}
			if got.CodeHash != hash {
				t.Errorf("code_hash changed by migration: %q != %q", got.CodeHash, hash)
			}
			if got.CodeDisplay == legacy {
				t.Errorf("legacy display not re-masked: still %q", got.CodeDisplay)
			}
			if td := protocol.CanonicalBandTail(got.CodeDisplay); td != "" {
				t.Errorf("re-masked display %q still recoverable (tail %q)", got.CodeDisplay, td)
			}
			if protocol.BandCodeHash(got.CodeDisplay) == hash {
				t.Errorf("re-masked display %q still resolves the band", got.CodeDisplay)
			}
			// The owner's saved FULL code still resolves (hash intact).
			if protocol.BandCodeHash(legacy) != got.CodeHash {
				t.Errorf("owner's saved full code no longer resolves after migration")
			}

			// Idempotent: a second run re-masks nothing and disturbs neither row.
			n2, err := m.RemaskBandDisplays()
			if err != nil {
				t.Fatalf("RemaskBandDisplays (2nd): %v", err)
			}
			if n2 != 0 {
				t.Errorf("second migration re-masked %d, want 0 (idempotent)", n2)
			}
			if again, _, _ := m.BandByCodeHash(protocol.BandCodeHash(freshTail)); again.CodeDisplay != freshDisplay {
				t.Errorf("already-masked band was disturbed: %q != %q", again.CodeDisplay, freshDisplay)
			}
		})
	}
}

// TestBandExpiryCount verifies the active-count helper excludes EXPIRED bands (not
// just revoked ones) on both backends - the cap must free a slot when a band lapses.
func TestBandExpiryCount(t *testing.T) {
	for name, m := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			// One live (never-expiring) + one already expired.
			_ = m.CreateBand(Band{ID: "live", CodeHash: "lh", Owner: "o", NodeID: "nl", ExpiresAt: 0})
			_ = m.CreateBand(Band{ID: "gone", CodeHash: "gh", Owner: "o", NodeID: "ng",
				ExpiresAt: now.Add(-time.Hour).Unix()})
			if n, _ := m.CountActiveBands("o", now); n != 1 {
				t.Errorf("CountActiveBands with one expired = %d, want 1", n)
			}
		})
	}
}

// TestBandExpiryAndQuota verifies Active/Expired and the Phase-1 free quota of 1.
func TestBandExpiryAndQuota(t *testing.T) {
	now := time.Now()
	live := Band{ExpiresAt: 0}
	if !live.Active(now) {
		t.Errorf("never-expiring band should be active")
	}
	expired := Band{ExpiresAt: now.Add(-time.Hour).Unix()}
	if expired.Active(now) {
		t.Errorf("expired band should be inactive")
	}
	if !expired.Expired(now) {
		t.Errorf("expired band Expired() = false")
	}
	if q := BandQuota("anyone"); q != 1 {
		t.Errorf("Phase-1 BandQuota = %d, want 1", q)
	}
}

// TestBandModelDenied verifies the per-band model allow-list.
func TestBandModelDenied(t *testing.T) {
	open := Band{} // empty Models = any
	if open.ModelDenied("anything") {
		t.Errorf("empty model list should allow any model")
	}
	scoped := Band{Models: []string{"qwen", "llama"}}
	if scoped.ModelDenied("qwen") {
		t.Errorf("allowed model denied")
	}
	if !scoped.ModelDenied("gpt") {
		t.Errorf("disallowed model not denied")
	}
}
