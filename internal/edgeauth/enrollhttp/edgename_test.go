package enrollhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth/enrollhttp"
)

// features/join/surfaces.feature, "joined the Edge home as <name>": a joiner learns the Edge's name
// from the answer, while the answer's body keeps exactly the keys an old joiner knows
// (features/edge/enrollment_root_trust.feature, "an old joiner can still enroll against a new
// authority"). The name rides beside the body, never in it.
func TestTheEdgeNameCrossesTheWireBesideTheAnswer(t *testing.T) {
	_, url, user := rig(t)

	resp, err := enrollhttp.Enroll(context.Background(), url, ask(t, user))
	require.NoError(t, err)
	require.Equal(t, "shed", resp.Edge)

	body, err := json.Marshal(ask(t, user))
	require.NoError(t, err)
	r, err := http.Post(url+enrollhttp.Path, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	old := map[string]bool{"node_id": true, "account": true, "cert": true, "root": true, "root_proof": true, "root_proof_status": true}
	for k := range m {
		require.True(t, old[k], "the answer has a key %q an old joiner does not know", k)
	}
}
