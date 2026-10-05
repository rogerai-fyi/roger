package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func TestValidProfileName(t *testing.T) {
	for _, ok := range []string{"a", "coding", "c0-d_e", "9"} {
		require.True(t, ValidProfileName(ok), ok)
	}
	for _, bad := range []string{"", "Coding", "-a", "_a", "a b", "a/b", string(make([]byte, 65))} {
		require.False(t, ValidProfileName(bad), bad)
	}
}

func TestParseProfilesSections(t *testing.T) {
	_, err := ParseProfiles([]byte(`{`))
	require.Error(t, err)

	ps, err := ParseProfiles([]byte(`{"limits":{}}`))
	require.NoError(t, err)
	require.Empty(t, ps.Names())

	ps, err = ParseProfiles([]byte(`{"profiles":null}`))
	require.NoError(t, err)
	require.Empty(t, ps.Names())

	ps, err = ParseProfiles([]byte(`{"profiles":[1]}`))
	require.NoError(t, err)
	require.Equal(t, "profiles: ignored (not an object) - run roger profile list", ps.SectionWarn)

	ps, err = ParseProfiles([]byte(`{"profiles":{
		"default":{"model":"x"},
		"Bad Name":{"model":"x"},
		"zeta":{"model":"m","max_tokens":5,"system":"s","roger":{"prefer":1,"min_tps":3},"provider":{"bogus":1}},
		"alpha":{"models":["a","b"]},
		"broken":{"roger":{"min_ctx":-1}},
		"notobj":7,
		"loop":{"roger":{"profile":"@profile/alpha"}},
		"provstr":{"provider":"x"}}}`))
	require.NoError(t, err)
	require.True(t, ps.ReservedSeen)
	require.Equal(t, []string{"Bad Name"}, ps.BadNames)
	require.Equal(t, []string{"alpha", "zeta"}, ps.Names())
	z, zerr, ok := ps.Get("zeta")
	require.True(t, ok)
	require.NoError(t, zerr)
	require.Equal(t, []string{"provider.bogus", "roger.prefer"}, z.Unknown)
	require.Equal(t, []string{"max_tokens", "system"}, z.NonRouting)
	require.Equal(t, map[string]any{"model": "m", "roger": map[string]any{"min_tps": float64(3)}}, z.Body)

	for name, want := range map[string]string{
		"broken":  "profile broken: roger.min_ctx must be a positive integer",
		"notobj":  "profile notobj: not an object",
		"loop":    "profile loop: profiles cannot reference profiles",
		"provstr": "profile provstr: provider must be an object",
	} {
		_, perr, found := ps.Get(name)
		require.True(t, found, name)
		require.EqualError(t, perr, want)
	}
	_, _, found := ps.Get("nope")
	require.False(t, found)
	var nilPS *Profiles
	_, _, found = nilPS.Get("x")
	require.False(t, found)
}

