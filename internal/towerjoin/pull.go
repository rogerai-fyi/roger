package towerjoin

// pull.go: the Tower side of pull dispatch (features/tower/tower_pull_dispatch.feature). The
// Tower asks Core for work over its own signed requests, the same authentication every other
// link call uses, and posts the hub's answer back the same way.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// pullClient outlasts Core's 25 s hold, so an empty pull ends with Core's 204 rather than our
// own timeout.
var pullClient = &http.Client{Timeout: 45 * time.Second, CheckRedirect: protocol.NoDowngradeRedirect}

// ErrSessionGone is Core saying it no longer holds the link session a pull or an answer named:
// the link is re-opening, and the call is worth repeating on the new session.
var ErrSessionGone = errors.New("Roger Core no longer holds this link session")

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
	if status, err := towerPostLimit(ctx, st, "/tower/work/pull", body, &out, pullClient, maxPullBody); err != nil {
		return nil, sessionGone(status, err)
	}
	return out.Work, nil
}

// AnswerWork posts the hub's answer to one piece of pulled work; it stops with ctx.
func AnswerWork(ctx context.Context, st *tower.State, sessionID string, a towerhub.PullAnswer) error {
	towerID, err := CoreTowerID(st)
	if err != nil {
		return err
	}
	a.TowerID, a.SessionID = towerID, sessionID
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	// The Tower and the work are named in the URL and the signature covers the body's digest, so
	// Core can check who is answering what before it buffers an answer of tens of megabytes.
	path := "/tower/work/answer?tower_id=" + url.QueryEscape(towerID) + "&work=" + url.QueryEscape(a.ID)
	status, err := towerPostAs(ctx, st, path, body, true, nil, pullClient, 1<<20)
	if err != nil && status >= 500 {
		// Core's own trouble, not a refusal of this answer: worth another try, like a lost post.
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return sessionGone(status, err)
}

// sessionGone marks Core's 409 on the pull routes, its answer to a session it does not hold.
func sessionGone(status int, err error) error {
	if err != nil && status == http.StatusConflict {
		return fmt.Errorf("%w: %v", ErrSessionGone, err)
	}
	return err
}
