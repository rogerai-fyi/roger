package main

// edgeinvite.go is `roger edge invite` (features/join/invite.feature, "roger edge invite needs no
// joiner key first"; ruling 2026-10-10). The owner mints a key-less invite on the Edge authority;
// it prints one `roger join <invite>` line, and the first device that presents the code is
// admitted. `roger edge authority allow <key>` still works for an owner who wants it.

import (
	"fmt"
	"net"
	"os"
	"strings"

	"rogerai.fm/roger/v6/internal/edgeauth"
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
	code, err := local.MintBearerCode()
	if err != nil {
		return err
	}
	fp := local.Descriptor().Fingerprint
	at := argv.flags["at"]
	if at == "" {
		b, rerr := os.ReadFile(edgeAuthorityPortFile())
		port := strings.TrimSpace(string(b))
		if ips := edgeLANIPs(); rerr == nil && port != "" && len(ips) > 0 {
			at = net.JoinHostPort(ips[0], port)
		}
	}
	if at == "" {
		fmt.Println("start roger on this machine so the authority answers on your network, then run this again.")
		fmt.Println("  (or name the address it answers on: roger edge invite --at <address>:<port>)")
		return nil
	}
	fmt.Println("on the device you are adding, run:")
	fmt.Printf("  roger join %s\n", edgeInvite(at, fp, code))
	fmt.Printf("works once, for the next %d minutes. root %s - the device shows the same when it has joined.\n",
		int(edgeauth.CodeLifetime.Minutes()), edgeauth.ShortFingerprint(fp))
	return nil
}
