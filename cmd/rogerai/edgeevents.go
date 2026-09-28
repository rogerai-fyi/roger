package main

// `roger edge events` (features/edge/events.feature): what happened on this Edge, newest first,
// merged from each node's own history and the alert log's crossings. A READ - it records nothing.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
)

// edgeEventsRead is the timeline as the surfaces want it, plus a note when the alert history could
// not be read. A fleet that cannot be read is an error: an empty timeline would be a lie.
func edgeEventsRead(q edge.EventQuery, now time.Time) ([]edge.Event, string, error) {
	st, err := loadEdgeState()
	if err != nil {
		return nil, "", err
	}
	nodes, err := st.list()
	if err != nil {
		return nil, "", err
	}
	edgeAttachOwnHousehold(nodes, now)
	note := ""
	alerts, err := edgeAlertLog().Recent(2000)
	if err != nil {
		note = "the alert history could not be read: " + err.Error()
		alerts = nil
	}
	return edge.Events(nodes, alerts, q, now), note, nil
}

func cmdEdgeEvents(cfg config, args []string) error {
	leaf, _ := edgeLeaf("events")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	now := time.Now()
	var q edge.EventQuery
	if want, ok := argv.flags["node"]; ok {
		st, err := loadEdgeState()
		if err != nil {
			return err
		}
		nodes, err := st.list()
		if err != nil {
			return err
		}
		n, found, err := edgeResolve(nodes, want)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("no node on this Edge is called %q", want)
		}
		q.NodeID = n.ID
	}
	if want, ok := argv.flags["since"]; ok {
		d, err := time.ParseDuration(want)
		if err != nil || d <= 0 {
			return usagef("--since takes a duration like 2h or 30m, not %q", want)
		}
		q.Since = d
	}
	if want, ok := argv.flags["limit"]; ok {
		var n int
		if _, err := fmt.Sscanf(want, "%d", &n); err != nil || n < 0 {
			return usagef("--limit takes a whole number, not %q", want)
		}
		q.Limit = n
	}
	events, note, err := edgeEventsRead(q, now)
	if err != nil {
		return err
	}
	if argv.has("json") {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"events": events, "note": note})
	}
	edgeWriteEvents(os.Stdout, events, note, now)
	return nil
}

func edgeWriteEvents(w io.Writer, events []edge.Event, note string, now time.Time) {
	if note != "" {
		fmt.Fprintln(w, "("+note+")")
	}
	if len(events) == 0 {
		fmt.Fprintln(w, "nothing has happened on this Edge yet.")
		return
	}
	fmt.Fprintf(w, "EVENTS · %d, newest first\n", len(events))
	for _, e := range events {
		mark := " "
		if e.Kind == edge.EventAlert {
			mark = "▲"
		}
		fmt.Fprintf(w, "%s %-7s %-18s %-22s %s\n", mark, edge.Age(now.Sub(e.At)), elideTo(e.Node, 18), elideTo(e.What, 22), e.Detail)
	}
}
