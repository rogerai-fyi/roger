package towerjoin

// dispatch.go is the Tower's relay leg: collect work from Roger Core, hand it to the Station
// it names, and return the answer.
//
// THE TOWER IS A COURIER AND NOTHING ELSE. It cannot read the grant into anything it is
// allowed to act on, it cannot alter the request (the grant commits to a digest of it, and
// the Station checks), and it cannot alter the answer (the Station's receipt commits to a
// digest of that, and Core checks). Everything here is deliberately shaped so that the
// bytes it carries are the bytes it received - it never decodes and re-encodes either one.
//
// It relays a FAILURE just as faithfully as a result. A Station that refuses, or that cannot
// be reached, is reported to Core as that attempt failing; the alternative is an attempt
// left hanging until its deadline while a caller waits, which is a worse answer to the same
// question.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/tower"
)

// Work is one attempt Core handed this Tower.
type Work struct {
	AttemptID string          `json:"attempt_id"`
	Grant     json.RawMessage `json:"grant"`
	Request   json.RawMessage `json:"request"`
}

// ErrNoWork is Core saying there is nothing to do right now. It is not a failure: an idle
// fleet spends most of its time here.
var ErrNoWork = errors.New("no work")

// PollDispatch waits for Core to hand this Tower an attempt.
func PollDispatch(st *tower.State, client *http.Client) (Work, error) {
	adm, ok := LoadAdmission(st.Dir())
	if !ok || adm.TowerID == "" {
		return Work{}, errors.New("this Tower is not registered yet - run `roger-tower register` first")
	}
	body, err := json.Marshal(map[string]string{"tower_id": adm.TowerID})
	if err != nil {
		return Work{}, err
	}
	var work Work
	status, err := towerPostStatus(st, "/tower/dispatch", body, &work, client)
	if err != nil {
		return Work{}, err
	}
	if status == http.StatusNoContent {
		return Work{}, ErrNoWork
	}
	if work.AttemptID == "" {
		// A 200 with nothing in it is not work. Treating it as such would spin the loop.
		return Work{}, ErrNoWork
	}
	return work, nil
}

// ReturnResult hands Core what the Station produced - or reports that it produced nothing.
func ReturnResult(st *tower.State, attemptID string, resp station.ExecuteResponse) error {
	adm, ok := LoadAdmission(st.Dir())
	if !ok || adm.TowerID == "" {
		return errors.New("this Tower is not registered yet")
	}
	out := map[string]any{"tower_id": adm.TowerID, "attempt_id": attemptID}
	if resp.Failure != "" || resp.Receipt == nil {
		failure := resp.Failure
		if failure == "" {
			failure = "the Station returned no receipt"
		}
		out["failure"] = failure
	} else {
		out["receipt"] = resp.Receipt
		// RELAYED VERBATIM. json.RawMessage all the way through: the digest the Station signed
		// is over these exact bytes, so any re-encoding here - a map round trip, a re-indent -
		// would invalidate a perfectly good result and look like tampering, which is precisely
		// what it would be.
		out["body"] = resp.Body
	}
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return towerPost(st, "/tower/dispatch/result", body, nil)
}

// RelayToStation hands the work to the Station and returns whatever it says.
func RelayToStation(endpoint string, w Work, client *http.Client) station.ExecuteResponse {
	payload, err := json.Marshal(station.ExecuteRequest{Grant: w.Grant, Request: w.Request})
	if err != nil {
		return station.ExecuteResponse{Failure: "could not frame the request for the Station"}
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		// The Station being unreachable is a FAILURE OF THIS ATTEMPT, reported as such rather
		// than swallowed. Core is waiting, and telling it now is the difference between a
		// caller getting an answer and a caller getting a timeout.
		return station.ExecuteResponse{Failure: "the Station could not be reached: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return station.ExecuteResponse{Failure: "the Station's answer could not be read"}
	}
	if resp.StatusCode != http.StatusOK {
		return station.ExecuteResponse{
			Failure: fmt.Sprintf("the Station replied %d", resp.StatusCode)}
	}
	var out station.ExecuteResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return station.ExecuteResponse{Failure: "the Station's answer was not readable"}
	}
	return out
}

// dispatchClient is the long-poll client. Its timeout must exceed Core's poll wait, or every
// poll would look like a transport failure to the Tower and like a disconnect to Core.
var dispatchClient = &http.Client{Timeout: 60 * time.Second}

// DispatchClient is the client the poll loop should use.
func DispatchClient() *http.Client { return dispatchClient }
