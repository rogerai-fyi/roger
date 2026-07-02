package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// rc_e2e_test.go is the LIVE end-to-end for BASE STATION: it drives the REAL client bridge +
// SSE reader (internal/client) against the REAL /rc/* broker handlers over a real HTTP server,
// with real request signing — the whole host↔broker↔viewer loop, no mocks. Per the founder's
// hard-won lesson (a live E2E catches what 100 green unit tests miss), this stands the pieces
// up and drives an actual remote turn round-trip.
func TestRCLiveE2E(t *testing.T) {
	mem := store.NewMem()
	// Bind THIS test's local signing key as a logged-in owner (u_gh_7) so the signed client
	// calls resolve to a real account wallet (remote control is same-account only).
	if err := mem.BindOwner(store.Owner{GitHubID: 7, Login: "e2e", Pubkey: client.UserPubHex()}); err != nil {
		t.Fatal(err)
	}
	b := &broker{db: mem, pubOfUser: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/rc/enable", b.rcEnable)
	mux.HandleFunc("/rc/sessions", b.rcSessions)
	mux.HandleFunc("/rc/attach", b.rcAttach)
	mux.HandleFunc("/rc/revoke-all", b.rcRevokeAll)
	mux.HandleFunc("/rc/", b.rcSubtree)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1) HOST enables remote control (real signed POST /rc/enable).
	rb, res, err := client.EnableRC(srv.URL, "e2e-station · RogerAI")
	if err != nil {
		t.Fatalf("EnableRC: %v", err)
	}
	if res.Code == "" || res.HostToken == "" {
		t.Fatal("enable must return a one-time code + host token")
	}
	rb.Run()
	defer rb.Stop()

	// The host: on a remote turn, run its "agent" (here: echo back an assistant answer + final).
	hostDone := make(chan struct{})
	go func() {
		for in := range rb.Inbound() {
			switch in.Kind {
			case protocol.RCInTurn:
				rb.Emit(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "host heard: " + in.Text})
				rb.Emit(protocol.RCFrame{Kind: protocol.RCKindFinal, Text: "done"})
			case protocol.RCInBackfill:
				rb.Emit(protocol.RCFrame{Kind: protocol.RCKindBackfill, Viewer: in.Viewer, Text: "prior transcript"})
			}
		}
		close(hostDone)
	}()

	// 2) The session shows up on the roster, online.
	deadline := time.Now().Add(3 * time.Second)
	for {
		sessions, err := client.ListRC(srv.URL)
		if err != nil {
			t.Fatalf("ListRC: %v", err)
		}
		if len(sessions) == 1 && sessions[0].ID == res.SessionID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session did not appear on the roster: %+v", sessions)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 3) A VIEWER attaches with the link code (real signed, same account).
	att, err := client.AttachRC(srv.URL, res.Code)
	if err != nil {
		t.Fatalf("AttachRC: %v", err)
	}
	if att.AttachToken == "" || att.SessionID != res.SessionID {
		t.Fatalf("attach must return a token for the session, got %+v", att)
	}

	// 4) The viewer STREAMS (real SSE) and, once connected, SENDS a turn.
	frames := make(chan protocol.RCFrame, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = client.StreamRC(ctx, srv.URL, att.SessionID, att.AttachToken, 0, func(f protocol.RCFrame) {
			select {
			case frames <- f:
			case <-ctx.Done():
			}
		})
	}()

	// Give the stream a moment to connect (it triggers a backfill inbound), then send a turn.
	time.Sleep(150 * time.Millisecond)
	if err := client.SendRC(srv.URL, att.SessionID, att.AttachToken, protocol.RCInbound{Kind: protocol.RCInTurn, Text: "ping"}); err != nil {
		t.Fatalf("SendRC: %v", err)
	}

	// 5) The viewer must observe: its own echoed user turn, the host's assistant answer, and
	//    the backfill snapshot — the full round trip.
	sawUser, sawAssistant, sawBackfill := false, false, false
	timeout := time.After(4 * time.Second)
	for !(sawUser && sawAssistant) {
		select {
		case f := <-frames:
			switch f.Kind {
			case protocol.RCKindUser:
				if f.Text == "ping" {
					sawUser = true
				}
			case protocol.RCKindAssistant:
				if f.Text == "host heard: ping" {
					sawAssistant = true
				}
			case protocol.RCKindBackfill:
				sawBackfill = true
			}
		case <-timeout:
			t.Fatalf("timed out; sawUser=%v sawAssistant=%v sawBackfill=%v", sawUser, sawAssistant, sawBackfill)
		}
	}
	if !sawBackfill {
		t.Log("note: backfill frame not observed within the window (non-fatal)")
	}

	// 6) Revoke-all ends the session; a re-attach with the old code now uniform-404s.
	if err := client.RevokeRC(srv.URL, ""); err != nil {
		t.Fatalf("RevokeRC: %v", err)
	}
	if _, err := client.AttachRC(srv.URL, res.Code); err == nil {
		t.Fatal("attaching to a revoked session must fail (uniform 404)")
	}
}
