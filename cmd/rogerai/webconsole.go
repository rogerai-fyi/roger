package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/node"
	"rogerai.fm/roger/v6/internal/tui"
	"rogerai.fm/roger/v6/internal/webui"
)

// defaultWebuiPort is where the browser node console binds first; if it's taken the
// server scans upward (webui.Listen), so a busy port never dead-ends.
const defaultWebuiPort = "4180"

// webuiEnabled reports whether the browser console should come up. It is ON by default;
// the saved config can opt out (config.Webui=false), and the --no-webui flag forces it
// off for a single run (--webui forces it on). The flags are consumed by stripWebuiFlags.
func (c config) webuiEnabled() bool { return c.Webui == nil || *c.Webui }

// webuiOpenEnabled reports whether the console also auto-opens in a browser at launch.
// OFF by default (founder respec 2026-07-14: on terminal-embedded browsers the auto-open
// trapped the TUI); `roger config set webui-open true` opts back in. Either way the URL
// is printed and `w` (BROWSE) / /webui (AGENT) open it on demand.
func (c config) webuiOpenEnabled() bool { return c.WebuiOpen != nil && *c.WebuiOpen }

// openBrowser is a seam over the guarded default-browser launcher, so a test can
// observe the auto-open decision without spawning a process.
var openBrowser = tui.OpenURL

// webConsoleListen is a seam over webui.Server.Listen so a test can force the bind to
// fail and cover startWebConsole's non-fatal return-"" branch (webui.Listen's OS-picked
// fallback makes a real bind failure otherwise unreachable in a test).
var webConsoleListen = func(s *webui.Server, addr string) (net.Listener, string, error) {
	return s.Listen(addr)
}

// stripWebuiFlags removes the global --no-webui / --webui / --webui-port=N flags from a
// raw argv tail and reports the resulting enabled state + port. They are global (not tied
// to a subcommand), so the dispatcher filters them before reading os.Args[1]; a real
// command keeps all its own args.
func stripWebuiFlags(args []string, enabled bool, port string) (rest []string, outEnabled bool, outPort string) {
	outEnabled, outPort = enabled, port
	for _, a := range args {
		switch {
		case a == "--no-webui":
			outEnabled = false
		case a == "--webui":
			outEnabled = true
		case strings.HasPrefix(a, "--webui-port="):
			if p := strings.TrimPrefix(a, "--webui-port="); p != "" {
				outPort = p
			}
		default:
			rest = append(rest, a)
		}
	}
	return rest, outEnabled, outPort
}

// startWebConsole stands up the localhost browser console over ctrl (the SAME controller
// the TUI/daemon drives), printing the tokenized URL, and returns that URL ("" on a bind
// failure) so the TUI can open it on demand (`w` / /webui). It returns immediately; the
// server runs in a background goroutine for the life of the process. A bind failure is
// non-fatal — the terminal front-end carries on.
// writeLimit is the console's save, and it MERGES rather than replaces.
//
// Storing a whole struct built from just the fields the browser's price form knows about
// silently dropped every other field. It cost a routing rule first (Limit gained Quants,
// the standing quant rule set on the band card with Q) and would cost a money cap next
// (MaxIn, the input-price ceiling set with `roger config set-limit --max-in`): editing an
// output price in the browser threw the other away, with nothing failing and nothing warned.
//
// The merge is now field-general instead of a hand-maintained carry list: Update starts from
// the model's STORED cap and this closure overwrites ONLY the two fields the form actually
// edits (MaxOut, MinTPS), so MaxIn, the quant rule, and anything Limit gains later survive by
// default. Quants is the one field the form CAN edit, over the wire as a pointer: nil means
// "this save did not touch the rule" (keep the stored one), a non-nil value - empty slice
// included - is an explicit instruction. The whole read-modify-write runs under the store's
// lock, so a concurrent TUI edit cannot slip in between the read and the write.
func writeLimit(limits *tui.LimitStore) func(string, webui.SpendLimit) {
	return func(model string, l webui.SpendLimit) {
		limits.Update(model, func(cur tui.Limit) tui.Limit {
			cur.MaxOut = l.MaxOut
			cur.MinTPS = l.MinTPS
			if l.Quants != nil {
				cur.Quants = append([]string(nil), *l.Quants...)
			}
			return cur
		})
	}
}

