package detect

import (
	"regexp"
	"strconv"
	"strings"
)

// ParamsFromID estimates a model's parameter count in billions from its id, for an offer
// whose station did not declare one (ROUTING-EXPRESSION-CONTRACT §5: the broker marks the
// value as an estimate). It reads the size token model ids carry ("llama-3.3-70b",
// "phi-4-mini-3.8b", "qwen2.5-0.5b", "wave-pico-293m"); for a mixture-of-experts id the
// first token is the total ("qwen3-30b-a3b" -> 30, the "a3b" active count is not a
// separate token). A few families publish no size in the id; they are listed by name.
// ok=false when nothing in the id says the size.
func ParamsFromID(id string) (float64, bool) {
	s := strings.ToLower(id)
	for _, f := range paramsByFamily {
		if strings.Contains(s, f.prefix) {
			return f.b, true
		}
	}
	m := paramsTokenRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	if m[2] == "m" {
		v /= 1000
	}
	return v, true
}

// paramsTokenRe is a size token bounded by separators: "70b", "3.8b", "293m". A token glued
// to letters ("a3b", "v3") is not a size.
var paramsTokenRe = regexp.MustCompile(`(?:^|[-_./:])(\d+(?:\.\d+)?)([bm])(?:$|[-_./:])`)

// paramsByFamily are the families whose ids carry no size token.
var paramsByFamily = []struct {
	prefix string
	b      float64
}{
	{"deepseek-v3", 671},
	{"deepseek-r1", 671},
}
