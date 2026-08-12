package towerjoin

// collect.go is the receipt courier: the Tower gathering its Stations' evidence and carrying
// it to Roger Core.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY THE TOWER DOES THIS
//
// On the edge path a receipt goes to the CONSUMER, inside TLS the Tower cannot read - so the
// Tower never sees that copy, and a Station cannot reach Core to deliver another. The Tower
// is the one party with a foot in both rooms: it can reach the Station on their private
// network and it holds a signed channel to Core. So it couriers.
//
// # WHAT A DISHONEST COURIER CAN AND CANNOT DO
//
// It cannot forge or alter a receipt - Station-signed, key never held here. It can WITHHOLD,
// and a withheld receipt is an attempt that never settles, which unpaid exactly one party:
// this Tower's own operator. No enforcement mechanism required; the incentive is the
// mechanism.
//
// # AT-LEAST-ONCE, CONFIRMED AFTER CORE ANSWERS
//
// Collection copies; only confirmation removes; Core's settlement is one-use. So a crash
// between any two steps re-runs safely: a receipt forwarded twice loses the swap at Core
// and is confirmed off the outbox on the "already settled" answer. TERMINAL refusals confirm
// too - a receipt Core will never accept would otherwise wedge the queue behind it forever -
// but an unreachable Core or Station leaves everything in place for the next pass.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"rogerai.fm/roger/v5/internal/tower"
)

// collectEvery is how often the courier does a round. Frequent enough that settlement lands
// well inside the grace window; cheap when there is nothing to carry.
const collectEvery = 30 * time.Second

// collected is what a Station hands over.
type collected struct {
	Receipts []struct {
		AttemptID string `json:"attempt_id"`
		StationID string `json:"station_id"`
		Receipt   []byte `json:"receipt"`
	} `json:"receipts"`
}

// CollectReceipts does one round: every Station, every pending receipt, forward, confirm.
//
// It reports what it moved so the loop can log usefully, and returns an error only for the
// kind of failure the NEXT round cannot fix alone - an unreadable identity, not an
// unreachable Station.
func CollectReceipts(st *tower.State, stations map[string]string, out io.Writer) (int, error) {
	adm, ok := LoadAdmission(st.Dir())
	if !ok || adm.TowerID == "" {
		return 0, errors.New("this Tower is not registered, so it has nowhere to deliver receipts")
	}
	moved := 0
	for stationID, base := range stations {
		got, err := fetchReceipts(base)
		if err != nil {
			// The Station being down is its operator's ordinary morning, not this loop's
			// failure. Say so and move to the next; the outbox holds.
			fmt.Fprintf(out, "could not collect receipts from %s: %v\n", stationID, err)
			continue
		}
		var settled []string
		for _, r := range got.Receipts {
			// The VERDICT and the ERROR are separate answers: a terminal refusal is an error
			// worth reporting AND a verdict worth confirming - holding a receipt Core will
			// never accept would wedge the queue behind it. The first version of this loop
			// checked the error first and dropped the verdict, so refused receipts were
			// re-presented forever.
			done, ferr := forwardReceipt(st, adm.TowerID, r.StationID, r.AttemptID, r.Receipt)
			if ferr != nil {
				fmt.Fprintf(out, "could not settle attempt %s: %v\n", r.AttemptID, ferr)
			}
			if done {
				settled = append(settled, r.AttemptID)
				if ferr == nil {
					moved++
				}
			}
		}
		if len(settled) > 0 {
			if err := confirmSettled(base, settled); err != nil {
				// Core has the evidence, so nothing is lost - the Station re-offers these and
				// the replay loses Core's one-use swap next round. Noisy is better than stuck.
				fmt.Fprintf(out, "could not confirm %d receipt(s) to %s: %v\n",
					len(settled), stationID, err)
			}
		}
	}
	return moved, nil
}

func fetchReceipts(base string) (collected, error) {
	resp, err := httpClient.Post(strings.TrimRight(base, "/")+"/receipts/collect",
		"application/json", strings.NewReader("{}"))
	if err != nil {
		return collected{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return collected{}, fmt.Errorf("the Station answered %d", resp.StatusCode)
	}
	var out collected
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return collected{}, err
	}
	return out, nil
}

// forwardReceipt hands one receipt to Core. done=true means the outbox may forget it: Core
// settled it, had already settled it, or refused it terminally.
func forwardReceipt(st *tower.State, towerID, stationID, attemptID string, receipt []byte) (bool, error) {
	body, err := json.Marshal(map[string]any{
		"tower_id":   towerID,
		"station_id": stationID,
		"attempt_id": attemptID,
		"receipt":    base64.StdEncoding.EncodeToString(receipt),
	})
	if err != nil {
		return false, err
	}
	status, err := towerPostStatus(st, "/tower/edge/settle", body, nil, nil)
	switch {
	case err == nil && status == http.StatusOK:
		return true, nil
	case status == http.StatusConflict:
		// Already settled - a replay of our own earlier success, or a digest disagreement
		// Core has recorded against the relay. Either way this receipt's story is over.
		return true, nil
	case status >= 400 && status < 500 && status != 0:
		// Terminal: Core read it and said no. Holding it would wedge the queue behind it.
		return true, fmt.Errorf("refused terminally (%d): %w", status, err)
	default:
		// Unreachable, or a 5xx: the next round retries.
		if err == nil {
			err = fmt.Errorf("core answered %d", status)
		}
		return false, err
	}
}

func confirmSettled(base string, attemptIDs []string) error {
	body, err := json.Marshal(map[string]any{"attempt_ids": attemptIDs})
	if err != nil {
		return err
	}
	resp, err := httpClient.Post(strings.TrimRight(base, "/")+"/receipts/settled",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the Station answered %d", resp.StatusCode)
	}
	return nil
}

// KeepCollecting runs the courier until stopped.
func KeepCollecting(st *tower.State, stations map[string]string, out io.Writer,
	stop <-chan struct{}, ticker func(time.Duration) (<-chan time.Time, func())) {

	if len(stations) == 0 {
		return
	}
	tick, cancel := ticker(collectEvery)
	defer cancel()
	for {
		select {
		case <-stop:
			return
		case <-tick:
			moved, err := CollectReceipts(st, stations, out)
			if err != nil {
				fmt.Fprintf(out, "receipt collection failed: %v\n", err)
				continue
			}
			if moved > 0 {
				fmt.Fprintf(out, "settled %d receipt(s) with Roger Core\n", moved)
			}
		}
	}
}
