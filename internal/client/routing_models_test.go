package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGuestModelsAreFilteredToTheOwners: with an owner --models list, a guest models[] keeps
// only the entries inside it; one with NO overlap is refused locally (an OpenAI-shaped 400
// routing_outside_session), never forwarded as an empty list the broker would read as absent.
func TestGuestModelsAreFilteredToTheOwners(t *testing.T) {
	owner := Routing{Models: []string{"a", "b"}}
	for name, c := range map[string]struct{ guest, want, refusal string }{
		"no guest list takes the owner's":    {`{"model":"a"}`, `{"model":"a","models":["a","b"]}`, ""},
		"a null list takes the owner's":      {`{"model":"a","models":null}`, `{"model":"a","models":["a","b"]}`, ""},
		"overlap keeps only the inside":      {`{"model":"a","models":["b","c"]}`, `{"model":"a","models":["b"]}`, ""},
		"ids compare exactly, as the broker": {`{"model":"a","models":["B"]}`, "", "models names no model inside this session's allowed models"},
		"a sugared entry is matched bare":    {`{"model":"a","models":["b:nitro"]}`, `{"model":"a","models":["b:nitro"]}`, ""},
		"no overlap is refused":              {`{"model":"a","models":["c","d"]}`, "", "models names no model inside this session's allowed models"},
		"an empty list takes the owner's":    {`{"model":"a","models":[]}`, `{"model":"a","models":["a","b"]}`, ""},
		"a list that is not ids is refused":  {`{"model":"a","models":"c"}`, "", "models must be a list of model ids"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := owner.Apply([]byte(c.guest))
			if c.refusal != "" {
				var rr *RoutingRefusal
				require.ErrorAs(t, err, &rr)
				require.Equal(t, c.refusal, rr.Msg)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, c.want, string(out))
		})
	}

	// through the live proxy: refused locally, the broker never sees the request
	hit := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	t.Cleanup(broker.Close)
	h := ProxyHandler(ProxyOptions{Broker: broker.URL, User: "u", Model: "a", Models: []string{"a", "b"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"a","models":["c"],"messages":[{"role":"user","content":"hi"}]}`)))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var e struct {
		Error struct{ Type, Code, Message string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	require.Equal(t, "invalid_request_error", e.Error.Type)
	require.Equal(t, "routing_outside_session", e.Error.Code)
	require.Contains(t, e.Error.Message, "allowed models")
	require.False(t, hit, "a refused body never reaches the broker")
}