func TestValidateRoutingBody(t *testing.T) {
	cases := []struct{ body, err string }{
		{`{}`, ""},
		{`{"model":null,"models":null,"provider":null,"roger":null}`, ""},
		{`{"model":"a","models":["a","a:free","b","c","d","e"]}`, ""}, // 5 distinct after sugar
		{`{"model":7}`, "model must be a model id"},
		{`{"model":" "}`, "model must be a model id"},
		{`{"models":"a"}`, "models must be a list of model ids"},
		{`{"models":[1]}`, "models must be a list of model ids"},
		{`{"models":["a","b","c","d","e","f"]}`, "models has more than 5 distinct models"},
		{`{"provider":1}`, "provider must be an object"},
		{`{"roger":1}`, "roger must be an object"},
		{`{"provider":{"order":[]}}`, "provider.order must be a non-empty list of station ids"},
		{`{"provider":{"only":"x"}}`, "provider.only must be a non-empty list of station ids"},
		{`{"provider":{"ignore":[]}}`, ""},
		{`{"provider":{"ignore":[""]}}`, "provider.ignore must be a non-empty list of station ids"},
		{`{"provider":{"order":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u","v","w","x","y","z","A","B","C","D","E","F","G"]}}`, "provider.order has more than 32 entries"},
		{`{"provider":{"order":["a","b"],"only":["a"]}}`, "provider.order names b, outside provider.only"},
		{`{"provider":{"order":["a"],"only":["a","b"]}}`, ""},
		{`{"provider":{"allow_fallbacks":"no"}}`, "provider.allow_fallbacks must be a boolean"},
		{`{"provider":{"require_parameters":1}}`, "provider.require_parameters must be a boolean"},
		{`{"provider":{"sort":"cheap"}}`, "provider.sort must be price, throughput or latency"},
		{`{"provider":{"sort":"latency","allow_fallbacks":true,"require_parameters":false}}`, ""},
		{`{"provider":{"quantizations":[1]}}`, "provider.quantizations must be a list of labels"},
		{`{"provider":{"max_price":3}}`, "provider.max_price must be an object"},
		{`{"provider":{"max_price":{"image":1}}}`, "provider.max_price.image is not supported"},
		{`{"provider":{"max_price":{"prompt":-1}}}`, "provider.max_price.prompt must be a price >= 0"},
		{`{"provider":{"max_price":{"request":"1"}}}`, "provider.max_price.request must be a price >= 0"},
		{`{"provider":{"max_price":{"prompt":null,"completion":0,"request":0.5}}}`, ""},
		{`{"roger":{"pref":"warp"}}`, "roger.pref must be one of " + joinPrefs()},
		{`{"roger":{"pref":""}}`, "roger.pref must be one of " + joinPrefs()},
		{`{"roger":{"trust_min":"gold"}}`, "roger.trust_min must be any, verified or confidential"},
		{`{"roger":{"require":"tools"}}`, "roger.require must be a list"},
		{`{"roger":{"require":["audio"]}}`, "roger.require: unknown capability audio"},
		{`{"roger":{"require":["tools","vision"]}}`, ""},
		{`{"roger":{"region":[]}}`, "roger.region must be a list of lowercase regions"},
		{`{"roger":{"region":["EU"]}}`, "roger.region: EU is not a lowercase region"},
		{`{"roger":{"region":["eu","us-west"]}}`, ""},
		{`{"roger":{"params_b":[1]}}`, "roger.params_b must be [min, max]"},
		{`{"roger":{"params_b":"7"}}`, "roger.params_b must be [min, max]"},
		{`{"roger":{"params_b":[0,0]}}`, "roger.params_b must be [min, max] with 0 <= min <= max and max > 0"},
		{`{"roger":{"params_b":[9,7]}}`, "roger.params_b must be [min, max] with 0 <= min <= max and max > 0"},
		{`{"roger":{"params_b":[7,"x"]}}`, "roger.params_b must be [min, max] with 0 <= min <= max and max > 0"},
		{`{"roger":{"params_b":[7,70]}}`, ""},
		{`{"roger":{"min_ctx":0}}`, "roger.min_ctx must be a positive integer"},
		{`{"roger":{"max_ttft_ms":1.5}}`, "roger.max_ttft_ms must be a positive integer"},
		{`{"roger":{"min_ctx":"8k"}}`, "roger.min_ctx must be a positive integer"},
		{`{"roger":{"min_tps":-1}}`, "roger.min_tps must be >= 0"},
		{`{"roger":{"min_tps":"fast"}}`, "roger.min_tps must be >= 0"},
		{`{"roger":{"self_hosted_only":"y"}}`, "roger.self_hosted_only must be a boolean"},
		{`{"roger":{"confidential":1}}`, "roger.confidential must be a boolean"},
		{`{"roger":{"freq":5}}`, "roger.freq must be a string"},
		{`{"roger":{"freq":"AAAA","confidential":true,"self_hosted_only":false,"min_ctx":8192,"max_ttft_ms":900,"min_tps":0,"trust_min":"verified"}}`, ""},
		{`{"provider":{"sort":"price"},"roger":{"pref":"fast"}}`, "provider.sort and roger.pref are exclusive"},
	}
	for _, c := range cases {
		err := ValidateRoutingBody(obj(t, c.body))
		if c.err == "" {
			require.NoError(t, err, c.body)
		} else {
			require.EqualError(t, err, c.err, c.body)
		}
	}
}

