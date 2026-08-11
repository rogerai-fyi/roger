package main

// dispatchloop.go is the Tower collecting work for its Stations while it holds the link.
//
// It runs ALONGSIDE the heartbeat rather than inside it, in its own goroutine, because the
// two have opposite shapes: a heartbeat is a short call on a timer, and a dispatch poll is a
// long wait that ends the moment there is something to do. Folding one into the other would
// mean either a heartbeat delayed by up to a poll, or a poll cut short by every heartbeat.
//
// THE TOWER IS A COURIER. It carries the grant and the request to the Station unchanged, and
// carries the Station's receipt and body back unchanged. It cannot usefully alter either -
// the grant commits to a digest of the request and the receipt to a digest of the answer -
// and this file is written so that it never even re-encodes them.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

// dispatchBackoff is how long to wait after a poll that failed for a reason other than "no
// work". Long enough not to hammer a Core that is having a bad time, short enough that a
// Tower is back in service quickly once it recovers.
const dispatchBackoff = 5 * time.Second

// stationEndpoints maps a Station ID to where this Tower reaches it.
type stationEndpoints map[string]string

// parseStationEndpoints reads the repeatable --station id=URL flag.
func parseStationEndpoints(vals []string) (stationEndpoints, error) {
	out := stationEndpoints{}
	for _, v := range vals {
		id, url, ok := cutOne(v, "=")
		if !ok || id == "" || url == "" {
			return nil, fmt.Errorf("--station wants ID=URL, got %q", v)
		}
		out[id] = url
	}
	return out, nil
}

func cutOne(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// runDispatch polls for work until stopped.
//
// Every exit from a single attempt reports SOMETHING to Core - a result or a failure -
// because Core has a caller waiting on the other side. The one case that reports nothing is
// a Tower that cannot reach Core at all, where there is nowhere to report to and the
// attempt's deadline is what ends it.
func runDispatch(st *tower.State, out io.Writer, stations stationEndpoints, stop <-chan struct{}) {
	if len(stations) == 0 {
		// Nothing to dispatch TO. Said once, rather than polling forever for work that could
		// only be refused: a Tower with attached Stations it cannot reach is a
		// misconfiguration worth naming at startup.
		fmt.Fprint(out, "no --station endpoints given, so this Tower will not collect work.\n"+
			"Its Stations' offers are still relayed; add --station ID=URL to serve them.\n")
		return
	}
	fmt.Fprintf(out, "collecting work for %d Station(s)\n", len(stations))

	for {
		select {
		case <-stop:
			return
		default:
		}

		work, err := towerjoin.PollDispatch(st, towerjoin.DispatchClient())
		switch {
		case errors.Is(err, towerjoin.ErrNoWork):
			continue // the ordinary case for an idle fleet
		case err != nil:
			fmt.Fprintf(out, "could not collect work (%v) - retrying\n", err)
			if !sleepOrStop(dispatchBackoff, stop) {
				return
			}
			continue
		}
		handleWork(st, out, stations, work)
	}
}

// handleWork relays one attempt and returns whatever came of it.
func handleWork(st *tower.State, out io.Writer, stations stationEndpoints, work towerjoin.Work) {
	stationID, endpoint := routeFor(stations, work)
	if endpoint == "" {
		// A grant for a Station this Tower cannot reach. Reported rather than dropped: Core
		// is waiting, and a fast honest failure beats a deadline.
		fmt.Fprintf(out, "attempt %s names a Station this Tower has no endpoint for\n", work.AttemptID)
		report(st, out, work.AttemptID, station.ExecuteResponse{
			Failure: "this Tower has no endpoint for the Station in this grant"})
		return
	}
	resp := towerjoin.RelayToStation(endpoint, work, towerjoin.DispatchClient())
	if resp.Failure != "" {
		fmt.Fprintf(out, "attempt %s: station %s could not serve it: %s\n",
			work.AttemptID, stationID, resp.Failure)
	}
	report(st, out, work.AttemptID, resp)
}

// routeFor picks the endpoint for the Station this grant names.
//
// With exactly one Station configured the answer is that one - the common shape, and it
// saves an operator from having to keep the ID in the flag in step with the Station's own.
// With several, the grant must name one this Tower knows, and the Tower reads the Station ID
// from the grant it is relaying rather than choosing for itself.
func routeFor(stations stationEndpoints, work towerjoin.Work) (string, string) {
	id := stationIDOf(work.Grant)
	if url, ok := stations[id]; ok {
		return id, url
	}
	if len(stations) == 1 && id == "" {
		for k, v := range stations {
			return k, v
		}
	}
	return id, ""
}

// runDispatchInBackground starts the loop and returns a function that waits for it to end.
func runDispatchInBackground(st *tower.State, out io.Writer, stations stationEndpoints, stop <-chan struct{}) func() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runDispatch(st, out, stations, stop)
	}()
	return wg.Wait
}

func report(st *tower.State, out io.Writer, attemptID string, resp station.ExecuteResponse) {
	if err := towerjoin.ReturnResult(st, attemptID, resp); err != nil {
		// Nothing more to do: the attempt will end on its deadline, and Core's caller gets a
		// timeout. Worth printing, because a Tower that cannot return results is producing
		// nothing while looking busy.
		fmt.Fprintf(out, "could not return the result for %s: %v\n", attemptID, err)
	}
}

func sleepOrStop(d time.Duration, stop <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-stop:
		return false
	}
}

// stationIDOf reads the Station a grant names.
//
// Read-only, and from the SIGNED object: the Tower is routing by what Core authorized rather
// than by anything it decided for itself. It does not verify the signature - it cannot, and
// does not need to. Getting this wrong sends the work to a Station that will refuse it,
// which is a failed attempt rather than a security problem, because the Station checks the
// grant names IT before doing anything.
func stationIDOf(grant []byte) string {
	var obj struct {
		StationID string `json:"station_id"`
	}
	if err := json.Unmarshal(grant, &obj); err != nil {
		return ""
	}
	return obj.StationID
}
