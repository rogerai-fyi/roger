package main

// `roger edge trouble [<node>] [--since <cursor>] [--json]` (features/edge/trouble_feed.feature):
// read a member's trouble feed from a terminal the way the phone does - over the node's pinned
// mTLS face, with this machine's own certificate - and print it in the station's words. The
// phone-less proof that a machine's feed answers, and a diagnostic when it does not.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
)

// edgeCallerCertFn is this machine's certificate (a seam for the test).
var edgeCallerCertFn = edgeCallerCert

// edgeTroubleWatchEvery is the poll cadence of --watch (the phone's own); edgeTroubleWatchMax
// bounds the reads, 0 = until interrupted (the test bounds it).
var (
	edgeTroubleWatchEvery = 30 * time.Second
	edgeTroubleWatchMax   = 0
)

func cmdEdgeTrouble(cfg config, args []string) error {
	leaf, _ := edgeLeaf("trouble")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	target := "."
	if len(argv.pos) == 1 {
		target = argv.pos[0]
	}
	if target == "." || target == "self" {
		target = edgeThisNodeID()
	}
	peer, _, err := edgeResolveNodeFace(target)
	if err != nil {
		return err
	}
	cert := edgeCallerCertFn()
	if cert == nil {
		return fmt.Errorf("this machine has no Edge certificate to read a feed with - run `roger edge enroll` first")
	}
	if argv.has("watch") {
		return edgeWatchTrouble(peer, cert, argv.flags["since"])
	}
	feed, err := edgeDialTrouble(context.Background(), peer, cert, argv.flags["since"])
	if err != nil {
		return err
	}
	if argv.has("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(feed)
	}
	edgePrintTroubleFeed(peer.Node, feed)
	return nil
}

// edgePrintTroubleFeed words a feed for a terminal.
func edgePrintTroubleFeed(node string, f edge.TroubleFeed) {
	now := time.Unix(f.At, 0)
	head := fmt.Sprintf("TROUBLE on %s · %d standing", node, len(f.Standing))
	if f.Rate >= 10 {
		head += fmt.Sprintf(" · PILE-UP: %d in 10 min", f.Rate)
	} else if f.Rate > 0 {
		head += fmt.Sprintf(" · %d in the last 10 min", f.Rate)
	}
	fmt.Println(head)
	if len(f.Standing) == 0 {
		fmt.Println("  ALL CLEAR - nothing is over its line")
	}
	for _, c := range f.Standing {
		ack := "-"
		switch {
		case c.Shelved:
			ack = "STAND BY until " + time.Unix(c.UntilAt, 0).Local().Format("15:04")
		case c.Acked:
			ack = "ROGER " + c.AckBy
		}
		stale := ""
		if c.Stale {
			stale = "  STALE"
		}
		fmt.Printf("  ▲ %-3s %-6s %-18s %-18s %-9s %s%s\n", c.Priority, edge.Age(time.Duration(c.SinceS)*time.Second), c.Node, c.When, c.Value, ack, stale)
		fmt.Printf("        why it matters  %s\n        do              %s\n", c.Why, c.Do)
	}
	if len(f.Transitions) > 0 {
		fmt.Println("  recent")
		for _, tr := range f.Transitions {
			word := "cleared"
			if tr.Firing {
				word = "fired"
			}
			if tr.Test {
				word = "TEST"
			}
			fmt.Printf("    %s  %-8s %s on %s\n", time.Unix(tr.At, 0).Local().Format("15:04"), word, tr.When, tr.Node)
		}
	}
	so := f.SignOff
	if so.Raised > 0 || so.Cleared > 0 || len(so.DeadAir) > 0 {
		line := fmt.Sprintf("  sign-off  this shift  %d raised · %d cleared", so.Raised, so.Cleared)
		if so.Loudest != "" {
			line += fmt.Sprintf(" · loudest %s (%d)", so.Loudest, so.LoudestCount)
		}
		if len(so.DeadAir) > 0 {
			parts := make([]string, 0, len(so.DeadAir))
			for _, d := range so.DeadAir {
				parts = append(parts, d.Node+" "+edge.Age(time.Duration(d.ForS)*time.Second))
			}
			line += " · dead air " + strings.Join(parts, ", ")
		}
		fmt.Println(line)
	}
	fmt.Printf("  read %s · cursor %s\n", now.Local().Format("15:04:05"), f.Cursor)
}

// edgeDialTrouble GETs a member's feed over its pinned face with this machine's certificate.
var edgeDialTrouble = func(ctx context.Context, p client.EdgePeer, cert *tls.Certificate, since string) (edge.TroubleFeed, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // verified below, against the pin, before any byte is sent
		MinVersion:         tls.VersionTLS12,
		Certificates:       []tls.Certificate{*cert},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("the peer presented no certificate")
			}
			if fp := fmt.Sprintf("%x", sha256.Sum256(cs.PeerCertificates[0].Raw)); fp != p.Pin {
				return fmt.Errorf("the peer's certificate does not match its pin")
			}
			return nil
		},
	}
	tr := &http.Transport{TLSClientConfig: tlsCfg, DisableKeepAlives: true,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u := "https://" + p.Addr + edge.TroublePath
	if since != "" {
		u += "?since=" + since
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return edge.TroubleFeed{}, err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return edge.TroubleFeed{}, fmt.Errorf("could not reach %s's face at %s: %w", p.Node, p.Addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return edge.TroubleFeed{}, fmt.Errorf("%s refused the feed: %s", p.Node, e.Error)
	}
	var feed edge.TroubleFeed
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return edge.TroubleFeed{}, fmt.Errorf("%s answered with something that is not a feed: %w", p.Node, err)
	}
	return feed, nil
}

// edgeWatchTrouble polls the feed the way the phone does and prints each transition once: the
// cursor moves forward with every read, the slack re-sends what was seen, and (rule, node, at)
// drops it. It runs until interrupted (or the test's bound).
func edgeWatchTrouble(p client.EdgePeer, cert *tls.Certificate, since string) error {
	fmt.Printf("watching %s every %s · ctrl-c stops\n", p.Node, edgeTroubleWatchEvery)
	seen := map[string]bool{}
	for reads := 0; edgeTroubleWatchMax == 0 || reads < edgeTroubleWatchMax; reads++ {
		if reads > 0 {
			time.Sleep(edgeTroubleWatchEvery)
		}
		feed, err := edgeDialTrouble(context.Background(), p, cert, since)
		if err != nil {
			fmt.Printf("  %s  %v\n", time.Now().Format("15:04:05"), err)
			continue
		}
		for _, tr := range feed.Transitions {
			key := fmt.Sprintf("%s|%s|%d", tr.Rule, tr.NodeID, tr.At)
			if seen[key] {
				continue
			}
			seen[key] = true
			word := "cleared"
			if tr.Firing {
				word = "fired"
			}
			if tr.Test {
				word = "TEST"
			}
			fmt.Printf("  %s  ▲ %-3s %-8s %s on %s\n", time.Unix(tr.At, 0).Local().Format("15:04:05"), tr.Priority, word, tr.When, tr.Node)
		}
		since = feed.Cursor
	}
	return nil
}