func startWebConsole(cfg config, ctrl *node.Controller, port string, limits *tui.LimitStore, hooks *tui.Hooks) string {
	s := webui.New(ctrl, webui.Options{
		// THE SAME EDGE the TUI's [3] EDGE screen holds: one fleet, one candidate list, one
		// adopt path, one session ledger. A console with its own would draw a second Edge.
		Edge: edgeHooksFor(cfg, hooks),
		// THE SAME `roger model`, from the browser (modelweb.go).
		Models: modelWebHooks(),
		// THE SAME LAN DIAL the TUI reads, in LAN mode; nil otherwise.
		LANDial: lanDialFor(cfg),
		Broker:  cfg.Broker, User: cfg.User, ClientID: gitHubClientID(),
		// THE SAME STORE the TUI edits, not a copy. The console's spend table and
		// [4] CONFIG are two views of one setting; two stores would let them disagree
		// about what the operator is willing to pay, and the loser is whichever wrote
		// first. Nil-safe: a console with no store shows the table as unavailable.
		ReadLimits: func() map[string]webui.SpendLimit {
			out := map[string]webui.SpendLimit{}
			for m, l := range limits.Snapshot() {
				q := append([]string(nil), l.Quants...)
				out[m] = webui.SpendLimit{MaxOut: l.MaxOut, MinTPS: l.MinTPS, Quants: &q}
			}
			return out
		},
		WriteLimit: writeLimit(limits),
	})
	ln, url, err := webConsoleListen(s, "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web console: could not bind a localhost port:", err)
		return ""
	}
	fmt.Printf("web console → %s\n", url)
	go func() { _ = s.Serve(ln) }()
	// Kick an initial detection in the background so the browser SHARE tab is populated on
	// first paint. The TUI only detects lazily (on entering SHARE), so without this a fresh
	// launch would show an empty table until the user clicked re-detect. Best-effort; the
	// snapshot/SSE picks up whatever it finds, and re-detect can refine it.
	go func() {
		found, _ := ctrl.Detect("", "")
		// No-persist: a passive launch scan populates the table for display but must NOT
		// rewrite saved share config — that's reserved for an explicit re-detect.
		ctrl.LoadRowsNoPersist(found)
	}()
	// Auto-open ONLY when the saved config opts in (webui_open: true) - the default is
	// to just print the URL (founder respec 2026-07-14: the auto-open trapped the TUI
	// under terminal-embedded browsers). The launcher still self-gates on a real
	// interactive terminal, so a headless `roger share` daemon never hijacks a browser.
	if cfg.webuiOpenEnabled() {
		openBrowser(url)
	}
	return url
}

// edgeHooksFor lifts the Edge seams off the TUI hooks for the console. Nil hooks (a
// caller with no TUI) or an unwired Edge leave the console's EDGE tab honestly disabled.
func edgeHooksFor(cfg config, h *tui.Hooks) webui.EdgeHooks {
	if h == nil || h.EdgeFleet == nil {
		return webui.EdgeHooks{}
	}
	return webui.EdgeHooks{
		// WHAT RAISES AN ALARM, from the same log and the same wording the terminal uses, so a rule
		// set in the browser and one set at the prompt are one rule with one id and one scope.
		Rules: func() ([]edge.Rule, error) { return edgeAlertLog().Rules() },
		SetRule: func(metric, op, value, scope string) (string, error) {
			return edgeAlertSetRule(edgeAlertLog(), metric, op, value, scope)
		},
		RuleOff: func(id string, off bool) error { return edgeAlertLog().SetOff(id, off) },
		// WHERE AN ALERT GOES. The URL travels one way: in through AddSink, never back out, because
		// Sinks() cannot return it and the payload has no field for it.
		Sinks: func() ([]edge.Sink, error) { return edgeAlertLog().Sinks() },
		AddSink: func(kind, url string) (edge.Sink, error) {
			return edgeAlertLog().AddSink(edge.SinkKind(kind), url, time.Now())
		},
		RemoveSink: func(id string) (bool, error) { return edgeAlertLog().RemoveSink(id) },
		TestSink: func(id string) (edge.Delivery, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return edgeAlertLog().TestSink(ctx, id, time.Now())
		},
		RemoveRule: func(id string) error { return edgeAlertLog().Remove(id) },
		// ONE MAINTENANCE ACTION on a member, through the very path a typed `roger edge maintain`
		// takes. The console adds no authority of its own: the owner's maintenance switch ON THAT
		// MACHINE and an authority-signed grant carrying maintain still decide, and the task is
		// parsed by the same parser the callee runs, so reboot and shutdown are refused by name.
		ServeModel: edgeServeModelHook, // the same request as `roger model serve <id> --on <node>`
		Maintain: func(node string, t edge.MaintainTask) (edge.MaintainResult, error) {
			r, _, err := edgeMaintainInvokeNamed(cfg, node, t)
			return r, err
		},
		Self: h.EdgeSelf, Fleet: h.EdgeFleet, Candidates: h.EdgeCandidates,
		Adopt: h.EdgeAdopt, Name: h.EdgeName, Forget: h.EdgeForget,
		ConfirmActuate: h.EdgeConfirmActuate,
		SetAgent:       h.EdgeSetAgent, AddAgent: h.EdgeAddAgent,
		AgentActive: h.EdgeAgentActive, SetPrefer: h.EdgeSetPrefer,
		SetAgentJob: h.EdgeSetAgentJob, ClearAgentJob: h.EdgeClearAgentJob,
		Grants: h.EdgeGrants, IssueGrant: h.EdgeIssueGrant, RevokeGrant: h.EdgeRevokeGrant,
		Alerts:   h.EdgeAlerts,
		Roger:    h.EdgeRoger,
		StandBy:  h.EdgeStandBy,
		Floor:    edgeFloorHook(h),
		Events:   edgeEventsHook(),
		Intent:   edgeIntentHook(),
		Adopting: h.EdgeAdopting, PersistentAgents: h.EdgePersistentAgents, AgentJobs: h.EdgeAgentJobs,
		Household: h.EdgeHousehold,
		Sessions:  h.EdgeSessions, Status: h.EdgeStatus,
	}
}

