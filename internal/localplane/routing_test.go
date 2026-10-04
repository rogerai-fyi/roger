package localplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLocalRoutingRefusals(t *testing.T) {
	cases := []struct {
		name, body, code, msg string
	}{
		{"not json", `nope`, "", "a model is required"},
		{"model not a string", `{"model":7}`, "", "a model is required"},
		{"no model at all", `{"messages":[]}`, "", "a model is required"},
		{"models not a list", `{"model":"a","models":"b"}`, "", "models must be a list of model ids"},
		{"models blank id", `{"models":[" "]}`, "", "models must be a list of model ids"},
		{"models non-string", `{"models":[1]}`, "", "models must be a list of model ids"},
		{"too many", `{"models":["a","b","c","d","e","f"]}`, "", "too many models (max 5)"},
		{"provider not object", `{"model":"a","provider":[]}`, "", "provider must be an object"},
		{"fallbacks not bool", `{"model":"a","provider":{"allow_fallbacks":"no"}}`, "", "provider.allow_fallbacks must be a boolean"},
		{"only empty", `{"model":"a","provider":{"only":[]}}`, "", "provider.only must be a non-empty list of local station ids"},
		{"order not list", `{"model":"a","provider":{"order":"s1"}}`, "", "provider.order must be a non-empty list of local station ids"},
		{"ignore blank id", `{"model":"a","provider":{"ignore":[""]}}`, "", "provider.ignore must be a non-empty list of local station ids"},
		{"unknown provider key", `{"model":"a","provider":{"zap":1}}`, "", "unknown routing key provider.zap"},
		{"roger not object", `{"model":"a","roger":1}`, "", "roger must be an object"},
		{"confidential not bool", `{"model":"a","roger":{"confidential":"y"}}`, "", "roger.confidential must be a boolean"},
		{"trust_min not string", `{"model":"a","roger":{"trust_min":1}}`, "", "roger.trust_min must be any, verified or confidential"},
		{"trust_min unknown", `{"model":"a","roger":{"trust_min":"gold"}}`, "", "roger.trust_min must be any, verified or confidential"},
		{"unknown roger key", `{"model":"a","roger":{"zap":1}}`, "", "unknown routing key roger.zap"},
		{"profile model", `{"model":"@profile/x"}`, "unknown_profile", "profiles resolve on the client; send the model"},
		{"roger.profile", `{"model":"a","roger":{"profile":"x"}}`, "unknown_profile", "profiles resolve on the client; send the model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, e := parseLocalRouting([]byte(c.body), false)
			require.NotNil(t, e)
			require.Equal(t, 400, e.status)
			require.Equal(t, c.code, e.code)
			require.Equal(t, c.msg, e.msg)
		})
	}
}

func TestParseLocalRoutingHonoredAndIgnored(t *testing.T) {
	lr, e := parseLocalRouting([]byte(`{"model":"a:free:nitro","models":["a","b:floor",null],
		"provider":{"only":["s1"],"ignore":["s2"],"order":["s1"],"allow_fallbacks":false,"sort":"price",
		"max_price":{"prompt":1,"completion":2},"only":null},
		"roger":{"trust_min":"verified","min_tps":5},"messages":[]}`), false)
	require.NotNil(t, e) // a null element in models[] is not a model id
	lr, e = parseLocalRouting([]byte(`{"model":"a:free:nitro","models":["a","b:floor"],
		"provider":{"only":["s1"],"ignore":["s2"],"order":["s1"],"allow_fallbacks":false,"sort":"price",
		"max_price":{"prompt":1,"completion":2},"quantizations":["q8"],"order":null},
		"roger":{"trust_min":"verified","min_tps":5,"confidential":false},"messages":[]}`), false)
	require.Nil(t, e)
	require.Equal(t, []string{"a", "b"}, lr.models)
	require.True(t, lr.noFallbacks && lr.needVerify && !lr.needAttest)
	require.Equal(t, []string{"model:floor", "model:free", "model:nitro", "provider.max_price.completion",
		"provider.max_price.prompt", "provider.quantizations", "provider.sort", "roger.min_tps"}, lr.ignored)
	require.True(t, lr.admits("s1"))
	require.False(t, lr.admits("s2"))
	require.False(t, lr.admits("s3"))

	lr, e = parseLocalRouting([]byte(`{"model":"a","provider":{"max_price":5},"roger":{"trust_min":"confidential"}}`), false)
	require.Nil(t, e)
	require.Equal(t, []string{"provider.max_price"}, lr.ignored)
	require.True(t, lr.needAttest)

	lr, e = parseLocalRouting([]byte(`{"model":"a","roger":{"trust_min":"any"}}`), true)
	require.Nil(t, e)
	require.True(t, lr.needAttest, "the X-Roger-Confidential header alone requires attestation")
}

func TestJobBodyKeepsOrderAndSetsModel(t *testing.T) {
	lr, e := parseLocalRouting([]byte(`{"messages":[1],"model":"a:free","provider":{"only":["s"]},"temperature":0.2}`), false)
	require.Nil(t, e)
	require.Equal(t, `{"messages":[1],"model":"b","temperature":0.2}`, string(lr.jobBody("b")))

	lr, e = parseLocalRouting([]byte(`{"models":["a"],"messages":[]}`), false)
	require.Nil(t, e)
	require.Equal(t, `{"messages":[],"model":"a"}`, string(lr.jobBody("a")))

	lr, e = parseLocalRouting([]byte(`{"models":["a"]}`), false)
	require.Nil(t, e)
	require.Equal(t, `{"model":"a"}`, string(lr.jobBody("a")))
}

func TestObjectKeysEdges(t *testing.T) {
	require.Nil(t, objectKeys([]byte(`[1]`)))
	require.Nil(t, objectKeys([]byte(``)))
	require.Equal(t, []string{"a"}, objectKeys([]byte(`{"a":1,`)))
	require.Equal(t, []string{"a", "b"}, objectKeys([]byte(`{"a":1,"b":}`)))
}

func TestConstraintMissMessages(t *testing.T) {
	st := []stationView{{id: "s1", models: []string{"m"}}, {id: "s2", models: []string{"m"}}}
	lr := localRouting{}
	require.Equal(t, "", lr.constraintMiss(st, "m"))
	require.Equal(t, "no local station matches for x", lr.constraintMiss(st, "x"))
	lr = localRouting{only: setOf([]string{"s9", "s8"})}
	require.Equal(t, "no local station matches: only for m", lr.constraintMiss(st, "m")) // never echoes ids (founder ruling 2026-10-04)
	lr = localRouting{ignore: setOf([]string{"s1", "s2"})}
	require.Equal(t, "no local station matches: ignore removed every station for m", lr.constraintMiss(st, "m"))
	lr = localRouting{order: []string{"s7"}, noFallbacks: true}
	require.Equal(t, "no local station matches: order for m", lr.constraintMiss(st, "m"))
}

func TestModelsEndpoint(t *testing.T) {
	st := standaloneState(t)
	priv := admitClient(t, st)
	attachKeyedStation(t, st, "s1", []string{"zeta", "alpha"})
	attachKeyedStation(t, st, "s2", []string{"alpha"})
	srv := New(st)

	// unauthenticated: the uniform 401, never a 404 (an old broker's answer)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedBody(t, priv, http.MethodPost, "/v1/models", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedBody(t, priv, http.MethodGet, "/v1/models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "list", got.Object)
	require.Len(t, got.Data, 2)
	require.Equal(t, "alpha", got.Data[0].ID)
	require.Equal(t, "zeta", got.Data[1].ID)
	require.Equal(t, "local", got.Data[0].OwnedBy)
}
