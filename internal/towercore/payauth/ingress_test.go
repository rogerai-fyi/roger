package payauth

import (
	"sync"
	"testing"
	"time"
)

func hint(id, hash string) Hint {
	return Hint{EventID: id, RawBodyHash: hash, Merchant: "acct_1", SourceID: "pi_1",
		SourceKind: "payment_intent", ReceivedAt: time.Unix(1_700_000_000, 0)}
}

// THE REPLAY/MUTATION TABLE from the spec, row by row.
func TestReplayAndMutationAreDistinguished(t *testing.T) {
	t.Run("exact id and hash sequentially: idempotent, one trigger", func(t *testing.T) {
		s := NewMemIngress()
		out, _, err := s.Admit(hint("E", "H"))
		if err != nil || out != OutcomeFresh {
			t.Fatalf("first delivery = %v/%v, want fresh", out, err)
		}
		out, _, err = s.Admit(hint("E", "H"))
		if err != nil || out != OutcomeDuplicate {
			t.Fatalf("retry = %v/%v, want duplicate - a retry must not schedule a second fetch", out, err)
		}
	})

	t.Run("exact id and hash concurrently: one record, one coalesced trigger", func(t *testing.T) {
		s := NewMemIngress()
		const n = 32
		var wg sync.WaitGroup
		fresh := make(chan struct{}, n)
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				if out, _, err := s.Admit(hint("E", "H")); err == nil && out == OutcomeFresh {
					fresh <- struct{}{}
				}
			}()
		}
		wg.Wait()
		close(fresh)
		if got := len(fresh); got != 1 {
			t.Fatalf("%d concurrent deliveries produced %d fresh outcomes, want exactly 1", n, got)
		}
	})

	t.Run("same id, another body hash: conflict quarantine, nothing guessed", func(t *testing.T) {
		s := NewMemIngress()
		_, _, _ = s.Admit(hint("E", "H"))
		out, rec, err := s.Admit(hint("E", "DIFFERENT"))
		if err != nil || out != OutcomeConflict {
			t.Fatalf("mutated body = %v/%v, want conflict", out, err)
		}
		if !rec.Quarantined() {
			t.Fatal("the record must be quarantined so nothing downstream acts on it")
		}
		if rec.RawBodyHash != "H" {
			t.Fatalf("the original bytes must stand, not be overwritten by the newer ones: %q", rec.RawBodyHash)
		}
		// And it STAYS quarantined - a third delivery of the original bytes does not clear it.
		if out, _, _ := s.Admit(hint("E", "H")); out != OutcomeConflict {
			t.Fatalf("a conflict resolved itself on retry: %v", out)
		}
	})

	t.Run("another id with identical bytes: a distinct hint", func(t *testing.T) {
		s := NewMemIngress()
		_, _, _ = s.Admit(hint("E1", "H"))
		out, _, err := s.Admit(hint("E2", "H"))
		if err != nil || out != OutcomeFresh {
			t.Fatalf("distinct id = %v/%v, want fresh - identical bytes under a unique id is a "+
				"separate event, and the authenticated fetch is what prevents duplicate money", out, err)
		}
	})
}

func TestAnIngressRecordNeedsItsEventID(t *testing.T) {
	if _, _, err := NewMemIngress().Admit(hint("", "H")); err == nil {
		t.Fatal("an empty event id would collapse every anonymous delivery onto one row")
	}
}

// Get had never been run - 0.0% - which matters because it is the read the DURABLE store is
// held to parity against: a reference implementation nobody reads from is a reference for
// nothing. The read must return exactly what Admit recorded, and a miss must be a clean
// (zero, false), never an invented row.
func TestGetReturnsWhatAdmitRecordedAndNothingElse(t *testing.T) {
	m := NewMemIngress()
	if _, _, err := m.Admit(hint("evt-1", "h1")); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.Get("evt-1")
	if err != nil || !ok {
		t.Fatalf("Get(evt-1) = ok=%v err=%v, want the admitted record", ok, err)
	}
	if got.RawBodyHash != "h1" || got.Merchant != "acct_1" || !got.ReceivedAt.Equal(time.Unix(1_700_000_000, 0)) {
		t.Fatalf("Get returned %+v, not what Admit recorded", got)
	}
	if _, ok, err := m.Get("evt-nobody"); err != nil || ok {
		t.Fatalf("a miss must be (zero, false, nil), got ok=%v err=%v", ok, err)
	}
}
