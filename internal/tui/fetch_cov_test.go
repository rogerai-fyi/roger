package tui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeBroker answers the read endpoints the TUI's fetch commands call with canned JSON.
func fakeReadBroker(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/discover":
			_, _ = w.Write([]byte(`{"offers":[{"node_id":"n1","model":"m1","price_out":2,"online":true}]}`))
		case "/balance":
			_, _ = w.Write([]byte(`{"balance":12.5,"logged_in":true,"monthly_cap":100,"monthly_spend":20}`))
		default: // /connect/status (payout)
			_, _ = w.Write([]byte(`{"status":"active","earnings":{"payable":5,"held":1}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestFetchCommands covers the broker-fetch tea.Cmd builders by executing the returned
// command against a fake broker (the on-the-event-loop reads the model dispatches).
func TestFetchCommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // SignRequest mints a key for the signed reads
	b := fakeReadBroker(t)

	// fetchOffers -> offersMsg with the decoded offers.
	if msg := fetchOffers(b)(); func() bool { om, ok := msg.(offersMsg); return ok && len(om) == 1 }() == false {
		t.Errorf("fetchOffers = %T %+v, want offersMsg with 1 offer", msg, msg)
	}
	// An unreachable broker -> errMsg, not a panic.
	if _, ok := fetchOffers("http://127.0.0.1:0")().(errMsg); !ok {
		t.Error("fetchOffers(unreachable) should yield errMsg")
	}

	// fetchBalance -> balanceMsg.
	if bm, ok := fetchBalance(b, "u_gh_1")().(balanceMsg); !ok || bm.balance != 12.5 || !bm.loggedIn {
		t.Errorf("fetchBalance = %T, want balanceMsg{12.5, loggedIn}", fetchBalance(b, "u")())
	}

	// fetchPayoutStatus -> payoutStatusMsg (best-effort; never errors out).
	if _, ok := fetchPayoutStatus(b)().(payoutStatusMsg); !ok {
		t.Errorf("fetchPayoutStatus should yield payoutStatusMsg")
	}
}

// TestSendChat covers the chat relay command: an unreachable broker surfaces a
// chatErrMsg INLINE (the silent-no-response fix), not a footer error or a panic.
func TestSendChat(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, ok := sendChat("http://127.0.0.1:0", "u", "m", "hello", false, 0)().(chatErrMsg); !ok {
		t.Error("sendChat(unreachable) should yield chatErrMsg")
	}
}
