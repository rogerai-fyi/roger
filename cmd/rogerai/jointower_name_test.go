package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/joininvite"
)

// features/join/invite.feature, "A NAMED INVITE, RUN AS PRINTED" (live E2E 2026-10-28): the name a
// Tower join sends. A named invite joins under its own name; another name is refused here, before
// anything is sent; an invite that names nobody joins under this machine's name.
func TestJoinName(t *testing.T) {
	host := func() (string, error) { return "roggentoo", nil }
	noHost := func() (string, error) { return "", errors.New("no hostname") }
	const refusal = `this invite is for "laptop-x"; run the line as printed, or ask the owner for an invite for your name: roger tower invite --for <your name>`
	for _, tc := range []struct {
		name, forName string
		kind          joininvite.Kind
		pos           []string
		host          func() (string, error)
		want, err     string
	}{
		{name: "named invite, no name given: the invite's name", forName: "laptop-x", kind: joininvite.TowerClient, host: host, want: "laptop-x"},
		{name: "named station invite, no name given", forName: "shelf-gpu", kind: joininvite.TowerStation, host: host, want: "shelf-gpu"},
		{name: "named invite, no hostname: still the invite's name", forName: "laptop-x", kind: joininvite.TowerClient, host: noHost, want: "laptop-x"},
		{name: "named invite, the same name given", forName: "laptop-x", kind: joininvite.TowerClient, pos: []string{"laptop-x"}, host: host, want: "laptop-x"},
		{name: "named invite, another name given", forName: "laptop-x", kind: joininvite.TowerClient, pos: []string{"laptop-y"}, host: host, err: refusal},
		{name: "named invite, a name differing only in case", forName: "laptop-x", kind: joininvite.TowerClient, pos: []string{"LAPTOP-X"}, host: host, err: refusal},
		{name: "named invite, the hostname given explicitly", forName: "laptop-x", kind: joininvite.TowerClient, pos: []string{"roggentoo"}, host: host, err: refusal},
		{name: "named station invite, another name", forName: "shelf-gpu", kind: joininvite.TowerStation, pos: []string{"kitchen"},
			host: host, err: `this invite is for "shelf-gpu"; run the line as printed, or ask the owner for an invite for your name: roger tower invite --for <your name>`},
		{name: "unnamed invite, no name given: the hostname", kind: joininvite.TowerClient, host: host, want: "roggentoo"},
		{name: "unnamed invite, no name, no hostname: empty", kind: joininvite.TowerClient, host: noHost, want: ""},
		{name: "unnamed invite, a name given", kind: joininvite.TowerClient, pos: []string{"laptop-y"}, host: host, want: "laptop-y"},
		{name: "unnamed station invite, a name given", kind: joininvite.TowerStation, pos: []string{"kitchen-pi"}, host: host, want: "kitchen-pi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := joinName(joininvite.Invite{Kind: tc.kind, For: tc.forName}, tc.pos, tc.host)
			if tc.err != "" {
				require.EqualError(t, err, tc.err)
				var r *joinRefusal
				require.ErrorAs(t, err, &r, "a name refusal is final: no other address is tried")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
