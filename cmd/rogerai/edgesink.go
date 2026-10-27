package main

// WHERE AN ALERT GOES (features/edge/health.feature §5).
//
//	roger edge sink                                   what is configured
//	roger edge sink add discord <webhook url>         somewhere for alerts to go
//	roger edge sink add webhook http://box.local/hook  a box on the LAN, no internet needed
//	roger edge sink test sk_3f2a                      prove it works, without waiting for trouble
//	roger edge sink rm sk_3f2a
//
// An Edge is PRIVATE BY DEFAULT: with no sink configured an alert shows on every fleet surface and
// goes nowhere. The URL an owner hands over IS A SECRET - a webhook URL is a bearer credential - so
// it goes into the store and never comes back out: this file cannot print it even if it tried,
// because the read it has access to does not carry it.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"rogerai.fm/roger/v7/internal/edge"
)

func cmdEdgeSink(cfg config, args []string) error {
	leaf, _ := edgeLeaf("sink")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	l := edgeAlertLog()
	if len(argv.pos) == 0 {
		return edgePrintSinks(os.Stdout, l)
	}
	switch argv.pos[0] {
	case "add":
		if len(argv.pos) < 3 {
			return usagef("usage: roger edge sink add <%s> <url>", strings.Join(edgeSinkKindWords(), "|"))
		}
		s, err := l.AddSink(edge.SinkKind(strings.ToLower(argv.pos[1])), argv.pos[2], time.Now())
		if err != nil {
			return err
		}
		fmt.Printf("alerts now go to %s\n", s.Words())
		fmt.Println("  its URL is stored on this machine only, and nothing reads it back out - not this")
		fmt.Println("  command, not the console, not a log line. Prove it works: roger edge sink test " + s.ID)
		return nil
	case "rm", "remove":
		if len(argv.pos) < 2 {
			return usagef("usage: roger edge sink rm <id>  (`roger edge sink` lists them)")
		}
		had, err := l.RemoveSink(argv.pos[1])
		if err != nil {
			return err
		}
		if !had {
			return fmt.Errorf("no sink has the id %q - `roger edge sink` lists them", argv.pos[1])
		}
		fmt.Printf("dropped %s; no alert goes there again.\n", argv.pos[1])
		return nil
	case "test":
		if len(argv.pos) < 2 {
			return usagef("usage: roger edge sink test <id>  (`roger edge sink` lists them)")
		}
		d, err := l.TestSink(context.Background(), argv.pos[1], time.Now())
		if err != nil {
			return err
		}
		if !d.OK {
			return fmt.Errorf("%s did not take it: %s", d.Words, d.Says)
		}
		fmt.Printf("a clearly-marked test reached %s.\n", d.Words)
		fmt.Println("  it raised no alarm and changed no rule: whoever is on the other end knows it was a drill.")
		return nil
	}
	return usagef("roger edge sink takes add, rm or test, not %q", argv.pos[0])
}

func edgeSinkKindWords() []string {
	out := make([]string, 0, len(edge.SinkKinds()))
	for _, k := range edge.SinkKinds() {
		out = append(out, string(k))
	}
	return out
}

func edgePrintSinks(w io.Writer, l edge.AlertLog) error {
	sinks, err := l.Sinks()
	if err != nil {
		return err
	}
	if len(sinks) == 0 {
		fmt.Fprintln(w, "no sink is configured, so alerts stay on this machine.")
		fmt.Fprintln(w, "  they still show on every fleet surface and in the alert log - they simply go nowhere.")
		fmt.Fprintf(w, "  send them somewhere: roger edge sink add <%s> <url>\n", strings.Join(edgeSinkKindWords(), "|"))
		return nil
	}
	fmt.Fprintf(w, "ALERTS GO TO · %d\n", len(sinks))
	for _, s := range sinks {
		fmt.Fprintf(w, "%-12s %-10s %s\n", s.ID, string(s.Kind), s.Host)
	}
	fmt.Fprintln(w, "  the URL of each is stored on this machine and is never read back out.")
	fmt.Fprintln(w, "  prove one works: roger edge sink test <id>")
	return nil
}
