package main

// edgeinvite.go is `roger edge invite` (features/join/invite.feature, "roger edge invite needs no
// joiner key first"; ruling 2026-10-10). The owner mints a key-less invite on the Edge authority;
// it prints one `roger join <invite>` line, and the first device that presents the code is
// admitted. `roger edge authority allow <key>` still works for an owner who wants it.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/look"
	"rogerai.fm/roger/v7/internal/qr"
	"rogerai.fm/roger/v7/internal/store"
)

func cmdEdgeInvite(cfg config, args []string) error {
	leaf, _ := edgeLeaf("invite")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return err
	}
	if !ok {
		return usagef("this machine is not an Edge authority, so it has no Edge to invite to.\n" +
			"  designate it first: roger edge authority local <name>")
	}
	// --for binds the invite to one device (features/edge/empty_edge_simple.feature §7): the device
	// is named by what `roger edge nearby` prints, and only its node key can redeem the code.
	var forNode store.EdgeNode
	if want, ok := argv.flags["for"]; ok {
		if forNode, err = edgeInviteTarget(want); err != nil {
			return err
		}
	}
	at := edgeInviteAddr(argv.flags["at"])
	if at == "" {
		fmt.Println("start roger on this machine so the authority answers on your network, then run this again.")
		fmt.Println("  (or name the address it answers on: roger edge invite --at <address>:<port>)")
		return nil
	}
	invite, err := edgeMintInvite(local, at, forNode)
	if err != nil {
		return err
	}
	line := joininvite.JoinLine(invite)
	if forNode.ID != "" {
		fmt.Printf("on %s (and only on it), run:\n", edge.UnitName(forNode, nil))
	} else {
		fmt.Println("on the device you are adding, run:")
	}
	fmt.Printf("  %s\n", line)
	// An invite for one device is for a device in the room: the QR is always drawn for it to scan.
	if argv.has("qr") || forNode.ID != "" || isatty.IsTerminal(os.Stdout.Fd()) {
		qr.Show(os.Stdout, line)
	}
	fmt.Printf("works once, for the next %d minutes. root %s - the device shows the same when it has joined.\n",
		int(edgeauth.CodeLifetime.Minutes()), edgeauth.ShortFingerprint(local.Descriptor().Fingerprint))
	return nil
}

// edgeInviteAddr is the address an invite names: the one asked for, else this machine's first LAN
// address and the port its authority last answered on, else nothing (the authority is not up).
func edgeInviteAddr(at string) string {
	if at != "" {
		return at
	}
	b, err := os.ReadFile(edgeAuthorityPortFile())
	port := strings.TrimSpace(string(b))
	if ips := edgeLANIPs(); err == nil && port != "" && len(ips) > 0 {
		return net.JoinHostPort(ips[0], port)
	}
	return ""
}

// edgeMintInvite mints one invite on the authority and returns it (the roger://join form). With a device
// named it is bound to that node id; a device that joins by claim (no certificate, a phone) also gets
// its claim granted, so the claim poll it is already running is answered too. The TUI's i and
// `roger edge invite --for` both come here, so the two can never mint different things.
func edgeMintInvite(local *edgeauth.Local, at string, n store.EdgeNode) (string, error) {
	if n.ID != "" && n.Pin == "" {
		if err := edgeAdoptByClaim(n.ID, n.Name); err != nil {
			return "", err
		}
	}
	code, err := local.MintBearerCodeFor(n.ID)
	if err != nil {
		return "", err
	}
	return edgeInviteFor(at, local.Descriptor().Fingerprint, code, n.ID), nil
}

// edgeInviteTarget is the device --for names: one device seen on this network, by its node id, an
// unambiguous prefix of it, or its name. Two devices sharing a name are refused by name and told
// apart by id, never resolved to the first.
func edgeInviteTarget(want string) (store.EdgeNode, error) {
	st, err := loadEdgeState()
	if err != nil {
		return store.EdgeNode{}, err
	}
	c, ok, err := edgeResolve(st.candidates, want)
	if err != nil {
		return store.EdgeNode{}, err
	}
	if !ok {
		return store.EdgeNode{}, usagef("no device %q has been seen on this network (roger edge nearby lists them)", want)
	}
	return c, nil
}

// edgeInviteForNode is i on a nearby row: the same bound invite `roger edge invite --for` mints, at
// the address this machine's authority answers on.
func edgeInviteForNode(n store.EdgeNode) (string, time.Time, error) {
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	if err != nil {
		return "", time.Time{}, err
	}
	if !ok {
		return "", time.Time{}, errors.New(look.NearbyNoEdgeYet)
	}
	at := edgeInviteAddr("")
	if at == "" {
		return "", time.Time{}, errors.New(look.NearbyNotServing)
	}
	line, err := edgeMintInvite(local, at, n)
	return line, time.Now().Add(edgeauth.CodeLifetime), err
}
