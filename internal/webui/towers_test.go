package webui

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/tower"
)

// ownerConsole is a console over the owner's hooks, recording what each action reached.
func ownerConsole(t *testing.T, reached *[]string) (*httptest.Server, string) {
	t.Helper()
	lab := ownerjoin.Tower{Dir: "/srv/lab", Label: "lab", Addr: "192.168.1.20:8787", FP: "9f3c-0000-1111-2222"}
	h := &ownerjoin.Hooks{
		Towers: func() []ownerjoin.Tower { return []ownerjoin.Tower{lab} },
		Requests: func(ownerjoin.Tower) ([]tower.PairRequest, error) {
			return []tower.PairRequest{{ID: "r1", Name: "laptop-x", Phrase: "amber-river-71"}}, nil
		},
		Decide: func(dir, id string, approve bool) error {
			*reached = append(*reached, "decide "+dir+" "+id)
			if id == "gone" {
				return errors.New("that request expired")
			}
			return nil
		},
		Invite: func(t ownerjoin.Tower) (ownerjoin.Invite, error) {
			*reached = append(*reached, "invite "+t.Dir)
			return ownerjoin.Invite{Dir: t.Dir, ID: "i1", Line: "roger join roger://join?kind=tower-client"}, nil
		},
		Outcome: func(dir, id string) (string, bool) { return "laptop-x joined", true },
		Pinned:  func() []ownerjoin.Pinned { return []ownerjoin.Pinned{{Addr: "192.168.1.30:8787", FP: "1234"}} },
	}
	srv := New(testCtrl(), Options{Owner: h})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs, srv.Token()
}

func post(t *testing.T, hs *httptest.Server, tok, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(hs.URL+path+"?t="+tok, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestTheConsoleListsTowersRequestsAndPinsInTheTUIsSentences(t *testing.T) {
	var reached []string
	hs, tok := ownerConsole(t, &reached)
	resp, err := http.Get(hs.URL + "/api/towers?t=" + tok)
	require.NoError(t, err)
	defer resp.Body.Close()
	var v towersView
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	require.Equal(t, "Tower lab · 192.168.1.20:8787 · 9f3c-0000-1111-2222", v.Towers[0].Line)
	require.Equal(t, "laptop-x wants to join · amber-river-71", v.Requests[0].Ask)
	require.Equal(t, "approve only if laptop-x shows the same phrase", v.Requests[0].Hint)
	require.Equal(t, "192.168.1.30:8787 · pinned 1234", v.Pinned[0].Line)
}

func TestAConsoleActionNamesOnlyATowerThisUserServes(t *testing.T) {
	var reached []string
	hs, tok := ownerConsole(t, &reached)
	for _, c := range []struct {
		path string
		body any
	}{
		{"/api/towers/invite", map[string]string{"dir": "/etc"}},
		{"/api/towers/invite", map[string]string{}},
		{"/api/towers/decide", map[string]any{"dir": "/etc", "id": "r1", "approve": true}},
	} {
		resp := post(t, hs, tok, c.path, c.body)
		require.Equal(t, http.StatusNotFound, resp.StatusCode, c.path)
	}
	resp, err := http.Get(hs.URL + "/api/towers/invite/outcome?t=" + tok + "&dir=/etc&id=i1")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Empty(t, reached, "nothing reached a Tower this user does not serve")

	// The served Tower works.
	resp = post(t, hs, tok, "/api/towers/invite", map[string]string{"dir": "/srv/lab"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var inv shownInvite
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inv))
	require.Contains(t, inv.QR, "▀")
	require.NotContains(t, inv.QR, "\x1b", "the console's QR carries no terminal colour codes")
	require.Equal(t, []string{"invite /srv/lab"}, reached)
}

func TestTheConsoleSaysWhenAnAnswerFails(t *testing.T) {
	var reached []string
	hs, tok := ownerConsole(t, &reached)
	resp := post(t, hs, tok, "/api/towers/decide", map[string]any{"dir": "/srv/lab", "id": "gone", "approve": true})
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	resp = post(t, hs, tok, "/api/towers/decide", map[string]any{"dir": "/srv/lab"})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	r, err := http.Get(hs.URL + "/api/towers/invite/outcome?t=" + tok + "&dir=/srv/lab&id=i1&expires_at=1")
	require.NoError(t, err)
	defer r.Body.Close()
	var o map[string]any
	require.NoError(t, json.NewDecoder(r.Body).Decode(&o))
	require.Equal(t, true, o["over"])
	require.True(t, strings.Contains(o["line"].(string), "laptop-x joined"))
	require.Equal(t, "expired", o["left"])
}

func TestTheConsoleShowsNoTowersWithoutTheOwnerHooks(t *testing.T) {
	srv := New(testCtrl(), Options{})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/towers?t=" + srv.Token())
	require.NoError(t, err)
	defer resp.Body.Close()
	var v towersView
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	require.Empty(t, v.Towers)
	r2 := post(t, hs, srv.Token(), "/api/towers/invite", map[string]string{"dir": "/srv/lab"})
	require.Equal(t, http.StatusNotFound, r2.StatusCode)
}