// edgeFloorHook adapts the TUI hook's four values to the console's one struct (nil when unwired).
// edgeEventsHook hands the console the same merged timeline the CLI prints and the fleet screen's
// EVENTS tab draws (features/edge/events.feature), worded once here so the three cannot diverge.
func edgeEventsHook() func(time.Time, int) []webui.EdgeEvent {
	return func(now time.Time, limit int) []webui.EdgeEvent {
		events, _, err := edgeEventsRead(edge.EventQuery{Limit: limit}, now)
		if err != nil {
			return nil // an Edge that cannot be read is said elsewhere; the tab simply has no list
		}
		out := make([]webui.EdgeEvent, 0, len(events))
		for _, e := range events {
			detail := e.Detail
			if e.Count > 1 {
				detail += fmt.Sprintf("   ×%d over %s", e.Count, edge.Age(e.At.Sub(e.Since)))
			}
			out = append(out, webui.EdgeEvent{Age: edge.Age(now.Sub(e.At)) + " ago", Node: e.Node,
				What: e.What, Detail: detail, Alarm: e.Kind == edge.EventAlert})
		}
		return out
	}
}

// edgeIntentHook hands the console what the owner asked of this Edge and what they left standing, from
// the same reads `roger edge expect`, `roger edge drift` and `roger edge schedule` print - so the
// browser and the terminal cannot tell the owner different things.
func edgeIntentHook() func(time.Time) webui.EdgeFleetIntent {
	return func(now time.Time) webui.EdgeFleetIntent {
		var out webui.EdgeFleetIntent
		set, drift, _, err := edgeDriftRead(now)
		if err == nil {
			differ := map[string]int{}
			for _, d := range drift {
				if d.State == edge.DriftOut {
					differ[string(d.What)+"|"+d.Expected]++
				}
			}
			names := edgeNodeNames()
			for _, e := range set {
				out.Expect = append(out.Expect, webui.EdgeExpectLine{Expected: e.Words(),
					Of: edgeExpectScopeWords(e.Scope, names), Differ: differ[string(e.What)+"|"+e.Words()]})
			}
			for _, d := range drift {
				out.Drift = append(out.Drift, webui.EdgeDriftLine{Node: d.Node, Expected: d.Expected,
					Found: d.Found, Unknown: d.State == edge.DriftUnknown, Dark: d.Dark})
			}
		}
		if jobs, err := edgeAlertLog().Jobs(); err == nil {
			names := edgeNodeNames()
			for _, j := range jobs {
				line := webui.EdgeScheduleLine{ID: j.ID, What: j.Words(),
					Of: edgeExpectScopeWords(j.Scope, names), Cadence: j.Cadence(), Last: "never run", Next: "-"}
				if !j.LastRun.IsZero() {
					line.Last = edge.Age(now.Sub(j.LastRun)) + " ago"
				}
				if at, ok := j.NextRun(now); ok {
					line.Next = edgeWhenWords(at, now)
				}
				for _, r := range lastRunsOf(j, 3) {
					line.Runs = append(line.Runs, r.Node+": "+r.Outcome)
				}
				out.Schedule = append(out.Schedule, line)
			}
		}
		return out
	}
}

func edgeFloorHook(h *tui.Hooks) func(time.Time) webui.EdgeFloor {
	if h.EdgeFloor == nil {
		return nil
	}
	return func(now time.Time) webui.EdgeFloor {
		samples, calls, rate, so := h.EdgeFloor(now)
		f := webui.EdgeFloor{Samples: samples, Calls: calls, Rate: rate, SignOff: so}
		if h.EdgeReads != nil {
			f.Reads = h.EdgeReads()
		}
		return f
	}
}
