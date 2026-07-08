package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// confirmGate tracks whether the host is currently awaiting a tool confirm (and its id), set
// by the stream reader on a confirm_req frame and cleared on confirm_done. The input loop only
// treats a bare y/n as a confirm ANSWER while a confirm is actually pending — so a
// conversational "y" can never silently approve a mutating tool on the host.
type confirmGate struct {
	mu      sync.Mutex
	pending bool
	id      string
}

func (g *confirmGate) set(id string) { g.mu.Lock(); g.pending, g.id = true, id; g.mu.Unlock() }
func (g *confirmGate) clear()        { g.mu.Lock(); g.pending = false; g.mu.Unlock() }
func (g *confirmGate) take() (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.pending {
		return false, ""
	}
	g.pending = false
	return true, g.id
}

// remote.go is the `roger remote` CLI: manage + drive your private BASE STATION sessions from
// another terminal. It is the second-surface VIEWER (attach + stream + interleave input) and
// the roster/admin surface (list · off · link). The host itself enables remote control from
// inside the [0] AGENT with /remote-control. All calls are same-account (signed with the local
// user key). See docs-internal/REMOTE-CONTROL-DESIGN.md.

// rcLinkURL builds the shareable deep link for a session's short code. The code rides in the
// URL FRAGMENT (#) so it never reaches the broker's server logs.
func rcLinkURL(short string) string {
	// r.html (not a bare /r): the site is a static host that serves exact paths only, so the
	// .html is explicit. The code rides in the FRAGMENT (#) so it never reaches server logs.
	if short == "" {
		return "https://rogerai.fyi/r.html"
	}
	return "https://rogerai.fyi/r.html#" + short
}

func cmdRemote(cfg config, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "ls":
		return remoteList(cfg)
	case "attach", "join":
		if len(args) < 2 {
			return fmt.Errorf("usage: roger remote attach <code>   (the session's link code)")
		}
		return remoteAttach(cfg, strings.Join(args[1:], " "))
	case "off", "stop", "revoke":
		id := ""
		if len(args) > 1 {
			id = args[1]
		}
		return remoteOff(cfg, id)
	case "link":
		if len(args) > 1 {
			return remoteLinkCode(cfg, args[1])
		}
		return remoteLinkHelp()
	default:
		return fmt.Errorf("unknown: roger remote %q · try: list · attach <code> · off [id] · link", sub)
	}
}

// remoteList prints the honesty line then the roster.
func remoteList(cfg config) error {
	fmt.Println("remote sessions are private to your account, relayed via the broker (TLS, not E2E), and run tools on the host machine")
	sessions, err := client.ListRC(cfg.Broker)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("\nno remote sessions — run /remote-control inside `roger` (the [0] AGENT) on a machine to put one on the air")
		return nil
	}
	fmt.Println()
	for _, s := range sessions {
		dot := "○ offline"
		if s.Online && !s.Revoked {
			dot = "● live"
		} else if s.Revoked {
			dot = "· ended"
		}
		fmt.Printf("  %-8s %-24s %s\n", dot, s.Name, s.ID)
	}
	fmt.Println("\ncontinue one:  roger remote attach <code>   (the link code shown when it was enabled)")
	return nil
}

