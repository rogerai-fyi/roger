package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRoutingApplyOwnerCeiling: the session OWNER's routing is a ceiling a guest body may
// only tighten (contract §1a, §9): min_tps is the max of the two, a guest quant list must
// sit inside the owner's rule or the request is refused locally, the owner's band (freq)
// can never be dropped or swapped from the body, confidential / self_hosted_only are OR'd,
// ignore is unioned, pref is a default the guest may override.
func TestRoutingApplyOwnerCeiling(t *testing.T) {
	owner := Routing{Pref: "cheap", MinTPS: 20, Confidential: true, SelfHostedOnly: true,
		Quantizations: []string{"Q8_0", "BF16", "unknown"}, Ignore: []string{"n9"}, MaxOut: 2, MaxIn: 0.5}
	type want struct {
		roger, provider map[string]any
		refusal         string
	}
	for name, tc := range map[string]struct {
		guest string
		want  want
	}{
		"no carrier: the owner's routing as is": {
			guest: `{"model":"m"}`,
			want: want{
				roger:    map[string]any{"pref": "cheap", "min_tps": 20.0, "confidential": true, "self_hosted_only": true},
				provider: map[string]any{"quantizations": []any{"Q8_0", "BF16", "unknown"}, "ignore": []any{"n9"}},
			},
		},
		"guest lowers min_tps: the owner's floor stands": {
			guest: `{"model":"m","roger":{"min_tps":5}}`,
			want:  want{roger: map[string]any{"min_tps": 20.0}},
		},
		"guest raises min_tps: the guest's floor is taken": {
			guest: `{"model":"m","roger":{"min_tps":40}}`,
			want:  want{roger: map[string]any{"min_tps": 40.0}},
		},
		"guest weakens confidential: still confidential": {
			guest: `{"model":"m","roger":{"confidential":false,"self_hosted_only":false}}`,
			want:  want{roger: map[string]any{"confidential": true, "self_hosted_only": true}},
		},
		"guest overrides pref: a default, so the guest wins": {
			guest: `{"model":"m","roger":{"pref":"fast"}}`,
			want:  want{roger: map[string]any{"pref": "fast"}},
		},
		"guest freq is dropped: the owner's band stands": {
			guest: `{"model":"m","roger":{"freq":"147.520 MHz ZZZZ-ZZZZ"}}`,
			want:  want{roger: map[string]any{"freq": nil}},
		},
		"guest narrows the quant rule": {
			guest: `{"model":"m","provider":{"quantizations":["bf16"]}}`,
			want:  want{provider: map[string]any{"quantizations": []any{"bf16"}}},
		},
		"guest quant outside the rule is refused": {
			guest: `{"model":"m","provider":{"quantizations":["Q4_K_M"]}}`,
			want:  want{refusal: "quantization Q4_K_M is outside this session's quant rule"},
		},
		"guest ignore is unioned with the owner's": {
			guest: `{"model":"m","provider":{"ignore":["n8"]}}`,
			want:  want{provider: map[string]any{"ignore": []any{"n8", "n9"}}},
		},
		"no price carrier: the owner's caps are written into the body": {
			guest: `{"model":"m"}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 2.0, "prompt": 0.5}}},
		},
		"guest raises the out cap: clamped to the owner's": {
			guest: `{"model":"m","provider":{"max_price":{"completion":50}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 2.0, "prompt": 0.5}}},
		},
		"guest tightens the out cap: the guest's lower cap is kept": {
			guest: `{"model":"m","provider":{"max_price":{"completion":1}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 1.0, "prompt": 0.5}}},
		},
		"guest raises the in cap: clamped to the owner's": {
			guest: `{"model":"m","provider":{"max_price":{"prompt":3}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 2.0, "prompt": 0.5}}},
		},
		"guest tightens the in cap: kept": {
			guest: `{"model":"m","provider":{"max_price":{"prompt":0.1,"completion":0.3}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 0.3, "prompt": 0.1}}},
		},
		"guest out cap of 0 states no cap: the owner's applies": {
			guest: `{"model":"m","provider":{"max_price":{"completion":0}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 2.0, "prompt": 0.5}}},
		},
		"guest out cap of null states no cap: the owner's applies": {
			guest: `{"model":"m","provider":{"max_price":{"completion":null}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 2.0, "prompt": 0.5}}},
		},
		"other max_price keys are the guest's and pass through": {
			guest: `{"model":"m","provider":{"max_price":{"request":0.02}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": 2.0, "prompt": 0.5, "request": 0.02}}},
		},
		"a malformed guest cap is left for the broker to refuse, never silently repaired": {
			guest: `{"model":"m","provider":{"max_price":{"completion":"abc"}}}`,
			want:  want{provider: map[string]any{"max_price": map[string]any{"completion": "abc", "prompt": 0.5}}},
		},
		"a max_price that is not an object is left for the broker to refuse": {
			guest: `{"model":"m","provider":{"max_price":7}}`,
			want:  want{provider: map[string]any{"max_price": 7.0}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := owner.Apply([]byte(tc.guest))
			if tc.want.refusal != "" {
				var rr *RoutingRefusal
				if !errors.As(err, &rr) || rr.Msg != tc.want.refusal {
					t.Fatalf("err = %v, want refusal %q", err, tc.want.refusal)
				}
				if out != nil {
					t.Fatalf("a refused body must not be produced; got %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			var got struct {
				Roger    map[string]any `json:"roger"`
				Provider map[string]any `json:"provider"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			for k, v := range tc.want.roger {
				if !equalJSON(got.Roger[k], v) {
					t.Errorf("roger.%s = %v, want %v", k, got.Roger[k], v)
				}
			}
			for k, v := range tc.want.provider {
				if !equalJSON(got.Provider[k], v) {
					t.Errorf("provider.%s = %v, want %v", k, got.Provider[k], v)
				}
			}
			if strings.Contains(string(out), "ZZZZ-ZZZZ") {
				t.Errorf("the guest's band code reached the wire: %s", out)
			}
		})
	}
}

// TestRoutingApplyPriceCaps: the price ceiling without the rest of an owner's routing. An
// owner with no --max-in lets a guest prompt cap pass as given; a Routing with no caps
// writes no max_price at all (the in-booth chat and harness paths, which carry the cap as
// the X-Roger-Max-Price-Out header and have no guest).
func TestRoutingApplyPriceCaps(t *testing.T) {
	maxPrice := func(out []byte) any {
		var got struct {
			Provider map[string]any `json:"provider"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got.Provider["max_price"]
	}
	out, err := Routing{MaxOut: 10}.Apply([]byte(`{"model":"m","provider":{"max_price":{"prompt":3}}}`))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := map[string]any{"completion": 10.0, "prompt": 3.0}; !equalJSON(maxPrice(out), want) {
		t.Errorf("no owner in-cap: max_price = %v, want %v (the guest's prompt cap passes as given)", maxPrice(out), want)
	}
	out, err = Routing{Pref: "fast"}.Apply([]byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if mp := maxPrice(out); mp != nil {
		t.Errorf("a Routing with no caps wrote max_price = %v", mp)
	}
	out, err = Routing{Pref: "fast"}.Apply([]byte(`{"model":"m","provider":{"max_price":{"completion":50}}}`))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := map[string]any{"completion": 50.0}; !equalJSON(maxPrice(out), want) {
		t.Errorf("a Routing with no caps must not touch a caller's max_price; got %v", maxPrice(out))
	}
}

// TestFoldCallerCarriersPriceCaps: header mode lifts a guest's price caps onto the header
// values under the same ceiling - tightened, never raised - and names what has no header.
func TestFoldCallerCarriersPriceCaps(t *testing.T) {
	for name, tc := range map[string]struct {
		owner           Routing
		guest           string
		wantOut, wantIn float64
		wantDropped     string
	}{
		"guest raises the out cap: the owner's stands":   {Routing{MaxOut: 2}, `{"provider":{"max_price":{"completion":50}}}`, 2, 0, ""},
		"guest tightens the out cap: lifted":             {Routing{MaxOut: 2}, `{"provider":{"max_price":{"completion":1}}}`, 1, 0, ""},
		"guest raises the in cap: the owner's stands":    {Routing{MaxOut: 2, MaxIn: 0.5}, `{"provider":{"max_price":{"prompt":3}}}`, 2, 0.5, ""},
		"guest tightens the in cap: lifted":              {Routing{MaxOut: 2, MaxIn: 0.5}, `{"provider":{"max_price":{"prompt":0.1}}}`, 2, 0.1, ""},
		"no owner in cap: the guest's passes as given":   {Routing{MaxOut: 2}, `{"provider":{"max_price":{"prompt":3}}}`, 2, 3, ""},
		"zero and null state no cap":                     {Routing{MaxOut: 2, MaxIn: 0.5}, `{"provider":{"max_price":{"completion":0,"prompt":null}}}`, 2, 0.5, ""},
		"a per-request cap has no header form: named":    {Routing{MaxOut: 2}, `{"provider":{"max_price":{"request":0.02,"completion":1}}}`, 1, 0, "provider.max_price.request"},
		"a max_price that is not an object: named whole": {Routing{MaxOut: 2}, `{"provider":{"max_price":7}}`, 2, 0, "provider.max_price"},
	} {
		t.Run(name, func(t *testing.T) {
			r := tc.owner
			_, dropped, _, err := r.foldCallerCarriers([]byte(tc.guest))
			if err != nil {
				t.Fatalf("fold: %v", err)
			}
			if r.MaxOut != tc.wantOut || r.MaxIn != tc.wantIn {
				t.Errorf("caps = out %v in %v, want out %v in %v", r.MaxOut, r.MaxIn, tc.wantOut, tc.wantIn)
			}
			if got := strings.Join(dropped, ","); got != tc.wantDropped {
				t.Errorf("dropped = %q, want %q", got, tc.wantDropped)
			}
		})
	}
}

// TestFoldCallerCarriersOwnerCeiling: HeaderMode applies the same ceiling to a guest body
// before lifting it onto the header wire.
func TestFoldCallerCarriersOwnerCeiling(t *testing.T) {
	owner := Routing{MinTPS: 20, Quantizations: []string{"Q8_0", "unknown"}}
	r := owner
	out, dropped, hasModels, err := r.foldCallerCarriers([]byte(`{"model":"m","roger":{"min_tps":5,"freq":"ZZZZ"},"provider":{"quantizations":["q8_0"]}}`))
	if err != nil || hasModels {
		t.Fatalf("fold: err=%v hasModels=%v", err, hasModels)
	}
	if r.MinTPS != 20 {
		t.Errorf("min_tps lowered to %v; the owner's floor must stand", r.MinTPS)
	}
	if strings.Contains(string(out), "ZZZZ") || strings.Contains(string(out), "roger") {
		t.Errorf("carriers must leave the body: %s", out)
	}
	if strings.Join(dropped, ",") != "provider.quantizations" {
		t.Errorf("dropped = %v, want provider.quantizations (freq is the owner's, never reported)", dropped)
	}
	r = owner
	if _, _, _, err := r.foldCallerCarriers([]byte(`{"model":"m","provider":{"quantizations":["Q4_K_M"]}}`)); err == nil || !strings.Contains(err.Error(), "outside this session's quant rule") {
		t.Errorf("a guest quant outside the rule must be refused in header mode too; err = %v", err)
	}
	r = owner
	if _, _, _, err := r.foldCallerCarriers([]byte(`[1,2]`)); err == nil {
		t.Errorf("a non-object body must be an error")
	}
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
