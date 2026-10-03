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

// modelsSection is the console's MODELS section, where every model action lives.
func modelsSection(t *testing.T) string {
	t.Helper()
	js := consoleJS(t)
	a, b := strings.Index(js, "/* 8c. MODELS"), strings.Index(js, "/* end MODELS */")
	require.True(t, a > 0 && b > a, "the models section is missing")
	return js[a:b]
}

// local_models.feature §11 (@web): the serving settings are one visible line of SHARE, under roger's
// own rows, each changeable in place with the same choices the CLI has.
func TestTheServingSettingsAreOneLineOfShareUnderRogersRows(t *testing.T) {
	html := consoleHTML(t)
	share := panelOf(t, html, "panel-share")
	rows, line, get := strings.Index(share, `id="share-roger-rows"`), strings.Index(share, `id="share-settings"`), strings.Index(share, `id="share-getmodel"`)
	require.True(t, rows > 0 && line > rows, "the settings line sits under roger's rows")
	require.True(t, get > line, "and above the search it opens")
	set := share[line:get]
	require.Contains(t, share, `class="share-settings mono small" id="share-settings" hidden`, "hidden until the models API answers")
	require.Contains(t, set, `<select class="inp small" id="share-set-runtime"`, "the build is a choice of the CLI's choices")
	for _, id := range []string{"share-set-idle", "share-set-budget"} {
		require.Contains(t, set, `<input class="inp small" id="`+id+`" type="text"`, "%s is typed in place", id)
	}
	for _, id := range []string{"share-set-idle-apply", "share-set-budget-apply"} {
		require.Contains(t, set, `id="`+id+`"`, "%s applies it", id)
	}
	for _, id := range []string{"share-set-runtime-now", "share-set-idle-now", "share-set-budget-now"} {
		require.Contains(t, set, `id="`+id+`"`, "%s says the setting in words", id)
	}
	require.Contains(t, set, `aria-label=`, "every control is named for a screen reader")

	sec := modelsSection(t)
	require.Contains(t, sec, "renderServingSettings(v)", "every render of the view redraws the line")
	require.Contains(t, sec, `show($("share-settings"), true)`, "always shown when the models API answers")
	require.Contains(t, sec, `show($("share-settings"), false)`, "and not when it cannot")
	require.Contains(t, sec, `"build: "`)
	require.Contains(t, sec, `" - " + (st.here || "")`, "the build says what it means here")
	require.Contains(t, sec, `"idle: " + (st.idle || "off")`)
	require.Contains(t, sec, `"disk: " + (st.disk_budget > 0 ? fmtBytes(st.disk_budget) + " budget, " + fmtBytes(v.disk_used) + " used" : "no budget")`)
	require.Contains(t, sec, `(st.choices || [])`, "the select offers the server's choices, no more")
	require.Contains(t, sec, "document.activeElement", "a poll never overwrites what the owner is choosing")
}

// §11 (@web): a change is asked first (No holds the focus), carries the owner's yes, and answers with
// the CLI's own sentence and a fresh view.
func TestAServingSettingIsAskedFirstAndAnswersWithTheCLIsSentence(t *testing.T) {
	sec := modelsSection(t)
	a := strings.Index(sec, "  function modelsSet(kind, value, title, body) {")
	require.True(t, a > 0, "one function changes a setting")
	f := sec[a:]
	f = f[:strings.Index(f, "\n  }\n")]
	require.Contains(t, f, "askSafe(title, body")
	require.Contains(t, f, ".then(function (yes) {\n      if (!yes) return;")
	require.Contains(t, f, `apiPost("/api/models/set", { confirm: true, kind: kind, value: value })`)
	require.Contains(t, f, "toast(d.says)")
	require.Contains(t, f, `$("share-set-says").textContent = d.says`, "a long answer (what the next fetch removes) stays readable after the toast")
	require.Contains(t, consoleHTML(t), `<pre class="set-says" id="share-set-says" hidden></pre>`)
	require.Contains(t, f, "renderModels(d.view)")
	require.Contains(t, f, `toast(e.message, "err")`, "a refused value says the server's reason")
	for _, kind := range []string{`modelsSet("runtime"`, `modelsSet("idle"`, `modelsSet("budget"`} {
		require.Contains(t, sec, kind)
	}
	require.Contains(t, sec, "applies from the next serve", "the question says when it applies")
	require.Contains(t, sec, "removes nothing now", "a budget removes nothing by itself")
	require.Contains(t, sec, `"keydown"`, "Enter in a field applies it")
}

// §11 (@web): roger's served rows name the build and show the measured speed once it has answered,
// the estimate (marked as one) until then - for the serving entry and every "also".
func TestRogersServedRowsShowTheBuildAndTheMeasuredSpeed(t *testing.T) {
	js := consoleJS(t)
	a, b := strings.Index(js, "  function renderModels(v) {"), strings.Index(js, "  function modelsServe(m, current, add) {")
	r := js[a:b]
	served := r[strings.Index(r, "[v.serving].concat(v.also || [])"):strings.Index(r, `(v.models || []).forEach`)]
	require.Contains(t, served, "servedSpeed(one)", "each served row, the first and every also")
	sec := modelsSection(t)
	require.Contains(t, sec, `{ cpu: "CPU", vulkan: "Vulkan", cuda: "CUDA", metal: "Metal" }`)
	require.Contains(t, sec, `" build"`)
	require.Contains(t, sec, `one.measured_tok_s > 0 ? Number(one.measured_tok_s).toFixed(1) + " tok/s measured" : (one.estimate || "")`,
		"a measurement replaces the estimate; the server marks an estimate as one")
}

// §11 (@web): the line works at phone width, on the console's own tokens.
func TestTheServingSettingsLineFitsAPhone(t *testing.T) {
	b, err := os.ReadFile("assets/console.css")
	require.NoError(t, err)
	css := string(b)
	require.Contains(t, css, ".share-settings {")
	require.Contains(t, css, ".share-settings .set-item {")
	require.Contains(t, css, "flex-wrap: wrap", "items wrap rather than scroll the page sideways")
	a := strings.Index(css, ".share-settings {")
	block := css[a : a+strings.Index(css[a:], "\n/* end share-settings */")]
	require.NotContains(t, block, "#", "colours come from the tokens, so dark mode follows")
	require.NotContains(t, block, "box-shadow", "hairlines, no glows")
}
