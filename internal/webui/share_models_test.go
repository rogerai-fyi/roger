package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func consoleHTML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("assets/console.html")
	require.NoError(t, err)
	return string(b)
}

// panelOf returns a panel's markup, from its id to the next section.
func panelOf(t *testing.T, html, id string) string {
	t.Helper()
	a := strings.Index(html, `id="`+id+`"`)
	require.True(t, a > 0, id)
	b := strings.Index(html[a:], `<section`)
	if b < 0 {
		return html[a:]
	}
	return html[a : a+b]
}

// features/serving/local_models.feature §9 (@web): the console has no models tab; everything
// §8 describes is reached from SHARE.
func TestTheConsoleHasNoSeparateModelsTab(t *testing.T) {
	html, js := consoleHTML(t), consoleJS(t)
	require.NotContains(t, html, `data-tab="models"`)
	require.NotContains(t, html, `id="panel-models"`)
	require.Contains(t, js, `var TABS = ["chat", "share", "account", "browse", "edge", "settings"];`)
	share := panelOf(t, html, "panel-share")
	for _, id := range []string{"share-roger-rows", "share-getmodel", "models-search", "models-q", "models-machine", "models-servelog"} {
		require.Contains(t, share, `id="`+id+`"`, "%s lives in the SHARE panel", id)
	}
	require.Contains(t, share, "SHARE › GET A MODEL", "the search says where it is: inside SHARE")
	require.Contains(t, js, `if (name === "share") modelsStart(); else modelsStop();`, "the rows keep time while SHARE is shown")
}

// §9 (@web): roger's own rows are rows of the SHARE table - served, on disk, a fetch with its bar -
// and the list always ends with the visible way to get a model.
func TestRogersOwnModelsAreRowsOfTheShareTable(t *testing.T) {
	js := consoleJS(t)
	a, b := strings.Index(js, "  function renderModels(v) {"), strings.Index(js, "  function modelsServe(m, current, add) {")
	require.True(t, a > 0 && b > a)
	r := js[a:b]
	require.Contains(t, r, `$("share-roger-rows")`)
	require.Contains(t, r, `"served by roger itself`)
	require.Contains(t, r, `"ROGER"`)
	require.Contains(t, r, `"stop serving"`)
	require.Contains(t, r, `"ON DISK"`)
	require.Contains(t, r, `"serve here"`)
	require.Contains(t, r, "modelsServe(m, cur, false)", "serving an on-disk row goes through the asked-first path")
	require.Contains(t, r, `"meter-fill"`)
	require.Contains(t, r, `" left"`, "a fetch states its time left")
	require.Contains(t, r, `"/s"`, "and its rate")
	get := strings.Index(r, `"+ Get a model that fits this machine"`)
	require.True(t, get > 0, "the get row exists")
	for _, earlier := range []string{`"ON DISK"`, `"meter-fill"`, `"stop serving"`} {
		require.Less(t, strings.Index(r, earlier), get, "the get row is appended after %s: always last", earlier)
	}
	require.NotContains(t, r[get:], "return", "nothing returns before the get row is drawn")
	require.Contains(t, r, `v.budget > 0 ? fmtBytes(v.budget) + " " + v.pool`, "it states the machine's memory")
	require.Contains(t, r, `show($("share-getmodel"), true)`, "choosing it opens the search inside SHARE")
	require.NotContains(t, r, "innerHTML")
}

// local_models.feature §4: the console says how fast a file would answer apart from whether it fits,
// in the row and in the question before a fetch.
func TestTheConsoleSaysSpeedApartFromFit(t *testing.T) {
	js := consoleJS(t)
	require.Contains(t, js, `if (c.pace_line) verdict.appendChild(el("div", "mono", c.pace_line));`)
	require.Contains(t, js, `(c.pace_line ? "\n" + c.pace_line : "")`)
	require.Contains(t, js, `(c.room ? "\n\n" + c.room : "")`, "and what the owner's disk budget removes, before it is asked")
}

// §5 in the console: every model roger serves is a row with its own stop, and a model on disk can be
// added beside what is on the air, as well as switched to.
func TestTheConsoleServesASecondBesideTheFirst(t *testing.T) {
	js := consoleJS(t)
	require.Contains(t, js, `[v.serving].concat(v.also || [])`, "each serve is a row")
	require.Contains(t, js, `modelsStopServing(one)`, "each with its own stop")
	require.Contains(t, js, `{ confirm: true, id: sv.id }`, "stopping one names it")
	require.Contains(t, js, `"add beside"`)
	require.Contains(t, js, `modelsServe(m, cur, true)`)
	require.Contains(t, js, `add: !!add`, "the request says it is an add")
}

// local_models.feature §11: a serving setting is changed from the console with a POST that carries the
// owner's yes, and the answer is the CLI's own sentence.
func TestASettingIsChangedFromTheConsoleWithTheOwnersYes(t *testing.T) {
	var got []string
	s := New(testCtrl(), Options{Models: ModelHooks{
		View: func() (ModelsView, error) { return ModelsView{}, nil },
		Set: func(kind, value string) (string, error) {
			got = append(got, kind+"="+value)
			if kind == "speed" {
				return "", errors.New(`"speed" is not a serving setting: runtime, idle or budget`)
			}
			return "idle timeout: 2h0m0s", nil
		},
	}})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	code, _ := call(t, srv, s.Token(), "GET", "/api/models/set", "")
	require.Equal(t, http.StatusMethodNotAllowed, code)
	code, _ = call(t, srv, s.Token(), "POST", "/api/models/set", `{"kind":"idle"}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = call(t, srv, s.Token(), "POST", "/api/models/set", `{"kind":"idle","value":"2h"}`)
	require.Equal(t, http.StatusPreconditionRequired, code, "no yes, no change")
	require.Empty(t, got)
	code, body := call(t, srv, s.Token(), "POST", "/api/models/set", `{"confirm":true,"kind":"idle","value":"2h"}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, "idle timeout: 2h0m0s")
	code, body = call(t, srv, s.Token(), "POST", "/api/models/set", `{"confirm":true,"kind":"speed","value":"x"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, body, "not a serving setting")
	code, _ = call(t, srv, "", "POST", "/api/models/set", `{"confirm":true,"kind":"idle","value":"2h"}`)
	require.NotEqual(t, http.StatusOK, code, "without the console's token, nothing")
	require.Equal(t, []string{"idle=2h", "speed=x"}, got)

	off := New(testCtrl(), Options{})
	srv2 := httptest.NewServer(off.Handler())
	defer srv2.Close()
	code, _ = call(t, srv2, off.Token(), "POST", "/api/models/set", `{"confirm":true,"kind":"idle","value":"2h"}`)
	require.Equal(t, http.StatusNotImplemented, code)
}
