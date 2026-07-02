package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// rc_cross_instance_test.go is the multi-instance E2E for BASE STATION (Increment 5): the HOST
// polls one broker instance and a VIEWER streams+sends on a DIFFERENT instance, with both
// instances sharing one session store (the shared Postgres, here a shared Mem) and one Valkey
// bus (here miniredis). It proves a viewer inbound crosses to the host's poll and a host frame
// crosses to the viewer's stream — the whole point of the RC bus.
func TestRCCrossInstanceE2E(t *testing.T) {
	mr := miniredis.RunT(t)
	newBus := func() *valkeyStore {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		if err != nil {
			t.Fatalf("newValkeyStore: %v", err)
		}
		t.Cleanup(func() { _ = vs.Close() })
		return vs
	}

	// One shared session store (the prod shared Postgres), two broker instances each with their
	// OWN bus client to the SAME miniredis, both in multi-instance mode.
	mem := store.NewMem()
	if err := mem.BindOwner(store.Owner{GitHubID: 11, Login: "xi", Pubkey: client.UserPubHex()}); err != nil {
		t.Fatal(err)
	}
	hostBroker := &broker{db: mem, pubOfUser: map[string]string{}, shared: newBus(), multiInstance: true}
	viewBroker := &broker{db: mem, pubOfUser: map[string]string{}, shared: newBus(), multiInstance: true}

	mux := func(b *broker) http.Handler {
		m := http.NewServeMux()
		m.HandleFunc("/rc/enable", b.rcEnable)
		m.HandleFunc("/rc/sessions", b.rcSessions)
		m.HandleFunc("/rc/attach", b.rcAttach)
		m.HandleFunc("/rc/revoke-all", b.rcRevokeAll)
		m.HandleFunc("/rc/", b.rcSubtree)
		return m
	}
	srvHost := httptest.NewServer(mux(hostBroker))
	defer srvHost.Close()
	srvView := httptest.NewServer(mux(viewBroker))
	defer srvView.Close()

	// 1) HOST enables on instance A and starts its bridge (polls srvHost).
	rb, res, err := client.EnableRC(srvHost.URL, "cross-instance · RogerAI")
	if err != nil {
		t.Fatalf("EnableRC: %v", err)
	}
	rb.Run()
	defer rb.Stop()
	go func() {
		for in := range rb.Inbound() {
			if in.Kind == protocol.RCInTurn {
				rb.Emit(protocol.RCFrame{Kind: protocol.RCKindAssistant, Text: "A-host heard: " + in.Text})
			}
		}
	}()

	// 2) VIEWER owner-joins on instance B (session is visible via the shared store).
	att, err := client.JoinRC(srvView.URL, res.SessionID)
	if err != nil {
		t.Fatalf("JoinRC on the peer instance: %v", err)
	}
	if att == "" {
		t.Fatal("owner-join must mint an attach token on the peer instance")
	}

	// 3) VIEWER streams from instance B.
	frames := make(chan protocol.RCFrame, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = client.StreamRC(ctx, srvView.URL, res.SessionID, att, 0, func(f protocol.RCFrame) {
			select {
			case frames <- f:
			case <-ctx.Done():
			}
		})
	}()

	// Let the host poll (subscribed to the inbound bus) and the viewer stream (subscribed to the
	// outbound bus) both establish before we cross a message — pub/sub is fire-and-forget.
	time.Sleep(300 * time.Millisecond)

	// 4) VIEWER sends a turn to instance B; it must cross the bus to the host on instance A, who
	//    answers; the answer must cross back to the viewer's stream on instance B.
	if err := client.SendRC(srvView.URL, res.SessionID, att, protocol.RCInbound{Kind: protocol.RCInTurn, Text: "cross"}); err != nil {
		t.Fatalf("SendRC on the peer instance: %v", err)
	}

	sawUser, sawAssistant := false, false
	timeout := time.After(5 * time.Second)
	for !(sawUser && sawAssistant) {
		select {
		case f := <-frames:
			switch f.Kind {
			case protocol.RCKindUser:
				if f.Text == "cross" {
					sawUser = true
				}
			case protocol.RCKindAssistant:
				if f.Text == "A-host heard: cross" {
					sawAssistant = true
				}
			}
		case <-timeout:
			t.Fatalf("cross-instance relay timed out; sawUser=%v sawAssistant=%v", sawUser, sawAssistant)
		}
	}
}

// TestRCBusHelpers is the unit-level proof that the RC bus primitives cross two clients on one
// backend: publish on one valkeyStore, receive on another; and the shared seq is monotonic.
func TestRCBusHelpers(t *testing.T) {
	mr := miniredis.RunT(t)
	mk := func() *valkeyStore {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		if err != nil {
			t.Fatalf("newValkeyStore: %v", err)
		}
		t.Cleanup(func() { _ = vs.Close() })
		return vs
	}
	pub, sub := mk(), mk()
	sid := "rcs_bus"

	// RCOut: a frame published on one client reaches a subscriber on the other.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, cancelOut, err := sub.busSubscribeRCOut(ctx, sid)
	if err != nil {
		t.Fatalf("busSubscribeRCOut: %v", err)
	}
	defer cancelOut()
	if err := pub.busPublishRCOut(sid, []byte(`{"kind":"assistant","text":"hi"}`)); err != nil {
		t.Fatalf("busPublishRCOut: %v", err)
	}
	select {
	case raw := <-out:
		if string(raw) != `{"kind":"assistant","text":"hi"}` {
			t.Fatalf("unexpected RCOut payload: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RCOut did not cross the bus")
	}

	// RCIn: same, the other direction.
	in, cancelIn, err := sub.busSubscribeRCIn(ctx, sid)
	if err != nil {
		t.Fatalf("busSubscribeRCIn: %v", err)
	}
	defer cancelIn()
	if err := pub.busPublishRCIn(sid, []byte(`{"kind":"turn","text":"go"}`)); err != nil {
		t.Fatalf("busPublishRCIn: %v", err)
	}
	select {
	case raw := <-in:
		if string(raw) != `{"kind":"turn","text":"go"}` {
			t.Fatalf("unexpected RCIn payload: %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RCIn did not cross the bus")
	}

	// Shared seq is monotonic across both clients.
	s1, err := pub.busNextRCSeq(sid)
	if err != nil {
		t.Fatalf("busNextRCSeq: %v", err)
	}
	s2, err := sub.busNextRCSeq(sid)
	if err != nil {
		t.Fatalf("busNextRCSeq: %v", err)
	}
	if !(s1 >= 1 && s2 == s1+1) {
		t.Fatalf("shared seq not monotonic across clients: s1=%d s2=%d", s1, s2)
	}
}