// remoteAttach exchanges the code for an attach token, streams the live transcript, and lets
// you type turns back into the running host agent. Ctrl-C detaches (the session stays live).
func remoteAttach(cfg config, code string) error {
	att, err := client.AttachRC(cfg.Broker, code)
	if err != nil {
		return err
	}
	fmt.Printf("attached to %q — private, broker-relayed, tools run on the host. ctrl-c detaches.\n\n", att.Name)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	go func() { <-sigc; cancel() }()

	// A background reader lets you type turns while the stream prints. The confirm gate
	// ensures a bare y/n is only sent as a confirm ANSWER while the host is actually asking.
	gate := &confirmGate{}
	// os.Stdin is captured HERE, once, and passed in: the loop is fire-and-forget (a
	// terminal read cannot be canceled portably, so it cannot be joined), and a loop
	// re-reading the GLOBAL os.Stdin from that leaked goroutine races anyone who
	// swaps the global later (`go test -race` caught it against the test harness's
	// stdin restore).
	go remoteInputLoop(ctx, cfg.Broker, att.SessionID, att.AttachToken, os.Stdin, gate)

	err = client.StreamRC(ctx, cfg.Broker, att.SessionID, att.AttachToken, 0, func(f protocol.RCFrame) {
		switch f.Kind {
		case protocol.RCKindUser:
			fmt.Printf("▸ (%s) %s\n", f.Origin, f.Text)
		case protocol.RCKindAssistant, protocol.RCKindFinal:
			if strings.TrimSpace(f.Text) != "" {
				fmt.Printf("◂ %s\n", f.Text)
			}
		case protocol.RCKindToolCall:
			fmt.Printf("  ◉ %s\n", f.Tool)
		case protocol.RCKindToolResult:
			fmt.Printf("  ✓ %s\n", f.Tool)
		case protocol.RCKindConfirmReq:
			gate.set(f.ConfirmID)
			fmt.Printf("  ? %s — type 'y' to approve or 'n' to deny (runs on the host)\n", f.Tool)
		case protocol.RCKindConfirmDone:
			gate.clear()
			v := "denied"
			if f.Approve != nil && *f.Approve {
				v = "approved"
			}
			fmt.Printf("  ✓ %s from %s\n", v, f.Origin)
		case protocol.RCKindStatus:
			// A guest-operator handoff (or the DJ-back return): render it so the CLI viewer
			// never sees the stream go dead mid-handoff, matching the TUI + web console. The
			// ONE shared formatter keeps the copy from drifting; "◉" is the CLI's on-air glyph
			// (as on tool_call). Content-blind: only operator/model/spend + the fixed text.
			if line := client.OperatorStatusLine(f, "◉"); strings.TrimSpace(line) != "" {
				fmt.Printf("%s\n", line)
			}
		case protocol.RCKindBackfill:
			if strings.TrimSpace(f.Text) != "" {
				fmt.Printf("%s\n─── (live from here) ───\n", f.Text)
			}
		case protocol.RCKindError:
			fmt.Printf("✕ %s\n", f.Text)
		case protocol.RCKindEnded:
			fmt.Println("— the session ended on the host —")
		}
	})
	if err != nil && ctx.Err() == nil {
		return err
	}
	fmt.Println("\ndetached.")
	return nil
}

// remoteInputLoop reads lines from `in` (the caller's stdin, captured once at spawn -
// never the global, see remoteAttach) and sends each as a turn. A bare y/n/yes/no is sent as a
// CONFIRM answer ONLY while the host is actually awaiting one (the gate) — carrying the confirm
// id so a stale answer can never resolve a different tool; otherwise it is an ordinary turn.
func remoteInputLoop(ctx context.Context, broker, sid, attach string, stdin io.Reader, gate *confirmGate) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := stdin.Read(buf)
		if err != nil {
			return
		}
		text := strings.TrimSpace(string(buf[:n]))
		if text == "" {
			continue
		}
		in := protocol.RCInbound{Kind: protocol.RCInTurn, Text: text}
		switch strings.ToLower(text) {
		case "y", "yes", "n", "no":
			if pending, id := gate.take(); pending {
				approve := strings.HasPrefix(strings.ToLower(text), "y")
				in = protocol.RCInbound{Kind: protocol.RCInConfirm, Approve: approve, ConfirmID: id}
			}
		}
		_ = client.SendRC(broker, sid, attach, in)
	}
}

// remoteOff ends one session (id given) or every session (no id).
func remoteOff(cfg config, id string) error {
	if err := client.RevokeRC(cfg.Broker, id); err != nil {
		return err
	}
	if id == "" {
		fmt.Println("all remote sessions ended.")
	} else {
		fmt.Printf("remote session %s ended.\n", id)
	}
	return nil
}

// remoteLinkCode mints a FRESH one-time link code for a session (id) and prints the code + the
// phone URL — for handing a live session to another device.
func remoteLinkCode(cfg config, sessionID string) error {
	code, short, err := client.RotateRCCode(cfg.Broker, sessionID)
	if err != nil {
		return err
	}
	fmt.Printf("link code (one-time, expires in 10 min): %s\n", code)
	fmt.Printf("open on a phone:  %s\n", rcLinkURL(short))
	fmt.Println("or, from another terminal:  roger remote attach " + short)
	return nil
}

func remoteLinkHelp() error {
	fmt.Println("to put a session on the air, run /remote-control inside `roger` (the [0] AGENT).")
	fmt.Println("it prints a one-time link code + a rogerai.fyi/r.html#<code> URL you can open on your phone.")
	fmt.Println("mint a fresh code for a session:  roger remote link <session-id>")
	fmt.Println("continue a session from here:     roger remote attach <code>")
	return nil
}