func joinPrefs() string {
	out := ""
	for i, p := range RoutingPrefs {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func TestIdListAcceptsStringSlices(t *testing.T) {
	ids, err := idList([]string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, ids)
	_, err = idList([]string{"a", " "})
	require.Error(t, err)
	_, err = idList(3)
	require.Error(t, err)
}

func TestMergeProfile(t *testing.T) {
	prof := obj(t, `{"model":"m","models":["a"],
		"provider":{"sort":"price","only":["s1"],"max_price":{"prompt":1,"completion":2}},
		"roger":{"min_tps":5,"region":["eu"]}}`)
	cases := []struct{ name, req, want, err string }{
		{"empty request keeps the profile", `{}`,
			`{"model":"m","models":["a"],"provider":{"sort":"price","only":["s1"],"max_price":{"prompt":1,"completion":2}},"roger":{"min_tps":5,"region":["eu"]}}`, ""},
		{"top-level replace and clear", `{"model":"x","models":null,"messages":[1]}`,
			`{"model":"x","messages":[1],"provider":{"sort":"price","only":["s1"],"max_price":{"prompt":1,"completion":2}},"roger":{"min_tps":5,"region":["eu"]}}`, ""},
		{"per-subkey replace", `{"provider":{"only":["s2"]},"roger":{"min_tps":9}}`,
			`{"model":"m","models":["a"],"provider":{"sort":"price","only":["s2"],"max_price":{"prompt":1,"completion":2}},"roger":{"min_tps":9,"region":["eu"]}}`, ""},
		{"null sub-key clears", `{"provider":{"sort":null},"roger":{"region":null,"min_tps":null}}`,
			`{"model":"m","models":["a"],"provider":{"only":["s1"],"max_price":{"prompt":1,"completion":2}}}`, ""},
		{"null object clears", `{"provider":null}`,
			`{"model":"m","models":["a"],"roger":{"min_tps":5,"region":["eu"]}}`, ""},
		{"max_price per sub-key", `{"provider":{"max_price":{"prompt":0.5,"completion":null,"request":3}}}`,
			`{"model":"m","models":["a"],"provider":{"sort":"price","only":["s1"],"max_price":{"prompt":0.5,"request":3}},"roger":{"min_tps":5,"region":["eu"]}}`, ""},
		{"max_price non-object replaces", `{"provider":{"max_price":7}}`,
			`{"model":"m","models":["a"],"provider":{"sort":"price","only":["s1"],"max_price":7},"roger":{"min_tps":5,"region":["eu"]}}`, ""},
		{"non-object carrier left for the validator", `{"roger":"x"}`,
			`{"model":"m","models":["a"],"provider":{"sort":"price","only":["s1"],"max_price":{"prompt":1,"completion":2}},"roger":"x"}`, ""},
		{"pref from the request conflicts with the profile's sort", `{"roger":{"pref":"fast"}}`, "",
			"provider.sort and roger.pref are exclusive (provider.sort from the profile, roger.pref from the request)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := MergeProfile(prof, obj(t, c.req))
			if c.err != "" {
				require.EqualError(t, err, c.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, obj(t, c.want), got)
		})
	}
	// the profile is never mutated by a merge
	require.Equal(t, []any{"s1"}, prof["provider"].(map[string]any)["only"])
	require.Equal(t, float64(2), prof["provider"].(map[string]any)["max_price"].(map[string]any)["completion"])

	_, err := MergeProfile(obj(t, `{"roger":{"pref":"fast"}}`), obj(t, `{"provider":{"sort":"price"}}`))
	require.EqualError(t, err, "provider.sort and roger.pref are exclusive (roger.pref from the profile, provider.sort from the request)")

	got, err := MergeProfile(obj(t, `{}`), obj(t, `{"roger":{"min_tps":null}}`))
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestResolveProfileBody(t *testing.T) {
	ps, err := ParseProfiles([]byte(`{"profiles":{
		"coding":{"models":["a","b","c"],"roger":{"min_tps":5}},
		"one":{"models":["a"]},
		"pinned":{"model":"p","models":["q"]},
		"bare":{"roger":{"min_tps":1}},
		"sorted":{"provider":{"sort":"price"}},
		"bad":{"roger":{"min_ctx":0}}}}`))
	require.NoError(t, err)
	cases := []struct{ name, body, want, err string }{
		{"no reference is unchanged", `{"model":"x","b":1}`, `{"model":"x","b":1}`, ""},
		{"not json is unchanged", `nope`, `nope`, ""},
		{"model reference", `{"model":"@profile/coding","messages":[]}`,
			`{"model":"a","models":["b","c"],"messages":[],"roger":{"min_tps":5}}`, ""},
		{"single-model list drops models", `{"model":"@profile/one"}`, `{"model":"a"}`, ""},
		{"profile model wins", `{"model":"@profile/pinned"}`, `{"model":"p","models":["q"]}`, ""},
		{"no model leaves it empty for the band", `{"model":"@profile/bare"}`, `{"model":"","roger":{"min_tps":1}}`, ""},
		{"roger.profile with the caller's model", `{"model":"x","roger":{"profile":"@profile/bare","min_tps":7}}`,
			`{"model":"x","roger":{"min_tps":7}}`, ""},
		{"roger.profile only key is dropped", `{"model":"x","roger":{"profile":"@profile/one"}}`,
			`{"model":"x","models":["a"]}`, ""},
		{"same profile twice is one", `{"model":"@profile/one","roger":{"profile":"@profile/one"}}`, `{"model":"a"}`, ""},
		{"case-sensitive prefix is not a reference", `{"model":"@Profile/coding"}`, `{"model":"@Profile/coding"}`, ""},
		{"unknown profile", `{"model":"@profile/nope"}`, "", "unknown profile nope"},
		{"profile with a load error", `{"model":"@profile/bad"}`, "", "profile bad: roger.min_ctx must be a positive integer"},
		{"two profiles", `{"model":"@profile/one","roger":{"profile":"@profile/coding"}}`, "", "two profiles named: @profile/one and @profile/coding"},
		{"sugar on the reference", `{"model":"@profile/coding:free"}`, "", "sugar on a profile reference; put :free on the profile's models"},
		{"merge conflict names the profile", `{"model":"@profile/sorted","roger":{"pref":"fast"}}`, "",
			"profile sorted: provider.sort and roger.pref are exclusive (provider.sort from the profile, roger.pref from the request)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, resolved, err := ResolveProfileBody([]byte(c.body), ps)
			if c.err != "" {
				require.EqualError(t, err, c.err)
				var rr *RoutingRefusal
				require.ErrorAs(t, err, &rr)
				return
			}
			require.NoError(t, err)
			if c.want == c.body {
				require.False(t, resolved)
				require.Equal(t, c.body, string(out))
				return
			}
			require.True(t, resolved)
			require.JSONEq(t, c.want, string(out))
		})
	}
}

func TestProfileStoreRereadsOnChangeAndKeepsLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := NewProfileStore(path)
	require.Empty(t, s.Get().Names(), "a missing file is an empty set")
	require.Equal(t, 0, s.Reads())

	require.NoError(t, os.WriteFile(path, []byte(`{"profiles":{"a":{"model":"x"}}}`), 0o600))
	require.Equal(t, []string{"a"}, s.Get().Names())
	require.Equal(t, []string{"a"}, s.Get().Names())
	require.Equal(t, 1, s.Reads(), "an unchanged file is not re-read")

	require.NoError(t, os.WriteFile(path, []byte(`{"profiles":{"a":{"model":"x"},"b":{"model":"y"}}}`), 0o600))
	require.Equal(t, []string{"a", "b"}, s.Get().Names())
	require.Equal(t, 2, s.Reads())

	require.NoError(t, os.WriteFile(path, []byte(`{"profiles":`), 0o600))
	require.Equal(t, []string{"a", "b"}, s.Get().Names(), "a corrupt file keeps the last good set")
	require.NoError(t, os.WriteFile(path, []byte(`{"profiles":{}`), 0o600))
	later := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, later, later))
	require.Equal(t, []string{"a", "b"}, s.Get().Names())

	require.NoError(t, os.Remove(path))
	require.Equal(t, []string{"a", "b"}, s.Get().Names(), "a removed file keeps the last good set")

	bad := NewProfileStore(filepath.Join(t.TempDir(), "c.json"))
	require.NoError(t, os.WriteFile(bad.path, []byte(`{`), 0o600))
	require.Empty(t, bad.Get().Names(), "a corrupt first read is an empty set, never nil")

	require.Equal(t, ConfigPath(), NewProfileStore("").path)
}

