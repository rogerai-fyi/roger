package tui

// voicelist.go is the bundled Kokoro voice catalog + the prefix→hint decoder + the local
// /v1/audio/voices response parser that feed the SHARE VOICE BOOTH picker. It is plain DATA +
// parsing (no logic, no UI): the operator's LOCAL server is the source of truth for which voices
// exist (GET /v1/audio/voices), and this bundled list is the FALLBACK so the picker never blanks
// when the server can't enumerate them. The prefix convention af_/am_/bf_/bm_ =
// American/British female/male drives the human group labels.

import (
	"encoding/json"
	"sort"
	"strings"
)

// bundledKokoroVoices is the shipped fallback list of Kokoro voice ids — used when the operator's
// local server does not expose GET /v1/audio/voices (or isn't reachable) so the picker is never
// empty. These are the standard Kokoro-82M voice ids (af_/am_ American, bf_/bm_ British, plus a few
// multilingual). It is a snapshot for the fallback ONLY; the live local list always wins when
// available. Kept sorted for a stable grid order.
func bundledKokoroVoices() []string {
	out := append([]string(nil), kokoroBundled...)
	sort.Strings(out)
	return out
}

var kokoroBundled = []string{
	// American female (af_)
	"af_heart", "af_bella", "af_nicole", "af_sarah", "af_sky",
	"af_nova", "af_aoede", "af_kore", "af_jessica", "af_river", "af_alloy",
	// American male (am_)
	"am_onyx", "am_michael", "am_fenrir", "am_puck", "am_echo",
	"am_eric", "am_liam", "am_adam", "am_santa",
	// British female (bf_)
	"bf_emma", "bf_isabella", "bf_alice", "bf_lily",
	// British male (bm_)
	"bm_george", "bm_fable", "bm_lewis", "bm_daniel",
	// Other / multilingual
	"ef_dora", "em_alex", "ff_siwis",
}

// voiceGroup is one labeled bucket of the picker grid (e.g. "American female" → [af_heart, ...]).
type voiceGroup struct {
	label string
	ids   []string
}

// prefixKey → human label, in the order the picker renders them. "Other" is the catch-all for any
// id whose 3-char prefix (2 letters + underscore) is not one of the four known ones.
var voicePrefixLabels = []struct{ key, label string }{
	{"af_", "American female"},
	{"am_", "American male"},
	{"bf_", "British female"},
	{"bm_", "British male"},
}

// prefixHint decodes a Kokoro id's leading prefix into its human group label (af_ → "American
// female", etc.). An unknown/short/empty prefix falls back to "Other", so a multilingual id
// (ef_/em_/ff_) or a malformed one never crashes the grouping.
func prefixHint(id string) string {
	for _, p := range voicePrefixLabels {
		if strings.HasPrefix(id, p.key) {
			return p.label
		}
	}
	return "Other"
}

// groupVoices buckets a flat id list into the four ordered groups plus "Other", preserving input
// order within each group. Empty groups are omitted so the grid never shows a header with no rows.
func groupVoices(ids []string) []voiceGroup {
	order := []string{"American female", "American male", "British female", "British male", "Other"}
	byLabel := map[string][]string{}
	for _, id := range ids {
		l := prefixHint(id)
		byLabel[l] = append(byLabel[l], id)
	}
	out := make([]voiceGroup, 0, len(order))
	for _, l := range order {
		if len(byLabel[l]) > 0 {
			out = append(out, voiceGroup{label: l, ids: byLabel[l]})
		}
	}
	return out
}

// parseVoicesResponse reads a local server's GET /v1/audio/voices body into a flat id list. The
// endpoint shape varies across Kokoro forks, so it accepts the common shapes:
//   - a bare JSON array of ids:            ["af_heart","am_onyx"]
//   - {"voices":[ ... ]} of strings or {"id":...} objects
//   - {"data":[{"id":...}]} (OpenAI-ish)
//
// A body it can't parse (or one with no ids) yields nil — the caller then falls back to the
// bundled list. It NEVER panics on malformed input.
func parseVoicesResponse(body []byte) []string {
	// 1) bare array of strings.
	var arr []string
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		return dedupeNonEmpty(arr)
	}
	// 2) object with a "voices" or "data" array of strings-or-{id}.
	var obj struct {
		Voices []json.RawMessage `json:"voices"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil
	}
	raws := obj.Voices
	if len(raws) == 0 {
		raws = obj.Data
	}
	var out []string
	for _, r := range raws {
		var s string
		if json.Unmarshal(r, &s) == nil && s != "" {
			out = append(out, s)
			continue
		}
		var o struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(r, &o) == nil {
			if o.ID != "" {
				out = append(out, o.ID)
			} else if o.Name != "" {
				out = append(out, o.Name)
			}
		}
	}
	return dedupeNonEmpty(out)
}

// dedupeNonEmpty drops empties + duplicates while preserving order (a server could list an id
// twice; the grid must not).
func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
