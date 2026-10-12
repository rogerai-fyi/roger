package main

// The j prompt's host side (features/edge/empty_edge_simple.feature §4): what each way a join can
// fail reads as, from the real errors the join path returns, and that an invite or an address is
// told apart the way `roger join` tells them apart.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/look"
)

func TestEdgeJoinPromptSays(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nothing", nil, ""},
		{"no live code (used or expired)", fmt.Errorf("%w: the Edge at x holds no live code", edgeauth.ErrNoRootProof), look.JoinSaysUsedOrExpired},
		{"pin mismatch", fmt.Errorf("%w (at x)", edgeauth.ErrPinMismatch), look.JoinSaysOtherEdge},
		{"proof mismatch", fmt.Errorf("%w (3 attempts left)", edgeauth.ErrRootProofMismatch), look.JoinSaysOtherEdge},
		{"refused", &joinRefusal{msg: "shed refused the request\nask the owner, or use an invite"}, look.JoinSaysRefused},
		{"other refusal", &joinRefusal{msg: "that name is taken\nmore"}, "that name is taken"},
		{"timed out", errPairUnanswered("shed"), look.JoinSaysTimeout},
		{"unreachable", errors.New("could not reach the authority at http://x: dial tcp: connection refused"), look.JoinSaysNoAnswer},
		{"cancelled", errPairCancelled, errPairCancelled.Error()},
		{"anything else", errors.New("first line\nsecond line"), "first line"},
	}
	for _, c := range cases {
		got := edgeJoinPromptSays(c.err)
		if c.want == "" {
			require.NoError(t, got, c.name)
			continue
		}
		require.EqualError(t, got, c.want, c.name)
	}
}

func TestEdgeJoinPromptStartTellsInvitesFromAddresses(t *testing.T) {
	useTempConfig(t)
	cfg := loadConfig()
	fp := strings.Repeat("ab", 32)
	tower := joininvite.Invite{Kind: joininvite.TowerClient, Addrs: []string{"192.168.1.20:8787"}, FP: fp, Code: joincode.Tower.NewCode()}.String()
	far := joininvite.Invite{Kind: joininvite.Edge, Addrs: []string{"203.0.113.9:8791"}, FP: fp, Code: joincode.Edge.NewCode()}.String()

	_, err := edgeJoinPromptStart(cfg, tower)
	require.EqualError(t, err, look.JoinSaysTower)
	_, err = edgeJoinPromptStart(cfg, "roger://join?kind=edge&nonsense")
	require.EqualError(t, err, look.JoinSaysNotInvite)
	_, err = edgeJoinPromptStart(cfg, far)
	require.EqualError(t, err, look.JoinSaysNotLocal, "an invite off this network is refused before any connection")
	_, err = edgeJoinPromptStart(cfg, "192.168.1.10:notaport")
	require.EqualError(t, err, look.JoinSaysNotAddress)
	_, err = edgeJoinPromptStart(cfg, "203.0.113.9:8791")
	require.EqualError(t, err, look.JoinSaysNotLocal)

	// A local address with nothing answering as an Edge authority.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, lerr)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	_, err = edgeJoinPromptStart(cfg, fmt.Sprintf("127.0.0.1:%d", port))
	require.EqualError(t, err, look.JoinSaysNoAnswer)

	// An address that answers as something that is not an Edge authority is "nothing answered" too.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	t.Cleanup(srv.Close)
	_, err = edgeJoinPromptStart(cfg, strings.TrimPrefix(srv.URL, "http://"))
	require.EqualError(t, err, look.JoinSaysNoAnswer)
}