func TestDropGuestFreqAndTakeFreq(t *testing.T) {
	cases := []struct{ in, want string }{
		{`nope`, `nope`},
		{`{"model":"x","roger":{"freq":"A"}}`, `{"model":"x","roger":{"freq":"A"}}`}, // no profile: untouched
		{`{"model":"@profile/a"}`, `{"model":"@profile/a"}`},
		{`{"model":"@profile/a","roger":{"min_tps":1}}`, `{"model":"@profile/a","roger":{"min_tps":1}}`},
		{`{"model":"@profile/a:free","roger":{"freq":"A"}}`, `{"model":"@profile/a:free","roger":{"freq":"A"}}`},
		{`{"model":"@profile/a","roger":{"freq":"A"}}`, `{"model":"@profile/a"}`},
		{`{"model":"x","roger":{"profile":"@profile/a","freq":"A"}}`, `{"model":"x","roger":{"profile":"@profile/a"}}`},
	}
	for _, c := range cases {
		require.Equal(t, c.want, compact(t, string(dropGuestFreq([]byte(c.in)))), c.in)
	}

	for _, c := range []struct{ in, want, freq string }{
		{`nope`, `nope`, ""},
		{`{"model":"x"}`, `{"model":"x"}`, ""},
		{`{"model":"x","roger":{"min_tps":1}}`, `{"model":"x","roger":{"min_tps":1}}`, ""},
		{`{"model":"x","roger":{"freq":"AAAA"}}`, `{"model":"x"}`, "AAAA"},
		{`{"model":"x","roger":{"freq":"AAAA","min_tps":1}}`, `{"model":"x","roger":{"min_tps":1}}`, "AAAA"},
	} {
		out, f := takeFreq([]byte(c.in))
		require.Equal(t, c.freq, f, c.in)
		require.Equal(t, c.want, compact(t, string(out)), c.in)
	}
}

func compact(t *testing.T, s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
