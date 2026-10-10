package main

// join.go is `roger join <invite>`: the one verb for every local join (features/join/invite.feature).
// The invite says what it joins: an Edge (the device enrolls, checking the root by the code's
// proof) or a Tower (as a client or a Station). `roger lan join` and `roger edge enroll --invite`
// are aliases.

import (
	"context"
	"os"
	"strings"

	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/lanmode"
)

const joinUsage = "usage: roger join <invite> [name] [--yes]  (the owner prints the invite with roger tower invite or roger edge invite)\n   or: roger join <address> [name] [--station]  (ask the Tower to let you in; the owner approves)"

func cmdJoin(cfg config, args []string) error {
	var pos, rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return usagef("%s", joinUsage)
	}
	in, err := joininvite.Parse(pos[0])
	if err != nil {
		// Not an invite: an address to ask (request-and-approve).
		addr, aerr := lanmode.ParseAddr(pos[0])
		if aerr != nil {
			return usagef("%v\n%s", err, joinUsage)
		}
		if v := lanmode.Classify(context.Background(), addr, nil); !v.Local {
			return usagef("%s is not a local address (%s): a local join only reaches machines on your network", pos[0], lanmode.LocalRanges)
		}
		name := ""
		if len(pos) > 1 {
			name = pos[1]
		} else if h, herr := os.Hostname(); herr == nil {
			name = h
		}
		station := false
		for _, f := range rest {
			station = station || f == "--station"
		}
		return joinPairAt(os.Stdout, pos[0], name, station)
	}
	// An invite whose addresses are not local is refused before any connection.
	var local []string
	for _, a := range in.Addrs {
		addr, perr := lanmode.ParseAddr(a)
		if perr != nil {
			continue
		}
		if v := lanmode.Classify(context.Background(), addr, nil); v.Local {
			local = append(local, a)
		}
	}
	if len(local) == 0 {
		return usagef("%s is not a local address (%s): a local invite only joins machines on your network",
			strings.Join(in.Addrs, ", "), lanmode.LocalRanges)
	}
	in.Addrs = local
	if in.Kind == joininvite.Edge {
		ea := []string{}
		if len(pos) > 1 {
			ea = append(ea, pos[1])
		}
		ea = append(ea, "--invite", in.String())
		return cmdEdgeEnroll(cfg, append(ea, rest...))
	}
	return joinTower(cfg, in, pos[1:], rest)
}
