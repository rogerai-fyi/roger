package main

// `roger edge setup` (features/edge/onboard.feature, @cli) - the interactive CLI mirror of
// the TUI wizard. It asks new-or-join, a name, and (join) the authority's address, then drives
// the SAME edgeDesignateCore / edgeEnrollCore the individual commands use. It never invents its
// own enrollment, and when it cannot prompt (no terminal) it declines cleanly and names the
// non-interactive commands instead of guessing.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
)

// edgeIsInteractive reports whether this process can run a prompt. A seam so the spec can
// drive both the interactive and the "no terminal" paths; production reads the real stdin.
var edgeIsInteractive = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

func cmdEdgeSetup(cfg config, args []string) error {
	leaf, _ := edgeLeaf("setup")
	if _, err := parseEdgeArgv(leaf, args); err != nil {
		return err
	}
	if !edgeIsInteractive() {
		// No terminal to walk them through it. Say what the wizard would have done, as the
		// exact commands that do it without a prompt - and change nothing.
		fmt.Println("roger edge setup needs a terminal to walk you through it, and this is not one.")
		fmt.Println("Set up this machine without a prompt using the individual commands:")
		fmt.Println("  start a NEW Edge here (this machine roots it, no internet needed):")
		fmt.Println("    roger edge authority local <name>")
		fmt.Println("    roger edge enroll <name>")
		fmt.Println("  JOIN an existing Edge (once its authority has allowed this machine):")
		fmt.Println("    roger edge enroll <name> --authority http://<authority-address>")
		fmt.Println("  the key the authority must allow (run on the authority):")
		fmt.Println("    roger edge authority allow " + client.UserPubHex())
		return nil
	}

	r := bufio.NewReader(edgeStdin)
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := r.ReadString('\n')
		return strings.TrimSpace(line)
	}

	fmt.Println("Set up this machine's Edge.")
	choice := strings.ToLower(ask("Start a [n]ew Edge (this machine becomes the authority), or [j]oin an existing one? [N/j]: "))
	join := strings.HasPrefix(choice, "j")

	def := edgeDefaultName()
	name := ask(fmt.Sprintf("A name for this machine [%s]: ", def))
	if name == "" {
		name = def
	}
	if err := edge.ValidName(name); err != nil {
		return usagef("%v", err)
	}

	if join {
		return edgeSetupJoin(cfg, name, ask)
	}
	return edgeSetupNew(cfg, name)
}

// edgeSetupNew designates this machine the authority and enrolls it, with no network. It is
// idempotent across the two steps: a re-run after a designate that already happened skips
// re-designating and just finishes the enroll (so it recovers rather than hitting ErrRootExists).
func edgeSetupNew(cfg config, name string) error {
	if _, isAuthority, err := edgeauth.OpenLocal(edgeAuthDir()); err != nil || !isAuthority {
		if _, err := edgeDesignateCore(edgeAuthDir(), name); err != nil {
			return err
		}
	}
	if _, err := edgeEnrollCore(cfg, name, "", client.LinkedLogin()); err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("Your Edge is live, rooted here as %q - mode LOCAL, formed with no internet.\n", name)
	fmt.Println("Add another machine - on it, run:")
	fmt.Println("  " + edgeSetupEnrollHint())
	return nil
}

// edgeSetupJoin enrolls against the authority's address. When the authority does not yet allow
// this machine it guides the allow step and exits WITHOUT pretending the machine joined; every
// other refusal is returned in the authority's own words.
func edgeSetupJoin(cfg config, name string, ask func(string) string) error {
	addr := ask("The authority's address on your network (e.g. http://192.168.1.10:8791): ")
	if addr == "" {
		return usagef("an authority address is needed to join an existing Edge")
	}
	_, err := edgeEnrollCore(cfg, name, addr, client.LinkedLogin())
	if errors.Is(err, edgeauth.ErrUnknownMachine) {
		fmt.Println()
		fmt.Println("This machine is not allowed on that Edge yet, so it did not join.")
		fmt.Println("On the AUTHORITY machine, run:")
		fmt.Println("  roger edge authority allow " + client.UserPubHex())
		fmt.Println("Then run `roger edge setup` again (or `roger edge enroll " + name + " --authority " + addr + "`).")
		return nil
	}
	if err != nil {
		return edgeSetupJoinError(addr, err)
	}
	fmt.Println()
	fmt.Printf("Joined: this machine is on the Edge as %q.\n", name)
	return nil
}

// edgeSetupJoinError frames a failed join for a person. A genuine refusal is left in the
// authority's OWN words (the spec's rule); only a transport failure - the authority not
// answering at all - is reframed to name it as unreachable, with the address the owner typed,
// so they see "could not reach the authority at ..." instead of a raw Go dial error. A
// not-allowed error passes through unchanged for the caller to map to the allow step.
func edgeSetupJoinError(addr string, err error) error {
	if err == nil || errors.Is(err, edgeauth.ErrUnknownMachine) {
		return err
	}
	if strings.Contains(err.Error(), "the authority refused") {
		return err // a real refusal, in the authority's own words
	}
	return fmt.Errorf("could not reach the authority at %s: %v", addr, err)
}

// edgeSetupEnrollHint is the command another machine runs to join, using this machine's
// authority address when it is known, and a clear placeholder when it is not.
func edgeSetupEnrollHint() string {
	return edgeSetupEnrollHintFrom(edgeSelfStatus(nil, edgeDiscoveryFactsFromEnv()))
}

func edgeSetupEnrollHintFrom(self edge.SelfStatus) string {
	if self.AuthorityAddr != "" {
		return "roger edge enroll <name> --authority " + self.AuthorityAddr
	}
	return "roger edge enroll <name> --authority http://<this machine's LAN address>:8791"
}
