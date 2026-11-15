package towerjoin

// pull.go: the Tower side of pull dispatch (features/tower/tower_pull_dispatch.feature). The
// Tower asks Core for work over its own signed requests, the same authentication every other
// link call uses, and posts the hub's answer back the same way.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// pullClient outlasts Core's 25 s hold, so an empty pull ends with Core's 204 rather than our
// own timeout.
var pullClient = &http.Client{Timeout: 45 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}

// maxPullBody bounds one pull's answer: Core caps a batch at 24 MiB of sealed requests, which is
// about 32 MiB as base64 inside JSON.
const maxPullBody = 40 << 20

// PullWork holds a pull open on the current session and returns the work Core hands over, or
// none when the hold ends empty.
func PullWork(ctx context.Context, st *tower.State, sessionID string) ([]towerhub.PullWork, error) {
	towerID, err := CoreTowerID(st)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{"tower_id": towerID, "session_id": sessionID})
	if err != nil {
		return nil, err
	}
	var out struct {
		Work []towerhub.PullWork `json:"work"`
	}
	if _, err := towerPostLimit(ctx, st, "/tower/work/pull", body, &out, pullClient, maxPullBody); err != nil {
		return nil, err
	}
	return out.Work, nil
}

// AnswerWork posts the hub's answer to one piece of pulled work.
func AnswerWork(st *tower.State, sessionID string, a towerhub.PullAnswer) error {
	towerID, err := CoreTowerID(st)
	if err != nil {
		return err
	}
	a.TowerID, a.SessionID = towerID, sessionID
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = towerPostLimit(context.Background(), st, "/tower/work/answer", body, nil, pullClient, 1<<20)
	return err
}
