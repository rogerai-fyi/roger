package main

// join.go is `roger join <invite>`: the one verb for every local join (features/join/invite.feature).
// The invite says what it joins: an Edge (the device enrolls, checking the root by the code's
// proof) or a Tower (as a client or a Station). `roger lan join` and `roger edge enroll --invite`
// are aliases.

import (
	"context"
	"strings"

	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/lanmode"
)

const joinUsage = "usage: roger join <invite> [name] [--yes]  (the owner prints the invite with roger tower invite or roger edge invite)"

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
		return usagef("%v\n%s", err, joinUsage)
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
