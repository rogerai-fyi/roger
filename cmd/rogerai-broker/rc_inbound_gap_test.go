package main

// rc_inbound_gap_test.go pins that a viewer inbound sent while the host is BETWEEN polls is not
// lost in multi-instance RC (audit finding #5). rcDeliverInbound only PUBLISHed to the bus, and
// the host is subscribed only during a live long-poll, so a /rc send in the poll gap reached 0
// subscribers and Redis dropped it (while rcSend returned 200), hanging the viewer's tool.
// Real two-instance miniredis bus + the real RC client, no mocks. Reuses the E2E harness shape.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func TestRCInboundBufferedAcrossPollGap(t *testing.T) {
	mr := miniredis.RunT(t)
	newBus := func() *valkeyStore {
		vs, err := newValkeyStore("redis://" + mr.Addr())
		if err != nil {
			t.Fatalf("newValkeyStore: %v", err)
		}
		t.Cleanup(func() { _ = vs.Close() })
		return vs
	}
	mem := store.NewMem()
	if err := mem.BindOwner(store.Owner{GitHubID: 11, Login: "xi", Pubkey: client.UserPubHex()}); err != nil {
		t.Fatal(err)
	}
	hostBroker := &broker{db: mem, pubOfUser: map[string]string{}, shared: newBus(), multiInstance: true}
	viewBroker := &broker{db: mem, pubOfUser: map[string]string{}, shared: newBus(), multiInstance: true}
	mux := func(b *broker) http.Handler {
		m := http.NewServeMux()
		m.HandleFunc("/rc/enable", b.rcEnable)
		m.HandleFunc("/rc/attach", b.rcAttach)
		m.HandleFunc("/rc/", b.rcSubtree)
		return m
	}
	srvHost := httptest.NewServer(mux(hostBroker))
	defer srvHost.Close()
	srvView := httptest.NewServer(mux(viewBroker))
	defer srvView.Close()

	// HOST enables but does NOT Run() its bridge -> NO host poll is subscribed = the poll gap.
	rb, res, err := client.EnableRC(srvHost.URL, "gap · RogerAI")
	if err != nil {
		t.Fatalf("EnableRC: %v", err)
	}
	defer rb.Stop()

	att, err := client.JoinRC(srvView.URL, res.SessionID)
	if err != nil {
		t.Fatalf("JoinRC: %v", err)
	}

	// VIEWER sends during the gap. rcSend returns 200; pre-fix this PUBLISHed to 0 subscribers
	// and was dropped.
	if err := client.SendRC(srvView.URL, res.SessionID, att, protocol.RCInbound{Kind: protocol.RCInTurn, Text: "gap"}); err != nil {
		t.Fatalf("SendRC: %v", err)
	}

	// Now the host issues ONE manual long-poll. It must return the buffered inbound quickly.
	req, _ := http.NewRequest(http.MethodGet, srvHost.URL+"/rc/"+res.SessionID+"/poll", nil)
	req.Header.Set("Authorization", "Bearer "+res.HostToken)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("the gap-sent inbound was lost: the host poll returned no inbound and timed out (%v)", err)
	}
	defer resp.Body.Close()
	var in protocol.RCInbound
	_ = json.NewDecoder(resp.Body).Decode(&in)
	if in.Kind != protocol.RCInTurn || in.Text != "gap" {
		t.Fatalf("host poll did not deliver the gap inbound: got kind=%v text=%q (status %d)", in.Kind, in.Text, resp.StatusCode)
	}
}
